package warp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// assignTestRow is the test configuration in a three-dimensional embedding
// space, so a topic's centre can be written by hand.
func assignTestRow() *tables.TableWarpConfig {
	row := validWarpConfigRow()
	row.EmbeddingDimension = 3
	return row
}

// assignTestEmbedding embeds a request by what it asks: refunds along one
// axis, passwords along another, anything else along the third. Not unit
// length, as a model's embeddings need not be: filing has to scale them.
func assignTestEmbedding(_ *schemas.BifrostContext, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	vector := []float64{0, 0, 3}
	switch text := embeddingRequestText(request); {
	case contains(text, "refund"):
		vector = []float64{2, 0, 0}
	case contains(text, "password"):
		vector = []float64{0, 0.5, 0}
	}
	return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: vector}}}}, nil
}

// assignTestIndexer is an indexer that files what it embeds in store, writing
// its batches often enough for a test to wait on.
func assignTestIndexer(t *testing.T, store logstore.WarpTopicStore) *LogIndexer {
	t.Helper()
	indexer := newLogIndexer(&recordingStore{row: assignTestRow()}, newFakeWarpVectorStore(), assignTestEmbedding, nil, 20*time.Millisecond)
	indexer.SetTopicStore(store)
	t.Cleanup(indexer.Close)
	return indexer
}

func assignTestLog(id, question string, at time.Time) *logstore.Log {
	team := "team-a"
	return &logstore.Log{
		ID: id, Timestamp: at, CreatedAt: at, Object: string(schemas.ChatCompletionRequest), Status: "success",
		Provider: "openai", Model: "gpt-4o", TeamID: &team, TotalTokens: 7,
		InputHistoryParsed: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &question}}},
	}
}

// seedAssignTopic writes one active topic with the given centre and floor,
// built with the test configuration's embedding model unless told otherwise.
func seedAssignTopic(t *testing.T, store logstore.WarpTopicStore, id string, centre []float32, floor float64, signature ...string) {
	t.Helper()
	topic := logstore.WarpTopic{ID: id, Label: id, Centroid: logstore.EncodeWarpVector(centre), AssignFloor: floor, EmbeddingSignature: embeddingConfigSignature(configFromRow(assignTestRow()))}
	if len(signature) > 0 {
		topic.EmbeddingSignature = signature[0]
	}
	require.NoError(t, store.UpsertWarpTopics(context.Background(), []logstore.WarpTopic{topic}))
}

// filedRows waits for the indexer's batches to land and returns every
// assignment row, by log id.
func filedRows(t *testing.T, store logstore.WarpTopicStore, want int) map[string]logstore.WarpTopicAssignment {
	t.Helper()
	var rows []logstore.WarpTopicAssignment
	require.Eventually(t, func() bool {
		var err error
		rows, err = store.ListWarpTopicAssignments(context.Background(), logstore.WarpTopicAssignmentFilter{}, 100)
		require.NoError(t, err)
		return len(rows) >= want
	}, 5*time.Second, 10*time.Millisecond)
	byID := make(map[string]logstore.WarpTopicAssignment, len(rows))
	for _, row := range rows {
		byID[row.LogID] = row
	}
	return byID
}

