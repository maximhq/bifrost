package bifrost

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// turnLogTracer records attached logs separately for each unit's trace.
type turnLogTracer struct {
	schemas.NoOpTracer
	mu          sync.Mutex
	logs        map[string][]schemas.PluginLogEntry
	lastFlushed string
}

// CreateTrace allocates a distinct trace for each unit.
func (t *turnLogTracer) CreateTrace(_ string, _ ...string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	id := fmt.Sprintf("turn-%d", len(t.logs))
	t.logs[id] = nil
	return id
}

// AttachPluginLogs appends each phase's logs to its trace.
func (t *turnLogTracer) AttachPluginLogs(id string, logs []schemas.PluginLogEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.logs[id] = append(t.logs[id], logs...)
}

func (t *turnLogTracer) CompleteAndFlushTrace(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastFlushed = id
}

// turnLogPlugin emits diagnostics from both phases, including refused units.
type turnLogPlugin struct {
	catalogProbePlugin
	refuse bool
}

// PreLLMHook records admission and can refuse the unit before provider work.
func (p *turnLogPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	ctx.Log(schemas.LogLevelInfo, "pre")
	if p.refuse {
		return req, &schemas.LLMPluginShortCircuit{Error: newBifrostErrorFromMsg("refused")}, nil
	}
	return req, nil, nil
}

// PostLLMHook records settlement while preserving the response and error.
func (p *turnLogPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	ctx.Log(schemas.LogLevelInfo, "post")
	return resp, err, nil
}

// TestRealtimeTurnPluginLogsSurviveContextHandoff checks ordered logs and isolation across units.
func TestRealtimeTurnPluginLogsSurviveContextHandoff(t *testing.T) {
	for _, requestType := range []schemas.RequestType{schemas.RealtimeRequest, schemas.LiveRequest} {
		for _, refuse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/refuse=%t", requestType, refuse), func(t *testing.T) {
				tracer := &turnLogTracer{logs: make(map[string][]schemas.PluginLogEntry)}
				plugin := &turnLogPlugin{refuse: refuse}
				client, err := Init(context.Background(), schemas.BifrostConfig{
					Account: NewMockAccount(), Logger: NewNoOpLogger(), Tracer: tracer,
					LLMPlugins: []schemas.LLMPlugin{plugin},
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(client.Shutdown)
				for unit := 0; unit < 2; unit++ {
					preCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
					hooks, preErr := client.RunRealtimeTurnPreHooks(preCtx, &schemas.BifrostRequest{
						RequestType:      requestType,
						ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "test-model"},
					})
					if refuse {
						if preErr == nil || hooks != nil {
							t.Fatal("expected refusal")
						}
					} else {
						if preErr != nil || hooks == nil {
							t.Fatalf("admission failed: %v", preErr)
						}
						// Both transports copy values into a fresh context for settlement.
						postCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
						for key, value := range preCtx.GetUserValues() {
							if key != schemas.BifrostContextKeyRequestStartTime {
								postCtx.SetValue(key, value)
							}
						}
						// The runner carries the original start time through settlement.
						startedAt := preCtx.Value(schemas.BifrostContextKeyRequestStartTime)
						require.IsType(t, time.Time{}, startedAt)
						_, postErr := hooks.PostHookRunner(postCtx, &schemas.BifrostResponse{
							ResponsesResponse: &schemas.BifrostResponsesResponse{Model: "test-model"},
						}, nil)
						hooks.Cleanup()
						if postErr != nil {
							t.Fatal(postErr)
						}
						assert.Equal(t, startedAt, postCtx.Value(schemas.BifrostContextKeyRequestStartTime))
					}
					traceID := preCtx.Value(schemas.BifrostContextKeyTraceID).(string)
					logs := tracer.logs[traceID]
					if len(logs) != 2 || logs[0].Message != "pre" || logs[1].Message != "post" {
						t.Fatalf("unit %d logs = %+v, want pre then post exactly once", unit, logs)
					}
					if preCtx.HasPluginLogs() {
						t.Fatal("pre-hook context retained undrained logs")
					}
				}
			})
		}
	}
}

type requestIdentityPlugin struct {
	cacheHitPlugin
	root      *schemas.BifrostContext
	postRoot  *schemas.BifrostContext
	requestID string
	traceID   string
}

func (p *requestIdentityPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	p.root = ctx.Root()
	p.requestID, _ = ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	p.traceID, _ = ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	if req.RequestType == schemas.RealtimeRequest {
		return req, nil, nil
	}
	return p.cacheHitPlugin.PreLLMHook(ctx, req)
}

