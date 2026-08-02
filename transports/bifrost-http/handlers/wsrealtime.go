package handlers

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fasthttp/router"
	ws "github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
	"github.com/valyala/fasthttp"
)

const (
	realtimeWSPingInterval     = 15 * time.Second
	realtimeWSPongTimeout      = 45 * time.Second
	realtimeWSPingWriteTimeout = 10 * time.Second
	realtimeWSWriteTimeout     = 30 * time.Second
)

// WSRealtimeHandler handles bidirectional WebSocket proxying for the Realtime API.
type WSRealtimeHandler struct {
	client       *bifrost.Bifrost
	config       *lib.Config
	handlerStore lib.HandlerStore
	pool         *bfws.Pool
	sessions     *bfws.SessionManager
}

// NewWSRealtimeHandler creates a new Realtime WebSocket handler.
func NewWSRealtimeHandler(client *bifrost.Bifrost, config *lib.Config, pool *bfws.Pool) *WSRealtimeHandler {
	maxConns := config.WebSocketConfig.MaxConnections

	return &WSRealtimeHandler{
		client:       client,
		config:       config,
		handlerStore: config,
		pool:         pool,
		sessions:     bfws.NewSessionManager(maxConns),
	}
}

// RegisterRoutes registers the Realtime WebSocket endpoint at the base path and OpenAI integration paths.
func (h *WSRealtimeHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handler := lib.ChainMiddlewares(h.handleUpgrade, middlewares...)
	r.GET("/v1/realtime", handler)
	for _, path := range integrations.OpenAIRealtimePaths("/openai") {
		r.GET(path, handler)
	}
}

func (h *WSRealtimeHandler) Close() {
	if h == nil || h.sessions == nil {
		return
	}
	h.sessions.CloseAll()
}

