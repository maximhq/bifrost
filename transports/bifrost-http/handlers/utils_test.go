package handlers

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

type requestFallbackHTTPAccount struct{ url string }

func (requestFallbackHTTPAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.Anthropic}, nil
}
func (requestFallbackHTTPAccount) GetKeysForProvider(_ context.Context, _ schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{{ID: "fallback-key", Value: *schemas.NewSecretVar("synthetic-key"), Models: schemas.WhiteList{"*"}, Weight: 1}}, nil
}
func (a requestFallbackHTTPAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if provider != schemas.Anthropic {
		return nil, fmt.Errorf("provider %s is not configured", provider)
	}
	config := schemas.DefaultNetworkConfig
	config.BaseURL = a.url
	config.MaxRetries = 0
	return &schemas.ProviderConfig{NetworkConfig: config, ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 4}}, nil
}

type requestFallbackHTTPRouting struct {
	mu                    sync.Mutex
	preRequests, attempts int
}

func (*requestFallbackHTTPRouting) GetName() string { return "fallback-http-routing" }
func (*requestFallbackHTTPRouting) Cleanup() error  { return nil }
func (p *requestFallbackHTTPRouting) PreRequestHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	p.mu.Lock()
	p.preRequests++
	p.mu.Unlock()
	if provider, _, _ := req.GetRequestFields(); provider == "" {
		req.SetProvider(schemas.Anthropic)
	}
	return nil
}
func (p *requestFallbackHTTPRouting) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	p.mu.Lock()
	p.attempts++
	p.mu.Unlock()
	return req, nil, nil
}
func (*requestFallbackHTTPRouting) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, err, nil
}

