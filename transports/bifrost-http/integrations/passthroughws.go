package integrations

import (
	"strings"

	"github.com/fasthttp/websocket"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/passthroughparse"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// PassthroughWebSocketTarget is the upstream a passthrough route resolved for a WebSocket upgrade.
type PassthroughWebSocketTarget struct {
	Provider          schemas.ModelProvider // provider that serves the connection
	BaseProvider      schemas.ModelProvider // built-in provider whose API the route speaks
	Path              string                // stripped provider path, e.g. "/v1/responses"
	APIVersion        string                // version segment stripped with the route prefix
	RawQuery          string                // raw query string, no "?"
	UpstreamURL       string                // host-backed base URL; empty means the provider's own
	Headers           map[string]string     // client handshake headers safe to send upstream
	ForwardCallerAuth bool                  // Headers carries the caller's bearer as the upstream credential
}

// PassthroughWebSocketHandler serves a WebSocket upgrade a passthrough route accepted. It owns the
// response: it either upgrades the connection or writes an HTTP error.
type PassthroughWebSocketHandler func(ctx *fasthttp.RequestCtx, target *PassthroughWebSocketTarget)

// SetPassthroughWebSocketHandler sets the handler that serves WebSocket upgrades on the paths the
// passthrough config lists in WebSocketPaths. Without one those paths answer as plain HTTP.
func (g *GenericRouter) SetPassthroughWebSocketHandler(handler PassthroughWebSocketHandler) {
	g.passthroughWS = handler
}

// servesWebSocket reports whether a stripped path serves Responses WebSocket mode.
func (c *PassthroughConfig) servesWebSocket(path string) bool {
	for _, p := range c.WebSocketPaths {
		if p == path {
			return true
		}
	}
	return false
}

// forwards reports whether the HTTP passthrough forwards method on the full request path. An
// unrestricted router forwards everything under its prefix.
func (c *PassthroughConfig) forwards(method, fullPath string) bool {
	if len(c.AllowedRoutes) == 0 {
		return true
	}
	for _, allowed := range c.AllowedRoutes {
		if strings.EqualFold(allowed.Method, method) && allowed.Path == fullPath {
			return true
		}
	}
	return false
}

// handlePassthroughWebSocket takes a GET on a WebSocket-capable passthrough path. It returns false
// when the request should continue as plain HTTP passthrough.
func (g *GenericRouter) handlePassthroughWebSocket(ctx *fasthttp.RequestCtx, provider, baseProvider schemas.ModelProvider, path, apiVersion, callerAuth string, safeHeaders map[string]string) bool {
	cfg := g.passthroughCfg
	if g.passthroughWS == nil || !websocket.FastHTTPIsWebSocketUpgrade(ctx) {
		if cfg.forwards(fasthttp.MethodGet, string(ctx.Path())) {
			return false
		}
		g.handlePassthroughNoRoute(ctx)
		return true
	}
	headers := make(map[string]string, len(safeHeaders))
	for name, value := range safeHeaders {
		if isWebSocketHandshakeHeader(name) {
			continue
		}
		headers[name] = value
	}
	forward := passthroughForwardsCallerAuth(provider, callerAuth, cfg.UpstreamURL)
	if forward {
		headers["authorization"] = callerAuth
	}
	g.passthroughWS(ctx, &PassthroughWebSocketTarget{
		Provider:          provider,
		BaseProvider:      baseProvider,
		Path:              path,
		APIVersion:        apiVersion,
		RawQuery:          string(ctx.URI().QueryString()),
		UpstreamURL:       cfg.UpstreamURL,
		Headers:           headers,
		ForwardCallerAuth: forward,
	})
	return true
}

// isWebSocketHandshakeHeader reports whether a lower-cased client header belongs to the client's
// own handshake. The upstream dial writes its own, and a body-less upgrade has no content headers.
func isWebSocketHandshakeHeader(name string) bool {
	switch name {
	case "upgrade", "origin", "content-length", "content-type":
		return true
	}
	return strings.HasPrefix(name, "sec-websocket-")
}

// TurnRequest builds the passthrough request one response.create message stands for: a POST of
// that message to the route's path. It marks ctx the way the HTTP passthrough does for the same
// request, so plugins and logs treat a WebSocket turn like the HTTP call it replaces.
func (t *PassthroughWebSocketTarget) TurnRequest(ctx *schemas.BifrostContext, message []byte) *schemas.BifrostPassthroughRequest {
	model := extractPassthroughModel(t.Path, gjson.GetBytes(message, "model").Str)
	if t.ForwardCallerAuth {
		ctx.SetValue(schemas.BifrostContextKeySkipKeySelection, true)
	}
	registerPassthroughTextRewriters(ctx, passthroughparse.Classify(t.BaseProvider, model, fasthttp.MethodPost, t.Path))
	return &schemas.BifrostPassthroughRequest{
		Method:       fasthttp.MethodPost,
		Path:         t.Path,
		APIVersion:   t.APIVersion,
		RawQuery:     t.RawQuery,
		UpstreamURL:  t.UpstreamURL,
		Body:         message,
		SafeHeaders:  t.Headers,
		Provider:     t.Provider,
		BaseProvider: t.BaseProvider,
		Model:        model,
	}
}
