package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	ws "github.com/fasthttp/websocket"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

// fakeVoiceLive stands in for Azure AI Voice Live. It records the URL shape and
// auth header the plugin presents, then echoes realtime events back.
type fakeVoiceLive struct {
	mu       sync.Mutex
	path     string
	query    map[string]string
	apiKey   string
	received []string
	addr     string
}

func startFakeVoiceLive(t *testing.T) *fakeVoiceLive {
	t.Helper()
	f := &fakeVoiceLive{query: map[string]string{}}
	up := ws.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f.addr = ln.Addr().String()

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.path = r.URL.Path
		for k := range r.URL.Query() {
			f.query[k] = r.URL.Query().Get(k)
		}
		f.apiKey = r.Header.Get("api-key")
		f.mu.Unlock()

		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.received = append(f.received, string(msg))
			f.mu.Unlock()
			reply := `{"type":"response.done","echo":"` + strings.ReplaceAll(string(msg), `"`, `\"`) + `"}`
			if err := conn.WriteMessage(mt, []byte(reply)); err != nil {
				return
			}
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f
}

// newTransportReplica builds a fasthttp server that reproduces exactly what
// TransportInterceptorMiddleware does around HTTPTransportPreHook:
//
//	bifrostCtx := schemas.NewBifrostContext(rctx, NoDeadline)   // parent = RequestCtx
//	bifrostCtx.SetValue(ModelCatalog, ...)                      // always stamped
//	pluginCtx := bifrostCtx.WithPluginScope(&name)              // what the hook receives
//	resp, err := plugin.HTTPTransportPreHook(pluginCtx, req)
//	if resp != nil { applyHTTPResponseToCtx(rctx, resp); return }
//	next(rctx)
//
// applyHTTPResponseToCtx is unexported in the transport, so its three lines are
// reproduced verbatim here.
func newTransportReplica(nativeHandlerRan *bool) fasthttp.RequestHandler {
	return func(rctx *fasthttp.RequestCtx) {
		bifrostCtx := schemas.NewBifrostContext(rctx, schemas.NoDeadline)
		bifrostCtx.SetValue(schemas.BifrostContextKeyModelCatalog, "catalog-sentinel")

		req := schemas.AcquireHTTPRequest()
		defer schemas.ReleaseHTTPRequest(req)
		req.Method = string(rctx.Method())
		req.Path = string(rctx.Path())
		for k, v := range rctx.Request.Header.All() {
			req.Headers[string(k)] = string(v)
		}
		for k, v := range rctx.Request.URI().QueryArgs().All() {
			req.Query[string(k)] = string(v)
		}

		name := GetName()
		pluginCtx := bifrostCtx.WithPluginScope(&name)
		resp, err := HTTPTransportPreHook(pluginCtx, req)
		pluginCtx.ReleasePluginScope()

		if err != nil {
			rctx.SetStatusCode(500)
			rctx.SetBodyString(err.Error())
			return
		}
		if resp != nil {
			rctx.SetStatusCode(resp.StatusCode)
			for k, v := range resp.Headers {
				rctx.Response.Header.Set(k, v)
			}
			if resp.Body != nil {
				rctx.SetBody(resp.Body)
			}
			return
		}
		// Stands in for Bifrost's own WSRealtimeHandler.
		*nativeHandlerRan = true
		rctx.SetStatusCode(200)
		rctx.SetBodyString("bifrost native realtime handler")
	}
}

// TestRequestCtxIsReachableFromPluginScope is the load-bearing assumption of
// this whole plugin: the context handed to HTTPTransportPreHook must yield the
// live *fasthttp.RequestCtx, or the socket cannot be hijacked.
//
// If this test ever fails, the plugin is broken and the fix is a core
// affordance — see the README.
func TestRequestCtxIsReachableFromPluginScope(t *testing.T) {
	var rctx fasthttp.RequestCtx
	rctx.Init(&fasthttp.Request{}, nil, nil)

	bifrostCtx := schemas.NewBifrostContext(&rctx, schemas.NoDeadline)
	bifrostCtx.SetValue(schemas.BifrostContextKeyModelCatalog, "catalog-sentinel")

	name := GetName()
	pluginCtx := bifrostCtx.WithPluginScope(&name)

	recovered, ok := pluginCtx.GetParentCtxWithUserValues().(*fasthttp.RequestCtx)
	if !ok {
		t.Fatal("plugin-scoped context no longer yields *fasthttp.RequestCtx; socket hijack is impossible")
	}
	if recovered != &rctx {
		t.Fatal("recovered a different RequestCtx than the one in flight")
	}

	// The root context does NOT work, because its user-value map is non-empty and
	// GetParentCtxWithUserValues wraps the parent in context.WithValue layers.
	if _, ok := bifrostCtx.GetParentCtxWithUserValues().(*fasthttp.RequestCtx); ok {
		t.Log("note: root context also yielded the RequestCtx (no user values were set)")
	}
}

