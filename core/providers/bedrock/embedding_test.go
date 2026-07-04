package bedrock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/bytedance/sonic"
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

type titanEmbeddingCaptureTransport struct {
	mu       sync.Mutex
	requests []BedrockTitanEmbeddingRequest
	paths    []string

	failInput               string
	failStatus              int
	releaseFailureOnSuccess chan struct{}
	releaseFailureOnce      sync.Once
}

func (t *titanEmbeddingCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var titanReq BedrockTitanEmbeddingRequest
	if err := sonic.Unmarshal(body, &titanReq); err != nil {
		return nil, err
	}

	t.mu.Lock()
	t.requests = append(t.requests, titanReq)
	t.paths = append(t.paths, req.URL.Path)
	t.mu.Unlock()

	if titanReq.InputText == t.failInput {
		if t.releaseFailureOnSuccess != nil {
			<-t.releaseFailureOnSuccess
		}
		status := t.failStatus
		if status == 0 {
			status = http.StatusInternalServerError
		}
		respBody, err := sonic.Marshal(BedrockError{
			Type:    "InternalServerException",
			Message: "forced titan embedding failure",
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(bytes.NewReader(respBody)),
		}, nil
	}

	embeddingValueByInput := map[string]float64{
		"alpha":   3,
		"bravo":   2,
		"charlie": 1,
	}
	tokenCountByInput := map[string]int{
		"alpha":   11,
		"bravo":   13,
		"charlie": 17,
	}
	respBody, err := sonic.Marshal(BedrockTitanEmbeddingResponse{
		Embedding:           []float64{embeddingValueByInput[titanReq.InputText]},
		InputTextTokenCount: tokenCountByInput[titanReq.InputText],
	})
	if err != nil {
		return nil, err
	}

	if t.releaseFailureOnSuccess != nil {
		t.releaseFailureOnce.Do(func() {
			close(t.releaseFailureOnSuccess)
		})
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Test": []string{titanReq.InputText}},
		Body:       io.NopCloser(bytes.NewReader(respBody)),
	}, nil
}

func (t *titanEmbeddingCaptureTransport) captured() ([]BedrockTitanEmbeddingRequest, []string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	requests := append([]BedrockTitanEmbeddingRequest(nil), t.requests...)
	paths := append([]string(nil), t.paths...)
	return requests, paths
}

func TestBedrockTitanEmbeddingBatchFansOutAndAggregates(t *testing.T) {
	transport := &titanEmbeddingCaptureTransport{}
	provider := &BedrockProvider{
		client: &http.Client{Transport: transport},
	}
	normalize := true
	dimensions := 256
	request := &schemas.BifrostEmbeddingRequest{
		Model: "amazon.titan-embed-text-v2:0",
		Input: &schemas.EmbeddingInput{Texts: []string{"alpha", "bravo", "charlie"}},
		Params: &schemas.EmbeddingParameters{
			Dimensions: &dimensions,
			ExtraParams: map[string]interface{}{
				"normalize": normalize,
			},
		},
	}
	key := schemas.Key{
		Value: *schemas.NewSecretVar("bedrock-api-key"),
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region: schemas.NewSecretVar("us-west-2"),
		},
	}

	response, bifrostErr := provider.Embedding(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, request)
	require.Nil(t, bifrostErr)
	require.NotNil(t, response)

	require.Len(t, response.Data, 3)
	assert.Equal(t, "list", response.Object)
	assert.Equal(t, request.Model, response.Model)
	assert.Equal(t, 41, response.Usage.PromptTokens)
	assert.Equal(t, 41, response.Usage.TotalTokens)
	assert.Equal(t, 0, response.Data[0].Index)
	assert.Equal(t, []float64{3}, response.Data[0].Embedding.EmbeddingArray)
	assert.Equal(t, 1, response.Data[1].Index)
	assert.Equal(t, []float64{2}, response.Data[1].Embedding.EmbeddingArray)
	assert.Equal(t, 2, response.Data[2].Index)
	assert.Equal(t, []float64{1}, response.Data[2].Embedding.EmbeddingArray)

	capturedRequests, paths := transport.captured()
	require.Len(t, capturedRequests, 3)
	assert.Len(t, paths, 3)
	inputs := make(map[string]bool, len(capturedRequests))
	for _, capturedRequest := range capturedRequests {
		inputs[capturedRequest.InputText] = true
		require.NotNil(t, capturedRequest.Dimensions)
		assert.Equal(t, dimensions, *capturedRequest.Dimensions)
		require.NotNil(t, capturedRequest.Normalize)
		assert.Equal(t, normalize, *capturedRequest.Normalize)
		assert.NotContains(t, capturedRequest.InputText, "\n")
	}
	assert.Equal(t, map[string]bool{"alpha": true, "bravo": true, "charlie": true}, inputs)
	for _, path := range paths {
		assert.Equal(t, "/model/amazon.titan-embed-text-v2:0/invoke", path)
	}
}

func TestBedrockTitanEmbeddingSingleTextUsesNonBatchPath(t *testing.T) {
	transport := &titanEmbeddingCaptureTransport{}
	provider := &BedrockProvider{
		client: &http.Client{Transport: transport},
	}
	text := "alpha"
	request := &schemas.BifrostEmbeddingRequest{
		Model: "amazon.titan-embed-text-v2:0",
		Input: &schemas.EmbeddingInput{Text: &text},
	}
	key := schemas.Key{
		Value: *schemas.NewSecretVar("bedrock-api-key"),
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region: schemas.NewSecretVar("us-west-2"),
		},
	}

	response, bifrostErr := provider.Embedding(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, request)
	require.Nil(t, bifrostErr)
	require.NotNil(t, response)

	require.Len(t, response.Data, 1)
	assert.Equal(t, request.Model, response.Model)
	assert.Equal(t, 0, response.Data[0].Index)
	assert.Equal(t, []float64{3}, response.Data[0].Embedding.EmbeddingArray)
	assert.Equal(t, 11, response.Usage.PromptTokens)
	assert.Equal(t, 11, response.Usage.TotalTokens)

	capturedRequests, paths := transport.captured()
	require.Len(t, capturedRequests, 1)
	assert.Equal(t, "alpha", capturedRequests[0].InputText)
	assert.Equal(t, []string{"/model/amazon.titan-embed-text-v2:0/invoke"}, paths)
}

func TestBedrockTitanEmbeddingBatchReturnsFirstRealErrorAndHeaders(t *testing.T) {
	transport := &titanEmbeddingCaptureTransport{
		failInput:               "bravo",
		releaseFailureOnSuccess: make(chan struct{}),
	}
	provider := &BedrockProvider{
		client: &http.Client{Transport: transport},
	}
	request := &schemas.BifrostEmbeddingRequest{
		Model: "amazon.titan-embed-text-v2:0",
		Input: &schemas.EmbeddingInput{Texts: []string{"alpha", "bravo"}},
	}
	key := schemas.Key{
		Value: *schemas.NewSecretVar("bedrock-api-key"),
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region: schemas.NewSecretVar("us-west-2"),
		},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	response, bifrostErr := provider.Embedding(ctx, key, request)
	require.Nil(t, response)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, http.StatusInternalServerError, *bifrostErr.StatusCode)
	assert.Equal(t, "forced titan embedding failure", bifrostErr.Error.Message)
	if bifrostErr.Error.Error != nil {
		assert.False(t, errors.Is(bifrostErr.Error.Error, context.Canceled))
	}

	headers, ok := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string)
	require.True(t, ok)
	assert.Equal(t, "alpha", headers["X-Test"])
}