func (h *WSRealtimeHandler) handleUpgrade(ctx *fasthttp.RequestCtx) {
	path := string(ctx.Path())
	modelParam := string(ctx.QueryArgs().Peek("model"))
	deploymentParam := string(ctx.QueryArgs().Peek("deployment"))
	auth := captureAuthHeaders(ctx)
	// OpenAI's SDK sends the API key via WebSocket subprotocol: "openai-insecure-api-key.<key>".
	// Extract it into the auth headers so downstream processing recognizes it.
	if auth.authorization == "" {
		if token := extractRealtimeSubprotocolAPIKey(ctx); token != "" {
			auth.authorization = "Bearer " + token
		}
	}

	providerKey, model, err := resolveRealtimeTarget(ctx, h.config, path, modelParam, deploymentParam)
	if err != nil {
		upgrader := h.websocketUpgrader("")
		upgradeErr := upgrader.Upgrade(ctx, func(conn *ws.Conn) {
			defer conn.Close()
			clientConn := newRealtimeClientConn(conn)
			clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", err.Error()))
		})
		if upgradeErr != nil {
			logger.Warn("websocket upgrade failed for %s: %v", path, upgradeErr)
		}
		return
	}

	// Run PreRequestHook to give governance + LB a chance to route the realtime connection.
	// Realtime bypasses handleRequest (per-turn pipelines instead), so we invoke the routing
	// phase explicitly here. Mutations to provider/model are read back into the local vars
	// and copied to fasthttp user values so snapshotRealtimeMiddlewareValues picks up any
	// ctx changes (governance team/customer IDs, routing engine logs).
	preReqCtx, preReqCancel := createBifrostContextFromAuth(h.handlerStore, auth)
	if preReqCtx == nil {
		preReqCancel()
		upgrader := h.websocketUpgrader("")
		upgradeErr := upgrader.Upgrade(ctx, func(conn *ws.Conn) {
			defer conn.Close()
			clientConn := newRealtimeClientConn(conn)
			clientConn.writeRealtimeError(newRealtimeWireBifrostError(500, "server_error", "failed to create request context"))
		})
		if upgradeErr != nil {
			logger.Warn("websocket upgrade failed for %s: %v", path, upgradeErr)
		}
		return
	}
	preReqCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.RealtimeRequest)
	if realtimeDefaultProviderForPath(path) == schemas.OpenAI {
		preReqCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	}
	// Surface full request headers + query params on the pre-request context so governance
	// CEL routing rules (which read headers[...] / params[...]) see the same shape they would
	// for normal HTTP requests. Mirrors lib/ctx.go ConvertToBifrostContext; the normal HTTP
	// path doesn't run for WS upgrades, so we populate these explicitly. Keys are lowercased.
	allHeaders := make(map[string]string)
	ctx.Request.Header.All()(func(key, value []byte) bool {
		allHeaders[strings.ToLower(string(key))] = string(value)
		return true
	})
	preReqCtx.SetValue(schemas.BifrostContextKeyRequestHeaders, allHeaders)
	if queryArgs := ctx.Request.URI().QueryArgs(); queryArgs.Len() > 0 {
		allQuery := make(map[string]string, queryArgs.Len())
		queryArgs.All()(func(key, value []byte) bool {
			allQuery[strings.ToLower(string(key))] = string(value)
			return true
		})
		preReqCtx.SetValue(schemas.BifrostContextKeyRequestQuery, allQuery)
	}
	preReq := &schemas.BifrostRequest{
		RequestType: schemas.RealtimeRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: providerKey,
			Model:    model,
		},
	}
	h.client.RunPreRequestHooks(preReqCtx, preReq)
	routedProvider, routedModel, _ := preReq.GetRequestFields()
	if routedProvider == "" {
		// Mirror the empty-provider check in core handleRequest. No routing layer
		// (governance routing rules / LB / modelcatalogresolver) could pick a provider
		// for this model — caller's input is unresolvable.
		upgrader := h.websocketUpgrader("")
		upgradeErr := upgrader.Upgrade(ctx, func(conn *ws.Conn) {
			defer conn.Close()
			clientConn := newRealtimeClientConn(conn)
			clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", fmt.Sprintf("no provider could be resolved for model %q (set as provider/model or configure the model catalog)", model)))
		})
		if upgradeErr != nil {
			logger.Warn("websocket upgrade failed for %s: %v", path, upgradeErr)
		}
		preReqCancel()
		return
	}
	providerKey = routedProvider
	if routedModel != "" {
		model = routedModel
	}
	// Mirror ctx values back to fasthttp user values so snapshotRealtimeMiddlewareValues
	// (called below) picks them up — same mechanism TransportInterceptorMiddleware uses.
	for k, v := range preReqCtx.GetUserValues() {
		ctx.SetUserValue(k, v)
	}
	preReqCancel()

	provider := h.client.GetProviderByKey(providerKey)
	rtProvider, ok := provider.(schemas.RealtimeProvider)
	if provider == nil || !ok || !rtProvider.SupportsRealtimeAPI() {
		upgrader := h.websocketUpgrader("")
		upgradeErr := upgrader.Upgrade(ctx, func(conn *ws.Conn) {
			defer conn.Close()
			clientConn := newRealtimeClientConn(conn)
			clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", "provider does not support realtime: "+string(providerKey)))
		})
		if upgradeErr != nil {
			logger.Warn("websocket upgrade failed for %s: %v", path, upgradeErr)
		}
		return
	}

	// Capture governance/routing values set by the transport middleware.
	// TransportInterceptorMiddleware copies BifrostContext user values to individual
	// fasthttp UserValue slots after HTTPTransportPreHook runs. We snapshot them now
	// because the fasthttp RequestCtx is recycled after the handler returns — the
	// WebSocket session outlives it.
	middlewareContextValues := snapshotRealtimeMiddlewareValues(ctx)

	upgrader := h.websocketUpgrader(rtProvider.RealtimeWebSocketSubprotocol())
	err = upgrader.Upgrade(ctx, func(conn *ws.Conn) {
		defer conn.Close()
		clientConn := newRealtimeClientConn(conn)

		session, sessionErr := h.sessions.Create(conn)
		if sessionErr != nil {
			clientConn.writeRealtimeError(newRealtimeWireBifrostError(429, "rate_limit_exceeded", sessionErr.Error()))
			return
		}
		defer h.sessions.Remove(conn)

		h.runRealtimeSession(clientConn, session, auth, path, providerKey, model, middlewareContextValues)
	})
	if err != nil {
		logger.Warn("websocket upgrade failed for %s: %v", path, err)
	}
}

