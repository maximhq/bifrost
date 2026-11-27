package warp

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/framework/sidekiq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// discoveryTestService is a service over a real log store with the pool
// seeded: perSubject requests on each of the given subjects, each subject a
// direction in a 3-dimensional space, named by what its requests ask.
func discoveryTestService(t *testing.T, chat ChatFunc) (*Service, *semanticLogReader) {
	t.Helper()
	reader := topicTestReader(t, map[string]logstore.Log{})
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: assignTestRow()}), WithLogReader(reader),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(assignTestEmbedding), WithChatFunc(chat),
	)
	t.Cleanup(service.Shutdown)
	return service, reader
}

// discoveryDimension is the embedding space the pooled vectors live in. Wider
// than the live-assignment tests' three, so a subject's requests spread the
// way real ones do: k-means splits the subject and the merge joins it again.
const discoveryDimension = 64

// discoverySubject is the direction a subject's requests sit around.
func discoverySubject(axis int) []float32 {
	return topicTestDirection(discoveryDimension, axis, axis, 0)
}

// poolSubject pools count requests about one subject, around its direction,
// and files each under no topic the way live assignment does. The logs are
// written too, for naming.
func poolSubject(t *testing.T, reader *semanticLogReader, prefix, question string, axis, count int, at time.Time) []string {
	t.Helper()
	ctx := context.Background()
	store := reader.topics.(logstore.LogStore)
	rng := rand.New(rand.NewSource(int64(axis)))
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	ids := make([]string, 0, count)
	var pooled []logstore.WarpTopicUnmatched
	var filed []logstore.WarpTopicAssignment
	points := topicTestPoints(rng, [][]float32{discoverySubject(axis)}, count, 0.5)
	for index := range count {
		id := fmt.Sprintf("%s-%03d", prefix, index)
		ids = append(ids, id)
		vector := points[index]
		when := at.Add(time.Duration(index) * time.Minute)
		team := "team-" + prefix
		entry := logstore.Log{
			ID: id, Timestamp: when, CreatedAt: when, Object: string(schemas.ChatCompletionRequest), Status: "success",
			Provider: "openai", Model: "gpt-4o", TeamID: &team, ContentSummary: question,
			InputHistoryParsed: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &question}}},
		}
		require.NoError(t, store.Create(ctx, &entry))
		reader.logs[id] = entry
		pooled = append(pooled, logstore.WarpTopicUnmatched{LogID: id, Timestamp: when, LogCreatedAt: when, Vector: logstore.EncodeWarpVector(vector), EmbeddingSignature: signature})
		row := logstore.WarpTopicAssignmentFromLog(&entry)
		filed = append(filed, row)
	}
	require.NoError(t, store.AddWarpTopicUnmatched(ctx, pooled))
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, filed))
	return ids
}

func runDiscovery(t *testing.T, service *Service) BackfillJobMeta {
	t.Helper()
	ctx := context.Background()
	metaJSON, err := service.BuildDiscoveryJobMeta(ctx)
	require.NoError(t, err)
	finalJSON, err := service.RunDiscoveryJob(ctx, tables.TableSidekiqJob{ID: "discovery-1", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	return final
}

func activeTopics(t *testing.T, store logstore.WarpTopicStore) map[string]logstore.WarpTopic {
	t.Helper()
	rows, err := store.ListWarpTopics(context.Background(), logstore.WarpTopicFilter{Statuses: []string{logstore.WarpTopicStatusActive}})
	require.NoError(t, err)
	byLabel := map[string]logstore.WarpTopic{}
	for _, row := range rows {
		byLabel[row.Label] = row
	}
	return byLabel
}

// Discovery clusters the pool. A cluster large enough becomes a topic, named
// like a run names them, and its requests are filed under it and leave the
// pool. A cluster too small to be a pattern stays pooled for next time.
func TestWarpDiscoveryMakesTopicsOfThePool(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	store := reader.topics
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour)
	refunds := poolSubject(t, reader, "refund", "how do I get a refund", 0, 60, at)
	poolSubject(t, reader, "password", "reset my password", 1, 60, at)
	few := poolSubject(t, reader, "weather", "what is the weather", 2, 3, at)

	final := runDiscovery(t, service)
	require.Equal(t, 123, final.Scanned)
	require.Equal(t, 2, final.Indexed, "two clusters were large enough")
	// A few of a subject's own can land in a fragment too small to keep, and
	// stay pooled with the three; every request is one or the other.
	require.InDelta(t, 120, final.Assigned, 6)
	require.Equal(t, 123, final.Assigned+final.Unassigned)
	require.GreaterOrEqual(t, final.Unassigned, 3, "the three stay pooled")
	require.Zero(t, final.Merged)
	require.Contains(t, final.Message, "2 new topic(s)")
	require.Contains(t, final.Message, "stay pooled")

	topics := activeTopics(t, store)
	require.Len(t, topics, 2)
	require.Contains(t, topics, "Refund requests")
	require.Contains(t, topics, "Password resets")
	for label, topic := range topics {
		require.Equal(t, embeddingConfigSignature(configFromRow(assignTestRow())), topic.EmbeddingSignature, label)
		// The samples nearest the centre and the members drawn, some of which
		// may be the same request.
		require.Greater(t, len(topic.ExampleLogIDs()), topicsLabelSamples, "%s keeps its drawn members, for a later merge to name from", label)
		require.LessOrEqual(t, len(topic.ExampleLogIDs()), topicsLabelSamples+topicsLabelDrawn, label)
		require.Greater(t, topic.Cohesion, 0.8, label)
		require.Greater(t, topic.AssignFloor, 0.5, label)
	}

	usage, err := store.SummarizeWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{})
	require.NoError(t, err)
	counts := map[string]int64{}
	for _, entry := range usage {
		counts[entry.TopicID] = entry.Requests
	}
	require.InDelta(t, 60, counts[topics["Refund requests"].ID], 6)
	require.InDelta(t, 60, counts[topics["Password resets"].ID], 6)
	require.EqualValues(t, final.Unassigned, counts[""], "what stayed pooled is still filed under no topic")
	filed, err := store.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{LogIDs: refunds[:1]}, 1)
	require.NoError(t, err)
	require.Len(t, filed, 1)
	require.Equal(t, "team-refund", filed[0].TeamID, "the row kept its columns")
	require.Greater(t, filed[0].Similarity, 0.8)

	pooled, err := store.ListWarpTopicUnmatched(ctx, embeddingConfigSignature(configFromRow(assignTestRow())), 100)
	require.NoError(t, err)
	require.Subset(t, warpUnmatchedIDs(pooled), few)
	require.Len(t, pooled, final.Unassigned)

	// The tool sees them at once.
	listed, total := listAllTopics(t, NewTopicReader(&recordingStore{row: assignTestRow()}, store))
	require.Len(t, listed, 2)
	require.Equal(t, 123, total)
}

// A pool too small to be worth the model calls is left alone, and said so.
func TestWarpDiscoveryWaitsForAPoolWorthClustering(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	poolSubject(t, reader, "refund", "how do I get a refund", 0, 20, time.Now().UTC().Add(-time.Hour))
	final := runDiscovery(t, service)
	require.Equal(t, 20, final.Scanned)
	require.Zero(t, final.Indexed)
	require.Contains(t, final.Message, "20 request(s) are waiting for a topic")
	require.Empty(t, activeTopics(t, reader.topics))
}

