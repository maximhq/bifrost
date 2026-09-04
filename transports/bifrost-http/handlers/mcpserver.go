// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains MCP (Model Context Protocol) server implementation for HTTP streaming.
package handlers

import (
	"context"
	"fmt"
	"strings"
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

// mcpServerName is what the server calls itself in the initialize handshake. One server serves
// every caller, so it carries no caller's name.
const mcpServerName = "bifrost"

// MCPToolExecutor interface defines the method needed for executing MCP tools
type MCPToolManager interface {
	GetAvailableMCPTools(ctx context.Context) []schemas.ChatTool
	ExecuteChatMCPTool(ctx context.Context, toolCall *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError)
	ExecuteResponsesMCPTool(ctx context.Context, toolCall *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError)
}

// MCPGatewayAdmitter decides whether a /mcp request may be served, and what it may reach. It runs
// the request through the same governance funnel every tool execution passes, so a caller is told at
// initialize and tools/list exactly what tools/call would tell them, by the same rules.
type MCPGatewayAdmitter interface {
	// AdmitMCPGatewayRequest evaluates the request as an MCP tool execution with no tool named yet,
	// resolving and recording its access on ctx. It returns that access, and the refusal governance
	// gave when it gave one. Nil access with no refusal means nothing restricts the request (the
	// deployment has nothing to resolve access with, or the request presented nothing), and it is
	// served unrestricted, as every other path serves it.
	AdmitMCPGatewayRequest(ctx *schemas.BifrostContext) (schemas.Access, *schemas.BifrostError)
}

// virtualMCPGatewayResolver is the optional capability behind a Virtual MCP endpoint (/mcp/<slug>): it resolves
// the tools a request may see for the Virtual MCP the slug names, gated on that vMCP being assigned to
// the request's key. Satisfied by the same admitter that resolves gateway access. Absent (or nil)
// means there is no governance store to resolve assignment against, so /mcp/<slug> is refused.
type virtualMCPGatewayResolver interface {
	// VirtualMCPToolAccess returns the served tool include-list and whether the slug's Virtual MCP is
	// assigned to the request's key (assigned=false → 403). The tools are already narrowed to what the
	// request's access permits.
	VirtualMCPToolAccess(ctx *schemas.BifrostContext, slug string, access schemas.Access) (served []string, assigned bool)
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
	toolManager MCPToolManager
	// admitter answers what the request may reach. Optional: without one nothing resolves access,
	// and every request is served unrestricted.
	admitter MCPGatewayAdmitter
	config   *lib.Config
	// identityResolver answers the identity questions that are the transport's rather than
	// governance's: whether a user-mode caller still exists, and whether user mode is offered at
	// all. Optional.
	identityResolver OAuth2IdentityResolver
	// vkCache serves by-ID virtual key lookups on the JWT auth path from the
	// governance in-memory store, avoiding a per-request DB read. Optional: a nil
	// cache or a miss falls back to the config store. See getVirtualKeyByID.
	vkCache VirtualKeyCache
	// mcpServer is the one server every caller is served from. What each caller sees of it is
	// decided per request, from the tool list stamped on the request context, so nothing about
	// it is per caller. Replaced whole when the tool registry changes; a request already holding
	// the previous one finishes against it.
	mcpServer atomic.Pointer[server.MCPServer]
}

// getVirtualKeyByID resolves a virtual key by its row ID for the JWT auth path,
// preferring the governance in-memory cache and falling back to the config store
// on a miss (e.g. a key created since the cache last refreshed) or when no cache
// is wired. Whether the key may be used is not decided here: governance refuses a
// key that is inactive or expired, from the grant it resolves for it.
func (h *MCPServerHandler) getVirtualKeyByID(ctx context.Context, vkID string) (*tables.TableVirtualKey, error) {
	if h.vkCache != nil {
		if vk, ok := h.vkCache.GetVirtualKeyByID(ctx, vkID); ok && vk != nil {
			return vk, nil
		}
	}
	if h.config.ConfigStore == nil {
		return nil, fmt.Errorf("virtual key not found")
	}
	vk, err := h.config.ConfigStore.GetVirtualKey(ctx, vkID)
	if err != nil || vk == nil {
		return nil, fmt.Errorf("virtual key not found")
	}
	return vk, nil
}

// NewMCPServerHandler creates a new MCP server handler instance
func NewMCPServerHandler(ctx context.Context, config *lib.Config, toolManager MCPToolManager, admitter MCPGatewayAdmitter, identityResolver OAuth2IdentityResolver, vkCache VirtualKeyCache) (*MCPServerHandler, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if toolManager == nil {
		return nil, fmt.Errorf("tool manager is required")
	}

	handler := &MCPServerHandler{
		toolManager:      toolManager,
		admitter:         admitter,
		config:           config,
		identityResolver: identityResolver,
		vkCache:          vkCache,
	}

	if err := handler.SyncMCPServer(ctx); err != nil {
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
	// Virtual MCP endpoints: same handlers, narrowed to the slug's vMCP in admit.
	r.POST("/mcp/{slug}", lib.ChainMiddlewares(h.handleMCPServer, middlewares...))
	r.GET("/mcp/{slug}", lib.ChainMiddlewares(h.handleMCPServerSSE, middlewares...))
}

func (h *MCPServerHandler) handleMCPServer(ctx *fasthttp.RequestCtx) {
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	bifrostCtx.SetValue(schemas.BifrostContextKeyIsMCPGateway, true)

	if refusal := h.admit(ctx, bifrostCtx); refusal != nil {
		SendError(ctx, refusal.status, refusal.message)
		return
	}
	mcpServer := h.server()
	if mcpServer == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP server is not ready")
		return
	}

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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	bifrostCtx.SetValue(schemas.BifrostContextKeyIsMCPGateway, true)

	if refusal := h.admit(ctx, bifrostCtx); refusal != nil {
		cancel()
		SendError(ctx, refusal.status, refusal.message)
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
		// proxies and let us detect client disconnect via reader.SendHeartbeat()
		// returning false — fasthttp.RequestCtx never cancels bifrostCtx on its own.
		//
		// Use the shared frame, never a local one: a hand-rolled ": ping\n\n" carries the
		// trailing blank line #5883 removed (some decoders dispatch it as an empty event,
		// #5874) and bypasses the line-boundary gate #5905 added.
		ticker := time.NewTicker(sseHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if !reader.SendHeartbeat() {
					return
				}
			case <-(*bifrostCtx).Done():
				return
			}
		}
	}()
}

