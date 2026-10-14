package xai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

var _ schemas.RealtimeProvider = (*XAIProvider)(nil)
var _ schemas.RealtimeSessionProvider = (*XAIProvider)(nil)
var _ schemas.RealtimeUsageExtractor = (*XAIProvider)(nil)

// SupportsRealtimeAPI advertises xAI's native JSON-over-WebSocket voice protocol.
func (provider *XAIProvider) SupportsRealtimeAPI() bool { return true }

// RealtimeWebSocketURL retains the configured origin and explicit model without inventing a transcription-only intent.
func (provider *XAIProvider) RealtimeWebSocketURL(_ schemas.Key, model, intent string) (string, *schemas.BifrostError) {
	if strings.TrimSpace(model) == "" || intent != "" {
		return "", xaiRealtimeError(400, "xai realtime requires a model and does not support a transcription-only intent", nil)
	}
	endpoint, err := url.Parse(provider.networkConfig.BaseURL.GetValue())
	if err != nil || endpoint == nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return "", xaiRealtimeError(400, "invalid xai realtime base URL", err)
	}
	switch endpoint.Scheme {
	case "https":
		endpoint.Scheme = "wss"
	case "http":
		endpoint.Scheme = "ws"
	default:
		return "", xaiRealtimeError(400, "xai realtime base URL must use HTTP or HTTPS", nil)
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/realtime"
	endpoint.RawPath = ""
	query := url.Values{}
	query.Set("model", model)
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

// RealtimeHeaders binds the selected provider key while preserving configured non-authentication headers.
func (provider *XAIProvider) RealtimeHeaders(_ *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	if strings.TrimSpace(key.Value.GetValue()) == "" {
		return nil, xaiRealtimeError(401, "xai realtime requires a provider credential", nil)
	}
	headers := map[string]string{}
	for name, value := range provider.networkConfig.ExtraHeaders {
		if !strings.EqualFold(name, "Authorization") {
			headers[name] = value
		}
	}
	headers["Authorization"] = "Bearer " + key.Value.GetValue()
	return headers, nil
}

// SupportsRealtimeWebRTC leaves SDP disabled because xAI's documented voice endpoint is a WebSocket.
func (provider *XAIProvider) SupportsRealtimeWebRTC() bool { return false }

// ExchangeRealtimeWebRTCSDP reports the unsupported transport without fabricating an SDP answer.
func (provider *XAIProvider) ExchangeRealtimeWebRTCSDP(_ *schemas.BifrostContext, _ schemas.Key, _ string, _ string, _ json.RawMessage) (string, *schemas.BifrostError) {
	return "", xaiRealtimeError(400, "xai realtime does not support WebRTC SDP exchange", nil)
}

// RealtimeWebRTCDataChannelLabel has no value for the unsupported WebRTC transport.
func (provider *XAIProvider) RealtimeWebRTCDataChannelLabel() string { return "" }

// RealtimeWebSocketSubprotocol requests no invented upstream protocol; provider authentication uses the selected bearer key.
func (provider *XAIProvider) RealtimeWebSocketSubprotocol() string { return "" }

// ShouldStartRealtimeTurn uses explicit generation or committed input to enter the existing governance turn pipeline.
func (provider *XAIProvider) ShouldStartRealtimeTurn(event *schemas.BifrostRealtimeEvent) bool {
	return event != nil && (event.Type == schemas.RTEventResponseCreate || event.Type == schemas.RTEventInputAudioBufferCommitted)
}

// RealtimeTurnFinalEvent identifies the native completed response, never an unfamiliar event or audio fragment.
func (provider *XAIProvider) RealtimeTurnFinalEvent() schemas.RealtimeEventType {
	return schemas.RTEventResponseDone
}

// ShouldForwardRealtimeEvent preserves unfamiliar native events for clients that understand them.
func (provider *XAIProvider) ShouldForwardRealtimeEvent(_ *schemas.BifrostRealtimeEvent) bool {
	return true
}

// ShouldAccumulateRealtimeOutput recognizes text deltas only; audio and cumulative input transcripts are not appended as text.
func (provider *XAIProvider) ShouldAccumulateRealtimeOutput(kind schemas.RealtimeEventType) bool {
	switch string(kind) {
	case "response.text.delta", "response.output_text.delta", "response.audio_transcript.delta", "response.output_audio_transcript.delta":
		return true
	}
	return false
}

// CreateRealtimeClientSecret performs actual upstream minting; the HTTP gateway owns identity-bound token replacement and expiry caching.
func (provider *XAIProvider) CreateRealtimeClientSecret(ctx *schemas.BifrostContext, key schemas.Key, raw json.RawMessage) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	if err := ctx.Err(); err != nil {
		return nil, xaiRealtimeError(499, "xai realtime mint cancelled", err)
	}
	body, err := xaiRealtimeMintBody(raw)
	if err != nil {
		return nil, xaiRealtimeError(400, "invalid xai realtime client-secret request", err)
	}
	headers, bifrostErr := provider.RealtimeHeaders(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI(strings.TrimRight(provider.networkConfig.BaseURL.GetValue(), "/") + "/v1/realtime/client_secrets")
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	req.SetBody(body)
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, providerUtils.SetErrorLatency(ParseXAIError(resp), latency)
	}
	responseBody, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, xaiRealtimeError(502, "failed to decode xai client-secret response", decodeErr)
	}
	responseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, responseHeaders)
	return &schemas.BifrostPassthroughResponse{StatusCode: resp.StatusCode(), Headers: responseHeaders, Body: responseBody, ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.XAI, RequestType: schemas.RealtimeRequest, Latency: latency.Milliseconds(), ProviderResponseHeaders: responseHeaders}}, nil
}

