package openai

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestReasoningDetails_ParsedFromIncomingRequest reproduces GH #5274: an
// OpenAI-compatible assistant message carrying reasoning_details (OpenRouter-style
// Anthropic thinking-signature replay) must survive unmarshal and reach the
// Bifrost schema, not be silently dropped.
func TestReasoningDetails_ParsedFromIncomingRequest(t *testing.T) {
	raw := []byte(`{
		"model": "anthropic/claude-sonnet-4-5",
		"messages": [
			{"role": "user", "content": "What is the weather in Paris?"},
			{"role": "assistant", "content": "",
			 "tool_calls": [{"id": "toolu_1", "type": "function",
				"function": {"name": "get_weather", "arguments": "{\"city\":\"Paris\"}"}}],
			 "reasoning_details": [{"index": 0, "type": "reasoning.text",
				"text": "thinking about Paris weather", "signature": "Eu8Bsig"}]},
			{"role": "tool", "tool_call_id": "toolu_1", "content": "22C, sunny"}
		]
	}`)

	var req OpenAIChatRequest
	require.NoError(t, sonic.Unmarshal(raw, &req))

	require.Len(t, req.Messages, 3)
	assistantMsg := req.Messages[1]
	require.NotNil(t, assistantMsg.OpenAIChatAssistantMessage)
	require.Len(t, assistantMsg.OpenAIChatAssistantMessage.ReasoningDetails, 1)
	detail := assistantMsg.OpenAIChatAssistantMessage.ReasoningDetails[0]
	require.Equal(t, schemas.BifrostReasoningDetailsTypeText, detail.Type)
	require.NotNil(t, detail.Signature)
	require.Equal(t, "Eu8Bsig", *detail.Signature)

	bifrostMessages := ConvertOpenAIMessagesToBifrostMessages(req.Messages)
	require.Len(t, bifrostMessages, 3)
	require.NotNil(t, bifrostMessages[1].ChatAssistantMessage)
	require.Len(t, bifrostMessages[1].ChatAssistantMessage.ReasoningDetails, 1)
	require.Equal(t, "Eu8Bsig", *bifrostMessages[1].ChatAssistantMessage.ReasoningDetails[0].Signature)
}

// TestReasoningDetails_SynthesizedFromPlainReasoningContent covers plain-text
// reasoning replay (no structured reasoning_details, just reasoning_content) —
// it should still produce a reasoning.text detail so it isn't lost either.
func TestReasoningDetails_SynthesizedFromPlainReasoningContent(t *testing.T) {
	text := "plain reasoning text"
	messages := []OpenAIMessage{
		{
			Role: schemas.ChatMessageRoleAssistant,
			OpenAIChatAssistantMessage: &OpenAIChatAssistantMessage{
				Reasoning: &text,
			},
		},
	}

	bifrostMessages := ConvertOpenAIMessagesToBifrostMessages(messages)
	require.Len(t, bifrostMessages, 1)
	require.NotNil(t, bifrostMessages[0].ChatAssistantMessage)
	require.Len(t, bifrostMessages[0].ChatAssistantMessage.ReasoningDetails, 1)
	detail := bifrostMessages[0].ChatAssistantMessage.ReasoningDetails[0]
	require.Equal(t, schemas.BifrostReasoningDetailsTypeText, detail.Type)
	require.Equal(t, text, *detail.Text)
	// Synthesized details carry no signature — downstream Anthropic conversion
	// must not turn these into replayed thinking blocks.
	require.Nil(t, detail.Signature)
}

// TestReasoningDetails_RoundTripsBackToOpenAIMessages covers the response
// direction: ReasoningDetails set on a Bifrost message must be copied back
// onto the OpenAI wire message (needed by OpenAI-compatible upstreams like
// OpenRouter that accept reasoning replay).
func TestReasoningDetails_RoundTripsBackToOpenAIMessages(t *testing.T) {
	signature := "sig123"
	text := "some thinking"
	messages := []schemas.ChatMessage{
		{
			Role: schemas.ChatMessageRoleAssistant,
			ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ReasoningDetails: []schemas.ChatReasoningDetails{
					{Index: 0, Type: schemas.BifrostReasoningDetailsTypeText, Text: &text, Signature: &signature},
				},
			},
		},
	}

	openaiMessages := ConvertBifrostMessagesToOpenAIMessages(messages)
	require.Len(t, openaiMessages, 1)
	require.NotNil(t, openaiMessages[0].OpenAIChatAssistantMessage)
	require.Len(t, openaiMessages[0].OpenAIChatAssistantMessage.ReasoningDetails, 1)
	require.Equal(t, signature, *openaiMessages[0].OpenAIChatAssistantMessage.ReasoningDetails[0].Signature)
}
