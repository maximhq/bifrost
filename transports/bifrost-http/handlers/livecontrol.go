package handlers

import (
	"strconv"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// LiveControlHandler serves the HTTP routes that act on an existing GPT Live session by id.
type LiveControlHandler struct {
	gateway *liveGateway
}

// NewLiveControlHandler creates a new GPT Live session control handler.
func NewLiveControlHandler(client *bifrost.Bifrost, config *lib.Config) *LiveControlHandler {
	return &LiveControlHandler{gateway: &liveGateway{client: client, config: config, handlerStore: config}}
}

// RegisterRoutes registers the session routes at the base path and the OpenAI integration paths.
func (h *LiveControlHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	content := lib.ChainMiddlewares(h.handleContent, middlewares...)
	r.GET("/v1/live/sessions/{session_id}/content", content)
	for _, path := range integrations.OpenAILiveSessionPaths("/openai", "content") {
		r.GET(path, content)
	}
}

// liveContentClient downloads a recording through the plugin pipeline, so the download is
// counted, logged and traced like a file download.
type liveContentClient interface {
	LiveSessionContentRequest(ctx *schemas.BifrostContext, req *schemas.BifrostLiveContentRequest) (*schemas.LiveContentResponse, *schemas.BifrostError)
}

// handleContent downloads a stored session's recording. Nothing is billed.
func (h *LiveControlHandler) handleContent(ctx *fasthttp.RequestCtx) {
	req, ok := h.gateway.prepareRequest(ctx)
	if !ok || !h.gateway.resolveSession(ctx, req) {
		return
	}
	defer req.cancel()
	bifrostCtx, cancel := h.gateway.sessionContext(req.auth, req.preReqCtx, req.middlewareValues, req.path)
	defer cancel()
	serveLiveContent(ctx, bifrostCtx, h.gateway.client, req.providerKey, req.sessionID)
}

// serveLiveContent fetches the recording through the client and writes it as the provider served it.
// The provider's session id rides on the context so the log row names the session it belongs to.
func serveLiveContent(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, client liveContentClient, providerKey schemas.ModelProvider, sessionID string) {
	bifrostCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.LiveContentRequest)
	bifrostCtx.SetValue(schemas.BifrostContextKeyRealtimeProviderSessionID, sessionID)
	content, bifrostErr := client.LiveSessionContentRequest(bifrostCtx, &schemas.BifrostLiveContentRequest{Provider: providerKey, SessionID: sessionID})
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}
	ctx.Response.Header.Set("Content-Type", content.ContentType)
	ctx.Response.Header.Set("Content-Length", strconv.Itoa(len(content.Content)))
	ctx.SetBody(content.Content)
}
