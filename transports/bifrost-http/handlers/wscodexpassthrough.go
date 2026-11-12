package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fasthttp/router"
	ws "github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/v2/bifrost-http/websocket"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

const (
	// codexMaxOpenTurns matches the upstream's cap on in-flight responses per connection.
	codexMaxOpenTurns = 16
	// codexMaxClientFrameBytes caps a client frame when the upstream advertises no smaller limit.
	codexMaxClientFrameBytes = 16 << 20
	// codexMaxRefusalBodyBytes bounds the upstream refusal body mirrored to the client.
	codexMaxRefusalBodyBytes = 64 << 10
	// codexMaxRetainedBytes bounds the request frames one connection holds for its open turns;
	// each is kept until its response ends, because usage is read against it.
	codexMaxRetainedBytes = 64 << 20
	// codexMaxSessionsPerCaller bounds the sockets one caller holds open, so one caller cannot
	// take every slot of the shared connection limit. Codex opens a few per session.
	codexMaxSessionsPerCaller = 32

	// codexHijackWait bounds how long an accepted upgrade waits for the server to hand over the
	// connection. Past it the server is taken never to (the 101 could not be written), and the
	// upstream socket and the caller's share are given back.
	codexHijackWait = 30 * time.Second
	// codexIdleSessionTimeout closes a socket with no open turn and no traffic for this long. Codex
	// reconnects for its next turn and resends its full input.
	codexIdleSessionTimeout = 15 * time.Minute
	codexIdleCheckInterval  = time.Minute

	codexMaxMessageBytesHeader = "x-codex-websocket-max-message-bytes"

	codexTooManyTurnsMessage    = "this connection already has the maximum number of responses in flight"
	codexTooMuchRetainedMessage = "this connection already holds the maximum request data in flight; wait for a response to finish"
)

// codexUpgradeResponseHeaders are the upstream handshake headers Codex reads from the 101.
var codexUpgradeResponseHeaders = []string{
	"x-codex-turn-state",
	"openai-model",
	"x-reasoning-included",
	codexMaxMessageBytesHeader,
	"x-models-etag",
	"x-request-id",
	"x-oai-request-id",
	"cf-ray",
}

// codexRefusalResponseHeaders are the upstream headers kept when a refused handshake is mirrored.
var codexRefusalResponseHeaders = []string{"content-type", "retry-after", "x-request-id", "x-oai-request-id", "cf-ray"}

// WSCodexPassthroughHandler relays Codex's Responses WebSocket on the ChatGPT passthrough to
// chatgpt.com, signed in with the caller's own ChatGPT token. Frames cross unchanged in both
// directions; Bifrost reads each response.create to run the turn through the plugin pipeline
// (governance, logging, usage) the way the HTTP route runs each POST, and reads the upstream
// events only to attribute them to their turn.
type WSCodexPassthroughHandler struct {
	client       *bifrost.Bifrost
	config       *lib.Config
	handlerStore lib.HandlerStore
	sessions     *bfws.SessionManager
	maxSessions  int
	upgrader     ws.FastHTTPUpgrader
	upstreamURL  string

	idleTimeout   time.Duration // codexIdleSessionTimeout; tests shorten it
	idleCheck     time.Duration // codexIdleCheckInterval; tests shorten it
	maxRetained   int           // codexMaxRetainedBytes; tests shorten it
	maxPerCaller  int           // codexMaxSessionsPerCaller; tests shorten it
	hijackWait    time.Duration // codexHijackWait; tests shorten it
	reserved      atomic.Int64  // connection slots held from the cap check until the session exists
	callersMu     sync.Mutex
	callerSockets map[string]int
}

// newWSCodexPassthroughHandler creates the relay. It is built next to the Responses WebSocket
// handler and shares its connection limit setting.
func newWSCodexPassthroughHandler(client *bifrost.Bifrost, config *lib.Config, handlerStore lib.HandlerStore) *WSCodexPassthroughHandler {
	maxConns := 0
	if config != nil && config.WebSocketConfig != nil {
		maxConns = config.WebSocketConfig.MaxConnections
	}
	return &WSCodexPassthroughHandler{
		client:       client,
		config:       config,
		handlerStore: handlerStore,
		sessions:     bfws.NewSessionManager(maxConns),
		maxSessions:  maxConns,
		upgrader: ws.FastHTTPUpgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
				origin := string(ctx.Request.Header.Peek("Origin"))
				if origin == "" {
					return true
				}
				return config != nil && config.ClientConfig != nil && IsOriginAllowed(origin, config.ClientConfig.AllowedOrigins)
			},
		},
		upstreamURL:   integrations.ChatGPTCodexResponsesWebSocketURL,
		idleTimeout:   codexIdleSessionTimeout,
		idleCheck:     codexIdleCheckInterval,
		maxRetained:   codexMaxRetainedBytes,
		maxPerCaller:  codexMaxSessionsPerCaller,
		hijackWait:    codexHijackWait,
		callerSockets: make(map[string]int),
	}
}

