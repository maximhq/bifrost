package bedrock

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
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
		// expectedWire is what the native invoke route re-emits when it differs from
		// the provider payload — the envelope is rebuilt from the canonical response,
		// so fields the canonical schema does not carry (Cohere's id/texts) drop out.
		expectedWire string
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
			// AWS returns the top-level float vector alongside embeddingsByType
			// whenever float is among the requested representations.
			providerResponse: `{
				"embedding":[0.25,0.75],
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
			expectedWire: `{
				"response_type":"embeddings_by_type",
				"embeddings":{
					"float":[[0.25,0.75]],
					"int8":[[1,-1]],
					"uint8":[[1,255]],
					"binary":[[1,0]],
					"ubinary":[[1,0]]
				}
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

			embeddingRequest, convErr := invokeRequest.ToBifrostEmbeddingRequest(ctx)
			require.NoError(t, convErr)
			response, bifrostErr := provider.Embedding(
				ctx,
				testBedrockKey(),
				embeddingRequest,
			)
			require.Nil(t, bifrostErr)
			require.NotNil(t, response)
			assert.JSONEq(t, test.providerBody, string(<-providerRequestBody))
			if strings.Contains(test.providerResponse, "embeddingsByType") || strings.Contains(test.providerResponse, "embeddings_by_type") {
				assert.Nil(t, response.ExtraFields.RawResponse, "typed native payload must not bypass raw-response policy")
				normalizedJSON, marshalErr := json.Marshal(response)
				require.NoError(t, marshalErr)
				assert.NotContains(t, string(normalizedJSON), "embeddingsByType")
				assert.NotContains(t, string(normalizedJSON), "embeddings_by_type")

				ctxWithRawCapture := testBedrockCtx()
				ctxWithRawCapture.SetValue(schemas.BifrostContextKeyCaptureRawResponse, true)
				rawCaptureRequest, convErr := invokeRequest.ToBifrostEmbeddingRequest(ctxWithRawCapture)
				require.NoError(t, convErr)
				responseWithRawCapture, rawCaptureErr := provider.Embedding(
					ctxWithRawCapture,
					testBedrockKey(),
					rawCaptureRequest,
				)
				require.Nil(t, rawCaptureErr)
				require.NotNil(t, responseWithRawCapture)
				assert.JSONEq(t, test.providerBody, string(<-providerRequestBody))
				assert.NotNil(t, responseWithRawCapture.ExtraFields.RawResponse, "explicit raw-response capture must remain supported")
			}

			expectedWire := test.expectedWire
			if expectedWire == "" {
				expectedWire = test.providerResponse
			}

			invokeResponse, err := ToBedrockEmbeddingInvokeResponse(ctx, response)
			require.NoError(t, err)
			wireResponse, err := json.Marshal(invokeResponse)
			require.NoError(t, err)
			assert.JSONEq(t, expectedWire, string(wireResponse))

			// The envelope must survive a store-and-replay round trip: a cached
			// response reaches this converter as plain JSON, so anything the
			// canonical schema drops on the way out is gone on a cache hit.
			serialized, err := json.Marshal(response)
			require.NoError(t, err)
			var replayed schemas.BifrostEmbeddingResponse
			require.NoError(t, json.Unmarshal(serialized, &replayed))
			replayedInvoke, err := ToBedrockEmbeddingInvokeResponse(ctx, &replayed)
			require.NoError(t, err)
			replayedWire, err := json.Marshal(replayedInvoke)
			require.NoError(t, err)
			assert.JSONEq(t, expectedWire, string(replayedWire), "cached response must re-emit the same native envelope")
		})
	}
}

func TestBedrockTitanEmbeddingResponsePreservesAllTypedEncodings(t *testing.T) {
	response := (&BedrockTitanEmbeddingResponse{
		EmbeddingsByType: &BedrockTitanEmbeddingsByType{
			Float:  []float64{0.25, 0.75},
			Binary: []int8{1, 0, -1},
		},
		InputTextTokenCount: 3,
	}).ToBifrostEmbeddingResponse()

	require.NotNil(t, response)
	require.Len(t, response.Data, 2)
	assert.Equal(t, []float64{0.25, 0.75}, response.Data[0].Embedding.EmbeddingArray)
	assert.Equal(t, []int8{1, 0, -1}, response.Data[1].Embedding.EmbeddingInt8Array)
	assert.Equal(t, 0, response.Data[0].Index)
	assert.Equal(t, 0, response.Data[1].Index)
	// The labels are what let the native invoke converter tell the two entries
	// apart; they share an index because both describe the same input.
	assert.Equal(t, schemas.EmbeddingEncodingFloat, response.Data[0].EncodingFormat)
	assert.Equal(t, schemas.EmbeddingEncodingBinary, response.Data[1].EncodingFormat)
}

func TestBedrockTitanEmbeddingResponsePrefersTypedEncodingsOverLegacyFloat(t *testing.T) {
	response := (&BedrockTitanEmbeddingResponse{
		// Titan V2 includes this legacy float vector alongside embeddingsByType when
		// float is one of multiple requested representations.
		Embedding: []float64{0.5, 0.5},
		EmbeddingsByType: &BedrockTitanEmbeddingsByType{
			Float:  []float64{0.25, 0.75},
			Binary: []int8{1, 0},
		},
		InputTextTokenCount: 3,
	}).ToBifrostEmbeddingResponse()

	require.NotNil(t, response)
	require.Len(t, response.Data, 2)
	assert.Equal(t, []float64{0.25, 0.75}, response.Data[0].Embedding.EmbeddingArray)
	assert.Equal(t, []int8{1, 0}, response.Data[1].Embedding.EmbeddingInt8Array)
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
			Input: []schemas.EmbeddingInputItem{},
		})
		require.Error(t, err)
		assert.Nil(t, req)
		assert.Contains(t, err.Error(), "no input")
	})

	t.Run("accepts decoded JSON arrays", func(t *testing.T) {
		req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
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
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
			Params: &schemas.EmbeddingParameters{ExtraParams: map[string]interface{}{
				"embeddingTypes": invalid,
			}},
		})
		require.NoError(t, err)
		assert.Nil(t, req.EmbeddingTypes)
		assert.Equal(t, invalid, req.ExtraParams["embeddingTypes"])
	})
}

