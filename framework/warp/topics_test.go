package warp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/framework/sidekiq"
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

// A missing embedding provider or model is a Warp that is not set up, which
// the start route answers with 503; only a bad time range is the caller's 400.
func TestBuildTopicsJobMetaErrorClasses(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	build := func(row *tables.TableWarpConfig, end time.Time) error {
		service := NewService(nil,
			WithConfigStore(&recordingStore{row: row}), WithLogReader(topicTestReader(t, topicTestLogs(start))),
			WithVectorStore(newFakeWarpVectorStore()),
		)
		defer service.Shutdown()
		_, err := service.BuildTopicsJobMeta(context.Background(), start, end)
		return err
	}

	noProvider := validWarpConfigRow()
	noProvider.EmbeddingProvider = ""
	noModel := validWarpConfigRow()
	noModel.EmbeddingModel = ""
	for name, row := range map[string]*tables.TableWarpConfig{"no embedding provider": noProvider, "no embedding model": noModel} {
		err := build(row, start.Add(time.Hour))
		require.ErrorIs(t, err, ErrUnavailable, name)
		require.NotErrorIs(t, err, ErrInvalidConfig, name)
	}

	require.NoError(t, build(validWarpConfigRow(), start.Add(time.Hour)), "the same setup with a full config starts")
	err := build(validWarpConfigRow(), start)
	require.ErrorIs(t, err, ErrInvalidConfig, "an empty window is the caller's error")
	require.NotErrorIs(t, err, ErrUnavailable)
}

// The prompt has to send topic questions here first.
func TestWarpSystemPromptPrefersListTopics(t *testing.T) {
	content := systemInstructions(&schemas.WarpConfig{}, true)
	require.Contains(t, content, "call list_topics first, with the window and filters the question names")
	require.Contains(t, content, "Only if it reports that no topics have been built yet")
	require.NotContains(t, content, "not available to the person asking", "a restricted caller is answered from their own requests now")
}

// Topics are counted over whatever window and slice the question names, and
// each row carries its trend. The model is told what one call answers - so it
// neither declines a time range it can serve nor compares two calls by hand -
// and what is still not there, so it can say so and stop rather than slice
// the logs looking for it.
func TestWarpSystemPromptSaysWhatTopicsCannotAnswer(t *testing.T) {
	content := systemInstructions(&schemas.WarpConfig{}, true)
	for _, line := range []string{
		"Topics are counted over a window",
		"defaults to the last 7 days",
		"trend block against the period of equal length just before it",
		"new_in_window",
		"with interval hour or day",
		"growing or shrinking, new, costly or failing",
		`"this week against last week" is one call`,
		"The requests behind a topic are reached with topic_ids",
		"never guess one",
		"sessions are sessions, not people",
		"list_topics with group_by theme",
		"the themes are the answer and the topics the detail",
		"is not available yet",
		"Do not rebuild a topic breakdown",
	} {
		require.Contains(t, content, line)
	}
	for _, gone := range []string{"Topics are a snapshot", "a different time range", "Call list_topics once"} {
		require.NotContains(t, content, gone)
	}
	require.Contains(t, content, "link each topic's name to its own link")
	require.NotContains(t, content, "Leave a topic's name unlinked")
	require.Contains(t, content, `say "requests" when you report them`)
}

// A survey scoped to a person, team or time range ("what kinds of tasks was
// Rohan doing") is a list_topics call with that window and those filters, in
// both setups. The semantic guidance used to send it to a query_logs sample
// first while the base prompt sent it to list_topics, and nothing said which
// one won. The sample is only the fallback for a deployment with no topics.
func TestWarpSystemPromptSendsScopedSurveysToListTopics(t *testing.T) {
	for _, semantic := range []bool{true, false} {
		content := systemInstructions(&schemas.WarpConfig{}, semantic)
		require.Contains(t, content, "call list_topics first, with the window and filters the question names")
		require.Contains(t, content, "Only if it reports that no topics have been built yet")
		require.Contains(t, content, "filled in from returned and total_matching")
		// query_usage_by has no model dimension and refuses one, so the sample's
		// shape comes from the tool that owns the per-model split.
		require.Contains(t, content, "query_model_performance for the models in it")
		require.NotContains(t, content, "query_usage_by with dimension model")
		require.NotContains(t, content, "never goes to list_topics")
	}
	require.Contains(t, SemanticSearchGuidance, "A survey question goes to list_topics as described above, with the window and filters it names")
	require.Contains(t, SemanticSearchGuidance, "Only when list_topics reports that no topics have been built yet")
	require.NotContains(t, SemanticSearchGuidance, "A scoped survey")
}

// Discovery reads the pool from the log store and never the vector store, so
// a deployment with no vector store still registers it and keeps the queue
// upkeep starts it through. Only the run that pages vectors out needs one.
func TestWarpRegisterTopicsKeepsDiscoveryWithoutAVectorStore(t *testing.T) {
	for _, withVectors := range []bool{false, true} {
		options := []Option{WithConfigStore(&recordingStore{row: assignTestRow()}), WithLogReader(topicTestReader(t, map[string]logstore.Log{}))}
		if withVectors {
			options = append(options, WithVectorStore(newFakeWarpVectorStore()))
		}
		service := NewService(nil, options...)
		t.Cleanup(service.Shutdown)
		runner := sidekiq.New(nil, nil, 1, "")
		service.RegisterTopics(runner)

		require.NotNil(t, service.topicJobs, "vector store %v: upkeep has a queue to start discovery through", withVectors)
		_, summarized := runner.Summarize(TopicDiscoveryJobKind, "{}")
		require.True(t, summarized, "vector store %v: discovery is registered", withVectors)
	}
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
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(topicTestReader(t, logs)),
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
		reader = topicTestReader(t, logs)
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

// A topic is named from each text once, the samples first, so a prompt sent
// to several providers is shown to the model as one request.
func TestLabelTextsShowsEachTextOnce(t *testing.T) {
	texts := map[string]string{"a": "graviton summary", "b": "graviton summary", "c": "fix the docs", "d": "", "e": "cut a release"}
	require.Equal(t, []string{"graviton summary", "fix the docs", "cut a release"},
		labelTexts(texts, []string{"a", "b", "c"}, []string{"d", "c", "e", "missing"}))
}

// Names written after a merge are told the names merged, so the model names
// what they share rather than picking one of them again.
func TestLabelTopicOffersFormerNames(t *testing.T) {
	var prompt string
	chat := func(_ context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		prompt = responsesInputText(req.Input)
		return TextTurn("Coding Agent Work"), nil
	}
	label, err := askTopicLabel(context.Background(), chat, configFromRow(validWarpConfigRow()), []string{"fix the docs"}, []string{"Documentation Updates", "Release Delay Management"})
	require.NoError(t, err)
	require.Equal(t, "Coding Agent Work", label)
	require.Contains(t, prompt, "Documentation Updates; Release Delay Management")

	_, err = labelTopic(context.Background(), chat, configFromRow(validWarpConfigRow()), []string{"fix the docs"})
	require.NoError(t, err)
	require.NotContains(t, prompt, "merged", "a topic named the first time has no former names")
}

// An answer with no name in it is no name to askTopicLabel, so a caller with
// a name already keeps it; labelTopic, naming a topic the first time, falls
// back to the texts' own words.
func TestAskTopicLabelReportsAnAnswerWithNoName(t *testing.T) {
	chat := func(context.Context, *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return TextTurn(" \"\" "), nil
	}
	config := configFromRow(validWarpConfigRow())
	label, err := askTopicLabel(context.Background(), chat, config, []string{"refund please"}, nil)
	require.NoError(t, err)
	require.Empty(t, label)
	label, err = labelTopic(context.Background(), chat, config, []string{"refund please"})
	require.NoError(t, err)
	require.Equal(t, fallbackTopicLabel([]string{"refund please"}), label)
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
	reader := &countingHydrator{semanticLogReader: topicTestReader(t, topicTestLogs(start))}
	vectors := newFakeWarpVectorStore()
	service, _ := topicTestService(t, vectors, reader, topicTestNaming)

	metaJSON, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = service.RunTopicsJob(context.Background(), tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)
	topics, err := service.topicStore().ListWarpTopics(context.Background(), logstore.WarpTopicFilter{})
	require.NoError(t, err)
	require.Len(t, topics, 2)
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

// While a run is writing, the store holds the previous topics and some of the
// new ones. list_topics answers from the newest run that finished writing, so
// a question asked in that moment gets the previous topics whole rather than
// a few of the new ones.

// A label is cut by characters, not bytes, so a name in a script that takes
// more than one byte a character keeps its full length and stays valid text.
func TestWarpCleanTopicLabelCutsByCharacter(t *testing.T) {
	label := cleanTopicLabel(strings.Repeat("é", topicsMaxLabelChars+10))
	require.True(t, utf8.ValidString(label))
	require.Equal(t, topicsMaxLabelChars, utf8.RuneCountInString(label))
	require.Equal(t, "Refund Requests", cleanTopicLabel("\"Refund Requests.\"\nmore"))
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
	topics, err := service.topicStore().ListWarpTopics(context.Background(), logstore.WarpTopicFilter{})
	require.NoError(t, err)
	require.Empty(t, topics, "and writes nothing")
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

// Two clusters that come back with the same name are one topic to anyone
// reading the list, however far apart their centres are. On real traffic the
// same name turned up on clusters between 0.70 and 0.93 alike, while clusters
// that were plainly different sat at 0.94 - so no similarity threshold could
// have joined the first without joining the second. The name can.

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
	// Out of range saturates at int's bounds instead of wrapping or landing on
	// whatever the CPU's float conversion gives.
	for _, value := range []interface{}{1e300, float32(math.MaxFloat32), "1e300", math.Inf(1)} {
		require.Equal(t, math.MaxInt, propertyInt(value), "%#v", value)
	}
	for _, value := range []interface{}{-1e300, "-1e300", math.Inf(-1)} {
		require.Equal(t, math.MinInt, propertyInt(value), "%#v", value)
	}
	require.Zero(t, propertyInt(math.NaN()))
	require.Zero(t, propertyInt("NaN"))
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
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(topicTestReader(t, logs)),
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

// topicTestLogStore is a real SQLite log store. The topics are rows in it and
// the job copies each request's columns from its log row, so a fake would
// have to reproduce the store to prove anything.
func topicTestLogStore(t *testing.T) logstore.LogStore {
	t.Helper()
	store, err := logstore.NewLogStore(context.Background(), &logstore.Config{
		Enabled: true, Type: logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "warp.db")},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	return store
}

// topicTestReader is the reader the topic tests run through: the given logs
// for hydration, and the same logs in a log store that keeps the topic tables.
func topicTestReader(t *testing.T, logs map[string]logstore.Log) *semanticLogReader {
	t.Helper()
	store := topicTestLogStore(t)
	for _, entry := range logs {
		require.NoError(t, store.Create(context.Background(), &entry))
	}
	return &semanticLogReader{logs: logs, topics: store}
}

// listAllTopics lists every topic over all time, as a question with no
// window would, returning the topics and the total they are shares of.
func listAllTopics(t *testing.T, reader *TopicReader) ([]Topic, int) {
	t.Helper()
	list, err := reader.ListTopics(context.Background(), allTimeTopicQuery(15))
	require.NoError(t, err)
	return list.Topics, int(list.TotalRequests)
}

// A name search runs before the list is cut to its limit: a small topic whose
// name matches is found even when larger ones would fill the limit.
func TestTopicReaderSearchesNamesBeforeTheLimit(t *testing.T) {
	store := topicTestLogStore(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "big", Label: "Password resets", EmbeddingSignature: topicTestSignature(), CreatedAt: at},
		{ID: "small", Label: "Refund fraud", EmbeddingSignature: topicTestSignature(), CreatedAt: at},
	}))
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, []logstore.WarpTopicAssignment{
		{LogID: "b1", TopicID: "big", Timestamp: at}, {LogID: "b2", TopicID: "big", Timestamp: at},
		{LogID: "s1", TopicID: "small", Timestamp: at},
	}))
	reader := NewTopicReader(&recordingStore{row: validWarpConfigRow()}, store)

	query := allTimeTopicQuery(1)
	query.NameContains = "  REFUND "
	list, err := reader.ListTopics(ctx, query)
	require.NoError(t, err)
	require.Len(t, list.Topics, 1)
	require.Equal(t, "small", list.Topics[0].ID)

	list, err = reader.ListTopics(ctx, allTimeTopicQuery(1))
	require.NoError(t, err)
	require.Equal(t, "big", list.Topics[0].ID, "with no name, the largest topic still leads")
}

