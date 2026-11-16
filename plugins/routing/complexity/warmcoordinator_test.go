// Tests for electing one warmer per generation.
package complexity

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeWarmCoordinator is an in-memory stand-in with the one property that
// matters: a claim is granted to exactly one caller until it is released.
type fakeWarmCoordinator struct {
	mu         sync.Mutex
	claims     map[string]bool
	results    map[string]WarmGeneration
	claimErr   error
	publishErr error
	claimCalls int
	// heldObserved is closed the first time a waiter sees a claim held, so a
	// test can release the claim only once it is known to have been observed.
	heldObserved chan struct{}
	heldOnce     sync.Once
}

func newFakeWarmCoordinator() *fakeWarmCoordinator {
	return &fakeWarmCoordinator{claims: map[string]bool{}, results: map[string]WarmGeneration{}, heldObserved: make(chan struct{})}
}

func (f *fakeWarmCoordinator) Claim(key string, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls++
	if f.claimErr != nil {
		return false, f.claimErr
	}
	if f.claims[key] {
		return false, nil
	}
	f.claims[key] = true
	return true, nil
}

func (f *fakeWarmCoordinator) ClaimHeld(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	held := f.claims[key]
	if held {
		f.heldOnce.Do(func() { close(f.heldObserved) })
	}
	return held
}

func (f *fakeWarmCoordinator) Release(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.claims, key)
}

func (f *fakeWarmCoordinator) Publish(key string, generation WarmGeneration, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.publishErr != nil {
		return f.publishErr
	}
	f.results[key] = generation
	return nil
}

func (f *fakeWarmCoordinator) Lookup(key string) (WarmGeneration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	generation, ok := f.results[key]
	return generation, ok
}

// countingEmbed records every phrase that reaches the provider.
type countingEmbed struct {
	mu     sync.Mutex
	phrase []string
}

func (c *countingEmbed) fn(ctx context.Context, config *SemanticConfig, text string) ([]float32, error) {
	c.mu.Lock()
	c.phrase = append(c.phrase, text)
	c.mu.Unlock()
	return testSemanticEmbedding(ctx, config, text)
}

func (c *countingEmbed) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.phrase)
}

// TestCoordinatedWarmSkipsEmbeddingWhenAPeerAlreadyWarmed is the whole point of
// the coordinator. A configuration change reaches every node at once, so
// without it each node embeds the identical phrase set into the identical
// shared namespace — N times the provider spend for one generation.
func TestCoordinatedWarmSkipsEmbeddingWhenAPeerAlreadyWarmed(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	coordinator := newFakeWarmCoordinator()

	// First node wins the claim and does the work.
	first := &countingEmbed{}
	loaded, namespace, dimension, err := coordinatedWarmSemanticExemplars(
context.Background(), coordinator, nil, nil, store, &config, first.fn, nil, newSemanticEmbeddingCache())
	require.NoError(t, err)
	require.Equal(t, 3, loaded)
	require.Positive(t, first.count(), "the winner must actually embed")

	// Second node finds the claim taken, waits, and adopts what the first wrote.
	second := &countingEmbed{}
	secondLoaded, secondNamespace, secondDimension, err := coordinatedWarmSemanticExemplars(
context.Background(), coordinator, nil, nil, store, &config, second.fn, nil, newSemanticEmbeddingCache())
	require.NoError(t, err)

	assert.Equal(t, 0, second.count(), "a peer that adopts a warmed generation must embed nothing")
	assert.Equal(t, loaded, secondLoaded)
	assert.Equal(t, namespace, secondNamespace, "both nodes must serve the same generation")
	assert.Equal(t, dimension, secondDimension)
}

// TestWarmCoordinatorIsDisabledForLocalChromem prevents peers from waiting for
// a namespace that only exists in the elected node's process.
func TestWarmCoordinatorIsDisabledForLocalChromem(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	coordinator := newFakeWarmCoordinator()

	assert.Nil(t, warmCoordinatorForStore(coordinator, store))
}