// SyncMCPServer rebuilds the server from the tools currently available and swaps it in. Called
// whenever the set of MCP clients or their tools changes. Every caller is served from this one
// server; what each of them sees is decided per request.
func (h *MCPServerHandler) SyncMCPServer(ctx context.Context) error {
	availableTools := h.toolManager.GetAvailableMCPTools(ctx)
	h.mcpServer.Store(h.buildServer(availableTools))
	logger.Debug("Synced MCP server with %d tools", len(availableTools))
	return nil
}

// server returns the server requests are currently served from, or nil before the first sync.
func (h *MCPServerHandler) server() *server.MCPServer {
	return h.mcpServer.Load()
}

// buildServer registers every available tool on a fresh server. A tool's handler reads nothing
// about the caller: what a request may see and call rides on its context, and both the tool filter
// and the executor read it from there.
func (h *MCPServerHandler) buildServer(availableTools []schemas.ChatTool) *server.MCPServer {
	mcpServer := server.NewMCPServer(
		mcpServerName,
		version,
		server.WithToolCapabilities(true),
	)
	// Per-request tool filter so tools/list answers with what this request may see.
	server.WithToolFilter(h.makeIncludeClientsFilter())(mcpServer)

	// Register tools from all connected clients
	for _, tool := range availableTools {
		// Only process function tools (skip custom tools)
		if tool.Function == nil {
			continue
		}

		// Capture tool name for closure
		toolName := tool.Function.Name

		handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			logger.Debug("[mcp-server] tool handler start tool=%q arg_count=%d", toolName, len(request.GetArguments()))
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
				logger.Debug("[mcp-server] tool handler error tool=%q error=%s", toolName, bifrost.GetErrorMessage(err))
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
			logger.Debug("[mcp-server] tool handler success tool=%q", toolName)

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
		mcpServer.AddTool(mcp.Tool{
			Name:        toolName,
			Description: description,
			InputSchema: inputSchema,
			Annotations: toolAnnotation,
		}, handler)
	}
	return mcpServer
}

