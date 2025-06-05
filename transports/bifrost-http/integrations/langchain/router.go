package langchain

import (
	"encoding/json"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// LangChainRouter holds route registrations for langchain endpoints.
type LangChainRouter struct {
	client *bifrost.Bifrost
}

// NewLangChainRouter creates a new LangChainRouter with the given bifrost client.
func NewLangChainRouter(client *bifrost.Bifrost) *LangChainRouter {
	return &LangChainRouter{client: client}
}

// RegisterRoutes registers all langchain routes on the given router.
func (l *LangChainRouter) RegisterRoutes(r *router.Router) {
	r.POST("/langchain/v1/chat/completions", l.handleChatCompletion)
}

// handleChatCompletion handles POST /langchain/v1/chat/completions
func (l *LangChainRouter) handleChatCompletion(ctx *fasthttp.RequestCtx) {
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
