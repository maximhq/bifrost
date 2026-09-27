package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
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
	liveBootstrapTimeout  = 15 * time.Second
	liveBootstrapMaxBytes = 1 << 20
)

// WSLiveHandler relays GPT Live primary WebSocket sessions and bills them as they run.
type WSLiveHandler struct {
	gateway  *liveGateway
	config   *lib.Config
	pool     *bfws.Pool
	sessions *bfws.SessionManager
}

// NewWSLiveHandler creates a new GPT Live WebSocket handler.
func NewWSLiveHandler(client *bifrost.Bifrost, config *lib.Config, pool *bfws.Pool) *WSLiveHandler {
	return &WSLiveHandler{
		gateway:  &liveGateway{client: client, config: config, handlerStore: config},
		config:   config,
		pool:     pool,
		sessions: bfws.NewSessionManager(config.WebSocketConfig.MaxConnections),
	}
}

// RegisterRoutes registers the GPT Live primary and sideband endpoints at the base path and the
// OpenAI integration paths.
func (h *WSLiveHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	primary := lib.ChainMiddlewares(h.handleUpgrade, middlewares...)
	r.GET("/v1/live/sessions", primary)
	for _, path := range integrations.OpenAILivePaths("/openai") {
		r.GET(path, primary)
	}
	attach := lib.ChainMiddlewares(h.handleAttach, middlewares...)
	r.GET("/v1/live/sessions/{session_id}/attach", attach)
	for _, path := range integrations.OpenAILiveSessionPaths("/openai", "attach") {
		r.GET(path, attach)
	}
}

func (h *WSLiveHandler) Close() {
	if h == nil || h.sessions == nil {
		return
	}
	h.sessions.CloseAll()
}

// upgrade completes the WebSocket upgrade and reports whether it succeeded; serve then runs on
// the connection once this handler returns.
func (h *WSLiveHandler) upgrade(ctx *fasthttp.RequestCtx, up *liveRequest, serve func(clientConn *realtimeClientConn)) bool {
	upgrader := ws.FastHTTPUpgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
			origin := string(ctx.Request.Header.Peek("Origin"))
			return origin == "" || IsOriginAllowed(origin, h.config.ClientConfig.AllowedOrigins)
		},
	}
	err := upgrader.Upgrade(ctx, func(conn *ws.Conn) {
		defer conn.Close()
		defer up.cancel()
		serve(newRealtimeClientConn(conn))
	})
	if err != nil {
		up.cancel()
		logger.Warn("websocket upgrade failed for %s: %v", up.path, err)
		return false
	}
	return true
}

func (h *WSLiveHandler) handleUpgrade(ctx *fasthttp.RequestCtx) {
	up, ok := h.gateway.prepareRequest(ctx)
	if !ok {
		return
	}
	h.upgrade(ctx, up, func(clientConn *realtimeClientConn) {
		h.serveSession(clientConn, up.preReqCtx, up.auth, up.path, up.middlewareValues)
	})
}

// handleAttach joins a sideband to a running session. The sideband bills nothing, the primary does.
func (h *WSLiveHandler) handleAttach(ctx *fasthttp.RequestCtx) {
	up, ok := h.gateway.prepareRequest(ctx)
	if !ok || !h.gateway.resolveSession(ctx, up) {
		return
	}
	// Unlike a new session, a sideband needs nothing from the client after the upgrade, so its
	// key and upstream are settled first and a refusal is a plain HTTP status.
	relay, bifrostErr := h.openSideband(up)
	if bifrostErr != nil {
		up.cancel()
		SendBifrostError(ctx, bifrostErr)
		return
	}
	if !h.upgrade(ctx, up, func(clientConn *realtimeClientConn) { h.serveSideband(relay, clientConn) }) {
		relay.cancel()
		h.pool.Discard(relay.upstream)
	}
}

// openSideband selects a key and dials the provider's attach endpoint on it.
func (h *WSLiveHandler) openSideband(up *liveRequest) (*liveSidebandRelay, *schemas.BifrostError) {
	ctx, cancel := h.gateway.sessionContext(up.auth, up.preReqCtx, up.middlewareValues, up.path)
	key, bifrostErr := h.gateway.controlKey(ctx, up.providerKey)
	if bifrostErr != nil {
		cancel()
		return nil, bifrostErr
	}
	upstream, bifrostErr := h.dial(ctx, up.provider, up.providerKey, key, schemas.LiveConnectionSideband, up.sessionID)
	if bifrostErr != nil {
		cancel()
		return nil, bifrostErr
	}
	return &liveSidebandRelay{
		liveUpdatePolicy: liveUpdatePolicy{models: h.gateway.client, provider: up.providerKey, key: key, checkKeyModels: true},
		upstream:         upstream,
		cancel:           cancel,
	}, nil
}

