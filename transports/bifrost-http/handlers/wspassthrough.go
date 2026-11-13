package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ws "github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/v2/bifrost-http/websocket"
	"github.com/valyala/fasthttp"
)

// wsPassthroughUpstreamReadLimit bounds one upstream event at the ceiling the HTTP Responses
// relay enforces per SSE frame, so both transports accept exactly the same events.
const wsPassthroughUpstreamReadLimit = 10 << 20

// wsPassthroughMaxMessageHeader is the handshake response header a Codex backend uses to tell the
// client the largest message it accepts.
const wsPassthroughMaxMessageHeader = "X-Codex-Websocket-Max-Message-Bytes"

// WSPassthroughHandler serves Responses WebSocket mode on the passthrough routes. Unlike
// WSResponsesHandler it parses nothing: each client connection gets one dedicated upstream
// connection and frames are relayed as received in both directions. The provider keeps a
// conversation's history on that upstream connection, so it is never pooled or shared, and a
// client turn only carries its new input. Each response.create message is one governed, logged
// passthrough request: plugin hooks, usage and cost run per turn exactly as they do for the HTTP
// POST the message replaces.
type WSPassthroughHandler struct {
	client       *bifrost.Bifrost
	config       *lib.Config
	handlerStore lib.HandlerStore
	sessions     *bfws.SessionManager
	upgrader     ws.FastHTTPUpgrader
}

