package integrations

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/providers/cohere"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateCohereRouteConfigsIncludesRerank(t *testing.T) {
	routes := CreateCohereRouteConfigs("/cohere")

	assert.Len(t, routes, 4, "should have 4 cohere routes")

	var rerankRoute *RouteConfig
	for i := range routes {
		if routes[i].Path == "/cohere/v2/rerank" && routes[i].Method == "POST" {
			rerankRoute = &routes[i]
			break
		}
	}

	require.NotNil(t, rerankRoute, "rerank route should exist")
	assert.Equal(t, RouteConfigTypeCohere, rerankRoute.Type)
	assert.NotNil(t, rerankRoute.GetHTTPRequestType)
	assert.Equal(t, schemas.RerankRequest, rerankRoute.GetHTTPRequestType(nil))
	assert.NotNil(t, rerankRoute.GetRequestTypeInstance)
	assert.NotNil(t, rerankRoute.RequestConverter)
	assert.NotNil(t, rerankRoute.RerankResponseConverter)
	assert.NotNil(t, rerankRoute.ErrorConverter)

	reqInstance := rerankRoute.GetRequestTypeInstance(context.Background())
	_, ok := reqInstance.(*cohere.CohereRerankRequest)
	assert.True(t, ok, "rerank request instance should be CohereRerankRequest")
}

func TestCohereRerankRouteRequestConverter(t *testing.T) {
	routes := CreateCohereRouteConfigs("/cohere")

	var rerankRoute *RouteConfig
	for i := range routes {
		if routes[i].Path == "/cohere/v2/rerank" {
			rerankRoute = &routes[i]
			break
		}
	}
	require.NotNil(t, rerankRoute)
	require.NotNil(t, rerankRoute.RequestConverter)

	topN := 1
	req := &cohere.CohereRerankRequest{
		Model:     "rerank-v3.5",
		Query:     "what is bifrost?",
		Documents: []string{"doc1", "doc2"},
		TopN:      &topN,
	}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostReq, err := rerankRoute.RequestConverter(bifrostCtx, req)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.RerankRequest)

	assert.Equal(t, schemas.Cohere, bifrostReq.RerankRequest.Provider)
	assert.Equal(t, "rerank-v3.5", bifrostReq.RerankRequest.Model)
	assert.Equal(t, "what is bifrost?", bifrostReq.RerankRequest.Query)
	require.Len(t, bifrostReq.RerankRequest.Documents, 2)
	assert.Equal(t, "doc1", bifrostReq.RerankRequest.Documents[0].Text)
	assert.Equal(t, "doc2", bifrostReq.RerankRequest.Documents[1].Text)
	require.NotNil(t, bifrostReq.RerankRequest.Params)
	require.NotNil(t, bifrostReq.RerankRequest.Params.TopN)
	assert.Equal(t, 1, *bifrostReq.RerankRequest.Params.TopN)
}

func TestCohereRerankResponseConverterUsesRawResponse(t *testing.T) {
	routes := CreateCohereRouteConfigs("/cohere")

	var rerankRoute *RouteConfig
	for i := range routes {
		if routes[i].Path == "/cohere/v2/rerank" {
			rerankRoute = &routes[i]
			break
		}
	}
	require.NotNil(t, rerankRoute)
	require.NotNil(t, rerankRoute.RerankResponseConverter)

	raw := map[string]interface{}{"id": "r-123", "results": []interface{}{}}
	resp := &schemas.BifrostRerankResponse{
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    schemas.Cohere,
			RawResponse: raw,
		},
	}

	converted, err := rerankRoute.RerankResponseConverter(nil, resp)
	require.NoError(t, err)
	assert.Equal(t, raw, converted)
}
