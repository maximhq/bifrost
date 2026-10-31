// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains MCP (Model Context Protocol) server implementation for HTTP streaming.
package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
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
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// sseHeartbeatInterval is the cadence of SSE comment pings on the MCP SSE
// stream. It must stay below typical proxy/load-balancer idle timeouts (60s on
// most stacks) so connections aren't reaped, while being large enough to avoid
// gratuitous wake-ups on idle clients.
var sseHeartbeatInterval = 15 * time.Second

// mcpServerName is what the server calls itself in the initialize handshake. One server serves
// every caller, so it carries no caller's name.
const mcpServerName = "bifrost"

// MCPToolExecutor interface defines the method needed for executing MCP tools
type MCPToolManager interface {
	GetAvailableMCPTools(ctx context.Context) []schemas.ChatTool
	// GetMCPServerInstructions returns the upstream instructions this request may see,
	// already aggregated and size-bounded. Scoped by the same context the tool filter reads.
	GetMCPServerInstructions(ctx context.Context) string
	ExecuteChatMCPTool(ctx context.Context, toolCall *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError)
	ExecuteResponsesMCPTool(ctx context.Context, toolCall *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError)
}

// MCPGatewayAdmitter decides whether a /mcp request may be served, and what it may reach. It runs
// the request through the same governance funnel every tool execution passes, so a caller is told at
// initialize and tools/list exactly what tools/call would tell them, by the same rules. It also
// resolves what a /mcp/<slug> request may see, so slug endpoints need no optional capability.
type MCPGatewayAdmitter interface {
	// AdmitMCPGatewayRequest evaluates the request as an MCP tool execution with no tool named yet,
	// resolving and recording its access on ctx. It returns that access, and the refusal governance
	// gave when it gave one. Nil access with no refusal means nothing restricts the request (the
	// deployment has nothing to resolve access with, or the request presented nothing), and it is
	// served unrestricted, as every other path serves it.
	AdmitMCPGatewayRequest(ctx *schemas.BifrostContext) (schemas.Access, *schemas.BifrostError)
	// VirtualMCPToolAccess returns the served tool include-list and whether the slug's Virtual MCP is
	// assigned to the request's key (assigned=false → 403). Tools are narrowed to the request's access.
	VirtualMCPToolAccess(ctx *schemas.BifrostContext, slug string, access schemas.Access) (served []string, assigned bool)
	// MCPClientToolAccess is the same for a single MCP client the slug names (ok=false → 403).
	MCPClientToolAccess(ctx *schemas.BifrostContext, slug string, access schemas.Access) (served []string, ok bool)
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
	// mcpServer is the one server every caller is served from, for the life of the handler. What
	// each caller sees of it is decided per request, from the tool list stamped on the request
	// context, so nothing about it is per caller. Its tools are replaced in place on a registry change.
	mcpServer *server.MCPServer
	// transport speaks the Streamable HTTP rules of every protocol revision on top of mcpServer.
	transport *server.StreamableHTTPServer
	served    MCPResourcesAndPromptsServer // nil serves tools only
	// tokenValidator, when set, may refuse a verified Bifrost-issued token.
	tokenValidator MCPTokenValidator
}

// SetTokenValidator installs a check every verified Bifrost-issued token on /mcp
// must pass, for grants whose tokens can be revoked before they expire. nil
// removes it.
func (h *MCPServerHandler) SetTokenValidator(validator MCPTokenValidator) {
	h.tokenValidator = validator
}

// VerifyBifrostToken verifies a Bifrost-issued access token for audience: its
// signature under the server's key, issuer, lifetime and audience. It lets
// another module accept these tokens on routes /mcp does not serve.
func (h *MCPServerHandler) VerifyBifrostToken(ctx *fasthttp.RequestCtx, rawToken, audience string) (*MCPTokenClaims, error) {
	// Never verify against an issuer derived from the request Host header, which
	// the caller controls: with none pinned, no token is accepted.
	h.config.Mu.RLock()
	oauthCfg := h.config.ClientConfig.OAuth2ServerConfig
	h.config.Mu.RUnlock()
	if oauthCfg == nil || strings.TrimSpace(oauthCfg.IssuerURL.GetValue()) == "" {
		return nil, fmt.Errorf("issuer_url is not set")
	}
	signingKey, err := h.config.GetOAuth2SigningKey(ctx)
	if err != nil {
		logger.Error("mcp: failed to load oauth2 signing key for jwt verification: %v", err)
		return nil, fmt.Errorf("signing key unavailable")
	}
	claims, err := verifyBifrostJWT(ctx, rawToken, h.config, signingKey, audience)
	if err != nil {
		return nil, err
	}
	public := claims.public(rawToken)
	return &public, nil
}

