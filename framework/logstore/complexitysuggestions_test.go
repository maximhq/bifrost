package logstore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// complexitySuggestionTables are the tables this file's tests own on every
// backend.
var complexitySuggestionTables = []string{"model_routing_suggestions", "model_routing_suggestion_runs", "model_routing_vector_deletes"}

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
	require.NoError(t, s.SetCurrentRouting(map[string]int64{ComplexitySuggestionUnmatchedTier: requests - 2, "MEDIUM": 2}))
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
		assert.Equal(t, map[string]int64{ComplexitySuggestionUnmatchedTier: 38, "MEDIUM": 2}, got.CurrentRouting())
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
			RequestsClustered: 500, RequestsAssigned: 380, RequestsSkipped: 20, RequestsGrouped: 820,
			ClustersDecided: 3, ClustersOverLimit: 1, ClustersAsked: 26, ClustersDone: 26, ClustersFailed: 2,
			ClustersCovered: 10, ClustersIncoherent: 5, ClustersUnreadable: 3,
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
		assert.Equal(t, [12]int64{500, 380, 20, 820, 3, 1, 26, 26, 2, 10, 5, 3}, [12]int64{
			last.RequestsClustered, last.RequestsAssigned, last.RequestsSkipped, last.RequestsGrouped,
			last.ClustersDecided, last.ClustersOverLimit, last.ClustersAsked, last.ClustersDone, last.ClustersFailed,
			last.ClustersCovered, last.ClustersIncoherent, last.ClustersUnreadable,
		}, "the breakdown round-trips")
		assert.Empty(t, last.Stage)
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

		require.NoError(t, store.DeleteComplexityVectorDeletes(ctx, []ComplexityVectorDelete{{LogID: "early"}, {LogID: "missing"}}))
		require.NoError(t, store.DeleteComplexityVectorDeletes(ctx, nil))
		rows, err = store.ListComplexityVectorDeletes(ctx, 10)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, "late", rows[0].LogID)
	})
}

// A run reads the suggestions, spends minutes asking the model, then writes
// its outcome. A decision the user makes in between must survive the run's
// write; a run's write that lands first leaves the user's decision to apply
// on what it wrote. New suggestions are inserted either way.
func TestReconcileComplexitySuggestionsKeepsDecisionsMadeDuringARun(t *testing.T) {
	forEachComplexitySuggestionStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		read := testComplexitySuggestion(t, "decided", 10, []float32{1, 0})
		require.NoError(t, store.UpsertComplexitySuggestions(ctx, []ComplexitySuggestion{read}))
		status := func(id string) string {
			rows, err := store.ListComplexitySuggestions(ctx, ComplexitySuggestionFilter{IDs: []string{id}, OmitCentroids: true})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			return rows[0].Status
		}

		// The user accepts after the run read it open.
		resolved := time.Now().UTC()
		moved, err := store.UpdateComplexitySuggestionStatus(ctx, "decided", ComplexitySuggestionStatusOpen, ComplexitySuggestionStatusAccepted, &resolved)
		require.NoError(t, err)
		require.True(t, moved)
		superseded := read
		superseded.Status, superseded.ResolvedAt = ComplexitySuggestionStatusSuperseded, &resolved
		fresh := testComplexitySuggestion(t, "fresh", 5, []float32{0, 1})
		skipped, err := store.ReconcileComplexitySuggestions(ctx, []ComplexitySuggestionWrite{
			{Suggestion: superseded, From: ComplexitySuggestionStatusOpen},
			{Suggestion: fresh},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"decided"}, skipped)
		assert.Equal(t, ComplexitySuggestionStatusAccepted, status("decided"), "the accept made during the run stands")
		assert.Equal(t, ComplexitySuggestionStatusOpen, status("fresh"), "a new suggestion is still written")

		// The run's write lands first: it refreshes an open suggestion, and
		// the user's decision on it, read at open, then applies.
		refreshed := fresh
		refreshed.RequestCount = 9
		skipped, err = store.ReconcileComplexitySuggestions(ctx, []ComplexitySuggestionWrite{{Suggestion: refreshed, From: ComplexitySuggestionStatusOpen}})
		require.NoError(t, err)
		assert.Empty(t, skipped)
		rows, err := store.ListComplexitySuggestions(ctx, ComplexitySuggestionFilter{IDs: []string{"fresh"}, OmitCentroids: true})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.EqualValues(t, 9, rows[0].RequestCount)
		moved, err = store.UpdateComplexitySuggestionStatus(ctx, "fresh", ComplexitySuggestionStatusOpen, ComplexitySuggestionStatusDismissed, &resolved)
		require.NoError(t, err)
		assert.True(t, moved)
		assert.Equal(t, ComplexitySuggestionStatusDismissed, status("fresh"))
	})
}

// One log can be pending in several capture namespaces. Each is its own row:
// recording the second does not replace the first, and clearing one leaves
// the other for its retry.
func TestComplexityVectorDeletesAreKeyedByLogAndNamespace(t *testing.T) {
	forEachComplexitySuggestionStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		a := ComplexityVectorDelete{LogID: "x", Namespace: "BifrostComplexityRequests_a"}
		b := ComplexityVectorDelete{LogID: "x", Namespace: "BifrostComplexityRequests_b"}
		require.NoError(t, store.AddComplexityVectorDeletes(ctx, []ComplexityVectorDelete{a}))
		require.NoError(t, store.AddComplexityVectorDeletes(ctx, []ComplexityVectorDelete{b}))
		rows, err := store.ListComplexityVectorDeletes(ctx, 10)
		require.NoError(t, err)
		require.Len(t, rows, 2, "a second namespace is a second row")

		require.NoError(t, store.DeleteComplexityVectorDeletes(ctx, []ComplexityVectorDelete{a}))
		rows, err = store.ListComplexityVectorDeletes(ctx, 10)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, b.Namespace, rows[0].Namespace, "the other namespace's row stays")
	})
}

