package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	ws "github.com/fasthttp/websocket"
	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// SocketRequest is a WebSocket upgrade request for a relay, as Request is an HTTP call: the
// caller's handshake headers (credentials and gateway headers already stripped) and the
// subprotocols it offered.
type SocketRequest struct {
	Request
	Subprotocols []string
}

// SocketClient is the caller's side of a relayed session, implemented by the transport over the
// upgraded connection. Writes are serialized by the implementation; Interrupt forces a blocked
// ReadMessage to return, since closing a hijacked connection cannot.
type SocketClient interface {
	ReadMessage() (messageType int, data []byte, err error)
	WriteMessage(messageType int, data []byte) error
	WriteClose(code int, reason string) error
	Interrupt()
}

// Socket is one admitted WebSocket session: the pre-hooks have run and the upstream handshake
// succeeded. The transport upgrades the caller and calls Run, or Abort if the upgrade fails. The
// post-hooks run exactly once, when the session ends, with the whole session's usage; while it is
// open, its usage is settled to budgets in billing windows (see settleWindow).
type Socket struct {
	m           *Manager
	gate        *callGate
	ctx         *schemas.BifrostContext
	req         *schemas.BifrostRelayRequest
	upstream    *ws.Conn
	headers     map[string]string // upstream handshake response headers the caller may see
	subprotocol string

	billing    bool
	window     time.Duration // billing window; 0 settles only at close
	idle       time.Duration // longest the session may carry no data frame either way
	maxSession time.Duration // longest the session may stay open; 0 for no bound
	readLimit  int64
	acct       socketAccount
	activity   *atomic.Int64 // the logging plugin's last-activity slot; nil without the plugin
	lastData   atomic.Int64  // unix nanos of the last data frame either way
	captureIn  []byte
	captureOut []byte
	capture    bool
	settled    float64
	windows    int
	settleMu   sync.Mutex
	upstreamMu sync.Mutex // serializes data writes to the upstream
	endOnce    sync.Once
	finishOnce sync.Once
	ended      chan struct{}
	outcome    socketOutcome
}

// socketOutcome is how a session ended: what the caller is sent, what the upstream is sent, and
// the error the row and the post-hooks see (nil for a normal end).
type socketOutcome struct {
	closedBy     string
	clientCode   int
	clientReason string
	callerCode   int // the close code the caller ended with, when it closed the session
	callerReason string
	upstreamCode int
	err          *schemas.BifrostError
}

// maxCloseReasonBytes is the longest close reason a close frame can carry (125 minus the code).
const maxCloseReasonBytes = 123

// socketCloseGrace bounds the wait for the caller's close reply before its read is cut off.
const socketCloseGrace = time.Second