// allTimeTopicQuery is a query whose window holds every test timestamp.
func allTimeTopicQuery(limit int) TopicQuery {
	start := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	return TopicQuery{Filter: logstore.WarpTopicAssignmentFilter{StartTime: &start, EndTime: &end}, Limit: limit}
}

// topicTestSignature is the embedding signature the test configuration
// carries, which is what every topic row is stamped with.
func topicTestSignature() string {
	return embeddingConfigSignature(configFromRow(validWarpConfigRow()))
}

func topicLabelsByID(topics []logstore.WarpTopic) map[string]string {
	labels := make(map[string]string, len(topics))
	for _, topic := range topics {
		labels[topic.ID] = topic.Label
	}
	return labels
}

// The job reads the log vectors, clusters them, labels each cluster through
// the model, and files the result in the log store: one row per topic, and
// one row per request saying which topic it is in.
func TestWarpTopicsJobClustersLabelsAndStores(t *testing.T) {
	var labels atomic.Int32
	chat := func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		labels.Add(1)
		require.Nil(t, req.Params.Tools, "labelling is a plain completion, no tools")
		if contains(responsesInputText(req.Input), "refund") {
			return TextTurn("Refund requests"), nil
		}
		return TextTurn("Password resets"), nil
	}
	service, start := topicTestService(t, newFakeWarpVectorStore(), nil, chat)
	store := service.topicStore()
	ctx := context.Background()

	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)
	finalJSON, err := service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, 6, final.Scanned, "every vector in the window is read")
	require.Equal(t, 2, final.Indexed, "one topic per cluster")
	require.Zero(t, final.Failed)
	require.Zero(t, final.Skipped)
	require.EqualValues(t, 2, labels.Load(), "one label call per cluster")

	topics, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{})
	require.NoError(t, err)
	require.Len(t, topics, 2)
	for _, topic := range topics {
		require.Equal(t, logstore.WarpTopicKindTopic, topic.Kind)
		require.Equal(t, logstore.WarpTopicStatusActive, topic.Status)
		require.Equal(t, topicTestSignature(), topic.EmbeddingSignature, "a topic says which embedding model it was built with")
		centroid, err := logstore.DecodeWarpVector(topic.Centroid)
		require.NoError(t, err)
		require.Len(t, centroid, 3, "the centroid lives in the same space as the logs")
		require.Len(t, topic.ExampleLogIDs(), 3, "every member of a cluster of three is an example")
		require.Greater(t, topic.Cohesion, 0.9)
	}
	usage, err := store.SummarizeWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{})
	require.NoError(t, err)
	byLabel := topicLabelsByID(topics)
	seen := map[string]int{}
	for _, entry := range usage {
		seen[byLabel[entry.TopicID]] = int(entry.Requests)
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
	require.Contains(t, final.Message, "Clustered 6 request(s) into 2 topic(s)")
}

// Every request the run read is filed with the columns a topic question is
// asked by - who sent it, what it cost - copied from its log row, not from
// the vector's metadata, which is a lossy copy of them.
func TestWarpTopicsJobFilesEveryRequestFromItsLog(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := topicTestLogs(start)
	first := logs["log-0"]
	team, app, cost, session := "team-a", "cursor", 0.5, "session-1"
	first.TeamID, first.App, first.Cost, first.SessionID = &team, &app, &cost, &session
	first.Provider, first.Model, first.TotalTokens = "openai", "gpt-4o", 42
	logs["log-0"] = first
	service, _ := topicTestService(t, newFakeWarpVectorStore(), topicTestReader(t, logs), topicTestNaming)
	store := service.topicStore()
	ctx := context.Background()

	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)

	all, err := store.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{}, 100)
	require.NoError(t, err)
	require.Len(t, all, 6, "one row per request read")
	topics, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Statuses: []string{logstore.WarpTopicStatusActive}})
	require.NoError(t, err)
	byLabel := topicLabelsByID(topics)
	for _, row := range all {
		require.NotEmpty(t, row.TopicID, "every request here is in a topic")
		require.Greater(t, row.Similarity, 0.9, "and was close to its centre")
		require.False(t, row.AssignedAt.IsZero())
		require.True(t, row.Timestamp.Equal(logs[row.LogID].Timestamp), "%s: %s", row.LogID, row.Timestamp)
	}
	filed, err := store.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{LogIDs: []string{"log-0"}}, 1)
	require.NoError(t, err)
	require.Len(t, filed, 1)
	require.Equal(t, "Refund requests", byLabel[filed[0].TopicID])
	require.Equal(t, "team-a", filed[0].TeamID)
	require.Equal(t, "cursor", filed[0].App)
	require.Equal(t, "session-1", filed[0].SessionID)
	require.Equal(t, "gpt-4o", filed[0].Model)
	require.InDelta(t, 0.5, filed[0].Cost, 1e-9)
	require.Equal(t, 42, filed[0].TotalTokens)

	// Which is what lets the rows be counted the way logs are.
	usage, err := store.SummarizeWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{TeamIDs: []string{"team-a"}})
	require.NoError(t, err)
	require.Len(t, usage, 1)
	require.EqualValues(t, 1, usage[0].Requests)
	require.InDelta(t, 0.5, usage[0].Cost, 1e-9)
}

// A request indexed and then deleted from the log store has no row to copy.
// It is counted as skipped and said so, not filed from nothing.
func TestWarpTopicsJobSkipsRequestsWhoseLogIsGone(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := topicTestLogs(start)
	reader := topicTestReader(t, logs)
	require.NoError(t, reader.topics.(logstore.LogStore).DeleteLog(context.Background(), "log-5"))
	service, _ := topicTestService(t, newFakeWarpVectorStore(), reader, topicTestNaming)
	ctx := context.Background()

	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)
	finalJSON, err := service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, 6, final.Scanned, "the vector was still read and clustered")
	require.Equal(t, 1, final.Skipped)
	require.Contains(t, final.Message, "1 were indexed but are no longer in the log store")
	all, err := service.topicStore().ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{}, 100)
	require.NoError(t, err)
	require.Len(t, all, 5)
}

// The requests that matched no topic are kept with their vectors, so the next
// discovery can start from them. A request that is in a topic now is taken
// out of that pool, wherever an earlier run had left it.
func TestWriteTopicsPoolsWhatMatchedNoTopic(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := topicTestLogs(start)
	store := topicTestReader(t, logs).topics
	ctx := context.Background()
	vectors := topicTestVectors()
	// log-0 was pooled by an earlier run.
	require.NoError(t, store.AddWarpTopicUnmatched(ctx, []logstore.WarpTopicUnmatched{{
		LogID: "log-0", Timestamp: logs["log-0"].Timestamp, Vector: logstore.EncodeWarpVector(vectors["log-0"]), EmbeddingSignature: "sig",
	}}))

	cluster := topicCluster{sum: make([]float32, 3), label: "Refund requests", sampleIDs: []string{"log-0", "log-1"}}
	for _, id := range []string{"log-0", "log-1", "log-2", "log-3"} {
		for d, value := range vectors[id] {
			cluster.sum[d] += value
		}
		cluster.size++
		cluster.members = append(cluster.members, topicMember{logID: id, similarity: 0.8})
	}
	run := &topicRun{floor: 0.5}
	for _, id := range []string{"log-4", "log-5"} {
		run.pool(topicVector{logID: id, vector: vectors[id]})
		run.placed = append(run.placed, requestPlacement{logID: id, cluster: -1})
	}
	meta := BackfillJobMeta{ConfigSignature: "sig", StartTime: start, EndTime: start.Add(24 * time.Hour)}
	written, err := fileTestTopics(ctx, store, &meta, run, []topicCluster{cluster}, 0)
	require.NoError(t, err)
	require.Len(t, written, 1)
	require.InDelta(t, 0.5, written[0].AssignFloor, 1e-9, "the floor the run settled on travels with the topic")
	require.Equal(t, 1, meta.Indexed)
	require.Zero(t, meta.Skipped)

	usage, err := store.SummarizeWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{})
	require.NoError(t, err)
	counts := map[string]int64{}
	for _, entry := range usage {
		counts[entry.TopicID] = entry.Requests
	}
	require.Equal(t, map[string]int64{written[0].ID: 4, "": 2}, counts, "the unmatched requests are filed too, under no topic")

	pooled, err := store.ListWarpTopicUnmatched(ctx, "sig", 10)
	require.NoError(t, err)
	require.Equal(t, []string{"log-5", "log-4"}, warpUnmatchedIDs(pooled), "pooled with their vectors; log-0 is in a topic now and left the pool")
	vector, err := logstore.DecodeWarpVector(pooled[0].Vector)
	require.NoError(t, err)
	require.Equal(t, vectors["log-5"], vector)
}

