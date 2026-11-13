package handlers

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/encoder"
	"github.com/fasthttp/router"
	ws "github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/v2/bifrost-http/websocket"
	"github.com/valyala/fasthttp"
)

const (
	speechSessionConfigTimeout   = 15 * time.Second
	speechSessionIdleTimeout     = 5 * time.Minute
	speechSessionMaxMessageBytes = 1 << 20
)

const speechSessionAuthRefusalMessage = "authentication is required for speech sessions. Provide a virtual key (x-bf-vk or an sk-bf- bearer token)."

// speechSessionConfigKnownFields are the speech.config fields that are not provider extra params.
var speechSessionConfigKnownFields = func() map[string]bool {
	fields := maps.Clone(speechParamsKnownFields)
	fields["type"] = true
	return fields
}()

// WSSpeechSessionHandler serves incremental text-to-speech: the client streams text in over a WebSocket
// and the provider's audio streams back while the text is still being written.
type WSSpeechSessionHandler struct {
	client   *bifrost.Bifrost
	config   *lib.Config
	pool     *bfws.Pool
	sessions *bfws.SessionManager
}

// NewWSSpeechSessionHandler creates a new speech session WebSocket handler.
func NewWSSpeechSessionHandler(client *bifrost.Bifrost, config *lib.Config, pool *bfws.Pool) *WSSpeechSessionHandler {
	return &WSSpeechSessionHandler{
		client:   client,
		config:   config,
		pool:     pool,
		sessions: bfws.NewSessionManager(config.WebSocketConfig.MaxConnections),
	}
}

// RegisterRoutes registers the speech session endpoint.
func (h *WSSpeechSessionHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/v1/audio/speech/stream", lib.ChainMiddlewares(h.handleUpgrade, middlewares...))
	// An ElevenLabs client's text-to-dialogue socket, on the /elevenlabs base URL its SDK is given.
	r.GET(elevenlabsDialoguePath, lib.ChainMiddlewares(h.handleElevenlabsDialogueUpgrade, middlewares...))
}

// Close closes active speech sessions.
func (h *WSSpeechSessionHandler) Close() {
	if h == nil || h.sessions == nil {
		return
	}
	h.sessions.CloseAll()
}

// handleUpgrade refuses an anonymous caller with a plain 401 before anything is opened on the operator's key.
func (h *WSSpeechSessionHandler) handleUpgrade(ctx *fasthttp.RequestCtx) {
	path := string(ctx.Path())
	setupHeaders := captureSessionHeaders(ctx)
	preReqCtx, cancel := createBifrostContextFromHeaders(h.config, setupHeaders)
	preReqCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.SpeechStreamRequest)
	if h.config.ClientConfig.EnforceAuthOnInference && !governance.PresentedAnyCredential(preReqCtx) {
		cancel()
		SendBifrostError(ctx, newRealtimeWireBifrostError(401, "invalid_request_error", speechSessionAuthRefusalMessage))
		return
	}
	populateRealtimeRequestContext(ctx, preReqCtx)
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
		defer recoverWebSocketSession(conn)
		defer cancel()
		h.serve(newRealtimeClientConn(conn), preReqCtx, setupHeaders, middlewareValues)
	})
	if err != nil {
		cancel()
		logger.Warn("websocket upgrade failed for %s: %v", path, err)
	}
}