// titan-embed-image-v1 accepts an image alongside the text. The text models must keep
// rejecting images rather than quietly embedding the text alone.
func TestToBedrockTitanMultimodalEmbeddingRequest(t *testing.T) {
	redPixelPNG := "iVBORw0KGgoAAAANSUhEUg=="

	t.Run("image part reaches inputImage with the data URI stripped", func(t *testing.T) {
		dataURI := "data:image/png;base64," + redPixelPNG
		text := "a red square"
		dimensions := 384
		req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: "amazon.titan-embed-image-v1",
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeText, Text: &text},
				{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{Data: &dataURI}},
			}}},
			Params: &schemas.EmbeddingParameters{Dimensions: &dimensions},
		})
		require.NoError(t, err)
		require.NotNil(t, req.InputImage)
		assert.Equal(t, redPixelPNG, *req.InputImage)
		assert.Equal(t, "a red square", req.InputText)
		// The multimodal model carries the output length under embeddingConfig, not the
		// top-level dimensions field the text models use.
		assert.Nil(t, req.Dimensions)
		require.NotNil(t, req.EmbeddingConfig)
		require.NotNil(t, req.EmbeddingConfig.OutputEmbeddingLength)
		assert.Equal(t, 384, *req.EmbeddingConfig.OutputEmbeddingLength)
	})

	t.Run("image only, no text", func(t *testing.T) {
		req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: "amazon.titan-embed-image-v1",
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{Data: &redPixelPNG}},
			}}},
		})
		require.NoError(t, err)
		assert.Empty(t, req.InputText)
		require.NotNil(t, req.InputImage)
		wireBody, err := json.Marshal(req)
		require.NoError(t, err)
		assert.NotContains(t, string(wireBody), `"inputText"`)
	})

	// An image bound for a text-only model is forwarded rather than rejected here:
	// AWS answers "extraneous key [inputImage] is not permitted", which is more
	// accurate than a verdict guessed from the model name and cannot go stale when
	// AWS adds a variant.
	t.Run("text-only Titan models forward the image and let AWS rule", func(t *testing.T) {
		req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: "amazon.titan-embed-text-v2:0",
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{Data: &redPixelPNG}},
			}}},
		})
		require.NoError(t, err)
		require.NotNil(t, req.InputImage)
		assert.Equal(t, redPixelPNG, *req.InputImage)
	})

	t.Run("part types Titan has no field for are still rejected", func(t *testing.T) {
		audio := "AA=="
		_, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: "amazon.titan-embed-image-v1",
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeAudio, Audio: &schemas.EmbeddingMediaPart{Data: &audio}},
			}}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `do not support "audio" parts`)
	})

	t.Run("two images cannot be represented in one request", func(t *testing.T) {
		_, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: "amazon.titan-embed-image-v1",
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{Data: &redPixelPNG}},
				{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{Data: &redPixelPNG}},
			}}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at most one image")
	})

	t.Run("remote urls are rejected rather than dropped", func(t *testing.T) {
		remote := "https://example.com/red.png"
		_, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: "amazon.titan-embed-image-v1",
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: &remote}},
			}}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inline base64 image data")
	})

	t.Run("invoke route carries inputImage through as one input", func(t *testing.T) {
		assert.Equal(t, schemas.EmbeddingRequest,
			DetectInvokeRequestType([]byte(`{"inputImage":"`+redPixelPNG+`"}`), "amazon.titan-embed-image-v1"))

		var invokeRequest BedrockInvokeRequest
		require.NoError(t, json.Unmarshal(
			[]byte(`{"inputText":"a red square","inputImage":"`+redPixelPNG+`","embeddingConfig":{"outputEmbeddingLength":256}}`),
			&invokeRequest))
		invokeRequest.ModelID = "bedrock/amazon.titan-embed-image-v1"

		converted, err := invokeRequest.ToBifrostEmbeddingRequest(testBedrockCtx())
		require.NoError(t, err)
		require.Len(t, converted.Input, 1, "inputText and inputImage describe one input")
		require.Len(t, converted.Input[0].Content, 2)
		assert.Equal(t, schemas.EmbeddingContentPartTypeText, converted.Input[0].Content[0].Type)
		assert.Equal(t, schemas.EmbeddingContentPartTypeImage, converted.Input[0].Content[1].Type)
		require.NotNil(t, converted.Params.Dimensions)
		assert.Equal(t, 256, *converted.Params.Dimensions)
	})
}

func TestBedrockTitanEmbeddingResponsePreservesLegacyEmptyEntry(t *testing.T) {
	response := (&BedrockTitanEmbeddingResponse{InputTextTokenCount: 3}).ToBifrostEmbeddingResponse()
	require.NotNil(t, response)
	require.Len(t, response.Data, 1)
	assert.Equal(t, 0, response.Data[0].Index)
	assert.Equal(t, "embedding", response.Data[0].Object)
	assert.Nil(t, response.Data[0].Embedding.EmbeddingArray)
	assert.Empty(t, response.Data[0].EncodingFormat, "an ordinary single-vector response stays unlabelled")
	assert.Equal(t, 3, response.Usage.TotalTokens)
}

func TestBedrockTitanEmbeddingResponseLeavesOrdinaryV2ResponseUnlabelled(t *testing.T) {
	// AWS returns embeddingsByType on every Titan V2 response, carrying float alone
	// for an ordinary request. Labelling that would change the shape of every
	// existing Titan V2 call, so only a multi-representation response is labelled.
	response := (&BedrockTitanEmbeddingResponse{
		Embedding:           []float64{0.25, 0.75},
		EmbeddingsByType:    &BedrockTitanEmbeddingsByType{Float: []float64{0.25, 0.75}},
		InputTextTokenCount: 4,
	}).ToBifrostEmbeddingResponse()

	require.NotNil(t, response)
	require.Len(t, response.Data, 1)
	assert.Empty(t, response.Data[0].EncodingFormat)
	assert.Equal(t, []float64{0.25, 0.75}, response.Data[0].Embedding.EmbeddingArray)

	invokeResponse, err := ToBedrockEmbeddingInvokeResponse(testBedrockCtx(), response)
	require.NoError(t, err)
	wire, err := json.Marshal(invokeResponse)
	require.NoError(t, err)
	assert.JSONEq(t, `{"embedding":[0.25,0.75],"inputTextTokenCount":4}`, string(wire),
		"the native envelope for an ordinary Titan V2 request must not change")
}

