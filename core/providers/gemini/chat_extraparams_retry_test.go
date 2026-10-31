package gemini_test

import (
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Chat Completions parity: the conversion runs once per retry/fallback attempt on
// the same Bifrost request and removes safety_settings, cached_content and labels
// from the outbound ExtraParams. It used to alias the source map, so the second
// attempt was sent without any of them.
func TestToGeminiChatCompletionRequest_ExtraParamsSurviveRetries(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Vertex,
		Model:    "gemini-2.5-flash",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Hello"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			ExtraParams: map[string]interface{}{
				"safety_settings": []interface{}{
					map[string]interface{}{
						"category":  "HARM_CATEGORY_HARASSMENT",
						"threshold": "BLOCK_NONE",
					},
				},
				"cached_content":     "cachedContents/abc123",
				"labels":             map[string]interface{}{"team": "platform"},
				"custom_passthrough": "keep-me",
			},
		},
	}

	for attempt := 1; attempt <= 3; attempt++ {
		geminiReq, err := gemini.ToGeminiChatCompletionRequest(nil, bifrostReq)
		require.NoError(t, err, "attempt %d", attempt)
		require.NotNil(t, geminiReq, "attempt %d", attempt)

		require.Len(t, geminiReq.SafetySettings, 1, "attempt %d", attempt)
		assert.Equal(t, "HARM_CATEGORY_HARASSMENT", geminiReq.SafetySettings[0].Category, "attempt %d", attempt)
		assert.Equal(t, "BLOCK_NONE", geminiReq.SafetySettings[0].Threshold, "attempt %d", attempt)
		assert.Equal(t, "cachedContents/abc123", geminiReq.CachedContent, "attempt %d", attempt)
		assert.Equal(t, map[string]string{"team": "platform"}, geminiReq.Labels, "attempt %d", attempt)
		assert.Equal(t, map[string]interface{}{"custom_passthrough": "keep-me"}, geminiReq.GetExtraParams(), "attempt %d", attempt)
	}

	// The Bifrost request itself must be left intact for the next attempt.
	assert.Contains(t, bifrostReq.Params.ExtraParams, "safety_settings")
	assert.Contains(t, bifrostReq.Params.ExtraParams, "cached_content")
	assert.Contains(t, bifrostReq.Params.ExtraParams, "labels")
	assert.Len(t, bifrostReq.Params.ExtraParams, 4)
}