// The pool keeps the vectors of only so many unmatched requests, but every
// unmatched request is filed under no topic. One past the cap must not keep a
// row an earlier run wrote against a topic this run replaced.
func TestWriteTopicsFilesUnmatchedPastThePoolCap(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := topicTestLogs(start)
	store := topicTestReader(t, logs).topics
	ctx := context.Background()
	vectors := topicTestVectors()
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, []logstore.WarpTopicAssignment{{
		LogID: "log-5", TopicID: "stale", Timestamp: logs["log-5"].Timestamp, LogCreatedAt: logs["log-5"].CreatedAt,
	}}))

	cluster := topicCluster{sum: make([]float32, 3), label: "Refund requests", sampleIDs: []string{"log-0"}}
	for _, id := range []string{"log-0", "log-1", "log-2"} {
		for d, value := range vectors[id] {
			cluster.sum[d] += value
		}
		cluster.size++
		cluster.members = append(cluster.members, topicMember{logID: id, similarity: 0.9})
	}
	run := newTopicRun(topicLimits{pooled: 1}, 1)
	run.floor, run.clusters, run.centres = 0.9, []topicCluster{cluster}, [][]float32{cluster.centroid()}
	for _, id := range []string{"log-3", "log-4", "log-5"} {
		run.assign(topicVector{logID: id, vector: vectors[id]})
	}
	require.Equal(t, 3, run.unmatched)
	require.Len(t, run.pooled, 1, "the pool stops at its cap")

	meta := BackfillJobMeta{ConfigSignature: "sig", StartTime: start, EndTime: start.Add(24 * time.Hour)}
	written, err := fileTestTopics(ctx, store, &meta, run, run.clusters, 0)
	require.NoError(t, err)
	require.Len(t, written, 1)

	rows, err := store.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{}, 100)
	require.NoError(t, err)
	byLog := map[string]string{}
	for _, row := range rows {
		byLog[row.LogID] = row.TopicID
	}
	require.Equal(t, map[string]string{
		"log-0": written[0].ID, "log-1": written[0].ID, "log-2": written[0].ID,
		"log-3": "", "log-4": "", "log-5": "",
	}, byLog, "every unmatched request has a row under no topic, the pooled one or not")

	pooled, err := store.ListWarpTopicUnmatched(ctx, "sig", 10)
	require.NoError(t, err)
	require.Equal(t, []string{"log-3"}, warpUnmatchedIDs(pooled), "only the pooled request is kept with its vector")
}

// The writer files requests as they are placed and keeps less than a batch:
// the clustered requests leave the run once their topics are written, and
// nothing after them is kept - not even which requests matched no topic. A
// failed run retires the topics it wrote and deletes the rows it filed under
// no topic, which would otherwise count as unmatched once the model has topics
// again; it finds them in the store as the rows with no topic assigned since
// it started, so an older one is left alone.
func TestTopicWriterHoldsABatchAndAbandonsCleanly(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := topicTestLogs(start)
	store := topicTestReader(t, logs).topics
	ctx := context.Background()
	vectors := topicTestVectors()
	cluster := topicCluster{sum: make([]float32, 3), label: "Refund requests", sampleIDs: []string{"log-0"}}
	for _, id := range []string{"log-0", "log-1", "log-2"} {
		for d, value := range vectors[id] {
			cluster.sum[d] += value
		}
		cluster.size++
		cluster.members = append(cluster.members, topicMember{logID: id, similarity: 0.9})
	}
	run := newTopicRun(topicLimits{}, 1)
	run.floor, run.clusters, run.centres = 0.99, []topicCluster{cluster}, [][]float32{cluster.centroid()}
	meta := BackfillJobMeta{ConfigSignature: "sig", StartTime: start, EndTime: start.Add(24 * time.Hour)}
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, []logstore.WarpTopicAssignment{{
		LogID: "earlier", Timestamp: start, AssignedAt: time.Now().UTC().Add(-time.Hour),
	}}))
	writer := newTopicWriter(store, &meta, run, 2)

	require.NoError(t, writer.file(ctx))
	require.Nil(t, run.clusters[0].members, "the clustered requests leave the run once filed")
	require.Less(t, len(writer.pending), 2, "less than a batch is kept back")
	for _, id := range []string{"log-3", "log-4", "log-5"} {
		run.assign(topicVector{logID: id, vector: vectors[id]})
		require.NoError(t, writer.file(ctx))
		require.Empty(t, run.placed)
		require.Less(t, len(writer.pending), 2)
	}
	require.NoError(t, writer.finish(ctx))
	byLog := func() map[string]string {
		rows, err := store.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{}, 100)
		require.NoError(t, err)
		filed := map[string]string{}
		for _, row := range rows {
			filed[row.LogID] = row.TopicID
		}
		return filed
	}
	topic := writer.topics[0].ID
	require.Equal(t, map[string]string{
		"log-0": topic, "log-1": topic, "log-2": topic, "log-3": "", "log-4": "", "log-5": "", "earlier": "",
	}, byLog(), "every request is filed; the password requests matched no refund topic")

	require.NoError(t, writer.abandon(ctx))
	require.Equal(t, map[string]string{"log-0": topic, "log-1": topic, "log-2": topic, "earlier": ""}, byLog(),
		"the rows this run filed under no topic are deleted, and the one from before it stays")
	topics, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{IDs: []string{writer.topics[0].ID}})
	require.NoError(t, err)
	require.Equal(t, logstore.WarpTopicStatusRetired, topics[0].Status, "and the topic is retired, so its rows stop counting")
}

// fileTestTopics writes clusters as a run that merged into them would: their
// members and whatever else the run placed are filed, then the topics are
// made active. It returns the topics as written.
func fileTestTopics(ctx context.Context, store logstore.WarpTopicStore, meta *BackfillJobMeta, run *topicRun, clusters []topicCluster, batchSize int) ([]logstore.WarpTopic, error) {
	run.clusters = clusters
	writer := newTopicWriter(store, meta, run, batchSize)
	if err := writer.file(ctx); err != nil {
		return writer.topics, err
	}
	if err := writer.finish(ctx); err != nil {
		return writer.topics, err
	}
	return writer.topics, writer.activate(ctx, run.clusters)
}

func warpUnmatchedIDs(rows []logstore.WarpTopicUnmatched) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.LogID)
	}
	return ids
}

// Topics are seeded once per embedding model, and admission holds that within
// one process only: another instance sharing the job store can queue a run
// beside this one's. The partition runs them one after another, so the second
// finds the first one's topics and leaves them, and what was filed under them
// stays filed.
func TestWarpTopicsJobQueuedBehindASeedLeavesItsTopics(t *testing.T) {
	service, start := topicTestService(t, newFakeWarpVectorStore(), nil, topicTestNaming)
	store := service.topicStore()
	ctx := context.Background()
	// Both are built before either runs, as two instances admitting at once
	// would build them.
	first, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)
	second, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "first", Metadata: first}, func(string) error { return nil })
	require.NoError(t, err)
	seeded, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{})
	require.NoError(t, err)
	require.NotEmpty(t, seeded)
	filed, err := store.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{}, 100)
	require.NoError(t, err)
	require.NotEmpty(t, filed)

	finalJSON, err := service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "second", Metadata: second}, func(string) error { return nil })
	require.NoError(t, err, "a run with nothing to do has not failed")

	after, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{})
	require.NoError(t, err)
	require.Equal(t, seeded, after, "nothing retired and nothing written")
	refiled, err := store.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{}, 100)
	require.NoError(t, err)
	require.Equal(t, filed, refiled)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Contains(t, final.Message, "already built")
}

// Once upkeep has retired every topic, the model is back to having none and
// is seeded again. The retired topics keep their rows, so a request filed
// under one keeps its row too, but the list never shows a stale topic beside
// a fresh one or counts a request filed under one.
func TestWarpTopicsJobReseedsOnceEveryTopicIsRetired(t *testing.T) {
	// The model is unreachable this run: clusters still get written, with
	// fallback names, and the failure is counted rather than fatal.
	unreachable := func(context.Context, *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "no keys found that support model: openai/gpt-4o"}}
	}
	service, start := topicTestService(t, newFakeWarpVectorStore(), nil, unreachable)
	store := service.topicStore()
	ctx := context.Background()
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{{
		ID: "stale", Label: "Old", EmbeddingSignature: topicTestSignature(), Status: logstore.WarpTopicStatusRetired,
		Centroid: logstore.EncodeWarpVector([]float32{0, 0, 1}),
	}}))
	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err, "a retired topic does not make the model seeded")
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, []logstore.WarpTopicAssignment{{LogID: "earlier", TopicID: "stale", Timestamp: start.Add(-time.Hour)}}))

	finalJSON, err := service.RunTopicsJob(ctx, tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)

	topics, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{})
	require.NoError(t, err)
	require.Len(t, topics, 3, "the previous topic keeps its row")
	labels := map[string]bool{}
	for _, topic := range topics {
		if topic.ID == "stale" {
			require.Equal(t, logstore.WarpTopicStatusRetired, topic.Status)
			continue
		}
		require.Equal(t, logstore.WarpTopicStatusActive, topic.Status)
		labels[topic.Label] = true
	}
	require.True(t, labels["refund"] || labels["password"] || len(labels) == 2, "fallback labels come from the questions' words: %v", labels)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Equal(t, 2, final.Failed, "each failed label is counted")
	require.Contains(t, final.LastError, "no keys found")

	listed, total := listAllTopics(t, NewTopicReader(&recordingStore{row: validWarpConfigRow()}, store))
	require.Len(t, listed, 2)
	require.Equal(t, 6, total, "the request filed under the retired topic is not counted")
}

// A run seeds the topics of the configured embedding model and touches no
// other: a topic built with another embedding model neither holds the run
// back nor is retired by it, and keeps its centre. Themes are left out here:
// the run regroups them afterwards (GroupTopics), which retires a theme left
// without topics, and that is tested with the themes.
func TestWarpTopicsJobLeavesOtherModelsTopics(t *testing.T) {
	service, start := topicTestService(t, newFakeWarpVectorStore(), nil, topicTestChat("Refund requests"))
	store := service.topicStore()
	ctx := context.Background()
	centre := logstore.EncodeWarpVector([]float32{0, 0, 1})
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "other-model", Label: "Other", EmbeddingSignature: "another-embedding-space", Centroid: centre},
	}))
	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)

	finalJSON, err := service.RunTopicsJob(ctx, tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.NotEmpty(t, final.Topics, "the run seeded this model's topics")

	rows, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{IDs: []string{"other-model"}})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, logstore.WarpTopicStatusActive, rows[0].Status)
	require.Equal(t, centre, rows[0].Centroid)
}

