package realtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Protocol: client events the gateway refuses, rewrites or must not forward.

// One response at a time: a second response.create while one is running is refused by the
// gateway, which never forwards it, and the running turn finishes normally.
func TestProtocol_SecondResponseWhileOneIsActiveIsRefused(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	s.client.send(responseCreate())
	s.upstream.expect("response.create")

	s.client.send(responseCreate())
	s.client.expectError("invalid_request_error", "already has an active response")
	s.client.send(inputAudioClear())
	s.upstream.expect("input_audio_buffer.clear") // and not the second response.create

	reply := textReply("resp_1", "Only once.", tokens{input: 5, output: 2})
	s.upstream.send(reply...)
	s.client.expectRelayed(reply...)
	assert.Equal(t, "success", s.turnRow().Status)
}

func TestProtocol_MalformedJSONIsRefusedAndTheSessionGoesOn(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)

	s.client.sendRaw(`{"type": "response.create"`)
	s.client.expectError("invalid_request_error", "failed to parse realtime event JSON")

	s.client.send(inputAudioClear())
	s.upstream.expect("input_audio_buffer.clear")
}

func TestProtocol_BinaryFrameEndsTheSession(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)

	s.client.sendBinary([]byte{0x00, 0x01, 0x02})

	s.client.expectError("invalid_request_error", "only accepts text messages")
	s.client.expectClosed()
	s.upstream.expectClosed()
}

// Model names inside a session.update are written the way Bifrost's clients write them,
// provider/model; the provider only knows the bare name.
func TestProtocol_SessionUpdateModelPrefixesAreStripped(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)

	s.client.send(sessionUpdate(map[string]any{
		"type":  "realtime",
		"audio": map[string]any{"input": map[string]any{"transcription": map[string]any{"model": "openai/gpt-4o-transcribe"}}},
	}))

	update := s.upstream.expect("session.update")[0]
	assert.Equal(t, "gpt-4o-transcribe", update.Get("session.audio.input.transcription.model").Str)
}

// A transcription session names its model in the first session.update rather than in the URL.
// The gateway holds the frames until it knows the model, dials upstream with intent=transcription,
// and each completed transcription is a turn.
func TestProtocol_TranscriptionSessionDiscoversItsModelInBand(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	model := newModel(t)
	c := connect(t, dial{query: "intent=transcription"})

	c.send(sessionUpdate(map[string]any{
		"type":  "transcription",
		"audio": map[string]any{"input": map[string]any{"transcription": map[string]any{"model": "openai/" + model}}},
	}))
	up := upstream.session(t, model)
	c.expect("session.created")
	assert.Equal(t, "transcription", up.Intent)
	up.expect("session.update")

	speech := []event{inputAudioCommitted("item_1"), transcriptionCompleted("item_1", "Hello there.")}
	up.send(speech...)
	c.expectRelayed(speech...)

	row := waitTurnRows(t, model, 1)[0]
	assert.Equal(t, "success", row.Status)
	assert.Contains(t, row.raw.Raw, "Hello there.")
}
