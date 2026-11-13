package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"unsafe"

	ws "github.com/fasthttp/websocket"
	"github.com/maximhq/bifrost/core/providers/elevenlabs"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// Paths an ElevenLabs client calls under /elevenlabs, the base URL its SDK is given for the gateway.
const (
	elevenlabsScribePath   = "/elevenlabs" + elevenlabs.NativeScribeRoute
	elevenlabsDialoguePath = "/elevenlabs" + elevenlabs.NativeDialogueRoute
)

// elevenlabsMaxFrameBytes bounds one frame a client sends, as for the other sessions the gateway relays.
const elevenlabsMaxFrameBytes = 1 << 20

// elevenlabsAuthRefusalMessage is what an anonymous ElevenLabs client is told, naming the header its SDK sends.
const elevenlabsAuthRefusalMessage = "authentication is required. Provide a Bifrost virtual key as the ElevenLabs API key (the xi-api-key header)."

// elevenlabsScribeProtocol is Scribe's own protocol, which an ElevenLabs client speaks through the gateway: it
// names the model in the URL, so there is no first frame, and it reads a failure as a frame.
var elevenlabsScribeProtocol = &nativeProtocol{
	name:        "ElevenLabs Scribe",
	integration: "elevenlabs",
	open:        openElevenlabsScribe,
	writeError:  writeElevenlabsScribeError,
}

// elevenlabsNativeProvider is what the ElevenLabs sockets ask of the provider: the upstream a client's URL names,
// and the hooks that govern a Scribe connection.
type elevenlabsNativeProvider interface {
	NativeRealtimeDial(ctx *schemas.BifrostContext, key schemas.Key, route, rawQuery, model string) (url string, headers map[string]string, err *schemas.BifrostError)
	NativeRealtimeProtocol(route, rawQuery string) (schemas.RealtimeProvider, *schemas.BifrostError)
}

var _ elevenlabsNativeProvider = (*elevenlabs.ElevenlabsProvider)(nil)

// openElevenlabsScribe addresses the Scribe endpoint on the key's base URL with the client's query, and returns the
// hooks that meter the connection's audio.
func openElevenlabsScribe(ctx *schemas.BifrostContext, providerKey schemas.ModelProvider, provider schemas.RealtimeProvider, key schemas.Key, rawQuery, model string) (*nativeUpstream, *schemas.BifrostError) {
	native, ok := provider.(elevenlabsNativeProvider)
	if !ok {
		return nil, newRealtimeWireBifrostError(400, "invalid_request_error",
			fmt.Sprintf("provider %s cannot serve ElevenLabs Scribe clients; use %s for it", providerKey, lib.WithBasePath("/v1/realtime")))
	}
	url, headers, bifrostErr := native.NativeRealtimeDial(ctx, key, elevenlabs.NativeScribeRoute, rawQuery, model)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	protocol, bifrostErr := native.NativeRealtimeProtocol(elevenlabs.NativeScribeRoute, rawQuery)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return &nativeUpstream{url: url, headers: headers, protocol: protocol}, nil
}

// elevenlabsScribeError is the frame Scribe sends for a failure, before it closes the session.
type elevenlabsScribeError struct {
	MessageType string `json:"message_type"`
	Error       string `json:"error"`
}

// elevenlabsDialogueError is the frame the text-to-dialogue socket sends for a failure, before it closes the session.
type elevenlabsDialogueError struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}

// sendElevenlabsHandshakeError refuses an upgrade in ElevenLabs' error shape, which its SDKs read from a failed
// handshake.
func sendElevenlabsHandshakeError(ctx *fasthttp.RequestCtx, bifrostErr *schemas.BifrostError) {
	ctx.SetStatusCode(bifrostErr.EffectiveHTTPStatus())
	ctx.SetContentType("application/json")
	body, err := providerUtils.MarshalSorted(elevenlabs.ToElevenlabsError(bifrostErr))
	if err != nil {
		ctx.SetStatusCode(http.StatusInternalServerError)
		return
	}
	ctx.SetBody(body)
}

