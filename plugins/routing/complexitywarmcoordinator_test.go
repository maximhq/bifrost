package routing

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newWarmTestTable returns a warm table on a new live store.
func newWarmTestTable(t *testing.T) *livestore.Table[string, string] {
	t.Helper()
	warm := stringTableForTest(t)
	return warm
}

// TestComplexityWarmCoordinatorGrantsOneClaim is the property the whole optimization
// rests on: of the nodes that reload a configuration together, one warms.
func TestComplexityWarmCoordinatorGrantsOneClaim(t *testing.T) {
	warm := newWarmTestTable(t)
	first := newComplexityWarmCoordinator(warm, "node-a")
	second := newComplexityWarmCoordinator(warm, "node-b")
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

// TestComplexityWarmCoordinatorSeparatesGenerations keeps one generation's claim from
// suppressing an unrelated one's warm.
func TestComplexityWarmCoordinatorSeparatesGenerations(t *testing.T) {
	warm := newWarmTestTable(t)
	coordinator := newComplexityWarmCoordinator(warm, "node-a")

	won, err := coordinator.Claim("key-one", time.Minute)
	require.NoError(t, err)
	require.True(t, won)

	won, err = coordinator.Claim("key-two", time.Minute)
	require.NoError(t, err)
	assert.True(t, won, "a different generation is different work")
	assert.False(t, coordinator.ClaimHeld("key-three"))
}

// TestComplexityWarmCoordinatorPublishRoundTrip covers a record this coordinator
// published and one another writer put in the table.
func TestComplexityWarmCoordinatorPublishRoundTrip(t *testing.T) {
	warm := newWarmTestTable(t)
	coordinator := newComplexityWarmCoordinator(warm, "node-a")

	generation := complexity.WarmGeneration{Dimension: 1536, Namespace: "BifrostComplexityRouter_abc123"}
	require.NoError(t, coordinator.Publish("generation-key", generation, time.Minute))

	got, ok := coordinator.Lookup("generation-key")
	require.True(t, ok)
	assert.Equal(t, generation, got)

	// Another writer's record.
	warm.PutWithTTL(complexityWarmResultKeyPrefix+"other-key", "1536|BifrostComplexityRouter_abc123", time.Minute)
	decoded, ok := coordinator.Lookup("other-key")
	require.True(t, ok, "another writer's record must decode the same as this one's")
	assert.Equal(t, generation, decoded)

	_, ok = coordinator.Lookup("never-published")
	assert.False(t, ok)
}

// TestComplexityWarmCoordinatorRejectsUnusableRecords keeps a malformed or truncated
// record from being read as a real generation — adopting a wrong dimension
// would point a node at a namespace that does not exist.
func TestComplexityWarmCoordinatorRejectsUnusableRecords(t *testing.T) {
	for _, value := range []string{
		"",
		"1536",
		"|BifrostComplexityRouter_abc",
		"1536|",
		"notanumber|BifrostComplexityRouter_abc",
		"0|BifrostComplexityRouter_abc",
		"-1|BifrostComplexityRouter_abc",
	} {
		_, ok := decodeStoredWarmGeneration(value)
		assert.False(t, ok, "value %#v must not decode as a generation", value)
	}
}

// TestComplexityWarmCoordinatorRefusesIncompletePublish stops a half-known generation
// from being advertised to peers.
func TestComplexityWarmCoordinatorRefusesIncompletePublish(t *testing.T) {
	warm := newWarmTestTable(t)
	coordinator := newComplexityWarmCoordinator(warm, "node-a")

	assert.Error(t, coordinator.Publish("k", complexity.WarmGeneration{Dimension: 0, Namespace: "ns"}, time.Minute))
	assert.Error(t, coordinator.Publish("k", complexity.WarmGeneration{Dimension: 8, Namespace: "  "}, time.Minute))
	// The separator would make the record ambiguous on the way back.
	assert.Error(t, coordinator.Publish("k", complexity.WarmGeneration{Dimension: 8, Namespace: "a|b"}, time.Minute))

	_, ok := coordinator.Lookup("k")
	assert.False(t, ok)
}

// TestComplexityWarmCoordinatorNilStoreDisablesCoordination documents the deployment
// with no warm table: every process warms for itself, which is what happened before
// coordination existed.
func TestComplexityWarmCoordinatorNilStoreDisablesCoordination(t *testing.T) {
	assert.Nil(t, newComplexityWarmCoordinator(nil, "node-a"))

	var coordinator *complexityWarmCoordinator
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
