package logstore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// warpTopicTables are the tables this file's tests own on every backend.
var warpTopicTables = []string{"warp_topics", "warp_topic_assignments", "warp_topic_unmatched"}

// forEachWarpTopicStore runs test against every backend there is. SQLite
// always runs. Postgres and ClickHouse run when reachable, and skip their own
// subtest when not, so a laptop with neither still exercises the SQL paths.
//
// Each backend is built the way production builds it, migrations included, so
// the three tables exist only if the migration that creates them is registered.
func forEachWarpTopicStore(t *testing.T, test func(t *testing.T, store LogStore)) {
	t.Run("sqlite", func(t *testing.T) {
		store, err := newSqliteLogStore(context.Background(), &SQLiteConfig{
			Path: filepath.Join(t.TempDir(), "topics.db"),
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
		// The same clean slate parityBackends takes, in this package's own
		// schema: drop everything and migrate from nothing.
		dropAllManagedMatViews(db)
		for _, table := range append([]string{"mcp_tool_logs", "async_jobs", "webhook_deliveries", "logs"}, warpTopicTables...) {
			require.NoError(t, db.Exec("DROP TABLE IF EXISTS "+table+" CASCADE").Error)
		}
		require.NoError(t, db.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)").Error)
		require.NoError(t, db.Exec("DELETE FROM migrations").Error)
		require.NoError(t, triggerMigrations(context.Background(), db, testLogger{}))
		test(t, &RDBLogStore{db: db, logger: testLogger{}})
	})
	t.Run("clickhouse", func(t *testing.T) {
		store := trySetupClickHouseStore(t)
		for _, table := range warpTopicTables {
			require.NoError(t, store.db.Exec("TRUNCATE TABLE "+table).Error)
		}
		test(t, store)
	})
}

// warpTopicTestTime is a fixed instant with a millisecond part, so a backend
// that dropped sub-second precision anywhere would show it.
func warpTopicTestTime() time.Time {
	return time.Date(2026, 9, 1, 12, 0, 0, 250_000_000, time.UTC)
}

func warpTestAssignment(logID, topicID string, at time.Time) WarpTopicAssignment {
	return WarpTopicAssignment{
		LogID: logID, TopicID: topicID, Timestamp: at, LogCreatedAt: at,
		Similarity: 0.8, Provider: "openai", Model: "gpt-4o", Status: "success",
	}
}

func warpTestUnmatched(logID, signature string, at time.Time, vector []float32) WarpTopicUnmatched {
	return WarpTopicUnmatched{
		LogID: logID, Timestamp: at, LogCreatedAt: at,
		EmbeddingSignature: signature, Vector: EncodeWarpVector(vector),
	}
}

func warpUsageByTopic(usage []WarpTopicUsage) map[string]WarpTopicUsage {
	byTopic := make(map[string]WarpTopicUsage, len(usage))
	for _, u := range usage {
		byTopic[u.TopicID] = u
	}
	return byTopic
}

func warpAssignmentLogIDs(assignments []WarpTopicAssignment) []string {
	ids := make([]string, 0, len(assignments))
	for _, a := range assignments {
		ids = append(ids, a.LogID)
	}
	return ids
}

func warpUnmatchedLogIDs(rows []WarpTopicUnmatched) []string {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.LogID)
	}
	return ids
}

func TestEncodeWarpVectorRoundTrip(t *testing.T) {
	vector := []float32{0, 1, -1, 0.123456789, 3.4e38, -1.4e-45}
	encoded := EncodeWarpVector(vector)
	require.Len(t, encoded, 4*len(vector))
	decoded, err := DecodeWarpVector(encoded)
	require.NoError(t, err)
	require.Equal(t, vector, decoded, "a vector must come back bit for bit")

	require.Nil(t, EncodeWarpVector(nil))
	decoded, err = DecodeWarpVector(nil)
	require.NoError(t, err)
	require.Nil(t, decoded)

	_, err = DecodeWarpVector([]byte{1, 2, 3, 4, 5})
	require.Error(t, err, "a truncated vector must be refused, not read short")
}