// The Titan envelope holds one input's vectors. A response covering several inputs — only
// reachable when the invoke route fronts a non-Titan provider — has to use the
// multi-embedding envelope, or every vector but one is dropped at HTTP 200.
func TestBedrockEmbeddingInvokeEnvelopeFitsInputCount(t *testing.T) {
	ctx := testBedrockCtx()

	t.Run("non-Titan provider with two inputs", func(t *testing.T) {
		wire, err := ToBedrockEmbeddingInvokeResponse(ctx, &schemas.BifrostEmbeddingResponse{
			Model: "text-embedding-3-small",
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 8, TotalTokens: 8},
			Data: []schemas.EmbeddingData{
				{Index: 0, Object: "embedding", Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{0.25, 0.75}}},
				{Index: 1, Object: "embedding", Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{-0.5, 0.125}}},
			},
		})
		require.NoError(t, err)
		encoded, err := json.Marshal(wire)
		require.NoError(t, err)
		assert.JSONEq(t, `{"embeddings":[[0.25,0.75],[-0.5,0.125]],"response_type":"embeddings_floats"}`, string(encoded))
	})

	t.Run("labelled entries from a non-Titan provider stay distinct", func(t *testing.T) {
		wire, err := ToBedrockEmbeddingInvokeResponse(ctx, &schemas.BifrostEmbeddingResponse{
			Model: "embed-english-v3.0",
			Data: []schemas.EmbeddingData{
				{Index: 0, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{0.25}}, EncodingFormat: schemas.EmbeddingEncodingFloat},
				{Index: 1, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{0.5}}, EncodingFormat: schemas.EmbeddingEncodingFloat},
			},
		})
		require.NoError(t, err)
		encoded, err := json.Marshal(wire)
		require.NoError(t, err)
		assert.JSONEq(t, `{"embeddings":{"float":[[0.25],[0.5]]},"response_type":"embeddings_by_type"}`, string(encoded))
	})

	// Titan's typed response carries several entries for one input, which the Titan
	// envelope does represent — the index, not the entry count, is what decides.
	t.Run("Titan float plus binary for one input keeps the Titan envelope", func(t *testing.T) {
		wire, err := ToBedrockEmbeddingInvokeResponse(ctx, &schemas.BifrostEmbeddingResponse{
			Model: "amazon.titan-embed-text-v2:0",
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 1, TotalTokens: 1},
			Data: []schemas.EmbeddingData{
				{Index: 0, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{0.25, 0.75}}, EncodingFormat: schemas.EmbeddingEncodingFloat},
				{Index: 0, Embedding: schemas.EmbeddingStruct{EmbeddingInt8Array: []int8{1, 0}}, EncodingFormat: schemas.EmbeddingEncodingBinary},
			},
		})
		require.NoError(t, err)
		encoded, err := json.Marshal(wire)
		require.NoError(t, err)
		assert.JSONEq(t, `{"embedding":[0.25,0.75],"embeddingsByType":{"float":[0.25,0.75],"binary":[1,0]},"inputTextTokenCount":1}`, string(encoded))
	})
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
	})

	t.Run("returns error for non-nil but empty input", func(t *testing.T) {
		req, err := ToBedrockCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: nil,
		})
		require.Error(t, err)
		assert.Nil(t, req)
	})

	t.Run("single text content extracts typed params", func(t *testing.T) {
		text := "hello"
		truncate := "RIGHT"
		dimensions := 512
		maxTokens := 128
		bifrostReq := &schemas.BifrostEmbeddingRequest{
			Model: "cohere.embed-english-v3",
			Input: []schemas.EmbeddingInputItem{
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}},
			},
			Params: &schemas.EmbeddingParameters{
				Dimensions: &dimensions,
				ExtraParams: map[string]interface{}{
					"input_type":      "search_query",
					"embedding_types": []string{"float"},
					"trace_id":        "req-123",
					"max_tokens":      maxTokens,
					"truncate":        truncate,
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
		assert.Equal(t, &maxTokens, req.MaxTokens)
		require.NotNil(t, req.Truncate)
		assert.Equal(t, truncate, *req.Truncate)
		assert.Equal(t, map[string]interface{}{"trace_id": "req-123"}, req.ExtraParams)
	})

	t.Run("multiple text contents batch into texts array", func(t *testing.T) {
		hello := "hello"
		world := "world"
		bifrostReq := &schemas.BifrostEmbeddingRequest{
			Model: "cohere.embed-multilingual-v3",
			Input: []schemas.EmbeddingInputItem{
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &hello}}},
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &world}}},
			},
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

	t.Run("defaults input_type when the caller omits it", func(t *testing.T) {
		// AWS requires input_type and has no default, so an absent value would go out
		// as "" and be rejected. SDKs shaped around Titan's single-field body never
		// send it; this is what lets them reach a Cohere model at all.
		text := "hello"
		req, err := ToBedrockCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
		})
		require.NoError(t, err)
		assert.Equal(t, BedrockCohereInputTypeSearchDocument, req.InputType)
	})

	t.Run("caller input_type always wins over the default", func(t *testing.T) {
		text := "hello"
		req, err := ToBedrockCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
			Params: &schemas.EmbeddingParameters{
				ExtraParams: map[string]interface{}{"input_type": "search_query"},
			},
		})
		require.NoError(t, err)
		assert.Equal(t, "search_query", req.InputType)
	})

	t.Run("embedding types accept decoded JSON arrays", func(t *testing.T) {
		text := "hello"
		bifrostReq := &schemas.BifrostEmbeddingRequest{
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
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

	// encoding_format is the standard field and carries the representation on the
	// ordinary path.
	t.Run("encoding_format maps onto embedding_types", func(t *testing.T) {
		text := "hello"
		int8Format := schemas.EmbeddingEncodingInt8
		req, err := ToBedrockCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model:  "cohere.embed-v4:0",
			Input:  []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
			Params: &schemas.EmbeddingParameters{EncodingFormat: &int8Format},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"int8"}, req.EmbeddingTypes)
	})

	// v3 has no base64 representation and v4's is byte-identical to encoding the float
	// vector locally, so base64 is served from floats rather than asked of AWS.
	t.Run("encoding_format base64 is not forwarded as embedding_types", func(t *testing.T) {
		text := "hello"
		base64Format := schemas.EmbeddingEncodingBase64
		req, err := ToBedrockCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model:  "cohere.embed-v4:0",
			Input:  []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
			Params: &schemas.EmbeddingParameters{EncodingFormat: &base64Format},
		})
		require.NoError(t, err)
		assert.Empty(t, req.EmbeddingTypes)
	})

	// embedding_types reaches the converter only through the Bedrock integration, where
	// it is the caller's own wire field and must survive untouched — including the
	// multi-representation form encoding_format cannot express.
	t.Run("native embedding_types wins over encoding_format", func(t *testing.T) {
		text := "hello"
		base64Format := schemas.EmbeddingEncodingBase64
		req, err := ToBedrockCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: "cohere.embed-v4:0",
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
			Params: &schemas.EmbeddingParameters{
				EncodingFormat: &base64Format,
				ExtraParams:    map[string]interface{}{"embedding_types": []string{"float", "int8"}},
			},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"float", "int8"}, req.EmbeddingTypes)
	})
}

// Titan mirrors Cohere: encoding_format on the standard path, embeddingTypes for the
// Bedrock integration, integration wins.
func TestToBedrockTitanEmbeddingRequestEncodingFormat(t *testing.T) {
	text := "hello"
	input := []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}}

	t.Run("encoding_format maps onto embeddingTypes", func(t *testing.T) {
		format := schemas.EmbeddingEncodingBinary
		req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model:  "amazon.titan-embed-text-v2:0",
			Input:  input,
			Params: &schemas.EmbeddingParameters{EncodingFormat: &format},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"binary"}, req.EmbeddingTypes)
	})

	// Titan G1 takes inputText and nothing else - AWS rejects embeddingTypes as an extraneous
	// key - and float is what it returns anyway, so asking for it must stay a no-op.
	t.Run("encoding_format float is never forwarded", func(t *testing.T) {
		format := schemas.EmbeddingEncodingFloat
		for _, model := range []string{"amazon.titan-embed-text-v1", "amazon.titan-embed-g1-text-02", "amazon.titan-embed-text-v2:0"} {
			req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
				Model:  model,
				Input:  input,
				Params: &schemas.EmbeddingParameters{EncodingFormat: &format},
			})
			require.NoError(t, err)
			assert.Empty(t, req.EmbeddingTypes, "float leaked into the request for %s", model)
		}
	})

	t.Run("native embeddingTypes wins over encoding_format", func(t *testing.T) {
		format := schemas.EmbeddingEncodingBase64
		req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: "amazon.titan-embed-text-v2:0",
			Input: input,
			Params: &schemas.EmbeddingParameters{
				EncodingFormat: &format,
				ExtraParams:    map[string]interface{}{"embeddingTypes": []string{"float", "binary"}},
			},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"float", "binary"}, req.EmbeddingTypes)
	})

	t.Run("absent encoding_format leaves embeddingTypes unset", func(t *testing.T) {
		req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model:  "amazon.titan-embed-text-v2:0",
			Input:  input,
			Params: &schemas.EmbeddingParameters{},
		})
		require.NoError(t, err)
		assert.Empty(t, req.EmbeddingTypes)
	})

	// base64 is outside Titan's enum: AWS answers "only 1 subschema matches out of 2"
	// rather than ignoring it, so it is encoded from the float vector on the way back.
	t.Run("encoding_format base64 is not forwarded", func(t *testing.T) {
		format := schemas.EmbeddingEncodingBase64
		req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model:  "amazon.titan-embed-text-v2:0",
			Input:  input,
			Params: &schemas.EmbeddingParameters{EncodingFormat: &format},
		})
		require.NoError(t, err)
		assert.Empty(t, req.EmbeddingTypes)
	})

	// titan-embed-image-v1 has no embeddingTypes field at all; AWS rejects the key as
	// extraneous, so even a valid representation must not be forwarded for it.
	t.Run("multimodal model never carries embeddingTypes", func(t *testing.T) {
		for _, format := range []string{schemas.EmbeddingEncodingFloat, schemas.EmbeddingEncodingBinary, schemas.EmbeddingEncodingBase64} {
			f := format
			req, err := ToBedrockTitanEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
				Model:  "amazon.titan-embed-image-v1",
				Input:  input,
				Params: &schemas.EmbeddingParameters{EncodingFormat: &f},
			})
			require.NoError(t, err)
			assert.Empty(t, req.EmbeddingTypes, "encoding_format %q leaked into the multimodal request", f)
		}
	})
}

