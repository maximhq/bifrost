package langchain

import (
	"encoding/json"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// LangChainRouter holds route registrations for LangChain endpoints.
type LangChainRouter struct {
	client *bifrost.Bifrost
}

// NewLangChainRouter creates a new LangChainRouter with the given bifrost client.
func NewLangChainRouter(client *bifrost.Bifrost) *LangChainRouter {
	return &LangChainRouter{client: client}
}

// RegisterRoutes registers all LangChain routes on the given router.
func (l *LangChainRouter) RegisterRoutes(r *router.Router) {
	r.POST("/langchain/chat", l.handleChatInvoke)
	r.POST("/langchain/invoke", l.handleInvoke)
	r.POST("/langchain/batch", l.handleBatch)
	r.POST("/langchain/stream", l.handleStream)
}

// handleChatInvoke handles POST /langchain/chat - simplified chat interface
func (l *LangChainRouter) handleChatInvoke(ctx *fasthttp.RequestCtx) {
	var req LangChainChatRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		json.NewEncoder(ctx).Encode(map[string]string{"error": err.Error()})
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
		json.NewEncoder(ctx).Encode(err)
		return
	}

	langchainResponse := DeriveLangChainFromBifrostResponse(result)
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	json.NewEncoder(ctx).Encode(langchainResponse)
}

// handleInvoke handles POST /langchain/invoke - general invoke interface
func (l *LangChainRouter) handleInvoke(ctx *fasthttp.RequestCtx) {
	var req LangChainInvokeRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		json.NewEncoder(ctx).Encode(map[string]string{"error": err.Error()})
		return
	}

	bifrostReq := req.ConvertToBifrostRequest()

	bifrostCtx := lib.ConvertToBifrostContext(ctx)

	var result *schemas.BifrostResponse
	var err *schemas.BifrostError

	if req.Type == "chat" {
		result, err = l.client.ChatCompletionRequest(*bifrostCtx, bifrostReq)
	} else {
		result, err = l.client.TextCompletionRequest(*bifrostCtx, bifrostReq)
	}

	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		json.NewEncoder(ctx).Encode(err)
		return
	}

	langchainResponse := DeriveLangChainInvokeFromBifrostResponse(result, req.Type)
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	json.NewEncoder(ctx).Encode(langchainResponse)
}

// handleBatch handles POST /langchain/batch - batch processing
func (l *LangChainRouter) handleBatch(ctx *fasthttp.RequestCtx) {
	var req LangChainBatchRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		json.NewEncoder(ctx).Encode(map[string]string{"error": err.Error()})
		return
	}

	bifrostCtx := lib.ConvertToBifrostContext(ctx)
	var responses []LangChainInvokeResponse

	// Process each request in the batch
	for _, input := range req.Inputs {
		bifrostReq := input.ConvertToBifrostRequest()

		var result *schemas.BifrostResponse
		var err *schemas.BifrostError

		if input.Type == "chat" {
			result, err = l.client.ChatCompletionRequest(*bifrostCtx, bifrostReq)
		} else {
			result, err = l.client.TextCompletionRequest(*bifrostCtx, bifrostReq)
		}

		if err != nil {
			// Add error response to batch
			responses = append(responses, LangChainInvokeResponse{
				Error: err.Error.Message,
			})
		} else {
			langchainResponse := DeriveLangChainInvokeFromBifrostResponse(result, input.Type)
			responses = append(responses, *langchainResponse)
		}
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	json.NewEncoder(ctx).Encode(LangChainBatchResponse{
		Results: responses,
	})
}

// handleStream handles POST /langchain/stream - streaming interface (simplified)
func (l *LangChainRouter) handleStream(ctx *fasthttp.RequestCtx) {
	// For now, we'll return a non-streaming response
	// In production, you'd implement Server-Sent Events or WebSocket streaming
	l.handleInvoke(ctx)
}
