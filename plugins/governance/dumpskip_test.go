package governance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// failingTxConfigStore wraps a real store so a test can make the next dump
// transaction fail, either with a plain error or with a deadlock.
type failingTxConfigStore struct {
	configstore.ConfigStore

	mu      sync.Mutex
	failErr error
}

// failNext makes the next ExecuteTransaction return err without running fn.
func (s *failingTxConfigStore) failNext(err error) {
	s.mu.Lock()
	s.failErr = err
	s.mu.Unlock()
}

// ExecuteTransaction fails once when armed, otherwise delegates.
func (s *failingTxConfigStore) ExecuteTransaction(ctx context.Context, fn func(tx *gorm.DB) error) error {
	s.mu.Lock()
	err := s.failErr
	s.failErr = nil
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.ConfigStore.ExecuteTransaction(ctx, fn)
}

// dumpSkipFixture is a SQLite-backed store seeded with three budgets and three
// rate limits, counting every UPDATE statement the dump issues.
type dumpSkipFixture struct {
	store       *LocalGovernanceStore
	configStore *failingTxConfigStore
	updates     atomic.Int64
}

// newDumpSkipFixture builds the fixture and registers the UPDATE counter.
func newDumpSkipFixture(t *testing.T) *dumpSkipFixture {
	t.Helper()
	ctx := context.Background()
	logger := NewMockLogger()
	inner, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/dumpskip.db"},
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, inner.Close(ctx)) })

	now := time.Now().UTC().Truncate(time.Second)
	hour := "1h"
	// The live store loads the limits something holds, so key i holds budget i and rate limit i.
	var holders []configstoreTables.TableVirtualKey
	for i, id := range []string{"b1", "b2", "b3"} {
		b := buildBudgetWithUsage(id, 1000, 10, "24h")
		b.CreatedAt, b.UpdatedAt, b.LastReset = now, now, now
		holders = append(holders, *buildVirtualKeyWithMultiBudgets(fmt.Sprintf("vk-%d", i+1), fmt.Sprintf("sk-bf-dumpskip-%d", i+1), fmt.Sprintf("dumpskip %d", i+1), []configstoreTables.TableBudget{*b}))
	}
	for i, id := range []string{"r1", "r2", "r3"} {
		rl := buildRateLimitWithUsage(id, 1_000_000, 5, 1_000, 1)
		rl.TokenResetDuration, rl.RequestResetDuration = &hour, &hour
		rl.TokenLastReset, rl.RequestLastReset = now, now
		require.NoError(t, inner.CreateRateLimit(ctx, rl))
		rateLimitID := id
		holders[i].RateLimitID = &rateLimitID
	}
	h, err := openLiveHarness(t, inner, &configstore.GovernanceConfig{VirtualKeys: holders}, nil)
	require.NoError(t, err)

	f := &dumpSkipFixture{configStore: &failingTxConfigStore{ConfigStore: inner}}
	require.NoError(t, inner.DB().Callback().Update().After("gorm:update").Register("dumpskip:count", func(*gorm.DB) {
		f.updates.Add(1)
	}))
	f.store, err = NewLocalGovernanceStore(ctx, logger, f.configStore, h.live, nil)
	require.NoError(t, err)
	t.Cleanup(f.store.Close)
	require.NotNil(t, f.store.LoadBudget(ctx, "b1"))
	require.NotNil(t, f.store.LoadRateLimit(ctx, "r1"))
	return f
}

// take returns and clears the UPDATE counter.
func (f *dumpSkipFixture) take() int64 {
	return f.updates.Swap(0)
}

// bumpRateLimitTokens adds tokens to a rate limit's in-memory counter only, so
// the database row keeps its old value until a dump writes the new one.
func (f *dumpSkipFixture) bumpRateLimitTokens(t *testing.T, id string, tokens int64) {
	t.Helper()
	require.NotNil(t, f.store.LoadRateLimit(context.Background(), id))
	require.NoError(t, f.store.BumpRateLimitUsageBy(context.Background(), id, tokens, 0))
}

