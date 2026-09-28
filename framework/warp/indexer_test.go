package warp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/stretchr/testify/require"
)

type fakeWarpVectorStore struct {
	mu         sync.Mutex
	namespace  string
	dimension  int
	adds       map[string]map[string]interface{}
	embeddings map[string][]float32
	// namespaceOf records which namespace each id was added to, so the
	// paging read and deletes can be scoped the way the real store scopes them.
	namespaceOf map[string]string
	// existing seeds namespaces the store already had, so a test can tell a
	// namespace this save created from one it must not touch.
	existing  []string
	deleted   []string
	nearest   []vectorstore.SearchResult
	queries   []vectorstore.Query
	limit     int64
	limits    []int64
	threshold float64
	// listErr makes namespace discovery fail; addErr makes the next Add fail
	// once. createCalls counts CreateNamespace calls, so a test can see whether
	// provisioning ran per log or once per configuration.
	listErr     error
	addErr      error
	createCalls int
	// failNamespace/failAfter/failErr make writes into one namespace fail once
	// failAfter of them have succeeded, so a test can stop a run partway
	// through writing.
	failNamespace string
	failAfter     int
	failErr       error
	// created is every namespace CreateNamespace was called for. With strict
	// set, a paging read of any other namespace fails, which is how the
	// embedded and Pinecone backends behave after a restart: they keep each
	// namespace's dimension in memory and cannot enumerate without it.
	created map[string]bool
	strict  bool
	// omitVectors makes paging reads leave the vectors out even when asked,
	// as a backend that does not support returning them does.
	omitVectors bool
	// filtersOnServer makes the store one that applies a read's filter itself
	// and says so. reads records the filter of every paging read.
	filtersOnServer bool
	reads           [][]vectorstore.Query
}

func newFakeWarpVectorStore() *fakeWarpVectorStore {
	return &fakeWarpVectorStore{adds: map[string]map[string]interface{}{}, embeddings: map[string][]float32{}, namespaceOf: map[string]string{}}
}

func (f *fakeWarpVectorStore) Ping(context.Context) error { return nil }
func (f *fakeWarpVectorStore) CreateNamespace(_ context.Context, namespace string, dimension int, _ map[string]vectorstore.VectorStoreProperties) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.namespace = namespace
	f.dimension = dimension
	f.createCalls++
	if f.created == nil {
		f.created = map[string]bool{}
	}
	f.created[namespace] = true
	return nil
}
func (f *fakeWarpVectorStore) DeleteNamespace(_ context.Context, namespace string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, namespace)
	return nil
}
func (f *fakeWarpVectorStore) ListNamespaces(context.Context, string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]string(nil), f.existing...), nil
}
func (f *fakeWarpVectorStore) GetChunk(context.Context, string, string) (vectorstore.SearchResult, error) {
	return vectorstore.SearchResult{}, nil
}
func (f *fakeWarpVectorStore) GetChunks(context.Context, string, []string) ([]vectorstore.SearchResult, error) {
	return nil, nil
}

// GetAll lists a namespace's entries in id order, one page, carrying vectors
// only when the read asked for them - the same contract as the real store.
func (f *fakeWarpVectorStore) FiltersVectorReadsOnServer() bool { return f.filtersOnServer }