// GatewayAudience identifies Bifrost's inference routes as a token audience.
func (h *MCPServerHandler) GatewayAudience(ctx *fasthttp.RequestCtx) string {
	return oauth2GatewayAudience(ctx, h.config)
}

// getVirtualKeyByID resolves a virtual key by its row ID for the JWT auth path,
// preferring the governance in-memory cache and falling back to the config store
// on a miss (e.g. a key created since the cache last refreshed) or when no cache
// is wired. Whether the key may be used is not decided here: governance refuses a
// key that is inactive or expired, from the grant it resolves for it.
func (h *MCPServerHandler) getVirtualKeyByID(ctx context.Context, vkID string) (*tables.TableVirtualKey, error) {
	return virtualKeyByID(ctx, h.vkCache, h.config.ConfigStore, vkID)
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

	handler.served, _ = toolManager.(MCPResourcesAndPromptsServer)
	handler.mcpServer = handler.buildServer()
	handler.transport = newMCPTransport(handler.mcpServer)
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
	// Streamable HTTP: POST carries messages, GET opens the legacy notification stream, DELETE ends
	// a legacy session. Slug endpoints are the same handler, narrowed to the slug in admit.
	handler := lib.ChainMiddlewares(h.handleMCPServer, middlewares...)
	for _, path := range []string{"/mcp", "/mcp/{slug}"} {
		r.POST(path, handler)
		r.GET(path, handler)
		r.DELETE(path, handler)
	}
}

// handleMCPServer admits the request, then hands it to mcp-go's Streamable HTTP transport, which
// applies the rules of whichever protocol revision the client speaks. Requests that cannot open a
// stream are answered inline; GET and subscriptions/listen are served as SSE streams.
func (h *MCPServerHandler) handleMCPServer(ctx *fasthttp.RequestCtx) {
	if h.refuseOrigin(ctx) {
		return
	}
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	bifrostCtx.SetValue(schemas.BifrostContextKeyIsMCPGateway, true)

	if refusal := h.admit(ctx, bifrostCtx); refusal != nil {
		cancel()
		sendMCPRefusal(ctx, refusal)
		return
	}

	req := &server.HTTPRequest{
		Method:  string(ctx.Method()),
		Header:  mcpRequestHeader(ctx),
		Body:    ctx.PostBody(),
		Context: bifrostCtx,
	}
	if !opensMCPStream(ctx) {
		defer cancel()
		w := &mcpBufferedWriter{ctx: ctx, header: make(http.Header, 4)}
		h.transport.Handle(w, req)
		w.finish()
		return
	}
	h.serveMCPStream(ctx, bifrostCtx, cancel, req)
}

// serveMCPStream runs the transport on its own goroutine so the response can outlive this handler.
// It waits until the transport either starts an SSE stream or finishes with a plain reply, so a
// refusal (405 for a modern GET, a version error) is answered as an ordinary response.
func (h *MCPServerHandler) serveMCPStream(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, cancel context.CancelFunc, req *server.HTTPRequest) {
	w := newMCPStreamWriter(cancel)
	handled := make(chan struct{})

	// Pre-allocate the completer slot before the stream can start; see streamMCP for why.
	var completerSlot atomic.Value
	ctx.SetUserValue(schemas.BifrostContextKeyTransportPostHookCompleter, &completerSlot)
	traceCompleter, _ := ctx.UserValue(schemas.BifrostContextKeyTraceCompleter).(func([]schemas.PluginLogEntry))

	go func() {
		defer close(handled)
		h.transport.Handle(w, req)
	}()

	select {
	case <-w.streaming:
	case <-handled:
		cancel()
		w.copyHeaders(ctx)
		ctx.SetBody(w.body)
		return
	}

	// Signal to transport-plugin and tracing middlewares that this is a streaming
	// response. Without this, fasthttpResponseToHTTPResponse calls ctx.Response.Body()
	// during post-hook processing, which materializes the SSE body stream and
	// deadlocks waiting for an EOF that only arrives after the goroutine exits.
	ctx.SetUserValue(schemas.BifrostContextKeyDeferTraceCompletion, true)
	w.copyHeaders(ctx)
	ctx.Response.Header.Set("X-Accel-Buffering", "no")
	ctx.Response.SetBodyStream(w.reader, -1)

	go streamMCP(bifrostCtx, cancel, w.reader, handled, &completerSlot, traceCompleter)
}