func TestVoiceLiveRealtimeProxy(t *testing.T) {
	fake := startFakeVoiceLive(t)

	if err := Init(map[string]any{
		"endpoint":        "http://" + fake.addr,
		"api_key":         "sk-voice-live-test",
		"api_version":     "2025-10-01",
		"models":          []any{"gpt-realtime", "phi4-mm-realtime"},
		"allowed_origins": []any{"*"},
		"max_connections": 4,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer Cleanup()

	var nativeHandlerRan bool
	ln := fasthttputil.NewInmemoryListener()
	srv := &fasthttp.Server{Handler: newTransportReplica(&nativeHandlerRan)}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Shutdown()

	dialer := ws.Dialer{NetDial: func(network, addr string) (net.Conn, error) { return ln.Dial() }}

	t.Run("claims gpt-realtime and relays both directions", func(t *testing.T) {
		conn, resp, err := dialer.Dial("ws://inmemory/v1/realtime?model=gpt-realtime", http.Header{})
		if err != nil {
			t.Fatalf("upgrade must succeed: %v", err)
		}
		defer conn.Close()
		if resp.StatusCode != fasthttp.StatusSwitchingProtocols {
			t.Fatalf("want 101, got %d", resp.StatusCode)
		}
		if nativeHandlerRan {
			t.Fatal("Bifrost's realtime handler must be bypassed")
		}

		sessionUpdate := `{"type":"session.update","session":{"voice":"en-US-AvaNeural"}}`
		if err := conn.WriteMessage(ws.TextMessage, []byte(sessionUpdate)); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, got, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !strings.Contains(string(got), `"type":"response.done"`) {
			t.Fatalf("want a Voice Live server event, got %s", got)
		}

		// Binary audio frames must survive the hop with their type intact.
		if err := conn.WriteMessage(ws.BinaryMessage, []byte{0x01, 0x02, 0x03}); err != nil {
			t.Fatalf("write binary: %v", err)
		}
		mt, _, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read binary: %v", err)
		}
		if mt != ws.BinaryMessage {
			t.Fatalf("binary audio frame changed type to %d", mt)
		}

		fake.mu.Lock()
		defer fake.mu.Unlock()
		if fake.path != "/voice-live/realtime" {
			t.Fatalf("upstream path = %q, want /voice-live/realtime", fake.path)
		}
		if fake.query["api-version"] != "2025-10-01" || fake.query["model"] != "gpt-realtime" {
			t.Fatalf("upstream query = %v", fake.query)
		}
		if fake.apiKey != "sk-voice-live-test" {
			t.Fatalf("upstream api-key = %q", fake.apiKey)
		}
	})

	t.Run("claims phi4-mm-realtime and strips the provider prefix", func(t *testing.T) {
		conn, _, err := dialer.Dial("ws://inmemory/v1/realtime?model=azure/phi4-mm-realtime", http.Header{})
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer conn.Close()
		if err := conn.WriteMessage(ws.TextMessage, []byte(`{"type":"response.create"}`)); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatalf("read: %v", err)
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if fake.query["model"] != "phi4-mm-realtime" {
			t.Fatalf("upstream model = %q, want phi4-mm-realtime", fake.query["model"])
		}
	})

	t.Run("unclaimed models fall through to Bifrost", func(t *testing.T) {
		nativeHandlerRan = false
		_, resp, err := dialer.Dial("ws://inmemory/v1/realtime?model=gpt-4o-realtime-preview", http.Header{})
		if err == nil {
			t.Fatal("native handler returns 200, so the upgrade must fail")
		}
		if resp == nil || resp.StatusCode != 200 {
			t.Fatalf("want the native handler's 200, got %v", resp)
		}
		if !nativeHandlerRan {
			t.Fatal("unclaimed models must reach Bifrost's own handler")
		}
	})

	t.Run("non-realtime requests are ignored", func(t *testing.T) {
		req := schemas.AcquireHTTPRequest()
		defer schemas.ReleaseHTTPRequest(req)
		req.Method = "POST"
		req.Path = "/v1/chat/completions"

		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		resp, err := HTTPTransportPreHook(ctx, req)
		if err != nil || resp != nil {
			t.Fatalf("want pass-through, got resp=%v err=%v", resp, err)
		}
	})
}

// TestRejectsCleartextEndpointOutsideLoopback covers the config guard: the
// api-key travels in a header on the upstream hop, so an unencrypted endpoint
// would hand it to anyone on the path. Loopback stays allowed because that is
// what local fakes and these tests use.
func TestRejectsCleartextEndpointOutsideLoopback(t *testing.T) {
	cases := []struct {
		endpoint   string
		wantReject bool
	}{
		{"https://demo.services.ai.azure.com", false},
		{"demo.services.ai.azure.com", false}, // bare host defaults to wss://
		{"wss://demo.services.ai.azure.com", false},
		{"http://127.0.0.1:8080", false},
		{"http://localhost:8080", false},
		{"ws://[::1]:8080", false},
		{"http://demo.services.ai.azure.com", true},
		{"ws://demo.services.ai.azure.com", true},
		{"http://10.0.0.5:8080", true},
	}

	for _, tc := range cases {
		err := Init(map[string]any{"endpoint": tc.endpoint, "api_key": "sk-test"})
		if err == nil {
			Cleanup()
		}
		if tc.wantReject && err == nil {
			t.Errorf("endpoint %q: want rejection, got none", tc.endpoint)
		}
		if !tc.wantReject && err != nil {
			t.Errorf("endpoint %q: want accepted, got %v", tc.endpoint, err)
		}
	}
}

func TestUpstreamURLShape(t *testing.T) {
	c := &Config{Endpoint: "https://demo.services.ai.azure.com", APIVersion: "2025-10-01"}

	got := buildUpstreamURL(c, "gpt-realtime", "")
	want := "wss://demo.services.ai.azure.com/voice-live/realtime?api-version=2025-10-01&model=gpt-realtime"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}

	got = buildUpstreamURL(c, "phi4-mm-realtime", "transcription")
	if !strings.Contains(got, "intent=transcription") {
		t.Fatalf("intent not propagated: %s", got)
	}

	// A bare host must still produce a wss:// URL.
	if u := buildUpstreamURL(&Config{Endpoint: "demo.services.ai.azure.com", APIVersion: "v1"}, "m", ""); !strings.HasPrefix(u, "wss://") {
		t.Fatalf("bare host did not get a scheme: %s", u)
	}
}