// OpenSocket admits a WebSocket upgrade for a relay route: the route must be served as sessions,
// the pre-hooks (governance and logging) run as for an HTTP call, and the upstream handshake is
// made before the caller is upgraded, so a refusal from either is a plain HTTP status the caller
// can act on rather than a 101 followed by a close. The returned Socket owns the post-hooks.
func (m *Manager) OpenSocket(ctx *schemas.BifrostContext, in SocketRequest) (sock *Socket, finalErr *schemas.BifrostError) {
	e := m.get(in.Endpoint)
	if e == nil {
		return nil, statusError(http.StatusNotFound, fmt.Sprintf("relay %q not found", in.Endpoint))
	}
	if name, dup := ambiguousHeader(in.Headers); dup {
		return nil, statusError(http.StatusBadRequest, fmt.Sprintf("header %s is set under two spellings; send it once", name))
	}
	// Routes match on the headers the upstream handshake receives, as Serve matches an HTTP call.
	headers := e.routeHeaders(in.Headers, callerExtraHeaders(ctx))
	p := e.plan(http.MethodGet, in.Path, headers)
	if !p.allowed {
		if p.route != nil {
			return nil, statusError(http.StatusNotFound, fmt.Sprintf("route GET %s on relay %q is disabled", in.Path, in.Endpoint))
		}
		return nil, statusError(http.StatusNotFound, fmt.Sprintf("no route for GET %s on relay %q", in.Path, in.Endpoint))
	}
	if !p.socket {
		return nil, statusError(http.StatusBadRequest, fmt.Sprintf("route GET %s on relay %q does not accept WebSocket upgrades", in.Path, in.Endpoint))
	}
	contentType, _ := headerValue("content-type", headers, e.operatorHeaders)
	model, err := ReadRequestModel(p.usage.modelSource, nil, contentType, in.RawQuery, p.usage.effectiveModelPath())
	if err != nil {
		return nil, statusError(http.StatusBadRequest, err.Error())
	}
	req := &schemas.BifrostRelayRequest{
		RelayName:  e.cfg.Name,
		Method:     http.MethodGet,
		Path:       in.Path,
		RawQuery:   in.RawQuery,
		Headers:    in.Headers,
		Model:      model,
		RouteIndex: p.index,
		Billable:   p.billing,
		Streams:    true,
		WebSocket:  true,
	}
	if p.route != nil {
		req.RoutePath = e.cfg.Routes[p.index].Path
		req.RouteHeaders = p.route.matchedHeaders(headers, e.operatorLogHeaders)
	}
	if requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string); requestID == "" {
		ctx.SetValue(schemas.BifrostContextKeyRequestID, uuid.New().String())
	}

	g := m.openGate(ctx, req, false)
	admitted := false
	defer func() {
		if r := recover(); r != nil {
			if m.logger != nil {
				m.logger.Error("recovered from panic opening relay socket %s: %v", req.RelayName, r)
			}
			bErr := statusError(http.StatusInternalServerError, fmt.Sprintf("panic in relay: %v", r))
			_, postErr := g.post(nil, bErr)
			if postErr == nil {
				postErr = bErr
			}
			sock, finalErr, admitted = nil, postErr, false
		}
		if !admitted {
			g.releaseOnce()
		}
	}()
	if shortCircuit := g.pre(); shortCircuit != nil {
		_, bErr := g.post(shortCircuit.Response, shortCircuit.Error)
		if bErr != nil {
			return nil, bErr
		}
		// A session has no reply a pre-hook could stand in for.
		return nil, statusError(http.StatusBadGateway, "a relay pre-hook answered a WebSocket upgrade with a response")
	}
	req = g.req

	conn, headers, subprotocol, dialErr := e.dialSocket(ctx, req, in.Subprotocols)
	if dialErr != nil {
		_, bErr := g.post(nil, dialErr)
		if bErr == nil {
			bErr = dialErr
		}
		return nil, bErr
	}
	s := &Socket{
		m:           m,
		gate:        g,
		ctx:         ctx,
		req:         req,
		upstream:    conn,
		headers:     headers,
		subprotocol: subprotocol,
		billing:     p.billing,
		idle:        time.Duration(e.streamIdle) * time.Second,
		ended:       make(chan struct{}),
		acct:        socketAccount{u: p.usage, billing: p.billing},
	}
	if wsCfg := e.cfg.WebSocket; wsCfg != nil {
		s.readLimit = wsCfg.MaxMessageBytes
		s.maxSession = time.Duration(wsCfg.MaxSessionSeconds) * time.Second
	}
	s.window = time.Duration(e.cfg.WebSocket.BillingWindow()) * time.Second
	if s.readLimit > 0 {
		conn.SetReadLimit(s.readLimit)
	}
	s.capture = relayContentCaptureEnabled(ctx)
	s.activity, _ = ctx.Value(schemas.BifrostContextKeyRelayActivity).(*atomic.Int64)
	s.touch()
	m.sockets.Store(s, struct{}{})
	admitted = true
	return s, nil
}

// Subprotocol is what the upstream negotiated, to echo on the caller's upgrade; "" for none.
func (s *Socket) Subprotocol() string { return s.subprotocol }

// MaxMessageBytes is the frame size bound the caller's connection should read with; 0 for none.
func (s *Socket) MaxMessageBytes() int64 { return s.readLimit }

// Headers are the upstream handshake response headers the caller may see on its 101.
func (s *Socket) Headers() map[string]string { return s.headers }

// Abort ends an admitted session whose caller was never upgraded: the upstream is closed and the
// post-hooks run with the reason, so the row records the refusal and nothing is charged.
func (s *Socket) Abort(bErr *schemas.BifrostError) {
	s.finishOnce.Do(func() {
		_ = s.upstream.Close()
		s.gate.post(nil, bErr)
		s.gate.releaseOnce()
		s.m.sockets.Delete(s)
	})
}