// codexCallerKey names who holds a socket for the per-caller limit, from what the admission
// pipeline recorded on the grant: the user the request is attributed to first, so one user cannot
// multiply its share across the keys it holds; then the virtual key the presented key resolved
// to, only while that key may be used; else the peer address. The x-bf-vk header itself is never
// the key: a value nothing resolved (even one sent beside a direct key, which admits the request
// on its own) or a dead key's row could be a new name on every socket. The ChatGPT token is not
// used, for the same reason.
func codexCallerKey(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) string {
	if g := bifrostCtx.Grant(); g != nil {
		if identity := g.Identity(); identity != nil {
			if user := identity.User(); user != nil && user.ID != "" {
				return "user:" + user.ID
			}
		}
	}
	if vk := governance.ResolvedVirtualKeyID(bifrostCtx); vk != "" {
		return "vk:" + vk
	}
	return "ip:" + ctx.RemoteIP().String()
}

// reserveSession holds one slot of the connection limit from the admission check until the
// relayed session exists, so handshakes racing for the last slot are refused with 429 before the
// upgrade rather than upgraded and then closed. The returned release is safe to call more than once.
func (h *WSCodexPassthroughHandler) reserveSession() (func(), bool) {
	if h.maxSessions <= 0 {
		return func() {}, true
	}
	if int(h.reserved.Add(1))+h.sessions.Count() > h.maxSessions {
		h.reserved.Add(-1)
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(func() { h.reserved.Add(-1) }) }, true
}

// acquireCallerSocket takes one of the caller's sockets, or reports that the caller holds them
// all. The returned release gives it back and is safe to call more than once.
func (h *WSCodexPassthroughHandler) acquireCallerSocket(key string) (func(), bool) {
	h.callersMu.Lock()
	defer h.callersMu.Unlock()
	if h.callerSockets[key] >= h.maxPerCaller {
		return nil, false
	}
	h.callerSockets[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.callersMu.Lock()
			defer h.callersMu.Unlock()
			if h.callerSockets[key] <= 1 {
				delete(h.callerSockets, key)
				return
			}
			h.callerSockets[key]--
		})
	}, true
}

// RegisterRoutes registers the upgrade on the Codex responses path. POST on the same path stays
// with the ChatGPT passthrough router.
func (h *WSCodexPassthroughHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET(integrations.ChatGPTCodexResponsesRoute, lib.ChainMiddlewares(h.handleUpgrade, middlewares...))
}

// Close ends every relayed session.
func (h *WSCodexPassthroughHandler) Close() {
	if h != nil && h.sessions != nil {
		h.sessions.CloseAll()
	}
}

