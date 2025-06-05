package langgraph

import (
	"encoding/json"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// LangGraphRouter holds route registrations for langgraph endpoints.
type LangGraphRouter struct {
	client *bifrost.Bifrost
}

// NewLangGraphRouter creates a new LangGraphRouter with the given bifrost client.
func NewLangGraphRouter(client *bifrost.Bifrost) *LangGraphRouter {
	return &LangGraphRouter{client: client}
}

// RegisterRoutes registers all langgraph routes on the given router.
func (l *LangGraphRouter) RegisterRoutes(r *router.Router) {
	r.POST("/langgraph/v1/chat/completions", l.handleChatCompletion)
}

// handleChatCompletion handles POST /langgraph/v1/chat/completions
func (l *LangGraphRouter) handleChatCompletion(ctx *fasthttp.RequestCtx) {
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
n
