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
				Document: &CohereRerankDocument{Text: "provider-doc-1", ID: schemas.Ptr("doc-1"), Metadata: map[string]interface{}{"topic": "geography"}},
			},
			{
				Index:          0,
				RelevanceScore: 0.91,
				Document: &CohereRerankDocument{Text: "provider-doc-0"},
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
				Document: &CohereRerankDocument{Text: "provider-doc-1"},
			},
			{
				Index:          0,
				RelevanceScore: 0.91,
				Document: &CohereRerankDocument{Text: "provider-doc-0"},
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

func TestToCohereRerankResponse(t *testing.T) {
	cohereResp := ToCohereRerankResponse(&schemas.BifrostRerankResponse{
		ID: "rerank-response-id",
		Results: []schemas.RerankResult{
			{Index: 1, RelevanceScore: 0.91, Document: &schemas.RerankDocument{Text: "doc-1"}},
			{Index: 0, RelevanceScore: 0.62},
		},
		Model: "rerank-v3.5",
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15},
	})

	require.NotNil(t, cohereResp)
	assert.Equal(t, "rerank-response-id", cohereResp.ID)
	require.Len(t, cohereResp.Results, 2)
	assert.Equal(t, 1, cohereResp.Results[0].Index)
	assert.InDelta(t, 0.91, cohereResp.Results[0].RelevanceScore, 1e-9)
	// Cohere echoes the document back when the caller asked for documents.
	require.NotNil(t, cohereResp.Results[0].Document)
	assert.Equal(t, "doc-1", cohereResp.Results[0].Document.Text)
	assert.Nil(t, cohereResp.Results[1].Document)

	require.NotNil(t, cohereResp.Meta)
	require.NotNil(t, cohereResp.Meta.Tokens)
	require.NotNil(t, cohereResp.Meta.Tokens.InputTokens)
	assert.Equal(t, 12, *cohereResp.Meta.Tokens.InputTokens)
	require.NotNil(t, cohereResp.Meta.Tokens.OutputTokens)
	assert.Equal(t, 3, *cohereResp.Meta.Tokens.OutputTokens)
	require.NotNil(t, cohereResp.Meta.BilledUnits)
	require.NotNil(t, cohereResp.Meta.BilledUnits.InputTokens)
	assert.Equal(t, 12, *cohereResp.Meta.BilledUnits.InputTokens)
}

func TestToCohereRerankResponseOmitsEmptyIDAndMeta(t *testing.T) {
	cohereResp := ToCohereRerankResponse(&schemas.BifrostRerankResponse{
		Results: []schemas.RerankResult{{Index: 0, RelevanceScore: 0.5}},
	})

	require.NotNil(t, cohereResp)
	assert.Nil(t, cohereResp.Meta)

	encoded, err := json.Marshal(cohereResp)
	require.NoError(t, err)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(encoded, &payload))
	assert.NotContains(t, payload, "id")
	assert.NotContains(t, payload, "meta")

	assert.Nil(t, ToCohereRerankResponse(nil))
}

func TestToCohereRerankResponseDocumentIsAlwaysAnObject(t *testing.T) {
	// Cohere returns results[].document as an object even for a plain-string request
	// document. Emitting a bare string here breaks the official SDK, which parses the
	// field into a typed document.
	cohereResp := ToCohereRerankResponse(&schemas.BifrostRerankResponse{
		Results: []schemas.RerankResult{
			{Index: 0, RelevanceScore: 0.9, Document: &schemas.RerankDocument{Text: "doc-0"}},
		},
	})

	encoded, err := json.Marshal(cohereResp)
	require.NoError(t, err)

	var payload struct {
		Results []struct {
			Document map[string]interface{} `json:"document"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(encoded, &payload))
	require.Len(t, payload.Results, 1)
	require.NotNil(t, payload.Results[0].Document, "document must serialize as an object, not a string")
	assert.Equal(t, "doc-0", payload.Results[0].Document["text"])
}
