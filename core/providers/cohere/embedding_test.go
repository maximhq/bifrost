package cohere

import (
	"context"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToCohereEmbeddingRequest(t *testing.T) {
	t.Run("returns nil for missing input", func(t *testing.T) {
		assert.Nil(t, ToCohereEmbeddingRequest(nil))
		assert.Nil(t, ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{}))
		assert.Nil(t, ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: &schemas.EmbeddingInput{},
		}))
	})

	t.Run("single text keeps model in direct cohere body", func(t *testing.T) {
		text := "hello"
		truncate := "END"
		dimensions := 1024
		maxTokens := 256
		bifrostReq := &schemas.BifrostEmbeddingRequest{
			Model: "embed-v4.0",
			Input: &schemas.EmbeddingInput{Text: &text},
			Params: &schemas.EmbeddingParameters{
				Dimensions: &dimensions,
				ExtraParams: map[string]interface{}{
					"input_type":      "classification",
					"embedding_types": []string{"float", "int8"},
					"truncate":        truncate,
					"max_tokens":      maxTokens,
					"priority":        "high",
				},
			},
		}

		req := ToCohereEmbeddingRequest(bifrostReq)
		require.NotNil(t, req)
		assert.Equal(t, "embed-v4.0", req.Model)
		assert.Equal(t, "classification", req.InputType)
		assert.Equal(t, []string{"hello"}, req.Texts)
		assert.Equal(t, []string{"float", "int8"}, req.EmbeddingTypes)
		assert.Equal(t, &dimensions, req.OutputDimension)
		assert.Equal(t, &maxTokens, req.MaxTokens)
		require.NotNil(t, req.Truncate)
		assert.Equal(t, truncate, *req.Truncate)
		assert.Equal(t, map[string]interface{}{"priority": "high"}, req.ExtraParams)
	})

	t.Run("multiple texts use default input type", func(t *testing.T) {
		req := ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: "embed-english-v3.0",
			Input: &schemas.EmbeddingInput{Texts: []string{"hello", "world"}},
		})

		require.NotNil(t, req)
		assert.Equal(t, "embed-english-v3.0", req.Model)
		assert.Equal(t, "search_document", req.InputType)
		assert.Equal(t, []string{"hello", "world"}, req.Texts)
		assert.Nil(t, req.ExtraParams)
	})
}

func TestToCohereEmbeddingRequestBodyIncludesModelForDirectCohere(t *testing.T) {
	text := "hello"
	bifrostReq := &schemas.BifrostEmbeddingRequest{
		Model: "embed-v4.0",
		Input: &schemas.EmbeddingInput{Text: &text},
	}

	wireBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		context.Background(),
		bifrostReq,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToCohereEmbeddingRequest(bifrostReq), nil
		},
		schemas.Cohere,
	)
	require.Nil(t, bifrostErr)
	assert.JSONEq(t, `{
		"model": "embed-v4.0",
		"input_type": "search_document",
		"texts": ["hello"]
	}`, string(wireBody))
}
