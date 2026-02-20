package cohere

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCohereRerankResponseToBifrostRerankResponse(t *testing.T) {
	response := (&CohereRerankResponse{
		ID: "rerank-response-id",
		Results: []CohereRerankResult{
			{
				Index:          1,
				RelevanceScore: 0.62,
				Document: map[string]interface{}{
					"text":  "provider-doc-1",
					"id":    "doc-1",
					"topic": "geography",
				},
			},
			{
				Index:          0,
				RelevanceScore: 0.91,
				Document: map[string]interface{}{
					"text": "provider-doc-0",
				},
			},
		},
	}).ToBifrostRerankResponse(nil, false)

	require.NotNil(t, response)
	assert.Equal(t, "rerank-response-id", response.ID)
	require.Len(t, response.Results, 2)
	assert.Equal(t, 0, response.Results[0].Index)
	assert.Equal(t, 1, response.Results[1].Index)
	require.NotNil(t, response.Results[0].Document)
	require.NotNil(t, response.Results[1].Document)
	assert.Equal(t, "provider-doc-0", response.Results[0].Document.Text)
	assert.Equal(t, "provider-doc-1", response.Results[1].Document.Text)
	require.NotNil(t, response.Results[1].Document.ID)
	assert.Equal(t, "doc-1", *response.Results[1].Document.ID)
	assert.Equal(t, "geography", response.Results[1].Document.Meta["topic"])
}

func TestCohereRerankResponseToBifrostRerankResponseReturnDocuments(t *testing.T) {
	requestDocs := []schemas.RerankDocument{
		{Text: "request-doc-0"},
		{Text: "request-doc-1"},
	}

	response := (&CohereRerankResponse{
		Results: []CohereRerankResult{
			{
				Index:          1,
				RelevanceScore: 0.62,
				Document: map[string]interface{}{
					"text": "provider-doc-1",
				},
			},
			{
				Index:          0,
				RelevanceScore: 0.91,
				Document: map[string]interface{}{
					"text": "provider-doc-0",
				},
			},
		},
	}).ToBifrostRerankResponse(requestDocs, true)

	require.NotNil(t, response)
	require.Len(t, response.Results, 2)
	require.NotNil(t, response.Results[0].Document)
	require.NotNil(t, response.Results[1].Document)
	assert.Equal(t, 0, response.Results[0].Index)
	assert.Equal(t, 1, response.Results[1].Index)
	assert.Equal(t, "request-doc-0", response.Results[0].Document.Text)
	assert.Equal(t, "request-doc-1", response.Results[1].Document.Text)
}