// handleUpgrade answers everything it can with a plain HTTP status before upgrading, because
// that is what Codex acts on: 426 sends it to HTTP for the rest of the session, 401 refreshes its
// token, and any other refusal is retried and then falls back. The upstream is dialed first so
// its refusal reaches Codex unchanged and its handshake headers ride the client's 101.
func (h *WSCodexPassthroughHandler) handleUpgrade(ctx *fasthttp.RequestCtx) {
	if !ws.FastHTTPIsWebSocketUpgrade(ctx) {
		ctx.Response.Header.Set("Allow", fasthttp.MethodPost)
		SendBifrostError(ctx, newBifrostError(fasthttp.StatusMethodNotAllowed, "method_not_allowed", "GET on this route only opens a WebSocket; send responses as POST"))
		return
	}
	safeHeaders, callerAuth := integrations.PassthroughSafeHeaders(ctx)
	if !integrations.IsChatGPTBearer(callerAuth) {
		// Only the caller's ChatGPT token can sign in to chatgpt.com, so without one there is
		// nothing to relay. 426 moves Codex to HTTP at once, where the route's usual rules apply.
		SendBifrostError(ctx, newBifrostError(fasthttp.StatusUpgradeRequired, "upgrade_required", "the Codex WebSocket relay needs a ChatGPT sign-in token; send this request over HTTP"))
		return
	}
	if _, status, err := integrations.ResolveChatGPTPassthroughProvider(ctx, h.handlerStore); err != nil {
		SendBifrostError(ctx, newBifrostError(status, "invalid_request_error", err.Error()))
		return
	}

	admissionCtx, admissionCancel := lib.ConvertToBifrostContext(ctx, h.handlerStore)
	defer admissionCancel()
	if h.enforceAuthOnInference() && !governance.PresentedAnyCredential(admissionCtx) {
		SendBifrostError(ctx, newBifrostError(fasthttp.StatusUnauthorized, "invalid_request_error", "authentication is required: provide a virtual key in x-bf-vk"))
		return
	}
	// Resolve the presented credential before anything is dialed, as realtime admission does, so
	// a forged or revoked key is refused here and the per-caller limit keys on a checked identity.
	// Governance does not resolve passthrough request types in its pre-request hook, so the check
	// asks as the websocket responses route would. It runs on a child context, so nothing it
	// stamps reaches the turns, which run the full pipeline themselves. Once per socket.
	callerCtx, callerCancel := schemas.NewBifrostContextWithCancel(admissionCtx)
	defer callerCancel()
	h.client.RunPreRequestHooks(callerCtx, &schemas.BifrostRequest{
		RequestType:      schemas.WebSocketResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI},
	})
	if h.enforceAuthOnInference() && !governance.PresentedCredentialResolved(callerCtx) {
		SendBifrostError(ctx, newBifrostError(fasthttp.StatusUnauthorized, "invalid_request_error", realtimeUnresolvedCredentialMessage))
		return
	}
	releaseSlot, ok := h.reserveSession()
	if !ok {
		SendBifrostError(ctx, newBifrostError(fasthttp.StatusTooManyRequests, "websocket_connection_limit_reached", bfws.ErrConnectionLimitReached.Error()))
		return
	}
	releaseCaller, ok := h.acquireCallerSocket(codexCallerKey(ctx, callerCtx))
	if !ok {
		releaseSlot()
		SendBifrostError(ctx, newBifrostError(fasthttp.StatusTooManyRequests, "websocket_connection_limit_reached", "this caller already holds the maximum number of Codex websockets"))
		return
	}

	upstream, upgradeHeaders, refusal := h.dialUpstream(safeHeaders, callerAuth)
	if refusal != nil {
		releaseCaller()
		releaseSlot()
		refusal(ctx)
		return
	}
	for name, value := range upgradeHeaders {
		ctx.Response.Header.Set(name, value)
	}

	setupHeaders := captureSessionHeaders(ctx)
	middlewareValues := snapshotRealtimeMiddlewareValuesWithContext(ctx, admissionCtx)
	turnHeaders := make(map[string]string, len(safeHeaders)+1)
	for name, value := range safeHeaders {
		if !isCodexHandshakeOnlyHeader(name) {
			turnHeaders[name] = value
		}
	}
	turnHeaders["authorization"] = callerAuth

	// fasthttp calls the upgrade callback only after it has written the 101, and skips it when that
	// write fails or the connection was marked to close. Whichever of the callback and the watchdog
	// takes the socket first owns the upstream socket, the caller's share and the slot; the
	// watchdog gives them back when the callback never comes.
	const (
		handoverPending int32 = iota
		handoverTaken
		handoverAbandoned
	)
	var handover atomic.Int32
	abandon := func() {
		if handover.CompareAndSwap(handoverPending, handoverAbandoned) {
			_ = upstream.Close()
			releaseCaller()
			releaseSlot()
		}
	}
	watchdog := time.AfterFunc(h.hijackWait, abandon)
	err := h.upgrader.Upgrade(ctx, func(conn *ws.Conn) {
		if !handover.CompareAndSwap(handoverPending, handoverTaken) {
			_ = conn.Close()
			return
		}
		watchdog.Stop()
		defer releaseCaller()
		defer conn.Close()
		defer recoverWebSocketSession(conn)
		defer upstream.Close()

		session, sessionErr := h.sessions.Create(conn)
		releaseSlot()
		if sessionErr != nil {
			_ = conn.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(ws.CloseTryAgainLater, sessionErr.Error()), time.Now().Add(realtimeWSWriteTimeout))
			return
		}
		defer h.sessions.Remove(conn)
		conn.SetReadLimit(codexClientReadLimit(upgradeHeaders[codexMaxMessageBytesHeader]))

		relay := &codexRelay{
			handler:          h,
			client:           newRealtimeClientConn(conn),
			upstream:         upstream,
			sessionID:        session.ID(),
			setupHeaders:     setupHeaders,
			middlewareValues: middlewareValues,
			turnHeaders:      turnHeaders,
			upstreamHeaders:  upgradeHeaders,
			idleTimeout:      resolveWSStreamIdleTimeout(h.config, schemas.OpenAI),
		}
		relay.run()
	})
	if err != nil {
		watchdog.Stop()
		abandon()
		logger.Warn("websocket upgrade failed for %s: %v", integrations.ChatGPTCodexResponsesRoute, err)
	}
}

func (h *WSCodexPassthroughHandler) enforceAuthOnInference() bool {
	return h.config != nil && h.config.ClientConfig != nil && h.config.ClientConfig.EnforceAuthOnInference
}

// dialUpstream opens the chatgpt.com socket with the caller's token. On failure it returns the
// response to send instead of upgrading: the upstream's own refusal when it answered, 502 when
// it could not be reached.
func (h *WSCodexPassthroughHandler) dialUpstream(safeHeaders map[string]string, callerAuth string) (*ws.Conn, map[string]string, func(*fasthttp.RequestCtx)) {
	headers := http.Header{}
	var proxyConfig *schemas.ProxyConfig
	if h.config != nil {
		if providerCfg, err := h.config.GetProviderConfigRaw(schemas.OpenAI); err == nil && providerCfg != nil {
			proxyConfig = providerCfg.ProxyConfig
			// Operator headers go first so the client's own win, as on the HTTP route.
			if providerCfg.NetworkConfig != nil {
				for name, value := range providerCfg.NetworkConfig.ExtraHeaders {
					if !isCodexHandshakeOnlyHeader(strings.ToLower(name)) && !isSecurityDeniedExtraHeader(name) {
						headers.Set(name, value.GetValue())
					}
				}
			}
		}
	}
	for name, value := range safeHeaders {
		if !isCodexHandshakeOnlyHeader(name) {
			headers.Set(name, value)
		}
	}
	headers.Set("Authorization", callerAuth)

	conn, resp, err := bfws.Dial(h.upstreamURL, headers, proxyConfig)
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			return nil, nil, mirrorUpstreamRefusal(resp)
		}
		logger.Warn("codex websocket relay could not reach the upstream: %v", err)
		return nil, nil, func(ctx *fasthttp.RequestCtx) {
			SendBifrostError(ctx, newBifrostError(fasthttp.StatusBadGateway, "upstream_connection_error", "could not open the upstream websocket"))
		}
	}
	upgradeHeaders := make(map[string]string, len(codexUpgradeResponseHeaders))
	for _, name := range codexUpgradeResponseHeaders {
		if value := resp.Header.Get(name); value != "" {
			upgradeHeaders[name] = value
		}
	}
	return conn, upgradeHeaders, nil
}

