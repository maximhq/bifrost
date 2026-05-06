package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	ws "github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
	"github.com/valyala/fasthttp"
)

// wsWriter abstracts a WebSocket write target. Both *ws.Conn (pre-session)
// and *bfws.Session (post-session, mutex-protected) satisfy this interface.
type wsWriter interface {
	WriteMessage(messageType int, data []byte) error
}

// WSResponsesHandler handles WebSocket connections for the Responses API WebSocket Mode.
// Clients connect via `GET /v1/responses` with a WS upgrade and send `response.create` events.
// Each event is routed through the standard Bifrost inference pipeline (PreLLMHook, key selection,
// provider call, PostLLMHook) via the HTTP bridge, with native WS upstream as an optimization.
type WSResponsesHandler struct {
	client       *bifrost.Bifrost
	config       *lib.Config
	handlerStore lib.HandlerStore
	pool         *bfws.Pool
	sessions     *bfws.SessionManager
	upgrader     ws.FastHTTPUpgrader
}

// NewWSResponsesHandler creates a new WebSocket Responses handler.
func NewWSResponsesHandler(client *bifrost.Bifrost, config *lib.Config, pool *bfws.Pool) *WSResponsesHandler {
	maxConns := config.WebSocketConfig.MaxConnections

	return &WSResponsesHandler{
		client:       client,
		config:       config,
		handlerStore: config,
		pool:         pool,
		sessions:     bfws.NewSessionManager(maxConns),
		upgrader: ws.FastHTTPUpgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
				origin := string(ctx.Request.Header.Peek("Origin"))
				if origin == "" {
					return true
				}
				return IsOriginAllowed(origin, config.ClientConfig.AllowedOrigins)
			},
		},
	}
}

// Close gracefully shuts down all active WebSocket responses sessions.
func (h *WSResponsesHandler) Close() {
	if h == nil || h.sessions == nil {
		return
	}
	h.sessions.CloseAll()
}

// RegisterRoutes registers the WebSocket Responses endpoint at the base path
// and all OpenAI integration paths.
func (h *WSResponsesHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handler := lib.ChainMiddlewares(h.handleUpgrade, middlewares...)
	// Base path (outside integration prefix)
	r.GET("/v1/responses", handler)
	// OpenAI integration paths (/openai/v1/responses, /openai/responses, /openai/openai/responses)
	for _, path := range integrations.OpenAIWSResponsesPaths("/openai") {
		r.GET(path, handler)
	}
}

// handleUpgrade upgrades the HTTP connection to WebSocket and starts the event loop.
func (h *WSResponsesHandler) handleUpgrade(ctx *fasthttp.RequestCtx) {
	err := h.upgrader.Upgrade(ctx, func(conn *ws.Conn) {
		defer conn.Close()

		session, sessionErr := h.sessions.Create(conn)
		if sessionErr != nil {
			writeWSError(conn, 429, "websocket_connection_limit_reached", sessionErr.Error())
			return
		}
		defer h.sessions.Remove(conn)

		// Capture auth headers from the upgrade request for per-event context creation
		authHeaders := captureAuthHeaders(ctx)

		h.eventLoop(conn, session, authHeaders)
	})
	if err != nil {
		logger.Warn("websocket upgrade failed for /v1/responses: %v", err)
	}
}

// authHeaders holds auth-related headers captured during the WS upgrade.
type authHeaders struct {
	authorization string
	virtualKey    string
	apiKey        string
	googAPIKey    string
	baggage       string
	headers       map[string][]string
}

// captureAuthHeaders captures the auth headers from the request.
func captureAuthHeaders(ctx *fasthttp.RequestCtx) *authHeaders {
	ah := &authHeaders{
		authorization: string(ctx.Request.Header.Peek("Authorization")),
		virtualKey:    string(ctx.Request.Header.Peek("x-bf-vk")),
		apiKey:        string(ctx.Request.Header.Peek("x-api-key")),
		googAPIKey:    string(ctx.Request.Header.Peek("x-goog-api-key")),
		baggage:       string(ctx.Request.Header.Peek("baggage")),
		headers:       make(map[string][]string),
	}

	for key, value := range ctx.Request.Header.All() {
		k := strings.ToLower(string(key))
		ah.headers[k] = append(ah.headers[k], string(value))
	}
	return ah
}

