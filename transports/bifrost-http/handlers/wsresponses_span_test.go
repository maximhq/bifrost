package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wsSpanTestAccount is a minimal offline Account exposing OpenAI with one keyed
// entry, so GetProviderByKey / SelectKeyForProviderRequestType resolve without
// any network call.
type wsSpanTestAccount struct{}

func (wsSpanTestAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (wsSpanTestAccount) GetKeysForProvider(_ context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	if p != schemas.OpenAI {
		return nil, nil
	}
	return []schemas.Key{{
		ID:     "test-key-1",
		Value:  *schemas.NewSecretVar("sk-test"),
		Models: schemas.WhiteList{"*"},
		Weight: 1.0,
	}}, nil
}

func (wsSpanTestAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if p != schemas.OpenAI {
		return nil, fmt.Errorf("unsupported provider %s", p)
	}
	return &schemas.ProviderConfig{
		NetworkConfig:            schemas.DefaultNetworkConfig,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}

// wsLLMSpanCapture is an ObservabilityPlugin that records whether the flushed
// trace contains an llm.call span and, if so, the provider/model/usage
// attributes carried on it. Attributes are read from the export snapshot, which
// is an isolated copy safe to iterate.
type wsLLMSpanCapture struct {
	once       sync.Once
	done       chan struct{}
	foundLLM   bool
	provider   any
	model      any
	total      any
	status     schemas.SpanStatus
	statusCode any
	errAttr    any
}

func (p *wsLLMSpanCapture) GetName() string { return "ws-llm-span-capture" }
func (p *wsLLMSpanCapture) Cleanup() error  { return nil }
func (p *wsLLMSpanCapture) Inject(_ context.Context, trace *schemas.Trace) error {
	defer p.once.Do(func() { close(p.done) })
	if trace == nil {
		return nil
	}
	for _, span := range trace.Spans {
		if span != nil && span.Kind == schemas.SpanKindLLMCall {
			p.foundLLM = true
			p.provider = span.Attributes[schemas.AttrProviderName]
			p.model = span.Attributes[schemas.AttrRequestModel]
			p.total = span.Attributes[schemas.AttrTotalTokens]
			p.status = span.Status
			p.statusCode = span.Attributes["status_code"]
			p.errAttr = span.Attributes["error"]
		}
	}
	return nil
}

func newWSTestUpgrader() websocket.Upgrader {
	return websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
}

// TestNativeWSUpstreamProducesLLMCallSpan asserts that a realtime/WebSocket
// Responses turn taking the native WS upstream path (tryNativeWSUpstream)
// produces an llm.call span carrying provider, model, and token usage, matching
// what every other request type exports to span-based observability connectors.
//
// Regression for issue #6265: the native WS path runs post-hooks and flushes the
// trace, but never starts a SpanKindLLMCall span, so span-derived exports record
// the turn as an unattributed row (no provider, model, or usage).
func TestNativeWSUpstreamProducesLLMCallSpan(t *testing.T) {
	SetLogger(bifrost.NewDefaultLogger(schemas.LogLevelError))

	// Fake OpenAI WS upstream: read the forwarded response.create event, then
	// reply with a terminal response.completed carrying usage.
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := newWSTestUpgrader()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
		completed := `{"type":"response.completed","sequence_number":1,"response":{"id":"resp_test","model":"gpt-realtime","usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}}`
		_ = c.WriteMessage(websocket.TextMessage, []byte(completed))
		// Hold the connection open briefly so the handler reads the terminal event
		// before the server tears the socket down.
		time.Sleep(300 * time.Millisecond)
	}))
	defer upstreamSrv.Close()

	// Client-facing sink: drains whatever the handler relays back to the client.
	clientSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := newWSTestUpgrader()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer clientSrv.Close()

	// Tracer wired with an observability sink that captures the flushed trace.
	store := tracing.NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := tracing.NewTracer(store, nil, nil)
	defer tracer.Stop()
	capture := &wsLLMSpanCapture{done: make(chan struct{})}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{capture}, nil)

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: wsSpanTestAccount{},
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
		Tracer:  tracer,
	})
	require.NoError(t, err)
	defer client.Shutdown()

	pool := bfws.NewPool(&schemas.WSPoolConfig{})
	defer pool.Close()

	h := &WSResponsesHandler{
		client:       client,
		config:       &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{schemas.OpenAI: {}}},
		handlerStore: testWSHandlerStore{},
		pool:         pool,
		sessions:     bfws.NewSessionManager(10),
	}

	// Pre-pin the upstream to the session so the native path reuses it (matching
	// provider + key) instead of dialing through the pool.
	upstreamURL := "ws" + strings.TrimPrefix(upstreamSrv.URL, "http")
	upConn, err := bfws.DialUpstream(upstreamURL, nil, schemas.OpenAI, "test-key-1", nil)
	require.NoError(t, err)

	clientURL := "ws" + strings.TrimPrefix(clientSrv.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(clientURL, nil)
	require.NoError(t, err)

	session := bfws.NewSession(clientConn)
	session.SetUpstream(upConn)
	defer session.Close()

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-realtime",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
	}
	rawEvent := []byte(`{"type":"response.create","model":"gpt-realtime","input":"hello"}`)

	handled := h.tryNativeWSUpstream(session, ctx, req, rawEvent)
	require.True(t, handled, "native WS path should have handled the turn")

	select {
	case <-capture.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the trace to flush")
	}

	require.True(t, capture.foundLLM,
		"native WS responses turn produced no llm.call span; span-based connectors export it unattributed (issue #6265)")
	assert.Equal(t, schemas.OTelProviderName(schemas.OpenAI), capture.provider, "llm.call span must carry the provider")
	assert.Equal(t, "gpt-realtime", capture.model, "llm.call span must carry the request model")
	assert.NotNil(t, capture.total, "llm.call span must carry token usage")
	assert.Equal(t, schemas.SpanStatusOk, capture.status, "a completed turn must end the llm.call span with ok status")
}

