package utils

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

const rewriteToken = "@@T0@@"

// spliceRewriter replaces every occurrence of rewriteToken in the provider-built
// body with payload, streaming over sub-slices (io.MultiReader, nothing
// materialised). A body without the token is sent unchanged.
func spliceRewriter(payload []byte, calls *atomic.Int32) RequestBodyRewriter {
	token := []byte(rewriteToken)
	return func(body []byte) (io.Reader, int64, error) {
		calls.Add(1)
		if !bytes.Contains(body, token) {
			return nil, 0, nil
		}
		var parts []io.Reader
		var size int64
		rest := body
		for {
			i := bytes.Index(rest, token)
			if i < 0 {
				parts = append(parts, bytes.NewReader(rest))
				size += int64(len(rest))
				break
			}
			parts = append(parts, bytes.NewReader(rest[:i]), bytes.NewReader(payload))
			size += int64(i + len(payload))
			rest = rest[i+len(token):]
		}
		return io.MultiReader(parts...), size, nil
	}
}

func rewriterCtx(rw RequestBodyRewriter) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	ctx.SetValue(schemas.BifrostContextKeyRequestBodyRewriter, rw)
	return ctx
}

// wireRecorder is an upstream that records every request whose body arrived
// complete (as many bytes as its Content-Length), plus every handler entry.
type wireRecorder struct {
	srv     *httptest.Server
	mu      sync.Mutex
	bodies  []string
	lengths []string
	entered atomic.Int32
	sse     bool
}

func newWireRecorder(t *testing.T, sse bool) *wireRecorder {
	t.Helper()
	w := &wireRecorder{sse: sse}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.entered.Add(1)
		b, err := io.ReadAll(r.Body)
		if err != nil || int64(len(b)) != r.ContentLength {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		w.mu.Lock()
		w.bodies = append(w.bodies, string(b))
		w.lengths = append(w.lengths, r.Header.Get("Content-Length"))
		w.mu.Unlock()
		if w.sse {
			rw.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(rw, "data: {}\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(rw, "ok")
	}))
	t.Cleanup(w.srv.Close)
	return w
}

func (w *wireRecorder) complete() ([]string, []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.bodies...), append([]string(nil), w.lengths...)
}

func fastClient() *fasthttp.Client {
	return ConfigureDialer(&fasthttp.Client{
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}, true)
}

func newPost(url, body string) *fasthttp.Request {
	req := fasthttp.AcquireRequest()
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.SetBodyString(body)
	return req
}

func sendUnary(ctx context.Context, c *fasthttp.Client, req *fasthttp.Request) *schemas.BifrostError {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	_, berr, wait := MakeRequestWithContext(ctx, c, req, resp)
	wait()
	return berr
}

func sendStream(ctx *schemas.BifrostContext, c *fasthttp.Client, req *fasthttp.Request) error {
	resp := fasthttp.AcquireResponse()
	if err := DoStreamingRequest(ctx, c, req, resp); err != nil {
		fasthttp.ReleaseResponse(resp)
		return err
	}
	_, _ = io.ReadAll(resp.BodyStream())
	ReleaseStreamingResponse(ctx, resp)
	return nil
}