func (f *fakeWarpVectorStore) GetAll(ctx context.Context, namespace string, queries []vectorstore.Query, _ []string, cursor *string, limit int64) ([]vectorstore.SearchResult, *string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.strict && !f.created[namespace] {
		return nil, nil, fmt.Errorf("dimension not set for namespace %s: call CreateNamespace first", namespace)
	}
	if cursor == nil {
		f.reads = append(f.reads, queries)
	}
	ids := make([]string, 0, len(f.adds))
	for id := range f.adds {
		if f.namespaceOf[id] == namespace && (!f.filtersOnServer || fakeMatchesAll(f.adds[id], queries)) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if cursor != nil {
		for index, id := range ids {
			if id == *cursor {
				ids = ids[index+1:]
				break
			}
		}
	}
	results := make([]vectorstore.SearchResult, 0, len(ids))
	var next *string
	for _, id := range ids {
		if limit > 0 && int64(len(results)) == limit {
			break
		}
		result := vectorstore.SearchResult{ID: id, Properties: f.adds[id]}
		if vectorstore.IncludeVectorsRequested(ctx) && !f.omitVectors {
			result.Vector = f.embeddings[id]
		}
		results = append(results, result)
		next = new(id)
	}
	if limit > 0 && int64(len(results)) < limit {
		next = nil
	}
	return results, next, nil
}
func (f *fakeWarpVectorStore) GetNearest(_ context.Context, _ string, _ []float32, queries []vectorstore.Query, _ []string, threshold float64, limit int64) ([]vectorstore.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = queries
	f.threshold = threshold
	f.limit = limit
	f.limits = append(f.limits, limit)
	// A real vector store returns at most top-K. Returning the whole fixture
	// regardless hid every bug that only shows up once the cap actually bites.
	if limit >= 0 && int64(len(f.nearest)) > limit {
		return append([]vectorstore.SearchResult(nil), f.nearest[:limit]...), nil
	}
	return f.nearest, nil
}
func (f *fakeWarpVectorStore) RequiresVectors() bool { return true }
func (f *fakeWarpVectorStore) failAddAfter(namespace string, succeeded int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNamespace, f.failAfter, f.failErr = namespace, succeeded, err
}

func (f *fakeWarpVectorStore) Add(_ context.Context, namespace string, id string, embedding []float32, metadata map[string]interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil && namespace == f.failNamespace {
		if f.failAfter == 0 {
			return f.failErr
		}
		f.failAfter--
	}
	if f.addErr != nil {
		err := f.addErr
		f.addErr = nil
		return err
	}
	f.adds[id] = metadata
	f.embeddings[id] = embedding
	f.namespaceOf[id] = namespace
	return nil
}
func (f *fakeWarpVectorStore) Delete(_ context.Context, namespace string, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.namespaceOf[id] == namespace {
		delete(f.adds, id)
		delete(f.embeddings, id)
		delete(f.namespaceOf, id)
	}
	return nil
}
func (f *fakeWarpVectorStore) DeleteAll(_ context.Context, namespace string, queries []vectorstore.Query) ([]vectorstore.DeleteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var deleted []vectorstore.DeleteResult
	for id, ns := range f.namespaceOf {
		if ns != namespace || !fakeMatchesAll(f.adds[id], queries) {
			continue
		}
		delete(f.adds, id)
		delete(f.embeddings, id)
		delete(f.namespaceOf, id)
		deleted = append(deleted, vectorstore.DeleteResult{ID: id})
	}
	return deleted, nil
}

// fakeMatchesAll applies a delete's filter the way the real stores do: every
// condition has to hold, and a condition on a property the entry does not
// have does not hold.
func fakeMatchesAll(metadata map[string]interface{}, queries []vectorstore.Query) bool {
	for _, query := range queries {
		value, present := metadata[query.Field]
		if !present {
			return false
		}
		switch query.Operator {
		case vectorstore.QueryOperatorEqual:
			if fmt.Sprint(value) != fmt.Sprint(query.Value) {
				return false
			}
		case vectorstore.QueryOperatorNotEqual:
			if fmt.Sprint(value) == fmt.Sprint(query.Value) {
				return false
			}
		case vectorstore.QueryOperatorLessThan:
			if propertyInt(value) >= propertyInt(query.Value) {
				return false
			}
		case vectorstore.QueryOperatorGreaterThanOrEqual:
			if propertyInt(value) < propertyInt(query.Value) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// idsIn lists the ids stored under a namespace, sorted.
func (f *fakeWarpVectorStore) idsIn(namespace string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := []string{}
	for id, ns := range f.namespaceOf {
		if ns == namespace {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
func (f *fakeWarpVectorStore) Close(context.Context, string) error { return nil }

func TestWarpIndexerEmbedsAndStoresVisibleConversation(t *testing.T) {
	row := validWarpConfigRow()
	row.EmbeddingAPIKeyID = "embedding-key"
	vectors := newFakeWarpVectorStore()
	var embedded string
	executor := func(ctx *schemas.BifrostContext, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		require.Equal(t, true, ctx.Value(schemas.BifrostContextKeySkipPluginPipeline))
		require.Equal(t, "embedding-key", ctx.Value(schemas.BifrostContextKeyAPIKeyID))
		embedded = *request.Input.Text
		vector := make([]float64, row.EmbeddingDimension)
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: vector}}}}, nil
	}
	indexer := NewLogIndexer(&recordingStore{row: row}, vectors, executor, nil)
	defer indexer.Close()
	user, assistant := "find payment failures", "The card was declined"
	latency, cost := 432.6, 0.001234
	entry := &logstore.Log{
		ID: "log-1", Timestamp: time.Unix(100, 0), Object: string(schemas.ChatCompletionRequest), Status: "success",
		Provider: "openai", Model: "gpt-4o", Latency: &latency, Cost: &cost, PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20,
		InputHistoryParsed:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &user}}},
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &assistant}},
	}
	outcome, err := indexer.Index(context.Background(), entry)
	require.NoError(t, err)
	require.Equal(t, IndexOutcomeIndexed, outcome)
	require.Equal(t, "user: find payment failures\nassistant: The card was declined", embedded)
	require.Equal(t, schemas.WarpDefaultLogVectorStoreNamespace, vectors.namespace)
	require.Equal(t, 1536, vectors.dimension)
	require.Equal(t, "log-1", vectors.adds["log-1"]["log_id"])
	require.Equal(t, "success", vectors.adds["log-1"]["status"])
	require.Equal(t, int64(433), vectors.adds["log-1"]["latency_ms"])
	require.Equal(t, int64(1234), vectors.adds["log-1"]["cost_micro_usd"])
	require.Equal(t, 12, vectors.adds["log-1"]["prompt_tokens"])
	require.Equal(t, 8, vectors.adds["log-1"]["completion_tokens"])
	require.Equal(t, 20, vectors.adds["log-1"]["total_tokens"])
	require.NotContains(t, vectors.adds["log-1"], "content")
}

