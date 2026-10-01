package warp

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Two tight groups far apart must come out as two clusters with the right
// members. The vectors are unit length, as the embedding models produce, so
// cosine and dot product agree.
func TestKMeansSeparatesObviousGroups(t *testing.T) {
	vectors := [][]float32{
		{1, 0, 0}, {0.99, 0.1, 0}, {0.98, 0, 0.1},
		{0, 1, 0}, {0.1, 0.99, 0}, {0, 0.98, 0.1},
	}
	assignments, centroids, err := kMeans(context.Background(), vectors, 2, 10, 1)
	require.NoError(t, err)
	require.Len(t, centroids, 2)
	require.Equal(t, assignments[0], assignments[1])
	require.Equal(t, assignments[1], assignments[2])
	require.Equal(t, assignments[3], assignments[4])
	require.Equal(t, assignments[4], assignments[5])
	require.NotEqual(t, assignments[0], assignments[3])
}

// The cluster count follows the corpus size and deliberately overshoots: a
// topic split in two is put back together by the merge, while two topics
// forced into one cluster cannot be told apart afterwards. A handful of
// conversations is not over-clustered, because there the extra clusters would
// be singletons.
func TestTopicClusterCount(t *testing.T) {
	require.Equal(t, 0, topicClusterCount(0))
	require.Equal(t, 1, topicClusterCount(1))
	require.Equal(t, 2, topicClusterCount(8))
	require.Equal(t, 14, topicClusterCount(100))
	require.Equal(t, 44, topicClusterCount(1000))
	require.Equal(t, topicsMaxClusters, topicClusterCount(1_000_000))
}

// topicTestPoints draws unit vectors around each direction: the direction
// plus noise of the given length spread over every dimension, which is how
// conversations on one subject sit around it in an embedding space.
func topicTestPoints(rng *rand.Rand, directions [][]float32, perDirection int, noise float64) [][]float32 {
	points := make([][]float32, 0, len(directions)*perDirection)
	for _, direction := range directions {
		scale := noise / math.Sqrt(float64(len(direction)))
		for range perDirection {
			point := make([]float32, len(direction))
			for d, value := range direction {
				point[d] = value + float32(rng.NormFloat64()*scale)
			}
			points = append(points, unitVector(point))
		}
	}
	return points
}

// topicTestDirection is a unit vector along one axis, tilted toward another
// by the given cosine, so two directions can be made as similar as a test
// needs.
func topicTestDirection(dimension, axis, toward int, cosine float64) []float32 {
	direction := make([]float32, dimension)
	direction[toward] = float32(cosine)
	direction[axis] = float32(math.Sqrt(1 - cosine*cosine))
	return direction
}

// Seeding and assignment are the expensive part of a run and were rewritten
// to do less work. The result must not depend on how the work is divided:
// the same seed gives the same clusters whether the points are few enough to
// assign inline or many enough to be shared across workers.
func TestKMeansIsDeterministicAcrossWorkers(t *testing.T) {
	const dimension = 32
	rng := rand.New(rand.NewSource(5))
	directions := [][]float32{
		topicTestDirection(dimension, 0, 0, 0), topicTestDirection(dimension, 1, 1, 0),
		topicTestDirection(dimension, 2, 2, 0), topicTestDirection(dimension, 3, 3, 0),
	}
	points := topicTestPoints(rng, directions, topicsParallelMinPoints, 0.4)
	require.Greater(t, len(points), topicsParallelMinPoints)

	first, firstCentroids, err := kMeans(context.Background(), points, 4, topicsKMeansIterations, 9)
	require.NoError(t, err)
	second, secondCentroids, err := kMeans(context.Background(), points, 4, topicsKMeansIterations, 9)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, firstCentroids, secondCentroids)
	for subject := range directions {
		base := subject * topicsParallelMinPoints
		for member := range topicsParallelMinPoints {
			require.Equal(t, first[base], first[base+member])
		}
	}
}

// Without a model, the label is still something a person can read: the most
// frequent meaningful words across the cluster's questions.
func TestFallbackTopicLabel(t *testing.T) {
	label := fallbackTopicLabel([]string{"how do I get a refund", "refund not received yet", "what is the refund status"})
	require.Contains(t, label, "refund")
	require.NotContains(t, label, "the")
	require.Equal(t, "Untitled topic", fallbackTopicLabel(nil))
}

func topicTestLogs(start time.Time) map[string]logstore.Log {
	logs := map[string]logstore.Log{}
	questions := []string{
		"how do I get a refund", "refund not received yet", "what is the refund status",
		"reset my password please", "password reset link expired", "cannot reset password",
	}
	for index, question := range questions {
		id := fmt.Sprintf("log-%d", index)
		logs[id] = logstore.Log{
			ID: id, Timestamp: start.Add(time.Duration(index) * time.Minute), Object: string(schemas.ChatCompletionRequest),
			Status: "success", ContentSummary: question,
		}
	}
	return logs
}

func topicTestVectors() map[string][]float32 {
	return map[string][]float32{
		"log-0": {1, 0, 0}, "log-1": {0.99, 0.1, 0}, "log-2": {0.98, 0, 0.1},
		"log-3": {0, 1, 0}, "log-4": {0.1, 0.99, 0}, "log-5": {0, 0.98, 0.1},
	}
}

