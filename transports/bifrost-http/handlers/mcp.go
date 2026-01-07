// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains MCP (Model Context Protocol) tool execution handlers.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

type MCPManager interface {
	ReconnectMCPClient(ctx context.Context, id string) error
	AddMCPClient(ctx context.Context, clientConfig schemas.MCPClientConfig) error
	RemoveMCPClient(ctx context.Context, id string) error
	EditMCPClient(ctx context.Context, id string, updatedConfig configstoreTables.TableMCPClient) error
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
func (h *MCPHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/mcp/clients", lib.ChainMiddlewares(h.getMCPClients, middlewares...))
	r.POST("/api/mcp/client", lib.ChainMiddlewares(h.addMCPClient, middlewares...))
	r.PUT("/api/mcp/client/{id}", lib.ChainMiddlewares(h.editMCPClient, middlewares...))
	r.DELETE("/api/mcp/client/{id}", lib.ChainMiddlewares(h.removeMCPClient, middlewares...))
	r.POST("/api/mcp/client/{id}/reconnect", lib.ChainMiddlewares(h.reconnectMCPClient, middlewares...))
}

// MCPClientResponse represents the response structure for MCP clients
type MCPClientResponse struct {
	Config configstoreTables.TableMCPClient `json:"config"`
	Tools  []schemas.ChatToolFunction       `json:"tools"`
	State  schemas.MCPConnectionState       `json:"state"`
}

// getMCPClients handles GET /api/mcp/clients - Get all MCP clients
func (h *MCPHandler) getMCPClients(ctx *fasthttp.RequestCtx) {
	// Get clients from store config
	configsInStore := h.store.MCPConfig
	if configsInStore == nil {
		SendJSON(ctx, []MCPClientResponse{})
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
	clients := make([]MCPClientResponse, 0, len(configsInStore.ClientConfigs))

	for _, configClient := range configsInStore.ClientConfigs {
		// Redact sensitive fields before sending to UI
		redactedConfig := h.store.RedactTableMCPClient(configClient)

		if connectedClient, exists := connectedClientsMap[configClient.ClientID]; exists {
			// Sort tools alphabetically by name
			sortedTools := make([]schemas.ChatToolFunction, len(connectedClient.Tools))
			copy(sortedTools, connectedClient.Tools)
			sort.Slice(sortedTools, func(i, j int) bool {
				return sortedTools[i].Name < sortedTools[j].Name
			})

			clients = append(clients, MCPClientResponse{
				Config: redactedConfig,
				Tools:  sortedTools,
				State:  connectedClient.State,
			})
		} else {
			// Client is in config but not connected, mark as errored
			clients = append(clients, MCPClientResponse{
				Config: redactedConfig,
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
	if err := h.mcpManager.ReconnectMCPClient(ctx, id); err != nil {
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
	var req configstoreTables.TableMCPClient
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}
	if err := validateToolsToExecute(req.ToolsToExecute); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_execute: %v", err))
		return
	}
	// Auto-clear tools_to_auto_execute if tools_to_execute is empty
	// If no tools are allowed to execute, no tools can be auto-executed
	if len(req.ToolsToExecute) == 0 {
		req.ToolsToAutoExecute = []string{}
	}
	if err := validateToolsToAutoExecute(req.ToolsToAutoExecute, req.ToolsToExecute); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_auto_execute: %v", err))
		return
	}
	if err := validateMCPClientName(req.Name); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid client name: %v", err))
		return
	}

	// Convert to schemas.MCPClientConfig for the runtime MCP manager
	schemasConfig := schemas.MCPClientConfig{
		ID:                 req.ClientID,
		Name:               req.Name,
		IsCodeModeClient:   req.IsCodeModeClient,
		ConnectionType:     schemas.MCPConnectionType(req.ConnectionType),
		ConnectionString:   req.ConnectionString,
		StdioConfig:        req.StdioConfig,
		ToolsToExecute:     req.ToolsToExecute,
		ToolsToAutoExecute: req.ToolsToAutoExecute,
		Headers:            req.Headers,
	}

	if err := h.mcpManager.AddMCPClient(ctx, schemasConfig); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to connect MCP client: %v", err))
		return
	}
	// Creating MCP client config in config store
	if h.store.ConfigStore != nil {
		if err := h.store.ConfigStore.CreateMCPClientConfig(ctx, req); err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Added to core but failed to create MCP config in database: %v, please restart bifrost to keep core and database in sync", err))
			return
		}
	}
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client connected successfully",
	})
}

