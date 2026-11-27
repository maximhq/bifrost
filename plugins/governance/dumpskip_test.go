package governance

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
	for _, id := range []string{"b1", "b2", "b3"} {
		b := buildBudgetWithUsage(id, 1000, 10, "24h")
		b.CreatedAt, b.UpdatedAt, b.LastReset = now, now, now
		require.NoError(t, inner.CreateBudget(ctx, b))
	}
	for _, id := range []string{"r1", "r2", "r3"} {
		rl := buildRateLimitWithUsage(id, 1_000_000, 5, 1_000, 1)
		rl.TokenResetDuration, rl.RequestResetDuration = &hour, &hour
		rl.TokenLastReset, rl.RequestLastReset = now, now
		require.NoError(t, inner.CreateRateLimit(ctx, rl))
	}

	f := &dumpSkipFixture{configStore: &failingTxConfigStore{ConfigStore: inner}}
	require.NoError(t, inner.DB().Callback().Update().After("gorm:update").Register("dumpskip:count", func(*gorm.DB) {
		f.updates.Add(1)
	}))
	f.store, err = NewLocalGovernanceStore(ctx, logger, f.configStore, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, f.store.LoadBudget(ctx, "b1"))
	require.NotNil(t, f.store.LoadRateLimit(ctx, "r1"))
	return f
}

// take returns and clears the UPDATE counter.
func (f *dumpSkipFixture) take() int64 {
	return f.updates.Swap(0)
}

// bumpRateLimitTokens adds tokens to a rate limit's in-memory counter.
func (f *dumpSkipFixture) bumpRateLimitTokens(t *testing.T, id string, tokens int64) {
	t.Helper()
	rl := f.store.LoadRateLimit(context.Background(), id)
	require.NotNil(t, rl)
	clone := *rl
	clone.TokenCurrentUsage += tokens
	f.store.rateLimits.Store(id, &clone)
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
			require.NoError(t, f.store.DumpRateLimits(ctx, nil, nil))
			require.NoError(t, f.store.DumpBudgets(ctx, nil))
			f.take()

			f.bumpRateLimitTokens(t, "r1", 7)
			require.NoError(t, f.store.BumpBudgetUsage(ctx, "b1", 1.5))

			f.configStore.failNext(tc.err)
			err := f.store.DumpRateLimits(ctx, nil, nil)
			require.Equal(t, tc.wantErr, err != nil, "rate limit dump error: %v", err)
			f.configStore.failNext(tc.err)
			err = f.store.DumpBudgets(ctx, nil)
			require.Equal(t, tc.wantErr, err != nil, "budget dump error: %v", err)
			require.Equal(t, int64(0), f.take(), "the failed cycle wrote nothing")

			// No new usage: the retry must still carry the failed rows.
			require.NoError(t, f.store.DumpRateLimits(ctx, nil, nil))
			require.NoError(t, f.store.DumpBudgets(ctx, nil))
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

	require.NoError(t, f.store.DumpBudgets(ctx, map[string]float64{"b1": 100}))
	require.NoError(t, f.store.DumpRateLimits(ctx, map[string]int64{"r1": 50}, map[string]int64{"r1": 3}))
	f.take()
	b, err := f.configStore.GetBudget(ctx, "b1")
	require.NoError(t, err)
	require.Equal(t, 110.0, b.CurrentUsage)
	rl, err := f.configStore.GetRateLimit(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, int64(55), rl.TokenCurrentUsage)
	require.Equal(t, int64(4), rl.RequestCurrentUsage)

	require.NoError(t, f.store.DumpBudgets(ctx, map[string]float64{"b1": 120}))
	require.NoError(t, f.store.DumpRateLimits(ctx, map[string]int64{"r1": 60}, map[string]int64{"r1": 3}))
	b, err = f.configStore.GetBudget(ctx, "b1")
	require.NoError(t, err)
	require.Equal(t, 130.0, b.CurrentUsage)
}

