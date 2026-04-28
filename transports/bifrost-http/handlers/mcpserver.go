// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains MCP (Model Context Protocol) server implementation for HTTP streaming.
package handlers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

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

	// Register per-request tool filter so x-bf-mcp-include-clients and x-bf-mcp-include-tools are respected on tools/list
	server.WithToolFilter(handler.makeIncludeClientsFilter())(handler.globalMCPServer)

	// Register per-request tool filter so x-bf-mcp-include-clients and x-bf-mcp-include-tools are respected on tools/list
	server.WithToolFilter(handler.makeIncludeClientsFilter())(handler.globalMCPServer)

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
// injectMCPSessionIdentity sets the MCP gateway flag and, if a per-user OAuth
// session exists, injects the session token and identity (VK / User ID) directly
// into the BifrostContext. This avoids header-based identity propagation which
// would be vulnerable to spoofing by upstream callers.
//
// Governance context keys are set here intentionally (bypassing governance plugin)
// because in the MCP gateway path, identity is pre-authenticated via the OAuth session.
func injectMCPSessionIdentity(bifrostCtx *schemas.BifrostContext, session *tables.TablePerUserOAuthSession) {
	bifrostCtx.SetValue(schemas.BifrostContextKeyIsMCPGateway, true)
	if session != nil {
		if session.AccessToken != "" {
			bifrostCtx.SetValue(schemas.BifrostContextKeyMCPUserSession, session.AccessToken)
		}
		if session.VirtualKeyID != nil && *session.VirtualKeyID != "" && session.VirtualKey != nil && session.VirtualKey.Value != "" {
			bifrostCtx.SetValue(schemas.BifrostContextKeyVirtualKey, session.VirtualKey.Value)
		}
		if session.UserID != nil && *session.UserID != "" {
			bifrostCtx.SetValue(schemas.BifrostContextKeyUserID, *session.UserID)
		}
	}
}

func (h *MCPServerHandler) handleMCPServer(ctx *fasthttp.RequestCtx) {
	mcpServer, session, err := h.getMCPServerForRequest(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusUnauthorized, err.Error())
		return
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()

	injectMCPSessionIdentity(bifrostCtx, session)

	// Use mcp-go server to handle the request
	// HandleMessage processes JSON-RPC messages and returns appropriate responses
	response := mcpServer.HandleMessage(bifrostCtx, ctx.PostBody())

	// Check if response is nil (notification - no response needed)
	if response == nil {
		ctx.SetStatusCode(fasthttp.StatusAccepted)
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
	_, session, err := h.getMCPServerForRequest(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusUnauthorized, err.Error())
		return
	}

	// Set SSE headers
	ctx.SetContentType("text/event-stream")
	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.Response.Header.Set("Connection", "keep-alive")

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)

	injectMCPSessionIdentity(bifrostCtx, session)

	// Use SSEStreamReader to bypass fasthttp's internal pipe batching
	reader := lib.NewSSEStreamReader()
	ctx.Response.SetBodyStream(reader, -1)

	go func() {
		defer func() {
			cancel()
			reader.Done()
		}()

		// Send initial connection message
		initMessage := map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  "connection/opened",
		}
		if initJSON, err := sonic.Marshal(initMessage); err == nil {
			buf := make([]byte, 0, len(initJSON)+8)
			buf = append(buf, "data: "...)
			buf = append(buf, initJSON...)
			buf = append(buf, '\n', '\n')
			reader.Send(buf)
		}

		// Wait for context cancellation (client disconnect or server-side cancel)
		<-(*bifrostCtx).Done()
	}()
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
			vkServer := server.NewMCPServer(
				vk.Name,
				version,
				server.WithToolCapabilities(true),
			)
			server.WithToolFilter(h.makeIncludeClientsFilter())(vkServer)
			h.vkMCPServers[vk.Value] = vkServer
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
		server.WithToolFilter(h.makeIncludeClientsFilter())(vkServer)
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
				ctx = context.WithValue(ctx, schemas.MCPContextKeyIncludeTools, toolFilter)
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
				if err.ExtraFields.MCPAuthRequired != nil {
					return mcp.NewToolResultError(fmt.Sprintf(
						"Authentication required for %s. Open this URL to connect your account: %s",
						err.ExtraFields.MCPAuthRequired.MCPClientName, err.ExtraFields.MCPAuthRequired.AuthorizeURL,
					)), nil
				}
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

		// Map Bifrost annotations back to MCP tool annotations
		var toolAnnotation mcp.ToolAnnotation
		if tool.Annotations != nil {
			toolAnnotation = mcp.ToolAnnotation{
				Title:           tool.Annotations.Title,
				ReadOnlyHint:    tool.Annotations.ReadOnlyHint,
				DestructiveHint: tool.Annotations.DestructiveHint,
				IdempotentHint:  tool.Annotations.IdempotentHint,
				OpenWorldHint:   tool.Annotations.OpenWorldHint,
			}
		}

		// Register tool with the server
		server.AddTool(mcp.Tool{
			Name:        toolName,
			Description: description,
			InputSchema: inputSchema,
			Annotations: toolAnnotation,
		}, handler)
	}
}

