package warp

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/queryscope"
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
		require.Len(t, topic.ExampleLogIDs(), topicsLabelSamples, label)
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

// Upkeep rewrites topic rows whole, as the runs and discoveries that write
// topics do, so it stands aside while one of those is in flight - and when it
// cannot tell, rather than guess.
func TestWarpUpkeepWaitsForTopicJobs(t *testing.T) {
	service, reader := discoveryTestService(t, topicTestNaming)
	store := reader.topics
	ctx := context.Background()
	old := time.Now().UTC().Add(-2 * topicsRetireAfter)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{{
		ID: "silent", Label: "Silent", Centroid: logstore.EncodeWarpVector([]float32{0, 1, 0}),
		EmbeddingSignature: embeddingConfigSignature(configFromRow(assignTestRow())), CreatedAt: old,
	}}))

	for _, kind := range []string{TopicsJobKind, TopicDiscoveryJobKind} {
		service.backfillJobs = discoveryJobs{inFlight: map[string]*tables.TableSidekiqJob{kind: {ID: kind}}}
		service.topicUpkeepTick(ctx)
		require.Contains(t, activeTopics(t, store), "Silent", "upkeep waits while a %s job is in flight", kind)
	}
	service.backfillJobs = failingJobs{}
	service.topicUpkeepTick(ctx)
	require.Contains(t, activeTopics(t, store), "Silent", "upkeep waits when it cannot tell whether a job is in flight")

	service.backfillJobs = discoveryJobs{inFlight: map[string]*tables.TableSidekiqJob{}}
	service.topicUpkeepTick(ctx)
	require.NotContains(t, activeTopics(t, store), "Silent", "with nothing in flight, upkeep runs")
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
