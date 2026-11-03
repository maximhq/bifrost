package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fasthttp/router"
	ws "github.com/fasthttp/websocket"
	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// relayPrefix is where every relay is served: /relay/{endpoint}/{path}.
const relayPrefix = "/relay/"

// relayDroppedHeaders are caller headers never forwarded to an endpoint: credentials
// (the endpoint signs in with its own), connection-level and hop-by-hop headers, and cookies.
var relayDroppedHeaders = map[string]struct{}{
	"authorization": {}, "api-key": {}, "x-api-key": {}, "x-goog-api-key": {}, "ocp-apim-subscription-key": {},
	"subscription-key": {}, "x-functions-key": {}, "proxy-authorization": {}, "cookie": {}, "set-cookie": {},
	"host": {}, "connection": {}, "transfer-encoding": {}, "accept-encoding": {}, "keep-alive": {}, "te": {},
	"trailer": {}, "upgrade": {}, "proxy-connection": {}, "proxy-authenticate": {}, "content-length": {},
}

// RelayGatewayHandler serves the relay data plane, /relay/{endpoint}/{path}, for every configured
// relay, beside the management API in RelaysHandler the way the Agent Gateway serves both. It authenticates the caller like any inference route, strips the caller's credentials,
// and hands the call to the manager, which runs the Relays hooks (governance and
// logging) around the relay.
type RelayGatewayHandler struct {
	manager      *relay.Manager
	handlerStore lib.HandlerStore
	logger       schemas.Logger
	config       *lib.Config

	// sockets counts open WebSocket sessions against websocket.max_connections_per_user, the
	// bound the Realtime and GPT Live handlers each apply to their own sessions.
	sockets atomic.Int64
}

// NewRelayGatewayHandler builds the data-plane handler.
func NewRelayGatewayHandler(manager *relay.Manager, config *lib.Config, logger schemas.Logger) *RelayGatewayHandler {
	h := &RelayGatewayHandler{manager: manager, config: config, logger: logger}
	if config != nil {
		h.handlerStore = config // a nil *lib.Config must stay a nil HandlerStore
	}
	return h
}

// Close ends every open WebSocket session, for shutdown; each runs its post-hooks as it ends.
func (r *RelayGatewayHandler) Close() {
	if r != nil && r.manager != nil {
		r.manager.CloseSockets()
	}
}

// RegisterRoutes mounts the relay for every method an endpoint route may use.
func (r *RelayGatewayHandler) RegisterRoutes(rt *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handler := lib.ChainMiddlewares(r.handle, middlewares...)
	for _, method := range []string{fasthttp.MethodGet, fasthttp.MethodPost, fasthttp.MethodPut, fasthttp.MethodPatch, fasthttp.MethodDelete, fasthttp.MethodHead} {
		rt.Handle(method, relayPrefix+"{endpoint}/{path:*}", handler)
	}
}

// relayGatewayError is a non-provider error with an explicit status, for refusals made here.
func relayGatewayError(err error, message string, statusCode int) *schemas.BifrostError {
	e := &schemas.BifrostError{StatusCode: &statusCode, Error: &schemas.ErrorField{Message: message, Error: err}}
	return e
}

// sendRelayError writes a relay error the way inference and passthrough errors are written
// (SendBifrostError: sanitized, the root span stamped, extra_fields moved to headers when the
// client asks for that), stamped as a relay request. Once the request has a Bifrost context, its
// context-only headers (upstream latency) are written in the same pass, as GenericRouter.sendError
// writes them once.
func sendRelayError(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, bifrostErr *schemas.BifrostError) {
	if bifrostErr == nil {
		bifrostErr = relayGatewayError(nil, lib.ClientSafeInternalErrorMessage, fasthttp.StatusInternalServerError)
	}
	if bifrostErr.ExtraFields.RequestType == "" {
		bifrostErr.ExtraFields.RequestType = schemas.RelayRequest
	}
	sendBifrostErrorWithContext(ctx, bifrostCtx, bifrostErr)
}

// endpointRequestPath splits /relay/{endpoint}/{rest} into the endpoint name and the rooted
// path below it.
func endpointRequestPath(full string) (string, string, bool) {
	rest, ok := strings.CutPrefix(full, relayPrefix)
	if !ok {
		return "", "", false
	}
	name, path, found := strings.Cut(rest, "/")
	if name == "" || !schemas.ValidRelayName(name) {
		return "", "", false
	}
	if !found {
		return name, "/", true
	}
	return name, "/" + path, true
}

