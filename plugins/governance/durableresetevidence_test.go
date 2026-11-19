package governance

import (
	"bytes"
	"context"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"log"
	"testing"
	"time"
)

// TestDurableResetEvidenceRestart proves reset writes survive reconstruction,
// and calendar adoption followed by a dump preserves exact independent markers.
func TestDurableResetEvidenceRestart(t *testing.T) {
	ctx := context.Background()
	fixture := newDumpSkipFixture(t)
	store := fixture.store
	budget := store.LoadBudget(ctx, "b1")
	rate := store.LoadRateLimit(ctx, "r1")
	budgetConfig := *budget
	budgetConfig.ResetDuration = "1d"
	require.NoError(t, fixture.configStore.UpdateBudget(ctx, &budgetConfig))
	store.UpsertBudgetConfig(ctx, budget.ID, &budgetConfig)
	rateConfig := *rate
	day := "1d"
	rateConfig.TokenResetDuration, rateConfig.RequestResetDuration = &day, &day
	require.NoError(t, fixture.configStore.UpdateRateLimit(ctx, &rateConfig))
	store.UpsertRateLimitConfig(ctx, rate.ID, &rateConfig)
	budgetBoundary := budget.LastReset.Add(time.Hour)
	tokenBoundary := rate.TokenLastReset.Add(time.Hour)
	resetBudget, ok := store.ResetBudgetAt(ctx, budget.ID, budgetBoundary)
	require.True(t, ok)
	resetRate, ok := store.ResetRateLimitAt(ctx, rate.ID, &tokenBoundary, nil)
	require.True(t, ok)
	require.NoError(t, store.ResetExpiredBudgets(ctx, []*configstoreTables.TableBudget{resetBudget}))
	require.NoError(t, store.ResetExpiredRateLimits(ctx, []*configstoreTables.TableRateLimit{resetRate}))
	restarted, err := NewLocalGovernanceStore(ctx, NewMockLogger(), fixture.configStore, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, budgetBoundary.UnixNano(), restarted.LoadBudget(ctx, budget.ID).UsageResetEpoch)
	tokenEpoch, requestEpoch := restarted.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch, restarted.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch
	require.Equal(t, tokenBoundary.UnixNano(), tokenEpoch)
	require.Zero(t, requestEpoch)
	require.Zero(t, restarted.LoadBudget(ctx, budget.ID).CurrentUsage)
	require.Zero(t, restarted.LoadRateLimit(ctx, rate.ID).TokenCurrentUsage)
	require.Equal(t, rate.RequestCurrentUsage, restarted.LoadRateLimit(ctx, rate.ID).RequestCurrentUsage)
	require.True(t, restarted.AdoptCalendarAlignmentInMemory(ctx, budget.ID, budgetBoundary.Add(48*time.Hour)))
	require.True(t, restarted.AdoptRateLimitCalendarAlignmentInMemory(ctx, rate.ID, tokenBoundary.Add(48*time.Hour)))
	require.NoError(t, restarted.GetBudgetDumper(ctx, nil)(ctx))
	require.NoError(t, restarted.GetRateLimitDumper(ctx, nil, nil)(ctx))
	afterAdoption, err := NewLocalGovernanceStore(ctx, NewMockLogger(), fixture.configStore, nil, nil, nil)
	require.NoError(t, err)
	require.True(t, afterAdoption.LoadBudget(ctx, budget.ID).LastReset.After(budgetBoundary))
	require.Equal(t, budgetBoundary.UnixNano(), afterAdoption.LoadBudget(ctx, budget.ID).UsageResetEpoch)
	tokenEpoch, requestEpoch = afterAdoption.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch, afterAdoption.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch
	require.Equal(t, tokenBoundary.UnixNano(), tokenEpoch)
	require.Zero(t, requestEpoch)
}

// TestDurableResetEvidenceBaselineInstallation verifies baseline evidence is
// monotonic and only accepted dimensions can install received markers.
func TestDurableResetEvidenceBaselineInstallation(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	boundary := time.Now().UTC().Truncate(time.Second)
	budget := buildBudgetWithUsage("budget", 100, 7, "1h")
	budget.LastReset = boundary
	rate := buildRateLimitWithUsage("rate", 1000, 9, 100, 3)
	rate.TokenLastReset, rate.RequestLastReset = boundary, boundary
	store.budgets.Store(budget.ID, budget)
	store.rateLimits.Store(rate.ID, rate)
	epoch := boundary.UnixNano() - 1
	require.True(t, store.ApplyBudgetBaselineWithResetEpoch(ctx, budget.ID, 5, boundary, epoch))
	require.True(t, store.ApplyBudgetBaselineWithResetEpoch(ctx, budget.ID, 6, boundary, epoch-1))
	require.Equal(t, epoch, store.LoadBudget(ctx, budget.ID).UsageResetEpoch)
	acceptedTokens, acceptedRequests := store.ApplyRateLimitBaselinesWithResetEpochs(ctx, rate.ID, 4, 2, boundary.Add(-time.Hour), boundary, epoch, epoch-2)
	require.False(t, acceptedTokens)
	require.True(t, acceptedRequests)
	require.Zero(t, store.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch)
	require.Equal(t, epoch-2, store.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch)
	acceptedTokens, acceptedRequests = store.ApplyRateLimitBaselinesWithResetEpochs(ctx, rate.ID, 4, 2, boundary, boundary, epoch, 0)
	require.True(t, acceptedTokens)
	require.True(t, acceptedRequests)
	require.Equal(t, epoch, store.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch)
	require.Equal(t, epoch-2, store.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch)
	store.UpsertBudgetConfig(ctx, budget.ID, budget)
	store.UpsertRateLimitConfig(ctx, rate.ID, rate)
	require.Equal(t, epoch, store.LoadBudget(ctx, budget.ID).UsageResetEpoch)
	require.Equal(t, epoch, store.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch)
	require.Equal(t, epoch-2, store.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch)
}