// A discovered cluster that is an existing topic - by its centre, or by the
// name the model gives it - is folded into that topic rather than standing
// beside it as a second one.
func TestWarpDiscoveryFoldsClustersIntoExistingTopics(t *testing.T) {
	var namedRefunds atomic.Int32
	service, reader := discoveryTestService(t, func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		if contains(responsesInputText(req.Input), "refund") {
			namedRefunds.Add(1)
		}
		return topicTestNaming(ctx, req)
	})
	store := reader.topics
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour)
	// Refunds already have a topic, with a floor so high that pooled refund
	// requests still fell under it; passwords have a topic by name only,
	// whose centre points somewhere else entirely.
	seedAssignTopic(t, store, "existing-refunds", discoverySubject(0), 0.9999)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{{ID: "named-passwords", Label: "password   RESETS", Centroid: logstore.EncodeWarpVector(discoverySubject(2)), EmbeddingSignature: embeddingConfigSignature(configFromRow(assignTestRow()))}}))
	poolSubject(t, reader, "refund", "how do I get a refund", 0, 60, at)
	poolSubject(t, reader, "password", "reset my password", 1, 60, at)

	final := runDiscovery(t, service)
	require.Zero(t, final.Indexed, "nothing new: both clusters belong to topics that exist")
	require.Equal(t, 2, final.Merged)
	require.InDelta(t, 120, final.Assigned, 6)
	require.Equal(t, 120, final.Assigned+final.Unassigned)
	require.Contains(t, final.Message, "0 new topic(s), 2 cluster(s) folded into existing topics")

	usage, err := store.SummarizeWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{})
	require.NoError(t, err)
	counts := map[string]int64{}
	for _, entry := range usage {
		counts[entry.TopicID] = entry.Requests
	}
	require.InDelta(t, 60, counts["existing-refunds"], 6, "by centre")
	require.InDelta(t, 60, counts["named-passwords"], 6, "by name, whatever its centre")
	require.EqualValues(t, final.Unassigned, counts[""])
	count, err := store.CountWarpTopicUnmatched(ctx, embeddingConfigSignature(configFromRow(assignTestRow())))
	require.NoError(t, err)
	require.EqualValues(t, final.Unassigned, count, "filed requests leave the pool")
	require.Zero(t, namedRefunds.Load(), "a cluster that is an existing topic by centre needs no name")

	// The topics a cluster was folded into now describe what they hold: the
	// centre moves towards the requests filed under it, and the cohesion is
	// theirs rather than the zero the topic was written with.
	topics, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{IDs: []string{"existing-refunds", "named-passwords"}})
	require.NoError(t, err)
	require.Len(t, topics, 2)
	for _, topic := range topics {
		centre, err := logstore.DecodeWarpVector(topic.Centroid)
		require.NoError(t, err)
		require.Greater(t, topic.Cohesion, 0.8, topic.ID)
		if topic.ID == "named-passwords" {
			require.Greater(t, dot(centre, discoverySubject(1)), float32(0.9), "pulled onto the password requests")
		}
	}
}

// Upkeep retires a topic whose traffic stopped, but not a young one that has
// not had its chance, and retires outright one built with another embedding
// model.
func TestWarpUpkeepRetiresSilentTopics(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	store := reader.topics
	ctx := context.Background()
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	now := time.Now().UTC()
	old := now.Add(-2 * topicsRetireAfter)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "busy", Label: "Busy", Centroid: logstore.EncodeWarpVector([]float32{1, 0, 0}), EmbeddingSignature: signature, CreatedAt: old},
		{ID: "silent", Label: "Silent", Centroid: logstore.EncodeWarpVector([]float32{0, 1, 0}), EmbeddingSignature: signature, CreatedAt: old},
		{ID: "young", Label: "Young", Centroid: logstore.EncodeWarpVector([]float32{0, 0, 1}), EmbeddingSignature: signature, CreatedAt: now.Add(-time.Hour)},
		{ID: "other-model", Label: "Other", Centroid: logstore.EncodeWarpVector([]float32{1, 0, 0}), EmbeddingSignature: "another model", CreatedAt: now.Add(-time.Hour)},
	}))
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, []logstore.WarpTopicAssignment{
		{LogID: "busy-now", TopicID: "busy", Timestamp: now.Add(-time.Hour)},
		{LogID: "silent-then", TopicID: "silent", Timestamp: old},
		{LogID: "other-now", TopicID: "other-model", Timestamp: now.Add(-time.Hour)},
	}))

	done, err := service.MaintainTopics(ctx)
	require.NoError(t, err)
	require.Equal(t, TopicUpkeep{Retired: 2}, done)
	topics := activeTopics(t, store)
	require.Len(t, topics, 2)
	require.Contains(t, topics, "Busy")
	require.Contains(t, topics, "Young")

	all, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{IDs: []string{"silent", "other-model"}})
	require.NoError(t, err)
	for _, topic := range all {
		require.Equal(t, logstore.WarpTopicStatusRetired, topic.Status, topic.ID)
	}
	done, err = service.MaintainTopics(ctx)
	require.NoError(t, err)
	require.Equal(t, TopicUpkeep{}, done, "a second pass finds nothing to do")
}

// Upkeep tidies what nothing reads: a pooled request embedded by another
// model is dropped, a merged topic whose survivor was retired is retired too,
// and a row retired for longer than the grace period is deleted - but not
// before, so a reader in flight still finds the row an id names.
func TestWarpUpkeepPrunesWhatNothingReads(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	store := reader.topics
	ctx := context.Background()
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	now := time.Now().UTC()
	old := now.Add(-2 * topicsRetireAfter)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "busy", Label: "Busy", Centroid: logstore.EncodeWarpVector([]float32{1, 0, 0}), EmbeddingSignature: signature, CreatedAt: old},
		{ID: "silent", Label: "Silent", Centroid: logstore.EncodeWarpVector([]float32{0, 1, 0}), EmbeddingSignature: signature, CreatedAt: old},
		{ID: "into-silent", Label: "Also silent", Centroid: logstore.EncodeWarpVector([]float32{0, 1, 0}), EmbeddingSignature: signature, Status: logstore.WarpTopicStatusMerged, MergedInto: "silent"},
		{ID: "into-busy", Label: "Busy too", Centroid: logstore.EncodeWarpVector([]float32{1, 0, 0}), EmbeddingSignature: signature, Status: logstore.WarpTopicStatusMerged, MergedInto: "busy"},
		{ID: "long-gone", Label: "Long gone", Centroid: logstore.EncodeWarpVector([]float32{0, 0, 1}), EmbeddingSignature: signature, Status: logstore.WarpTopicStatusRetired},
	}))
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, []logstore.WarpTopicAssignment{
		{LogID: "busy-now", TopicID: "busy", Timestamp: now.Add(-time.Hour)},
		{LogID: "silent-then", TopicID: "silent", Timestamp: old},
	}))
	require.NoError(t, store.AddWarpTopicUnmatched(ctx, []logstore.WarpTopicUnmatched{
		{LogID: "old-model", Timestamp: now, LogCreatedAt: now, Vector: logstore.EncodeWarpVector([]float32{1, 0, 0}), EmbeddingSignature: "another model"},
		{LogID: "this-model", Timestamp: now, LogCreatedAt: now, Vector: logstore.EncodeWarpVector([]float32{1, 0, 0}), EmbeddingSignature: signature},
	}))

	// Within the grace period nothing retired is deleted yet.
	done, err := service.MaintainTopics(ctx)
	require.NoError(t, err)
	require.Equal(t, TopicUpkeep{Retired: 1, Dropped: 1}, done)
	rows := topicsByID(t, store, logstore.WarpTopicKindTopic)
	require.Len(t, rows, 5, "retired rows are kept for the grace period")
	require.Equal(t, logstore.WarpTopicStatusRetired, rows["silent"].Status)
	require.Equal(t, logstore.WarpTopicStatusRetired, rows["into-silent"].Status, "merged into a retired topic, so retired too")
	require.Equal(t, logstore.WarpTopicStatusMerged, rows["into-busy"].Status, "its survivor is still active")
	pooled, err := store.ListWarpTopicUnmatched(ctx, signature, 10)
	require.NoError(t, err)
	require.Len(t, pooled, 1, "the other model's request was dropped")
	gone, err := store.CountWarpTopicUnmatched(ctx, "another model")
	require.NoError(t, err)
	require.Zero(t, gone)

	// Past it, they go; what is still read stays.
	service.topicLimits.pruneGrace = time.Nanosecond
	time.Sleep(2 * time.Millisecond)
	done, err = service.MaintainTopics(ctx)
	require.NoError(t, err)
	require.Equal(t, TopicUpkeep{Pruned: 3}, done, "silent, into-silent and long-gone")
	rows = topicsByID(t, store, logstore.WarpTopicKindTopic)
	require.Len(t, rows, 2)
	require.Contains(t, rows, "busy")
	require.Contains(t, rows, "into-busy")
}

