package schemas

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OpenRouter treats reasoning_content as a bidirectional alias of reasoning:
// "Reasoning tokens will appear in the `reasoning` field of each message ... You
// can also use `reasoning_content` as an alias - it functions identically to
// `reasoning`" (https://openrouter.ai/docs/use-cases/reasoning-tokens).
//
// Bifrost only did the inbound half: ChatAssistantMessage.UnmarshalJSON folds an
// incoming reasoning_content into Reasoning, but nothing ever emitted the alias
// back out. A client written against DeepSeek or xAI - both of which spell the
// field reasoning_content - therefore read a Bifrost response as having no
// reasoning at all, even though the text was right there under a different key.
func TestReasoningContentAliasIsEmittedOnOutput(t *testing.T) {
	reasoning := "Working through the escape counts night by night."

	t.Run("assistant message emits both spellings", func(t *testing.T) {
		encoded, err := json.Marshal(ChatAssistantMessage{Reasoning: &reasoning})
		require.NoError(t, err)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		assert.Equal(t, reasoning, decoded["reasoning"])
		assert.Equal(t, reasoning, decoded["reasoning_content"], "reasoning_content alias must be emitted")
	})

	t.Run("no reasoning means no alias key", func(t *testing.T) {
		encoded, err := json.Marshal(ChatAssistantMessage{})
		require.NoError(t, err)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		_, hasAlias := decoded["reasoning_content"]
		assert.False(t, hasAlias, "an assistant message without reasoning must not carry an empty alias")
	})

	// The load-bearing case. ChatMessage embeds *ChatAssistantMessage, and Go
	// promotes an embedded type's MarshalJSON to the outer struct: adding one to
	// ChatAssistantMessage without a matching ChatMessage.MarshalJSON makes the
	// whole message serialise as just the assistant fields, silently dropping
	// role, content and name from every chat response. ChatMessage.UnmarshalJSON
	// already exists for exactly this reason on the decode side.
	t.Run("embedding does not swallow the outer message fields", func(t *testing.T) {
		content := "The farmer has 14 sheep."
		name := "assistant-1"
		msg := ChatMessage{
			Name:    &name,
			Role:    ChatMessageRoleAssistant,
			Content: &ChatMessageContent{ContentStr: &content},
			ChatAssistantMessage: &ChatAssistantMessage{
				Reasoning: &reasoning,
			},
		}

		encoded, err := json.Marshal(msg)
		require.NoError(t, err)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		assert.Equal(t, string(ChatMessageRoleAssistant), decoded["role"], "role must survive the embedded marshaller")
		assert.Equal(t, content, decoded["content"], "content must survive the embedded marshaller")
		assert.Equal(t, name, decoded["name"], "name must survive the embedded marshaller")
		assert.Equal(t, reasoning, decoded["reasoning"])
		assert.Equal(t, reasoning, decoded["reasoning_content"])
	})

	t.Run("tool message flattening still works", func(t *testing.T) {
		callID := "call_123"
		content := "42"
		msg := ChatMessage{
			Role:            ChatMessageRoleTool,
			Content:         &ChatMessageContent{ContentStr: &content},
			ChatToolMessage: &ChatToolMessage{ToolCallID: &callID},
		}

		encoded, err := json.Marshal(msg)
		require.NoError(t, err)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		assert.Equal(t, string(ChatMessageRoleTool), decoded["role"])
		assert.Equal(t, callID, decoded["tool_call_id"])
		_, hasAlias := decoded["reasoning_content"]
		assert.False(t, hasAlias)
	})

	// Streaming carries the same asymmetry in a different type:
	// ChatStreamResponseChoiceDelta.UnmarshalJSON also folds reasoning_content in,
	// and also never emitted it back. DeepSeek streams its reasoning under exactly
	// that key, so a DeepSeek-shaped client consuming a Bifrost stream would watch
	// the whole thinking phase go by without seeing a single token of it.
	t.Run("stream delta emits both spellings", func(t *testing.T) {
		encoded, err := json.Marshal(ChatStreamResponseChoiceDelta{Reasoning: &reasoning})
		require.NoError(t, err)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		assert.Equal(t, reasoning, decoded["reasoning"])
		assert.Equal(t, reasoning, decoded["reasoning_content"], "stream deltas must carry the alias too")
	})

	t.Run("stream delta without reasoning has no alias key", func(t *testing.T) {
		content := "hello"
		encoded, err := json.Marshal(ChatStreamResponseChoiceDelta{Content: &content})
		require.NoError(t, err)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		assert.Equal(t, content, decoded["content"], "ordinary content deltas must be untouched")
		_, hasAlias := decoded["reasoning_content"]
		assert.False(t, hasAlias)
	})

	// The alias is emitted from Reasoning rather than stored, so a marshal/unmarshal
	// round trip must not end up with two independent copies that can drift.
	t.Run("round trip keeps a single source of truth", func(t *testing.T) {
		encoded, err := json.Marshal(ChatAssistantMessage{Reasoning: &reasoning})
		require.NoError(t, err)

		var back ChatAssistantMessage
		require.NoError(t, json.Unmarshal(encoded, &back))
		require.NotNil(t, back.Reasoning)
		assert.Equal(t, reasoning, *back.Reasoning)
		require.Len(t, back.ReasoningDetails, 1, "the synthesized reasoning_details entry must survive")
		require.NotNil(t, back.ReasoningDetails[0].Text)
		assert.Equal(t, reasoning, *back.ReasoningDetails[0].Text)
	})
}