func TestWarpTopicsRoundTrip(t *testing.T) {
	forEachWarpTopicStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		centroid := []float32{0.6, -0.8, 0.25}

		billing := WarpTopic{
			ID: "topic-billing", Label: "Billing questions",
			Centroid: EncodeWarpVector(centroid), AssignFloor: 0.62, Cohesion: 0.81,
			EmbeddingSignature: "openai/text-embedding-3-small/1536", ParentID: "theme-money",
		}
		require.NoError(t, billing.SetExampleLogIDs([]string{"log-1", "log-2"}))
		theme := WarpTopic{
			ID: "theme-money", Kind: WarpTopicKindTheme, Label: "Money",
			EmbeddingSignature: "openai/text-embedding-3-small/1536",
		}
		other := WarpTopic{
			ID: "topic-old", Label: "From the previous model", Status: WarpTopicStatusRetired,
			Centroid: EncodeWarpVector([]float32{1, 0}), EmbeddingSignature: "openai/ada-002/1536",
		}
		require.NoError(t, store.UpsertWarpTopics(ctx, []WarpTopic{billing, theme, other}))

		all, err := store.ListWarpTopics(ctx, WarpTopicFilter{})
		require.NoError(t, err)
		require.Len(t, all, 3)

		topics, err := store.ListWarpTopics(ctx, WarpTopicFilter{IDs: []string{"topic-billing"}})
		require.NoError(t, err)
		require.Len(t, topics, 1)
		got := topics[0]
		assert.Equal(t, WarpTopicKindTopic, got.Kind, "kind defaults to topic")
		assert.Equal(t, WarpTopicStatusActive, got.Status, "status defaults to active")
		assert.Equal(t, "Billing questions", got.Label)
		assert.Equal(t, "theme-money", got.ParentID)
		assert.InDelta(t, 0.62, got.AssignFloor, 1e-9)
		assert.InDelta(t, 0.81, got.Cohesion, 1e-9)
		assert.Equal(t, []string{"log-1", "log-2"}, got.ExampleLogIDs())
		assert.False(t, got.CreatedAt.IsZero())
		assert.False(t, got.UpdatedAt.IsZero())
		decoded, err := DecodeWarpVector(got.Centroid)
		require.NoError(t, err)
		assert.Equal(t, centroid, decoded, "the centroid must survive storage bit for bit")

		// Filters, each on its own.
		themes, err := store.ListWarpTopics(ctx, WarpTopicFilter{Kinds: []string{WarpTopicKindTheme}})
		require.NoError(t, err)
		require.Len(t, themes, 1)
		assert.Equal(t, "theme-money", themes[0].ID)
		assert.Empty(t, themes[0].Centroid, "a theme written without a centroid reads back without one")

		active, err := store.ListWarpTopics(ctx, WarpTopicFilter{Statuses: []string{WarpTopicStatusActive}})
		require.NoError(t, err)
		assert.Len(t, active, 2)

		current, err := store.ListWarpTopics(ctx, WarpTopicFilter{EmbeddingSignature: "openai/text-embedding-3-small/1536"})
		require.NoError(t, err)
		assert.Len(t, current, 2, "topics from another embedding model must not be selected")

		light, err := store.ListWarpTopics(ctx, WarpTopicFilter{IDs: []string{"topic-billing"}, OmitCentroids: true})
		require.NoError(t, err)
		require.Len(t, light, 1)
		assert.Empty(t, light[0].Centroid)
		assert.Equal(t, "Billing questions", light[0].Label)

		// Writing a topic again replaces it: one row, the new values.
		got.Label = "Billing and invoices"
		got.Status = WarpTopicStatusMerged
		got.MergedInto = "topic-payments"
		require.NoError(t, store.UpsertWarpTopics(ctx, []WarpTopic{got}))

		all, err = store.ListWarpTopics(ctx, WarpTopicFilter{})
		require.NoError(t, err)
		require.Len(t, all, 3, "an upsert must not leave a second row behind")
		topics, err = store.ListWarpTopics(ctx, WarpTopicFilter{IDs: []string{"topic-billing"}})
		require.NoError(t, err)
		require.Len(t, topics, 1)
		assert.Equal(t, "Billing and invoices", topics[0].Label)
		assert.Equal(t, WarpTopicStatusMerged, topics[0].Status)
		assert.Equal(t, "topic-payments", topics[0].MergedInto)
		assert.WithinDuration(t, got.CreatedAt, topics[0].CreatedAt, time.Millisecond, "a rewrite keeps the creation time it was given")
		assert.Equal(t, centroid, func() []float32 { v, _ := DecodeWarpVector(topics[0].Centroid); return v }())

		require.Error(t, store.UpsertWarpTopics(ctx, []WarpTopic{{Label: "no id"}}))
	})
}