// Topic health is what the settings card and the index status show: how
// many topics and themes there are to file under, how many requests wait in
// the pool, and how much of the last day matched nothing.
func TestWarpTopicHealthReadsLiveFiling(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	store := reader.topics
	ctx := context.Background()
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	now := time.Now().UTC()

	health, err := service.TopicHealth(ctx)
	require.NoError(t, err)
	require.Equal(t, TopicHealth{}, health, "nothing built, nothing filed")

	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "money", Label: "Money", Kind: logstore.WarpTopicKindTheme, EmbeddingSignature: signature},
		{ID: "refunds", Label: "Refunds", ParentID: "money", EmbeddingSignature: signature},
		{ID: "passwords", Label: "Passwords", EmbeddingSignature: signature},
		{ID: "retired", Label: "Retired", EmbeddingSignature: signature, Status: logstore.WarpTopicStatusRetired},
		{ID: "other-model", Label: "Other", EmbeddingSignature: "another model"},
	}))
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, []logstore.WarpTopicAssignment{
		{LogID: "r-1", TopicID: "refunds", Timestamp: now.Add(-time.Hour)},
		{LogID: "r-2", TopicID: "refunds", Timestamp: now.Add(-2 * time.Hour)},
		{LogID: "p-1", TopicID: "passwords", Timestamp: now.Add(-time.Hour)},
		{LogID: "n-1", TopicID: "", Timestamp: now.Add(-time.Hour)},
		{LogID: "n-old", TopicID: "", Timestamp: now.Add(-48 * time.Hour)},
	}))
	require.NoError(t, store.AddWarpTopicUnmatched(ctx, []logstore.WarpTopicUnmatched{
		{LogID: "n-1", Timestamp: now, LogCreatedAt: now, Vector: logstore.EncodeWarpVector([]float32{1, 0, 0}), EmbeddingSignature: signature},
		{LogID: "n-other", Timestamp: now, LogCreatedAt: now, Vector: logstore.EncodeWarpVector([]float32{1, 0, 0}), EmbeddingSignature: "another model"},
	}))
	health, err = service.TopicHealth(ctx)
	require.NoError(t, err)
	require.Equal(t, TopicHealth{ActiveTopics: 2, Themes: 1, Pooled: 1, RequestsDay: 4, UnmatchedDay: 1, UnmatchedShare: 0.25}, health)

	// The topic and pool counts cover the whole deployment, and the pool has no
	// owner to scope by. A caller whose reads are narrowed gets none of it,
	// rather than their own day's requests beside the deployment's topics.
	scoped := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("user_id = ?", "someone") })
	_, err = service.TopicHealth(scoped)
	require.ErrorIs(t, err, ErrUnavailable, "restricted through the context's scope")
	service.callerRestriction = func(context.Context) CallerRestriction { return CallerRestriction{Restricted: true} }
	_, err = service.TopicHealth(ctx)
	require.ErrorIs(t, err, ErrUnavailable, "restricted through the deployment's resolver")
}

// A pass that changes the topics regroups them into themes; one that changes
// nothing leaves the themes alone.
func TestWarpUpkeepRegroupsThemesWhenTopicsChange(t *testing.T) {
	service, reader := discoveryTestService(t, themeTestChat(money))
	store := reader.topics
	ctx := context.Background()
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	now := time.Now().UTC()
	old := now.Add(-2 * topicsRetireAfter)
	// Five topics far apart, so nothing merges; one silent for long enough
	// to retire.
	var rows []logstore.WarpTopic
	for axis, label := range []string{"Refund requests", "Invoice questions", "Password resets", "Login failures", "Weather"} {
		vector := make([]float32, 8)
		vector[axis] = 1
		rows = append(rows, logstore.WarpTopic{
			ID: fmt.Sprintf("t-%d", axis), Label: label, Centroid: logstore.EncodeWarpVector(vector), Cohesion: 0.98,
			EmbeddingSignature: signature, CreatedAt: old,
		})
	}
	require.NoError(t, store.UpsertWarpTopics(ctx, rows))
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, []logstore.WarpTopicAssignment{
		{LogID: "r-0", TopicID: "t-0", Timestamp: now.Add(-time.Hour)},
		{LogID: "r-1", TopicID: "t-1", Timestamp: now.Add(-time.Hour)},
		{LogID: "r-2", TopicID: "t-2", Timestamp: now.Add(-time.Hour)},
		{LogID: "r-3", TopicID: "t-3", Timestamp: now.Add(-time.Hour)},
		{LogID: "r-4", TopicID: "t-4", Timestamp: old},
	}))

	done, err := service.MaintainTopics(ctx)
	require.NoError(t, err)
	require.Equal(t, TopicUpkeep{Retired: 1, Themes: 2}, done, "Weather retired; the rest grouped")
	themes := topicsByID(t, store, logstore.WarpTopicKindTheme)
	require.Len(t, themes, 2)
	for _, theme := range themes {
		require.Equal(t, signature, theme.EmbeddingSignature)
	}

	done, err = service.MaintainTopics(ctx)
	require.NoError(t, err)
	require.Equal(t, TopicUpkeep{}, done, "nothing changed, nothing regrouped")
}

// Two topics that have come to mean the same thing are merged, the smaller
// into the larger, and the requests filed under the smaller still count
// under the larger when the tool lists it.
func TestWarpUpkeepMergesTopicsThatMeanTheSameThing(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	store := reader.topics
	ctx := context.Background()
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	threshold := 0.98
	service.backfillJobs = seedRun(t, tables.SidekiqStatusCompleted, signature, &threshold)
	now := time.Now().UTC()
	tight := func(axis int, lean float32) []float32 {
		vector := []float32{0, 0, 0}
		vector[axis] = 1
		vector[(axis+1)%3] = lean
		return unitVector(vector)
	}
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "big", Label: "Refund requests", Centroid: logstore.EncodeWarpVector(tight(0, 0)), Cohesion: 0.98, AssignFloor: 0.9, EmbeddingSignature: signature},
		{ID: "twin", Label: "Refunds", Centroid: logstore.EncodeWarpVector(tight(0, 0.05)), Cohesion: 0.98, AssignFloor: 0.9, EmbeddingSignature: signature},
		{ID: "namesake", Label: "  refund REQUESTS ", Centroid: logstore.EncodeWarpVector(tight(2, 0)), Cohesion: 0.98, EmbeddingSignature: signature},
		{ID: "apart", Label: "Password resets", Centroid: logstore.EncodeWarpVector(tight(1, 0)), Cohesion: 0.98, EmbeddingSignature: signature},
		// Merged into twin by an earlier pass. When twin merges into big it
		// has to follow, so no merged topic ever points at a merged topic.
		{ID: "earlier", Label: "Refund", Centroid: logstore.EncodeWarpVector(tight(0, 0.1)), Cohesion: 0.98, EmbeddingSignature: signature, Status: logstore.WarpTopicStatusMerged, MergedInto: "twin"},
	}))
	var rows []logstore.WarpTopicAssignment
	file := func(topic string, count int) {
		for index := range count {
			rows = append(rows, logstore.WarpTopicAssignment{LogID: fmt.Sprintf("%s-%d", topic, index), TopicID: topic, Timestamp: now.Add(-time.Hour)})
		}
	}
	file("big", 30)
	file("twin", 10)
	file("namesake", 5)
	file("apart", 20)
	file("earlier", 3)
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, rows))

	done, err := service.MaintainTopics(ctx)
	require.NoError(t, err)
	require.Equal(t, TopicUpkeep{Merged: 2}, done)
	topics := activeTopics(t, store)
	require.Len(t, topics, 2)
	require.Contains(t, topics, "Refund requests")
	require.Contains(t, topics, "Password resets")
	survivor := topics["Refund requests"]
	require.Equal(t, "big", survivor.ID, "the larger survives")
	require.InDelta(t, 0.9, survivor.AssignFloor, 1e-9, "and keeps its floor")
	require.Less(t, survivor.Cohesion, 0.98, "its cohesion now covers what it absorbed")

	for _, id := range []string{"twin", "namesake", "earlier"} {
		merged, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{IDs: []string{id}})
		require.NoError(t, err)
		require.Len(t, merged, 1)
		require.Equal(t, logstore.WarpTopicStatusMerged, merged[0].Status, id)
		require.Equal(t, "big", merged[0].MergedInto, id)
		require.NotEmpty(t, merged[0].Centroid, "%s is written back whole", id)
	}

	listed, total := listAllTopics(t, NewTopicReader(&recordingStore{row: assignTestRow()}, store))
	require.Len(t, listed, 2)
	require.Equal(t, 68, total)
	require.Equal(t, "Refund requests", listed[0].Label)
	require.Equal(t, 48, int(listed[0].Requests), "30 of its own, 10, 5 and 3 filed under the topics merged into it")
	require.Equal(t, 20, int(listed[1].Requests))

	// Its series folds them in the same way, bucket by bucket.
	query := allTimeTopicQuery(15)
	query.BucketSeconds = 365 * 86400
	list, err := NewTopicReader(&recordingStore{row: assignTestRow()}, store).ListTopics(ctx, query)
	require.NoError(t, err)
	var inSeries int64
	for _, point := range list.Topics[0].Series {
		inSeries += point.Requests
	}
	require.EqualValues(t, 48, inSeries)

	// A topic merged into one that is later merged itself still resolves.
	survivor.Status, survivor.MergedInto = logstore.WarpTopicStatusMerged, "apart"
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{survivor}))
	listed, total = listAllTopics(t, NewTopicReader(&recordingStore{row: assignTestRow()}, store))
	require.Len(t, listed, 1)
	require.Equal(t, 68, total)
	require.Equal(t, 68, int(listed[0].Requests))
}