// writeElevenlabsScribeError reports a failure the way Scribe does, as a frame of the kind its SDKs act on, and then
// closes with the code that failure maps to.
func writeElevenlabsScribeError(c *realtimeClientConn, bifrostErr *schemas.BifrostError) {
	kind := "error"
	switch bifrostErr.EffectiveHTTPStatus() {
	case 400:
		kind = "invalid_request"
	case 401, 403:
		kind = "auth_error"
	case 402:
		kind = "quota_exceeded"
	case 429:
		kind = "rate_limited"
	}
	_, _, message, _ := mapRealtimeWireErrorFields(bifrostErr)
	if payload, err := providerUtils.MarshalSorted(elevenlabsScribeError{MessageType: kind, Error: message}); err == nil {
		_ = c.WriteMessage(ws.TextMessage, payload)
	}
	c.writeNativeClose(bifrostErr)
}

// handleElevenlabsScribeUpgrade serves an ElevenLabs client that speaks Scribe's realtime protocol, such as the
// ElevenLabs SDKs pointed at the gateway. Like a Gemini Live client it is admitted after the upgrade, but it names
// its model in the URL, and every option is a query parameter that goes upstream as sent.
func (h *WSRealtimeHandler) handleElevenlabsScribeUpgrade(ctx *fasthttp.RequestCtx) {
	path := string(ctx.Path())
	rawQuery := string(ctx.URI().QueryString())
	rawModel := strings.TrimSpace(string(ctx.QueryArgs().Peek("model_id")))
	setupHeaders := captureSessionHeaders(ctx)
	preReqCtx, preReqCancel := createBifrostContextFromHeaders(h.handlerStore, setupHeaders)
	preReqCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.RealtimeRequest)
	preReqCtx.SetValue(schemas.BifrostContextKeyIntegrationType, elevenlabsScribeProtocol.integration)
	if authErr := refuseUnauthenticatedRealtime(h.config.ClientConfig.EnforceAuthOnInference, preReqCtx, setupHeaders.authorization); authErr != nil {
		preReqCancel()
		sendElevenlabsHandshakeError(ctx, newRealtimeWireBifrostError(401, "invalid_api_key", elevenlabsAuthRefusalMessage))
		return
	}
	if rawModel == "" {
		preReqCancel()
		sendElevenlabsHandshakeError(ctx, newRealtimeWireBifrostError(400, "invalid_request", "the model_id query parameter is required"))
		return
	}
	populateRealtimeRequestContext(ctx, preReqCtx)
	// The query feeds routing rules, which have no use for the single-use token a browser client sends.
	if query, ok := preReqCtx.Value(schemas.BifrostContextKeyRequestQuery).(map[string]string); ok {
		delete(query, "token")
	}

	upgrader := h.websocketUpgrader("")
	err := upgrader.Upgrade(ctx, func(conn *ws.Conn) {
		defer conn.Close()
		defer recoverWebSocketSession(conn)
		defer preReqCancel()
		conn.SetReadLimit(elevenlabsMaxFrameBytes)
		clientConn := newRealtimeClientConn(conn)
		clientConn.native = elevenlabsScribeProtocol
		clientConn.nativeQuery = rawQuery
		// A transcription session: its turns are priced by the audio they carry, not by tokens.
		h.runBootstrappedRealtimeSession(ctx, conn, clientConn, preReqCtx, preReqCancel, setupHeaders, path, rawModel, "transcription", nil)
	})
	if err != nil {
		preReqCancel()
		logger.Warn("websocket upgrade failed for %s: %v", path, err)
	}
}

// handleElevenlabsDialogueUpgrade serves an ElevenLabs client that speaks the text-to-dialogue socket's protocol,
// the only WebSocket that serves eleven_v4_turbo. The frames are relayed as they are; the session is one request
// through the plugin pipeline, admitted when it opens and billed by the characters the client sent.
func (h *WSSpeechSessionHandler) handleElevenlabsDialogueUpgrade(ctx *fasthttp.RequestCtx) {
	path := string(ctx.Path())
	rawQuery := string(ctx.URI().QueryString())
	rawModel := strings.TrimSpace(string(ctx.QueryArgs().Peek("model_id")))
	setupHeaders := captureSessionHeaders(ctx)
	preReqCtx, cancel := createBifrostContextFromHeaders(h.config, setupHeaders)
	preReqCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.SpeechStreamRequest)
	if h.config.ClientConfig.EnforceAuthOnInference && !governance.PresentedAnyCredential(preReqCtx) {
		cancel()
		sendElevenlabsHandshakeError(ctx, newRealtimeWireBifrostError(401, "invalid_api_key", elevenlabsAuthRefusalMessage))
		return
	}
	if rawModel == "" {
		cancel()
		sendElevenlabsHandshakeError(ctx, newRealtimeWireBifrostError(400, "invalid_request", "the model_id query parameter is required"))
		return
	}
	populateRealtimeRequestContext(ctx, preReqCtx)
	if query, ok := preReqCtx.Value(schemas.BifrostContextKeyRequestQuery).(map[string]string); ok {
		delete(query, "token")
	}
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
		conn.SetReadLimit(elevenlabsMaxFrameBytes)
		h.serveElevenlabsDialogue(newRealtimeClientConn(conn), preReqCtx, setupHeaders, middlewareValues, rawModel, rawQuery)
	})
	if err != nil {
		cancel()
		logger.Warn("websocket upgrade failed for %s: %v", path, err)
	}
}

