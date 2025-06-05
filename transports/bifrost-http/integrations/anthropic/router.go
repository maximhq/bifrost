package anthropic

import (
	"encoding/json"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// AnthropicRouter holds route registrations for anthropic endpoints.
type AnthropicRouter struct {
	client *bifrost.Bifrost
}

// NewAnthropicRouter creates a new AnthropicRouter with the given bifrost client.
func NewAnthropicRouter(client *bifrost.Bifrost) *AnthropicRouter {
	return &AnthropicRouter{client: client}
}

// RegisterRoutes registers all anthropic routes on the given router.
func (a *AnthropicRouter) RegisterRoutes(r *router.Router) {
	r.POST("/anthropic/v1/messages", a.handleChatCompletion)
}

// handleChatCompletion handles POST /anthropic/v1/messages
func (a *AnthropicRouter) handleChatCompletion(ctx *fasthttp.RequestCtx) {
	var req ChatCompletionRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		json.NewEncoder(ctx).Encode(err)
		return
	}

	bifrostReq := req.ConvertToBifrostRequest("")
	bifrostCtx := lib.ConvertToBifrostContext(ctx)

	result, err := a.client.ChatCompletionRequest(*bifrostCtx, bifrostReq)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		json.NewEncoder(ctx).Encode(err)
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	json.NewEncoder(ctx).Encode(result)
}
n
