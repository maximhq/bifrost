package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// No provider is configured: every request must be answered by the native plugin.
// Reuse the offline account's config but make an accidental upstream call fail closed.
type inferenceStreamAccount struct{ wsSpanTestAccount }

func (inferenceStreamAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return nil, nil
}
func (inferenceStreamAccount) GetKeysForProvider(context.Context, schemas.ModelProvider) ([]schemas.Key, error) {
	return nil, fmt.Errorf("unexpected provider call in offline stream regression")
}

type inferenceFinalityObserver struct {
	mu     sync.Mutex
	finals []bool
}

func (*inferenceFinalityObserver) GetName() string { return "inference-finality-observer" }
func (*inferenceFinalityObserver) Cleanup() error  { return nil }
func (*inferenceFinalityObserver) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (*inferenceFinalityObserver) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}
func (p *inferenceFinalityObserver) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finals = append(p.finals, bifrost.IsFinalChunk(ctx))
	return resp, err, nil
}

type inferenceStreamProducer struct {
	inferenceFinalityObserver
	explicit bool
	seen     int
}

func (*inferenceStreamProducer) GetName() string { return "inference-stream-producer" }
func (p *inferenceStreamProducer) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	stream := make(chan *schemas.BifrostStreamChunk, 3)
	for i, text := range []string{"A", "B", "C"} {
		chunk := inferenceContentChunk(i, text)
		if p.explicit {
			chunk.Finality = schemas.StreamChunkFinalityNonFinal
			if i == 2 {
				chunk.Finality = schemas.StreamChunkFinalityFinal
			}
		}
		stream <- chunk
	}
	close(stream)
	if p.explicit {
		// Reproduce a producer finishing before the consumer processes any chunks.
		// Explicit chunk metadata must override this premature shared root value.
		ctx.Root().SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	}
	return req, &schemas.LLMPluginShortCircuit{Stream: stream}, nil
}
func (p *inferenceStreamProducer) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if !p.explicit {
		// Legacy control: publish finality synchronously, before downstream post-hooks.
		p.seen++
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, p.seen == 3)
	}
	return resp, err, nil
}

// The real logger must receive the actual asynchronous trace export, not a
// fabricated Trace passed directly to Inject. The channel joins that export.
type inferenceTraceLogging struct {
	*logging.LoggerPlugin
	done      chan struct{}
	traceID   string
	rootEnd   time.Time
	openSpans []string
	err       error
}

func (p *inferenceTraceLogging) Inject(ctx context.Context, trace *schemas.Trace) error {
	defer close(p.done)
	// Export snapshots are pooled and released after Inject returns; retain scalars only.
	p.traceID = trace.InternalID
	if p.traceID == "" {
		p.traceID = trace.TraceID
	}
	if trace.RootSpan != nil {
		p.rootEnd = trace.RootSpan.EndTime
	}
	for _, span := range trace.Spans {
		if span.EndTime.IsZero() {
			p.openSpans = append(p.openSpans, span.Name)
		}
	}
	p.err = p.LoggerPlugin.Inject(ctx, trace)
	return p.err
}

func inferenceContentChunk(index int, text string) *schemas.BifrostStreamChunk {
	chunk := contentChunk(text)
	chunk.BifrostChatResponse.Model = "offline"
	chunk.BifrostChatResponse.ExtraFields.ChunkIndex = index
	if index == 2 {
		chunk.BifrostChatResponse.Choices[0].FinishReason = schemas.Ptr("stop")
	}
	return chunk
}

func inferenceStreamRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI, Model: "offline",
		Input:  []schemas.ChatMessage{{Role: "user", Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("offline")}}},
		Params: &schemas.ChatParameters{},
	}
}

