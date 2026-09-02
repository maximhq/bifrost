package schemas

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reasoning replay on the NATIVE /v1/chat/completions surface.
//
// That surface parses the incoming body straight into []ChatMessage, so the OpenAI-wire
// normalization added for the /openai/* integration (openai.ConvertOpenAIMessagesToBifrostMessages,
// upstream dd927388d / #5888) never runs here. ChatAssistantMessage.UnmarshalJSON was the only
// normalizer in play and it folded reasoning_content alone - a caller replaying
// reasoning_details[].text (OpenRouter's documented spelling, and exactly what a client that
// captured Bifrost's own response emits on the next turn) kept Reasoning nil.
//
// Reasoning nil is not a cosmetic miss. ConvertBifrostMessagesToOpenAIMessages carries
// reasoning to an OpenAI-compatible upstream solely via ChatAssistantMessage.Reasoning, and
// ReasoningDetails is inbound-only there by design, so the replayed chain of thought was
// dropped on the way out and never reached the model. Multi-turn tool loops therefore ran
// with no prior <think> block at all.
//
// Precedence mirrors the integration-surface normalizer: reasoning_content > reasoning >
// reasoning_details text. Structured details are never consumed or reordered by the fold -
// Anthropic/Gemini/Bedrock replay reads ReasoningDetails directly (Text/Signature/Data) and
// must keep seeing exactly what the client sent.

const replayDetailsPayload = `{"role":"assistant","content":"visible content","reasoning_details":[{"index":0,"type":"reasoning.text","text":"preserved hidden reasoning"}],"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"printf 'CASE_002_OK\\\\n'\"}"}}]}`

func TestChatAssistantMessageIngressFoldsReasoningDetails(t *testing.T) {
	t.Run("reasoning_details populates Reasoning alongside tool calls", func(t *testing.T) {
		var msg ChatMessage
		require.NoError(t, json.Unmarshal([]byte(replayDetailsPayload), &msg))

		assistant := msg.ChatAssistantMessage
		require.NotNil(t, assistant, "a reasoning-bearing assistant turn must allocate ChatAssistantMessage")
		require.NotNil(t, assistant.Reasoning, "replayed reasoning_details must fold into Reasoning")
		assert.Equal(t, "preserved hidden reasoning", *assistant.Reasoning)

		require.Len(t, assistant.ReasoningDetails, 1, "the structured detail must survive the fold")
		require.NotNil(t, assistant.ReasoningDetails[0].Text)
		assert.Equal(t, "preserved hidden reasoning", *assistant.ReasoningDetails[0].Text)
		assert.Equal(t, BifrostReasoningDetailsTypeText, assistant.ReasoningDetails[0].Type)

		require.Len(t, assistant.ToolCalls, 1, "tool_calls must be untouched by the fold")
		require.NotNil(t, assistant.ToolCalls[0].ID)
		assert.Equal(t, "call_1", *assistant.ToolCalls[0].ID)
		assert.Equal(t, "bash", *assistant.ToolCalls[0].Function.Name)
	})

	t.Run("bare reasoning_content still wins", func(t *testing.T) {
		var msg ChatMessage
		require.NoError(t, json.Unmarshal(
			[]byte(`{"role":"assistant","reasoning_content":"reasoning A"}`), &msg))
		require.NotNil(t, msg.ChatAssistantMessage)
		require.NotNil(t, msg.ChatAssistantMessage.Reasoning)
		assert.Equal(t, "reasoning A", *msg.ChatAssistantMessage.Reasoning)
	})

	t.Run("bare reasoning alias is accepted", func(t *testing.T) {
		var msg ChatMessage
		require.NoError(t, json.Unmarshal(
			[]byte(`{"role":"assistant","reasoning":"reasoning B"}`), &msg))
		require.NotNil(t, msg.ChatAssistantMessage)
		require.NotNil(t, msg.ChatAssistantMessage.Reasoning)
		assert.Equal(t, "reasoning B", *msg.ChatAssistantMessage.Reasoning)
	})

	// All three spellings at once, deliberately different. reasoning_content is the
	// provider-native field and takes precedence; reasoning is the OpenRouter alias; a
	// details text is the last resort so a reasoning-only replay is still honoured.
	t.Run("precedence is reasoning_content over reasoning over details", func(t *testing.T) {
		var msg ChatMessage
		require.NoError(t, json.Unmarshal([]byte(`{"role":"assistant",
			"reasoning_content":"A",
			"reasoning":"B",
			"reasoning_details":[{"index":0,"type":"reasoning.text","text":"C"}]}`), &msg))
		require.NotNil(t, msg.ChatAssistantMessage)
		require.NotNil(t, msg.ChatAssistantMessage.Reasoning)
		assert.Equal(t, "A", *msg.ChatAssistantMessage.Reasoning,
			"reasoning_content must take precedence")

		require.Len(t, msg.ChatAssistantMessage.ReasoningDetails, 1,
			"the caller's details must be preserved verbatim")
		assert.Equal(t, "C", *msg.ChatAssistantMessage.ReasoningDetails[0].Text)
	})

	// reasoning_content wins over details even without the middle alias, which is the
	// shape a DeepSeek/xAI client replays.
	t.Run("reasoning_content wins over details", func(t *testing.T) {
		var msg ChatMessage
		require.NoError(t, json.Unmarshal([]byte(`{"role":"assistant",
			"reasoning_content":"A",
			"reasoning_details":[{"index":0,"type":"reasoning.text","text":"C"}]}`), &msg))
		require.NotNil(t, msg.ChatAssistantMessage.Reasoning)
		assert.Equal(t, "A", *msg.ChatAssistantMessage.Reasoning)
	})
}