// A topic merged with topics of other names is named again, from examples of
// all of them and with their names as hints. Kept under its old name, a
// topic of coding sessions went on being listed as one session's subject
// after three topics about docs, releases and a desktop app were merged into
// it.
func TestWarpUpkeepRenamesTopicsMergedFromOtherNames(t *testing.T) {
	var mu sync.Mutex
	var prompts []string
	chat := func(_ context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		text := responsesInputText(req.Input)
		mu.Lock()
		prompts = append(prompts, text)
		mu.Unlock()
		if strings.Contains(text, "Topics:") {
			return TextTurn(`{"themes":[]}`), nil
		}
		return TextTurn("Gateway Engineering Work"), nil
	}
	service, reader := discoveryTestService(t, chat)
	store := reader.topics
	ctx := context.Background()
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	threshold := 0.98
	service.backfillJobs = seedRun(t, tables.SidekiqStatusCompleted, signature, &threshold)
	now := time.Now().UTC()
	near := func(lean float32) []float32 { return unitVector([]float32{1, lean, 0}) }
	topic := func(id, label string, lean float32, examples ...string) logstore.WarpTopic {
		row := logstore.WarpTopic{ID: id, Label: label, Centroid: logstore.EncodeWarpVector(near(lean)), Cohesion: 0.98, EmbeddingSignature: signature}
		require.NoError(t, row.SetExampleLogIDs(examples))
		for _, example := range examples {
			reader.logs[example] = logstore.Log{ID: example, ContentSummary: "question from " + example}
		}
		return row
	}
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		topic("big", "Graviton Token Compression", 0, "big-a", "big-b", "big-c", "big-d"),
		topic("docs", "Documentation Updates", 0.05, "docs-a", "docs-b", "docs-c", "docs-d"),
		{ID: "apart", Label: "Password resets", Centroid: logstore.EncodeWarpVector([]float32{0, 0, 1}), Cohesion: 0.98, EmbeddingSignature: signature},
	}))
	var rows []logstore.WarpTopicAssignment
	for topic, count := range map[string]int{"big": 30, "docs": 10, "apart": 20} {
		for index := range count {
			rows = append(rows, logstore.WarpTopicAssignment{LogID: fmt.Sprintf("%s-%d", topic, index), TopicID: topic, Timestamp: now.Add(-time.Hour)})
		}
	}
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, rows))

	done, err := service.MaintainTopics(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, done.Merged)
	topics := activeTopics(t, store)
	require.Contains(t, topics, "Gateway Engineering Work", "the survivor is named for what it now holds")
	require.Contains(t, topics, "Password resets", "a topic no merge touched keeps its name")
	survivor := topics["Gateway Engineering Work"]
	require.Equal(t, "big", survivor.ID)
	examples := strings.Join(survivor.ExampleLogIDs(), ",")
	require.Contains(t, examples, "big-")
	require.Contains(t, examples, "docs-", "its examples now span the topics merged")

	var naming string
	for _, prompt := range prompts {
		if strings.Contains(prompt, "Topic name:") {
			naming = prompt
		}
	}
	require.Contains(t, naming, "Graviton Token Compression; Documentation Updates")
	require.Contains(t, naming, "question from big-a")
	require.Contains(t, naming, "question from docs-a")
}

// A model that hangs must cost the merged topics their new names and nothing
// else. Naming ran before the merges were written and shared the pass's
// deadline, so a model that never answered left the write an expired
// context: the merges and retirements were lost and the next pass asked the
// same model again.
func TestWarpUpkeepWritesMergesWhenNamingHangs(t *testing.T) {
	chat := func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		if strings.Contains(responsesInputText(req.Input), "Topics:") {
			return TextTurn(`{"themes":[]}`), nil
		}
		<-ctx.Done()
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "model did not answer"}}
	}
	service, reader := discoveryTestService(t, chat)
	store := reader.topics
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	threshold := 0.98
	service.backfillJobs = seedRun(t, tables.SidekiqStatusCompleted, signature, &threshold)
	now := time.Now().UTC()
	topic := func(id, label string, lean float32) logstore.WarpTopic {
		row := logstore.WarpTopic{ID: id, Label: label, Centroid: logstore.EncodeWarpVector(unitVector([]float32{1, lean, 0})), Cohesion: 0.98, EmbeddingSignature: signature}
		require.NoError(t, row.SetExampleLogIDs([]string{id + "-example"}))
		reader.logs[id+"-example"] = logstore.Log{ID: id + "-example", ContentSummary: "question about " + label}
		return row
	}
	require.NoError(t, store.UpsertWarpTopics(context.Background(), []logstore.WarpTopic{
		topic("big", "Graviton Token Compression", 0), topic("docs", "Documentation Updates", 0.05),
	}))
	var rows []logstore.WarpTopicAssignment
	for topic, count := range map[string]int{"big": 30, "docs": 10} {
		for index := range count {
			rows = append(rows, logstore.WarpTopicAssignment{LogID: fmt.Sprintf("%s-%d", topic, index), TopicID: topic, Timestamp: now.Add(-time.Hour)})
		}
	}
	require.NoError(t, store.UpsertWarpTopicAssignments(context.Background(), rows))

	// The pass has a second past the time it keeps back for the themes, so
	// naming gets a second however long its own budget.
	ctx, cancel := context.WithTimeout(context.Background(), topicsGroupTimeout+time.Second)
	defer cancel()
	started := time.Now()
	done, err := service.MaintainTopics(ctx)
	require.NoError(t, err)
	require.Less(t, time.Since(started), 30*time.Second, "naming gave up at its budget")
	require.Equal(t, 1, done.Merged)
	merged, err := store.ListWarpTopics(context.Background(), logstore.WarpTopicFilter{IDs: []string{"docs"}})
	require.NoError(t, err)
	require.Len(t, merged, 1)
	require.Equal(t, logstore.WarpTopicStatusMerged, merged[0].Status, "the merge is written")
	require.Contains(t, activeTopics(t, store), "Graviton Token Compression", "the survivor keeps its name")
}

