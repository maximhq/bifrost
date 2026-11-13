package handlers

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

const uiTestShell = `<!doctype html>
<html lang="en">
	<head>
		<link rel="icon" type="image/x-icon" href="/favicon.ico" />
		<script>var up = "<img class=l src=/bifrost-logo.webp>";</script>
		<script type="module" crossorigin src="/assets/index-abc.js"></script>
		<link rel="modulepreload" crossorigin href="/assets/chunk-def.js">
	</head>
	<body><a href="https://getbifrost.ai">Bifrost</a><a href="//cdn.example">cdn</a><img src="/bifrost-logo.webp" /></body>
</html>`

// uiTestFS returns an in-memory UI bundle: the uiTestShell page, a JS asset and
// a favicon.
func uiTestFS() fstest.MapFS {
	return fstest.MapFS{
		"ui/index.html":          {Data: []byte(uiTestShell)},
		"ui/assets/index-abc.js": {Data: []byte("console.log(1)")},
		"ui/favicon.ico":         {Data: []byte("ico")},
	}
}

// startDevUIProxy serves the development proxy on a real loopback connection.
func startDevUIProxy(t testing.TB, upstreamAddr string) string {
	t.Helper()
	h := &UIHandler{uiDevClient: &fasthttp.HostClient{
		Addr: upstreamAddr, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second,
	}}
	server := &fasthttp.Server{Handler: func(ctx *fasthttp.RequestCtx) {
		if !h.serveDevDashboard(ctx) {
			ctx.Error("embedded fallback", fasthttp.StatusServiceUnavailable)
		}
	}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		h.uiDevClient.CloseIdleConnections()
		_ = server.Shutdown()
	})
	return "http://" + listener.Addr().String()
}

func TestUIHandler_DevWebSocket(t *testing.T) {
	for _, termination := range []string{"client_close", "client_disconnect", "upstream_close", "upstream_disconnect"} {
		t.Run(termination, func(t *testing.T) {
			connections := make(chan *websocket.Conn, 1)
			upgrader := websocket.Upgrader{Subprotocols: []string{"vite-hmr"}, EnableCompression: true}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !websocket.IsWebSocketUpgrade(r) {
					_, _ = io.WriteString(w, "normal HTTP")
					return
				}
				assert.Equal(t, "/hmr?token=test", r.URL.RequestURI())
				assert.Equal(t, "test-header", r.Header.Get("X-Test"))
				conn, err := upgrader.Upgrade(w, r, nil)
				if assert.NoError(t, err) {
					connections <- conn
				}
			}))
			t.Cleanup(upstream.Close)
			proxyURL := startDevUIProxy(t, strings.TrimPrefix(upstream.URL, "http://"))
			dialer := websocket.Dialer{Subprotocols: []string{"vite-hmr"}, EnableCompression: true, HandshakeTimeout: 5 * time.Second}
			client, response, err := dialer.Dial(strings.Replace(proxyURL, "http://", "ws://", 1)+"/hmr?token=test", http.Header{"X-Test": {"test-header"}})
			require.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })
			require.Equal(t, http.StatusSwitchingProtocols, response.StatusCode)
			require.Equal(t, "vite-hmr", client.Subprotocol())
			var backend *websocket.Conn
			select {
			case backend = <-connections:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream did not upgrade")
			}
			t.Cleanup(func() { _ = backend.Close() })
			require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
			require.NoError(t, backend.SetReadDeadline(time.Now().Add(5*time.Second)))

			// This GET must use an HTTP connection, not the upgraded Vite socket.
			httpClient := &http.Client{Timeout: 5 * time.Second}
			checkHTTP := func() {
				t.Helper()
				resp, err := httpClient.Get(proxyURL + "/?repro=1")
				require.NoError(t, err)
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.Equal(t, "normal HTTP", string(body))
			}
			checkHTTP()
			text := []byte(strings.Repeat("HMR update ", 100))
			require.NoError(t, client.WriteMessage(websocket.TextMessage, text))
			kind, payload, err := backend.ReadMessage()
			require.NoError(t, err, "upstream must receive frames, never an HTTP GET")
			require.Equal(t, websocket.TextMessage, kind)
			require.Equal(t, text, payload)
			binary := []byte{0, 1, 127, 128, 255}
			require.NoError(t, backend.WriteMessage(websocket.BinaryMessage, binary))
			kind, payload, err = client.ReadMessage()
			require.NoError(t, err)
			require.Equal(t, websocket.BinaryMessage, kind)
			require.Equal(t, binary, payload)

			closing, peer := client, backend
			if strings.HasPrefix(termination, "upstream") {
				closing, peer = backend, client
			}
			if strings.HasSuffix(termination, "disconnect") {
				require.NoError(t, closing.Close())
			} else {
				require.NoError(t, closing.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"), time.Now().Add(time.Second)))
			}
			_, _, err = peer.ReadMessage()
			var closeErr *websocket.CloseError
			require.ErrorAs(t, err, &closeErr, "peer must close rather than time out")
			if strings.HasSuffix(termination, "disconnect") {
				require.Equal(t, websocket.CloseAbnormalClosure, closeErr.Code)
			} else {
				require.Equal(t, websocket.CloseNormalClosure, closeErr.Code)
				// Read the close acknowledgement, proving close frames flow both ways.
				_, _, err = closing.ReadMessage()
				require.True(t, websocket.IsCloseError(err, websocket.CloseNormalClosure), "%v", err)
			}
			checkHTTP()
		})
	}
}