// eventLoop reads events from the client WebSocket and processes them.
func (h *WSResponsesHandler) eventLoop(conn *ws.Conn, session *bfws.Session, auth *authHeaders) {
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if isExpectedResponsesClientClose(err, session) {
				return
			}
			logger.Warn("websocket read error: %v", err)
			return
		}

		// Parse the event type
		var envelope struct {
			Type string `json:"type"`
		}
		if err := sonic.Unmarshal(message, &envelope); err != nil {
			writeWSError(session, 400, "invalid_request_error", "failed to parse event JSON")
			continue
		}

		switch schemas.WebSocketEventType(envelope.Type) {
		case schemas.WSEventResponseCreate:
			h.handleResponseCreate(session, auth, message)
		default:
			writeWSError(session, 400, "invalid_request_error", "unsupported event type: "+envelope.Type)
		}
	}
}

// handleResponseCreate processes a response.create event.
// Strategy: try native WS upstream for providers that support it, otherwise use HTTP bridge.
// If native WS upstream fails mid-stream, falls back to HTTP bridge.
func (h *WSResponsesHandler) handleResponseCreate(session *bfws.Session, auth *authHeaders, message []byte) {
	var event schemas.WebSocketResponsesEvent

	if err := sonic.Unmarshal(message, &event); err != nil {
		writeWSError(session, 400, "invalid_request_error", "failed to parse response.create event")
		return
	}

	// Store override: default to store=true (Codex sends false by default but expects true).
	// If DisableStore is set in provider config, force store=false.
	// If client explicitly sets store, respect that value unless DisableStore overrides it.
	provider, modelName := schemas.ParseModelString(event.Model, schemas.OpenAI)
	if provider == "" || modelName == "" {
		writeWSError(session, 400, "invalid_request_error", "failed to parse model string")
		return
	}

	if providerCfg, cfgErr := h.config.GetProviderConfigRaw(provider); cfgErr == nil &&
		providerCfg.OpenAIConfig != nil && providerCfg.OpenAIConfig.DisableStore {
		event.Store = schemas.Ptr(false)
	} else {
		event.Store = schemas.Ptr(true)
	}

	bifrostReq, err := h.convertEventToRequest(&event)
	if err != nil {
		writeWSError(session, 400, "invalid_request_error", err.Error())
		return
	}

	// Extract extra params (unknown fields) and forward them, matching the HTTP path behavior
	extraParams, extractErr := extractExtraParams(message, wsResponsesKnownFields)
	if extractErr == nil && len(extraParams) > 0 {
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.ResponsesParameters{}
		}
		bifrostReq.Params.ExtraParams = extraParams
	}

	bifrostCtx, cancel := createBifrostContextFromAuth(h.handlerStore, auth)
	if bifrostCtx == nil {
		writeWSError(session, 500, "server_error", "failed to create request context")
		return
	}
	if parentRequestID, _ := bifrostCtx.Value(schemas.BifrostContextKeyParentRequestID).(string); parentRequestID == "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyParentRequestID, session.ID())
	}

	// Rewrite the raw event for upstream: strip provider/ prefix from model,
	// apply store override. The original bytes contain the bifrost-format model
	// (e.g. "openai/gpt-5.5") which upstream providers don't understand.
	upstreamEvent, rewriteErr := rewriteUpstreamEvent(message, bifrostReq.Model, event.Store)
	if rewriteErr != nil {
		logger.Warn("failed to rewrite upstream event: %v, using original", rewriteErr)
		upstreamEvent = message
	}

	// Try native WS upstream first
	if h.tryNativeWSUpstream(session, bifrostCtx, bifrostReq, upstreamEvent) {
		cancel()
		return
	}

	// Fall back to HTTP bridge
	h.executeHTTPBridge(session, bifrostCtx, cancel, bifrostReq)
}