// A run that cannot finish writing retires what it wrote and leaves the
// previous topics as they were - here a re-seed's, which upkeep had retired -
// so a question asked afterwards is never answered from a few of the new ones.
func TestWarpTopicsJobKeepsPreviousTopicsWhenWriteFails(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := topicTestLogs(start)
	reader := topicTestReader(t, logs)
	store := reader.topics.(logstore.LogStore)
	reader.topics = &failingTopicStore{LogStore: store, err: fmt.Errorf("log store unavailable")}
	service, _ := topicTestService(t, newFakeWarpVectorStore(), reader, topicTestNaming)
	ctx := context.Background()
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{{
		ID: "previous", Label: "Old", EmbeddingSignature: topicTestSignature(), Status: logstore.WarpTopicStatusRetired,
	}}))
	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)

	finalJSON, err := service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorContains(t, err, "log store unavailable")

	active, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Statuses: []string{logstore.WarpTopicStatusActive}})
	require.NoError(t, err)
	require.Empty(t, active, "none of the new topics is left active")
	all, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{})
	require.NoError(t, err)
	require.Len(t, all, 3, "the new topics were written and then retired")
	for _, topic := range all {
		if topic.ID == "previous" {
			require.Equal(t, "Old", topic.Label, "the previous topic is as it was")
		}
	}

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Zero(t, final.Indexed)
	require.Empty(t, final.Topics)
	require.Contains(t, final.Message, "previous topics were kept")
}

// A run files its requests a batch at a time as it reads, replacing whatever
// row each had: on a re-seed, a row under a topic upkeep retired, or one under
// an older embedding model's. Those rows count for nothing, so a failed run
// does not keep them to put back - keeping them made a run's memory grow with
// its window. It retires the topics it wrote, which takes what it filed under
// them out of every count, and deletes what it filed under no topic. What
// list_topics answers is unchanged, and so are the rows it did not reach.
func TestWarpTopicsJobFailingPartwayLeavesTheAnswerAsItWas(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := topicTestLogs(start)
	reader := topicTestReader(t, logs)
	store := reader.topics.(logstore.LogStore)
	service, _ := topicTestService(t, newFakeWarpVectorStore(), reader, topicTestNaming)
	ctx := context.Background()
	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)
	run := func(id string) (string, error) {
		return service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: id, Metadata: metaJSON}, func(string) error { return nil })
	}
	assignments := func() map[string]string {
		rows, err := store.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{}, 100)
		require.NoError(t, err)
		byLog := make(map[string]string, len(rows))
		for _, row := range rows {
			byLog[row.LogID] = row.TopicID
		}
		return byLog
	}
	_, err = run("job-1")
	require.NoError(t, err)
	retireActiveTopics(t, ctx, store)
	before := assignments()
	require.Len(t, before, 6)
	topicReader := NewTopicReader(&recordingStore{row: validWarpConfigRow()}, store)
	listBefore, totalBefore := listAllTopics(t, topicReader)

	// Six requests in batches of two: the first lands, the second fails - its
	// write may have partly landed - and the third is never reached.
	limits := defaultTopicLimits()
	limits.write = 2
	service.topicLimits = limits
	reader.topics = &failingTopicStore{LogStore: store, err: fmt.Errorf("log store unavailable"), failOn: 2}
	finalJSON, err := run("job-2")
	require.ErrorContains(t, err, "log store unavailable")
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Contains(t, final.Message, "previous topics were kept")

	written, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Statuses: []string{logstore.WarpTopicStatusActive, logstore.WarpTopicStatusSeeding}})
	require.NoError(t, err)
	require.Empty(t, written, "what the failed run wrote is retired")
	after := assignments()
	untouched := 0
	for logID, topicID := range after {
		if topicID == before[logID] {
			untouched++
		}
		require.NotEmpty(t, topicID, "a row the run filed under no topic is deleted")
	}
	require.GreaterOrEqual(t, untouched, 2, "the batch it never reached keeps its rows")
	listAfter, totalAfter := listAllTopics(t, topicReader)
	require.Equal(t, listBefore, listAfter, "list_topics answers as it did")
	require.Equal(t, totalBefore, totalAfter)
}

// The topics are written as soon as the merge settles them, so a run that
// fails to report its progress after that has written something. It takes it
// back as any other failure does, rather than leaving seeding topics behind.
func TestWarpTopicsJobFailingAProgressReportTakesBackWhatItWrote(t *testing.T) {
	for _, stage := range []string{"labelling", "Activating"} {
		t.Run(stage, func(t *testing.T) {
			service, start := topicTestService(t, newFakeWarpVectorStore(), nil, topicTestNaming)
			store := service.topicStore()
			ctx := context.Background()
			metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
			require.NoError(t, err)
			_, err = service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(snapshot string) error {
				if strings.Contains(snapshot, stage) {
					return errors.New("job store unavailable")
				}
				return nil
			})
			require.ErrorContains(t, err, "job store unavailable")
			left, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Statuses: []string{logstore.WarpTopicStatusActive, logstore.WarpTopicStatusSeeding}})
			require.NoError(t, err)
			require.Empty(t, left, "what the run wrote is retired")
		})
	}
}

// retireActiveTopics retires every active topic, as upkeep does once each
// has gone quiet, so the model can be seeded again.
func retireActiveTopics(t *testing.T, ctx context.Context, store logstore.WarpTopicStore) {
	t.Helper()
	active, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Kinds: []string{logstore.WarpTopicKindTopic}, Statuses: []string{logstore.WarpTopicStatusActive}})
	require.NoError(t, err)
	require.NoError(t, retireTopics(ctx, store, active))
}

// failingTopicStore is a log store whose assignment writes fail: every one,
// or with failOn set, only that one (counted from 1).
type failingTopicStore struct {
	logstore.LogStore
	err    error
	failOn int
	calls  int
}

func (f *failingTopicStore) UpsertWarpTopicAssignments(ctx context.Context, assignments []logstore.WarpTopicAssignment) error {
	f.calls++
	if f.failOn == 0 || f.calls == f.failOn {
		return f.err
	}
	return f.LogStore.UpsertWarpTopicAssignments(ctx, assignments)
}

// A run that cannot write its assignments retires the topics it wrote. When
// that cleanup fails too, the run says so: the topics it wrote are still
// active, and an operator reading only the write error would not know.
func TestWarpTopicsJobReportsAFailedCleanup(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	reader := topicTestReader(t, topicTestLogs(start))
	store := reader.topics.(logstore.LogStore)
	service, _ := topicTestService(t, newFakeWarpVectorStore(), reader, topicTestNaming)
	reader.topics = &retireFailingTopicStore{failingTopicStore: failingTopicStore{LogStore: store, err: fmt.Errorf("assignments unavailable")}, err: fmt.Errorf("topics unavailable")}
	ctx := context.Background()
	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)
	finalJSON, err := service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorContains(t, err, "assignments unavailable")
	require.ErrorContains(t, err, "topics unavailable", "the failed cleanup is part of the error")
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Contains(t, final.LastError, "assignments unavailable")
	require.Contains(t, final.LastError, "topics unavailable")

	// The topics the failed run could not retire were never made active, so
	// they do not read as a finished seed: a retry is admitted, and upkeep
	// deletes them once they have sat long enough.
	left, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Kinds: []string{logstore.WarpTopicKindTopic}})
	require.NoError(t, err)
	require.NotEmpty(t, left)
	for _, topic := range left {
		require.Equal(t, logstore.WarpTopicStatusSeeding, topic.Status)
	}
	_, err = service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err, "a failed seed does not lock the model out of seeding again")
	pruned, err := pruneRetiredTopics(ctx, store, time.Now().UTC().Add(topicsRetireAfter+time.Hour), topicsRetireAfter)
	require.NoError(t, err)
	require.Equal(t, len(left), pruned)

	// A cleanup that succeeds leaves the error as it was.
	reader.topics = &failingTopicStore{LogStore: store, err: fmt.Errorf("assignments unavailable")}
	_, err = service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "job-2", Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorContains(t, err, "assignments unavailable")
	require.NotContains(t, err.Error(), "retire")
}

// cancellingThemeStore cancels the run when grouping reads the themes: a
// cancel that arrives after the new topics are active.
type cancellingThemeStore struct {
	logstore.LogStore
	cancel context.CancelFunc
}