func TestWarpTopicAssignmentsSummarize(t *testing.T) {
	forEachWarpTopicStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		base := warpTopicTestTime()
		at := func(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }

		rows := []WarpTopicAssignment{
			warpTestAssignment("a1", "billing", at(0)),
			warpTestAssignment("a2", "billing", at(1)),
			warpTestAssignment("a3", "billing", at(2)),
			warpTestAssignment("a4", "sql", at(3)),
			warpTestAssignment("a5", "sql", at(4)),
			warpTestAssignment("a6", "", at(5)), // matched nothing
		}
		rows[0].SessionID, rows[0].Cost, rows[0].Latency, rows[0].TotalTokens, rows[0].TeamID = "s1", 0.10, 100, 10, "team-a"
		rows[1].SessionID, rows[1].Cost, rows[1].Latency, rows[1].TotalTokens, rows[1].TeamID = "s1", 0.20, 300, 20, "team-a"
		rows[2].SessionID, rows[2].Cost, rows[2].TotalTokens, rows[2].TeamID = "s2", 0.30, 30, "team-b" // latency unmeasured
		rows[2].Status = "error"
		rows[3].Cost, rows[3].Latency, rows[3].TotalTokens, rows[3].TeamID = 1.5, 50, 5, "team-b" // no session
		rows[4].SessionID, rows[4].TeamID, rows[4].Model = "s3", "team-b", "claude"
		require.NoError(t, store.UpsertWarpTopicAssignments(ctx, rows))

		usage, err := store.SummarizeWarpTopicAssignments(ctx, WarpTopicAssignmentFilter{})
		require.NoError(t, err)
		require.Len(t, usage, 3)
		assert.Equal(t, "billing", usage[0].TopicID, "largest topic first")
		byTopic := warpUsageByTopic(usage)

		billing := byTopic["billing"]
		assert.EqualValues(t, 3, billing.Requests)
		assert.EqualValues(t, 2, billing.Sessions, "s1 twice and s2 once is two sessions")
		assert.EqualValues(t, 1, billing.Errors)
		assert.InDelta(t, 0.60, billing.Cost, 1e-9)
		assert.EqualValues(t, 60, billing.Tokens)
		assert.InDelta(t, 200, billing.AvgLatency, 1e-9, "the unmeasured request must not pull the average toward zero")

		sql := byTopic["sql"]
		assert.EqualValues(t, 2, sql.Requests)
		assert.EqualValues(t, 1, sql.Sessions, "a request without a session is not a session")
		assert.EqualValues(t, 0, sql.Errors)

		unmatched := byTopic[""]
		assert.EqualValues(t, 1, unmatched.Requests, "requests with no topic are counted under the empty id")
		assert.Zero(t, unmatched.AvgLatency)

		// A window, inclusive at both ends and exact to the millisecond.
		start, end := at(1), at(3)
		usage, err = store.SummarizeWarpTopicAssignments(ctx, WarpTopicAssignmentFilter{StartTime: &start, EndTime: &end})
		require.NoError(t, err)
		byTopic = warpUsageByTopic(usage)
		assert.EqualValues(t, 2, byTopic["billing"].Requests)
		assert.EqualValues(t, 1, byTopic["sql"].Requests)
		assert.NotContains(t, byTopic, "")

		// Filters narrow together.
		usage, err = store.SummarizeWarpTopicAssignments(ctx, WarpTopicAssignmentFilter{TeamIDs: []string{"team-b"}, Models: []string{"gpt-4o"}})
		require.NoError(t, err)
		byTopic = warpUsageByTopic(usage)
		assert.EqualValues(t, 1, byTopic["billing"].Requests)
		assert.EqualValues(t, 1, byTopic["sql"].Requests)

		usage, err = store.SummarizeWarpTopicAssignments(ctx, WarpTopicAssignmentFilter{TopicIDs: []string{""}})
		require.NoError(t, err)
		require.Len(t, usage, 1, "the empty topic id selects the unmatched requests")
		assert.EqualValues(t, 1, usage[0].Requests)

		// A request assigned again moves; it is not counted twice.
		moved := rows[5]
		moved.TopicID = "sql"
		moved.Similarity = 0.9
		require.NoError(t, store.UpsertWarpTopicAssignments(ctx, []WarpTopicAssignment{moved}))
		usage, err = store.SummarizeWarpTopicAssignments(ctx, WarpTopicAssignmentFilter{})
		require.NoError(t, err)
		byTopic = warpUsageByTopic(usage)
		assert.EqualValues(t, 3, byTopic["sql"].Requests)
		assert.NotContains(t, byTopic, "", "the request's earlier row must be gone")
		var total int64
		for _, u := range usage {
			total += u.Requests
		}
		assert.EqualValues(t, 6, total)

		require.Error(t, store.UpsertWarpTopicAssignments(ctx, []WarpTopicAssignment{{TopicID: "billing", Timestamp: base}}), "no log id")
		require.Error(t, store.UpsertWarpTopicAssignments(ctx, []WarpTopicAssignment{{LogID: "x", TopicID: "billing"}}), "no timestamp")
	})
}