// xaiRealtimeMintBody removes gateway routing selectors while retaining native expiry and additive upstream fields without invented defaults.
func xaiRealtimeMintBody(raw []byte) ([]byte, error) {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || root == nil {
		return nil, fmt.Errorf("client-secret body must be an object")
	}
	delete(root, "model")
	if data, exists := root["session"]; exists {
		var session map[string]json.RawMessage
		if json.Unmarshal(data, &session) != nil || session == nil {
			return nil, fmt.Errorf("session must be an object")
		}
		delete(session, "model")
		if kind, exists := session["type"]; exists {
			if string(kind) != `"realtime"` {
				return nil, fmt.Errorf("xai client-secret minting requires a realtime session")
			}
			delete(session, "type")
		}
		// xAI mints connection credentials, not configured sessions. Dropping settings would falsely promise a binding.
		if len(session) != 0 {
			return nil, fmt.Errorf("configure xai voice settings with session.update after connecting")
		}
		delete(root, "session")
	}
	return providerUtils.MarshalSorted(root)
}

// xaiRealtimeError attaches provider identity without including credentials or response payloads in local errors.
func xaiRealtimeError(status int, message string, err error) *schemas.BifrostError {
	result := providerUtils.NewBifrostOperationError(message, err)
	result.StatusCode = schemas.Ptr(status)
	result.ExtraFields.Provider = schemas.XAI
	result.ExtraFields.RequestType = schemas.RealtimeRequest
	return result
}

// ToBifrostRealtimeEvent inspects recognized shapes and retains exact native bytes, including unfamiliar event variants.
func (provider *XAIProvider) ToBifrostRealtimeEvent(raw json.RawMessage) (*schemas.BifrostRealtimeEvent, error) {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || root == nil {
		return nil, fmt.Errorf("invalid xai realtime event")
	}
	var kind string
	if json.Unmarshal(root["type"], &kind) != nil || kind == "" {
		return nil, fmt.Errorf("xai realtime event requires a type")
	}
	projection := map[string]json.RawMessage{"type": root["type"]}
	if id := root["event_id"]; id != nil {
		projection["event_id"] = id
	}
	nested := ""
	switch schemas.RealtimeEventType(kind) {
	case schemas.RTEventSessionUpdate, schemas.RTEventSessionCreated, schemas.RTEventSessionUpdated:
		nested = "session"
	case schemas.RTEventConversationItemCreate, schemas.RTEventConversationItemAdded, schemas.RTEventConversationItemCreated, schemas.RTEventConversationItemRetrieved, schemas.RTEventConversationItemDone, schemas.RTEventResponseOutputItemAdded, schemas.RTEventResponseOutputItemDone:
		nested = "item"
	case schemas.RTEventError:
		nested = "error"
	}
	if nested != "" && root[nested] != nil {
		projection[nested] = root[nested]
	}
	normalized, err := providerUtils.MarshalSorted(projection)
	if err != nil {
		return nil, err
	}
	event, err := schemas.ParseRealtimeEvent(normalized)
	if err != nil {
		return nil, err
	}
	event.RawData = append(json.RawMessage(nil), raw...)
	event.ExtraParams = map[string]json.RawMessage{}
	for name, value := range root {
		if name != "type" && name != "event_id" && name != nested {
			event.ExtraParams[name] = value
		}
	}
	switch kind {
	case "response.text.delta", "response.output_text.delta", "response.audio_transcript.delta", "response.output_audio_transcript.delta", "response.function_call_arguments.delta", "response.audio.delta", "response.output_audio.delta":
		var delta string
		if json.Unmarshal(root["delta"], &delta) != nil {
			return nil, fmt.Errorf("recognized xai realtime delta must be a string")
		}
		event.Delta = &schemas.RealtimeDelta{Text: delta}
		if kind == "response.audio.delta" || kind == "response.output_audio.delta" {
			event.Delta.Text = ""
			event.Delta.Audio = delta
		}
	}
	return event, nil
}

