package utils

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http/httputil"
	"sync"
	"sync/atomic"
	"time"

	"github.com/valyala/fasthttp"
)

// This file is Bifrost's fasthttp RoundTripper. It exists because fasthttp's
// default transport (github.com/valyala/fasthttp v1.74.0, client.go:3398-3524)
// cannot bound the wait for response headers on a streamed response without
// also bounding the body: the read deadline it sets before parsing headers
// stays on the socket for as long as the caller reads Response.BodyStream.
// Bifrost therefore used to run streaming clients with ReadTimeout=0, which
// left a request to an upstream that accepts the connection and never answers
// pinned until the upstream closed it (maximhq/bifrost#7034). The default
// transport also has no way to observe a context, so cancelling a request
// only stopped Bifrost from waiting; the socket read carried on.
//
// contextTransport owns the socket for the life of the request, so it can:
//   - apply the client's WriteTimeout / ReadTimeout (default_request_timeout_in_seconds)
//     to the request write and to the wait for response headers, tightened by the
//     request context's deadline;
//   - clear those deadlines the moment headers are parsed for a streamed body, so
//     stream_idle_timeout_in_seconds (NewIdleTimeoutReader) is the only bound on the
//     body, exactly as before;
//   - close the socket when the request context is cancelled at any point before the
//     body is handed over, so the blocked write or read returns immediately.
//
// The request context reaches the transport through bindRequestContext, called by
// DoStreamingRequest and MakeRequestWithContext around client.Do. fasthttp's Request
// carries no user values and Client.Transport is shared by every request, so a
// pointer-keyed map is the only channel available.
//
// Buffered (non-streamed) responses go through resp.ReadLimitBody exactly like the
// default transport. Streamed responses cannot: Response.SetBodyStream resets the
// body first, and that reset returns fasthttp's pooled request stream to its pool,
// so the transport parses headers with ResponseHeader.Read and decodes the body
// itself (chunked via net/http/httputil, Content-Length via io.LimitReader,
// identity until close). The resulting streamBody implements fasthttp's
// ReadCloserWithError contract that NewIdleTimeoutReader, SetupStreamCancellation,
// ReleaseStreamingResponse and Response.Body already rely on.
//
// Not supported, by design: per-request DoTimeout / DoDeadline (their deadline is
// unexported; Bifrost never uses them), Response.SkipBody combined with streaming
// beyond HEAD/204/304, and HTTP pipelining.

// requestContexts maps an in-flight *fasthttp.Request to its context. Requests are
// pooled, so a binding must be removed once client.Do returns and before the
// request is released; bindRequestContext's unbind does that.
var requestContexts sync.Map // *fasthttp.Request -> context.Context

// bindRequestContext associates ctx with req for the duration of a client.Do call
// so contextTransport can honor its deadline and cancellation. The returned unbind
// is idempotent and must run after client.Do has returned. A ctx that can never be
// cancelled or expire binds nothing.
func bindRequestContext(req *fasthttp.Request, ctx context.Context) (unbind func()) {
	if req == nil || ctx == nil || ctx.Done() == nil {
		return func() {}
	}
	requestContexts.Store(req, ctx)
	var once sync.Once
	return func() {
		once.Do(func() {
			requestContexts.CompareAndDelete(req, ctx)
		})
	}
}

func lookupRequestContext(req *fasthttp.Request) context.Context {
	if v, ok := requestContexts.Load(req); ok {
		if ctx, ok := v.(context.Context); ok {
			return ctx
		}
	}
	return context.Background()
}

// NewContextTransport returns the RoundTripper installed on every fasthttp client
// Bifrost builds (see ConfigureDialer, BuildStreamingClient,
// BuildLargeResponseClient).
func NewContextTransport() fasthttp.RoundTripper {
	return &contextTransport{}
}

type contextTransport struct{}

// maxInterimResponses mirrors fasthttp's unexported cap on 1xx responses parsed
// before the final status line.
const maxInterimResponses = 10

var errTooManyInterimResponses = errors.New("too many 1xx interim responses")

// testHookBeforeConnRelease runs on the buffered path right before the
// connection's fate is decided (CloseConn or ReleaseConn). Nil outside tests;
// it lets a test land a cancellation at the one point where a still-running
// watcher could close a socket that is about to be pooled.
var testHookBeforeConnRelease func()