func TestWarpTopicAssignmentsList(t *testing.T) {
	forEachWarpTopicStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		base := warpTopicTestTime()

		first := warpTestAssignment("l1", "billing", base)
		first.SessionID, first.UserID, first.TeamID, first.VirtualKeyID = "s1", "u1", "team-a", "vk1"
		first.CustomerID, first.BusinessUnitID, first.App = "c1", "bu1", "cursor"
		first.Cost, first.Latency, first.TotalTokens, first.Similarity = 0.25, 420.5, 77, 0.91
		// Two rows in the same millisecond: the log id breaks the tie.
		rows := []WarpTopicAssignment{
			first,
			warpTestAssignment("l2", "billing", base.Add(time.Second)),
			warpTestAssignment("l3", "billing", base.Add(time.Second)),
			warpTestAssignment("l4", "sql", base.Add(2*time.Second)),
		}
		require.NoError(t, store.UpsertWarpTopicAssignments(ctx, rows))

		all, err := store.ListWarpTopicAssignments(ctx, WarpTopicAssignmentFilter{}, 0)
		require.NoError(t, err)
		assert.Equal(t, []string{"l4", "l3", "l2", "l1"}, warpAssignmentLogIDs(all), "newest first, log id descending within an instant")

		limited, err := store.ListWarpTopicAssignments(ctx, WarpTopicAssignmentFilter{TopicIDs: []string{"billing"}}, 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"l3", "l2"}, warpAssignmentLogIDs(limited))

		// By log id, which is how a caller checks which of a topic's example
		// requests it can see.
		picked, err := store.ListWarpTopicAssignments(ctx, WarpTopicAssignmentFilter{LogIDs: []string{"l1", "l4", "missing"}}, 10)
		require.NoError(t, err)
		assert.Equal(t, []string{"l4", "l1"}, warpAssignmentLogIDs(picked))

		got := picked[1]
		assert.Equal(t, "billing", got.TopicID)
		assert.True(t, got.Timestamp.Equal(base), "timestamp %s, want %s", got.Timestamp, base)
		assert.True(t, got.LogCreatedAt.Equal(base))
		assert.False(t, got.AssignedAt.IsZero(), "the assignment time is stamped when the caller leaves it out")
		assert.Equal(t, "s1", got.SessionID)
		assert.Equal(t, "u1", got.UserID)
		assert.Equal(t, "team-a", got.TeamID)
		assert.Equal(t, "vk1", got.VirtualKeyID)
		assert.Equal(t, "c1", got.CustomerID)
		assert.Equal(t, "bu1", got.BusinessUnitID)
		assert.Equal(t, "openai", got.Provider)
		assert.Equal(t, "gpt-4o", got.Model)
		assert.Equal(t, "cursor", got.App)
		assert.Equal(t, "success", got.Status)
		assert.InDelta(t, 0.25, got.Cost, 1e-9)
		assert.InDelta(t, 420.5, got.Latency, 1e-9)
		assert.Equal(t, 77, got.TotalTokens)
		assert.InDelta(t, 0.91, got.Similarity, 1e-9)
	})
}