// The list pages in its order and filters by status; the counts cover every
// status whatever the list selected; and a decision moves a suggestion only
// from the status it was read at.
func TestComplexitySuggestionsPagingCountsAndConditionalStatus(t *testing.T) {
	forEachComplexitySuggestionStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		a := testComplexitySuggestion(t, "a", 30, []float32{1, 0})
		b := testComplexitySuggestion(t, "b", 20, []float32{0, 1})
		c := testComplexitySuggestion(t, "c", 10, []float32{1, 1})
		c.Status = ComplexitySuggestionStatusDismissed
		require.NoError(t, store.UpsertComplexitySuggestions(ctx, []ComplexitySuggestion{a, b, c}))

		page, err := store.ListComplexitySuggestions(ctx, ComplexitySuggestionFilter{Statuses: []string{ComplexitySuggestionStatusOpen}, Limit: 1, OmitCentroids: true})
		require.NoError(t, err)
		require.Len(t, page, 1)
		assert.Equal(t, "a", page[0].ID, "largest first")
		page, err = store.ListComplexitySuggestions(ctx, ComplexitySuggestionFilter{Statuses: []string{ComplexitySuggestionStatusOpen}, Limit: 1, Offset: 1, OmitCentroids: true})
		require.NoError(t, err)
		require.Len(t, page, 1)
		assert.Equal(t, "b", page[0].ID)

		counts, err := store.CountComplexitySuggestionsByStatus(ctx, ComplexitySuggestionFilter{Statuses: []string{ComplexitySuggestionStatusOpen}, Limit: 1})
		require.NoError(t, err)
		assert.Equal(t, map[string]int{ComplexitySuggestionStatusOpen: 2, ComplexitySuggestionStatusDismissed: 1}, counts)

		resolved := time.Now().UTC()
		moved, err := store.UpdateComplexitySuggestionStatus(ctx, "a", ComplexitySuggestionStatusOpen, ComplexitySuggestionStatusAccepted, &resolved)
		require.NoError(t, err)
		assert.True(t, moved)
		moved, err = store.UpdateComplexitySuggestionStatus(ctx, "a", ComplexitySuggestionStatusOpen, ComplexitySuggestionStatusDismissed, &resolved)
		require.NoError(t, err)
		assert.False(t, moved, "a decision read at open loses once another moved it")
		moved, err = store.UpdateComplexitySuggestionStatus(ctx, "missing", ComplexitySuggestionStatusOpen, ComplexitySuggestionStatusAccepted, &resolved)
		require.NoError(t, err)
		assert.False(t, moved)

		rows, err := store.ListComplexitySuggestions(ctx, ComplexitySuggestionFilter{IDs: []string{"a"}})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, ComplexitySuggestionStatusAccepted, rows[0].Status)
		require.NotNil(t, rows[0].ResolvedAt)
		assert.NotEmpty(t, rows[0].Centroid, "the centroid is kept for the next run's match")

		moved, err = store.UpdateComplexitySuggestionStatus(ctx, "a", ComplexitySuggestionStatusAccepted, ComplexitySuggestionStatusOpen, nil)
		require.NoError(t, err)
		assert.True(t, moved)
		rows, err = store.ListComplexitySuggestions(ctx, ComplexitySuggestionFilter{IDs: []string{"a"}})
		require.NoError(t, err)
		assert.Nil(t, rows[0].ResolvedAt, "reopening clears when it was resolved")
	})
}

// ClickHouse writes both tables complexitySuggestionWriteBatch rows per
// insert, as the RDB store does, so a large delete or run never builds one
// statement for all of its rows; every row still lands.
func TestClickHouseComplexityWritesAreChunked(t *testing.T) {
	store := trySetupClickHouseStore(t)
	for _, table := range complexitySuggestionTables {
		require.NoError(t, store.db.Exec("TRUNCATE TABLE "+table).Error)
	}
	ctx := context.Background()
	var inserts int
	require.NoError(t, store.db.Callback().Create().After("gorm:create").Register("test:count_complexity_inserts", func(*gorm.DB) { inserts++ }))
	t.Cleanup(func() { _ = store.db.Callback().Create().Remove("test:count_complexity_inserts") })

	n := complexitySuggestionWriteBatch*2 + 1
	deletes := make([]ComplexityVectorDelete, n)
	suggestions := make([]ComplexitySuggestion, n)
	for i := range n {
		deletes[i] = ComplexityVectorDelete{LogID: fmt.Sprintf("log-%04d", i)}
		suggestions[i] = testComplexitySuggestion(t, fmt.Sprintf("s-%04d", i), int64(10+i), []float32{1, 0})
	}

	inserts = 0
	require.NoError(t, store.AddComplexityVectorDeletes(ctx, deletes))
	assert.Equal(t, 3, inserts, "one insert per chunk")
	rows, err := store.ListComplexityVectorDeletes(ctx, n+10)
	require.NoError(t, err)
	assert.Len(t, rows, n)

	inserts = 0
	require.NoError(t, store.UpsertComplexitySuggestions(ctx, suggestions))
	assert.Equal(t, 3, inserts, "one insert per chunk")
	listed, err := store.ListComplexitySuggestions(ctx, ComplexitySuggestionFilter{OmitCentroids: true})
	require.NoError(t, err)
	assert.Len(t, listed, n)
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
	require.NoError(t, store.DeleteComplexityVectorDeletes(ctx, []ComplexityVectorDelete{{LogID: "pending"}}))

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