// serveUITest sends a GET for path through the handler's dashboard route and
// returns the request context holding the response.
func serveUITest(h *UIHandler, path string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI(path)
	h.serveDashboard(ctx)
	return ctx
}

// TestUIHandler_NoBasePathServesShellUnchanged pins the default: the shell is
// served byte-for-byte as embedded and no rewritten copies are kept.
func TestUIHandler_NoBasePathServesShellUnchanged(t *testing.T) {
	h := NewUIHandler(uiTestFS(), nil, "")
	assert.Nil(t, h.basePathShells)
	for _, path := range []string{"/", "/workspace/logs"} {
		ctx := serveUITest(h, path)
		assert.Equal(t, uiTestShell, string(ctx.Response.Body()), path)
	}
}

// TestUIHandler_BasePathRewritesShell checks root-absolute src/href refs gain the
// prefix (quoted and unquoted), external and protocol-relative URLs do not, and
// the runtime global is set at the top of <head>, for the root and SPA routes.
func TestUIHandler_BasePathRewritesShell(t *testing.T) {
	h := NewUIHandler(uiTestFS(), nil, "/tools/llm-gw")
	for _, path := range []string{"/", "/workspace/logs", "/login"} {
		body := string(serveUITest(h, path).Response.Body())
		assert.Contains(t, body, `<head><script>window.__BIFROST_BASE_PATH__="/tools/llm-gw"</script>`, path)
		assert.Contains(t, body, `href="/tools/llm-gw/favicon.ico"`, path)
		assert.Contains(t, body, `src=/tools/llm-gw/bifrost-logo.webp>`, path)
		assert.Contains(t, body, `src="/tools/llm-gw/assets/index-abc.js"`, path)
		assert.Contains(t, body, `href="/tools/llm-gw/assets/chunk-def.js"`, path)
		assert.Contains(t, body, `<img src="/tools/llm-gw/bifrost-logo.webp" />`, path)
		assert.Contains(t, body, `href="https://getbifrost.ai"`, path)
		assert.Contains(t, body, `href="//cdn.example"`, path)
		assert.NotContains(t, body, `"/assets/`, path)
	}
	// Static assets are served untouched; the prefix is stripped before routing.
	assert.Equal(t, "console.log(1)", string(serveUITest(h, "/assets/index-abc.js").Response.Body()))
}

// TestUIHandler_NonCanonicalBasePathNotSpliced pins that the shell rewrite
// only splices a value lib.NormalizeBasePath returns unchanged. Anything else
// (markup, a trailing slash, a missing leading slash) is refused and the shell
// is served as embedded, so a caller that skips normalization cannot inject
// into the HTML or the inline script.
func TestUIHandler_NonCanonicalBasePathNotSpliced(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, basePath := range []string{`/x"><script>alert(1)</script>`, `/x";alert(1);//`, "/bifrost/", "bifrost", "/a/../b"} {
		h := NewUIHandler(uiTestFS(), nil, basePath)
		assert.Nil(t, h.basePathShells, basePath)
		assert.Equal(t, uiTestShell, string(serveUITest(h, "/").Response.Body()), basePath)
	}
}