// A restricted caller's scope is a predicate over the log ownership columns.
// The assignments table carries those columns so that the same predicate, built
// for logs and never told about this table, restricts it too.
func TestWarpTopicAssignmentsRespectAccessScope(t *testing.T) {
	forEachWarpTopicStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		base := warpTopicTestTime()

		mine := warpTestAssignment("mine", "billing", base)
		mine.UserID = "u1"
		viaKey := warpTestAssignment("via-key", "sql", base.Add(time.Second))
		viaKey.UserID, viaKey.VirtualKeyID = "u2", "vk2"
		viaTeam := warpTestAssignment("via-team", "billing", base.Add(2*time.Second))
		viaTeam.UserID, viaTeam.TeamID = "u3", "team-a"
		theirs := warpTestAssignment("theirs", "billing", base.Add(3*time.Second))
		theirs.UserID, theirs.TeamID, theirs.CustomerID, theirs.BusinessUnitID = "u9", "team-z", "c9", "bu9"
		require.NoError(t, store.UpsertWarpTopicAssignments(ctx, []WarpTopicAssignment{mine, viaKey, viaTeam, theirs}))

		// The closure shape the enterprise wrapper builds (dacscope.go logScope).
		scoped := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB {
			return db.Where("(user_id IN ? OR team_id IN ? OR virtual_key_id IN ?)", []string{"u1"}, []string{"team-a"}, []string{"vk2"})
		})

		listed, err := store.ListWarpTopicAssignments(scoped, WarpTopicAssignmentFilter{}, 0)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"mine", "via-key", "via-team"}, warpAssignmentLogIDs(listed))

		usage, err := store.SummarizeWarpTopicAssignments(scoped, WarpTopicAssignmentFilter{})
		require.NoError(t, err)
		byTopic := warpUsageByTopic(usage)
		assert.EqualValues(t, 2, byTopic["billing"].Requests, "another team's request must not be counted")
		assert.EqualValues(t, 1, byTopic["sql"].Requests)

		// Naming a row outside the scope does not reach it.
		reached, err := store.ListWarpTopicAssignments(scoped, WarpTopicAssignmentFilter{LogIDs: []string{"theirs"}}, 0)
		require.NoError(t, err)
		assert.Empty(t, reached)

		// A principal with no dimensions fails closed.
		closed := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("1 = 0") })
		listed, err = store.ListWarpTopicAssignments(closed, WarpTopicAssignmentFilter{}, 0)
		require.NoError(t, err)
		assert.Empty(t, listed)
		usage, err = store.SummarizeWarpTopicAssignments(closed, WarpTopicAssignmentFilter{})
		require.NoError(t, err)
		assert.Empty(t, usage)

		// Topics have no owner, so the scope must not be applied to them: on a
		// table without the ownership columns it would be an error, not a filter.
		require.NoError(t, store.UpsertWarpTopics(ctx, []WarpTopic{{ID: "billing", Label: "Billing", EmbeddingSignature: "sig"}}))
		topics, err := store.ListWarpTopics(scoped, WarpTopicFilter{})
		require.NoError(t, err)
		assert.Len(t, topics, 1)
	})
}