func (p *requestIdentityPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	p.postRoot = ctx.Root()
	return resp, err, nil
}

func newRequestIdentityClient(t testing.TB, parent context.Context, tracer schemas.Tracer, plugin *requestIdentityPlugin) *Bifrost {
	t.Helper()
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, 1)
	client, err := Init(parent, schemas.BifrostConfig{
		Account: account, Logger: NewNoOpLogger(), Tracer: tracer,
		LLMPlugins: []schemas.LLMPlugin{plugin},
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
	return client
}

func callerIdentityContext() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "caller-request")
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "caller-trace")
	return ctx
}

func TestSDKRequestIdentity(t *testing.T) {
	plugin := &requestIdentityPlugin{}
	tracer := &turnLogTracer{logs: make(map[string][]schemas.PluginLogEntry)}
	client := newRequestIdentityClient(t, context.Background(), tracer, plugin)
	turn := &schemas.BifrostRequest{
		RequestType:      schemas.RealtimeRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: testChatModel},
	}
	hooks, turnErr := client.RunRealtimeTurnPreHooks(nil, turn)
	require.Nil(t, turnErr)
	hooks.Cleanup()
	turnCtx, turnRequestID, turnTraceID := plugin.root, plugin.requestID, plugin.traceID
	require.NotEmpty(t, turnRequestID)
	require.NotEmpty(t, turnTraceID)

	stream, streamErr := client.ChatCompletionStreamRequest(nil, chatRequest())
	require.Nil(t, streamErr)
	require.Equal(t, 1, drainStream(stream))
	require.NotEmpty(t, plugin.requestID)
	require.NotEmpty(t, plugin.traceID)
	assert.NotEqual(t, turnRequestID, plugin.requestID, "chat stream reused the realtime request ID")
	assert.NotEqual(t, turnTraceID, plugin.traceID, "chat stream reused the realtime trace ID")
	assert.ErrorIs(t, turnCtx.Err(), context.Canceled)
	assert.Nil(t, client.ctx.Value(schemas.BifrostContextKeyRequestID))
	assert.Nil(t, client.ctx.Value(schemas.BifrostContextKeyTraceID))

	caller := callerIdentityContext()
	stream, streamErr = client.ChatCompletionStreamRequest(caller, chatRequest())
	require.Nil(t, streamErr)
	require.Equal(t, 1, drainStream(stream))
	assert.Equal(t, "caller-request", plugin.requestID)
	assert.Equal(t, "caller-trace", plugin.traceID)
	assert.NoError(t, caller.Err())

	_, unaryErr := client.ChatCompletionRequest(nil, chatRequest())
	require.Nil(t, unaryErr)
	require.NotEmpty(t, plugin.traceID)
	assert.NotEqual(t, turnTraceID, plugin.traceID)
	unaryTraceID := plugin.traceID
	assert.Equal(t, unaryTraceID, tracer.lastFlushed)
	_, unaryErr = client.ChatCompletionRequest(caller, chatRequest())
	require.Nil(t, unaryErr)
	assert.Equal(t, "caller-trace", plugin.traceID)
	assert.Equal(t, unaryTraceID, tracer.lastFlushed, "the caller owns its trace")
}

func TestSDKRequestIdentityWithTracedParent(t *testing.T) {
	type callerKey struct{}
	parent := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	parent.SetValue(schemas.BifrostContextKeyRequestID, "init-request")
	parent.SetValue(schemas.BifrostContextKeyFallbackRequestID, "init-fallback")
	parent.SetValue(schemas.BifrostContextKeyTraceID, "init-trace")
	parent.SetValue(callerKey{}, "instance default")
	plugin := &requestIdentityPlugin{}
	client := newRequestIdentityClient(t, parent, &turnLogTracer{logs: make(map[string][]schemas.PluginLogEntry)}, plugin)
	client.ctx.SetValue(schemas.BifrostContextKeyTraceID, "instance-trace")
	client.ctx.SetValue(callerKey{}, "instance service write")
	seenRequests := map[string]bool{"init-request": true}
	seenTraces := map[string]bool{"init-trace": true, "instance-trace": true}
	checkIdentity := func() {
		t.Helper()
		require.NotEmpty(t, plugin.requestID)
		require.NotEmpty(t, plugin.traceID)
		assert.False(t, seenRequests[plugin.requestID], "reused request ID %s", plugin.requestID)
		assert.False(t, seenTraces[plugin.traceID], "reused trace ID %s", plugin.traceID)
		seenRequests[plugin.requestID], seenTraces[plugin.traceID] = true, true
		assert.Equal(t, plugin.traceID, plugin.root.Value(schemas.BifrostContextKeyExportTraceID))
		assert.Nil(t, plugin.root.Value(schemas.BifrostContextKeyFallbackRequestID))
		assert.Nil(t, plugin.postRoot.Value(schemas.BifrostContextKeyFallbackRequestID))
		assert.Equal(t, "instance default", plugin.root.Value(callerKey{}))
	}
	for range 2 {
		stream, err := client.ChatCompletionStreamRequest(nil, chatRequest())
		require.Nil(t, err)
		require.Equal(t, 1, drainStream(stream))
		checkIdentity()
		_, err = client.ChatCompletionRequest(nil, chatRequest())
		require.Nil(t, err)
		checkIdentity()
	}
	assert.Equal(t, "init-request", parent.Value(schemas.BifrostContextKeyRequestID))
	assert.Equal(t, "init-trace", parent.Value(schemas.BifrostContextKeyTraceID))

	_, unaryErr := client.ChatCompletionRequest(parent, chatRequest())
	require.Nil(t, unaryErr)
	assert.Equal(t, "init-request", plugin.requestID)
	assert.Equal(t, "init-trace", plugin.traceID)
	assert.Equal(t, "init-fallback", plugin.postRoot.Value(schemas.BifrostContextKeyFallbackRequestID))
}