// The job reads the log vectors, clusters them, labels each cluster through
// the model, and writes one centroid per topic beside the log namespace.
func TestWarpTopicsJobClustersLabelsAndStores(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	vectors := newFakeWarpVectorStore()
	logs := topicTestLogs(start)
	for id, vector := range topicTestVectors() {
		entry := logs[id]
		require.NoError(t, vectors.Add(context.Background(), schemas.WarpDefaultLogVectorStoreNamespace, id, vector, map[string]interface{}{
			"log_id": id, "timestamp": entry.Timestamp.Unix(), "warp_log": true,
		}))
	}
	var labels atomic.Int32
	model := &scriptedModel{}
	chat := func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		labels.Add(1)
		require.Nil(t, req.Params.Tools, "labelling is a plain completion, no tools")
		text := responsesInputText(req.Input)
		if contains(text, "refund") {
			return TextTurn("Refund requests"), nil
		}
		return TextTurn("Password resets"), nil
	}
	_ = model
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(&semanticLogReader{logs: logs}),
		WithVectorStore(vectors), WithEmbeddingExecutor(backfillEmbeddingExecutor), WithChatFunc(chat),
	)
	defer service.Shutdown()

	metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)
	finalJSON, err := service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, 6, final.Scanned, "every vector in the window is read")
	require.Equal(t, 2, final.Indexed, "one topic per cluster")
	require.Zero(t, final.Failed)
	require.EqualValues(t, 2, labels.Load(), "one label call per cluster")

	topicsNamespace := topicsNamespaceFor(schemas.WarpDefaultLogVectorStoreNamespace)
	ids := vectors.idsIn(topicsNamespace)
	require.Len(t, ids, 2)
	seen := map[string]int{}
	for _, id := range ids {
		meta := vectors.adds[id]
		seen[meta["label"].(string)] = int(meta["size"].(int64))
		require.Equal(t, true, meta["warp_topic"])
		require.NotEmpty(t, meta["sample_log_ids"])
		require.Len(t, vectors.embeddings[id], 3, "the centroid lives in the same space as the logs")
	}
	require.Equal(t, map[string]int{"Refund requests": 3, "Password resets": 3}, seen)

	// How the run decided is part of its result: the threshold it derived,
	// how many clusters it joined, and how tight each topic came out.
	require.NotNil(t, final.MergeThreshold)
	require.Greater(t, *final.MergeThreshold, 0.9)
	require.Zero(t, final.Merged, "the two subjects are nothing alike")
	require.Len(t, final.Topics, 2)
	for _, topic := range final.Topics {
		require.Contains(t, []string{"Refund requests", "Password resets"}, topic.Label)
		require.Equal(t, 3, topic.Size)
		require.Greater(t, topic.Cohesion, 0.9)
	}
	for _, id := range ids {
		require.Greater(t, vectors.adds[id]["cohesion_milli"].(int64), int64(900))
		require.Equal(t, int64(2), vectors.adds[id]["run_topics"], "each topic says how many its run wrote")
		require.Equal(t, "job-1", vectors.adds[id]["run_id"])
	}
	require.Contains(t, final.Message, "Clustered 6 request(s) into 2 topic(s)")
}

// A recompute replaces the previous topics rather than piling onto them, so
// the list never shows a stale cluster beside a fresh one.
func TestWarpTopicsJobReplacesPreviousTopics(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	vectors := newFakeWarpVectorStore()
	logs := topicTestLogs(start)
	for id, vector := range topicTestVectors() {
		require.NoError(t, vectors.Add(context.Background(), schemas.WarpDefaultLogVectorStoreNamespace, id, vector, map[string]interface{}{"log_id": id, "timestamp": logs[id].Timestamp.Unix(), "warp_log": true}))
	}
	topicsNamespace := topicsNamespaceFor(schemas.WarpDefaultLogVectorStoreNamespace)
	require.NoError(t, vectors.Add(context.Background(), topicsNamespace, "stale", []float32{0, 0, 1}, map[string]interface{}{"warp_topic": true, "label": "Old", "size": int64(9), "run_id": "job-before", "run_topics": int64(1), "computed_at": start.Unix()}))

	// The model is unreachable this run: clusters still get written, with
	// fallback names, and the failure is counted rather than fatal.
	unreachable := func(context.Context, *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "no keys found that support model: openai/gpt-4o"}}
	}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(&semanticLogReader{logs: logs}),
		WithVectorStore(vectors), WithEmbeddingExecutor(backfillEmbeddingExecutor), WithChatFunc(unreachable),
	)
	defer service.Shutdown()
	metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)
	finalJSON, err := service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)
	require.NotContains(t, vectors.idsIn(topicsNamespace), "stale")
	require.Len(t, vectors.idsIn(topicsNamespace), 2)
	labels := map[string]bool{}
	for _, id := range vectors.idsIn(topicsNamespace) {
		labels[vectors.adds[id]["label"].(string)] = true
	}
	require.True(t, labels["refund"] || labels["password"] || len(labels) == 2, "fallback labels come from the questions' words: %v", labels)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, 2, final.Failed, "each failed label is counted")
	require.Contains(t, final.LastError, "no keys found")
}

