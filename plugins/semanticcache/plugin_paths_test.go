package semanticcache

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// -----------------------------------------------------------------------------
// PostLLMHook error path
// -----------------------------------------------------------------------------

func TestPostLLMHook_SkipsOnBifrostError(t *testing.T) {
	store := newObservableStore()
	plugin := newTestPlugin(t, store)

	ctx := newBaseTestContext()
	ctx.SetValue(CacheKey, keyForTest(t, ""))

	// Drive a normal PreLLMHook so cacheState exists.
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("hello", 0.7, 50),
	}
	if _, _, err := plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}

	// Pass a non-nil bifrost error to PostLLMHook.
	bifrostErr := &schemas.BifrostError{
		Error: &schemas.ErrorField{Message: "upstream blew up"},
	}
	res := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.ChatCompletionRequest},
		},
	}
	if _, _, err := plugin.PostLLMHook(ctx, res, bifrostErr); err != nil {
		t.Fatalf("PostLLMHook failed: %v", err)
	}
	plugin.WaitForPendingOperations()

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.addIDs) != 0 {
		t.Fatalf("expected zero cache writes on error response, got %d", len(store.addIDs))
	}
}

// -----------------------------------------------------------------------------
// shouldSkipCacheWrite paths
//
// shouldSkipCacheWrite gates only the cache WRITE — cache_debug telemetry is
// stamped before this is consulted (see PostLLMHook). The cache-hit replay
// case is handled separately as an early return in PostLLMHook and is not
// exercised here.
// -----------------------------------------------------------------------------

func TestShouldSkipCacheWrite_LargePayloadMode(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())

	ctx := newBaseTestContext()
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)

	if !plugin.shouldSkipCacheWrite(ctx) {
		t.Fatal("expected LargePayloadMode to skip the cache write")
	}
}

func TestShouldSkipCacheWrite_LargeResponseMode(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())

	ctx := newBaseTestContext()
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseMode, true)

	if !plugin.shouldSkipCacheWrite(ctx) {
		t.Fatal("expected LargeResponseMode to skip the cache write")
	}
}

func TestShouldSkipCacheWrite_NoStoreFlag(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())

	ctx := newBaseTestContext()
	ctx.SetValue(CacheNoStoreKey, true)

	if !plugin.shouldSkipCacheWrite(ctx) {
		t.Fatal("expected CacheNoStoreKey=true to skip the cache write")
	}
}

func TestShouldSkipCacheWrite_DefaultIsFalse(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	if plugin.shouldSkipCacheWrite(newBaseTestContext()) {
		t.Fatal("expected default context to allow the cache write")
	}
}

// -----------------------------------------------------------------------------
// Init validation
// -----------------------------------------------------------------------------