// NewWSPassthroughHandler creates the passthrough WebSocket relay handler.
func NewWSPassthroughHandler(client *bifrost.Bifrost, config *lib.Config) *WSPassthroughHandler {
	return &WSPassthroughHandler{
		client:       client,
		config:       config,
		handlerStore: config,
		sessions:     bfws.NewSessionManager(config.WebSocketConfig.MaxConnections),
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

// Close ends every active passthrough WebSocket session.
func (h *WSPassthroughHandler) Close() {
	if h == nil || h.sessions == nil {
		return
	}
	h.sessions.CloseAll()
}

// clientReadLimit is the largest message a client may send. One response.create message is the
// WebSocket form of one request body, so it takes the HTTP request body limit.
func (h *WSPassthroughHandler) clientReadLimit() int64 {
	limitMB := h.config.ClientConfig.MaxRequestBodySizeMB
	if limitMB <= 0 {
		limitMB = lib.DefaultClientConfig.MaxRequestBodySizeMB
	}
	return int64(limitMB) << 20
}

// HandleUpgrade serves a WebSocket upgrade a passthrough route accepted. The upstream is dialed
// before the client is upgraded: an upstream that refuses the handshake is answered with its
// HTTP status instead of an upgrade the client would have to diagnose from a closed socket, and
// the upstream's handshake response headers, which carry per-connection state a client such as
// Codex reads, go out on the 101.
func (h *WSPassthroughHandler) HandleUpgrade(ctx *fasthttp.RequestCtx, target *integrations.PassthroughWebSocketTarget) {
	setupHeaders := captureSessionHeaders(ctx)

	bifrostCtx, cancel := createBifrostContextFromHeaders(h.handlerStore, setupHeaders)
	defer cancel()
	// The route is checked once here with the request every turn stands for, so a provider whose
	// policy refuses it never gets a connection.
	if bifrostErr := h.client.PreparePassthroughRelay(bifrostCtx, target.Provider, target.TurnRequest(bifrostCtx, nil)); bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	var key schemas.Key
	if !target.ForwardCallerAuth {
		selected, err := h.client.SelectKeyForProviderRequestType(bifrostCtx, schemas.PassthroughStreamRequest, target.Provider, "")
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		key = selected
	}

	upstreamURL, headers, err := h.upstreamDial(target, key)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	var proxyConfig *schemas.ProxyConfig
	if providerCfg, cfgErr := h.config.GetProviderConfigRaw(target.Provider); cfgErr == nil && providerCfg != nil {
		proxyConfig = providerCfg.ProxyConfig
	}
	upstream, upstreamHeaders, err := bfws.DialRelay(upstreamURL, headers, requestedSubprotocols(ctx), proxyConfig)
	if err != nil {
		status := fasthttp.StatusBadGateway
		var handshakeErr *bfws.HandshakeError
		if errors.As(err, &handshakeErr) && handshakeErr.StatusCode >= 400 {
			status = handshakeErr.StatusCode
		}
		logger.Warn("passthrough websocket upstream dial failed for %s%s: %v", target.Provider, target.Path, err)
		SendError(ctx, status, "upstream websocket handshake failed: "+err.Error())
		return
	}

	clientLimit := h.clientReadLimit()
	writeRelayHandshakeHeaders(ctx, upstreamHeaders, clientLimit)

	upgrader := h.upgrader
	if subprotocol := upstream.Subprotocol(); subprotocol != "" {
		upgrader.Subprotocols = []string{subprotocol}
	}
	upgraded := false
	err = upgrader.Upgrade(ctx, func(conn *ws.Conn) {
		defer conn.Close()
		defer upstream.Close()
		defer recoverWebSocketSession(conn)

		session, sessionErr := h.sessions.Create(conn)
		if sessionErr != nil {
			writeWSError(conn, 429, "websocket_connection_limit_reached", sessionErr.Error())
			return
		}
		defer h.sessions.Remove(conn)

		conn.SetReadLimit(clientLimit)
		upstream.SetReadLimit(wsPassthroughUpstreamReadLimit)

		relay := &wsPassthroughRelay{
			handler:      h,
			target:       target,
			session:      session,
			client:       conn,
			upstream:     upstream,
			setupHeaders: setupHeaders,
			key:          key,
			idleTimeout:  resolveWSStreamIdleTimeout(h.config, target.Provider),
		}
		relay.run()
	})
	upgraded = err == nil
	if !upgraded {
		upstream.Close()
		logger.Warn("websocket upgrade failed for passthrough %s%s: %v", target.Provider, target.Path, err)
	}
}

// requestedSubprotocols lists the subprotocols the client offered, to offer the upstream the same.
func requestedSubprotocols(ctx *fasthttp.RequestCtx) []string {
	offered := string(ctx.Request.Header.Peek("Sec-WebSocket-Protocol"))
	if offered == "" {
		return nil
	}
	var protocols []string
	for _, protocol := range strings.Split(offered, ",") {
		if protocol = strings.TrimSpace(protocol); protocol != "" {
			protocols = append(protocols, protocol)
		}
	}
	return protocols
}

// upstreamDial returns the upstream WebSocket URL and handshake headers for a target. A
// host-backed route dials its fixed host; any other dials the provider's own Responses WebSocket
// endpoint. The provider's headers go first and the client's safe headers over them, which never
// include an Authorization the route did not accept as the upstream credential.
func (h *WSPassthroughHandler) upstreamDial(target *integrations.PassthroughWebSocketTarget, key schemas.Key) (string, http.Header, error) {
	headers := http.Header{}
	provider := h.client.GetProviderByKey(target.Provider)
	wsProvider, _ := provider.(schemas.WebSocketCapableProvider)
	if wsProvider != nil {
		for name, value := range wsProvider.WebSocketHeaders(key) {
			headers.Set(name, value)
		}
	}
	for name, value := range target.Headers {
		headers.Set(name, value)
	}

	var upstreamURL string
	if target.UpstreamURL != "" {
		upstreamURL = webSocketScheme(target.UpstreamURL) + target.Path
	} else {
		if wsProvider == nil || !wsProvider.SupportsWebSocketMode() {
			return "", nil, errors.New("provider " + string(target.Provider) + " does not support websocket mode")
		}
		upstreamURL = wsProvider.WebSocketResponsesURL(key)
	}
	if target.RawQuery != "" {
		upstreamURL += "?" + target.RawQuery
	}
	return upstreamURL, headers, nil
}

// webSocketScheme turns an http(s) base URL into its ws(s) form.
func webSocketScheme(base string) string {
	base = strings.TrimRight(base, "/")
	if rest, ok := strings.CutPrefix(base, "https://"); ok {
		return "wss://" + rest
	}
	if rest, ok := strings.CutPrefix(base, "http://"); ok {
		return "ws://" + rest
	}
	return base
}

// writeRelayHandshakeHeaders copies the upstream's handshake response headers onto the client's
// 101, leaving out the ones that describe the upstream hop itself. The advertised message limit
// is lowered to this gateway's own when the gateway accepts less than the upstream does.
func writeRelayHandshakeHeaders(ctx *fasthttp.RequestCtx, upstream http.Header, clientLimit int64) {
	for name, values := range upstream {
		lower := strings.ToLower(name)
		switch lower {
		case "connection", "upgrade", "content-length", "content-type", "transfer-encoding",
			"set-cookie", "date", "server", "proxy-authenticate", "www-authenticate":
			continue
		}
		if strings.HasPrefix(lower, "sec-websocket-") || lib.IsGatewayOwnedResponseHeader(name) {
			continue
		}
		for _, value := range values {
			ctx.Response.Header.Add(name, value)
		}
	}
	advertised := clientLimit
	if upstreamLimit, err := strconv.ParseInt(upstream.Get(wsPassthroughMaxMessageHeader), 10, 64); err == nil && upstreamLimit > 0 && upstreamLimit < advertised {
		advertised = upstreamLimit
	}
	ctx.Response.Header.Set(wsPassthroughMaxMessageHeader, strconv.FormatInt(advertised, 10))
}

// Who ended a relay first; the answer decides how its open turns are recorded.
const (
	wsRelayOpen int32 = iota
	wsRelayClientEnded
	wsRelayUpstreamEnded
	wsRelayUpstreamTimedOut
)

// wsPassthroughRelay is one client connection and its dedicated upstream.
type wsPassthroughRelay struct {
	handler      *WSPassthroughHandler
	target       *integrations.PassthroughWebSocketTarget
	session      *bfws.Session
	client       *ws.Conn
	upstream     *ws.Conn
	setupHeaders *sessionHeaders
	key          schemas.Key
	idleTimeout  time.Duration
	ended        atomic.Int32 // wsRelay* value of the side that ended the relay

	mu    sync.Mutex
	turns []*wsPassthroughTurn // open turns, oldest first
}

// wsPassthroughTurn is one response.create message and the upstream events that answer it.
type wsPassthroughTurn struct {
	ctx        *schemas.BifrostContext
	cancel     context.CancelFunc
	hooks      *bifrost.WSStreamHooks
	rawRequest []byte
	usage      *schemas.BifrostPassthroughUsage
	start      time.Time
	lastChunk  time.Time
	chunkIndex int
	span       schemas.SpanHandle
}

// run relays until either side ends. The client-to-upstream direction runs on the caller's
// goroutine and the upstream-to-client direction on its own; the first to stop closes both
// connections, which stops the other, and turns still open are then ended as failed.
func (r *wsPassthroughRelay) run() {
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		if err := runWebSocketRelay(r.client, r.relayUpstream); err != nil {
			// A panic in a hook: nothing closed the connections yet.
			r.ended.CompareAndSwap(wsRelayOpen, wsRelayUpstreamEnded)
			_ = r.client.Close()
		}
	}()

	r.relayClient()
	_ = r.upstream.Close()
	<-upstreamDone

	switch r.ended.Load() {
	case wsRelayUpstreamTimedOut:
		r.abortTurns(newBifrostError(504, "upstream_timeout", "upstream websocket stream timed out"))
	case wsRelayUpstreamEnded:
		r.abortTurns(newBifrostError(502, "upstream_connection_error", "upstream websocket connection closed"))
	default:
		r.abortTurns(newBifrostError(499, "client_connection_error", "client websocket connection closed"))
	}
}

// relayClient forwards client messages upstream until the client connection ends. A
// response.create message opens a turn first, and is dropped with an error event to the client
// when a plugin or the provider's configuration refuses it; the connection stays usable.
func (r *wsPassthroughRelay) relayClient() {
	for {
		msgType, message, err := r.client.ReadMessage()
		if err != nil {
			if r.ended.CompareAndSwap(wsRelayOpen, wsRelayClientEnded) {
				closeRelayPeer(r.upstream, err, ws.CloseGoingAway)
			}
			if errors.Is(err, ws.ErrReadLimit) {
				drainOversizeMessage(r.client)
			}
			return
		}
		if msgType == ws.TextMessage && providerUtils.GetJSONField(message, "type").Str == string(schemas.WSEventResponseCreate) {
			var ok bool
			if message, ok = r.beginTurn(message); !ok {
				continue
			}
		}
		if err := r.upstream.WriteMessage(msgType, message); err != nil {
			r.ended.CompareAndSwap(wsRelayOpen, wsRelayUpstreamEnded)
			return
		}
	}
}

// relayUpstream forwards upstream events to the client until the upstream connection ends, and
// feeds each event to the oldest open turn. It closes the client connection on the way out.
func (r *wsPassthroughRelay) relayUpstream() error {
	defer r.client.Close()
	for {
		msgType, event, err := r.upstream.ReadMessage()
		if err != nil {
			cause := wsRelayUpstreamEnded
			if isWSReadTimeout(err) {
				cause = wsRelayUpstreamTimedOut
			}
			if r.ended.CompareAndSwap(wsRelayOpen, cause) {
				if cause == wsRelayUpstreamTimedOut {
					writeWSError(r.session, 504, "upstream_timeout", "upstream websocket stream timed out")
				}
				closeRelayPeer(r.client, err, ws.CloseInternalServerErr)
			}
			return nil
		}
		if msgType == ws.TextMessage {
			r.observe(event)
		}
		if err := r.session.WriteMessage(msgType, event); err != nil {
			r.ended.CompareAndSwap(wsRelayOpen, wsRelayClientEnded)
			return nil
		}
	}
}

// wsPassthroughOversizeDrain is how long the rest of an oversize client message is read and
// discarded before the connection is closed.
const wsPassthroughOversizeDrain = 2 * time.Second

// drainOversizeMessage discards what a client is still sending after its message passed the read
// limit. The limit already answered with a 1009 close frame, but closing a socket that has unread
// data resets it, and a reset can reach the client before the close frame does: the client then
// sees a broken connection instead of the reason. Reading on for a bounded time lets the frame
// land; nothing read here is kept.
func drainOversizeMessage(conn *ws.Conn) {
	netConn := conn.NetConn()
	if netConn == nil {
		return
	}
	_ = netConn.SetReadDeadline(time.Now().Add(wsPassthroughOversizeDrain))
	_, _ = io.Copy(io.Discard, netConn)
}

// closeRelayPeer tells the other side of a relay why the session is ending: with the close code
// its peer sent when there was one, and fallback otherwise.
func closeRelayPeer(peer *ws.Conn, err error, fallback int) {
	code, text := fallback, ""
	var closeErr *ws.CloseError
	if errors.As(err, &closeErr) {
		switch closeErr.Code {
		case ws.CloseNoStatusReceived, ws.CloseAbnormalClosure, ws.CloseTLSHandshake:
			// Codes a close frame may not carry.
		default:
			code, text = closeErr.Code, closeErr.Text
		}
	}
	_ = peer.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(code, text), time.Now().Add(time.Second))
}

