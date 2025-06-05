package litellm

import (
	"encoding/json"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// LiteLLMRouter holds route registrations for LiteLLM endpoints.
type LiteLLMRouter struct {
	client *bifrost.Bifrost
}

// NewLiteLLMRouter creates a new LiteLLMRouter with the given bifrost client.
func NewLiteLLMRouter(client *bifrost.Bifrost) *LiteLLMRouter {
	return &LiteLLMRouter{client: client}
}

// RegisterRoutes registers all LiteLLM routes on the given router.
func (l *LiteLLMRouter) RegisterRoutes(r *router.Router) {
	r.POST("/litellm/chat/completions", l.handleChatCompletion)
	r.POST("/litellm/v1/chat/completions", l.handleChatCompletion)
	r.POST("/litellm/completions", l.handleCompletion)
	r.POST("/litellm/v1/completions", l.handleCompletion)
}

// handleChatCompletion handles POST /chat/completions and /v1/chat/completions
func (l *LiteLLMRouter) handleChatCompletion(ctx *fasthttp.RequestCtx) {
	var req LiteLLMChatRequest
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

	bifrostReq := req.ConvertToBifrostRequest()

	bifrostCtx := lib.ConvertToBifrostContext(ctx)

	result, err := l.client.ChatCompletionRequest(*bifrostCtx, bifrostReq)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetContentType("application/json")
		jsonBytes, _ := json.Marshal(err)
		ctx.SetBody(jsonBytes)
		return
	}

	litellmResponse := DeriveLiteLLMFromBifrostResponse(result)
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	jsonBytes, _ := json.Marshal(litellmResponse)
	ctx.SetBody(jsonBytes)
}

// handleCompletion handles POST /completions and /v1/completions
func (l *LiteLLMRouter) handleCompletion(ctx *fasthttp.RequestCtx) {
	var req LiteLLMCompletionRequest
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

	bifrostReq := req.ConvertToBifrostRequest()

	bifrostCtx := lib.ConvertToBifrostContext(ctx)

	result, err := l.client.TextCompletionRequest(*bifrostCtx, bifrostReq)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetContentType("application/json")
		jsonBytes, _ := json.Marshal(err)
		ctx.SetBody(jsonBytes)
		return
	}

	litellmResponse := DeriveLiteLLMCompletionFromBifrostResponse(result)
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	jsonBytes, _ := json.Marshal(litellmResponse)
	ctx.SetBody(jsonBytes)
}
