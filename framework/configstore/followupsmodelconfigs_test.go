package configstore

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// followupsMaxVarsRecorder registers query callbacks on db that record the
// largest number of bind parameters any SELECT carried, so a test can pin that
// a read never binds one parameter per id or per result row.
func followupsMaxVarsRecorder(t *testing.T, db *gorm.DB) func() int {
	t.Helper()
	var mu sync.Mutex
	maxVars := 0
	name := fmt.Sprintf("followups:maxvars:%s", t.Name())
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		mu.Lock()
		defer mu.Unlock()
		if n := len(tx.Statement.Vars); n > maxVars {
			maxVars = n
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return maxVars
	}
}

// followupsSeedScopedModelConfigs inserts count virtual_key-scoped model configs,
// perScope per scope id vk-s-<n>, each owning one multi budget (bm-i) and pointing
// at one legacy budget (bl-i) and one rate limit (rl-i). Even-numbered configs are
// calendar aligned.
func followupsSeedScopedModelConfigs(t *testing.T, db *gorm.DB, count, perScope int) {
	t.Helper()
	inListSeedSeries(t, db, count, `INSERT INTO governance_budgets
		(id, max_limit, reset_duration, last_reset, current_usage, override_amount, override_mode, override_cycles_remaining, override_cycles_total, created_at, updated_at)
		SELECT 'bl-' || i, 1, '1d', CURRENT_TIMESTAMP, 0, 0, '', 0, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM n`)
	inListSeedSeries(t, db, count, `INSERT INTO governance_rate_limits
		(id, created_at, updated_at)
		SELECT 'rl-' || i, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM n`)
	inListSeedSeries(t, db, count, fmt.Sprintf(`INSERT INTO governance_model_configs
		(id, model_name, provider, scope, scope_id, calendar_aligned, budget_id, rate_limit_id, config_hash, created_at, updated_at)
		SELECT 'mc-' || i, 'model-' || i, 'openai', 'virtual_key', 'vk-s-' || ((i - 1) / %d), (i %% 2 = 0), 'bl-' || i, 'rl-' || i, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM n`, perScope))
	inListSeedSeries(t, db, count, `INSERT INTO governance_budgets
		(id, max_limit, reset_duration, last_reset, current_usage, override_amount, override_mode, override_cycles_remaining, override_cycles_total, model_config_id, created_at, updated_at)
		SELECT 'bm-' || i, 1, '1M', CURRENT_TIMESTAMP, 0, 0, '', 0, 0, 'mc-' || i, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM n`)
}

// followupsAssertModelConfigs checks that got holds exactly the configs mc-1..count
// with every relation attached and calendar alignment stamped onto owned budgets.
func followupsAssertModelConfigs(t *testing.T, got []tables.TableModelConfig, count int) {
	t.Helper()
	require.Len(t, got, count)
	seen := make(map[string]struct{}, count)
	for _, mc := range got {
		var n int
		_, err := fmt.Sscanf(mc.ID, "mc-%d", &n)
		require.NoError(t, err)
		seen[mc.ID] = struct{}{}
		require.Len(t, mc.Budgets, 1, mc.ID)
		require.Equal(t, fmt.Sprintf("bm-%d", n), mc.Budgets[0].ID)
		require.Equal(t, mc.CalendarAligned, mc.Budgets[0].IsCalendarAligned, mc.ID)
		require.Equal(t, n%2 == 0, mc.CalendarAligned, mc.ID)
		require.NotNil(t, mc.Budget, mc.ID)
		require.Equal(t, fmt.Sprintf("bl-%d", n), mc.Budget.ID)
		require.NotNil(t, mc.RateLimit, mc.ID)
		require.Equal(t, fmt.Sprintf("rl-%d", n), mc.RateLimit.ID)
	}
	require.Len(t, seen, count)
}