// renameTestTopics writes a large topic and a smaller one alike enough to be
// merged into it at the seed's threshold, each with one example to name from.
func renameTestTopics(t *testing.T, service *Service, reader *semanticLogReader) {
	t.Helper()
	store := reader.topics
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	threshold := 0.98
	service.backfillJobs = seedRun(t, tables.SidekiqStatusCompleted, signature, &threshold)
	topic := func(id, label string, lean float32) logstore.WarpTopic {
		row := logstore.WarpTopic{ID: id, Label: label, Centroid: logstore.EncodeWarpVector(unitVector([]float32{1, lean, 0})), Cohesion: 0.98, EmbeddingSignature: signature}
		require.NoError(t, row.SetExampleLogIDs([]string{id + "-example"}))
		reader.logs[id+"-example"] = logstore.Log{ID: id + "-example", ContentSummary: "question about " + label}
		return row
	}
	require.NoError(t, store.UpsertWarpTopics(context.Background(), []logstore.WarpTopic{
		topic("big", "Graviton Token Compression", 0), topic("docs", "Documentation Updates", 0.05),
	}))
	now := time.Now().UTC()
	var rows []logstore.WarpTopicAssignment
	for topic, count := range map[string]int{"big": 30, "docs": 10} {
		for index := range count {
			rows = append(rows, logstore.WarpTopicAssignment{LogID: fmt.Sprintf("%s-%d", topic, index), TopicID: topic, Timestamp: now.Add(-time.Hour)})
		}
	}
	require.NoError(t, store.UpsertWarpTopicAssignments(context.Background(), rows))
}

// A model whose answer holds no name leaves the survivor its name. Renaming
// through labelTopic put a name made of the examples' common words in its
// place, which is what renaming a merged topic is meant to avoid.
func TestWarpUpkeepKeepsTheNameWhenTheModelGivesNone(t *testing.T) {
	chat := func(_ context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		if strings.Contains(responsesInputText(req.Input), "Topics:") {
			return TextTurn(`{"themes":[]}`), nil
		}
		return TextTurn("   "), nil
	}
	service, reader := discoveryTestService(t, chat)
	renameTestTopics(t, service, reader)

	done, err := service.MaintainTopics(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, done.Merged)
	topics := activeTopics(t, reader.topics)
	require.Len(t, topics, 1)
	require.Contains(t, topics, "Graviton Token Compression")
}

// Another instance's upkeep can change a survivor while this one waits for its
// new name. The rename must not write back the copy it took before naming: a
// topic the other instance merged away came back active.
func TestWarpUpkeepRenameDoesNotUndoAnotherInstance(t *testing.T) {
	var reader *semanticLogReader
	chat := func(_ context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		if strings.Contains(responsesInputText(req.Input), "Topics:") {
			return TextTurn(`{"themes":[]}`), nil
		}
		// While the name is being written, another instance merges the
		// survivor into a topic of its own.
		rows, err := reader.topics.ListWarpTopics(context.Background(), logstore.WarpTopicFilter{IDs: []string{"big"}})
		if err != nil || len(rows) != 1 {
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "read survivor"}}
		}
		rows[0].Status, rows[0].MergedInto = logstore.WarpTopicStatusMerged, "elsewhere"
		if err := reader.topics.UpsertWarpTopics(context.Background(), rows); err != nil {
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: err.Error()}}
		}
		return TextTurn("Gateway Engineering Work"), nil
	}
	service, topicsReader := discoveryTestService(t, chat)
	reader = topicsReader
	renameTestTopics(t, service, reader)
	// The topic the other instance merges into: live, and like neither.
	require.NoError(t, reader.topics.UpsertWarpTopics(context.Background(), []logstore.WarpTopic{{
		ID: "elsewhere", Label: "Password resets", Centroid: logstore.EncodeWarpVector([]float32{0, 0, 1}), Cohesion: 0.98,
		EmbeddingSignature: embeddingConfigSignature(configFromRow(assignTestRow())),
	}}))

	done, err := service.MaintainTopics(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, done.Merged)
	rows, err := reader.topics.ListWarpTopics(context.Background(), logstore.WarpTopicFilter{IDs: []string{"big"}})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, logstore.WarpTopicStatusMerged, rows[0].Status, "the other instance's merge stands")
	require.Equal(t, "elsewhere", rows[0].MergedInto)
	require.Equal(t, "Graviton Token Compression", rows[0].Label, "nor is it renamed over the other instance's row")
}

// Upkeep merges at the threshold the seed run merged at, not one it derives
// from the stored topics. Those are already merged and looser than the
// clusters a run measures, so a derived threshold sat below the seed's and
// upkeep merged, within the hour, four topics about different coding work
// that the seed had just kept apart.
func TestWarpUpkeepKeepsApartWhatTheSeedKeptApart(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	store := reader.topics
	ctx := context.Background()
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	threshold := 0.95
	service.backfillJobs = seedRun(t, tables.SidekiqStatusCompleted, signature, &threshold)
	now := time.Now().UTC()
	// Four topics leaning off one shared direction: each pair about 0.89
	// alike, below the seed's 0.95 but far above their own cohesion of 0.7.
	var topics []logstore.WarpTopic
	var rows []logstore.WarpTopicAssignment
	for index, label := range []string{"Graviton Token Compression", "Documentation Updates", "Release Delay Management", "Claude Desktop Troubleshooting"} {
		vector := make([]float32, 5)
		vector[0], vector[index+1] = 1, 0.35
		id := fmt.Sprintf("t-%d", index)
		topics = append(topics, logstore.WarpTopic{ID: id, Label: label, Centroid: logstore.EncodeWarpVector(unitVector(vector)), Cohesion: 0.7, EmbeddingSignature: signature})
		for request := range 10 * (index + 1) {
			rows = append(rows, logstore.WarpTopicAssignment{LogID: fmt.Sprintf("%s-%d", id, request), TopicID: id, Timestamp: now.Add(-time.Hour)})
		}
	}
	require.NoError(t, store.UpsertWarpTopics(ctx, topics))
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, rows))

	done, err := service.MaintainTopics(ctx)
	require.NoError(t, err)
	require.Zero(t, done.Merged, "none is as alike as the seed required")
	require.Len(t, activeTopics(t, store), 4)

	done, err = service.MaintainTopics(ctx)
	require.NoError(t, err)
	require.Zero(t, done.Merged, "nor on the next pass")
}

// Without the threshold of a seed run that finished over this embedding
// model, upkeep does not guess one: it merges topics by name and nothing
// else.
func TestWarpUpkeepMergesByNameAloneWithoutASeedThreshold(t *testing.T) {
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	threshold := 0.5
	for name, jobs := range map[string]BackfillJobStore{
		"no job store":          nil,
		"no run":                seedRunJobs{},
		"run failed":            seedRun(t, tables.SidekiqStatusFailed, signature, &threshold),
		"run cancelled":         seedRun(t, tables.SidekiqStatusCancelled, signature, &threshold),
		"other embedding model": seedRun(t, tables.SidekiqStatusCompleted, "other", &threshold),
		"run derived none":      seedRun(t, tables.SidekiqStatusCompleted, signature, nil),
		"metadata unreadable":   seedRunJobs{latest: &tables.TableSidekiqJob{Kind: TopicsJobKind, Status: tables.SidekiqStatusCompleted, Metadata: "{"}},
	} {
		t.Run(name, func(t *testing.T) {
			service, reader := discoveryTestService(t, topicTestNaming)
			service.backfillJobs = jobs
			store := reader.topics
			ctx := context.Background()
			now := time.Now().UTC()
			require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
				{ID: "a", Label: "Refund requests", Centroid: logstore.EncodeWarpVector([]float32{1, 0, 0}), Cohesion: 0.9, EmbeddingSignature: signature},
				{ID: "b", Label: "Refunds", Centroid: logstore.EncodeWarpVector(unitVector([]float32{1, 0.05, 0})), Cohesion: 0.9, EmbeddingSignature: signature},
				{ID: "c", Label: " refund requests", Centroid: logstore.EncodeWarpVector([]float32{0, 0, 1}), Cohesion: 0.9, EmbeddingSignature: signature},
			}))
			var rows []logstore.WarpTopicAssignment
			for topic, count := range map[string]int{"a": 30, "b": 10, "c": 5} {
				for index := range count {
					rows = append(rows, logstore.WarpTopicAssignment{LogID: fmt.Sprintf("%s-%d", topic, index), TopicID: topic, Timestamp: now.Add(-time.Hour)})
				}
			}
			require.NoError(t, store.UpsertWarpTopicAssignments(ctx, rows))

			done, err := service.MaintainTopics(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, done.Merged, "only the namesake is merged")
			topics := activeTopics(t, store)
			require.Contains(t, topics, "Refund requests")
			require.Contains(t, topics, "Refunds", "alike, but not merged on a guessed threshold")
		})
	}
}

