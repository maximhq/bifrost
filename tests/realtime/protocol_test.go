package realtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Protocol: client events the gateway refuses, rewrites or must not forward.

// One response at a time: a second response.create while one is running is refused by the
// gateway, which never forwards it (the provider would refuse it too, with an error of its own),
// and the running turn finishes normally.
func TestProtocol_SecondResponseWhileOneIsActiveIsRefused(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	s.configure(textSession())

	s.client.send(userMessage("Say hello."), responseCreate(), responseCreate())
	frames := s.client.collect("response.done")

	require.Equal(t, 1, countType(frames, "error"), "only the gateway's refusal: %s", typesOf(frames))
	assertError(t, findType(frames, "error"), "invalid_request_error", "already has an active response")
	assert.Equal(t, 1, countType(frames, "response.created"))
	assert.Equal(t, "success", s.turnRow().Status)
}

func TestProtocol_MalformedJSONIsRefusedAndTheSessionGoesOn(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)

	s.client.sendRaw(`{"type": "response.create"`)
	s.client.expectError("invalid_request_error", "failed to parse realtime event JSON")

	s.client.send(inputAudioClear())
	s.client.until("input_audio_buffer.cleared")
}

func TestProtocol_BinaryFrameEndsTheSession(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)

	s.client.sendBinary([]byte{0x00, 0x01, 0x02})

	s.client.expectError("invalid_request_error", "only accepts text messages")
	s.client.expectClosed()
}

// Model names inside a session.update are written the way Bifrost's clients write them,
// provider/model; the provider only knows the bare name, and would refuse the prefixed one.
func TestProtocol_SessionUpdateModelPrefixesAreStripped(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)

	updated := s.configure(map[string]any{
		"type":  "realtime",
		"audio": map[string]any{"input": map[string]any{"transcription": map[string]any{"model": "openai/" + transcribeModel}}},
	})

	assert.Equal(t, transcribeModel, updated.Get("session.audio.input.transcription.model").Str)
}

// A transcription session names its model in the first session.update rather than in the URL.
// The gateway holds the frames until it knows the model, dials upstream with intent=transcription,
// and each completed transcription is a turn.
func TestProtocol_TranscriptionSessionDiscoversItsModelInBand(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	c := connect(t, dial{query: "intent=transcription"})

	c.send(sessionUpdate(map[string]any{
		"type": "transcription",
		"audio": map[string]any{"input": map[string]any{
			"transcription":  map[string]any{"model": "openai/" + transcribeModel},
			"turn_detection": map[string]any{"type": "server_vad"},
		}},
	}))
	c.until("session.created")
	c.streamAudio("testdata/ask-weather.wav")
	frames := c.until("conversation.item.input_audio_transcription.completed")

	transcript := frames[len(frames)-1].Get("transcript").Str
	assert.Contains(t, strings.ToLower(transcript), "weather")
	row := waitTurnRows(t, c.vk, 1)[0]
	assert.Equal(t, "success", row.Status)
	assert.Equal(t, transcribeModel, row.Model)
	assert.Contains(t, row.raw.Raw, transcript)
}