// Each request is filed as it is indexed: under the nearest topic it is like
// enough to belong to, with its columns copied from the log, or under no
// topic and into the pool with its vector.
func TestWarpIndexerFilesEachRequestUnderItsNearestTopic(t *testing.T) {
	store := topicTestLogStore(t)
	seedAssignTopic(t, store, "refunds", []float32{1, 0, 0}, 0.9)
	seedAssignTopic(t, store, "passwords", []float32{0, 1, 0}, 0.9)
	indexer := assignTestIndexer(t, store)
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	for _, entry := range []*logstore.Log{
		assignTestLog("l-refund", "how do I get a refund", at),
		assignTestLog("l-password", "reset my password", at.Add(time.Minute)),
		assignTestLog("l-stranger", "what is the weather", at.Add(2*time.Minute)),
	} {
		outcome, err := indexer.Index(ctx, entry)
		require.NoError(t, err)
		require.Equal(t, IndexOutcomeIndexed, outcome)
	}

	rows := filedRows(t, store, 3)
	require.Equal(t, "refunds", rows["l-refund"].TopicID)
	require.InDelta(t, 1, rows["l-refund"].Similarity, 1e-6)
	require.Equal(t, "passwords", rows["l-password"].TopicID)
	require.Empty(t, rows["l-stranger"].TopicID, "like no topic, so filed under none")
	require.Zero(t, rows["l-stranger"].Similarity)
	for id, row := range rows {
		require.Equal(t, "team-a", row.TeamID, id)
		require.Equal(t, "gpt-4o", row.Model, id)
		require.Equal(t, 7, row.TotalTokens, id)
		require.False(t, row.AssignedAt.IsZero(), id)
	}
	require.True(t, rows["l-refund"].Timestamp.Equal(at))

	pooled, err := store.ListWarpTopicUnmatched(ctx, embeddingConfigSignature(configFromRow(assignTestRow())), 10)
	require.NoError(t, err)
	require.Len(t, pooled, 1, "only the request that matched nothing is pooled")
	require.Equal(t, "l-stranger", pooled[0].LogID)
	vector, err := logstore.DecodeWarpVector(pooled[0].Vector)
	require.NoError(t, err)
	require.Equal(t, []float32{0, 0, 1}, vector, "pooled as a unit vector, which is what discovery clusters")
}

// Before the first run there are no topics to file under, and pooling every
// request would copy the whole index into the log store for a run that reads
// the index itself. Nothing is written.
func TestWarpIndexerFilesNothingWithoutTopics(t *testing.T) {
	store := topicTestLogStore(t)
	indexer := assignTestIndexer(t, store)
	ctx := context.Background()
	_, err := indexer.Index(ctx, assignTestLog("l-1", "how do I get a refund", time.Now()))
	require.NoError(t, err)
	indexer.Close()

	rows, err := store.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{}, 10)
	require.NoError(t, err)
	require.Empty(t, rows)
	count, err := store.CountWarpTopicUnmatched(ctx, embeddingConfigSignature(configFromRow(assignTestRow())))
	require.NoError(t, err)
	require.Zero(t, count)
}

// A topic built with another embedding model means nothing against today's
// vectors, so it takes no requests.
func TestWarpIndexerIgnoresTopicsFromAnotherEmbeddingModel(t *testing.T) {
	store := topicTestLogStore(t)
	seedAssignTopic(t, store, "refunds-elsewhere", []float32{1, 0, 0}, 0.5, "another model")
	indexer := assignTestIndexer(t, store)
	_, err := indexer.Index(context.Background(), assignTestLog("l-1", "how do I get a refund", time.Now()))
	require.NoError(t, err)
	indexer.Close()

	rows, err := store.ListWarpTopicAssignments(context.Background(), logstore.WarpTopicAssignmentFilter{}, 10)
	require.NoError(t, err)
	require.Empty(t, rows)
}

// The centres are read from the store once and kept, so a run that writes
// new topics has to be able to say so, and a run on another instance has to
// reach the indexer when the copy expires.
func TestWarpIndexerReadsTopicsAgainWhenToldOrWhenStale(t *testing.T) {
	store := topicTestLogStore(t)
	indexer := assignTestIndexer(t, store)
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	_, err := indexer.Index(ctx, assignTestLog("before", "how do I get a refund", at))
	require.NoError(t, err)
	seedAssignTopic(t, store, "refunds", []float32{1, 0, 0}, 0.9)
	_, err = indexer.Index(ctx, assignTestLog("cached", "how do I get a refund", at.Add(time.Minute)))
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	rows, err := store.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{}, 10)
	require.NoError(t, err)
	require.Empty(t, rows, "the indexer still holds the empty set it read")

	indexer.InvalidateTopics()
	_, err = indexer.Index(ctx, assignTestLog("told", "how do I get a refund", at.Add(2*time.Minute)))
	require.NoError(t, err)
	filed := filedRows(t, store, 1)
	require.Equal(t, "refunds", filed["told"].TopicID)

	seedAssignTopic(t, store, "passwords", []float32{0, 1, 0}, 0.9)
	_, err = indexer.Index(ctx, assignTestLog("still-cached", "reset my password", at.Add(3*time.Minute)))
	require.NoError(t, err)
	filed = filedRows(t, store, 2)
	require.Empty(t, filed["still-cached"].TopicID, "the new topic is not seen until the copy expires")

	indexer.centres.ttl = 0
	_, err = indexer.Index(ctx, assignTestLog("stale", "reset my password", at.Add(4*time.Minute)))
	require.NoError(t, err)
	filed = filedRows(t, store, 3)
	require.Equal(t, "passwords", filed["stale"].TopicID)
}

