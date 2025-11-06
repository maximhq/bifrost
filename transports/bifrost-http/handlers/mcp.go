// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains MCP (Model Context Protocol) tool execution handlers.
package handlers

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// MCPHandler manages HTTP requests for MCP tool operations
type MCPHandler struct {
	client *bifrost.Bifrost
	store  *lib.Config
}

// NewMCPHandler creates a new MCP handler instance
func NewMCPHandler(client *bifrost.Bifrost, store *lib.Config) *MCPHandler {
	return &MCPHandler{
		client: client,
		store:  store,
	}
}

// RegisterRoutes registers all MCP-related routes
func (h *MCPHandler) RegisterRoutes(r *router.Router, middlewares ...lib.BifrostHTTPMiddleware) {
	// MCP tool execution endpoint
	r.POST("/v1/mcp/tool/execute", lib.ChainMiddlewares(h.executeTool, middlewares...))
	r.GET("/api/mcp/clients", lib.ChainMiddlewares(h.getMCPClients, middlewares...))
	r.POST("/api/mcp/client", lib.ChainMiddlewares(h.addMCPClient, middlewares...))
	r.PUT("/api/mcp/client/{name}", lib.ChainMiddlewares(h.editMCPClient, middlewares...))
	r.DELETE("/api/mcp/client/{name}", lib.ChainMiddlewares(h.removeMCPClient, middlewares...))
	r.POST("/api/mcp/client/{name}/reconnect", lib.ChainMiddlewares(h.reconnectMCPClient, middlewares...))
}

// executeTool handles POST /v1/mcp/tool/execute - Execute MCP tool
func (h *MCPHandler) executeTool(ctx *fasthttp.RequestCtx) {
	var req schemas.ChatAssistantMessageToolCall
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	// Validate required fields
	if req.Function.Name == nil || *req.Function.Name == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Tool function name is required")
		return
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, false)
	defer cancel() // Ensure cleanup on function exit
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	// Execute MCP tool
	resp, bifrostErr := h.client.ExecuteMCPTool(*bifrostCtx, req)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Send successful response
	SendJSON(ctx, resp)
}

// getMCPClients handles GET /api/mcp/clients - Get all MCP clients
func (h *MCPHandler) getMCPClients(ctx *fasthttp.RequestCtx) {
	// Get clients from store config
	configsInStore := h.store.MCPConfig
	if configsInStore == nil {
		SendJSON(ctx, []schemas.MCPClient{})
		return
	}

	// Get actual connected clients from Bifrost
	clientsInBifrost, err := h.client.GetMCPClients()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get MCP clients from Bifrost: %v", err))
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
			// Sort tools alphabetically by name
			sortedTools := make([]schemas.ChatToolFunction, len(connectedClient.Tools))
			copy(sortedTools, connectedClient.Tools)
			sort.Slice(sortedTools, func(i, j int) bool {
				return sortedTools[i].Name < sortedTools[j].Name
			})

			clients = append(clients, schemas.MCPClient{
				Name:   connectedClient.Name,
				Config: h.store.RedactMCPClientConfig(connectedClient.Config),
				Tools:  sortedTools,
				State:  connectedClient.State,
			})
		} else {
			// Client is in config but not connected, mark as errored
			clients = append(clients, schemas.MCPClient{
				Name:   configClient.Name,
				Config: h.store.RedactMCPClientConfig(configClient),
				Tools:  []schemas.ChatToolFunction{}, // No tools available since connection failed
				State:  schemas.MCPConnectionStateError,
			})
		}
	}

	SendJSON(ctx, clients)
}

// reconnectMCPClient handles POST /api/mcp/client/{name}/reconnect - Reconnect an MCP client
func (h *MCPHandler) reconnectMCPClient(ctx *fasthttp.RequestCtx) {
	name, err := getNameFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid name: %v", err))
		return
	}

	if err := h.client.ReconnectMCPClient(name); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to reconnect MCP client: %v", err))
		return
	}

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client reconnected successfully",
	})
}

// addMCPClient handles POST /api/mcp/client - Add a new MCP client
func (h *MCPHandler) addMCPClient(ctx *fasthttp.RequestCtx) {
	var req schemas.MCPClientConfig
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	if err := validateToolsToExecute(req.ToolsToExecute); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_execute: %v", err))
		return
	}

	if err := h.store.AddMCPClient(ctx, req); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to add MCP client: %v", err))
		return
	}

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client added successfully",
	})
}

// editMCPClient handles PUT /api/mcp/client/{name} - Edit MCP client
func (h *MCPHandler) editMCPClient(ctx *fasthttp.RequestCtx) {
	name, err := getNameFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid name: %v", err))
		return
	}

	var req schemas.MCPClientConfig
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	// Validate tools_to_execute
	if err := validateToolsToExecute(req.ToolsToExecute); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_execute: %v", err))
		return
	}

	if err := h.store.EditMCPClientTools(ctx, name, req.ToolsToExecute); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to edit MCP client tools: %v", err))
		return
	}

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client tools edited successfully",
	})
}

// removeMCPClient handles DELETE /api/mcp/client/{name} - Remove an MCP client
func (h *MCPHandler) removeMCPClient(ctx *fasthttp.RequestCtx) {
	name, err := getNameFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid name: %v", err))
		return
	}

	if err := h.store.RemoveMCPClient(ctx, name); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to remove MCP client: %v", err))
		return
	}

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client removed successfully",
	})
}

func getNameFromCtx(ctx *fasthttp.RequestCtx) (string, error) {
	nameValue := ctx.UserValue("name")
	if nameValue == nil {
		return "", fmt.Errorf("missing name parameter")
	}
	nameStr, ok := nameValue.(string)
	if !ok {
		return "", fmt.Errorf("invalid name parameter type")
	}

	return nameStr, nil
}

func validateToolsToExecute(toolsToExecute []string) error {
	if len(toolsToExecute) > 0 {
		// Check if wildcard "*" is combined with other tool names
		hasWildcard := slices.Contains(toolsToExecute, "*")
		if hasWildcard && len(toolsToExecute) > 1 {
			return fmt.Errorf("invalid tools_to_execute: wildcard '*' cannot be combined with other tool names")
		}

		// Check for duplicate entries
		seen := make(map[string]bool)
		for _, tool := range toolsToExecute {
			if seen[tool] {
				return fmt.Errorf("invalid tools_to_execute: duplicate tool name '%s'", tool)
			}
			seen[tool] = true
		}
	}

	return nil
}