// list_topics is the tool that answers "what do people ask about": clusters
// by size, with a share and links a reader can open.
func TestWarpListTopicsToolReturnsSortedTopics(t *testing.T) {
	vectors := newFakeWarpVectorStore()
	topicsNamespace := topicsNamespaceFor(schemas.WarpDefaultLogVectorStoreNamespace)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for _, topic := range []struct {
		id    string
		label string
		size  int64
	}{{"t-small", "Password resets", 3}, {"t-big", "Refund requests", 9}} {
		require.NoError(t, vectors.Add(context.Background(), topicsNamespace, topic.id, []float32{1, 0, 0}, map[string]interface{}{
			"warp_topic": true, "topic_id": topic.id, "label": topic.label, "size": topic.size, "sample_log_ids": "log-1,log-2",
			"window_start": now.Add(-24 * time.Hour).Unix(), "window_end": now.Unix(), "computed_at": now.Unix(), "total_clustered": int64(12),
			"cohesion_milli": int64(875),
		}))
	}
	reader := NewTopicReader(&recordingStore{row: validWarpConfigRow()}, vectors)
	result, err := runTool(t, "list_topics", &ToolDeps{logManager: &fakeLogReader{}, topics: reader}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	topics := out["topics"].([]Topic)
	require.Len(t, topics, 2)
	require.Equal(t, "Refund requests", topics[0].Label)
	require.Equal(t, 9, topics[0].Size)
	require.InDelta(t, 0.75, topics[0].Share, 1e-9)
	require.InDelta(t, 0.875, topics[0].Cohesion, 1e-9)
	require.Equal(t, []string{logDetailLink("log-1"), logDetailLink("log-2")}, topics[0].SampleLinks)
	require.Equal(t, 12, out["total_clustered"])
	_, hasHint := out["hint"]
	require.False(t, hasHint)

	// A topic's links each open one conversation. They are named for what
	// they are, and the result offers nothing that reads as "the requests
	// behind this topic", because the Logs page cannot show that set: a
	// topic's name linked to one request says nine and opens one.
	encoded, err := sonic.MarshalString(out)
	require.NoError(t, err)
	require.Contains(t, encoded, `"example_links":["`+logDetailLink("log-1")+`","`+logDetailLink("log-2")+`"]`)
	require.NotContains(t, encoded, "sample_links")
	_, hasLogsLink := out["logs_link"]
	require.False(t, hasLogsLink, "the unfiltered Logs page is not what a topic count covers")
	require.Contains(t, out["links"], "one example request")
	require.Contains(t, out["counts"], "count requests")
	require.Contains(t, out["cannot_answer"], "a different time range")
}

func TestWarpListTopicsToolHintsWhenNothingComputed(t *testing.T) {
	reader := NewTopicReader(&recordingStore{row: validWarpConfigRow()}, newFakeWarpVectorStore())
	result, err := runTool(t, "list_topics", &ToolDeps{logManager: &fakeLogReader{}, topics: reader}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Empty(t, out["topics"])
	require.Contains(t, out["hint"], "No topic clusters have been computed")
}

// The prompt has to send topic questions here first.
func TestWarpSystemPromptPrefersListTopics(t *testing.T) {
	content := systemInstructions(&schemas.WarpConfig{}, true)
	require.Contains(t, content, "call list_topics first")
	require.Contains(t, content, "or that topics are not available to the person asking")
}

// Topics answer one question: what the requests of the last computed window
// were about. Asked for anything finer - another time range, one team, a
// trend, the requests behind a topic - the model used to go looking, slicing
// the logs call after call for an answer no tool holds. It is told what there
// is and what there is not, so it can say so and stop.
func TestWarpSystemPromptSaysWhatTopicsCannotAnswer(t *testing.T) {
	content := systemInstructions(&schemas.WarpConfig{}, true)
	for _, line := range []string{
		"Topics are a snapshot",
		"a different time range",
		"one team, customer, user, model or app",
		"whether a topic is growing",
		"every request in a topic",
		"how many people or conversations",
		"cost, latency or errors per topic",
		"Call list_topics once",
		"is not available yet",
	} {
		require.Contains(t, content, line)
	}
	require.Contains(t, content, "Leave a topic's name unlinked")
	require.Contains(t, content, `say "requests" when you report them`)
}

// responsesInputText flattens a request's input for assertions.
func responsesInputText(input []schemas.ResponsesMessage) string {
	text := ""
	for _, message := range input {
		if message.Content != nil && message.Content.ContentStr != nil {
			text += *message.Content.ContentStr + " "
		}
	}
	return text
}

func contains(text, needle string) bool {
	return len(needle) > 0 && len(text) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(text); i++ {
			if text[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// Weaviate's cursor paging refuses a where filter, so the window is applied
// here, on the timestamp each entry carries. A vector outside the window must
// not be clustered even though the store hands it back.
func TestWarpTopicsJobAppliesWindowInCode(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	vectors := newFakeWarpVectorStore()
	logs := topicTestLogs(start)
	for id, vector := range topicTestVectors() {
		require.NoError(t, vectors.Add(context.Background(), schemas.WarpDefaultLogVectorStoreNamespace, id, vector, map[string]interface{}{"log_id": id, "timestamp": logs[id].Timestamp.Unix(), "warp_log": true}))
	}
	// Far outside the window, and pointing away from both groups.
	require.NoError(t, vectors.Add(context.Background(), schemas.WarpDefaultLogVectorStoreNamespace, "log-old", []float32{0, 0, 1}, map[string]interface{}{"log_id": "log-old", "timestamp": start.Add(-48 * time.Hour).Unix(), "warp_log": true}))
	require.NoError(t, vectors.Add(context.Background(), schemas.WarpDefaultLogVectorStoreNamespace, "not-a-log", []float32{0, 0, 1}, map[string]interface{}{"log_id": "not-a-log", "timestamp": start.Add(time.Hour).Unix()}))

	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(&semanticLogReader{logs: logs}),
		WithVectorStore(vectors), WithEmbeddingExecutor(backfillEmbeddingExecutor),
		WithChatFunc(func(context.Context, *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			return TextTurn("Topic"), nil
		}),
	)
	defer service.Shutdown()
	metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)
	finalJSON, err := service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, 6, final.Scanned, "the out-of-window vector and the non-log entry are excluded")
}

// topicTestService seeds the six test conversations and returns a service
// over them.
func topicTestService(t *testing.T, vectors *fakeWarpVectorStore, reader LogReader, chat ChatFunc) (*Service, time.Time) {
	t.Helper()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := topicTestLogs(start)
	for id, vector := range topicTestVectors() {
		require.NoError(t, vectors.Add(context.Background(), schemas.WarpDefaultLogVectorStoreNamespace, id, vector, map[string]interface{}{"log_id": id, "timestamp": logs[id].Timestamp.Unix(), "warp_log": true}))
	}
	if reader == nil {
		reader = &semanticLogReader{logs: logs}
	}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader),
		WithVectorStore(vectors), WithEmbeddingExecutor(backfillEmbeddingExecutor), WithChatFunc(chat),
	)
	t.Cleanup(service.Shutdown)
	return service, start
}