// The base64 a caller gets when the model has no native base64 representation. The bytes
// are little-endian float32, which is what Bedrock Cohere v4 returns for embedding_types
// base64 and what OpenAI's encoding_format base64 produces - verified against both.
func TestEncodeEmbeddingsAsBase64(t *testing.T) {
	t.Run("float vectors become little-endian float32 base64", func(t *testing.T) {
		resp := &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{
			{Index: 0, Object: "embedding", Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1, -2, 0.5}}},
		}}
		encodeEmbeddingsAsBase64(resp)

		require.NotNil(t, resp.Data[0].Embedding.EmbeddingStr)
		assert.Nil(t, resp.Data[0].Embedding.EmbeddingArray)
		assert.Equal(t, schemas.EmbeddingEncodingBase64, resp.Data[0].EncodingFormat)

		raw, err := base64.StdEncoding.DecodeString(*resp.Data[0].Embedding.EmbeddingStr)
		require.NoError(t, err)
		require.Len(t, raw, 12)
		for i, want := range []float32{1, -2, 0.5} {
			assert.Equal(t, want, math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:])))
		}
	})

	t.Run("a representation the provider already returned is left alone", func(t *testing.T) {
		native := "already-encoded"
		resp := &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{
			{Index: 0, Object: "embedding", Embedding: schemas.EmbeddingStruct{EmbeddingStr: &native}, EncodingFormat: schemas.EmbeddingEncodingBase64},
		}}
		encodeEmbeddingsAsBase64(resp)
		assert.Equal(t, native, *resp.Data[0].Embedding.EmbeddingStr)
	})

	t.Run("every entry of a batch is converted", func(t *testing.T) {
		resp := &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{
			{Index: 0, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1}}},
			{Index: 1, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{2}}},
		}}
		encodeEmbeddingsAsBase64(resp)
		for i := range resp.Data {
			require.NotNil(t, resp.Data[i].Embedding.EmbeddingStr, "entry %d was not converted", i)
		}
		assert.NotEqual(t, *resp.Data[0].Embedding.EmbeddingStr, *resp.Data[1].Embedding.EmbeddingStr)
	})
}

// Which requests the local encoding applies to. A Bedrock-native embedding_types field
// means the caller came through the integration and owns the response envelope.
func TestShouldEncodeEmbeddingsAsBase64(t *testing.T) {
	base64Format := schemas.EmbeddingEncodingBase64
	floatFormat := schemas.EmbeddingEncodingFloat

	assert.False(t, shouldEncodeEmbeddingsAsBase64(nil))
	assert.False(t, shouldEncodeEmbeddingsAsBase64(&schemas.BifrostEmbeddingRequest{}))
	assert.False(t, shouldEncodeEmbeddingsAsBase64(&schemas.BifrostEmbeddingRequest{
		Params: &schemas.EmbeddingParameters{EncodingFormat: &floatFormat},
	}))
	assert.True(t, shouldEncodeEmbeddingsAsBase64(&schemas.BifrostEmbeddingRequest{
		Params: &schemas.EmbeddingParameters{EncodingFormat: &base64Format},
	}))
	assert.False(t, shouldEncodeEmbeddingsAsBase64(&schemas.BifrostEmbeddingRequest{
		Params: &schemas.EmbeddingParameters{
			EncodingFormat: &base64Format,
			ExtraParams:    map[string]interface{}{"embedding_types": []string{"float"}},
		},
	}))
	assert.False(t, shouldEncodeEmbeddingsAsBase64(&schemas.BifrostEmbeddingRequest{
		Params: &schemas.EmbeddingParameters{
			EncodingFormat: &base64Format,
			ExtraParams:    map[string]interface{}{"embeddingTypes": []string{"float", "binary"}},
		},
	}))
}

func TestToBedrockCohereEmbeddingRequestWireBody(t *testing.T) {
	text := "hello"
	bifrostReq := &schemas.BifrostEmbeddingRequest{
		Model: "cohere.embed-english-v3",
		Input: []schemas.EmbeddingInputItem{
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}},
		},
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
	assert.JSONEq(t, `{
		"input_type": "search_document",
		"texts": ["hello"],
		"embedding_types": ["float"]
	}`, string(wireBody))
}