// Real HTTP/SSE requests exercise each ingress parser, routing, provider dispatch and egress.
// No live provider or credential is used: the primary always fails and the backup answers locally.
func TestRequestFallbackHTTP(t *testing.T) {
	SetLogger(&mockLogger{})
	var mu sync.Mutex
	var models []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		model := gjson.GetBytes(body, "model").String()
		mu.Lock()
		models = append(models, model)
		mu.Unlock()
		if model != "claude-haiku-4-5" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"type":"error","error":{"type":"not_found_error","message":"primary unavailable"}}`)
			return
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, event := range []struct{ name, data string }{
				{"message_start", `{"type":"message_start","message":{"id":"msg_backup","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`},
				{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
				{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"fallback-ok"}}`},
				{"content_block_stop", `{"type":"content_block_stop","index":0}`},
				{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`},
				{"message_stop", `{"type":"message_stop"}`},
			} {
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, event.data)
				w.(http.Flusher).Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_backup","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"fallback-ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(upstream.Close)
	plugin := &requestFallbackHTTPRouting{}
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: requestFallbackHTTPAccount{url: upstream.URL}, Logger: bifrost.NewNoOpLogger(), LLMPlugins: []schemas.LLMPlugin{plugin}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Shutdown)
	r := router.New()
	clientConfig := lib.DefaultClientConfig
	store := &lib.Config{ClientConfig: &clientConfig}
	NewInferenceHandler(nil, client, store).RegisterRoutes(r)
	integrations.NewOpenAIRouter(client, store, nil, bifrost.NewNoOpLogger()).RegisterRoutes(r)
	integrations.NewAnthropicRouter(client, store, nil, bifrost.NewNoOpLogger()).RegisterRoutes(r)
	integrations.NewGenAIRouter(client, store, nil, bifrost.NewNoOpLogger()).RegisterRoutes(r)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fasthttp.Server{Handler: r.Handler}
	go func() {
		if err := server.Serve(listener); err != nil {
			t.Errorf("gateway serve: %v", err)
		}
	}()
	t.Cleanup(func() { _ = server.Shutdown() })
	httpClient := &http.Client{Timeout: 10 * time.Second}
	for _, route := range []struct{ path, shape, terminal string }{
		{"/v1/chat/completions", "chat", "[DONE]"},
		{"/v1/responses", "responses", "response.completed"},
		{"/openai/v1/chat/completions", "chat", "[DONE]"},
		{"/openai/v1/responses", "responses", "response.completed"},
		{"/anthropic/v1/messages", "anthropic", "message_stop"},
		{"/genai/v1beta/models/", "genai", ""},
	} {
		for _, prefix := range []string{"anthropic/", ""} {
			for _, streaming := range []bool{false, true} {
				for _, tc := range []struct {
					name, fallbacks string
					primary         string
					status          int
					models          []string
				}{
					{name: "bare", fallbacks: `["foo/model","claude-haiku-4-5","anthropic/"]`, status: http.StatusOK, models: []string{"claude-sonnet-4-5", "claude-haiku-4-5"}},
					{name: "unknown-only", fallbacks: `["foo/model","anthropic/"]`, status: http.StatusNotFound, models: []string{"claude-sonnet-4-5"}},
					{name: "explicit", fallbacks: `["foo/model","anthropic/claude-haiku-4-5"]`, status: http.StatusOK, models: []string{"claude-sonnet-4-5", "claude-haiku-4-5"}},
					{name: "exhausted-bare", fallbacks: `["foo/model","claude-missing-backup"]`, status: http.StatusNotFound, models: []string{"claude-sonnet-4-5", "claude-missing-backup"}},
					{name: "primary-success", primary: "claude-haiku-4-5", fallbacks: `["foo/model","claude-missing-backup"]`, status: http.StatusOK, models: []string{"claude-haiku-4-5"}},
				} {
					t.Run(fmt.Sprintf("%s/prefix=%s/stream=%t/%s", route.path, prefix, streaming, tc.name), func(t *testing.T) {
						mu.Lock()
						models = nil
						mu.Unlock()
						plugin.mu.Lock()
						plugin.preRequests, plugin.attempts = 0, 0
						plugin.mu.Unlock()
						model := prefix + "claude-sonnet-4-5"
						if tc.primary != "" {
							model = prefix + tc.primary
						}
						path := route.path
						body := `{"model":"` + model + `","fallbacks":` + tc.fallbacks + `,"stream":` + strconv.FormatBool(streaming)
						switch route.shape {
						case "chat":
							body += `,"messages":[{"role":"user","content":"hi"}],"max_tokens":8}`
						case "responses":
							body += `,"input":"hi","max_output_tokens":8}`
						case "anthropic":
							body += `,"messages":[{"role":"user","content":"hi"}],"max_tokens":8}`
						case "genai":
							path += model + ":generateContent"
							if streaming {
								path = strings.TrimSuffix(path, ":generateContent") + ":streamGenerateContent"
							}
							body += `,"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":8}}`
						}
						resp, err := httpClient.Post("http://"+listener.Addr().String()+path, "application/json", strings.NewReader(body))
						if err != nil {
							t.Fatal(err)
						}
						data, err := io.ReadAll(resp.Body)
						_ = resp.Body.Close()
						if err != nil {
							t.Fatal(err)
						}
						if resp.StatusCode != tc.status {
							t.Fatalf("status=%d body=%s", resp.StatusCode, data)
						}
						if tc.status == http.StatusOK {
							if !strings.Contains(string(data), "fallback-ok") {
								t.Fatalf("no fallback content: %s", data)
							}
							isFallback, fallbackIndex := "true", "1"
							if tc.primary != "" {
								// Primary success omits fallback-only response headers.
								isFallback, fallbackIndex = "", ""
							}
							for header, want := range map[string]string{
								lib.HeaderBifrostRoutingInfoProvider:   "anthropic",
								lib.HeaderBifrostRoutingInfoModel:      "claude-haiku-4-5",
								lib.HeaderBifrostRoutingInfoIsFallback: isFallback,
								lib.HeaderBifrostFallbackIndex:         fallbackIndex,
							} {
								if got := resp.Header.Get(header); got != want {
									t.Errorf("%s = %q, want %q", header, got, want)
								}
							}
						} else if !strings.Contains(string(data), "primary unavailable") {
							t.Fatalf("dropping invalid fallbacks changed the primary error: %s", data)
						}
						if tc.status == http.StatusOK && streaming && route.terminal != "" && !strings.Contains(string(data), route.terminal) {
							t.Fatalf("missing terminal %s: %s", route.terminal, data)
						}
						mu.Lock()
						got := append([]string(nil), models...)
						mu.Unlock()
						if !slices.Equal(got, tc.models) {
							t.Fatalf("wrong upstream attempts (invalid entries must be dropped): %v", got)
						}
						plugin.mu.Lock()
						preRequests, attempts := plugin.preRequests, plugin.attempts
						plugin.mu.Unlock()
						if preRequests != 1 || attempts != len(tc.models) {
							t.Fatalf("routing ran %d times and per-attempt pre-hook %d times", preRequests, attempts)
						}
					})
				}
			}
		}
	}
}