func (s *cancellingThemeStore) ListWarpTopics(ctx context.Context, filter logstore.WarpTopicFilter) ([]logstore.WarpTopic, error) {
	if slices.Contains(filter.Kinds, logstore.WarpTopicKindTheme) {
		s.cancel()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return s.LogStore.ListWarpTopics(ctx, filter)
}

// A run cancelled once its topics are active still groups them, then stops as
// cancelled. Stopped before grouping, the topics stayed ungrouped for good: a
// second seed run is refused, and nothing else regroups an unchanged set.
// Here the stale theme holds no topic, so finished grouping retires it.
func TestWarpTopicsJobGroupsBeforeStoppingWhenCancelled(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	reader := topicTestReader(t, topicTestLogs(start))
	store := reader.topics.(logstore.LogStore)
	service, _ := topicTestService(t, newFakeWarpVectorStore(), reader, topicTestNaming)
	require.NoError(t, store.UpsertWarpTopics(context.Background(), []logstore.WarpTopic{
		{ID: "theme-stale", Kind: logstore.WarpTopicKindTheme, Label: "Stale", EmbeddingSignature: topicTestSignature()},
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader.topics = &cancellingThemeStore{LogStore: store, cancel: cancel}
	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)

	finalJSON, err := service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Contains(t, final.Message, "grouped into themes first")
	require.Empty(t, final.LastError)

	themes, err := store.ListWarpTopics(context.Background(), logstore.WarpTopicFilter{IDs: []string{"theme-stale"}})
	require.NoError(t, err)
	require.Len(t, themes, 1)
	require.Equal(t, logstore.WarpTopicStatusRetired, themes[0].Status, "grouping ran to the end")
}

// A run that fails after writing its topics retires them, and the indexer in
// this process is told: it may have read them while they were active, and
// would otherwise keep filing requests under retired topics until its copy
// expires.
func TestWarpTopicsJobInvalidatesTheIndexerWhenWritingFails(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	reader := topicTestReader(t, topicTestLogs(start))
	store := reader.topics.(logstore.LogStore)
	service, _ := topicTestService(t, newFakeWarpVectorStore(), reader, topicTestNaming)
	require.NotNil(t, service.indexer)
	ctx := context.Background()
	_, err := service.indexer.centres.get(ctx, topicTestSignature())
	require.NoError(t, err)
	loaded := func() bool {
		service.indexer.centres.mu.Lock()
		defer service.indexer.centres.mu.Unlock()
		return !service.indexer.centres.loadedAt.IsZero()
	}
	require.True(t, loaded())

	reader.topics = &failingTopicStore{LogStore: store, err: fmt.Errorf("assignments unavailable")}
	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorContains(t, err, "assignments unavailable")
	require.False(t, loaded(), "the next request reads the topics again")
}

// retireFailingTopicStore fails assignment writes as failingTopicStore does,
// and every topic write after the first: the run's own topics land, and
// retiring them again does not.
type retireFailingTopicStore struct {
	failingTopicStore
	err    error
	writes int
}

func (f *retireFailingTopicStore) UpsertWarpTopics(ctx context.Context, topics []logstore.WarpTopic) error {
	f.writes++
	if f.writes > 1 {
		return f.err
	}
	return f.LogStore.UpsertWarpTopics(ctx, topics)
}

// A batch whose requests are all gone from the log store has nothing to file.
// It must not read assignments with no ids to read them by - an empty filter
// is every row - nor write an empty set.
func TestWriteTopicsSkipsABatchWithNothingToFile(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := topicTestLogs(start)
	inner := topicTestReader(t, logs).topics.(logstore.LogStore)
	ctx := context.Background()
	vectors := topicTestVectors()
	require.NoError(t, inner.UpsertWarpTopicAssignments(ctx, []logstore.WarpTopicAssignment{{LogID: "log-5", TopicID: "older", Timestamp: logs["log-5"].Timestamp}}))
	store := &recordingTopicStore{LogStore: inner}

	cluster := topicCluster{sum: make([]float32, 3), label: "Refund requests", sampleIDs: []string{"log-0"}}
	for _, id := range []string{"gone-1", "gone-2", "log-0"} {
		vector := vectors["log-0"]
		for d, value := range vector {
			cluster.sum[d] += value
		}
		cluster.size++
		cluster.members = append(cluster.members, topicMember{logID: id, similarity: 0.9})
	}
	meta := BackfillJobMeta{ConfigSignature: "sig", StartTime: start, EndTime: start.Add(24 * time.Hour)}
	// Batches of two, in the order placed: gone-1 and gone-2, then log-0.
	_, err := fileTestTopics(ctx, store, &meta, &topicRun{floor: 0.5}, []topicCluster{cluster}, 2)
	require.NoError(t, err)
	require.Equal(t, 2, meta.Skipped)
	require.Empty(t, store.assignmentReads, "the rows a request had are replaced, not read to put back")
	for _, rows := range store.assignmentWrites {
		require.NotEmpty(t, rows, "no empty write")
	}
	require.Len(t, store.assignmentWrites, 1, "only the batch with a request to file writes")
}

// recordingTopicStore records the assignment reads and writes made through it.
type recordingTopicStore struct {
	logstore.LogStore
	assignmentReads  []logstore.WarpTopicAssignmentFilter
	assignmentWrites [][]logstore.WarpTopicAssignment
}

func (r *recordingTopicStore) ListWarpTopicAssignments(ctx context.Context, filter logstore.WarpTopicAssignmentFilter, limit int) ([]logstore.WarpTopicAssignment, error) {
	r.assignmentReads = append(r.assignmentReads, filter)
	return r.LogStore.ListWarpTopicAssignments(ctx, filter, limit)
}

func (r *recordingTopicStore) UpsertWarpTopicAssignments(ctx context.Context, rows []logstore.WarpTopicAssignment) error {
	r.assignmentWrites = append(r.assignmentWrites, rows)
	return r.LogStore.UpsertWarpTopicAssignments(ctx, rows)
}

// Topics live in the log store, so Warp has them only through a reader that
// can reach it. Without one there is no job to start and no tool to answer,
// and a reader bound later brings them with it.
func TestWarpTopicsNeedALogStoreThatKeepsThem(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := topicTestLogs(start)
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithLogReader(&semanticLogReader{logs: logs}),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(backfillEmbeddingExecutor), WithChatFunc(topicTestNaming),
	)
	defer service.Shutdown()
	require.Nil(t, service.topicLister())
	_, err := service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.ErrorIs(t, err, ErrUnavailable)

	service.SetLogReader(topicTestReader(t, logs))
	require.NotNil(t, service.topicLister())
	_, err = service.BuildTopicsJobMeta(context.Background(), start, start.Add(24*time.Hour))
	require.NoError(t, err)

	service.SetLogReader(nil)
	require.Nil(t, service.topicLister())
}

// list_topics is the tool that answers "what do people ask about": topics by
// size, with a share and links a reader can open, counted from the rows.
// topicToolDeps are tool deps for an administrator asking over the given
// store, with the clock pinned so a relative window lands on the test rows.
func topicToolDeps(store logstore.LogStore, now time.Time) *ToolDeps {
	return &ToolDeps{
		logManager: &fakeLogReader{}, clock: now,
		topics: NewTopicReader(&recordingStore{row: validWarpConfigRow()}, store),
		scope:  Scope{HasIdentity: true, UserID: "admin", Unrestricted: true},
	}
}

func TestWarpListTopicsToolReturnsSortedTopics(t *testing.T) {
	store := topicTestLogStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "t-small", Label: "Password resets", Cohesion: 0.5, ExampleLogIDsJSON: `["log-1","log-2"]`, EmbeddingSignature: topicTestSignature(), CreatedAt: now.Add(-30 * 24 * time.Hour)},
		{ID: "t-big", Label: "Refund requests", Cohesion: 0.875, ExampleLogIDsJSON: `["log-1","log-2"]`, EmbeddingSignature: topicTestSignature(), CreatedAt: now.Add(-2 * 24 * time.Hour)},
	}))
	var rows []logstore.WarpTopicAssignment
	for index := range 12 {
		topic := "t-big"
		if index < 3 {
			topic = "t-small"
		}
		row := logstore.WarpTopicAssignment{LogID: fmt.Sprintf("log-%d", index), TopicID: topic, Timestamp: now.Add(-time.Duration(index) * time.Minute), SessionID: fmt.Sprintf("s-%d", index/2), Cost: 0.5, TotalTokens: 100, Latency: 200}
		if index == 4 {
			row.Status = "error"
		}
		rows = append(rows, row)
	}
	// Two that matched nothing, and one from before the week the window covers.
	rows = append(rows,
		logstore.WarpTopicAssignment{LogID: "none-1", Timestamp: now.Add(-time.Hour)},
		logstore.WarpTopicAssignment{LogID: "none-2", Timestamp: now.Add(-2 * time.Hour)},
		logstore.WarpTopicAssignment{LogID: "old", TopicID: "t-big", Timestamp: now.Add(-8 * 24 * time.Hour)},
	)
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, rows))

	result, err := runTool(t, "list_topics", topicToolDeps(store, now), map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	topics := out["topics"].([]Topic)
	require.Len(t, topics, 2)
	big := topics[0]
	require.Equal(t, "Refund requests", big.Label)
	require.EqualValues(t, 9, big.Requests)
	require.InDelta(t, 9.0/14, big.Share, 1e-9, "a share of every request in the window, the unmatched ones included")
	require.EqualValues(t, 5, big.Sessions)
	require.EqualValues(t, 1, big.Errors)
	require.InDelta(t, 4.5, big.Cost, 1e-9)
	require.EqualValues(t, 900, big.Tokens)
	require.InDelta(t, 200, big.AvgLatency, 1e-9)
	require.InDelta(t, 0.875, big.Cohesion, 1e-9)
	require.Equal(t, now.Add(-2*24*time.Hour), big.FirstSeen)
	require.True(t, big.New, "made inside the window")
	require.False(t, topics[1].New)
	require.Equal(t, []string{logDetailLink("log-1"), logDetailLink("log-2")}, big.SampleLinks)
	require.True(t, big.Trend.HasPreviousPeriod, "one request the week before")
	require.EqualValues(t, 1, big.Trend.PreviousRequests)
	require.InDelta(t, 800, big.Trend.RequestsTrend, 1e-9)
	require.Nil(t, big.Trend.CostTrend, "the week before cost nothing, so there is no percentage")
	require.False(t, topics[1].Trend.HasPreviousPeriod)
	require.Zero(t, topics[1].Trend.RequestsTrend)
	require.Equal(t, int64(14), out["total_requests"])
	require.Equal(t, int64(2), out["unmatched_requests"])
	require.Equal(t, 2, out["total_topics"])
	require.Equal(t, "deployment", out["scope"])
	require.Equal(t, map[string]string{"start": now.Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano), "end": now.Format(time.RFC3339Nano)}, out["window"], "the last 7 days unless told otherwise")
	require.Equal(t, map[string]string{"start": now.Add(-14 * 24 * time.Hour).Format(time.RFC3339Nano), "end": now.Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)}, out["previous_window"])
	_, hasHint := out["hint"]
	require.False(t, hasHint)

	// A topic's links each open one conversation. They are named for what
	// they are, and the result offers nothing that reads as "the requests
	// behind this topic", because the Logs page cannot show that set: a
	// topic's name linked to one request says nine and opens one.
	encoded, err := sonic.MarshalString(out)
	require.NoError(t, err)
	require.Contains(t, encoded, `"example_links":["`+logDetailLink("log-1")+`","`+logDetailLink("log-2")+`"]`)
	require.Contains(t, encoded, `"new_in_window":true`)
	require.Contains(t, encoded, `"cost_trend":null`)
	require.NotContains(t, encoded, "sample_links")
	require.NotContains(t, encoded, "series", "no interval, no series")
	_, hasLogsLink := out["logs_link"]
	require.False(t, hasLogsLink, "the unfiltered Logs page is not what a topic count covers")
	require.Contains(t, out["links"], "one example request")
	require.Contains(t, out["counts"], "count requests")
	require.Contains(t, out["trend"], "previous_window")
	require.Contains(t, out["requests"], "topic_ids")
	require.NotContains(t, out["cannot_answer"], "every request in a topic")
	require.NotContains(t, out["cannot_answer"], "time range")

	// Each row links to its own requests, over the window the count ran on,
	// so a name that says nine opens nine.
	require.Contains(t, big.Link, "topic_ids=t-big")
	require.Contains(t, big.Link, "start_time=")
	require.Equal(t, big.Link, logsViewLink(&logstore.SearchFilters{StartTime: new(now.Add(-7 * 24 * time.Hour)), EndTime: new(now), TopicIDs: []string{"t-big"}}))
	require.Contains(t, encoded, `"link":"`+logsViewLink(&logstore.SearchFilters{StartTime: new(now.Add(-7 * 24 * time.Hour)), EndTime: new(now), TopicIDs: []string{"t-small"}})+`"`)
	require.Contains(t, out["links"], "link the name to it")
}