// makeIncludeClientsFilter returns a ToolFilterFunc that narrows tools/list to what the request's
// context permits: the include lists the caller sent (x-bf-mcp-include-clients,
// x-bf-mcp-include-tools) and the tool list the request was admitted with, which admit stamps on the
// same key. When the context carries neither the filter is a no-op.
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

// Admission

// mcpRefusal is what a /mcp request is refused with: the status to answer and the message to say.
type mcpRefusal struct {
	status  int
	message string
}

// admit settles who is asking and what they may reach, in that order, before a single JSON-RPC
// message is handled. Authentication is the transport's: which credential the request presents,
// whether the configured mode accepts it, whether it verifies. What the credential grants is
// governance's, asked through the admitter so initialize and tools/list are refused by exactly the
// rules tools/call is refused by. What survives is stamped on bifrostCtx as the tool list this
// request may see and call.
func (h *MCPServerHandler) admit(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) *mcpRefusal {
	if err := h.authenticate(ctx, bifrostCtx); err != nil {
		return &mcpRefusal{status: fasthttp.StatusUnauthorized, message: err.Error()}
	}

	access, refused := h.admitAccess(bifrostCtx)
	if refused != nil {
		status := fasthttp.StatusForbidden
		if refused.StatusCode != nil {
			status = *refused.StatusCode
		}
		// A refusal for want of a usable credential is answered the way every other credential
		// failure is, with the pointer to where one can be obtained.
		if status == fasthttp.StatusUnauthorized && h.discoveryEnabled() {
			ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
		}
		return &mcpRefusal{status: status, message: bifrost.GetErrorMessage(refused)}
	}

	// A Virtual MCP endpoint (/mcp/<slug>) serves one Virtual MCP: gate on it being assigned to the key and
	// narrow the tools to it. The plain /mcp route carries no slug and serves the full access.
	if slug, _ := ctx.UserValue("slug").(string); slug != "" {
		return h.admitVirtualMCP(bifrostCtx, slug, access)
	}

	stampToolAccess(bifrostCtx, access)
	return nil
}

// admitVirtualMCP narrows an already-admitted request to the Virtual MCP its slug names. The vMCP must be
// assigned to the request's key, or the request is refused with 403; otherwise the tools it may see
// are stamped as the served include-list. Reached only from admit, after access is resolved.
func (h *MCPServerHandler) admitVirtualMCP(bifrostCtx *schemas.BifrostContext, slug string, access schemas.Access) *mcpRefusal {
	resolver, ok := h.admitter.(virtualMCPGatewayResolver)
	if !ok {
		return &mcpRefusal{status: fasthttp.StatusForbidden, message: "access_denied"}
	}
	served, assigned := resolver.VirtualMCPToolAccess(bifrostCtx, slug, access)
	if !assigned {
		return &mcpRefusal{status: fasthttp.StatusForbidden, message: "access_denied"}
	}
	bifrostCtx.SetValue(schemas.MCPContextKeyIncludeTools, served)
	return nil
}