// tryNativeWSUpstream attempts to forward the event to a native WS upstream connection.
// Returns true if the event was handled (successfully or with error sent to client).
// Returns false if the provider doesn't support WS and we should fall back to HTTP bridge.
func (h *WSResponsesHandler) tryNativeWSUpstream(
	session *bfws.Session,
	ctx *schemas.BifrostContext,
	req *schemas.BifrostResponsesRequest,
	rawEvent []byte,
) bool {
	provider := h.client.GetProviderByKey(req.Provider)
	if provider == nil {
		return false
	}

	wsProvider, ok := provider.(schemas.WebSocketCapableProvider)
	if !ok || !wsProvider.SupportsWebSocketMode() {
		return false
	}

	key, err := h.client.SelectKeyForProviderRequestType(ctx, schemas.WebSocketResponsesRequest, req.Provider, req.Model)
	if err != nil {
		writeWSError(session, 400, "invalid_request_error", err.Error())
		return true
	}

	wsURL := wsProvider.WebSocketResponsesURL(key)
	upstream := session.Upstream()
	upstreamFromPool := true

	// Validate the pinned upstream matches the current request's provider/key.
	hasForwardedHeaders := hasWebSocketForwardedHeaders(ctx)
	if upstream != nil && !upstream.IsClosed() &&
		(upstream.Provider() != req.Provider || upstream.KeyID() != key.ID || hasForwardedHeaders) {
		h.pool.Discard(upstream)
		session.SetUpstream(nil)
		upstream = nil
	}

	// If request-scoped headers are present, do not use the shared pool: pooling
	// those dials either leaks metadata across clients or explodes pool key cardinality.
	if hasForwardedHeaders {
		headers := mergeWebSocketHeaders(ctx, wsProvider.WebSocketHeaders(key))
		upstream, err = bfws.DialUpstream(wsURL, headers, req.Provider, key.ID)
		if err != nil {
			logger.Warn("failed to dial upstream WS connection for %s with forwarded headers: %v, falling back to HTTP bridge", req.Provider, err)
			return false
		}
		upstreamFromPool = false
		defer upstream.Close()
	} else if upstream == nil || upstream.IsClosed() {
		poolKey := bfws.PoolKey{
			Provider: req.Provider,
			KeyID:    key.ID,
			Endpoint: wsURL,
		}

		headers := mergeWebSocketHeaders(ctx, wsProvider.WebSocketHeaders(key))

		upstream, err = h.pool.Get(poolKey, headers)
		if err != nil {
			logger.Warn("failed to get upstream WS connection for %s: %v, falling back to HTTP bridge", req.Provider, err)
			return false
		}
		session.SetUpstream(upstream)
	}

	closeUpstream := func() {
		if upstreamFromPool {
			h.pool.Discard(upstream)
		} else if upstream != nil {
			upstream.Close()
		}
		session.SetUpstream(nil)
	}

	// Run plugin pre-hooks before forwarding to upstream
	bifrostReq := &schemas.BifrostRequest{
		RequestType:      schemas.WebSocketResponsesRequest,
		ResponsesRequest: req,
	}

	hooks, preErr := h.client.RunStreamPreHooks(ctx, bifrostReq)
	if preErr != nil {
		writeWSBifrostError(session, preErr)
		return true
	}
	defer hooks.Cleanup()

	// If a plugin short-circuited with a cached response, write it and skip upstream
	if hooks.ShortCircuitResponse != nil {
		writeWSShortCircuitResponse(session, hooks.ShortCircuitResponse)
		return true
	}

	finalizeTerminalPostHooks := func(bifrostErr *schemas.BifrostError) {
		if bifrostErr == nil {
			return
		}
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		if _, postErr := hooks.PostHookRunner(ctx, nil, bifrostErr); postErr != nil {
			logger.Warn("failed to finalize WS post-hooks for %s: %v", req.Provider, postErr)
		}
	}

	// Forward the raw event to upstream
	if err := upstream.WriteMessage(ws.TextMessage, rawEvent); err != nil {
		logger.Warn("upstream WS write failed for %s: %v, falling back to HTTP bridge", req.Provider, err)
		closeUpstream()
		return false
	}

	// Retrieve tracer and traceID for chunk accumulation
	tracer, _ := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
	traceID, _ := ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	streamIdleTimeout := resolveWSStreamIdleTimeout(h.config, req.Provider)

	// Read response events from upstream and relay to client, running post-hooks per chunk
	forwardedAny := false
	for {
		if err := upstream.SetReadDeadline(time.Now().Add(streamIdleTimeout)); err != nil {
			// Fail closed: if we can't arm the idle timeout, don't risk hanging forever.
			logger.Warn("failed to set upstream WS read deadline for %s: %v, treating as terminal", req.Provider, err)
			closeUpstream()
			finalizeTerminalPostHooks(newBifrostError(502, "upstream_connection_error", "failed to arm upstream read deadline"))
			writeWSError(session, 502, "upstream_connection_error", "upstream websocket connection error")
			return true
		}

		msgType, data, readErr := upstream.ReadMessage()
		if readErr != nil {
			if isWSReadTimeout(readErr) {
				logger.Warn("upstream WS idle timeout for %s after %s", req.Provider, streamIdleTimeout)
				closeUpstream()
				finalizeTerminalPostHooks(newBifrostError(504, "upstream_timeout", "upstream websocket stream timed out"))
				writeWSError(session, 504, "upstream_timeout", "upstream websocket stream timed out")
				return true
			}

			logger.Warn("upstream WS read failed for %s: %v, falling back to HTTP bridge", req.Provider, readErr)
			closeUpstream()
			if !forwardedAny {
				return false
			}
			finalizeTerminalPostHooks(newBifrostError(502, "upstream_connection_error", "upstream websocket stream interrupted"))
			writeWSError(session, 502, "upstream_connection_error", "upstream websocket stream interrupted")
			return true
		}

		if err := upstream.SetReadDeadline(time.Time{}); err != nil {
			logger.Warn("failed to clear upstream WS read deadline for %s: %v", req.Provider, err)
		}

		streamResp := parseUpstreamWSEvent(data, req.Provider, req.Model)
		isTerminal := streamResp != nil && isTerminalStreamType(streamResp.Type)

		if isTerminal {
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		}

		if streamResp != nil {
			resp := &schemas.BifrostResponse{ResponsesStreamResponse: streamResp}

			if tracer != nil && traceID != "" {
				tracer.AddStreamingChunk(traceID, resp)
			}

			_, postErr := hooks.PostHookRunner(ctx, resp, nil)
			if postErr != nil {
				closeUpstream()
				writeWSBifrostError(session, postErr)
				return true
			}
		}

		if writeErr := session.WriteMessage(msgType, data); writeErr != nil {
			closeUpstream()
			// Only finalize post-hooks if they haven't already run for this chunk.
			// When isTerminal && streamResp != nil, PostHookRunner already ran above (line 366),
			// so calling finalizeTerminalPostHooks again would double-fire the end-of-stream signal.
			if streamResp == nil || !isTerminal {
				finalizeTerminalPostHooks(newBifrostError(499, "client_connection_error", "client websocket connection interrupted"))
			}
			return true
		}
		forwardedAny = true

		if isTerminal {
			session.MarkResponsesTurnCompleted()
			h.trackResponseID(session, data)
			return true
		}
	}
}

