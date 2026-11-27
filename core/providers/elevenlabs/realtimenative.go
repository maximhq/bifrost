package elevenlabs

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"unsafe"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

var (
	_ schemas.RealtimeProvider       = (*scribeNativeProtocol)(nil)
	_ schemas.RealtimeUsageExtractor = (*scribeNativeProtocol)(nil)
)

// The ElevenLabs WebSocket endpoints a client may call through the gateway.
const (
	NativeScribeRoute   = "/v1/speech-to-text/realtime"
	NativeDialogueRoute = "/v1/text-to-dialogue/stream-input"
)

// nativeQueryCredentials are the query parameters that carry a client's own credential, which never goes upstream.
var nativeQueryCredentials = [...]string{"token", "xi-api-key"}

// NativeRealtimeDial addresses an ElevenLabs endpoint on the key's base URL with the client's query as it was sent,
// less its credentials, and the model the gateway resolved.
func (provider *ElevenlabsProvider) NativeRealtimeDial(ctx *schemas.BifrostContext, key schemas.Key, route, rawQuery, model string) (string, map[string]string, *schemas.BifrostError) {
	if route != NativeScribeRoute && route != NativeDialogueRoute {
		return "", nil, providerUtils.NewBifrostBadRequestError("elevenlabs has no native realtime route " + route)
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", nil, providerUtils.NewBifrostBadRequestError("invalid query: " + err.Error())
	}
	for _, name := range nativeQueryCredentials {
		query.Del(name)
	}
	query.Set("model_id", model)
	headers, bifrostErr := provider.RealtimeHeaders(ctx, key)
	if bifrostErr != nil {
		return "", nil, bifrostErr
	}
	return provider.realtimeBaseURL() + route + "?" + query.Encode(), headers, nil
}

// NativeRealtimeProtocol returns the hooks of one Scribe connection, which meter its audio from the format the
// client's query names.
func (provider *ElevenlabsProvider) NativeRealtimeProtocol(route, rawQuery string) (schemas.RealtimeProvider, *schemas.BifrostError) {
	if route != NativeScribeRoute {
		return nil, providerUtils.NewBifrostBadRequestError("elevenlabs governs no native realtime route " + route + " turn by turn")
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, providerUtils.NewBifrostBadRequestError("invalid query: " + err.Error())
	}
	return &scribeNativeProtocol{bytesPerSecond: scribeAudioBytesPerSecond(query.Get("audio_format"))}, nil
}

// scribeAudioBytesPerSecond is the size of a second of the audio an audio_format names: 16-bit mono for pcm_N,
// a byte a sample for ulaw_N and alaw_N. Scribe takes pcm_16000 when the query names none.
func scribeAudioBytesPerSecond(audioFormat string) int64 {
	codec, rate, ok := strings.Cut(audioFormat, "_")
	hertz, err := strconv.Atoi(rate)
	if !ok || err != nil || hertz <= 0 {
		return 16000 * 2
	}
	if codec == "ulaw" || codec == "alaw" {
		return int64(hertz)
	}
	return int64(hertz) * 2
}

// scribeNativeProtocol governs one connection of a client that speaks Scribe's own protocol through the gateway,
// whose frames the relay passes on as they are. A turn is the audio sent from one committed transcript to the
// next, and its usage is the audio seconds, which Scribe does not report: the frames are the only count.
type scribeNativeProtocol struct {
	bytesPerSecond int64
	audio          atomic.Int64 // bytes of audio the client sent since the last committed transcript
}

// ToBifrostRealtimeEvent reads what a frame is to the turn pipeline and meters the audio a client frame carries.
// RawData keeps the frame as it was, which is what is forwarded.
func (p *scribeNativeProtocol) ToBifrostRealtimeEvent(frame json.RawMessage) (*schemas.BifrostRealtimeEvent, error) {
	var messageType, audio, text, errText string
	var commit bool
	// The frame is read in place; the relay allocates one per message and never reuses it, so the strings pointing into it stay valid.
	gjson.Parse(unsafe.String(unsafe.SliceData(frame), len(frame))).ForEach(func(name, value gjson.Result) bool {
		switch name.Str {
		case "message_type":
			messageType = value.Str
		case "audio_base_64":
			audio = value.Str
		case "commit":
			commit = value.Bool()
		case "text":
			text = value.Str
		case "error":
			errText = value.Str
		}
		return true
	})

	event := &schemas.BifrostRealtimeEvent{Type: schemas.RealtimeEventType(messageType), RawData: frame}
	switch {
	case errText != "":
		event.Error = &schemas.RealtimeError{Type: messageType, Code: messageType, Message: errText}
	case messageType == "input_audio_chunk":
		p.audio.Add(base64DecodedLen(audio))
		event.Type = schemas.RTEventInputAudioAppend
		if commit {
			event.Type = schemas.RTEventInputAudioCommit
		}
	case messageType == "session_started":
		event.Type = schemas.RTEventSessionCreated
		event.Session = &schemas.RealtimeSession{ID: gjson.GetBytes(frame, "session_id").Str}
	case messageType == "committed_transcript":
		transcript, err := schemas.MarshalSorted(text)
		if err != nil {
			return nil, err
		}
		event.Type = schemas.RTEventInputAudioTransCompleted
		event.ExtraParams = map[string]json.RawMessage{"transcript": transcript}
	}
	return event, nil
}

