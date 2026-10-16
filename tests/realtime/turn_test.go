package realtime

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Turns: what a healthy session relays and logs. A turn starts when the client asks for a
// response (or the provider commits the user's audio) and ends at response.done; each one is a
// realtime.turn row.

func TestTurn_TextReplyIsRelayedAndLogged(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	assert.Equal(t, "Bearer "+upstreamKey, s.upstream.Authorization, "the gateway dials with its provider key, never the client's")

	s.client.send(userMessage("What is the capital of France?"), responseCreate())
	s.upstream.expect("conversation.item.create", "response.create")

	reply := textReply("resp_1", "Paris.", tokens{input: 12, output: 3})
	s.upstream.send(reply...)
	s.client.expectRelayed(reply...)

	row := s.turnRow()
	assert.Equal(t, "success", row.Status)
	assert.Equal(t, "openai", row.Provider)
	assert.Equal(t, s.model, row.Model)
	assert.Equal(t, "fake-openai", row.SelectedKeyName)
	assert.Equal(t, suiteKey.ID, row.VirtualKeyID)
	assert.Equal(t, s.upstream.ID, row.Metadata.ProviderSessionID)
	assert.Equal(t, "websocket", row.Metadata.Transport)
	assert.Equal(t, "What is the capital of France?", row.inputText())
	assert.Equal(t, "Paris.", row.outputText())
	assert.Equal(t, 12, row.TokenUsage.Input)
	assert.Equal(t, 3, row.TokenUsage.Output)
	assert.InDelta(t, costOf(tokens{input: 12, output: 3}), row.Cost, 1e-12)
}

// With server VAD the provider decides when the user has finished speaking: the turn starts from
// the upstream side, and the transcript of what was said is the turn's input.
func TestTurn_ServerVADTurnStartsUpstream(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)

	speech := []event{inputAudioCommitted("item_user_1"), transcriptionCompleted("item_user_1", "Turn on the lights.")}
	reply := textReply("resp_1", "Done.", tokens{input: 20, output: 2})
	s.upstream.send(speech...)
	s.upstream.send(reply...)
	s.client.expectRelayed(speech...)
	s.client.expectRelayed(reply...)

	row := s.turnRow()
	assert.Equal(t, "success", row.Status)
	assert.Equal(t, "Turn on the lights.", row.inputText())
	assert.Equal(t, "Done.", row.outputText())
}

// A function call ends one turn; the app answers it and asks for the next response, which is a
// turn of its own whose input is the function's output.
func TestTurn_ToolCallRoundTrip(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)

	s.client.send(userMessage("Weather in Paris?"), responseCreate())
	s.upstream.expect("conversation.item.create", "response.create")
	call := toolCallReply("resp_1", "call_1", "get_weather", `{"city":"Paris"}`, tokens{input: 30, output: 8})
	s.upstream.send(call...)
	s.client.expectRelayed(call...)

	s.client.send(functionCallOutput("call_1", `{"conditions":"sunny"}`), responseCreate())
	s.upstream.expect("conversation.item.create", "response.create")
	reply := textReply("resp_2", "It is sunny in Paris.", tokens{input: 45, output: 6})
	s.upstream.send(reply...)
	s.client.expectRelayed(reply...)

	rows := s.turnRows(2)
	assert.Equal(t, []string{"get_weather"}, rows[0].toolCalls())
	assert.Contains(t, rows[1].inputText(), `{"conditions":"sunny"}`)
	assert.Equal(t, "It is sunny in Paris.", rows[1].outputText())
	assert.InDelta(t, costOf(tokens{input: 30, output: 8}), rows[0].Cost, 1e-12)
	assert.InDelta(t, costOf(tokens{input: 45, output: 6}), rows[1].Cost, 1e-12)
}

// OpenAI reports usage per response, so consecutive turns are billed independently.
func TestTurn_EachResponseIsItsOwnRow(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)

	for i, usage := range []tokens{{input: 10, output: 1}, {input: 20, output: 2}, {input: 30, output: 3}} {
		s.client.send(responseCreate())
		s.upstream.expect("response.create")
		reply := textReply(fmt.Sprintf("resp_%d", i+1), "ok", usage)
		s.upstream.send(reply...)
		s.client.expectRelayed(reply...)
	}

	rows := s.turnRows(3)
	for i, want := range []int{11, 22, 33} {
		assert.Equal(t, want, rows[i].TokenUsage.Total, "turn %d", i+1)
	}
}