func topicTestChat(label string) ChatFunc {
	return func(context.Context, *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return TextTurn(label), nil
	}
}

// topicTestNaming names a cluster for what its samples are about, as a model
// would, so the two test subjects stay two topics.
func topicTestNaming(_ context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if contains(responsesInputText(req.Input), "refund") {
		return TextTurn("Refund requests"), nil
	}
	return TextTurn("Password resets"), nil
}

// countingHydrator counts the log store round trips labelling makes.
type countingHydrator struct {
	*semanticLogReader
	calls atomic.Int32
}

func (r *countingHydrator) GetLogsByIDs(ctx context.Context, ids []string) ([]logstore.Log, error) {
	r.calls.Add(1)
	return r.semanticLogReader.GetLogsByIDs(ctx, ids)
}

// Every cluster needs its sample conversations read back before it can be
// named. That is one query per batch of samples, not one per cluster: a run
// can have a hundred clusters.
func TestWarpTopicsJobHydratesSamplesInOneQuery(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	reader := &countingHydrator{semanticLogReader: &semanticLogReader{logs: topicTestLogs(start)}}
	vectors := newFakeWarpVectorStore()
	service, _ := topicTestService(t, vectors, reader, topicTestNaming)

	metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)
	require.Len(t, vectors.idsIn(topicsNamespaceFor(schemas.WarpDefaultLogVectorStoreNamespace)), 2)
	require.EqualValues(t, 1, reader.calls.Load())
	require.Len(t, reader.sawIDs, 6, "the samples of both clusters, in the one query")
}

// Naming is a model call per cluster and the slowest part of a run, so the
// calls overlap. Each call here waits until a second one is in flight; were
// they made one at a time, neither would ever see the other.
func TestWarpTopicsJobLabelsClustersConcurrently(t *testing.T) {
	var inFlight, overlapped atomic.Int32
	both := make(chan struct{})
	var once sync.Once
	chat := func(context.Context, *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		if inFlight.Add(1) >= 2 {
			once.Do(func() { close(both) })
		}
		defer inFlight.Add(-1)
		select {
		case <-both:
			overlapped.Add(1)
		case <-time.After(2 * time.Second):
		}
		return TextTurn("Topic"), nil
	}
	vectors := newFakeWarpVectorStore()
	service, start := topicTestService(t, vectors, nil, chat)
	metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)
	require.EqualValues(t, 2, overlapped.Load(), "both label calls were in flight together")
}

// A run either replaces the topics or leaves them alone. When a topic cannot
// be written, what the run did manage to write is taken back and the previous
// topics stay, so a failed recompute never costs the answer that was there.
func TestWarpTopicsJobKeepsPreviousTopicsWhenWriteFails(t *testing.T) {
	vectors := newFakeWarpVectorStore()
	service, start := topicTestService(t, vectors, nil, topicTestNaming)
	topicsNamespace := topicsNamespaceFor(schemas.WarpDefaultLogVectorStoreNamespace)
	require.NoError(t, vectors.Add(context.Background(), topicsNamespace, "previous", []float32{0, 0, 1}, map[string]interface{}{
		"warp_topic": true, "label": "Old", "size": int64(9), "run_id": "job-before", "run_topics": int64(1), "computed_at": start.Unix(),
	}))
	metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)

	// The second topic write fails: one new topic is already in the store.
	vectors.failAddAfter(topicsNamespace, 1, fmt.Errorf("vector store unavailable"))
	finalJSON, err := service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorContains(t, err, "vector store unavailable")
	require.Equal(t, []string{"previous"}, vectors.idsIn(topicsNamespace))

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Zero(t, final.Indexed)
	require.Empty(t, final.Topics)
	require.Contains(t, final.Message, "previous topics were kept")
}