// TestFollowupsGetModelConfigsByScopeAndScopeIDsBindsFewParams pins that loading
// the model configs of more scope ids than the bind-parameter limit, whose result
// rows also outnumber that limit, succeeds with every relation attached and never
// binds more than a handful of parameters per statement.
func TestFollowupsGetModelConfigsByScopeAndScopeIDsBindsFewParams(t *testing.T) {
	const scopes = 1000
	const perScope = 40
	const count = scopes * perScope
	for _, ns := range inListLimitStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			followupsSeedScopedModelConfigs(t, ns.db, count, perScope)
			ids := inListSyntheticIDs("missing", inListOverLimit)
			for i := range scopes {
				ids = append(ids, fmt.Sprintf("vk-s-%d", i))
			}
			maxVars := followupsMaxVarsRecorder(t, ns.db)

			got, err := ns.store.GetModelConfigsByScopeAndScopeIDs(context.Background(), tables.ModelConfigScopeVirtualKey, ids)
			require.NoError(t, err)
			followupsAssertModelConfigs(t, got, count)
			require.LessOrEqual(t, maxVars(), 4, "a statement bound one parameter per id or per row")
		})
	}
}

// TestFollowupsGetModelConfigsByScopeAndScopeIDsSubBatches pins that when a
// chunk of scope ids yields more configs than one preload batch, relations are
// still attached to every config, and that the result keeps the order of a
// single unbatched read.
func TestFollowupsGetModelConfigsByScopeAndScopeIDsSubBatches(t *testing.T) {
	restoreChunk, restoreBatch := modelConfigScopeIDChunkSize, modelConfigPreloadBatchSize
	modelConfigScopeIDChunkSize, modelConfigPreloadBatchSize = 3, 7
	t.Cleanup(func() { modelConfigScopeIDChunkSize, modelConfigPreloadBatchSize = restoreChunk, restoreBatch })

	const scopes = 10
	const perScope = 5
	const count = scopes * perScope
	for _, ns := range inListLimitStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			followupsSeedScopedModelConfigs(t, ns.db, count, perScope)
			ids := make([]string, 0, scopes)
			for i := range scopes {
				ids = append(ids, fmt.Sprintf("vk-s-%d", i))
			}
			got, err := ns.store.GetModelConfigsByScopeAndScopeIDs(context.Background(), tables.ModelConfigScopeVirtualKey, ids)
			require.NoError(t, err)
			followupsAssertModelConfigs(t, got, count)

			// A single scope id is one chunk; its rows come back in the same order
			// as the plain read the old implementation issued.
			var want []tables.TableModelConfig
			require.NoError(t, ns.db.Where("scope = ? AND scope_id = ?", tables.ModelConfigScopeVirtualKey, "vk-s-4").Find(&want).Error)
			one, err := ns.store.GetModelConfigsByScopeAndScopeIDs(context.Background(), tables.ModelConfigScopeVirtualKey, []string{"vk-s-4"})
			require.NoError(t, err)
			require.Len(t, one, len(want))
			for i := range want {
				require.Equal(t, want[i].ID, one[i].ID)
			}
		})
	}
}

// TestFollowupsGetModelConfigsByScopeAndScopeIDsDedupesAcrossChunks pins that a
// scope id repeated on both sides of a chunk boundary returns its configs once,
// as the single IN query did before the id list was chunked.
func TestFollowupsGetModelConfigsByScopeAndScopeIDsDedupesAcrossChunks(t *testing.T) {
	restoreChunk := modelConfigScopeIDChunkSize
	modelConfigScopeIDChunkSize = 2
	t.Cleanup(func() { modelConfigScopeIDChunkSize = restoreChunk })

	const scopes = 3
	const perScope = 2
	for _, ns := range inListLimitStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			followupsSeedScopedModelConfigs(t, ns.db, scopes*perScope, perScope)
			// vk-s-0 is at index 0 (first chunk) and index 2 (second chunk).
			ids := []string{"vk-s-0", "vk-s-1", "vk-s-0", "vk-s-2"}
			got, err := ns.store.GetModelConfigsByScopeAndScopeIDs(context.Background(), tables.ModelConfigScopeVirtualKey, ids)
			require.NoError(t, err)
			followupsAssertModelConfigs(t, got, scopes*perScope)
		})
	}
}