// streamMCP keeps an MCP SSE stream alive until the transport finishes or the client goes away,
// then runs the deferred transport post-hooks and completes the trace. It reads nothing from the
// fasthttp ctx: TransportInterceptorMiddleware stores the completer into the closure-captured slot
// after the handler returns, and fasthttp recycles the RequestCtx.
func streamMCP(bifrostCtx *schemas.BifrostContext, cancel context.CancelFunc, reader *lib.SSEStreamReader, handled <-chan struct{}, completerSlot *atomic.Value, traceCompleter func([]schemas.PluginLogEntry)) {
	var transportLogs []schemas.PluginLogEntry
	defer func() {
		// Run the deferred transport post-hook completer before cancelling the
		// context so plugins see a live context. Errors are logged server-side
		// only — the stream is already closing.
		transportLogs = runMCPStreamCompleter(completerSlot)
		cancel()
		reader.Done()
		// Complete the trace after streaming finishes, passing transport plugin logs.
		if traceCompleter != nil {
			traceCompleter(transportLogs)
		}
	}()

	// Periodic SSE comment heartbeats keep idle connections alive through
	// proxies and let us detect client disconnect via reader.SendHeartbeat()
	// returning false — fasthttp.RequestCtx never cancels bifrostCtx on its own.
	//
	// Use the shared frame, never a local one: a hand-rolled ": ping\n\n" carries the
	// trailing blank line #5883 removed (some decoders dispatch it as an empty event,
	// #5874) and bypasses the line-boundary gate #5905 added.
	// One heartbeat up front: fasthttp sends the headers with the first chunk, and a legacy GET
	// stream may have nothing else to say for a while.
	if !reader.SendHeartbeat() {
		cancel()
		<-handled
		return
	}
	ticker := time.NewTicker(sseHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !reader.SendHeartbeat() {
				cancel()
				<-handled
				return
			}
		case <-handled:
			return
		case <-(*bifrostCtx).Done():
			<-handled
			return
		}
	}
}

// runMCPStreamCompleter invokes the transport post-hook completer once the middleware has published
// it, and returns the transport plugin logs. The store races with the stream on fast or empty
// streams, so it waits briefly; 100ms is ample, as it runs a few instructions after the handler returns.
func runMCPStreamCompleter(completerSlot *atomic.Value) []schemas.PluginLogEntry {
	var loaded any
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		if loaded = completerSlot.Load(); loaded != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	postHookCompleter, ok := loaded.(func() ([]schemas.PluginLogEntry, error))
	if !ok {
		return nil
	}
	logs, err := postHookCompleter()
	if err != nil {
		logger.Warn("transport post-hook failed after stream terminated: %v", err)
	}
	return logs
}

// SyncMCPServer replaces the server's tools with those currently available. Called whenever the set
// of MCP clients or their tools changes; the server tells listening clients the list changed. Every
// caller is served from this one server; what each of them sees is decided per request.
func (h *MCPServerHandler) SyncMCPServer(ctx context.Context) error {
	availableTools := h.toolManager.GetAvailableMCPTools(ctx)
	h.mcpServer.SetTools(h.serverTools(availableTools)...)
	logger.Debug("Synced MCP server with %d tools", len(availableTools))
	if h.served != nil {
		h.syncResourcesAndPrompts(ctx)
	}
	return nil
}

// server returns the server requests are served from.
func (h *MCPServerHandler) server() *server.MCPServer {
	return h.mcpServer
}

// forwardServerInstructions answers initialize with the upstream servers' own usage guidance,
// which the MCP spec carries in this field and which a gateway that drops it eats on the
// client's behalf. Runs as an AfterInitialize hook rather than through server.WithInstructions
// because that option is fixed at construction, and one server here serves every caller: the
// text has to be resolved per request, from the same context the tool filter reads, or a caller
// narrowed to one upstream would be handed the instructions of servers it cannot reach.
func (h *MCPServerHandler) forwardServerInstructions(ctx context.Context, _ any, _ *mcp.InitializeRequest, result *mcp.InitializeResult) {
	if result != nil {
		h.setInstructions(ctx, &result.Instructions)
	}
}