// T1.1 + T1.2: a 1 MiB splice on the unary and the streaming client; the same
// req sent twice splices afresh each time and is never mutated.
func TestRequestBodyRewriter_SplicesUnaryAndStreamingAndResends(t *testing.T) {
	payload := strings.Repeat("x", 1<<20)
	skeleton := `{"messages":[{"content":"` + rewriteToken + `"}]}`
	want := `{"messages":[{"content":"` + payload + `"}]}`

	for _, streaming := range []bool{false, true} {
		t.Run("streaming="+strconv.FormatBool(streaming), func(t *testing.T) {
			var calls atomic.Int32
			ctx := rewriterCtx(spliceRewriter([]byte(payload), &calls))
			up := newWireRecorder(t, streaming)
			client := fastClient()
			if streaming {
				client = BuildStreamingClient(client)
			}
			req := newPost(up.srv.URL+"/v1/chat/completions", skeleton)
			t.Cleanup(func() { fasthttp.ReleaseRequest(req) })

			for range 2 {
				if streaming {
					require.NoError(t, sendStream(ctx, client, req))
				} else {
					require.Nil(t, sendUnary(ctx, client, req))
				}
			}

			assert.Equal(t, skeleton, string(req.Body()), "req must not be mutated")
			assert.Equal(t, int32(2), calls.Load(), "each send must invoke the rewriter afresh")
			bodies, lengths := up.complete()
			require.Len(t, bodies, 2)
			for i := range bodies {
				assert.True(t, bodies[i] == want, "send %d: upstream got %d bytes, want %d", i, len(bodies[i]), len(want))
				assert.Equal(t, strconv.Itoa(len(want)), lengths[i])
			}
		})
	}
}

// staleConnServer serves HTTP/1.1 on loopback and closes the first connection
// right after its first response (keep-alive advertised), so the client pools a
// socket the upstream already closed. closed fires once that happened.
type staleConnServer struct {
	ln     net.Listener
	closed chan struct{}
	mu     sync.Mutex
	bodies []string
}

func newStaleConnServer(t *testing.T) *staleConnServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &staleConnServer{ln: ln, closed: make(chan struct{})}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		first := true
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, first)
			first = false
		}
	}()
	return s
}

func (s *staleConnServer) serve(conn net.Conn, closeAfterFirst bool) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		r, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.bodies = append(s.bodies, string(b))
		s.mu.Unlock()
		if _, err := io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"); err != nil {
			return
		}
		if closeAfterFirst {
			_ = conn.Close()
			close(s.closed)
			return
		}
	}
}

// T1.3: fasthttp's stale-connection retry re-enters RoundTrip with the same req;
// the rewriter runs again and the retried body is spliced.
func TestRequestBodyRewriter_StaleConnectionRetrySplicesAfresh(t *testing.T) {
	payload := strings.Repeat("s", 64<<10)
	var calls atomic.Int32
	ctx := rewriterCtx(spliceRewriter([]byte(payload), &calls))
	srv := newStaleConnServer(t)
	client := fastClient()
	client.MaxConnsPerHost = 1
	url := "http://" + srv.ln.Addr().String() + "/v1/chat/completions"

	first := newPost(url, `{"warm":true}`)
	t.Cleanup(func() { fasthttp.ReleaseRequest(first) })
	require.Nil(t, sendUnary(ctx, client, first))
	select {
	case <-srv.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never closed the pooled connection")
	}
	calls.Store(0)

	req := newPost(url, `{"c":"`+rewriteToken+`"}`)
	t.Cleanup(func() { fasthttp.ReleaseRequest(req) })
	require.Nil(t, sendUnary(ctx, client, req))

	assert.GreaterOrEqual(t, calls.Load(), int32(2), "the retried RoundTrip must re-invoke the rewriter")
	srv.mu.Lock()
	defer srv.mu.Unlock()
	require.Len(t, srv.bodies, 2)
	assert.True(t, srv.bodies[1] == `{"c":"`+payload+`"}`, "retried body was not spliced (%d bytes)", len(srv.bodies[1]))
}

func newHTTPPost(ctx context.Context, t *testing.T, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(body)))
	require.NoError(t, err)
	return req
}