func TestBedrockInvokeEmbeddingRejectsInvalidInputBlocks(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"unknown type", `{"input_type":"search_document","inputs":[{"content":[{"type":"text","text":"a"},{"type":"document","text":"b"}]}]}`},
		{"text without text", `{"input_type":"search_document","inputs":[{"content":[{"type":"text"}]}]}`},
		{"image_url without image_url", `{"input_type":"search_document","inputs":[{"content":[{"type":"image_url"}]}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var invokeRequest BedrockInvokeRequest
			require.NoError(t, json.Unmarshal([]byte(test.body), &invokeRequest))
			invokeRequest.ModelID = "bedrock/cohere.embed-v4:0"

			req, err := invokeRequest.ToBifrostEmbeddingRequest(testBedrockCtx())
			require.Error(t, err)
			assert.Nil(t, req)
			assert.True(t, providerUtils.IsInvalidRequestError(err))
		})
	}

	var valid BedrockInvokeRequest
	require.NoError(t, json.Unmarshal([]byte(`{"input_type":"search_document","inputs":[{"content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`), &valid))
	valid.ModelID = "bedrock/cohere.embed-v4:0"
	req, err := valid.ToBifrostEmbeddingRequest(testBedrockCtx())
	require.NoError(t, err)
	require.Len(t, req.Input, 1)
	assert.Len(t, req.Input[0].Content, 2)
}

// Nova is a third envelope alongside Titan's and Cohere's: a taskType plus a params
// object holding exactly one modality, every knob of which AWS requires and none of
// which it defaults. Probed live against amazon.nova-2-multimodal-embeddings-v1:0.
func TestToBedrockNovaEmbeddingRequest(t *testing.T) {
	const novaModel = "amazon.nova-2-multimodal-embeddings-v1:0"
	redPixelPNG := "iVBORw0KGgoAAAANSUhEUg=="

	textRequest := func(params *schemas.EmbeddingParameters) *schemas.BifrostEmbeddingRequest {
		text := "alpha kestrel harbor"
		return &schemas.BifrostEmbeddingRequest{
			Model: novaModel,
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeText, Text: &text},
			}}},
			Params: params,
		}
	}

	t.Run("text produces the documented wire body", func(t *testing.T) {
		req, err := ToBedrockNovaEmbeddingRequest(textRequest(nil))
		require.NoError(t, err)
		wireBody, err := json.Marshal(req)
		require.NoError(t, err)
		assert.JSONEq(t, `{
			"taskType": "SINGLE_EMBEDDING",
			"singleEmbeddingParams": {
				"embeddingPurpose": "GENERIC_INDEX",
				"text": {"truncationMode": "END", "value": "alpha kestrel harbor"}
			}
		}`, string(wireBody))
	})

	t.Run("dimensions map onto embeddingDimension", func(t *testing.T) {
		dimensions := 1024
		req, err := ToBedrockNovaEmbeddingRequest(textRequest(&schemas.EmbeddingParameters{Dimensions: &dimensions}))
		require.NoError(t, err)
		require.NotNil(t, req.SingleEmbeddingParams.EmbeddingDimension)
		assert.Equal(t, 1024, *req.SingleEmbeddingParams.EmbeddingDimension)
	})

	// embeddingPurpose is Nova's own name for task_type and arrives only through the
	// Bedrock integration, so it wins - the same precedence embeddingTypes has on Titan.
	t.Run("task_type becomes embeddingPurpose and embeddingPurpose outranks it", func(t *testing.T) {
		req, err := ToBedrockNovaEmbeddingRequest(textRequest(&schemas.EmbeddingParameters{
			TaskType: schemas.Ptr("CLASSIFICATION"),
		}))
		require.NoError(t, err)
		// Compared against the wire spelling, not the const, so a const whose value drifts
		// from AWS's enum still fails here.
		assert.Equal(t, BedrockNovaEmbeddingPurpose("CLASSIFICATION"), req.SingleEmbeddingParams.EmbeddingPurpose)

		req, err = ToBedrockNovaEmbeddingRequest(textRequest(&schemas.EmbeddingParameters{
			TaskType:    schemas.Ptr("CLASSIFICATION"),
			ExtraParams: map[string]interface{}{"embeddingPurpose": "DOCUMENT_RETRIEVAL"},
		}))
		require.NoError(t, err)
		assert.Equal(t, BedrockNovaEmbeddingPurpose("DOCUMENT_RETRIEVAL"), req.SingleEmbeddingParams.EmbeddingPurpose)
	})

	t.Run("truncationMode is overridable and stays out of ExtraParams", func(t *testing.T) {
		req, err := ToBedrockNovaEmbeddingRequest(textRequest(&schemas.EmbeddingParameters{
			ExtraParams: map[string]interface{}{"truncationMode": "START", "someOtherKey": 1},
		}))
		require.NoError(t, err)
		assert.Equal(t, BedrockNovaTruncationMode("START"), req.SingleEmbeddingParams.Text.TruncationMode)
		assert.NotContains(t, req.ExtraParams, "truncationMode")
		assert.Contains(t, req.ExtraParams, "someOtherKey")
	})

	t.Run("several text parts join into the one value Nova accepts", func(t *testing.T) {
		first, second := "alpha", "kestrel"
		req, err := ToBedrockNovaEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: novaModel,
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeText, Text: &first},
				{Type: schemas.EmbeddingContentPartTypeText, Text: &second},
			}}},
		})
		require.NoError(t, err)
		require.NotNil(t, req.SingleEmbeddingParams.Text.Value)
		assert.Equal(t, "alpha\nkestrel", *req.SingleEmbeddingParams.Text.Value)
	})

	t.Run("inline media travels as bytes with the format derived", func(t *testing.T) {
		tests := []struct {
			name     string
			part     schemas.EmbeddingContentPart
			format   string
			pickFrom func(*BedrockNovaSingleEmbeddingParams) (string, BedrockNovaEmbeddingSource)
		}{
			{
				name: "image data uri",
				part: schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeImage,
					Image: &schemas.EmbeddingMediaPart{Data: schemas.Ptr("data:image/png;base64," + redPixelPNG)}},
				format: "png",
				pickFrom: func(p *BedrockNovaSingleEmbeddingParams) (string, BedrockNovaEmbeddingSource) {
					return p.Image.Format, p.Image.Source
				},
			},
			{
				name: "image bare base64 with mime_type",
				part: schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeImage,
					Image: &schemas.EmbeddingMediaPart{Data: &redPixelPNG, MIMEType: schemas.Ptr("image/jpeg")}},
				format: "jpeg",
				pickFrom: func(p *BedrockNovaSingleEmbeddingParams) (string, BedrockNovaEmbeddingSource) {
					return p.Image.Format, p.Image.Source
				},
			},
			{
				name: "audio",
				part: schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeAudio,
					Audio: &schemas.EmbeddingMediaPart{Data: schemas.Ptr("data:audio/mpeg;base64,AA==")}},
				format: "mp3",
				pickFrom: func(p *BedrockNovaSingleEmbeddingParams) (string, BedrockNovaEmbeddingSource) {
					return p.Audio.Format, p.Audio.Source
				},
			},
			{
				name: "video",
				part: schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeVideo,
					Video: &schemas.EmbeddingMediaPart{Data: schemas.Ptr("data:video/quicktime;base64,AA==")}},
				format: "mov",
				pickFrom: func(p *BedrockNovaSingleEmbeddingParams) (string, BedrockNovaEmbeddingSource) {
					return p.Video.Format, p.Video.Source
				},
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				req, err := ToBedrockNovaEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
					Model: novaModel,
					Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{test.part}}},
				})
				require.NoError(t, err)
				format, source := test.pickFrom(req.SingleEmbeddingParams)
				assert.Equal(t, test.format, format)
				require.NotNil(t, source.Bytes, "inline media travels as bytes")
				assert.Nil(t, source.S3Location)
				assert.NotContains(t, *source.Bytes, "data:", "the data URI prefix is stripped")
			})
		}
	})

	// Nova resolves s3:// itself, the only Bedrock embedding model that does.
	t.Run("s3 urls become an s3Location", func(t *testing.T) {
		req, err := ToBedrockNovaEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: novaModel,
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeImage,
					Image: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("s3://my-bucket/photos/red-square.png")}},
			}}},
		})
		require.NoError(t, err)
		require.NotNil(t, req.SingleEmbeddingParams.Image.Source.S3Location)
		assert.Equal(t, "s3://my-bucket/photos/red-square.png", req.SingleEmbeddingParams.Image.Source.S3Location.URI)
		assert.Equal(t, "png", req.SingleEmbeddingParams.Image.Format, "the key's extension names the format when no mime_type does")
	})

	// embeddingMode is required on video and has no AWS default. AUDIO_VIDEO_SEPARATE
	// asks for an audio and a video vector instead of one covering both.
	t.Run("video carries an embeddingMode, overridable through extra params", func(t *testing.T) {
		videoRequest := func(params *schemas.EmbeddingParameters) *schemas.BifrostEmbeddingRequest {
			return &schemas.BifrostEmbeddingRequest{
				Model: novaModel,
				Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
					{Type: schemas.EmbeddingContentPartTypeVideo,
						Video: &schemas.EmbeddingMediaPart{Data: schemas.Ptr("data:video/mp4;base64,AA==")}},
				}}},
				Params: params,
			}
		}
		req, err := ToBedrockNovaEmbeddingRequest(videoRequest(nil))
		require.NoError(t, err)
		assert.Equal(t, BedrockNovaVideoMode("AUDIO_VIDEO_COMBINED"), req.SingleEmbeddingParams.Video.EmbeddingMode)

		req, err = ToBedrockNovaEmbeddingRequest(videoRequest(&schemas.EmbeddingParameters{
			ExtraParams: map[string]interface{}{"embeddingMode": "AUDIO_VIDEO_SEPARATE"},
		}))
		require.NoError(t, err)
		assert.Equal(t, BedrockNovaVideoMode("AUDIO_VIDEO_SEPARATE"), req.SingleEmbeddingParams.Video.EmbeddingMode)
	})

	t.Run("requests Nova cannot represent are refused here", func(t *testing.T) {
		tests := []struct {
			name    string
			input   []schemas.EmbeddingInputItem
			message string
		}{
			{
				name: "text and image in one item",
				input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
					{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("a red square")},
					{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{Data: schemas.Ptr("data:image/png;base64," + redPixelPNG)}},
				}}},
				message: "one modality per request",
			},
			{
				name: "two images in one item",
				input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
					{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{Data: schemas.Ptr("data:image/png;base64," + redPixelPNG)}},
					{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{Data: schemas.Ptr("data:image/png;base64," + redPixelPNG)}},
				}}},
				message: "one modality per request",
			},
			{
				name: "a batch of two items",
				input: []schemas.EmbeddingInputItem{
					{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("alpha")}}},
					{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("kestrel")}}},
				},
				message: "exactly one content item per request",
			},
			{
				name: "a file part, which Nova has no modality for",
				input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
					{Type: schemas.EmbeddingContentPartTypeFile, File: &schemas.EmbeddingMediaPart{Data: schemas.Ptr("data:application/pdf;base64,AA==")}},
				}}},
				message: `do not support "file" parts`,
			},
			{
				name: "an https url Nova will not fetch",
				input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
					{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("https://example.com/red.png")}},
				}}},
				message: "s3:// or inline base64 only",
			},
			{
				name: "an s3 uri naming no object",
				input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
					{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("s3://my-bucket")}},
				}}},
				message: "expected s3://bucket/key",
			},
			{
				name: "media whose format nothing names",
				input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
					{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{Data: &redPixelPNG}},
				}}},
				message: "cannot determine the image format",
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				_, err := ToBedrockNovaEmbeddingRequest(&schemas.BifrostEmbeddingRequest{Model: novaModel, Input: test.input})
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.message)
				assert.True(t, providerUtils.IsInvalidRequestError(err), "caller faults must be 400s, got %v", err)
			})
		}
	})
}

