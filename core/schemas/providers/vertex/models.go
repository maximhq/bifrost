package vertex

import (
	"net/url"
	"strconv"

	"github.com/maximhq/bifrost/core/schemas"
)

func ToVertexListModelsURL(request *schemas.BifrostListModelsRequest, baseURL string) string {
	// Add limit parameter (default to 100 for Vertex)
	pageSize := request.PageSize
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}

	// Build query parameters
	params := url.Values{}
	params.Set("pageSize", strconv.Itoa(pageSize))

	if request.PageToken != "" {
		params.Set("pageToken", request.PageToken)
	}

	return baseURL + "?" + params.Encode()
}

func (response *VertexListModelsResponse) ToBifrostListModelsResponse() *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data:          make([]schemas.Model, 0, len(response.Models)),
		NextPageToken: response.NextPageToken,
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
