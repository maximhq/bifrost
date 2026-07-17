package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests verify that non-stream (BifrostResponseChoice) and stream
// (ChatStreamResponseChoiceDelta) assistant responses mirror "reasoning" into
// "reasoning_content" on the wire, so OpenAI-compatible clients using the LiteLLM/AI SDK
// dialect (e.g. @ai-sdk/openai-compatible) can consume reasoning output without a
// Bifrost-specific response transform (issue #5325) — and that the alias does NOT leak
// into outbound provider request payloads, which also reuse ChatMessage.

func TestMarshal_BifrostResponseChoice_MirrorsReasoningContent(t *testing.T) {
	reasoning := "internal reasoning summary"
	choice := BifrostResponseChoice{
		Index: 0,
		ChatNonStreamResponseChoice: &ChatNonStreamResponseChoice{
			Message: &ChatMessage{
				Role: ChatMessageRoleAssistant,
				ChatAssistantMessage: &ChatAssistantMessage{
					Reasoning: &reasoning,
				},
			},
		},
	}

	output, err := Marshal(choice)
	require.NoError(t, err)

	var decoded map[string]interface{}
	require.NoError(t, Unmarshal(output, &decoded))

	message, ok := decoded["message"].(map[string]interface{})
	require.True(t, ok, "expected message object in %s", output)
	assert.Equal(t, reasoning, message["reasoning"])
	assert.Equal(t, reasoning, message["reasoning_content"])
}

func TestMarshal_BifrostResponseChoice_NoReasoning_OmitsReasoningContent(t *testing.T) {
	content := "final answer"
	contentPtr := ChatMessageContent{ContentStr: &content}
	choice := BifrostResponseChoice{
		Index: 0,
		ChatNonStreamResponseChoice: &ChatNonStreamResponseChoice{
			Message: &ChatMessage{
				Role:    ChatMessageRoleAssistant,
				Content: &contentPtr,
			},
		},
	}

	output, err := Marshal(choice)
	require.NoError(t, err)

	var decoded map[string]interface{}
	require.NoError(t, Unmarshal(output, &decoded))

	message, ok := decoded["message"].(map[string]interface{})
	require.True(t, ok)
	_, hasReasoningContent := message["reasoning_content"]
	assert.False(t, hasReasoningContent)
}

func TestMarshal_BifrostResponseChoice_Stream_MirrorsReasoningContent(t *testing.T) {
	reasoning := "internal reasoning fragment"
	choice := BifrostResponseChoice{
		Index: 0,
		ChatStreamResponseChoice: &ChatStreamResponseChoice{
			Delta: &ChatStreamResponseChoiceDelta{
				Reasoning: &reasoning,
			},
		},
	}

	output, err := Marshal(choice)
	require.NoError(t, err)

	var decoded map[string]interface{}
	require.NoError(t, Unmarshal(output, &decoded))

	delta, ok := decoded["delta"].(map[string]interface{})
	require.True(t, ok, "expected delta object in %s", output)
	assert.Equal(t, reasoning, delta["reasoning"])
	assert.Equal(t, reasoning, delta["reasoning_content"])
}

// Request-side ChatMessage (used directly, not via BifrostResponseChoice) must NOT gain
// reasoning_content — providers like Perplexity/HuggingFace marshal []ChatMessage
// straight into outbound request bodies and may reject unknown fields.
func TestMarshal_ChatMessage_RequestSide_NoReasoningContentLeak(t *testing.T) {
	reasoning := "internal reasoning summary"
	messages := []ChatMessage{
		{
			Role: ChatMessageRoleAssistant,
			ChatAssistantMessage: &ChatAssistantMessage{
				Reasoning: &reasoning,
			},
		},
	}

	output, err := Marshal(messages)
	require.NoError(t, err)

	var decoded []map[string]interface{}
	require.NoError(t, Unmarshal(output, &decoded))

	require.Len(t, decoded, 1)
	assert.Equal(t, reasoning, decoded[0]["reasoning"])
	_, hasReasoningContent := decoded[0]["reasoning_content"]
	assert.False(t, hasReasoningContent, "reasoning_content must not leak into outbound request messages")
}

// Round-trip: a response we emit (with both reasoning and reasoning_content) must still
// unmarshal cleanly without duplicating reasoning_details.
func TestRoundTrip_BifrostResponseChoice_ReasoningContentAlias(t *testing.T) {
	reasoning := "roundtrip reasoning"
	choice := BifrostResponseChoice{
		Index: 0,
		ChatNonStreamResponseChoice: &ChatNonStreamResponseChoice{
			Message: &ChatMessage{
				Role: ChatMessageRoleAssistant,
				ChatAssistantMessage: &ChatAssistantMessage{
					Reasoning: &reasoning,
				},
			},
		},
	}

	output, err := Marshal(choice)
	require.NoError(t, err)

	var decoded BifrostResponseChoice
	require.NoError(t, Unmarshal(output, &decoded))

	require.NotNil(t, decoded.ChatNonStreamResponseChoice)
	require.NotNil(t, decoded.ChatNonStreamResponseChoice.Message)
	require.NotNil(t, decoded.ChatNonStreamResponseChoice.Message.ChatAssistantMessage)
	require.NotNil(t, decoded.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.Reasoning)
	assert.Equal(t, reasoning, *decoded.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.Reasoning)
	require.Len(t, decoded.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ReasoningDetails, 1)
}
