package semanticcache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// observableStore is a fuller mock than directFastPathStore — it records all
// Delete / DeleteAll / DeleteNamespace calls so the tests can assert on the
// public Clear* APIs and on Cleanup teardown behavior.
type observableStore struct {
	mu               sync.Mutex
	chunks           map[string]vectorstore.SearchResult
	addIDs           []string
	deleteIDs        []string
	deleteAllQueries [][]vectorstore.Query
	namespaceDeletes int
	deleteAllErr     error
	deleteErr        error
	deleteAllResults []vectorstore.DeleteResult
}

func newObservableStore() *observableStore {
	return &observableStore{chunks: make(map[string]vectorstore.SearchResult)}
}

func (s *observableStore) Ping(ctx context.Context) error { return nil }
func (s *observableStore) CreateNamespace(ctx context.Context, ns string, dim int, props map[string]vectorstore.VectorStoreProperties) error {
	return nil
}
func (s *observableStore) DeleteNamespace(ctx context.Context, ns string) error {
	s.mu.Lock()
	s.namespaceDeletes++
	s.mu.Unlock()
	return nil
}
func (s *observableStore) GetChunk(ctx context.Context, ns string, id string) (vectorstore.SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.chunks[id]
	if !ok {
		return vectorstore.SearchResult{}, vectorstore.ErrNotFound
	}
	return r, nil
}
func (s *observableStore) GetChunks(ctx context.Context, ns string, ids []string) ([]vectorstore.SearchResult, error) {
	return nil, vectorstore.ErrNotSupported
}
func (s *observableStore) GetAll(ctx context.Context, ns string, q []vectorstore.Query, sf []string, cur *string, lim int64) ([]vectorstore.SearchResult, *string, error) {
	return nil, nil, vectorstore.ErrNotSupported
}
func (s *observableStore) GetNearest(ctx context.Context, ns string, v []float32, q []vectorstore.Query, sf []string, th float64, lim int64) ([]vectorstore.SearchResult, error) {
	return nil, vectorstore.ErrNotSupported
}
func (s *observableStore) RequiresVectors() bool { return false }
func (s *observableStore) Add(ctx context.Context, ns string, id string, e []float32, m map[string]interface{}) error {
	s.mu.Lock()
	s.addIDs = append(s.addIDs, id)
	s.chunks[id] = vectorstore.SearchResult{ID: id, Properties: m}
	s.mu.Unlock()
	return nil
}
func (s *observableStore) Delete(ctx context.Context, ns string, id string) error {
	s.mu.Lock()
	s.deleteIDs = append(s.deleteIDs, id)
	delete(s.chunks, id)
	err := s.deleteErr
	s.mu.Unlock()
	return err
}
func (s *observableStore) DeleteAll(ctx context.Context, ns string, queries []vectorstore.Query) ([]vectorstore.DeleteResult, error) {
	s.mu.Lock()
	s.deleteAllQueries = append(s.deleteAllQueries, queries)
	results := s.deleteAllResults
	err := s.deleteAllErr
	s.mu.Unlock()
	return results, err
}
func (s *observableStore) Close(ctx context.Context, ns string) error { return nil }

func newTestPlugin(t *testing.T, store vectorstore.VectorStore) *Plugin {
	t.Helper()
	cfg := getDefaultTestConfig()
	return &Plugin{
		store:  store,
		config: cfg,
		logger: bifrost.NewDefaultLogger(schemas.LogLevelDebug),
		stopCh: make(chan struct{}),
	}
}

// -----------------------------------------------------------------------------
// ClearCacheForCacheID
// -----------------------------------------------------------------------------

func TestClearCacheForCacheID_EmptyIDRejected(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	if err := plugin.ClearCacheForCacheID(""); err == nil {
		t.Fatal("expected error for empty cache ID")
	}
}