func TestWarpTopicUnmatchedPool(t *testing.T) {
	forEachWarpTopicStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		base := warpTopicTestTime()
		const sig, oldSig = "openai/text-embedding-3-small/1536", "openai/ada-002/1536"
		vector := []float32{0.1, -0.2, 0.3, 0.4}

		rows := []WarpTopicUnmatched{
			warpTestUnmatched("u1", sig, base, vector),
			// Three rows in one instant, so the count bound has a tie to break.
			warpTestUnmatched("u2", sig, base.Add(time.Minute), []float32{1, 0, 0, 0}),
			warpTestUnmatched("u3", sig, base.Add(time.Minute), []float32{0, 1, 0, 0}),
			warpTestUnmatched("u4", sig, base.Add(time.Minute), []float32{0, 0, 1, 0}),
			warpTestUnmatched("u5", sig, base.Add(2*time.Minute), []float32{0, 0, 0, 1}),
			warpTestUnmatched("old-model", oldSig, base.Add(3*time.Minute), []float32{1, 1}),
		}
		require.NoError(t, store.AddWarpTopicUnmatched(ctx, rows))

		count, err := store.CountWarpTopicUnmatched(ctx, sig)
		require.NoError(t, err)
		assert.EqualValues(t, 5, count)

		pool, err := store.ListWarpTopicUnmatched(ctx, sig, 100)
		require.NoError(t, err)
		assert.Equal(t, []string{"u5", "u4", "u3", "u2", "u1"}, warpUnmatchedLogIDs(pool), "newest first; another model's vectors are not in this pool")
		decoded, err := DecodeWarpVector(pool[4].Vector)
		require.NoError(t, err)
		assert.Equal(t, vector, decoded, "a pooled vector must survive storage bit for bit")
		assert.True(t, pool[4].Timestamp.Equal(base))

		newest, err := store.ListWarpTopicUnmatched(ctx, sig, 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"u5", "u4"}, warpUnmatchedLogIDs(newest))
		_, err = store.ListWarpTopicUnmatched(ctx, sig, 0)
		require.Error(t, err, "an unbounded read of the pool is refused")

		// Pooling a request again replaces its row.
		again := warpTestUnmatched("u1", sig, base, []float32{9, 9, 9, 9})
		require.NoError(t, store.AddWarpTopicUnmatched(ctx, []WarpTopicUnmatched{again}))
		count, err = store.CountWarpTopicUnmatched(ctx, sig)
		require.NoError(t, err)
		assert.EqualValues(t, 5, count)
		pool, err = store.ListWarpTopicUnmatched(ctx, sig, 100)
		require.NoError(t, err)
		decoded, err = DecodeWarpVector(pool[4].Vector)
		require.NoError(t, err)
		assert.Equal(t, []float32{9, 9, 9, 9}, decoded)

		// Count bound: keep the newest four of six. The fifth newest is u2, in a
		// three-way tie with u3 and u4, which both stay.
		deleted, err := store.PruneWarpTopicUnmatched(ctx, time.Time{}, 4)
		require.NoError(t, err)
		assert.EqualValues(t, 2, deleted)
		pool, err = store.ListWarpTopicUnmatched(ctx, sig, 100)
		require.NoError(t, err)
		assert.Equal(t, []string{"u5", "u4", "u3"}, warpUnmatchedLogIDs(pool))

		// Within the bound, nothing goes.
		deleted, err = store.PruneWarpTopicUnmatched(ctx, time.Time{}, 4)
		require.NoError(t, err)
		assert.Zero(t, deleted)
		deleted, err = store.PruneWarpTopicUnmatched(ctx, time.Time{}, 0)
		require.NoError(t, err)
		assert.Zero(t, deleted, "no bound given, nothing pruned")

		// Age bound: strictly older than the cutoff.
		deleted, err = store.PruneWarpTopicUnmatched(ctx, base.Add(2*time.Minute), 0)
		require.NoError(t, err)
		assert.EqualValues(t, 2, deleted)
		pool, err = store.ListWarpTopicUnmatched(ctx, sig, 100)
		require.NoError(t, err)
		assert.Equal(t, []string{"u5"}, warpUnmatchedLogIDs(pool))

		require.NoError(t, store.DeleteWarpTopicUnmatched(ctx, []string{"u5", "missing"}))
		require.NoError(t, store.DeleteWarpTopicUnmatched(ctx, nil))
		count, err = store.CountWarpTopicUnmatched(ctx, sig)
		require.NoError(t, err)
		assert.Zero(t, count)
		count, err = store.CountWarpTopicUnmatched(ctx, oldSig)
		require.NoError(t, err)
		assert.EqualValues(t, 1, count, "deletes by id leave other rows alone")

		require.Error(t, store.AddWarpTopicUnmatched(ctx, []WarpTopicUnmatched{{LogID: "no-vector", Timestamp: base, EmbeddingSignature: sig}}))
	})
}

