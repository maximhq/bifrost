package handlers

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/fasthttp/router"
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
}

// NewRelayGatewayHandler builds the data-plane handler.
func NewRelayGatewayHandler(manager *relay.Manager, handlerStore lib.HandlerStore, logger schemas.Logger) *RelayGatewayHandler {
	return &RelayGatewayHandler{manager: manager, handlerStore: handlerStore, logger: logger}
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

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, r.handlerStore)
	headers := make(map[string]string)
	connectionTokens := map[string]struct{}{}
	for _, token := range strings.Split(string(ctx.Request.Header.Peek("Connection")), ",") {
		if token = strings.ToLower(strings.TrimSpace(token)); token != "" {
			connectionTokens[token] = struct{}{}
		}
	}
	ctx.Request.Header.All()(func(key, value []byte) bool {
		name := strings.ToLower(string(key))
		if _, drop := relayDroppedHeaders[name]; drop {
			return true
		}
		if _, drop := connectionTokens[name]; drop {
			return true
		}
		// Beyond the fixed list, any credential-looking header is dropped (Cf-Access-Jwt-Assertion,
		// x-amzn-oidc-*, X-Forwarded-Access-Token, *secret*, *-token): an auth proxy in front of
		// Bifrost adds these to every request, and a relay's upstream is a third party that could
		// replay them against Bifrost. Provider passthrough reaches only known providers. A caller
		// who means to send such a header names it with x-bf-eh-*, which is exempt.
		if strings.HasPrefix(name, "x-bf-") || schemas.IsSensitiveHeader(name) {
			return true
		}
		headers[name] = string(value)
		return true
	})
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
	ctx.Response.SetBodyStream(reader, -1)
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
			// stream itself.
			if readErr != nil {
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
// corrupt it.
func relayHeartbeatEligible(headers map[string]string) bool {
	for k, v := range headers {
		if strings.EqualFold(k, "content-type") {
			mediaType, _, _ := strings.Cut(v, ";")
			return strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream")
		}
	}
	return false
}

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
		switch strings.ToLower(k) {
		case "content-length", "transfer-encoding", "connection", "keep-alive", "content-encoding":
			continue
		}
		ctx.Response.Header.Set(k, v)
	}
}