// admitAccess asks governance what the request may reach. Without an admitter there is nothing to
// ask, and the answer is what a request nobody governs has always had: no restriction.
func (h *MCPServerHandler) admitAccess(bifrostCtx *schemas.BifrostContext) (schemas.Access, *schemas.BifrostError) {
	if h.admitter == nil {
		return nil, nil
	}
	return h.admitter.AdmitMCPGatewayRequest(bifrostCtx)
}

// stampToolAccess records the tools this request may see and call, as the include list the tool
// filter and the executor both read. A request with no access is one nothing restricts, and is left
// unstamped: it enumerates nothing, and an empty list would read as no tool at all. A list the caller
// supplied is narrowed to the access rather than replaced, so a caller can ask for less but never
// for more.
func stampToolAccess(bifrostCtx *schemas.BifrostContext, access schemas.Access) {
	if access == nil {
		return
	}
	tools := access.MCPToolIncludeList()
	if requested := bifrostCtx.Value(schemas.MCPContextKeyIncludeTools); requested != nil {
		requestedList, _ := requested.([]string)
		tools = access.NarrowMCPToolIncludeList(requestedList)
	}
	bifrostCtx.SetValue(schemas.MCPContextKeyIncludeTools, tools)
}

// authSettings reads the two settings admission depends on under the config lock.
func (h *MCPServerHandler) authSettings() (enforceAuth bool, authMode tables.MCPServerAuthMode) {
	h.config.Mu.RLock()
	defer h.config.Mu.RUnlock()
	return h.config.ClientConfig.EnforceAuthOnInference, h.config.ClientConfig.MCPServerAuthMode
}

// discoveryEnabled reports whether Bifrost-issued OAuth tokens are accepted on /mcp, which is also
// when a refusal points the caller at the authorization server.
func (h *MCPServerHandler) discoveryEnabled() bool {
	_, authMode := h.authSettings()
	return authMode == tables.MCPServerAuthModeBoth || authMode == tables.MCPServerAuthModeOAuth
}