func resolveWSStreamIdleTimeout(config *lib.Config, provider schemas.ModelProvider) time.Duration {
	idleTimeoutSeconds := schemas.DefaultStreamIdleTimeoutInSeconds
	if config != nil {
		if providerCfg, err := config.GetProviderConfigRaw(provider); err == nil && providerCfg != nil &&
			providerCfg.NetworkConfig != nil && providerCfg.NetworkConfig.StreamIdleTimeoutInSeconds > 0 {
			idleTimeoutSeconds = providerCfg.NetworkConfig.StreamIdleTimeoutInSeconds
		}
	}
	if idleTimeoutSeconds <= 0 {
		idleTimeoutSeconds = schemas.DefaultStreamIdleTimeoutInSeconds
	}
	return time.Duration(idleTimeoutSeconds) * time.Second
}

func isWSReadTimeout(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isExpectedResponsesClientClose(err error, session *bfws.Session) bool {
	if err == nil {
		return true
	}
	if !ws.IsUnexpectedCloseError(err, ws.CloseGoingAway, ws.CloseNormalClosure) {
		return true
	}
	if session != nil && session.HasCompletedResponsesTurn() && isResponsesEOFClose(err) {
		return true
	}
	return false
}

func isResponsesEOFClose(err error) bool {
	var closeErr *ws.CloseError
	if !errors.As(err, &closeErr) {
		return false
	}
	return closeErr.Code == ws.CloseAbnormalClosure || closeErr.Code == ws.CloseNoStatusReceived
}

func newBifrostError(statusCode int, errType, message string) *schemas.BifrostError {
	return &schemas.BifrostError{
		StatusCode: schemas.Ptr(statusCode),
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr(errType),
			Message: message,
		},
	}
}

