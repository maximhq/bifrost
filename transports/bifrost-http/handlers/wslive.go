package handlers

import (
	"errors"
	"fmt"
	"strings"
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

// RegisterRoutes registers the GPT Live endpoint at the base path and the OpenAI integration paths.
func (h *WSLiveHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handler := lib.ChainMiddlewares(h.handleUpgrade, middlewares...)
	r.GET("/v1/live/sessions", handler)
	for _, path := range integrations.OpenAILivePaths("/openai") {
		r.GET(path, handler)
	}
}

func (h *WSLiveHandler) Close() {
	if h == nil || h.sessions == nil {
		return
	}
	h.sessions.CloseAll()
}

func (h *WSLiveHandler) handleUpgrade(ctx *fasthttp.RequestCtx) {
	path := string(ctx.Path())
	auth := captureAuthHeaders(ctx)
	preReqCtx, preReqCancel := createBifrostContextFromAuth(h.gateway.handlerStore, auth)
	preReqCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.LiveRequest)
	if strings.HasPrefix(path, "/openai") {
		preReqCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	}

	// Accepting the upgrade opens an upstream session on the operator's key, so an anonymous
	// caller is refused with a plain 401 before it.
	if authErr := h.gateway.refuseAnonymous(preReqCtx); authErr != nil {
		preReqCancel()
		SendBifrostError(ctx, authErr)
		return
	}
	populateRealtimeRequestContext(ctx, preReqCtx)
	// The fasthttp ctx is recycled once this handler returns, and the session outlives it.
	middlewareValues := snapshotRealtimeMiddlewareValues(ctx)

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
		defer preReqCancel()
		h.serveSession(newRealtimeClientConn(conn), preReqCtx, auth, path, middlewareValues)
	})
	if err != nil {
		preReqCancel()
		logger.Warn("websocket upgrade failed for %s: %v", path, err)
	}
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
	upstream, bifrostErr := h.dial(admission)
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
	relay.liveSessionController = admission.controller(h.gateway.client, relay)
	relay.run()
}

// dial opens the upstream primary WebSocket on the session's key.
func (h *WSLiveHandler) dial(admission *liveAdmission) (*bfws.UpstreamConn, *schemas.BifrostError) {
	wsURL, bifrostErr := admission.provider.LiveWebSocketURL(admission.key, schemas.LiveConnectionPrimary, "")
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	headers, bifrostErr := admission.provider.LiveHeaders(admission.ctx, admission.key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	var proxyConfig *schemas.ProxyConfig
	if providerCfg, cfgErr := h.config.GetProviderConfigRaw(admission.providerKey); cfgErr == nil && providerCfg != nil {
		proxyConfig = providerCfg.ProxyConfig
	}
	upstream, err := h.pool.Get(bfws.PoolKey{Provider: admission.providerKey, KeyID: admission.key.ID, Endpoint: wsURL}, mapToHTTPHeader(headers), proxyConfig)
	if err != nil {
		return nil, newRealtimeWireBifrostError(502, "server_error", err.Error())
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
	r.releaseClient(clientDone)
}

// releaseClient ends the client side. Close on a fasthttp hijacked connection does not interrupt
// a pending read, so a repeated read deadline releases it (a pong pushes one deadline out).
func (r *liveWSRelay) releaseClient(clientDone <-chan struct{}) {
	if !r.clientGone.Load() {
		// WriteControl is safe alongside the connection's other writers.
		_ = r.clientConn.conn.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(ws.CloseNormalClosure, "live session ended"), time.Now().Add(realtimeWSWriteTimeout))
	}
	for {
		_ = r.clientConn.conn.SetReadDeadline(time.Now())
		select {
		case <-clientDone:
			return
		case <-time.After(100 * time.Millisecond):
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
