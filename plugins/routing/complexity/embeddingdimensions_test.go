// Tests for remembering a measured embedding width across restarts.
package complexity

import (
	"context"
	"errors"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDimensionStore is a durable memory of measured widths, standing in for
// the config row without a database.
type fakeDimensionStore struct {
	mu     sync.Mutex
	widths map[string]int
	writes int
}

func newFakeDimensionStore() *fakeDimensionStore {
	return &fakeDimensionStore{widths: map[string]int{}}
}

func (f *fakeDimensionStore) Dimension(identity string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dimension, ok := f.widths[identity]
	return dimension, ok
}

func (f *fakeDimensionStore) Remember(identity string, dimension int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.widths[identity] = dimension
	f.writes++
}

// TestRememberedDimensionLetsARestartAdoptWithoutEmbedding is the restart case.
// The vector width is the only part of a generation's identity that cannot be
// derived from configuration, so without a memory of it a boot must call the
// provider just to learn whether the generation it wants is already stored.
func TestRememberedDimensionLetsARestartAdoptWithoutEmbedding(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	dimensions := newFakeDimensionStore()

	// First boot measures and records.
	first := &countingEmbed{}
	_, namespace, dimension, err := coordinatedWarmSemanticExemplars(
context.Background(), nil, dimensions, nil, store, &config, first.fn, nil, newSemanticEmbeddingCache())
	require.NoError(t, err)
	require.Positive(t, first.count())
	remembered, ok := dimensions.Dimension(semanticEmbeddingIdentity(config.Semantic))
	require.True(t, ok)
	assert.Equal(t, dimension, remembered)

	// A restart brings an empty embedding cache but the same store and row.
	second := &countingEmbed{}
	loaded, restartNamespace, restartDimension, err := coordinatedWarmSemanticExemplars(
context.Background(), nil, dimensions, nil, store, &config, second.fn, nil, newSemanticEmbeddingCache())
	require.NoError(t, err)
	assert.Equal(t, 0, second.count(), "a restart onto an already-warmed generation must call no provider")
	assert.Equal(t, len(semanticExemplars(&config)), loaded)
	assert.Equal(t, namespace, restartNamespace)
	assert.Equal(t, dimension, restartDimension)
}

// TestRememberedDimensionIsOnlyAHint covers the value going stale — a model
// re-versioned under the same name returning a different width. Adoption must
// fail rather than mislead, and the new measurement must replace the old.
func TestRememberedDimensionIsOnlyAHint(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	dimensions := newFakeDimensionStore()
	identity := semanticEmbeddingIdentity(config.Semantic)

	// A width nothing was ever warmed at.
	dimensions.Remember(identity, testSemanticDimension+7)

	embed := &countingEmbed{}
	loaded, namespace, dimension, err := coordinatedWarmSemanticExemplars(
context.Background(), nil, dimensions, nil, store, &config, embed.fn, nil, newSemanticEmbeddingCache())
	require.NoError(t, err)
	assert.Positive(t, embed.count(), "a width that adopts nothing must fall back to measuring")
	assert.Equal(t, testSemanticDimension, dimension, "the measurement wins over the stale memory")
	assert.NotEmpty(t, namespace)
	assert.Equal(t, len(semanticExemplars(&config)), loaded)

	corrected, ok := dimensions.Dimension(identity)
	require.True(t, ok)
	assert.Equal(t, testSemanticDimension, corrected, "the stale value must be replaced, not kept failing")
}

// TestRememberedDimensionIsNotRecordedForAFailedWarm keeps a width that was
// never confirmed by a completed generation out of the durable record.
func TestRememberedDimensionIsNotRecordedForAFailedWarm(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	dimensions := newFakeDimensionStore()

	failing := func(context.Context, *SemanticConfig, string) ([]float32, error) {
		return nil, errors.New("provider down")
	}
	_, _, _, err := coordinatedWarmSemanticExemplars(
context.Background(), nil, dimensions, nil, store, &config, failing, nil, newSemanticEmbeddingCache())
	require.Error(t, err)

	_, ok := dimensions.Dimension(semanticEmbeddingIdentity(config.Semantic))
	assert.False(t, ok, "a failed warm measured nothing worth remembering")
}

// TestAdoptingPeerRemembersTheWidthItWasGiven means a node that never measured
// can still take the no-provider path on its own next restart.
func TestAdoptingPeerRemembersTheWidthItWasGiven(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := newTestChromemStore(t, logger)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	coordinator := newFakeWarmCoordinator()

	winner := &countingEmbed{}
	_, _, dimension, err := coordinatedWarmSemanticExemplars(
context.Background(), coordinator, nil, nil, store, &config, winner.fn, nil, newSemanticEmbeddingCache())
	require.NoError(t, err)

	peerDimensions := newFakeDimensionStore()
	peer := &countingEmbed{}
	_, _, _, err = coordinatedWarmSemanticExemplars(
context.Background(), coordinator, peerDimensions, nil, store, &config, peer.fn, nil, newSemanticEmbeddingCache())
	require.NoError(t, err)
	require.Equal(t, 0, peer.count())

	remembered, ok := peerDimensions.Dimension(semanticEmbeddingIdentity(config.Semantic))
	require.True(t, ok, "an adopting peer must record the width it adopted at")
	assert.Equal(t, dimension, remembered)
}
