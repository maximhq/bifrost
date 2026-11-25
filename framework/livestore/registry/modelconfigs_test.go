package registry

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// modelConfigIDsOf lists the IDs of model config records in the order given.
func modelConfigIDsOf(rows []*ModelConfig) []string {
	ids := make([]string, 0, len(rows))
	for _, m := range rows {
		ids = append(ids, ModelConfigID(m))
	}
	return ids
}

// TestModelConfigsPageAndScope checks that pages walk every model config in ID order, that a
// record carries its model, provider, scope, rate limit ID, budget IDs and calendar flag, that an
// ID with no row is left out, that ModelConfigsByKeys finds only key-scoped configs, and the scope
// index key and key ID each scope gives.
func TestModelConfigsPageAndScope(t *testing.T) {
	ctx := context.Background()
	db := newProviderDB(t, nil).DB()
	insert(t, db,
		rateLimitRow("rl-mc", 1), budgetRow("legacy-b", 9),
		&tables.TableModelConfig{ID: "mc-c", ModelName: "claude-3", Provider: ptr("anthropic")},
		&tables.TableModelConfig{ID: "mc-a", ModelName: "gpt-4o", RateLimitID: ptr("rl-mc"), BudgetID: ptr("legacy-b"), CalendarAligned: true},
		&tables.TableModelConfig{ID: "mc-b", ModelName: "gpt-4o", Provider: ptr("openai"), Scope: tables.ModelConfigScopeVirtualKey, ScopeID: ptr("vk-1")},
	)
	held := budgetRow("mcb-1", 5)
	held.ModelConfigID = ptr("mc-a")
	insert(t, db, held)

	var walked []*ModelConfig
	after := ""
	for {
		rows, next, err := ModelConfigsPage(ctx, db, after, 2)
		require.NoError(t, err)
		walked = append(walked, rows...)
		if next == "" {
			break
		}
		after = next
	}
	require.Equal(t, []string{"mc-a", "mc-b", "mc-c"}, modelConfigIDsOf(walked))
	global, keyScoped, anthropic := walked[0], walked[1], walked[2]
	require.Equal(t, &ModelConfig{
		ID: "mc-a", ModelName: "gpt-4o", Scope: tables.ModelConfigScopeGlobal, RateLimitID: "rl-mc",
		BudgetIDs: []string{"mcb-1"}, CalendarAligned: true, BudgetID: "legacy-b",
	}, global, "the record carries the legacy budget_id column as read")
	require.Equal(t, ModelConfigScopeKey(tables.ModelConfigScopeGlobal, ""), ModelConfigScope(global))
	require.Empty(t, ModelConfigKeyID(global), "a global config belongs to no key")
	require.Equal(t, ptr("openai"), keyScoped.Provider)
	require.Equal(t, "vk-1", keyScoped.ScopeID)
	require.Equal(t, tables.ModelConfigScopeVirtualKey+"\x00vk-1", ModelConfigScope(keyScoped))
	require.Equal(t, "vk-1", ModelConfigKeyID(keyScoped))
	require.Equal(t, ptr("anthropic"), anthropic.Provider)

	rows, err := ModelConfigsByID(ctx, db, []string{"mc-c", "missing"})
	require.NoError(t, err)
	require.Equal(t, []string{"mc-c"}, modelConfigIDsOf(rows))
	rows, err = ModelConfigsByKeys(ctx, db, []string{"vk-1", "vk-2"})
	require.NoError(t, err)
	require.Equal(t, []string{"mc-b"}, modelConfigIDsOf(rows), "only configs scoped to the keys")
	rows, err = ModelConfigsByKeys(ctx, db, []string{"mc-a"})
	require.NoError(t, err)
	require.Empty(t, rows, "a global config is no key's, whatever its ID")
}

// TestModelConfigFromTableDefaultsToGlobal checks that a config with no scope is read as global,
// the scope the column defaults to, and that another scope is kept with its scope ID.
func TestModelConfigFromTableDefaultsToGlobal(t *testing.T) {
	rec := ModelConfigFromTable(&tables.TableModelConfig{ID: "mc-1", ModelName: "gpt-4o", RateLimitID: ptr("rl-1")}, []string{"b-1"})
	require.Equal(t, tables.ModelConfigScopeGlobal, rec.Scope)
	require.Empty(t, rec.ScopeID)
	require.Equal(t, "rl-1", rec.RateLimitID)
	require.Equal(t, []string{"b-1"}, rec.BudgetIDs)

	rec = ModelConfigFromTable(&tables.TableModelConfig{ID: "mc-2", ModelName: "gpt-4o", Scope: tables.ModelConfigScopeVirtualKey, ScopeID: ptr("vk-9")}, nil)
	require.Equal(t, tables.ModelConfigScopeVirtualKey, rec.Scope)
	require.Equal(t, "vk-9", ModelConfigKeyID(rec))
}