// TestNativeWSUpstreamFailedResponseProducesErroredLLMCallSpan asserts that a
// terminal response.failed event ends the llm.call span with error status and the
// provider's error code/message, rather than as a success (issue #6265, provider
// error path). Usage present on the failed event is still recorded.
func TestNativeWSUpstreamFailedResponseProducesErroredLLMCallSpan(t *testing.T) {
	SetLogger(bifrost.NewDefaultLogger(schemas.LogLevelError))

	// Fake OpenAI WS upstream that replies with a terminal response.failed carrying
	// an error object and usage.
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := newWSTestUpgrader()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
		failed := `{"type":"response.failed","sequence_number":1,"response":{"id":"resp_test","model":"gpt-realtime","status":"failed","error":{"code":"server_error","message":"the model failed to generate a response"},"usage":{"input_tokens":11,"output_tokens":0,"total_tokens":11}}}`
		_ = c.WriteMessage(websocket.TextMessage, []byte(failed))
		time.Sleep(300 * time.Millisecond)
	}))
	defer upstreamSrv.Close()

	clientSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := newWSTestUpgrader()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer clientSrv.Close()

	store := tracing.NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := tracing.NewTracer(store, nil, nil)
	defer tracer.Stop()
	capture := &wsLLMSpanCapture{done: make(chan struct{})}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{capture}, nil)

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: wsSpanTestAccount{},
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
		Tracer:  tracer,
	})
	require.NoError(t, err)
	defer client.Shutdown()

	pool := bfws.NewPool(&schemas.WSPoolConfig{})
	defer pool.Close()

	h := &WSResponsesHandler{
		client:       client,
		config:       &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{schemas.OpenAI: {}}},
		handlerStore: testWSHandlerStore{},
		pool:         pool,
		sessions:     bfws.NewSessionManager(10),
	}

	upstreamURL := "ws" + strings.TrimPrefix(upstreamSrv.URL, "http")
	upConn, err := bfws.DialUpstream(upstreamURL, nil, schemas.OpenAI, "test-key-1", nil)
	require.NoError(t, err)

	clientURL := "ws" + strings.TrimPrefix(clientSrv.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(clientURL, nil)
	require.NoError(t, err)

	session := bfws.NewSession(clientConn)
	session.SetUpstream(upConn)
	defer session.Close()

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-realtime",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
	}
	rawEvent := []byte(`{"type":"response.create","model":"gpt-realtime","input":"hello"}`)

	handled := h.tryNativeWSUpstream(session, ctx, req, rawEvent)
	require.True(t, handled, "native WS path should have handled the failed turn")

	select {
	case <-capture.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the trace to flush")
	}

	require.True(t, capture.foundLLM, "failed native WS turn must still produce an llm.call span")
	assert.Equal(t, schemas.SpanStatusError, capture.status, "response.failed must end the llm.call span with error status")
	assert.Equal(t, "the model failed to generate a response", capture.errAttr, "errored llm.call span must carry the provider error message")
	assert.Equal(t, 502, capture.statusCode, "errored llm.call span must carry a status code")
	assert.NotNil(t, capture.total, "usage present on the failed event must still be recorded")
}