func (h *WSRealtimeHandler) websocketUpgrader(subprotocol string) ws.FastHTTPUpgrader {
	upgrader := ws.FastHTTPUpgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
			origin := string(ctx.Request.Header.Peek("Origin"))
			if origin == "" {
				return true
			}
			return IsOriginAllowed(origin, h.config.ClientConfig.AllowedOrigins)
		},
	}
	if strings.TrimSpace(subprotocol) != "" {
		upgrader.Subprotocols = []string{subprotocol}
	}
	return upgrader
}

func (h *WSRealtimeHandler) runRealtimeSession(
	clientConn *realtimeClientConn,
	session *bfws.Session,
	auth *authHeaders,
	path string,
	providerKey schemas.ModelProvider,
	model string,
	middlewareValues map[any]any,
) {
	clientConn.startHeartbeat()
	defer clientConn.stopHeartbeat()

	bifrostCtx, cancel := createBifrostContextFromAuth(h.handlerStore, auth)
	if bifrostCtx == nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(500, "server_error", "failed to create request context"))
		return
	}
	defer cancel()

	// Restore governance and routing values from the transport middleware context.
	// These include routing rule ID/name, virtual key ID/name, routing engines,
	// routing engine logs, raw-storage header overrides, and other values set by
	// HTTPTransportPreHook plugins (governance, prompts, etc.).
	applyRealtimeMiddlewareValues(bifrostCtx, middlewareValues)

	// Resolve ephemeral key mapping to restore virtual key context.
	token := extractRealtimeBearerTokenFromHeader(auth.authorization)
	if isRealtimeEphemeralToken(token) {
		mapping, ok := lookupRealtimeEphemeralKeyMapping(h.handlerStore.GetKVStore(), token)
		if ok {
			applyRealtimeEphemeralKeyMapping(bifrostCtx, mapping)
		}
	}

	bifrostCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.RealtimeRequest)
	if strings.HasPrefix(path, "/openai") {
		bifrostCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	}

	provider := h.client.GetProviderByKey(providerKey)
	if provider == nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", "provider not found: "+string(providerKey)))
		return
	}

	rtProvider, ok := provider.(schemas.RealtimeProvider)
	if !ok || !rtProvider.SupportsRealtimeAPI() {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", "provider does not support realtime: "+string(providerKey)))
		return
	}

	key, err := h.client.SelectKeyForProviderRequestType(bifrostCtx, schemas.RealtimeRequest, providerKey, model)
	if err != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", err.Error()))
		return
	}

	// Resolve model alias so the provider receives the actual model identifier.
	model = key.Aliases.Resolve(model)

	// Compute raw storage flag from provider config + per-request header overrides.
	// Normal inference computes this inside bifrost.executeRequest, which is bypassed
	// for realtime WebSocket connections. Setting it on the session context ensures
	// turn-level hooks can read it via shouldStoreRealtimeRawPayloads().
	applyRealtimeRawStorageContext(bifrostCtx, h.client.ComputeRawStorageForProvider(bifrostCtx, providerKey))

	// Tag the session context with transport type for downstream logging/metadata.
	bifrostCtx.SetValue(schemas.BifrostContextKeyRealtimeTransport, "websocket")

	wsURL := rtProvider.RealtimeWebSocketURL(key, model)
	realtimeHeaders, headerErr := rtProvider.RealtimeHeaders(bifrostCtx, key)
	if headerErr != nil {
		clientConn.writeRealtimeError(headerErr)
		return
	}
	upstream, err := h.pool.Get(bfws.PoolKey{
		Provider: providerKey,
		KeyID:    key.ID,
		Endpoint: wsURL,
	}, mapToHTTPHeader(realtimeHeaders))
	if err != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(502, "server_error", err.Error()))
		return
	}
	defer h.pool.Discard(upstream)

	errCh := make(chan error, 2)
	go func() {
		errCh <- h.relayClientToRealtimeProvider(clientConn, session, upstream, rtProvider, bifrostCtx, providerKey, model, key)
	}()
	go func() {
		errCh <- h.relayRealtimeProviderToClient(clientConn, session, upstream, rtProvider, bifrostCtx, providerKey, model, key)
	}()

	firstErr := <-errCh
	_ = upstream.Close()
	_ = clientConn.Close()
	secondErr := <-errCh

	if logErr := selectRealtimeRelayError(firstErr, secondErr); logErr != nil {
		logger.Warn("realtime websocket relay ended for %s/%s on %s: %v", providerKey, model, path, logErr)
	}
}

