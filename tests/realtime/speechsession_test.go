package realtime

import (
	"encoding/base64"
	"testing"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Speech sessions: text streams in over /v1/audio/speech/stream and the provider's audio streams back
// while the text is still being written. A session is one request against the virtual key's limits and
// one row, and it is billed by the characters the client sent.

const speechRoute = "/v1/audio/speech/stream"

// speechProviders are the providers that serve a session, with what their config needs.
var speechProviders = map[string]struct {
	model string
	voice string
	extra map[string]any
}{
	"elevenlabs": {model: "elevenlabs/eleven_v4_turbo", voice: envOr("SPEECH_ELEVENLABS_VOICE", "21m00Tcm4TlvDq8ikWAM")},
	"soniox":     {model: "soniox/tts-rt-v2", voice: "Daniel", extra: map[string]any{"language_code": "en"}},
}

// speechClient connects and sends speech.config, and returns the client with the session.created it got.
func speechClient(t *testing.T, provider string, d dial) (*client, gjson.Result) {
	t.Helper()
	target := speechProviders[provider]
	d.route = speechRoute
	c := connect(t, d)
	c.send(speechConfig(target.model, target.voice, target.extra))
	return c, c.expect("speech.session.created")[0]
}

// untilSegmentDone reads the frames of a segment: its audio, tagged with it, up to speech.segment.done.
func untilSegmentDone(t *testing.T, c *client, segment int) (audioBytes int) {
	t.Helper()
	for _, frame := range c.until("speech.segment.done") {
		switch frame.Get("type").Str {
		case "speech.audio.delta":
			assert.EqualValues(t, segment, frame.Get("segment").Int(), "audio of another segment: %s", frame.Raw)
			audio, err := base64.StdEncoding.DecodeString(frame.Get("audio").Str)
			require.NoError(t, err, "audio is base64")
			audioBytes += len(audio)
		case "speech.segment.done":
			assert.EqualValues(t, segment, frame.Get("segment").Int())
		}
	}
	return audioBytes
}

// A flush speaks what the provider holds and closes a segment, the end closes the last one and the
// session, and the client is told how many characters it is billed for.
func TestSpeech_FlushedSegmentsAreSpokenInOrderAndBilledByCharacters(t *testing.T) {
	for provider := range speechProviders {
		t.Run(provider, func(t *testing.T) {
			requireGateway(t)
			t.Parallel()
			c, created := speechClient(t, provider, dial{})
			assert.Equal(t, speechProviders[provider].model, created.Get("session.model").Str)
			assert.Equal(t, "pcm", created.Get("session.response_format").Str)
			assert.NotEmpty(t, created.Get("session.id").Str)
			assert.Positive(t, created.Get("session.sample_rate").Int(), "a raw format names its sample rate")

			first, second := "Hello there, this is a short test of the gateway. ", "And one more sentence, after the flush."
			c.send(speechText("Hello there, "), speechText("this is a short test of the gateway. "), speechFlush())
			assert.Positive(t, untilSegmentDone(t, c, 1))

			c.send(speechText(second), speechEnd())
			assert.Positive(t, untilSegmentDone(t, c, 2))
			done := c.until("speech.audio.done")
			assert.EqualValues(t, utf8.RuneCountInString(first+second), done[len(done)-1].Get("usage.input_chars").Int())
			c.expectClosed()
			assert.EqualValues(t, websocket.CloseNormalClosure, c.closeCode.Load())

			row := waitSpeechRows(t, c.vk, 1)[0]
			assert.Equal(t, "success", row.Status)
			assert.Equal(t, provider, row.Provider)
			assert.Equal(t, c.vk.ID, row.VirtualKeyID)
		})
	}
}

// A cancel drops what is pending and says so; nothing of the dropped segment follows, and the session
// speaks again.
func TestSpeech_ACancelIsAcknowledgedAndTheSessionKeepsSpeaking(t *testing.T) {
	for provider := range speechProviders {
		t.Run(provider, func(t *testing.T) {
			requireGateway(t)
			t.Parallel()
			c, _ := speechClient(t, provider, dial{})

			c.send(speechText("This is a long sentence that takes a while to say out loud, so there is plenty of audio still to come when it is cancelled. "), speechFlush())
			c.until("speech.audio.delta")
			c.send(speechCancel())
			frames := c.until("speech.cancelled")
			cancelled := frames[len(frames)-1].Get("segment").Int()
			require.Positive(t, cancelled)

			c.send(speechText("Back after the cancel."), speechEnd())
			for _, frame := range c.until("speech.audio.done") {
				if frame.Get("type").Str != "speech.audio.done" {
					assert.Greater(t, frame.Get("segment").Int(), cancelled, "a frame of the cancelled segment followed the cancel: %s", frame.Raw)
				}
			}
			c.expectClosed()
			waitSpeechRows(t, c.vk, 1)
		})
	}
}

// Whatever cannot open a session is answered in-band before the provider is called, and a refused
// upgrade is a plain HTTP status.
func TestSpeech_RefusalsComeBeforeAnythingOpens(t *testing.T) {
	requireGateway(t)
	t.Parallel()

	status, _ := refused(t, dial{route: speechRoute, anonymous: true})
	assert.Equal(t, 401, status)

	c := connect(t, dial{route: speechRoute})
	c.send(speechText("no config first"))
	c.expectError("invalid_request_error", "must be speech.config")
	c.expectClosed()

	c = connect(t, dial{route: speechRoute})
	c.send(speechConfig("openai/tts-1", "alloy", nil))
	c.expectError("invalid_request_error", "does not support incremental speech")
	c.expectClosed()

	c = connect(t, dial{route: speechRoute})
	c.send(speechConfig("elevenlabs/eleven_flash_v2_5", speechProviders["elevenlabs"].voice, nil))
	c.expectError(anyType, "eleven_flash_v2_5")
	c.expectClosed()
}

// A session is one request against the virtual key's limits, checked when it opens.
func TestSpeech_RequestLimitStopsTheNextSession(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{maxRequests: 1})
	c, _ := speechClient(t, "elevenlabs", dial{vk: vk})
	c.send(speechText("Say first."), speechEnd())
	c.until("speech.audio.done")
	waitSpeechRows(t, vk, 1)

	second := connect(t, dial{route: speechRoute, vk: vk})
	second.send(speechConfig(speechProviders["elevenlabs"].model, speechProviders["elevenlabs"].voice, nil))
	second.expectError("rate_limit_exceeded", "")
	second.expectClosed()
}