// TestNativeWSUpstreamTimeoutProducesErroredLLMCallSpan asserts that when the
// native WS upstream stalls (no reply within the idle timeout), the turn still
// produces an llm.call span, ended with error status and the upstream status
// code, rather than exporting as a blank row (issue #6265, error path).
func TestNativeWSUpstreamTimeoutProducesErroredLLMCallSpan(t *testing.T) {
	SetLogger(bifrost.NewDefaultLogger(schemas.LogLevelError))

	// Fake OpenAI WS upstream that reads the forwarded event, then never replies,
	// so the handler's read deadline fires.
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := newWSTestUpgrader()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
		// Stall past the 1s idle timeout without sending any event.
		time.Sleep(2 * time.Second)
	}))
	defer upstreamSrv.Close()

	clientSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := newWSTestUpgrader()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer clientSrv.Close()

	store := tracing.NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := tracing.NewTracer(store, nil, nil)
	defer tracer.Stop()
	capture := &wsLLMSpanCapture{done: make(chan struct{})}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{capture}, nil)

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: wsSpanTestAccount{},
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
		Tracer:  tracer,
	})
	require.NoError(t, err)
	defer client.Shutdown()

	pool := bfws.NewPool(&schemas.WSPoolConfig{})
	defer pool.Close()

	h := &WSResponsesHandler{
		client: client,
		// 1s idle timeout so the stalled upstream trips the read deadline quickly.
		config: &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {NetworkConfig: &schemas.NetworkConfig{StreamIdleTimeoutInSeconds: 1}},
		}},
		handlerStore: testWSHandlerStore{},
		pool:         pool,
		sessions:     bfws.NewSessionManager(10),
	}

	upstreamURL := "ws" + strings.TrimPrefix(upstreamSrv.URL, "http")
	upConn, err := bfws.DialUpstream(upstreamURL, nil, schemas.OpenAI, "test-key-1", nil)
	require.NoError(t, err)

	clientURL := "ws" + strings.TrimPrefix(clientSrv.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(clientURL, nil)
	require.NoError(t, err)

	session := bfws.NewSession(clientConn)
	session.SetUpstream(upConn)
	defer session.Close()

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-realtime",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
	}
	rawEvent := []byte(`{"type":"response.create","model":"gpt-realtime","input":"hello"}`)

	handled := h.tryNativeWSUpstream(session, ctx, req, rawEvent)
	require.True(t, handled, "native WS path should have handled the stalled turn")

	select {
	case <-capture.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the trace to flush")
	}

	require.True(t, capture.foundLLM, "stalled native WS turn must still produce an llm.call span")
	assert.Equal(t, schemas.OTelProviderName(schemas.OpenAI), capture.provider, "errored llm.call span must carry the provider")
	assert.Equal(t, "gpt-realtime", capture.model, "errored llm.call span must carry the request model")
	assert.Equal(t, schemas.SpanStatusError, capture.status, "stalled turn must end the llm.call span with error status")
	assert.Equal(t, 504, capture.statusCode, "errored llm.call span must carry the upstream status code")
}