func TestWarpIndexerSkipsPrivateProcessingAndSelfTraffic(t *testing.T) {
	indexer := NewLogIndexer(&recordingStore{row: validWarpConfigRow()}, newFakeWarpVectorStore(), func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		t.Fatal("embedding should not run")
		return nil, nil
	}, nil)
	defer indexer.Close()
	text, warpApp := "secret", "Warp"
	for _, entry := range []*logstore.Log{
		{ID: "hidden", Object: string(schemas.ChatCompletionRequest), Status: "success", ContentHidden: true, ContentSummary: text},
		{ID: "processing", Object: string(schemas.ChatCompletionRequest), Status: "processing", ContentSummary: text},
		{ID: "warp", Object: string(schemas.ResponsesRequest), Status: "success", App: &warpApp, ContentSummary: text},
	} {
		outcome, err := indexer.Index(context.Background(), entry)
		require.NoError(t, err)
		require.Equal(t, IndexOutcomeSkipped, outcome)
	}
}

func TestWarpIndexerRejectsWrongEmbeddingDimension(t *testing.T) {
	indexer := NewLogIndexer(&recordingStore{row: validWarpConfigRow()}, newFakeWarpVectorStore(), func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 2}}}}}, nil
	}, nil)
	defer indexer.Close()
	_, err := indexer.Index(context.Background(), &logstore.Log{ID: "bad", Object: string(schemas.ChatCompletionRequest), Status: "success", ContentSummary: "hello"})
	require.ErrorContains(t, err, "dimension mismatch")
}

