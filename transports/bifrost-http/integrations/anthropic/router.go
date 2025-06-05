package anthropic

import (
	"encoding/json"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// AnthropicRouter holds route registrations for Anthropic endpoints.
type AnthropicRouter struct {
	client *bifrost.Bifrost
}

// NewAnthropicRouter creates a new AnthropicRouter with the given bifrost client.
func NewAnthropicRouter(client *bifrost.Bifrost) *AnthropicRouter {
	return &AnthropicRouter{client: client}
}

// RegisterRoutes registers all Anthropic routes on the given router.
func (a *AnthropicRouter) RegisterRoutes(r *router.Router) {
	r.POST("/anthropic/v1/messages", a.handleMessages)
}

// handleMessages handles POST /v1/messages
func (a *AnthropicRouter) handleMessages(ctx *fasthttp.RequestCtx) {
	var req AnthropicMessageRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetContentType("application/json")
		errResponse := map[string]string{"error": err.Error()}
		jsonBytes, _ := json.Marshal(errResponse)
		ctx.SetBody(jsonBytes)
		return
	}

	if req.Model == "" {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetBodyString("Model parameter is required")
		return
	}

	if req.MaxTokens == 0 {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetBodyString("max_tokens parameter is required")
		return
	}

	bifrostReq := req.ConvertToBifrostRequest()

	bifrostCtx := lib.ConvertToBifrostContext(ctx)

	result, err := a.client.ChatCompletionRequest(*bifrostCtx, bifrostReq)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetContentType("application/json")
		jsonBytes, _ := json.Marshal(err)
		ctx.SetBody(jsonBytes)
		return
	}

	anthropicResponse := DeriveAnthropicFromBifrostResponse(result)
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	jsonBytes, _ := json.Marshal(anthropicResponse)
	ctx.SetBody(jsonBytes)
}
