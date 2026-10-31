package realtime

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Nova: Amazon Nova 2 Sonic on Bedrock, served on the same OpenAI Realtime API. The gateway
// translates the protocol to Bedrock's bidirectional stream, so a client written for OpenAI works
// unchanged, within Nova's rules: configure the session before any audio, and keep audio flowing.

func openNovaSession(t *testing.T) *session {
	t.Helper()
	s := openSession(t, dial{query: "model=bedrock/" + novaModel})
	s.configure(map[string]any{"type": "realtime", "instructions": instructions})
	return s
}

func TestNova_TextTurnIsRelayedAndLogged(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openNovaSession(t)
	s.client.streamAudio("")

	done := s.ask("What is the capital of France?")

	require.Equal(t, "completed", done.Get("response.status").Str, "response.done: %s", done.Raw)
	row := s.turnRow()
	assert.Equal(t, "success", row.Status)
	assert.Equal(t, "bedrock", row.Provider)
	assert.Equal(t, novaModel, row.Model)
	assert.Equal(t, "bedrock", row.SelectedKeyName)
	assert.Equal(t, s.client.vk.ID, row.VirtualKeyID)
	assert.Equal(t, "websocket", row.Metadata.Transport)
	assert.Equal(t, "What is the capital of France?", row.inputText())
	assert.Equal(t, replyText(done), row.outputText())
	assert.Contains(t, strings.ToLower(row.outputText()), "paris")
	usage := usageOf(done)
	assert.Equal(t, usage.input, row.TokenUsage.Input)
	assert.Equal(t, usage.output, row.TokenUsage.Output)
}

// Nova detects the end of speech itself; the turn's input is its transcript of what was said.
func TestNova_VoiceTurnIsTranscribedAndLogged(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openNovaSession(t)

	s.client.streamAudio("testdata/ask-weather.wav") // "What's the weather like in Paris right now?"
	frames := s.client.untilAll("response.done", "conversation.item.input_audio_transcription.completed")

	assert.Contains(t, strings.ToLower(findType(frames, "conversation.item.input_audio_transcription.completed").Get("transcript").Str), "weather")
	assert.Positive(t, countType(frames, "response.output_audio.delta"), "Nova answers out loud")
	row := s.turnRow()
	assert.Equal(t, "success", row.Status)
	assert.Contains(t, strings.ToLower(row.inputText()), "weather")
	assert.Equal(t, replyText(findType(frames, "response.done")), row.outputText())
}

// Nova fixes the session when the conversation starts, so a later session.update is refused in-band
// and the session goes on.
func TestNova_SessionUpdateAfterStartIsRefused(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openNovaSession(t)
	s.client.send(inputAudioAppend(base64.StdEncoding.EncodeToString(make([]byte, audioChunkLen)))) // starts the conversation
	s.client.streamAudio("")

	s.client.send(sessionUpdate(map[string]any{"type": "realtime", "instructions": "Answer in French."}))
	frames := s.client.collect("error")

	errObj := assertError(t, frames[len(frames)-1], "invalid_request_error", "fixes the session configuration")
	assert.Equal(t, "session_update_after_start", errObj.Get("code").Str)
	done := s.ask("Say hello.")
	require.Equal(t, "completed", done.Get("response.status").Str, "the session goes on: %s", done.Raw)
}
