package soniox

import (
	"encoding/json"
	"errors"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

var (
	_ schemas.RealtimeProvider           = (*SonioxProvider)(nil)
	_ schemas.RealtimeTranslatorProvider = (*SonioxProvider)(nil)
)

// SupportsRealtimeAPI reports that Soniox serves real-time transcription over WebSocket. Every session is
// translated to the OpenAI Realtime protocol, so the provider's own event conversion is never used.
func (provider *SonioxProvider) SupportsRealtimeAPI() bool {
	return true
}

// isRealtimeModel reports whether a model is one of Soniox's real-time transcription models, such as
// stt-rt-v5. Its asynchronous models are stt-async-*.
func isRealtimeModel(model string) bool {
	return strings.HasPrefix(model, "stt-rt-")
}

// NewRealtimeTranslator builds the translator of a transcription session.
func (provider *SonioxProvider) NewRealtimeTranslator(_ schemas.Key, model string) (schemas.RealtimeTranslator, *schemas.BifrostError) {
	if !isRealtimeModel(model) {
		return nil, providerUtils.NewBifrostBadRequestError("soniox serves realtime sessions with its stt-rt models, not " + model)
	}
	return newSonioxTranslator(model), nil
}

// RealtimeWebSocketURL returns the WSS URL of a transcription session, on the host of the base URL's region.
// Soniox has no other realtime service, and it takes the model in its start frame, not in the URL.
func (provider *SonioxProvider) RealtimeWebSocketURL(_ *schemas.BifrostContext, _ schemas.Key, model, intent string) (string, *schemas.BifrostError) {
	if intent != "transcription" {
		return "", providerUtils.NewUnsupportedOperationError(schemas.RealtimeRequest, provider.GetProviderKey())
	}
	if !isRealtimeModel(model) {
		return "", providerUtils.NewBifrostBadRequestError("soniox serves realtime sessions with its stt-rt models, not " + model)
	}
	return provider.realtimeURL, nil
}

// RealtimeHeaders returns the headers of the real-time WebSocket: the API key as a bearer token.
func (provider *SonioxProvider) RealtimeHeaders(_ *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	headers := map[string]string{"Authorization": "Bearer " + key.Value.GetValue()}
	for name, value := range provider.networkConfig.ExtraHeaders {
		if strings.EqualFold(name, "Authorization") {
			continue
		}
		headers[name] = value.GetValue()
	}
	return headers, nil
}

// SupportsRealtimeWebRTC returns false: Soniox has no WebRTC transport.
func (provider *SonioxProvider) SupportsRealtimeWebRTC() bool {
	return false
}

// ExchangeRealtimeWebRTCSDP is not supported by Soniox.
func (provider *SonioxProvider) ExchangeRealtimeWebRTCSDP(_ *schemas.BifrostContext, _ schemas.Key, _ string, _ string, _ json.RawMessage) (string, *schemas.BifrostError) {
	return "", providerUtils.NewBifrostBadRequestError("WebRTC SDP exchange is not supported by Soniox")
}

var errRealtimeTranslated = errors.New("soniox realtime sessions are translated to the OpenAI Realtime protocol, not relayed")

// ToBifrostRealtimeEvent is never used: a Soniox session is translated, not relayed.
func (provider *SonioxProvider) ToBifrostRealtimeEvent(json.RawMessage) (*schemas.BifrostRealtimeEvent, error) {
	return nil, errRealtimeTranslated
}

// ToProviderRealtimeEvent is never used: a Soniox session is translated, not relayed.
func (provider *SonioxProvider) ToProviderRealtimeEvent(*schemas.BifrostRealtimeEvent) (json.RawMessage, error) {
	return nil, errRealtimeTranslated
}

func (provider *SonioxProvider) ShouldStartRealtimeTurn(*schemas.BifrostRealtimeEvent) bool {
	return false
}

func (provider *SonioxProvider) RealtimeTurnFinalEvent() schemas.RealtimeEventType {
	return schemas.RTEventInputAudioTransCompleted
}

func (provider *SonioxProvider) RealtimeWebRTCDataChannelLabel() string {
	return ""
}

func (provider *SonioxProvider) RealtimeWebSocketSubprotocol() string {
	return ""
}

func (provider *SonioxProvider) ShouldForwardRealtimeEvent(*schemas.BifrostRealtimeEvent) bool {
	return true
}

func (provider *SonioxProvider) ShouldAccumulateRealtimeOutput(schemas.RealtimeEventType) bool {
	return false
}
