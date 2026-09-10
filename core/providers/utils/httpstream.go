package utils

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/valyala/fasthttp"
)

// ---------------------------------------------------------------------------
// net/http transport for streaming responses (issue #6143)
//
// fasthttp's streaming close callback returns the pooled *requestStream and
// *bufio.Reader to their sync.Pools BEFORE closing the connection, and closing
// the connection is the only thing that unblocks a reader parked in
// (*requestStream).Read. A cancelled stream therefore always resumes its reader
// on an object another request may already own. That ordering lives inside
// fasthttp (client.go, transport.RoundTrip) and is identical in every release
// from v1.69.0 through v1.73.0, so no caller-side locking can fix it.
//
// net/http has neither problem. http.Response.Body guards its closed flag with
// a mutex (net/http.bodyEOFSignal) and never returns the body reader to a pool,
// so Close may race a Read safely. Sending streaming requests through net/http
// and injecting the resulting body into the fasthttp.Response via SetBodyStream
// keeps every downstream helper working unchanged, while
// Response.closeBodyStream takes its io.Closer branch and skips
// releaseRequestStream entirely.
// ---------------------------------------------------------------------------

// streamingHTTPTwins maps a long-lived streaming *fasthttp.Client to the
// *http.Client that mirrors its dialer, proxy, TLS and pool settings.
//
// Keyed on the fasthttp client pointer, which is safe here because every client
// reaching this path is a provider's streamingClient field, built once in the
// provider constructor. Per-request clones (BuildLargeResponseClient) must not
// be used as keys; those paths resolve the twin from their long-lived base via
// StreamingHTTPTwin.
var streamingHTTPTwins sync.Map // *fasthttp.Client -> *http.Client

// StreamingHTTPTwin returns the net/http client mirroring fh's connection
// configuration, creating and caching it on first use.
//
// Everything that matters carries over through two fields. ConfigureProxy and
// ConfigureDialer compose proxy dialing, SSRF address filtering and TCP
// keepalive into fh.Dial, and ConfigureTLS puts the custom CA and
// InsecureSkipVerify into fh.TLSConfig. Mapping those onto Transport.DialContext
// and Transport.TLSClientConfig preserves all of it with no duplicated logic.
func StreamingHTTPTwin(fh *fasthttp.Client) *http.Client {
	if fh == nil {
		return &http.Client{}
	}
	if c, ok := streamingHTTPTwins.Load(fh); ok {
		return c.(*http.Client)
	}

	tr := &http.Transport{
		TLSClientConfig: fh.TLSConfig,
		// fasthttp speaks HTTP/1.1 only. Opportunistically negotiating h2 here
		// would change framing behaviour for every provider at once.
		ForceAttemptHTTP2: false,
		// DecompressStreamBody inspects Content-Encoding and unwraps gzip with
		// a pooled reader. net/http must not add its own Accept-Encoding or
		// transparently unwrap, or that logic would see an already-decoded body
		// with the header stripped.
		DisableCompression: true,
	}
	if fh.Dial != nil {
		dial := fh.Dial
		tr.DialContext = func(_ context.Context, _, addr string) (net.Conn, error) {
			return dial(addr)
		}
	}
	if fh.MaxConnsPerHost > 0 {
		tr.MaxConnsPerHost = fh.MaxConnsPerHost
		tr.MaxIdleConnsPerHost = fh.MaxConnsPerHost
	}
	if fh.MaxIdleConnDuration > 0 {
		tr.IdleConnTimeout = fh.MaxIdleConnDuration
	}

	c := &http.Client{
		Transport: tr,
		// No Timeout: a stream lives arbitrarily long and Client.Timeout would
		// cap the whole body read. Idle detection stays with
		// NewIdleTimeoutReader, matching BuildStreamingClient's contract.
		//
		// fasthttp's client.Do does not follow redirects (DoRedirects does), so
		// stopping at the first response preserves current behaviour.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	actual, _ := streamingHTTPTwins.LoadOrStore(fh, c)
	return actual.(*http.Client)
}

// fastHTTPRequestToHTTP converts a populated fasthttp request into an
// equivalent net/http request bound to ctx, so cancelling ctx aborts the
// connection, the send and the body read (see net/http.NewRequestWithContext:
// "the context controls the entire lifetime of a request and its response").
func fastHTTPRequestToHTTP(ctx context.Context, req *fasthttp.Request) (*http.Request, error) {
	if req == nil {
		return nil, fmt.Errorf("nil fasthttp request")
	}
	uri := req.URI().String()
	method := string(req.Header.Method())

	var body io.Reader
	if b := req.Body(); len(b) > 0 {
		body = bytes.NewReader(b)
	}

	hr, err := http.NewRequestWithContext(ctx, method, uri, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		hr.ContentLength = int64(len(req.Body()))
	}

	req.Header.VisitAll(func(k, v []byte) {
		key := string(k)
		switch http.CanonicalHeaderKey(key) {
		case "Host":
			// net/http carries the authority in Request.Host, and rejects it as
			// a normal header.
			hr.Host = string(v)
		case "Connection":
			// net/http computes the Connection header itself and expresses
			// "close" through Request.Close instead. Several providers set
			// Connection: close on streaming requests to stop a torn connection
			// from being reused, so the intent has to survive the conversion.
			if strings.EqualFold(string(v), "close") {
				hr.Close = true
			}
		case "Content-Length", "Transfer-Encoding":
			// Framing headers are net/http's to compute.
		default:
			hr.Header.Add(key, string(v))
		}
	})
	return hr, nil
}

// injectHTTPStreamResponse copies status and headers from a net/http streaming
// response onto resp and installs its body as resp's body stream, so
// resp.BodyStream(), ExtractProviderResponseHeaders, DecompressStreamBody and
// ReleaseStreamingResponse all keep working against a net/http body.
func injectHTTPStreamResponse(resp *fasthttp.Response, hr *http.Response) {
	// SetBodyStream calls ResetBody and then SetContentLength, so it has to run
	// before the header copy or it would clobber the provider's headers.
	resp.SetBodyStream(hr.Body, -1)
	resp.SetStatusCode(hr.StatusCode)
	for k, vals := range hr.Header {
		for i, v := range vals {
			if i == 0 {
				resp.Header.Set(k, v)
				continue
			}
			resp.Header.Add(k, v)
		}
	}
}

// DoStreamingRequestViaHTTP sends req through the net/http twin of client and
// installs the streaming response body on resp. See the file header for why the
// streaming path does not use fasthttp.
func DoStreamingRequestViaHTTP(ctx context.Context, client *fasthttp.Client, req *fasthttp.Request, resp *fasthttp.Response) error {
	hr, err := fastHTTPRequestToHTTP(ctx, req)
	if err != nil {
		return err
	}
	httpResp, err := StreamingHTTPTwin(client).Do(hr)
	if err != nil {
		return err
	}
	injectHTTPStreamResponse(resp, httpResp)
	return nil
}
