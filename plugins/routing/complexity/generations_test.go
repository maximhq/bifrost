// Tests for recognising, listing and removing stored exemplar generations.
package complexity

import (
	"context"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdoptSemanticGenerationRequiresACompleteGeneration keeps a peer from
// adopting a namespace whose vectors are still being written. The marker is
// what makes a generation complete, and it is written last.
func TestAdoptSemanticGenerationRequiresACompleteGeneration(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	exemplars := semanticExemplars(&config)

	_, adopted, err := adoptSemanticGeneration(context.Background(), store, &config, exemplars, testSemanticDimension)
	require.NoError(t, err)
	assert.False(t, adopted, "nothing has been warmed yet")

	_, _, dimension, err := warmSemanticExemplars(context.Background(), nil, store, &config, testSemanticEmbedding, nil, newSemanticEmbeddingCache())
	require.NoError(t, err)

	_, adopted, err = adoptSemanticGeneration(context.Background(), store, &config, exemplars, dimension)
	require.NoError(t, err)
	assert.True(t, adopted, "a completed generation is adoptable")

	// A different width is a different generation, not this one.
	_, adopted, err = adoptSemanticGeneration(context.Background(), store, &config, exemplars, dimension+1)
	require.NoError(t, err)
	assert.False(t, adopted)
}

// TestListGenerationsFlagsTheServingOne is what makes retired generations
// actionable. The store holds no phrase text, so a namespace is an opaque hash;
// without knowing which one is live, an operator cannot safely delete any.
func TestListGenerationsFlagsTheServingOne(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)

	classifier := NewSemanticClassifier(context.Background(), logger)
	t.Cleanup(func() { require.NoError(t, classifier.Close()) })
	classifier.SetConfiguredStore(store)
	classifier.SetEmbeddingFunc(testSemanticEmbedding)

	v1 := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	classifier.Configure(&v1)
	requireSemanticReady(t, classifier)
	v1Namespace := classifier.Status().Namespace
	require.NotEmpty(t, v1Namespace)

	// A configuration change mints a new generation. This classifier keeps its
	// own store, so the old namespace is reclaimed; write one directly to stand
	// in for the retired generation a shared backend would have kept.
	retired := SemanticVectorStoreNamespace + "_deadbeef"
	require.NoError(t, store.CreateNamespace(context.Background(), retired, testSemanticDimension, semanticVectorStoreProperties))

	generations, err := classifier.ListGenerations(context.Background())
	require.NoError(t, err)

	byNamespace := map[string]bool{}
	for _, generation := range generations {
		byNamespace[generation.Namespace] = generation.Active
	}
	require.Contains(t, byNamespace, v1Namespace)
	assert.True(t, byNamespace[v1Namespace], "the serving generation must be flagged active")
	require.Contains(t, byNamespace, retired)
	assert.False(t, byNamespace[retired], "a retired generation must not be flagged active")
}

// TestDeleteGenerationRefusesTheServingOne stops an operator from deleting the
// namespace routing is querying, which would leave classification pointed at
// records that no longer exist until the next warm replaced them.
func TestDeleteGenerationRefusesTheServingOne(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)

	classifier := NewSemanticClassifier(context.Background(), logger)
	t.Cleanup(func() { require.NoError(t, classifier.Close()) })
	classifier.SetConfiguredStore(store)
	classifier.SetEmbeddingFunc(testSemanticEmbedding)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	classifier.Configure(&config)
	requireSemanticReady(t, classifier)

	active := classifier.Status().Namespace
	require.NotEmpty(t, active)
	err := classifier.DeleteGeneration(context.Background(), active)
	require.ErrorIs(t, err, ErrGenerationActive)

	// Still there, and still serving.
	generations, err := classifier.ListGenerations(context.Background())
	require.NoError(t, err)
	assert.Contains(t, generationNamespaces(generations), active)
}

// TestDeleteGenerationRefusesForeignNamespaces keeps this from becoming a
// general-purpose collection deleter. The store is shared with the semantic
// cache and, on an external backend, with whatever else the operator runs there.
func TestDeleteGenerationRefusesForeignNamespaces(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	classifier := NewSemanticClassifier(context.Background(), logger)
	t.Cleanup(func() { require.NoError(t, classifier.Close()) })
	classifier.SetConfiguredStore(store)
	classifier.SetEmbeddingFunc(testSemanticEmbedding)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	classifier.Configure(&config)
	requireSemanticReady(t, classifier)

	somebodyElses := "BifrostSemanticCache"
	require.NoError(t, store.CreateNamespace(context.Background(), somebodyElses, testSemanticDimension, nil))

	for _, namespace := range []string{somebodyElses, "", "BifrostComplexityRouter", "unrelated"} {
		err := classifier.DeleteGeneration(context.Background(), namespace)
		require.ErrorIs(t, err, ErrNotAGeneration, "namespace %q must be refused", namespace)
	}

	// The neighbouring namespace is untouched.
	names, err := store.ListNamespaces(context.Background(), "")
	require.NoError(t, err)
	assert.Contains(t, names, somebodyElses)
}