// serve runs one speech session: config, admission, provider session, relay, settlement.
func (h *WSSpeechSessionHandler) serve(clientConn *realtimeClientConn, preReqCtx *schemas.BifrostContext, setupHeaders *sessionHeaders, middlewareValues map[any]any) {
	clientConn.startHeartbeat()
	defer clientConn.stopHeartbeat()

	frame, err := readSpeechSessionConfig(clientConn)
	if err != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", err.Error()))
		return
	}
	req, err := parseSpeechSessionConfig(frame)
	if err != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", err.Error()))
		return
	}
	// The model arrives in-band, so the pre-request pipeline runs after the upgrade, still before a
	// session slot or a provider connection exists.
	provider, bifrostErr := h.resolveTarget(preReqCtx, req)
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

	ctx, cancel := createBifrostContextFromHeaders(h.config, setupHeaders)
	defer cancel()
	applyRealtimeMiddlewareValues(ctx, liveMiddlewareValues(middlewareValues, preReqCtx))
	lib.SettleIdentity(ctx)
	ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.SpeechStreamRequest)

	unit, bifrostErr := openSpeechSessionUnit(h.client, ctx, session.ID(), req)
	if bifrostErr != nil {
		clientConn.writeRealtimeError(bifrostErr)
		return
	}
	// Every exit from here on settles the session, a panic included.
	var (
		key     schemas.Key
		run     *speechSessionRun
		failure *schemas.BifrostError
	)
	defer func() {
		chars := 0
		if run != nil {
			chars = int(run.chars.Load())
			if failure == nil {
				failure = run.failure
			}
		}
		unit.settle(req, key, chars, failure)
	}()
	key, err = h.client.SelectKeyForProviderRequestType(unit.ctx, schemas.SpeechStreamRequest, req.Provider, req.Model)
	if err != nil {
		failure = newRealtimeWireBifrostError(400, "invalid_request_error", err.Error())
		clientConn.writeRealtimeError(failure)
		return
	}
	// The provider receives the model the alias resolves to; the alias stays on the context for its overrides.
	sessionReq := *req
	if aliasConfig := key.Aliases.ResolveConfig(req.Model); aliasConfig != nil {
		ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: req.Model, Config: aliasConfig})
		sessionReq.Model = aliasConfig.ModelID
	}

	speech, bifrostErr := provider.OpenSpeechSession(ctx, key, &sessionReq, h.dialer(req.Provider, key))
	if bifrostErr != nil {
		failure = bifrostErr
		clientConn.writeRealtimeError(failure)
		return
	}
	defer speech.Close()

	format, sampleRate := speech.AudioFormat()
	run = &speechSessionRun{clientConn: clientConn, speech: speech, idleTimeout: speechSessionIdleTimeout}
	created := &schemas.BifrostSpeechSessionEvent{
		Type:    schemas.SpeechSessionEventCreated,
		Session: &schemas.SpeechSessionInfo{ID: session.ID(), Model: string(req.Provider) + "/" + req.Model, ResponseFormat: format, SampleRate: sampleRate},
	}
	if err := run.write(created); err != nil {
		return
	}

	logErr := runRealtimeRelays(clientConn.conn, run.relayClient, run.relayEvents, func() {
		// The client side closes first, it marks the teardown, so the read this close ends is not reported as a provider failure.
		_ = clientConn.Close()
		_ = speech.Close()
	})
	if logErr != nil {
		logger.Warn("speech session %s ended with an error: %v", session.ID(), logErr)
	}
}

// resolveTarget runs the pre-request pipeline for the config's model, which may route it, and returns the
// provider that serves it. It sets the routed provider and model on req.
func (h *WSSpeechSessionHandler) resolveTarget(preReqCtx *schemas.BifrostContext, req *schemas.BifrostSpeechRequest) (schemas.SpeechSessionProvider, *schemas.BifrostError) {
	preReq := &schemas.BifrostRequest{RequestType: schemas.SpeechStreamRequest, SpeechRequest: req}
	h.client.RunPreRequestHooks(preReqCtx, preReq)
	if h.config.ClientConfig.EnforceAuthOnInference && !governance.PresentedCredentialResolved(preReqCtx) {
		return nil, newRealtimeWireBifrostError(401, "invalid_request_error", realtimeUnresolvedCredentialMessage)
	}
	providerKey, model, _ := preReq.GetRequestFields()
	if providerKey == "" || strings.TrimSpace(model) == "" {
		return nil, newRealtimeWireBifrostError(400, "invalid_request_error", fmt.Sprintf("no provider could be resolved for model %q (set as provider/model or configure the model catalog)", req.Model))
	}
	provider, ok := h.client.GetProviderByKey(providerKey).(schemas.SpeechSessionProvider)
	if !ok {
		return nil, newRealtimeWireBifrostError(400, "invalid_request_error", "provider does not support incremental speech: "+string(providerKey))
	}
	req.Provider, req.Model = providerKey, model
	return provider, nil
}