// TestSendJSON_StandardBytes pins the exact json.NewEncoder byte contract that the
// switch to sonic must not regress: HTML escaping, sorted map keys (deterministic
// output), and a trailing newline. The input has out-of-order keys and HTML
// metacharacters so all three properties show up in one exact-bytes comparison.
// sonic.ConfigDefault would break every one of them.
func TestSendJSON_StandardBytes(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	SendJSON(ctx, map[string]string{"z": "1", "a": "<b>&</b>"})

	got := string(ctx.Response.Body())
	want := `{"a":"\u003cb\u003e\u0026\u003c/b\u003e","z":"1"}` + "\n"
	if got != want {
		t.Errorf("SendJSON bytes mismatch:\n got  %q\n want %q", got, want)
	}
}

// TestSendJSON_Deterministic pins that a many-key map serializes byte-identically
// every time. Without key sorting (ConfigDefault) Go's randomized map iteration
// would make this flaky — the regression we must never reintroduce.
func TestSendJSON_Deterministic(t *testing.T) {
	m := map[string]int{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6, "g": 7, "h": 8}
	first := &fasthttp.RequestCtx{}
	SendJSON(first, m)
	want := string(first.Response.Body())
	for i := 0; i < 50; i++ {
		ctx := &fasthttp.RequestCtx{}
		SendJSON(ctx, m)
		if got := string(ctx.Response.Body()); got != want {
			t.Fatalf("non-deterministic output on iter %d:\n want %s\n got  %s", i, want, got)
		}
	}
}

// TestSendJSONWithStatus_StandardBytes mirrors SendJSON's exact-bytes contract and
// pins the custom status code.
func TestSendJSONWithStatus_StandardBytes(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	SendJSONWithStatus(ctx, map[string]string{"z": "1", "a": "<x>"}, fasthttp.StatusCreated)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusCreated {
		t.Errorf("status = %d, want %d", got, fasthttp.StatusCreated)
	}
	got := string(ctx.Response.Body())
	want := `{"a":"\u003cx\u003e","z":"1"}` + "\n"
	if got != want {
		t.Errorf("SendJSONWithStatus bytes mismatch:\n got  %q\n want %q", got, want)
	}
}

// TestSendJSON_MarshalError pins the marshal-failure early-return: an unsupported
// value (a channel) must yield HTTP 500 and the SendError body, never a partial or
// panicking write. Guards the err != nil branch that the happy-path tests skip.
func TestSendJSON_MarshalError(t *testing.T) {
	SetLogger(&mockLogger{}) // error path logs a warning; shared no-op logger
	ctx := &fasthttp.RequestCtx{}
	SendJSON(ctx, make(chan int)) // channels are unmarshalable

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Errorf("status = %d, want %d", got, fasthttp.StatusInternalServerError)
	}
	if body := string(ctx.Response.Body()); !strings.Contains(body, "Failed to encode response") {
		t.Errorf("expected SendError body, got %q", body)
	}
}

// TestSendJSONWithStatus_MarshalError mirrors the marshal-failure early-return for
// the custom-status helper: the 500 from SendError must win over the requested
// status code.
func TestSendJSONWithStatus_MarshalError(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := &fasthttp.RequestCtx{}
	SendJSONWithStatus(ctx, make(chan int), fasthttp.StatusCreated)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Errorf("status = %d, want %d (SendError must override the requested status)", got, fasthttp.StatusInternalServerError)
	}
	if body := string(ctx.Response.Body()); !strings.Contains(body, "Failed to encode response") {
		t.Errorf("expected SendError body, got %q", body)
	}
}

// TestSendBifrostError_BodylessStatusBecomes502 pins that an upstream status that
// forbids a body (1xx, 204, 304) is never echoed on an error. fasthttp strips the
// body for those codes, so the JSON error would silently vanish and the client
// would see an empty 204 with Content-Type: application/json.
func TestSendBifrostError_BodylessStatusBecomes502(t *testing.T) {
	for _, code := range []int{fasthttp.StatusContinue, fasthttp.StatusNoContent, fasthttp.StatusResetContent, fasthttp.StatusNotModified} {
		ctx := &fasthttp.RequestCtx{}
		SendBifrostError(ctx, &schemas.BifrostError{
			StatusCode: new(code),
			Error:      &schemas.ErrorField{Message: "provider API error (status " + strconv.Itoa(code) + ")"},
		})
		if got := ctx.Response.StatusCode(); got != fasthttp.StatusBadGateway {
			t.Errorf("upstream %d: status got %d, want %d", code, got, fasthttp.StatusBadGateway)
		}
		if body := string(ctx.Response.Body()); !strings.Contains(body, "provider API error") {
			t.Errorf("upstream %d: error body missing, got %q", code, body)
		}
	}
}

