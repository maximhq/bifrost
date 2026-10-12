package warp

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
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

// by size, with a share and links a reader can open.

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
	run := &topicRun{floor: 0.5, pooled: []topicVector{{logID: "log-4", vector: vectors["log-4"]}, {logID: "log-5", vector: vectors["log-5"]}}}
	meta := BackfillJobMeta{ConfigSignature: "sig", StartTime: start, EndTime: start.Add(24 * time.Hour)}
	written, err := writeTopics(ctx, store, &meta, run, []topicCluster{cluster}, 0)
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
	written, err := writeTopics(ctx, store, &meta, run, run.clusters, 0)
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

func warpUnmatchedIDs(rows []logstore.WarpTopicUnmatched) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.LogID)
	}
	return ids
}

// A recompute replaces the previous topics: they are retired rather than
// deleted, so a request filed under one keeps its row, but the list never
// shows a stale topic beside a fresh one or counts a request filed under one.
func TestWarpTopicsJobReplacesPreviousTopics(t *testing.T) {
	// The model is unreachable this run: clusters still get written, with
	// fallback names, and the failure is counted rather than fatal.
	unreachable := func(context.Context, *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "no keys found that support model: openai/gpt-4o"}}
	}
	service, start := topicTestService(t, newFakeWarpVectorStore(), nil, unreachable)
	store := service.topicStore()
	ctx := context.Background()
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{{
		ID: "stale", Label: "Old", EmbeddingSignature: topicTestSignature(), Centroid: logstore.EncodeWarpVector([]float32{0, 0, 1}),
	}}))
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, []logstore.WarpTopicAssignment{{LogID: "earlier", TopicID: "stale", Timestamp: start.Add(-time.Hour)}}))

	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)
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

	listed, total, err := NewTopicReader(&recordingStore{row: validWarpConfigRow()}, store).ListTopics(ctx, 15)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Equal(t, 6, total, "the request filed under the retired topic is not counted")
}

// A run replaces the topics list_topics serves - active topics under the
// configured embedding model - and nothing else. A theme, or a topic built
// with another embedding model, is not its to retire. The topic it does
// retire keeps its centre: retiring rewrites the whole row.
func TestWarpTopicsJobRetiresOnlyTheTopicsItReplaces(t *testing.T) {
	service, start := topicTestService(t, newFakeWarpVectorStore(), nil, topicTestChat("Refund requests"))
	store := service.topicStore()
	ctx := context.Background()
	centre := logstore.EncodeWarpVector([]float32{0, 0, 1})
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "replaced", Label: "Old", EmbeddingSignature: topicTestSignature(), Centroid: centre},
		{ID: "theme", Kind: logstore.WarpTopicKindTheme, Label: "Billing", EmbeddingSignature: topicTestSignature(), Centroid: centre},
		{ID: "other-model", Label: "Other", EmbeddingSignature: "another-embedding-space", Centroid: centre},
	}))

	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = service.RunTopicsJob(ctx, tables.TableSidekiqJob{Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)

	rows, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{IDs: []string{"replaced", "theme", "other-model"}})
	require.NoError(t, err)
	status := map[string]string{}
	for _, row := range rows {
		status[row.ID] = row.Status
		require.Equal(t, centre, row.Centroid, "%s keeps its centre", row.ID)
	}
	require.Equal(t, map[string]string{
		"replaced":    logstore.WarpTopicStatusRetired,
		"theme":       logstore.WarpTopicStatusActive,
		"other-model": logstore.WarpTopicStatusActive,
	}, status)
}

// A run that cannot finish writing retires what it wrote and leaves the
// previous topics as they were, so a question asked afterwards is answered
// from a whole set of topics rather than a few of the new ones.
func TestWarpTopicsJobKeepsPreviousTopicsWhenWriteFails(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := topicTestLogs(start)
	reader := topicTestReader(t, logs)
	store := reader.topics.(logstore.LogStore)
	reader.topics = &failingTopicStore{LogStore: store, err: fmt.Errorf("log store unavailable")}
	service, _ := topicTestService(t, newFakeWarpVectorStore(), reader, topicTestNaming)
	ctx := context.Background()
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{{ID: "previous", Label: "Old", EmbeddingSignature: topicTestSignature()}}))

	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)
	finalJSON, err := service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorContains(t, err, "log store unavailable")

	active, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Statuses: []string{logstore.WarpTopicStatusActive}})
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, "previous", active[0].ID)
	all, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{})
	require.NoError(t, err)
	require.Len(t, all, 3, "the new topics were written and then retired")

	var final BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(finalJSON), &final))
	require.Zero(t, final.Indexed)
	require.Empty(t, final.Topics)
	require.Contains(t, final.Message, "previous topics were kept")
}