// Rows are written in batches: when enough have gathered, without waiting
// out the interval, and whatever is left when the indexer closes.
func TestWarpIndexerWritesFiledRequestsInBatches(t *testing.T) {
	store := topicTestLogStore(t)
	seedAssignTopic(t, store, "refunds", []float32{1, 0, 0}, 0.9)
	indexer := newLogIndexer(&recordingStore{row: assignTestRow()}, newFakeWarpVectorStore(), assignTestEmbedding, nil, time.Hour)
	indexer.SetTopicStore(store)
	indexer.assigner.flushSize = 2
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	_, err := indexer.Index(ctx, assignTestLog("l-1", "how do I get a refund", at))
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	rows, err := store.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{}, 10)
	require.NoError(t, err)
	require.Empty(t, rows, "one request waits for the interval")
	require.Equal(t, 1, indexer.assigner.pending())

	_, err = indexer.Index(ctx, assignTestLog("l-2", "refund status", at.Add(time.Minute)))
	require.NoError(t, err)
	filedRows(t, store, 2)

	_, err = indexer.Index(ctx, assignTestLog("l-3", "another refund", at.Add(2*time.Minute)))
	require.NoError(t, err)
	indexer.Close()
	rows, err = store.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{}, 10)
	require.NoError(t, err)
	require.Len(t, rows, 3, "closing writes what was waiting")
}

// The pool is bounded. Every so many pooled requests it is pruned to the
// newest few within the age bound.
func TestWarpIndexerPrunesThePool(t *testing.T) {
	store := topicTestLogStore(t)
	seedAssignTopic(t, store, "refunds", []float32{1, 0, 0}, 0.9)
	indexer := assignTestIndexer(t, store)
	indexer.assigner.pruneEvery, indexer.assigner.poolMax, indexer.assigner.poolMaxAge = 3, 2, time.Hour
	ctx := context.Background()
	// Recent, so the count bound is what prunes; and one too old to keep,
	// whatever the count.
	at := time.Now().UTC().Add(-10 * time.Minute)
	for index, question := range []string{"weather", "jokes", "recipes"} {
		_, err := indexer.Index(ctx, assignTestLog(question, question, at.Add(time.Duration(index)*time.Minute)))
		require.NoError(t, err)
	}
	indexer.Close()
	pooled, err := store.ListWarpTopicUnmatched(ctx, embeddingConfigSignature(configFromRow(assignTestRow())), 10)
	require.NoError(t, err)
	require.Equal(t, []string{"recipes", "jokes"}, warpUnmatchedIDs(pooled), "the newest two stay")

	indexer = assignTestIndexer(t, store)
	indexer.assigner.pruneEvery, indexer.assigner.poolMax, indexer.assigner.poolMaxAge = 1, 100, time.Hour
	_, err = indexer.Index(ctx, assignTestLog("stale", "stale", time.Now().Add(-2*time.Hour)))
	require.NoError(t, err)
	indexer.Close()
	pooled, err = store.ListWarpTopicUnmatched(ctx, embeddingConfigSignature(configFromRow(assignTestRow())), 10)
	require.NoError(t, err)
	require.Equal(t, []string{"recipes", "jokes"}, warpUnmatchedIDs(pooled), "a request older than the age bound does not stay, however few are pooled")
}

