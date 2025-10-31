package cohere

import (
	"net/url"
	"strconv"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func ToCohereListModelsURL(request *schemas.BifrostListModelsRequest, baseURL string) string {
	pageSize := request.PageSize
	if pageSize <= 0 {
		pageSize = schemas.DefaultPageSize
	}

	// Build query parameters
	params := url.Values{}
	params.Set("page_size", strconv.Itoa(pageSize))

	if request.PageToken != "" {
		params.Set("page_token", request.PageToken)
	}

	if request.ExtraParams != nil {
		if endpoint, ok := request.ExtraParams["endpoint"].(string); ok && endpoint != "" {
			params.Set("endpoint", endpoint)
		}
		if defaultOnly, ok := request.ExtraParams["default_only"].(bool); ok && defaultOnly {
			params.Set("default_only", "true")
		}
	}

	return baseURL + "?" + params.Encode()
}

func (response *CohereListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data:          make([]schemas.Model, 0, len(response.Models)),
		NextPageToken: response.NextPageToken,
	}

	for _, model := range response.Models {
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:               string(providerKey) + "/" + model.Name,
			Name:             schemas.Ptr(model.Name),
			ContextLength:    schemas.Ptr(int(model.ContextLength)),
			SupportedMethods: model.Endpoints,
		})
	}

	return bifrostResponse
}