func TestBedrockNovaEmbeddingResponse(t *testing.T) {
	t.Run("each vector carries the modality that produced it", func(t *testing.T) {
		var response BedrockNovaEmbeddingResponse
		require.NoError(t, json.Unmarshal([]byte(`{"embeddings":[
			{"embedding":[0.1,0.2],"embeddingType":"AUDIO"},
			{"embedding":[0.3,0.4],"embeddingType":"VIDEO"}
		]}`), &response))

		converted, err := response.ToBifrostEmbeddingResponse()
		require.NoError(t, err)
		require.Len(t, converted.Data, 2)
		assert.Equal(t, schemas.EmbeddingModalityAudio, converted.Data[0].Modality)
		assert.Equal(t, 0, converted.Data[0].Index)
		assert.Equal(t, schemas.EmbeddingModalityVideo, converted.Data[1].Modality)
		assert.Equal(t, 1, converted.Data[1].Index)
		assert.Equal(t, []float64{0.3, 0.4}, converted.Data[1].Embedding.EmbeddingArray)
		// Nova reports its token count in a response header only, so Usage stays nil
		// here and the provider backfills it from X-Amzn-Bedrock-Input-Token-Count.
		assert.Nil(t, converted.Usage)
	})

	t.Run("a combined audio-video vector is labelled as such", func(t *testing.T) {
		converted, err := (&BedrockNovaEmbeddingResponse{Embeddings: []BedrockNovaEmbedding{
			{Embedding: []float64{0.1}, EmbeddingType: "AUDIO_VIDEO_COMBINED"},
		}}).ToBifrostEmbeddingResponse()
		require.NoError(t, err)
		require.Len(t, converted.Data, 1)
		assert.Equal(t, schemas.EmbeddingModalityAudioVideo, converted.Data[0].Modality)
	})

	t.Run("a label we do not know leaves the modality empty", func(t *testing.T) {
		converted, err := (&BedrockNovaEmbeddingResponse{Embeddings: []BedrockNovaEmbedding{
			{Embedding: []float64{0.1}, EmbeddingType: "SOMETHING_NEW"},
		}}).ToBifrostEmbeddingResponse()
		require.NoError(t, err)
		require.Len(t, converted.Data, 1)
		assert.Empty(t, converted.Data[0].Modality)
		assert.Equal(t, []float64{0.1}, converted.Data[0].Embedding.EmbeddingArray)
	})
}

// The Bedrock integration fronts the native invoke route, so a Nova body written for
// AWS must survive the trip through the canonical schema and come back in its own
// envelope rather than Titan's or Cohere's.
func TestBedrockNovaEmbeddingInvokeRoundTrip(t *testing.T) {
	const novaModel = "amazon.nova-2-multimodal-embeddings-v1:0"

	t.Run("a SINGLE_EMBEDDING body is detected as an embedding request", func(t *testing.T) {
		assert.Equal(t, schemas.EmbeddingRequest, DetectInvokeRequestType(
			[]byte(`{"taskType":"SINGLE_EMBEDDING","singleEmbeddingParams":{"embeddingPurpose":"GENERIC_INDEX","text":{"truncationMode":"END","value":"hello"}}}`),
			novaModel))
	})

	t.Run("the native text body round-trips unchanged", func(t *testing.T) {
		var invokeRequest BedrockInvokeRequest
		require.NoError(t, json.Unmarshal([]byte(`{
			"taskType": "SINGLE_EMBEDDING",
			"singleEmbeddingParams": {
				"embeddingPurpose": "DOCUMENT_RETRIEVAL",
				"embeddingDimension": 1024,
				"text": {"truncationMode": "START", "value": "alpha kestrel harbor"}
			}
		}`), &invokeRequest))
		invokeRequest.ModelID = "bedrock/" + novaModel

		converted, err := invokeRequest.ToBifrostEmbeddingRequest(testBedrockCtx())
		require.NoError(t, err)
		require.Len(t, converted.Input, 1)
		require.NotNil(t, converted.Params.Dimensions)
		assert.Equal(t, 1024, *converted.Params.Dimensions)

		outbound, err := ToBedrockNovaEmbeddingRequest(converted)
		require.NoError(t, err)
		wireBody, err := json.Marshal(outbound)
		require.NoError(t, err)
		assert.JSONEq(t, `{
			"taskType": "SINGLE_EMBEDDING",
			"singleEmbeddingParams": {
				"embeddingPurpose": "DOCUMENT_RETRIEVAL",
				"embeddingDimension": 1024,
				"text": {"truncationMode": "START", "value": "alpha kestrel harbor"}
			}
		}`, string(wireBody))
	})

	t.Run("an s3 video body keeps its source and embeddingMode", func(t *testing.T) {
		var invokeRequest BedrockInvokeRequest
		require.NoError(t, json.Unmarshal([]byte(`{
			"taskType": "SINGLE_EMBEDDING",
			"singleEmbeddingParams": {
				"embeddingPurpose": "GENERIC_INDEX",
				"video": {
					"format": "mp4",
					"embeddingMode": "AUDIO_VIDEO_SEPARATE",
					"source": {"s3Location": {"uri": "s3://my-bucket/clips/counter.mp4"}}
				}
			}
		}`), &invokeRequest))
		invokeRequest.ModelID = "bedrock/" + novaModel

		converted, err := invokeRequest.ToBifrostEmbeddingRequest(testBedrockCtx())
		require.NoError(t, err)
		outbound, err := ToBedrockNovaEmbeddingRequest(converted)
		require.NoError(t, err)
		require.NotNil(t, outbound.SingleEmbeddingParams.Video)
		assert.Equal(t, "mp4", outbound.SingleEmbeddingParams.Video.Format)
		assert.Equal(t, BedrockNovaVideoMode("AUDIO_VIDEO_SEPARATE"), outbound.SingleEmbeddingParams.Video.EmbeddingMode)
		require.NotNil(t, outbound.SingleEmbeddingParams.Video.Source.S3Location)
		assert.Equal(t, "s3://my-bucket/clips/counter.mp4", outbound.SingleEmbeddingParams.Video.Source.S3Location.URI)
	})

	t.Run("the response comes back in Nova's envelope, not Titan's", func(t *testing.T) {
		native, err := ToBedrockEmbeddingInvokeResponse(testBedrockCtx(), &schemas.BifrostEmbeddingResponse{
			Model: novaModel,
			Data: []schemas.EmbeddingData{
				{Index: 0, Object: "embedding", Modality: schemas.EmbeddingModalityAudio,
					Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{0.1, 0.2}}},
				{Index: 1, Object: "embedding", Modality: schemas.EmbeddingModalityVideo,
					Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{0.3, 0.4}}},
			},
		})
		require.NoError(t, err)
		wireBody, err := json.Marshal(native)
		require.NoError(t, err)
		assert.JSONEq(t, `{"embeddings":[
			{"embedding":[0.1,0.2],"embeddingType":"AUDIO"},
			{"embedding":[0.3,0.4],"embeddingType":"VIDEO"}
		]}`, string(wireBody))
	})

	t.Run("a text source Bifrost cannot carry is a caller error", func(t *testing.T) {
		var invokeRequest BedrockInvokeRequest
		require.NoError(t, json.Unmarshal([]byte(`{
			"taskType": "SINGLE_EMBEDDING",
			"singleEmbeddingParams": {
				"embeddingPurpose": "GENERIC_INDEX",
				"text": {"truncationMode": "END", "source": {"s3Location": {"uri": "s3://my-bucket/doc.txt"}}}
			}
		}`), &invokeRequest))
		invokeRequest.ModelID = "bedrock/" + novaModel

		_, err := invokeRequest.ToBifrostEmbeddingRequest(testBedrockCtx())
		require.Error(t, err)
		assert.True(t, providerUtils.IsInvalidRequestError(err))
	})
}

