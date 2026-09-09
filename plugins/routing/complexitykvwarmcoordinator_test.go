package routing

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newWarmCoordinatorTestStore(t *testing.T) *kvstore.Store {
	t.Helper()
	store, err := kvstore.New(kvstore.Config{
		DefaultTTL:      time.Minute,
		CleanupInterval: time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestKVWarmCoordinatorGrantsOneClaim is the property the whole optimization
// rests on: of the nodes that reload a configuration together, one warms.
func TestKVWarmCoordinatorGrantsOneClaim(t *testing.T) {
	store := newWarmCoordinatorTestStore(t)
	first := newKVComplexityWarmCoordinator(store, "node-a")
	second := newKVComplexityWarmCoordinator(store, "node-b")
	require.NotNil(t, first)
	require.NotNil(t, second)

	won, err := first.Claim("generation-key", time.Minute)
	require.NoError(t, err)
	assert.True(t, won)

	won, err = second.Claim("generation-key", time.Minute)
	require.NoError(t, err)
	assert.False(t, won, "a second node must not also be told to warm")
	assert.True(t, second.ClaimHeld("generation-key"))

	// Releasing lets a peer take over rather than wait out the TTL.
	first.Release("generation-key")
	assert.False(t, second.ClaimHeld("generation-key"))
	won, err = second.Claim("generation-key", time.Minute)
	require.NoError(t, err)
	assert.True(t, won)
}

// TestKVWarmCoordinatorSeparatesGenerations keeps one generation's claim from
// suppressing an unrelated one's warm.
func TestKVWarmCoordinatorSeparatesGenerations(t *testing.T) {
	store := newWarmCoordinatorTestStore(t)
	coordinator := newKVComplexityWarmCoordinator(store, "node-a")

	won, err := coordinator.Claim("key-one", time.Minute)
	require.NoError(t, err)
	require.True(t, won)

	won, err = coordinator.Claim("key-two", time.Minute)
	require.NoError(t, err)
	assert.True(t, won, "a different generation is different work")
	assert.False(t, coordinator.ClaimHeld("key-three"))
}

// TestKVWarmCoordinatorPublishRoundTrip covers both shapes a published record
// comes back in: the string a local write stores, and the raw JSON bytes a
// gossiped write arrives as on a peer, since no decoder is registered for this
// key prefix.
func TestKVWarmCoordinatorPublishRoundTrip(t *testing.T) {
	store := newWarmCoordinatorTestStore(t)
	coordinator := newKVComplexityWarmCoordinator(store, "node-a")

	generation := complexity.WarmGeneration{Dimension: 1536, Namespace: "BifrostComplexityRouter_abc123"}
	require.NoError(t, coordinator.Publish("generation-key", generation, time.Minute))

	got, ok := coordinator.Lookup("generation-key")
	require.True(t, ok)
	assert.Equal(t, generation, got)

	// The replicated shape.
	encoded, err := json.Marshal("1536|BifrostComplexityRouter_abc123")
	require.NoError(t, err)
	decoded, ok := decodeStoredWarmGeneration(encoded)
	require.True(t, ok, "a gossiped record must decode the same as a local one")
	assert.Equal(t, generation, decoded)

	_, ok = coordinator.Lookup("never-published")
	assert.False(t, ok)
}

// TestKVWarmCoordinatorRejectsUnusableRecords keeps a malformed or truncated
// record from being read as a real generation — adopting a wrong dimension
// would point a node at a namespace that does not exist.
func TestKVWarmCoordinatorRejectsUnusableRecords(t *testing.T) {
	for _, value := range []any{
		"",
		"1536",
		"|BifrostComplexityRouter_abc",
		"1536|",
		"notanumber|BifrostComplexityRouter_abc",
		"0|BifrostComplexityRouter_abc",
		"-1|BifrostComplexityRouter_abc",
		42,
		nil,
	} {
		_, ok := decodeStoredWarmGeneration(value)
		assert.False(t, ok, "value %#v must not decode as a generation", value)
	}
}

// TestKVWarmCoordinatorRefusesIncompletePublish stops a half-known generation
// from being advertised to peers.
func TestKVWarmCoordinatorRefusesIncompletePublish(t *testing.T) {
	store := newWarmCoordinatorTestStore(t)
	coordinator := newKVComplexityWarmCoordinator(store, "node-a")

	assert.Error(t, coordinator.Publish("k", complexity.WarmGeneration{Dimension: 0, Namespace: "ns"}, time.Minute))
	assert.Error(t, coordinator.Publish("k", complexity.WarmGeneration{Dimension: 8, Namespace: "  "}, time.Minute))
	// The separator would make the record ambiguous on the way back.
	assert.Error(t, coordinator.Publish("k", complexity.WarmGeneration{Dimension: 8, Namespace: "a|b"}, time.Minute))

	_, ok := coordinator.Lookup("k")
	assert.False(t, ok)
}

// TestKVWarmCoordinatorNilStoreDisablesCoordination documents the deployment
// with no KVStore: every node warms for itself, which is what happened before
// coordination existed.
func TestKVWarmCoordinatorNilStoreDisablesCoordination(t *testing.T) {
	assert.Nil(t, newKVComplexityWarmCoordinator(nil, "node-a"))

	var coordinator *kvComplexityWarmCoordinator
	won, err := coordinator.Claim("k", time.Minute)
	require.NoError(t, err)
	assert.True(t, won, "a nil coordinator must never withhold a warm")
	assert.False(t, coordinator.ClaimHeld("k"))
	coordinator.Release("k")
	_, ok := coordinator.Lookup("k")
	assert.False(t, ok)
}

// TestRoutingNodeIDsAreDistinct guards the value that keeps two nodes' claims
// apart.
func TestRoutingNodeIDsAreDistinct(t *testing.T) {
	seen := map[string]struct{}{}
	for range 100 {
		id := newRoutingNodeID()
		require.NotEmpty(t, id)
		_, duplicate := seen[id]
		require.False(t, duplicate, "node ids must not collide")
		seen[id] = struct{}{}
	}
}
