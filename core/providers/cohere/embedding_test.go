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
	t.Run("returns error for missing input", func(t *testing.T) {
		_, err := ToCohereEmbeddingRequest(nil)
		assert.Error(t, err)
		_, err = ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{})
		assert.Error(t, err)
		_, err = ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: nil,
		})
		assert.Error(t, err)
	})

	t.Run("single text content extracts typed params", func(t *testing.T) {
		text := "hello"
		truncate := "END"
		dimensions := 1024
		maxTokens := 256
		bifrostReq := &schemas.BifrostEmbeddingRequest{
			Model: "embed-v4.0",
			Input: []schemas.EmbeddingInputItem{
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}},
			},
			Params: &schemas.EmbeddingParameters{
				Dimensions: &dimensions,
				ExtraParams: map[string]interface{}{
					"input_type":      "classification",
					"embedding_types": []string{"float", "int8"},
					"priority":        "high",
					"max_tokens":      maxTokens,
					"truncate":        truncate,
				},
			},
		}

		req, err := ToCohereEmbeddingRequest(bifrostReq)
		require.NoError(t, err)
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

	t.Run("multiple text contents batch into texts array with default input type", func(t *testing.T) {
		hello := "hello"
		world := "world"
		req, err := ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: "embed-english-v3.0",
			Input: []schemas.EmbeddingInputItem{
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &hello}}},
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &world}}},
			},
		})

		require.NoError(t, err)
		require.NotNil(t, req)
		assert.Equal(t, "embed-english-v3.0", req.Model)
		assert.Equal(t, "search_document", req.InputType)
		assert.Equal(t, []string{"hello", "world"}, req.Texts)
		assert.Nil(t, req.ExtraParams)
	})

	t.Run("multimodal content uses inputs array", func(t *testing.T) {
		text := "describe this"
		imageURL := "data:image/jpeg;base64,abc123"
		req, err := ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: "embed-v4.0",
			Input: []schemas.EmbeddingInputItem{
				{Content: schemas.EmbeddingContent{
					{Type: schemas.EmbeddingContentPartTypeText, Text: &text},
					{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: &imageURL}},
				}},
			},
		})

		require.NoError(t, err)
		require.NotNil(t, req)
		require.Len(t, req.Inputs, 1)
		require.Len(t, req.Inputs[0].Content, 2)
		assert.Equal(t, CohereContentBlockTypeText, req.Inputs[0].Content[0].Type)
		assert.Equal(t, CohereContentBlockTypeImage, req.Inputs[0].Content[1].Type)
	})
}

func TestToCohereEmbeddingRequestBodyIncludesModelForDirectCohere(t *testing.T) {
	text := "hello"
	bifrostReq := &schemas.BifrostEmbeddingRequest{
		Model: "embed-v4.0",
		Input: []schemas.EmbeddingInputItem{
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}},
		},
	}

	wireBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		context.Background(),
		bifrostReq,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToCohereEmbeddingRequest(bifrostReq)
		},
	)
	require.Nil(t, bifrostErr)
	assert.JSONEq(t, `{
		"model": "embed-v4.0",
		"input_type": "search_document",
		"texts": ["hello"]
	}`, string(wireBody))
}

func TestCohereEmbeddingResponseLabelsEachEncoding(t *testing.T) {
	base64Vec := "AAEC"
	resp := &CohereEmbeddingResponse{
		Embeddings: &CohereEmbeddingData{
			Float:   [][]float64{{0.1, 0.2}},
			Int8:    [][]int8{{1, -1}},
			Uint8:   [][]int32{{0, 255}},
			Binary:  [][]int8{{1, 0}},
			Ubinary: [][]int32{{7, 8}},
			Base64:  []string{base64Vec},
		},
	}

	bifrostResp := resp.ToBifrostEmbeddingResponse()
	require.Len(t, bifrostResp.Data, 6)

	byFormat := map[string]schemas.EmbeddingData{}
	for _, d := range bifrostResp.Data {
		assert.Equal(t, 0, d.Index, "every representation describes input 0")
		byFormat[d.EncodingFormat] = d
	}

	assert.Equal(t, []float64{0.1, 0.2}, byFormat[schemas.EmbeddingEncodingFloat].Embedding.EmbeddingArray)
	assert.Equal(t, []int8{1, -1}, byFormat[schemas.EmbeddingEncodingInt8].Embedding.EmbeddingInt8Array)
	assert.Equal(t, []int8{1, 0}, byFormat[schemas.EmbeddingEncodingBinary].Embedding.EmbeddingInt8Array)
	assert.Equal(t, []int32{0, 255}, byFormat[schemas.EmbeddingEncodingUint8].Embedding.EmbeddingInt32Array)
	assert.Equal(t, []int32{7, 8}, byFormat[schemas.EmbeddingEncodingUbinary].Embedding.EmbeddingInt32Array)
	require.NotNil(t, byFormat[schemas.EmbeddingEncodingBase64].Embedding.EmbeddingStr)
	assert.Equal(t, base64Vec, *byFormat[schemas.EmbeddingEncodingBase64].Embedding.EmbeddingStr)

	// The labels are what let the Cohere envelope be rebuilt without losing an encoding.
	roundTrip := ToCohereEmbeddingResponse(bifrostResp)
	require.NotNil(t, roundTrip.ResponseType)
	assert.Equal(t, "embeddings_by_type", *roundTrip.ResponseType)
	assert.Equal(t, resp.Embeddings.Float, roundTrip.Embeddings.Float)
	assert.Equal(t, resp.Embeddings.Int8, roundTrip.Embeddings.Int8)
	assert.Equal(t, resp.Embeddings.Uint8, roundTrip.Embeddings.Uint8)
	assert.Equal(t, resp.Embeddings.Binary, roundTrip.Embeddings.Binary)
	assert.Equal(t, resp.Embeddings.Ubinary, roundTrip.Embeddings.Ubinary)
	assert.Equal(t, resp.Embeddings.Base64, roundTrip.Embeddings.Base64)
}