// TestDumpRetriesFailedWrites pins that a failed or deadlocked dump loses no
// usage: the next cycle writes every row again, including the ones that failed.
func TestDumpRetriesFailedWrites(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		err     error
		wantErr bool
	}{
		{name: "error", err: errors.New("connection reset"), wantErr: true},
		{name: "deadlock", err: errors.New("ERROR: deadlock detected (SQLSTATE 40P01)"), wantErr: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDumpSkipFixture(t)
			require.NoError(t, f.store.GetRateLimitDumper(ctx, nil, nil)(ctx))
			require.NoError(t, f.store.GetBudgetDumper(ctx, nil)(ctx))
			f.take()

			f.bumpRateLimitTokens(t, "r1", 7)
			require.NoError(t, f.store.BumpBudgetUsage(ctx, "b1", 1.5))

			f.configStore.failNext(tc.err)
			err := f.store.GetRateLimitDumper(ctx, nil, nil)(ctx)
			require.Equal(t, tc.wantErr, err != nil, "rate limit dump error: %v", err)
			f.configStore.failNext(tc.err)
			err = f.store.GetBudgetDumper(ctx, nil)(ctx)
			require.Equal(t, tc.wantErr, err != nil, "budget dump error: %v", err)
			require.Equal(t, int64(0), f.take(), "the failed cycle wrote nothing")

			// No new usage: the retry must still carry the failed rows.
			require.NoError(t, f.store.GetRateLimitDumper(ctx, nil, nil)(ctx))
			require.NoError(t, f.store.GetBudgetDumper(ctx, nil)(ctx))
			require.Equal(t, int64(3+6), f.take(), "the retry writes every rate limit and budget, including the ones that failed")
			rl, err := f.configStore.GetRateLimit(ctx, "r1")
			require.NoError(t, err)
			require.Equal(t, int64(12), rl.TokenCurrentUsage)
			b, err := f.configStore.GetBudget(ctx, "b1")
			require.NoError(t, err)
			require.Equal(t, 11.5, b.CurrentUsage)
		})
	}
}

// TestDumpAppliesBaselines pins that the value written is local usage plus the
// gossip baseline, so a baseline that moves without local traffic is still
// persisted.
func TestDumpAppliesBaselines(t *testing.T) {
	ctx := context.Background()
	f := newDumpSkipFixture(t)

	require.NoError(t, f.store.GetBudgetDumper(ctx, map[string]float64{"b1": 100})(ctx))
	require.NoError(t, f.store.GetRateLimitDumper(ctx, map[string]int64{"r1": 50}, map[string]int64{"r1": 3})(ctx))
	f.take()
	b, err := f.configStore.GetBudget(ctx, "b1")
	require.NoError(t, err)
	require.Equal(t, 110.0, b.CurrentUsage)
	rl, err := f.configStore.GetRateLimit(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, int64(55), rl.TokenCurrentUsage)
	require.Equal(t, int64(4), rl.RequestCurrentUsage)

	require.NoError(t, f.store.GetBudgetDumper(ctx, map[string]float64{"b1": 120})(ctx))
	require.NoError(t, f.store.GetRateLimitDumper(ctx, map[string]int64{"r1": 60}, map[string]int64{"r1": 3})(ctx))
	b, err = f.configStore.GetBudget(ctx, "b1")
	require.NoError(t, err)
	require.Equal(t, 130.0, b.CurrentUsage)
}

// TestDumpPersistsResets pins that an in-memory reset is persisted by the next
// dump even though the row was already persisted before it.
func TestDumpPersistsResets(t *testing.T) {
	ctx := context.Background()
	f := newDumpSkipFixture(t)
	require.NoError(t, f.store.GetBudgetDumper(ctx, nil)(ctx))
	f.take()

	_, ok := f.store.ResetBudgetUsageInMemory(ctx, "b2")
	require.True(t, ok)
	require.NoError(t, f.store.GetBudgetDumper(ctx, nil)(ctx))
	require.Equal(t, int64(6), f.take(), "every budget is written, including the reset one")
	b, err := f.configStore.GetBudget(ctx, "b2")
	require.NoError(t, err)
	require.Zero(t, b.CurrentUsage)
}

