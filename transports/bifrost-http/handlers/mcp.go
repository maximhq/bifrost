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
	store  *lib.ConfigStore
}

// NewMCPHandler creates a new MCP handler instance
func NewMCPHandler(client *bifrost.Bifrost, logger schemas.Logger, store *lib.ConfigStore) *MCPHandler {
	return &MCPHandler{
		client: client,
		logger: logger,
		store:  store,
	}
}

// RegisterRoutes registers all MCP-related routes
func (h *MCPHandler) RegisterRoutes(r *router.Router) {
	// MCP tool execution endpoint
	r.POST("/v1/mcp/tool/execute", h.ExecuteTool)
	r.GET("/mcp/clients", h.GetMCPClients)
	r.POST("/mcp/client", h.AddMCPClient)
	r.PUT("/mcp/client/{name}", h.EditMCPClientTools)
	r.DELETE("/mcp/client/{name}", h.RemoveMCPClient)
	r.POST("/mcp/client/{name}/reconnect", h.ReconnectMCPClient)
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

// GetMCPClients handles GET /mcp/clients - Get all MCP clients
func (h *MCPHandler) GetMCPClients(ctx *fasthttp.RequestCtx) {
	// Get clients from store config
	configsInStore := h.store.GetMCPConfig()
	if configsInStore == nil {
		SendJSON(ctx, []schemas.MCPClient{}, h.logger)
		return
	}

	// Get actual connected clients from Bifrost
	clientsInBifrost, err := h.client.GetMCPClients()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get MCP clients from Bifrost: %v", err), h.logger)
		return
	}

	// Create a map of connected clients for quick lookup
	connectedClientsMap := make(map[string]schemas.MCPClient)
	for _, client := range clientsInBifrost {
		connectedClientsMap[client.Name] = client
	}

	// Build the final client list, including errored clients
	clients := make([]schemas.MCPClient, 0, len(configsInStore.ClientConfigs))

	for _, configClient := range configsInStore.ClientConfigs {
		if connectedClient, exists := connectedClientsMap[configClient.Name]; exists {
			// Client is connected, use the actual client data
			clients = append(clients, connectedClient)
		} else {
			// Client is in config but not connected, mark as errored
			clients = append(clients, schemas.MCPClient{
				Name:   configClient.Name,
				Config: configClient,
				Tools:  []string{}, // No tools available since connection failed
				State:  schemas.MCPConnectionStateError,
			})
		}
	}

	SendJSON(ctx, clients, h.logger)
}

// ReconnectMCPClient handles POST /mcp/client/{name}/reconnect - Reconnect an MCP client
func (h *MCPHandler) ReconnectMCPClient(ctx *fasthttp.RequestCtx) {
	name := ctx.UserValue("name").(string)

	if err := h.client.ReconnectMCPClient(name); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to reconnect MCP client: %v", err), h.logger)
		return
	}

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client reconnected successfully",
	}, h.logger)
}

// AddMCPClient handles POST /mcp/client - Add a new MCP client
func (h *MCPHandler) AddMCPClient(ctx *fasthttp.RequestCtx) {
	var req schemas.MCPClientConfig
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	if err := h.store.AddMCPClient(req); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to add MCP client: %v", err), h.logger)
		return
	}

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client added successfully",
	}, h.logger)
}

// EditMCPClientTools handles PUT /mcp/client/{name} - Edit MCP client tools
func (h *MCPHandler) EditMCPClientTools(ctx *fasthttp.RequestCtx) {
	name := ctx.UserValue("name").(string)

	var req struct {
		ToolsToExecute []string `json:"tools_to_execute,omitempty"`
		ToolsToSkip    []string `json:"tools_to_skip,omitempty"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	if err := h.store.EditMCPClientTools(name, req.ToolsToExecute, req.ToolsToSkip); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to edit MCP client tools: %v", err), h.logger)
		return
	}

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client tools edited successfully",
	}, h.logger)
}

// RemoveMCPClient handles DELETE /mcp/client/{name} - Remove an MCP client
func (h *MCPHandler) RemoveMCPClient(ctx *fasthttp.RequestCtx) {
	name := ctx.UserValue("name").(string)

	if err := h.store.RemoveMCPClient(name); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to remove MCP client: %v", err), h.logger)
		return
	}

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client removed successfully",
	}, h.logger)
}
