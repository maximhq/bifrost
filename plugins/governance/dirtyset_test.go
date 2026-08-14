package governance

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sortedDrain drains a set and sorts the result, since stripe iteration order
// is unspecified.
func sortedDrain(set *dirtyKeySet) []string {
	ids := set.drain()
	sort.Strings(ids)
	return ids
}

// TestDirtySetDrainReturnsEachIDOnceAndClears covers the basic contract: a key
// marked any number of times comes back once, and a second drain is empty.
func TestDirtySetDrainReturnsEachIDOnceAndClears(t *testing.T) {
	var set dirtyKeySet
	for range 5 {
		set.mark("budget-a")
	}
	set.mark("budget-b")

	assert.Equal(t, []string{"budget-a", "budget-b"}, sortedDrain(&set))
	assert.Empty(t, set.drain(), "a drain must clear what it returned")
}

// TestDirtySetIgnoresEmptyIDs guards the loops that mark whatever ID they were
// handed, so a missing ID never becomes a phantom entry to look up.
func TestDirtySetIgnoresEmptyIDs(t *testing.T) {
	var set dirtyKeySet
	set.mark("")
	set.markAll([]string{"", "real"})
	assert.Equal(t, []string{"real"}, sortedDrain(&set))
}

// TestDirtySetMarkIsConcurrencySafe exercises the request-path property: many
// goroutines mark at once, and nothing is lost or duplicated.
func TestDirtySetMarkIsConcurrencySafe(t *testing.T) {
	var set dirtyKeySet
	var wg sync.WaitGroup
	for worker := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				set.mark(string(rune('a'+worker)) + string(rune('a'+i%26)))
			}
		}()
	}
	wg.Wait()

	seen := make(map[string]struct{})
	for _, id := range set.drain() {
		_, duplicate := seen[id]
		require.False(t, duplicate, "drain returned %q twice", id)
		seen[id] = struct{}{}
	}
	assert.Len(t, seen, 16*26)
}

// TestChargingABudgetMarksItDirty is the whole point of the set: usage sync has
// to be able to find what moved without walking every budget in memory.
func TestChargingABudgetMarksItDirty(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocalGovernanceStore(ctx, NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	store.UpsertBudgetConfig(ctx, "budget", &configstoreTables.TableBudget{ID: "budget", MaxLimit: 100})
	require.Empty(t, store.DrainDirtyBudgetIDs(), "an upsert alone is not a usage change")

	require.NoError(t, store.BumpBudgetUsage(ctx, "budget", 5))
	assert.Equal(t, []string{"budget"}, store.DrainDirtyBudgetIDs())
	assert.Empty(t, store.DrainDirtyBudgetIDs(), "the second tick sees nothing new")
}

// TestChargingAnAbsentBudgetMarksNothing keeps a charge against a budget that is
// not in memory from queueing a lookup that can never succeed.
func TestChargingAnAbsentBudgetMarksNothing(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocalGovernanceStore(ctx, NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	require.NoError(t, store.BumpBudgetUsage(ctx, "missing", 5))
	assert.Empty(t, store.DrainDirtyBudgetIDs())
}

// TestDeletingABudgetMarksItDirty is what lets usage sync emit a tombstone. A
// receiver merging deltas must never infer removal from absence, so the
// deletion has to be announced.
func TestDeletingABudgetMarksItDirty(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocalGovernanceStore(ctx, NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	store.UpsertBudgetConfig(ctx, "budget", &configstoreTables.TableBudget{ID: "budget", MaxLimit: 100})
	require.NoError(t, store.BumpBudgetUsage(ctx, "budget", 1))
	require.NotEmpty(t, store.DrainDirtyBudgetIDs())

	store.DeleteBudget(ctx, "budget")
	assert.Equal(t, []string{"budget"}, store.DrainDirtyBudgetIDs(), "a deletion must be announced")
}

// TestRemarkingRestoresDrainedIDs backs the failure path: a sync that could not
// send has to put back what it took, or the change is invisible until the next
// full baseline sweep.
func TestRemarkingRestoresDrainedIDs(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocalGovernanceStore(ctx, NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	store.UpsertBudgetConfig(ctx, "budget", &configstoreTables.TableBudget{ID: "budget", MaxLimit: 100})
	require.NoError(t, store.BumpBudgetUsage(ctx, "budget", 3))

	drained := store.DrainDirtyBudgetIDs()
	require.Equal(t, []string{"budget"}, drained)

	store.RemarkDirtyBudgetIDs(drained)
	assert.Equal(t, []string{"budget"}, store.DrainDirtyBudgetIDs(), "a failed send must not lose the change")
}
