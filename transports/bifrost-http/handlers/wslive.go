package handlers

import (
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fasthttp/router"
	ws "github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
	"github.com/valyala/fasthttp"
)

const (
	liveBootstrapTimeout   = 15 * time.Second
	liveBootstrapMaxBytes  = 1 << 20
	liveCloseDrainTimeout  = 15 * time.Second
	liveStaleCheckInterval = 5 * time.Second
)

const liveAuthRefusalMessage = "authentication is required for live sessions. Provide a virtual key (x-bf-vk or an sk-bf- bearer token)."

// WSLiveHandler relays GPT Live primary WebSocket sessions and bills them as they run.
type WSLiveHandler struct {
	client       *bifrost.Bifrost
	config       *lib.Config
	handlerStore lib.HandlerStore
	pool         *bfws.Pool
	sessions     *bfws.SessionManager
}

// NewWSLiveHandler creates a new GPT Live WebSocket handler.
func NewWSLiveHandler(client *bifrost.Bifrost, config *lib.Config, pool *bfws.Pool) *WSLiveHandler {
	return &WSLiveHandler{
		client:       client,
		config:       config,
		handlerStore: config,
		pool:         pool,
		sessions:     bfws.NewSessionManager(config.WebSocketConfig.MaxConnections),
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
	preReqCtx, preReqCancel := createBifrostContextFromAuth(h.handlerStore, auth)
	preReqCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.LiveRequest)
	if strings.HasPrefix(path, "/openai") {
		preReqCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	}

	// Accepting the upgrade opens an upstream session on the operator's key, so an anonymous
	// caller is refused with a plain 401 before it.
	if h.config.ClientConfig.EnforceAuthOnInference && !governance.PresentedAnyCredential(preReqCtx) {
		preReqCancel()
		SendBifrostError(ctx, newRealtimeWireBifrostError(401, "invalid_request_error", liveAuthRefusalMessage))
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

	// The model arrives in-band, so the pre-request pipeline (credential resolution, routing)
	// runs after the upgrade, still before a session slot or an upstream connection exists.
	providerKey, voiceModel := schemas.ParseModelString(start.Model, realtimeDefaultProviderForPath(path))
	preReq := &schemas.BifrostRequest{
		RequestType:      schemas.LiveRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: providerKey, Model: voiceModel},
	}
	h.client.RunPreRequestHooks(preReqCtx, preReq)
	if h.config.ClientConfig.EnforceAuthOnInference && !governance.PresentedCredentialResolved(preReqCtx) {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(401, "invalid_request_error", realtimeUnresolvedCredentialMessage))
		return
	}
	providerKey, voiceModel, _ = preReq.GetRequestFields()
	if providerKey == "" || strings.TrimSpace(voiceModel) == "" {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", fmt.Sprintf("no provider could be resolved for model %q (set as provider/model or configure the model catalog)", start.Model)))
		return
	}
	backendModel, err := liveBackendModel(start, providerKey)
	if err != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", err.Error()))
		return
	}
	liveProvider, ok := h.client.GetProviderByKey(providerKey).(schemas.LiveProvider)
	if !ok {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", "provider does not support live sessions: "+string(providerKey)))
		return
	}

	session, sessionErr := h.sessions.Create(clientConn.conn)
	if sessionErr != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(429, "rate_limit_exceeded", sessionErr.Error()))
		return
	}
	defer h.sessions.Remove(clientConn.conn)

	bifrostCtx, cancel := createBifrostContextFromAuth(h.handlerStore, auth)
	defer cancel()
	applyRealtimeMiddlewareValues(bifrostCtx, liveMiddlewareValues(middlewareValues, preReqCtx))
	lib.SettleIdentity(bifrostCtx)
	bifrostCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.LiveRequest)
	if strings.HasPrefix(path, "/openai") {
		bifrostCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	}

	// One key serves the whole session: OpenAI calls the backend model on it too.
	key, err := h.client.SelectKeyForProviderRequestType(bifrostCtx, schemas.LiveRequest, providerKey, voiceModel, backendModel)
	if err != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", err.Error()))
		return
	}

	meter := newLiveMeter(h.client, bifrostCtx, providerKey, key, session.ID())
	if bifrostErr := meter.admit(voiceModel, backendModel); bifrostErr != nil {
		clientConn.writeRealtimeError(bifrostErr)
		return
	}
	// Every exit from here on bills what OpenAI reported; a session.closed finish runs first.
	defer func() { meter.finish(meter.lastReportedSeconds()) }()

	startFrame, err = rewriteLiveModels(startFrame, start, key, voiceModel, backendModel)
	if err != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(500, "server_error", "failed to prepare session.start: "+err.Error()))
		return
	}
	wsURL, bifrostErr := liveProvider.LiveWebSocketURL(key, schemas.LiveConnectionPrimary, "")
	if bifrostErr != nil {
		clientConn.writeRealtimeError(bifrostErr)
		return
	}
	headers, bifrostErr := liveProvider.LiveHeaders(bifrostCtx, key)
	if bifrostErr != nil {
		clientConn.writeRealtimeError(bifrostErr)
		return
	}
	var proxyConfig *schemas.ProxyConfig
	if providerCfg, cfgErr := h.config.GetProviderConfigRaw(providerKey); cfgErr == nil && providerCfg != nil {
		proxyConfig = providerCfg.ProxyConfig
	}
	upstream, err := h.pool.Get(bfws.PoolKey{Provider: providerKey, KeyID: key.ID, Endpoint: wsURL}, mapToHTTPHeader(headers), proxyConfig)
	if err != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(502, "server_error", err.Error()))
		return
	}
	// A live socket is one session; it is never returned to the pool for reuse.
	defer h.pool.Discard(upstream)
	if err := upstream.WriteMessage(ws.TextMessage, startFrame); err != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(502, "server_error", "failed to send session.start upstream"))
		return
	}

	_, isDirectKey := bifrostCtx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key)
	relay := &liveRelay{
		models:          h.client,
		clientConn:      clientConn,
		upstream:        upstream,
		meter:           meter,
		provider:        providerKey,
		key:             key,
		checkKeyModels:  !isDirectKey,
		upstreamDone:    make(chan struct{}),
		drainTimeout:    liveCloseDrainTimeout,
		staleCheckEvery: liveStaleCheckInterval,
	}
	relay.run()
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