// writeWSShortCircuitResponse writes a short-circuited plugin response as WS events.
func writeWSShortCircuitResponse(session *bfws.Session, resp *schemas.BifrostResponse) {
	if resp.ResponsesResponse != nil {
		data, err := sonic.Marshal(resp.ResponsesResponse)
		if err != nil {
			return
		}
		if err := session.WriteMessage(ws.TextMessage, data); err != nil {
			return
		}
		if resp.ResponsesResponse.ID != nil && *resp.ResponsesResponse.ID != "" {
			session.SetLastResponseID(*resp.ResponsesResponse.ID)
		}
	} else if resp.ResponsesStreamResponse != nil {
		data, err := sonic.Marshal(resp.ResponsesStreamResponse)
		if err != nil {
			return
		}
		session.WriteMessage(ws.TextMessage, data)
	}
}

// parseUpstreamWSEvent attempts to parse a raw upstream WS event into a BifrostResponsesStreamResponse.
// It populates ExtraFields so downstream plugins (logging, tracing) can identify the request type.
// Returns nil if the data cannot be parsed (non-fatal, the raw bytes are still relayed).
func parseUpstreamWSEvent(data []byte, provider schemas.ModelProvider, model string) *schemas.BifrostResponsesStreamResponse {
	var streamResp schemas.BifrostResponsesStreamResponse
	if err := sonic.Unmarshal(data, &streamResp); err != nil {
		return nil
	}
	if streamResp.Type == "" {
		return nil
	}
	streamResp.ExtraFields.RequestType = schemas.ResponsesStreamRequest
	streamResp.ExtraFields.Provider = provider
	streamResp.ExtraFields.OriginalModelRequested = model
	// Use SequenceNumber as ChunkIndex so each event gets a unique index.
	// Without this, all chunks default to ChunkIndex=0 and the accumulator's
	// dedup (ResponsesChunksSeen) drops every chunk after the first — losing
	// output text, token usage, and cost data from the logs.
	streamResp.ExtraFields.ChunkIndex = streamResp.SequenceNumber
	return &streamResp
}

// isTerminalStreamType returns true if the event type signals the end of a response stream.
func isTerminalStreamType(t schemas.ResponsesStreamResponseType) bool {
	switch t {
	case schemas.ResponsesStreamResponseTypeCompleted,
		schemas.ResponsesStreamResponseTypeFailed,
		schemas.ResponsesStreamResponseTypeIncomplete,
		schemas.ResponsesStreamResponseTypeError:
		return true
	}
	return false
}

