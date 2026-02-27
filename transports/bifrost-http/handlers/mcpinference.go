package handlers

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

type MCPInferenceHandler struct {
	client *bifrost.Bifrost
	store  *lib.Config
}

// NewMCPInferenceHandler creates a new MCP inference handler instance
func NewMCPInferenceHandler(client *bifrost.Bifrost, store *lib.Config) *MCPInferenceHandler {
	return &MCPInferenceHandler{
		client: client,
		store:  store,
	}
}

// RegisterRoutes registers the MCP inference routes
func (h *MCPInferenceHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.POST("/v1/mcp/tool/execute", lib.ChainMiddlewares(h.executeTool, middlewares...))
}

// executeTool handles POST /v1/mcp/tool/execute - Execute MCP tool
func (h *MCPInferenceHandler) executeTool(ctx *fasthttp.RequestCtx) {
	// Check format query parameter
	format := strings.ToLower(string(ctx.QueryArgs().Peek("format")))
	switch format {
	case "chat", "":
		h.executeChatMCPTool(ctx)
	case "responses":
		h.executeResponsesMCPTool(ctx)
	default:
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid format value, must be 'chat' or 'responses'")
		return
	}
}

// executeChatMCPTool handles POST /v1/mcp/tool/execute?format=chat - Execute MCP tool
func (h *MCPInferenceHandler) executeChatMCPTool(ctx *fasthttp.RequestCtx) {
	var req schemas.ChatAssistantMessageToolCall
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	// Validate required fields
	if req.Function.Name == nil || *req.Function.Name == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Tool function name is required")
		return
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, false, h.store.GetHeaderFilterConfig())
	defer cancel() // Ensure cleanup on function exit
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	// Execute MCP tool
	toolMessage, bifrostErr := h.client.ExecuteChatMCPTool(bifrostCtx, &req)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Send successful response
	SendJSON(ctx, toolMessage)
}

// executeResponsesMCPTool handles POST /v1/mcp/tool/execute?format=responses - Execute MCP tool
func (h *MCPInferenceHandler) executeResponsesMCPTool(ctx *fasthttp.RequestCtx) {
	var req schemas.ResponsesToolMessage
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	// Validate required fields
	if req.Name == nil || *req.Name == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Tool function name is required")
		return
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, false, h.store.GetHeaderFilterConfig())
	defer cancel() // Ensure cleanup on function exit
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	// Execute MCP tool
	toolMessage, bifrostErr := h.client.ExecuteResponsesMCPTool(bifrostCtx, &req)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Send successful response
	SendJSON(ctx, toolMessage)
}
