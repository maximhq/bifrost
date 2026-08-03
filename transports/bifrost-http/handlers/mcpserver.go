// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains MCP (Model Context Protocol) server implementation for HTTP streaming.
package handlers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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

// sseHeartbeatInterval is the cadence of SSE comment pings on the MCP SSE
// stream. It must stay below typical proxy/load-balancer idle timeouts (60s on
// most stacks) so connections aren't reaped, while being large enough to avoid
// gratuitous wake-ups on idle clients.
const sseHeartbeatInterval = 15 * time.Second

// MCPToolExecutor interface defines the method needed for executing MCP tools
type MCPToolManager interface {
	GetAvailableMCPTools(ctx context.Context) []schemas.ChatTool
	ExecuteChatMCPTool(ctx context.Context, toolCall *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError)
	ExecuteResponsesMCPTool(ctx context.Context, toolCall *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError)
}

// VirtualKeyCache resolves a virtual key by its row ID from an in-memory cache,
// letting the JWT auth path avoid a per-request database read. Satisfied by the
// governance plugin's in-memory store. Optional: when nil (or a cache miss), the
// handler falls back to the config store.
type VirtualKeyCache interface {
	GetVirtualKeyByID(ctx context.Context, vkID string) (*tables.TableVirtualKey, bool)
}

// MCPServerHandler manages HTTP requests for MCP server operations
// It implements the MCP protocol over HTTP streaming (SSE) for MCP clients
type MCPServerHandler struct {
	toolManager     MCPToolManager
	globalMCPServer *server.MCPServer
	vkMCPServers    map[string]*server.MCPServer // Map of vk value -> mcp server
	config          *lib.Config
	// identityResolver scopes a user-mode /mcp request to the user's own tools by
	// resolving a representative virtual key. Optional: when nil, user-mode
	// requests fall back to the global server.
	identityResolver OAuth2IdentityResolver
	// vkCache serves by-ID virtual key lookups on the JWT auth path from the
	// governance in-memory store, avoiding a per-request DB read. Optional: a nil
	// cache or a miss falls back to the config store. See getVirtualKeyByID.
	vkCache VirtualKeyCache
	mu      sync.RWMutex
}

// getVirtualKeyByID resolves a virtual key by its row ID for the JWT auth path,
// preferring the governance in-memory cache and falling back to the config store
// on a miss (e.g. a key created since the cache last refreshed) or when no cache
// is wired. The active-state check is left to the caller, matching both sources
// (neither filters inactive keys by ID).
func (h *MCPServerHandler) getVirtualKeyByID(ctx context.Context, vkID string) (*tables.TableVirtualKey, error) {
	if h.vkCache != nil {
		if vk, ok := h.vkCache.GetVirtualKeyByID(ctx, vkID); ok && vk != nil {
			return vk, nil
		}
	}
	if h.config.ConfigStore == nil {
		return nil, fmt.Errorf("virtual key not found or inactive")
	}
	vk, err := h.config.ConfigStore.GetVirtualKey(ctx, vkID)
	if err != nil || vk == nil {
		return nil, fmt.Errorf("virtual key not found or inactive")
	}
	return vk, nil
}

// NewMCPServerHandler creates a new MCP server handler instance
func NewMCPServerHandler(ctx context.Context, config *lib.Config, toolManager MCPToolManager, identityResolver OAuth2IdentityResolver, vkCache VirtualKeyCache) (*MCPServerHandler, error) {
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
		toolManager:      toolManager,
		globalMCPServer:  globalMCPServer,
		config:           config,
		vkMCPServers:     make(map[string]*server.MCPServer),
		identityResolver: identityResolver,
		vkCache:          vkCache,
	}

	// Register per-request tool filter so x-bf-mcp-include-clients and x-bf-mcp-include-tools are respected on tools/list
	server.WithToolFilter(handler.makeIncludeClientsFilter())(handler.globalMCPServer)

	// Register per-request tool filter so x-bf-mcp-include-clients and x-bf-mcp-include-tools are respected on tools/list
	server.WithToolFilter(handler.makeIncludeClientsFilter())(handler.globalMCPServer)

	if err := handler.SyncAllMCPServers(ctx); err != nil {
		return nil, fmt.Errorf("failed to sync all MCP servers: %w", err)
	}

	// Warm the signing-key cache when OAuth discovery is enabled: this creates the
	// key if absent and populates the cache, so the first JWKS/issuance/verify
	// request need not pay the load. This is the single startup warm path for both
	// OSS and enterprise. Best-effort — the verify path lazily loads it on a miss —
	// but a failure is logged since a persistent one means OAuth cannot work.
	if config.ClientConfig.IsMCPOAuthDiscoveryEnabled() {
		if _, err := handler.config.GetOAuth2SigningKey(ctx); err != nil {
			logger.Warn("mcp: failed to warm oauth2 signing key: %v", err)
		}
	}

	return handler, nil
}