// TestDumpRewritesEveryRowEachCycle pins dev's dump contract: every dump writes
// every budget and rate limit, so a row another writer changed (a peer, an
// operator edit, a restore) is overwritten with this node's in-memory value on
// the very next cycle, not minutes later.
func TestDumpRewritesEveryRowEachCycle(t *testing.T) {
	ctx := context.Background()
	f := newDumpSkipFixture(t)
	require.NoError(t, f.store.GetRateLimitDumper(ctx, nil, nil)(ctx))
	require.NoError(t, f.store.GetBudgetDumper(ctx, nil)(ctx))
	f.take()

	require.NoError(t, f.configStore.DB().Exec("UPDATE governance_rate_limits SET token_current_usage = 999 WHERE id = ?", "r3").Error)
	require.NoError(t, f.configStore.DB().Exec("UPDATE governance_budgets SET current_usage = 999 WHERE id = ?", "b3").Error)
	require.NoError(t, f.store.GetRateLimitDumper(ctx, nil, nil)(ctx))
	require.NoError(t, f.store.GetBudgetDumper(ctx, nil)(ctx))

	require.Equal(t, int64(3+6), f.take(), "every dump writes every rate limit (1 statement each) and budget (2 each)")
	rl, err := f.configStore.GetRateLimit(ctx, "r3")
	require.NoError(t, err)
	require.Equal(t, int64(5), rl.TokenCurrentUsage, "the next dump restores this node's rate-limit usage")
	b, err := f.configStore.GetBudget(ctx, "b3")
	require.NoError(t, err)
	require.Equal(t, 10.0, b.CurrentUsage, "the next dump restores this node's budget usage")
}

// TestDumpDefersWritesAndRetainsCapturedUsage ensures transfers after capture
// cannot change a dump and database work starts only when its writer is invoked.
func TestDumpDefersWritesAndRetainsCapturedUsage(t *testing.T) {
	ctx := context.Background()
	f := newDumpSkipFixture(t)
	f.take()
	beforeBudget, err := f.configStore.GetBudget(ctx, "b1")
	require.NoError(t, err)
	beforeRate, err := f.configStore.GetRateLimit(ctx, "r1")
	require.NoError(t, err)
	budgetBaselines := map[string]float64{"b1": 100}
	tokenBaselines := map[string]int64{"r1": 50}
	requestBaselines := map[string]int64{"r1": 3}
	budgetDumper := f.store.GetBudgetDumper(ctx, budgetBaselines)
	rateLimitDumper := f.store.GetRateLimitDumper(ctx, tokenBaselines, requestBaselines)
	require.Zero(t, f.take(), "capture must not write to the database")
	budget, err := f.configStore.GetBudget(ctx, "b1")
	require.NoError(t, err)
	require.Equal(t, beforeBudget.CurrentUsage, budget.CurrentUsage)
	rateLimit, err := f.configStore.GetRateLimit(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, beforeRate.TokenCurrentUsage, rateLimit.TokenCurrentUsage)
	require.Equal(t, beforeRate.RequestCurrentUsage, rateLimit.RequestCurrentUsage)

	budgetBaselines["b1"] = 200
	tokenBaselines["r1"] = 100
	requestBaselines["r1"] = 6
	require.NoError(t, f.store.BumpBudgetUsage(ctx, "b1", 7))
	f.bumpRateLimitTokens(t, "r1", 9)
	require.NoError(t, budgetDumper(ctx))
	require.NoError(t, rateLimitDumper(ctx))
	budget, err = f.configStore.GetBudget(ctx, "b1")
	require.NoError(t, err)
	require.Equal(t, 110.0, budget.CurrentUsage)
	rateLimit, err = f.configStore.GetRateLimit(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, int64(55), rateLimit.TokenCurrentUsage)
	require.Equal(t, int64(4), rateLimit.RequestCurrentUsage)
}