// A merged topic is named from examples in proportion to how many requests
// each of its topics holds, the smallest still shown at least once, and each
// name it had is offered once however it was cased.
func TestMergedTopicExamplesFollowSize(t *testing.T) {
	topics := []logstore.WarpTopic{{ID: "small"}, {ID: "large"}}
	require.NoError(t, topics[0].SetExampleLogIDs([]string{"s1", "s2", "s3", "s4"}))
	require.NoError(t, topics[1].SetExampleLogIDs([]string{"l1", "l2", "l3", "l4"}))
	picked := mergedTopicExamples(topics, map[string]int64{"small": 10, "large": 1000}, 4)
	require.Equal(t, []string{"l1", "s1", "l2", "l3"}, picked, "the larger topic first and most, the smaller at least once")
	require.Equal(t, []string{"Graviton", "Docs"}, distinctTopicNames([]logstore.WarpTopic{{Label: "Graviton"}, {Label: " graviton "}, {Label: "Docs"}}))
}

// Requests filed under a topic since merged count for the topic it was merged
// into: a survivor whose recent traffic is all under a merged id is not
// silent, and its size covers them when it is rebuilt as a cluster.
func TestWarpUpkeepCountsRequestsUnderMergedTopics(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	store := reader.topics
	ctx := context.Background()
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	now := time.Now().UTC()
	old := now.Add(-2 * topicsRetireAfter)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "survivor", Label: "Refund requests", Centroid: logstore.EncodeWarpVector([]float32{1, 0, 0}), Cohesion: 1, EmbeddingSignature: signature, CreatedAt: old},
		{ID: "twin", Label: "Refunds", Status: logstore.WarpTopicStatusMerged, MergedInto: "survivor", EmbeddingSignature: signature, CreatedAt: old},
	}))
	rows := []logstore.WarpTopicAssignment{{LogID: "own", TopicID: "survivor", Timestamp: old}}
	for index := range 99 {
		rows = append(rows, logstore.WarpTopicAssignment{LogID: fmt.Sprintf("twin-%d", index), TopicID: "twin", Timestamp: now.Add(-time.Hour)})
	}
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, rows))

	done, err := service.MaintainTopics(ctx)
	require.NoError(t, err)
	require.Zero(t, done.Retired, "its recent requests are filed under the topic merged into it")
	require.Contains(t, activeTopics(t, store), "Refund requests")

	// Ten requests along another axis folded into a topic of a hundred
	// barely move its centre; folded into a topic of one, they would carry it.
	existing, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{IDs: []string{"survivor"}})
	require.NoError(t, err)
	cluster := topicCluster{sum: []float32{0, 10, 0}, size: 10}
	folded, err := foldIntoExistingTopics(ctx, store, existing, []topicPlacement{{cluster: cluster, topic: "survivor"}})
	require.NoError(t, err)
	require.Len(t, folded, 1)
	centre, err := logstore.DecodeWarpVector(folded[0].Centroid)
	require.NoError(t, err)
	require.Greater(t, dot(centre, []float32{1, 0, 0}), float32(0.99), "sized at 100, not 1")
}

// discoveryQueue records what the scheduler enqueued.
type discoveryQueue struct {
	mu    sync.Mutex
	kinds []string
	keys  []string
}

func (q *discoveryQueue) EnqueuePartitioned(_ context.Context, _, kind, key, _, _ string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.kinds = append(q.kinds, kind)
	q.keys = append(q.keys, key)
	return nil
}

// seedRunJobs is a job store whose latest topics run is the one given.
type seedRunJobs struct{ latest *tables.TableSidekiqJob }

func (seedRunJobs) GetInFlightSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return nil, nil
}

func (j seedRunJobs) GetLatestSidekiqJobByKind(_ context.Context, kind string) (*tables.TableSidekiqJob, error) {
	if kind != TopicsJobKind {
		return nil, nil
	}
	return j.latest, nil
}

// seedRun is a topics run that ended with status, over signature, having
// merged its clusters at threshold.
func seedRun(t *testing.T, status, signature string, threshold *float64) seedRunJobs {
	t.Helper()
	metadata, err := sonic.MarshalString(BackfillJobMeta{ConfigSignature: signature, MergeThreshold: threshold})
	require.NoError(t, err)
	return seedRunJobs{latest: &tables.TableSidekiqJob{ID: "seed", Kind: TopicsJobKind, Status: status, Metadata: metadata}}
}

// discoveryJobs is a job store that reports one in-flight job of a kind.
type discoveryJobs struct {
	inFlight map[string]*tables.TableSidekiqJob
}

func (j discoveryJobs) GetInFlightSidekiqJobByKind(_ context.Context, kind string) (*tables.TableSidekiqJob, error) {
	return j.inFlight[kind], nil
}

func (j discoveryJobs) GetLatestSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return nil, nil
}

// Discovery is started when enough requests are pooled, serially with the
// runs that write topics, and never while one of either is in flight.
func TestWarpDiscoveryIsStartedWhenThePoolIsDue(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	queue := &discoveryQueue{}
	jobs := &discoveryJobs{inFlight: map[string]*tables.TableSidekiqJob{}}
	service.topicJobs, service.backfillJobs = queue, jobs
	ctx := context.Background()

	started, err := service.StartDiscoveryIfDue(ctx)
	require.NoError(t, err)
	require.False(t, started, "an empty pool")
	poolSubject(t, reader, "refund", "how do I get a refund", 0, schemas.WarpDefaultTopicDiscoveryMinRequests, time.Now().UTC().Add(-time.Hour))

	jobs.inFlight[TopicsJobKind] = &tables.TableSidekiqJob{ID: "run"}
	started, err = service.StartDiscoveryIfDue(ctx)
	require.NoError(t, err)
	require.False(t, started, "a run is in flight")
	delete(jobs.inFlight, TopicsJobKind)
	jobs.inFlight[TopicDiscoveryJobKind] = &tables.TableSidekiqJob{ID: "discovery"}
	started, err = service.StartDiscoveryIfDue(ctx)
	require.NoError(t, err)
	require.False(t, started, "a discovery is in flight")
	delete(jobs.inFlight, TopicDiscoveryJobKind)

	started, err = service.StartDiscoveryIfDue(ctx)
	require.NoError(t, err)
	require.True(t, started)
	require.Equal(t, []string{TopicDiscoveryJobKind}, queue.kinds)
	require.Equal(t, []string{TopicsJobPartition}, queue.keys, "in the partition the runs use, so the two never interleave")
}

// racingDiscoveryJobs lets two schedulers' in-flight lookups overlap: each
// waits a moment for the other before it answers, and what has been queued
// counts as in flight.
type racingDiscoveryJobs struct {
	queue   *discoveryQueue
	arrived atomic.Int32
	both    chan struct{}
}

func (j *racingDiscoveryJobs) GetInFlightSidekiqJobByKind(_ context.Context, kind string) (*tables.TableSidekiqJob, error) {
	if j.arrived.Add(1) == 2 {
		close(j.both)
	}
	select {
	case <-j.both:
	case <-time.After(200 * time.Millisecond):
	}
	j.queue.mu.Lock()
	defer j.queue.mu.Unlock()
	for _, queued := range j.queue.kinds {
		if queued == kind {
			return &tables.TableSidekiqJob{ID: "queued", Kind: kind}, nil
		}
	}
	return nil, nil
}

func (j *racingDiscoveryJobs) GetLatestSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return nil, nil
}

