package configstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupSuggestionScheduleStore(t *testing.T) *RDBConfigStore {
	store := setupRDBTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(&tables.TableComplexitySuggestionSchedule{}))
	// An in-memory SQLite database is per connection: one connection keeps
	// every claim on the same table. The version check is what is under test.
	sqlDB, err := store.DB().DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	return store
}

// A router has no schedule state until it is set; setting it again (the
// schedule changed) moves the next run and bumps the version.
func TestComplexitySuggestionScheduleSetAndGet(t *testing.T) {
	store := setupSuggestionScheduleStore(t)
	ctx := context.Background()
	row, err := store.GetComplexitySuggestionSchedule(ctx, "default")
	require.NoError(t, err)
	assert.Nil(t, row)

	first := time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC)
	set, err := store.SetComplexitySuggestionSchedule(ctx, "default", "hash-1", first, nil)
	require.NoError(t, err)
	require.True(t, set)
	row, err = store.GetComplexitySuggestionSchedule(ctx, "default")
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.True(t, row.NextRunAt.Equal(first))
	assert.Equal(t, "hash-1", row.ScheduleHash)
	assert.Nil(t, row.LastRunAt)

	moved := first.Add(6 * time.Hour)
	set, err = store.SetComplexitySuggestionSchedule(ctx, "default", "hash-2", moved, &row.Version)
	require.NoError(t, err)
	require.True(t, set)
	again, err := store.GetComplexitySuggestionSchedule(ctx, "default")
	require.NoError(t, err)
	assert.True(t, again.NextRunAt.Equal(moved))
	assert.Equal(t, "hash-2", again.ScheduleHash)
	assert.Greater(t, again.Version, row.Version, "a schedule change invalidates claims read before it")
}

// Of nodes that read the same due run, exactly one claims it; the claim moves
// the next run on and records the slot's run, and a claim read before a
// schedule change no longer applies. Production config stores are Postgres,
// where the upsert and the claim are separate SQL, so the test runs there too
// when the local test Postgres is reachable.
func TestComplexitySuggestionScheduleClaimIsExclusive(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { testComplexitySuggestionScheduleClaim(t, setupSuggestionScheduleStore(t)) })
	t.Run("postgres", func(t *testing.T) {
		store := trySetupPostgresSchemaStore(t, "configstore_suggestion_schedules_test", &tables.TableComplexitySuggestionSchedule{})
		if store == nil {
			t.Skip("local test Postgres is not reachable")
		}
		testComplexitySuggestionScheduleClaim(t, store)
	})
}

func testComplexitySuggestionScheduleClaim(t *testing.T, store *RDBConfigStore) {
	ctx := context.Background()
	due := time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC)
	next := due.Add(24 * time.Hour)
	set, err := store.SetComplexitySuggestionSchedule(ctx, "default", "hash", due, nil)
	require.NoError(t, err)
	require.True(t, set)
	row, err := store.GetComplexitySuggestionSchedule(ctx, "default")
	require.NoError(t, err)

	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := range 8 {
		wg.Go(func() {
			ok, err := store.ClaimComplexitySuggestionRun(ctx, "default", row.Version, row.NextRunAt, next, "run-"+string(rune('a'+i)))
			assert.NoError(t, err)
			if ok {
				mu.Lock()
				won++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	assert.Equal(t, 1, won, "exactly one node takes a due run")

	after, err := store.GetComplexitySuggestionSchedule(ctx, "default")
	require.NoError(t, err)
	assert.True(t, after.NextRunAt.Equal(next))
	require.NotNil(t, after.LastRunAt)
	assert.True(t, after.LastRunAt.Equal(due))
	assert.NotEmpty(t, after.LastRunID)

	// A claim from a row read before the schedule changed is refused.
	set, err = store.SetComplexitySuggestionSchedule(ctx, "default", "hash-2", next.Add(time.Hour), &after.Version)
	require.NoError(t, err)
	require.True(t, set)
	ok, err := store.ClaimComplexitySuggestionRun(ctx, "default", after.Version, after.NextRunAt, next.Add(48*time.Hour), "late")
	require.NoError(t, err)
	assert.False(t, ok)

	// Routers' rows are independent.
	ok, err = store.ClaimComplexitySuggestionRun(ctx, "other", 0, due, next, "run-x")
	require.NoError(t, err)
	assert.False(t, ok, "no row, nothing to claim")
}

// A node's setter that lands after another node has already set the row and
// claimed its due slot must not put that slot back: the claimed run would be
// due again and run twice. Both ways a setter runs are covered: no row yet,
// and a schedule change.
func TestComplexitySuggestionScheduleDelayedSetterCannotUndoAClaim(t *testing.T) {
	store := setupSuggestionScheduleStore(t)
	ctx := context.Background()
	due := time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC)
	next := due.Add(24 * time.Hour)

	// Both nodes found no row. B sets it first and claims the slot once due.
	set, err := store.SetComplexitySuggestionSchedule(ctx, "default", "hash", due, nil)
	require.NoError(t, err)
	require.True(t, set)
	row, err := store.GetComplexitySuggestionSchedule(ctx, "default")
	require.NoError(t, err)
	ok, err := store.ClaimComplexitySuggestionRun(ctx, "default", row.Version, row.NextRunAt, next, "run-b")
	require.NoError(t, err)
	require.True(t, ok)

	// A's setter, computed before the slot was due, lands now and loses.
	set, err = store.SetComplexitySuggestionSchedule(ctx, "default", "hash", due, nil)
	require.NoError(t, err)
	assert.False(t, set, "a row written since A read none is not overwritten")
	after, err := store.GetComplexitySuggestionSchedule(ctx, "default")
	require.NoError(t, err)
	assert.True(t, after.NextRunAt.Equal(next), "the claimed slot stays claimed: next run %s", after.NextRunAt)
	assert.Equal(t, "run-b", after.LastRunID)

	// The schedule changed: both nodes read the same row. B sets the new
	// schedule and claims its slot; A's setter from the same read loses.
	changedDue := next.Add(time.Hour)
	read := after.Version
	set, err = store.SetComplexitySuggestionSchedule(ctx, "default", "hash-2", changedDue, &read)
	require.NoError(t, err)
	require.True(t, set)
	row, err = store.GetComplexitySuggestionSchedule(ctx, "default")
	require.NoError(t, err)
	ok, err = store.ClaimComplexitySuggestionRun(ctx, "default", row.Version, row.NextRunAt, changedDue.Add(24*time.Hour), "run-b2")
	require.NoError(t, err)
	require.True(t, ok)
	set, err = store.SetComplexitySuggestionSchedule(ctx, "default", "hash-2", changedDue, &read)
	require.NoError(t, err)
	assert.False(t, set, "a setter from a stale read is refused")
	final, err := store.GetComplexitySuggestionSchedule(ctx, "default")
	require.NoError(t, err)
	assert.True(t, final.NextRunAt.Equal(changedDue.Add(24*time.Hour)))
	assert.Equal(t, "run-b2", final.LastRunID)
}

// The migration creates the table on a store that predates it, and running it
// again is a no-op.
func TestMigrationAddComplexitySuggestionSchedulesTable(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, migrationAddComplexitySuggestionSchedulesTable(context.Background(), db, testMigrationLogger))
	assert.True(t, db.Migrator().HasTable(&tables.TableComplexitySuggestionSchedule{}))
	require.NoError(t, migrationAddComplexitySuggestionSchedulesTable(context.Background(), db, testMigrationLogger))
}