func TestInit_RejectsNilConfig(t *testing.T) {
	if _, err := Init(context.Background(), nil, bifrost.NewDefaultLogger(schemas.LogLevelError), newObservableStore()); err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestInit_RejectsNilStore(t *testing.T) {
	cfg := &Config{Provider: schemas.OpenAI, EmbeddingModel: "text-embedding-3-small", Dimension: 1536}
	if _, err := Init(context.Background(), cfg, bifrost.NewDefaultLogger(schemas.LogLevelError), nil); err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestInit_RejectsNegativeDimension(t *testing.T) {
	cfg := &Config{Dimension: -1}
	if _, err := Init(context.Background(), cfg, bifrost.NewDefaultLogger(schemas.LogLevelError), newObservableStore()); err == nil || !strings.Contains(err.Error(), "dimension") {
		t.Fatalf("expected dimension error, got %v", err)
	}
}

func TestInit_RejectsZeroDimensionWithProvider(t *testing.T) {
	cfg := &Config{Provider: schemas.OpenAI, EmbeddingModel: "text-embedding-3-small", Dimension: 0}
	if _, err := Init(context.Background(), cfg, bifrost.NewDefaultLogger(schemas.LogLevelError), newObservableStore()); err == nil || !strings.Contains(err.Error(), "dimension") {
		t.Fatalf("expected dimension error when provider set with zero dimension, got %v", err)
	}
}

func TestInit_AllowsDirectOnlyMode(t *testing.T) {
	// Provider="" + Dimension=1 is the documented direct-only mode.
	cfg := &Config{Dimension: 1}
	plugin, err := Init(context.Background(), cfg, bifrost.NewDefaultLogger(schemas.LogLevelError), newObservableStore())
	if err != nil {
		t.Fatalf("expected direct-only mode to init successfully, got %v", err)
	}
	if plugin == nil {
		t.Fatal("expected non-nil plugin in direct-only mode")
	}
	_ = plugin.Cleanup()
}

// -----------------------------------------------------------------------------
// PreLLMHook fallback when embedding executor missing
// -----------------------------------------------------------------------------

func TestPreLLMHook_FallsBackToDirectWhenExecutorMissing(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	// Intentionally do NOT set plugin.embeddingRequestExecutor.

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("hello", 0.7, 50),
	}
	ctx := CreateContextWithCacheKey(t, "")

	// PreLLMHook should not error, should not panic, and direct search should
	// still populate state.DirectCacheID.
	_, sc, err := plugin.PreLLMHook(ctx, req)
	if err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}
	if sc != nil {
		t.Fatalf("expected miss (empty store), got short-circuit %+v", sc)
	}

	requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	state := plugin.getCacheState(requestID)
	if state == nil || state.DirectCacheID == "" {
		t.Fatal("expected DirectCacheID populated even without embedding executor")
	}
	if state.Embeddings != nil {
		t.Fatalf("expected no embedding generated when executor missing, got %v", state.Embeddings)
	}
}

// -----------------------------------------------------------------------------
// Expired-entry full lifecycle
// -----------------------------------------------------------------------------

func TestExpiredEntry_DetectedAndDeleted(t *testing.T) {
	store := newObservableStore()
	plugin := newTestPlugin(t, store)

	// Plant an already-expired entry under a deterministic ID.
	expiredID := "expired-id-1"
	chunkJSON, _ := json.Marshal(&schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{},
	})
	store.chunks[expiredID] = vectorstore.SearchResult{
		ID: expiredID,
		Properties: map[string]interface{}{
			"response":   string(chunkJSON),
			"expires_at": time.Now().Add(-1 * time.Minute).Unix(),
		},
	}

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("hi", 0.7, 50),
	}
	ctx := newBaseTestContext()
	state := &cacheState{}

	sc, err := plugin.buildResponseFromResult(
		ctx, state, req,
		store.chunks[expiredID],
		CacheTypeDirect, nil, nil,
	)
	if err != nil {
		t.Fatalf("buildResponseFromResult failed: %v", err)
	}
	if sc != nil {
		t.Fatal("expected expired entry to surface as a miss (nil short-circuit)")
	}

	// The async delete is tracked on writersWg, so this drain must observe it.
	plugin.WaitForPendingOperations()

	store.mu.Lock()
	defer store.mu.Unlock()
	found := false
	for _, id := range store.deleteIDs {
		if id == expiredID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected expired entry %q to be deleted, got delete log %v", expiredID, store.deleteIDs)
	}
}

// -----------------------------------------------------------------------------
// WebSocketResponsesRequest support
// -----------------------------------------------------------------------------

func TestIsSemanticCacheSupportedRequestType_WebSocket(t *testing.T) {
	if !isSemanticCacheSupportedRequestType(schemas.WebSocketResponsesRequest) {
		t.Fatal("WebSocketResponsesRequest should be supported")
	}
}

// -----------------------------------------------------------------------------
// UnmarshalJSON rejection paths
// -----------------------------------------------------------------------------

func TestUnmarshalJSON_RejectsUnsupportedTTLType(t *testing.T) {
	var c Config
	if err := c.UnmarshalJSON([]byte(`{"provider":"openai","ttl":true}`)); err == nil {
		t.Fatal("expected error for boolean TTL")
	}
}

func TestUnmarshalJSON_RejectsNegativeTTL(t *testing.T) {
	var c Config
	if err := c.UnmarshalJSON([]byte(`{"provider":"openai","ttl":-5}`)); err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("expected non-negative TTL error, got %v", err)
	}
}

