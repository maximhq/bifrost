package realtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Turns: what a healthy session relays and logs. A turn starts when the client asks for a
// response (or the provider commits the user's audio) and ends at response.done; each one is a
// realtime.turn row that matches what the client was told.

func TestTurn_TextReplyIsRelayedAndLogged(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	s.configure(textSession())

	done := s.ask("What is the capital of France?")

	require.Equal(t, "completed", done.Get("response.status").Str, "response.done: %s", done.Raw)
	row := s.turnRow()
	assert.Equal(t, "success", row.Status)
	assert.Equal(t, "openai", row.Provider)
	assert.Equal(t, openaiModel, row.Model)
	assert.Equal(t, "openai", row.SelectedKeyName)
	assert.Equal(t, s.client.vk.ID, row.VirtualKeyID)
	assert.Equal(t, s.created.Get("session.id").Str, row.Metadata.ProviderSessionID)
	assert.Equal(t, "websocket", row.Metadata.Transport)
	assert.Equal(t, "What is the capital of France?", row.inputText())
	assert.Equal(t, replyText(done), row.outputText())
	assert.Contains(t, strings.ToLower(row.outputText()), "paris")
	usage := usageOf(done)
	assert.Equal(t, usage.input, row.TokenUsage.Input)
	assert.Equal(t, usage.output, row.TokenUsage.Output)
	assert.Positive(t, row.Cost, "the turn is priced from the model catalog")
}

// With server VAD the provider decides when the user has finished speaking: the turn starts from
// the upstream side, and the transcript of what was said is the turn's input.
func TestTurn_ServerVADTurnStartsUpstream(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	config := textSession()
	config["audio"] = map[string]any{"input": map[string]any{
		"transcription":  map[string]any{"model": transcribeModel},
		"turn_detection": map[string]any{"type": "server_vad"},
	}}
	s.configure(config)

	s.client.streamAudio("testdata/ask-weather.wav") // "What's the weather like in Paris right now?"
	frames := s.client.untilAll("response.done", "conversation.item.input_audio_transcription.completed")

	assert.Equal(t, 1, countType(frames, "input_audio_buffer.committed"))
	done := findType(frames, "response.done")
	row := s.turnRow()
	assert.Equal(t, "success", row.Status)
	assert.Contains(t, strings.ToLower(findType(frames, "conversation.item.input_audio_transcription.completed").Get("transcript").Str), "weather")
	assert.Contains(t, strings.ToLower(row.inputText()), "weather")
	assert.Equal(t, replyText(done), row.outputText())
}

// A function call ends one turn; the app answers it and asks for the next response, which is a
// turn of its own whose input is the function's output.
func TestTurn_ToolCallRoundTrip(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	config := textSession()
	config["tools"] = []any{weatherTool}
	s.configure(config)

	s.client.send(userMessage("What is the weather in Paris?"), responseCreateWith(map[string]any{"tool_choice": "required"}))
	call := functionCall(s.responseDone())
	require.Equal(t, "get_weather", call.Get("name").Str, "function call: %s", call.Raw)

	// The answer must be text: with the tool still in the session the model could call it again.
	s.client.send(functionCallOutput(call.Get("call_id").Str, `{"conditions":"sunny"}`), responseCreateWith(map[string]any{"tool_choice": "none"}))
	done := s.responseDone()

	rows := s.turnRows(2)
	assert.Equal(t, []string{"get_weather"}, rows[0].toolCalls())
	assert.Contains(t, rows[1].inputText(), `{"conditions":"sunny"}`)
	assert.Equal(t, replyText(done), rows[1].outputText())
	assert.Contains(t, strings.ToLower(rows[1].outputText()), "sunny")
}

// OpenAI reports usage per response, so consecutive turns are billed independently.
func TestTurn_EachResponseIsItsOwnRow(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	s.configure(textSession())

	var usages []tokens
	for _, question := range []string{"Say one.", "Say two.", "Say three."} {
		usages = append(usages, usageOf(s.ask(question)))
	}

	rows := s.turnRows(3)
	for i, usage := range usages {
		assert.Equal(t, usage.input, rows[i].TokenUsage.Input, "turn %d", i+1)
		assert.Equal(t, usage.output, rows[i].TokenUsage.Output, "turn %d", i+1)
	}
	assert.Less(t, rows[0].TokenUsage.Input, rows[2].TokenUsage.Input, "later turns carry the conversation so far")
}
