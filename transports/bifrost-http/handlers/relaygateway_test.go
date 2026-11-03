package handlers

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestEndpointRequestPath(t *testing.T) {
	cases := []struct {
		in, name, path string
		ok             bool
	}{
		{"/relay/tavily/search", "tavily", "/search", true},
		{"/relay/tavily", "tavily", "/", true},
		{"/relay/tavily/v1/a/b", "tavily", "/v1/a/b", true},
		{"/relay/bad name/x", "", "", false},
		{"/relay//x", "", "", false},
		{"/other/tavily/x", "", "", false},
	}
	for _, tc := range cases {
		name, path, ok := endpointRequestPath(tc.in)
		assert.Equal(t, tc.ok, ok, tc.in)
		assert.Equal(t, tc.name, name, tc.in)
		assert.Equal(t, tc.path, path, tc.in)
	}
}

func TestRelayGatewayHandlerRelays(t *testing.T) {
	var got *http.Request
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", "yes")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	manager := relay.NewManager(nil, nil)
	require.NoError(t, manager.Upsert(&schemas.RelayConfig{
		Name: "tavily", BaseURL: upstream.URL, SkipRouteCheck: true,
		Auth:       schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer},
		Credential: &schemas.RelayCredential{Value: schemas.NewSecretVar("sk-endpoint")},
	}))
	router := NewRelayGatewayHandler(manager, nil, nil)

	var ctx fasthttp.RequestCtx
	ctx.Request.SetRequestURI("/relay/tavily/search?q=1")
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.Header.Set("Authorization", "Bearer sk-bf-virtual-key")
	ctx.Request.Header.Set("x-bf-vk", "sk-bf-virtual-key")
	ctx.Request.Header.Set("Cookie", "session=1")
	ctx.Request.Header.Set("X-Custom", "kept")
	ctx.Request.SetBodyString(`{"query":"x"}`)
	router.handle(&ctx)

	require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Equal(t, `{"ok":true}`, string(ctx.Response.Body()))
	assert.Equal(t, "yes", string(ctx.Response.Header.Peek("X-Upstream")))
	require.NotNil(t, got)
	assert.Equal(t, "Bearer sk-endpoint", got.Header.Get("Authorization"), "the caller's virtual key never reaches the upstream")
	assert.Empty(t, got.Header.Get("x-bf-vk"))
	assert.Empty(t, got.Header.Get("Cookie"))
	assert.Equal(t, "kept", got.Header.Get("X-Custom"))
	assert.Equal(t, "/search", got.URL.Path)
	assert.Equal(t, "q=1", got.URL.RawQuery)
	assert.Equal(t, `{"query":"x"}`, gotBody)

	var missing fasthttp.RequestCtx
	missing.Request.SetRequestURI("/relay/unknown/x")
	missing.Request.Header.SetMethod(fasthttp.MethodGet)
	router.handle(&missing)
	assert.Equal(t, 404, missing.Response.StatusCode())

	var traversal fasthttp.RequestCtx
	traversal.Request.SetRequestURI("/relay/tavily/x")
	traversal.Request.Header.SetMethod(fasthttp.MethodGet)
	traversal.URI().SetPathBytes([]byte("/relay/tavily//evil.com/x"))
	router.handle(&traversal)
	require.Equal(t, 200, traversal.Response.StatusCode())
	assert.Equal(t, upstream.Listener.Addr().String(), got.Host, "a doubled slash cannot move the request off the endpoint's host")
	assert.Equal(t, "/evil.com/x", got.URL.Path)
}

