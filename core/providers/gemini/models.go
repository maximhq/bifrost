package gemini

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

func ToGeminiListModelsURL(request *schemas.BifrostListModelsRequest, baseURL string) string {
	// Add limit parameter (default to 1000)
	pageSize := request.PageSize
	if pageSize <= 0 {
		pageSize = schemas.DefaultPageSize
	}	

	// Build query parameters
	params := url.Values{}
	params.Set("pageSize", strconv.Itoa(pageSize))

	if request.PageToken != "" {
		params.Set("pageToken", request.PageToken)
	}

	return baseURL + "?" + params.Encode()
}

func (response *GeminiListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data:          make([]schemas.Model, 0, len(response.Models)),
		NextPageToken: response.NextPageToken,
	}

	for _, model := range response.Models {
		contextLength := model.InputTokenLimit + model.OutputTokenLimit
		// Remove prefix models/ from model.Name
		modelName := strings.TrimPrefix(model.Name, "models/")
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:               string(providerKey) + "/" + modelName,
			Name:             schemas.Ptr(model.DisplayName),
			Description:      schemas.Ptr(model.Description),
			ContextLength:    schemas.Ptr(int(contextLength)),
			SupportedMethods: model.SupportedGenerationMethods,
		})
	}

	return bifrostResponse
}

func ToGeminiListModelsResponse(resp *schemas.BifrostListModelsResponse) *GeminiListModelsResponse {
	if resp == nil {
		return nil
	}

	geminiResponse := &GeminiListModelsResponse{
		Models: make([]GeminiModel, 0, len(resp.Data)),
		NextPageToken: resp.NextPageToken,
	}

	for _, model := range resp.Data {
		geminiModel := GeminiModel{
			Name:                       model.ID,
			SupportedGenerationMethods: model.SupportedMethods,
		}
		if model.Name != nil {
			geminiModel.DisplayName = *model.Name
		}
		if model.Description != nil {
			geminiModel.Description = *model.Description
		}

		geminiResponse.Models = append(geminiResponse.Models, geminiModel)
	}

	return geminiResponse
}