// serveSideband relays one sideband on an upgraded connection and releases what openSideband took.
func (h *WSLiveHandler) serveSideband(relay *liveSidebandRelay, clientConn *realtimeClientConn) {
	defer relay.cancel()
	defer h.pool.Discard(relay.upstream)
	clientConn.startHeartbeat()
	defer clientConn.stopHeartbeat()

	if _, sessionErr := h.sessions.Create(clientConn.conn); sessionErr != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(429, "rate_limit_exceeded", sessionErr.Error()))
		return
	}
	defer h.sessions.Remove(clientConn.conn)
	relay.clientConn = clientConn
	relay.run()
}

// serveSession runs one live session: bootstrap, admission, dial, relay, close.
func (h *WSLiveHandler) serveSession(clientConn *realtimeClientConn, preReqCtx *schemas.BifrostContext, auth *authHeaders, path string, middlewareValues map[any]any) {
	clientConn.startHeartbeat()
	defer clientConn.stopHeartbeat()

	startFrame, start, err := readLiveSessionStart(clientConn)
	if err != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", err.Error()))
		return
	}
	// The model arrives in-band, so the pre-request pipeline runs after the upgrade, still before
	// a session slot or an upstream connection exists.
	target, bifrostErr := h.gateway.resolveTarget(preReqCtx, path, start)
	if bifrostErr != nil {
		clientConn.writeRealtimeError(bifrostErr)
		return
	}

	session, sessionErr := h.sessions.Create(clientConn.conn)
	if sessionErr != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(429, "rate_limit_exceeded", sessionErr.Error()))
		return
	}
	defer h.sessions.Remove(clientConn.conn)

	admission, bifrostErr := h.gateway.admit(auth, preReqCtx, middlewareValues, path, target, session.ID())
	if bifrostErr != nil {
		clientConn.writeRealtimeError(bifrostErr)
		return
	}
	defer admission.cancel()
	// Every exit from here on bills what OpenAI reported; a session.closed finish runs first.
	defer func() { admission.meter.finish(admission.meter.lastReportedSeconds()) }()

	startFrame, err = rewriteLiveModels(startFrame, start, admission.key, target.voiceModel, target.backendModel)
	if err != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(500, "server_error", "failed to prepare session.start: "+err.Error()))
		return
	}
	upstream, bifrostErr := h.dial(admission.ctx, admission.provider, admission.providerKey, admission.key, schemas.LiveConnectionPrimary, "")
	if bifrostErr != nil {
		clientConn.writeRealtimeError(bifrostErr)
		return
	}
	// A live socket is one session; it is never returned to the pool for reuse.
	defer h.pool.Discard(upstream)
	if err := upstream.WriteMessage(ws.TextMessage, startFrame); err != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(502, "server_error", "failed to send session.start upstream"))
		return
	}

	relay := &liveWSRelay{clientConn: clientConn, upstream: upstream}
	relay.liveSessionController = admission.controller(h.gateway, relay)
	relay.run()
}

// dial opens an upstream live WebSocket of the given kind on key.
func (h *WSLiveHandler) dial(ctx *schemas.BifrostContext, provider schemas.LiveProvider, providerKey schemas.ModelProvider, key schemas.Key, kind schemas.LiveConnectionKind, sessionID string) (*bfws.UpstreamConn, *schemas.BifrostError) {
	wsURL, bifrostErr := provider.LiveWebSocketURL(key, kind, sessionID)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	headers, bifrostErr := provider.LiveHeaders(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	var proxyConfig *schemas.ProxyConfig
	if providerCfg, cfgErr := h.config.GetProviderConfigRaw(providerKey); cfgErr == nil && providerCfg != nil {
		proxyConfig = providerCfg.ProxyConfig
	}
	upstream, err := h.pool.Get(bfws.PoolKey{Provider: providerKey, KeyID: key.ID, Endpoint: wsURL}, mapToHTTPHeader(headers), proxyConfig)
	if err != nil {
		// Only the status reaches the client; the URL and body stay in the server log.
		logger.Warn("live %s dial failed: %v", kind, err)
		var handshake *bfws.HandshakeError
		if errors.As(err, &handshake) && handshake.StatusCode == fasthttp.StatusNotFound {
			return nil, newRealtimeWireBifrostError(404, "invalid_request_error", "live session not found, or it does not accept this connection (a sideband needs a WebRTC or SIP session)")
		}
		return nil, newRealtimeWireBifrostError(502, "server_error", fmt.Sprintf("failed to open the %s live connection at the provider", kind))
	}
	return upstream, nil
}

// readLiveSessionStart reads the bootstrap frame, which must be session.start.
func readLiveSessionStart(clientConn *realtimeClientConn) ([]byte, *schemas.LiveSession, error) {
	if err := clientConn.conn.SetReadDeadline(time.Now().Add(liveBootstrapTimeout)); err != nil {
		return nil, nil, err
	}
	defer clientConn.refreshReadDeadline()
	messageType, message, err := clientConn.conn.ReadMessage()
	if err != nil {
		return nil, nil, fmt.Errorf("session.start was not received: %w", err)
	}
	if messageType != ws.TextMessage {
		return nil, nil, errors.New("live sessions only accept text messages")
	}
	if len(message) > liveBootstrapMaxBytes {
		return nil, nil, errors.New("session.start exceeded 1 MiB")
	}
	if schemas.LiveEventTypeOf(message) != schemas.LiveEventSessionStart {
		return nil, nil, errors.New("the first event of a live session must be session.start")
	}
	event, err := schemas.ParseLiveEvent(message)
	if err != nil {
		return nil, nil, errors.New("failed to parse session.start JSON")
	}
	if event.Session == nil || strings.TrimSpace(event.Session.Model) == "" {
		return nil, nil, errors.New("session.start requires session.model")
	}
	return message, event.Session, nil
}

// liveWSRelay carries a live session over WebSockets. Frames pass through untouched; the
// controller reads control events by type, without decoding audio.
type liveWSRelay struct {
	*liveSessionController
	clientConn *realtimeClientConn
	upstream   *bfws.UpstreamConn
}

func (r *liveWSRelay) sendUpstream(message []byte) error {
	return r.upstream.WriteMessage(ws.TextMessage, message)
}

func (r *liveWSRelay) sendClient(message []byte) error {
	return r.clientConn.WriteMessage(ws.TextMessage, message)
}

func (r *liveWSRelay) abandonUpstream() {
	_ = r.upstream.Close()
}

func (r *liveWSRelay) run() {
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		r.pumpClient()
	}()
	go r.watchStale()
	r.pumpUpstream()
	releaseLiveClient(r.clientConn, r.clientGone.Load(), clientDone)
}

