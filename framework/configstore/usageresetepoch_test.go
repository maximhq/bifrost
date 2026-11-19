package configstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// TestUsageResetEpochMigrationLeavesUnknown proves legacy boundaries are not
// promoted into evidence, including nonzero calendar-adoption boundaries.
func TestUsageResetEpochMigrationLeavesUnknown(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.AutoMigrate(&tables.TableBudget{}, &tables.TableRateLimit{}))
	boundary := time.Now().UTC().Truncate(time.Second)
	budget := tables.TableBudget{ID: "legacy-budget", MaxLimit: 100, ResetDuration: "1h", CurrentUsage: 7, LastReset: boundary}
	rate := tables.TableRateLimit{ID: "legacy-rate", TokenCurrentUsage: 9, RequestCurrentUsage: 3, TokenLastReset: boundary, RequestLastReset: boundary}
	require.NoError(t, db.Create(&budget).Error)
	require.NoError(t, db.Create(&rate).Error)
	require.NoError(t, db.Migrator().DropColumn(&tables.TableBudget{}, "UsageResetEpoch"))
	require.NoError(t, db.Migrator().DropColumn(&tables.TableRateLimit{}, "TokenUsageResetEpoch"))
	require.NoError(t, db.Migrator().DropColumn(&tables.TableRateLimit{}, "RequestUsageResetEpoch"))
	require.NoError(t, migrationAddGovernanceUsageResetEpochs(ctx, db, testMigrationLogger))
	require.NoError(t, migrationAddGovernanceUsageResetEpochs(ctx, db, testMigrationLogger))
	require.NoError(t, db.First(&budget, "id = ?", budget.ID).Error)
	require.NoError(t, db.First(&rate, "id = ?", rate.ID).Error)
	require.Zero(t, budget.UsageResetEpoch)
	require.Zero(t, rate.TokenUsageResetEpoch)
	require.Zero(t, rate.RequestUsageResetEpoch)
	require.Equal(t, 7.0, budget.CurrentUsage)
	require.Equal(t, int64(9), rate.TokenCurrentUsage)
	require.Equal(t, int64(3), rate.RequestCurrentUsage)
	require.True(t, budget.LastReset.Equal(boundary))
}

// TestUsageResetEpochConfigUpdatesPreserveRuntime verifies configuration-shaped
// saves cannot erase durable accounting evidence, including unknown zero input.
func TestUsageResetEpochConfigUpdatesPreserveRuntime(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	epoch := time.Now().UnixNano()
	budget := &tables.TableBudget{ID: "budget-reset-evidence", MaxLimit: 100, ResetDuration: "1h", CurrentUsage: 7, LastReset: time.Now(), UsageResetEpoch: epoch}
	rate := &tables.TableRateLimit{ID: "rate-reset-evidence", TokenCurrentUsage: 9, RequestCurrentUsage: 3, TokenUsageResetEpoch: epoch, RequestUsageResetEpoch: epoch - 1}
	require.NoError(t, store.CreateBudget(ctx, budget))
	require.NoError(t, store.CreateRateLimit(ctx, rate))
	require.NoError(t, store.UpdateBudget(ctx, &tables.TableBudget{ID: budget.ID, MaxLimit: 200, ResetDuration: "1h"}))
	require.NoError(t, store.UpdateRateLimit(ctx, &tables.TableRateLimit{ID: rate.ID}))
	loadedBudget, err := store.GetBudget(ctx, budget.ID)
	require.NoError(t, err)
	loadedRate, err := store.GetRateLimit(ctx, rate.ID)
	require.NoError(t, err)
	require.Equal(t, epoch, loadedBudget.UsageResetEpoch)
	require.Equal(t, epoch, loadedRate.TokenUsageResetEpoch)
	require.Equal(t, epoch-1, loadedRate.RequestUsageResetEpoch)
	require.Equal(t, 7.0, loadedBudget.CurrentUsage)
}