// TestSendBifrostError_NormalStatusPreserved guards the guard: ordinary upstream
// codes still pass through untouched.
func TestSendBifrostError_NormalStatusPreserved(t *testing.T) {
	for _, code := range []int{fasthttp.StatusBadRequest, fasthttp.StatusTooManyRequests, fasthttp.StatusInternalServerError} {
		ctx := &fasthttp.RequestCtx{}
		SendBifrostError(ctx, &schemas.BifrostError{
			StatusCode: new(code),
			Error:      &schemas.ErrorField{Message: "boom"},
		})
		if got := ctx.Response.StatusCode(); got != code {
			t.Errorf("status got %d, want %d", got, code)
		}
	}
}

// useUnguardedURLAccessibilityDialer swaps checkURLAccessibility's dial context
// for a plain dialer so the test can reach a loopback-bound httptest.Server,
// restoring the production guarded dialer afterward. Test-only.
func useUnguardedURLAccessibilityDialer(t *testing.T) {
	t.Helper()
	prev := checkURLAccessibilityDialContext
	checkURLAccessibilityDialContext = (&net.Dialer{}).DialContext
	t.Cleanup(func() { checkURLAccessibilityDialContext = prev })
}

func TestCheckURLAccessibility_HTTP200(t *testing.T) {
	useUnguardedURLAccessibilityDialer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := checkURLAccessibility(srv.URL); err != nil {
		t.Fatalf("expected no error for HTTP 200, got: %v", err)
	}
}

func TestCheckURLAccessibility_HTTPNon200(t *testing.T) {
	useUnguardedURLAccessibilityDialer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if err := checkURLAccessibility(srv.URL); err == nil {
		t.Fatal("expected error for HTTP 404, got nil")
	}
}