// A run writes its assignments in batches, and each one replaces the row a
// request already had. A run whose later batch fails puts back the rows its
// earlier batches replaced, so the previous topics keep every request they
// had rather than losing the ones the failed run got to first.
func TestWarpTopicsJobRestoresAssignmentsWhenALaterBatchFails(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := topicTestLogs(start)
	reader := topicTestReader(t, logs)
	store := reader.topics.(logstore.LogStore)
	service, _ := topicTestService(t, newFakeWarpVectorStore(), reader, topicTestNaming)
	ctx := context.Background()
	run := func(id string) (string, error) {
		metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
		require.NoError(t, err)
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
	_, err := run("job-1")
	require.NoError(t, err)
	// One request with no row yet, so the failed run's first batch both
	// replaces rows and adds one.
	require.NoError(t, store.DeleteWarpTopicAssignments(ctx, []string{"log-0"}))
	before := assignments()
	require.Len(t, before, 5)
	reader2 := NewTopicReader(&recordingStore{row: validWarpConfigRow()}, store)
	topicsBefore, totalBefore, err := reader2.ListTopics(ctx, 15)
	require.NoError(t, err)

	// Six requests in batches of four: the first batch lands, the second fails.
	limits := defaultTopicLimits()
	limits.write = 4
	service.topicLimits = limits
	reader.topics = &failingTopicStore{LogStore: store, err: fmt.Errorf("log store unavailable"), failOn: 2}
	_, err = run("job-2")
	require.ErrorContains(t, err, "log store unavailable")

	require.Equal(t, before, assignments(), "every request is back under the topic it had, and the added row is gone")
	topicsAfter, totalAfter, err := reader2.ListTopics(ctx, 15)
	require.NoError(t, err)
	require.Equal(t, totalBefore, totalAfter)
	require.Equal(t, topicsBefore, topicsAfter)
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

	// A cleanup that succeeds leaves the error as it was.
	reader.topics = &failingTopicStore{LogStore: store, err: fmt.Errorf("assignments unavailable")}
	_, err = service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "job-2", Metadata: metaJSON}, func(string) error { return nil })
	require.ErrorContains(t, err, "assignments unavailable")
	require.NotContains(t, err.Error(), "retire")
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
	// Batches of two, sorted: gone-1 and gone-2, then log-0.
	_, err := writeTopics(ctx, store, &meta, &topicRun{floor: 0.5}, []topicCluster{cluster}, 2)
	require.NoError(t, err)
	require.Equal(t, 2, meta.Skipped)
	for _, filter := range store.assignmentReads {
		require.NotEmpty(t, filter.LogIDs, "no read of every assignment")
	}
	for _, rows := range store.assignmentWrites {
		require.NotEmpty(t, rows, "no empty write")
	}
	require.Len(t, store.assignmentReads, 1, "only the batch with a request to file reads")
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
func TestWarpListTopicsToolReturnsSortedTopics(t *testing.T) {
	store := topicTestLogStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "t-small", Label: "Password resets", Cohesion: 0.5, ExampleLogIDsJSON: `["log-1","log-2"]`, EmbeddingSignature: topicTestSignature(), CreatedAt: now},
		{ID: "t-big", Label: "Refund requests", Cohesion: 0.875, ExampleLogIDsJSON: `["log-1","log-2"]`, EmbeddingSignature: topicTestSignature(), CreatedAt: now},
	}))
	var rows []logstore.WarpTopicAssignment
	for index := range 12 {
		topic := "t-big"
		if index < 3 {
			topic = "t-small"
		}
		rows = append(rows, logstore.WarpTopicAssignment{LogID: fmt.Sprintf("log-%d", index), TopicID: topic, Timestamp: now.Add(-time.Duration(index) * time.Minute)})
	}
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, rows))

	reader := NewTopicReader(&recordingStore{row: validWarpConfigRow()}, store)
	result, err := runTool(t, "list_topics", &ToolDeps{logManager: &fakeLogReader{}, topics: reader, scope: Scope{HasIdentity: true, UserID: "admin", Unrestricted: true}}, map[string]any{})
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
	require.Equal(t, now.Format(time.RFC3339), out["computed_at"])
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
	reader := NewTopicReader(&recordingStore{row: validWarpConfigRow()}, topicTestLogStore(t))
	result, err := runTool(t, "list_topics", &ToolDeps{logManager: &fakeLogReader{}, topics: reader, scope: Scope{HasIdentity: true, UserID: "admin", Unrestricted: true}}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Empty(t, out["topics"])
	require.Contains(t, out["hint"], "No topic clusters have been computed")
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

	topics, total, err := NewTopicReader(&recordingStore{row: validWarpConfigRow()}, store).ListTopics(ctx, 15)
	require.NoError(t, err)
	require.Len(t, topics, 1)
	require.Equal(t, "Refund requests", topics[0].Label)
	require.Equal(t, 4, topics[0].Size)
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

	topics, total, err := NewTopicReader(&recordingStore{row: validWarpConfigRow()}, service.topicStore()).ListTopics(context.Background(), 15)
	require.NoError(t, err)
	require.Len(t, topics, 2)
	require.Equal(t, 6, total)
}

