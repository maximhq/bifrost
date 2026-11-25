package governance

import (
	"context"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/stretchr/testify/require"
)

// TestAKeyNeverNamesABudgetTheStoreCannotCheck: replacing a key's budget publishes the key and its
// budgets in one refresh. A request reads the key's budget IDs and checks each through the store,
// which counts a budget once its Budgets subscriber has seeded it. So when the subscribers learn
// of the new budget, the key must still name the old one, still checkable, and once the old one is
// removed, the key must name the new one, already checkable: at no point does the key name a
// budget the store would skip.
func TestAKeyNeverNamesABudgetTheStoreCannotCheck(t *testing.T) {
	oldBudget := &configstoreTables.TableBudget{ID: "b-old", MaxLimit: 10, ResetDuration: "1M"}
	vk := buildVirtualKeyWithBudget("vk-1", "sk-bf-replace", "Replaced budget", oldBudget)
	h := newLiveHarness(t, &configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{*vk}}, nil)

	var mu sync.Mutex
	var store *LocalGovernanceStore
	var unchecked []string
	// Subscribed before the store, so it runs ahead of the store's own seeding in each call: what
	// it sees is what a request reading the key at that moment would see.
	cancel := h.live.Collections.Budgets.Subscribe(func(ctx context.Context, _ []livestore.Change[registry.Budget]) {
		mu.Lock()
		defer mu.Unlock()
		if store == nil {
			return
		}
		k, ok := h.live.Collections.VirtualKeys.Get("vk-1")
		if !ok {
			return
		}
		for _, id := range k.BudgetIDs {
			if store.LoadBudget(ctx, id) == nil {
				unchecked = append(unchecked, id)
			}
		}
	})
	t.Cleanup(cancel)
	s := h.mustGovernanceStore(NewMockLogger(), nil)
	mu.Lock()
	store = s
	mu.Unlock()
	require.NotNil(t, s.LoadBudget(context.Background(), "b-old"))

	replaced := *vk
	replaced.Budgets = []configstoreTables.TableBudget{{ID: "b-new", MaxLimit: 20, ResetDuration: "1M"}}
	h.putVirtualKey(&replaced)

	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, unchecked, "the key named a budget the store could not check yet")
	k, ok := h.live.Collections.VirtualKeys.Get("vk-1")
	require.True(t, ok)
	require.Equal(t, []string{"b-new"}, k.BudgetIDs)
	require.NotNil(t, s.LoadBudget(context.Background(), "b-new"))
	require.Nil(t, s.LoadBudget(context.Background(), "b-old"))
}