func TestToCohereEmbeddingResponseOrdersByIndex(t *testing.T) {
	bifrostResp := &schemas.BifrostEmbeddingResponse{
		Data: []schemas.EmbeddingData{
			{Index: 1, EncodingFormat: schemas.EmbeddingEncodingInt8, Embedding: schemas.EmbeddingStruct{EmbeddingInt8Array: []int8{11}}},
			{Index: 1, EncodingFormat: schemas.EmbeddingEncodingFloat, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1.1}}},
			{Index: 0, EncodingFormat: schemas.EmbeddingEncodingInt8, Embedding: schemas.EmbeddingStruct{EmbeddingInt8Array: []int8{10}}},
			{Index: 0, EncodingFormat: schemas.EmbeddingEncodingFloat, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1.0}}},
		},
	}

	resp := ToCohereEmbeddingResponse(bifrostResp)
	assert.Equal(t, [][]float64{{1.0}, {1.1}}, resp.Embeddings.Float)
	assert.Equal(t, [][]int8{{10}, {11}}, resp.Embeddings.Int8)
	assert.Equal(t, 1, bifrostResp.Data[0].Index, "caller's data must not be reordered")

	plain := ToCohereEmbeddingResponse(&schemas.BifrostEmbeddingResponse{
		Data: []schemas.EmbeddingData{
			{Index: 2, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{2}}},
			{Index: 1, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1}}},
			{Index: 0, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{0}}},
		},
	})
	assert.Equal(t, "embeddings_floats", *plain.ResponseType)
	assert.Equal(t, [][]float64{{0}, {1}, {2}}, plain.Embeddings.Float)
}

func TestCohereEmbeddingUint8VectorsSerializeAsNumbers(t *testing.T) {
	// []uint8 is []byte, which marshals to a base64 string; int32 keeps the array form.
	body, err := schemas.Marshal(&CohereEmbeddingData{Uint8: [][]int32{{0, 1, 2, 255}}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"uint8":[[0,1,2,255]]}`, string(body))
}

func TestCohereEmbeddingResponseCarriesImageTokens(t *testing.T) {
	// Live /v2/embed on an image: input_tokens 0, image_tokens 6, no tokens block.
	inputTokens, imageTokens := 0, 6
	bifrostResp := (&CohereEmbeddingResponse{
		Embeddings: &CohereEmbeddingData{Float: [][]float64{{1, 2}}},
		Meta: &CohereEmbeddingMeta{
			BilledUnits: &CohereBilledUnits{InputTokens: &inputTokens, ImageTokens: &imageTokens},
		},
	}).ToBifrostEmbeddingResponse()

	require.NotNil(t, bifrostResp.Usage)
	require.NotNil(t, bifrostResp.Usage.PromptTokensDetails)
	assert.Equal(t, 6, bifrostResp.Usage.PromptTokensDetails.ImageTokens)
	assert.Equal(t, 0, bifrostResp.Usage.PromptTokens, "image tokens bill separately, so they stay out of the text cost base")
	assert.Equal(t, 6, bifrostResp.Usage.TotalTokens)
}

func TestToCohereEmbeddingResponseMetaMatchesEmbedEndpoint(t *testing.T) {
	// /v2/embed returns billed_units only: no tokens block, and never output_tokens.
	resp := ToCohereEmbeddingResponse(&schemas.BifrostEmbeddingResponse{
		Data: []schemas.EmbeddingData{{Index: 0, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1}}}},
		Usage: &schemas.BifrostLLMUsage{
			PromptTokens:        3,
			TotalTokens:         9,
			PromptTokensDetails: &schemas.ChatPromptTokensDetails{ImageTokens: 6},
		},
	})

	require.NotNil(t, resp.Meta)
	assert.Nil(t, resp.Meta.Tokens, "embed never returns a tokens block")
	require.NotNil(t, resp.Meta.BilledUnits)
	assert.Nil(t, resp.Meta.BilledUnits.OutputTokens, "embed never returns output_tokens")
	require.NotNil(t, resp.Meta.BilledUnits.InputTokens)
	assert.Equal(t, 3, *resp.Meta.BilledUnits.InputTokens)
	require.NotNil(t, resp.Meta.BilledUnits.ImageTokens)
	assert.Equal(t, 6, *resp.Meta.BilledUnits.ImageTokens)
}