// While a run is writing, the store holds the previous topics and some of the
// new ones. list_topics answers from the newest run that finished writing, so
// a question asked in that moment gets the previous topics whole rather than
// a few of the new ones.
func TestWarpListTopicsServesNewestCompleteRun(t *testing.T) {
	vectors := newFakeWarpVectorStore()
	topicsNamespace := topicsNamespaceFor(schemas.WarpDefaultLogVectorStoreNamespace)
	earlier := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	add := func(id, label, run string, runTopics int64, at time.Time, total int64) {
		require.NoError(t, vectors.Add(context.Background(), topicsNamespace, id, []float32{1, 0, 0}, map[string]interface{}{
			"warp_topic": true, "topic_id": id, "label": label, "size": int64(4), "run_id": run, "run_topics": runTopics,
			"window_start": at.Add(-24 * time.Hour).Unix(), "window_end": at.Unix(), "computed_at": at.Unix(), "total_clustered": total,
		}))
	}
	add("old-1", "Refund requests", "run-old", 2, earlier, 8)
	add("old-2", "Password resets", "run-old", 2, earlier, 8)
	add("new-1", "Billing", "run-new", 3, earlier.Add(time.Hour), 30)

	reader := NewTopicReader(&recordingStore{row: validWarpConfigRow()}, vectors)
	topics, total, err := reader.ListTopics(context.Background(), 15)
	require.NoError(t, err)
	require.Equal(t, 8, total)
	require.Len(t, topics, 2)
	require.ElementsMatch(t, []string{"Refund requests", "Password resets"}, []string{topics[0].Label, topics[1].Label})

	// Once the new run has written everything it set out to, it takes over.
	add("new-2", "Invoices", "run-new", 3, earlier.Add(time.Hour), 30)
	add("new-3", "Refunds", "run-new", 3, earlier.Add(time.Hour), 30)
	topics, total, err = reader.ListTopics(context.Background(), 15)
	require.NoError(t, err)
	require.Equal(t, 30, total)
	require.Len(t, topics, 3)
	require.ElementsMatch(t, []string{"Billing", "Invoices", "Refunds"}, []string{topics[0].Label, topics[1].Label, topics[2].Label})
}

// Cancelling has to reach a run that is still clustering, not only one that
// has got as far as naming things.
func TestWarpTopicsJobStopsWhileClustering(t *testing.T) {
	vectors := newFakeWarpVectorStore()
	var labelled atomic.Int32
	service, start := topicTestService(t, vectors, nil, func(context.Context, *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		labelled.Add(1)
		return TextTurn("Topic"), nil
	})
	metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancelled by the first checkpoint, which is the one before clustering.
	finalJSON, err := service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { cancel(); return nil })
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, labelled.Load(), "a cancelled run does not go on to spend model calls")
	require.Empty(t, vectors.idsIn(topicsNamespaceFor(schemas.WarpDefaultLogVectorStoreNamespace)))
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Contains(t, final.Message, "Stopped while clustering")

	_, _, err = kMeans(ctx, [][]float32{{1, 0}, {0, 1}}, 2, 10, 1)
	require.ErrorIs(t, err, context.Canceled)
}

// The same window over the same embedding space starts k-means from the same
// place, so recomputing unchanged data gives the same topics instead of a
// reshuffle. A different window is a different run.
func TestTopicsSeedFollowsTheWindow(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	meta := BackfillJobMeta{StartTime: start, EndTime: start.Add(24 * time.Hour), ConfigSignature: "openai|embed"}
	require.Equal(t, topicsSeed(meta), topicsSeed(meta))
	other := meta
	other.EndTime = meta.EndTime.Add(time.Hour)
	require.NotEqual(t, topicsSeed(meta), topicsSeed(other))
	other = meta
	other.ConfigSignature = "openai|embed-large"
	require.NotEqual(t, topicsSeed(meta), topicsSeed(other))
}

// The embedded and Pinecone stores keep each namespace's dimension in memory,
// so after a restart they cannot page a namespace until it has been declared
// again. Indexing declares the log namespace as a side effect; a process that
// has indexed nothing since it started has not, and topics must still run -
// and be readable - there.
func TestWarpTopicsWorkOnAStoreThatForgetsNamespaces(t *testing.T) {
	vectors := newFakeWarpVectorStore()
	service, start := topicTestService(t, vectors, nil, topicTestNaming)
	vectors.strict = true

	metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)
	finalJSON, err := service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, 6, final.Scanned)
	require.Equal(t, 2, final.Indexed)

	// A reader in a process that has declared nothing.
	vectors.created = nil
	topics, total, err := NewTopicReader(&recordingStore{row: validWarpConfigRow()}, vectors).ListTopics(context.Background(), 15)
	require.NoError(t, err)
	require.Len(t, topics, 2)
	require.Equal(t, 6, total)
}

// A store that returns conversations without their vectors cannot be
// clustered. That has to be said, not reported as an empty window: "nothing
// to cluster" sends an operator looking for missing data that is right there.
func TestWarpTopicsJobReportsAStoreThatReturnsNoVectors(t *testing.T) {
	vectors := newFakeWarpVectorStore()
	service, start := topicTestService(t, vectors, nil, topicTestNaming)
	vectors.omitVectors = true

	metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)
	finalJSON, err := service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorContains(t, err, "did not return their vectors")
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Contains(t, final.LastError, "6 request(s)")
}