// Relay replies and errors carry the Bifrost response headers inference replies carry, errors use
// the shared error envelope, and x-bf-eh-* headers reach the upstream as they do on the inference
// and passthrough paths, without ever replacing the relay's credential.
func TestRelayGatewayHeadersErrorsAndExtraHeaders(t *testing.T) {
	var got *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()
	manager := relay.NewManager(nil, nil)
	require.NoError(t, manager.Upsert(&schemas.RelayConfig{
		Name: "tavily", BaseURL: upstream.URL, SkipRouteCheck: true,
		Auth:       schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer},
		Credential: &schemas.RelayCredential{Value: schemas.NewSecretVar("sk-endpoint")},
	}))
	h := NewRelayGatewayHandler(manager, nil, nil)

	var ok fasthttp.RequestCtx
	ok.Request.SetRequestURI("/relay/tavily/search")
	ok.Request.Header.SetMethod(fasthttp.MethodPost)
	ok.Request.Header.Set("x-bf-eh-x-tenant", "acme")
	ok.Request.Header.Set("x-bf-eh-authorization", "Bearer caller-override")
	// What an auth proxy in front of Bifrost adds: never forwarded to a relay's upstream.
	ok.Request.Header.Set("Cf-Access-Jwt-Assertion", "proxy-jwt")
	ok.Request.Header.Set("X-Amzn-Oidc-Data", "oidc")
	ok.Request.Header.Set("X-Forwarded-Access-Token", "proxy-token")
	// Unless the caller names it through x-bf-eh-*.
	ok.Request.Header.Set("x-bf-eh-x-upstream-token", "chosen")
	ok.Request.SetBodyString(`{}`)
	h.handle(&ok)
	require.Equal(t, 200, ok.Response.StatusCode())
	require.NotNil(t, got)
	assert.Equal(t, "acme", got.Header.Get("x-tenant"), "an x-bf-eh-* header reaches the upstream")
	assert.Empty(t, got.Header.Get("Cf-Access-Jwt-Assertion"), "a proxy's identity token never reaches a relay upstream")
	assert.Empty(t, got.Header.Get("X-Amzn-Oidc-Data"))
	assert.Empty(t, got.Header.Get("X-Forwarded-Access-Token"))
	assert.Equal(t, "chosen", got.Header.Get("X-Upstream-Token"), "a sensitive-looking header the caller sends through x-bf-eh-* is forwarded")
	assert.Equal(t, "Bearer sk-endpoint", got.Header.Get("Authorization"), "it never replaces the relay's credential")
	assert.Equal(t, string(schemas.RelayRequest), string(ok.Response.Header.Peek(lib.HeaderBifrostRequestType)))

	var missing fasthttp.RequestCtx
	missing.Request.SetRequestURI("/relay/unknown/x")
	missing.Request.Header.SetMethod(fasthttp.MethodGet)
	h.handle(&missing)
	require.Equal(t, 404, missing.Response.StatusCode())
	assert.Equal(t, string(schemas.RelayRequest), string(missing.Response.Header.Peek(lib.HeaderBifrostRequestType)), "errors carry the request type header too")
	var body schemas.BifrostError
	require.NoError(t, json.Unmarshal(missing.Response.Body(), &body))
	assert.Equal(t, schemas.RelayRequest, body.ExtraFields.RequestType, "the shared error envelope")
}

func TestRelayStreamRunsDeferredTransportPostHooks(t *testing.T) {
	var slot atomic.Value
	ran := false
	slot.Store(func() ([]schemas.PluginLogEntry, error) {
		ran = true
		return []schemas.PluginLogEntry{{PluginName: "transport"}}, nil
	})
	logs := runTransportPostHookCompleter(&slot, nil)
	assert.True(t, ran, "the transport post-hooks run once the stream ends")
	assert.Len(t, logs, 1)

	var empty atomic.Value
	assert.Nil(t, runTransportPostHookCompleter(&empty, nil), "no completer published means no transport plugins to run")
}

// serveRelayGatewayHTTP runs the gateway on a real socket, as a relayed stream needs one: the body is
// written by fasthttp after the handler returns, and a heartbeat needs a connection to probe.
func serveRelayGatewayHTTP(t *testing.T, h *RelayGatewayHandler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &fasthttp.Server{Handler: h.handle}
	go server.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { _ = server.Shutdown() })
	return "http://" + ln.Addr().String()
}

func streamingRelay(t *testing.T, name, upstreamURL string) *RelayGatewayHandler {
	t.Helper()
	manager := relay.NewManager(nil, nil)
	require.NoError(t, manager.Upsert(&schemas.RelayConfig{
		Name: name, BaseURL: upstreamURL,
		Auth:       schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer},
		Credential: &schemas.RelayCredential{Value: schemas.NewSecretVar("sk-endpoint")},
		Routes:     []schemas.RelayRouteConfig{{Method: "POST", Path: "/s", Stream: schemas.Ptr(true)}},
	}))
	return NewRelayGatewayHandler(manager, nil, nil)
}