// TestDeleteGenerationRemovesARetiredOne is the operation itself.
func TestDeleteGenerationRemovesARetiredOne(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	classifier := NewSemanticClassifier(context.Background(), logger)
	t.Cleanup(func() { require.NoError(t, classifier.Close()) })
	classifier.SetConfiguredStore(store)
	classifier.SetEmbeddingFunc(testSemanticEmbedding)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	classifier.Configure(&config)
	requireSemanticReady(t, classifier)

	retired := SemanticVectorStoreNamespace + "_0badc0de"
	require.NoError(t, store.CreateNamespace(context.Background(), retired, testSemanticDimension, semanticVectorStoreProperties))

	require.NoError(t, classifier.DeleteGeneration(context.Background(), retired))

	generations, err := classifier.ListGenerations(context.Background())
	require.NoError(t, err)
	assert.NotContains(t, generationNamespaces(generations), retired)
	// Deleting what is already gone is not an error; a retry must converge.
	assert.NoError(t, classifier.DeleteGeneration(context.Background(), retired))
}

func generationNamespaces(generations []GenerationInfo) []string {
	names := make([]string, 0, len(generations))
	for _, generation := range generations {
		names = append(names, generation.Namespace)
	}
	return names
}

// TestDeleteGenerationRefusesWhileWarming closes the window in which a warm is
// building toward a namespace the classifier cannot name yet: the fingerprint
// is only known once the width has been measured, inside the warm. Deleting
// then can remove the generation the warm is about to activate.
func TestDeleteGenerationRefusesWhileWarming(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)

	release := make(chan struct{})
	slowEmbed := func(ctx context.Context, config *SemanticConfig, text string) ([]float32, error) {
		<-release
		return testSemanticEmbedding(ctx, config, text)
	}

	classifier := NewSemanticClassifier(context.Background(), logger)
	t.Cleanup(func() {
		close(release)
		require.NoError(t, classifier.Close())
	})
	classifier.SetConfiguredStore(store)
	classifier.SetEmbeddingFunc(slowEmbed)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	classifier.Configure(&config)

	require.Eventually(t, func() bool {
		return classifier.Status().State == SemanticStatusWarming
	}, time.Second, 10*time.Millisecond)

	err := classifier.DeleteGeneration(context.Background(), SemanticVectorStoreNamespace+"_whatever")
	require.ErrorIs(t, err, ErrGenerationActive,
		"deletion must be refused while a warm could be building the target namespace")
}

// TestWarmDoesNotActivateANamespaceBeingDeleted covers the interleaving the
// warming guard alone cannot: a delete that has already passed its checks and
// is inside store.DeleteNamespace when a warm completes. Activating then would
// serve records the delete is erasing.
func TestWarmDoesNotActivateANamespaceBeingDeleted(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)

	classifier := NewSemanticClassifier(context.Background(), logger)
	t.Cleanup(func() { require.NoError(t, classifier.Close()) })
	classifier.SetConfiguredStore(store)
	classifier.SetEmbeddingFunc(testSemanticEmbedding)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	classifier.Configure(&config)
	requireSemanticReady(t, classifier)

	target := classifier.Status().Namespace
	require.NotEmpty(t, target)

	// Stand in for a delete that is past its guards and inside the store call.
	classifier.mu.Lock()
	if classifier.deleting == nil {
		classifier.deleting = map[string]int{}
	}
	classifier.deleting[target]++
	classifier.active = nil
	classifier.mu.Unlock()

	// A warm completing now must not publish the marked namespace.
	classifier.Configure(&config)
	time.Sleep(200 * time.Millisecond)
	assert.Empty(t, classifier.Status().Namespace,
		"a namespace being deleted must not be activated by a concurrent warm")

	// Once the delete finishes, the retry is free to activate again. Clearing
	// the mark and waking the waiter is what DeleteGeneration does on its way
	// out of store.DeleteNamespace.
	classifier.mu.Lock()
	delete(classifier.deleting, target)
	classifier.deletionDone.Broadcast()
	classifier.mu.Unlock()
	require.Eventually(t, func() bool {
		return classifier.Status().Namespace == target
	}, 3*time.Second, 25*time.Millisecond,
		"activation must resume once the deletion completes")
}

// fakeClaimer records the namespaces a warm announced.
type fakeClaimer struct {
	mu     sync.Mutex
	claims []string
}

func (f *fakeClaimer) ClaimGeneration(_ context.Context, namespace string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims = append(f.claims, namespace)
}