// TestCheckURLAccessibility_BlocksLoopbackByDefault proves the production
// dialer (not overridden) refuses a loopback target: an admin-supplied
// pricing_url/model_parameters_url/mcp_library_url is dialed through the
// guarded dialer, and the error returned to the caller stays generic rather
// than reflecting transport detail from the target.
func TestCheckURLAccessibility_BlocksLoopbackByDefault(t *testing.T) {
	SetLogger(&mockLogger{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := checkURLAccessibility(srv.URL)
	if err == nil {
		t.Fatal("expected loopback target to be blocked, got nil error")
	}
	if err.Error() != "url is not reachable" {
		t.Fatalf("expected a generic error (no reflected transport detail), got: %v", err)
	}
}

// TestCheckURLAccessibility_DoesNotFollowRedirects pins that the check judges the
// URL the operator validated, not wherever it redirects: a 302 to a second server
// that would answer 200 must still be reported as not accessible.
func TestCheckURLAccessibility_DoesNotFollowRedirects(t *testing.T) {
	useUnguardedURLAccessibilityDialer(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	followed := false
	target.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed = true
		w.WriteHeader(http.StatusOK)
	})
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	err := checkURLAccessibility(redirector.URL)
	if followed {
		t.Fatal("redirect target was requested; redirects must not be followed")
	}
	if err == nil {
		t.Fatal("expected a redirecting URL to be reported as not accessible, got nil")
	}
}

// TestRequestWorkContext_DetachesFromServerShutdown pins the invariant that makes
// RequestWorkContext safe: the context it returns must not inherit the RequestCtx's
// Done(), which is the server-wide fasthttp.Server.done. Deriving from it directly leaves a
// watcher goroutine that panics with "missing cancel error" once Shutdown resets the field.
// Request values must still read through for the handler's lifetime.
func TestRequestWorkContext_DetachesFromServerShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	type result struct {
		workCtx context.Context
		value   any
		parent  <-chan struct{}
	}
	got := make(chan result, 1)
	served := make(chan struct{})

	srv := &fasthttp.Server{
		CloseOnShutdown: true,
		Handler: func(ctx *fasthttp.RequestCtx) {
			ctx.SetUserValue("tenant", "acme")
			workCtx, cancel := RequestWorkContext(ctx, RequestWorkTimeout)
			t.Cleanup(cancel)
			got <- result{workCtx: workCtx, value: workCtx.Value("tenant"), parent: ctx.Done()}
			close(served)
		},
	}
	go func() { _ = srv.Serve(ln) }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := conn.Write([]byte("GET /health HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	<-served
	r := <-got
	_ = conn.Close()

	if r.value != "acme" {
		t.Fatalf("request value not readable through work context: got %v, want acme", r.value)
	}
	if r.parent == nil {
		t.Fatal("precondition failed: RequestCtx.Done() was nil, so this test proves nothing")
	}
	if r.workCtx.Done() == r.parent {
		t.Fatal("work context shares the server-wide done channel; it must be detached")
	}

	if err := srv.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	// The server channel has now been closed and reset. A context still parented to the
	// RequestCtx would be cancelled by that; a detached one must survive it.
	if err := r.workCtx.Err(); err != nil {
		t.Fatalf("work context was cancelled by server shutdown: %v", err)
	}
}

// A burst of "no keys found for provider" 400s never reaches a provider, so no
// llm.call span opens and the trace carried only "HTTP 400".
func TestSendBifrostError_StampsRootSpanForPreDispatchFailure(t *testing.T) {
	store := tracing.NewTraceStore(5*time.Minute, nil)
	t.Cleanup(store.Stop)
	tracer := tracing.NewTracer(store, nil, nil)
	t.Cleanup(tracer.Stop)

	traceID := tracer.CreateTrace("")
	spanCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	_, rootHandle := tracer.StartSpan(spanCtx, "/v1/chat/completions", schemas.SpanKindHTTPRequest)

	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyTracer, schemas.Tracer(tracer))
	ctx.SetUserValue(schemas.BifrostContextKeyTraceID, traceID)

	bifrostErr := &schemas.BifrostError{
		StatusCode: schemas.Ptr(400),
		Error: &schemas.ErrorField{
			Message: "no keys found for provider: vertex and model: claude-opus-5-5",
			Type:    schemas.Ptr(schemas.NoKeySupportsModel),
		},
	}
	bifrostErr.ExtraFields.Provider = schemas.Vertex
	bifrostErr.ExtraFields.RequestType = schemas.ChatCompletionRequest
	bifrostErr.ExtraFields.OriginalModelRequested = "claude-opus-5-5"
	bifrostErr.ExtraFields.ErrorType = schemas.ErrorTypeCallerModelNotAvailable

	SendBifrostError(ctx, bifrostErr)

	root := tracer.SpanFromHandle(rootHandle)
	for key, want := range map[string]any{
		schemas.AttrError:               "no keys found for provider: vertex and model: claude-opus-5-5",
		schemas.AttrErrorTypeSpec:       schemas.NoKeySupportsModel,
		schemas.AttrBifrostErrorType:    string(schemas.ErrorTypeCallerModelNotAvailable),
		schemas.AttrRequestModel:        "claude-opus-5-5",
		schemas.AttrBifrostProviderName: string(schemas.Vertex),
	} {
		if got, _ := root.GetAttribute(key); got != want {
			t.Errorf("root span %s = %v, want %v", key, got, want)
		}
	}
	if got, _ := root.GetAttribute(schemas.AttrProviderName); got != schemas.OTelProviderName(schemas.Vertex) {
		t.Errorf("root span %s = %v, want %v", schemas.AttrProviderName, got, schemas.OTelProviderName(schemas.Vertex))
	}
}

// A request that did reach a provider already had its resolved model propagated to
// the root span; the originally requested one must not overwrite it.
func TestSendBifrostError_DoesNotOverwriteResolvedIdentity(t *testing.T) {
	store := tracing.NewTraceStore(5*time.Minute, nil)
	t.Cleanup(store.Stop)
	tracer := tracing.NewTracer(store, nil, nil)
	t.Cleanup(tracer.Stop)

	traceID := tracer.CreateTrace("")
	spanCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	_, rootHandle := tracer.StartSpan(spanCtx, "/v1/chat/completions", schemas.SpanKindHTTPRequest)
	tracer.SpanFromHandle(rootHandle).SetAttribute(schemas.AttrRequestModel, "resolved-model")

	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyTracer, schemas.Tracer(tracer))
	ctx.SetUserValue(schemas.BifrostContextKeyTraceID, traceID)

	bifrostErr := &schemas.BifrostError{StatusCode: schemas.Ptr(500)}
	bifrostErr.ExtraFields.OriginalModelRequested = "alias-the-caller-asked-for"
	SendBifrostError(ctx, bifrostErr)

	if got, _ := tracer.SpanFromHandle(rootHandle).GetAttribute(schemas.AttrRequestModel); got != "resolved-model" {
		t.Errorf("root span %s = %v, want the already-propagated resolved model", schemas.AttrRequestModel, got)
	}
}