// serveElevenlabsDialogue runs one relayed session: admission, the provider socket, the relay, settlement.
func (h *WSSpeechSessionHandler) serveElevenlabsDialogue(clientConn *realtimeClientConn, preReqCtx *schemas.BifrostContext, setupHeaders *sessionHeaders, middlewareValues map[any]any, rawModel, rawQuery string) {
	clientConn.startHeartbeat()
	defer clientConn.stopHeartbeat()

	// The path names the provider, as for Scribe: ElevenLabs is the only one with this socket.
	providerKey, model := schemas.ParseModelString(rawModel, realtimeDefaultProviderForPath(elevenlabsDialoguePath))
	req := &schemas.BifrostSpeechRequest{Provider: providerKey, Model: model, Input: &schemas.SpeechInput{}}
	// The pre-request pipeline may route the model, still before a session slot or a provider connection exists.
	provider, bifrostErr := h.resolveTarget(preReqCtx, req)
	if bifrostErr != nil {
		writeElevenlabsDialogueError(clientConn, bifrostErr)
		return
	}
	dialer, ok := provider.(elevenlabsNativeProvider)
	if !ok {
		writeElevenlabsDialogueError(clientConn, newRealtimeWireBifrostError(400, "invalid_request", "provider "+string(req.Provider)+" cannot serve ElevenLabs text-to-dialogue clients"))
		return
	}

	session, sessionErr := h.sessions.Create(clientConn.conn)
	if sessionErr != nil {
		writeElevenlabsDialogueError(clientConn, newRealtimeWireBifrostError(429, "rate_limited", sessionErr.Error()))
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
		writeElevenlabsDialogueError(clientConn, bifrostErr)
		return
	}
	// Every exit from here on settles the session, a panic included.
	var (
		key     schemas.Key
		run     *elevenlabsDialogueRun
		failure *schemas.BifrostError
	)
	defer func() {
		chars := 0
		if run != nil {
			chars = int(run.chars.Load())
			if failure == nil {
				failure = run.failure.Load()
			}
		}
		unit.settle(req, key, chars, failure)
	}()
	var err error
	key, err = h.client.SelectKeyForProviderRequestType(unit.ctx, schemas.SpeechStreamRequest, req.Provider, req.Model)
	if err != nil {
		failure = newRealtimeWireBifrostError(400, "invalid_request", err.Error())
		writeElevenlabsDialogueError(clientConn, failure)
		return
	}
	// The provider receives the model the alias resolves to; the alias stays on the context for its overrides.
	upstreamModel := req.Model
	if aliasConfig := key.Aliases.ResolveConfig(req.Model); aliasConfig != nil {
		ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: req.Model, Config: aliasConfig})
		upstreamModel = aliasConfig.ModelID
	}

	url, headers, bifrostErr := dialer.NativeRealtimeDial(ctx, key, elevenlabs.NativeDialogueRoute, rawQuery, upstreamModel)
	if bifrostErr != nil {
		failure = bifrostErr
		writeElevenlabsDialogueError(clientConn, failure)
		return
	}
	upstream, bifrostErr := h.dialer(req.Provider, key)(ctx, url, headers)
	if bifrostErr != nil {
		failure = bifrostErr
		writeElevenlabsDialogueError(clientConn, failure)
		return
	}
	defer upstream.Close()

	run = &elevenlabsDialogueRun{clientConn: clientConn, upstream: upstream}
	logErr := runRealtimeRelays(clientConn.conn, run.relayClient, run.relayUpstream, func() {
		// The client side closes first, it marks the teardown, so the read this close ends is not reported as a provider failure.
		_ = clientConn.Close()
		_ = upstream.Close()
	})
	if logErr != nil {
		logger.Warn("elevenlabs text-to-dialogue session %s ended with an error: %v", session.ID(), logErr)
	}
}

