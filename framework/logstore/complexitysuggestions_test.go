package logstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// complexitySuggestionTables are the tables this file's tests own on every
// backend.
var complexitySuggestionTables = []string{"complexity_suggestions", "complexity_suggestion_runs", "complexity_vector_deletes"}

// forEachComplexitySuggestionStore runs test against every backend, built the
// way production builds it, so the tables exist only if the migration that
// creates them is registered. Postgres and ClickHouse skip when unreachable.
func forEachComplexitySuggestionStore(t *testing.T, test func(t *testing.T, store LogStore)) {
	t.Run("sqlite", func(t *testing.T) {
		store, err := newSqliteLogStore(context.Background(), &SQLiteConfig{
			Path: filepath.Join(t.TempDir(), "suggestions.db"),
		}, testLogger{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close(context.Background()) })
		test(t, store)
	})
	t.Run("postgres", func(t *testing.T) {
		db := trySetupPostgresDB(t)
		if db == nil {
			t.Skip("Postgres not available")
		}
		dropAllManagedMatViews(db)
		tables := append([]string{"mcp_tool_logs", "async_jobs", "webhook_deliveries", "logs"}, warpTopicTables...)
		for _, table := range append(tables, complexitySuggestionTables...) {
			require.NoError(t, db.Exec("DROP TABLE IF EXISTS "+table+" CASCADE").Error)
		}
		require.NoError(t, db.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)").Error)
		require.NoError(t, db.Exec("DELETE FROM migrations").Error)
		require.NoError(t, triggerMigrations(context.Background(), db, testLogger{}))
		test(t, &RDBLogStore{db: db, logger: testLogger{}})
	})
	t.Run("clickhouse", func(t *testing.T) {
		store := trySetupClickHouseStore(t)
		for _, table := range complexitySuggestionTables {
			require.NoError(t, store.db.Exec("TRUNCATE TABLE "+table).Error)
		}
		test(t, store)
	})
}

func testComplexitySuggestion(t *testing.T, id string, requests int64, centroid []float32) ComplexitySuggestion {
	t.Helper()
	s := ComplexitySuggestion{
		ID: id, Tier: "COMPLEX", Category: "uncovered", Confidence: "high", Reason: "multi-step design work",
		RequestCount: requests, Coverage: 0.75, NearestPhrase: "summarize this", NearestTier: "SIMPLE", NearestSimilarity: 0.42,
		Centroid: EncodeWarpVector(centroid), EmbeddingSignature: "BifrostComplexityRequests_abc", RunID: "run-1",
	}
	require.NoError(t, s.SetPhrases([]string{"design a distributed cache", "plan a schema migration"}))
	require.NoError(t, s.SetExampleLogIDs([]string{"log-medoid", "log-2"}))
	return s
}