// forwardDiscoverInstructions is forwardServerInstructions for 2026-07-28 clients, which learn
// about the server from server/discover instead of initialize.
func (h *MCPServerHandler) forwardDiscoverInstructions(ctx context.Context, _ any, _ *mcp.DiscoverRequest, result *mcp.DiscoverResult) {
	if result != nil {
		h.setInstructions(ctx, &result.Instructions)
	}
}

// setInstructions applies the request's upstream instructions. Silent when there are none, so no
// upstream instructions means no field at all rather than an empty one.
func (h *MCPServerHandler) setInstructions(ctx context.Context, field *string) {
	if instructions := h.toolManager.GetMCPServerInstructions(ctx); instructions != "" {
		*field = instructions
	}
}

// buildServer builds the one server every caller is served from, with no tools yet.
func (h *MCPServerHandler) buildServer() *server.MCPServer {
	hooks := &server.Hooks{}
	hooks.AddAfterListTools(h.narrowListedTools)
	hooks.AddAfterInitialize(h.forwardServerInstructions)
	hooks.AddAfterDiscover(h.forwardDiscoverInstructions)

	// No WithToolFilter: mcp-go also applies a tool filter to tools/call and answers "not found" before
	// Bifrost runs, which skips the MCP pre-hooks (governance, logging). tools/list is narrowed by
	// narrowListedTools instead, and tools/call is decided by Bifrost.
	options := []server.ServerOption{
		server.WithToolCapabilities(true),
	}
	if h.served != nil {
		options = append(options, h.resourcesAndPromptsOptions(hooks)...)
	}
	return server.NewMCPServer(mcpServerName, version, append(options, server.WithHooks(hooks))...)
}

// serverTools turns every available tool into a server tool. A tool's handler reads nothing about
// the caller: what a request may see and call rides on its context, and both the tool filter and
// the executor read it from there.
func (h *MCPServerHandler) serverTools(availableTools []schemas.ChatTool) []server.ServerTool {
	tools := make([]server.ServerTool, 0, len(availableTools))
	for _, tool := range availableTools {
		// Only process function tools (skip custom tools)
		if tool.Function == nil {
			continue
		}

		// Capture tool name for closure
		toolName := tool.Function.Name
		declaresOutputSchema := tool.MCP != nil && len(tool.MCP.OutputSchema) > 0

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
					return mcp.NewToolResultError(mcpAuthRequiredToolResult(authReq)), nil
				}
				return mcp.NewToolResultError(fmt.Sprintf("Tool execution failed: %v", bifrost.GetErrorMessage(err))), nil
			}
			logger.Debug("[mcp-server] tool handler success tool=%q", toolName)

			return gatewayToolResult(toolMessage, declaresOutputSchema), nil
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

		serverTool := mcp.Tool{
			Name:        toolName,
			Description: description,
			InputSchema: inputSchema,
			Annotations: toolAnnotation,
		}
		if tool.MCP != nil {
			serverTool.Title, serverTool.Icons, serverTool.RawOutputSchema = tool.MCP.Title, tool.MCP.Icons, tool.MCP.OutputSchema
		}
		tools = append(tools, server.ServerTool{Tool: serverTool, Handler: handler})
	}
	return tools
}