// liveBackendModel returns the Responses backend model, which must be on the session's provider.
// It is empty for client delegation, where the app calls its backend itself.
func liveBackendModel(start *schemas.LiveSession, providerKey schemas.ModelProvider) (string, error) {
	if start.Delegation == nil || start.Delegation.Type != schemas.LiveDelegationResponses || start.Delegation.Responses == nil {
		return "", nil
	}
	backendProvider, model := schemas.ParseModelString(start.Delegation.Responses.Model, providerKey)
	if strings.TrimSpace(model) == "" {
		return "", errors.New("session.delegation.responses.model is required for responses delegation")
	}
	if backendProvider != providerKey {
		return "", fmt.Errorf("session.delegation.responses.model must be served by %s, the provider of session.model", providerKey)
	}
	return model, nil
}

// rewriteLiveModels sends OpenAI the bare, alias-resolved model names; other bytes stay as sent.
func rewriteLiveModels(frame []byte, start *schemas.LiveSession, key schemas.Key, voiceModel, backendModel string) ([]byte, error) {
	frame, err := setLiveModel(frame, "session.model", start.Model, key.Aliases.Resolve(voiceModel))
	if err != nil || backendModel == "" {
		return frame, err
	}
	return setLiveModel(frame, "session.delegation.responses.model", start.Delegation.Responses.Model, key.Aliases.Resolve(backendModel))
}

func setLiveModel(frame []byte, path, sent, wire string) ([]byte, error) {
	if sent == wire {
		return frame, nil
	}
	encoded, err := schemas.Marshal(wire)
	if err != nil {
		return nil, err
	}
	return providerUtils.SetRawJSONField(frame, path, encoded)
}

