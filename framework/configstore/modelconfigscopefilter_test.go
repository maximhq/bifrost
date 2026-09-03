package configstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
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