// The window and the filters are the ones every other tool takes, over the
// columns an assignment carries; a filter it does not carry is refused by
// name rather than ignored. With an interval each topic also comes bucket by
// bucket.
func TestWarpListTopicsToolTakesAWindowFiltersAndAnInterval(t *testing.T) {
	store := topicTestLogStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "refunds", Label: "Refund requests", EmbeddingSignature: topicTestSignature(), CreatedAt: now.Add(-60 * 24 * time.Hour)},
		{ID: "passwords", Label: "Password resets", EmbeddingSignature: topicTestSignature(), CreatedAt: now.Add(-60 * 24 * time.Hour)},
	}))
	var rows []logstore.WarpTopicAssignment
	for day := range 4 {
		for index := range 2 {
			at := now.Add(-time.Duration(day)*24*time.Hour - time.Duration(index)*time.Hour)
			rows = append(rows,
				logstore.WarpTopicAssignment{LogID: fmt.Sprintf("r-%d-%d", day, index), TopicID: "refunds", Timestamp: at, TeamID: "team-a", Model: "gpt-4o", Cost: 1},
				logstore.WarpTopicAssignment{LogID: fmt.Sprintf("p-%d-%d", day, index), TopicID: "passwords", Timestamp: at, TeamID: "team-b", Model: "gpt-4o", SessionID: "sess-1"},
			)
		}
	}
	rows = append(rows, logstore.WarpTopicAssignment{LogID: "r-claude", TopicID: "refunds", Timestamp: now.Add(-time.Hour), TeamID: "team-a", Model: "claude"})
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, rows))
	deps := topicToolDeps(store, now)

	// A window of the last two days, by team.
	result, err := runTool(t, "list_topics", deps, map[string]any{"filters": map[string]any{"start_time": "-2d", "team_ids": []any{"team-a"}}})
	require.NoError(t, err)
	out := result.(map[string]any)
	topics := out["topics"].([]Topic)
	require.Len(t, topics, 1, "the other team's topic has no request the filter reaches")
	require.Equal(t, "Refund requests", topics[0].Label)
	require.EqualValues(t, 6, topics[0].Requests, "days 0 and 1, the first of day 2 exactly at the window's edge, and the claude one")
	require.EqualValues(t, 3, topics[0].Trend.PreviousRequests, "the two days before hold the rest of day 2 and day 3")
	require.InDelta(t, 100, topics[0].Trend.RequestsTrend, 1e-9)
	require.Equal(t, "named", out["scope"])
	require.Equal(t, int64(6), out["total_requests"])
	require.Contains(t, topics[0].Link, "team_ids=team-a")
	require.Contains(t, topics[0].Link, "topic_ids=refunds")

	// A model and a session narrow the same way.
	result, err = runTool(t, "list_topics", deps, map[string]any{"filters": map[string]any{"models": []any{"claude"}}})
	require.NoError(t, err)
	require.EqualValues(t, 1, result.(map[string]any)["topics"].([]Topic)[0].Requests)
	result, err = runTool(t, "list_topics", deps, map[string]any{"filters": map[string]any{"session_id": "sess-1"}})
	require.NoError(t, err)
	require.Equal(t, "Password resets", result.(map[string]any)["topics"].([]Topic)[0].Label)

	// Day by day, oldest first, with the window's day boundaries.
	result, err = runTool(t, "list_topics", deps, map[string]any{"filters": map[string]any{"start_time": "-2d", "models": []any{"gpt-4o"}}, "interval": "day"})
	require.NoError(t, err)
	topics = result.(map[string]any)["topics"].([]Topic)
	require.Len(t, topics, 2)
	for _, topic := range topics {
		require.Len(t, topic.Series, 3, "%s: three UTC days touch the window", topic.Label)
		require.Equal(t, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC), topic.Series[0].Bucket)
		require.Equal(t, time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), topic.Series[2].Bucket)
		for index, point := range topic.Series {
			want := 2
			if index == 0 {
				want = 1 // the window starts at noon that day
			}
			require.EqualValues(t, want, point.Requests, "%s on %s", topic.Label, point.Bucket)
		}
	}
	require.Equal(t, "Password resets", topics[0].Label, "equal in requests, so in label order")
	require.InDelta(t, 1, topics[1].Series[0].Cost, 1e-9)
	require.InDelta(t, 2, topics[1].Series[1].Cost, 1e-9)

	// A window too long for its interval, and a filter a topic count cannot take.
	_, err = runTool(t, "list_topics", deps, map[string]any{"filters": map[string]any{"start_time": "-30d"}, "interval": "hour"})
	require.ErrorContains(t, err, "more than 200 buckets")
	// Buckets are aligned to the epoch and both end buckets count: exactly 200
	// hours that start mid-hour touch 201 of them, and 200 hours that start on
	// the hour and stop short of the last touch 200.
	_, err = runTool(t, "list_topics", deps, map[string]any{"filters": map[string]any{"start_time": "2026-08-20T00:30:00Z", "end_time": "2026-08-28T08:30:00Z"}, "interval": "hour"})
	require.ErrorContains(t, err, "more than 200 buckets")
	_, err = runTool(t, "list_topics", deps, map[string]any{"filters": map[string]any{"start_time": "2026-08-20T00:00:00Z", "end_time": "2026-08-28T07:59:59Z"}, "interval": "hour"})
	require.NoError(t, err)
	_, err = runTool(t, "list_topics", deps, map[string]any{"filters": map[string]any{"stop_reasons": []any{"length"}, "min_cost": 1}})
	require.ErrorContains(t, err, "unknown filter fields: min_cost, stop_reasons")
	require.ErrorContains(t, err, "Supported fields are: apps, business_unit_ids, customer_ids, end_time, models, providers, scope, session_id, start_time, status, team_ids, user_ids, virtual_key_ids")
	_, err = runTool(t, "list_topics", deps, map[string]any{"filters": map[string]any{"apps": []any{"Warp"}}})
	require.ErrorContains(t, err, `apps must not name "Warp"`)
}

// The filter the tool advertises is the shared one cut down to what a topic
// count can honour, field descriptions included, so the model reads the same
// words for start_time here as everywhere else.
func TestTopicFilterSchemaIsTheSharedFilterCutDown(t *testing.T) {
	var shared, topic struct {
		Description string         `json:"description"`
		Properties  map[string]any `json:"properties"`
	}
	require.NoError(t, sonic.UnmarshalString(FilterSchema, &shared))
	require.NoError(t, sonic.UnmarshalString(TopicFilterSchema, &topic))
	require.ElementsMatch(t, topicFilterFields, slices.Collect(maps.Keys(topic.Properties)))
	for field, property := range topic.Properties {
		require.Equal(t, shared.Properties[field], property, field)
	}
	require.Contains(t, topic.Description, "last 7 days")
	require.NotContains(t, topic.Description, "24 hours")

	tool, ok := toolByName(buildTools(), "list_topics")
	require.True(t, ok)
	require.ElementsMatch(t, []string{"filters", "interval", "group_by", "limit"}, tool.argumentNames())
}

// With group_by theme the answer is the themes, each its topics' counts
// added up with the topics inside, largest theme first, and the topics in no
// theme on their own. A theme links to its requests as a topic does, and a
// topic row says which theme it is in either way.
func TestWarpListTopicsToolGroupsByTheme(t *testing.T) {
	store := topicTestLogStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "money", Label: "Billing", Kind: logstore.WarpTopicKindTheme, EmbeddingSignature: topicTestSignature()},
		{ID: "access", Label: "Account Access", Kind: logstore.WarpTopicKindTheme, EmbeddingSignature: topicTestSignature()},
		{ID: "gone", Label: "Retired theme", Kind: logstore.WarpTopicKindTheme, EmbeddingSignature: topicTestSignature(), Status: logstore.WarpTopicStatusRetired},
		{ID: "refunds", Label: "Refund requests", ParentID: "money", EmbeddingSignature: topicTestSignature()},
		{ID: "invoices", Label: "Invoice questions", ParentID: "money", EmbeddingSignature: topicTestSignature()},
		{ID: "old-invoices", Label: "Invoices", ParentID: "money", EmbeddingSignature: topicTestSignature(), Status: logstore.WarpTopicStatusMerged, MergedInto: "invoices"},
		{ID: "passwords", Label: "Password resets", ParentID: "access", EmbeddingSignature: topicTestSignature()},
		{ID: "weather", Label: "Weather", EmbeddingSignature: topicTestSignature()},
		{ID: "stale", Label: "Stale", ParentID: "gone", EmbeddingSignature: topicTestSignature()},
	}))
	var rows []logstore.WarpTopicAssignment
	file := func(topic string, count int, at time.Time, cost float64) {
		for index := range count {
			rows = append(rows, logstore.WarpTopicAssignment{LogID: fmt.Sprintf("%s-%d-%d", topic, at.Day(), index), TopicID: topic, Timestamp: at, Cost: cost, SessionID: topic})
		}
	}
	file("refunds", 6, now.Add(-time.Hour), 1)
	file("invoices", 3, now.Add(-time.Hour), 1)
	file("old-invoices", 2, now.Add(-26*time.Hour), 1)
	file("passwords", 6, now.Add(-time.Hour), 0)
	file("weather", 4, now.Add(-time.Hour), 0)
	file("stale", 1, now.Add(-time.Hour), 0)
	file("", 1, now.Add(-time.Hour), 0)
	file("refunds", 2, now.Add(-10*24*time.Hour), 1) // the week before
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, rows))

	result, err := runTool(t, "list_topics", topicToolDeps(store, now), map[string]any{"group_by": "theme", "interval": "day"})
	require.NoError(t, err)
	out := result.(map[string]any)
	_, flat := out["topics"]
	require.False(t, flat, "grouped, the topics come inside their themes")
	themes := out["themes"].([]TopicTheme)
	require.Len(t, themes, 2)
	require.Equal(t, 2, out["total_themes"])
	require.Equal(t, 2, out["returned"])
	billing := themes[0]
	require.Equal(t, "Billing", billing.Label)
	require.EqualValues(t, 11, billing.Requests, "6 refunds, 3 invoices, 2 under the topic merged into invoices")
	require.EqualValues(t, 3, billing.Sessions)
	require.InDelta(t, 11, billing.Cost, 1e-9)
	require.InDelta(t, 11.0/23, billing.Share, 1e-9, "of every request in the window")
	require.True(t, billing.Trend.HasPreviousPeriod)
	require.EqualValues(t, 2, billing.Trend.PreviousRequests)
	require.InDelta(t, 450, billing.Trend.RequestsTrend, 1e-9)
	require.Contains(t, billing.Link, "topic_ids=money")
	require.Len(t, billing.Topics, 2)
	require.Equal(t, "Refund requests", billing.Topics[0].Label)
	require.Equal(t, "Billing", billing.Topics[0].Theme)
	require.Equal(t, "money", billing.Topics[0].ThemeID)
	require.Contains(t, billing.Topics[0].Link, "topic_ids=refunds")
	require.EqualValues(t, 5, billing.Topics[1].Requests, "3 of its own and 2 merged in")
	var inSeries int64
	for _, point := range billing.Series {
		inSeries += point.Requests
	}
	require.EqualValues(t, 11, inSeries, "the theme's series is its topics' added up")
	require.Len(t, billing.Series, 2, "yesterday and today")
	require.Equal(t, "Account Access", themes[1].Label)
	require.EqualValues(t, 6, themes[1].Requests)
	require.False(t, themes[1].Trend.HasPreviousPeriod)

	ungrouped := out["ungrouped_topics"].([]Topic)
	require.Len(t, ungrouped, 2, "no theme, and a retired theme is no theme")
	require.Equal(t, "Weather", ungrouped[0].Label)
	require.Empty(t, ungrouped[0].Theme)
	require.Equal(t, "Stale", ungrouped[1].Label)
	require.Empty(t, ungrouped[1].ThemeID)
	require.Contains(t, ungrouped[0].Link, "topic_ids=weather")
	require.Equal(t, int64(23), out["total_requests"])
	require.Contains(t, out["links"], "theme's link")
	require.NotContains(t, out["cannot_answer"], "themes")
	_, hasHint := out["hint"]
	require.False(t, hasHint)

	// limit counts themes.
	result, err = runTool(t, "list_topics", topicToolDeps(store, now), map[string]any{"group_by": "theme", "limit": float64(1)})
	require.NoError(t, err)
	out = result.(map[string]any)
	require.Len(t, out["themes"], 1)
	require.Equal(t, 2, out["total_themes"])

	// Listed flat, each topic still says which theme it is in.
	result, err = runTool(t, "list_topics", topicToolDeps(store, now), map[string]any{})
	require.NoError(t, err)
	topics := result.(map[string]any)["topics"].([]Topic)
	require.Equal(t, "Password resets", topics[0].Label)
	require.Equal(t, "Account Access", topics[0].Theme)
	encoded, err := sonic.MarshalString(topics)
	require.NoError(t, err)
	require.Contains(t, encoded, `"theme":"Billing"`)

	// Nothing grouped yet: the topics come back on their own, with a word.
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "money", Label: "Billing", Kind: logstore.WarpTopicKindTheme, EmbeddingSignature: topicTestSignature(), Status: logstore.WarpTopicStatusRetired},
		{ID: "access", Label: "Account Access", Kind: logstore.WarpTopicKindTheme, EmbeddingSignature: topicTestSignature(), Status: logstore.WarpTopicStatusRetired},
	}))
	result, err = runTool(t, "list_topics", topicToolDeps(store, now), map[string]any{"group_by": "theme"})
	require.NoError(t, err)
	out = result.(map[string]any)
	require.Empty(t, out["themes"])
	require.Len(t, out["ungrouped_topics"], 5)
	require.Contains(t, out["hint"], "not grouped into themes")
}

