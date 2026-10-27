package handlers

import (
	"encoding/json"

	ws "github.com/fasthttp/websocket"
	openaiProvider "github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	bfws "github.com/maximhq/bifrost/transports/v2/bifrost-http/websocket"
)

// openAIRealtimeWire supplies the OpenAI Realtime rules for the client side of a translated connection.
var openAIRealtimeWire = &openaiProvider.OpenAIProvider{}

// realtimeClientProtocol returns the provider whose realtime rules govern the stream the client sees: the
// provider itself when its frames are relayed as they are, OpenAI's when a translator turns them into
// OpenAI Realtime events.
func realtimeClientProtocol(provider schemas.RealtimeProvider) schemas.RealtimeProvider {
	if _, ok := provider.(schemas.RealtimeTranslatorProvider); ok {
		return openAIRealtimeWire
	}
	return provider
}

// newRealtimeTranslator builds the connection's translator, or returns nil when the provider's frames are
// relayed as they are.
func newRealtimeTranslator(provider schemas.RealtimeProvider, key schemas.Key, model string) (schemas.RealtimeTranslator, *schemas.BifrostError) {
	translating, ok := provider.(schemas.RealtimeTranslatorProvider)
	if !ok {
		return nil, nil
	}
	translator, bifrostErr := translating.NewRealtimeTranslator(key, model)
	if bifrostErr == nil && translator == nil {
		return nil, newRealtimeWireBifrostError(500, "server_error", "provider returned no realtime translator")
	}
	return translator, bifrostErr
}

// writeRealtimeProviderFrames sends a translation's provider frames, in order.
func writeRealtimeProviderFrames(upstream schemas.RealtimeUpstream, frames []json.RawMessage) error {
	for _, frame := range frames {
		if err := upstream.WriteMessage(ws.TextMessage, frame); err != nil {
			return err
		}
	}
	return nil
}

// forwardTranslatedRealtimeFrames runs a translation's client events through the turn pipeline and writes
// them, exactly as if the provider had sent them. stop reports that the relay must end with err.
func (h *WSRealtimeHandler) forwardTranslatedRealtimeFrames(
	clientConn *realtimeClientConn,
	session *bfws.Session,
	provider schemas.RealtimeProvider,
	bifrostCtx *schemas.BifrostContext,
	providerKey schemas.ModelProvider,
	model string,
	key schemas.Key,
	transcriptionSession bool,
	frames []json.RawMessage,
	lastEventWasError *bool,
) (bool, error) {
	for _, frame := range frames {
		if stop, err := h.forwardRealtimeProviderFrame(clientConn, session, provider, bifrostCtx, providerKey, model, key, transcriptionSession, ws.TextMessage, frame, lastEventWasError); stop {
			return true, err
		}
	}
	return false, nil
}

// relayTranslatedProviderFrame translates one provider frame and relays the result. The provider frames
// the translation asks for are written while the translation lock is held, so they keep the translator's
// order across both relay directions; the client events then run through the turn pipeline.
func (h *WSRealtimeHandler) relayTranslatedProviderFrame(
	clientConn *realtimeClientConn,
	session *bfws.Session,
	upstream schemas.RealtimeUpstream,
	translator schemas.RealtimeTranslator,
	provider schemas.RealtimeProvider,
	bifrostCtx *schemas.BifrostContext,
	providerKey schemas.ModelProvider,
	model string,
	key schemas.Key,
	transcriptionSession bool,
	message []byte,
	lastEventWasError *bool,
) (bool, error) {
	var translation schemas.RealtimeTranslation
	var translateErr, writeErr error
	func() {
		session.LockRealtimeTranslator()
		defer session.UnlockRealtimeTranslator()
		if translation, translateErr = translator.ToClient(message); translateErr == nil {
			writeErr = writeRealtimeProviderFrames(upstream, translation.Provider)
		}
	}()
	if translateErr != nil {
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
		return true, translateErr
	}
	if writeErr != nil {
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
		return true, writeErr
	}
	return h.forwardTranslatedRealtimeFrames(clientConn, session, provider, bifrostCtx, providerKey, model, key, transcriptionSession, translation.Client, lastEventWasError)
}

// relayTranslatedProviderClose gives the translator the provider's close reason, so a protocol that reports
// errors only through it can hand the client an error event before the close itself is relayed. The turn
// pipeline sees that event first, so the turn log records the provider's error rather than a generic one.
func (h *WSRealtimeHandler) relayTranslatedProviderClose(
	clientConn *realtimeClientConn,
	session *bfws.Session,
	translator schemas.RealtimeTranslator,
	provider schemas.RealtimeProvider,
	bifrostCtx *schemas.BifrostContext,
	providerKey schemas.ModelProvider,
	model string,
	key schemas.Key,
	transcriptionSession bool,
	closeErr *ws.CloseError,
) {
	var translation schemas.RealtimeTranslation
	func() {
		session.LockRealtimeTranslator()
		defer session.UnlockRealtimeTranslator()
		translation = translator.Close(closeErr.Code, closeErr.Text)
	}()
	_, _ = h.forwardTranslatedRealtimeFrames(clientConn, session, provider, bifrostCtx, providerKey, model, key, transcriptionSession, translation.Client, nil)
}

// openRealtimeTranslator runs the translator's Open step before either relay starts, so the client sees
// the events it expects first, such as session.created.
func (h *WSRealtimeHandler) openRealtimeTranslator(
	clientConn *realtimeClientConn,
	session *bfws.Session,
	upstream schemas.RealtimeUpstream,
	translator schemas.RealtimeTranslator,
	provider schemas.RealtimeProvider,
	bifrostCtx *schemas.BifrostContext,
	providerKey schemas.ModelProvider,
	model string,
	key schemas.Key,
	transcriptionSession bool,
) error {
	opening := translator.Open()
	if err := writeRealtimeProviderFrames(upstream, opening.Provider); err != nil {
		clientConn.writeRealtimeError(newRealtimeWireBifrostError(502, "server_error", "failed to write realtime event upstream"))
		return err
	}
	_, err := h.forwardTranslatedRealtimeFrames(clientConn, session, provider, bifrostCtx, providerKey, model, key, transcriptionSession, opening.Client, nil)
	return err
}