// No trace active (plain API routes, tracing disabled) must not panic.
func TestSendBifrostError_NoTracerIsSafe(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	SendBifrostError(ctx, &schemas.BifrostError{StatusCode: schemas.Ptr(500)})
	if ctx.Response.StatusCode() != 500 {
		t.Errorf("status = %d, want 500", ctx.Response.StatusCode())
	}
}

// TestSendJSON_ExtraFieldsPlacement pins client.extra_fields_placement on the native send path:
// body mode keeps extra_fields, headers mode drops it from the body and writes it as headers.
func TestSendJSON_ExtraFieldsPlacement(t *testing.T) {
	resp := &schemas.BifrostChatResponse{
		ID:          "chatcmpl-1",
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.ChatCompletionRequest, Provider: schemas.OpenAI, Latency: 12},
	}

	bodyCtx := &fasthttp.RequestCtx{}
	SendJSON(bodyCtx, resp)
	if !strings.Contains(string(bodyCtx.Response.Body()), `"extra_fields"`) {
		t.Fatalf("body mode must keep extra_fields, got %s", bodyCtx.Response.Body())
	}

	headersCtx := &fasthttp.RequestCtx{}
	headersCtx.SetUserValue(lib.FastHTTPUserValueExtraFieldsInHeaders, true)
	lib.ApplyBifrostResponseHeaders(headersCtx, nil, resp.ExtraFields)
	SendJSON(headersCtx, resp)
	body := string(headersCtx.Response.Body())
	if strings.Contains(body, `"extra_fields"`) {
		t.Fatalf("headers mode must drop extra_fields from the body, got %s", body)
	}
	if !strings.Contains(body, `"id":"chatcmpl-1"`) {
		t.Fatalf("headers mode must keep the rest of the body, got %s", body)
	}
	if got := string(headersCtx.Response.Header.Peek(lib.HeaderBifrostLatency)); got != "12" {
		t.Fatalf("latency header got %q, want 12", got)
	}
}

// TestSendBifrostError_ExtraFieldsPlacement pins the same contract on native error responses.
func TestSendBifrostError_ExtraFieldsPlacement(t *testing.T) {
	newErr := func() *schemas.BifrostError {
		return &schemas.BifrostError{
			StatusCode:  new(fasthttp.StatusTooManyRequests),
			Error:       &schemas.ErrorField{Message: "slow down"},
			ExtraFields: schemas.BifrostErrorExtraFields{Provider: schemas.OpenAI, RetryAfter: 2000},
		}
	}

	bodyCtx := &fasthttp.RequestCtx{}
	SendBifrostError(bodyCtx, newErr())
	if !strings.Contains(string(bodyCtx.Response.Body()), `"extra_fields"`) {
		t.Fatalf("body mode must keep extra_fields, got %s", bodyCtx.Response.Body())
	}

	headersCtx := &fasthttp.RequestCtx{}
	headersCtx.SetUserValue(lib.FastHTTPUserValueExtraFieldsInHeaders, true)
	SendBifrostError(headersCtx, newErr())
	body := string(headersCtx.Response.Body())
	if strings.Contains(body, `"extra_fields"`) {
		t.Fatalf("headers mode must drop extra_fields from the error body, got %s", body)
	}
	if !strings.Contains(body, `"slow down"`) {
		t.Fatalf("headers mode must keep the error message, got %s", body)
	}
	if got := headersCtx.Response.StatusCode(); got != fasthttp.StatusTooManyRequests {
		t.Fatalf("status got %d, want 429", got)
	}
	if got := string(headersCtx.Response.Header.Peek(lib.HeaderBifrostRetryAfterMs)); got != "2000" {
		t.Fatalf("retry-after header got %q, want 2000", got)
	}
	if got := string(headersCtx.Response.Header.Peek(lib.HeaderBifrostProvider)); got != "openai" {
		t.Fatalf("provider header got %q, want openai", got)
	}
}