// Two schedulers that find the pool due at once queue one discovery: the
// lookup and the enqueue are admitted together.
func TestWarpDiscoveryIsStartedOnceWhenTwoFindItDue(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	queue := &discoveryQueue{}
	service.topicJobs, service.backfillJobs = queue, &racingDiscoveryJobs{queue: queue, both: make(chan struct{})}
	poolSubject(t, reader, "refund", "how do I get a refund", 0, schemas.WarpDefaultTopicDiscoveryMinRequests, time.Now().UTC().Add(-time.Hour))

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.StartDiscoveryIfDue(context.Background())
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	require.Equal(t, []string{TopicDiscoveryJobKind}, queue.kinds)
}

// The pool size that starts a discovery is configured: here thirty, so
// twenty-nine waiting is not yet due and thirty is.
func TestWarpDiscoveryIsDueAtTheConfiguredPoolSize(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	service.store.(*recordingStore).row.TopicDiscoveryMinRequests = 30
	queue := &discoveryQueue{}
	service.topicJobs, service.backfillJobs = queue, &discoveryJobs{inFlight: map[string]*tables.TableSidekiqJob{}}
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour)

	poolSubject(t, reader, "refund", "how do I get a refund", 0, 29, at)
	started, err := service.StartDiscoveryIfDue(ctx)
	require.NoError(t, err)
	require.False(t, started, "29 of 30")

	poolSubject(t, reader, "password", "reset my password", 1, 1, at)
	started, err = service.StartDiscoveryIfDue(ctx)
	require.NoError(t, err)
	require.True(t, started)
}

// A discovery reads the same setting: a pool of twenty is clustered when
// twenty is what the operator asked for.
func TestWarpDiscoveryClustersAPoolAtTheConfiguredSize(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	service.store.(*recordingStore).row.TopicDiscoveryMinRequests = 20
	poolSubject(t, reader, "refund", "how do I get a refund", 0, 20, time.Now().UTC().Add(-time.Hour))
	final := runDiscovery(t, service)
	require.Equal(t, 20, final.Scanned)
	require.NotContains(t, final.Message, "waiting for a topic")
	require.Contains(t, final.Message, "Clustered 20 pooled request(s)")
}

// Every node that shares a job store keeps topics through its queue, so no
// two passes ever overlap. A node that can see the job store but has no runner
// to queue on does not keep them inline instead: inline upkeep holds no lock,
// and another node could start a queued pass the moment after it looked. Only
// a node with no job store at all - nothing to coordinate through - keeps
// them in this process.
func TestWarpUpkeepRunsInlineOnlyWithoutAJobStore(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	store := reader.topics
	ctx := context.Background()
	old := time.Now().UTC().Add(-2 * topicsRetireAfter)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{{
		ID: "silent", Label: "Silent", Centroid: logstore.EncodeWarpVector([]float32{0, 1, 0}),
		EmbeddingSignature: embeddingConfigSignature(configFromRow(assignTestRow())), CreatedAt: old,
	}}))
	require.Nil(t, service.topicJobs, "no runner to queue on")

	// Nothing in flight, so an inline pass would run and retire the topic.
	service.backfillJobs = discoveryJobs{inFlight: map[string]*tables.TableSidekiqJob{}}
	service.topicUpkeepTick(ctx)
	require.Contains(t, activeTopics(t, store), "Silent", "a node that shares a job store never keeps topics inline")

	service.backfillJobs = nil
	service.topicUpkeepTick(ctx)
	require.NotContains(t, activeTopics(t, store), "Silent", "with no job store, upkeep runs in this process")
}

// upkeepQueue records what was queued, and refuses ids it has seen as a
// shared job store refuses a duplicate primary key.
type upkeepQueue struct {
	ids, kinds, keys []string
}

func (q *upkeepQueue) EnqueuePartitioned(_ context.Context, id, kind, key, _, _ string) error {
	if slices.Contains(q.ids, id) {
		return errors.New(`ERROR: duplicate key value violates unique constraint "sidekiq_pkey"`)
	}
	q.ids, q.kinds, q.keys = append(q.ids, id), append(q.kinds, kind), append(q.keys, key)
	return nil
}

// With a job runner, the timer queues upkeep in the topics partition rather
// than running it: that partition runs one topics job at a time across every
// instance, which is what keeps two instances from rewriting a topic at once.
// Instances queuing in the same hour make one job, and none is queued while
// one waits.
func TestWarpUpkeepIsQueuedInTheTopicsPartition(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	store := reader.topics
	ctx := context.Background()
	old := time.Now().UTC().Add(-2 * topicsRetireAfter)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{{
		ID: "silent", Label: "Silent", Centroid: logstore.EncodeWarpVector([]float32{0, 1, 0}),
		EmbeddingSignature: embeddingConfigSignature(configFromRow(assignTestRow())), CreatedAt: old,
	}}))
	queue := &upkeepQueue{}
	service.topicJobs, service.backfillJobs = queue, discoveryJobs{inFlight: map[string]*tables.TableSidekiqJob{}}

	service.topicUpkeepTick(ctx)
	require.Equal(t, []string{TopicUpkeepJobKind}, queue.kinds)
	require.Equal(t, []string{TopicsJobPartition}, queue.keys)
	require.Contains(t, activeTopics(t, store), "Silent", "queued, not run in this process")

	// The ids, at fixed times: the timer's own call above reads the clock,
	// and a test that compared it with a second reading would fail whenever
	// the two fell either side of an hour.
	queue = &upkeepQueue{}
	hour := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	require.NoError(t, service.enqueueTopicUpkeep(ctx, queue, hour.Add(5*time.Minute)))
	require.NoError(t, service.enqueueTopicUpkeep(ctx, queue, hour.Add(55*time.Minute)), "a duplicate is another instance's job, not a failure")
	require.Len(t, queue.ids, 1, "instances queuing in the same hour make one job")
	require.NoError(t, service.enqueueTopicUpkeep(ctx, queue, hour.Add(topicsDiscoveryInterval)))
	require.Len(t, queue.ids, 2, "the next hour is a new job")

	service.backfillJobs = discoveryJobs{inFlight: map[string]*tables.TableSidekiqJob{TopicUpkeepJobKind: {ID: "waiting"}}}
	require.NoError(t, service.enqueueTopicUpkeep(ctx, queue, hour.Add(2*topicsDiscoveryInterval)))
	require.Len(t, queue.ids, 2, "none is queued while one waits")

	service.backfillJobs = failingJobs{}
	require.Error(t, service.enqueueTopicUpkeep(ctx, queue, hour.Add(3*topicsDiscoveryInterval)), "a job store that cannot be read is reported")
	require.Len(t, queue.ids, 2)

	// With no job store to ask, upkeep is still queued, or it would never run:
	// a service with a queue does not run it inline. The hourly id keeps it
	// to one job an hour.
	service.backfillJobs = nil
	require.NoError(t, service.enqueueTopicUpkeep(ctx, queue, hour.Add(4*topicsDiscoveryInterval)))
	require.NoError(t, service.enqueueTopicUpkeep(ctx, queue, hour.Add(4*topicsDiscoveryInterval+time.Minute)))
	require.Len(t, queue.ids, 3, "queued once for the hour without a job store")
}

// The upkeep job is one pass of MaintainTopics, and reports what it did.
// Warp being off is nothing to keep, not a failed job.
func TestWarpUpkeepJobMaintainsTopics(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	store := reader.topics
	ctx := context.Background()
	old := time.Now().UTC().Add(-2 * topicsRetireAfter)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{{
		ID: "silent", Label: "Silent", Centroid: logstore.EncodeWarpVector([]float32{0, 1, 0}),
		EmbeddingSignature: embeddingConfigSignature(configFromRow(assignTestRow())), CreatedAt: old,
	}}))
	final, err := service.RunTopicUpkeepJob(ctx, tables.TableSidekiqJob{ID: "upkeep", Metadata: "{}"}, func(string) error { return nil })
	require.NoError(t, err)
	require.NotContains(t, activeTopics(t, store), "Silent")
	var meta topicUpkeepJobMeta
	require.NoError(t, sonic.UnmarshalString(final, &meta))
	require.Equal(t, 1, meta.Retired)
	require.Equal(t, meta.Message, SummarizeTopicUpkeepMeta(final).Message)
	require.Contains(t, meta.Message, "Retired 1 topic(s)")

	off := NewService(nil, WithConfigStore(&recordingStore{row: assignTestRow()}))
	t.Cleanup(off.Shutdown)
	final, err = off.RunTopicUpkeepJob(ctx, tables.TableSidekiqJob{ID: "upkeep", Metadata: "{}"}, func(string) error { return nil })
	require.NoError(t, err, "no topic store is nothing to keep")
	require.Contains(t, SummarizeTopicUpkeepMeta(final).Message, "not available")
}