func TestClearCacheForCacheID_PointDelete(t *testing.T) {
	store := newObservableStore()
	plugin := newTestPlugin(t, store)

	if err := plugin.ClearCacheForCacheID("cache-abc"); err != nil {
		t.Fatalf("ClearCacheForCacheID failed: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.deleteIDs) != 1 || store.deleteIDs[0] != "cache-abc" {
		t.Fatalf("expected single Delete call for 'cache-abc', got %v", store.deleteIDs)
	}
}

// -----------------------------------------------------------------------------
// ClearCacheForKey
// -----------------------------------------------------------------------------

func TestClearCacheForKey_FiltersByCacheKeyAndPluginMarker(t *testing.T) {
	store := newObservableStore()
	plugin := newTestPlugin(t, store)

	if err := plugin.ClearCacheForKey("session-42"); err != nil {
		t.Fatalf("ClearCacheForKey failed: %v", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.deleteAllQueries) != 1 {
		t.Fatalf("expected one DeleteAll call, got %d", len(store.deleteAllQueries))
	}
	queries := store.deleteAllQueries[0]
	gotKey, gotMarker := false, false
	for _, q := range queries {
		if q.Field == "cache_key" && q.Value == "session-42" && q.Operator == vectorstore.QueryOperatorEqual {
			gotKey = true
		}
		if q.Field == "from_bifrost_semantic_cache_plugin" && q.Value == true {
			gotMarker = true
		}
	}
	if !gotKey {
		t.Errorf("expected cache_key=session-42 filter, got %+v", queries)
	}
	if !gotMarker {
		t.Errorf("expected from_bifrost_semantic_cache_plugin=true filter, got %+v", queries)
	}
}

// -----------------------------------------------------------------------------
// stampCacheDebugForMiss
// -----------------------------------------------------------------------------

func TestStampCacheDebugForMiss_AlwaysSetsCacheID(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	state := &cacheState{}
	extra := &schemas.BifrostResponseExtraFields{}

	plugin.stampCacheDebugForMiss(state, extra, "stored-id-123", false, false)

	if extra.CacheDebug == nil {
		t.Fatal("expected CacheDebug to be stamped on miss")
	}
	if extra.CacheDebug.CacheHit {
		t.Fatal("expected CacheHit=false on miss")
	}
	if extra.CacheDebug.CacheID == nil || *extra.CacheDebug.CacheID != "stored-id-123" {
		t.Fatalf("expected CacheID=stored-id-123, got %v", extra.CacheDebug.CacheID)
	}
	// No semantic search ran → embedding fields should be unset.
	if extra.CacheDebug.ProviderUsed != nil || extra.CacheDebug.InputTokens != nil {
		t.Fatalf("expected embedding fields nil on direct-only miss, got %+v", extra.CacheDebug)
	}
}

func TestStampCacheDebugForMiss_AddsTelemetryWhenSemanticRan(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	state := &cacheState{EmbeddingsInputTokens: 42}
	extra := &schemas.BifrostResponseExtraFields{}

	plugin.stampCacheDebugForMiss(state, extra, "id-x", false, false)

	if extra.CacheDebug.InputTokens == nil || *extra.CacheDebug.InputTokens != 42 {
		t.Fatalf("expected InputTokens=42, got %v", extra.CacheDebug.InputTokens)
	}
	if extra.CacheDebug.ProviderUsed == nil {
		t.Fatal("expected ProviderUsed to be stamped when semantic ran")
	}
}

func TestStampCacheDebugForMiss_StreamSkipsNonFinalChunks(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	state := &cacheState{}
	extra := &schemas.BifrostResponseExtraFields{}

	plugin.stampCacheDebugForMiss(state, extra, "id-y", true, false) // mid-stream

	if extra.CacheDebug != nil {
		t.Fatal("expected mid-stream chunk to NOT be stamped")
	}
}

// -----------------------------------------------------------------------------
// Cleanup
// -----------------------------------------------------------------------------

func TestCleanup_SkipsEntryDeletionWhenDisabled(t *testing.T) {
	store := newObservableStore()
	plugin := newTestPlugin(t, store) // CleanUpOnShutdown=false

	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.deleteAllQueries) != 0 {
		t.Errorf("expected no DeleteAll calls when cleanup disabled, got %d", len(store.deleteAllQueries))
	}
	if store.namespaceDeletes != 0 {
		t.Errorf("expected no DeleteNamespace calls when cleanup disabled, got %d", store.namespaceDeletes)
	}
}

func TestCleanup_DrainsPendingWriters(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())

	var done atomic.Bool
	plugin.writersWg.Add(1)
	go func() {
		defer plugin.writersWg.Done()
		time.Sleep(50 * time.Millisecond)
		done.Store(true)
	}()

	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}
	if !done.Load() {
		t.Fatal("expected Cleanup to wait for pending writers to finish")
	}
}