// TestDurableResetEvidenceStaleDumpDimensions verifies stale snapshots cannot
// lower persisted evidence or overwrite one dimension when the other advances.
func TestDurableResetEvidenceStaleDumpDimensions(t *testing.T) {
	ctx := context.Background()
	fixture := newDumpSkipFixture(t)
	store := fixture.store
	budget := store.LoadBudget(ctx, "b1")
	rate := store.LoadRateLimit(ctx, "r1")
	target := budget.LastReset.Add(time.Hour)
	resetBudget, ok := store.ResetBudgetAt(ctx, budget.ID, target)
	require.True(t, ok)
	require.NoError(t, store.ResetExpiredBudgets(ctx, []*configstoreTables.TableBudget{resetBudget}))
	require.NoError(t, store.writeBudgetBatch(ctx, fixture.configStore.DB(), []budgetDumpRow{{ID: budget.ID, CurrentUsage: 99, LastReset: target}}, "<="))
	loadedBudget, err := fixture.configStore.GetBudget(ctx, budget.ID)
	require.NoError(t, err)
	require.Zero(t, loadedBudget.CurrentUsage)
	require.Equal(t, target.UnixNano(), loadedBudget.UsageResetEpoch)
	resetRate, ok := store.ResetRateLimitAt(ctx, rate.ID, &target, nil)
	require.True(t, ok)
	require.NoError(t, store.ResetExpiredRateLimits(ctx, []*configstoreTables.TableRateLimit{resetRate}))
	require.NoError(t, store.dumpRateLimitBatch(ctx, fixture.configStore.DB(), []rateLimitDumpRow{{
		ID: rate.ID, TokenCurrentUsage: 99, TokenLastReset: target,
		RequestCurrentUsage: 8, RequestLastReset: target, RequestUsageResetEpoch: target.UnixNano(),
	}}))
	loadedRate, err := fixture.configStore.GetRateLimit(ctx, rate.ID)
	require.NoError(t, err)
	require.Zero(t, loadedRate.TokenCurrentUsage)
	require.Equal(t, target.UnixNano(), loadedRate.TokenUsageResetEpoch)
	require.Equal(t, int64(8), loadedRate.RequestCurrentUsage)
	require.Equal(t, target.UnixNano(), loadedRate.RequestUsageResetEpoch)
}

// TestDurableResetPostgresSQL verifies generated PostgreSQL batch statements
// retain exact BIGINT evidence and dimension guards without opening a connection.
// It checks SQL emission, not execution against a PostgreSQL server.
func TestDurableResetPostgresSQL(t *testing.T) {
	var output bytes.Buffer
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 dbname=unused sslmode=disable"}), &gorm.Config{
		DryRun: true, DisableAutomaticPing: true,
		Logger: gormlogger.New(log.New(&output, "", 0), gormlogger.Config{LogLevel: gormlogger.Info}),
	})
	require.NoError(t, err)
	store := newStandaloneStoreForResetBenchmark()
	ctx := context.Background()
	boundary := time.Unix(1793975176, 42292000).UTC()
	epoch := boundary.UnixNano()
	require.NoError(t, store.writeBudgetBatch(ctx, db, []budgetDumpRow{{ID: "budget", CurrentUsage: 4, LastReset: boundary, UsageResetEpoch: epoch}}, "<="))
	require.NoError(t, store.dumpRateLimitBatch(ctx, db, []rateLimitDumpRow{{ID: "rate", TokenCurrentUsage: 9, TokenLastReset: boundary, TokenUsageResetEpoch: epoch, RequestCurrentUsage: 2, RequestLastReset: boundary, RequestUsageResetEpoch: epoch - 1}}))
	require.NoError(t, store.resetRateLimitDimensionBatch(ctx, db, "token_current_usage", "token_last_reset", []rateLimitBoundaryRow{{ID: "rate", Boundary: boundary, UsageResetEpoch: epoch}}))
	statements := output.String()
	require.Contains(t, statements, "1793975176042292000")
	require.Contains(t, statements, "1793975176042291999")
	require.Contains(t, statements, "usage_reset_epoch = v.ure")
	require.Contains(t, statements, "t.usage_reset_epoch <= v.ure")
	require.Contains(t, statements, "t.token_usage_reset_epoch <= v.tue")
	require.Contains(t, statements, "t.request_usage_reset_epoch <= v.rue")
	require.Contains(t, statements, "token_usage_reset_epoch = v.e")
	require.Contains(t, statements, "t.token_usage_reset_epoch <= v.e")
	require.Contains(t, statements, "::bigint")
}