func (h *WSRealtimeHandler) relayClientToRealtimeProvider(
	clientConn *realtimeClientConn,
	session *bfws.Session,
	upstream *bfws.UpstreamConn,
	provider schemas.RealtimeProvider,
	bifrostCtx *schemas.BifrostContext,
	providerKey schemas.ModelProvider,
	model string,
	key schemas.Key,
) error {
	for {
		messageType, message, err := clientConn.ReadMessage()
		if err != nil {
			finalizeRealtimeTurnHooksOnTransportError(
				h.client,
				bifrostCtx,
				session,
				providerKey,
				model,
				&key,
				499,
				"client_closed_request",
				"client realtime websocket disconnected before turn completed",
			)
			if isNormalWebSocketClosure(err) {
				return nil
			}
			return err
		}
		if messageType != ws.TextMessage {
			clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", "realtime websocket only accepts text messages"))
			return nil
		}

		event, err := schemas.ParseRealtimeEvent(message)
		if err != nil {
			clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", "failed to parse realtime event JSON"))
			continue
		}
		// Extract pending tool/input summaries but defer recording until the event
		// passes validation — rejected events must not pollute session state.
		toolItemID, toolSummary := pendingRealtimeToolOutputUpdate(event)
		inputItemID, inputSummary := pendingRealtimeInputUpdate(event)

		startsTurn := provider.ShouldStartRealtimeTurn(event)
		if startsTurn {
			if session.PeekRealtimeTurnHooks() != nil {
				clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", "Conversation already has an active response in progress."))
				continue
			}
			if toolSummary != "" {
				session.RecordRealtimeToolOutput(toolItemID, toolSummary, string(message))
			}
			if inputSummary != "" {
				session.RecordRealtimeInput(inputItemID, inputSummary, string(message))
			}
			if bifrostErr := startRealtimeTurnHooks(h.client, bifrostCtx, session, provider, providerKey, model, &key, event.Type); bifrostErr != nil {
				clientConn.writeRealtimeError(bifrostErr)
				return nil
			}
		}

		sanitizeRealtimeSessionEventForProvider(event)
		providerEvent, err := provider.ToProviderRealtimeEvent(event)
		if err != nil {
			if startsTurn {
				if finalizeErr := finalizeRealtimeTurnHooksWithError(
					h.client,
					bifrostCtx,
					session,
					providerKey,
					model,
					&key,
					schemas.RTEventError,
					nil,
					newRealtimeWireBifrostError(400, "invalid_request_error", err.Error()),
				); finalizeErr != nil {
					clientConn.writeRealtimeError(finalizeErr)
					return nil
				}
			}
			clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", err.Error()))
			continue
		}

		// Track session metadata only after provider translation succeeds. Rejected
		// session.update events must not affect later turn logs.
		updateRealtimeSessionFromEvent(session, event)

		// Record tool output / input only after the event passed validation.
		if !startsTurn {
			if toolSummary != "" {
				session.RecordRealtimeToolOutput(toolItemID, toolSummary, string(message))
			}
			if inputSummary != "" {
				session.RecordRealtimeInput(inputItemID, inputSummary, string(message))
			}
		}

		if err := upstream.WriteMessage(ws.TextMessage, providerEvent); err != nil {
			finalizeRealtimeTurnHooksWithError(
				h.client,
				bifrostCtx,
				session,
				providerKey,
				model,
				&key,
				schemas.RTEventError,
				nil,
				newRealtimeWireBifrostError(502, "server_error", "failed to write realtime event upstream"),
			)
			clientConn.writeRealtimeError(newRealtimeWireBifrostError(502, "server_error", "failed to write realtime event upstream"))
			return err
		}
	}
}