// beginTurn opens the turn a response.create message starts: it builds the passthrough request the
// message stands for and runs the plugin pre-hooks on it. It returns the message to send upstream,
// which is the client's unless a plugin rewrote it, and false when the turn was refused.
func (r *wsPassthroughRelay) beginTurn(message []byte) ([]byte, bool) {
	h := r.handler
	ctx, cancel := createBifrostContextFromHeaders(h.handlerStore, r.setupHeaders)
	if parentRequestID, _ := ctx.Value(schemas.BifrostContextKeyParentRequestID).(string); parentRequestID == "" {
		ctx.SetValue(schemas.BifrostContextKeyParentRequestID, r.session.ID())
	}
	req := r.target.TurnRequest(ctx, message)
	if bifrostErr := h.client.PreparePassthroughRelay(ctx, r.target.Provider, req); bifrostErr != nil {
		cancel()
		writeWSBifrostError(r.session, bifrostErr)
		return nil, false
	}
	// The upstream connection was dialed with one key, so every turn on it runs on that key.
	if r.key.ID != "" {
		if req.Model != "" && !h.client.KeySupportsModel(r.target.Provider, r.key, req.Model) {
			cancel()
			writeWSError(r.session, 400, "invalid_request_error", "model "+req.Model+" is not served by the key this connection was opened with; reconnect to use it")
			return nil, false
		}
		ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, r.key.ID)
		ctx.SetValue(schemas.BifrostContextKeySelectedKeyName, r.key.Name)
	}

	bifrostReq := &schemas.BifrostRequest{RequestType: schemas.PassthroughStreamRequest, PassthroughRequest: req}
	hooks, bifrostErr := h.client.RunStreamPreHooks(ctx, bifrostReq)
	if bifrostErr != nil {
		cancel()
		writeWSBifrostError(r.session, bifrostErr)
		return nil, false
	}
	if hooks.ShortCircuitResponse != nil {
		// A relay forwards provider events and cannot render a plugin's typed response as one.
		hooks.Cleanup()
		cancel()
		writeWSError(r.session, 501, "server_error", "a plugin answered this request directly, which websocket passthrough cannot deliver")
		return nil, false
	}
	if hooks.Request != nil && hooks.Request.PassthroughRequest != nil {
		req = hooks.Request.PassthroughRequest
	}

	now := time.Now()
	turn := &wsPassthroughTurn{ctx: ctx, cancel: cancel, hooks: hooks, rawRequest: req.Body, start: now, lastChunk: now}
	turn.startSpan(hooks.Request, r.target.Provider, req.Model)

	r.mu.Lock()
	if len(r.turns) == 0 {
		_ = r.upstream.SetReadDeadline(now.Add(r.idleTimeout))
	}
	r.turns = append(r.turns, turn)
	r.mu.Unlock()
	return req.Body, true
}

