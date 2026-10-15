package lib

import (
	"bytes"
	"net"
	"sync"
	"testing"

	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

// TestResolveBodyBufferPoolLimit covers default, explicit, clamped and disabled values.
func TestResolveBodyBufferPoolLimit(t *testing.T) {
	const maxBody = 100 * 1024 * 1024
	tests := []struct {
		name    string
		cfg     *ServerConfig
		maxBody int
		want    int
	}{
		{name: "nil config uses default", cfg: nil, maxBody: maxBody, want: DefaultBodyBufferPoolLimitBytes},
		{name: "unset uses default", cfg: &ServerConfig{}, maxBody: maxBody, want: DefaultBodyBufferPoolLimitBytes},
		{name: "explicit value", cfg: &ServerConfig{BodyBufferPoolLimitBytes: 16 << 20}, maxBody: maxBody, want: 16 << 20},
		{name: "clamped to max body size", cfg: &ServerConfig{BodyBufferPoolLimitBytes: 512 << 20}, maxBody: maxBody, want: maxBody},
		{name: "default clamped to small max body size", cfg: &ServerConfig{}, maxBody: 1 << 20, want: 1 << 20},
		{name: "no max body size leaves value", cfg: &ServerConfig{BodyBufferPoolLimitBytes: 512 << 20}, maxBody: 0, want: 512 << 20},
		{name: "disabled", cfg: &ServerConfig{BodyBufferPoolLimitBytes: -1}, maxBody: maxBody, want: BodyBufferPoolLimitDisabled},
		{name: "any negative disables", cfg: &ServerConfig{BodyBufferPoolLimitBytes: -42}, maxBody: maxBody, want: BodyBufferPoolLimitDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveBodyBufferPoolLimit(tt.cfg, tt.maxBody); got != tt.want {
				t.Fatalf("ResolveBodyBufferPoolLimit() = %d, want %d", got, tt.want)
			}
		})
	}
}

// bodyCapServer runs a real fasthttp server whose handler records the capacity of
// each request's body buffer, so tests can observe what a pooled ctx retained.
type bodyCapServer struct {
	ln     *fasthttputil.InmemoryListener
	client *fasthttp.HostClient
	mu     sync.Mutex
	caps   []int
}

// newBodyCapServer starts the server and a single-connection client so sequential
// requests share one keep-alive connection (and therefore one RequestCtx).
func newBodyCapServer(t *testing.T) *bodyCapServer {
	t.Helper()
	s := &bodyCapServer{ln: fasthttputil.NewInmemoryListener()}
	srv := &fasthttp.Server{
		MaxRequestBodySize: 256 << 20,
		Handler: func(ctx *fasthttp.RequestCtx) {
			s.mu.Lock()
			s.caps = append(s.caps, cap(ctx.Request.Body()))
			s.mu.Unlock()
		},
	}
	go srv.Serve(s.ln) //nolint:errcheck
	s.client = &fasthttp.HostClient{
		Addr:     "bodycap",
		MaxConns: 1,
		Dial:     func(string) (net.Conn, error) { return s.ln.Dial() },
	}
	t.Cleanup(func() {
		_ = srv.Shutdown()
		_ = s.ln.Close()
	})
	return s
}

// send posts a body of the given size, optionally closing the connection afterwards.
func (s *bodyCapServer) send(t *testing.T, size int, closeConn bool) {
	t.Helper()
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI("http://bodycap/v1/responses")
	req.Header.SetMethod(fasthttp.MethodPost)
	req.SetBody(bytes.Repeat([]byte{'a'}, size))
	if closeConn {
		req.SetConnectionClose()
	}
	if err := s.client.Do(req, resp); err != nil {
		t.Fatalf("request of %d bytes failed: %v", size, err)
	}
}

// lastCap returns the body buffer capacity seen by the most recent request.
func (s *bodyCapServer) lastCap() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caps[len(s.caps)-1]
}

// withBodyBufferPoolLimit applies the limit for one test and restores fasthttp's
// process-global default afterwards so other tests are unaffected.
func withBodyBufferPoolLimit(t *testing.T, limit int) {
	t.Helper()
	ApplyBodyBufferPoolLimit(&ServerConfig{BodyBufferPoolLimitBytes: limit}, 256<<20)
	t.Cleanup(func() { fasthttp.SetBodySizePoolLimit(-1, -1) })
}