// gatewayToolResult is what /mcp answers for a tool message: the upstream's own result while no plugin
// has rewritten the message, else a result rebuilt from it. isError always follows the message.
func gatewayToolResult(message *schemas.ChatMessage, declaresOutputSchema bool) *mcp.CallToolResult {
	if message == nil {
		return mcp.NewToolResultText("")
	}
	isError := message.ChatToolMessage != nil && message.ChatToolMessage.IsError != nil && *message.ChatToolMessage.IsError
	if upstream := message.ChatToolMessage.MCPResult(message.Content); upstream != nil {
		result := *upstream
		result.IsError = isError
		return &result
	}
	result := &mcp.CallToolResult{IsError: isError, Content: []mcp.Content{}}
	if message.Content == nil {
		return result
	}
	if message.Content.ContentStr != nil {
		result.Content = append(result.Content, mcp.NewTextContent(*message.Content.ContentStr))
	}
	for _, block := range message.Content.ContentBlocks {
		if content := gatewayToolContent(block); content != nil {
			result.Content = append(result.Content, content)
		}
	}
	// A tool declaring an output schema must return structured content; a rebuilt result carries it
	// in its JSON text (the form MCP asks servers to mirror it in).
	if declaresOutputSchema && !isError {
		var text strings.Builder
		for _, content := range result.Content {
			if textContent, ok := content.(mcp.TextContent); ok {
				text.WriteString(textContent.Text)
			}
		}
		var structured map[string]any
		if sonic.UnmarshalString(text.String(), &structured) == nil && structured != nil {
			result.StructuredContent = structured
		} else {
			// A success must carry what the schema promises; the upstream's own would undo the rewrite.
			result.IsError = true
			result.Content = append(result.Content, mcp.NewTextContent("This result was changed after the tool ran and no longer matches the tool's output schema."))
		}
	}
	return result
}

// gatewayToolContent turns a tool message content block back into MCP content.
func gatewayToolContent(block schemas.ChatContentBlock) mcp.Content {
	switch {
	case block.Text != nil:
		return mcp.NewTextContent(*block.Text)
	case block.ImageURLStruct != nil:
		if mimeType, data, ok := schemas.SplitBase64DataURL(block.ImageURLStruct.URL); ok {
			return mcp.NewImageContent(data, mimeType)
		}
		return mcp.NewTextContent(block.ImageURLStruct.URL)
	case block.InputAudio != nil:
		mimeType := "audio/wav"
		if block.InputAudio.Format != nil {
			mimeType = "audio/" + *block.InputAudio.Format
		}
		// MCP audio is bare base64, like blobs.
		data := block.InputAudio.Data
		if urlType, payload, ok := schemas.SplitBase64DataURL(data); ok {
			data, mimeType = payload, urlType
		}
		return mcp.NewAudioContent(data, mimeType)
	case block.File != nil && block.File.FileData != nil:
		// MCP blobs are bare base64; tool messages carry files as data URLs.
		data, mimeType := *block.File.FileData, ""
		if urlType, payload, ok := schemas.SplitBase64DataURL(data); ok {
			data, mimeType = payload, urlType
		}
		if block.File.FileType != nil {
			mimeType = *block.File.FileType
		}
		return mcp.NewEmbeddedResource(mcp.BlobResourceContents{URI: "file:///" + derefOr(block.File.Filename, "file"), MIMEType: mimeType, Blob: data})
	}
	return nil
}

func derefOr(value *string, fallback string) string {
	if value == nil || *value == "" {
		return fallback
	}
	return *value
}

// narrowListedTools narrows a tools/list answer to what the request's context permits: the include
// lists the caller sent (x-bf-mcp-include-clients, x-bf-mcp-include-tools) and the tool list the
// request was admitted with, which admit stamps on the same key. A no-op when the context carries
// neither. It edits the page mcp-go built, so the server must not paginate tools/list.
func (h *MCPServerHandler) narrowListedTools(ctx context.Context, _ any, _ *mcp.ListToolsRequest, result *mcp.ListToolsResult) {
	if result == nil || !requestNarrowed(ctx) {
		return
	}
	result.Tools = keepAllowed(result.Tools, func(t mcp.Tool) string { return t.Name }, h.toolManager.GetAvailableMCPTools(ctx), func(t schemas.ChatTool) string {
		if t.Function == nil {
			return ""
		}
		return t.Function.Name
	})
}

// Admission

// mcpRefusal is what a /mcp request is refused with: the status to answer and the message to say.
// A governance refusal also keeps the error it came as, so /mcp answers with the same error type
// execute and chat answer with.
type mcpRefusal struct {
	status  int
	message string
	err     *schemas.BifrostError
}

// sendMCPRefusal writes a refusal: the governance error as it came, with the refusal's status, or
// a plain error carrying the status and message.
func sendMCPRefusal(ctx *fasthttp.RequestCtx, refusal *mcpRefusal) {
	if refusal.err != nil {
		SendBifrostError(ctx, refusal.err)
		return
	}
	SendError(ctx, refusal.status, refusal.message)
}