// RoundTrip implements fasthttp.RoundTripper. The retry flag is true only for a
// failure before the response headers were parsed on a connection reused from
// the pool: the upstream may have closed it while idle, and RetryIfErr
// (network.StaleConnectionRetryIfErr) walks past such sockets. The same failure
// on a socket dialed for this request is the upstream failing, so it is
// reported without a fasthttp-level retry and counts against Bifrost's own
// max_retries (maximhq/bifrost#7035). ErrBodyTooLarge never retries.
func (t *contextTransport) RoundTrip(hc *fasthttp.HostClient, req *fasthttp.Request, resp *fasthttp.Response) (retry bool, err error) {
	ctx := lookupRequestContext(req)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}

	customSkipBody := resp.SkipBody

	cc, err := hc.AcquireConn(0, req.ConnectionClose())
	if err != nil {
		return false, err
	}
	conn := cc.Conn()
	resp.ParseNetConn(conn)
	// fasthttp stamps lastUseTime only when a connection is returned to the
	// pool, so a zero value means this socket was dialed for this request.
	reused := !cc.LastUseTime().IsZero()
	retryOn := func(err error) bool {
		return reused && !errors.Is(err, fasthttp.ErrBodyTooLarge)
	}

	watcher := startCancelWatcher(ctx, conn)
	defer watcher.stop()

	if err = conn.SetWriteDeadline(deadlineFor(hc.WriteTimeout, ctx)); err != nil {
		hc.CloseConn(cc)
		return retryOn(err), watcher.classify(err)
	}

	resetConnection := false
	if hc.MaxConnDuration > 0 && time.Since(cc.CreatedTime()) > hc.MaxConnDuration && !req.ConnectionClose() {
		req.SetConnectionClose()
		resetConnection = true
	}

	bw := hc.AcquireWriter(conn)
	err = req.Write(bw)
	if resetConnection {
		req.Header.ResetConnectionClose()
	}
	if err == nil {
		err = bw.Flush()
	}
	hc.ReleaseWriter(bw)
	if err != nil {
		hc.CloseConn(cc)
		return retryOn(err), watcher.classify(err)
	}

	// Setting a deadline fails with "use of closed network connection" when the
	// watcher closed the socket a moment ago; classify turns that into ctx.Err().
	if err = conn.SetReadDeadline(deadlineFor(hc.ReadTimeout, ctx)); err != nil {
		hc.CloseConn(cc)
		return retryOn(err), watcher.classify(err)
	}

	if customSkipBody || req.Header.IsHead() {
		resp.SkipBody = true
	}
	if hc.DisableHeaderNamesNormalizing {
		resp.Header.DisableNormalizing()
	}

	br := hc.AcquireReader(conn)

	if !resp.StreamBody {
		// Buffered body: the read deadline covers the whole response, which is
		// what a unary call wants. Headers and body are read as two steps so a
		// failure can be told apart by whether the upstream ever answered: a
		// failure before the status line is parsed is what the stale keep-alive
		// walk (RetryIfErr) exists for, while a body cut short after it means the
		// upstream has processed the request, so the POST surfaces as an error
		// and is never replayed on a new socket.
		resp.ResetBody()
		if err = readResponseHeaders(resp, br); err != nil {
			hc.ReleaseReader(br)
			hc.CloseConn(cc)
			return retryOn(err), watcher.classify(err)
		}
		err = readBufferedBody(resp, br, hc.MaxResponseBodySize)
		hc.ReleaseReader(br)
		if err != nil {
			hc.CloseConn(cc)
			return false, watcher.classify(err)
		}
		// The watcher must not outlive our ownership of the socket: once the
		// connection is pooled, a late cancellation would close a connection
		// another request may already hold. Same order as the streamed path.
		watcher.stop()
		if watcher.wasCancelled() {
			hc.CloseConn(cc)
			return false, ctx.Err()
		}
		if testHookBeforeConnRelease != nil {
			testHookBeforeConnRelease()
		}
		if resetConnection || req.ConnectionClose() || resp.ConnectionClose() {
			hc.CloseConn(cc)
		} else {
			hc.ReleaseConn(cc)
		}
		return false, nil
	}

	// Streamed body: parse headers only, then hand the socket to the body.
	resp.ResetBody()
	if err = readResponseHeaders(resp, br); err != nil {
		hc.ReleaseReader(br)
		hc.CloseConn(cc)
		return retryOn(err), watcher.classify(err)
	}

	// Headers are in: the header wait is over. From here on the body is bounded
	// by NewIdleTimeoutReader and cancelled by SetupStreamCancellation, both of
	// which close the stream through CloseWithError below.
	watcher.stop()
	if watcher.wasCancelled() {
		hc.ReleaseReader(br)
		hc.CloseConn(cc)
		return false, ctx.Err()
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		hc.ReleaseReader(br)
		hc.CloseConn(cc)
		return false, err
	}

	// Decide reuse from req now: the caller releases req to fasthttp's pool as
	// soon as the stream is handed over, so reading it later from the body's
	// close path would race with that Reset. resp is still owned by the caller
	// when the stream is closed (ReleaseStreamingResponse closes before it
	// releases), so it is re-checked lazily like fasthttp's default transport
	// does, since a consumer may mark the connection unusable mid-body.
	closeConn := resetConnection || req.ConnectionClose() || resp.ConnectionClose()
	body := &streamBody{
		reader: newStreamBodyReader(resp, br),
		conn:   conn,
		release: func(discard bool) {
			hc.ReleaseReader(br)
			if discard || closeConn || resp.ConnectionClose() {
				hc.CloseConn(cc)
			} else {
				hc.ReleaseConn(cc)
			}
		},
	}
	resp.SetBodyStream(body, resp.Header.ContentLength())
	return false, nil
}

