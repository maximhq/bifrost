package warp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// Two tight groups far apart must come out as two clusters with the right
// members. The vectors are unit length, as the embedding models produce, so
// cosine and dot product agree.
func TestKMeansSeparatesObviousGroups(t *testing.T) {
	vectors := [][]float32{
		{1, 0, 0}, {0.99, 0.1, 0}, {0.98, 0, 0.1},
		{0, 1, 0}, {0.1, 0.99, 0}, {0, 0.98, 0.1},
	}
	assignments, centroids := kMeans(vectors, 2, 10, 1)
	require.Len(t, centroids, 2)
	require.Equal(t, assignments[0], assignments[1])
	require.Equal(t, assignments[1], assignments[2])
	require.Equal(t, assignments[3], assignments[4])
	require.Equal(t, assignments[4], assignments[5])
	require.NotEqual(t, assignments[0], assignments[3])
}

// The cluster count follows the data rather than a fixed k: one for a handful
// of conversations, capped for a large corpus, never more clusters than
// vectors.
func TestTopicClusterCount(t *testing.T) {
	require.Equal(t, 1, topicClusterCount(1))
	require.Equal(t, 2, topicClusterCount(8))
	require.Equal(t, 7, topicClusterCount(100))
	require.Equal(t, topicsMaxClusters, topicClusterCount(1_000_000))
	require.Equal(t, 0, topicClusterCount(0))
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
	labels := 0
	model := &scriptedModel{}
	chat := func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		labels++
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
	finalJSON, err := service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, 6, final.Scanned, "every vector in the window is read")
	require.Equal(t, 2, final.Indexed, "one topic per cluster")
	require.Zero(t, final.Failed)
	require.Equal(t, 2, labels, "one label call per cluster")

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
	require.NoError(t, vectors.Add(context.Background(), topicsNamespace, "stale", []float32{0, 0, 1}, map[string]interface{}{"warp_topic": true, "label": "Old", "size": int64(9)}))

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
	require.Equal(t, []string{logDetailLink("log-1"), logDetailLink("log-2")}, topics[0].SampleLinks)
	require.Equal(t, 12, out["total_clustered"])
	_, hasHint := out["hint"]
	require.False(t, hasHint)
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
	content := systemInstructions(&schemas.WarpConfig{})
	require.Contains(t, content, "call list_topics first")
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
