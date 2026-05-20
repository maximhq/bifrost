package bedrock

import (
	"context"
	"encoding/json"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToBedrockCohereEmbeddingRequest(t *testing.T) {
	t.Run("returns error for nil request", func(t *testing.T) {
		req, err := ToBedrockCohereEmbeddingRequest(nil)
		require.Error(t, err)
		assert.Nil(t, req)
		assert.Contains(t, err.Error(), "nil")
	})

	t.Run("returns error for missing input", func(t *testing.T) {
		req, err := ToBedrockCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{})
		require.Error(t, err)
		assert.Nil(t, req)
		assert.Contains(t, err.Error(), "no input")
	})

	t.Run("returns error for non-nil but empty input", func(t *testing.T) {
		req, err := ToBedrockCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: &schemas.EmbeddingInput{},
		})
		require.Error(t, err)
		assert.Nil(t, req)
		assert.Contains(t, err.Error(), "no input")
	})

	t.Run("single text strips model and extracts typed params", func(t *testing.T) {
		text := "hello"
		truncate := "RIGHT"
		dimensions := 512
		bifrostReq := &schemas.BifrostEmbeddingRequest{
			Model: "cohere.embed-english-v3",
			Input: &schemas.EmbeddingInput{Text: &text},
			Params: &schemas.EmbeddingParameters{
				Dimensions: &dimensions,
				ExtraParams: map[string]interface{}{
					"input_type":      "search_query",
					"embedding_types": []string{"float"},
					"truncate":        truncate,
					"max_tokens":      float64(128),
					"trace_id":        "req-123",
				},
			},
		}

		req, err := ToBedrockCohereEmbeddingRequest(bifrostReq)
		require.NoError(t, err)
		require.NotNil(t, req)
		assert.Equal(t, "search_query", req.InputType)
		assert.Equal(t, []string{"hello"}, req.Texts)
		assert.Equal(t, []string{"float"}, req.EmbeddingTypes)
		assert.Equal(t, &dimensions, req.OutputDimension)
		assert.Equal(t, 128, *req.MaxTokens)
		require.NotNil(t, req.Truncate)
		assert.Equal(t, truncate, *req.Truncate)
		assert.Equal(t, map[string]interface{}{"trace_id": "req-123"}, req.ExtraParams)
	})

	t.Run("multiple texts preserve bedrock body shape", func(t *testing.T) {
		bifrostReq := &schemas.BifrostEmbeddingRequest{
			Model: "cohere.embed-multilingual-v3",
			Input: &schemas.EmbeddingInput{Texts: []string{"hello", "world"}},
			Params: &schemas.EmbeddingParameters{
				ExtraParams: map[string]interface{}{
					"input_type": "search_document",
				},
			},
		}

		req, err := ToBedrockCohereEmbeddingRequest(bifrostReq)
		require.NoError(t, err)
		assert.Equal(t, []string{"hello", "world"}, req.Texts)
		assert.Equal(t, "search_document", req.InputType)
	})
}

func TestToBedrockCohereEmbeddingRequestBodyOmitsModel(t *testing.T) {
	text := "hello"
	bifrostReq := &schemas.BifrostEmbeddingRequest{
		Model: "cohere.embed-english-v3",
		Input: &schemas.EmbeddingInput{Text: &text},
		Params: &schemas.EmbeddingParameters{
			ExtraParams: map[string]interface{}{
				"input_type":      "search_document",
				"embedding_types": []string{"float"},
			},
		},
	}

	wireBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		context.Background(),
		bifrostReq,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToBedrockCohereEmbeddingRequest(bifrostReq)
		},
	)
	require.Nil(t, bifrostErr)
	assert.NotContains(t, string(wireBody), `"model"`)
	assert.JSONEq(t, `{
		"input_type": "search_document",
		"texts": ["hello"],
		"embedding_types": ["float"]
	}`, string(wireBody))
}

func TestDetermineEmbeddingModelType(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantType  string
		wantError bool
	}{
		{
			name:     "titan text model",
			model:    "amazon.titan-embed-text-v1",
			wantType: "titan",
		},
		{
			name:     "titan image model",
			model:    "amazon.titan-embed-image-v1",
			wantType: "titan",
		},
		{
			name:     "cohere embed model",
			model:    "cohere.embed-english-v3",
			wantType: "cohere",
		},
		{
			name:      "unsupported model",
			model:     "unknown.embedding-model-v1",
			wantError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			modelType, err := DetermineEmbeddingModelType(tc.model)
			if tc.wantError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.wantType, modelType)
		})
	}
}