// mirrorUpstreamRefusal returns a writer that answers the client with the upstream's refused
// handshake: its status, body and the headers that explain it, so Codex reacts to chatgpt.com's
// answer (a 401 refreshes its token) rather than to a gateway rewrite of it.
func mirrorUpstreamRefusal(resp *http.Response) func(*fasthttp.RequestCtx) {
	var body []byte
	if resp.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(resp.Body, codexMaxRefusalBodyBytes))
		_ = resp.Body.Close()
	}
	status := resp.StatusCode
	headers := make(map[string]string, len(codexRefusalResponseHeaders))
	for _, name := range codexRefusalResponseHeaders {
		if value := resp.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	return func(ctx *fasthttp.RequestCtx) {
		for name, value := range headers {
			ctx.Response.Header.Set(name, value)
		}
		ctx.SetStatusCode(status)
		ctx.SetBody(body)
	}
}

// isCodexHandshakeOnlyHeader reports a header that belongs to the client's own handshake or
// connection and must not be copied onto the upstream dial or a turn's request headers.
func isCodexHandshakeOnlyHeader(name string) bool {
	if strings.HasPrefix(name, "sec-websocket-") {
		return true
	}
	switch name {
	case "authorization", "upgrade", "connection", "origin", "keep-alive", "te", "host", "content-length", "content-type":
		return true
	}
	return false
}

// codexClientReadLimit is the largest client frame the relay accepts: the upstream's advertised
// limit when it sets one, never above codexMaxClientFrameBytes. A larger frame closes the
// connection with 1009, which Codex answers by retrying the turn over HTTP.
func codexClientReadLimit(advertised string) int64 {
	if n, err := strconv.ParseInt(strings.TrimSpace(advertised), 10, 64); err == nil && n > 0 && n < codexMaxClientFrameBytes {
		return n
	}
	return codexMaxClientFrameBytes
}

// codexTurn is one response.create on a relayed connection, from the frame that started it to
// the upstream event that ended it. Only the upstream relay touches it once it is queued.
type codexTurn struct {
	ctx        *schemas.BifrostContext
	cancel     context.CancelFunc
	hooks      *bifrost.WSStreamHooks
	body       []byte
	path       string
	lane       string
	responseID string
	start      time.Time
	last       time.Time
	chunks     int
	usage      *schemas.BifrostPassthroughUsage
	// interrupted is set when the upstream accepts the client's interrupt. The turn stays open
	// for the terminal event that follows, which still carries the usage to bill.
	interrupted bool
}

const (
	codexClientSide   int32 = 1
	codexUpstreamSide int32 = 2
)

// codexRelay is one relayed Codex connection.
type codexRelay struct {
	handler          *WSCodexPassthroughHandler
	client           *realtimeClientConn
	upstream         *ws.Conn
	upstreamWriteMu  sync.Mutex
	sessionID        string
	setupHeaders     *sessionHeaders
	middlewareValues map[any]any
	turnHeaders      map[string]string
	upstreamHeaders  map[string]string
	idleTimeout      time.Duration
	endedFirst       atomic.Int32 // which relay ended first: codexClientSide or codexUpstreamSide
	lastActivity     atomic.Int64 // unix nanos of the last frame in either direction

	mu       sync.Mutex
	open     []*codexTurn // in the order their response.create arrived
	retained int          // bytes of request frames the open turns hold
}

// run relays until either side ends, then settles every turn still open so none is left
// without a log row.
func (r *codexRelay) run() {
	r.client.startHeartbeat()
	defer r.client.stopHeartbeat()
	r.touch()
	stopIdle := make(chan struct{})
	idleDone := make(chan struct{})
	go r.closeWhenIdle(stopIdle, idleDone)
	defer func() {
		close(stopIdle)
		<-idleDone
	}()

	relayErr := runRealtimeRelays(r.client.conn, r.relayClientToUpstream, r.relayUpstreamToClient, func() {
		_ = r.client.Close()
		_ = r.upstream.Close()
	})

	status, code, message := fasthttp.StatusBadGateway, "upstream_connection_error", "upstream websocket closed before the response completed"
	if r.endedFirst.Load() == codexClientSide {
		status, code, message = 499, "client_closed_request", "client websocket closed before the response completed"
	}
	r.mu.Lock()
	open := r.open
	r.open = nil
	r.retained = 0
	r.mu.Unlock()
	for _, turn := range open {
		r.finishTurnWithError(turn, newBifrostError(status, code, message))
	}
	if relayErr != nil {
		logger.Warn("codex websocket relay ended: %v", relayErr)
	}
}