// trackResponseID extracts and stores the response ID from terminal events.
func (h *WSResponsesHandler) trackResponseID(session *bfws.Session, data []byte) {
	var envelope struct {
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if err := sonic.Unmarshal(data, &envelope); err == nil && envelope.Response.ID != "" {
		session.SetLastResponseID(envelope.Response.ID)
	}
}

// convertEventToRequest converts a WebSocket response.create event to a BifrostResponsesRequest.
func (h *WSResponsesHandler) convertEventToRequest(event *schemas.WebSocketResponsesEvent) (*schemas.BifrostResponsesRequest, error) {
	provider, modelName := schemas.ParseModelString(event.Model, schemas.OpenAI)
	if provider == "" || modelName == "" {
		return nil, errModelFormat
	}

	var input []schemas.ResponsesMessage
	if event.Input != nil {
		// Try parsing as array first
		if err := sonic.Unmarshal(event.Input, &input); err != nil {
			// Try as string
			var inputStr string
			if strErr := sonic.Unmarshal(event.Input, &inputStr); strErr != nil {
				return nil, errInputRequired
			}
			input = []schemas.ResponsesMessage{
				{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: &inputStr},
				},
			}
		}
	}
	if len(input) == 0 {
		return nil, errInputRequired
	}

	params := &schemas.ResponsesParameters{}
	if event.Temperature != nil {
		params.Temperature = event.Temperature
	}
	if event.TopP != nil {
		params.TopP = event.TopP
	}
	if event.MaxOutputTokens != nil {
		params.MaxOutputTokens = event.MaxOutputTokens
	}
	if event.Instructions != "" {
		params.Instructions = &event.Instructions
	}
	if event.PreviousResponseID != "" {
		params.PreviousResponseID = &event.PreviousResponseID
	}
	if event.Store != nil {
		params.Store = event.Store
	}
	if event.Tools != nil {
		var tools []schemas.ResponsesTool
		if err := sonic.Unmarshal(event.Tools, &tools); err == nil {
			params.Tools = tools
		}
	}
	if event.ToolChoice != nil {
		var tc schemas.ResponsesToolChoice
		if err := sonic.Unmarshal(event.ToolChoice, &tc); err == nil {
			params.ToolChoice = &tc
		}
	}
	if event.Reasoning != nil {
		var reasoning schemas.ResponsesParametersReasoning
		if err := sonic.Unmarshal(event.Reasoning, &reasoning); err == nil {
			params.Reasoning = &reasoning
		}
	}
	if event.Text != nil {
		var text schemas.ResponsesTextConfig
		if err := sonic.Unmarshal(event.Text, &text); err == nil {
			params.Text = &text
		}
	}
	if event.Metadata != nil {
		var metadata map[string]any
		if err := sonic.Unmarshal(event.Metadata, &metadata); err == nil {
			params.Metadata = &metadata
		}
	}
	if event.Truncation != "" {
		params.Truncation = &event.Truncation
	}

	return &schemas.BifrostResponsesRequest{
		Provider: schemas.ModelProvider(provider),
		Model:    modelName,
		Input:    input,
		Params:   params,
	}, nil
}

// createBifrostContextFromAuth builds a BifrostContext from the auth headers captured during upgrade.
func createBifrostContextFromAuth(handlerStore lib.HandlerStore, auth *authHeaders) (*schemas.BifrostContext, context.CancelFunc) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	if auth == nil {
		return ctx, cancel
	}

	if sessionID := lib.ParseSessionIDFromBaggage(auth.baggage); sessionID != "" {
		ctx.SetValue(schemas.BifrostContextKeyParentRequestID, sessionID)
	}

	if auth.virtualKey != "" {
		ctx.SetValue(schemas.BifrostContextKeyVirtualKey, auth.virtualKey)
	}

	// Handle Bearer token with sk-bf- prefix (virtual key via Authorization header)
	if auth.authorization != "" {
		if strings.HasPrefix(auth.authorization, "Bearer ") {
			token := strings.TrimPrefix(auth.authorization, "Bearer ")
			if strings.HasPrefix(token, "sk-bf-") {
				ctx.SetValue(schemas.BifrostContextKeyVirtualKey, token)
			} else if handlerStore != nil && handlerStore.ShouldAllowDirectKeys() {
				key := schemas.Key{
					ID:     "header-provided",
					Value:  *schemas.NewEnvVar(token),
					Models: schemas.WhiteList{"*"},
					Weight: 1.0,
				}
				ctx.SetValue(schemas.BifrostContextKeyDirectKey, key)
			}
		}
	}
	if auth.apiKey != "" {
		if strings.HasPrefix(auth.apiKey, "sk-bf-") {
			ctx.SetValue(schemas.BifrostContextKeyVirtualKey, auth.apiKey)
		} else if handlerStore != nil && handlerStore.ShouldAllowDirectKeys() {
			key := schemas.Key{
				ID:     "header-provided",
				Value:  *schemas.NewEnvVar(auth.apiKey),
				Models: schemas.WhiteList{"*"},
				Weight: 1.0,
			}
			ctx.SetValue(schemas.BifrostContextKeyDirectKey, key)
		}
	}
	if auth.googAPIKey != "" {
		if strings.HasPrefix(auth.googAPIKey, "sk-bf-") {
			ctx.SetValue(schemas.BifrostContextKeyVirtualKey, auth.googAPIKey)
		} else if handlerStore != nil && handlerStore.ShouldAllowDirectKeys() {
			key := schemas.Key{
				ID:     "header-provided",
				Value:  *schemas.NewEnvVar(auth.googAPIKey),
				Models: schemas.WhiteList{"*"},
				Weight: 1.0,
			}
			ctx.SetValue(schemas.BifrostContextKeyDirectKey, key)
		}
	}

	// Forward x-bf-* headers
	matcher := (*lib.HeaderMatcher)(nil)
	if handlerStore != nil {
		matcher = handlerStore.GetHeaderMatcher()
	}
	extraHeaders := make(map[string][]string)
	for k, values := range auth.headers {
		for _, v := range values {
			switch {
			case k == "x-bf-vk":
				// Already handled above
			case k == "x-bf-api-key":
				ctx.SetValue(schemas.BifrostContextKeyAPIKeyName, v)
			case strings.HasPrefix(k, "x-bf-eh-"):
				addForwardedHeader(extraHeaders, matcher, strings.TrimPrefix(k, "x-bf-eh-"), v)
			case matcher != nil && matcher.HasAllowlist() && matcher.MatchesAllow(k):
				if !strings.HasPrefix(k, "x-bf-") && !isSecurityDeniedExtraHeader(k) && !matcher.MatchesDeny(k) {
					extraHeaders[k] = append(extraHeaders[k], v)
				}
			}
		}
	}
	if len(extraHeaders) > 0 {
		ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, extraHeaders)
	}

	return ctx, cancel
}