// ToProviderRealtimeEvent applies canonical edits over retained native bytes so explicit clears and opaque metadata survive translation.
func (provider *XAIProvider) ToProviderRealtimeEvent(event *schemas.BifrostRealtimeEvent) (json.RawMessage, error) {
	if event == nil {
		return nil, fmt.Errorf("xai realtime event is nil")
	}
	codec := &openai.OpenAIProvider{}
	encoded, err := codec.ToProviderRealtimeEvent(event)
	if err != nil {
		return nil, err
	}
	if len(event.RawData) == 0 {
		return encoded, nil
	}
	// RawData for client events comes from the canonical parser; provider events use the native decoder.
	baseline, err := schemas.ParseRealtimeEvent(event.RawData)
	if err != nil {
		baseline, err = provider.ToBifrostRealtimeEvent(event.RawData)
	}
	if err != nil {
		return nil, err
	}
	before, err := codec.ToProviderRealtimeEvent(baseline)
	if err != nil {
		return nil, err
	}
	result, err := xaiRealtimePatch(event.RawData, before, encoded)
	if err != nil {
		return nil, err
	}
	if event.Type == schemas.RTEventSessionUpdate {
		// These are server-owned credentials/metadata, not mutable session configuration.
		for _, path := range []string{"session.id", "session.object", "session.expires_at", "session.client_secret"} {
			result, err = providerUtils.DeleteJSONField(result, path)
			if err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

// xaiRealtimePatch changes only canonical fields that actually changed, retaining exact JSON values for untouched provider data.
func xaiRealtimePatch(original, before, after []byte) ([]byte, error) {
	if bytes.Equal(before, after) {
		return append([]byte(nil), original...), nil
	}
	var oldFields, newFields, rawFields map[string]json.RawMessage
	if json.Unmarshal(before, &oldFields) != nil || oldFields == nil || json.Unmarshal(after, &newFields) != nil || newFields == nil || json.Unmarshal(original, &rawFields) != nil || rawFields == nil {
		return append([]byte(nil), after...), nil
	}
	for name, old := range oldFields {
		next, exists := newFields[name]
		if !exists {
			delete(rawFields, name)
			continue
		}
		if bytes.Equal(old, next) {
			continue
		}
		patched, err := xaiRealtimePatch(rawFields[name], old, next)
		if err != nil {
			return nil, err
		}
		rawFields[name] = patched
	}
	for name, next := range newFields {
		if _, exists := oldFields[name]; !exists {
			rawFields[name] = next
		}
	}
	return providerUtils.MarshalSorted(rawFields)
}

// ExtractRealtimeTurnUsage reads actual compatible terminal usage and never estimates missing measurements.
func (provider *XAIProvider) ExtractRealtimeTurnUsage(raw []byte) *schemas.BifrostLLMUsage {
	var envelope struct {
		Type     string `json:"type"`
		Response struct {
			Usage map[string]json.RawMessage `json:"usage"`
		} `json:"response"`
		Usage map[string]json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Type != string(schemas.RTEventResponseDone) {
		return nil
	}
	usage := envelope.Response.Usage
	if usage == nil {
		usage = envelope.Usage
	}
	if len(usage) == 0 {
		return nil
	}
	var kind string
	if value, exists := usage["type"]; exists {
		if json.Unmarshal(value, &kind) != nil || (kind != "duration" && kind != "tokens") {
			return nil
		}
	}
	if kind != "duration" {
		// An unfamiliar accounting shape is not a measured zero-token turn.
		for _, name := range []string{"input_tokens", "output_tokens", "total_tokens"} {
			var count int
			value, exists := usage[name]
			if !exists || bytes.Equal(value, []byte("null")) || json.Unmarshal(value, &count) != nil || count < 0 {
				return nil
			}
		}
	}
	return (&openai.OpenAIProvider{}).ExtractRealtimeTurnUsage(raw)
}

// ExtractRealtimeTurnOutput projects actual terminal assistant text and correlated tool calls using the shared compatible wire shape.
func (provider *XAIProvider) ExtractRealtimeTurnOutput(raw []byte) *schemas.ChatMessage {
	return (&openai.OpenAIProvider{}).ExtractRealtimeTurnOutput(raw)
}