// Run relays frames between the caller and the upstream until either side ends the session, the
// session goes idle or reaches its bound, a billing window is refused, or the relay shuts down.
// It returns once both pumps have stopped and the post-hooks have run.
func (s *Socket) Run(client SocketClient) {
	opened := time.Now()
	var pumps sync.WaitGroup
	inDone := make(chan struct{})
	pumps.Add(2)
	go func() {
		defer pumps.Done()
		defer s.recoverPump("upstream")
		s.pumpUpstream(client)
	}()
	go func() {
		defer pumps.Done()
		defer close(inDone)
		defer s.recoverPump("client")
		s.pumpClient(client)
	}()

	stopTimers := s.startTimers()
	<-s.ended
	stopTimers()

	// Close both sides: the caller gets the close frame the outcome names, the upstream its own.
	o := s.outcome
	if o.clientCode != 0 {
		_ = client.WriteClose(o.clientCode, o.clientReason)
	}
	if o.upstreamCode != 0 {
		_ = s.upstream.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(o.upstreamCode, ""), time.Now().Add(socketCloseGrace))
	}
	_ = s.upstream.Close()
	select {
	case <-inDone:
	case <-time.After(socketCloseGrace):
		for interrupted := false; !interrupted; {
			client.Interrupt()
			select {
			case <-inDone:
				interrupted = true
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	pumps.Wait()
	// While a session is open, Bifrost only forwards frames: the time is the caller and the
	// upstream talking, as the waits between a stream's chunks are. It counts as upstream, so
	// the row's overhead is Bifrost's own work (hooks and the handshake), not the session length.
	schemas.AddUpstreamLatency(s.ctx, time.Since(opened))
	s.finish()
}

// recoverPump turns a panic in a pump into an internal-error end of the session.
func (s *Socket) recoverPump(side string) {
	if r := recover(); r != nil {
		if s.m.logger != nil {
			s.m.logger.Error("recovered from panic in relay socket %s pump for %s: %v", side, s.req.RelayName, r)
		}
		s.end(socketOutcome{
			closedBy: schemas.RelaySocketClosedByGateway, clientCode: ws.CloseInternalServerErr, clientReason: "relay error",
			upstreamCode: ws.CloseGoingAway, err: statusError(http.StatusInternalServerError, "panic in relay socket"),
		})
	}
}

// pumpUpstream forwards upstream frames to the caller, observing usage on text frames.
func (s *Socket) pumpUpstream(client SocketClient) {
	for {
		messageType, data, err := s.upstream.ReadMessage()
		if err != nil {
			s.endFromUpstream(err)
			return
		}
		s.onFrame(false, messageType, data)
		if err := client.WriteMessage(messageType, data); err != nil {
			s.end(socketOutcome{closedBy: schemas.RelaySocketClosedByClient, upstreamCode: ws.CloseGoingAway, err: cancelledError(err)})
			return
		}
	}
}

// pumpClient forwards the caller's frames to the upstream.
func (s *Socket) pumpClient(client SocketClient) {
	for {
		messageType, data, err := client.ReadMessage()
		if err != nil {
			s.endFromClient(err)
			return
		}
		s.onFrame(true, messageType, data)
		s.upstreamMu.Lock()
		err = s.upstream.WriteMessage(messageType, data)
		s.upstreamMu.Unlock()
		if err != nil {
			s.end(socketOutcome{
				closedBy: schemas.RelaySocketClosedByUpstream, clientCode: ws.CloseInternalServerErr, clientReason: "upstream connection lost",
				err: transportError(err, time.Since(s.gate.start)),
			})
			return
		}
	}
}

// onFrame counts one data frame, observes usage on an upstream text frame, keeps the frame for the
// log row when content capture is on, and marks the session active.
func (s *Socket) onFrame(inbound bool, messageType int, data []byte) {
	text := messageType == ws.TextMessage
	s.acct.onFrame(inbound, text, data)
	if s.capture && text {
		s.settleMu.Lock()
		if inbound {
			s.captureIn = appendFrame(s.captureIn, data)
		} else {
			s.captureOut = appendFrame(s.captureOut, data)
		}
		s.settleMu.Unlock()
	}
	s.touch()
}

// appendFrame keeps a session's text frames one per line, as received; there is no bound of the
// relay's own, as on an HTTP reply.
func appendFrame(buf, frame []byte) []byte {
	if len(buf) > 0 {
		buf = append(buf, '\n')
	}
	return append(buf, frame...)
}

// touch records data activity for the idle bound and the logging plugin's stale sweep.
func (s *Socket) touch() {
	now := time.Now().UnixNano()
	s.lastData.Store(now)
	if s.activity != nil {
		s.activity.Store(now)
	}
}

// endFromUpstream maps how the upstream ended the session. A normal close (1000, 1001) or a close
// with no status is a normal end and its code is forwarded; any other close code is forwarded and
// recorded as an error; a connection lost without a close frame is a transport error.
func (s *Socket) endFromUpstream(err error) {
	var closeErr *ws.CloseError
	if errors.As(err, &closeErr) {
		o := socketOutcome{closedBy: schemas.RelaySocketClosedByUpstream, clientCode: closeErr.Code, clientReason: closeErr.Text}
		switch closeErr.Code {
		case ws.CloseNormalClosure, ws.CloseGoingAway:
		case ws.CloseNoStatusReceived:
			o.clientCode = ws.CloseNormalClosure
		case ws.CloseAbnormalClosure, ws.CloseTLSHandshake:
			o.clientCode, o.clientReason = ws.CloseInternalServerErr, "upstream connection lost"
			o.err = transportError(err, time.Since(s.gate.start))
		default:
			o.err = upstreamCloseError(closeErr)
		}
		s.end(o)
		return
	}
	if errors.Is(err, ws.ErrReadLimit) {
		s.end(socketOutcome{
			closedBy: schemas.RelaySocketClosedByGateway, clientCode: ws.CloseMessageTooBig, clientReason: "upstream message exceeds max_message_bytes",
			err: statusError(http.StatusBadGateway, "the upstream sent a frame larger than websocket.max_message_bytes"),
		})
		return
	}
	s.end(socketOutcome{
		closedBy: schemas.RelaySocketClosedByUpstream, clientCode: ws.CloseInternalServerErr, clientReason: "upstream connection lost",
		err: transportError(err, time.Since(s.gate.start)),
	})
}

// endFromClient maps how the caller ended the session. A normal close is the normal end of a
// session (success) and its code is forwarded upstream; a connection dropped without a close, or
// a caller that stopped answering pings, is a hang-up, recorded as cancelled as for HTTP.
func (s *Socket) endFromClient(err error) {
	var closeErr *ws.CloseError
	if errors.As(err, &closeErr) {
		o := socketOutcome{closedBy: schemas.RelaySocketClosedByClient, callerCode: closeErr.Code, callerReason: closeErr.Text, upstreamCode: closeErr.Code}
		switch closeErr.Code {
		case ws.CloseNormalClosure, ws.CloseGoingAway:
		case ws.CloseNoStatusReceived:
			o.upstreamCode = ws.CloseNormalClosure
		case ws.CloseAbnormalClosure, ws.CloseTLSHandshake:
			o.upstreamCode = ws.CloseGoingAway
			o.err = cancelledError(err)
		default:
			if !sendableCloseCode(closeErr.Code) {
				o.upstreamCode = ws.CloseGoingAway
			}
		}
		s.end(o)
		return
	}
	if errors.Is(err, ws.ErrReadLimit) {
		s.end(socketOutcome{
			closedBy: schemas.RelaySocketClosedByGateway, clientCode: ws.CloseMessageTooBig, clientReason: "message exceeds max_message_bytes",
			upstreamCode: ws.CloseGoingAway, err: statusError(http.StatusRequestEntityTooLarge, "the caller sent a frame larger than websocket.max_message_bytes"),
		})
		return
	}
	s.end(socketOutcome{closedBy: schemas.RelaySocketClosedByClient, upstreamCode: ws.CloseGoingAway, err: cancelledError(err)})
}

// end records the first way the session ended and signals Run; later calls are ignored, so the
// error a pump sees while the session is already closing never replaces the real reason.
func (s *Socket) end(o socketOutcome) {
	s.endOnce.Do(func() {
		o.clientReason = truncateCloseReason(o.clientReason)
		s.outcome = o
		close(s.ended)
	})
}

// startTimers starts the idle bound, the session bound and the billing windows; the returned func
// stops them and waits for a window in progress.
func (s *Socket) startTimers() func() {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	if s.idle > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			check := max(s.idle/4, 250*time.Millisecond)
			ticker := time.NewTicker(check)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					if time.Since(time.Unix(0, s.lastData.Load())) >= s.idle {
						s.end(socketOutcome{
							closedBy: schemas.RelaySocketClosedByGateway, clientCode: ws.CloseGoingAway, clientReason: "relay idle timeout",
							upstreamCode: ws.CloseGoingAway, err: transportError(providerUtils.ErrStreamIdleTimeout, time.Since(s.gate.start)),
						})
						return
					}
				}
			}
		}()
	}
	if s.maxSession > 0 {
		timer := time.AfterFunc(s.maxSession, func() {
			s.end(socketOutcome{
				closedBy: schemas.RelaySocketClosedByGateway, clientCode: ws.ClosePolicyViolation, clientReason: "relay session reached max_session_seconds",
				upstreamCode: ws.CloseNormalClosure,
			})
		})
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-stop
			timer.Stop()
		}()
	}
	if s.window > 0 && s.billing && s.gate.pipeline != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(s.window)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					s.settleWindow()
				}
			}
		}()
	}
	return func() {
		close(stop)
		wg.Wait()
	}
}

