package mistral

import (
	"encoding/json"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// MistralRouter holds route registrations for mistral endpoints.
type MistralRouter struct {
	client *bifrost.Bifrost
}

// NewMistralRouter creates a new MistralRouter with the given bifrost client.
func NewMistralRouter(client *bifrost.Bifrost) *MistralRouter {
	return &MistralRouter{client: client}
}

// RegisterRoutes registers all mistral routes on the given router.
func (m *MistralRouter) RegisterRoutes(r *router.Router) {
	r.POST("/mistral/v1/chat/completions", m.handleChatCompletion)
}

// handleChatCompletion handles POST /mistral/v1/chat/completions
func (m *MistralRouter) handleChatCompletion(ctx *fasthttp.RequestCtx) {
	var req ChatCompletionRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		json.NewEncoder(ctx).Encode(err)
		return
	}

	bifrostReq := req.ConvertToBifrostRequest("")
	bifrostCtx := lib.ConvertToBifrostContext(ctx)

	result, err := m.client.ChatCompletionRequest(*bifrostCtx, bifrostReq)
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