// relayForwardHeaders collects the caller headers a relay forwards to its endpoint, lowercased.
// The Connection header is read in place (Peek returns the header's own bytes) and each key is
// lowercased into one reused buffer, so a header that is dropped, including Connection itself and
// any header it names, allocates nothing; only a forwarded header's name and value are copied.
func relayForwardHeaders(header *fasthttp.RequestHeader, upgrade bool) map[string]string {
	headers := make(map[string]string)
	connection := header.Peek(fasthttp.HeaderConnection)
	var lowerBuf [64]byte
	lower := lowerBuf[:0]
	header.All()(func(key, value []byte) bool {
		lower = lowerASCII(lower[:0], key)
		if _, drop := relayDroppedHeaders[string(lower)]; drop {
			return true
		}
		if connectionNames(connection, lower) {
			return true
		}
		// Beyond the fixed list, any credential-looking header is dropped (Cf-Access-Jwt-Assertion,
		// x-amzn-oidc-*, X-Forwarded-Access-Token, *secret*, *-token): an auth proxy in front of
		// Bifrost adds these to every request, and a relay's upstream is a third party that could
		// replay them against Bifrost. Provider passthrough reaches only known providers. A caller
		// who means to send such a header names it with x-bf-eh-*, which is exempt.
		if bytes.HasPrefix(lower, []byte("x-bf-")) {
			return true
		}
		name := string(lower)
		if schemas.IsSensitiveHeader(name) {
			return true
		}
		// A WebSocket handshake is the relay's own with the upstream: the dialer sets the
		// handshake headers, and the caller's Origin names Bifrost's caller, not the upstream's.
		if upgrade && (strings.HasPrefix(name, "sec-websocket-") || name == "origin") {
			return true
		}
		headers[name] = string(value)
		return true
	})
	return headers
}

// lowerASCII appends b to dst with ASCII letters lowercased; a header name is ASCII (RFC 9110 §5.1).
func lowerASCII(dst, b []byte) []byte {
	for _, c := range b {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		dst = append(dst, c)
	}
	return dst
}