func addForwardedHeader(extraHeaders map[string][]string, matcher *lib.HeaderMatcher, name string, value string) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || isSecurityDeniedExtraHeader(name) {
		return
	}
	if matcher != nil && !matcher.ShouldAllow(name) {
		return
	}
	extraHeaders[name] = append(extraHeaders[name], value)
}

func isSecurityDeniedExtraHeader(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(name, "sec-websocket-") {
		return true
	}
	switch name {
	case "authorization", "proxy-authorization", "cookie", "host", "content-length", "connection", "transfer-encoding",
		"upgrade", "origin", "x-api-key", "x-goog-api-key", "x-bf-api-key", "x-bf-api-key-id", "x-bf-vk":
		return true
	default:
		return false
	}
}

func mergeWebSocketHeaders(ctx *schemas.BifrostContext, providerHeaders map[string]string) http.Header {
	merged := http.Header{}
	for key, value := range providerHeaders {
		merged.Set(key, value)
	}
	if extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
		for key, values := range extraHeaders {
			if len(values) == 0 || isSecurityDeniedExtraHeader(key) {
				continue
			}
			merged.Del(key)
			for _, value := range values {
				merged.Add(key, value)
			}
		}
	}
	return merged
}

func hasWebSocketForwardedHeaders(ctx *schemas.BifrostContext) bool {
	extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	if !ok {
		return false
	}
	for key, values := range extraHeaders {
		if len(values) > 0 && !isSecurityDeniedExtraHeader(key) {
			return true
		}
	}
	return false
}

// executeHTTPBridge runs the response through the existing streaming inference pipeline.
func (h *WSResponsesHandler) executeHTTPBridge(
	session *bfws.Session,
	ctx *schemas.BifrostContext,
	cancel context.CancelFunc,
	req *schemas.BifrostResponsesRequest,
) {
	defer cancel()
	// Flush the trace after the stream completes so the log entry is persisted
	// to the log store. Without this, the trace is created by tryStreamRequest
	// but never flushed — logs won't appear in the dashboard.
	defer completeTrace(ctx)

	stream, bifrostErr := h.client.ResponsesStreamRequest(ctx, req)
	if bifrostErr != nil {
		writeWSBifrostError(session, bifrostErr)
		return
	}

	// Relay streaming chunks as WS messages
	for chunk := range stream {
		if chunk == nil {
			continue
		}

		chunkJSON, err := sonic.Marshal(chunk)
		if err != nil {
			logger.Warn("failed to marshal stream chunk: %v", err)
			continue
		}

		if writeErr := session.WriteMessage(ws.TextMessage, chunkJSON); writeErr != nil {
			return
		}

		// Track terminal responses for session chaining and close classification.
		if chunk.BifrostResponsesStreamResponse != nil &&
			isTerminalStreamType(chunk.BifrostResponsesStreamResponse.Type) {
			session.MarkResponsesTurnCompleted()
			if chunk.BifrostResponsesStreamResponse.Response != nil &&
				chunk.BifrostResponsesStreamResponse.Response.ID != nil &&
				*chunk.BifrostResponsesStreamResponse.Response.ID != "" {
				session.SetLastResponseID(*chunk.BifrostResponsesStreamResponse.Response.ID)
			}
		}
	}
}