func (r *codexRelay) touch() {
	r.lastActivity.Store(time.Now().UnixNano())
}

// closeWhenIdle ends a session that has no open turn and has carried no frame for the idle
// timeout, so an abandoned socket does not hold a connection slot and an upstream socket forever.
func (r *codexRelay) closeWhenIdle(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(r.handler.idleCheck)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		r.mu.Lock()
		idle := len(r.open) == 0 && time.Since(time.Unix(0, r.lastActivity.Load())) >= r.handler.idleTimeout
		r.mu.Unlock()
		if !idle {
			continue
		}
		deadline := time.Now().Add(realtimeCloseHandshakeGrace)
		_ = r.client.conn.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(ws.CloseNormalClosure, "idle"), deadline)
		_ = r.upstream.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(ws.CloseNormalClosure, "idle"), deadline)
		_ = r.client.Close()
		_ = r.upstream.Close()
		return
	}
}

// classifyCodexClientFrame decides what a client frame is, failing closed. Codex sends two
// events: response.create, which starts a turn and runs through the pipeline, and
// response.interrupt, which is forwarded. Anything else, a binary frame, or a frame the relay
// might read differently from chatgpt.com (invalid JSON, a repeated top-level key) is refused,
// so no frame can reach the upstream as a request the pipeline did not see.
func classifyCodexClientFrame(messageType int, data []byte) (schemas.WebSocketEventType, error) {
	if messageType != ws.TextMessage {
		return "", errors.New("only text frames are accepted")
	}
	if !gjson.ValidBytes(data) {
		return "", errors.New("event is not valid JSON")
	}
	root := gjson.ParseBytes(data)
	if !root.IsObject() {
		return "", errors.New("event must be a JSON object")
	}
	var (
		kind      schemas.WebSocketEventType
		duplicate string
		badType   bool
	)
	seen := make(map[string]struct{}, 16)
	root.ForEach(func(key, value gjson.Result) bool {
		name := key.String()
		if _, ok := seen[name]; ok {
			duplicate = name
			return false
		}
		seen[name] = struct{}{}
		if name == "type" {
			if value.Type != gjson.String {
				badType = true
				return false
			}
			kind = schemas.WebSocketEventType(value.Str)
		}
		return true
	})
	switch {
	case duplicate != "":
		return "", fmt.Errorf("event repeats the key %q", duplicate)
	case badType:
		return "", errors.New("event type must be a string")
	}
	switch kind {
	case schemas.WSEventResponseCreate, schemas.WSEventResponseInterrupt:
		return kind, nil
	}
	return "", fmt.Errorf("unsupported event type %q", kind)
}

// relayClientToUpstream forwards client frames, running each response.create through the
// pipeline first.
func (r *codexRelay) relayClientToUpstream() error {
	defer r.endedFirst.CompareAndSwap(0, codexClientSide)
	for {
		messageType, data, err := r.client.ReadMessage()
		if err != nil {
			var closeErr *ws.CloseError
			if errors.As(err, &closeErr) && closeErr.Code != ws.CloseAbnormalClosure && closeErr.Code != ws.CloseNoStatusReceived && closeErr.Code != ws.CloseTLSHandshake {
				_ = r.upstream.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(closeErr.Code, closeErr.Text), time.Now().Add(realtimeWSWriteTimeout))
				return nil
			}
			// Codex lets a socket go by dropping the connection, without a close frame, so a
			// client that vanishes has ended its session; it is not a relay failure.
			if isNormalWebSocketClosure(err) || r.client.closing() || isResponsesEOFClose(err) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		r.touch()
		kind, classifyErr := classifyCodexClientFrame(messageType, data)
		if classifyErr != nil {
			writeWSError(r.client, fasthttp.StatusBadRequest, "invalid_request_error", classifyErr.Error())
			continue
		}
		if kind == schemas.WSEventResponseCreate {
			if err := r.startTurn(data); err != nil {
				return err
			}
			continue
		}
		if err := r.writeUpstream(ws.TextMessage, data); err != nil {
			return err
		}
	}
}