// Naming runs from a job, with nobody's request behind it. The calls have to
// say so, or governance turns every one of them away and the topics come out
// named after their own words.
func TestWarpTopicsJobLabelsAsBackgroundWork(t *testing.T) {
	var background, calls atomic.Int32
	chat := func(ctx context.Context, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		calls.Add(1)
		if isBackgroundWork(ctx) {
			background.Add(1)
		}
		return TextTurn("Topic"), nil
	}
	vectors := newFakeWarpVectorStore()
	service, start := topicTestService(t, vectors, nil, chat)
	metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load())
	require.EqualValues(t, 2, background.Load())
}

// cappedHydrator returns at most limit logs per call, as the gateway's log
// reader does.
type cappedHydrator struct {
	*semanticLogReader
	limit int
	mu    sync.Mutex
	sizes []int
}

func (r *cappedHydrator) GetLogsByIDs(ctx context.Context, ids []string) ([]logstore.Log, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sizes = append(r.sizes, len(ids))
	if len(ids) > r.limit {
		ids = ids[:r.limit]
	}
	return r.semanticLogReader.GetLogsByIDs(ctx, ids)
}

// The log reader answers for a bounded number of ids per call and drops the
// rest without saying so. A run with more samples than that has to ask in
// pieces, or every cluster past the bound is named from nothing.
func TestTopicSampleTextsStaysWithinTheHydrationBound(t *testing.T) {
	logs := map[string]logstore.Log{}
	clusters := make([]topicCluster, 0, 60)
	for cluster := range 60 {
		var samples []string
		for sample := range topicsLabelSamples {
			id := fmt.Sprintf("log-%d-%d", cluster, sample)
			logs[id] = logstore.Log{ID: id, ContentSummary: fmt.Sprintf("question %d", cluster)}
			samples = append(samples, id)
		}
		clusters = append(clusters, topicCluster{sampleIDs: samples})
	}
	reader := &cappedHydrator{semanticLogReader: &semanticLogReader{logs: logs}, limit: topicsHydrationBatch}
	service := NewService(nil, WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(reader))
	defer service.Shutdown()

	texts := service.topicSampleTexts(context.Background(), clusters)
	require.Len(t, texts, 60*topicsLabelSamples, "every cluster's samples came back")
	for _, size := range reader.sizes {
		require.LessOrEqual(t, size, topicsHydrationBatch)
	}
	require.Len(t, reader.sizes, 3)
}

// A cluster is named from what its conversations were about. Named from their
// last messages, a cluster of people answering "all" to a clarifying question
// came out as "Cluster Analysis Placeholder": the model was shown the word
// "all" four times and had nothing to name.
func TestTopicLabelsReadTheOpeningQuestion(t *testing.T) {
	opening, reply := "Which model responds fastest on average this week?", "all"
	entry := &logstore.Log{
		ID: "log-1", Object: string(schemas.ResponsesRequest), Status: "success",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{
			responsesTurn(schemas.ResponsesInputMessageRoleUser, opening),
			responsesTurn(schemas.ResponsesInputMessageRoleAssistant, "Whose traffic should I compare?"),
			responsesTurn(schemas.ResponsesInputMessageRoleUser, reply),
		},
	}
	text := logUserText(entry)
	require.Contains(t, text, opening)
	require.Contains(t, text, reply)
	require.Less(t, strings.Index(text, opening), strings.Index(text, reply), "the question comes first")

	single := &logstore.Log{ID: "log-2", Object: string(schemas.ChatCompletionRequest), Status: "success", ContentSummary: opening}
	require.Equal(t, opening, logUserText(single))

	hidden := &logstore.Log{ID: "log-3", ContentHidden: true, ContentSummary: opening}
	require.Empty(t, logUserText(hidden))
}

// Topics are computed over every request in the deployment, and a topic is
// not a row: the store's row-level scope, which guards every other read, has
// nothing to apply to. A caller who may only see part of the traffic would be
// handed names, sizes and example requests drawn from all of it. So the tool
// answers only a caller nothing restricts, and sends the rest to the sample,
// which the store does scope.
func TestWarpListTopicsIsWithheldFromARestrictedCaller(t *testing.T) {
	vectors := newFakeWarpVectorStore()
	topicsNamespace := topicsNamespaceFor(schemas.WarpDefaultLogVectorStoreNamespace)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	require.NoError(t, vectors.Add(context.Background(), topicsNamespace, "t-1", []float32{1, 0, 0}, map[string]interface{}{
		"warp_topic": true, "topic_id": "t-1", "label": "Refund requests", "size": int64(9), "sample_log_ids": "log-1",
		"computed_at": now.Unix(), "total_clustered": int64(9), "run_id": "run", "run_topics": int64(1),
	}))
	deps := &ToolDeps{logManager: &fakeLogReader{}, topics: NewTopicReader(&recordingStore{row: validWarpConfigRow()}, vectors)}
	tool, ok := toolByName(buildTools(), "list_topics")
	require.True(t, ok)

	for name, ctx := range map[string]context.Context{
		"row scope":       queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB { return db }),
		"dimension scope": queryscope.WithDimensionScope(context.Background(), func(string) ([]string, bool) { return nil, true }),
	} {
		result, err := tool.execute(ctx, deps, map[string]any{})
		require.NoError(t, err, name)
		out := result.(map[string]any)
		encoded, err := sonic.MarshalString(out)
		require.NoError(t, err)
		require.NotContains(t, encoded, "Refund requests", name)
		require.NotContains(t, encoded, "log-1", name)
		require.Equal(t, false, out["available"], name)
		require.Contains(t, out["hint"], "take one bounded sample", name)
	}

	result, err := tool.execute(context.Background(), deps, map[string]any{})
	require.NoError(t, err)
	require.Len(t, result.(map[string]any)["topics"], 1, "a caller nothing restricts gets the topics")
}