// A relayed SSE stream carries the comment heartbeat a passthrough SSE stream carries while the
// upstream is quiet; any other stream is forwarded byte for byte, with nothing added.
func TestRelayStreamHeartbeatFollowsPassthrough(t *testing.T) {
	previous := relayHeartbeatInterval
	relayHeartbeatInterval = 20 * time.Millisecond
	t.Cleanup(func() { relayHeartbeatInterval = previous })
	for _, tc := range []struct {
		contentType string
		heartbeat   bool
	}{{"text/event-stream; charset=utf-8", true}, {"application/octet-stream", false}} {
		t.Run(tc.contentType, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = io.WriteString(w, "data: one\n\n")
				w.(http.Flusher).Flush()
				time.Sleep(200 * time.Millisecond)
				_, _ = io.WriteString(w, "data: two\n\n")
			}))
			defer upstream.Close()
			base := serveRelayGatewayHTTP(t, streamingRelay(t, "sse", upstream.URL))
			resp, err := http.Post(base+"/relay/sse/s", "application/json", strings.NewReader(`{}`))
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			text := strings.ReplaceAll(string(body), ": heartbeat\n", "")
			assert.Equal(t, "data: one\n\ndata: two\n\n", text, "the upstream's bytes arrive unchanged")
			assert.Equal(t, tc.heartbeat, strings.Contains(string(body), ": heartbeat\n"))
		})
	}
}

// A caller that hangs up during a quiet stream is noticed by the heartbeat and the upstream request
// is cancelled, as a passthrough stream's is, rather than held open until the idle timeout.
func TestRelayStreamCancelsUpstreamWhenCallerLeaves(t *testing.T) {
	previous := relayHeartbeatInterval
	relayHeartbeatInterval = 20 * time.Millisecond
	t.Cleanup(func() { relayHeartbeatInterval = previous })
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(10 * time.Second):
		}
	}))
	defer upstream.Close()
	base := serveRelayGatewayHTTP(t, streamingRelay(t, "quiet", upstream.URL))
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	require.NoError(t, err)
	_, err = io.WriteString(conn, "POST /relay/quiet/s HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}")
	require.NoError(t, err)
	buf := make([]byte, 512)
	_, err = conn.Read(buf)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream request was not cancelled after the caller left")
	}
}

// panickingRelayPipeline panics in the post-hook, as a faulty plugin would.
type panickingRelayPipeline struct{}

func (panickingRelayPipeline) RunRelayPreHooks(_ *schemas.BifrostContext, req *schemas.BifrostRelayRequest, _ func(int)) (*schemas.BifrostRelayRequest, *schemas.RelayPluginShortCircuit, int) {
	return req, nil, 0
}

func (panickingRelayPipeline) RunRelayPostHooks(*schemas.BifrostContext, *schemas.BifrostRelayResponse, *schemas.BifrostError, int) (*schemas.BifrostRelayResponse, *schemas.BifrostError) {
	panic("post-hook failed")
}

// A panic on a relay stream's goroutines (here a post-hook, which runs when the upstream ends)
// ends that stream, not the process, and the caller still gets the bytes and the end of the body.
func TestRelayStreamSurvivesPanicOnItsGoroutines(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
	}))
	defer upstream.Close()
	h := streamingRelay(t, "boom", upstream.URL)
	h.manager.SetPluginPipeline(func() relay.PluginPipeline { return panickingRelayPipeline{} }, func(relay.PluginPipeline) {})
	base := serveRelayGatewayHTTP(t, h)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(base+"/relay/boom/s", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "data: one\n\n", string(body))
}

