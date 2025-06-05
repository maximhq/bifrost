package openai

import (
	"encoding/json"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// OpenAIRouter holds route registrations for OpenAI endpoints.
type OpenAIRouter struct {
	client *bifrost.Bifrost
}

// NewOpenAIRouter creates a new OpenAIRouter with the given bifrost client.
func NewOpenAIRouter(client *bifrost.Bifrost) *OpenAIRouter {
	return &OpenAIRouter{client: client}
}

// RegisterRoutes registers all OpenAI routes on the given router.
func (o *OpenAIRouter) RegisterRoutes(r *router.Router) {
	r.POST("/openai/v1/chat/completions", o.handleChatCompletion)
}

// handleChatCompletion handles POST /v1/chat/completions
func (o *OpenAIRouter) handleChatCompletion(ctx *fasthttp.RequestCtx) {
	var req OpenAIChatRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		errorResponse, _ := json.Marshal(map[string]string{"error": err.Error()})
		ctx.SetBody(errorResponse)
		return
	}

	if req.Model == "" {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetBodyString("Model parameter is required")
		return
	}

	bifrostReq := req.ConvertToBifrostRequest()

	bifrostCtx := lib.ConvertToBifrostContext(ctx)

	result, err := o.client.ChatCompletionRequest(*bifrostCtx, bifrostReq)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		errorResponse, _ := json.Marshal(err)
		ctx.SetBody(errorResponse)
		return
	}

	openaiResponse := DeriveOpenAIFromBifrostResponse(result)
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	responseBody, _ := json.Marshal(openaiResponse)
	ctx.SetBody(responseBody)
}