// T1.4: net/http splice, GetBody replay splices afresh, nil reader sends the
// body unchanged, and a non-replayable body fails closed before client.Do.
func TestRequestBodyRewriter_NetHTTP(t *testing.T) {
	payload := strings.Repeat("y", 1<<20)
	skeleton := `{"c":"` + rewriteToken + `"}`
	want := `{"c":"` + payload + `"}`

	t.Run("splice and replay", func(t *testing.T) {
		var calls atomic.Int32
		ctx := rewriterCtx(spliceRewriter([]byte(payload), &calls))
		up := newWireRecorder(t, false)
		req := newHTTPPost(ctx, t, up.srv.URL, skeleton)
		resp, err := DoHTTPRequest(up.srv.Client(), req)
		require.NoError(t, err)
		_ = resp.Body.Close()

		bodies, lengths := up.complete()
		require.Len(t, bodies, 1)
		assert.True(t, bodies[0] == want, "upstream got %d bytes, want %d", len(bodies[0]), len(want))
		assert.Equal(t, strconv.Itoa(len(want)), lengths[0])
		assert.Equal(t, int64(len(want)), req.ContentLength)

		before := calls.Load()
		replay, err := req.GetBody()
		require.NoError(t, err)
		got, err := io.ReadAll(replay)
		require.NoError(t, err)
		assert.True(t, string(got) == want, "replay yielded %d bytes, want %d", len(got), len(want))
		assert.Equal(t, before+1, calls.Load(), "GetBody must re-invoke the rewriter")
	})

	t.Run("nil reader sends unchanged", func(t *testing.T) {
		var calls atomic.Int32
		ctx := rewriterCtx(spliceRewriter([]byte(payload), &calls))
		up := newWireRecorder(t, false)
		req := newHTTPPost(ctx, t, up.srv.URL, `{"c":"plain"}`)
		resp, err := DoHTTPRequest(up.srv.Client(), req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		bodies, _ := up.complete()
		assert.Equal(t, []string{`{"c":"plain"}`}, bodies)
		assert.Equal(t, int32(1), calls.Load())
	})

	t.Run("GetBody nil fails closed", func(t *testing.T) {
		var calls atomic.Int32
		ctx := rewriterCtx(spliceRewriter([]byte(payload), &calls))
		up := newWireRecorder(t, false)
		req := newHTTPPost(ctx, t, up.srv.URL, skeleton)
		req.GetBody = nil
		_, err := DoHTTPRequest(up.srv.Client(), req)
		require.ErrorIs(t, err, ErrRequestBodyRewrite)
		assert.Equal(t, int32(0), up.entered.Load(), "client.Do must not be called")
		assert.Equal(t, int32(0), calls.Load())
	})

	t.Run("replay that cannot splice fails", func(t *testing.T) {
		var n atomic.Int32
		ctx := rewriterCtx(func(body []byte) (io.Reader, int64, error) {
			switch n.Add(1) {
			case 1:
				return bytes.NewReader([]byte(want)), int64(len(want)), nil
			case 2:
				return nil, 0, nil
			default:
				return nil, 0, errors.New("token mutated")
			}
		})
		req := newHTTPPost(ctx, t, "http://127.0.0.1:1", skeleton)
		require.NoError(t, applyRequestBodyRewriterHTTP(req))
		_, err := req.GetBody()
		require.ErrorIs(t, err, ErrRequestBodyRewrite, "a nil reader on replay must not fall back to the skeleton")
		_, err = req.GetBody()
		require.ErrorIs(t, err, ErrRequestBodyRewrite)
	})
}

// T1.6 (transport half): a rewriter error, or a negative size, aborts the
// attempt before any byte reaches the listener, on both transports.
func TestRequestBodyRewriter_AbortSendsNothing(t *testing.T) {
	errMutated := errors.New("token mutated")
	cases := map[string]RequestBodyRewriter{
		"error": func([]byte) (io.Reader, int64, error) { return nil, 0, errMutated },
		"negative size": func([]byte) (io.Reader, int64, error) {
			return strings.NewReader("x"), -1, nil
		},
	}
	for name, rw := range cases {
		t.Run(name, func(t *testing.T) {
			var accepted atomic.Int32
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { _ = ln.Close() })
			received := make(chan int, 4)
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					accepted.Add(1)
					go func() {
						defer conn.Close()
						b, _ := io.ReadAll(conn)
						received <- len(b)
					}()
				}
			}()
			url := "http://" + ln.Addr().String() + "/x"
			ctx := rewriterCtx(rw)

			req := newPost(url, `{"c":"`+rewriteToken+`"}`)
			t.Cleanup(func() { fasthttp.ReleaseRequest(req) })
			berr := sendUnary(ctx, fastClient(), req)
			require.NotNil(t, berr)
			require.NotNil(t, berr.Error)
			assert.ErrorIs(t, berr.Error.Error, ErrRequestBodyRewrite)
			if name == "error" {
				assert.ErrorIs(t, berr.Error.Error, errMutated)
			}

			hreq := newHTTPPost(ctx, t, url, `{"c":"`+rewriteToken+`"}`)
			_, err = DoHTTPRequest(&http.Client{}, hreq)
			require.ErrorIs(t, err, ErrRequestBodyRewrite)

			_ = ln.Close()
			for i := int32(0); i < accepted.Load(); i++ {
				select {
				case n := <-received:
					assert.Equal(t, 0, n, "no byte may reach the listener")
				case <-time.After(5 * time.Second):
					t.Fatal("connection never closed")
				}
			}
		})
	}
}