func TestSDKUnaryTraceWaitsForAbandonedWorker(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	tracer := &countingTracer{}
	account := mockAccountFor("http://127.0.0.1")
	client, err := Init(parent, schemas.BifrostConfig{Account: account, Logger: NewNoOpLogger(), Tracer: tracer})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)

	// Let the SDK return on cancellation while the provider still owns the response.
	upstream := &abandonedUpstreamProvider{
		Provider: client.GetProviderByKey(schemas.OpenAI),
		started:  make(chan struct{}, 1),
		release:  make(chan struct{}, 1),
	}
	cfg, err := account.GetConfigForProvider(schemas.OpenAI)
	require.NoError(t, err)
	queue := &ProviderQueue{queue: make(chan *ChannelMessage, 1), done: make(chan struct{})}
	original, _ := client.requestQueues.Swap(schemas.OpenAI, queue)
	var workers sync.WaitGroup
	workers.Add(1)
	go client.requestWorker(upstream, cfg, queue, &workers)
	t.Cleanup(func() {
		close(upstream.release)
		queue.signalClosing()
		workers.Wait()
		client.requestQueues.Store(schemas.OpenAI, original)
	})

	returned := make(chan *schemas.BifrostError, 1)
	go func() {
		_, err := client.ChatCompletionRequest(nil, chatRequest())
		returned <- err
	}()
	select {
	case <-upstream.started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach the provider")
	}
	cancel()
	select {
	case err := <-returned:
		require.NotNil(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled SDK call did not return")
	}
	require.Zero(t, tracer.flushed.Load())
	upstream.release <- struct{}{}
	queue.signalClosing()
	workers.Wait()
	require.EqualValues(t, 1, tracer.flushed.Load())
}

func TestSDKOverlappingHookContexts(t *testing.T) {
	plugin := &requestIdentityPlugin{}
	client := newRequestIdentityClient(t, context.Background(), &turnLogTracer{logs: make(map[string][]schemas.PluginLogEntry)}, plugin)
	req := &schemas.BifrostRequest{
		RequestType:      schemas.RealtimeRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: testChatModel},
	}
	ws, err := client.RunStreamPreHooks(nil, req)
	require.Nil(t, err)
	wsCtx := plugin.root
	turn, err := client.RunRealtimeTurnPreHooks(nil, req)
	require.Nil(t, err)
	turnCtx := plugin.root
	defer turn.Cleanup()
	assert.NotEqual(t, wsCtx.Value(schemas.BifrostContextKeyTraceID), plugin.traceID)

	resp := &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Model: testChatModel}}
	wsStartedAt := wsCtx.Value(schemas.BifrostContextKeyRequestStartTime)
	require.IsType(t, time.Time{}, wsStartedAt)
	postCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, err = ws.PostHookRunner(postCtx, resp, nil)
	require.Nil(t, err)
	assert.Same(t, postCtx, plugin.postRoot)
	assert.Equal(t, wsStartedAt, postCtx.Value(schemas.BifrostContextKeyRequestStartTime))

	_, err = ws.PostHookRunner(nil, resp, nil)
	ws.Cleanup()
	require.Nil(t, err)
	assert.Same(t, wsCtx, plugin.postRoot)
	assert.ErrorIs(t, wsCtx.Err(), context.Canceled)
	assert.NoError(t, turnCtx.Err())
	_, err = turn.PostHookRunner(nil, resp, nil)
	require.Nil(t, err)
	assert.Same(t, turnCtx, plugin.postRoot)
}