// RegisterTopics binds the upkeep job beside discovery, and marks it routine
// so its completed runs stay out of the jobs listing.
func TestRegisterTopicsBindsTheUpkeepJob(t *testing.T) {
	service, _ := discoveryTestService(t, topicTestNaming)
	runner := sidekiq.New(nil, nil, 1, "")
	t.Cleanup(runner.Shutdown)
	service.RegisterTopics(runner)
	_, summarized := runner.Summarize(TopicUpkeepJobKind, "{}")
	require.True(t, summarized)
	require.Equal(t, []string{TopicUpkeepJobKind}, runner.QuietOnSuccessKinds())
	require.NotContains(t, TopicsJobKinds, TopicUpkeepJobKind, "a waiting upkeep must not refuse a seed run")
}

// A deployment that turns logging on after startup gets its log reader late.
// The topic jobs must already be bound by then, or the node has a job store
// and no queue, and upkeep is skipped on it until restart. Bound before the
// reader arrives, they find no topic store and do nothing, and the timer queues
// nothing while there is none.
func TestRegisterTopicsBindsTheJobsBeforeTheLogReader(t *testing.T) {
	service := NewService(nil, WithConfigStore(&recordingStore{row: assignTestRow()}))
	t.Cleanup(service.Shutdown)
	runner := sidekiq.New(nil, nil, 1, "")
	t.Cleanup(runner.Shutdown)
	service.RegisterTopics(runner)
	require.NotNil(t, service.topicJobs, "the queue is bound with no log reader yet")
	_, summarized := runner.Summarize(TopicUpkeepJobKind, "{}")
	require.True(t, summarized)

	queue := &upkeepQueue{}
	service.topicJobs, service.backfillJobs = queue, discoveryJobs{inFlight: map[string]*tables.TableSidekiqJob{}}
	service.topicUpkeepTick(context.Background())
	require.Empty(t, queue.ids, "nothing is queued while there is no topic store")

	service.SetLogReader(topicTestReader(t, map[string]logstore.Log{}))
	service.topicUpkeepTick(context.Background())
	require.Equal(t, []string{TopicUpkeepJobKind}, queue.kinds, "once logging is on, upkeep is queued")
}

// failingJobs is a job store that cannot be read.
type failingJobs struct{}

func (failingJobs) GetInFlightSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return nil, errors.New("job store unavailable")
}

func (failingJobs) GetLatestSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return nil, nil
}

// stallingTopicStore holds every topic read until its context ends.
type stallingTopicStore struct {
	logstore.WarpTopicStore
	entered  chan struct{}
	once     sync.Once
	finished chan error
}

// ListWarpVectorDeletes reports nothing to retry, so a tick reaches the topic
// read this store stalls on.
func (s *stallingTopicStore) ListWarpVectorDeletes(context.Context, int) ([]logstore.WarpVectorDelete, error) {
	return nil, nil
}

func (s *stallingTopicStore) ListWarpTopics(ctx context.Context, _ logstore.WarpTopicFilter) ([]logstore.WarpTopic, error) {
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()
	s.finished <- ctx.Err()
	return nil, ctx.Err()
}

// Shutdown stops an upkeep pass in flight, not only the next one: a pass can
// run for minutes against a slow store, and each pass's own timeout is no
// substitute for being told to stop.
func TestWarpShutdownInterruptsUpkeep(t *testing.T) {
	store := &stallingTopicStore{entered: make(chan struct{}), finished: make(chan error, 1)}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: assignTestRow()}), WithLogReader(&semanticLogReader{logs: map[string]logstore.Log{}, topics: store}),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(assignTestEmbedding),
	)
	stop := make(chan struct{})
	closeStop := sync.OnceFunc(func() { close(stop) })
	// Registered before the loop starts, so a failed wait still stops it.
	t.Cleanup(func() {
		closeStop()
		service.Shutdown()
	})
	go service.runTopicUpkeep(stop, time.Millisecond)
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no upkeep pass started")
	}
	closeStop()
	select {
	case err := <-store.finished:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("the pass in flight was not interrupted")
	}
}

// vanishingTopicStore lists merged topics but has none by id, as a store does
// when a topic is deleted between the two reads.
type vanishingTopicStore struct {
	logstore.WarpTopicStore
	merged []logstore.WarpTopic
}

func (s vanishingTopicStore) ListWarpTopics(_ context.Context, filter logstore.WarpTopicFilter) ([]logstore.WarpTopic, error) {
	if len(filter.IDs) > 0 {
		return nil, nil
	}
	return s.merged, nil
}

// A merged topic that cannot be read back whole is reported as missing, with
// how many rows came back, rather than as a wrapped nil error.
func TestRepointMergedTopicsReportsAMissingRow(t *testing.T) {
	store := vanishingTopicStore{merged: []logstore.WarpTopic{{ID: "old", MergedInto: "gone"}}}
	_, err := repointMergedTopics(context.Background(), store, map[string]struct{}{"survivor": {}}, []logstore.WarpTopic{{ID: "gone", MergedInto: "survivor"}})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "%!w")
	require.Contains(t, err.Error(), "want 1 row, got 0")
}

// A chain already deeper than one level, as a pass whose write failed partway
// leaves one, is pointed straight at its live survivor by the next pass, even
// one that merges nothing: the logs filter follows a single merged_into.
func TestWarpUpkeepFlattensExistingMergeChains(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	store := reader.topics
	ctx := context.Background()
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	now := time.Now().UTC()
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "live", Label: "Live", Centroid: logstore.EncodeWarpVector([]float32{1, 0, 0}), EmbeddingSignature: signature, CreatedAt: now.Add(-time.Hour)},
		{ID: "middle", Label: "Middle", Status: logstore.WarpTopicStatusMerged, MergedInto: "live", EmbeddingSignature: signature},
		{ID: "deep", Label: "Deep", Status: logstore.WarpTopicStatusMerged, MergedInto: "middle", EmbeddingSignature: signature},
		{ID: "orphan", Label: "Orphan", Status: logstore.WarpTopicStatusMerged, MergedInto: "gone", EmbeddingSignature: signature},
	}))

	done, err := service.MaintainTopics(ctx)
	require.NoError(t, err)
	require.Zero(t, done.Merged)
	rows, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Statuses: []string{logstore.WarpTopicStatusMerged}})
	require.NoError(t, err)
	into := map[string]string{}
	for _, row := range rows {
		into[row.ID] = row.MergedInto
	}
	require.Equal(t, map[string]string{"middle": "live", "deep": "live"}, into, "flattened before pruning, so deep is not taken for an orphan")
	orphan, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{IDs: []string{"orphan"}})
	require.NoError(t, err)
	require.Len(t, orphan, 1)
	require.Equal(t, logstore.WarpTopicStatusRetired, orphan[0].Status, "a chain that ends nowhere live is pruned")
}

func TestResolveMergedTopics(t *testing.T) {
	active := map[string]struct{}{"a": {}, "b": {}}
	merged := []logstore.WarpTopic{
		{ID: "x", MergedInto: "a"},
		{ID: "y", MergedInto: "x"},
		{ID: "z", MergedInto: "gone"},
		{ID: "loop1", MergedInto: "loop2"},
		{ID: "loop2", MergedInto: "loop1"},
	}
	require.Equal(t, map[string]string{"x": "a", "y": "a"}, resolveMergedTopics(active, merged))
}