// observe feeds one upstream event to the oldest open turn: usage is read from the events that
// carry it, the event goes through the turn's post-hooks, and a terminal event closes the turn.
// The upstream idle deadline is armed only while a turn is open, so a quiet connection between
// turns is never timed out.
func (r *wsPassthroughRelay) observe(event []byte) {
	r.mu.Lock()
	if len(r.turns) == 0 {
		r.mu.Unlock()
		return
	}
	turn := r.turns[0]
	terminal := openai.EndsOpenAIPassthroughStream(event)
	if terminal {
		r.turns[0] = nil
		r.turns = r.turns[1:]
	}
	if len(r.turns) == 0 {
		_ = r.upstream.SetReadDeadline(time.Time{})
	} else {
		_ = r.upstream.SetReadDeadline(time.Now().Add(r.idleTimeout))
	}
	r.mu.Unlock()

	turn.chunk(r.target.Path, event)
	if terminal {
		turn.finish(r.target.Path, event)
	}
}

// abortTurns ends every turn still open when the relay stops.
func (r *wsPassthroughRelay) abortTurns(bifrostErr *schemas.BifrostError) {
	r.mu.Lock()
	turns := r.turns
	r.turns = nil
	r.mu.Unlock()
	for _, turn := range turns {
		turn.fail(bifrostErr)
	}
}