// liveMiddlewareValues adds what the pre-request pipeline resolved after the upgrade (routing,
// identity) to the values captured from the transport middleware before it.
func liveMiddlewareValues(snapshot map[any]any, preReqCtx *schemas.BifrostContext) map[any]any {
	values := make(map[any]any, len(snapshot))
	maps.Copy(values, snapshot)
	for _, k := range realtimeMiddlewareKeys {
		if v := preReqCtx.Value(k); v != nil {
			values[k] = v
		}
	}
	return values
}

// liveModelChecker is the slice of *bifrost.Bifrost the relay needs; tests substitute it.
type liveModelChecker interface {
	KeySupportsModel(providerKey schemas.ModelProvider, key schemas.Key, model string) bool
}

// liveRelay pumps frames both ways. Audio passes through untouched; only control events that
// affect access or billing are read, by type, without decoding the frame.
type liveRelay struct {
	models          liveModelChecker
	clientConn      *realtimeClientConn
	upstream        *bfws.UpstreamConn
	meter           *liveMeter
	provider        schemas.ModelProvider
	key             schemas.Key
	checkKeyModels  bool // false for a caller-supplied direct key, which carries no model lists
	drainTimeout    time.Duration
	staleCheckEvery time.Duration

	upstreamDone chan struct{}
	clientGone   atomic.Bool
	closeOnce    sync.Once
	refusalOnce  sync.Once
	drainMu      sync.Mutex
	drainTimer   *time.Timer
}

func (r *liveRelay) run() {
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		r.pumpClient()
	}()
	go r.watchStale()
	r.pumpUpstream()
	r.stopDrainTimer()
	r.releaseClient(clientDone)
}