// RegisterRoutes registers the MCP server routes.
func (h *MCPServerHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// MCP server endpoint - supports both POST (JSON-RPC) and GET (SSE)
	r.POST("/mcp", lib.ChainMiddlewares(h.handleMCPServer, middlewares...))
	r.GET("/mcp", lib.ChainMiddlewares(h.handleMCPServerSSE, middlewares...))
}

func (h *MCPServerHandler) handleMCPServer(ctx *fasthttp.RequestCtx) {
	authResult, err := h.getMCPServerForRequest(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusUnauthorized, err.Error())
		return
	}

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	bifrostCtx.SetValue(schemas.BifrostContextKeyIsMCPGateway, true)
	defer cancel()

	// Inject JWT identity into BifrostContext so downstream resolvers
	// (per-user OAuth, governance, tool-group filtering) see the same context
	// keys as header-based auth paths.
	if authResult.jwtClaims != nil {
		if injErr := injectJWTContext(bifrostCtx, authResult.jwtClaims, authResult.jwtVK); injErr != nil {
			SendError(ctx, fasthttp.StatusUnauthorized, injErr.Error())
			return
		}
	}
	mcpServer := authResult.mcpServer

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
	authResult, err := h.getMCPServerForRequest(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusUnauthorized, err.Error())
		return
	}
	// Signal to transport-plugin and tracing middlewares that this is a streaming
	// response. Without this, fasthttpResponseToHTTPResponse calls ctx.Response.Body()
	// during post-hook processing, which materializes the SSE body stream and
	// deadlocks waiting for an EOF that only arrives after the goroutine exits.
	ctx.SetUserValue(schemas.BifrostContextKeyDeferTraceCompletion, true)

	// Pre-allocate atomic.Value slot for the transport post-hook completer.
	// TransportInterceptorMiddleware stores the completer into this slot after next(ctx)
	// returns. The goroutine reads from the closure-captured pointer, avoiding any ctx
	// access after the handler returns (fasthttp recycles RequestCtx).
	var completerSlot atomic.Value
	ctx.SetUserValue(schemas.BifrostContextKeyTransportPostHookCompleter, &completerSlot)

	// Get the trace completer function for use in the streaming callback.
	// Signature: func([]schemas.PluginLogEntry) — accepts transport plugin logs so it
	// never needs to read from ctx.UserValue (ctx may be recycled).
	traceCompleter, _ := ctx.UserValue(schemas.BifrostContextKeyTraceCompleter).(func([]schemas.PluginLogEntry))

	// Set SSE headers
	ctx.SetContentType("text/event-stream")
	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.Response.Header.Set("Connection", "keep-alive")
	ctx.Response.Header.Set("X-Accel-Buffering", "no")

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	bifrostCtx.SetValue(schemas.BifrostContextKeyIsMCPGateway, true)

	if authResult.jwtClaims != nil {
		if injErr := injectJWTContext(bifrostCtx, authResult.jwtClaims, authResult.jwtVK); injErr != nil {
			cancel()
			SendError(ctx, fasthttp.StatusUnauthorized, injErr.Error())
			return
		}
	}

	// Use SSEStreamReader to bypass fasthttp's internal pipe batching
	reader := lib.NewSSEStreamReader()
	ctx.Response.SetBodyStream(reader, -1)

	go func() {
		var transportLogs []schemas.PluginLogEntry
		completerRan := false
		// runCompleter invokes the transport post-hook completer at most once.
		// sendSSEOnError=true emits plugin errors as SSE "event: error" frames so the
		// client sees them; =false logs server-side only (defer fallback, after stream
		// termination). The MCP SSE handler has no happy-path completion point, so it
		// only ever invokes this from the defer with sendSSEOnError=false.
		runCompleter := func(sendSSEOnError bool) {
			if completerRan {
				return
			}
			// Bounded wait for TransportInterceptorMiddleware to publish the completer.
			// It calls slot.Store after next(ctx) returns, which races with this goroutine
			// on fast/empty streams. 100ms is ample — the store runs a few instructions
			// after the handler returns.
			var loaded any
			deadline := time.Now().Add(100 * time.Millisecond)
			for {
				if loaded = completerSlot.Load(); loaded != nil {
					break
				}
				if time.Now().After(deadline) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if loaded == nil {
				return
			}
			postHookCompleter, ok := loaded.(func() ([]schemas.PluginLogEntry, error))
			if !ok {
				return
			}
			completerRan = true
			logs, err := postHookCompleter()
			if err != nil {
				if sendSSEOnError {
					errorJSON, marshalErr := sonic.Marshal(map[string]string{"error": err.Error()})
					if marshalErr == nil {
						reader.SendError(errorJSON)
					}
				} else {
					logger.Warn("transport post-hook failed after stream terminated: %v", err)
				}
			}
			transportLogs = logs
		}

		defer func() {
			// Run the deferred transport post-hook completer before cancelling the
			// context so plugins see a live context. Errors are logged server-side
			// only — the stream is already closing.
			runCompleter(false)
			cancel()
			reader.Done()
			// Complete the trace after streaming finishes, passing transport plugin logs.
			// This ensures all spans are properly ended before the trace is sent to OTEL.
			if traceCompleter != nil {
				traceCompleter(transportLogs)
			}
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
			if !reader.Send(buf) {
				return
			}
		}

		// Periodic SSE comment heartbeats keep idle connections alive through
		// proxies and let us detect client disconnect via reader.Send() returning
		// false — fasthttp.RequestCtx never cancels bifrostCtx on its own.
		ticker := time.NewTicker(sseHeartbeatInterval)
		defer ticker.Stop()
		ping := []byte(": ping\n\n")
		for {
			select {
			case <-ticker.C:
				if !reader.Send(ping) {
					return
				}
			case <-(*bifrostCtx).Done():
				return
			}
		}
	}()
}