// startSpan opens the llm.call span of a turn. The relay bypasses the core request path that
// starts one, and span-based observability connectors export a turn without it unattributed.
func (t *wsPassthroughTurn) startSpan(req *schemas.BifrostRequest, provider schemas.ModelProvider, model string) {
	tracer, _ := t.ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
	traceID, _ := t.ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	if tracer == nil || traceID == "" || req == nil {
		return
	}
	otelOp := schemas.OTelOperationName(schemas.PassthroughStreamRequest)
	spanID, handle := tracer.StartSpanID(t.ctx, otelOp+" "+model, schemas.SpanKindLLMCall)
	t.span = handle
	if span := tracer.SpanFromHandle(handle); span != nil {
		span.SetAttribute(schemas.AttrProviderName, schemas.OTelProviderName(provider))
		span.SetAttribute(schemas.AttrBifrostProviderName, string(provider))
		span.SetAttribute(schemas.AttrRequestModel, model)
		span.SetAttribute(schemas.AttrOperationName, otelOp)
		span.SetAttribute(schemas.AttrLegacyRequestType, string(schemas.PassthroughStreamRequest))
	}
	tracer.PopulateLLMRequestAttributes(handle, req)
	if spanID != "" {
		t.ctx.SetValue(schemas.BifrostContextKeySpanID, spanID)
	}
}

// endSpan closes the turn's llm.call span before the final post-hook flushes the trace.
func (t *wsPassthroughTurn) endSpan(resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) {
	tracer, _ := t.ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
	if tracer == nil || t.span == nil {
		return
	}
	tracer.PopulateLLMResponseAttributes(t.ctx, t.span, resp, bifrostErr)
	if bifrostErr != nil {
		tracer.EndSpan(t.span, schemas.SpanStatusError, "request failed")
	} else {
		tracer.EndSpan(t.span, schemas.SpanStatusOk, "")
	}
	t.span = nil
}

