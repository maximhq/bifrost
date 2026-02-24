// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains MCP (Model Context Protocol) server implementation for HTTP streaming.
package handlers

import (
	"bufio"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// MCPToolExecutor interface defines the method needed for executing MCP tools
type MCPToolManager interface {
	GetAvailableMCPTools(ctx context.Context) []schemas.ChatTool
	ExecuteChatMCPTool(ctx context.Context, toolCall *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError)
	ExecuteResponsesMCPTool(ctx context.Context, toolCall *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError)
}

// MCPServerHandler manages HTTP requests for MCP server operations
// It implements the MCP protocol over HTTP streaming (SSE) for MCP clients
type MCPServerHandler struct {
	toolManager     MCPToolManager
	globalMCPServer *server.MCPServer
	vkMCPServers    map[string]*server.MCPServer // Map of vk value -> mcp server
	config          *lib.Config
	mu              sync.RWMutex
}

// NewMCPServerHandler creates a new MCP server handler instance
func NewMCPServerHandler(ctx context.Context, config *lib.Config, toolManager MCPToolManager) (*MCPServerHandler, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if toolManager == nil {
		return nil, fmt.Errorf("tool manager is required")
	}

	// Create MCP server instance using mcp-go
	globalMCPServer := server.NewMCPServer(
		"global",
		version,
		server.WithToolCapabilities(true),
	)

	handler := &MCPServerHandler{
		toolManager:     toolManager,
		globalMCPServer: globalMCPServer,
		config:          config,
		vkMCPServers:    make(map[string]*server.MCPServer),
	}

	if err := handler.SyncAllMCPServers(ctx); err != nil {
		return nil, fmt.Errorf("failed to sync all MCP servers: %w", err)
	}

	return handler, nil
}

// RegisterRoutes registers the MCP server route
func (h *MCPServerHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// MCP server endpoint - supports both POST (JSON-RPC) and GET (SSE)
	r.POST("/mcp", lib.ChainMiddlewares(h.handleMCPServer, middlewares...))
	r.GET("/mcp", lib.ChainMiddlewares(h.handleMCPServerSSE, middlewares...))
}

// handleMCPServer handles POST requests for MCP JSON-RPC 2.0 messages
func (h *MCPServerHandler) handleMCPServer(ctx *fasthttp.RequestCtx) {
	mcpServer, err := h.getMCPServerForRequest(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusUnauthorized, err.Error())
		return
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, false, h.config.GetHeaderFilterConfig())
	defer cancel()

	// Use mcp-go server to handle the request
	// HandleMessage processes JSON-RPC messages and returns appropriate responses
	response := mcpServer.HandleMessage(bifrostCtx, ctx.PostBody())

	// Check if response is nil (notification - no response needed)
	if response == nil {
		ctx.SetStatusCode(fasthttp.StatusOK)
		return
	}

	// Marshal and send response
	responseJSON, err := sonic.Marshal(response)
	if err != nil {
		logger.Warn(fmt.Sprintf("Failed to marshal MCP response: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to encode response: %v", err))
		return
	}

	ctx.SetContentType("application/json")
	ctx.SetBody(responseJSON)
}

// handleMCPServerSSE handles GET requests for MCP Server-Sent Events streaming
func (h *MCPServerHandler) handleMCPServerSSE(ctx *fasthttp.RequestCtx) {
	_, err := h.getMCPServerForRequest(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusUnauthorized, err.Error())
		return
	}

	// Set SSE headers
	ctx.SetContentType("text/event-stream")
	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.Response.Header.Set("Connection", "keep-alive")

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, false, h.config.GetHeaderFilterConfig())

	// Use streaming response writer
	ctx.Response.SetBodyStreamWriter(func(w *bufio.Writer) {
		defer func() {
			cancel()
			_ = w.Flush()
		}()

		// Send initial connection message
		initMessage := map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  "connection/opened",
		}
		if initJSON, err := sonic.Marshal(initMessage); err == nil {
			fmt.Fprintf(w, "data: %s\n\n", initJSON)
			w.Flush()
		}

		// Wait for context cancellation (client disconnect or server-side cancel)
		<-(*bifrostCtx).Done()
	})
}

// Sync methods for MCP servers