// TestDumpResetGenerationIgnoresCharges keeps ordinary request traffic from
// invalidating dumps while detecting operator resets at an unchanged boundary.
func TestDumpResetGenerationIgnoresCharges(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStore(t)
	boundary := time.Now().UTC().Truncate(time.Hour)
	budget := buildBudgetWithUsage("budget", 100, 3, "24h")
	budget.LastReset = boundary
	reloadBudget(t, store, budget.ID, budget)
	rate := buildRateLimit("rate", 1000, 1000)
	rate.TokenLastReset, rate.RequestLastReset = boundary, boundary
	reloadRateLimit(t, store, rate.ID, rate)
	unchanged := store.CaptureDumpResetGeneration()
	require.True(t, unchanged())
	require.NoError(t, store.BumpBudgetUsageBy(ctx, budget.ID, 2))
	require.NoError(t, store.BumpRateLimitUsageBy(ctx, rate.ID, 5, 7))
	require.True(t, unchanged())
	reset, ok := store.ResetBudgetUsageInMemory(ctx, budget.ID)
	require.True(t, ok)
	require.True(t, reset.LastReset.Equal(boundary))
	require.False(t, unchanged())
	require.True(t, store.CaptureDumpResetGeneration()())

	epoch := boundary.UnixNano()
	unchanged = store.CaptureDumpResetGeneration()
	require.True(t, store.ApplyBudgetBaselineWithResetEpoch(ctx, budget.ID, 0, boundary, epoch))
	require.False(t, unchanged())

	unchanged = store.CaptureDumpResetGeneration()
	tokens, requests := store.ApplyRateLimitBaselinesWithResetEpochs(ctx, rate.ID, 0, 0, boundary, boundary, epoch, 0)
	require.True(t, tokens && requests)
	require.False(t, unchanged())

	unchanged = store.CaptureDumpResetGeneration()
	tokens, requests = store.ApplyRateLimitBaselinesWithResetEpochs(ctx, rate.ID, 0, 0, boundary, boundary, epoch, epoch)
	require.True(t, tokens && requests)
	require.False(t, unchanged())
}