// deadlineFor returns now+timeout, tightened by ctx's deadline when that comes
// first. A zero time means no deadline.
func deadlineFor(timeout time.Duration, ctx context.Context) time.Time {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	if ctxDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || ctxDeadline.Before(deadline)) {
		deadline = ctxDeadline
	}
	return deadline
}

// readResponseHeaders parses the status line and headers, skipping 1xx interim
// responses the way Response.ReadLimitBody does.
func readResponseHeaders(resp *fasthttp.Response, br *bufio.Reader) error {
	if err := resp.Header.Read(br); err != nil {
		return err
	}
	for n := 0; ; n++ {
		status := resp.Header.StatusCode()
		if status < 100 || status > 199 || status == fasthttp.StatusSwitchingProtocols {
			return nil
		}
		if n >= maxInterimResponses {
			return errTooManyInterimResponses
		}
		if err := resp.Header.Read(br); err != nil {
			return err
		}
	}
}

// readBufferedBody mirrors the body half of Response.ReadLimitBody (fasthttp
// v1.74.0 http.go:1657-1673) once readResponseHeaders has parsed the headers:
// bodies that HTTP forbids are skipped, and a chunked body's trailer is consumed
// so a keep-alive connection is left aligned for reuse.
func readBufferedBody(resp *fasthttp.Response, br *bufio.Reader, maxBodySize int) error {
	status := resp.Header.StatusCode()
	if resp.SkipBody || status == fasthttp.StatusNoContent || status == fasthttp.StatusNotModified || (status >= 100 && status <= 199) {
		return nil
	}
	if err := resp.ReadBody(br, maxBodySize); err != nil {
		return err
	}
	if resp.Header.ContentLength() == -1 {
		if err := resp.Header.ReadTrailer(br); err != nil {
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
	}
	return nil
}

// newStreamBodyReader builds the body decoder for a streamed response from its
// parsed headers. The returned reader ends with io.EOF once the body, including
// any chunked trailer, has been consumed, which is what lets streamBody return
// the connection to the pool.
func newStreamBodyReader(resp *fasthttp.Response, br *bufio.Reader) io.Reader {
	status := resp.Header.StatusCode()
	if resp.SkipBody || status == fasthttp.StatusNoContent || status == fasthttp.StatusNotModified || (status >= 100 && status <= 199) {
		return eofReader{}
	}
	switch contentLength := resp.Header.ContentLength(); {
	case contentLength == -1:
		return &chunkedBody{chunked: httputil.NewChunkedReader(br), br: br, resp: resp}
	case contentLength >= 0:
		return io.LimitReader(br, int64(contentLength))
	default:
		// Identity encoding without a length: the body ends when the peer closes.
		return br
	}
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

// chunkedBody decodes a chunked body and consumes the trailer section after the
// terminating chunk, so a keep-alive connection is left positioned at the next
// response. net/http's chunked reader stops at the 0-length chunk and leaves the
// trailer and its final CRLF in br; ResponseHeader.ReadTrailer reads exactly that.
type chunkedBody struct {
	chunked io.Reader
	br      *bufio.Reader
	resp    *fasthttp.Response
	done    bool
}

func (c *chunkedBody) Read(p []byte) (int, error) {
	if c.done {
		return 0, io.EOF
	}
	n, err := c.chunked.Read(p)
	if errors.Is(err, io.EOF) {
		if trailerErr := c.resp.Header.ReadTrailer(c.br); trailerErr != nil && !errors.Is(trailerErr, io.EOF) {
			return n, trailerErr
		}
		c.done = true
	}
	return n, err
}

// streamBody is the io.Reader handed out as Response.BodyStream for a streamed
// response. It mirrors fasthttp's clientStreamBody (client.go:3351-3396):
// reads are serialized, CloseWithError interrupts an in-flight read by closing
// the socket, and the pooled reader and connection are released only once no
// read is running. Only CloseWithError is implemented, never io.Closer, because
// fasthttp's closeBodyStreamReader would call both.
type streamBody struct {
	reader    io.Reader
	conn      net.Conn
	release   func(discard bool)
	closed    atomic.Bool
	fullyRead bool
	readLock  sync.Mutex
	closeOnce sync.Once
}

func (s *streamBody) Read(p []byte) (int, error) {
	if s.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	s.readLock.Lock()
	defer s.readLock.Unlock()
	if s.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	n, err := s.reader.Read(p)
	if errors.Is(err, io.EOF) {
		s.fullyRead = true
	}
	return n, err
}

// CloseWithError implements fasthttp.ReadCloserWithError. A non-nil err, a read
// still in flight, or a body that was not read to EOF all mean the connection
// cannot be reused: it is closed rather than returned to the pool. Idempotent.
func (s *streamBody) CloseWithError(err error) error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		locked := s.readLock.TryLock()
		discard := err != nil || !locked
		if discard || !s.fullyRead {
			discard = true
			_ = s.conn.Close() // unblocks a Read parked on the socket
		}
		if !locked {
			s.readLock.Lock()
		}
		defer s.readLock.Unlock()
		s.release(discard)
	})
	return nil
}