// settleWindow charges the usage the session has run up since the last window. It runs the
// pre-hooks for a continuation unit (governance checks the caller's budgets and rate limits again,
// without counting a request) and, if admitted, the post-hooks with the unsettled cost. A refused
// window ends the session: the caller is closed with 1008 and the final settlement charges what
// was forwarded. Each unit carries its own request id, as a GPT Live billing unit does, so
// governance's per-request billing dedupe never drops a window; the logging plugin writes no row
// for a unit, since the session's row is written once at the end.
func (s *Socket) settleWindow() {
	select {
	case <-s.ended:
		return
	default:
	}
	unitCtx := s.unitContext()
	unit := s.m.openGate(unitCtx, s.req, true)
	defer unit.releaseOnce()
	if shortCircuit := unit.pre(); shortCircuit != nil {
		unit.post(shortCircuit.Response, shortCircuit.Error)
		if refusal := shortCircuit.Error; refusal != nil {
			reason := "relay session refused"
			if refusal.Error != nil && refusal.Error.Message != "" {
				reason = refusal.Error.Message
			}
			s.end(socketOutcome{
				closedBy: schemas.RelaySocketClosedByGateway, clientCode: ws.ClosePolicyViolation, clientReason: reason,
				upstreamCode: ws.CloseNormalClosure, err: refusal,
			})
		}
		return
	}
	s.settleMu.Lock()
	cost := s.acct.cost()
	delta := max(cost-s.settled, 0)
	s.settleMu.Unlock()
	unit.post(&schemas.BifrostRelayResponse{
		RelayName: s.req.RelayName, StatusCode: http.StatusSwitchingProtocols, Streamed: true, WebSocket: true,
		Usage: &schemas.RelayUsage{Cost: delta},
	}, nil)
	s.settleMu.Lock()
	s.settled += delta
	s.windows++
	s.settleMu.Unlock()
}