// dialer opens a provider connection through the pool, which counts it and applies the provider's proxy.
func (h *WSSpeechSessionHandler) dialer(providerKey schemas.ModelProvider, key schemas.Key) schemas.SpeechSessionDialer {
	var proxyConfig *schemas.ProxyConfig
	if providerCfg, cfgErr := h.config.GetProviderConfigRaw(providerKey); cfgErr == nil && providerCfg != nil {
		proxyConfig = providerCfg.ProxyConfig
	}
	return func(_ *schemas.BifrostContext, url string, headers map[string]string) (schemas.RealtimeUpstream, *schemas.BifrostError) {
		upstream, err := h.pool.Get(bfws.PoolKey{Provider: providerKey, KeyID: key.ID, Endpoint: url}, mapToHTTPHeader(headers), proxyConfig)
		if err != nil {
			// Only the status reaches the client; the URL and body stay in the server log.
			logger.Warn("speech session dial failed: %v", err)
			return nil, newRealtimeWireBifrostError(502, "server_error", "failed to open the speech session at the provider")
		}
		return &speechSessionUpstream{UpstreamConn: upstream, pool: h.pool}, nil
	}
}

// speechSessionUpstream returns its connection to the pool's accounting when closed; a session's
// connection is never reused.
type speechSessionUpstream struct {
	*bfws.UpstreamConn
	pool *bfws.Pool
	once sync.Once
}

func (u *speechSessionUpstream) Close() error {
	u.once.Do(func() { u.pool.Discard(u.UpstreamConn) })
	return nil
}

// readSpeechSessionConfig reads the first frame, which must be speech.config.
func readSpeechSessionConfig(clientConn *realtimeClientConn) ([]byte, error) {
	clientConn.conn.SetReadLimit(speechSessionMaxMessageBytes)
	if err := clientConn.conn.SetReadDeadline(time.Now().Add(speechSessionConfigTimeout)); err != nil {
		return nil, err
	}
	defer clientConn.refreshReadDeadline()
	messageType, message, err := clientConn.conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("speech.config was not received: %w", err)
	}
	if messageType != ws.TextMessage {
		return nil, errors.New("speech sessions only accept text messages")
	}
	var event schemas.SpeechSessionClientEvent
	if err := sonic.Unmarshal(message, &event); err != nil || event.Type != schemas.SpeechSessionEventConfig {
		return nil, errors.New("the first event of a speech session must be speech.config")
	}
	return message, nil
}

// parseSpeechSessionConfig reads the fields of POST /v1/audio/speech, minus the input, from speech.config.
func parseSpeechSessionConfig(frame []byte) (*schemas.BifrostSpeechRequest, error) {
	var config SpeechRequest
	if err := sonic.Unmarshal(frame, &config); err != nil {
		return nil, errors.New("invalid speech.config payload")
	}
	provider, model := schemas.ParseModelString(config.Model, "")
	params := config.SpeechParameters
	if params == nil {
		params = &schemas.SpeechParameters{}
	}
	extraParams, err := extractExtraParams(frame, speechSessionConfigKnownFields)
	if err != nil {
		logger.Warn("Failed to extract extra params: %v", err)
	}
	params.ExtraParams = extraParams
	return &schemas.BifrostSpeechRequest{Provider: provider, Model: model, Input: &schemas.SpeechInput{}, Params: params}, nil
}

// speechSessionRun relays one open session between the client and its provider.
type speechSessionRun struct {
	clientConn  *realtimeClientConn
	speech      schemas.SpeechSession
	idleTimeout time.Duration         // how long the client may stay silent
	chars       atomic.Int64          // characters of text the client sent
	failure     *schemas.BifrostError // set by relayEvents when the provider ended the session abnormally
	payload     []byte                // the encoded event being sent
}

// write sends one event to the client. One goroutine writes at a time, so the encode buffer is reused.
func (r *speechSessionRun) write(event *schemas.BifrostSpeechSessionEvent) error {
	r.payload = r.payload[:0]
	if err := encoder.EncodeInto(&r.payload, event, 0); err != nil {
		return err
	}
	return r.clientConn.WriteMessage(ws.TextMessage, r.payload)
}