func TestChatAssistantMessageIngressReasoningDetailsEdgeCases(t *testing.T) {
	tests := []struct {
		name            string
		payload         string
		wantReasoning   *string
		wantDetailsLen  int
		allocatesAssist bool
	}{
		{
			name:           "empty details array",
			payload:        `{"role":"assistant","content":"hi","reasoning_details":[]}`,
			wantReasoning:  nil,
			wantDetailsLen: 0,
		},
		{
			name:           "details with no text field at all",
			payload:        `{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.summary"}]}`,
			wantReasoning:  nil,
			wantDetailsLen: 1,
		},
		{
			name:           "details with explicit null text",
			payload:        `{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.text","text":null}]}`,
			wantReasoning:  nil,
			wantDetailsLen: 1,
		},
		{
			name:           "details with empty-string text is not fabricated into reasoning",
			payload:        `{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.text","text":""}]}`,
			wantReasoning:  nil,
			wantDetailsLen: 1,
		},
		{
			// Signature/encrypted-only entries carry no plaintext. They must not be
			// summarised into a bogus Reasoning string; the provider path that can use
			// them reads the detail itself.
			name:           "signature-only detail yields no plaintext",
			payload:        `{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.text","signature":"sig-abc"}]}`,
			wantReasoning:  nil,
			wantDetailsLen: 1,
		},
		{
			name:           "encrypted-only detail yields no plaintext",
			payload:        `{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.encrypted","data":"gAAAA..."}]}`,
			wantReasoning:  nil,
			wantDetailsLen: 1,
		},
		{
			// Only detail Text is a plaintext chain of thought; a summary is a
			// derived digest and must not be passed off as reasoning.
			name:           "summary-only detail yields no plaintext",
			payload:        `{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.summary","summary":"a summary"}]}`,
			wantReasoning:  nil,
			wantDetailsLen: 1,
		},
		{
			// Multiple details: the first text-bearing entry wins, and the whole
			// array is preserved in order for signed replay.
			name:           "multiple details take the first text-bearing entry",
			payload:        `{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.encrypted","data":"blob"},{"index":1,"type":"reasoning.text","text":"first text"},{"index":2,"type":"reasoning.text","text":"second text"}]}`,
			wantReasoning:  strPtr("first text"),
			wantDetailsLen: 3,
		},
		{
			name:            "plain assistant message gains no reasoning fields",
			payload:         `{"role":"assistant","content":"plain answer"}`,
			wantReasoning:   nil,
			wantDetailsLen:  0,
			allocatesAssist: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg ChatMessage
			require.NoError(t, json.Unmarshal([]byte(tt.payload), &msg), "must not panic on malformed reasoning replay")

			if !tt.allocatesAssist && msg.ChatAssistantMessage == nil && tt.wantReasoning == nil {
				// No assistant-only field was populated: nothing to assert beyond
				// "did not invent an assistant block".
				return
			}
			require.NotNil(t, msg.ChatAssistantMessage)

			if tt.wantReasoning == nil {
				assert.Nil(t, msg.ChatAssistantMessage.Reasoning,
					"must not synthesize reasoning text from a detail that carries none")
			} else {
				require.NotNil(t, msg.ChatAssistantMessage.Reasoning)
				assert.Equal(t, *tt.wantReasoning, *msg.ChatAssistantMessage.Reasoning)
			}
			assert.Len(t, msg.ChatAssistantMessage.ReasoningDetails, tt.wantDetailsLen)
		})
	}
}

// A plain assistant message must serialize exactly as before the fold - no empty
// reasoning, reasoning_content or reasoning_details keys.
func TestChatAssistantMessagePlainRoundTripIsUnchanged(t *testing.T) {
	content := "plain answer"
	encoded, err := json.Marshal(ChatMessage{
		Role:    ChatMessageRoleAssistant,
		Content: &ChatMessageContent{ContentStr: &content},
	})
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, "assistant", decoded["role"])
	assert.Equal(t, content, decoded["content"])
	for _, key := range []string{"reasoning", "reasoning_content", "reasoning_details"} {
		_, present := decoded[key]
		assert.False(t, present, "a message with no reasoning must not gain a %q key", key)
	}
}

// The fold must not mutate the caller's own message: the transport shares the parsed
// slice with plugins, so a second marshal of the same value must be byte-identical.
func TestChatAssistantMessageFoldDoesNotMutateCaller(t *testing.T) {
	var msg ChatMessage
	require.NoError(t, json.Unmarshal([]byte(replayDetailsPayload), &msg))

	first, err := json.Marshal(msg)
	require.NoError(t, err)
	second, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second), "repeated marshal must be stable")

	// The fold sets Reasoning from the detail text without rewriting the detail.
	require.Len(t, msg.ChatAssistantMessage.ReasoningDetails, 1)
	assert.Equal(t, "preserved hidden reasoning", *msg.ChatAssistantMessage.ReasoningDetails[0].Text)
}

func strPtr(s string) *string { return &s }