// Close closed `done` and then waited, but a worker already inside indexItem
// held a context.Background() - and generateWarpEmbedding derives NoDeadline
// from it. A provider that had stopped answering therefore held shutdown open
// with nothing able to interrupt it.
func TestWarpIndexerCloseInterruptsInFlightWork(t *testing.T) {
	blocked := make(chan struct{})
	released := make(chan struct{})
	var once sync.Once

	executor := func(ctx *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		once.Do(func() { close(blocked) })
		// Behaves like a provider that has stopped answering: it returns only
		// once the context it was handed is cancelled.
		<-ctx.Done()
		close(released)
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "cancelled"}}
	}

	user, assistant := "find payment failures", "The card was declined"
	entry := &logstore.Log{
		ID: "log-close", Timestamp: time.Unix(100, 0), Object: string(schemas.ChatCompletionRequest), Status: "success",
		Provider: "openai", Model: "gpt-4o",
		InputHistoryParsed:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &user}}},
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &assistant}},
	}

	indexer := NewLogIndexer(&recordingStore{row: validWarpConfigRow()}, newFakeWarpVectorStore(), executor, nil)
	// A short budget so the test does not wait out the production one; what is
	// under test is that the budget expiring interrupts the wedged call at all.
	indexer.drainBudget = 100 * time.Millisecond
	indexer.Enqueue(context.Background(), entry)

	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never reached the embedding call")
	}

	closed := make(chan struct{})
	go func() {
		indexer.Close()
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited on work it had no way to interrupt")
	}
	select {
	case <-released:
	default:
		t.Fatal("the in-flight embedding call was never cancelled")
	}
}

// Close must not throw away work that was already accepted. Enqueue told the
// caller the item was taken, and an orderly shutdown that drops the queue loses
// exactly the indexing the backfill would then have to redo.
func TestWarpIndexerDrainsQueueOnClose(t *testing.T) {
	var indexed sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]bool{}

	executor := func(_ *schemas.BifrostContext, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		mu.Lock()
		seen[*request.Input.Text] = true
		mu.Unlock()
		indexed.Done()
		vector := make([]float64, 1536)
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: vector}}}}, nil
	}

	indexer := NewLogIndexer(&recordingStore{row: validWarpConfigRow()}, newFakeWarpVectorStore(), executor, nil)
	const queued = 8
	indexed.Add(queued)
	for i := range queued {
		user, assistant := fmt.Sprintf("question-%d", i), "answer"
		indexer.Enqueue(context.Background(), &logstore.Log{
			ID: fmt.Sprintf("log-%d", i), Timestamp: time.Unix(100, 0),
			Object: string(schemas.ChatCompletionRequest), Status: "success", Provider: "openai", Model: "gpt-4o",
			InputHistoryParsed:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &user}}},
			OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &assistant}},
		})
	}

	indexer.Close()

	done := make(chan struct{})
	go func() { indexed.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		mu.Lock()
		got := len(seen)
		mu.Unlock()
		t.Fatalf("Close discarded queued work: only %d of %d items were indexed", got, queued)
	}
}

// The synchronous entry point reports what it did. A configuration that is not
// usable means nothing was written, and calling that "indexed" tells the
// backfill it has covered a log it never touched - so the gap is never repaired.
func TestWarpIndexReportsSkippedWhenUnconfigured(t *testing.T) {
	user, assistant := "find payment failures", "The card was declined"
	entry := &logstore.Log{
		ID: "log-1", Timestamp: time.Unix(100, 0), Object: string(schemas.ChatCompletionRequest), Status: "success",
		Provider: "openai", Model: "gpt-4o",
		InputHistoryParsed:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &user}}},
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &assistant}},
	}

	// No row at all: Warp is not configured, so nothing can be embedded.
	indexer := NewLogIndexer(&recordingStore{}, newFakeWarpVectorStore(), nil, nil)
	defer indexer.Close()

	outcome, err := indexer.Index(context.Background(), entry)
	require.NoError(t, err, "an unconfigured deployment is not an error")
	require.Equal(t, IndexOutcomeSkipped, outcome,
		"nothing was written, so the caller must not be told it was indexed")
}