// editMCPClient handles PUT /api/mcp/client/{id} - Edit MCP client
func (h *MCPHandler) editMCPClient(ctx *fasthttp.RequestCtx) {
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid id: %v", err))
		return
	}

	// Accept the full table client config to support tool_pricing
	var req configstoreTables.TableMCPClient
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	req.ClientID = id

	// Validate tools_to_execute
	if err := validateToolsToExecute(req.ToolsToExecute); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_execute: %v", err))
		return
	}
	// Auto-clear tools_to_auto_execute if tools_to_execute is empty
	// If no tools are allowed to execute, no tools can be auto-executed
	if len(req.ToolsToExecute) == 0 {
		req.ToolsToAutoExecute = []string{}
	}
	// Validate tools_to_auto_execute
	if err := validateToolsToAutoExecute(req.ToolsToAutoExecute, req.ToolsToExecute); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_auto_execute: %v", err))
		return
	}
	// Validate client name
	if err := validateMCPClientName(req.Name); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid client name: %v", err))
		return
	}

	// Get existing config to handle redacted values
	var existingConfig *configstoreTables.TableMCPClient
	if h.store.MCPConfig != nil {
		for i, client := range h.store.MCPConfig.ClientConfigs {
			if client.ClientID == id {
				existingConfig = &h.store.MCPConfig.ClientConfigs[i]
				break
			}
		}
	}

	if existingConfig == nil {
		SendError(ctx, fasthttp.StatusNotFound, "MCP client not found")
		return
	}

	// Merge redacted values - preserve old values if incoming values are redacted and unchanged
	req = mergeMCPRedactedValues(req, *existingConfig, h.store.RedactTableMCPClient(*existingConfig))

	// EditMCPClient internally handles database update, env vars, and runtime update
	if err := h.mcpManager.EditMCPClient(ctx, id, req); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to edit MCP client: %v", err))
		return
	}
	// Update MCP client config in config store with merged values
	if h.store.ConfigStore != nil {
		if err := h.store.ConfigStore.UpdateMCPClientConfig(ctx, id, mergedConfig); err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Updated in core but failed to save MCP config in database: %v, please restart bifrost to keep core and database in sync", err))
			return
		}
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
	// Deleting MCP client config from config store
	if h.store.ConfigStore != nil {
		if err := h.store.ConfigStore.DeleteMCPClientConfig(ctx, id); err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Removed from core but failed to delete MCP config from database: %v, please restart bifrost to keep core and database in sync", err))
			return
		}
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

func validateToolsToAutoExecute(toolsToAutoExecute []string, toolsToExecute []string) error {
	if len(toolsToAutoExecute) > 0 {
		// Check if wildcard "*" is combined with other tool names
		hasWildcard := slices.Contains(toolsToAutoExecute, "*")
		if hasWildcard && len(toolsToAutoExecute) > 1 {
			return fmt.Errorf("wildcard '*' cannot be combined with other tool names")
		}

		// Check for duplicate entries
		seen := make(map[string]bool)
		for _, tool := range toolsToAutoExecute {
			if seen[tool] {
				return fmt.Errorf("duplicate tool name '%s'", tool)
			}
			seen[tool] = true
		}

		// Check that all tools in ToolsToAutoExecute are also in ToolsToExecute
		// Create a set of allowed tools from ToolsToExecute
		allowedTools := make(map[string]bool)
		hasWildcardInExecute := slices.Contains(toolsToExecute, "*")
		if hasWildcardInExecute {
			// If "*" is in ToolsToExecute, all tools are allowed
			return nil
		}
		for _, tool := range toolsToExecute {
			allowedTools[tool] = true
		}

		// Validate each tool in ToolsToAutoExecute
		for _, tool := range toolsToAutoExecute {
			if tool == "*" {
				// Wildcard is allowed if "*" is in ToolsToExecute
				if !hasWildcardInExecute {
					return fmt.Errorf("tool '%s' in tools_to_auto_execute is not in tools_to_execute", tool)
				}
			} else if !allowedTools[tool] {
				return fmt.Errorf("tool '%s' in tools_to_auto_execute is not in tools_to_execute", tool)
			}
		}
	}

	return nil
}