// BenchmarkRelayStreamForward measures one relayed SSE stream end to end on a real socket: the
// producer copies each 4 KB read into a chunk and the writer hands it to the caller, as a
// passthrough stream does, holding at most schemas.DefaultStreamBufferSize chunks ahead of a slow
// caller.
func BenchmarkRelayStreamForward(b *testing.B) {
	payload := []byte(strings.Repeat("data: {\"delta\":\"abcdefghijklmnopqrstuvwxyz\"}\n\n", 64))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()
	manager := relay.NewManager(nil, nil)
	if err := manager.Upsert(&schemas.RelayConfig{
		Name: "bench", BaseURL: upstream.URL,
		Auth:       schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer},
		Credential: &schemas.RelayCredential{Value: schemas.NewSecretVar("sk-endpoint")},
		Routes:     []schemas.RelayRouteConfig{{Method: "POST", Path: "/s", Stream: schemas.Ptr(true)}},
	}); err != nil {
		b.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	gateway := NewRelayGatewayHandler(manager, nil, nil)
	// Publishes the transport post-hook completer after the handler returns, as
	// TransportInterceptorMiddleware does, so the stream does not wait out the bounded poll.
	server := &fasthttp.Server{Handler: func(ctx *fasthttp.RequestCtx) {
		gateway.handle(ctx)
		if slot, ok := ctx.UserValue(schemas.BifrostContextKeyTransportPostHookCompleter).(*atomic.Value); ok {
			slot.Store(func() ([]schemas.PluginLogEntry, error) { return nil, nil })
		}
	}}
	go server.Serve(ln)     //nolint:errcheck
	defer server.Shutdown() //nolint:errcheck
	url := "http://" + ln.Addr().String() + "/relay/bench/s"
	client := &http.Client{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := client.Post(url, "application/json", strings.NewReader(`{}`))
		if err != nil {
			b.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if len(body) != len(payload) {
			b.Fatalf("got %d bytes, want %d", len(body), len(payload))
		}
	}
}

// relayHeadersRequest builds the request headers a typical relay call carries; connection is the
// Connection header value, omitted when empty.
func relayHeadersRequest(connection string) *fasthttp.RequestHeader {
	var h fasthttp.RequestHeader
	h.SetMethod(fasthttp.MethodPost)
	h.SetRequestURI("/relay/tavily/search")
	h.SetHost("gateway.example.com")
	h.SetContentType("application/json")
	h.SetUserAgent("relay-client/1.0")
	h.Set("Authorization", "Bearer sk-bf-virtual-key")
	h.Set("Accept", "text/event-stream")
	h.Set("X-Custom", "kept")
	if connection != "" {
		h.Set("Connection", connection)
	}
	return &h
}

// A header the caller names in Connection is hop-by-hop (RFC 9110 §7.6.1) and never forwarded,
// matched case-insensitively and with the whitespace around each token ignored.
func TestRelayForwardHeadersDropsConnectionTokens(t *testing.T) {
	h := relayHeadersRequest(" keep-alive ,  X-Hop-One,x-hop-TWO , ,")
	h.Set("X-Hop-One", "1")
	h.Set("X-Hop-Two", "2")
	h.Set("X-Hop-Three", "3")
	got := relayForwardHeaders(h)
	assert.NotContains(t, got, "x-hop-one")
	assert.NotContains(t, got, "x-hop-two")
	assert.Equal(t, "3", got["x-hop-three"], "a header the Connection header does not name is forwarded")
	assert.Equal(t, "kept", got["x-custom"])
	assert.Equal(t, "text/event-stream", got["accept"])
	assert.NotContains(t, got, "connection")
	assert.NotContains(t, got, "authorization")
	assert.NotContains(t, got, "host")
}

// Reading the Connection header costs nothing on a request whose Connection header names only
// tokens the relay drops anyway (keep-alive, close) or that has none: the header is parsed in
// place, never split into strings or a set.
func TestRelayForwardHeadersConnectionParseDoesNotAllocate(t *testing.T) {
	without := relayHeadersRequest("")
	keepAlive := relayHeadersRequest("keep-alive")
	closing := relayHeadersRequest("close")
	base := testing.AllocsPerRun(100, func() { _ = relayForwardHeaders(without) })
	assert.Equal(t, base, testing.AllocsPerRun(100, func() { _ = relayForwardHeaders(keepAlive) }), "Connection: keep-alive allocates nothing extra")
	assert.Equal(t, base, testing.AllocsPerRun(100, func() { _ = relayForwardHeaders(closing) }), "Connection: close allocates nothing extra")
}

// BenchmarkRelayForwardHeaders measures collecting the forwarded headers for a typical relay call,
// which every relay request pays before it is served.
func BenchmarkRelayForwardHeaders(b *testing.B) {
	for _, connection := range []string{"", "keep-alive", "keep-alive, X-Hop"} {
		name := connection
		if name == "" {
			name = "none"
		}
		b.Run(name, func(b *testing.B) {
			h := relayHeadersRequest(connection)
			h.Set("X-Hop", "1")
			b.ReportAllocs()
			for b.Loop() {
				_ = relayForwardHeaders(h)
			}
		})
	}
}

// recordingRelayPipeline keeps the error the post-hooks are given, which is what the log row records.
type recordingRelayPipeline struct {
	err *atomic.Pointer[schemas.BifrostError]
}

func (recordingRelayPipeline) RunRelayPreHooks(_ *schemas.BifrostContext, req *schemas.BifrostRelayRequest, _ func(int)) (*schemas.BifrostRelayRequest, *schemas.RelayPluginShortCircuit, int) {
	return req, nil, 0
}

func (p recordingRelayPipeline) RunRelayPostHooks(_ *schemas.BifrostContext, resp *schemas.BifrostRelayResponse, bifrostErr *schemas.BifrostError, _ int) (*schemas.BifrostRelayResponse, *schemas.BifrostError) {
	if bifrostErr != nil {
		p.err.Store(bifrostErr)
	}
	return resp, bifrostErr
}

// rawRelayCall sends a relay request on a raw connection that asks the gateway to close it after the
// reply, and returns every byte the gateway wrote before the connection closed.
func rawRelayCall(t *testing.T, base, path string) string {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
	_, err = io.WriteString(conn, "POST "+path+" HTTP/1.1\r\nHost: x\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}")
	require.NoError(t, err)
	raw, err := io.ReadAll(conn)
	require.NoError(t, err, "the gateway closes the connection once the reply ends")
	return string(raw)
}

// A relayed stream the upstream fails partway (a reset, an idle timeout) is cut off without the
// chunked body's terminating chunk, so the caller's HTTP client reports an incomplete reply instead
// of a complete one; the bytes forwarded before the failure still reach the caller, and the log row
// still records the error. A stream the upstream completes ends with the terminating chunk.
func TestRelayStreamAbortsCallerWhenUpstreamFails(t *testing.T) {
	const terminator = "\r\n0\r\n\r\n"
	for _, tc := range []struct {
		name     string
		idle     int
		upstream func(w http.ResponseWriter, r *http.Request)
		fails    bool
	}{
		{name: "upstream completes", upstream: func(w http.ResponseWriter, r *http.Request) {}},
		{name: "upstream resets", fails: true, upstream: func(w http.ResponseWriter, r *http.Request) {
			panic(http.ErrAbortHandler) // closes the connection without the terminating chunk
		}},
		{name: "upstream goes idle", idle: 1, fails: true, upstream: func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: one\n\n")
				w.(http.Flusher).Flush()
				tc.upstream(w, r)
			}))
			defer upstream.Close()
			manager := relay.NewManager(nil, nil)
			cfg := &schemas.RelayConfig{
				Name: "flaky", BaseURL: upstream.URL,
				Auth:       schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer},
				Credential: &schemas.RelayCredential{Value: schemas.NewSecretVar("sk-endpoint")},
				Routes:     []schemas.RelayRouteConfig{{Method: "POST", Path: "/s", Stream: schemas.Ptr(true)}},
			}
			if tc.idle > 0 {
				cfg.Network = &schemas.RelayNetworkConfig{StreamIdleTimeoutInSeconds: tc.idle}
			}
			require.NoError(t, manager.Upsert(cfg))
			var recorded atomic.Pointer[schemas.BifrostError]
			pipeline := recordingRelayPipeline{err: &recorded}
			manager.SetPluginPipeline(func() relay.PluginPipeline { return pipeline }, func(relay.PluginPipeline) {})
			base := serveRelayGatewayHTTP(t, NewRelayGatewayHandler(manager, nil, nil))

			raw := rawRelayCall(t, base, "/relay/flaky/s")
			require.True(t, strings.HasPrefix(raw, "HTTP/1.1 200"), raw)
			assert.Contains(t, raw, "data: one\n\n", "the bytes forwarded before the end reach the caller")
			if !tc.fails {
				assert.True(t, strings.HasSuffix(raw, terminator), "a completed stream ends with the terminating chunk: %q", raw)
				assert.Nil(t, recorded.Load())
				return
			}
			assert.NotContains(t, raw, terminator, "a failed stream is cut off without the terminating chunk: %q", raw)
			assert.NotNil(t, recorded.Load(), "the log row records the upstream failure")

			// What an HTTP client sees: the reply is reported incomplete, never as a clean end.
			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Post(base+"/relay/flaky/s", "application/json", strings.NewReader(`{}`))
			require.NoError(t, err)
			defer resp.Body.Close()
			_, err = io.ReadAll(resp.Body)
			assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
		})
	}
}
