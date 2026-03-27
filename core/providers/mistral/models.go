package mistral

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

func (response *MistralListModelsResponse) ToBifrostListModelsResponse(allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Data)),
	}

	if !unfiltered && (allowedModels.IsEmpty() || blacklistedModels.IsBlockAll()) {
		return bifrostResponse
	}

	includedModels := make(map[string]bool)
	for _, model := range response.Data {
		if !unfiltered && allowedModels.IsRestricted() && !allowedModels.Contains(model.ID) {
			continue
		}
		if !unfiltered && blacklistedModels.IsBlocked(model.ID) {
			continue
		}
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:            string(schemas.Mistral) + "/" + model.ID,
			Name:          schemas.Ptr(model.Name),
			Description:   schemas.Ptr(model.Description),
			Created:       schemas.Ptr(model.Created),
			ContextLength: schemas.Ptr(int(model.MaxContextLength)),
			OwnedBy:       schemas.Ptr(model.OwnedBy),
		})
		includedModels[strings.ToLower(model.ID)] = true
	}

	// Backfill allowed models that were not in the response
	if !unfiltered && allowedModels.IsRestricted() {
		for _, allowedModel := range allowedModels {
			if blacklistedModels.IsBlocked(allowedModel) {
				continue
			}
			if !includedModels[strings.ToLower(allowedModel)] {
				bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
					ID:   string(schemas.Mistral) + "/" + allowedModel,
					Name: schemas.Ptr(allowedModel),
				})
				includedModels[strings.ToLower(allowedModel)] = true
			}
		}
	}

	return bifrostResponse
}
