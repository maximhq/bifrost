package gemini_test

import (
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToGeminiBatchGenerateContentRequest locks in the inline batch body conversion shared
// by the Gemini and Vertex batch paths: OpenAI-style "messages" bodies become Gemini
// "contents"/"systemInstruction", and already-native bodies pass through unchanged.
func TestToGeminiBatchGenerateContentRequest(t *testing.T) {
	t.Run("ConvertsOpenAIMessagesToContents", func(t *testing.T) {
		body := map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"role": "system", "content": "You are helpful."},
				map[string]interface{}{"role": "user", "content": "Hello"},
			},
		}

		req, err := gemini.ToGeminiBatchGenerateContentRequest(body)
		require.NoError(t, err)

		require.NotNil(t, req.SystemInstruction)
		require.NotEmpty(t, req.SystemInstruction.Parts)
		assert.Equal(t, "You are helpful.", req.SystemInstruction.Parts[0].Text)

		require.Len(t, req.Contents, 1)
		assert.Equal(t, "user", req.Contents[0].Role)
		require.NotEmpty(t, req.Contents[0].Parts)
		assert.Equal(t, "Hello", req.Contents[0].Parts[0].Text)
	})

	t.Run("PreservesNestedGenerationConfig", func(t *testing.T) {
		body := map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": "Hi"},
			},
			"generationConfig": map[string]interface{}{
				"temperature":     0.5,
				"maxOutputTokens": 256,
			},
		}

		req, err := gemini.ToGeminiBatchGenerateContentRequest(body)
		require.NoError(t, err)
		require.NotNil(t, req.GenerationConfig)
		require.NotNil(t, req.GenerationConfig.Temperature)
		assert.InDelta(t, 0.5, *req.GenerationConfig.Temperature, 1e-9)
		assert.Equal(t, int32(256), req.GenerationConfig.MaxOutputTokens)
	})

	t.Run("PassesThroughNativeGeminiBody", func(t *testing.T) {
		// Body already in Gemini shape (no "messages") is unmarshaled directly.
		body := map[string]interface{}{
			"contents": []interface{}{
				map[string]interface{}{
					"role":  "user",
					"parts": []interface{}{map[string]interface{}{"text": "List objects."}},
				},
			},
			"generationConfig": map[string]interface{}{"temperature": 0.2},
		}

		req, err := gemini.ToGeminiBatchGenerateContentRequest(body)
		require.NoError(t, err)
		assert.Nil(t, req.SystemInstruction)
		require.Len(t, req.Contents, 1)
		assert.Equal(t, "user", req.Contents[0].Role)
		require.NotEmpty(t, req.Contents[0].Parts)
		assert.Equal(t, "List objects.", req.Contents[0].Parts[0].Text)
		require.NotNil(t, req.GenerationConfig)
		require.NotNil(t, req.GenerationConfig.Temperature)
		assert.InDelta(t, 0.2, *req.GenerationConfig.Temperature, 1e-9)
	})

	t.Run("EmptyBodyProducesEmptyRequest", func(t *testing.T) {
		req, err := gemini.ToGeminiBatchGenerateContentRequest(map[string]interface{}{})
		require.NoError(t, err)
		assert.Empty(t, req.Contents)
		assert.Nil(t, req.SystemInstruction)
	})

	t.Run("PreservesToolsToolConfigCachedContentAndLabels", func(t *testing.T) {
		body := map[string]interface{}{
			"contents": []interface{}{
				map[string]interface{}{"role": "user", "parts": []interface{}{map[string]interface{}{"text": "hi"}}},
			},
			"tools": []interface{}{
				map[string]interface{}{"functionDeclarations": []interface{}{map[string]interface{}{"name": "get_weather"}}},
			},
			"toolConfig": map[string]interface{}{
				"functionCallingConfig": map[string]interface{}{"mode": "AUTO"},
			},
			"cachedContent": "cachedContents/abc",
			"labels":        map[string]interface{}{"team": "research"},
		}

		req, err := gemini.ToGeminiBatchGenerateContentRequest(body)
		require.NoError(t, err)

		require.Len(t, req.Tools, 1)
		require.Len(t, req.Tools[0].FunctionDeclarations, 1)
		assert.Equal(t, "get_weather", req.Tools[0].FunctionDeclarations[0].Name)
		require.NotNil(t, req.ToolConfig)
		require.NotNil(t, req.ToolConfig.FunctionCallingConfig)
		assert.Equal(t, "cachedContents/abc", req.CachedContent)
		assert.Equal(t, "research", req.Labels["team"])
	})
}