// A restricted caller must not learn what the whole deployment asks about from
// a tool that cannot scope its answer. list_topics is withheld from them, and
// the hint sends them to the sample the store does scope.
func TestWarpListTopicsIsWithheldFromARestrictedCaller(t *testing.T) {
	store := topicTestLogStore(t)
	ctx := context.Background()
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{{ID: "t-1", Label: "Refund requests", ExampleLogIDsJSON: `["log-1"]`, EmbeddingSignature: topicTestSignature()}}))
	require.NoError(t, store.UpsertWarpTopicAssignments(ctx, []logstore.WarpTopicAssignment{{LogID: "log-1", TopicID: "t-1", Timestamp: time.Now().UTC()}}))
	reader := NewTopicReader(&recordingStore{row: validWarpConfigRow()}, store)
	deps := &ToolDeps{logManager: &fakeLogReader{}, topics: reader, scope: Scope{Unrestricted: true}}
	tool, ok := toolByName(buildTools(), "list_topics")
	require.True(t, ok)

	// A caller the deployment's resolver restricts carries no queryscope on
	// the context; the restriction lives only in the turn's resolved scope.
	resolverRestricted := &ToolDeps{logManager: &fakeLogReader{}, topics: reader, scope: Scope{Visibility: "their teams' traffic"}}
	for name, call := range map[string]struct {
		ctx  context.Context
		deps *ToolDeps
	}{
		"row scope":           {queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB { return db }), deps},
		"dimension scope":     {queryscope.WithDimensionScope(context.Background(), func(string) ([]string, bool) { return nil, true }), deps},
		"resolver restricted": {context.Background(), resolverRestricted},
	} {
		result, err := tool.execute(call.ctx, call.deps, map[string]any{})
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
	require.Len(t, topics[0].ExampleLogIDs(), topicsLabelSamples)
	centroid, err := logstore.DecodeWarpVector(topics[0].Centroid)
	require.NoError(t, err)
	require.InDelta(t, 1, float64(dot(centroid, centroid)), 1e-5, "the centre is recomputed from every member")
	usage, err := store.SummarizeWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{TopicIDs: []string{topics[0].ID}})
	require.NoError(t, err)
	require.Len(t, usage, 1)
	require.EqualValues(t, 6, usage[0].Requests, "the joined topic holds both clusters' requests")

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

				topics, total, err := topicReader.ListTopics(ctx, 15)
				require.NoError(t, err, "run %d", run)
				require.Equal(t, 2*perSubject, total)
				require.Len(t, topics, 2, "run %d leaves only its own topics", run)
				for _, topic := range topics {
					require.GreaterOrEqual(t, topic.Size, perSubject-5)
					require.Len(t, topic.SampleLogIDs, topicsLabelSamples)
					require.Greater(t, topic.Cohesion, 0.5)
				}
			}
		})
	}
}
