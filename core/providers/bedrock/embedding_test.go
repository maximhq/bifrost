package bedrock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBedrockEmbeddingEncodingInvokeRoundTrip(t *testing.T) {
	tests := []struct {
		name             string
		model            string
		invokeBody       string
		providerBody     string
		providerResponse string
	}{
		{
			name:       "Titan V2 default float remains unchanged",
			model:      "amazon.titan-embed-text-v2:0",
			invokeBody: `{"inputText":"hello","dimensions":256}`,
			providerBody: `{
				"inputText":"hello",
				"dimensions":256
			}`,
			providerResponse: `{
				"embedding":[0.25,0.75],
				"inputTextTokenCount":1
			}`,
		},
		{
			name:       "Titan V2 binary",
			model:      "amazon.titan-embed-text-v2:0",
			invokeBody: `{"inputText":"hello","dimensions":256,"embeddingTypes":["binary"]}`,
			providerBody: `{
				"inputText":"hello",
				"dimensions":256,
				"embeddingTypes":["binary"]
			}`,
			providerResponse: `{
				"embeddingsByType":{"binary":[1,0,-1]},
				"inputTextTokenCount":1
			}`,
		},
		{
			name:       "Titan V2 float and binary",
			model:      "amazon.titan-embed-text-v2:0",
			invokeBody: `{"inputText":"hello","embeddingTypes":["float","binary"]}`,
			providerBody: `{
				"inputText":"hello",
				"embeddingTypes":["float","binary"]
			}`,
			providerResponse: `{
				"embeddingsByType":{"float":[0.25,0.75],"binary":[1,0]},
				"inputTextTokenCount":1
			}`,
		},
		{
			name:       "Cohere all typed encodings",
			model:      "cohere.embed-v4:0",
			invokeBody: `{"texts":["hello"],"input_type":"search_document","output_dimension":256,"embedding_types":["float","int8","uint8","binary","ubinary"]}`,
			providerBody: `{
				"texts":["hello"],
				"input_type":"search_document",
				"output_dimension":256,
				"embedding_types":["float","int8","uint8","binary","ubinary"]
			}`,
			providerResponse: `{
				"id":"embed-id",
				"response_type":"embeddings_by_type",
				"embeddings":{
					"float":[[0.25,0.75]],
					"int8":[[1,-1]],
					"uint8":[[1,255]],
					"binary":[[1,0]],
					"ubinary":[[1,0]]
				},
				"texts":["hello"]
			}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			providerRequestBody := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				providerRequestBody <- body
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.providerResponse))
			}))
			defer server.Close()

			provider := newTestProviderWithServer(t, server)
			ctx := testBedrockCtx()
			var invokeRequest BedrockInvokeRequest
			require.NoError(t, json.Unmarshal([]byte(test.invokeBody), &invokeRequest))
			invokeRequest.ModelID = "bedrock/" + test.model

			response, bifrostErr := provider.Embedding(
				ctx,
				testBedrockKey(),
				invokeRequest.ToBifrostEmbeddingRequest(ctx),
			)
			require.Nil(t, bifrostErr)
			require.NotNil(t, response)
			assert.JSONEq(t, test.providerBody, string(<-providerRequestBody))

			invokeResponse, err := ToBedrockEmbeddingInvokeResponse(ctx, response)
			require.NoError(t, err)
			wireResponse, err := json.Marshal(invokeResponse)
			require.NoError(t, err)
			assert.JSONEq(t, test.providerResponse, string(wireResponse))
		})
	}
}

func TestToBedrockTitanEmbeddingRequestEncodingTypes(t *testing.T) {
	text := "hello"
	t.Run("rejects missing input without panicking", func(t *testing.T) {
		req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{})
		require.Error(t, err)
		assert.Nil(t, req)
		assert.Contains(t, err.Error(), "no input")
	})

	t.Run("rejects empty input without panicking", func(t *testing.T) {
		req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: &schemas.EmbeddingInput{},
		})
		require.Error(t, err)
		assert.Nil(t, req)
		assert.Contains(t, err.Error(), "no input")
	})

	t.Run("accepts decoded JSON arrays", func(t *testing.T) {
		req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: &schemas.EmbeddingInput{Text: &text},
			Params: &schemas.EmbeddingParameters{ExtraParams: map[string]interface{}{
				"embeddingTypes": []interface{}{"float", "binary"},
			}},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"float", "binary"}, req.EmbeddingTypes)
		assert.NotContains(t, req.ExtraParams, "embeddingTypes")
	})

	t.Run("preserves invalid values for native validation", func(t *testing.T) {
		invalid := []interface{}{"binary", float64(1)}
		req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: &schemas.EmbeddingInput{Text: &text},
			Params: &schemas.EmbeddingParameters{ExtraParams: map[string]interface{}{
				"embeddingTypes": invalid,
			}},
		})
		require.NoError(t, err)
		assert.Nil(t, req.EmbeddingTypes)
		assert.Equal(t, invalid, req.ExtraParams["embeddingTypes"])
	})
}

func TestBedrockTitanEmbeddingResponsePreservesLegacyEmptyEntry(t *testing.T) {
	response := (&BedrockTitanEmbeddingResponse{InputTextTokenCount: 3}).ToBifrostEmbeddingResponse()
	require.NotNil(t, response)
	require.Len(t, response.Data, 1)
	assert.Equal(t, 0, response.Data[0].Index)
	assert.Equal(t, "embedding", response.Data[0].Object)
	assert.Nil(t, response.Data[0].Embedding.EmbeddingArray)
	assert.Equal(t, 3, response.Usage.TotalTokens)
}

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

	t.Run("embedding types accept decoded JSON arrays", func(t *testing.T) {
		text := "hello"
		bifrostReq := &schemas.BifrostEmbeddingRequest{
			Input: &schemas.EmbeddingInput{Text: &text},
			Params: &schemas.EmbeddingParameters{
				ExtraParams: map[string]interface{}{
					"embedding_types": []interface{}{"float", "int8", "binary"},
				},
			},
		}

		req, err := ToBedrockCohereEmbeddingRequest(bifrostReq)
		require.NoError(t, err)
		assert.Equal(t, []string{"float", "int8", "binary"}, req.EmbeddingTypes)
		assert.NotContains(t, req.ExtraParams, "embedding_types")
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
