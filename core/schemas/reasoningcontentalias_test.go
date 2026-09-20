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

// ModelScope (and other DeepSeek-shaped upstreams) keep sending reasoning_content
// as an empty string on every content-phase chunk once thinking has finished.
// Bifrost amplified that noise instead of dropping it: UnmarshalJSON folded the
// empty alias into a non-nil Reasoning, synthesized a reasoning_details entry with
// empty text from it, and MarshalJSON then re-emitted all of it under both
// spellings. Reasoning-aware clients read each of those empty fragments as a fresh
// thinking block, rendering "[Thinking 0.0s]" between every piece of the answer.
// See https://github.com/maximhq/bifrost/issues/7294.
func TestEmptyReasoningNoiseIsNotAmplified(t *testing.T) {
	assertNoReasoningKeys := func(t *testing.T, encoded []byte) {
		t.Helper()
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		for _, key := range []string{"reasoning", "reasoning_content", "reasoning_details"} {
			_, present := decoded[key]
			assert.False(t, present, "empty reasoning must not serialize a %q key", key)
		}
	}

	t.Run("stream delta drops empty reasoning fields", func(t *testing.T) {
		// Verbatim content-phase delta shape from issue #7294.
		raw := `{"content":"Hello, I'","reasoning":"","reasoning_content":"","reasoning_details":[{"index":0,"type":"reasoning.text","text":""}]}`

		var delta ChatStreamResponseChoiceDelta
		require.NoError(t, json.Unmarshal([]byte(raw), &delta))

		encoded, err := json.Marshal(delta)
		require.NoError(t, err)
		assertNoReasoningKeys(t, encoded)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		assert.Equal(t, "Hello, I'", decoded["content"], "content must survive the cleanup")
	})

	t.Run("stream delta does not synthesize details from an empty alias", func(t *testing.T) {
		raw := `{"content":"","reasoning_content":""}`

		var delta ChatStreamResponseChoiceDelta
		require.NoError(t, json.Unmarshal([]byte(raw), &delta))

		encoded, err := json.Marshal(delta)
		require.NoError(t, err)
		assertNoReasoningKeys(t, encoded)
	})

	t.Run("assistant message drops empty reasoning fields", func(t *testing.T) {
		raw := `{"content":"","reasoning":"","reasoning_content":"","reasoning_details":[{"index":0,"type":"reasoning.text","text":""}]}`

		var msg ChatAssistantMessage
		require.NoError(t, json.Unmarshal([]byte(raw), &msg))

		encoded, err := json.Marshal(msg)
		require.NoError(t, err)
		assertNoReasoningKeys(t, encoded)
	})

	t.Run("payload-bearing details survive an empty reasoning string", func(t *testing.T) {
		// A detail carrying a signature (or summary/data) is not noise even when
		// its text is empty - only the empty reasoning string itself is dropped.
		raw := `{"reasoning":"","reasoning_details":[{"index":0,"type":"reasoning.text","text":"","signature":"sig-abc"}]}`

		var delta ChatStreamResponseChoiceDelta
		require.NoError(t, json.Unmarshal([]byte(raw), &delta))

		encoded, err := json.Marshal(delta)
		require.NoError(t, err)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		_, hasReasoning := decoded["reasoning"]
		assert.False(t, hasReasoning, "the empty reasoning string is still dropped")
		details, hasDetails := decoded["reasoning_details"].([]any)
		require.True(t, hasDetails, "signed details must survive")
		require.Len(t, details, 1)
		assert.Equal(t, "sig-abc", details[0].(map[string]any)["signature"])
	})

	t.Run("real reasoning deltas are untouched", func(t *testing.T) {
		raw := `{"reasoning_content":"We"}`

		var delta ChatStreamResponseChoiceDelta
		require.NoError(t, json.Unmarshal([]byte(raw), &delta))

		encoded, err := json.Marshal(delta)
		require.NoError(t, err)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		assert.Equal(t, "We", decoded["reasoning"])
		assert.Equal(t, "We", decoded["reasoning_content"])
	})
}