func TestDetermineEmbeddingModelType(t *testing.T) {
	ctx := testBedrockCtx()
	tests := []struct {
		model     string
		modelType string
	}{
		{"amazon.nova-2-multimodal-embeddings-v1:0", "nova"},
		{"amazon.titan-embed-text-v2:0", "titan"},
		{"amazon.titan-embed-image-v1", "titan"},
		{"cohere.embed-v4:0", "cohere"},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			modelType, err := DetermineEmbeddingModelType(ctx, test.model)
			require.NoError(t, err)
			assert.Equal(t, test.modelType, modelType)
		})
	}

	_, err := DetermineEmbeddingModelType(ctx, "meta.llama3-8b-instruct-v1:0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported embedding model")
}

// An empty text value is a 400 here rather than an AWS minLength complaint, and two of
// them are still one modality - the count must not confuse "empty" with "absent".
func TestToBedrockNovaEmbeddingRequestRejectsEmptyText(t *testing.T) {
	for _, name := range []string{"one empty text part", "two empty text parts"} {
		t.Run(name, func(t *testing.T) {
			content := schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("")}}
			if name == "two empty text parts" {
				content = append(content, schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("")})
			}
			_, err := ToBedrockNovaEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
				Model: "amazon.nova-2-multimodal-embeddings-v1:0",
				Input: []schemas.EmbeddingInputItem{{Content: content}},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "text part carries no text")
			assert.True(t, providerUtils.IsInvalidRequestError(err))
		})
	}
}

// detailLevel selects the resolution Nova reads an image at: DOCUMENT_IMAGE is the
// higher one, meant for text on a page. It is image-only - AWS rejects the key on an
// audio part - and AWS defaults it, so an absent value must stay off the wire.
func TestToBedrockNovaEmbeddingRequestDetailLevel(t *testing.T) {
	const novaModel = "amazon.nova-2-multimodal-embeddings-v1:0"
	imagePart := schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeImage,
		Image: &schemas.EmbeddingMediaPart{Data: schemas.Ptr("data:image/png;base64,iVBORw0KGgoAAAANSUhEUg==")}}

	t.Run("absent by default", func(t *testing.T) {
		req, err := ToBedrockNovaEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: novaModel,
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{imagePart}}},
		})
		require.NoError(t, err)
		assert.Nil(t, req.SingleEmbeddingParams.Image.DetailLevel)
		wireBody, err := json.Marshal(req)
		require.NoError(t, err)
		assert.NotContains(t, string(wireBody), "detailLevel")
	})

	t.Run("forwarded onto the image part", func(t *testing.T) {
		req, err := ToBedrockNovaEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model:  novaModel,
			Input:  []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{imagePart}}},
			Params: &schemas.EmbeddingParameters{ExtraParams: map[string]interface{}{"detailLevel": "DOCUMENT_IMAGE"}},
		})
		require.NoError(t, err)
		require.NotNil(t, req.SingleEmbeddingParams.Image.DetailLevel)
		assert.Equal(t, BedrockNovaDetailLevel("DOCUMENT_IMAGE"), *req.SingleEmbeddingParams.Image.DetailLevel)
		assert.NotContains(t, req.ExtraParams, "detailLevel", "consumed, not also forwarded at the top level")
	})

	t.Run("never lands on an audio part, which AWS would refuse", func(t *testing.T) {
		req, err := ToBedrockNovaEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: novaModel,
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeAudio,
					Audio: &schemas.EmbeddingMediaPart{Data: schemas.Ptr("data:audio/mpeg;base64,AA==")}},
			}}},
			Params: &schemas.EmbeddingParameters{ExtraParams: map[string]interface{}{"detailLevel": "DOCUMENT_IMAGE"}},
		})
		require.NoError(t, err)
		wireBody, err := json.Marshal(req)
		require.NoError(t, err)
		assert.NotContains(t, string(wireBody), "detailLevel")
	})

	t.Run("a native detailLevel survives the invoke round trip", func(t *testing.T) {
		var invokeRequest BedrockInvokeRequest
		require.NoError(t, json.Unmarshal([]byte(`{
			"taskType": "SINGLE_EMBEDDING",
			"singleEmbeddingParams": {
				"embeddingPurpose": "DOCUMENT_RETRIEVAL",
				"image": {"format": "png", "detailLevel": "DOCUMENT_IMAGE", "source": {"bytes": "iVBORw0KGgoAAAANSUhEUg=="}}
			}
		}`), &invokeRequest))
		invokeRequest.ModelID = "bedrock/" + novaModel

		converted, err := invokeRequest.ToBifrostEmbeddingRequest(testBedrockCtx())
		require.NoError(t, err)
		outbound, err := ToBedrockNovaEmbeddingRequest(converted)
		require.NoError(t, err)
		wireBody, err := json.Marshal(outbound)
		require.NoError(t, err)
		assert.JSONEq(t, `{
			"taskType": "SINGLE_EMBEDDING",
			"singleEmbeddingParams": {
				"embeddingPurpose": "DOCUMENT_RETRIEVAL",
				"image": {"format": "png", "detailLevel": "DOCUMENT_IMAGE", "source": {"bytes": "iVBORw0KGgoAAAANSUhEUg=="}}
			}
		}`, string(wireBody))
	})
}