// Both tables hold rows about individual logs. Retention removes them on the
// log's own created time, so a row cannot outlive the log it describes.
func TestWarpTopicRowsExpireWithTheirLogs(t *testing.T) {
	forEachWarpTopicStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Millisecond)
		expired, kept := now.AddDate(0, 0, -40), now.AddDate(0, 0, -5)
		cutoff := now.AddDate(0, 0, -30)

		var assignments []WarpTopicAssignment
		var unmatched []WarpTopicUnmatched
		for i := range 5 {
			assignments = append(assignments, warpTestAssignment(fmt.Sprintf("old-%d", i), "billing", expired.Add(time.Duration(i)*time.Second)))
			unmatched = append(unmatched, warpTestUnmatched(fmt.Sprintf("old-%d", i), "sig", expired.Add(time.Duration(i)*time.Second), []float32{1, 2}))
		}
		assignments = append(assignments, warpTestAssignment("new-1", "billing", kept), warpTestAssignment("new-2", "sql", kept))
		unmatched = append(unmatched, warpTestUnmatched("new-1", "sig", kept, []float32{3, 4}))
		// The log's created time decides, not its request timestamp: a row whose
		// log was created inside retention stays even if the request is older.
		late := warpTestAssignment("old-request-new-log", "billing", expired)
		late.LogCreatedAt = kept
		assignments = append(assignments, late)
		require.NoError(t, store.UpsertWarpTopicAssignments(ctx, assignments))
		require.NoError(t, store.AddWarpTopicUnmatched(ctx, unmatched))

		// Drained the way LogsCleaner drains: until a batch comes back short.
		drain := func(deleteBatch func(context.Context, time.Time, int) (int64, error)) int64 {
			const batch = 2
			var total int64
			for range 10 {
				deleted, err := deleteBatch(ctx, cutoff, batch)
				require.NoError(t, err)
				total += deleted
				if deleted != batch {
					return total
				}
			}
			t.Fatal("retention never finished")
			return total
		}
		assert.EqualValues(t, 5, drain(store.DeleteWarpTopicAssignmentsBatch))
		assert.EqualValues(t, 5, drain(store.DeleteWarpTopicUnmatchedBatch))

		left, err := store.ListWarpTopicAssignments(ctx, WarpTopicAssignmentFilter{}, 100)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"new-1", "new-2", "old-request-new-log"}, warpAssignmentLogIDs(left))
		pool, err := store.ListWarpTopicUnmatched(ctx, "sig", 100)
		require.NoError(t, err)
		assert.Equal(t, []string{"new-1"}, warpUnmatchedLogIDs(pool))

		// A second pass finds nothing.
		assert.Zero(t, drain(store.DeleteWarpTopicAssignmentsBatch))
		assert.Zero(t, drain(store.DeleteWarpTopicUnmatchedBatch))
	})
}

func TestListLogsForWarpTopics(t *testing.T) {
	forEachWarpTopicStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		base := warpTopicTestTime()

		full := &Log{
			ID: "log-full", Timestamp: base, CreatedAt: base.Add(time.Second),
			Object: "chat.completion", Provider: "openai", Model: "gpt-4o", Status: "success",
			SessionID: strPtrP("s1"), UserID: strPtrP("u1"), TeamID: strPtrP("team-a"),
			VirtualKeyID: strPtrP("vk1"), CustomerID: strPtrP("c1"), BusinessUnitID: strPtrP("bu1"),
			App: strPtrP("cursor"), Cost: f64PtrP(0.42), Latency: f64PtrP(310.5), TotalTokens: 99,
		}
		// Nothing optional set: every nullable column is NULL.
		bare := &Log{
			ID: "log-bare", Timestamp: base.Add(time.Hour), CreatedAt: base.Add(time.Hour),
			Object: "chat.completion", Provider: "anthropic", Model: "claude", Status: "error",
		}
		require.NoError(t, store.Create(ctx, full))
		require.NoError(t, store.Create(ctx, bare))

		rows, err := store.ListLogsForWarpTopics(ctx, []string{"log-full", "log-bare", "log-missing"}, time.Time{}, time.Time{})
		require.NoError(t, err)
		require.Len(t, rows, 2, "a log that no longer exists is simply absent")
		byID := map[string]WarpTopicAssignment{}
		for _, row := range rows {
			byID[row.LogID] = row
		}

		got := byID["log-full"]
		assert.True(t, got.Timestamp.Equal(base), "timestamp %s, want %s", got.Timestamp, base)
		assert.True(t, got.LogCreatedAt.Equal(base.Add(time.Second)), "created_at %s", got.LogCreatedAt)
		assert.Equal(t, "s1", got.SessionID)
		assert.Equal(t, "u1", got.UserID)
		assert.Equal(t, "team-a", got.TeamID)
		assert.Equal(t, "vk1", got.VirtualKeyID)
		assert.Equal(t, "c1", got.CustomerID)
		assert.Equal(t, "bu1", got.BusinessUnitID)
		assert.Equal(t, "openai", got.Provider)
		assert.Equal(t, "gpt-4o", got.Model)
		assert.Equal(t, "cursor", got.App)
		assert.Equal(t, "success", got.Status)
		assert.InDelta(t, 0.42, got.Cost, 1e-9)
		assert.InDelta(t, 310.5, got.Latency, 1e-9)
		assert.Equal(t, 99, got.TotalTokens)
		assert.Empty(t, got.TopicID, "the topic is the caller's to fill")
		assert.Zero(t, got.Similarity)

		empty := byID["log-bare"]
		assert.Equal(t, "error", empty.Status)
		assert.Empty(t, empty.SessionID)
		assert.Empty(t, empty.UserID)
		assert.Empty(t, empty.TeamID)
		assert.Empty(t, empty.VirtualKeyID)
		assert.Empty(t, empty.CustomerID)
		assert.Empty(t, empty.BusinessUnitID)
		assert.Empty(t, empty.App)
		assert.Zero(t, empty.Cost)
		assert.Zero(t, empty.Latency)

		// What comes back is what an assignment is written from.
		got.TopicID, got.Similarity = "billing", 0.9
		require.NoError(t, store.UpsertWarpTopicAssignments(ctx, []WarpTopicAssignment{got, empty}))
		usage, err := store.SummarizeWarpTopicAssignments(ctx, WarpTopicAssignmentFilter{TeamIDs: []string{"team-a"}})
		require.NoError(t, err)
		require.Len(t, usage, 1)
		assert.Equal(t, "billing", usage[0].TopicID)
		assert.InDelta(t, 0.42, usage[0].Cost, 1e-9)

		// Time bounds are inclusive and exact to the millisecond.
		rows, err = store.ListLogsForWarpTopics(ctx, []string{"log-full", "log-bare"}, base, base)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, "log-full", rows[0].LogID)
		rows, err = store.ListLogsForWarpTopics(ctx, []string{"log-full", "log-bare"}, base.Add(time.Millisecond), time.Time{})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, "log-bare", rows[0].LogID)

		// This read serves the job that assigns every request, so a scope on
		// the context does not narrow it.
		closed := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("1 = 0") })
		rows, err = store.ListLogsForWarpTopics(closed, []string{"log-full", "log-bare"}, time.Time{}, time.Time{})
		require.NoError(t, err)
		assert.Len(t, rows, 2)

		rows, err = store.ListLogsForWarpTopics(ctx, nil, time.Time{}, time.Time{})
		require.NoError(t, err)
		assert.Empty(t, rows)
	})
}

