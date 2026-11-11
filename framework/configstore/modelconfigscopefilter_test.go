package configstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestGetModelConfigsPaginatedScopesFilter covers the multi-value scope filter.
// One UI filter option can cover several scope values, so the query must OR them:
// in an enterprise build the "User" option covers both "user" and
// "access_profile". Those two scopes are registered by enterprise at runtime and
// fail model-config validation in an OSS-only test, so this exercises the same
// OR-ing over the OSS-native scopes.
func TestGetModelConfigsPaginatedScopesFilter(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&tables.TableModelConfig{}))

	sid := func(v string) *string { return &v }
	seed := []tables.TableModelConfig{
		{ID: "mc-1", Scope: "virtual_key", ScopeID: sid("vk1"), ModelName: "gpt-4o"},
		{ID: "mc-2", Scope: "virtual_key", ScopeID: sid("vk2"), ModelName: "claude"},
		{ID: "mc-3", Scope: "global", ModelName: "gemini"},
	}
	for i := range seed {
		require.NoError(t, db.Create(&seed[i]).Error)
	}

	store := &RDBConfigStore{}
	store.db.Store(db)
	ctx := context.Background()

	modelsForParams := func(t *testing.T, params ModelConfigsQueryParams) []string {
		t.Helper()
		rows, total, err := store.GetModelConfigsPaginated(ctx, params)
		require.NoError(t, err)
		require.Equal(t, int64(len(rows)), total, "total_count must match the filtered rows, not the whole table")
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.ModelName)
		}
		return out
	}
	modelsFor := func(t *testing.T, scopes []string) []string {
		t.Helper()
		return modelsForParams(t, ModelConfigsQueryParams{Scopes: scopes})
	}

	t.Run("single scope filters exactly", func(t *testing.T) {
		require.ElementsMatch(t, []string{"gemini"}, modelsFor(t, []string{"global"}))
	})

	t.Run("several scopes are OR-ed", func(t *testing.T) {
		require.ElementsMatch(t, []string{"gpt-4o", "claude", "gemini"}, modelsFor(t, []string{"global", "virtual_key"}))
	})

	t.Run("empty scopes means no scope filter", func(t *testing.T) {
		require.Len(t, modelsFor(t, nil), 3)
	})

	t.Run("unknown scope matches nothing", func(t *testing.T) {
		require.Empty(t, modelsFor(t, []string{"nope"}))
	})

	// The deprecated single Scope field stays supported: an existing caller of
	// this published module must keep filtering exactly as it did.
	t.Run("deprecated Scope field still filters on its own", func(t *testing.T) {
		got := modelsForParams(t, ModelConfigsQueryParams{Scope: "global"})
		require.ElementsMatch(t, []string{"gemini"}, got)
	})

	// A query scope on the context (the enterprise DAC filter) narrows both the
	// page and its total, so the list never pages over rows the caller cannot see.
	t.Run("context query scope narrows page and total", func(t *testing.T) {
		scoped := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB {
			return db.Where("scope = ? OR scope_id = ?", "global", "vk1")
		})
		rows, total, err := store.GetModelConfigsPaginated(scoped, ModelConfigsQueryParams{Limit: 1})
		require.NoError(t, err)
		require.Equal(t, int64(2), total)
		require.Len(t, rows, 1)
		require.Contains(t, []string{"mc-1", "mc-3"}, rows[0].ID)
	})

	t.Run("deprecated Scope is OR-ed with Scopes", func(t *testing.T) {
		got := modelsForParams(t, ModelConfigsQueryParams{Scope: "global", Scopes: []string{"virtual_key"}})
		require.ElementsMatch(t, []string{"gemini", "gpt-4o", "claude"}, got)
	})

	t.Run("the same scope in both fields is not double-counted", func(t *testing.T) {
		got := modelsForParams(t, ModelConfigsQueryParams{Scope: "global", Scopes: []string{"global"}})
		require.ElementsMatch(t, []string{"gemini"}, got)
	})

	t.Run("both empty means no scope filter", func(t *testing.T) {
		require.Len(t, modelsForParams(t, ModelConfigsQueryParams{}), 3)
	})
}

// TestGetModelConfigsPaginatedAppliesToMCP covers the MCP page listing: only holder-level rows (model
// "*", no provider) with a shared or mcp budget are returned, so a row whose budgets are all model-only
// and every narrower row stay off it, and the total counts the same rows.
func TestGetModelConfigsPaginatedAppliesToMCP(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&tables.TableModelConfig{}, &tables.TableBudget{}, &tables.TableRateLimit{}))

	sid := func(v string) *string { return &v }
	seed := []tables.TableModelConfig{
		{ID: "global-shared", Scope: "global", ModelName: "*"},
		{ID: "vk1-mcp", Scope: "virtual_key", ScopeID: sid("vk1"), ModelName: "*"},
		{ID: "vk2-model-only", Scope: "virtual_key", ScopeID: sid("vk2"), ModelName: "*"},
		{ID: "vk3-no-budgets", Scope: "virtual_key", ScopeID: sid("vk3"), ModelName: "*"},
		{ID: "vk1-openai", Scope: "virtual_key", ScopeID: sid("vk1"), ModelName: "*", Provider: sid("openai")},
		{ID: "global-gpt-4o", Scope: "global", ModelName: "gpt-4o"},
	}
	for i := range seed {
		require.NoError(t, db.Create(&seed[i]).Error)
	}
	budgets := []struct {
		owner string
		scope tables.LimitScope
	}{
		{"global-shared", tables.LimitScopeShared},
		{"vk1-mcp", tables.LimitScopeMCP},
		{"vk1-mcp", tables.LimitScopeModel},
		{"vk2-model-only", tables.LimitScopeModel},
		{"vk1-openai", tables.LimitScopeShared},
		{"global-gpt-4o", tables.LimitScopeShared},
	}
	for i, b := range budgets {
		owner := b.owner
		require.NoError(t, db.Create(&tables.TableBudget{ID: owner + "-" + string(b.scope), MaxLimit: float64(i + 1), ResetDuration: "1d", Scope: b.scope, ModelConfigID: &owner}).Error)
	}

	store := &RDBConfigStore{}
	store.db.Store(db)
	rows, total, err := store.GetModelConfigsPaginated(context.Background(), ModelConfigsQueryParams{AppliesToMCP: true, Scopes: []string{"global", "virtual_key"}})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	require.ElementsMatch(t, []string{"global-shared", "vk1-mcp"}, ids)
	for _, r := range rows {
		if r.ID == "vk1-mcp" {
			require.Len(t, r.Budgets, 2, "a listed row carries all its budgets, model-only ones included")
		}
	}
}