// Two clusters that come back with the same name are one topic to anyone
// reading the list, however far apart their centres are. On real traffic the
// same name turned up on clusters between 0.70 and 0.93 alike, while clusters
// that were plainly different sat at 0.94 - so no similarity threshold could
// have joined the first without joining the second. The name can.
func TestWarpTopicsJobJoinsClustersGivenTheSameName(t *testing.T) {
	vectors := newFakeWarpVectorStore()
	// Every cluster is given the same name, whatever it holds.
	service, start := topicTestService(t, vectors, nil, topicTestChat("  refund Requests "))
	metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)
	finalJSON, err := service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)

	topicsNamespace := topicsNamespaceFor(schemas.WarpDefaultLogVectorStoreNamespace)
	ids := vectors.idsIn(topicsNamespace)
	require.Len(t, ids, 1)
	stored := vectors.adds[ids[0]]
	require.Equal(t, "refund Requests", stored["label"])
	require.Equal(t, int64(6), stored["size"], "the joined topic holds both clusters' requests")
	require.Equal(t, int64(1), stored["run_topics"])
	require.Len(t, strings.Split(stored["sample_log_ids"].(string), ","), topicsLabelSamples)
	require.InDelta(t, 1, float64(dot(vectors.embeddings[ids[0]], vectors.embeddings[ids[0]])), 1e-5, "the centre is recomputed from every member")

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, 1, final.Indexed)
	require.Equal(t, 1, final.MergedByName)
	require.Len(t, final.Topics, 1)
	require.Equal(t, 6, final.Topics[0].Size)
	require.Less(t, final.Topics[0].Cohesion, 0.9, "two subjects under one name are looser than either alone, and say so")
	require.Contains(t, final.Message, "1 more joined for sharing a name")
}

// TestWarpTopicsJobAgainstLiveVectorStores runs the whole job - read, cluster,
// name, write, replace, list - against each backend Bifrost supports.
//
// The fake store cannot stand in for this. Returning vectors was one of four
// things the job needed from a backend: it also has to page past the first
// page, hand properties back in a form the job can read (Redis returns every
// one as text), and delete by filter (Pinecone's serverless indexes refuse).
// Each of those passed the unit tests and failed on a real store.
//
// It needs the services of tests/docker-compose.yml, so it runs only when
// WARP_LIVE_VECTORSTORES is set. Hosts follow the vectorstore tests' variables.
func TestWarpTopicsJobAgainstLiveVectorStores(t *testing.T) {
	if os.Getenv("WARP_LIVE_VECTORSTORES") == "" {
		t.Skip("set WARP_LIVE_VECTORSTORES=1 with the vector stores running to run this")
	}
	env := func(key, fallback string) string {
		if value := os.Getenv(key); value != "" {
			return value
		}
		return fallback
	}
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	sv := func(v string) *schemas.SecretVar { return schemas.NewSecretVar(v) }
	configs := map[string]*vectorstore.Config{
		"chromem":  {Enabled: true, Type: vectorstore.VectorStoreTypeChromem, Config: vectorstore.ChromemConfig{}},
		"weaviate": {Enabled: true, Type: vectorstore.VectorStoreTypeWeaviate, Config: vectorstore.WeaviateConfig{Scheme: "http", Host: sv(env("WEAVIATE_HOST", "localhost:9000"))}},
		"qdrant":   {Enabled: true, Type: vectorstore.VectorStoreTypeQdrant, Config: vectorstore.QdrantConfig{Host: *sv(env("QDRANT_HOST", "localhost")), Port: *sv(env("QDRANT_PORT", "6334"))}},
		"redis":    {Enabled: true, Type: vectorstore.VectorStoreTypeRedis, Config: vectorstore.RedisConfig{Addr: sv(env("REDIS_ADDR", "localhost:6379"))}},
		"pinecone": {Enabled: true, Type: vectorstore.VectorStoreTypePinecone, Config: vectorstore.PineconeConfig{APIKey: *sv(env("PINECONE_API_KEY", "pclocal")), IndexHost: *sv(env("PINECONE_INDEX_HOST", "localhost:5081"))}},
	}
	for _, name := range []string{"chromem", "weaviate", "qdrant", "redis", "pinecone"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			store, err := vectorstore.NewVectorStore(ctx, configs[name], logger)
			require.NoError(t, err)
			namespace := fmt.Sprintf("WarpLive%d", time.Now().UnixNano()%1000000)
			row := validWarpConfigRow()
			row.LogVectorStoreNamespace = namespace
			config := configFromRow(row)
			topicsNamespace := topicsNamespaceFor(namespace)
			t.Cleanup(func() {
				_ = store.DeleteNamespace(context.Background(), namespace)
				_ = store.DeleteNamespace(context.Background(), topicsNamespace)
			})
			_, err = ensureWarpNamespace(ctx, store, namespace, config.EmbeddingDimension)
			require.NoError(t, err)

			start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
			rng := rand.New(rand.NewSource(1))
			logs := map[string]logstore.Log{}
			const perSubject = 130 // two pages of 200 between them, so paging is exercised
			for subject, question := range []string{"how do I get a refund", "reset my password please"} {
				for n := range perSubject {
					id := uuid.NewString()
					vector := make([]float32, config.EmbeddingDimension)
					for d := range vector {
						vector[d] = float32(rng.NormFloat64() * 0.01)
					}
					vector[subject] = 1
					at := start.Add(time.Duration(n) * time.Minute)
					logs[id] = logstore.Log{ID: id, Timestamp: at, Object: string(schemas.ChatCompletionRequest), Status: "success", ContentSummary: question}
					require.NoError(t, store.Add(ctx, namespace, id, unitVector(vector), map[string]interface{}{
						"log_id": id, "timestamp": at.Unix(), "warp_log": true, "status": "success", "object": "chat_completion",
						"provider": "openai", "model": "gpt-4o", "team_ids": []string{}, "customer_ids": []string{}, "business_unit_ids": []string{},
					}))
				}
			}
			time.Sleep(2 * time.Second)

			service := NewService(nil,
				WithConfigStore(&recordingStore{row: row}), WithLogReader(&semanticLogReader{logs: logs}),
				WithVectorStore(store), WithEmbeddingExecutor(backfillEmbeddingExecutor), WithChatFunc(topicTestNaming),
			)
			defer service.Shutdown()
			reader := NewTopicReader(&recordingStore{row: row}, store)

			for run := 1; run <= 2; run++ {
				metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
				require.NoError(t, err)
				finalJSON, err := service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: uuid.NewString(), Metadata: metaJSON}, func(string) error { return nil })
				require.NoError(t, err, "run %d", run)
				var final BackfillJobMeta
				require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
				require.Equal(t, 2*perSubject, final.Scanned, "run %d reads every vector", run)
				require.Equal(t, 2, final.Indexed, "run %d", run)
				require.Empty(t, final.LastError, "run %d", run)
				time.Sleep(2 * time.Second)

				topics, total, err := reader.ListTopics(ctx, 15)
				require.NoError(t, err, "run %d", run)
				require.Equal(t, 2*perSubject, total)
				require.Len(t, topics, 2, "run %d leaves only its own topics", run)
				for _, topic := range topics {
					require.GreaterOrEqual(t, topic.Size, perSubject-5)
					require.Len(t, topic.SampleLogIDs, topicsLabelSamples)
					require.Greater(t, topic.Cohesion, 0.5)
					require.Equal(t, start.Unix(), topic.WindowStart.Unix())
				}
				all, _, err := store.GetAll(ctx, topicsNamespace, nil, []string{"label", "run_id"}, nil, 100)
				require.NoError(t, err)
				require.Len(t, all, 2, "run %d: nothing stale is left in the store", run)
				time.Sleep(1100 * time.Millisecond) // computed_at is in seconds
			}
		})
	}
}