func (h *MCPServerHandler) SyncAllMCPServers(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	availableTools := h.toolManager.GetAvailableMCPTools(ctx)
	h.syncServer(h.globalMCPServer, availableTools, nil)
	logger.Debug("Synced global MCP server with %d tools", len(availableTools))

	// initialize vkMCPServers map
	if h.config.ConfigStore != nil {
		virtualKeys, err := h.config.ConfigStore.GetVirtualKeys(ctx)
		if err != nil {
			return fmt.Errorf("failed to get virtual keys: %w", err)
		}
		h.vkMCPServers = make(map[string]*server.MCPServer)
		for i := range virtualKeys {
			vk := &virtualKeys[i]
			h.vkMCPServers[vk.Value] = server.NewMCPServer(
				vk.Name,
				version,
				server.WithToolCapabilities(true),
			)
			availableTools, toolFilter := h.fetchToolsForVK(vk)
			h.syncServer(h.vkMCPServers[vk.Value], availableTools, toolFilter)
			logger.Debug("Synced MCP server for virtual key '%s' with %d tools", vk.Name, len(availableTools))
		}
	}
	return nil
}

func (h *MCPServerHandler) SyncVKMCPServer(vk *tables.TableVirtualKey) {
	h.mu.Lock()
	defer h.mu.Unlock()
	vkServer, ok := h.vkMCPServers[vk.Value]
	if !ok {
		// Add new server
		vkServer = server.NewMCPServer(
			vk.Name,
			version,
			server.WithToolCapabilities(true),
		)
		h.vkMCPServers[vk.Value] = vkServer
	}
	availableTools, toolFilter := h.fetchToolsForVK(vk)
	h.syncServer(vkServer, availableTools, toolFilter)
	h.vkMCPServers[vk.Value] = vkServer
	logger.Debug("Synced MCP server for virtual key '%s' with %d tools", vk.Name, len(availableTools))
}

func (h *MCPServerHandler) DeleteVKMCPServer(vkValue string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.vkMCPServers, vkValue)
}

func (h *MCPServerHandler) syncServer(server *server.MCPServer, availableTools []schemas.ChatTool, toolFilter []string) {
	// Clear existing tools
	toolMap := server.ListTools()
	for toolName, _ := range toolMap {
		server.DeleteTools(toolName)
	}

	// Register tools from all connected clients
	for _, tool := range availableTools {
		// Only process function tools (skip custom tools)
		if tool.Function == nil {
			continue
		}

		// Capture tool name for closure
		toolName := tool.Function.Name

		handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// Inject tool filter into execution context if present
			if toolFilter != nil {
				ctx = context.WithValue(ctx, schemas.BifrostContextKey("mcp-include-tools"), toolFilter)
			}
			// Convert to Bifrost tool call format
			toolCallType := "function"
			toolCallID := fmt.Sprintf("mcp-%s", toolName)
			argsJSON, jsonErr := sonic.Marshal(request.GetArguments())
			if jsonErr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal tool arguments: %v", jsonErr)), nil
			}
			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   &toolCallID,
				Type: &toolCallType,
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      &toolName,
					Arguments: string(argsJSON),
				},
			}

			// Execute the tool via tool executor
			toolMessage, err := h.toolManager.ExecuteChatMCPTool(ctx, &toolCall)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("Tool execution failed: %v", bifrost.GetErrorMessage(err))), nil
			}

			// Extract content from tool message
			var resultText string
			if toolMessage != nil && toolMessage.Content != nil {
				// Handle ContentStr (string content)
				if toolMessage.Content.ContentStr != nil {
					resultText = *toolMessage.Content.ContentStr
				} else if toolMessage.Content.ContentBlocks != nil {
					// Handle ContentBlocks (structured content)
					for _, block := range toolMessage.Content.ContentBlocks {
						if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil {
							resultText += *block.Text
						}
					}
				}
			}

			// Return result using mcp-go helper
			return mcp.NewToolResultText(resultText), nil
		}

		// Convert description from *string to string
		description := ""
		if tool.Function.Description != nil {
			description = *tool.Function.Description
		}

		// Convert Parameters to mcp.ToolInputSchema
		var inputSchema mcp.ToolInputSchema
		if tool.Function.Parameters != nil {
			inputSchema.Type = tool.Function.Parameters.Type
			if tool.Function.Parameters.Properties != nil {
				// Convert *map[string]interface{} to map[string]any
				props := make(map[string]any)
				tool.Function.Parameters.Properties.Range(func(key string, value interface{}) bool {
					props[key] = value
					return true
				})
				inputSchema.Properties = props
			}
			if tool.Function.Parameters.Required != nil {
				inputSchema.Required = tool.Function.Parameters.Required
			}
		} else {
			// Default to empty object schema if no parameters
			inputSchema.Type = "object"
			inputSchema.Properties = make(map[string]any)
		}

		// Register tool with the server
		server.AddTool(mcp.Tool{
			Name:        toolName,
			Description: description,
			InputSchema: inputSchema,
		}, handler)
	}
}

