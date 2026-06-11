package cohere

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCohereRerankResponseToBifrostRerankResponse verifies result ordering and
// provider-document parsing when converting a Cohere rerank response.
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

// TestCohereRerankResponseToBifrostRerankResponseReturnDocuments verifies that
// request documents are echoed back onto results when return_documents is set.
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

// TestCohereRerankResponseToBifrostRerankResponseSearchUnitsUsage verifies that
// a rerank response billed only in search units (no token counts) still yields
// a non-nil Usage with NumSearchQueries populated.
func TestCohereRerankResponseToBifrostRerankResponseSearchUnitsUsage(t *testing.T) {
	response := (&CohereRerankResponse{
		ID: "rerank-response-id",
		Results: []CohereRerankResult{
			{Index: 0, RelevanceScore: 0.91},
		},
		Meta: &CohereRerankMeta{
			BilledUnits: &CohereBilledUnits{SearchUnits: schemas.Ptr(2)},
		},
	}).ToBifrostRerankResponse(nil, false)

	require.NotNil(t, response)
	require.NotNil(t, response.Usage)
	require.NotNil(t, response.Usage.CompletionTokensDetails)
	require.NotNil(t, response.Usage.CompletionTokensDetails.NumSearchQueries)
	assert.Equal(t, 2, *response.Usage.CompletionTokensDetails.NumSearchQueries)
	assert.Equal(t, 0, response.Usage.TotalTokens)
}

// TestCohereRerankResponseToBifrostRerankResponseSearchUnitsWithTokenUsage verifies
// that token counts and search units are both preserved when billed_units carries
// the two together.
func TestCohereRerankResponseToBifrostRerankResponseSearchUnitsWithTokenUsage(t *testing.T) {
	response := (&CohereRerankResponse{
		ID: "rerank-response-id",
		Results: []CohereRerankResult{
			{Index: 0, RelevanceScore: 0.91},
		},
		Meta: &CohereRerankMeta{
			BilledUnits: &CohereBilledUnits{
				InputTokens:  schemas.Ptr(7),
				OutputTokens: schemas.Ptr(3),
				SearchUnits:  schemas.Ptr(1),
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