// TestDumpResetGenerationResourceRemoval rejects a capture whose resource was
// removed and avoids treating a later replacement as the original resource.
func TestDumpResetGenerationResourceRemoval(t *testing.T) {
	for _, resource := range []string{"budget", "rate"} {
		t.Run(resource, func(t *testing.T) {
			store := newStandaloneStore(t)
			budget := buildBudgetWithUsage("budget", 100, 3, "24h")
			reloadBudget(t, store, budget.ID, budget)
			rate := buildRateLimit("rate", 1000, 1000)
			// Zero window starts read back the same from every read of the row, so the replacement's
			// boundaries match the original's and only its removal tells them apart.
			rate.TokenLastReset, rate.RequestLastReset = time.Time{}, time.Time{}
			reloadRateLimit(t, store, rate.ID, rate)
			unchanged := store.CaptureDumpResetGeneration()
			if resource == "budget" {
				dropLimit(t, store, livestore.KindBudget, budget.ID)
			} else {
				dropLimit(t, store, livestore.KindRateLimit, rate.ID)
			}
			require.False(t, unchanged())
			if resource == "budget" {
				reloadBudget(t, store, budget.ID, budget)
			} else {
				reloadRateLimit(t, store, rate.ID, rate)
			}
			require.False(t, unchanged())
		})
	}
}

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
	refreshLimit(t, store, livestore.KindBudget, budget.ID, false)
	rateConfig := *rate
	day := "1d"
	rateConfig.TokenResetDuration, rateConfig.RequestResetDuration = &day, &day
	require.NoError(t, fixture.configStore.UpdateRateLimit(ctx, &rateConfig))
	refreshLimit(t, store, livestore.KindRateLimit, rate.ID, false)
	budgetBoundary := budget.LastReset.Add(time.Hour)
	tokenBoundary := rate.TokenLastReset.Add(time.Hour)
	resetBudget, ok := store.ResetBudgetAt(ctx, budget.ID, budgetBoundary)
	require.True(t, ok)
	resetRate, ok := store.ResetRateLimitAt(ctx, rate.ID, &tokenBoundary, nil)
	require.True(t, ok)
	require.NoError(t, store.ResetExpiredBudgets(ctx, []*configstoreTables.TableBudget{resetBudget}))
	require.NoError(t, store.ResetExpiredRateLimits(ctx, []*RateLimitReset{resetRate}))
	restarted := restartedStore(t, ctx, fixture.configStore)
	require.Equal(t, budgetBoundary.UnixNano(), restarted.LoadBudget(ctx, budget.ID).UsageResetEpoch)
	tokenEpoch, requestEpoch := restarted.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch, restarted.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch
	require.Equal(t, tokenBoundary.UnixNano(), tokenEpoch)
	require.Zero(t, requestEpoch)
	require.Zero(t, restarted.LoadBudget(ctx, budget.ID).CurrentUsage)
	require.Zero(t, restarted.LoadRateLimit(ctx, rate.ID).TokenCurrentUsage)
	require.Equal(t, rate.RequestCurrentUsage, restarted.LoadRateLimit(ctx, rate.ID).RequestCurrentUsage)
	require.True(t, restarted.AdoptCalendarAlignmentInMemory(ctx, budget.ID, budgetBoundary.Add(48*time.Hour)))
	require.True(t, restarted.AdoptRateLimitCalendarAlignmentInMemory(ctx, rate.ID, tokenBoundary.Add(48*time.Hour)))
	adoptedRate := restarted.LoadRateLimit(ctx, rate.ID)
	require.NoError(t, restarted.GetBudgetDumper(ctx, nil)(ctx))
	require.NoError(t, restarted.GetRateLimitDumper(ctx, nil, nil)(ctx))
	afterAdoption := restartedStore(t, ctx, fixture.configStore)
	require.True(t, afterAdoption.LoadBudget(ctx, budget.ID).LastReset.After(budgetBoundary))
	require.Equal(t, budgetBoundary.UnixNano(), afterAdoption.LoadBudget(ctx, budget.ID).UsageResetEpoch)
	require.True(t, afterAdoption.LoadRateLimit(ctx, rate.ID).TokenLastReset.Equal(adoptedRate.TokenLastReset))
	require.True(t, afterAdoption.LoadRateLimit(ctx, rate.ID).RequestLastReset.Equal(adoptedRate.RequestLastReset))
	tokenEpoch, requestEpoch = afterAdoption.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch, afterAdoption.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch
	require.Equal(t, tokenBoundary.UnixNano(), tokenEpoch)
	require.Zero(t, requestEpoch)
}

// restartedStore opens a governance store over a live store freshly loaded from configStore's
// database, the way a restarted node starts from what the previous one persisted.
func restartedStore(t *testing.T, ctx context.Context, configStore configstore.ConfigStore) *LocalGovernanceStore {
	t.Helper()
	h, err := openLiveHarness(t, configStore, nil, nil)
	require.NoError(t, err)
	store, err := NewLocalGovernanceStore(ctx, NewMockLogger(), configStore, h.live, nil)
	require.NoError(t, err)
	t.Cleanup(store.Close)
	return store
}