// fetchToolsForVK fetches the tools for a given virtual key value.
// vkValue is the virtual key value for the server, if empty, all tools will be fetched for global mcp server.
// Returns the list of available tools and the tool filter to be applied during execution.
func (h *MCPServerHandler) fetchToolsForVK(vk *tables.TableVirtualKey) ([]schemas.ChatTool, []string) {
	ctx := context.Background()
	var toolFilter []string

	if len(vk.MCPConfigs) > 0 {
		executeOnlyTools := make([]string, 0)
		for _, vkMcpConfig := range vk.MCPConfigs {
			if len(vkMcpConfig.ToolsToExecute) == 0 {
				// No tools specified in virtual key config - skip this client entirely
				continue
			}

			// Handle wildcard in virtual key config - allow all tools from this client
			if slices.Contains(vkMcpConfig.ToolsToExecute, "*") {
				// Virtual key uses wildcard - use client-specific wildcard
				executeOnlyTools = append(executeOnlyTools, fmt.Sprintf("%s-*", vkMcpConfig.MCPClient.Name))
				continue
			}

			for _, tool := range vkMcpConfig.ToolsToExecute {
				if tool != "" {
					// Add the tool - client config filtering will be handled by mcp.go
					// Note: Use '-' separator for individual tools (wildcard uses '-*' after client name, e.g., "client-*")
					executeOnlyTools = append(executeOnlyTools, fmt.Sprintf("%s-%s", vkMcpConfig.MCPClient.Name, tool))
				}
			}
		}

		// Set even when empty to exclude tools when no tools are present in the virtual key config
		ctx = context.WithValue(ctx, schemas.BifrostContextKey("mcp-include-tools"), executeOnlyTools)
		toolFilter = executeOnlyTools
	}

	return h.toolManager.GetAvailableMCPTools(ctx), toolFilter
}

// Utility methods

func (h *MCPServerHandler) getMCPServerForRequest(ctx *fasthttp.RequestCtx) (*server.MCPServer, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	h.config.Mu.RLock()
	enforceVK := h.config.ClientConfig.EnforceAuthOnInference
	h.config.Mu.RUnlock()

	vk := getVKFromRequest(ctx)

	// Return global MCP server if not enforcing virtual key header and no virtual key is provided
	if !enforceVK && vk == "" {
		return h.globalMCPServer, nil
	}

	// Check if virtual key is provided
	if vk == "" {
		return nil, fmt.Errorf("virtual key header is required to access MCP server.")
	}

	// Check if vk exists in the map
	vkServer, ok := h.vkMCPServers[vk]
	if !ok {
		return nil, fmt.Errorf("virtual key not found.")
	}

	return vkServer, nil
}

func getVKFromRequest(ctx *fasthttp.RequestCtx) string {
	if value := strings.TrimSpace(string(ctx.Request.Header.Peek(string(schemas.BifrostContextKeyVirtualKey)))); value != "" {
		return value
	}

	authHeader := strings.TrimSpace(string(ctx.Request.Header.Peek("Authorization")))
	if authHeader != "" {
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			token := strings.TrimSpace(authHeader[7:])
			if token != "" && strings.HasPrefix(strings.ToLower(token), governance.VirtualKeyPrefix) {
				return token
			}
		}
	}

	if apiKey := strings.TrimSpace(string(ctx.Request.Header.Peek("x-api-key"))); apiKey != "" {
		if strings.HasPrefix(strings.ToLower(apiKey), governance.VirtualKeyPrefix) {
			return apiKey
		}
	}

	return ""
}