// releaseClient ends the client side once the session is over. Close on a fasthttp hijacked
// connection does not interrupt a pending read, so the reader is released with a read deadline,
// repeated because a pong arriving meanwhile pushes the deadline out again.
func (r *liveRelay) releaseClient(clientDone <-chan struct{}) {
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

func (r *liveRelay) pumpClient() {
	for {
		messageType, message, err := r.clientConn.ReadMessage()
		if err != nil {
			// The client left: close the session upstream so its final usage still arrives.
			r.clientGone.Store(true)
			r.requestClose()
			return
		}
		if messageType != ws.TextMessage {
			r.clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", "live sessions only accept text messages"))
			continue
		}
		switch schemas.LiveEventTypeOf(message) {
		case schemas.LiveEventSessionUpdate:
			var bifrostErr *schemas.BifrostError
			if message, bifrostErr = r.admitSessionUpdate(message); bifrostErr != nil {
				r.clientConn.writeRealtimeError(bifrostErr)
				continue
			}
		case schemas.LiveEventSessionClose:
			r.closeOnce.Do(r.startDrainTimer)
		}
		if err := r.upstream.WriteMessage(ws.TextMessage, message); err != nil {
			return
		}
	}
}

func (r *liveRelay) pumpUpstream() {
	defer close(r.upstreamDone)
	for {
		messageType, message, err := r.upstream.ReadMessage()
		if err != nil {
			// Ended without session.closed: bill up to what OpenAI last reported.
			r.meter.finish(r.meter.lastReportedSeconds())
			if !r.clientGone.Load() && !isNormalWebSocketClosure(err) {
				r.clientConn.writeRealtimeError(newRealtimeWireBifrostError(502, "server_error", "the live session's upstream connection ended before session.closed"))
			}
			return
		}
		sessionClosed := messageType == ws.TextMessage && r.inspectUpstream(message)
		r.forward(messageType, message)
		if sessionClosed {
			return
		}
	}
}

// inspectUpstream meters an upstream control event and reports whether it ended the session.
func (r *liveRelay) inspectUpstream(message []byte) bool {
	switch schemas.LiveEventTypeOf(message) {
	case schemas.LiveEventSessionStarted:
		r.meter.setProviderSessionID(providerUtils.GetJSONField(message, "session.id").Str)
	case schemas.LiveEventUsageUpdated:
		if refusal := r.meter.onUsage(providerUtils.GetJSONField(message, "usage.seconds").Float()); refusal != nil {
			r.endForRefusal(refusal)
		}
	case schemas.LiveEventResponseEvent:
		switch schemas.ResponsesStreamResponseType(providerUtils.GetJSONField(message, "event.type").Str) {
		case schemas.ResponsesStreamResponseTypeCompleted, schemas.ResponsesStreamResponseTypeIncomplete, schemas.ResponsesStreamResponseTypeFailed:
			event, err := schemas.ParseLiveEvent(message)
			if err != nil || event.Event == nil || event.Event.Response == nil {
				logger.Warn("live session: failed to read backend usage from response.event: %v", err)
				return false
			}
			if refusal := r.meter.onBackendResponse(event.Event.Response.Response); refusal != nil {
				r.endForRefusal(refusal)
			}
		}
	case schemas.LiveEventSessionClosed:
		r.meter.finish(providerUtils.GetJSONField(message, "usage.seconds").Float())
		return true
	}
	return false
}

// admitSessionUpdate re-checks a changed backend model before it reaches OpenAI: the session's key
// must serve it and governance must admit it. A refused update is dropped.
func (r *liveRelay) admitSessionUpdate(message []byte) ([]byte, *schemas.BifrostError) {
	sent := providerUtils.GetJSONField(message, "session.delegation.responses.model").Str
	if strings.TrimSpace(sent) == "" {
		return message, nil
	}
	provider, model := schemas.ParseModelString(sent, r.provider)
	if provider != r.provider {
		return nil, newRealtimeWireBifrostError(400, "invalid_request_error", fmt.Sprintf("session.delegation.responses.model must be served by %s, the provider of session.model", r.provider))
	}
	if r.checkKeyModels && !r.models.KeySupportsModel(r.provider, r.key, model) {
		return nil, newRealtimeWireBifrostError(400, "invalid_request_error", fmt.Sprintf("the key serving this session does not support model %s", model))
	}
	if bifrostErr := r.meter.switchBackend(model); bifrostErr != nil {
		return nil, bifrostErr
	}
	message, err := setLiveModel(message, "session.delegation.responses.model", sent, r.key.Aliases.Resolve(model))
	if err != nil {
		return nil, newRealtimeWireBifrostError(500, "server_error", "failed to prepare session.update: "+err.Error())
	}
	return message, nil
}

func (r *liveRelay) forward(messageType int, message []byte) {
	if r.clientGone.Load() {
		return
	}
	if err := r.clientConn.WriteMessage(messageType, message); err != nil {
		r.clientGone.Store(true)
		r.requestClose()
	}
}

// endForRefusal tells the client why the session is ending, then closes it gracefully so the
// usage already spent is still reported and billed.
func (r *liveRelay) endForRefusal(refusal *schemas.BifrostError) {
	r.refusalOnce.Do(func() {
		if !r.clientGone.Load() {
			r.clientConn.writeRealtimeError(refusal)
		}
		r.requestClose()
	})
}

// requestClose sends session.close upstream once and bounds the wait for session.closed.
func (r *liveRelay) requestClose() {
	r.closeOnce.Do(func() {
		select {
		case <-r.upstreamDone:
			return
		default:
		}
		_ = r.upstream.WriteMessage(ws.TextMessage, []byte(`{"type":"session.close"}`))
		r.startDrainTimer()
	})
}

func (r *liveRelay) startDrainTimer() {
	r.drainMu.Lock()
	defer r.drainMu.Unlock()
	if r.drainTimer == nil {
		r.drainTimer = time.AfterFunc(r.drainTimeout, func() { _ = r.upstream.Close() })
	}
}

func (r *liveRelay) stopDrainTimer() {
	r.drainMu.Lock()
	defer r.drainMu.Unlock()
	if r.drainTimer != nil {
		r.drainTimer.Stop()
	}
}

// watchStale runs the budget check on wall time while OpenAI sends no usage.
func (r *liveRelay) watchStale() {
	ticker := time.NewTicker(r.staleCheckEvery)
	defer ticker.Stop()
	for {
		select {
		case <-r.upstreamDone:
			return
		case now := <-ticker.C:
			if refusal := r.meter.checkStale(now); refusal != nil {
				r.endForRefusal(refusal)
			}
		}
	}
}
