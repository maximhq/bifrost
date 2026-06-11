package cohere

import (
	"encoding/json"
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
				Document:       json.RawMessage(`{"text":"provider-doc-1","id":"doc-1","topic":"geography"}`),
			},
			{
				Index:          0,
				RelevanceScore: 0.91,
				Document:       json.RawMessage(`{"text":"provider-doc-0"}`),
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
				Document:       json.RawMessage(`{"text":"provider-doc-1"}`),
			},
			{
				Index:          0,
				RelevanceScore: 0.91,
				Document:       json.RawMessage(`{"text":"provider-doc-0"}`),
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

func TestCohereRerankResponseToBifrostRerankResponseSearchUnitsUsage(t *testing.T) {
	searchUnits := 2

	response := (&CohereRerankResponse{
		ID: "rerank-response-id",
		Results: []CohereRerankResult{
			{Index: 0, RelevanceScore: 0.91},
		},
		Meta: &CohereRerankMeta{
			BilledUnits: &CohereBilledUnits{SearchUnits: &searchUnits},
		},
	}).ToBifrostRerankResponse(nil, false)

	require.NotNil(t, response)
	require.NotNil(t, response.Usage)
	require.NotNil(t, response.Usage.CompletionTokensDetails)
	require.NotNil(t, response.Usage.CompletionTokensDetails.NumSearchQueries)
	assert.Equal(t, 2, *response.Usage.CompletionTokensDetails.NumSearchQueries)
	assert.Equal(t, 0, response.Usage.TotalTokens)
}

func TestCohereRerankResponseToBifrostRerankResponseSearchUnitsWithTokenUsage(t *testing.T) {
	searchUnits := 1
	inputTokens := 7
	outputTokens := 3

	response := (&CohereRerankResponse{
		ID: "rerank-response-id",
		Results: []CohereRerankResult{
			{Index: 0, RelevanceScore: 0.91},
		},
		Meta: &CohereRerankMeta{
			BilledUnits: &CohereBilledUnits{
				InputTokens:  &inputTokens,
				OutputTokens: &outputTokens,
				SearchUnits:  &searchUnits,
			},
		},
	}).ToBifrostRerankResponse(nil, false)

	require.NotNil(t, response)
	require.NotNil(t, response.Usage)
	assert.Equal(t, 7, response.Usage.PromptTokens)
	assert.Equal(t, 3, response.Usage.CompletionTokens)
	assert.Equal(t, 10, response.Usage.TotalTokens)
	require.NotNil(t, response.Usage.CompletionTokensDetails)
	require.NotNil(t, response.Usage.CompletionTokensDetails.NumSearchQueries)
	assert.Equal(t, 1, *response.Usage.CompletionTokensDetails.NumSearchQueries)
}