func TestWarpListTopicsToolHintsWhenNothingComputed(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	store := topicTestLogStore(t)
	result, err := runTool(t, "list_topics", topicToolDeps(store, now), map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Empty(t, out["topics"])
	require.Contains(t, out["hint"], "No topics have been built yet")

	// Topics that exist but hold none of the window's requests are a
	// different answer: the window is empty, not the deployment.
	require.NoError(t, store.UpsertWarpTopics(context.Background(), []logstore.WarpTopic{{ID: "t-1", Label: "Refund requests", EmbeddingSignature: topicTestSignature()}}))
	require.NoError(t, store.UpsertWarpTopicAssignments(context.Background(), []logstore.WarpTopicAssignment{{LogID: "old", TopicID: "t-1", Timestamp: now.Add(-30 * 24 * time.Hour)}}))
	result, err = runTool(t, "list_topics", topicToolDeps(store, now), map[string]any{})
	require.NoError(t, err)
	out = result.(map[string]any)
	require.Empty(t, out["topics"])
	require.Contains(t, out["hint"], "no request in this window")
	require.Equal(t, int64(0), out["total_requests"])
}

// The list is the active topics of the embedding model Warp is configured
// with, and the count is of the requests filed under those or under none.
// A topic from another model means nothing against today's vectors, and a
// retired one is no longer the answer; neither is listed, and the requests
// filed under them are not counted.
func TestWarpListTopicsReadsOnlyTheCurrentActiveTopics(t *testing.T) {
	store := topicTestLogStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "current", Label: "Refund requests", EmbeddingSignature: topicTestSignature()},
		{ID: "other-model", Label: "Refunds, as another model saw them", EmbeddingSignature: "another model"},
		{ID: "retired", Label: "Old", EmbeddingSignature: topicTestSignature(), Status: logstore.WarpTopicStatusRetired},
		{ID: "theme", Label: "Money", Kind: logstore.WarpTopicKindTheme, EmbeddingSignature: topicTestSignature()},
	}))
	var rows []logstore.WarpTopicAssignment
	file := func(topic string, count int) {
		for index := range count {
			rows = append(rows, logstore.WarpTopicAssignment{LogID: fmt.Sprintf("%s-%d", topic, index), TopicID: topic, Timestamp: now})
		}
	}
	file("current", 4)
	file("other-model", 2)
	file("retired", 3)
	file("", 1)
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, rows))

	topics, total := listAllTopics(t, NewTopicReader(&recordingStore{row: validWarpConfigRow()}, store))
	require.Len(t, topics, 1)
	require.Equal(t, "Refund requests", topics[0].Label)
	require.Equal(t, 4, int(topics[0].Requests))
	require.Equal(t, 5, total, "the four in the topic and the one in none")
	require.InDelta(t, 0.8, topics[0].Share, 1e-9)
}

// The embedded and Pinecone stores keep each namespace's dimension in memory,
// so after a restart they cannot page a namespace until it has been declared
// again. Indexing declares the log namespace as a side effect; a process that
// has indexed nothing since it started has not, and topics must still run
// there.
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

	topics, total := listAllTopics(t, NewTopicReader(&recordingStore{row: validWarpConfigRow()}, service.topicStore()))
	require.Len(t, topics, 2)
	require.Equal(t, 6, total)
}

// A store that applies the caller's scope to its own reads - the enterprise
// log store resolves it per call, from who is asking - answers a restricted
// caller with no queryscope on the context. Only that store does: a plain one
// is still refused, as above.
func TestWarpListTopicsServesARestrictedCallerThroughAStoreThatScopesItsReads(t *testing.T) {
	inner := topicTestLogStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	require.NoError(t, inner.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "shared", Label: "Refund requests", ExampleLogIDsJSON: `["theirs-1","ours-1"]`, EmbeddingSignature: topicTestSignature()},
		{ID: "theirs", Label: "Password resets", ExampleLogIDsJSON: `["theirs-2"]`, EmbeddingSignature: topicTestSignature()},
	}))
	require.NoError(t, inner.UpsertWarpTopicAssignments(ctx, []logstore.WarpTopicAssignment{
		{LogID: "ours-1", TopicID: "shared", Timestamp: now.Add(-time.Hour), TeamID: "team-a"},
		{LogID: "theirs-1", TopicID: "shared", Timestamp: now.Add(-time.Hour), TeamID: "team-z"},
		{LogID: "theirs-2", TopicID: "theirs", Timestamp: now.Add(-time.Hour), TeamID: "team-z"},
	}))
	deps := topicToolDeps(&perCallScopedTopicStore{LogStore: inner, teams: []string{"team-a"}}, now)
	deps.scope = Scope{HasIdentity: true, UserID: "u-a", Visibility: "their team's traffic"}

	// Naming the other team reaches nothing of theirs.
	result, err := runTool(t, "list_topics", deps, map[string]any{"filters": map[string]any{"team_ids": []any{"team-z"}}})
	require.NoError(t, err)
	require.Empty(t, result.(map[string]any)["topics"])

	result, err = runTool(t, "list_topics", deps, map[string]any{"filters": map[string]any{"scope": "all"}})
	require.NoError(t, err)
	out := result.(map[string]any)
	topics := out["topics"].([]Topic)
	require.Len(t, topics, 1)
	require.Equal(t, "Refund requests", topics[0].Label)
	require.EqualValues(t, 1, topics[0].Requests)
	require.Equal(t, []string{"ours-1"}, topics[0].SampleLogIDs)
	require.Equal(t, int64(1), out["total_requests"])
}

// perCallScopedTopicStore narrows its assignment reads to teams on every call,
// as the enterprise log store narrows them to the caller's principal, and says
// so through CallerScopedTopicStore.
type perCallScopedTopicStore struct {
	logstore.LogStore
	teams []string
}

func (s *perCallScopedTopicStore) ScopesWarpTopicReadsPerCall() bool { return true }

func (s *perCallScopedTopicStore) scoped(ctx context.Context) context.Context {
	return queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("team_id IN ?", s.teams) })
}

func (s *perCallScopedTopicStore) SummarizeWarpTopicAssignments(ctx context.Context, filter logstore.WarpTopicAssignmentFilter) ([]logstore.WarpTopicUsage, error) {
	return s.LogStore.SummarizeWarpTopicAssignments(s.scoped(ctx), filter)
}

func (s *perCallScopedTopicStore) ListWarpTopicAssignments(ctx context.Context, filter logstore.WarpTopicAssignmentFilter, limit int) ([]logstore.WarpTopicAssignment, error) {
	return s.LogStore.ListWarpTopicAssignments(s.scoped(ctx), filter, limit)
}

func (s *perCallScopedTopicStore) BucketWarpTopicAssignments(ctx context.Context, filter logstore.WarpTopicAssignmentFilter, bucketSizeSeconds int64) ([]logstore.WarpTopicBucket, error) {
	return s.LogStore.BucketWarpTopicAssignments(s.scoped(ctx), filter, bucketSizeSeconds)
}