type flakyReader struct {
	data []byte
	err  error
}

func (f *flakyReader) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

// T1.7: a reader that is short, long, or fails mid-body aborts with
// ErrRequestBodyRewrite on both transports and the upstream never receives a
// complete body.
func TestRequestBodyRewriter_SizeMismatchFailsClosed(t *testing.T) {
	body := bytes.Repeat([]byte("z"), 256<<10)
	cases := map[string]RequestBodyRewriter{
		"short": func([]byte) (io.Reader, int64, error) {
			return bytes.NewReader(body[:len(body)-1]), int64(len(body)), nil
		},
		"long": func([]byte) (io.Reader, int64, error) {
			return bytes.NewReader(body), int64(len(body) - 1), nil
		},
		"read error": func([]byte) (io.Reader, int64, error) {
			return &flakyReader{data: body[:len(body)/2], err: errors.New("disk on fire")}, int64(len(body)), nil
		},
	}
	for name, rw := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := rewriterCtx(rw)
			up := newWireRecorder(t, false)

			req := newPost(up.srv.URL, `{"c":"`+rewriteToken+`"}`)
			t.Cleanup(func() { fasthttp.ReleaseRequest(req) })
			berr := sendUnary(ctx, fastClient(), req)
			require.NotNil(t, berr)
			assert.ErrorIs(t, berr.Error.Error, ErrRequestBodyRewrite, "fasthttp")

			hreq := newHTTPPost(ctx, t, up.srv.URL, `{"c":"`+rewriteToken+`"}`)
			resp, err := DoHTTPRequest(up.srv.Client(), hreq)
			if resp != nil {
				_ = resp.Body.Close()
			}
			require.Error(t, err, "net/http")
			assert.ErrorIs(t, err, ErrRequestBodyRewrite, "net/http")

			bodies, _ := up.complete()
			assert.Empty(t, bodies, "the upstream must never receive a complete body")
		})
	}
}

// T1.8: a request whose body is already a stream is sent verbatim and the
// rewriter is not invoked.
func TestRequestBodyRewriter_BodyStreamSkipped(t *testing.T) {
	var calls atomic.Int32
	ctx := rewriterCtx(spliceRewriter([]byte("never"), &calls))
	up := newWireRecorder(t, false)
	raw := `{"c":"` + rewriteToken + `"}`
	req := fasthttp.AcquireRequest()
	t.Cleanup(func() { fasthttp.ReleaseRequest(req) })
	req.SetRequestURI(up.srv.URL)
	req.Header.SetMethod(http.MethodPost)
	req.SetBodyStream(strings.NewReader(raw), len(raw))
	require.Nil(t, sendUnary(ctx, fastClient(), req))
	bodies, _ := up.complete()
	assert.Equal(t, []string{raw}, bodies)
	assert.Equal(t, int32(0), calls.Load())
}

