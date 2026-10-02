package utils

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ErrRequestBodyRewrite is wrapped by every failure that originates in the
// request-body rewriter hook: the rewriter returned an error or a negative size,
// its reader yielded fewer or more bytes than the size it announced, or a
// net/http request carrying a body cannot be replayed. A request failing with it
// was aborted before a complete body reached the provider, and is not retried.
var ErrRequestBodyRewrite = errors.New("bifrost: request body rewrite aborted")

// RequestBodyRewriter lets an embedder replace the bytes a provider built for
// the outgoing request body at the moment the request is written to the socket,
// after every provider-specific mapping has run, without the provider ever
// materialising the replacement. A memory-bounded gateway uses it to let core
// map a small request in which large strings are placeholders, and splice the
// original bytes back in on the wire. It is carried on the request context under
// schemas.BifrostContextKeyRequestBodyRewriter; any other value type is ignored.
//
// body is the exact payload the provider built for this wire attempt (for SigV4
// providers, the payload that is signed). The rewriter must neither retain nor
// mutate it. The result means:
//
//   - r == nil, err == nil: send body unchanged (size is ignored).
//   - r != nil, size >= 0, err == nil: send exactly size bytes read from r, with
//     Content-Length: size. A reader yielding fewer or more bytes aborts the
//     attempt with ErrRequestBodyRewrite.
//   - err != nil (or a negative size): abort this wire attempt. Nothing is sent
//     and the transport returns an error wrapping ErrRequestBodyRewrite and err.
//     Core does not retry the attempt; configured fallbacks still run.
//
// The rewriter is invoked once per wire attempt — every fasthttp RoundTrip
// (including fasthttp's stale-connection retry), every core retry and fallback,
// every net/http send and GetBody replay — plus once per SigV4 payload digest.
// It must therefore be deterministic over body for the lifetime of the request
// (the signature hashes one invocation, the transport sends another) and return
// a fresh reader on every call. Core never closes the reader, even if it
// implements io.Closer.
//
// Requests whose body is already a stream (fasthttp SetBodyStream) are sent
// verbatim and do not invoke the rewriter. Internal requests prepared with
// ClearContextForInternalRequest drop the rewriter.
type RequestBodyRewriter func(body []byte) (r io.Reader, size int64, err error)

// RequestBodyRewriterFromContext returns the rewriter carried on ctx, or nil.
func RequestBodyRewriterFromContext(ctx context.Context) RequestBodyRewriter {
	if ctx == nil {
		return nil
	}
	rw, _ := ctx.Value(schemas.BifrostContextKeyRequestBodyRewriter).(RequestBodyRewriter)
	return rw
}

// rewriteBody runs rw over body and validates its result. A nil reader with a
// nil error means "send body unchanged".
func rewriteBody(rw RequestBodyRewriter, body []byte) (*exactSizeReader, error) {
	r, size, err := rw(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRequestBodyRewrite, err)
	}
	if r == nil {
		return nil, nil
	}
	if size < 0 {
		return nil, fmt.Errorf("%w: negative size %d", ErrRequestBodyRewrite, size)
	}
	e := &exactSizeReader{r: r, remaining: size}
	if size == 0 {
		// An empty body is never read by the transports (Read on a zero-size
		// exactSizeReader is an immediate io.EOF), so probe r here: a reader with
		// bytes beyond a size of 0 is a "long" reader like any other.
		if err := e.probeEnd(); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// writeRequestWithRewriter writes req to bw, substituting the rewritten body
// when ctx carries a rewriter. Without one (or for a body stream) it is exactly
// req.Write(bw). req is never mutated, so a retried RoundTrip — fasthttp's own
// stale-connection retry or a core retry — re-enters the rewriter with the
// provider-built body and obtains a fresh reader.
func writeRequestWithRewriter(ctx context.Context, req *fasthttp.Request, bw *bufio.Writer) error {
	rw := RequestBodyRewriterFromContext(ctx)
	if rw == nil || req.IsBodyStream() {
		return req.Write(bw)
	}
	body, err := rewriteBody(rw, req.Body())
	if err != nil {
		// Nothing has been written to bw yet.
		return err
	}
	if body == nil {
		return req.Write(bw)
	}
	// Copy headers and URI onto a pooled request so req keeps its body for a
	// retry. CopyTo also copies the provider-built body, which SetBodyStream then
	// drops; that copy is deliberate. fasthttp's header-and-URI-only copy
	// (copyToSkipBody) is unexported, and the alternatives all touch req: req is
	// shared with fasthttp's stale-connection retry and with every core retry,
	// so it must never be mutated, not even temporarily.
	out := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(out)
	req.CopyTo(out)
	out.SetBodyStream(body, int(body.remaining))
	return out.Write(bw)
}

// applyRequestBodyRewriterHTTP is the net/http counterpart of
// writeRequestWithRewriter (Bedrock and any other DoHTTPRequest caller). It
// replaces req.Body with the rewritten body and keeps req.GetBody able to replay
// it; a replay that cannot splice fails instead of falling back to the original
// bytes.
func applyRequestBodyRewriterHTTP(req *http.Request) error {
	rw := RequestBodyRewriterFromContext(req.Context())
	if rw == nil || req.Body == nil || req.Body == http.NoBody {
		return nil
	}
	if req.GetBody == nil {
		return fmt.Errorf("%w: request body not replayable", ErrRequestBodyRewrite)
	}
	orig, err := req.GetBody()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRequestBodyRewrite, err)
	}
	body, err := io.ReadAll(orig)
	_ = orig.Close()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRequestBodyRewrite, err)
	}
	first, err := rewriteBody(rw, body)
	if err != nil || first == nil {
		return err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(first)
	req.ContentLength = first.remaining
	req.GetBody = func() (io.ReadCloser, error) {
		replay, err := rewriteBody(rw, body)
		if err != nil {
			return nil, err
		}
		if replay == nil {
			return nil, fmt.Errorf("%w: replay returned no body", ErrRequestBodyRewrite)
		}
		return io.NopCloser(replay), nil
	}
	return nil
}