// A restricted caller is answered from the requests they may see. A topic
// none of those fall under is not in the answer at all, not even by name; one
// some do is listed with its name, counted over their requests alone, with
// only the examples they may open. The store applies their scope to every
// count, so the tool neither bounces them to a sample nor widens anything.
func TestWarpListTopicsAnswersARestrictedCallerFromTheirOwnRequests(t *testing.T) {
	store := topicTestLogStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "shared", Label: "Refund requests", ExampleLogIDsJSON: `["theirs-1","ours-1"]`, EmbeddingSignature: topicTestSignature()},
		{ID: "theirs", Label: "Password resets", ExampleLogIDsJSON: `["theirs-2"]`, EmbeddingSignature: topicTestSignature()},
	}))
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, []logstore.WarpTopicAssignment{
		{LogID: "ours-1", TopicID: "shared", Timestamp: now.Add(-time.Hour), TeamID: "team-a"},
		{LogID: "ours-2", TopicID: "shared", Timestamp: now.Add(-time.Hour), TeamID: "team-a"},
		{LogID: "ours-none", TopicID: "", Timestamp: now.Add(-time.Hour), TeamID: "team-a"},
		{LogID: "theirs-1", TopicID: "shared", Timestamp: now.Add(-time.Hour), TeamID: "team-z"},
		{LogID: "theirs-2", TopicID: "theirs", Timestamp: now.Add(-time.Hour), TeamID: "team-z"},
		{LogID: "theirs-3", TopicID: "theirs", Timestamp: now.Add(-time.Hour), TeamID: "team-z"},
		{LogID: "theirs-none", TopicID: "", Timestamp: now.Add(-time.Hour), TeamID: "team-z"},
	}))
	deps := topicToolDeps(store, now)
	deps.scope = Scope{HasIdentity: true, UserID: "u-a", Visibility: "their team's traffic"}
	tool, ok := toolByName(buildTools(), "list_topics")
	require.True(t, ok)

	// The closure shape the enterprise wrapper attaches (dacscope.go logScope).
	scoped := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
		return db.Where("team_id IN ?", []string{"team-a"})
	})
	result, err := tool.execute(scoped, deps, map[string]any{"filters": map[string]any{"scope": "all"}})
	require.NoError(t, err)
	out := result.(map[string]any)
	topics := out["topics"].([]Topic)
	require.Len(t, topics, 1)
	require.Equal(t, "Refund requests", topics[0].Label)
	require.EqualValues(t, 2, topics[0].Requests)
	require.InDelta(t, 2.0/3, topics[0].Share, 1e-9, "of the three requests they may see")
	require.Equal(t, []string{"ours-1"}, topics[0].SampleLogIDs, "the example from another team is not offered")
	require.Equal(t, []string{logDetailLink("ours-1")}, topics[0].SampleLinks)
	require.Equal(t, int64(3), out["total_requests"])
	require.Equal(t, int64(1), out["unmatched_requests"])
	require.Equal(t, "all", out["scope"])
	encoded, err := sonic.MarshalString(out)
	require.NoError(t, err)
	require.NotContains(t, encoded, "Password resets", "a topic none of their requests fall under")
	require.NotContains(t, encoded, "theirs")

	// Without "all", an identified caller's default is their own requests,
	// as with every other tool; nothing is filed under their user id here.
	result, err = tool.execute(scoped, deps, map[string]any{})
	require.NoError(t, err)
	out = result.(map[string]any)
	require.Empty(t, out["topics"])
	require.Equal(t, "self", out["scope"])
	require.Contains(t, out["hint"], "no request in this window and scope")

	// Restricted, but with no scope on the context for the topic tables to
	// apply: the enterprise wrapper scopes log reads per call, and the topic
	// tables are read beneath it. Every count would be the deployment's, and
	// naming another team would read theirs. The tool refuses before it reads.
	for _, args := range []map[string]any{
		{"filters": map[string]any{"team_ids": []any{"team-z"}}},
		{"filters": map[string]any{"scope": "all"}},
		{},
	} {
		result, err = tool.execute(context.Background(), deps, args)
		require.Error(t, err, "%v", args)
		require.Nil(t, result)
		require.Contains(t, err.Error(), "query_logs", "the refusal names what the caller can use instead")
	}

	// A caller nothing restricts gets everything.
	result, err = tool.execute(context.Background(), topicToolDeps(store, now), map[string]any{})
	require.NoError(t, err)
	out = result.(map[string]any)
	require.Len(t, out["topics"], 2)
	require.Equal(t, int64(7), out["total_requests"])
}

// Two clusters that come back with the same name are one topic to anyone
// reading the list, however far apart their centres are. On real traffic the
// same name turned up on clusters between 0.70 and 0.93 alike, while clusters
// that were plainly different sat at 0.94 - so no similarity threshold could
// have joined the first without joining the second. The name can.
func TestWarpTopicsJobJoinsClustersGivenTheSameName(t *testing.T) {
	// Every cluster is given the same name, whatever it holds.
	service, start := topicTestService(t, newFakeWarpVectorStore(), nil, topicTestChat("  refund Requests "))
	store := service.topicStore()
	ctx := context.Background()
	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)
	finalJSON, err := service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)

	topics, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Statuses: []string{logstore.WarpTopicStatusActive}})
	require.NoError(t, err)
	require.Len(t, topics, 1)
	require.Equal(t, "refund Requests", topics[0].Label)
	require.Len(t, topics[0].ExampleLogIDs(), 6, "a topic of six keeps all six, as samples or drawn")
	centroid, err := logstore.DecodeWarpVector(topics[0].Centroid)
	require.NoError(t, err)
	require.InDelta(t, 1, float64(dot(centroid, centroid)), 1e-5, "the centre is recomputed from every member")
	// Requests are filed before topics are named, so the one joined in keeps
	// its own and is merged into the one it joined, as upkeep's merges are.
	merged, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Statuses: []string{logstore.WarpTopicStatusMerged}})
	require.NoError(t, err)
	require.Len(t, merged, 1)
	require.Equal(t, topics[0].ID, merged[0].MergedInto)
	listed, total := listAllTopics(t, NewTopicReader(&recordingStore{row: validWarpConfigRow()}, store))
	require.Len(t, listed, 1)
	require.EqualValues(t, 6, listed[0].Requests, "the joined topic holds both clusters' requests")
	require.Equal(t, 6, total)

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
// name, write, replace, list - with each vector backend Bifrost supports
// behind it.
//
// The fake store cannot stand in for this. Returning vectors was one of three
// things the job needed from a backend: it also has to page past the first
// page and hand properties back in a form the job can read (Redis returns
// every one as text). Each of those passed the unit tests and failed on a
// real store.
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
			t.Cleanup(func() { _ = store.DeleteNamespace(context.Background(), namespace) })
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

			reader := topicTestReader(t, logs)
			service := NewService(nil,
				WithConfigStore(&recordingStore{row: row}), WithLogReader(reader),
				WithVectorStore(store), WithEmbeddingExecutor(backfillEmbeddingExecutor), WithChatFunc(topicTestNaming),
			)
			defer service.Shutdown()
			topicReader := NewTopicReader(&recordingStore{row: row}, reader.topics)

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

				topics, total := listAllTopics(t, topicReader)
				require.Equal(t, 2*perSubject, total)
				require.Len(t, topics, 2, "run %d leaves only its own topics", run)
				for _, topic := range topics {
					require.GreaterOrEqual(t, int(topic.Requests), perSubject-5)
					require.Len(t, topic.SampleLogIDs, topicsLabelSamples)
					require.Greater(t, topic.Cohesion, 0.5)
				}
			}
		})
	}
}

// Topics are seeded once per embedding model. From then on they are kept up
// to date as requests are indexed - filed live, new subjects discovered from
// the pool, the stale retired and the alike merged - and a second run would
// only throw that away. Another model's topics, a retired topic and a theme
// do not count: none of them is a topic this model's requests are filed under.
func TestWarpTopicsSeedRunsOncePerEmbeddingModel(t *testing.T) {
	service, start := topicTestService(t, newFakeWarpVectorStore(), nil, topicTestNaming)
	store := service.topicStore()
	ctx := context.Background()
	centre := logstore.EncodeWarpVector([]float32{0, 0, 1})
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "other-model", Label: "Other", EmbeddingSignature: "another-embedding-space", Centroid: centre},
		{ID: "retired", Label: "Gone", EmbeddingSignature: topicTestSignature(), Status: logstore.WarpTopicStatusRetired, Centroid: centre},
		{ID: "theme", Kind: logstore.WarpTopicKindTheme, Label: "Theme", EmbeddingSignature: topicTestSignature()},
	}))
	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err, "nothing is filed under this model's topics yet")
	_, err = service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "seed", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)

	_, err = service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.ErrorIs(t, err, ErrTopicsAlreadyBuilt)
}

// A seed run clusters the vectors already indexed, so before any exist there
// is nothing for it to do: the settings page asks for a backfill first. Only
// Warp's own vectors count, and a store that cannot answer says so rather
// than reading as empty.
func TestWarpHasIndexedRequests(t *testing.T) {
	service, _ := topicTestService(t, newFakeWarpVectorStore(), nil, topicTestNaming)
	indexed, err := service.HasIndexedRequests(context.Background())
	require.NoError(t, err)
	require.True(t, indexed)

	vectors := newFakeWarpVectorStore()
	require.NoError(t, vectors.Add(context.Background(), schemas.WarpDefaultLogVectorStoreNamespace, "not-warps", []float32{1, 0, 0}, map[string]interface{}{"log_id": "x"}))
	empty := NewService(nil, WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithVectorStore(vectors))
	t.Cleanup(empty.Shutdown)
	indexed, err = empty.HasIndexedRequests(context.Background())
	require.NoError(t, err)
	require.False(t, indexed, "another owner's entry is not an indexed request")

	// A store that does not apply the filter can fill the first page with
	// entries that are not Warp's. A log vector past it still counts.
	for index := range 2 * topicsIndexedProbePage {
		require.NoError(t, vectors.Add(context.Background(), schemas.WarpDefaultLogVectorStoreNamespace, fmt.Sprintf("a-other-%03d", index), []float32{1, 0, 0}, map[string]interface{}{"log_id": "x"}))
	}
	require.NoError(t, vectors.Add(context.Background(), schemas.WarpDefaultLogVectorStoreNamespace, "z-log", []float32{1, 0, 0}, map[string]interface{}{"log_id": "z", "warp_log": true}))
	indexed, err = empty.HasIndexedRequests(context.Background())
	require.NoError(t, err)
	require.True(t, indexed, "a Warp log vector past the first page is found")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = empty.HasIndexedRequests(cancelled)
	require.ErrorIs(t, err, context.Canceled)

	forgetful := newFakeWarpVectorStore()
	forgetful.strict = true
	unknown := NewService(nil, WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithVectorStore(forgetful))
	t.Cleanup(unknown.Shutdown)
	_, err = unknown.HasIndexedRequests(context.Background())
	require.Error(t, err)
}
