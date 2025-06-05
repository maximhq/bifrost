package litellm

import (
	"encoding/json"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// LiteLLMRouter holds route registrations for litellm endpoints.
type LiteLLMRouter struct {
	client *bifrost.Bifrost
}

// NewLiteLLMRouter creates a new LiteLLMRouter with the given bifrost client.
func NewLiteLLMRouter(client *bifrost.Bifrost) *LiteLLMRouter {
	return &LiteLLMRouter{client: client}
}

// RegisterRoutes registers all litellm routes on the given router.
func (l *LiteLLMRouter) RegisterRoutes(r *router.Router) {
	r.POST("/litellm/v1/chat/completions", l.handleChatCompletion)
}

// handleChatCompletion handles POST /litellm/v1/chat/completions
func (l *LiteLLMRouter) handleChatCompletion(ctx *fasthttp.RequestCtx) {
	var req ChatCompletionRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		json.NewEncoder(ctx).Encode(err)
		return
	}

	bifrostReq := req.ConvertToBifrostRequest("")
	bifrostCtx := lib.ConvertToBifrostContext(ctx)

	result, err := l.client.ChatCompletionRequest(*bifrostCtx, bifrostReq)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		json.NewEncoder(ctx).Encode(err)
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	json.NewEncoder(ctx).Encode(result)
}