// startTurn runs a response.create through the pipeline and forwards it. A refusal is answered
// in-band with an error event and the connection stays open, as an HTTP refusal leaves Codex
// free to send its next request.
func (r *codexRelay) startTurn(frame []byte) error {
	lane := providerUtils.GetJSONField(frame, "stream_id").Str
	r.mu.Lock()
	full := len(r.open) >= codexMaxOpenTurns
	tooLarge := r.retained+len(frame) > r.handler.maxRetained
	r.mu.Unlock()
	if full {
		r.writeTurnError(lane, fasthttp.StatusBadRequest, "too_many_in_flight_responses", codexTooManyTurnsMessage)
		return nil
	}
	if tooLarge {
		r.writeTurnError(lane, fasthttp.StatusBadRequest, "too_many_in_flight_responses", codexTooMuchRetainedMessage)
		return nil
	}

	turnCtx, cancel := r.newTurnContext()
	passthroughReq, err := integrations.NewChatGPTCodexTurn(turnCtx, frame, r.turnHeaders)
	if err != nil {
		cancel()
		r.writeTurnError(lane, fasthttp.StatusBadRequest, "invalid_request_error", err.Error())
		return nil
	}
	req := &schemas.BifrostRequest{RequestType: schemas.PassthroughStreamRequest, PassthroughRequest: passthroughReq}
	h := r.handler
	applyRealtimeRawStorageContext(turnCtx, h.client.ComputeRawStorageForProvider(turnCtx, schemas.OpenAI))
	h.client.RunPreRequestHooks(turnCtx, req)
	hooks, preErr := h.client.RunStreamPreHooks(turnCtx, req)
	if preErr != nil {
		cancel()
		status, code, message := wsBifrostErrorFields(preErr)
		r.writeTurnError(lane, status, code, message)
		return nil
	}
	if hooks.ShortCircuitResponse != nil {
		cancel()
		if passthrough := hooks.ShortCircuitResponse.PassthroughResponse; passthrough != nil && len(passthrough.Body) > 0 {
			return r.client.WriteMessage(ws.TextMessage, passthrough.Body)
		}
		// Codex waits for an answer to every response.create, so an empty one is still an error.
		r.writeTurnError(lane, fasthttp.StatusInternalServerError, "server_error", "the request was answered without a response body")
		return nil
	}

	// A plugin may have rewritten the body, so the size bound and the lane are read from the body
	// that is sent: chatgpt.com answers on the lane that body names.
	body := frame
	if rewritten := hooks.Request; rewritten != nil && rewritten.PassthroughRequest != nil && len(rewritten.PassthroughRequest.Body) > 0 {
		body = rewritten.PassthroughRequest.Body
		lane = providerUtils.GetJSONField(body, "stream_id").Str
	}
	now := time.Now()
	turn := &codexTurn{
		ctx:    turnCtx,
		cancel: cancel,
		hooks:  hooks,
		body:   body,
		path:   passthroughReq.Path,
		lane:   lane,
		start:  now,
		last:   now,
	}
	// Queued before the write, so an answer racing the write still finds its turn.
	r.mu.Lock()
	if r.retained+len(turn.body) > r.handler.maxRetained {
		r.mu.Unlock()
		refusal := newBifrostError(fasthttp.StatusBadRequest, "too_many_in_flight_responses", codexTooMuchRetainedMessage)
		r.finishTurnWithError(turn, refusal)
		r.writeTurnError(lane, fasthttp.StatusBadRequest, "too_many_in_flight_responses", codexTooMuchRetainedMessage)
		return nil
	}
	r.open = append(r.open, turn)
	r.retained += len(turn.body)
	_ = r.upstream.SetReadDeadline(now.Add(r.idleTimeout))
	r.mu.Unlock()

	if err := r.writeUpstream(ws.TextMessage, body); err != nil {
		if r.removeTurn(turn) {
			r.finishTurnWithError(turn, newBifrostError(fasthttp.StatusBadGateway, "upstream_connection_error", "could not forward the request to the upstream websocket"))
		}
		return err
	}
	return nil
}

// writeTurnError answers a turn with an error event naming the turn's stream_id lane, so Codex
// matches the error to the request it sent on that lane. A turn on the default lane gets none.
func (r *codexRelay) writeTurnError(lane string, status int, code, message string) {
	data, err := wsErrorEvent(status, code, message)
	if err != nil {
		return
	}
	if lane != "" {
		if withLane, setErr := providerUtils.SetJSONField(data, "stream_id", lane); setErr == nil {
			data = withLane
		}
	}
	_ = r.client.WriteMessage(ws.TextMessage, data)
}

// newTurnContext gives each turn its own request id and trace, with the identity the upgrade
// settled, the way each realtime turn is built.
func (r *codexRelay) newTurnContext() (*schemas.BifrostContext, context.CancelFunc) {
	turnCtx, cancel := createBifrostContextFromHeaders(r.handler.handlerStore, r.setupHeaders)
	applyRealtimeMiddlewareValues(turnCtx, r.middlewareValues)
	lib.SettleIdentity(turnCtx)
	if parent, _ := turnCtx.Value(schemas.BifrostContextKeyParentRequestID).(string); parent == "" {
		turnCtx.SetValue(schemas.BifrostContextKeyParentRequestID, r.sessionID)
	}
	return turnCtx, cancel
}

func (r *codexRelay) writeUpstream(messageType int, data []byte) error {
	r.upstreamWriteMu.Lock()
	defer r.upstreamWriteMu.Unlock()
	if err := r.upstream.SetWriteDeadline(time.Now().Add(realtimeWSWriteTimeout)); err != nil {
		return err
	}
	return r.upstream.WriteMessage(messageType, data)
}

