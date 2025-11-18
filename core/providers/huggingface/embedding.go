package huggingface

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Embedding handles embedding requests using HuggingFace's feature extraction API.
// Reference: https://huggingface.co/docs/inference-providers/en/tasks/feature-extraction
func (provider *HuggingFaceProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}

	effectiveRequest, resolvedModel := provider.prepareEmbeddingRequest(request, key)

	// Build the URL for feature extraction endpoint
	// Format: https://router.huggingface.co/hf-inference/models/{model}/pipeline/feature-extraction
	// Note: The base URL includes /v1 for chat/embeddings, but feature extraction doesn't use /v1
	baseURL := provider.networkConfig.BaseURL
	// Remove /v1 suffix if present
	baseURL = strings.TrimSuffix(baseURL, "/v1")

	url := fmt.Sprintf("%s/hf-inference/models/%s/pipeline/feature-extraction",
		baseURL,
		resolvedModel)

	// Create fasthttp request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	// Convert Bifrost request to HuggingFace format
	hfRequest := convertToHuggingFaceEmbeddingRequest(effectiveRequest)

	// Serialize request body
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return hfRequest, nil },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req.SetBody(jsonData)

	// Make the request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", provider.GetProviderKey(), string(resp.Body())))
		return nil, parseHuggingFaceEmbeddingError(resp, request.Model)
	}

	// Decode response body
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, provider.GetProviderKey())
	}

	// Parse HuggingFace response
	hfResponse := &huggingFaceEmbeddingResponse{}
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, hfResponse, provider.shouldSendRawResponse(ctx))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost format
	response := convertFromHuggingFaceEmbeddingResponse(hfResponse, request)

	// Set metadata
	response.Model = resolvedModel
	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.RequestType = schemas.EmbeddingRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	if provider.shouldSendRawResponse(ctx) {
		response.ExtraFields.RawResponse = rawResponse
	}

	provider.decorateResponseMetadata(&response.ExtraFields, request.Model, resolvedModel)

	return response, nil
}

// convertToHuggingFaceEmbeddingRequest converts Bifrost embedding request to HuggingFace format
func convertToHuggingFaceEmbeddingRequest(request *schemas.BifrostEmbeddingRequest) *huggingFaceEmbeddingRequest {
	hfReq := &huggingFaceEmbeddingRequest{}

	// Map input - HuggingFace uses "inputs" field
	if request.Input != nil {
		if request.Input.Text != nil {
			hfReq.Inputs = *request.Input.Text
		} else if request.Input.Texts != nil {
			hfReq.Inputs = request.Input.Texts
		}
	}

	// Map parameters if available
	if request.Params != nil {
		// HuggingFace supports normalize, prompt_name, truncate, truncation_direction
		// These would need to be added to BifrostEmbeddingRequest.Params if needed
	}

	return hfReq
}

// convertFromHuggingFaceEmbeddingResponse converts HuggingFace response to Bifrost format
func convertFromHuggingFaceEmbeddingResponse(hfResponse *huggingFaceEmbeddingResponse, request *schemas.BifrostEmbeddingRequest) *schemas.BifrostEmbeddingResponse {
	response := &schemas.BifrostEmbeddingResponse{
		Object: "list",
		Data:   make([]schemas.EmbeddingData, 0),
	}

	if hfResponse == nil || len(*hfResponse) == 0 {
		return response
	}

	// HuggingFace returns [][]float32 - array of embeddings
	embeddings := *hfResponse

	// Create EmbeddingData entries
	for i, embedding := range embeddings {
		data := schemas.EmbeddingData{
			Object: "embedding",
			Index:  i,
			Embedding: schemas.EmbeddingStruct{
				EmbeddingArray: embedding,
			},
		}
		response.Data = append(response.Data, data)
	}

	// Calculate token usage (HuggingFace doesn't provide usage info)
	// We can estimate based on input
	totalTokens := estimateTokens(request)
	response.Usage = &schemas.BifrostLLMUsage{
		PromptTokens: totalTokens,
		TotalTokens:  totalTokens,
	}

	return response
}

// estimateTokens provides a rough estimate of tokens for usage tracking
func estimateTokens(request *schemas.BifrostEmbeddingRequest) int {
	if request.Input == nil {
		return 0
	}

	count := 0
	if request.Input.Text != nil {
		// Rough estimate: ~4 characters per token
		count = len(*request.Input.Text) / 4
	} else if request.Input.Texts != nil {
		for _, text := range request.Input.Texts {
			count += len(text) / 4
		}
	}

	if count == 0 {
		count = 1
	}
	return count
}

// parseHuggingFaceEmbeddingError parses error responses from HuggingFace embedding API
func parseHuggingFaceEmbeddingError(resp *fasthttp.Response, model string) *schemas.BifrostError {
	errorResp := &huggingFaceErrorResponse{}
	bifrostErr := providerUtils.HandleProviderAPIError(resp, errorResp)

	// Enhance error message if we parsed HuggingFace error structure
	if errorResp.Error != "" || errorResp.Message != "" {
		message := errorResp.Error
		if message == "" {
			message = errorResp.Message
		}
		bifrostErr.Error = &schemas.ErrorField{
			Message: message,
		}
	}

	// Set additional metadata
	bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
		Provider:       schemas.HuggingFace,
		ModelRequested: model,
		RequestType:    schemas.EmbeddingRequest,
	}

	return bifrostErr
}