func (h *WSRealtimeHandler) relayRealtimeProviderToClient(
	clientConn *realtimeClientConn,
	session *bfws.Session,
	upstream *bfws.UpstreamConn,
	provider schemas.RealtimeProvider,
	bifrostCtx *schemas.BifrostContext,
	providerKey schemas.ModelProvider,
	model string,
	key schemas.Key,
) error {
	for {
		disconnectAfterWrite := false
		messageType, message, err := upstream.ReadMessage()
		if err != nil {
			finalizeRealtimeTurnHooksOnTransportError(
				h.client,
				bifrostCtx,
				session,
				providerKey,
				model,
				&key,
				502,
				"upstream_connection_error",
				"upstream realtime websocket closed before turn completed",
			)
			if isNormalWebSocketClosure(err) {
				return nil
			}
			finalizeRealtimeTurnHooksWithError(
				h.client,
				bifrostCtx,
				session,
				providerKey,
				model,
				&key,
				schemas.RTEventError,
				nil,
				newRealtimeWireBifrostError(502, "server_error", "upstream realtime websocket stream interrupted"),
			)
			clientConn.writeRealtimeError(newRealtimeWireBifrostError(502, "server_error", "upstream realtime websocket stream interrupted"))
			return err
		}

		if messageType == ws.TextMessage {
			event, err := provider.ToBifrostRealtimeEvent(message)
			if err != nil {
				finalizeRealtimeTurnHooksWithError(
					h.client,
					bifrostCtx,
					session,
					providerKey,
					model,
					&key,
					schemas.RTEventError,
					message,
					newRealtimeWireBifrostError(502, "server_error", "failed to translate upstream realtime event"),
				)
				clientConn.writeRealtimeError(newRealtimeWireBifrostError(502, "server_error", "failed to translate upstream realtime event"))
				return err
			}
			if event != nil {
				if event.Session != nil && event.Session.ID != "" {
					session.SetProviderSessionID(event.Session.ID)
				}
				// Track session tool definitions from session.created/session.updated.
				updateRealtimeSessionFromEvent(session, event)
				if event.Delta != nil && provider.ShouldAccumulateRealtimeOutput(event.Type) {
					session.AppendRealtimeOutputText(event.Delta.Text)
					session.AppendRealtimeOutputText(event.Delta.Transcript)
				}
				if provider.ShouldStartRealtimeTurn(event) && session.PeekRealtimeTurnHooks() == nil {
					if bifrostErr := startRealtimeTurnHooks(h.client, bifrostCtx, session, provider, providerKey, model, &key, event.Type); bifrostErr != nil {
						clientConn.writeRealtimeError(bifrostErr)
						return nil
					}
				}
			}
			if event != nil {
				inputItemID, inputSummary := pendingRealtimeInputUpdate(event)
				if !provider.ShouldForwardRealtimeEvent(event) {
					continue
				}
				if event.Type == provider.RealtimeTurnFinalEvent() {
					contentOverride := session.ConsumeRealtimeOutputText()
					if bifrostErr := finalizeRealtimeTurnHooks(h.client, bifrostCtx, session, provider, providerKey, model, &key, message, contentOverride); bifrostErr != nil {
						clientConn.writeRealtimeError(bifrostErr)
						return nil
					}
				} else if event.Error != nil {
					turnErr := newBifrostErrorFromRealtimeError(providerKey, model, message, event.Error)
					finalizeErr := finalizeRealtimeTurnHooksWithError(
						h.client,
						bifrostCtx,
						session,
						providerKey,
						model,
						&key,
						event.Type,
						message,
						turnErr,
					)
					if finalizeErr != nil {
						clientConn.writeRealtimeError(finalizeErr)
						return nil
					}
					// Defer the disconnect so the normal translated-write path
					// below still runs — otherwise terminal errors from translated
					// providers would reach the client in provider-native format.
					disconnectAfterWrite = shouldGracefullyDisconnectRealtime(turnErr)
				} else if inputSummary != "" {
					session.RecordRealtimeInput(inputItemID, inputSummary, string(message))
				}
				if len(event.RawData) == 0 {
					message, err = provider.ToProviderRealtimeEvent(event)
					if err != nil {
						clientConn.writeRealtimeError(newRealtimeWireBifrostError(502, "server_error", "failed to encode translated realtime event"))
						return err
					}
				}
			}
		}

		if err := clientConn.WriteMessage(messageType, message); err != nil {
			finalizeRealtimeTurnHooksOnTransportError(
				h.client,
				bifrostCtx,
				session,
				providerKey,
				model,
				&key,
				499,
				"client_closed_request",
				"client realtime websocket disconnected before turn completed",
			)
			if isNormalWebSocketClosure(err) {
				return nil
			}
			return err
		}
		if disconnectAfterWrite {
			return nil
		}
	}
}