// Live assignment files through the reader's store, so it follows the reader:
// a logging plugin enabled later brings it, and one removed takes it away.
func TestWarpIndexerFollowsTheLogReader(t *testing.T) {
	logs := map[string]logstore.Log{}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: assignTestRow()}), WithLogReader(&semanticLogReader{logs: logs}),
		WithVectorStore(newFakeWarpVectorStore()), WithEmbeddingExecutor(assignTestEmbedding),
	)
	defer service.Shutdown()
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	_, err := service.indexer.Index(ctx, assignTestLog("unfiled", "how do I get a refund", at))
	require.NoError(t, err)

	reader := topicTestReader(t, logs)
	seedAssignTopic(t, reader.topics, "refunds", []float32{1, 0, 0}, 0.9)
	service.SetLogReader(reader)
	_, err = service.indexer.Index(ctx, assignTestLog("filed", "how do I get a refund", at.Add(time.Minute)))
	require.NoError(t, err)
	rows := filedRows(t, reader.topics, 1)
	require.Equal(t, "refunds", rows["filed"].TopicID)
	require.NotContains(t, rows, "unfiled")

	service.SetLogReader(&semanticLogReader{logs: logs})
	_, err = service.indexer.Index(ctx, assignTestLog("after", "how do I get a refund", at.Add(2*time.Minute)))
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	rows = filedRows(t, reader.topics, 1)
	require.NotContains(t, rows, "after")
}

// A run that writes new topics tells the indexer at once, so the next request
// through this process is filed under them rather than under the retired ones
// for up to a minute.
func TestWarpTopicsRunRefreshesLiveAssignment(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	logs := topicTestLogs(start)
	reader := topicTestReader(t, logs)
	row := assignTestRow()
	vectors := newFakeWarpVectorStore()
	for id, vector := range topicTestVectors() {
		require.NoError(t, vectors.Add(context.Background(), schemas.WarpDefaultLogVectorStoreNamespace, id, vector, map[string]interface{}{"log_id": id, "timestamp": logs[id].Timestamp.Unix(), "warp_log": true}))
	}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: row}), WithLogReader(reader),
		WithVectorStore(vectors), WithEmbeddingExecutor(assignTestEmbedding), WithChatFunc(topicTestNaming),
	)
	defer service.Shutdown()
	ctx := context.Background()

	// Filed under the topics that exist now, which are none.
	_, err := service.indexer.Index(ctx, assignTestLog("before", "how do I get a refund", start.Add(time.Hour)))
	require.NoError(t, err)

	metaJSON, err := service.BuildTopicsJobMeta(ctx, start, start.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = service.RunTopicsJob(ctx, tables.TableSidekiqJob{ID: "job-1", Metadata: metaJSON}, func(string) error { return nil })
	require.NoError(t, err)
	topics, err := reader.topics.ListWarpTopics(ctx, logstore.WarpTopicFilter{Statuses: []string{logstore.WarpTopicStatusActive}})
	require.NoError(t, err)
	require.Len(t, topics, 2)

	_, err = service.indexer.Index(ctx, assignTestLog("after", "how do I get a refund", start.Add(2*time.Hour)))
	require.NoError(t, err)
	service.indexer.Close()
	rows, err := reader.topics.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{LogIDs: []string{"before", "after"}}, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1, "nothing to file under before the run; the run's topics at once after it")
	require.Equal(t, "after", rows[0].LogID)
	require.Equal(t, topicLabelsByID(topics)[rows[0].TopicID], "Refund requests")
}

func TestNearestTopic(t *testing.T) {
	centres := []topicCentre{
		{id: "x", centre: []float32{1, 0, 0}, floor: 0.9},
		{id: "y", centre: []float32{0, 1, 0}, floor: 0.5},
		{id: "odd", centre: []float32{1, 0}, floor: 0},
	}
	id, similarity, matched := nearestTopic(centres, []float32{0.8, 0.6, 0})
	require.Equal(t, "x", id, "the nearest, even when not near enough")
	require.InDelta(t, 0.8, similarity, 1e-6)
	require.False(t, matched, "below x's floor")

	id, _, matched = nearestTopic(centres, []float32{0.6, 0.8, 0})
	require.Equal(t, "y", id)
	require.True(t, matched, "above y's floor")

	id, _, matched = nearestTopic(centres, []float32{0, 0, 1})
	require.Equal(t, "x", id, "the first of equals")
	require.False(t, matched)

	_, _, matched = nearestTopic(nil, []float32{1})
	require.False(t, matched)
}