// TestUIHandler_BasePathShellRewriterSeesPrefixedShell checks the build's
// ShellRewriter still runs, on the base-path-adjusted document.
func TestUIHandler_BasePathShellRewriterSeesPrefixedShell(t *testing.T) {
	var seen string
	rewriter := func(_ *fasthttp.RequestCtx, data []byte) []byte {
		seen = string(data)
		return []byte(strings.ToUpper(string(data)))
	}
	h := NewUIHandler(uiTestFS(), rewriter, "/bifrost")
	body := string(serveUITest(h, "/").Response.Body())
	assert.Contains(t, seen, `window.__BIFROST_BASE_PATH__="/bifrost"`)
	assert.Contains(t, body, `WINDOW.__BIFROST_BASE_PATH__="/BIFROST"`)
}

// TestUIHandler_BasePathShellRewriterCannotMutateCache checks a ShellRewriter
// that edits its input in place does not change the cached base-path shell
// that later requests are served from.
func TestUIHandler_BasePathShellRewriterCannotMutateCache(t *testing.T) {
	var seen []string
	rewriter := func(_ *fasthttp.RequestCtx, data []byte) []byte {
		seen = append(seen, string(data))
		copy(data, "MUTATED")
		return data
	}
	h := NewUIHandler(uiTestFS(), rewriter, "/bifrost")
	serveUITest(h, "/")
	serveUITest(h, "/workspace/logs")
	if assert.Len(t, seen, 2) {
		assert.True(t, strings.HasPrefix(seen[1], "<!doctype html>"), "second request saw a mutated shell: %.20q", seen[1])
	}
}

// TestApplyBasePathToShell_HeadWithAttributes checks the runtime global goes
// right after an opening <head> that carries attributes, not before the
// doctype, and that a <header> element is not mistaken for <head>.
func TestApplyBasePathToShell_HeadWithAttributes(t *testing.T) {
	script := `<script>window.__BIFROST_BASE_PATH__="/b"</script>`
	for _, head := range []string{`<head lang="en">`, "<head\n\tdata-theme=\"dark\">", `<head>`} {
		doc := "<!doctype html><html>" + head + "<title>x</title></head><body><header>h</header></body></html>"
		out := string(applyBasePathToShell([]byte(doc), "/b"))
		assert.True(t, strings.HasPrefix(out, "<!doctype html>"), head)
		assert.Contains(t, out, head+script, head)
	}
	out := string(applyBasePathToShell([]byte(`<html><body><header>h</header></body></html>`), "/b"))
	assert.True(t, strings.HasPrefix(out, script), "no <head>: falls back to document start")
	assert.Contains(t, out, "<header>h</header>")
}

// TestApplyBasePathToShell_QuoteStyles pins that root-absolute refs get the
// prefix whether the attribute is double-quoted, single-quoted or unquoted,
// keeping the original quote, while protocol-relative refs are left alone.
func TestApplyBasePathToShell_QuoteStyles(t *testing.T) {
	doc := `<head></head><script src="/assets/a.js"></script><script src='/assets/b.js'></script><img src=/c.webp><a href='//cdn.example/x'>`
	out := string(applyBasePathToShell([]byte(doc), "/b"))
	assert.Contains(t, out, `src="/b/assets/a.js"`)
	assert.Contains(t, out, `src='/b/assets/b.js'`)
	assert.Contains(t, out, `src=/b/c.webp>`)
	assert.Contains(t, out, `href='//cdn.example/x'`)
}

// BenchmarkUIHandler_ServeShell measures the per-request cost of serving the
// HTML shell: at the domain root, under a base path (one map load), and under a
// base path with a ShellRewriter (which also clones the cached shell), plus a
// static asset for comparison.
func BenchmarkUIHandler_ServeShell(b *testing.B) {
	identity := func(_ *fasthttp.RequestCtx, data []byte) []byte { return data }
	cases := []struct {
		name string
		h    *UIHandler
		path string
	}{
		{"root", NewUIHandler(uiTestFS(), nil, ""), "/workspace/logs"},
		{"prefixed", NewUIHandler(uiTestFS(), nil, "/bifrost"), "/workspace/logs"},
		{"prefixed_rewriter", NewUIHandler(uiTestFS(), identity, "/bifrost"), "/workspace/logs"},
		{"asset", NewUIHandler(uiTestFS(), nil, "/bifrost"), "/assets/index-abc.js"},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI(c.path)
			b.ReportAllocs()
			for b.Loop() {
				ctx.Response.Reset()
				c.h.serveDashboard(ctx)
			}
		})
	}
}

func TestServeDashboardAPIPathsReturnJSONNotFound(t *testing.T) {
	h := NewUIHandler(uiTestFS(), nil, "")

	for _, path := range []string{"/api", "/api/logs", "/api/logs/stats", "/api/mcp-logs/anything"} {
		ctx := serveUITest(h, path)

		assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode(), path)
		assert.True(t, strings.HasPrefix(string(ctx.Response.Header.ContentType()), "application/json"), path)
		assert.Contains(t, string(ctx.Response.Body()), "Route not found: "+path, path)
	}
}