// connectionNames reports whether the Connection header value lists the header name (lowercase)
// as one of its comma-separated tokens, compared case-insensitively with surrounding whitespace
// trimmed. A header named there is hop-by-hop (RFC 9110 §7.6.1) and is not forwarded.
func connectionNames(connection, name []byte) bool {
	for len(connection) > 0 {
		token := connection
		if i := bytes.IndexByte(connection, ','); i >= 0 {
			token, connection = connection[:i], connection[i+1:]
		} else {
			connection = nil
		}
		token = bytes.TrimSpace(token)
		if len(token) != len(name) {
			continue
		}
		matched := true
		for i := range token {
			c := token[i]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != name[i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func (r *RelayGatewayHandler) handle(ctx *fasthttp.RequestCtx) {
	name, path, ok := endpointRequestPath(string(ctx.Path()))
	if !ok {
		sendRelayError(ctx, nil, relayGatewayError(nil, "no relay matches the request path", fasthttp.StatusNotFound))
		return
	}
	if err := lib.ValidatePassthroughPath(path); err != nil {
		sendRelayError(ctx, nil, relayGatewayError(err, err.Error(), fasthttp.StatusBadRequest))
		return
	}

	upgrade := ws.FastHTTPIsWebSocketUpgrade(ctx)
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, r.handlerStore)
	headers := relayForwardHeaders(&ctx.Request.Header, upgrade)
	if upgrade {
		// The session owns the request context from here, and releases it.
		r.handleSocket(ctx, bifrostCtx, cancel, name, path, headers)
		return
	}

	result, bifrostErr := r.manager.Serve(bifrostCtx, relay.Request{
		Endpoint: name,
		Method:   string(ctx.Method()),
		Path:     path,
		RawQuery: string(ctx.URI().QueryString()),
		Headers:  headers,
		Body:     ctx.Request.Body(),
	})
	if bifrostErr != nil {
		sendRelayError(ctx, bifrostCtx, bifrostErr)
		cancel()
		return
	}
	if result.Stream == nil {
		defer cancel()
		resp := result.Response
		writeEndpointHeaders(ctx, resp.Headers)
		// The routed-identity and latency headers inference responses carry (request type,
		// upstream latency, and the extra_fields scalars in headers mode).
		lib.ApplyBifrostResponseHeaders(ctx, bifrostCtx, schemas.BifrostResponseExtraFields{RequestType: schemas.RelayRequest, Latency: resp.Latency})
		ctx.SetStatusCode(resp.StatusCode)
		ctx.SetBody(resp.Body)
		return
	}

	stream := result.Stream
	// Skip post-hook body materialization; the trace completes when the stream ends. The transport
	// post-hooks are deferred too: TransportInterceptorMiddleware stores their completer in this
	// slot after the handler returns, and the stream writer runs it once the stream ends, as the
	// MCP server and inference streaming handlers do.
	ctx.SetUserValue(schemas.BifrostContextKeyDeferTraceCompletion, true)
	var completerSlot atomic.Value
	ctx.SetUserValue(schemas.BifrostContextKeyTransportPostHookCompleter, &completerSlot)
	traceCompleter, _ := ctx.UserValue(schemas.BifrostContextKeyTraceCompleter).(func([]schemas.PluginLogEntry))
	writeEndpointHeaders(ctx, stream.Headers)
	lib.ApplyBifrostStreamResponseHeaders(ctx, bifrostCtx, schemas.RelayRequest)
	ctx.SetStatusCode(stream.StatusCode)
	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.Response.Header.Set("X-Accel-Buffering", "no")
	// The stream is served the way a passthrough stream is (handlePassthroughStream): the upstream
	// is read on its own goroutine, up to a buffer ahead of the caller, so a slow caller does not
	// stall it; a second goroutine hands the chunks to fasthttp through an SSEStreamReader; and an SSE
	// reply gets the comment heartbeat, so a caller who left is noticed while the upstream is quiet.
	reader := lib.NewSSEStreamReader()
	body := &relayStreamBody{SSEStreamReader: reader}
	ctx.Response.SetBodyStream(body, -1)
	var heartbeatDone chan struct{}
	var heartbeatExited <-chan struct{}
	if relayHeartbeatEligible(stream.Headers) {
		heartbeatDone, heartbeatExited = lib.StartSSEHeartbeat(relayHeartbeatInterval, reader.SendHeartbeat, cancel)
	}
	chunks := make(chan []byte, schemas.DefaultStreamBufferSize)
	stop := make(chan struct{})
	producerDone := make(chan struct{})
	// When the upstream read ended; written before chunks closes, so the writer reads it once the
	// channel is drained.
	var upstreamEnded time.Time
	// The read error that ended the upstream, other than EOF; written before chunks closes too.
	var upstreamErr error
	go func() {
		defer close(producerDone)
		defer close(chunks)
		defer func() { upstreamEnded = time.Now() }()
		// Usage parsing and, at the end, the post-hooks run on this goroutine: a panic in either
		// ends this stream, never the process. Cancelling makes the writer wind the stream down.
		defer func() {
			if rec := recover(); rec != nil {
				r.logStreamPanic("reading", rec)
				cancel()
			}
		}()
		buf := make([]byte, relayStreamReadSize)
		for {
			n, readErr := stream.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				// Backpressure is measured here, as the LLM stream measures it on the provider goroutine:
				// the reader blocked handing a chunk on, which happens only once the buffer is full
				// because the caller is slow. Time the writer spends sending runs alongside the upstream
				// reads, so it would count the same wait twice.
				sendStart := time.Now()
				select {
				case chunks <- chunk:
				case <-stop:
					return
				}
				schemas.AddStreamBackpressure(bifrostCtx, time.Since(sendStart))
			}
			// EOF ends the stream normally; any other error (an idle timeout, an upstream reset, the
			// context cancelled once the caller left) has been reported to the post-hooks by the
			// stream itself, and cuts the caller's reply short (relayStreamBody).
			if readErr != nil {
				if readErr != io.EOF {
					upstreamErr = readErr
				}
				return
			}
		}
	}()
	go func() {
		// The time blocked handing chunks to the caller, as the LLM stream writer measures it. Like
		// that writer's, it only weights the backpressure split; it is never added to the total.
		var clientWrite time.Duration
		var drained time.Time // when the last queued chunk was handed over, once the upstream ended
		defer func() {
			// A panic in the loop below ends this stream, never the process.
			if rec := recover(); rec != nil {
				r.logStreamPanic("writing", rec)
			}
		}()
		defer func() {
			transportLogs := r.endStream(func() []schemas.PluginLogEntry {
				if heartbeatDone != nil {
					// Before reader.Done(): closing the event channel while the heartbeat could still
					// be sending on it panics.
					lib.StopSSEHeartbeat(reader, heartbeatDone, heartbeatExited)
				}
				close(stop)
				<-producerDone
				// A stream the upstream did not finish ends without the terminating chunk; set before
				// reader.Done() below, which is what lets fasthttp read the end of the body.
				if upstreamErr != nil {
					body.abort(upstreamErr)
				}
				// The producer has returned, so the stream is no longer read and can be closed: a
				// stream the producer did not finish ends as cancelled with what was forwarded.
				_ = stream.Close()
				// The chunks still queued when the upstream ended were the caller's to take: that drain
				// is backpressure too, counted only now the writer is done, and the figures the
				// post-hooks stamped at the upstream's end are stamped again with it.
				if !drained.IsZero() && !upstreamEnded.IsZero() && drained.After(upstreamEnded) {
					schemas.AddStreamBackpressure(bifrostCtx, drained.Sub(upstreamEnded))
				}
				bifrostCtx.StampStreamOverhead()
				bifrostCtx.StampStreamTransport(0, clientWrite)
				// Post-hooks run before the context is cancelled so plugins see it live.
				return runTransportPostHookCompleter(&completerSlot, r.logger)
			})
			// The body ends before the trace completes, as the LLM stream writer orders them, so the
			// caller never waits on trace export.
			reader.Done()
			cancel()
			if traceCompleter != nil {
				r.endStream(func() []schemas.PluginLogEntry { traceCompleter(transportLogs); return nil })
			}
		}()
		for chunk := range chunks {
			sendStart := time.Now()
			sent := reader.Send(chunk)
			clientWrite += time.Since(sendStart)
			if !sent {
				// The caller hung up. Cancelling the context aborts the upstream read the producer may
				// be blocked in, as a passthrough stream is cancelled.
				cancel()
				return
			}
		}
		drained = time.Now()
	}()
}

// relayStreamBody is the body fasthttp writes a relayed stream from: the SSEStreamReader it wraps,
// except that a stream whose upstream failed partway (a read error, an idle timeout, a reset, a
// usage-parse failure) ends in an error rather than io.EOF. fasthttp's writeBodyChunked then
// returns that error without writing the terminating "0\r\n\r\n" chunk and the server closes the
// connection, so the caller's HTTP client reports an incomplete reply (Go: io.ErrUnexpectedEOF)
// instead of a complete one. Only this relay path uses it; LLM and passthrough streams keep their
// own endings.
type relayStreamBody struct {
	*lib.SSEStreamReader
	aborted error // set by the stream writer before reader.Done(); read only once the event channel is closed
}

// abort makes the end of the body an error. It must be called before the reader's Done: the close
// of its event channel is what orders this write before Read sees the end.
func (b *relayStreamBody) abort(cause error) {
	b.aborted = fmt.Errorf("relay stream aborted before the upstream reply ended: %w", cause)
}

// Read implements io.Reader, replacing the end of an aborted stream with its error.
func (b *relayStreamBody) Read(p []byte) (int, error) {
	n, err := b.SSEStreamReader.Read(p)
	if err == io.EOF && b.aborted != nil {
		return n, b.aborted
	}
	return n, err
}

// endStream runs one step of a stream's teardown, recovering a panic in it (a post-hook, a span,
// trace export) so the steps after it still run and the caller's response still ends.
func (r *RelayGatewayHandler) endStream(step func() []schemas.PluginLogEntry) (logs []schemas.PluginLogEntry) {
	defer func() {
		if rec := recover(); rec != nil {
			r.logStreamPanic("ending", rec)
		}
	}()
	return step()
}

// logStreamPanic reports a panic recovered on a relay stream's goroutines.
func (r *RelayGatewayHandler) logStreamPanic(stage string, rec any) {
	if r.logger != nil {
		r.logger.Error("recovered from panic %s a relay stream: %v", stage, rec)
	}
}

// relayStreamReadSize is the read size of a relayed stream, a passthrough stream's.
const relayStreamReadSize = 4096

// relayHeartbeatInterval is how often a relayed SSE stream probes the caller; a variable so tests
// can shorten it.
var relayHeartbeatInterval = lib.DefaultSSEHeartbeatInterval

// relayHeartbeatEligible reports whether a relayed stream may carry the SSE comment heartbeat: only
// a reply whose media type is exactly text/event-stream, as passthroughHeartbeatEligible rules, since
// the relay forwards the upstream's bytes unchanged and extra bytes in any other format would
// corrupt it. A stream still content-encoded is excluded for the same reason: plain comment lines
// inside a compressed body break it.
func relayHeartbeatEligible(headers map[string]string) bool {
	eventStream := false
	for k, v := range headers {
		switch {
		case strings.EqualFold(k, "content-encoding"):
			if coding := strings.TrimSpace(v); coding != "" && !strings.EqualFold(coding, "identity") {
				return false
			}
		case strings.EqualFold(k, "content-type"):
			mediaType, _, _ := strings.Cut(v, ";")
			eventStream = strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream")
		}
	}
	return eventStream
}

// handleSocket serves a WebSocket upgrade on a relay route. The session is admitted before the
// caller is upgraded: the pre-hooks run and the upstream handshake is made, so a refusal from
// either is a plain HTTP status. The session then runs on a context detached from the request
// (fasthttp recycles the request once the handler returns), and the request's trace and transport
// post-hooks are deferred to the session's end, as they are for a relayed stream.
func (r *RelayGatewayHandler) handleSocket(ctx *fasthttp.RequestCtx, requestCtx *schemas.BifrostContext, cancelRequest context.CancelFunc, name, path string, headers map[string]string) {
	// The session context carries everything the request context had; the request-scoped one
	// (and its client-disconnect watcher) is no longer needed once the handler returns.
	defer cancelRequest()
	if limit := r.maxSockets(); r.sockets.Add(1) > limit {
		r.sockets.Add(-1)
		sendRelayError(ctx, requestCtx, relayGatewayError(nil, fmt.Sprintf("too many open relay WebSocket sessions (limit %d)", limit), fasthttp.StatusTooManyRequests))
		return
	}
	reserved := true
	defer func() {
		if reserved {
			r.sockets.Add(-1)
		}
	}()
	sessionCtx, cancelSession := detachRelaySessionContext(ctx, requestCtx)
	sock, bifrostErr := r.manager.OpenSocket(sessionCtx, relay.SocketRequest{
		Request: relay.Request{
			Endpoint: name,
			Method:   string(ctx.Method()),
			Path:     path,
			RawQuery: string(ctx.URI().QueryString()),
			Headers:  headers,
		},
		Subprotocols: offeredSubprotocols(ctx),
	})
	if bifrostErr != nil {
		cancelSession()
		sendRelayError(ctx, requestCtx, bifrostErr)
		return
	}
	ctx.SetUserValue(schemas.BifrostContextKeyDeferTraceCompletion, true)
	var completerSlot atomic.Value
	ctx.SetUserValue(schemas.BifrostContextKeyTransportPostHookCompleter, &completerSlot)
	traceCompleter, _ := ctx.UserValue(schemas.BifrostContextKeyTraceCompleter).(func([]schemas.PluginLogEntry))
	for k, v := range sock.Headers() {
		if lib.IsGatewayOwnedResponseHeader(k) {
			continue
		}
		ctx.Response.Header.Set(k, v)
	}
	upgrader := ws.FastHTTPUpgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
			origin := string(ctx.Request.Header.Peek("Origin"))
			if origin == "" || r.config == nil || r.config.ClientConfig == nil {
				return true
			}
			return IsOriginAllowed(origin, r.config.ClientConfig.AllowedOrigins)
		},
	}
	if protocol := sock.Subprotocol(); protocol != "" {
		upgrader.Subprotocols = []string{protocol}
	}
	reserved = false // the session releases its slot when it ends
	err := upgrader.Upgrade(ctx, func(conn *ws.Conn) {
		defer r.sockets.Add(-1)
		defer conn.Close()
		defer recoverWebSocketSession(conn)
		if limit := sock.MaxMessageBytes(); limit > 0 {
			conn.SetReadLimit(limit)
		}
		client := newRealtimeClientConn(conn)
		client.startHeartbeat()
		sock.Run(relaySocketClient{conn: client})
		client.stopHeartbeat()
		transportLogs := runTransportPostHookCompleter(&completerSlot, r.logger)
		cancelSession()
		if traceCompleter != nil {
			traceCompleter(transportLogs)
		}
	})
	if err != nil {
		r.sockets.Add(-1)
		sock.Abort(relayGatewayError(err, "the WebSocket upgrade with the caller failed", fasthttp.StatusBadRequest))
		cancelSession()
		if traceCompleter != nil {
			traceCompleter(runTransportPostHookCompleter(&completerSlot, r.logger))
		}
	}
}