// relayUpstreamToClient forwards every upstream frame first and only then reads it for its turn,
// so plugin work never delays the bytes Codex is waiting on. A frame Codex could not be sent is
// still read for its turn before the session ends: the usage it reports was spent either way.
func (r *codexRelay) relayUpstreamToClient() error {
	defer r.endedFirst.CompareAndSwap(0, codexUpstreamSide)
	for {
		messageType, data, err := r.upstream.ReadMessage()
		if err != nil {
			return r.upstreamEnded(err)
		}
		writeErr := r.client.WriteMessage(messageType, data)
		if writeErr == nil {
			r.touch()
		}
		if messageType == ws.TextMessage {
			r.observe(data)
		}
		if writeErr != nil {
			return writeErr
		}
		r.mu.Lock()
		if len(r.open) > 0 {
			_ = r.upstream.SetReadDeadline(time.Now().Add(r.idleTimeout))
		} else {
			_ = r.upstream.SetReadDeadline(time.Time{})
		}
		r.mu.Unlock()
	}
}

// upstreamEnded passes the upstream's close on to the client. A close without a frame, or a turn
// that went silent past the idle timeout, is a gateway-side failure and closes with 1011.
func (r *codexRelay) upstreamEnded(err error) error {
	if r.client.closing() {
		return nil
	}
	var closeErr *ws.CloseError
	if errors.As(err, &closeErr) && closeErr.Code != ws.CloseAbnormalClosure && closeErr.Code != ws.CloseTLSHandshake {
		_ = r.client.conn.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(closeErr.Code, closeErr.Text), time.Now().Add(realtimeWSWriteTimeout))
		return nil
	}
	reason := "upstream websocket stream interrupted"
	if isWSReadTimeout(err) {
		reason = "upstream websocket stream timed out"
	}
	_ = r.client.conn.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(ws.CloseInternalServerErr, reason), time.Now().Add(realtimeWSWriteTimeout))
	return err
}

// codexEvent is what the relay reads from an upstream event: enough to find its turn.
type codexEvent struct {
	kind       string
	lane       string
	responseID string
}

func peekCodexEvent(data []byte) codexEvent {
	event := codexEvent{
		kind: providerUtils.GetJSONField(data, "type").Str,
		lane: providerUtils.GetJSONField(data, "stream_id").Str,
	}
	if id := providerUtils.GetJSONField(data, "response.id").Str; id != "" {
		event.responseID = id
	} else {
		event.responseID = providerUtils.GetJSONField(data, "response_id").Str
	}
	return event
}

// observe attributes an upstream event to its turn, feeds it to the turn's post-hooks and ends
// the turn on its terminal event.
func (r *codexRelay) observe(data []byte) {
	event := peekCodexEvent(data)
	r.mu.Lock()
	turn := r.findTurn(event)
	var superseded []*codexTurn
	if turn != nil {
		switch event.kind {
		case "response.interrupt.accepted":
			turn.interrupted = true
		case "response.interrupt.failed":
			turn.interrupted = false
		}
		if turn.responseID == "" && event.responseID != "" {
			turn.responseID = event.responseID
			superseded = r.takeInterruptedLocked(turn)
		}
	}
	r.mu.Unlock()
	for _, old := range superseded {
		r.finishInterrupted(old)
	}
	if turn == nil {
		return
	}

	turn.feed(event.kind, data, r.upstreamHeaders)
	if openai.HasOpenAIPassthroughUsage(data) {
		if usage := openai.ExtractOpenAIPassthroughUsage(fasthttp.MethodPost, turn.path, turn.body, data); usage != nil {
			turn.usage = usage
		}
	}
	if !openai.EndsOpenAIPassthroughStream(data) {
		return
	}
	if !r.removeTurn(turn) {
		return
	}
	status := fasthttp.StatusOK
	if event.kind == string(schemas.WSEventError) {
		status = codexErrorEventStatus(data)
	}
	r.finishTurn(turn, status)
}

// takeInterruptedLocked removes the interrupted turns on next's lane that never got a terminal
// event. A lane runs one response at a time, so a new response starting there means they are
// over. Callers hold r.mu.
func (r *codexRelay) takeInterruptedLocked(next *codexTurn) []*codexTurn {
	var taken []*codexTurn
	kept := r.open[:0]
	for _, turn := range r.open {
		if turn != next && turn.interrupted && turn.lane == next.lane {
			taken = append(taken, turn)
			r.retained -= len(turn.body)
			continue
		}
		kept = append(kept, turn)
	}
	r.open = kept
	return taken
}

// finishInterrupted ends an interrupted turn that got no terminal event: billed with whatever
// usage its events carried, else recorded as cancelled.
func (r *codexRelay) finishInterrupted(turn *codexTurn) {
	if turn.usage != nil {
		r.finishTurn(turn, fasthttp.StatusOK)
		return
	}
	r.finishTurnWithError(turn, newBifrostError(499, "client_cancelled", "the client interrupted the response"))
}