// -----------------------------------------------------------------------------
// cacheState reaper
// -----------------------------------------------------------------------------

func TestCleanupOldCacheStates_ReapsOldEntries(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())

	plugin.cacheStates.Store("old-1", &cacheState{CreatedAt: time.Now().Add(-2 * cacheStateMaxAge)})
	plugin.cacheStates.Store("old-2", &cacheState{CreatedAt: time.Now().Add(-2 * cacheStateMaxAge)})
	plugin.cacheStates.Store("recent", &cacheState{CreatedAt: time.Now()})

	plugin.cleanupOldCacheStates()

	if _, ok := plugin.cacheStates.Load("old-1"); ok {
		t.Error("expected old-1 to be reaped")
	}
	if _, ok := plugin.cacheStates.Load("old-2"); ok {
		t.Error("expected old-2 to be reaped")
	}
	if _, ok := plugin.cacheStates.Load("recent"); !ok {
		t.Error("expected recent to be preserved")
	}
}

// -----------------------------------------------------------------------------
// Stream accumulator reaper
// -----------------------------------------------------------------------------

func TestCleanupOldStreamAccumulators_ReapsByLastSeenAt(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())

	plugin.streamAccumulators.Store("old", &StreamAccumulator{
		RequestID:  "old",
		LastSeenAt: time.Now().Add(-2 * streamAccumulatorMaxAge),
	})
	plugin.streamAccumulators.Store("recent", &StreamAccumulator{
		RequestID:  "recent",
		LastSeenAt: time.Now(),
	})

	plugin.cleanupOldStreamAccumulators()

	if _, ok := plugin.streamAccumulators.Load("old"); ok {
		t.Error("expected old accumulator to be reaped")
	}
	if _, ok := plugin.streamAccumulators.Load("recent"); !ok {
		t.Error("expected recent accumulator to be preserved")
	}
}

// -----------------------------------------------------------------------------
// Replay goroutine cancellation (buildStreamingResponseFromResult)
// -----------------------------------------------------------------------------

func TestBuildStreamingResponseFromResult_ConsumerAbandonment(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())

	// Build a cached entry with multiple chunks.
	chunkJSON := `{"chat_response":{"choices":[]}}`
	streamArray := []string{chunkJSON, chunkJSON, chunkJSON, chunkJSON, chunkJSON}

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionStreamRequest,
		ChatRequest: CreateBasicChatRequest("hi", 0.7, 50),
	}
	ctx := newBaseTestContext()
	state := &cacheState{}

	sc, err := plugin.buildStreamingResponseFromResult(
		ctx, state, req,
		vectorstore.SearchResult{ID: "stream-id"},
		streamArray, CacheTypeSemantic, nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("buildStreamingResponseFromResult failed: %v", err)
	}
	if sc == nil || sc.Stream == nil {
		t.Fatal("expected a stream short-circuit")
	}

	// Read one chunk, then cancel ctx — the replay goroutine should exit
	// (close the channel) instead of blocking on its send forever.
	// Guard the first receive so a regression that stalls the producer
	// fails fast instead of hanging until the suite-level timeout.
	select {
	case _, ok := <-sc.Stream:
		if !ok {
			t.Fatal("expected first replay chunk before cancellation, channel closed early")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replay goroutine did not emit the first chunk")
	}
	ctx.Cancel()

	// Drain remaining; channel must close within a reasonable bound.
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-sc.Stream:
			if !ok {
				return // channel closed → replay goroutine exited cleanly ✓
			}
		case <-timeout:
			t.Fatal("replay goroutine did not exit after ctx.Cancel()")
		}
	}
}