type modelListTraceRecorder struct{ streamSpanRecorder }

func (tr *modelListTraceRecorder) CreateTrace(_ string, _ ...string) string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	clear(tr.spans)
	return "model-list-trace"
}

func newModelListIdentityClient(t testing.TB, tracer schemas.Tracer) *Bifrost {
	t.Helper()
	server := httptest.NewServer(openAIStyleModelsHandler("model-a"))
	t.Cleanup(server.Close)
	account := NewMockAccount()
	for _, provider := range []schemas.ModelProvider{schemas.OpenAI, schemas.Groq} {
		account.AddProviderWithBaseURL(provider, 1, 4, server.URL)
		account.SetKeysForProvider(provider, wildcardKey(provider))
	}
	client, err := Init(context.Background(), schemas.BifrostConfig{Account: account, Logger: NewNoOpLogger(), Tracer: tracer})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
	return client
}

func TestSDKModelListTraceEnclosesProviderPolls(t *testing.T) {
	tracer := &modelListTraceRecorder{streamSpanRecorder{spans: make(map[string]*streamSpanRecord)}}
	client := newModelListIdentityClient(t, tracer)
	models, err := client.ListAllModels(nil, nil)
	require.Nil(t, err)
	require.Len(t, models.Data, 2)

	rootID, root := tracer.byName("list-all-models")
	require.NotNil(t, root)
	assert.Empty(t, root.parent)
	assert.False(t, root.end.IsZero())
	polls := 0
	for _, span := range tracer.spans {
		if span.name == "handle-setup" {
			polls++
			assert.Equal(t, rootID, span.parent)
			assert.False(t, span.start.Before(root.start))
		}
		assert.False(t, span.end.After(root.end))
	}
	assert.Equal(t, 2, polls)
}

func BenchmarkSDKListAllModels(b *testing.B) {
	for _, tc := range []struct {
		name   string
		tracer schemas.Tracer
	}{
		{"traced", &modelListTraceRecorder{streamSpanRecorder{spans: make(map[string]*streamSpanRecord)}}},
		{"untraced", nil},
	} {
		b.Run(tc.name, func(b *testing.B) {
			client := newModelListIdentityClient(b, tc.tracer)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := client.ListAllModels(nil, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSDKRequestIdentity(b *testing.B) {
	server := httptest.NewServer(sseHandler(`{"id":"chunk","choices":[{"index":0,"delta":{"content":"hello"}}]}`))
	b.Cleanup(server.Close)
	providerClient, err := Init(context.Background(), schemas.BifrostConfig{
		Account: mockAccountFor(server.URL), Logger: NewNoOpLogger(), Tracer: &countingTracer{},
	})
	require.NoError(b, err)
	b.Cleanup(providerClient.Shutdown)
	for _, contextName := range []string{"nil", "explicit"} {
		b.Run(contextName, func(b *testing.B) {
			client := newRequestIdentityClient(b, context.Background(), &countingTracer{}, &requestIdentityPlugin{})
			var ctx *schemas.BifrostContext
			if contextName == "explicit" {
				ctx = callerIdentityContext()
			}
			b.Run("chat-unary", func(b *testing.B) {
				req := chatRequest()
				for b.Loop() {
					if _, err := client.ChatCompletionRequest(ctx, req); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("realtime", func(b *testing.B) {
				req := &schemas.BifrostRequest{
					RequestType:      schemas.RealtimeRequest,
					ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: testChatModel},
				}
				for b.Loop() {
					hooks, err := client.RunRealtimeTurnPreHooks(ctx, req)
					if err != nil {
						b.Fatal(err)
					}
					hooks.Cleanup()
				}
			})
			for _, path := range []struct {
				name    string
				client  *Bifrost
				content string
			}{{"chat-stream", client, ""}, {"provider-stream", providerClient, "hello"}} {
				b.Run(path.name, func(b *testing.B) {
					req := chatRequest()
					for b.Loop() {
						requestCtx := ctx
						if ctx != nil && path.client == providerClient {
							// A real upstream connection has its own teardown state.
							requestCtx = callerIdentityContext()
						}
						stream, err := path.client.ChatCompletionStreamRequest(requestCtx, req)
						if err != nil {
							b.Fatal(err)
						}
						content, errors := drainChatStream(stream)
						if content != path.content || len(errors) > 0 {
							b.Fatalf("got content %q and stream errors %v", content, errors)
						}
					}
				})
			}
		})
	}
}