// cancelWatcher closes the connection when the request context ends while the
// transport still owns the socket (write phase and header wait). It is stopped
// once the body is handed over, or when RoundTrip returns.
type cancelWatcher struct {
	ctx       context.Context
	cancelled atomic.Bool
	stopCh    chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
}

func startCancelWatcher(ctx context.Context, conn net.Conn) *cancelWatcher {
	if ctx.Done() == nil {
		return nil
	}
	w := &cancelWatcher{ctx: ctx, stopCh: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		select {
		case <-ctx.Done():
			w.cancelled.Store(true)
			_ = conn.Close()
		case <-w.stopCh:
		}
	}()
	return w
}

// stop ends the watcher and waits for its goroutine, so wasCancelled is
// authoritative afterwards. Safe on a nil watcher and when called twice.
func (w *cancelWatcher) stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		close(w.stopCh)
		<-w.done
	})
}

func (w *cancelWatcher) wasCancelled() bool {
	return w != nil && w.cancelled.Load()
}

// classify turns a socket error into what callers already expect: the context
// error when the watcher closed the socket, fasthttp.ErrTimeout for a deadline
// expiry, and the raw error otherwise (io.EOF stays io.EOF so HostClient.Do can
// report ErrConnectionClosed and StaleConnectionRetryIfErr can retry).
func (w *cancelWatcher) classify(err error) error {
	if w != nil && w.cancelled.Load() {
		if ctxErr := w.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fasthttp.ErrTimeout
	}
	return err
}
