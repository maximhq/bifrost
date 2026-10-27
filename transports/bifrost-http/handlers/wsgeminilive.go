package handlers

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	ws "github.com/fasthttp/websocket"
	geminiProvider "github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// maxCloseReasonBytes is what a close frame's reason may hold: the 125 bytes of a control frame, less the code.
const maxCloseReasonBytes = 123

// handleGeminiLiveUpgrade serves a client that speaks the Gemini Live protocol itself, such as the Google
// GenAI SDK. Live names the model in the first frame instead of the URL, so, like a transcription session,
// the connection is admitted after the upgrade. The credential is still checked before it.
func (h *WSRealtimeHandler) handleGeminiLiveUpgrade(ctx *fasthttp.RequestCtx) {
	path := string(ctx.Path())
	auth := captureAuthHeaders(ctx)
	preReqCtx, preReqCancel := createBifrostContextFromAuth(h.handlerStore, auth)
	preReqCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.RealtimeRequest)
	preReqCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "genai")
	if authErr := refuseUnauthenticatedRealtime(h.config.ClientConfig.EnforceAuthOnInference, preReqCtx, auth.authorization); authErr != nil {
		preReqCancel()
		SendBifrostError(ctx, authErr)
		return
	}
	populateRealtimeRequestContext(ctx, preReqCtx)

	upgrader := h.websocketUpgrader("")
	err := upgrader.Upgrade(ctx, func(conn *ws.Conn) {
		defer conn.Close()
		defer recoverWebSocketSession(conn)
		defer preReqCancel()
		clientConn := newRealtimeClientConn(conn)
		clientConn.native = true

		setup, rawModel, setupErr := bufferGeminiLiveSetup(clientConn)
		if setupErr != nil {
			clientConn.writeRealtimeError(newRealtimeWireBifrostError(400, "invalid_request_error", setupErr.Error()))
			return
		}
		h.runBootstrappedRealtimeSession(ctx, conn, clientConn, preReqCtx, preReqCancel, auth, path, rawModel, "", []realtimeWebSocketFrame{setup})
	})
	if err != nil {
		preReqCancel()
		logger.Warn("websocket upgrade failed for %s: %v", path, err)
	}
}

// bufferGeminiLiveSetup reads the setup message that opens a Live session, and the model it names.
func bufferGeminiLiveSetup(clientConn *realtimeClientConn) (realtimeWebSocketFrame, string, error) {
	if err := clientConn.conn.SetReadDeadline(time.Now().Add(realtimeTranscriptionBootstrapTimeout)); err != nil {
		return realtimeWebSocketFrame{}, "", err
	}
	defer clientConn.refreshReadDeadline()

	messageType, message, err := clientConn.conn.ReadMessage()
	if err != nil {
		return realtimeWebSocketFrame{}, "", fmt.Errorf("the setup message was not received: %w", err)
	}
	if len(message) > realtimeTranscriptionBootstrapMaxBytes {
		return realtimeWebSocketFrame{}, "", fmt.Errorf("the setup message exceeds %d bytes", realtimeTranscriptionBootstrapMaxBytes)
	}
	model, err := geminiProvider.LiveSetupModel(message)
	if err != nil {
		return realtimeWebSocketFrame{}, "", err
	}
	return realtimeWebSocketFrame{messageType: messageType, data: message}, model, nil
}

// writeNativeClose reports a failure to a client that speaks the provider's own protocol the way Gemini Live
// does, by closing with a code and a reason, because that client has no error event to receive.
func (c *realtimeClientConn) writeNativeClose(bifrostErr *schemas.BifrostError) {
	code, reason := nativeCloseFrame(bifrostErr)
	_ = c.conn.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(code, reason), time.Now().Add(realtimeWSPingWriteTimeout))
}

// nativeCloseFrame maps an error to the close code Live would use for it, and a reason that fits a close frame.
func nativeCloseFrame(bifrostErr *schemas.BifrostError) (int, string) {
	_, _, message, _ := mapRealtimeWireErrorFields(bifrostErr)
	status := 500
	if bifrostErr != nil && bifrostErr.StatusCode != nil {
		status = *bifrostErr.StatusCode
	}
	code := ws.CloseInternalServerErr
	switch status {
	case 400:
		code = ws.CloseInvalidFramePayloadData
	case 401, 403, 404:
		code = ws.ClosePolicyViolation
	case 429:
		code = ws.CloseTryAgainLater
	}
	message = strings.ToValidUTF8(message, "")
	for len(message) > maxCloseReasonBytes {
		_, size := utf8.DecodeLastRuneInString(message)
		message = message[:len(message)-size]
	}
	return code, message
}