// TestCoordinatedWarmFallsBackWhenTheWarmerDies guards the failure mode that
// matters most: coordination must never be able to leave a node unwarmed.
func TestCoordinatedWarmFallsBackWhenTheWarmerDies(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	coordinator := newFakeWarmCoordinator()

	// A peer holds the claim and publishes nothing, then vanishes.
	key := semanticWarmKey(&config, semanticExemplars(&config))
	require.NotEmpty(t, key)
	won, err := coordinator.Claim(key, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	go func() {
		<-coordinator.heldObserved
		coordinator.Release(key)
	}()

	embed := &countingEmbed{}
	loaded, namespace, _, err := coordinatedWarmSemanticExemplars(
context.Background(), coordinator, nil, nil, store, &config, embed.fn, nil, newSemanticEmbeddingCache())
	require.NoError(t, err)
	assert.Equal(t, 3, loaded)
	assert.NotEmpty(t, namespace)
	assert.Positive(t, embed.count(), "a node must warm for itself once the claim is gone")
}

// TestCoordinatedWarmIgnoresAnUnavailableCoordinator keeps a degraded KV store
// from being able to stall classification.
func TestCoordinatedWarmIgnoresAnUnavailableCoordinator(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	coordinator := newFakeWarmCoordinator()
	coordinator.claimErr = errors.New("kv unavailable")

	embed := &countingEmbed{}
	loaded, _, _, err := coordinatedWarmSemanticExemplars(
context.Background(), coordinator, nil, nil, store, &config, embed.fn, nil, newSemanticEmbeddingCache())
	require.NoError(t, err)
	assert.Equal(t, 3, loaded)
	assert.Positive(t, embed.count(), "an unreadable coordinator must degrade to warming locally")
}

// TestCoordinatedWarmReleasesTheClaimOnFailure stops one node's failed warm
// from making every peer wait out the claim TTL for a generation that is never
// coming.
func TestCoordinatedWarmReleasesTheClaimOnFailure(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	coordinator := newFakeWarmCoordinator()

	failing := func(context.Context, *SemanticConfig, string) ([]float32, error) {
		return nil, errors.New("provider down")
	}
	_, _, _, err := coordinatedWarmSemanticExemplars(
context.Background(), coordinator, nil, nil, store, &config, failing, nil, newSemanticEmbeddingCache())
	require.Error(t, err)

	key := semanticWarmKey(&config, semanticExemplars(&config))
	assert.False(t, coordinator.ClaimHeld(key), "a failed warm must hand the claim back")
}

// TestSemanticWarmKeyIsIndependentOfDimension is the property that lets the
// claim be taken before any embedding happens. A key including the vector width
// could only be computed after every node had already paid for a batch.
func TestSemanticWarmKeyIsIndependentOfDimension(t *testing.T) {
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	exemplars := semanticExemplars(&config)
	key := semanticWarmKey(&config, exemplars)
	require.NotEmpty(t, key)

	// Same inputs, any width: one key, so all nodes contend for the same claim.
	assert.NotEqual(t, semanticFingerprint(&config, exemplars, 2), key)
	assert.NotEqual(t, semanticFingerprint(&config, exemplars, 1536), key)

	// Reordering phrases must not split the claim.
	reordered := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	reordered.Keywords.SimpleKeywords = []string{"simple exemplar"}
	reordered.Keywords.ComplexKeywords = []string{"complex exemplar"}
	reordered.Keywords.MediumKeywords = []string{"medium exemplar"}
	assert.Equal(t, key, semanticWarmKey(&reordered, semanticExemplars(&reordered)))

	// Anything that changes what gets embedded must change the key.
	remodelled := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	remodelled.Semantic.EmbeddingModel = "another-embedding-model"
	assert.NotEqual(t, key, semanticWarmKey(&remodelled, semanticExemplars(&remodelled)))

	edited := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	edited.Keywords.SimpleKeywords = append(edited.Keywords.SimpleKeywords, "one more exemplar")
	assert.NotEqual(t, key, semanticWarmKey(&edited, semanticExemplars(&edited)))
}

// TestCoordinatedWarmReleasesTheClaimWhenPublishFails stops a peer from waiting
// out the full timeout for a result that was never recorded. The warm itself
// succeeded, so the claim is the only thing left telling peers to keep waiting.
func TestCoordinatedWarmReleasesTheClaimWhenPublishFails(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	coordinator := newFakeWarmCoordinator()
	coordinator.publishErr = errors.New("kv unavailable")

	embed := &countingEmbed{}
	loaded, namespace, _, err := coordinatedWarmSemanticExemplars(
context.Background(), coordinator, nil, nil, store, &config, embed.fn, nil, newSemanticEmbeddingCache())
	require.NoError(t, err, "a publish failure must not fail the warm itself")
	assert.Equal(t, 3, loaded)
	assert.NotEmpty(t, namespace)

	key := semanticWarmKey(&config, semanticExemplars(&config))
	assert.False(t, coordinator.ClaimHeld(key),
		"an unpublished result must release the claim so peers stop waiting")
}