// TestDumpPersistsResets pins that an in-memory reset is persisted by the next
// dump even though the row was already persisted before it.
func TestDumpPersistsResets(t *testing.T) {
	ctx := context.Background()
	f := newDumpSkipFixture(t)
	require.NoError(t, f.store.DumpBudgets(ctx, nil))
	f.take()

	_, ok := f.store.ResetBudgetUsageInMemory(ctx, "b2")
	require.True(t, ok)
	require.NoError(t, f.store.DumpBudgets(ctx, nil))
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
	require.NoError(t, f.store.DumpRateLimits(ctx, nil, nil))
	require.NoError(t, f.store.DumpBudgets(ctx, nil))
	f.take()

	require.NoError(t, f.configStore.DB().Exec("UPDATE governance_rate_limits SET token_current_usage = 999 WHERE id = ?", "r3").Error)
	require.NoError(t, f.configStore.DB().Exec("UPDATE governance_budgets SET current_usage = 999 WHERE id = ?", "b3").Error)
	require.NoError(t, f.store.DumpRateLimits(ctx, nil, nil))
	require.NoError(t, f.store.DumpBudgets(ctx, nil))

	require.Equal(t, int64(3+6), f.take(), "every dump writes every rate limit (1 statement each) and budget (2 each)")
	rl, err := f.configStore.GetRateLimit(ctx, "r3")
	require.NoError(t, err)
	require.Equal(t, int64(5), rl.TokenCurrentUsage, "the next dump restores this node's rate-limit usage")
	b, err := f.configStore.GetBudget(ctx, "b3")
	require.NoError(t, err)
	require.Equal(t, 10.0, b.CurrentUsage, "the next dump restores this node's budget usage")
}

// TestRateLimitResetPreservesCalendarAdoptedRequests checks that a token-only
// reset persists only the token dimension. Calendar adoption advanced the
// request boundary in memory without clearing usage; writing that boundary in
// the reset batch would zero the persisted request counter.
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
	store.UpsertRateLimitConfig(ctx, rate.ID, rate, false)

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
	require.True(t, persisted.TokenLastReset.Equal(reset.TokenLastReset))
	require.Equal(t, int64(19), persisted.RequestCurrentUsage, "token-only reset zeroed the request counter")
	require.True(t, persisted.RequestLastReset.Equal(old))
}

// TestRateLimitDumpSkipsRowsResetAfterSnapshot pins that a rate limit reset or
// deleted between a dump's snapshot and its write is not written: the row
// carries the usage the reset cleared.
func TestRateLimitDumpSkipsRowsResetAfterSnapshot(t *testing.T) {
	ctx := context.Background()
	f := newDumpSkipFixture(t)
	gens := f.store.rateLimitResetGensSnapshot()
	rows := []rateLimitDumpRow{{ID: "r1"}, {ID: "r2"}, {ID: "r3"}}

	next := f.store.LoadRateLimit(ctx, "r1").TokenLastReset.Add(time.Hour)
	_, ok := f.store.ResetRateLimitAt(ctx, "r1", &next, nil)
	require.True(t, ok)
	f.store.DeleteRateLimit(ctx, "r2")

	kept := f.store.keepUnresetRateLimitRows(rows, gens)
	require.Equal(t, []rateLimitDumpRow{{ID: "r3"}}, kept)
}

// TestRateLimitDumpDoesNotRollBackPersistedReset pins that a dump from an older
// window cannot overwrite a reset that is already persisted, whether this node
// or another one persisted it.
func TestRateLimitDumpDoesNotRollBackPersistedReset(t *testing.T) {
	ctx := context.Background()
	f := newDumpSkipFixture(t)
	old := f.store.LoadRateLimit(ctx, "r1").TokenLastReset
	next := old.Add(time.Hour)
	require.NoError(t, f.configStore.DB().Exec(
		"UPDATE governance_rate_limits SET token_current_usage = 0, token_last_reset = ? WHERE id = ?", next, "r1").Error)

	require.NoError(t, f.store.DumpRateLimits(ctx, nil, nil))
	rl, err := f.configStore.GetRateLimit(ctx, "r1")
	require.NoError(t, err)
	require.Zero(t, rl.TokenCurrentUsage, "the old window's 5 tokens came back")
	require.True(t, rl.TokenLastReset.Equal(next), "the persisted reset boundary rolled back")

	other, err := f.configStore.GetRateLimit(ctx, "r2")
	require.NoError(t, err)
	require.Equal(t, int64(5), other.TokenCurrentUsage, "rows in the current window still persist")
}