// TestDurableResetEvidenceBaselineInstallation verifies baseline evidence is
// monotonic and only accepted dimensions can install received markers.
func TestDurableResetEvidenceBaselineInstallation(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStore(t)
	boundary := time.Now().UTC().Truncate(time.Second)
	budget := buildBudgetWithUsage("budget", 100, 7, "1h")
	budget.LastReset = boundary
	rate := buildRateLimitWithUsage("rate", 1000, 9, 100, 3)
	rate.TokenLastReset, rate.RequestLastReset = boundary, boundary
	putBudget(t, store, budget.ID, budget)
	putRateLimit(t, store, rate.ID, rate)
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
	reloadBudget(t, store, budget.ID, budget)
	reloadRateLimit(t, store, rate.ID, rate)
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
	require.NoError(t, store.ResetExpiredRateLimits(ctx, []*RateLimitReset{resetRate}))
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
	store := newStandaloneStore(t)
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
	require.Contains(t, statements, "THEN v.tue ELSE t.token_usage_reset_epoch END")
	require.Contains(t, statements, "THEN v.rue ELSE t.request_usage_reset_epoch END")
	require.Contains(t, statements, "t.token_usage_reset_epoch <= v.tue")
	require.Contains(t, statements, "t.request_usage_reset_epoch <= v.rue")
	require.Contains(t, statements, "token_usage_reset_epoch = v.e")
	require.Contains(t, statements, "t.token_usage_reset_epoch <= v.e")
	require.Contains(t, statements, "::bigint")
}

// TestRateLimitResetPreservesCalendarAdoptedRequests checks that a token reset
// leaves the adopted request boundary and usage for the ordinary dump.
func TestRateLimitResetPreservesCalendarAdoptedRequests(t *testing.T) {
	ctx := context.Background()
	fixture := newDumpSkipFixture(t)
	store := fixture.store
	adoption := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	old := adoption.Add(-24 * time.Hour)
	hour, day := "1h", "1d"
	rate := buildRateLimitWithUsage("adopted-requests", 1000, 17, 100, 19)
	rate.CreatedAt, rate.TokenLastReset, rate.RequestLastReset = old, old, old
	rate.TokenResetDuration, rate.RequestResetDuration = &hour, &day
	require.NoError(t, fixture.configStore.CreateRateLimit(ctx, rate))
	// Adoption follows its holder switching calendar alignment on, and the holder's flag is what
	// makes the rate limit calendar aligned in the store.
	recordCalendarFlag(t, store, livestore.KindRateLimit, rate.ID, true)
	refreshLimit(t, store, livestore.KindRateLimit, rate.ID, false)

	require.True(t, store.AdoptRateLimitCalendarAlignmentInMemory(ctx, rate.ID, adoption))
	adopted := store.LoadRateLimit(ctx, rate.ID)
	require.True(t, adopted.RequestLastReset.After(old))
	require.Equal(t, int64(19), adopted.RequestCurrentUsage)
	reset := store.resetExpiredRateLimitFromSnapshot(ctx, adopted, adoption.Add(time.Hour))
	require.NotNil(t, reset)
	require.Zero(t, reset.TokenCurrentUsage)
	require.Equal(t, int64(19), reset.RequestCurrentUsage)
	require.NoError(t, store.ResetExpiredRateLimits(ctx, []*RateLimitReset{reset}))

	persisted, err := fixture.configStore.GetRateLimit(ctx, rate.ID)
	require.NoError(t, err)
	require.Zero(t, persisted.TokenCurrentUsage)
	require.Equal(t, reset.TokenUsageResetEpoch, persisted.TokenUsageResetEpoch)
	require.Equal(t, int64(19), persisted.RequestCurrentUsage)
	require.True(t, persisted.RequestLastReset.Equal(old))
	require.Zero(t, persisted.RequestUsageResetEpoch)

	// A dump can persist post-reset traffic before the reset write is retried.
	fixture.bumpRateLimitTokens(t, rate.ID, 7)
	require.NoError(t, store.GetRateLimitDumper(ctx, nil, nil)(ctx))
	require.NoError(t, store.ResetExpiredRateLimits(ctx, []*RateLimitReset{reset}))
	persisted, err = fixture.configStore.GetRateLimit(ctx, rate.ID)
	require.NoError(t, err)
	require.Equal(t, int64(19), persisted.RequestCurrentUsage)
	require.Equal(t, int64(7), persisted.TokenCurrentUsage)
	require.True(t, persisted.RequestLastReset.Equal(adopted.RequestLastReset))
	require.Zero(t, persisted.RequestUsageResetEpoch)
}
