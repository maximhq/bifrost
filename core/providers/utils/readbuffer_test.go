package utils

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

// newLargeHeaderTestServer creates an in-memory fasthttp server that responds with
// response headers larger than fasthttp's default 4KB client read buffer, mimicking
// upstreams behind Cloudflare that attach large Set-Cookie and CSP headers.
// Returns a dial function for clients to connect to it and a cleanup function.
func newLargeHeaderTestServer(t *testing.T) (func(addr string) (net.Conn, error), func()) {
	t.Helper()
	ln := fasthttputil.NewInmemoryListener()

	server := &fasthttp.Server{
		Handler: func(ctx *fasthttp.RequestCtx) {
			// ~6KB cookie + ~2KB CSP header, comfortably above the 4096-byte default
			ctx.Response.Header.Set("Set-Cookie", "ph_bootstrap="+strings.Repeat("a", 6*1024)+"; Path=/; Secure")
			ctx.Response.Header.Set("Content-Security-Policy", strings.Repeat("connect-src https://example.com ", 64))
			ctx.SetStatusCode(fasthttp.StatusOK)
			ctx.SetBody([]byte(`{"ok":true}`))
		},
	}

	go server.Serve(ln) //nolint:errcheck

	return func(addr string) (net.Conn, error) {
			return ln.Dial()
		}, func() {
			ln.Close()
		}
}

// TestMakeRequestWithContext_LargeResponseHeaders verifies that provider clients
// configured with schemas.DefaultClientReadBufferSize can read responses whose
// headers exceed fasthttp's 4KB default read buffer (see issue #4264), while a
// client left at the default fails with a small-read-buffer error.
func TestMakeRequestWithContext_LargeResponseHeaders(t *testing.T) {
	dial, cleanup := newLargeHeaderTestServer(t)
	defer cleanup()

	t.Run("default_read_buffer_fails", func(t *testing.T) {
		client := &fasthttp.Client{
			Dial:        dial,
			ReadTimeout: 5 * time.Second,
		}

		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		req.SetRequestURI("http://test/")

		_, bifrostErr, wait := MakeRequestWithContext(context.Background(), client, req, resp)
		defer wait()

		if bifrostErr == nil {
			t.Fatal("expected small read buffer error with fasthttp's default ReadBufferSize, got success")
		}
		// GetErrorString() is not used here: it returns only the generic
		// classification message (ErrProviderDoRequest); the fasthttp
		// "small read buffer" detail lives in the wrapped Error.Error.
		errText := bifrostErr.Error.Message
		if bifrostErr.Error.Error != nil {
			errText += " " + bifrostErr.Error.Error.Error()
		}
		if !strings.Contains(errText, "small read buffer") {
			t.Fatalf("expected small read buffer error, got: %s", errText)
		}
	})

	t.Run("default_client_read_buffer_size_succeeds", func(t *testing.T) {
		client := &fasthttp.Client{
			Dial:           dial,
			ReadTimeout:    5 * time.Second,
			ReadBufferSize: schemas.DefaultClientReadBufferSize,
		}

		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		req.SetRequestURI("http://test/")

		_, bifrostErr, wait := MakeRequestWithContext(context.Background(), client, req, resp)
		defer wait()

		if bifrostErr != nil {
			t.Fatalf("expected success with %d-byte read buffer, got: %v", schemas.DefaultClientReadBufferSize, bifrostErr.Error.Message)
		}
		if resp.StatusCode() != fasthttp.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode())
		}
	})
}
