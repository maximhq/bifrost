// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains MCP (Model Context Protocol) tool execution handlers.
package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// MCPHandler manages HTTP requests for MCP tool operations
type MCPHandler struct {
	client *bifrost.Bifrost
	logger schemas.Logger
}

// NewMCPHandler creates a new MCP handler instance
func NewMCPHandler(client *bifrost.Bifrost, logger schemas.Logger) *MCPHandler {
	return &MCPHandler{
		client: client,
		logger: logger,
	}
}

// RegisterRoutes registers all MCP-related routes
func (h *MCPHandler) RegisterRoutes(r *router.Router) {
	// MCP tool execution endpoint
	r.POST("/v1/mcp/tool/execute", h.ExecuteTool)
}

// ExecuteTool handles POST /v1/mcp/tool/execute - Execute MCP tool
func (h *MCPHandler) ExecuteTool(ctx *fasthttp.RequestCtx) {
	var req schemas.ToolCall
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	// Validate required fields
	if req.Function.Name == nil || *req.Function.Name == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Tool function name is required", h.logger)
		return
	}

	// Convert context
	bifrostCtx := lib.ConvertToBifrostContext(ctx)
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context", h.logger)
		return
	}

	// Execute MCP tool
	resp, bifrostErr := h.client.ExecuteMCPTool(*bifrostCtx, req)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr, h.logger)
		return
	}

	// Send successful response
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	if encodeErr := json.NewEncoder(ctx).Encode(resp); encodeErr != nil {
		h.logger.Warn(fmt.Sprintf("Failed to encode response: %v", encodeErr))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to encode response: %v", encodeErr), h.logger)
	}
}