// fetchToolsForVK fetches the tools for a given virtual key value.
// vkValue is the virtual key value for the server, if empty, all tools will be fetched for global mcp server.
// Returns the list of available tools and the tool filter to be applied during execution.
func (h *MCPServerHandler) fetchToolsForVK(vk *tables.TableVirtualKey) ([]schemas.ChatTool, []string) {
	ctx := context.Background()
	var toolFilter []string

	executeOnlyTools := make([]string, 0)

	// Build a lookup of AllowOnAllVirtualKeys clients: clientID -> clientName.
	// Explicit VK MCPConfigs always take precedence over AllowOnAllVirtualKeys.
	allowAllVKsClients := h.config.GetAllowOnAllVirtualKeysClients()
	if allowAllVKsClients == nil {
		allowAllVKsClients = make(map[string]string)
	}

	// Process explicit VK MCPConfigs first.
	handledClients := make(map[string]bool)
	for _, vkMcpConfig := range vk.MCPConfigs {
		clientID := vkMcpConfig.MCPClient.ClientID
		if _, isAllowAll := allowAllVKsClients[clientID]; isAllowAll {
			// Explicit config exists — it takes precedence; mark handled regardless of tool list.
			handledClients[clientID] = true
		}
		if vkMcpConfig.ToolsToExecute.IsEmpty() {
			continue
		}
		if vkMcpConfig.ToolsToExecute.IsUnrestricted() {
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

	// For AllowOnAllVirtualKeys clients with no explicit VK config, allow all their tools.
	for clientID, clientName := range allowAllVKsClients {
		if !handledClients[clientID] {
			executeOnlyTools = append(executeOnlyTools, fmt.Sprintf("%s-*", clientName))
		}
	}

	// Always set the include-tools filter (empty = deny-all when no MCPConfigs and no AllowOnAllVirtualKeys clients)
	ctx = context.WithValue(ctx, schemas.MCPContextKeyIncludeTools, executeOnlyTools)
	toolFilter = executeOnlyTools

	return h.toolManager.GetAvailableMCPTools(ctx), toolFilter
}

// makeIncludeClientsFilter returns a ToolFilterFunc that dynamically filters the tools/list
// response based on the x-bf-mcp-include-clients and x-bf-mcp-include-tools request headers.
// When neither header is present the filter is a no-op, preserving existing behaviour.
func (h *MCPServerHandler) makeIncludeClientsFilter() server.ToolFilterFunc {
	return func(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
		if ctx.Value(schemas.MCPContextKeyIncludeClients) == nil && ctx.Value(schemas.MCPContextKeyIncludeTools) == nil {
			return tools
		}
		allowed := h.toolManager.GetAvailableMCPTools(ctx)
		allowedNames := make(map[string]bool, len(allowed))
		for _, t := range allowed {
			if t.Function != nil {
				allowedNames[t.Function.Name] = true
			}
		}
		result := make([]mcp.Tool, 0, len(tools))
		for _, tool := range tools {
			if allowedNames[tool.Name] {
				result = append(result, tool)
			}
		}
		return result
	}
}

// Utility methods

func (h *MCPServerHandler) getMCPServerForRequest(ctx *fasthttp.RequestCtx) (*server.MCPServer, *tables.TablePerUserOAuthSession, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	h.config.Mu.RLock()
	enforceVK := h.config.ClientConfig.EnforceAuthOnInference
	h.config.Mu.RUnlock()

	vk := getVKFromRequest(ctx)

	// Check for Bifrost per-user OAuth Bearer token (not a VK)
	userOauthSession, sessionErr := h.getPerUserOAuthSession(ctx)
	if sessionErr != nil {
		return nil, nil, fmt.Errorf("failed to look up OAuth session: %w", sessionErr)
	}

	// If per_user_oauth MCP clients are configured and no valid auth, return 401 with discovery
	if clients := h.config.GetPerUserOAuthMCPClients(); len(clients) > 0 && userOauthSession == nil && vk == "" {
		resourceMetadataURL := lib.BuildBaseURL(ctx, h.config.GetMCPExternalBaseURL()) + "/.well-known/oauth-protected-resource"
		ctx.Response.Header.Set("WWW-Authenticate",
			fmt.Sprintf(`Bearer resource_metadata="%s"`, resourceMetadataURL))
		return nil, nil, fmt.Errorf("oauth authentication required for mcp access")
	}

	if userOauthSession != nil {
		if !enforceVK && (userOauthSession.VirtualKeyID == nil || *userOauthSession.VirtualKeyID == "") {
			return h.globalMCPServer, userOauthSession, nil
		}

		if userOauthSession.VirtualKeyID == nil || *userOauthSession.VirtualKeyID == "" || userOauthSession.VirtualKey == nil {
			return nil, nil, fmt.Errorf("virtual key required in oauth session to access mcp server, please re-authenticate with a virtual key")
		}

		vkServer, ok := h.vkMCPServers[userOauthSession.VirtualKey.Value]
		if !ok {
			return nil, nil, fmt.Errorf("virtual key not found")
		}

		return vkServer, userOauthSession, nil
	}

	// Return global MCP server if not enforcing virtual key header and no virtual key is provided
	if !enforceVK && vk == "" {
		return h.globalMCPServer, nil, nil
	}

	if vk == "" {
		return nil, nil, fmt.Errorf("virtual key header required to access mcp server")
	}

	vkServer, ok := h.vkMCPServers[vk]
	if !ok {
		return nil, nil, fmt.Errorf("virtual key not found")
	}

	return vkServer, nil, nil
}

// getPerUserOAuthSession extracts and validates a Bifrost-issued per-user OAuth
// token from the Authorization header. Returns the session if valid, nil otherwise.
func (h *MCPServerHandler) getPerUserOAuthSession(ctx *fasthttp.RequestCtx) (*tables.TablePerUserOAuthSession, error) {
	authHeader := strings.TrimSpace(string(ctx.Request.Header.Peek("Authorization")))
	if authHeader == "" || !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return nil, nil
	}
	token := strings.TrimSpace(authHeader[7:])
	if token == "" || strings.HasPrefix(strings.ToLower(token), governance.VirtualKeyPrefix) {
		return nil, nil // It's a virtual key, not a per-user OAuth token
	}

	if h.config.ConfigStore == nil {
		return nil, nil
	}

	session, err := h.config.ConfigStore.GetPerUserOAuthSessionByAccessToken(ctx, token)
	if err != nil {
		logger.Warn("[mcp/auth] GetPerUserOAuthSessionByAccessToken error: %v", err)
		return nil, err
	}
	if session == nil {
		logger.Debug("[mcp/auth] Session not found for token")
		return nil, nil
	}

	// Check expiry
	if session.ExpiresAt.Before(time.Now()) {
		logger.Debug("[mcp/auth] Session expired: session_id=%s expires_at=%v", session.ID, session.ExpiresAt)
		return nil, nil
	}

	return session, nil
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
