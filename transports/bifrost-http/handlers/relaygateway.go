package handlers

import (
	"bufio"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
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

func sendRelayError(ctx *fasthttp.RequestCtx, bifrostErr *schemas.BifrostError) {
	bifrostErr = lib.SanitizeBifrostErrorForClient(bifrostErr)
	if bifrostErr == nil {
		bifrostErr = relayGatewayError(nil, lib.ClientSafeInternalErrorMessage, fasthttp.StatusInternalServerError)
	}
	ctx.SetStatusCode(bifrostErr.EffectiveHTTPStatus())
	ctx.SetContentType("application/json")
	body, err := sonic.Marshal(bifrostErr)
	if err != nil {
		ctx.SetBodyString(`{"error":{"message":"internal error"}}`)
		return
	}
	ctx.SetBody(body)
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
		sendRelayError(ctx, relayGatewayError(nil, "no relay matches the request path", fasthttp.StatusNotFound))
		return
	}
	if err := lib.ValidatePassthroughPath(path); err != nil {
		sendRelayError(ctx, relayGatewayError(err, err.Error(), fasthttp.StatusBadRequest))
		return
	}

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
		if strings.HasPrefix(name, "x-bf-") || schemas.IsSensitiveHeader(name) {
			return true
		}
		headers[name] = string(value)
		return true
	})

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, r.handlerStore)
	result, bifrostErr := r.manager.Serve(bifrostCtx, relay.Request{
		Endpoint: name,
		Method:   string(ctx.Method()),
		Path:     path,
		RawQuery: string(ctx.URI().QueryString()),
		Headers:  headers,
		Body:     ctx.Request.Body(),
	})
	if bifrostErr != nil {
		cancel()
		sendRelayError(ctx, bifrostErr)
		return
	}
	if result.Stream == nil {
		defer cancel()
		resp := result.Response
		writeEndpointHeaders(ctx, resp.Headers)
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
	ctx.SetStatusCode(stream.StatusCode)
	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.Response.Header.Set("X-Accel-Buffering", "no")
	ctx.SetBodyStreamWriter(func(w *bufio.Writer) {
		defer func() {
			_ = stream.Close()
			// Post-hooks run before the context is cancelled so plugins see it live.
			transportLogs := runTransportPostHookCompleter(&completerSlot, r.logger)
			cancel()
			if traceCompleter != nil {
				traceCompleter(transportLogs)
			}
		}()
		buf := make([]byte, 32*1024)
		for {
			n, readErr := stream.Read(buf)
			if n > 0 {
				if _, err := w.Write(buf[:n]); err != nil {
					return // the caller hung up; Close reports what was forwarded
				}
				if err := w.Flush(); err != nil {
					return
				}
			}
			// EOF ends the stream normally; any other error (an idle timeout, an upstream reset)
			// has already been reported to the post-hooks by the stream itself.
			if readErr != nil {
				return
			}
		}
	})
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