func TestStreamingResponseFinalityLoggingAndTrace(t *testing.T) {
	for _, name := range []string{"explicit_premature_root", "synchronous_legacy_control", "direct_cache", "semantic_cache"} {
		t.Run(name, func(t *testing.T) {
			logger := &mockLogger{}
			SetLogger(logger)
			observer := &inferenceFinalityObserver{}
			var producer schemas.LLMPlugin = &inferenceStreamProducer{explicit: name == "explicit_premature_root"}
			var cache *semanticcache.Plugin
			var cacheType semanticcache.CacheType
			var vectors *inferenceCacheStore
			var embeddingCalls atomic.Int64
			if strings.HasSuffix(name, "_cache") {
				cacheType = semanticcache.CacheType(strings.TrimSuffix(name, "_cache"))
				vectors = &inferenceCacheStore{chunks: make(map[string]vectorstore.SearchResult)}
				plugin, err := semanticcache.Init(context.Background(), &semanticcache.Config{Dimension: 3, Provider: schemas.OpenAI, EmbeddingModel: "offline-embedding"}, logger, vectors)
				require.NoError(t, err)
				cache = plugin.(*semanticcache.Plugin)
				producer = cache
				t.Cleanup(func() { require.NoError(t, cache.Cleanup()) })
				cache.SetEmbeddingRequestExecutor(func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
					embeddingCalls.Add(1)
					return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{0.1, 0.2, 0.3}}}}, Usage: &schemas.BifrostLLMUsage{TotalTokens: 7}}, nil
				})
				seedInferenceCache(t, cache)
			}

			store, err := logstore.NewLogStore(context.Background(), &logstore.Config{
				Enabled: true, Type: logstore.LogStoreTypeSQLite,
				Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")},
			}, logger)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close(context.Background())) })
			lp, err := logging.Init(context.Background(), &logging.Config{}, logger, store, nil, nil, nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lp.Cleanup()) })
			ts := tracing.NewTraceStore(5*time.Minute, logger)
			t.Cleanup(ts.Stop)
			tracer := tracing.NewTracer(ts, nil, logger)
			t.Cleanup(tracer.Stop)
			export := &inferenceTraceLogging{LoggerPlugin: lp, done: make(chan struct{})}
			tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{export}, nil)
			client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
				Account: inferenceStreamAccount{}, Logger: logger, Tracer: tracer, InitialPoolSize: 2,
				LLMPlugins: []schemas.LLMPlugin{lp, observer, producer},
			})
			require.NoError(t, err)
			t.Cleanup(client.Shutdown)

			body := serveInferenceLoggedStream(t, client, tracer, cacheType, export.done)
			select {
			case <-export.done:
			case <-time.After(5 * time.Second):
				t.Fatal("HTTP trace was not flushed")
			}
			require.NoError(t, export.err)
			// Drain the real SQLite writer after the trace-fed log enqueue completes.
			require.NoError(t, lp.Cleanup())
			row, err := store.FindByID(context.Background(), "inference-finality")
			require.NoError(t, err)
			require.True(t, row.Stream)
			require.NotNil(t, row.OutputMessageParsed)
			require.NotNil(t, row.OutputMessageParsed.Content)
			require.NotNil(t, row.OutputMessageParsed.Content.ContentStr)
			assert.Equal(t, "ABC", *row.OutputMessageParsed.Content.ContentStr, "SQLite must retain all streamed content")

			observer.mu.Lock()
			finals := slices.Clone(observer.finals)
			observer.mu.Unlock()
			assert.Equal(t, []bool{false, false, true}, finals, "IsFinalChunk must identify exactly one final hook")

			var text strings.Builder
			var events int
			for _, line := range strings.Split(body, "\n") {
				if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
					continue
				}
				var chunk schemas.BifrostChatResponse
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk))
				require.Len(t, chunk.Choices, 1)
				require.NotNil(t, chunk.Choices[0].ChatStreamResponseChoice)
				require.NotNil(t, chunk.Choices[0].Delta)
				require.NotNil(t, chunk.Choices[0].Delta.Content)
				text.WriteString(*chunk.Choices[0].Delta.Content)
				events++
			}
			require.Equal(t, "ABC", text.String(), "HTTP client must receive all content in order")
			require.Equal(t, 3, events)
			require.Equal(t, 1, strings.Count(body, "data: [DONE]"))
			require.NotContains(t, body, "finality", "internal finality metadata must not leak to SSE")
			require.NotEmpty(t, export.traceID)
			require.False(t, export.rootEnd.IsZero(), "HTTP root span must end before export")
			require.Empty(t, export.openSpans, "all spans must end before export")
			require.Nil(t, ts.GetTrace(export.traceID), "flushed trace must be removed")
			if cache != nil {
				cache.WaitForPendingOperations()
				require.NotNil(t, row.CacheDebugParsed)
				require.True(t, row.CacheDebugParsed.CacheHit)
				wantCalls := int64(0)
				if cacheType == semanticcache.CacheTypeSemantic {
					wantCalls = 1
				}
				require.Equal(t, wantCalls, embeddingCalls.Load(), "exercise the intended cache lookup")
				require.Equal(t, wantCalls, vectors.searches.Load())
			}
		})
	}
}