// writeWSError sends a JSON error event to a WebSocket write target.
// Accepts either a raw *ws.Conn (pre-session) or a *bfws.Session (mutex-protected).
func writeWSError(w wsWriter, status int, code, message string) {
	event := schemas.WebSocketErrorEvent{
		Type:   schemas.WSEventError,
		Status: status,
		Error: &schemas.WebSocketErrorBody{
			Code:    code,
			Message: message,
		},
	}
	data, err := sonic.Marshal(event)
	if err != nil {
		return
	}
	w.WriteMessage(ws.TextMessage, data)
}

// writeWSBifrostError converts a BifrostError to a WS error event.
func writeWSBifrostError(w wsWriter, bifrostErr *schemas.BifrostError) {
	status := 500
	if bifrostErr.StatusCode != nil && *bifrostErr.StatusCode > 0 {
		status = *bifrostErr.StatusCode
	}
	code := "server_error"
	msg := "internal server error"
	if bifrostErr.Error != nil {
		if bifrostErr.Error.Code != nil && *bifrostErr.Error.Code != "" {
			code = *bifrostErr.Error.Code
		} else if bifrostErr.Error.Type != nil && *bifrostErr.Error.Type != "" {
			code = *bifrostErr.Error.Type
		}
		if bifrostErr.Error.Message != "" {
			msg = bifrostErr.Error.Message
		}
	}
	writeWSError(w, status, code, msg)
}

// wsResponsesKnownFields lists the fields explicitly handled by WebSocketResponsesEvent.
// Anything not in this set is treated as an extra param and forwarded as-is to the provider.
var wsResponsesKnownFields = map[string]bool{
	"type":                 true,
	"model":                true,
	"store":                true,
	"input":                true,
	"instructions":         true,
	"previous_response_id": true,
	"tools":                true,
	"tool_choice":          true,
	"temperature":          true,
	"top_p":                true,
	"max_output_tokens":    true,
	"reasoning":            true,
	"metadata":             true,
	"text":                 true,
	"truncation":           true,
}

// rewriteUpstreamEvent modifies the raw JSON event for upstream consumption:
// - Replaces "model" with provider-native name (strips provider/ prefix)
// - Applies store override if modified
// Uses json.RawMessage to preserve all other field values exactly as-is.
// Re-marshaling via a map may reorder object fields; Responses JSON is
// order-insensitive, and this path only preserves semantics, not byte order.
func rewriteUpstreamEvent(rawEvent []byte, nativeModel string, store *bool) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := sonic.Unmarshal(rawEvent, &m); err != nil {
		return nil, err
	}

	modelBytes, err := sonic.Marshal(nativeModel)
	if err != nil {
		return nil, err
	}
	m["model"] = json.RawMessage(modelBytes)

	if store != nil {
		storeBytes, err := sonic.Marshal(*store)
		if err != nil {
			return nil, err
		}
		m["store"] = json.RawMessage(storeBytes)
	}

	return sonic.Marshal(m)
}

// completeTrace flushes the trace so the log entry is persisted to the log store.
func completeTrace(ctx *schemas.BifrostContext) {
	if ctx == nil {
		return
	}
	tracer, ok := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
	if !ok || tracer == nil {
		return
	}
	traceID, ok := ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	if !ok || traceID == "" {
		return
	}
	tracer.CompleteAndFlushTrace(traceID)
}

var (
	errModelFormat   = errorf("model should be in provider/model format")
	errInputRequired = errorf("input is required for responses")
)

func errorf(msg string) error {
	return &simpleError{msg: msg}
}

type simpleError struct {
	msg string
}

func (e *simpleError) Error() string {
	return e.msg
}