// TestResolvePeriod pins sliding and calendar period windows. Calendar periods
// start at local midnight in the requested timezone; sliding ones ignore it.
func TestResolvePeriod(t *testing.T) {
	thursday := time.Date(2026, 10, 15, 15, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		period   string
		timezone string
		now      time.Time
		want     time.Time
	}{
		{"sliding 1h", "1h", "", thursday, thursday.Add(-time.Hour)},
		{"sliding 24h ignores timezone", "24h", "Asia/Kolkata", thursday, thursday.Add(-24 * time.Hour)},
		{"sliding 30d", "30d", "", thursday, thursday.AddDate(0, 0, -30)},
		{"today in UTC", "today", "UTC", thursday, time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)},
		{"today defaults to UTC", "today", "", thursday, time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)},
		{"today in an unknown timezone falls back to UTC", "today", "Mars/Olympus", thursday, time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)},
		{"today in New York", "today", "America/New_York", thursday, time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC)},
		{"today in Kolkata", "today", "Asia/Kolkata", thursday, time.Date(2026, 10, 14, 18, 30, 0, 0, time.UTC)},
		{"today in Kolkata by its legacy name, which browsers still report", "today", "Asia/Calcutta", thursday, time.Date(2026, 10, 14, 18, 30, 0, 0, time.UTC)},
		{"this week starts Monday", "this_week", "UTC", thursday, time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)},
		{"this week in New York", "this_week", "America/New_York", thursday, time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC)},
		{"this week on a Sunday is the previous Monday", "this_week", "UTC", time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)},
		{"this month in UTC", "this_month", "UTC", thursday, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{"this month in Kolkata", "this_month", "Asia/Kolkata", thursday, time.Date(2026, 9, 30, 18, 30, 0, 0, time.UTC)},
		{"Kolkata has already rolled into the next month", "this_month", "Asia/Kolkata", time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 18, 30, 0, 0, time.UTC)},
		// Some zones start DST at midnight, so the day has no 00:00 and begins at 01:00.
		// time.Date normalizes such a midnight back to 23:00 on the previous day, which
		// would pull an hour of yesterday into "today".
		{"today in Havana when DST skips midnight", "today", "America/Havana", time.Date(2019, 3, 10, 12, 0, 0, 0, time.UTC), time.Date(2019, 3, 10, 5, 0, 0, 0, time.UTC)},
		{"today in Santiago when DST skips midnight", "today", "America/Santiago", time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC), time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC)},
		{"today in Santiago on an ordinary day", "today", "America/Santiago", time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC), time.Date(2026, 9, 7, 3, 0, 0, 0, time.UTC)},
		// Amman ended DST at 01:00 on 2021-10-29, so 00:00 happened twice. time.Date picks
		// the second one, which would drop the first hour of the day.
		{"today in Amman when DST end repeats midnight", "today", "Asia/Amman", time.Date(2021, 10, 29, 12, 0, 0, 0, time.UTC), time.Date(2021, 10, 28, 21, 0, 0, 0, time.UTC)},
		{"today in Amman on an ordinary day", "today", "Asia/Amman", time.Date(2021, 10, 30, 12, 0, 0, 0, time.UTC), time.Date(2021, 10, 29, 22, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := resolvePeriodAt(tc.period, tc.timezone, tc.now)
			if start == nil || end == nil {
				t.Fatalf("expected a window for %q, got nil", tc.period)
			}
			if !start.Equal(tc.want) {
				t.Fatalf("start = %s, want %s", start.UTC(), tc.want)
			}
			if !end.Equal(tc.now) {
				t.Fatalf("end = %s, want %s", end.UTC(), tc.now)
			}
		})
	}

	t.Run("unknown period", func(t *testing.T) {
		if start, end := resolvePeriodAt("2d", "UTC", thursday); start != nil || end != nil {
			t.Fatalf("expected nil window for unknown period, got %v %v", start, end)
		}
	})
}

// TestPeriodLocationRejectsLocal verifies "Local" does not resolve to the server's
// own timezone, so a calendar period never depends on where the server runs.
func TestPeriodLocationRejectsLocal(t *testing.T) {
	if got := periodLocation("Local"); got != time.UTC {
		t.Fatalf("periodLocation(\"Local\") = %v, want UTC", got)
	}
	if got := periodLocation("Asia/Kolkata"); got.String() != "Asia/Kolkata" {
		t.Fatalf("periodLocation(\"Asia/Kolkata\") = %v, want Asia/Kolkata", got)
	}
}