func resolveRealtimeTarget(_ *fasthttp.RequestCtx, _ *lib.Config, path, modelParam, deploymentParam string) (schemas.ModelProvider, string, error) {
	defaultProvider := realtimeDefaultProviderForPath(path)

	var rawParam string
	switch {
	case strings.TrimSpace(modelParam) != "":
		rawParam = strings.TrimSpace(modelParam)
	case strings.TrimSpace(deploymentParam) != "":
		rawParam = strings.TrimSpace(deploymentParam)
	default:
		return "", "", errRealtimeModelRequired
	}

	provider, model := schemas.ParseModelString(rawParam, defaultProvider)
	if strings.TrimSpace(model) == "" {
		return "", "", errRealtimeModelFormat
	}

	// Provider may be empty here when no path-default applies and the model has
	// no explicit prefix. The modelcatalogresolver PreRequestHook will fill it in
	// (or surface a clear error if no provider matches) — no inline lookup needed.
	return provider, model, nil
}

func realtimeDefaultProviderForPath(path string) schemas.ModelProvider {
	if strings.HasPrefix(path, "/openai/") {
		return schemas.OpenAI
	}
	return ""
}

func isNormalWebSocketClosure(err error) bool {
	return ws.IsCloseError(err, ws.CloseNormalClosure, ws.CloseGoingAway, ws.CloseNoStatusReceived)
}

func isExpectedRealtimeRelayShutdown(err error) bool {
	if err == nil {
		return true
	}
	if isNormalWebSocketClosure(err) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	// Relay teardown closes the opposite socket after the first side exits, which can
	// surface as a plain network-close read error instead of a websocket close frame.
	return strings.Contains(err.Error(), "use of closed network connection")
}

func selectRealtimeRelayError(errs ...error) error {
	for _, err := range errs {
		if err != nil && !isExpectedRealtimeRelayShutdown(err) {
			return err
		}
	}
	return nil
}

var (
	errRealtimeModelRequired    = errorf("model or deployment query parameter is required for realtime websocket")
	errRealtimeModelFormat      = errorf("model query parameter must resolve to provider/model for realtime websocket")
	errRealtimeDeploymentFormat = errorf("deployment query parameter must resolve to provider/model for realtime websocket")
)

type realtimeClientConn struct {
	conn      *ws.Conn
	writeMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}
}