// A suggestion comes back whole, its list fields decoded, and a write of the
// same ID replaces it: that is how a run refreshes counts and how a dismiss is
// recorded.
func TestComplexitySuggestionsRoundTripAndReplace(t *testing.T) {
	forEachComplexitySuggestionStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		created := warpTopicTestTime()
		big := testComplexitySuggestion(t, "big", 40, []float32{1, 0})
		big.CreatedAt = created
		small := testComplexitySuggestion(t, "small", 6, []float32{0, 1})
		small.EmbeddingSignature = "BifrostComplexityRequests_other"
		require.NoError(t, store.UpsertComplexitySuggestions(ctx, []ComplexitySuggestion{small, big}))

		all, err := store.ListComplexitySuggestions(ctx, ComplexitySuggestionFilter{})
		require.NoError(t, err)
		require.Len(t, all, 2)
		assert.Equal(t, "big", all[0].ID, "largest cluster first")
		got := all[0]
		assert.Equal(t, ComplexitySuggestionStatusOpen, got.Status, "an unset status is open")
		assert.Equal(t, []string{"design a distributed cache", "plan a schema migration"}, got.Phrases())
		assert.Equal(t, []string{"log-medoid", "log-2"}, got.ExampleLogIDs())
		assert.True(t, got.CreatedAt.Equal(created))
		assert.True(t, got.LastSeenAt.Equal(created), "first seen when created")
		assert.Nil(t, got.ResolvedAt)
		centroid, err := DecodeWarpVector(got.Centroid)
		require.NoError(t, err)
		assert.Equal(t, []float32{1, 0}, centroid)

		listed, err := store.ListComplexitySuggestions(ctx, ComplexitySuggestionFilter{OmitCentroids: true, EmbeddingSignature: "BifrostComplexityRequests_abc"})
		require.NoError(t, err)
		require.Len(t, listed, 1)
		assert.Empty(t, listed[0].Centroid, "a list without centroids does not read them")

		resolved := created.Add(time.Hour)
		got.Status = ComplexitySuggestionStatusDismissed
		got.ResolvedAt = &resolved
		require.NoError(t, store.UpsertComplexitySuggestions(ctx, []ComplexitySuggestion{got}))
		open, err := store.ListComplexitySuggestions(ctx, ComplexitySuggestionFilter{Statuses: []string{ComplexitySuggestionStatusOpen}})
		require.NoError(t, err)
		require.Len(t, open, 1)
		assert.Equal(t, "small", open[0].ID)
		dismissed, err := store.ListComplexitySuggestions(ctx, ComplexitySuggestionFilter{IDs: []string{"big"}})
		require.NoError(t, err)
		require.Len(t, dismissed, 1)
		assert.Equal(t, ComplexitySuggestionStatusDismissed, dismissed[0].Status)
		require.NotNil(t, dismissed[0].ResolvedAt)
		assert.True(t, dismissed[0].ResolvedAt.Equal(resolved))
		assert.True(t, dismissed[0].CreatedAt.Equal(created), "a rewrite keeps when it was first created")

		require.Error(t, store.UpsertComplexitySuggestions(ctx, []ComplexitySuggestion{{Tier: "SIMPLE"}}), "a suggestion needs an id")
		require.NoError(t, store.DeleteComplexitySuggestions(ctx, []string{"big", "missing"}))
		require.NoError(t, store.DeleteComplexitySuggestions(ctx, nil))
		all, err = store.ListComplexitySuggestions(ctx, ComplexitySuggestionFilter{})
		require.NoError(t, err)
		require.Len(t, all, 1)
		assert.Equal(t, "small", all[0].ID)
	})
}

