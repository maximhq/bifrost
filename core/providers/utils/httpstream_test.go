package utils

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

func TestStreamingHTTPTwin_StableAcrossCalls(t *testing.T) {
	t.Parallel()

	fh := &fasthttp.Client{Dial: func(string) (net.Conn, error) { return nil, net.ErrClosed }}
	a := StreamingHTTPTwin(fh)
	b := StreamingHTTPTwin(fh)
	if a != b {
		t.Fatal("StreamingHTTPTwin returned a different client for the same fasthttp client: " +
			"the cache would grow once per request and leak a connection pool each time")
	}
	if a.Transport == nil {
		t.Fatal("twin has no transport")
	}
}

// TestStreamingHTTPTwin_CarriesDialerAndTLS asserts the twin inherits the two
// fields that hold every network policy Bifrost applies: ConfigureProxy and
// ConfigureDialer compose proxying, SSRF filtering and keepalive into Dial, and
// ConfigureTLS puts the custom CA / InsecureSkipVerify into TLSConfig. Losing
// either would silently disable SSRF protection or certificate pinning.
func TestStreamingHTTPTwin_CarriesDialerAndTLS(t *testing.T) {
	t.Parallel()

	dialed := make(chan string, 1)
	fh := &fasthttp.Client{
		Dial: func(addr string) (net.Conn, error) {
			select {
			case dialed <- addr:
			default:
			}
			return nil, net.ErrClosed
		},
		MaxConnsPerHost:     7,
		MaxIdleConnDuration: 42 * time.Second,
	}
	fh = ConfigureTLS(fh, schemas.NetworkConfig{InsecureSkipVerify: true}, getLogger())

	twin := StreamingHTTPTwin(fh)
	tr, ok := twin.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", twin.Transport)
	}
	if tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("twin lost the fasthttp client's TLS config")
	}
	if tr.MaxConnsPerHost != 7 {
		t.Fatalf("MaxConnsPerHost = %d, want 7", tr.MaxConnsPerHost)
	}
	if tr.IdleConnTimeout != 42*time.Second {
		t.Fatalf("IdleConnTimeout = %v, want 42s", tr.IdleConnTimeout)
	}
	if tr.DialContext == nil {
		t.Fatal("twin lost the fasthttp client's Dial function")
	}
	//nolint:errcheck // the dial deliberately fails; we only assert it was routed
	_, _ = tr.DialContext(t.Context(), "tcp", "example.invalid:443")
	select {
	case addr := <-dialed:
		if addr != "example.invalid:443" {
			t.Fatalf("dialed %q, want example.invalid:443", addr)
		}
	default:
		t.Fatal("twin did not route the dial through the fasthttp Dial function")
	}
}

// TestFastHTTPRequestToHTTP_PreservesRequest covers the conversion details that
// silently break providers if wrong: method, URI, body, ordinary headers, and
// the Connection: close intent several providers rely on.
func TestFastHTTPRequestToHTTP_PreservesRequest(t *testing.T) {
	t.Parallel()

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.SetRequestURI("https://api.example.com/v1/chat/completions?beta=1")
	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Connection", "close")
	req.SetBodyString(`{"stream":true}`)

	hr, err := fastHTTPRequestToHTTP(t.Context(), req)
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	if hr.Method != http.MethodPost {
		t.Fatalf("method %q, want POST", hr.Method)
	}
	if got := hr.URL.String(); got != "https://api.example.com/v1/chat/completions?beta=1" {
		t.Fatalf("url %q", got)
	}
	if hr.Header.Get("Authorization") != "Bearer token" {
		t.Fatal("lost Authorization header")
	}
	if hr.Header.Get("Accept") != "text/event-stream" {
		t.Fatal("lost Accept header")
	}
	if !hr.Close {
		t.Fatal("Connection: close did not become Request.Close, so a torn connection could be reused")
	}
	if hr.Header.Get("Connection") != "" {
		t.Fatal("Connection header was forwarded verbatim; net/http must own framing")
	}
	if hr.ContentLength != int64(len(`{"stream":true}`)) {
		t.Fatalf("ContentLength = %d", hr.ContentLength)
	}
}

// TestDoStreamingRequestViaHTTP_PropagatesStatusAndHeaders guards the error
// path: a non-200 streaming response must still carry status and headers so the
// provider error converters and ExtractProviderResponseHeaders keep working.
func TestDoStreamingRequestViaHTTP_PropagatesStatusAndHeaders(t *testing.T) {
	t.Parallel()

	ln := fasthttputil.NewInmemoryListener()
	defer ln.Close() //nolint:errcheck
	srv := &fasthttp.Server{
		Handler: func(ctx *fasthttp.RequestCtx) {
			ctx.SetStatusCode(fasthttp.StatusTooManyRequests)
			ctx.Response.Header.Set("X-Ratelimit-Remaining", "0")
			ctx.SetContentType("application/json")
			ctx.SetBodyString(`{"error":{"message":"slow down"}}`)
		},
	}
	go srv.Serve(ln) //nolint:errcheck

	client := &fasthttp.Client{Dial: func(string) (net.Conn, error) { return ln.Dial() }}

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()
	req.SetRequestURI("http://test/v1/chat/completions")
	req.Header.SetMethod(fasthttp.MethodPost)
	resp.StreamBody = true

	ctx := schemas.NewBifrostContext(t.Context(), time.Time{})
	if err := DoStreamingRequestViaHTTP(ctx, client, req, resp); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer ReleaseStreamingResponse(ctx, resp)

	if resp.StatusCode() != fasthttp.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", resp.StatusCode())
	}
	if got := string(resp.Header.Peek("X-Ratelimit-Remaining")); got != "0" {
		t.Fatalf("X-Ratelimit-Remaining = %q, want 0", got)
	}
	if headers := ExtractProviderResponseHeaders(resp); len(headers) == 0 {
		t.Fatal("ExtractProviderResponseHeaders returned nothing for a net/http-backed response")
	}
}

// TestInjectHTTPStreamResponse_PreservesContentLength guards the ordering inside
// injectHTTPStreamResponse. SetBodyStream sets Content-Length to -1, so the
// header copy has to run after it. FinalizeResponseWithLargeDetection branches
// on resp.Header.ContentLength() to decide whether a response is "large", and a
// clobbered -1 would push every sized response down the unknown-length path.
func TestInjectHTTPStreamResponse_PreservesContentLength(t *testing.T) {
	t.Parallel()

	ln := fasthttputil.NewInmemoryListener()
	defer ln.Close() //nolint:errcheck
	body := `{"ok":true}`
	srv := &fasthttp.Server{
		Handler: func(ctx *fasthttp.RequestCtx) {
			ctx.SetStatusCode(fasthttp.StatusOK)
			ctx.SetContentType("application/json")
			ctx.SetBodyString(body)
		},
	}
	go srv.Serve(ln) //nolint:errcheck

	client := &fasthttp.Client{Dial: func(string) (net.Conn, error) { return ln.Dial() }}
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()
	req.SetRequestURI("http://test/v1/models")
	req.Header.SetMethod(fasthttp.MethodGet)
	resp.StreamBody = true

	ctx := schemas.NewBifrostContext(t.Context(), time.Time{})
	if err := DoStreamingRequestViaHTTP(ctx, client, req, resp); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer ReleaseStreamingResponse(ctx, resp)

	if got := resp.Header.ContentLength(); got != len(body) {
		t.Fatalf("ContentLength = %d, want %d: the header copy must run after SetBodyStream", got, len(body))
	}
}