// speechSessionFrame is one read of the client connection.
type speechSessionFrame struct {
	messageType int
	data        []byte
	err         error
}

// readFrames reads the client connection on its own goroutine, so the relay can also wait on its idle
// timer: closing a hijacked connection does not interrupt a pending read. It stops when the client
// does or when stop closes.
func (r *speechSessionRun) readFrames(stop <-chan struct{}) <-chan speechSessionFrame {
	frames := make(chan speechSessionFrame, 1)
	go func() {
		for {
			messageType, data, err := r.clientConn.ReadMessage()
			select {
			case frames <- speechSessionFrame{messageType, data, err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return frames
}

// relayClient applies the client's events to the provider session until the client leaves or stays silent
// for the idle timeout.
func (r *speechSessionRun) relayClient() error {
	stop := make(chan struct{})
	defer close(stop)
	frames := r.readFrames(stop)
	idle := time.NewTimer(r.idleTimeout)
	defer idle.Stop()
	for {
		var frame speechSessionFrame
		select {
		case frame = <-frames:
		case <-idle.C:
			r.clientConn.writeRealtimeError(newRealtimeWireBifrostError(408, "timeout_error", "the speech session was idle for too long"))
			return nil
		}
		messageType, message, err := frame.messageType, frame.data, frame.err
		if err != nil {
			if isNormalWebSocketClosure(err) || r.clientConn.closing() {
				return nil
			}
			return err
		}
		idle.Reset(r.idleTimeout)
		if messageType != ws.TextMessage {
			r.clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", "speech sessions only accept text messages"))
			continue
		}
		var event schemas.SpeechSessionClientEvent
		if err := sonic.Unmarshal(message, &event); err != nil {
			r.clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", "failed to parse the event JSON"))
			continue
		}
		switch event.Type {
		case schemas.SpeechSessionEventText:
			// Only text the session accepted is counted, since only that is billed.
			if err = r.speech.SendText(event.Text); err == nil {
				r.chars.Add(int64(utf8.RuneCountInString(event.Text)))
			}
		case schemas.SpeechSessionEventFlush:
			err = r.speech.Flush()
		case schemas.SpeechSessionEventEnd:
			err = r.speech.End()
		case schemas.SpeechSessionEventCancel:
			err = r.speech.Cancel()
		default:
			err = fmt.Errorf("unsupported event type %q", event.Type)
		}
		if err != nil {
			r.clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", err.Error()))
		}
	}
}

// relayEvents forwards the provider session's events to the client until the session is over: its
// last event, speech.audio.done, carries the characters sent, and its events closing ends it too.
func (r *speechSessionRun) relayEvents() error {
	for event := range r.speech.Events() {
		if event.Type == schemas.SpeechSessionEventAudioDone {
			event.Usage = &schemas.SpeechUsage{InputChars: int(r.chars.Load())}
		}
		if err := r.write(event); err != nil {
			return err
		}
		if event.Type == schemas.SpeechSessionEventAudioDone {
			return nil
		}
	}
	if r.failure = r.speech.Err(); r.failure != nil {
		r.clientConn.writeRealtimeError(r.failure)
		return nil
	}
	return nil
}

// speechSessionUnit is the session admitted as one request through the plugin pipeline: its
// pre-hooks have run, and settling runs the post-hooks that bill and log it.
type speechSessionUnit struct {
	ctx       *schemas.BifrostContext
	baseCtx   *schemas.BifrostContext
	hooks     *bifrost.RealtimeTurnHooks
	sessionID string
	requestID string
	traceID   string
	startedAt time.Time
	preValues map[any]any
}

// openSpeechSessionUnit runs the plugin pre-hooks for the session. Governance admits it here: provider and
// model access, budgets and rate limits are checked once, when the session opens.
func openSpeechSessionUnit(client *bifrost.Bifrost, baseCtx *schemas.BifrostContext, sessionID string, req *schemas.BifrostSpeechRequest) (*speechSessionUnit, *schemas.BifrostError) {
	unit := &speechSessionUnit{baseCtx: baseCtx, sessionID: sessionID, requestID: sessionID, startedAt: time.Now()}
	unit.ctx = unit.newContext()
	setRealtimeTurnStreamContext(unit.ctx, unit.startedAt, false)
	hooks, bifrostErr := client.RunRealtimeTurnPreHooks(unit.ctx, &schemas.BifrostRequest{RequestType: schemas.SpeechStreamRequest, SpeechRequest: req})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	unit.hooks = hooks
	unit.traceID, _ = unit.ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	unit.preValues = unit.ctx.GetUserValues()
	return unit, nil
}

// newContext builds the context the session's hooks run under: the session's identity and grant, its own
// request id and trace, grouped under the session id.
func (u *speechSessionUnit) newContext() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for ctxKey, value := range u.baseCtx.GetUserValues() {
		// The unit mints its own trace; a session-level trace would strand its log entry.
		if value == nil || ctxKey == schemas.BifrostContextKeyTraceID || ctxKey == schemas.BifrostContextKeyExportTraceID {
			continue
		}
		ctx.SetValue(ctxKey, value)
	}
	if g := u.baseCtx.Grant(); g != nil {
		ctx.SetGrant(g)
	}
	ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.SpeechStreamRequest)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, u.requestID)
	parentID := u.sessionID
	if external, ok := u.baseCtx.Value(schemas.BifrostContextKeyParentRequestID).(string); ok && strings.TrimSpace(external) != "" {
		parentID = strings.TrimSpace(external)
	}
	ctx.SetValue(schemas.BifrostContextKeyParentRequestID, parentID)
	return ctx
}

// settle runs the post-hooks. Whatever ended the session, the characters the client sent are billed:
// the provider spoke them, or was asked to. bifrostErr is set when the provider failed the session.
func (u *speechSessionUnit) settle(req *schemas.BifrostSpeechRequest, key schemas.Key, chars int, bifrostErr *schemas.BifrostError) {
	defer func() {
		if u.hooks.Cleanup != nil {
			u.hooks.Cleanup()
		}
	}()
	var resp *schemas.BifrostResponse
	if bifrostErr == nil || chars > 0 {
		resp = &schemas.BifrostResponse{SpeechStreamResponse: &schemas.BifrostSpeechStreamResponse{
			Type:        schemas.SpeechStreamResponseTypeDone,
			Usage:       &schemas.SpeechUsage{InputChars: chars},
			ExtraFields: schemas.BifrostResponseExtraFields{Latency: time.Since(u.startedAt).Milliseconds()},
		}}
	}
	if bifrostErr != nil {
		// The post-hooks annotate the error they settle; the client's copy stays as it was sent.
		postErr := *bifrostErr
		bifrostErr = &postErr
	}

	postCtx := u.newContext()
	applyRealtimeTurnContextValues(postCtx, u.preValues)
	if strings.TrimSpace(key.ID) != "" {
		postCtx.SetValue(schemas.BifrostContextKeySelectedKeyID, key.ID)
	}
	if strings.TrimSpace(key.Name) != "" {
		postCtx.SetValue(schemas.BifrostContextKeySelectedKeyName, key.Name)
	}
	restoreRealtimeTurnTraceContext(postCtx, u.traceID, u.preValues)
	if config := key.Aliases.ResolveConfig(req.Model); config != nil {
		postCtx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: req.Model, Config: config})
	} else {
		postCtx.SetValue(schemas.BifrostContextKeyResolvedAlias, nil)
	}
	routingInfo := schemas.BuildRoutingInfo(postCtx, req.Provider, req.Model, key)
	resp.PopulateRoutingInfo(routingInfo)
	bifrostErr.PopulateRoutingInfo(routingInfo)
	setRealtimeTurnStreamContext(postCtx, u.startedAt, true)
	if _, hookErr := u.hooks.PostHookRunner(postCtx, resp, bifrostErr); hookErr != nil && hookErr.Error != nil {
		logger.Warn("speech session %s post-hook returned an error: %s", u.sessionID, hookErr.Error.Message)
	}
	completeRealtimeTurnTrace(postCtx)
}