// Stores do not agree on what a property comes back as. Most decode numbers
// and booleans; Redis keeps every field as text and returns it that way, and
// a job that read only the typed forms took "true" and "1788000000" for
// missing values and found nothing to cluster.
func TestTopicPropertiesAreReadWhateverTheStoreReturns(t *testing.T) {
	for _, value := range []interface{}{1788000000, int64(1788000000), float64(1788000000), "1788000000", " 1788000000 ", "1788000000.0"} {
		require.Equal(t, 1788000000, propertyInt(value), "%#v", value)
	}
	for _, value := range []interface{}{nil, "", "soon", true, []string{"1"}} {
		require.Zero(t, propertyInt(value), "%#v", value)
	}
	for _, value := range []interface{}{true, "true", "TRUE", "1", 1, int64(1), float64(1)} {
		require.True(t, propertyBool(value), "%#v", value)
	}
	for _, value := range []interface{}{nil, false, "false", "0", "", "yes please", 0} {
		require.False(t, propertyBool(value), "%#v", value)
	}
}

// repeatingStore hands every page back twice over, as a store that walks its
// keys can while they are being written to.
type repeatingStore struct {
	*fakeWarpVectorStore
}

func (r *repeatingStore) GetAll(ctx context.Context, namespace string, queries []vectorstore.Query, fields []string, cursor *string, limit int64) ([]vectorstore.SearchResult, *string, error) {
	results, next, err := r.fakeWarpVectorStore.GetAll(ctx, namespace, queries, fields, cursor, limit)
	return append(results, results...), next, err
}

func TestWarpTopicsJobCountsARepeatedEntryOnce(t *testing.T) {
	vectors := newFakeWarpVectorStore()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := topicTestLogs(start)
	for id, vector := range topicTestVectors() {
		require.NoError(t, vectors.Add(context.Background(), schemas.WarpDefaultLogVectorStoreNamespace, id, vector, map[string]interface{}{"log_id": id, "timestamp": logs[id].Timestamp.Unix(), "warp_log": true}))
	}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(&semanticLogReader{logs: logs}),
		WithVectorStore(&repeatingStore{vectors}), WithEmbeddingExecutor(backfillEmbeddingExecutor), WithChatFunc(topicTestNaming),
	)
	defer service.Shutdown()
	metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)
	finalJSON, err := service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, 6, final.Scanned)
}