// maxSockets is the open-session bound: websocket.max_connections_per_user, as for Realtime.
func (r *RelayGatewayHandler) maxSockets() int64 {
	if r.config != nil && r.config.WebSocketConfig != nil && r.config.WebSocketConfig.MaxConnections > 0 {
		return int64(r.config.WebSocketConfig.MaxConnections)
	}
	return schemas.DefaultWSMaxConnections
}

// offeredSubprotocols reads the subprotocols the caller offered on its handshake.
func offeredSubprotocols(ctx *fasthttp.RequestCtx) []string {
	raw := string(ctx.Request.Header.Peek("Sec-WebSocket-Protocol"))
	if raw == "" {
		return nil
	}
	var out []string
	for _, protocol := range strings.Split(raw, ",") {
		if protocol = strings.TrimSpace(protocol); protocol != "" {
			out = append(out, protocol)
		}
	}
	return out
}

// detachRelaySessionContext copies the request's identity, grant, governance values and trace onto
// a context that outlives the request: the values the transport middleware stored on the fasthttp
// request, then the request context's own, which win. The trace is kept, since the relay defers
// its completion to the session's end.
func detachRelaySessionContext(ctx *fasthttp.RequestCtx, requestCtx *schemas.BifrostContext) (*schemas.BifrostContext, context.CancelFunc) {
	sessionCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	ctx.VisitUserValuesAll(func(key, value any) {
		if _, ok := key.(schemas.BifrostContextKey); ok && value != nil {
			sessionCtx.SetValue(key, value)
		}
	})
	for _, key := range realtimeMiddlewareKeys {
		if value := ctx.UserValue(key); value != nil {
			sessionCtx.SetValue(key, value)
		}
	}
	for key, value := range requestCtx.GetUserValues() {
		if value != nil {
			sessionCtx.SetValue(key, value)
		}
	}
	if g := requestCtx.Grant(); g != nil {
		sessionCtx.SetGrant(g)
	}
	return sessionCtx, cancel
}

