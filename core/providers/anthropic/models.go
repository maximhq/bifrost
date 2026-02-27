package anthropic

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func (response *AnthropicListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels []string, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data:    make([]schemas.Model, 0, len(response.Data)),
		FirstID: response.FirstID,
		LastID:  response.LastID,
		HasMore: schemas.Ptr(response.HasMore),
	}

	// Map Anthropic's cursor-based pagination to Bifrost's token-based pagination
	// If there are more results, set next_page_token to last_id so it can be used in the next request
	if response.HasMore && response.LastID != nil {
		bifrostResponse.NextPageToken = *response.LastID
	}

	includedModels := make(map[string]bool)
	for _, model := range response.Data {
		modelID := model.ID
		if !unfiltered && len(allowedModels) > 0 {
			allowed := false
			for _, allowedModel := range allowedModels {
				if schemas.SameBaseModel(model.ID, allowedModel) {
					modelID = allowedModel
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:      string(providerKey) + "/" + modelID,
			Name:    schemas.Ptr(model.DisplayName),
			Created: schemas.Ptr(model.CreatedAt.Unix()),
		})
		includedModels[modelID] = true
	}

	// Backfill allowed models that were not in the response
	if !unfiltered && len(allowedModels) > 0 {
		for _, allowedModel := range allowedModels {
			if !includedModels[allowedModel] {
				bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
					ID:   string(providerKey) + "/" + allowedModel,
					Name: schemas.Ptr(allowedModel),
				})
			}
		}
	}

	return bifrostResponse
}

func ToAnthropicListModelsResponse(response *schemas.BifrostListModelsResponse) *AnthropicListModelsResponse {
	if response == nil {
		return nil
	}

	anthropicResponse := &AnthropicListModelsResponse{
		Data: make([]AnthropicModel, 0, len(response.Data)),
	}
	if response.FirstID != nil {
		anthropicResponse.FirstID = response.FirstID
	}
	if response.LastID != nil {
		anthropicResponse.LastID = response.LastID
	}

	for _, model := range response.Data {
		anthropicModel := AnthropicModel{
			ID: model.ID,
		}
		if model.Name != nil {
			anthropicModel.DisplayName = *model.Name
		}
		if model.Created != nil {
			anthropicModel.CreatedAt = time.Unix(*model.Created, 0)
		}
		anthropicResponse.Data = append(anthropicResponse.Data, anthropicModel)
	}

	return anthropicResponse
}
