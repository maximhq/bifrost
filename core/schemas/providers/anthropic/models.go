package anthropic

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func (response *AnthropicListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider) *schemas.BifrostListModelsResponse {
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

	for _, model := range response.Data {
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:      string(providerKey) + "/" + model.ID,
			Name:    schemas.Ptr(model.DisplayName),
			Created: schemas.Ptr(model.CreatedAt.Unix()),
		})
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