// admit settles who is asking and what they may reach, in that order, before a single JSON-RPC
// message is handled. Authentication is the transport's: which credential the request presents,
// whether the configured mode accepts it, whether it verifies. What the credential grants is
// governance's, asked through the admitter so initialize and tools/list are refused by exactly the
// rules tools/call is refused by. What survives is stamped on bifrostCtx as the tool list this
// request may see and call.
func (h *MCPServerHandler) admit(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) *mcpRefusal {
	if err := h.authenticate(ctx, bifrostCtx); err != nil {
		// Every credential refusal in a mode that accepts Bifrost-issued tokens points at where a
		// token can be obtained, including the ones authenticate returns without setting it itself
		// (a token whose virtual key no longer exists, for one).
		if h.discoveryEnabled() && len(ctx.Response.Header.Peek("WWW-Authenticate")) == 0 {
			ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
		}
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
		sent := *refused
		sent.StatusCode = &status
		return &mcpRefusal{status: status, message: bifrost.GetErrorMessage(refused), err: &sent}
	}

	// A /mcp/<slug> endpoint serves one Virtual MCP or one MCP client: gate on the caller's grant and
	// narrow the tools to it. The plain /mcp route carries no slug and serves the full access.
	if slug, _ := ctx.UserValue("slug").(string); slug != "" {
		return h.admitBySlug(bifrostCtx, slug, access)
	}

	stampToolAccess(bifrostCtx, access)
	return nil
}

// mcpAuthRequiredToolResult is the text a tool result carries when a tool could not run because the
// caller still has to authenticate with the upstream server. The interactive kinds point the caller
// at a page to open: per-user OAuth at the provider's authorize page, per-user headers at the
// workspace page where the header values are submitted. Delegated exchange has no page to open,
// because what needs fixing is the credential on the request itself, so its resolver's message is
// the whole answer and is passed through as written. An interactive kind that arrived without a
// page to open falls back to its message the same way, rather than prompting the caller to open
// nothing.
func mcpAuthRequiredToolResult(authReq *schemas.MCPAuthRequiredError) string {
	url := authReq.AuthorizeURL
	action := "connect your account"
	switch authReq.Kind {
	case schemas.MCPAuthRequiredKindExchange:
		url = ""
	case schemas.MCPAuthRequiredKindHeaders:
		url = authReq.SubmitURL
		action = "submit the required headers"
	}
	if url == "" {
		if authReq.Message != "" {
			return authReq.Message
		}
		return fmt.Sprintf("Authentication required for %s.", authReq.MCPClientName)
	}
	message := fmt.Sprintf("Authentication required for %s. Open this URL to %s: %s", authReq.MCPClientName, action, url)
	if schemas.MCPAuthURLHasTempTokenFragment(url) {
		message += schemas.MCPAuthTempTokenReminder
	}
	return message
}