// writeElevenlabsDialogueError reports a failure the way the text-to-dialogue socket does, as a frame carrying the
// close code, and then closes with it.
func writeElevenlabsDialogueError(c *realtimeClientConn, bifrostErr *schemas.BifrostError) {
	code, _ := nativeCloseFrame(bifrostErr)
	_, _, message, _ := mapRealtimeWireErrorFields(bifrostErr)
	if payload, err := providerUtils.MarshalSorted(elevenlabsDialogueError{Error: message, Code: code}); err == nil {
		_ = c.WriteMessage(ws.TextMessage, payload)
	}
	c.writeNativeClose(bifrostErr)
}

// elevenlabsDialogueRun relays one open text-to-dialogue session between the client and ElevenLabs.
type elevenlabsDialogueRun struct {
	clientConn *realtimeClientConn
	upstream   schemas.RealtimeUpstream
	chars      atomic.Int64                         // characters of text the client sent, which ElevenLabs bills
	failure    atomic.Pointer[schemas.BifrostError] // the session's first failure, which either relay may meet
}

// elevenlabsFrameChars counts the characters of text a client frame sends: the text of each input, or the text of a
// single-voice frame. The frame is read in place and not kept.
func elevenlabsFrameChars(frame []byte) int {
	chars := 0
	gjson.Parse(unsafe.String(unsafe.SliceData(frame), len(frame))).ForEach(func(name, value gjson.Result) bool {
		switch name.Str {
		case "text":
			chars += utf8.RuneCountInString(value.Str)
		case "inputs":
			value.ForEach(func(_, input gjson.Result) bool {
				chars += utf8.RuneCountInString(input.Get("text").Str)
				return true
			})
		}
		return true
	})
	return chars
}

// relayClient forwards the client's frames to ElevenLabs as they are, counting the text of those it accepted: only
// that is billed.
func (r *elevenlabsDialogueRun) relayClient() error {
	for {
		messageType, message, err := r.clientConn.ReadMessage()
		if err != nil {
			if isNormalWebSocketClosure(err) || r.clientConn.closing() {
				return nil
			}
			return err
		}
		if messageType != ws.TextMessage {
			writeElevenlabsDialogueError(r.clientConn, newRealtimeWireBifrostError(400, "invalid_request", "the text-to-dialogue socket only accepts text messages"))
			return nil
		}
		chars := elevenlabsFrameChars(message)
		if err := r.upstream.WriteMessage(messageType, message); err != nil {
			failure := newRealtimeWireBifrostError(502, "server_error", "failed to write the frame to the provider")
			r.failure.CompareAndSwap(nil, failure)
			writeElevenlabsDialogueError(r.clientConn, failure)
			return err
		}
		r.chars.Add(int64(chars))
	}
}

// relayUpstream forwards ElevenLabs' frames to the client as they are until it closes the session. A close it ends
// with a code other than a normal one is a failure, which the session's settlement records.
func (r *elevenlabsDialogueRun) relayUpstream() error {
	for {
		messageType, message, err := r.upstream.ReadMessage()
		if err != nil {
			// The client left, and the gateway closed the provider's socket to end the session: nothing failed.
			if r.clientConn.closing() {
				return nil
			}
			var closeErr *ws.CloseError
			// 1006 and 1015 are what a read reports for a lost connection, not codes a close frame may carry.
			if errors.As(err, &closeErr) && closeErr.Code != ws.CloseAbnormalClosure && closeErr.Code != ws.CloseTLSHandshake {
				if !isNormalWebSocketClosure(err) {
					r.failure.CompareAndSwap(nil, newRealtimeWireBifrostError(502, "server_error", "the provider closed the session: "+closeErr.Text))
				}
				// The provider's own close, code and reason, is what the client reads.
				_ = r.clientConn.conn.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(closeErr.Code, closeErr.Text), time.Now().Add(realtimeWSWriteTimeout))
				return nil
			}
			failure := newRealtimeWireBifrostError(502, "server_error", "the provider's websocket stream was interrupted")
			r.failure.CompareAndSwap(nil, failure)
			writeElevenlabsDialogueError(r.clientConn, failure)
			return err
		}
		if err := r.clientConn.WriteMessage(messageType, message); err != nil {
			return err
		}
	}
}