// Exercise the actual completion handler and tracing middleware on an in-process
// HTTP connection; unlike serveStreamingResponse, this needs core/plugin hooks.
func serveInferenceLoggedStream(t *testing.T, client *bifrost.Bifrost, tracer *tracing.Tracer, cacheType semanticcache.CacheType, exported <-chan struct{}) string {
	t.Helper()
	h := &CompletionHandler{client: client, config: &lib.Config{}}
	handler := NewTracingMiddleware(tracer).Middleware()(func(fc *fasthttp.RequestCtx) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyRequestID, "inference-finality")
		for _, key := range []schemas.BifrostContextKey{schemas.BifrostContextKeyTraceID, schemas.BifrostContextKeySpanID} {
			ctx.SetValue(key, fc.UserValue(key))
		}
		if cacheType != "" {
			ctx.SetValue(semanticcache.CacheKey, "offline-cache")
			ctx.SetValue(semanticcache.CacheTypeKey, cacheType)
		}
		h.handleStreamingResponse(fc, ctx, schemas.ChatCompletionStreamRequest, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			return client.ChatCompletionStreamRequest(ctx, inferenceStreamRequest())
		}, ctx.Cancel)
		// No transport plugins are configured; publish their empty post-hook handoff.
		if slot, ok := fc.UserValue(schemas.BifrostContextKeyTransportPostHookCompleter).(*atomic.Value); ok {
			slot.Store(func() ([]schemas.PluginLogEntry, error) { return nil, nil })
		}
	})
	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = fasthttp.ServeConn(serverConn, handler)
	}()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
		<-done
	})
	require.NoError(t, clientConn.SetDeadline(time.Now().Add(10*time.Second)))
	_, err := io.WriteString(clientConn, "POST /v1/chat/completions HTTP/1.1\r\nHost: offline\r\nX-Request-ID: inference-finality\r\nConnection: close\r\n\r\n")
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	select {
	case <-exported:
		t.Fatal("trace flushed before HTTP stream drain")
	default:
	}
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

// Only the vector-store operations used by direct/semantic replay are mocked.
// Cache seeding, HTTP streaming, accumulation and SQLite use real implementations;
// the embedding executor above returns a deterministic offline vector.
type inferenceCacheStore struct {
	vectorstore.VectorStore
	mu       sync.Mutex
	chunks   map[string]vectorstore.SearchResult
	searches atomic.Int64
}

func (*inferenceCacheStore) CreateNamespace(context.Context, string, int, map[string]vectorstore.VectorStoreProperties) error {
	return nil
}
func (*inferenceCacheStore) RequiresVectors() bool { return false }
func (s *inferenceCacheStore) GetChunk(_ context.Context, _ string, id string) (vectorstore.SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chunk, ok := s.chunks[id]
	if !ok {
		return chunk, vectorstore.ErrNotFound
	}
	return chunk, nil
}
func (s *inferenceCacheStore) GetNearest(context.Context, string, []float32, []vectorstore.Query, []string, float64, int64) ([]vectorstore.SearchResult, error) {
	s.searches.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	var results []vectorstore.SearchResult
	for _, chunk := range s.chunks {
		chunk.Score = schemas.Ptr(1.0)
		results = append(results, chunk)
	}
	return results, nil
}
func (s *inferenceCacheStore) Add(_ context.Context, _ string, id string, _ []float32, properties map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunks[id] = vectorstore.SearchResult{ID: id, Properties: properties}
	return nil
}

func seedInferenceCache(t *testing.T, cache *semanticcache.Plugin) {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "seed")
	ctx.SetValue(semanticcache.CacheKey, "offline-cache")
	ctx.SetValue(semanticcache.CacheTypeKey, semanticcache.CacheTypeDirect)
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionStreamRequest, ChatRequest: inferenceStreamRequest()}
	_, shortCircuit, err := cache.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "seed must miss")
	for i, text := range []string{"A", "B", "C"} {
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, i == 2)
		resp := &schemas.BifrostResponse{ChatResponse: inferenceContentChunk(i, text).BifrostChatResponse}
		resp.PopulateExtraFields(schemas.ChatCompletionStreamRequest, schemas.OpenAI, "offline", "offline")
		_, _, err := cache.PostLLMHook(ctx, resp, nil)
		require.NoError(t, err)
	}
	cache.WaitForPendingOperations()
}