// unitContext is a billing unit's context: the session's identity, grant and governance values,
// its own request id, no trace (a unit emits no relay span), and the continuation flag governance
// honours for a relay session.
func (s *Socket) unitContext() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for key, value := range s.ctx.GetUserValues() {
		switch key {
		case schemas.BifrostContextKeyTraceID, schemas.BifrostContextKeyExportTraceID, schemas.BifrostContextKeyTracer,
			schemas.BifrostContextKeySpanID, schemas.BifrostContextKeyRequestID:
			continue
		}
		if value != nil {
			ctx.SetValue(key, value)
		}
	}
	if g := s.ctx.Grant(); g != nil {
		ctx.SetGrant(g)
	}
	ctx.SetValue(schemas.BifrostContextKeyRequestID, uuid.NewString())
	ctx.SetValue(schemas.BifrostContextKeySessionContinuation, true)
	ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.RelayRequest)
	return ctx
}

// finish runs the session's post-hooks once with the whole session: its counters, its usage, the
// part of the cost billing windows already charged, and how it ended.
func (s *Socket) finish() {
	s.finishOnce.Do(func() {
		defer s.m.sockets.Delete(s)
		defer s.gate.releaseOnce()
		bytesIn, bytesOut, framesIn, framesOut := s.acct.counters()
		closeCode, closeReason := s.outcome.clientCode, s.outcome.clientReason
		if closeCode == 0 {
			closeCode, closeReason = s.outcome.callerCode, s.outcome.callerReason
		}
		s.settleMu.Lock()
		resp := &schemas.BifrostRelayResponse{
			RelayName:   s.req.RelayName,
			StatusCode:  http.StatusSwitchingProtocols,
			Headers:     s.headers,
			Streamed:    true,
			WebSocket:   true,
			BytesOut:    bytesOut,
			BytesIn:     bytesIn,
			FramesIn:    framesIn,
			FramesOut:   framesOut,
			SettledCost: s.settled,
			Socket: &schemas.RelaySocketSummary{
				Subprotocol: s.subprotocol,
				CloseCode:   closeCode,
				CloseReason: closeReason,
				ClosedBy:    s.outcome.closedBy,
				Windows:     s.windows,
			},
		}
		if len(s.captureOut) > 0 {
			resp.Body = s.captureOut
		}
		if len(s.captureIn) > 0 {
			resp.RequestBody = s.captureIn
		}
		s.settleMu.Unlock()
		if s.billing {
			resp.Usage = s.acct.usage()
		}
		s.gate.post(resp, s.outcome.err)
	})
}