func (f *fakeClaimer) ReleaseGeneration(string) {}

func (f *fakeClaimer) claimed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.claims...)
}

// TestWarmClaimsItsTargetBeforeEmbedding is the point of claiming on target. A
// warm creates its namespace early and can spend minutes embedding; until it
// finishes there is nothing for a peer to see, and a warm slower than the
// reclamation interval would have its half-built namespace collected.
func TestWarmClaimsItsTargetBeforeEmbedding(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	claimer := &fakeClaimer{}

	embedded := 0
	countingEmbedFn := func(ctx context.Context, semantic *SemanticConfig, text string) ([]float32, error) {
		// By the first phrase past the dimension probe, the target must already
		// be claimed — that is what protects the rest of the warm.
		if embedded > 0 {
			assert.NotEmpty(t, claimer.claimed(), "the target must be claimed before the bulk of the embedding")
		}
		embedded++
		return testSemanticEmbedding(ctx, semantic, text)
	}

	loaded, namespace, _, err := warmSemanticExemplars(
		context.Background(), claimer, store, &config, countingEmbedFn, nil, newSemanticEmbeddingCache())
	require.NoError(t, err)
	require.Equal(t, 3, loaded)

	assert.Contains(t, claimer.claimed(), namespace,
		"the namespace a warm builds must be announced while it is building it")
}

// TestWarmSurvivesAClaimerThatCannotRecord keeps the registry from being able to
// stall a warm: a missed claim costs the early protection, never the warm.
func TestWarmSurvivesAClaimerThatCannotRecord(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)

	loaded, namespace, _, err := warmSemanticExemplars(
		context.Background(), brokenClaimer{}, store, &config, testSemanticEmbedding, nil, newSemanticEmbeddingCache())
	require.NoError(t, err)
	assert.Equal(t, 3, loaded)
	assert.NotEmpty(t, namespace)
}

// brokenClaimer stands in for a registry that cannot reach its config store.
type brokenClaimer struct{}

func (brokenClaimer) ClaimGeneration(context.Context, string) {}
func (brokenClaimer) ReleaseGeneration(string)                {}

// TestWorkerDoesNotSpawnADuplicateWhileWaitingOnDeletion guards the retry path.
// The worker clears its warming flag before deciding what to do next; if it then
// goes back round to warm again without restoring it, requestWarmupLocked is
// free to start a second worker alongside the first.
func TestWorkerDoesNotSpawnADuplicateWhileWaitingOnDeletion(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)

	classifier := NewSemanticClassifier(context.Background(), logger)
	t.Cleanup(func() { require.NoError(t, classifier.Close()) })
	classifier.SetConfiguredStore(store)
	classifier.SetEmbeddingFunc(testSemanticEmbedding)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	classifier.Configure(&config)
	requireSemanticReady(t, classifier)

	target := classifier.Status().Namespace
	require.NotEmpty(t, target)

	classifier.mu.Lock()
	if classifier.deleting == nil {
		classifier.deleting = map[string]int{}
	}
	classifier.deleting[target]++
	classifier.active = nil
	classifier.mu.Unlock()

	classifier.Configure(&config)
	require.Eventually(t, func() bool {
		classifier.mu.Lock()
		defer classifier.mu.Unlock()
		return classifier.warming
	}, time.Second, 10*time.Millisecond)

	// Parked waiting on the deletion, the worker must still count as warming so
	// nothing else starts one.
	classifier.mu.Lock()
	warming := classifier.warming
	classifier.mu.Unlock()
	assert.True(t, warming, "a worker waiting to retry must still be counted as warming")

	classifier.mu.Lock()
	delete(classifier.deleting, target)
	classifier.deletionDone.Broadcast()
	classifier.mu.Unlock()

	require.Eventually(t, func() bool {
		return classifier.Status().Namespace == target
	}, 3*time.Second, 25*time.Millisecond)
}

// TestCloseReleasesAWorkerWaitingOnDeletion keeps Close from blocking on a
// worker parked on the deletion condition.
func TestCloseReleasesAWorkerWaitingOnDeletion(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)

	classifier := NewSemanticClassifier(context.Background(), logger)
	classifier.SetConfiguredStore(store)
	classifier.SetEmbeddingFunc(testSemanticEmbedding)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	classifier.Configure(&config)
	requireSemanticReady(t, classifier)

	target := classifier.Status().Namespace
	classifier.mu.Lock()
	if classifier.deleting == nil {
		classifier.deleting = map[string]int{}
	}
	classifier.deleting[target]++
	classifier.active = nil
	classifier.mu.Unlock()
	classifier.Configure(&config)

	closed := make(chan error, 1)
	go func() { closed <- classifier.Close() }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked on a worker waiting for a deletion that never finishes")
	}
}
