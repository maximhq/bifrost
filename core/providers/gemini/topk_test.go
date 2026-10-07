package gemini_test

import (
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToGeminiChatCompletionRequest_TopK pins that the typed top_k chat parameter
// reaches generationConfig.topK, and that an ExtraParams top_k still works as the
// fallback for callers that send it there.
func TestToGeminiChatCompletionRequest_TopK(t *testing.T) {
	build := func(params *schemas.ChatParameters) *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: schemas.Gemini,
			Model:    "gemini-2.5-flash",
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")},
			}},
			Params: params,
		}
	}

	t.Run("typed", func(t *testing.T) {
		out, err := gemini.ToGeminiChatCompletionRequest(nil, build(&schemas.ChatParameters{
			TopK:        schemas.Ptr(40),
			ExtraParams: map[string]interface{}{"top_k": 7},
		}))
		require.NoError(t, err)
		require.NotNil(t, out.GenerationConfig.TopK)
		assert.Equal(t, 40, *out.GenerationConfig.TopK, "the typed value wins over the ExtraParams copy")
	})

	t.Run("extra params fallback", func(t *testing.T) {
		out, err := gemini.ToGeminiChatCompletionRequest(nil, build(&schemas.ChatParameters{
			ExtraParams: map[string]interface{}{"top_k": 7},
		}))
		require.NoError(t, err)
		require.NotNil(t, out.GenerationConfig.TopK)
		assert.Equal(t, 7, *out.GenerationConfig.TopK)
	})
}