// digestBufPool holds the 32 KiB copy buffers RewrittenBodyDigest streams the
// rewritten body through, so signing a large body allocates no buffer per call.
var digestBufPool = sync.Pool{New: func() any {
	b := make([]byte, 32<<10)
	return &b
}}

// RewrittenBodyDigest returns the hex SHA-256 and the length of the bytes that
// will actually be sent for body: the rewritten body when ctx carries a
// rewriter that rewrites it, body itself otherwise. SigV4 signers must hash and
// sign the length of these bytes, not of body, or the signature covers bytes
// that never reach the wire. The rewritten body is streamed into the hash and
// never materialised.
func RewrittenBodyDigest(ctx context.Context, body []byte) (sha256Hex string, size int64, err error) {
	if rw := RequestBodyRewriterFromContext(ctx); rw != nil {
		rewritten, err := rewriteBody(rw, body)
		if err != nil {
			return "", 0, err
		}
		if rewritten != nil {
			size := rewritten.remaining
			h := sha256.New()
			bufp := digestBufPool.Get().(*[]byte)
			_, err := io.CopyBuffer(h, rewritten, *bufp)
			digestBufPool.Put(bufp)
			if err != nil {
				return "", 0, err
			}
			return hex.EncodeToString(h.Sum(nil)), size, nil
		}
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), int64(len(body)), nil
}

// maxEmptyReads bounds how many consecutive (0, nil) reads exactSizeReader
// tolerates from the embedder's reader before giving up, like bufio does.
const maxEmptyReads = 100

// exactSizeReader yields exactly remaining bytes from r and fails with
// ErrRequestBodyRewrite when r ends early, fails, or has bytes beyond the
// announced size. Overflow is detected before the last in-size bytes are handed
// out, so an overflowing body never reaches the peer complete. It deliberately
// does not implement io.Closer: neither fasthttp nor net/http may close the
// embedder's reader.
type exactSizeReader struct {
	r         io.Reader
	remaining int64
}

func (e *exactSizeReader) Read(p []byte) (int, error) {
	if e.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > e.remaining {
		p = p[:e.remaining]
	}
	n, err := e.r.Read(p)
	e.remaining -= int64(n)
	switch {
	case err != nil && !errors.Is(err, io.EOF):
		e.remaining = 0
		return 0, fmt.Errorf("%w: read: %w", ErrRequestBodyRewrite, err)
	case e.remaining == 0:
		if err == nil {
			if perr := e.probeEnd(); perr != nil {
				return 0, perr
			}
		}
		return n, nil
	case err != nil: // io.EOF before size bytes
		short := e.remaining
		e.remaining = 0
		return 0, fmt.Errorf("%w: body ended %d bytes short of its size", ErrRequestBodyRewrite, short)
	}
	return n, nil
}

// probeEnd confirms r has no byte beyond the announced size.
func (e *exactSizeReader) probeEnd() error {
	var one [1]byte
	for range maxEmptyReads {
		n, err := e.r.Read(one[:])
		if n > 0 {
			return fmt.Errorf("%w: body longer than its size", ErrRequestBodyRewrite)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: read: %w", ErrRequestBodyRewrite, err)
		}
	}
	return fmt.Errorf("%w: %w", ErrRequestBodyRewrite, io.ErrNoProgress)
}