func TestServeDashboardKeepsNonAPIPathsOnTheSPAPath(t *testing.T) {
	h := NewUIHandler(uiTestFS(), nil, "")

	for _, path := range []string{"/apiary", "/dashboard/api-keys"} {
		body := string(serveUITest(h, path).Response.Body())
		assert.NotContains(t, body, "Route not found", path)
	}
}

// Both peers may send their first frame in the same packet as the HTTP headers.
func TestUIHandler_DevWebSocketBufferedFrames(t *testing.T) {
	clientFrame := []byte{0x81, 0x82, 0, 0, 0, 0, 'h', 'i'}
	serverFrame := []byte{0x81, 5, 'r', 'e', 'a', 'd', 'y'}
	received := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if !assert.NoError(t, err) {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, err = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n%s", serverFrame)
		if !assert.NoError(t, err) {
			return
		}
		frame := make([]byte, len(clientFrame))
		if _, err := io.ReadFull(buffered.Reader, frame); assert.NoError(t, err) {
			received <- frame
		}
	}))
	t.Cleanup(upstream.Close)
	proxyURL := startDevUIProxy(t, strings.TrimPrefix(upstream.URL, "http://"))
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(proxyURL, "http://"), 5*time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	request := "GET / HTTP/1.1\r\nHost: localhost:8080\r\nConnection: keep-alive, uPgRaDe\r\nUpgrade: WebSocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"
	_, err = conn.Write(append([]byte(request), clientFrame...))
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusSwitchingProtocols, response.StatusCode)
	frame := make([]byte, len(serverFrame))
	_, err = io.ReadFull(reader, frame)
	require.NoError(t, err)
	require.Equal(t, serverFrame, frame)
	select {
	case frame := <-received:
		require.Equal(t, clientFrame, frame)
	case <-time.After(5 * time.Second):
		t.Fatal("buffered client frame was not forwarded")
	}
	_, err = reader.ReadByte()
	require.ErrorIs(t, err, io.EOF, "upstream disconnect must close the browser socket")
}

func TestUIHandler_DevWebSocketHandshakeFailure(t *testing.T) {
	for _, scenario := range []string{"rejected", "malformed", "unreachable"} {
		t.Run(scenario, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "rejected" {
					w.Header().Set("X-Upstream", "rejected")
					http.Error(w, "handshake rejected", http.StatusForbidden)
					return
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if assert.NoError(t, err) {
					_, _ = io.WriteString(conn, "not HTTP\r\n\r\n")
					_ = conn.Close()
				}
			}))
			t.Cleanup(upstream.Close)
			if scenario == "unreachable" {
				upstream.Close()
			}
			proxyURL := startDevUIProxy(t, strings.TrimPrefix(upstream.URL, "http://"))
			dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
			_, response, err := dialer.Dial(strings.Replace(proxyURL, "http://", "ws://", 1)+"/", nil)
			require.Error(t, err)
			require.NotNil(t, response)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			if scenario == "rejected" {
				require.Equal(t, http.StatusForbidden, response.StatusCode)
				require.Equal(t, "rejected", response.Header.Get("X-Upstream"))
				require.Contains(t, string(body), "handshake rejected")
			} else {
				require.Equal(t, http.StatusBadGateway, response.StatusCode)
				require.Contains(t, string(body), "Failed to proxy dev UI WebSocket")
			}
		})
	}
}

func TestUIHandler_DevProxyGuards(t *testing.T) {
	for _, scenario := range []string{"disabled", "remote"} {
		t.Run(scenario, func(t *testing.T) {
			h := &UIHandler{}
			remoteIP := net.IPv4(127, 0, 0, 1)
			if scenario == "remote" {
				h.uiDevClient = &fasthttp.HostClient{Addr: "127.0.0.1:1"}
				remoteIP = net.IPv4(192, 0, 2, 1)
			}
			var req fasthttp.Request
			req.SetRequestURI("http://localhost:8080/")
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
			var ctx fasthttp.RequestCtx
			ctx.Init(&req, &net.TCPAddr{IP: remoteIP}, nil)
			require.False(t, h.serveDevDashboard(&ctx))
			require.False(t, ctx.Hijacked())
		})
	}
}