// A run is written when it starts and again when it ends; runs list newest
// first and prune down to the newest few.
func TestComplexitySuggestionRuns(t *testing.T) {
	forEachComplexitySuggestionStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		base := warpTopicTestTime()
		for i, id := range []string{"r1", "r2", "r3", "r4"} {
			require.NoError(t, store.UpsertComplexitySuggestionRun(ctx, &ComplexitySuggestionRun{
				ID: id, StartedAt: base.Add(time.Duration(i) * time.Hour), EmbeddingSignature: "sig",
			}))
		}
		finished := base.Add(3*time.Hour + time.Minute)
		require.NoError(t, store.UpsertComplexitySuggestionRun(ctx, &ComplexitySuggestionRun{
			ID: "r4", StartedAt: base.Add(3 * time.Hour), TriggeredBy: ComplexitySuggestionTriggerManual,
			Status: ComplexitySuggestionRunSucceeded, FinishedAt: &finished, EmbeddingSignature: "sig",
			Model: "openai/gpt-5", PromptHash: "abc123", RequestsRead: 900, Clusters: 30, Created: 4, Refreshed: 2, Superseded: 1, Cost: 0.0125,
		}))

		runs, err := store.ListComplexitySuggestionRuns(ctx, nil, 10)
		require.NoError(t, err)
		require.Len(t, runs, 4, "writing a run again replaces it")
		assert.Equal(t, []string{"r4", "r3", "r2", "r1"}, []string{runs[0].ID, runs[1].ID, runs[2].ID, runs[3].ID}, "newest first")
		last := runs[0]
		assert.Equal(t, ComplexitySuggestionRunSucceeded, last.Status)
		assert.Equal(t, ComplexitySuggestionTriggerManual, last.TriggeredBy)
		require.NotNil(t, last.FinishedAt)
		assert.True(t, last.FinishedAt.Equal(finished))
		assert.EqualValues(t, 900, last.RequestsRead)
		assert.InDelta(t, 0.0125, last.Cost, 1e-9)
		assert.Equal(t, ComplexitySuggestionRunRunning, runs[1].Status, "an unset status is running")
		assert.Equal(t, ComplexitySuggestionTriggerSchedule, runs[1].TriggeredBy, "an unset trigger is the schedule")
		assert.Nil(t, runs[1].FinishedAt)

		one, err := store.ListComplexitySuggestionRuns(ctx, []string{"r2"}, 10)
		require.NoError(t, err)
		require.Len(t, one, 1)
		assert.Equal(t, "r2", one[0].ID)
		_, err = store.ListComplexitySuggestionRuns(ctx, nil, 0)
		require.Error(t, err, "an unbounded read is refused")
		require.Error(t, store.UpsertComplexitySuggestionRun(ctx, &ComplexitySuggestionRun{}), "a run needs an id")

		deleted, err := store.PruneComplexitySuggestionRuns(ctx, 2)
		require.NoError(t, err)
		assert.EqualValues(t, 2, deleted)
		runs, err = store.ListComplexitySuggestionRuns(ctx, nil, 10)
		require.NoError(t, err)
		require.Len(t, runs, 2)
		assert.Equal(t, "r4", runs[0].ID)
		assert.Equal(t, "r3", runs[1].ID)
		deleted, err = store.PruneComplexitySuggestionRuns(ctx, 2)
		require.NoError(t, err)
		assert.Zero(t, deleted, "nothing beyond the newest keep")
		_, err = store.PruneComplexitySuggestionRuns(ctx, 0)
		require.Error(t, err, "keeping nothing is refused")
	})
}

// A captured vector delete that failed is recorded, listed oldest first for
// the retry, and removed once it succeeds.
func TestComplexityVectorDeletes(t *testing.T) {
	forEachComplexitySuggestionStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		base := warpTopicTestTime()
		require.NoError(t, store.AddComplexityVectorDeletes(ctx, []ComplexityVectorDelete{
			{LogID: "late", Namespace: "BifrostComplexityRequests_abc", CreatedAt: base.Add(time.Minute)},
			{LogID: "early", CreatedAt: base},
		}))
		rows, err := store.ListComplexityVectorDeletes(ctx, 10)
		require.NoError(t, err)
		require.Len(t, rows, 2)
		assert.Equal(t, "early", rows[0].LogID, "oldest first")
		assert.Empty(t, rows[0].Namespace, "an unknown namespace is kept as unknown")
		assert.Equal(t, "BifrostComplexityRequests_abc", rows[1].Namespace)
		assert.True(t, rows[0].CreatedAt.Equal(base))

		_, err = store.ListComplexityVectorDeletes(ctx, 0)
		require.Error(t, err, "an unbounded read is refused")
		require.Error(t, store.AddComplexityVectorDeletes(ctx, []ComplexityVectorDelete{{Namespace: "x"}}), "a row needs a log id")

		require.NoError(t, store.DeleteComplexityVectorDeletes(ctx, []string{"early", "missing"}))
		require.NoError(t, store.DeleteComplexityVectorDeletes(ctx, nil))
		rows, err = store.ListComplexityVectorDeletes(ctx, 10)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, "late", rows[0].LogID)
	})
}

