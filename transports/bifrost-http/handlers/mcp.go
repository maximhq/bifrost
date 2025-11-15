// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains MCP (Model Context Protocol) tool execution handlers.
package handlers

import (
	"context"
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

type MCPManager interface {
	AddMCPClient(ctx context.Context, clientConfig schemas.MCPClientConfig) error
	RemoveMCPClient(ctx context.Context, id string) error
	EditMCPClient(ctx context.Context, id string, updatedConfig schemas.MCPClientConfig) error
}

// MCPHandler manages HTTP requests for MCP tool operations
type MCPHandler struct {
	client     *bifrost.Bifrost
	store      *lib.Config
	mcpManager MCPManager
}

// NewMCPHandler creates a new MCP handler instance
func NewMCPHandler(mcpManager MCPManager, client *bifrost.Bifrost, store *lib.Config) *MCPHandler {
	return &MCPHandler{
		client:     client,
		store:      store,
		mcpManager: mcpManager,
	}
}

// RegisterRoutes registers all MCP-related routes
func (h *MCPHandler) RegisterRoutes(r *router.Router, middlewares ...lib.BifrostHTTPMiddleware) {
	// MCP tool execution endpoint
	r.POST("/v1/mcp/tool/execute", lib.ChainMiddlewares(h.executeTool, middlewares...))
	r.GET("/api/mcp/clients", lib.ChainMiddlewares(h.getMCPClients, middlewares...))
	r.POST("/api/mcp/client", lib.ChainMiddlewares(h.addMCPClient, middlewares...))
	r.PUT("/api/mcp/client/{id}", lib.ChainMiddlewares(h.editMCPClient, middlewares...))
	r.DELETE("/api/mcp/client/{id}", lib.ChainMiddlewares(h.removeMCPClient, middlewares...))
	r.POST("/api/mcp/client/{id}/reconnect", lib.ChainMiddlewares(h.reconnectMCPClient, middlewares...))
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
		connectedClientsMap[client.Config.ID] = client
	}

	// Build the final client list, including errored clients
	clients := make([]schemas.MCPClient, 0, len(configsInStore.ClientConfigs))

	for _, configClient := range configsInStore.ClientConfigs {
		if connectedClient, exists := connectedClientsMap[configClient.ID]; exists {
			// Sort tools alphabetically by name
			sortedTools := make([]schemas.ChatToolFunction, len(connectedClient.Tools))
			copy(sortedTools, connectedClient.Tools)
			sort.Slice(sortedTools, func(i, j int) bool {
				return sortedTools[i].Name < sortedTools[j].Name
			})

			clients = append(clients, schemas.MCPClient{
				Config: h.store.RedactMCPClientConfig(connectedClient.Config),
				Tools:  sortedTools,
				State:  connectedClient.State,
			})
		} else {
			// Client is in config but not connected, mark as errored
			clients = append(clients, schemas.MCPClient{
				Config: h.store.RedactMCPClientConfig(configClient),
				Tools:  []schemas.ChatToolFunction{}, // No tools available since connection failed
				State:  schemas.MCPConnectionStateError,
			})
		}
	}

	SendJSON(ctx, clients)
}

// reconnectMCPClient handles POST /api/mcp/client/{id}/reconnect - Reconnect an MCP client
func (h *MCPHandler) reconnectMCPClient(ctx *fasthttp.RequestCtx) {
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid id: %v", err))
		return
	}

	// Check if client is registered in Bifrost (can be not registered if client initialization failed)
	if clients, err := h.client.GetMCPClients(); err == nil && len(clients) > 0 {
		for _, client := range clients {
			if client.Config.ID == id {
				if err := h.client.ReconnectMCPClient(id); err != nil {
					SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to reconnect MCP client: %v", err))
					return
				} else {
					SendJSON(ctx, map[string]any{
						"status":  "success",
						"message": "MCP client reconnected successfully",
					})
					return
				}
			}
		}
	}

	// Config exists in store, but not in Bifrost (can happen if client initialization failed)
	clientConfig, err := h.store.GetMCPClient(id)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get MCP client config: %v", err))
		return
	}

	if err := h.client.AddMCPClient(*clientConfig); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to add MCP client: %v", err))
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
	if err := h.mcpManager.AddMCPClient(ctx, req); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to add MCP client: %v", err))
		return
	}
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client added successfully",
	})
}

// editMCPClient handles PUT /api/mcp/client/{id} - Edit MCP client
func (h *MCPHandler) editMCPClient(ctx *fasthttp.RequestCtx) {
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid id: %v", err))
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

	if err := h.mcpManager.EditMCPClient(ctx, id, req); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to edit MCP client: %v", err))
		return
	}

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client edited successfully",
	})
}

// removeMCPClient handles DELETE /api/mcp/client/{id} - Remove an MCP client
func (h *MCPHandler) removeMCPClient(ctx *fasthttp.RequestCtx) {
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid id: %v", err))
		return
	}
	if err := h.mcpManager.RemoveMCPClient(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to remove MCP client: %v", err))
		return
	}
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client removed successfully",
	})
}

func getIDFromCtx(ctx *fasthttp.RequestCtx) (string, error) {
	idValue := ctx.UserValue("id")
	if idValue == nil {
		return "", fmt.Errorf("missing id parameter")
	}
	idStr, ok := idValue.(string)
	if !ok {
		return "", fmt.Errorf("invalid id parameter type")
	}

	return idStr, nil
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