// chunk runs one upstream event through the turn's post-hooks. Hooks receive it framed as the SSE
// data event the HTTP route streams, so log accumulation and parsing treat both transports alike.
func (t *wsPassthroughTurn) chunk(path string, event []byte) {
	if openai.HasOpenAIPassthroughUsage(event) {
		if usage := openai.ExtractOpenAIPassthroughUsage(fasthttp.MethodPost, path, t.rawRequest, event); usage != nil {
			t.usage = usage
		}
	}
	now := time.Now()
	resp := &schemas.BifrostResponse{PassthroughResponse: &schemas.BifrostPassthroughResponse{
		StatusCode: fasthttp.StatusOK,
		Body:       sseDataFrame(event),
		ExtraFields: schemas.BifrostResponseExtraFields{
			PassthroughPath: path,
			ChunkIndex:      t.chunkIndex,
			Latency:         now.Sub(t.lastChunk).Milliseconds(),
		},
	}}
	t.lastChunk = now
	t.chunkIndex++
	if tracer, ok := t.ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer); ok && tracer != nil {
		if traceID, _ := t.ctx.Value(schemas.BifrostContextKeyTraceID).(string); traceID != "" {
			tracer.AddStreamingChunk(traceID, resp)
		}
	}
	if _, postErr := t.hooks.PostHookRunner(t.ctx, resp, nil); postErr != nil {
		logger.Warn("passthrough websocket post-hook returned an error: %v", postErr)
	}
}

// sseDataFrame frames one JSON event as an SSE data event.
func sseDataFrame(event []byte) []byte {
	frame := make([]byte, 0, len(event)+8)
	frame = append(frame, "data: "...)
	frame = append(frame, event...)
	return append(frame, "\n\n"...)
}

// finish ends a turn on its terminal event. An error event is the WebSocket form of an HTTP error
// response, so the turn is recorded as failed with the status and message the upstream gave;
// every other terminal event ends a served stream, with the usage observed along the way.
func (t *wsPassthroughTurn) finish(path string, event []byte) {
	if providerUtils.GetJSONField(event, "type").Str == string(schemas.WSEventError) {
		status := int(providerUtils.GetJSONField(event, "status").Int())
		if status < 400 {
			status = fasthttp.StatusBadGateway
		}
		code := providerUtils.GetJSONField(event, "error.code").Str
		if code == "" {
			code = providerUtils.GetJSONField(event, "error.type").Str
		}
		message := providerUtils.GetJSONField(event, "error.message").Str
		if message == "" {
			message = "upstream returned an error event"
		}
		bifrostErr := newBifrostError(status, code, message)
		bifrostErr.IsBifrostError = false
		t.fail(bifrostErr)
		return
	}
	t.ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	final := &schemas.BifrostResponse{PassthroughResponse: &schemas.BifrostPassthroughResponse{
		StatusCode: fasthttp.StatusOK,
		ExtraFields: schemas.BifrostResponseExtraFields{
			PassthroughPath: path,
			ChunkIndex:      t.chunkIndex,
			Latency:         time.Since(t.start).Milliseconds(),
			RawRequest:      t.rawRequest,
		},
		PassthroughUsage: t.usage,
	}}
	t.endSpan(final, nil)
	if _, postErr := t.hooks.PostHookRunner(t.ctx, final, nil); postErr != nil {
		logger.Warn("passthrough websocket final post-hook returned an error: %v", postErr)
	}
	t.release()
}

// fail ends a turn that produced no terminal response.
func (t *wsPassthroughTurn) fail(bifrostErr *schemas.BifrostError) {
	t.ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	t.endSpan(nil, bifrostErr)
	if _, postErr := t.hooks.PostHookRunner(t.ctx, nil, bifrostErr); postErr != nil && postErr != bifrostErr {
		logger.Warn("passthrough websocket post-hook returned an error: %v", postErr)
	}
	t.release()
}

// release frees what the turn held: the plugin pipeline, the context and the request body.
func (t *wsPassthroughTurn) release() {
	t.hooks.Cleanup()
	t.cancel()
	t.rawRequest = nil
}