// TestBodyBufferPoolLimitReleasesLargeBuffersOnKeepAlive is the regression for
// per-connection retention: after a 32MB body, the next small request on the same
// keep-alive connection must not still hold a 32MB buffer.
func TestBodyBufferPoolLimitReleasesLargeBuffersOnKeepAlive(t *testing.T) {
	withBodyBufferPoolLimit(t, 4<<20)
	s := newBodyCapServer(t)
	s.send(t, 32<<20, false)
	if got := s.lastCap(); got < 32<<20 {
		t.Fatalf("32MB request saw buffer cap %d, want >= 32MB", got)
	}
	s.send(t, 1024, false)
	if got := s.lastCap(); got > 4<<20 {
		t.Fatalf("small request after 32MB on the same connection kept buffer cap %d, want <= 4MB", got)
	}
}

// TestBodyBufferPoolLimitKeepsBuffersUnderLimit checks the reuse fast path is kept
// for bodies that fit under the limit.
func TestBodyBufferPoolLimitKeepsBuffersUnderLimit(t *testing.T) {
	withBodyBufferPoolLimit(t, 4<<20)
	s := newBodyCapServer(t)
	s.send(t, 3<<20, false)
	s.send(t, 1024, false)
	if got := s.lastCap(); got < 3<<20 {
		t.Fatalf("small request after 3MB kept buffer cap %d, want >= 3MB (buffer reused)", got)
	}
}

// TestBodyBufferPoolLimitReleasesOnConnectionClose checks a ctx returned to the pool
// at connection close does not carry its large buffer to the next connection.
func TestBodyBufferPoolLimitReleasesOnConnectionClose(t *testing.T) {
	withBodyBufferPoolLimit(t, 4<<20)
	s := newBodyCapServer(t)
	s.send(t, 32<<20, true)
	s.send(t, 1024, false)
	if got := s.lastCap(); got > 4<<20 {
		t.Fatalf("request on a new connection inherited buffer cap %d, want <= 4MB", got)
	}
}

// TestBodyBufferPoolLimitDisabledKeepsLegacyBehavior pins that -1 restores
// fasthttp's default of keeping the high-water-mark buffer.
func TestBodyBufferPoolLimitDisabledKeepsLegacyBehavior(t *testing.T) {
	fasthttp.SetBodySizePoolLimit(-1, -1)
	withBodyBufferPoolLimit(t, BodyBufferPoolLimitDisabled)
	s := newBodyCapServer(t)
	s.send(t, 32<<20, false)
	s.send(t, 1024, false)
	if got := s.lastCap(); got < 32<<20 {
		t.Fatalf("disabled limit released buffer: cap %d, want >= 32MB", got)
	}
}

// BenchmarkBodyBufferPoolLimit measures the per-request cost of releasing body buffers
// above the limit (fresh allocation each time) versus reusing the retained buffer, on a
// real fasthttp server over one keep-alive connection.
func BenchmarkBodyBufferPoolLimit(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"1KB", 1 << 10},
		{"1MB", 1 << 20},
		{"4MB", 4 << 20},
		{"16MB", 16 << 20},
		{"64MB", 64 << 20},
	}
	modes := []struct {
		name  string
		limit int
	}{
		{"retain", BodyBufferPoolLimitDisabled},
		{"limit4MB", 4 << 20},
	}
	for _, sz := range sizes {
		for _, mode := range modes {
			b.Run(sz.name+"/"+mode.name, func(b *testing.B) {
				fasthttp.SetBodySizePoolLimit(-1, -1)
				if mode.limit != BodyBufferPoolLimitDisabled {
					fasthttp.SetBodySizePoolLimit(mode.limit, -1)
				}
				b.Cleanup(func() { fasthttp.SetBodySizePoolLimit(-1, -1) })
				ln := fasthttputil.NewInmemoryListener()
				srv := &fasthttp.Server{MaxRequestBodySize: 256 << 20, Handler: func(ctx *fasthttp.RequestCtx) {}}
				go srv.Serve(ln) //nolint:errcheck
				b.Cleanup(func() { _ = srv.Shutdown(); _ = ln.Close() })
				client := &fasthttp.HostClient{Addr: "bench", MaxConns: 1, Dial: func(string) (net.Conn, error) { return ln.Dial() }}
				body := bytes.Repeat([]byte{'a'}, sz.size)
				req := fasthttp.AcquireRequest()
				resp := fasthttp.AcquireResponse()
				defer fasthttp.ReleaseRequest(req)
				defer fasthttp.ReleaseResponse(resp)
				req.SetRequestURI("http://bench/v1/responses")
				req.Header.SetMethod(fasthttp.MethodPost)
				req.SetBodyRaw(body)
				b.SetBytes(int64(sz.size))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := client.Do(req, resp); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
