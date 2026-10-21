package gemini

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A client that keeps extra_content.google.thought_signature, as Google's OpenAI-compatible
// endpoint emits it, must get that signature replayed, not the skip sentinel.

const testExtraContentSignature = "AQJyZWFsLXNpZ25hdHVyZS1ieXRlcwM="

func toolCallHistory(extraContent json.RawMessage) []schemas.ChatMessage {
	callID := "call_1"
	toolName := "get_weather"
	return []schemas.ChatMessage{
		{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: sptr("weather in Paris?")},
		},
		{
			Role: schemas.ChatMessageRoleAssistant,
			ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{{
					ID: &callID,
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      &toolName,
						Arguments: `{"city":"Paris"}`,
					},
					ExtraContent: extraContent,
				}},
			},
		},
		{
			Role:            schemas.ChatMessageRoleTool,
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: &callID},
			Content:         &schemas.ChatMessageContent{ContentStr: sptr("sunny")},
		},
	}
}

func replayedSignature(t testing.TB, extraContent json.RawMessage) string {
	t.Helper()
	contents, _, err := convertBifrostMessagesToGemini(toolCallHistory(extraContent))
	require.NoError(t, err)
	require.Len(t, contents, 3)
	require.Len(t, contents[1].Parts, 1)
	require.NotNil(t, contents[1].Parts[0].FunctionCall)
	return string(contents[1].Parts[0].ThoughtSignature)
}

func TestConvertBifrostMessagesToGemini_ExtraContentThoughtSignatureIsReplayed(t *testing.T) {
	got := replayedSignature(t, json.RawMessage(`{"google":{"thought_signature":"`+testExtraContentSignature+`"}}`))

	assert.Equal(t, "\x01\x02real-signature-bytes\x03", got)
}

func TestConvertBifrostMessagesToGemini_NoSignatureStillGetsTheSentinel(t *testing.T) {
	assert.Equal(t, skipThoughtSignatureValidator, replayedSignature(t, nil))
}

func TestConvertBifrostMessagesToGemini_UnusableExtraContentFallsBackToTheSentinel(t *testing.T) {
	for _, extra := range []string{
		`{"google":{}}`,
		`{"google":{"thought_signature":"not base64!"}}`,
		`{"other":{"thought_signature":"` + testExtraContentSignature + `"}}`,
	} {
		assert.Equal(t, skipThoughtSignatureValidator, replayedSignature(t, json.RawMessage(extra)), extra)
	}
}

func BenchmarkConvertBifrostMessagesToGemini_ExtraContentThoughtSignature(b *testing.B) {
	extra := json.RawMessage(`{"google":{"thought_signature":"` + testExtraContentSignature + `"}}`)
	messages := toolCallHistory(extra)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := convertBifrostMessagesToGemini(messages); err != nil {
			b.Fatal(err)
		}
	}
}