// Sync methods for MCP servers

func (h *MCPServerHandler) SyncAllMCPServers(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	availableTools := h.toolManager.GetAvailableMCPTools(ctx)
	h.syncServer(h.globalMCPServer, availableTools, nil)
	logger.Debug("Synced global MCP server with %d tools", len(availableTools))

	// Per-VK MCP servers are created lazily on first request (see
	// getMCPServerForRequest / ensureVKMCPServer) rather than eagerly here.
	// Building one server per virtual key previously scaled O(number of keys)
	// and stalled startup with large key counts (100k+). Resetting the map
	// invalidates any cached servers so they are rebuilt with the latest tool
	// configuration on next use.
	h.vkMCPServers = make(map[string]*server.MCPServer)
	return nil
}

func (h *MCPServerHandler) SyncVKMCPServer(vk *tables.TableVirtualKey) *server.MCPServer {
	h.mu.Lock()
	defer h.mu.Unlock()
	vkServer, ok := h.vkMCPServers[vk.Value.GetValue()]
	if !ok {
		// Add new server
		vkServer = server.NewMCPServer(
			vk.Name,
			version,
			server.WithToolCapabilities(true),
		)
		server.WithToolFilter(h.makeIncludeClientsFilter())(vkServer)
		h.vkMCPServers[vk.Value.GetValue()] = vkServer
	}
	availableTools, toolFilter := h.fetchToolsForVK(vk)
	h.syncServer(vkServer, availableTools, toolFilter)
	h.vkMCPServers[vk.Value.GetValue()] = vkServer
	logger.Debug("Synced MCP server for virtual key '%s' with %d tools", vk.Name, len(availableTools))
	return vkServer
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
			logger.Info("[mcp-server] tool handler start tool=%q arg_count=%d", toolName, len(request.GetArguments()))
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
				logger.Info("[mcp-server] tool handler error tool=%q error=%s", toolName, bifrost.GetErrorMessage(err))
				if authReq := err.ExtraFields.MCPAuthRequired; authReq != nil {
					// Two surfaces share this error: per-user OAuth uses
					// AuthorizeURL (the upstream provider's authorize page);
					// per-user headers uses SubmitURL (the workspace landing
					// page where the user submits their header values).
					// Pick whichever Kind populated.
					url := authReq.AuthorizeURL
					action := "connect your account"
					if authReq.Kind == schemas.MCPAuthRequiredKindHeaders {
						url = authReq.SubmitURL
						action = "submit the required headers"
					}
					message := fmt.Sprintf(
						"Authentication required for %s. Open this URL to %s: %s",
						authReq.MCPClientName, action, url,
					)
					if schemas.MCPAuthURLHasTempTokenFragment(url) {
						message += schemas.MCPAuthTempTokenReminder
					}
					return mcp.NewToolResultError(message), nil
				}
				return mcp.NewToolResultError(fmt.Sprintf("Tool execution failed: %v", bifrost.GetErrorMessage(err))), nil
			}
			logger.Info("[mcp-server] tool handler success tool=%q", toolName)

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

		inputSchema := convertToolFunctionParametersToMCPInputSchema(tool.Function.Parameters)

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

