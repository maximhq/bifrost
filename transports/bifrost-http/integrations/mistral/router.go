package mistral

import (
	"encoding/json"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// MistralRouter holds route registrations for Mistral endpoints.
type MistralRouter struct {
	client *bifrost.Bifrost
}

// NewMistralRouter creates a new MistralRouter with the given bifrost client.
func NewMistralRouter(client *bifrost.Bifrost) *MistralRouter {
	return &MistralRouter{client: client}
}

// RegisterRoutes registers all Mistral routes on the given router.
func (m *MistralRouter) RegisterRoutes(r *router.Router) {
	r.POST("/mistral/v1/chat/completions", m.handleChatCompletion)
}

// handleChatCompletion handles POST /v1/chat/completions
func (m *MistralRouter) handleChatCompletion(ctx *fasthttp.RequestCtx) {
	var req MistralChatRequest
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

	result, err := m.client.ChatCompletionRequest(*bifrostCtx, bifrostReq)
	if err != nil {
		// Determine appropriate HTTP status code based on error details
		statusCode := fasthttp.StatusInternalServerError // Default to 500

		// If the error has a specific status code from the provider, use it
		if err.StatusCode != nil {
			statusCode = *err.StatusCode
		} else if !err.IsBifrostError {
			// If it's not a Bifrost internal error, treat as client error
			statusCode = fasthttp.StatusBadRequest
		}

		ctx.SetStatusCode(statusCode)
		ctx.SetContentType("application/json")
		jsonBytes, _ := json.Marshal(err)
		ctx.SetBody(jsonBytes)
		return
	}

	mistralResponse := DeriveMistralFromBifrostResponse(result)
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	jsonBytes, _ := json.Marshal(mistralResponse)
	ctx.SetBody(jsonBytes)
}