func newRealtimeClientConn(conn *ws.Conn) *realtimeClientConn {
	return &realtimeClientConn{
		conn: conn,
		done: make(chan struct{}),
	}
}

func (c *realtimeClientConn) ReadMessage() (messageType int, p []byte, err error) {
	messageType, p, err = c.conn.ReadMessage()
	if err == nil {
		c.refreshReadDeadline()
	}
	return messageType, p, err
}

func (c *realtimeClientConn) WriteMessage(messageType int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(realtimeWSWriteTimeout)); err != nil {
		return err
	}
	if err := c.conn.WriteMessage(messageType, data); err != nil {
		return err
	}
	return c.conn.SetWriteDeadline(time.Time{})
}

func (c *realtimeClientConn) startHeartbeat() {
	c.installPongHandler()
	c.refreshReadDeadline()

	go func() {
		ticker := time.NewTicker(realtimeWSPingInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := c.writePing(); err != nil {
					_ = c.Close()
					return
				}
			case <-c.done:
				return
			}
		}
	}()
}

func (c *realtimeClientConn) stopHeartbeat() {
	c.closeDone()
}

func (c *realtimeClientConn) installPongHandler() {
	c.conn.SetPongHandler(func(string) error {
		return c.refreshReadDeadline()
	})
}

func (c *realtimeClientConn) refreshReadDeadline() error {
	return c.conn.SetReadDeadline(time.Now().Add(realtimeWSPongTimeout))
}

func (c *realtimeClientConn) writePing() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(realtimeWSPingWriteTimeout)); err != nil {
		return err
	}
	if err := c.conn.WriteMessage(ws.PingMessage, nil); err != nil {
		return err
	}
	return c.conn.SetWriteDeadline(time.Time{})
}

func (c *realtimeClientConn) closeDone() {
	c.closeOnce.Do(func() {
		close(c.done)
	})
}

func (c *realtimeClientConn) writeRealtimeError(bifrostErr *schemas.BifrostError) {
	payload := newRealtimeTurnErrorEventPayload(bifrostErr)
	_ = c.WriteMessage(ws.TextMessage, payload)
}

func (c *realtimeClientConn) Close() error {
	c.closeDone()
	return c.conn.Close()
}

const realtimeSubprotocolAPIKeyPrefix = "openai-insecure-api-key."

// extractRealtimeSubprotocolAPIKey extracts an API key from the Sec-WebSocket-Protocol
// header. The OpenAI SDK sends: "realtime, openai-insecure-api-key.<key>".
func extractRealtimeSubprotocolAPIKey(ctx *fasthttp.RequestCtx) string {
	header := string(ctx.Request.Header.Peek("Sec-WebSocket-Protocol"))
	for _, proto := range strings.Split(header, ",") {
		proto = strings.TrimSpace(proto)
		if strings.HasPrefix(proto, realtimeSubprotocolAPIKeyPrefix) {
			return strings.TrimPrefix(proto, realtimeSubprotocolAPIKeyPrefix)
		}
	}
	return ""
}

func mapToHTTPHeader(headers map[string]string) http.Header {
	merged := http.Header{}
	for key, value := range headers {
		merged.Set(key, value)
	}
	return merged
}

func newRealtimeWireBifrostError(status int, code, message string) *schemas.BifrostError {
	errType := code
	return &schemas.BifrostError{
		StatusCode: &status,
		Type:       &errType,
		Error: &schemas.ErrorField{
			Type:    &errType,
			Code:    &errType,
			Message: message,
		},
	}
}