func TestToBedrockTitanEmbeddingRequest_ImageOnly(t *testing.T) {
	inputImage := "iVBORw0KGgoAAAANSUhEUgAA"

	req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Input: &schemas.EmbeddingInput{},
		Params: &schemas.EmbeddingParameters{
			ExtraParams: map[string]interface{}{
				"inputImage": inputImage,
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, req)
	assert.Equal(t, "", req.InputText)
	assert.Equal(t, inputImage, req.InputImage)
	assert.NotContains(t, req.ExtraParams, "inputImage")

	// Confirm the wire format includes "inputImage" and does not include "inputText"
	wireBytes, marshalErr := json.Marshal(req)
	require.NoError(t, marshalErr)
	assert.NotContains(t, string(wireBytes), `"inputText"`)
	assert.Contains(t, string(wireBytes), `"inputImage"`)
}

func TestToBedrockTitanEmbeddingRequest_RejectsNoInput(t *testing.T) {
	req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Input: &schemas.EmbeddingInput{},
	})

	require.Error(t, err)
	assert.Nil(t, req)
	assert.Contains(t, err.Error(), "no input text or image provided")
}

func TestToBedrockTitanEmbeddingRequest_TextAndImage(t *testing.T) {
	inputText := "multimodal query"
	inputImage := "iVBORw0KGgoAAAANSUhEUgAA"

	req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Input: &schemas.EmbeddingInput{
			Text: &inputText,
		},
		Params: &schemas.EmbeddingParameters{
			ExtraParams: map[string]interface{}{
				"inputImage": inputImage,
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, req)
	assert.Equal(t, inputText, req.InputText)
	assert.Equal(t, inputImage, req.InputImage)
	assert.NotContains(t, req.ExtraParams, "inputImage")

	// Both text and image should appear in the wire format
	wireBytes, marshalErr := json.Marshal(req)
	require.NoError(t, marshalErr)
	assert.Contains(t, string(wireBytes), `"inputText"`)
	assert.Contains(t, string(wireBytes), `"inputImage"`)
}

func TestToBedrockTitanEmbeddingRequestLiftsInputImageAndNormalize(t *testing.T) {
	inputText := "multimodal embedding"
	inputImage := "iVBORw0KGgoAAAANSUhEUgAA"
	normalize := true

	req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Input: &schemas.EmbeddingInput{
			Text: &inputText,
		},
		Params: &schemas.EmbeddingParameters{
			ExtraParams: map[string]interface{}{
				"inputImage": inputImage,
				"normalize":  normalize,
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, req)
	assert.Equal(t, inputText, req.InputText)
	assert.Equal(t, inputImage, req.InputImage)
	require.NotNil(t, req.Normalize)
	assert.True(t, *req.Normalize)
	assert.NotContains(t, req.ExtraParams, "inputImage")
	assert.NotContains(t, req.ExtraParams, "normalize")
}

func TestToBedrockTitanEmbeddingRequest_NonStringInputImage(t *testing.T) {
	inputText := "text with non-string image"

	req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Input: &schemas.EmbeddingInput{
			Text: &inputText,
		},
		Params: &schemas.EmbeddingParameters{
			ExtraParams: map[string]interface{}{
				"inputImage": 42, // non-string: should not be lifted
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, req)
	assert.Equal(t, "", req.InputImage)
	require.NotNil(t, req.ExtraParams)
	assert.Equal(t, 42, req.ExtraParams["inputImage"])
}

func TestToBedrockTitanEmbeddingRequest_NilInputWithImage(t *testing.T) {
	inputImage := "iVBORw0KGgoAAAANSUhEUgAA"

	req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Input: nil, // explicitly no Input struct
		Params: &schemas.EmbeddingParameters{
			ExtraParams: map[string]interface{}{
				"inputImage": inputImage,
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, req)
	assert.Equal(t, "", req.InputText)
	assert.Equal(t, inputImage, req.InputImage)

	// Wire format must contain inputImage and must NOT contain inputText
	wireBytes, marshalErr := json.Marshal(req)
	require.NoError(t, marshalErr)
	assert.Contains(t, string(wireBytes), `"inputImage"`)
	assert.NotContains(t, string(wireBytes), `"inputText"`)
}

func TestToBedrockTitanEmbeddingRequest_NilInputWithoutImage(t *testing.T) {
	req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Input:  nil,
		Params: nil,
	})

	require.Error(t, err)
	assert.Nil(t, req)
	assert.Contains(t, err.Error(), "no input text or image provided")
}