func TestUnmarshalJSON_RejectsMalformedJSON(t *testing.T) {
	var c Config
	if err := c.UnmarshalJSON([]byte(`{not valid json`)); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestUnmarshalJSON_RejectsBadDurationString(t *testing.T) {
	var c Config
	if err := c.UnmarshalJSON([]byte(`{"provider":"openai","ttl":"forever"}`)); err == nil {
		t.Fatal("expected error for unparseable duration string")
	}
}

// -----------------------------------------------------------------------------
// Stream replay cancellation variants
// -----------------------------------------------------------------------------

func TestStreamReplay_CancelImmediately(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	chunk := `{"chat_response":{"choices":[]}}`
	streamArray := []string{chunk, chunk, chunk}

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionStreamRequest,
		ChatRequest: CreateBasicChatRequest("hi", 0.7, 50),
	}
	ctx := newBaseTestContext()
	state := &cacheState{}

	sc, err := plugin.buildStreamingResponseFromResult(
		ctx, state, req,
		vectorstore.SearchResult{ID: "stream-1"},
		streamArray, CacheTypeSemantic, nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("buildStreamingResponseFromResult failed: %v", err)
	}
	ctx.Cancel() // cancel before reading any chunks

	// Channel must close within a short window.
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-sc.Stream:
			if !ok {
				return // channel closed cleanly
			}
		case <-timeout:
			t.Fatal("replay goroutine did not exit after immediate cancel")
		}
	}
}

func TestStreamReplay_FullDrain(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	chunk := `{"chat_response":{"choices":[]}}`
	streamArray := []string{chunk, chunk, chunk}

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionStreamRequest,
		ChatRequest: CreateBasicChatRequest("hi", 0.7, 50),
	}
	ctx := newBaseTestContext()
	state := &cacheState{}

	sc, err := plugin.buildStreamingResponseFromResult(
		ctx, state, req,
		vectorstore.SearchResult{ID: "stream-2"},
		streamArray, CacheTypeSemantic, nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("buildStreamingResponseFromResult failed: %v", err)
	}

	count := 0
	for chunk := range sc.Stream {
		if chunk == nil {
			t.Fatal("received nil chunk")
		}
		count++
	}
	if count != len(streamArray) {
		t.Fatalf("expected %d chunks, got %d", len(streamArray), count)
	}
}

// -----------------------------------------------------------------------------
// Plugin-log emission on failure paths (ctx.Log)
// -----------------------------------------------------------------------------

// scopedTestContext returns a plugin-scoped BifrostContext so ctx.Log entries
// land on the per-request log store and can be inspected via GetPluginLogs.
// In production the framework wraps every plugin hook this way.
func scopedTestContext(t testing.TB, suffix string) *schemas.BifrostContext {
	t.Helper()
	root := CreateContextWithCacheKey(t, suffix)
	name := PluginName
	return root.WithPluginScope(&name)
}

func TestPreLLMHook_EmitsPluginLogOnEmbeddingFailure(t *testing.T) {
	store := newObservableStore()
	plugin := newTestPlugin(t, store)
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "rate limit exceeded"}}
	})

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("test prompt", 0.7, 50),
	}
	ctx := scopedTestContext(t, "")

	if _, _, err := plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}

	logs := ctx.GetPluginLogs()
	if len(logs) == 0 {
		t.Fatal("expected at least one plugin log entry on embedding failure, got none")
	}
	var found bool
	for _, l := range logs {
		if l.PluginName != PluginName {
			continue
		}
		if strings.Contains(l.Message, "semantic search skipped") && strings.Contains(l.Message, "rate limit") {
			if l.Level != schemas.LogLevelWarn {
				t.Errorf("expected Warn level for embedding failure, got %s", l.Level)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a Warn plugin log mentioning semantic search skipped + the upstream error, got %+v", logs)
	}
}

// pluginLogContains is a small assertion helper: returns true if any log
// entry from PluginName matches the substring at the given level (or any
// level if level is "").
func pluginLogContains(logs []schemas.PluginLogEntry, level schemas.LogLevel, substr string) bool {
	for _, l := range logs {
		if l.PluginName != PluginName {
			continue
		}
		if level != "" && l.Level != level {
			continue
		}
		if strings.Contains(l.Message, substr) {
			return true
		}
	}
	return false
}

func TestPreLLMHook_NoDebugLogsOnFlow(t *testing.T) {
	// We deliberately do not emit Debug-level plugin logs for normal cache
	// flow (hit/miss). cache_debug already conveys that. Only Warn-level
	// failure logs should appear on the response.
	store := newObservableStore()
	plugin := newTestPlugin(t, store)

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("first request", 0.7, 50),
	}
	ctx := scopedTestContext(t, "")
	if _, _, err := plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}

	logs := ctx.GetPluginLogs()
	for _, l := range logs {
		if l.PluginName != PluginName {
			continue
		}
		if l.Level == schemas.LogLevelDebug {
			t.Fatalf("expected no Debug plugin logs on normal flow, got %+v", l)
		}
	}
}