// closeForShutdown ends an open session as the relay shuts down.
func (s *Socket) closeForShutdown() {
	s.end(socketOutcome{
		closedBy: schemas.RelaySocketClosedByGateway, clientCode: ws.CloseServiceRestart, clientReason: "relay shutting down",
		upstreamCode: ws.CloseGoingAway, err: cancelledError(context.Canceled),
	})
}

// CloseSockets ends every open WebSocket session, for shutdown. Each session runs its post-hooks
// as it ends, so its row is written and its usage charged.
func (m *Manager) CloseSockets() {
	m.sockets.Range(func(key, _ any) bool {
		if s, ok := key.(*Socket); ok {
			s.closeForShutdown()
		}
		return true
	})
}

// dialSocket makes the upstream WebSocket handshake: the same host-pinned URL, header filtering and
// credential injection as an HTTP call, with the scheme swapped to ws/wss and aws_sigv4 presigned
// into the query. The caller's subprotocols are offered only where the relay allows them. A
// handshake the upstream refuses comes back with its HTTP status.
func (e *endpoint) dialSocket(ctx *schemas.BifrostContext, req *schemas.BifrostRelayRequest, offered []string) (*ws.Conn, map[string]string, string, *schemas.BifrostError) {
	httpReq, bifrostErr := e.buildRequest(ctx, upstreamRequest{method: http.MethodGet, path: req.Path, rawQuery: req.RawQuery, headers: req.Headers, socket: true})
	if bifrostErr != nil {
		return nil, nil, "", bifrostErr
	}
	target := *httpReq.URL
	switch target.Scheme {
	case "https":
		target.Scheme = "wss"
	case "http":
		target.Scheme = "ws"
	}
	headers := httpReq.Header.Clone()
	for name := range headers {
		lower := strings.ToLower(name)
		if lower == "upgrade" || lower == "connection" || lower == "host" || strings.HasPrefix(lower, "sec-websocket-") {
			headers.Del(name)
		}
	}
	dialer := *e.wsDialer
	dialer.Subprotocols = allowedSubprotocols(offered, e.cfg.WebSocket)
	start := time.Now()
	conn, resp, err := dialer.DialContext(ctx, target.String(), headers)
	handshake := time.Since(start)
	schemas.AddUpstreamLatency(ctx, handshake)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			if errors.Is(err, ws.ErrBadHandshake) {
				snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
				status := resp.StatusCode
				if status < 400 {
					status = http.StatusBadGateway
				}
				message := fmt.Sprintf("the upstream refused the WebSocket handshake with %d", resp.StatusCode)
				if text := strings.TrimSpace(string(snippet)); text != "" && utf8.ValidString(text) {
					message += ": " + text
				}
				bErr := statusError(status, message)
				bErr.ExtraFields.Latency = handshake.Milliseconds()
				return nil, nil, "", bErr
			}
		}
		return nil, nil, "", transportError(redactDialError(err), handshake)
	}
	responseHeaders := map[string]string{}
	if resp != nil {
		responseHeaders = e.responseHeaders(resp)
		for name := range responseHeaders {
			lower := strings.ToLower(name)
			if lower == "upgrade" || lower == "connection" || strings.HasPrefix(lower, "sec-websocket-") {
				delete(responseHeaders, name)
			}
		}
	}
	return conn, responseHeaders, conn.Subprotocol(), nil
}