// authenticate settles who is asking. It verifies whatever credential the request presents under
// the configured auth mode, refuses what the mode does not accept, and stamps the identity on
// bifrostCtx for governance to resolve. It decides nothing about what the caller may reach, and
// nothing about whether a credential is still usable: a key that is inactive or expired is refused
// by governance, from the grant it resolves for it, so both paths refuse it the same way.
//
// Authentication priority:
//  1. JWT Bearer token (when MCPServerAuthMode is both or oauth)
//  2. Header credentials, or an identity an upstream auth layer stamped (headers or both)
//  3. Anonymous access (when EnforceAuthOnInference is false)
//
// When MCPServerAuthMode is oauth (strict), header credentials are rejected.
func (h *MCPServerHandler) authenticate(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) error {
	enforceAuth, authMode := h.authSettings()
	discoveryEnabled := authMode == tables.MCPServerAuthModeBoth || authMode == tables.MCPServerAuthModeOAuth

	// --- JWT path ---
	if rawJWT := extractBearerJWT(ctx); rawJWT != "" && discoveryEnabled {
		// An OAuth token is the sole identity for the request. Reject when a
		// header-based virtual key (x-bf-vk / x-api-key / x-goog-api-key / Bearer vk) is also
		// presented: mixing credential sources is ambiguous, and for user- and
		// session-mode tokens — which carry no virtual key — a stray header VK
		// would otherwise leak onto the context and be attributed to the request.
		if headerVK := getVKFromRequest(ctx); headerVK != "" {
			ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
			return fmt.Errorf("conflicting credentials: an OAuth token and a virtual key header were both provided; send only the OAuth token")
		}

		// Load the signing key (cached for the process lifetime). A failure here is
		// an infrastructure fault — the config store or key is unavailable — not a
		// bad token. Log the detail for operators and return a clean message so it
		// is never mislabeled as the client's token being invalid.
		signingKey, err := h.config.GetOAuth2SigningKey(ctx)
		if err != nil {
			logger.Error("mcp: failed to load oauth2 signing key for jwt verification: %v", err)
			ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
			return fmt.Errorf("signing key unavailable")
		}
		claims, err := verifyMCPJWT(ctx, rawJWT, h.config, signingKey)
		if err != nil {
			ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
			// Forward verifyMCPJWT's error verbatim: it already labels a genuine
			// token failure ("invalid token: ...") precisely, while its config
			// faults ("signing key unavailable", ...) must not be mislabeled as
			// the client's token being bad.
			return err
		}

		switch schemas.MCPAuthMode(claims.BfMode) {
		case schemas.MCPAuthModeUser:
			// If a dashboard session is present on the request (BifrostContextKeyUserID,
			// set by the auth middleware) it must match bf_sub: a mismatch means the
			// session and the token disagree on identity. Its absence is not fatal: the
			// JWT itself proves identity, and initiating a new upstream per-user flow is
			// verified later at the session-bearing UI step (flowStart → canAccessUserFlow).
			sessionUserID, _ := ctx.UserValue(schemas.BifrostContextKeyUserID).(string)
			if sessionUserID != "" && sessionUserID != claims.Subject {
				ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
				return fmt.Errorf("session user does not match the authenticated token")
			}
			// Reject deleted or deactivated users at request time rather than letting an
			// already-issued access token keep working until it expires. Whether the user
			// still exists is an identity question; what they may reach is governance's.
			if h.identityResolver != nil {
				if active, err := h.identityResolver.IsUserActive(ctx, claims.Subject); err != nil {
					return fmt.Errorf("failed to verify user: %w", err)
				} else if !active {
					return fmt.Errorf("user is no longer active")
				}
			}
			return injectJWTContext(bifrostCtx, claims, nil)

		case schemas.MCPAuthModeSession:
			// Session-mode tokens carry no verified identity. When the operator
			// requires authentication (EnforceAuthOnInference=true), session-mode
			// JWT requests are rejected: the session itself is not deleted, but
			// this endpoint becomes inaccessible until the client re-authenticates
			// with a VK or user-mode token.
			if enforceAuth {
				ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
				return fmt.Errorf("authentication required; session-mode tokens are not accepted when authentication is enforced - re-authenticate with a virtual key or user identity")
			}
			return injectJWTContext(bifrostCtx, claims, nil)

		case schemas.MCPAuthModeVK:
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
				return fmt.Errorf("virtual-key identity is no longer accepted; re-authenticate")
			}
			// The token names the key by row ID; governance resolves keys by value, so the
			// row is read to stamp the value. Nothing else about the key is decided here.
			vk, err := h.getVirtualKeyByID(ctx, claims.Subject)
			if err != nil {
				return err
			}
			return injectJWTContext(bifrostCtx, claims, vk)

		default:
			return injectJWTContext(bifrostCtx, claims, nil)
		}
	}

	// --- oauth strict mode: reject non-JWT requests ---
	if authMode == tables.MCPServerAuthModeOAuth {
		ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
		return fmt.Errorf("this server requires OAuth JWT authentication; header credentials are not accepted in oauth mode")
	}

	// --- Header credentials, or an identity an upstream auth layer already authenticated ---
	// Converting the request already stamped a virtual key found in its headers, and copied the
	// user an upstream auth layer stamped. Governance resolves both; all that is decided here is
	// whether the request presented anything, when it must.
	//
	// EnforceAuth=false: anonymous access to the MCP server is allowed in dev mode.
	if !enforceAuth {
		return nil
	}
	if getVKFromRequest(ctx) != "" || bifrost.GetStringFromContext(bifrostCtx, schemas.BifrostContextKeyUserID) != "" {
		return nil
	}
	if discoveryEnabled {
		ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
	}
	return fmt.Errorf("virtual key required to access mcp server; set one of x-bf-vk, Authorization: Bearer <vk>, x-api-key, or x-goog-api-key in your MCP client config")
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