func convertToolFunctionParametersToMCPInputSchema(params *schemas.ToolFunctionParameters) mcp.ToolInputSchema {
	if params == nil {
		return mcp.ToolInputSchema{
			Type:       "object",
			Properties: make(map[string]any),
		}
	}

	inputSchema := mcp.ToolInputSchema{
		Type:     params.Type,
		Required: params.Required,
	}

	if params.Properties != nil {
		props := make(map[string]any, params.Properties.Len())
		params.Properties.Range(func(key string, value interface{}) bool {
			props[key] = value
			return true
		})
		inputSchema.Properties = props
	}

	if params.Defs != nil {
		defs := make(map[string]any, params.Defs.Len())
		params.Defs.Range(func(key string, value interface{}) bool {
			defs[key] = value
			return true
		})
		inputSchema.Defs = defs
	} else if params.Definitions != nil {
		defs := make(map[string]any, params.Definitions.Len())
		params.Definitions.Range(func(key string, value interface{}) bool {
			defs[key] = value
			return true
		})
		inputSchema.Defs = defs
	}

	return inputSchema
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

// mcpAuthResult carries the outcome of /mcp request authentication.
type mcpAuthResult struct {
	mcpServer *server.MCPServer
	jwtClaims *jwtMCPClaims           // non-nil when authenticated via JWT
	jwtVK     *tables.TableVirtualKey // non-nil when jwt bf_mode=vk
}

// getMCPServerForRequest authenticates the /mcp request and returns the
// appropriate scoped MCP server alongside any JWT claims that must be injected
// into the BifrostContext after it is created.
//
// Authentication priority:
//  1. JWT Bearer token (when MCPServerAuthMode is both or oauth)
//  2. VK / header credentials (when MCPServerAuthMode is headers or both)
//  3. Anonymous access (when EnforceAuthOnInference is false)
//
// When MCPServerAuthMode is oauth (strict), header credentials are rejected.
func (h *MCPServerHandler) getMCPServerForRequest(ctx *fasthttp.RequestCtx) (*mcpAuthResult, error) {
	h.config.Mu.RLock()
	enforceAuth := h.config.ClientConfig.EnforceAuthOnInference
	authMode := h.config.ClientConfig.MCPServerAuthMode
	h.config.Mu.RUnlock()

	discoveryEnabled := authMode == tables.MCPServerAuthModeBoth || authMode == tables.MCPServerAuthModeOAuth

	// --- Pre-authenticated user path ---
	// An upstream auth layer that authenticated the caller as a user stamps the
	// user id onto the request context. In headers/both modes, scope the request
	// to that user's virtual key — the same representative-VK scoping a user-mode
	// token gets — so a user authenticated by a bearer token is treated like a
	// virtual key. oauth-strict accepts only Bifrost-issued tokens and is excluded.
	if h.identityResolver != nil &&
		(authMode == tables.MCPServerAuthModeHeaders || authMode == tables.MCPServerAuthModeBoth) {
		if userID, _ := ctx.UserValue(schemas.BifrostContextKeyUserID).(string); userID != "" {
			// Dual-credential conflict (IDP token + VK) is handled upstream in the SCIM
			// InferenceMiddleware before identity is stamped, respecting the operator's
			// dual_credential_conflict_behavior config. No check needed here.
			vkID, err := h.identityResolver.ResolveUserVirtualKey(ctx, userID)
			if err != nil {
				return nil, err
			}
			if vkID == "" {
				return nil, fmt.Errorf("no MCP access grant for the authenticated user")
			}
			vk, err := h.getVirtualKeyByID(ctx, vkID)
			if err != nil {
				return nil, err
			}
			if !vk.IsActiveValue() {
				return nil, fmt.Errorf("virtual key is inactive")
			}
			vkServer, err := h.ensureVKMCPServerByValue(ctx, vk.Value.GetValue())
			if err != nil {
				return nil, err
			}
			return &mcpAuthResult{mcpServer: vkServer}, nil
		}
	}

	// --- JWT path ---
	if rawJWT := extractBearerJWT(ctx); rawJWT != "" && discoveryEnabled {
		// An OAuth token is the sole identity for the request. Reject when a
		// header-based virtual key (x-bf-vk / x-api-key / x-goog-api-key / Bearer vk) is also
		// presented: mixing credential sources is ambiguous, and for user- and
		// session-mode tokens — which carry no virtual key — a stray header VK
		// would otherwise leak onto the context and be attributed to the request.
		if headerVK := getVKFromRequest(ctx); headerVK != "" {
			ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
			return nil, fmt.Errorf("conflicting credentials: an OAuth token and a virtual key header were both provided; send only the OAuth token")
		}

		// Load the signing key (cached for the process lifetime). A failure here is
		// an infrastructure fault — the config store or key is unavailable — not a
		// bad token. Log the detail for operators and return a clean message so it
		// is never mislabeled as the client's token being invalid.
		signingKey, err := h.config.GetOAuth2SigningKey(ctx)
		if err != nil {
			logger.Error("mcp: failed to load oauth2 signing key for jwt verification: %v", err)
			ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
			return nil, fmt.Errorf("signing key unavailable")
		}
		claims, err := verifyMCPJWT(ctx, rawJWT, h.config, signingKey)
		if err != nil {
			if discoveryEnabled {
				ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
			}
			// Forward verifyMCPJWT's error verbatim: it already labels a genuine
			// token failure ("invalid token: ...") precisely, while its config
			// faults ("signing key unavailable", ...) must not be mislabeled as
			// the client's token being bad.
			return nil, err
		}

		// For user-mode JWTs, if a dashboard session is present on the request
		// (BifrostContextKeyUserID, set by the auth middleware) it must match
		// bf_sub — a mismatch means the session and the token disagree on
		// identity. Its absence is not fatal: the JWT itself proves identity, and
		// initiating a new upstream per-user flow is verified later at the
		// session-bearing UI step (flowStart → canAccessUserFlow).
		if schemas.MCPAuthMode(claims.BfMode) == schemas.MCPAuthModeUser {
			sessionUserID, _ := ctx.UserValue(schemas.BifrostContextKeyUserID).(string)
			if sessionUserID != "" && sessionUserID != claims.Subject {
				ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
				return nil, fmt.Errorf("session user does not match the authenticated token")
			}
		}

		// Session-mode tokens carry no verified identity. When the operator
		// requires authentication (EnforceAuthOnInference=true), session-mode
		// JWT requests are rejected — the session itself is not deleted, but
		// this endpoint becomes inaccessible until the client re-authenticates
		// with a VK or user-mode token.
		if schemas.MCPAuthMode(claims.BfMode) == schemas.MCPAuthModeSession && enforceAuth {
			ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
			return nil, fmt.Errorf("authentication required; session-mode tokens are not accepted when authentication is enforced - re-authenticate with a virtual key or user identity")
		}

		res := &mcpAuthResult{jwtClaims: claims}

		// For vk mode, look up the VK by ID to get the scoped server and value.
		if schemas.MCPAuthMode(claims.BfMode) == schemas.MCPAuthModeVK {
			// Live virtual-key identity cutoff: when virtual-key identity has been
			// disabled, reject vk-mode tokens at request time rather than waiting
			// for the access token to expire and its refresh to be denied. The
			// DisableVKIdentity flag is read first so the common (flag-off) path
			// stays a single lock-guarded bool — IsUserModeAvailable is consulted
			// only when the flag is set. Gated identically to the refresh cutoff and
			// the consent flow's availableModes so it can never fire where vk is
			// still an offered authentication path.
			if oauth2ServerCfg(h.config).DisableVKIdentity &&
				h.identityResolver != nil && h.identityResolver.IsUserModeAvailable() {
				ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
				return nil, fmt.Errorf("virtual-key identity is no longer accepted; re-authenticate")
			}
			vk, err := h.getVirtualKeyByID(ctx, claims.Subject)
			if err != nil {
				return nil, err
			}
			if !vk.IsActiveValue() {
				return nil, fmt.Errorf("virtual key is inactive")
			}
			res.jwtVK = vk
			vkServer, serverErr := h.ensureVKMCPServerByValue(ctx, vk.Value.GetValue())
			if serverErr != nil {
				return nil, serverErr
			}
			res.mcpServer = vkServer
		} else if scopedServer, scopedErr := h.userScopedServer(ctx, claims); scopedErr != nil {
			return nil, scopedErr
		} else if scopedServer != nil {
			res.mcpServer = scopedServer
		} else {
			res.mcpServer = h.globalMCPServer
		}
		return res, nil
	}

	// --- oauth strict mode: reject non-JWT requests ---
	if authMode == tables.MCPServerAuthModeOAuth {
		ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
		return nil, fmt.Errorf("this server requires OAuth JWT authentication; header credentials are not accepted in oauth mode")
	}

	// --- VK / header credential path ---
	vk := getVKFromRequest(ctx)

	// EnforceAuth=false: anonymous access to the global (un-scoped) MCP server
	// is allowed in dev mode. EnforceAuth=true: VK header is mandatory.
	if !enforceAuth && vk == "" {
		// Anonymous access allowed in dev mode.
		return &mcpAuthResult{mcpServer: h.globalMCPServer}, nil
	}

	if vk == "" {
		if discoveryEnabled {
			ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
		}
		return nil, fmt.Errorf("virtual key required to access mcp server; set one of x-bf-vk, Authorization: Bearer <vk>, x-api-key, or x-goog-api-key in your MCP client config")
	}

	vkServer, err := h.ensureVKMCPServerByValue(ctx, vk)
	if err != nil {
		return nil, err
	}
	return &mcpAuthResult{mcpServer: vkServer}, nil
}

// userScopedServer returns a per-VK MCP server scoped to a user-mode token's
// own tools, or nil (with nil error) when no scoping applies — no resolver, a
// non-user-mode token, or a user with no virtual key — so the caller falls back
// to the global server.
//
// User-mode tokens carry a user identity but no virtual key of their own. The
// resolver maps the user to a representative virtual key (any one of the user's
// equivalent keys), letting this reuse the per-VK scoped server instead of
// serving the global (unscoped) one. Session-mode tokens have no identity to
// scope by and return nil here.
func (h *MCPServerHandler) userScopedServer(ctx *fasthttp.RequestCtx, claims *jwtMCPClaims) (*server.MCPServer, error) {
	if h.identityResolver == nil || h.config.ConfigStore == nil {
		return nil, nil
	}
	if schemas.MCPAuthMode(claims.BfMode) != schemas.MCPAuthModeUser {
		return nil, nil
	}
	// Reject deleted or deactivated users at request time, mirroring the vk-mode
	// IsActiveValue() cutoff, rather than letting an already-issued access token
	// keep working until it expires. Placed before the no-virtual-key early
	// return below so a removed user cannot fall through to the global server.
	if active, err := h.identityResolver.IsUserActive(ctx, claims.Subject); err != nil {
		return nil, fmt.Errorf("failed to verify user: %w", err)
	} else if !active {
		return nil, fmt.Errorf("user is no longer active")
	}
	vkID, err := h.identityResolver.ResolveUserVirtualKey(ctx, claims.Subject)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve virtual key for user: %w", err)
	}
	if vkID == "" {
		return nil, nil
	}
	// Mirror the vk-mode branch: resolve the representative VK by ID to get its
	// value and active state, then reuse the shared per-VK server cache.
	vk, err := h.getVirtualKeyByID(ctx, vkID)
	if err != nil {
		return nil, err
	}
	if !vk.IsActiveValue() {
		return nil, fmt.Errorf("virtual key is inactive")
	}
	return h.ensureVKMCPServerByValue(ctx, vk.Value.GetValue())
}