// applyRealtimeMiddlewareValues copies governance and routing values from the transport
// middleware BifrostContext (populated by HTTPTransportPreHook plugins) to the long-lived
// WebSocket session context. Without this, values set by the governance plugin during
// the HTTP upgrade (routing rule ID/name, VK ID/name, routing engines, routing engine
// logs, raw-storage overrides) would be lost because the WebSocket handler creates a
// fresh BifrostContext that outlives the fasthttp request.
//
// Values already explicitly set by createBifrostContextFromAuth (VK, parent request ID,
// request headers, extra headers) are preserved — middleware values do not overwrite them
// since createBifrostContextFromAuth runs first.
// realtimeMiddlewareKeys lists the BifrostContext keys that TransportInterceptorMiddleware
// copies from the governance plugin's context onto individual fasthttp UserValue slots.
// We snapshot exactly these keys before the WebSocket upgrade so the long-lived session
// has access to routing rule info, virtual key resolution, routing engine logs, etc.
var realtimeMiddlewareKeys = []any{
	schemas.BifrostContextKeyGovernanceVirtualKeyID,
	schemas.BifrostContextKeyGovernanceVirtualKeyName,
	schemas.BifrostContextKeyGovernanceRoutingRuleID,
	schemas.BifrostContextKeyGovernanceRoutingRuleName,
	schemas.BifrostContextKeyGovernanceCustomerID,
	schemas.BifrostContextKeyGovernanceCustomerName,
	schemas.BifrostContextKeyGovernanceTeamID,
	schemas.BifrostContextKeyGovernanceTeamName,
	schemas.BifrostContextKeyGovernanceBusinessUnitID,
	schemas.BifrostContextKeyGovernanceBusinessUnitName,
	schemas.BifrostContextKeyGovernanceIncludeOnlyKeys,
	schemas.BifrostContextKeyGovernancePluginName,
	schemas.BifrostContextKeyRoutingEnginesUsed,
	schemas.BifrostContextKeyRoutingEngineLogs,
	schemas.BifrostContextKeyShouldStoreRawInLogs,
	schemas.BifrostContextKeyCaptureRawRequest,
	schemas.BifrostContextKeyCaptureRawResponse,
	schemas.BifrostContextKeyDropRawRequestFromClient,
	schemas.BifrostContextKeyDropRawResponseFromClient,
	schemas.BifrostContextKeyUserID,
	schemas.BifrostContextKeyUserName,
	schemas.BifrostContextKeyAPIKeyID,
	schemas.BifrostContextKeyAPIKeyName,
	schemas.BifrostContextKeySelectedKeyID,
	schemas.BifrostContextKeySelectedKeyName,
	// NOTE: BifrostContextKeyTraceID is intentionally NOT inherited here. The
	// upgrade request's trace is already ended by the time realtime turns run, so
	// inheriting it would route each turn's log entry into pendingLogsToInject
	// under a dead trace ID whose Inject() never fires, dropping the row. Each
	// realtime turn mints its own trace in RunRealtimeTurnPreHooks instead.
	schemas.BifrostContextKeyTransportPluginLogs,
}

// snapshotRealtimeMiddlewareValues reads governance/routing values from the fasthttp
// context's UserValue store. TransportInterceptorMiddleware copies them there as
// individual key-value pairs (not inside a BifrostContext). Routing engine logs
// emitted by PreRequestHook (governance routing rules, LB, modelcatalogresolver)
// are surfaced through the same mechanism — the hooks write them onto preReqCtx
// and handleUpgrade mirrors that ctx's user values onto the fasthttp ctx before
// this function is called.
func snapshotRealtimeMiddlewareValues(ctx *fasthttp.RequestCtx) map[any]any {
	result := make(map[any]any)
	for _, key := range realtimeMiddlewareKeys {
		if value := ctx.UserValue(key); value != nil {
			result[key] = value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func applyRealtimeMiddlewareValues(ctx *schemas.BifrostContext, middlewareValues map[any]any) {
	if ctx == nil || len(middlewareValues) == 0 {
		return
	}
	for key, value := range middlewareValues {
		if value == nil {
			continue
		}
		// Skip values already set by createBifrostContextFromAuth to avoid overwriting
		// auth-resolved values with stale middleware copies.
		if existing := ctx.Value(key); existing != nil {
			continue
		}
		ctx.SetValue(key, value)
	}
}