// relaySocketClient adapts the upgraded caller connection to relay.SocketClient, reusing the
// Realtime connection wrapper for serialized writes, write deadlines and the heartbeat.
type relaySocketClient struct {
	conn *realtimeClientConn
}

func (c relaySocketClient) ReadMessage() (int, []byte, error) { return c.conn.ReadMessage() }

func (c relaySocketClient) WriteMessage(messageType int, data []byte) error {
	return c.conn.WriteMessage(messageType, data)
}

func (c relaySocketClient) WriteClose(code int, reason string) error {
	return c.conn.conn.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(code, reason), time.Now().Add(realtimeCloseHandshakeGrace))
}

// Interrupt forces the pending read to return: closing a hijacked connection cannot interrupt it.
func (c relaySocketClient) Interrupt() { _ = c.conn.conn.SetReadDeadline(time.Now()) }

// runTransportPostHookCompleter runs the deferred transport post-hooks once a relay stream ends and
// returns their logs. The middleware publishes the completer just after the handler returns, which
// can race a short stream, so the slot is polled briefly. A post-hook error cannot reach the caller
// any more; it is logged.
func runTransportPostHookCompleter(slot *atomic.Value, logger schemas.Logger) []schemas.PluginLogEntry {
	var loaded any
	deadline := time.Now().Add(100 * time.Millisecond)
	for loaded = slot.Load(); loaded == nil && time.Now().Before(deadline); loaded = slot.Load() {
		time.Sleep(time.Millisecond)
	}
	completer, ok := loaded.(func() ([]schemas.PluginLogEntry, error))
	if !ok {
		return nil
	}
	logs, err := completer()
	if err != nil && logger != nil {
		logger.Warn("transport post-hook failed after relay stream ended: %v", err)
	}
	return logs
}

// writeEndpointHeaders copies the upstream's response headers, minus the ones the gateway owns and
// the framing headers fasthttp sets itself.
func writeEndpointHeaders(ctx *fasthttp.RequestCtx, headers map[string]string) {
	for k, v := range headers {
		if lib.IsGatewayOwnedResponseHeader(k) {
			continue
		}
		// Content-Encoding is kept: core/relay hands a buffered reply over decoded and drops it there,
		// and names it only on a stream it forwards still encoded, which the caller must decode.
		switch strings.ToLower(k) {
		case "content-length", "transfer-encoding", "connection", "keep-alive":
			continue
		}
		ctx.Response.Header.Set(k, v)
	}
}