// releaseLiveClient ends the client side once the upstream is gone. Close on a fasthttp hijacked
// connection does not interrupt a pending read, so a repeated read deadline releases it (a pong
// pushes one deadline out).
func releaseLiveClient(clientConn *realtimeClientConn, clientGone bool, clientDone <-chan struct{}) {
	if !clientGone {
		// WriteControl is safe alongside the connection's other writers.
		_ = clientConn.conn.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(ws.CloseNormalClosure, "live session ended"), time.Now().Add(realtimeWSWriteTimeout))
	}
	for {
		_ = clientConn.conn.SetReadDeadline(time.Now())
		select {
		case <-clientDone:
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// liveSidebandRelay carries a sideband: client commands pass the session's update check, upstream
// events pass through. The client leaving ends only the sideband.
type liveSidebandRelay struct {
	liveUpdatePolicy
	clientConn *realtimeClientConn
	upstream   *bfws.UpstreamConn
	cancel     context.CancelFunc // the sideband's context; nil in tests
	clientGone atomic.Bool
}

func (r *liveSidebandRelay) run() {
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		r.pumpClient()
	}()
	r.pumpUpstream()
	releaseLiveClient(r.clientConn, r.clientGone.Load(), clientDone)
}

func (r *liveSidebandRelay) pumpClient() {
	defer func() { _ = r.upstream.Close() }()
	for {
		messageType, message, err := r.clientConn.ReadMessage()
		if err != nil {
			r.clientGone.Store(true)
			return
		}
		if messageType != ws.TextMessage {
			r.clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", "live sessions only accept text messages"))
			continue
		}
		if schemas.LiveEventTypeOf(message) == schemas.LiveEventSessionUpdate {
			checked, _, bifrostErr := r.check(message)
			if bifrostErr != nil {
				r.clientConn.writeRealtimeError(bifrostErr)
				continue
			}
			message = checked
		}
		if err := r.upstream.WriteMessage(ws.TextMessage, message); err != nil {
			return
		}
	}
}

func (r *liveSidebandRelay) pumpUpstream() {
	for {
		messageType, message, err := r.upstream.ReadMessage()
		if err != nil {
			if !r.clientGone.Load() && !isNormalWebSocketClosure(err) {
				r.clientConn.writeRealtimeError(newRealtimeWireBifrostError(502, "server_error", "the live session's upstream connection ended"))
			}
			return
		}
		if r.clientGone.Load() {
			continue
		}
		if err := r.clientConn.WriteMessage(messageType, message); err != nil {
			r.clientGone.Store(true)
		}
	}
}

func (r *liveWSRelay) pumpClient() {
	for {
		messageType, message, err := r.clientConn.ReadMessage()
		if err != nil {
			r.clientLeft()
			return
		}
		if messageType != ws.TextMessage {
			r.sendError(newRealtimeWireBifrostError(400, "invalid_request_error", "live sessions only accept text messages"))
			continue
		}
		forward, ok := r.fromClient(message)
		if !ok {
			continue
		}
		if err := r.sendUpstream(forward); err != nil {
			return
		}
	}
}

func (r *liveWSRelay) pumpUpstream() {
	for {
		messageType, message, err := r.upstream.ReadMessage()
		if err != nil {
			r.upstreamEnded()
			if !r.clientGone.Load() && !isNormalWebSocketClosure(err) {
				r.sendError(newRealtimeWireBifrostError(502, "server_error", "the live session's upstream connection ended before session.closed"))
			}
			return
		}
		if messageType != ws.TextMessage {
			if !r.clientGone.Load() {
				_ = r.clientConn.WriteMessage(messageType, message)
			}
			continue
		}
		sessionClosed := r.fromUpstream(message)
		r.forwardToClient(message)
		if sessionClosed {
			return
		}
	}
}