func TestResolveCacheTypes_EmitsPluginLogOnInvalidValue(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	ctx := scopedTestContext(t, "")
	ctx.SetValue(CacheTypeKey, "not-a-cache-type") // wrong type

	plugin.resolveCacheTypes(ctx)

	logs := ctx.GetPluginLogs()
	var found bool
	for _, l := range logs {
		if l.PluginName == PluginName && strings.Contains(l.Message, "CacheTypeKey is not a CacheType") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected plugin log warning about invalid CacheTypeKey, got %+v", logs)
	}
}

// -----------------------------------------------------------------------------
// generateEmbedding handles all EmbeddingStruct representations
// -----------------------------------------------------------------------------

func TestGenerateEmbedding_AcceptsInt8Array(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{
			Data: []schemas.EmbeddingData{{
				Embedding: schemas.EmbeddingStruct{
					EmbeddingInt8Array: []int8{-128, -1, 0, 1, 127},
				},
			}},
		}, nil
	})

	ctx := scopedTestContext(t, "")
	emb, _, err := plugin.generateEmbedding(ctx, "anything")
	if err != nil {
		t.Fatalf("generateEmbedding failed for int8 input: %v", err)
	}
	want := []float32{-128, -1, 0, 1, 127}
	if !reflect.DeepEqual(emb, want) {
		t.Fatalf("int8 → float32 conversion: want %v, got %v", want, emb)
	}
}

func TestGenerateEmbedding_AcceptsInt32Array(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{
			Data: []schemas.EmbeddingData{{
				Embedding: schemas.EmbeddingStruct{
					EmbeddingInt32Array: []int32{0, 100000, -100000},
				},
			}},
		}, nil
	})

	ctx := scopedTestContext(t, "")
	emb, _, err := plugin.generateEmbedding(ctx, "anything")
	if err != nil {
		t.Fatalf("generateEmbedding failed for int32 input: %v", err)
	}
	want := []float32{0, 100000, -100000}
	if !reflect.DeepEqual(emb, want) {
		t.Fatalf("int32 → float32 conversion: want %v, got %v", want, emb)
	}
}

// -----------------------------------------------------------------------------
// Concurrent PreLLMHook on same requestID — last writer wins, no panic
// -----------------------------------------------------------------------------

func TestPreLLMHook_ConcurrentSameRequestID(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("hi", 0.7, 50),
	}

	requestID := "shared-request-id"
	const N = 8
	var wg sync.WaitGroup
	var panics atomic.Int32
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panics.Add(1)
				}
			}()
			ctx := newBaseTestContext()
			ctx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
			ctx.SetValue(CacheKey, keyForTest(t, ""))
			_, _, _ = plugin.PreLLMHook(ctx, req)
		}()
	}
	wg.Wait()

	if panics.Load() != 0 {
		t.Fatalf("expected zero panics under concurrent PreLLMHook, got %d", panics.Load())
	}
	// State for the shared requestID should exist (one of them won).
	if state := plugin.getCacheState(requestID); state == nil {
		t.Fatal("expected cache state to exist after concurrent PreLLMHook")
	}
}