// mergeMCPClientConfig merges an updated MCP client config with the old config to preserve sensitive fields.
// This follows the same pattern as provider config merging to handle redacted values correctly.
//
// The merge logic:
//   - If a field in the updated config is redacted and matches the old redacted value, it means the user
//     didn't actually modify that field, so we preserve the original raw value
//   - If a field is missing or nil in the updated config, we preserve the old value
//   - Otherwise, we use the updated value
//
// This prevents accidentally overwriting sensitive data (headers) with redacted
// versions when the user only intended to update other fields like IsCodeModeClient.
func mergeMCPClientConfig(oldConfig *schemas.MCPClientConfig, oldRedactedConfig *schemas.MCPClientConfig, updatedConfig schemas.MCPClientConfig) schemas.MCPClientConfig {
	// If old config doesn't exist, return the updated config as-is (new client case)
	if oldConfig == nil {
		return updatedConfig
	}

	// If old redacted config doesn't exist, use old config for comparison
	if oldRedactedConfig == nil {
		oldRedactedConfig = oldConfig
	}

	// Start with the updated config as base
	merged := updatedConfig

	// Preserve connection_type if not provided in update
	if merged.ConnectionType == "" && oldConfig.ConnectionType != "" {
		merged.ConnectionType = oldConfig.ConnectionType
	}

	// Preserve stdio_config if not provided in update
	if merged.StdioConfig == nil && oldConfig.StdioConfig != nil {
		merged.StdioConfig = oldConfig.StdioConfig
	}

	// Handle connection_string: preserve if nil, or restore original if redacted matches old redacted value
	if merged.ConnectionString == nil && oldConfig.ConnectionString != nil {
		merged.ConnectionString = oldConfig.ConnectionString
	} else if merged.ConnectionString != nil && merged.ConnectionString.IsRedacted() {
		// If updated connection_string is redacted and matches old redacted value, restore original
		if oldRedactedConfig.ConnectionString != nil && merged.ConnectionString.Equals(oldRedactedConfig.ConnectionString) {
			merged.ConnectionString = oldConfig.ConnectionString
		}
	}

	// Merge Headers: preserve old headers if new headers are empty or redacted
	if len(updatedConfig.Headers) == 0 {
		// If no new headers provided, keep old headers
		merged.Headers = oldConfig.Headers
	} else {
		// Merge individual headers
		if merged.Headers == nil {
			merged.Headers = make(map[string]schemas.EnvVar)
		}

		oldHeaders := oldConfig.Headers
		if oldHeaders == nil {
			oldHeaders = make(map[string]schemas.EnvVar)
		}

		// Process each header in the updated config
		for headerKey, newHeaderValue := range updatedConfig.Headers {
			// Check if this header is redacted and matches the old redacted value
			if newHeaderValue.IsRedacted() {
				oldRedactedValue, hasOldRedacted := oldRedactedConfig.Headers[headerKey]
				if hasOldRedacted && newHeaderValue.Equals(&oldRedactedValue) {
					// User didn't change this header, restore original value
					if oldValue, hasOld := oldHeaders[headerKey]; hasOld {
						merged.Headers[headerKey] = oldValue
					}
				}
			}
		}

		// Preserve any old headers that weren't included in the updated config
		for headerKey, oldHeaderValue := range oldHeaders {
			if _, exists := updatedConfig.Headers[headerKey]; !exists {
				merged.Headers[headerKey] = oldHeaderValue
			}
		}
	}

	return merged
}

func validateMCPClientName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("client name is required")
	}
	for _, r := range name {
		if r > 127 { // non-ASCII
			return fmt.Errorf("name must contain only ASCII characters")
		}
	}
	if strings.Contains(name, "-") {
		return fmt.Errorf("client name cannot contain hyphens")
	}
	if strings.Contains(name, " ") {
		return fmt.Errorf("client name cannot contain spaces")
	}
	if len(name) > 0 && name[0] >= '0' && name[0] <= '9' {
		return fmt.Errorf("client name cannot start with a number")
	}
	return nil
}

// mergeMCPRedactedValues merges incoming MCP client config with existing config,
// preserving old values when incoming values are redacted and unchanged.
// This follows the same pattern as provider config updates.
func mergeMCPRedactedValues(incoming, oldRaw, oldRedacted configstoreTables.TableMCPClient) configstoreTables.TableMCPClient {
	merged := incoming

	// Handle ConnectionString - if incoming is redacted and equals old redacted, keep old raw value
	if incoming.ConnectionString != nil && oldRaw.ConnectionString != nil && oldRedacted.ConnectionString != nil {
		if incoming.ConnectionString.IsRedacted() && incoming.ConnectionString.Equals(oldRedacted.ConnectionString) {
			merged.ConnectionString = oldRaw.ConnectionString
		}
	}

	// Handle Headers - for each header, check if it's redacted and unchanged
	if incoming.Headers != nil && oldRaw.Headers != nil && oldRedacted.Headers != nil {
		merged.Headers = make(map[string]schemas.EnvVar, len(incoming.Headers))
		for key, incomingValue := range incoming.Headers {
			if oldRedactedValue, existsInRedacted := oldRedacted.Headers[key]; existsInRedacted {
				if oldRawValue, existsInRaw := oldRaw.Headers[key]; existsInRaw {
					// If incoming value is redacted and equals old redacted value, use old raw value
					if incomingValue.IsRedacted() && incomingValue.Equals(&oldRedactedValue) {
						merged.Headers[key] = oldRawValue
					} else {
						merged.Headers[key] = incomingValue
					}
					continue
				}
			}
			// New header or changed header
			merged.Headers[key] = incomingValue
		}
	}

	return merged
}
