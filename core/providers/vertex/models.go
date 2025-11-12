package vertex

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

func (response *VertexListModelsResponse) ToBifrostListModelsResponse() *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Models)),
	}

	for _, model := range response.Models {
		if len(model.DeployedModels) == 0 {
			continue
		}
		for _, deployedModel := range model.DeployedModels {
			endpoint := strings.TrimSuffix(deployedModel.Endpoint, "/")
			parts := strings.Split(endpoint, "/")
			if len(parts) == 0 {
				continue
			}
			customModelID := parts[len(parts)-1]
			if customModelID == "" {
				continue
			}
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
				ID:          string(schemas.Vertex) + "/" + customModelID,
				Name:        schemas.Ptr(model.DisplayName),
				Description: schemas.Ptr(model.Description),
				Created:     schemas.Ptr(model.VersionCreateTime.Unix()),
			})
		}
	}
	bifrostResponse.NextPageToken = response.NextPageToken

	return bifrostResponse
}