// T1.10: without the key, writeRequestWithRewriter writes exactly what
// req.Write writes, with the same allocations.
func TestRequestBodyRewriter_NoKeyIsReqWrite(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	build := func() *fasthttp.Request {
		req := newPost("http://upstream.example/v1/chat/completions", `{"c":"`+rewriteToken+`"}`)
		req.Header.Set("Authorization", "Bearer k")
		return req
	}

	var viaHook, viaWrite bytes.Buffer
	r1, r2 := build(), build()
	t.Cleanup(func() { fasthttp.ReleaseRequest(r1); fasthttp.ReleaseRequest(r2) })
	bw := bufio.NewWriter(&viaHook)
	require.NoError(t, writeRequestWithRewriter(ctx, r1, bw))
	require.NoError(t, bw.Flush())
	bw = bufio.NewWriter(&viaWrite)
	require.NoError(t, r2.Write(bw))
	require.NoError(t, bw.Flush())
	assert.Equal(t, viaWrite.String(), viaHook.String())

	bw = bufio.NewWriter(io.Discard)
	hookAllocs := testing.AllocsPerRun(200, func() {
		_ = writeRequestWithRewriter(ctx, r1, bw)
		_ = bw.Flush()
	})
	writeAllocs := testing.AllocsPerRun(200, func() {
		_ = r2.Write(bw)
		_ = bw.Flush()
	})
	assert.Equal(t, writeAllocs, hookAllocs)
}

type closeCountingReader struct {
	io.Reader
	closes *atomic.Int32
}

func (c *closeCountingReader) Close() error {
	c.closes.Add(1)
	return nil
}

// T1.11: core never closes the embedder's reader, on either transport.
func TestRequestBodyRewriter_ReaderNeverClosed(t *testing.T) {
	var closes atomic.Int32
	want := `{"c":"spliced"}`
	ctx := rewriterCtx(func([]byte) (io.Reader, int64, error) {
		return &closeCountingReader{Reader: strings.NewReader(want), closes: &closes}, int64(len(want)), nil
	})
	up := newWireRecorder(t, false)

	req := newPost(up.srv.URL, `{"c":"`+rewriteToken+`"}`)
	t.Cleanup(func() { fasthttp.ReleaseRequest(req) })
	require.Nil(t, sendUnary(ctx, fastClient(), req))

	hreq := newHTTPPost(ctx, t, up.srv.URL, `{"c":"`+rewriteToken+`"}`)
	resp, err := DoHTTPRequest(up.srv.Client(), hreq)
	require.NoError(t, err)
	_ = resp.Body.Close()

	bodies, _ := up.complete()
	assert.Equal(t, []string{want, want}, bodies)
	assert.Equal(t, int32(0), closes.Load())
}

// RewrittenBodyDigest hashes and sizes the spliced bytes, and falls back to the
// body itself without a rewriter or when the rewriter declines.
func TestRewrittenBodyDigest(t *testing.T) {
	payload := strings.Repeat("d", 4096)
	skeleton := []byte(`{"c":"` + rewriteToken + `"}`)
	spliced := []byte(`{"c":"` + payload + `"}`)
	var calls atomic.Int32
	ctx := rewriterCtx(spliceRewriter([]byte(payload), &calls))

	gotHash, gotSize, err := RewrittenBodyDigest(ctx, skeleton)
	require.NoError(t, err)
	wantHash, wantSize, err := RewrittenBodyDigest(context.Background(), spliced)
	require.NoError(t, err)
	assert.Equal(t, wantHash, gotHash)
	assert.Equal(t, int64(len(spliced)), gotSize)
	assert.Equal(t, wantSize, gotSize)

	plainHash, plainSize, err := RewrittenBodyDigest(ctx, []byte(`{}`))
	require.NoError(t, err)
	refHash, _, _ := RewrittenBodyDigest(context.Background(), []byte(`{}`))
	assert.Equal(t, refHash, plainHash)
	assert.Equal(t, int64(2), plainSize)

	_, _, err = RewrittenBodyDigest(rewriterCtx(func([]byte) (io.Reader, int64, error) {
		return strings.NewReader("ab"), 3, nil
	}), skeleton)
	assert.ErrorIs(t, err, ErrRequestBodyRewrite)

	assert.Nil(t, RequestBodyRewriterFromContext(nil)) //nolint:staticcheck // nil ctx is part of the contract
	wrongType := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestBodyRewriter, "not a rewriter")
	assert.Nil(t, RequestBodyRewriterFromContext(wrongType))
}