// An enqueue after Close must be refused, not silently swallowed.
//
// select picks at random among ready cases, so with a buffered queue and a
// closed done channel it could take the queue send - and the workers have
// already exited, so that item is accepted and never indexed. The log itself is
// safe (this index is best-effort), but the silence is the problem: nothing
// records that the entry needs the backfill to repair it.
func TestWarpIndexerRefusesEnqueueAfterClose(t *testing.T) {
	var embedded atomic.Int64
	executor := func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		embedded.Add(1)
		vector := make([]float64, 1536)
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: vector}}}}, nil
	}

	indexer := NewLogIndexer(&recordingStore{row: validWarpConfigRow()}, newFakeWarpVectorStore(), executor, nil)
	indexer.Close()
	before := embedded.Load()

	// Many attempts: one is not enough to catch a random select, and the point
	// is that none of them may be taken.
	user, assistant := "after close", "answer"
	for i := range 200 {
		indexer.Enqueue(context.Background(), &logstore.Log{
			ID: fmt.Sprintf("post-close-%d", i), Timestamp: time.Unix(100, 0),
			Object: string(schemas.ChatCompletionRequest), Status: "success", Provider: "openai", Model: "gpt-4o",
			InputHistoryParsed:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &user}}},
			OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &assistant}},
		})
	}
	require.Equal(t, 0, len(indexer.queue), "nothing may be left sitting in a queue no worker will read")
	require.Equal(t, before, embedded.Load())
}

// indexableWarpLog builds a minimal entry the indexer will embed and store.
func indexableWarpLog(id string) *logstore.Log {
	user, assistant := "find payment failures", "The card was declined"
	return &logstore.Log{
		ID: id, Timestamp: time.Unix(100, 0), Object: string(schemas.ChatCompletionRequest), Status: "success",
		Provider: "openai", Model: "gpt-4o",
		InputHistoryParsed:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &user}}},
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &assistant}},
	}
}

// warpVectorFor returns an embedding executor that answers with a vector of
// the configured dimension.
func warpVectorFor(dimension int) EmbeddingExecutor {
	return func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: make([]float64, dimension)}}}}, nil
	}
}

// A ListNamespaces failure means ownership is unknowable: creating anyway with
// created=false leaves a namespace SaveConfig's compensation will never
// delete. Discovery failure must fail the ensure, not silently forfeit
// cleanup.
func TestWarpEnsureNamespacePropagatesDiscoveryErrors(t *testing.T) {
	vectors := newFakeWarpVectorStore()
	vectors.listErr = fmt.Errorf("list exploded")

	created, err := ensureWarpNamespace(context.Background(), vectors, "ns", 8)
	require.Error(t, err)
	require.False(t, created)
	require.Zero(t, vectors.createCalls, "an unlistable store must not be written to with unknown ownership")
}

// Provisioning is per configuration, not per log: ListNamespaces plus
// CreateNamespace on every indexed log is vector-store overhead on the hot
// path, and under load it turns into queue-full drops.
func TestWarpIndexerProvisionsNamespaceOncePerConfig(t *testing.T) {
	row := validWarpConfigRow()
	vectors := newFakeWarpVectorStore()
	indexer := NewLogIndexer(&recordingStore{row: row}, vectors, warpVectorFor(row.EmbeddingDimension), nil)
	defer indexer.Close()

	for _, id := range []string{"log-1", "log-2", "log-3"} {
		outcome, err := indexer.Index(context.Background(), indexableWarpLog(id))
		require.NoError(t, err)
		require.Equal(t, IndexOutcomeIndexed, outcome)
	}
	require.Equal(t, 1, vectors.createCalls, "an unchanged namespace/dimension pair must be provisioned once")

	// A failed write may mean the namespace vanished underneath the cache, so
	// the next log re-provisions rather than writing into the void forever.
	vectors.addErr = fmt.Errorf("namespace is gone")
	_, err := indexer.Index(context.Background(), indexableWarpLog("log-4"))
	require.Error(t, err)
	outcome, err := indexer.Index(context.Background(), indexableWarpLog("log-5"))
	require.NoError(t, err)
	require.Equal(t, IndexOutcomeIndexed, outcome)
	require.Equal(t, 2, vectors.createCalls, "a failed write must invalidate the provisioning cache")
}