// Closing drains the queue, and a request filed while it drains lands after
// the writer's last scheduled flush. It must still be written, or an orderly
// shutdown loses exactly the rows the writer had accepted.
func TestWarpIndexerCloseWritesWhatTheDrainFiled(t *testing.T) {
	store := topicTestLogStore(t)
	seedAssignTopic(t, store, "refunds", []float32{1, 0, 0}, 0.9)
	release := make(chan struct{})
	embed := func(ctx *schemas.BifrostContext, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		<-release
		return assignTestEmbedding(ctx, request)
	}
	indexer := newLogIndexer(&recordingStore{row: assignTestRow()}, newFakeWarpVectorStore(), embed, nil, time.Hour)
	indexer.SetTopicStore(store)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for index, id := range []string{"a", "b", "c"} {
		indexer.Enqueue(context.Background(), assignTestLog(id, "refund "+id, at.Add(time.Duration(index)*time.Minute)))
	}

	closed := make(chan struct{})
	go func() {
		indexer.Close()
		close(closed)
	}()
	// Close is now waiting on the workers, which are waiting on the model.
	time.Sleep(50 * time.Millisecond)
	close(release)
	<-closed

	rows, err := store.ListWarpTopicAssignments(context.Background(), logstore.WarpTopicAssignmentFilter{}, 10)
	require.NoError(t, err)
	require.Len(t, rows, 3)
}

// blockingTopicStore records what is written to it, and holds its first
// assignment write until release is closed.
type blockingTopicStore struct {
	logstore.WarpTopicStore
	mu       sync.Mutex
	written  []string
	entered  chan struct{}
	release  chan struct{}
	blockOne sync.Once
}

func (s *blockingTopicStore) UpsertWarpTopicAssignments(_ context.Context, rows []logstore.WarpTopicAssignment) error {
	s.blockOne.Do(func() {
		if s.entered != nil {
			close(s.entered)
			<-s.release
		}
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		s.written = append(s.written, row.LogID)
	}
	return nil
}

func (s *blockingTopicStore) AddWarpTopicUnmatched(context.Context, []logstore.WarpTopicUnmatched) error {
	return nil
}

func (s *blockingTopicStore) wrote() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.written...)
}

// deadlineTopicStore refuses a write on a cancelled context, as a real store
// does, and records whether each write it took carried a deadline.
type deadlineTopicStore struct {
	logstore.WarpTopicStore
	mu        sync.Mutex
	written   []string
	deadlines []bool
}

func (s *deadlineTopicStore) UpsertWarpTopicAssignments(ctx context.Context, rows []logstore.WarpTopicAssignment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, bounded := ctx.Deadline()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		s.written = append(s.written, row.LogID)
	}
	s.deadlines = append(s.deadlines, bounded)
	return nil
}

func (s *deadlineTopicStore) AddWarpTopicUnmatched(context.Context, []logstore.WarpTopicUnmatched) error {
	return nil
}

// A batch taken for writing is written though shutdown has cancelled the
// indexer's context, still bounded by the flush timeout: the loop's last
// flush, and the swap's write to the store it replaces. Both used to inherit
// the cancellation and drop the batch they had taken.
func TestTopicAssignerWritesWhatItTookAfterShutdownCancels(t *testing.T) {
	store := &deadlineTopicStore{}
	assigner := newTopicAssigner(nil)
	assigner.setStore(context.Background(), store)
	assigner.add(logstore.WarpTopicAssignment{LogID: "pending"}, nil)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	close(done)
	assigner.run(cancelled, done, time.Hour)
	require.Equal(t, []string{"pending"}, store.written, "the loop's last flush")
	require.Equal(t, []bool{true}, store.deadlines)

	old := &deadlineTopicStore{}
	indexer := newLogIndexer(&recordingStore{row: assignTestRow()}, newFakeWarpVectorStore(), assignTestEmbedding, nil, time.Hour)
	t.Cleanup(indexer.Close)
	indexer.SetTopicStore(old)
	indexer.assigner.add(logstore.WarpTopicAssignment{LogID: "swapped"}, nil)
	indexer.cancel()
	indexer.SetTopicStore(nil)
	require.Equal(t, []string{"swapped"}, old.written, "the swap's write to the old store")
	require.Equal(t, []bool{true}, old.deadlines)
}