// admitBySlug narrows an already-admitted request to the Virtual MCP or MCP client its slug names,
// stamping the served tools; 403 if the caller may reach neither. Without an admitter nothing is
// governed, so a client slug is served whole, as /mcp serves it.
func (h *MCPServerHandler) admitBySlug(bifrostCtx *schemas.BifrostContext, slug string, access schemas.Access) *mcpRefusal {
	if h.admitter == nil {
		if _, clientName, found := h.config.GetMCPClientBySlug(slug); found {
			bifrostCtx.SetValue(schemas.MCPContextKeyIncludeTools, []string{clientName + "-*"})
			return nil
		}
		return &mcpRefusal{status: fasthttp.StatusForbidden, message: "access_denied"}
	}
	// Slugs are unique across both namespaces, so try the Virtual MCP first, then a single client.
	if served, assigned := h.admitter.VirtualMCPToolAccess(bifrostCtx, slug, access); assigned {
		bifrostCtx.SetValue(schemas.MCPContextKeyIncludeTools, served)
		return nil
	}
	if served, ok := h.admitter.MCPClientToolAccess(bifrostCtx, slug, access); ok {
		bifrostCtx.SetValue(schemas.MCPContextKeyIncludeTools, served)
		return nil
	}
	// The wire answer stays 403 either way so slugs can't be probed; the log says which it was.
	if _, _, found := h.config.GetMCPClientBySlug(slug); found {
		logger.Debug("mcp: refused /mcp/%s: MCP client exists but the caller's access does not grant it", slug)
	} else {
		logger.Debug("mcp: refused /mcp/%s: no enabled MCP client and no Virtual MCP granted to the caller has this slug", slug)
	}
	return &mcpRefusal{status: fasthttp.StatusForbidden, message: "access_denied"}
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

// refuseOrigin answers 403 for a browser request whose Origin may not reach /mcp. Requests without
// an Origin (non-browser clients) pass. Returns true if the request was refused.
func (h *MCPServerHandler) refuseOrigin(ctx *fasthttp.RequestCtx) bool {
	origin := ctx.Request.Header.Peek("Origin")
	if len(origin) == 0 || h.originAllowed(string(origin)) {
		return false
	}
	SendError(ctx, fasthttp.StatusForbidden, fmt.Sprintf("Requests to /mcp from origin %q are refused; list the origin in client_config.allowed_origins (localhost origins are always allowed).", origin))
	return true
}

// originAllowed guards /mcp against DNS rebinding. Without auth, "*" and same-origin do not count
// (a rebound page is same-origin); with auth, allowed_origins applies as configured.
func (h *MCPServerHandler) originAllowed(origin string) bool {
	h.config.Mu.RLock()
	enforceAuth, allowed := h.config.ClientConfig.EnforceAuthOnInference, h.config.ClientConfig.AllowedOrigins
	h.config.Mu.RUnlock()
	if enforceAuth {
		return IsOriginAllowed(origin, allowed)
	}
	return originExplicitlyAllowed(origin, allowed)
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
//  1. An identity an upstream auth layer already authenticated (headers or both)
//  2. JWT Bearer token (when MCPServerAuthMode is both or oauth)
//  3. Header credentials (headers or both)
//  4. Anonymous access (when EnforceAuthOnInference is false)
//
// When MCPServerAuthMode is oauth (strict), header credentials are rejected.
func (h *MCPServerHandler) authenticate(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) error {
	enforceAuth, authMode := h.authSettings()
	discoveryEnabled := authMode == tables.MCPServerAuthModeBoth || authMode == tables.MCPServerAuthModeOAuth

	// --- Identity an upstream auth layer already authenticated ---
	// An upstream auth layer that verified the request's bearer against its own identity
	// provider stamps the user it names, and converting the request copied that onto
	// bifrostCtx. Such a bearer is a JWT this server never issued, so the JWT path below
	// would refuse it on the key id; the stamped user is the settled identity, and
	// governance resolves what it may reach. Accepted wherever header credentials are:
	// oauth strict mode admits only tokens this server issued and is excluded.
	if authMode != tables.MCPServerAuthModeOAuth &&
		bifrost.GetStringFromContext(bifrostCtx, schemas.BifrostContextKeyUserID) != "" {
		return nil
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
		if h.tokenValidator != nil {
			if err := h.tokenValidator(ctx, claims.public(rawJWT)); err != nil {
				ctx.Response.Header.Set("WWW-Authenticate", wwwAuthenticateValue(ctx, h.config))
				return err
			}
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
//  5. api-key        — must start with the VK prefix
//
// The prefix gate (governance.VirtualKeyPrefix) on the latter four lets real
// provider credentials pass through untouched, so only Bifrost virtual keys are
// picked up here. This is a projection of the shared resolver
// (lib.ResolveVirtualKeyFromHeaders), which is what keeps MCP, inference and the
// Agent Gateway at parity. Returns "" when no header carries a virtual key.
func getVKFromRequest(ctx *fasthttp.RequestCtx) string {
	vk, _ := lib.ResolveVirtualKeyFromHeaders(ctx)
	return vk
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

// Streamable HTTP transport over fasthttp

// newMCPTransport serves srv with the Streamable HTTP rules of every protocol revision mcp-go knows.
func newMCPTransport(srv *server.MCPServer) *server.StreamableHTTPServer {
	return server.NewStreamableHTTPServer(srv, server.WithSessionIdManager(&mcpSessionIDManager{}))
}

// mcpSessionIDManager hands legacy clients a session id at initialize but never requires one back.
// The gateway keeps no per-session state, so requests without an id, or with one minted by another
// replica, are served exactly as before ids were issued.
type mcpSessionIDManager struct {
	server.StatelessGeneratingSessionIdManager
}

func (*mcpSessionIDManager) Validate(string) (isTerminated bool, err error) { return false, nil }

// mcpRequestHeader copies the headers the transport reads (content negotiation, Mcp-* and
// MCP-Protocol-Version, Last-Event-ID) into the form mcp-go reads them in. Credentials stay out.
func mcpRequestHeader(ctx *fasthttp.RequestCtx) http.Header {
	header := make(http.Header, 6)
	for key, value := range ctx.Request.Header.All() {
		if len(key) >= 4 && bytes.EqualFold(key[:4], []byte("mcp-")) ||
			bytes.EqualFold(key, []byte(fasthttp.HeaderContentType)) ||
			bytes.EqualFold(key, []byte(fasthttp.HeaderAccept)) ||
			bytes.EqualFold(key, []byte("Last-Event-ID")) {
			header.Add(string(key), string(value))
		}
	}
	return header
}

// opensMCPStream reports whether the request is one whose answer is an SSE stream: the legacy GET
// stream, or a 2026-07-28 subscriptions/listen. Everything else is answered inline.
func opensMCPStream(ctx *fasthttp.RequestCtx) bool {
	if ctx.IsGet() {
		return true
	}
	if !ctx.IsPost() {
		return false
	}
	if method := ctx.Request.Header.Peek(mcp.HeaderMethod); len(method) > 0 {
		return string(method) == string(mcp.MethodSubscriptionsListen)
	}
	return gjson.GetBytes(ctx.PostBody(), "method").Str == string(mcp.MethodSubscriptionsListen)
}

// mcpBufferedWriter writes an inline answer straight into the fasthttp response. It reports that it
// cannot stream, so mcp-go answers with a single JSON body on the calling goroutine.
type mcpBufferedWriter struct {
	ctx         *fasthttp.RequestCtx
	header      http.Header
	wroteHeader bool
}

func (w *mcpBufferedWriter) Header() http.Header { return w.header }

func (w *mcpBufferedWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.ctx.SetStatusCode(status)
	copyMCPHeaders(w.ctx, w.header)
}

func (w *mcpBufferedWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.ctx.Write(p)
}

func (w *mcpBufferedWriter) Flush()          {}
func (w *mcpBufferedWriter) CanStream() bool { return false }

// finish commits the headers of an answer that wrote no body.
func (w *mcpBufferedWriter) finish() { w.WriteHeader(http.StatusOK) }

// mcpStreamWriter is written by mcp-go on its own goroutine. An SSE answer is forwarded to the
// client through reader as it is written; any other answer is kept in body for the handler to send.
type mcpStreamWriter struct {
	header      http.Header
	status      int
	wroteHeader bool
	body        []byte
	reader      *lib.SSEStreamReader
	streaming   chan struct{} // closed once the answer is known to be an SSE stream
	cancel      context.CancelFunc
}

func newMCPStreamWriter(cancel context.CancelFunc) *mcpStreamWriter {
	return &mcpStreamWriter{header: make(http.Header, 4), status: http.StatusOK, streaming: make(chan struct{}), cancel: cancel}
}

func (w *mcpStreamWriter) Header() http.Header { return w.header }

func (w *mcpStreamWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	if mediaType := w.header.Get("Content-Type"); strings.HasPrefix(mediaType, "text/event-stream") {
		w.reader = lib.NewSSEStreamReader()
		close(w.streaming)
	}
}

func (w *mcpStreamWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	if w.reader == nil {
		w.body = append(w.body, p...)
		return len(p), nil
	}
	// mcp-go reuses its write buffer, so the reader gets its own copy.
	if !w.reader.Send(append([]byte(nil), p...)) {
		w.cancel()
		return 0, errMCPClientGone
	}
	return len(p), nil
}

// Flush is a no-op: Write already hands every chunk to the client.
func (w *mcpStreamWriter) Flush()          {}
func (w *mcpStreamWriter) CanStream() bool { return true }

// copyHeaders commits the status and headers mcp-go wrote. Called by the handler once the answer is
// known, after which mcp-go no longer touches them.
func (w *mcpStreamWriter) copyHeaders(ctx *fasthttp.RequestCtx) {
	ctx.SetStatusCode(w.status)
	copyMCPHeaders(ctx, w.header)
}

var errMCPClientGone = errors.New("mcp client disconnected")

func copyMCPHeaders(ctx *fasthttp.RequestCtx, header http.Header) {
	for key, values := range header {
		for i, value := range values {
			if i == 0 {
				ctx.Response.Header.Set(key, value)
			} else {
				ctx.Response.Header.Add(key, value)
			}
		}
	}
}
