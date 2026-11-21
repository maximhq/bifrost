package bifrost

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// turnLogTracer records attached logs separately for each unit's trace.
type turnLogTracer struct {
	schemas.NoOpTracer
	mu   sync.Mutex
	logs map[string][]schemas.PluginLogEntry
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
							postCtx.SetValue(key, value)
						}
						_, postErr := hooks.PostHookRunner(postCtx, &schemas.BifrostResponse{
							ResponsesResponse: &schemas.BifrostResponsesResponse{Model: "test-model"},
						}, nil)
						hooks.Cleanup()
						if postErr != nil {
							t.Fatal(postErr)
						}
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
	client := newRequestIdentityClient(t, context.Background(), &turnLogTracer{logs: make(map[string][]schemas.PluginLogEntry)}, plugin)
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
	assert.NotEmpty(t, plugin.requestID)
	assert.NotEqual(t, turnRequestID, plugin.requestID)
	assert.NotEqual(t, turnTraceID, plugin.traceID)
}

func TestSDKRequestIdentityWithTracedParent(t *testing.T) {
	type callerKey struct{}
	parent := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	parent.SetValue(schemas.BifrostContextKeyRequestID, "init-request")
	parent.SetValue(schemas.BifrostContextKeyTraceID, "init-trace")
	parent.SetValue(callerKey{}, "instance default")
	plugin := &requestIdentityPlugin{}
	client := newRequestIdentityClient(t, parent, &turnLogTracer{logs: make(map[string][]schemas.PluginLogEntry)}, plugin)
	client.ctx.SetValue(schemas.BifrostContextKeyTraceID, "instance-trace")
	client.ctx.SetValue(callerKey{}, "instance service write")
	seenRequests := map[string]bool{"init-request": true}
	seenTraces := map[string]bool{"init-trace": true, "instance-trace": true}
	for range 2 {
		stream, err := client.ChatCompletionStreamRequest(nil, chatRequest())
		require.Nil(t, err)
		require.Equal(t, 1, drainStream(stream))
		require.NotEmpty(t, plugin.requestID)
		require.NotEmpty(t, plugin.traceID)
		assert.False(t, seenRequests[plugin.requestID], "reused request ID %s", plugin.requestID)
		assert.False(t, seenTraces[plugin.traceID], "reused trace ID %s", plugin.traceID)
		seenRequests[plugin.requestID], seenTraces[plugin.traceID] = true, true
		assert.Equal(t, plugin.traceID, plugin.root.Value(schemas.BifrostContextKeyExportTraceID))
		assert.Equal(t, "instance default", plugin.root.Value(callerKey{}))
	}
	assert.Equal(t, "init-request", parent.Value(schemas.BifrostContextKeyRequestID))
	assert.Equal(t, "init-trace", parent.Value(schemas.BifrostContextKeyTraceID))
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