// AWS's own reference documents video.format "3gp", which the API rejects in favour of
// "three_gp" (and accepts "mpg" as a synonym for "mpeg"). A native body copied from the
// reference must normalize to the spelling that works, and nothing may ever put "3gp" on
// the wire.
func TestToBedrockNovaEmbeddingRequestNormalizesVideoFormatSpelling(t *testing.T) {
	const novaModel = "amazon.nova-2-multimodal-embeddings-v1:0"
	for _, test := range []struct{ native, wire string }{
		{"3gp", "three_gp"},
		{"three_gp", "three_gp"},
		{"mpg", "mpeg"},
		{"mpeg", "mpeg"},
	} {
		t.Run(test.native, func(t *testing.T) {
			var invokeRequest BedrockInvokeRequest
			require.NoError(t, json.Unmarshal([]byte(`{
				"taskType": "SINGLE_EMBEDDING",
				"singleEmbeddingParams": {
					"embeddingPurpose": "GENERIC_INDEX",
					"video": {"format": "`+test.native+`", "embeddingMode": "AUDIO_VIDEO_COMBINED", "source": {"bytes": "AA=="}}
				}
			}`), &invokeRequest))
			invokeRequest.ModelID = "bedrock/" + novaModel

			converted, err := invokeRequest.ToBifrostEmbeddingRequest(testBedrockCtx())
			require.NoError(t, err)
			outbound, err := ToBedrockNovaEmbeddingRequest(converted)
			require.NoError(t, err)
			assert.Equal(t, test.wire, outbound.SingleEmbeddingParams.Video.Format)
		})
	}

	// The canonical path derives the format itself, so a gs-style extension or a media
	// type both land on three_gp too.
	for _, media := range []*schemas.EmbeddingMediaPart{
		{URL: schemas.Ptr("s3://my-bucket/clips/clip.3gp")},
		{Data: schemas.Ptr("data:video/3gpp;base64,AA==")},
	} {
		req, err := ToBedrockNovaEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: novaModel,
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeVideo, Video: media},
			}}},
		})
		require.NoError(t, err)
		assert.Equal(t, "three_gp", req.SingleEmbeddingParams.Video.Format)
	}
}

// Embedding unmarshals per model family instead of going through HandleProviderResponse, and
// for a long time captured only the response half - and captured it by decoding into a map,
// which reordered its keys. Every error return in the same method reports the request, so
// omitting it on success was an asymmetry rather than a policy.
func TestBedrockEmbeddingCapturesRawRequestAndResponse(t *testing.T) {
	// Deliberately not alphabetical: the request half preserves this order, the response half
	// does not, and the assertions below say so rather than relying on a fixture that hides it.
	const providerResponse = `{"inputTextTokenCount":3,"embedding":[0.5,0.25]}`

	newServer := func(t *testing.T) *httptest.Server {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(providerResponse))
		}))
		t.Cleanup(server.Close)
		return server
	}

	embeddingRequest := func() *schemas.BifrostEmbeddingRequest {
		return &schemas.BifrostEmbeddingRequest{
			Model: "amazon.titan-embed-text-v2:0",
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("hello")},
			}}},
			Params: &schemas.EmbeddingParameters{Dimensions: schemas.Ptr(256)},
		}
	}

	t.Run("both halves are reported when capture is on", func(t *testing.T) {
		provider := newTestProviderWithServer(t, newServer(t))
		ctx := testBedrockCtx()
		ctx.SetValue(schemas.BifrostContextKeyCaptureRawRequest, true)
		ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, true)

		response, bifrostErr := provider.Embedding(ctx, testBedrockKey(), embeddingRequest())
		require.Nil(t, bifrostErr)
		require.NotNil(t, response)

		require.NotNil(t, response.ExtraFields.RawRequest, "the converted provider body must be reported on success, as it already is on every error return")
		rawRequest, err := json.Marshal(response.ExtraFields.RawRequest)
		require.NoError(t, err)
		// inputText before dimensions: the order ToBedrockTitanEmbeddingRequest emits, which a
		// map decode would sort the other way.
		assert.Equal(t, `{"inputText":"hello","dimensions":256}`, string(rawRequest))

		// The response half goes through sonic.Unmarshal into interface{} - the idiom the other
		// hand-rolled handlers use - so it is semantically equal but not byte-equal: that decode
		// sorts keys. Asserted as JSONEq rather than Equal so the test states what actually holds.
		require.NotNil(t, response.ExtraFields.RawResponse)
		rawResponse, err := json.Marshal(response.ExtraFields.RawResponse)
		require.NoError(t, err)
		assert.JSONEq(t, providerResponse, string(rawResponse), "the provider's payload must survive")
	})

	t.Run("each half stays out when its flag is off", func(t *testing.T) {
		for _, test := range []struct {
			name                      string
			captureRequest            bool
			captureResponse           bool
			wantRequest, wantResponse bool
		}{
			{"neither", false, false, false, false},
			{"request only", true, false, true, false},
			{"response only", false, true, false, true},
		} {
			t.Run(test.name, func(t *testing.T) {
				provider := newTestProviderWithServer(t, newServer(t))
				ctx := testBedrockCtx()
				ctx.SetValue(schemas.BifrostContextKeyCaptureRawRequest, test.captureRequest)
				ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, test.captureResponse)

				response, bifrostErr := provider.Embedding(ctx, testBedrockKey(), embeddingRequest())
				require.Nil(t, bifrostErr)
				require.NotNil(t, response)
				assert.Equal(t, test.wantRequest, response.ExtraFields.RawRequest != nil, "raw request presence")
				assert.Equal(t, test.wantResponse, response.ExtraFields.RawResponse != nil, "raw response presence")
			})
		}
	})

	// Nova and Cohere unmarshal down different branches of the same switch, so the capture -
	// which sits after it - has to hold for all three families, not just Titan's.
	t.Run("the capture is family-independent", func(t *testing.T) {
		for _, test := range []struct{ model, response string }{
			{"cohere.embed-english-v3", `{"embeddings":[[0.5]],"id":"x","response_type":"embeddings_floats"}`},
			{"amazon.nova-2-multimodal-embeddings-v1:0", `{"embeddings":[{"embedding":[0.5],"embeddingType":"TEXT"}]}`},
		} {
			t.Run(test.model, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.ReadAll(r.Body)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(test.response))
				}))
				defer server.Close()

				provider := newTestProviderWithServer(t, server)
				ctx := testBedrockCtx()
				ctx.SetValue(schemas.BifrostContextKeyCaptureRawRequest, true)
				ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, true)

				request := embeddingRequest()
				request.Model = test.model
				request.Params = nil
				response, bifrostErr := provider.Embedding(ctx, testBedrockKey(), request)
				require.Nil(t, bifrostErr)
				require.NotNil(t, response)
				assert.NotNil(t, response.ExtraFields.RawRequest, "raw request missing for %s", test.model)

				rawResponse, err := json.Marshal(response.ExtraFields.RawResponse)
				require.NoError(t, err)
				assert.JSONEq(t, test.response, string(rawResponse), "provider payload altered for %s", test.model)
			})
		}
	})
}
