package integrations

import (
	"context"
	"testing"

	"github.com/bytedance/sonic"
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
		Documents: []cohere.CohereRerankDocument{{Text: "doc1"}, {Text: "doc2"}},
		TopN:      &topN,
	}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostReq, err := rerankRoute.RequestConverter(bifrostCtx, req)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.RerankRequest)

	// Provider resolution is deferred to the modelcatalogresolver plugin layer,
	// so the converter leaves it empty for an unprefixed model string.
	assert.Equal(t, schemas.ModelProvider(""), bifrostReq.RerankRequest.Provider)
	assert.Equal(t, "rerank-v3.5", bifrostReq.RerankRequest.Model)
	assert.Equal(t, "what is bifrost?", bifrostReq.RerankRequest.Query)
	require.Len(t, bifrostReq.RerankRequest.Documents, 2)
	assert.Equal(t, "doc1", bifrostReq.RerankRequest.Documents[0].Text)
	assert.Equal(t, "doc2", bifrostReq.RerankRequest.Documents[1].Text)
	require.NotNil(t, bifrostReq.RerankRequest.Params)
	require.NotNil(t, bifrostReq.RerankRequest.Params.TopN)
	assert.Equal(t, 1, *bifrostReq.RerankRequest.Params.TopN)
}

func TestCohereRerankResponseConverterEmitsCohereShape(t *testing.T) {
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

	resp := &schemas.BifrostRerankResponse{
		ID: "r-123",
		Results: []schemas.RerankResult{
			{Index: 1, RelevanceScore: 0.9},
			{Index: 0, RelevanceScore: 0.1},
		},
		Model: "rerank-v3.5",
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 12, CompletionTokens: 3},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Cohere,
		},
	}

	converted, err := rerankRoute.RerankResponseConverter(nil, resp)
	require.NoError(t, err)

	cohereResp, ok := converted.(*cohere.CohereRerankResponse)
	require.True(t, ok, "converter should emit *cohere.CohereRerankResponse")
	assert.Equal(t, "r-123", cohereResp.ID)
	require.Len(t, cohereResp.Results, 2)
	assert.Equal(t, 1, cohereResp.Results[0].Index)
	assert.InDelta(t, 0.9, cohereResp.Results[0].RelevanceScore, 1e-9)
	assert.Nil(t, cohereResp.Results[0].Document, "no document echoed when the canonical response carries none")
	require.NotNil(t, cohereResp.Meta)
	require.NotNil(t, cohereResp.Meta.Tokens)
	assert.Equal(t, 12, *cohereResp.Meta.Tokens.InputTokens)

	// The wire response must not leak Bifrost-only fields.
	encoded, err := sonic.Marshal(cohereResp)
	require.NoError(t, err)
	var payload map[string]interface{}
	require.NoError(t, sonic.Unmarshal(encoded, &payload))
	assert.NotContains(t, payload, "model")
	assert.NotContains(t, payload, "extra_fields")
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

func TestCohereRerankResponseConverterConvertsForCrossProvider(t *testing.T) {
	routes := CreateCohereRouteConfigs("/cohere")

	var rerankRoute *RouteConfig
	for i := range routes {
		if routes[i].Path == "/cohere/v2/rerank" {
			rerankRoute = &routes[i]
			break
		}
	}
	require.NotNil(t, rerankRoute)

	// A Bedrock-shaped raw body must never reach a Cohere client.
	resp := &schemas.BifrostRerankResponse{
		Results: []schemas.RerankResult{{Index: 0, RelevanceScore: 0.5}},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    schemas.Bedrock,
			RawResponse: map[string]interface{}{"results": []interface{}{}},
		},
	}

	converted, err := rerankRoute.RerankResponseConverter(nil, resp)
	require.NoError(t, err)
	cohereResp, ok := converted.(*cohere.CohereRerankResponse)
	require.True(t, ok, "cross-provider responses must be converted, not passed through")
	require.Len(t, cohereResp.Results, 1)
}