// base64DecodedLen is the length of what a base64 string decodes to, from its length and padding alone.
func base64DecodedLen(encoded string) int64 {
	n := int64(len(encoded)) / 4 * 3
	if strings.HasSuffix(encoded, "==") {
		return n - 2
	}
	if strings.HasSuffix(encoded, "=") {
		return n - 1
	}
	return n + int64(len(encoded)%4)*3/4
}

// ToProviderRealtimeEvent forwards the client's frame as it was sent.
func (p *scribeNativeProtocol) ToProviderRealtimeEvent(event *schemas.BifrostRealtimeEvent) (json.RawMessage, error) {
	return event.RawData, nil
}

// ExtractRealtimeTurnUsage reports the audio sent since the last committed transcript, and starts the next count.
func (p *scribeNativeProtocol) ExtractRealtimeTurnUsage(_ []byte) *schemas.BifrostLLMUsage {
	audio := p.audio.Swap(0)
	if audio <= 0 {
		return nil
	}
	seconds := float64(audio) / float64(p.bytesPerSecond)
	return &schemas.BifrostLLMUsage{AudioSeconds: &seconds}
}

// ExtractRealtimeTurnOutput has none: the turn's text is the transcript, which is its input.
func (p *scribeNativeProtocol) ExtractRealtimeTurnOutput(_ []byte) *schemas.ChatMessage { return nil }

// ShouldStartRealtimeTurn starts a turn at the first audio of a segment, so the plugins admit it before the
// audio goes upstream.
func (p *scribeNativeProtocol) ShouldStartRealtimeTurn(event *schemas.BifrostRealtimeEvent) bool {
	return event.Type == schemas.RTEventInputAudioAppend || event.Type == schemas.RTEventInputAudioCommit
}

// RealtimeTurnFinalEvent is the committed transcript, which ends the segment.
func (p *scribeNativeProtocol) RealtimeTurnFinalEvent() schemas.RealtimeEventType {
	return schemas.RTEventInputAudioTransCompleted
}

func (p *scribeNativeProtocol) ShouldForwardRealtimeEvent(_ *schemas.BifrostRealtimeEvent) bool {
	return true
}

func (p *scribeNativeProtocol) ShouldAccumulateRealtimeOutput(_ schemas.RealtimeEventType) bool {
	return false
}

func (p *scribeNativeProtocol) SupportsRealtimeAPI() bool { return true }

func (p *scribeNativeProtocol) SupportsRealtimeWebRTC() bool { return false }

func (p *scribeNativeProtocol) RealtimeWebRTCDataChannelLabel() string { return "" }

func (p *scribeNativeProtocol) RealtimeWebSocketSubprotocol() string { return "" }

// RealtimeWebSocketURL is never asked: the native route is addressed by NativeRealtimeDial.
func (p *scribeNativeProtocol) RealtimeWebSocketURL(_ *schemas.BifrostContext, _ schemas.Key, _, _ string) (string, *schemas.BifrostError) {
	return "", providerUtils.NewUnsupportedOperationError(schemas.RealtimeRequest, schemas.Elevenlabs)
}

// RealtimeHeaders is never asked: the native route is addressed by NativeRealtimeDial.
func (p *scribeNativeProtocol) RealtimeHeaders(_ *schemas.BifrostContext, _ schemas.Key) (map[string]string, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RealtimeRequest, schemas.Elevenlabs)
}

// ExchangeRealtimeWebRTCSDP is never asked: a Scribe client speaks WebSocket.
func (p *scribeNativeProtocol) ExchangeRealtimeWebRTCSDP(_ *schemas.BifrostContext, _ schemas.Key, _ string, _ string, _ json.RawMessage) (string, *schemas.BifrostError) {
	return "", providerUtils.NewUnsupportedOperationError(schemas.RealtimeRequest, schemas.Elevenlabs)
}
