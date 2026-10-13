package handlers

import (
	"io"
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
	serveLiveContent(ctx, bifrostCtx, h.gateway.client, req.providerKey, req.sessionID, cancel)
}

// serveLiveContent fetches the recording through the client and streams it as the provider served
// it. The provider's session id rides on the context so the log row names the session it belongs
// to. release ends the request context: on a refusal now, otherwise once the body has been sent,
// since cancelling earlier would close the upstream stream under the transport.
func serveLiveContent(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, client liveContentClient, providerKey schemas.ModelProvider, sessionID string, release func()) {
	bifrostCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.LiveContentRequest)
	bifrostCtx.SetValue(schemas.BifrostContextKeyRealtimeProviderSessionID, sessionID)
	content, bifrostErr := client.LiveSessionContentRequest(bifrostCtx, &schemas.BifrostLiveContentRequest{Provider: providerKey, SessionID: sessionID})
	if bifrostErr != nil {
		release()
		SendBifrostError(ctx, bifrostErr)
		return
	}
	ctx.Response.Header.Set("Content-Type", content.ContentType)
	// fasthttp reads the body until EOF when the size is unknown and closes it once sent.
	bodySize := -1
	if content.ContentLength > 0 {
		bodySize = int(content.ContentLength)
		ctx.Response.Header.Set("Content-Length", strconv.FormatInt(content.ContentLength, 10))
	}
	ctx.Response.SetBodyStream(&liveContentBody{ReadCloser: content.Body, release: release}, bodySize)
	// The post-hook middleware copies response bodies for plugins; that would drain the stream
	// before fasthttp sends it, so mark the request as streamed, as the large-response path does.
	ctx.SetUserValue(lib.FastHTTPUserValueLargeResponseMode, true)
}

// liveContentBody is a recording being sent: closing it releases the upstream response, then the
// request context. A client that hangs up midway ends the request first, so the upstream stream
// is abandoned the way a cancelled SSE stream is, instead of being drained to its end for nobody.
type liveContentBody struct {
	io.ReadCloser
	release func()
	sent    bool // the whole recording was read
}

func (b *liveContentBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.sent = true
	}
	return n, err
}

func (b *liveContentBody) Close() error {
	if !b.sent {
		b.endRequest()
	}
	err := b.ReadCloser.Close()
	b.endRequest()
	return err
}

func (b *liveContentBody) endRequest() {
	if b.release != nil {
		b.release()
		b.release = nil
	}
}