// allowedSubprotocols keeps the offered subprotocols the relay allows, in the caller's order.
func allowedSubprotocols(offered []string, cfg *schemas.RelayWebSocketConfig) []string {
	if cfg == nil || len(cfg.Subprotocols) == 0 || len(offered) == 0 {
		return nil
	}
	var out []string
	for _, protocol := range offered {
		for _, allowed := range cfg.Subprotocols {
			if strings.EqualFold(strings.TrimSpace(protocol), allowed) {
				out = append(out, allowed)
				break
			}
		}
	}
	return out
}

// redactDialError drops the URL from a dial error, since a query credential rides in it.
func redactDialError(err error) error {
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return err
	}
	return redactTransportError(err)
}

// upstreamCloseError is the row's error for an upstream that closed the session with an error code.
func upstreamCloseError(closeErr *ws.CloseError) *schemas.BifrostError {
	message := fmt.Sprintf("the upstream closed the session with code %d", closeErr.Code)
	if closeErr.Text != "" {
		message += ": " + closeErr.Text
	}
	return &schemas.BifrostError{Error: &schemas.ErrorField{Message: message, Error: closeErr}}
}

// cancelledError is the row's error for a caller that hung up, as a dropped HTTP stream is recorded.
func cancelledError(err error) *schemas.BifrostError {
	return &schemas.BifrostError{Error: &schemas.ErrorField{Type: schemas.Ptr(schemas.RequestCancelled), Message: schemas.ErrRequestCancelled, Error: err}}
}

// sendableCloseCode reports whether a close code may be sent in a close frame (RFC 6455 7.4).
func sendableCloseCode(code int) bool {
	switch {
	case code >= 1000 && code <= 1003, code >= 1007 && code <= 1014, code >= 3000 && code <= 4999:
		return true
	}
	return false
}

// truncateCloseReason keeps a close reason within the frame's limit without splitting a character.
func truncateCloseReason(reason string) string {
	if len(reason) <= maxCloseReasonBytes {
		return reason
	}
	cut := reason[:maxCloseReasonBytes]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// socketAccount accumulates one session's counters and usage. Usage is observed on upstream text
// frames (each frame is one event, as an SSE event is on an HTTP stream), and a size-priced
// mapping bills the bytes or characters forwarded: upstream to caller for a response-side
// quantity, caller to upstream for a request-side one (the audio a speech-to-text API bills on).
type socketAccount struct {
	mu        sync.Mutex
	u         resolvedUsage
	billing   bool
	acc       usageFields
	bytesIn   int64
	bytesOut  int64
	runesIn   int64
	runesOut  int64
	framesIn  int64
	framesOut int64
}

func (a *socketAccount) onFrame(inbound, text bool, data []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := int64(len(data))
	if inbound {
		a.framesIn++
		a.bytesIn += n
		if a.billing && a.u.unitsMeasure == schemas.RelayUnitsBodyChars {
			a.runesIn += int64(utf8.RuneCount(data))
		}
		return
	}
	a.framesOut++
	a.bytesOut += n
	if !a.billing {
		return
	}
	if a.u.unitsMeasure == schemas.RelayUnitsBodyChars {
		a.runesOut += int64(utf8.RuneCount(data))
	}
	if text && a.u.hasUsageEvent(data) {
		if f := a.u.eventFields(data); f.any() {
			a.acc.merge(f)
		}
	}
}

// fieldsLocked is the session's usage fields as of now, with the size-priced quantities applied.
func (a *socketAccount) fieldsLocked() usageFields {
	f := a.acc
	switch {
	case a.u.wholeBodyResponse():
		quantity := a.bytesOut
		if a.u.unitsMeasure == schemas.RelayUnitsBodyChars {
			quantity = a.runesOut
		}
		f.merge(usageFields{units: float64(quantity), hasUnits: true})
	case a.u.wholeBodyRequest():
		quantity := a.bytesIn
		if a.u.unitsMeasure == schemas.RelayUnitsBodyChars {
			quantity = a.runesIn
		}
		f.merge(usageFields{units: float64(quantity), hasUnits: true})
	}
	return f
}

// cost is the session's cost so far.
func (a *socketAccount) cost() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.u.build(a.fieldsLocked()).Cost
}

// usage is the session's whole usage, for the final post-hooks.
func (a *socketAccount) usage() *schemas.RelayUsage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.u.build(a.fieldsLocked())
}

func (a *socketAccount) counters() (bytesIn, bytesOut, framesIn, framesOut int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bytesIn, a.bytesOut, a.framesIn, a.framesOut
}