// findTurn returns the open turn an event belongs to. An event naming a response belongs to the
// turn that created it; a response.created for a response no turn holds yet binds to the oldest
// turn on its stream_id lane still waiting for one, since a lane runs one response at a time.
// Any other event naming an unknown response is ignored rather than guessed at, so a late event
// of a finished response is never billed to the next turn. An event naming no response is either
// a delta of the response streaming on its lane, or a request-level error rejecting a
// response.create that never started (a running response fails with response.failed, which names
// it). A delta goes to the oldest live turn already bound to a response, an error to the oldest
// live turn still waiting for one; each falls back to the other, then to the oldest interrupted
// turn. Callers hold r.mu.
func (r *codexRelay) findTurn(event codexEvent) *codexTurn {
	if event.responseID != "" {
		for _, turn := range r.open {
			if turn.responseID == event.responseID {
				return turn
			}
		}
		if event.kind != "response.created" {
			return nil
		}
		for _, turn := range r.open {
			if turn.lane == event.lane && turn.responseID == "" {
				return turn
			}
		}
		return nil
	}
	var unbound, bound, interrupted *codexTurn
	for _, turn := range r.open {
		if turn.lane != event.lane {
			continue
		}
		switch {
		case turn.interrupted:
			if interrupted == nil {
				interrupted = turn
			}
		case turn.responseID == "":
			if unbound == nil {
				unbound = turn
			}
		case bound == nil:
			bound = turn
		}
	}
	first, second := bound, unbound
	if event.kind == string(schemas.WSEventError) {
		first, second = unbound, bound
	}
	if first != nil {
		return first
	}
	if second != nil {
		return second
	}
	return interrupted
}

// removeTurn takes a turn off the open list and reports whether it was still there, so a turn is
// finished exactly once whichever path ends it.
func (r *codexRelay) removeTurn(turn *codexTurn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, open := range r.open {
		if open == turn {
			r.open = append(r.open[:i], r.open[i+1:]...)
			r.retained -= len(turn.body)
			return true
		}
	}
	return false
}

// feed runs one upstream event through the turn's post-hooks as a passthrough stream chunk.
// The event is framed as the SSE event the HTTP route would have carried, so the log of a
// WebSocket turn reads the same as one sent over HTTP.
func (t *codexTurn) feed(kind string, data []byte, headers map[string]string) {
	chunk := make([]byte, 0, len(kind)+len(data)+16)
	if kind != "" {
		chunk = append(chunk, "event: "...)
		chunk = append(chunk, kind...)
		chunk = append(chunk, '\n')
	}
	chunk = append(chunk, "data: "...)
	chunk = append(chunk, data...)
	chunk = append(chunk, '\n', '\n')

	now := time.Now()
	resp := &schemas.BifrostResponse{PassthroughResponse: &schemas.BifrostPassthroughResponse{
		StatusCode: fasthttp.StatusOK,
		Headers:    headers,
		Body:       chunk,
		ExtraFields: schemas.BifrostResponseExtraFields{
			ChunkIndex:              t.chunks,
			Latency:                 now.Sub(t.last).Milliseconds(),
			ProviderResponseHeaders: headers,
			PassthroughPath:         t.path,
		},
	}}
	t.chunks++
	t.last = now
	if _, err := t.hooks.PostHookRunner(t.ctx, resp, nil); err != nil {
		logger.Debug("codex websocket relay post-hook returned an error: %v", err)
	}
}

// finishTurn sends the turn's final chunk, carrying the usage its events reported, and releases
// its pipeline. The final chunk flushes the turn's trace and log row.
func (r *codexRelay) finishTurn(turn *codexTurn, status int) {
	defer turn.cancel()
	defer turn.hooks.Cleanup()
	turn.ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	final := &schemas.BifrostPassthroughResponse{
		StatusCode: status,
		Headers:    r.upstreamHeaders,
		ExtraFields: schemas.BifrostResponseExtraFields{
			ChunkIndex:              turn.chunks,
			Latency:                 time.Since(turn.start).Milliseconds(),
			RawRequest:              turn.body,
			ProviderResponseHeaders: r.upstreamHeaders,
			PassthroughPath:         turn.path,
		},
		PassthroughUsage: turn.usage,
	}
	if _, err := turn.hooks.PostHookRunner(turn.ctx, &schemas.BifrostResponse{PassthroughResponse: final}, nil); err != nil {
		logger.Debug("codex websocket relay final post-hook returned an error: %v", err)
	}
}

// finishTurnWithError ends a turn that got no terminal event, so its log row records why.
func (r *codexRelay) finishTurnWithError(turn *codexTurn, bifrostErr *schemas.BifrostError) {
	defer turn.cancel()
	defer turn.hooks.Cleanup()
	turn.ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	if _, err := turn.hooks.PostHookRunner(turn.ctx, nil, bifrostErr); err != nil {
		logger.Debug("codex websocket relay error post-hook returned an error: %v", err)
	}
}

// codexErrorEventStatus reads the HTTP status an upstream error event carries.
func codexErrorEventStatus(data []byte) int {
	for _, field := range []string{"status", "status_code"} {
		if status := providerUtils.GetJSONField(data, field).Int(); status >= 400 && status < 600 {
			return int(status)
		}
	}
	return fasthttp.StatusInternalServerError
}