func responsesTurn(role schemas.ResponsesMessageRoleType, text string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{Role: &role, Content: &schemas.ResponsesMessageContent{ContentStr: &text}}
}

func chatTurn(role schemas.ChatMessageRole, text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: &text}}
}

// A conversation is about what it opened with. Its last user message is often
// a reply to a question - "all", "yes", "-7d" - and a vector made from that
// alone files the conversation under the reply: every "all" lands together,
// whatever was being asked. So the opening question is embedded with the last
// exchange, for both request shapes.
func TestWarpIndexTextCarriesTheOpeningQuestion(t *testing.T) {
	opening, reply := "Which model responds fastest on average this week?", "all"
	responses, ok := buildLogIndexItem(&logstore.Log{
		ID: "log-responses", Object: string(schemas.ResponsesRequest), Status: "success",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{
			responsesTurn(schemas.ResponsesInputMessageRoleSystem, "You are a helpful analyst."),
			responsesTurn(schemas.ResponsesInputMessageRoleUser, opening),
			responsesTurn(schemas.ResponsesInputMessageRoleAssistant, "Whose traffic should I compare?"),
			responsesTurn(schemas.ResponsesInputMessageRoleUser, reply),
		},
	})
	require.True(t, ok)
	require.Equal(t, "user: "+opening+"\nuser: "+reply, responses.text)

	answer := "gpt-4o-mini, at 412 ms."
	chat, ok := buildLogIndexItem(&logstore.Log{
		ID: "log-chat", Object: string(schemas.ChatCompletionRequest), Status: "success",
		InputHistoryParsed: []schemas.ChatMessage{
			chatTurn(schemas.ChatMessageRoleSystem, "You are a helpful analyst."),
			chatTurn(schemas.ChatMessageRoleUser, opening),
			chatTurn(schemas.ChatMessageRoleAssistant, "Whose traffic should I compare?"),
			chatTurn(schemas.ChatMessageRoleUser, reply),
		},
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &answer}},
	})
	require.True(t, ok)
	require.Equal(t, "user: "+opening+"\nuser: "+reply+"\nassistant: "+answer, chat.text)

	// One user turn is not repeated as its own opening.
	single, ok := buildLogIndexItem(&logstore.Log{
		ID: "log-single", Object: string(schemas.ChatCompletionRequest), Status: "success",
		InputHistoryParsed: []schemas.ChatMessage{chatTurn(schemas.ChatMessageRoleUser, opening)},
	})
	require.True(t, ok)
	require.Equal(t, "user: "+opening, single.text)
}

// An opening message can be a pasted file. It gets at most half of what is
// embedded, so it cannot push out the exchange the request is actually about.
func TestWarpIndexTextBoundsTheOpeningQuestion(t *testing.T) {
	opening := strings.Repeat("x", warpMaxEmbeddingBytes*2)
	answer := "Done."
	item, ok := buildLogIndexItem(&logstore.Log{
		ID: "log-long", Object: string(schemas.ChatCompletionRequest), Status: "success",
		InputHistoryParsed: []schemas.ChatMessage{
			chatTurn(schemas.ChatMessageRoleUser, opening),
			chatTurn(schemas.ChatMessageRoleAssistant, "Which file?"),
			chatTurn(schemas.ChatMessageRoleUser, "the second one"),
		},
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &answer}},
	})
	require.True(t, ok)
	require.LessOrEqual(t, len(item.text), warpMaxEmbeddingBytes)
	require.True(t, strings.HasSuffix(item.text, "\nuser: the second one\nassistant: Done."), "the last exchange survives a long opening")
}