// TestDumpWritesRequestsPastNewerTokenWindow pins that the rate-limit pairs are
// guarded independently: when a peer already persisted a newer token window,
// the dump leaves that pair alone but still writes this node's request counter.
func TestDumpWritesRequestsPastNewerTokenWindow(t *testing.T) {
	ctx := context.Background()
	f := newDumpSkipFixture(t)
	require.NoError(t, f.store.DumpRateLimits(ctx, nil, nil))
	local := f.store.LoadRateLimit(ctx, "r1")
	require.NotNil(t, local)

	peerWindow := local.TokenLastReset.Add(time.Hour)
	require.NoError(t, f.configStore.DB().Exec("UPDATE governance_rate_limits SET token_current_usage = 3, token_last_reset = ? WHERE id = ?", peerWindow, "r1").Error)
	clone := *local
	clone.RequestCurrentUsage++
	f.store.rateLimits.Store("r1", &clone)
	require.NoError(t, f.store.DumpRateLimits(ctx, nil, nil))

	rl, err := f.configStore.GetRateLimit(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, int64(2), rl.RequestCurrentUsage, "the request counter was dropped behind a newer token window")
	require.True(t, rl.RequestLastReset.Equal(local.RequestLastReset))
	require.Equal(t, int64(3), rl.TokenCurrentUsage, "a stale token counter overwrote the peer's newer window")
	require.True(t, rl.TokenLastReset.Equal(peerWindow), "the dump rolled the token window back")
}

// TestDumpComparesBoundariesAcrossOffsets pins that the SQLite dump guards
// compare reset boundaries as instants. SQLite keeps a timestamp as text with
// the writer's UTC offset, so a boundary persisted in one offset must still be
// ordered correctly against a snapshot of the same or a nearby instant in UTC.
// Every stored text below sorts the opposite way from the instant it encodes.
func TestDumpComparesBoundariesAcrossOffsets(t *testing.T) {
	ctx := context.Background()
	ist := time.FixedZone("IST", 5*3600+1800)
	pst := time.FixedZone("PST", -8*3600)
	for _, tc := range []struct {
		name        string
		shift       time.Duration
		zone        *time.Location
		wantWritten bool
	}{
		{name: "same instant", shift: 0, zone: ist, wantWritten: true},
		{name: "newer stored", shift: time.Hour, zone: pst, wantWritten: false},
		{name: "older stored", shift: -time.Hour, zone: ist, wantWritten: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDumpSkipFixture(t)
			base := f.store.LoadRateLimit(ctx, "r1").TokenLastReset.UTC()
			stored := base.Add(tc.shift)
			storedText := stored.In(tc.zone).Format("2006-01-02 15:04:05-07:00")
			require.NoError(t, f.configStore.DB().Exec(
				"UPDATE governance_rate_limits SET token_current_usage = 0, token_last_reset = ?, request_current_usage = 0, request_last_reset = ? WHERE id = ?",
				storedText, storedText, "r1").Error)
			require.NoError(t, f.configStore.DB().Exec(
				"UPDATE governance_budgets SET current_usage = 0, last_reset = ? WHERE id = ?", storedText, "b1").Error)

			rl := *f.store.LoadRateLimit(ctx, "r1")
			rl.TokenLastReset, rl.RequestLastReset = base, base
			rl.TokenCurrentUsage, rl.RequestCurrentUsage = 42, 7
			f.store.rateLimits.Store("r1", &rl)
			b := *f.store.LoadBudget(ctx, "b1")
			b.LastReset, b.CurrentUsage = base, 42
			f.store.budgets.Store("b1", &b)

			require.NoError(t, f.store.DumpRateLimits(ctx, nil, nil))
			require.NoError(t, f.store.DumpBudgets(ctx, nil))

			gotRL, err := f.configStore.GetRateLimit(ctx, "r1")
			require.NoError(t, err)
			gotB, err := f.configStore.GetBudget(ctx, "b1")
			require.NoError(t, err)
			if tc.wantWritten {
				require.Equal(t, int64(42), gotRL.TokenCurrentUsage, "token usage was not written")
				require.Equal(t, int64(7), gotRL.RequestCurrentUsage, "request usage was not written")
				require.True(t, gotRL.TokenLastReset.Equal(base))
				require.True(t, gotRL.RequestLastReset.Equal(base))
				require.Equal(t, 42.0, gotB.CurrentUsage, "budget usage was not written")
				require.True(t, gotB.LastReset.Equal(base))
				return
			}
			require.Zero(t, gotRL.TokenCurrentUsage, "an older window's tokens overwrote a newer persisted one")
			require.Zero(t, gotRL.RequestCurrentUsage, "an older window's requests overwrote a newer persisted one")
			require.True(t, gotRL.TokenLastReset.Equal(stored), "the token window rolled back")
			require.True(t, gotRL.RequestLastReset.Equal(stored), "the request window rolled back")
			require.Zero(t, gotB.CurrentUsage, "an older window's spend overwrote a newer persisted one")
			require.True(t, gotB.LastReset.Equal(stored), "the budget window rolled back")
		})
	}
}