// A rewriter abort on a connection reused from the pool is not a stale socket:
// the reader's own io.ErrUnexpectedEOF must not trigger fasthttp's
// stale-connection retry, so the rewriter runs exactly once.
func TestRequestBodyRewriter_AbortOnReusedConnNotRetried(t *testing.T) {
	up := newWireRecorder(t, false)
	client := fastClient()
	client.MaxConnsPerHost = 1

	warm := newPost(up.srv.URL, `{"warm":true}`)
	t.Cleanup(func() { fasthttp.ReleaseRequest(warm) })
	require.Nil(t, sendUnary(context.Background(), client, warm))

	body := bytes.Repeat([]byte("u"), 64<<10)
	var calls atomic.Int32
	ctx := rewriterCtx(func([]byte) (io.Reader, int64, error) {
		calls.Add(1)
		return &flakyReader{data: body[:len(body)/2], err: io.ErrUnexpectedEOF}, int64(len(body)), nil
	})
	req := newPost(up.srv.URL, `{"c":"`+rewriteToken+`"}`)
	t.Cleanup(func() { fasthttp.ReleaseRequest(req) })
	berr := sendUnary(ctx, client, req)
	require.NotNil(t, berr)
	assert.ErrorIs(t, berr.Error.Error, ErrRequestBodyRewrite)
	assert.Equal(t, int32(1), calls.Load(), "a rewrite abort must not be retried as a stale connection")
	bodies, _ := up.complete()
	assert.Len(t, bodies, 1, "only the warm-up request may arrive complete")
}

// A reader announcing size 0 but yielding bytes is "long" like any other: the
// transports never read a zero-size body, so it must be caught up front, on
// both transports and in the SigV4 digest.
func TestRequestBodyRewriter_ZeroSizeLongReaderFailsClosed(t *testing.T) {
	rw := RequestBodyRewriter(func([]byte) (io.Reader, int64, error) {
		return strings.NewReader("extra"), 0, nil
	})
	ctx := rewriterCtx(rw)
	up := newWireRecorder(t, false)

	req := newPost(up.srv.URL, `{"c":"`+rewriteToken+`"}`)
	t.Cleanup(func() { fasthttp.ReleaseRequest(req) })
	berr := sendUnary(ctx, fastClient(), req)
	require.NotNil(t, berr)
	assert.ErrorIs(t, berr.Error.Error, ErrRequestBodyRewrite, "fasthttp")

	hreq := newHTTPPost(ctx, t, up.srv.URL, `{"c":"`+rewriteToken+`"}`)
	resp, err := DoHTTPRequest(up.srv.Client(), hreq)
	if resp != nil {
		_ = resp.Body.Close()
	}
	assert.ErrorIs(t, err, ErrRequestBodyRewrite, "net/http")

	_, _, err = RewrittenBodyDigest(ctx, []byte(`{}`))
	assert.ErrorIs(t, err, ErrRequestBodyRewrite, "digest")

	assert.Equal(t, int32(0), up.entered.Load(), "nothing may reach the upstream")

	// An empty reader with size 0 is a legitimate empty body.
	empty := rewriterCtx(func([]byte) (io.Reader, int64, error) { return strings.NewReader(""), 0, nil })
	_, size, err := RewrittenBodyDigest(empty, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, int64(0), size)
}