// ensureVKMCPServerByValue returns the per-VK server from cache or creates it.
func (h *MCPServerHandler) ensureVKMCPServerByValue(ctx context.Context, vkValue string) (*server.MCPServer, error) {
	h.mu.RLock()
	s, ok := h.vkMCPServers[vkValue]
	h.mu.RUnlock()
	// Fast path: a per-VK server already exists in the cache.
	if ok {
		return s, nil
	}
	// Slow path: build the per-VK server lazily on first use.
	return h.ensureVKMCPServer(ctx, vkValue)
}

// ensureVKMCPServer lazily builds and caches the MCP server for a virtual key on
// first use, looking the key up by value via the config store. Per-VK servers
// are no longer created eagerly at startup, so the first MCP request for a given
// key materializes it here. Returns "virtual key not found" if the value does
// not resolve to a known virtual key (or no config store is configured).
func (h *MCPServerHandler) ensureVKMCPServer(ctx context.Context, vkValue string) (*server.MCPServer, error) {
	if h.config.ConfigStore == nil {
		return nil, fmt.Errorf("virtual key not found")
	}
	vk, err := h.config.ConfigStore.GetVirtualKeyByValue(ctx, vkValue)
	if err != nil || vk == nil {
		return nil, fmt.Errorf("virtual key not found")
	}
	// GetVirtualKeyByValue does not filter inactive keys, so fail closed here:
	// a deactivated key must not yield (or cache) a usable MCP server. This is
	// the single chokepoint for both the header path and the JWT vk path.
	if !vk.IsActiveValue() {
		return nil, fmt.Errorf("virtual key is inactive")
	}
	// SyncVKMCPServer creates (or refreshes) and caches the server under the
	// handler write lock, returning the live server so a concurrent
	// SyncAllMCPServers cannot wipe the map out from under us before we read it.
	return h.SyncVKMCPServer(vk), nil
}

// getVKFromRequest extracts a virtual key from the request headers, checking
// each supported header in priority order and returning the first match:
//  1. x-bf-vk        — taken verbatim (no prefix check)
//  2. Authorization  — "Bearer <vk>", where <vk> must start with the VK prefix
//  3. x-api-key      — must start with the VK prefix
//  4. x-goog-api-key — must start with the VK prefix
//
// The prefix gate (governance.VirtualKeyPrefix) on the latter three lets real
// provider credentials pass through untouched, so only Bifrost virtual keys are
// picked up here. This header set mirrors the inference path, keeping MCP and
// inference at parity. Returns "" when no header carries a virtual key.
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

	if googAPIKey := strings.TrimSpace(string(ctx.Request.Header.Peek("x-goog-api-key"))); googAPIKey != "" {
		if strings.HasPrefix(strings.ToLower(googAPIKey), governance.VirtualKeyPrefix) {
			return googAPIKey
		}
	}

	return ""
}
