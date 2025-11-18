package huggingface

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	defaultModelFetchLimit = 200
	maxModelFetchLimit     = 1000
)

// toHubEntry converts an apiModelEntry to the internal huggingFaceModelHubEntry format
func (a apiModelEntry) toHubEntry() huggingFaceModelHubEntry {
	gated := false
	switch v := a.Gated.(type) {
	case bool:
		gated = v
	case string:
		gated = v != "" && v != "false"
	}

	return huggingFaceModelHubEntry{
		ID:          a.ID,
		ModelID:     a.ModelID,
		Author:      a.Author,
		PipelineTag: a.PipelineTag,
		Tags:        a.Tags,
		Private:     a.Private,
		Gated:       gated,
		CardData:    huggingFaceModelCardData{}, // API doesn't return cardData in list endpoint
	}
}

// ListModels queries the Hugging Face model hub API to list models served by the inference provider.
func (provider *HuggingFaceProvider) ListModels(ctx context.Context, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}

	if request == nil {
		request = &schemas.BifrostListModelsRequest{}
	}
	request.Provider = provider.GetProviderKey()

	// Use the model hub API with inference_provider parameter
	if len(keys) == 0 {
		response, err := provider.listModelsByKey(ctx, schemas.Key{}, request)
		if err != nil {
			return nil, err
		}
		return response.ApplyPagination(request.PageSize, request.PageToken), nil
	}

	wrapper := func(ctx context.Context, key schemas.Key, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
		return provider.listModelsByKey(ctx, key, req)
	}

	return providerUtils.HandleMultipleListModelsRequests(ctx, keys, request, wrapper, provider.logger)
}

func (provider *HuggingFaceProvider) listModelsByKey(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.buildModelHubURL(request))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	if key.Value != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value))
	}

	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, provider.handleModelHubError(resp)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, provider.GetProviderKey())
	}

	// Log response preview for debugging
	responsePreview := string(body)
	if len(responsePreview) > 500 {
		responsePreview = responsePreview[:500] + "..."
	}
	provider.logger.Debug("received Hugging Face API response", "provider", string(provider.GetProviderKey()), "url", req.URI().String(), "response_preview", responsePreview)

	// First unmarshal to apiModelEntry which matches the actual API response
	var apiResponse []apiModelEntry
	if err := sonic.Unmarshal(body, &apiResponse); err != nil {
		// Log full response body on unmarshal error for debugging
		responseBody := string(body)
		if len(responseBody) > 1000 {
			responseBody = responseBody[:1000] + "..."
		}
		provider.logger.Error("failed to unmarshal Hugging Face API response", "provider", string(provider.GetProviderKey()), "error", err.Error(), "response_body", responseBody)
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, provider.GetProviderKey())
	}

	// Convert to internal format
	hubResponse := make([]huggingFaceModelHubEntry, 0, len(apiResponse))
	for _, apiEntry := range apiResponse {
		hubResponse = append(hubResponse, apiEntry.toHubEntry())
	}

	provider.logger.Debug("successfully unmarshaled Hugging Face API response", "provider", string(provider.GetProviderKey()), "models_count", len(hubResponse))

	response := provider.buildListModelsResponse(hubResponse)
	response.ExtraFields.Latency = latency.Milliseconds()
	response.NextPageToken = provider.extractCursor(resp)

	return response, nil
}

func (provider *HuggingFaceProvider) extractCursor(resp *fasthttp.Response) string {
	if cursor := resp.Header.Peek("X-Next-Page"); len(cursor) > 0 {
		return string(cursor)
	}
	return ""
}

func (provider *HuggingFaceProvider) buildModelHubURL(request *schemas.BifrostListModelsRequest) string {
	values := url.Values{}

	// Add inference_provider parameter to filter models served by Hugging Face's inference provider
	// According to https://huggingface.co/docs/inference-providers/hub-api
	values.Set("inference_provider", "hf-inference")

	limit := request.PageSize
	if limit <= 0 {
		limit = defaultModelFetchLimit
	}
	if limit > maxModelFetchLimit {
		limit = maxModelFetchLimit
	}
	values.Set("limit", strconv.Itoa(limit))
	if cursor := strings.TrimSpace(request.PageToken); cursor != "" {
		values.Set("cursor", cursor)
	}
	values.Set("full", "1")
	values.Set("sort", "likes")
	values.Set("direction", "-1")

	for key, value := range request.ExtraParams {
		switch typed := value.(type) {
		case string:
			if typed != "" {
				values.Set(key, typed)
			}
		case fmt.Stringer:
			values.Set(key, typed.String())
		case int:
			values.Set(key, strconv.Itoa(typed))
		case float64:
			values.Set(key, strconv.Itoa(int(typed)))
		case bool:
			values.Set(key, strconv.FormatBool(typed))
		default:
			values.Set(key, fmt.Sprintf("%v", typed))
		}
	}

	return fmt.Sprintf("%s/api/models?%s", modelHubBaseURL, values.Encode())
}

func (provider *HuggingFaceProvider) handleModelHubError(resp *fasthttp.Response) *schemas.BifrostError {
	var apiError huggingFaceErrorResponse
	_ = sonic.Unmarshal(resp.Body(), &apiError)

	statusCode := resp.StatusCode()
	message := strings.TrimSpace(apiError.Message)
	if message == "" {
		message = strings.TrimSpace(apiError.Error)
	}

	if message == "" {
		message = fmt.Sprintf("unexpected Hugging Face response: %s", string(resp.Body()))
	}

	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: message,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider:    provider.GetProviderKey(),
			RequestType: schemas.ListModelsRequest,
		},
	}
}

func (provider *HuggingFaceProvider) buildListModelsResponse(entries []huggingFaceModelHubEntry) *schemas.BifrostListModelsResponse {
	models := convertHubEntriesToModels(entries, provider.GetProviderKey())
	return &schemas.BifrostListModelsResponse{
		Data: models,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    provider.GetProviderKey(),
			RequestType: schemas.ListModelsRequest,
		},
	}
}
