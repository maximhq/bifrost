package genai

import (
	"encoding/json"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// GenAIRouter holds route registrations for genai endpoints.
type GenAIRouter struct {
	client *bifrost.Bifrost
}

// NewGenAIRouter creates a new GenAIRouter with the given bifrost client.
func NewGenAIRouter(client *bifrost.Bifrost) *GenAIRouter {
	return &GenAIRouter{client: client}
}

// RegisterRoutes registers all genai routes on the given router.
func (g *GenAIRouter) RegisterRoutes(r *router.Router) {
	r.POST("/genai/v1beta/models/{model}", g.handleChatCompletion)
}

// handleChatCompletion handles POST /genai/v1beta/models/{model}
func (g *GenAIRouter) handleChatCompletion(ctx *fasthttp.RequestCtx) {
	model := ctx.UserValue("model")
	if model == nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetBodyString("Model parameter is required")
		return
	}
	modelStr := model.(string)
	modelStr = modelStr[:len(modelStr)-len(":generateContent")]
	if len(modelStr) > 0 && modelStr[len(modelStr)-1] == ':' {
		modelStr = modelStr[:len(modelStr)-1]
	}

	var req GeminiChatRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		json.NewEncoder(ctx).Encode(err)
		return
	}

	bifrostReq := req.ConvertToBifrostRequest("google/" + modelStr)

	bifrostCtx := lib.ConvertToBifrostContext(ctx)

	result, err := g.client.ChatCompletionRequest(schemas.Vertex, bifrostReq, *bifrostCtx)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		json.NewEncoder(ctx).Encode(err)
		return
	}

	genAIResponse := DeriveGenAIFromBifrostResponse(result)
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	json.NewEncoder(ctx).Encode(genAIResponse)
}