// A request filed while the store is being swapped reaches one of the two
// stores. setStore used to flush, let go of the lock, and then clear the
// batches: a request filed in between was cleared without being written.
func TestTopicAssignerSwapLosesNoFiledRequest(t *testing.T) {
	old := &blockingTopicStore{entered: make(chan struct{}), release: make(chan struct{})}
	next := &blockingTopicStore{}
	assigner := newTopicAssigner(nil)
	ctx := context.Background()
	assigner.setStore(ctx, old)
	assigner.add(logstore.WarpTopicAssignment{LogID: "before"}, nil)

	swapped := make(chan struct{})
	go func() {
		assigner.setStore(ctx, next)
		close(swapped)
	}()
	<-old.entered // the swap is writing "before" to the old store
	assigner.add(logstore.WarpTopicAssignment{LogID: "during"}, nil)
	close(old.release)
	<-swapped
	assigner.flush(ctx)

	written := append(old.wrote(), next.wrote()...)
	require.ElementsMatch(t, []string{"before", "during"}, written)
}

// Swapping the store holds back filing only for the swap itself. Writing what
// was waiting for the old store can take as long as that store does, and new
// requests are filed against the new one meanwhile.
func TestWarpIndexerSwapDoesNotHoldFilingDuringTheFlush(t *testing.T) {
	old := &blockingTopicStore{entered: make(chan struct{}), release: make(chan struct{})}
	next := &blockingTopicStore{}
	// An interval long enough that only the swap writes the batch.
	indexer := newLogIndexer(&recordingStore{row: assignTestRow()}, newFakeWarpVectorStore(), assignTestEmbedding, nil, time.Hour)
	t.Cleanup(indexer.Close)
	indexer.SetTopicStore(old)
	indexer.assigner.add(logstore.WarpTopicAssignment{LogID: "before"}, nil)

	swapped := make(chan struct{})
	go func() {
		indexer.SetTopicStore(next)
		close(swapped)
	}()
	<-old.entered // the swap is writing "before" to the old store
	locked := indexer.topicMu.TryRLock()
	if locked {
		indexer.topicMu.RUnlock()
	}
	close(old.release)
	<-swapped
	require.True(t, locked, "filing is not held up while the old store is written")
	require.Equal(t, []string{"before"}, old.wrote(), "what was waiting still goes to the store it was filed for")
	indexer.centres.mu.Lock()
	installed := indexer.centres.store
	indexer.centres.mu.Unlock()
	require.Equal(t, logstore.WarpTopicStore(next), installed)
}

// A request is filed in the store its topic was read from. A store swapped
// between the two used to receive a row naming a topic it does not have.
func TestWarpIndexerFilesUnderTheStoreItReadTopicsFrom(t *testing.T) {
	old, next := topicTestLogStore(t), topicTestLogStore(t)
	seedAssignTopic(t, old, "refunds", []float32{1, 0, 0}, 0.9)
	indexer := assignTestIndexer(t, old)
	swapped := make(chan struct{})
	indexer.beforeFile = func() {
		indexer.beforeFile = nil
		go func() {
			indexer.SetTopicStore(next)
			close(swapped)
		}()
		select {
		case <-swapped:
		case <-time.After(200 * time.Millisecond):
		}
	}

	_, err := indexer.Index(context.Background(), assignTestLog("l-refund", "how do I get a refund", time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)))
	require.NoError(t, err)
	<-swapped

	rows := filedRows(t, old, 1)
	require.Equal(t, "refunds", rows["l-refund"].TopicID)
	indexer.assigner.flush(context.Background())
	inNext, err := next.ListWarpTopicAssignments(context.Background(), logstore.WarpTopicAssignmentFilter{}, 10)
	require.NoError(t, err)
	require.Empty(t, inNext, "the new store has no topic named refunds")
}