// Warp's own conversations are not what people ask the models. They are told
// apart by app and user agent, but those come from headers, and a caller that
// reaches the model another way arrives without them - 629 such requests in
// one morning were indexed and clustered as user traffic. The instructions
// Warp sends are its own, whatever the request was labelled.
func TestWarpIndexerSkipsWarpTrafficByItsInstructions(t *testing.T) {
	instructions := SystemPrompt + "\n\nCosts are in EUR."
	api, agent := "API", "fasthttp"
	question := "Which model responds fastest on average this week?"
	for name, entry := range map[string]*logstore.Log{
		"typed parameters": {
			ID: "typed", Object: string(schemas.ResponsesRequest), Status: "success", App: &api, UserAgent: &agent, ContentSummary: question,
			ParamsParsed: &schemas.ResponsesParameters{Instructions: &instructions},
		},
		"parameters read back from the log store": {
			ID: "stored", Object: string(schemas.ResponsesRequest), Status: "success", App: &api, UserAgent: &agent, ContentSummary: question,
			ParamsParsed: map[string]interface{}{"instructions": instructions},
		},
		"a system message": {
			ID: "system", Object: string(schemas.ChatCompletionRequest), Status: "success", App: &api, UserAgent: &agent,
			InputHistoryParsed: []schemas.ChatMessage{chatTurn(schemas.ChatMessageRoleSystem, instructions), chatTurn(schemas.ChatMessageRoleUser, question)},
		},
	} {
		_, ok := buildLogIndexItem(entry)
		require.False(t, ok, name)
	}

	// Somebody else's assistant is user traffic like any other.
	other := "You are a support assistant for Acme."
	_, ok := buildLogIndexItem(&logstore.Log{
		ID: "other", Object: string(schemas.ResponsesRequest), Status: "success", App: &api, ContentSummary: question,
		ParamsParsed: &schemas.ResponsesParameters{Instructions: &other},
	})
	require.True(t, ok)
}

// A request that does not qualify for the index must not stay in it. One can
// stop qualifying after it was indexed - its content is hidden, or it turns
// out to be Warp's own - and a backfill that only skipped it left the old
// vector where search and clustering still found it. The backfill is how an
// index is repaired, so it takes out what it would not put in.
func TestWarpBackfillRemovesVectorsThatNoLongerQualify(t *testing.T) {
	row := validWarpConfigRow()
	vectors := newFakeWarpVectorStore()
	namespace := schemas.WarpDefaultLogVectorStoreNamespace
	for _, id := range []string{"now-hidden", "warp-own", "still-fine"} {
		require.NoError(t, vectors.Add(context.Background(), namespace, id, []float32{1, 0, 0}, map[string]interface{}{"log_id": id, "warp_log": true}))
	}
	indexer := NewLogIndexer(&recordingStore{row: row}, vectors, backfillEmbeddingExecutor, nil)
	defer indexer.Close()
	config := configFromRow(row)

	instructions := SystemPrompt
	for id, entry := range map[string]*logstore.Log{
		"now-hidden": {ID: "now-hidden", Object: string(schemas.ChatCompletionRequest), Status: "success", ContentHidden: true, ContentSummary: "secret"},
		"warp-own": {ID: "warp-own", Object: string(schemas.ResponsesRequest), Status: "success", ContentSummary: "Which model is fastest?",
			ParamsParsed: &schemas.ResponsesParameters{Instructions: &instructions}},
	} {
		outcome, _, err := indexer.IndexWithConfig(context.Background(), config, entry)
		require.NoError(t, err, id)
		require.Equal(t, IndexOutcomeSkipped, outcome, id)
	}
	outcome, _, err := indexer.IndexWithConfig(context.Background(), config, &logstore.Log{ID: "still-fine", Object: string(schemas.ChatCompletionRequest), Status: "success", ContentSummary: "how do I get a refund"})
	require.NoError(t, err)
	require.Equal(t, IndexOutcomeIndexed, outcome)
	require.Equal(t, []string{"still-fine"}, vectors.idsIn(namespace))
}
