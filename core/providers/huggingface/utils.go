package huggingface

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// buildRequestURL composes the final request URL based on context overrides.
func (provider *HuggingFaceProvider) buildRequestURL(ctx context.Context, defaultPath string, requestType schemas.RequestType) string {
	return provider.networkConfig.BaseURL + providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)
}

func (provider *HuggingFaceProvider) buildAuthHeader(key schemas.Key) map[string]string {
	if key.Value == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + key.Value}
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
			values.Set(key, strconv.FormatFloat(typed, 'f', -1, 64))
		case bool:
			values.Set(key, strconv.FormatBool(typed))
		default:
			values.Set(key, fmt.Sprintf("%v", typed))
		}
	}

	return fmt.Sprintf("%s/api/models?%s", modelHubBaseURL, values.Encode())
}