// The rollback refuses to drop decisions or pending deletes, and drops the
// tables once they are empty.
func TestComplexitySuggestionTablesRollbackGuard(t *testing.T) {
	store, err := newSqliteLogStore(context.Background(), &SQLiteConfig{
		Path: filepath.Join(t.TempDir(), "rollback.db"),
	}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	db := store.db
	ctx := context.Background()

	require.NoError(t, store.UpsertComplexitySuggestions(ctx, []ComplexitySuggestion{testComplexitySuggestion(t, "kept", 5, []float32{1})}))
	require.ErrorContains(t, rollbackComplexitySuggestionTables(db), "non-rollbackable")
	require.NoError(t, store.DeleteComplexitySuggestions(ctx, []string{"kept"}))

	require.NoError(t, store.AddComplexityVectorDeletes(ctx, []ComplexityVectorDelete{{LogID: "pending"}}))
	require.ErrorContains(t, rollbackComplexitySuggestionTables(db), "non-rollbackable")
	require.NoError(t, store.DeleteComplexityVectorDeletes(ctx, []string{"pending"}))

	require.NoError(t, store.UpsertComplexitySuggestionRun(ctx, &ComplexitySuggestionRun{ID: "history"}))
	require.NoError(t, rollbackComplexitySuggestionTables(db), "run history alone does not block a rollback")
	for _, model := range []any{&ComplexitySuggestion{}, &ComplexitySuggestionRun{}, &ComplexityVectorDelete{}} {
		assert.False(t, db.Migrator().HasTable(model))
	}
}

// Suggestions belong to one router: a router's listing never shows another's,
// and the router version a suggestion was computed against comes back with it.
func TestComplexitySuggestionsAreScopedToARouter(t *testing.T) {
	forEachComplexitySuggestionStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		eng := testComplexitySuggestion(t, "eng", 30, []float32{1, 0})
		eng.RouterID, eng.RouterVersion = "engineering", "v3"
		office := testComplexitySuggestion(t, "office", 20, []float32{1, 0})
		office.RouterID, office.RouterVersion = "office", "v1"
		require.NoError(t, store.UpsertComplexitySuggestions(ctx, []ComplexitySuggestion{eng, office}))

		got, err := store.ListComplexitySuggestions(ctx, ComplexitySuggestionFilter{RouterID: "engineering", Statuses: []string{ComplexitySuggestionStatusOpen}})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "eng", got[0].ID)
		assert.Equal(t, "v3", got[0].RouterVersion)

		all, err := store.ListComplexitySuggestions(ctx, ComplexitySuggestionFilter{})
		require.NoError(t, err)
		assert.Len(t, all, 2, "no router filter lists every router's suggestions")

		window := warpTopicTestTime().Add(-7 * 24 * time.Hour)
		require.NoError(t, store.UpsertComplexitySuggestionRun(ctx, &ComplexitySuggestionRun{
			ID: "run-eng", RouterID: "engineering", RouterVersion: "v3", StartedAt: warpTopicTestTime(), WindowStart: window,
		}))
		require.NoError(t, store.UpsertComplexitySuggestionRun(ctx, &ComplexitySuggestionRun{ID: "run-unwindowed", StartedAt: warpTopicTestTime()}))
		runs, err := store.ListComplexitySuggestionRuns(ctx, []string{"run-eng", "run-unwindowed"}, 10)
		require.NoError(t, err)
		require.Len(t, runs, 2)
		byID := map[string]ComplexitySuggestionRun{runs[0].ID: runs[0], runs[1].ID: runs[1]}
		assert.Equal(t, "engineering", byID["run-eng"].RouterID)
		assert.Equal(t, "v3", byID["run-eng"].RouterVersion)
		assert.True(t, byID["run-eng"].WindowStart.Equal(window))
		assert.True(t, byID["run-unwindowed"].WindowStart.Equal(warpTopicTestTime()), "an unset window starts with the run")
	})
}
