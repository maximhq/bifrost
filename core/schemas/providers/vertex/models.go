package vertex

import "github.com/maximhq/bifrost/core/schemas"

func (response *VertexListModelsResponse) ToBifrostListModelsResponse() *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data:          make([]schemas.Model, 0, len(response.Models)),
	}

	for _, model := range response.Models {
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:          string(schemas.Vertex) + "/" + model.Name,
			Name:        schemas.Ptr(model.DisplayName),
			Description: schemas.Ptr(model.Description),
			Created:     schemas.Ptr(model.VersionCreateTime.Unix()),
		})
	}

	return bifrostResponse
}