// BenchmarkUIHandler_DevHTTP measures the unchanged HTTP path through the dev proxy.
func BenchmarkUIHandler_DevHTTP(b *testing.B) {
	for _, size := range []struct {
		name  string
		bytes int
	}{{"small", 512}, {"shell", 32768}} {
		body := bytes.Repeat([]byte("x"), size.bytes)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
		b.Cleanup(upstream.Close)
		h := &UIHandler{uiDevClient: &fasthttp.HostClient{Addr: strings.TrimPrefix(upstream.URL, "http://")}}
		b.Cleanup(h.uiDevClient.CloseIdleConnections)
		run := func(b *testing.B, parallel bool) {
			b.ReportAllocs()
			b.SetBytes(int64(size.bytes))
			call := func(ctx *fasthttp.RequestCtx) bool {
				ctx.Response.Reset()
				return h.serveDevDashboard(ctx) && ctx.Response.StatusCode() == fasthttp.StatusOK
			}
			makeCtx := func() *fasthttp.RequestCtx {
				var req fasthttp.Request
				req.SetRequestURI("http://localhost:8080/?benchmark=1")
				ctx := &fasthttp.RequestCtx{}
				ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}, nil)
				return ctx
			}
			if parallel {
				b.RunParallel(func(pb *testing.PB) {
					ctx := makeCtx()
					for pb.Next() {
						if !call(ctx) {
							b.Error("proxy failed")
							return
						}
					}
				})
			} else {
				ctx := makeCtx()
				for b.Loop() {
					if !call(ctx) {
						b.Fatal("proxy failed")
					}
				}
			}
		}
		b.Run(size.name+"/serial", func(b *testing.B) { run(b, false) })
		b.Run(size.name+"/parallel", func(b *testing.B) { run(b, true) })
	}
}

// devWebSocketEOFConn signals payload writes and lets the other relay read EOF.
type devWebSocketEOFConn struct {
	net.Conn
	eof     <-chan struct{}
	writing chan<- struct{}
}

func (c devWebSocketEOFConn) Read([]byte) (int, error) {
	<-c.eof
	return 0, io.EOF
}

func (c devWebSocketEOFConn) Write(data []byte) (int, error) {
	if len(data) > 0 && data[0] == 0 { // The test payload, not the HTTP handshake.
		c.writing <- struct{}{}
	}
	return c.Conn.Write(data)
}

func TestUIHandler_DevWebSocketBlockedWriteTerminates(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if !assert.NoError(t, err) {
			return
		}
		defer conn.Close()
		_, err = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		if !assert.NoError(t, err) {
			return
		}
		_, _ = conn.Write(bytes.Repeat([]byte{0}, 4096))
		<-release
	}))
	defer upstream.Close()
	defer close(release)
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	eof := make(chan struct{}, 1)
	defer close(eof)
	writing := make(chan struct{}, 1)
	done := make(chan error, 1)
	var req fasthttp.Request
	req.SetRequestURI(upstream.URL)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	go func() {
		done <- proxyDevWebSocket(devWebSocketEOFConn{Conn: client, eof: eof, writing: writing}, &req, strings.TrimPrefix(upstream.URL, "http://"))
	}()
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(5*time.Second)))
	response, err := http.ReadResponse(bufio.NewReaderSize(peer, 128), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, response.StatusCode)
	select {
	case <-writing:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not start writing its payload")
	}
	// The peer stops reading, so forwarding the upstream payload blocks. EOF in
	// the other direction must interrupt that write without waiting on the peer.
	eof <- struct{}{}
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("relay did not terminate while downstream Write was blocked")
	}
}

// TestServeDashboardMissingAssetBody pins the body the dashboard's chunk-failure probe
// (ui/lib/utils/versionSkew.ts MISSING_ASSET_BODY_PREFIX, and the inline script in
// ui/index.html) matches to tell a rollout apart from a proxy or extension blocking
// the file. Changing this text makes every upgrade show the "couldn't load" message.
func TestServeDashboardMissingAssetBody(t *testing.T) {
	h := NewUIHandler(uiTestFS(), nil, "")

	for _, path := range []string{"/assets/trending-up-CnicU8gss.js", "/assets/logs-A1.css"} {
		ctx := serveUITest(h, path)
		assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode(), path)
		assert.True(t, strings.HasPrefix(string(ctx.Response.Body()), "404 - Static asset not found"), path)
	}

	// A chunk the build does have is served, so the probe classifies its failure as blocked.
	ctx := serveUITest(h, "/assets/index-abc.js")
	assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
}