// More ids than one statement takes: the read is chunked, and nothing is lost
// at a chunk boundary.
func TestListLogsForWarpTopicsReadsPastOneChunk(t *testing.T) {
	forEachWarpTopicStore(t, func(t *testing.T, store LogStore) {
		ctx := context.Background()
		base := warpTopicTestTime()
		const total = warpTopicWriteBatch + 7

		logs := make([]*Log, 0, total)
		ids := make([]string, 0, total)
		for i := range total {
			id := fmt.Sprintf("bulk-%04d", i)
			ids = append(ids, id)
			logs = append(logs, &Log{
				ID: id, Timestamp: base.Add(time.Duration(i) * time.Millisecond), CreatedAt: base,
				Object: "chat.completion", Provider: "openai", Model: "gpt-4o", Status: "success",
			})
		}
		require.NoError(t, store.BatchCreateIfNotExists(ctx, logs))

		rows, err := store.ListLogsForWarpTopics(ctx, ids, time.Time{}, time.Time{})
		require.NoError(t, err)
		assert.ElementsMatch(t, ids, warpAssignmentLogIDs(rows))

		// And the same number of assignments and pool rows in one call each.
		for i := range rows {
			rows[i].TopicID = "billing"
		}
		require.NoError(t, store.UpsertWarpTopicAssignments(ctx, rows))
		usage, err := store.SummarizeWarpTopicAssignments(ctx, WarpTopicAssignmentFilter{})
		require.NoError(t, err)
		require.Len(t, usage, 1)
		assert.EqualValues(t, total, usage[0].Requests)

		pool := make([]WarpTopicUnmatched, 0, total)
		for i, id := range ids {
			pool = append(pool, warpTestUnmatched(id, "sig", base.Add(time.Duration(i)*time.Millisecond), []float32{float32(i), 1}))
		}
		require.NoError(t, store.AddWarpTopicUnmatched(ctx, pool))
		require.NoError(t, store.DeleteWarpTopicUnmatched(ctx, ids[:total-3]))
		count, err := store.CountWarpTopicUnmatched(ctx, "sig")
		require.NoError(t, err)
		assert.EqualValues(t, 3, count)
	})
}
