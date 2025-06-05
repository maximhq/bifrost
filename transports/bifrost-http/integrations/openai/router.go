package openai

import (
	"encoding/json"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// OpenAIRouter holds route registrations for openai endpoints.
type OpenAIRouter struct {
	client *bifrost.Bifrost
}

// NewOpenAIRouter creates a new OpenAIRouter with the given bifrost client.
func NewOpenAIRouter(client *bifrost.Bifrost) *OpenAIRouter {
	return &OpenAIRouter{client: client}
}

// RegisterRoutes registers all openai routes on the given router.
func (o *OpenAIRouter) RegisterRoutes(r *router.Router) {
	r.POST("/openai/v1/chat/completions", o.handleChatCompletion)
}

// handleChatCompletion handles POST /openai/v1/chat/completions
func (o *OpenAIRouter) handleChatCompletion(ctx *fasthttp.RequestCtx) {
	var req ChatCompletionRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		json.NewEncoder(ctx).Encode(err)
		return
	}

	bifrostReq := req.ConvertToBifrostRequest("")
	bifrostCtx := lib.ConvertToBifrostContext(ctx)

	result, err := o.client.ChatCompletionRequest(*bifrostCtx, bifrostReq)
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
