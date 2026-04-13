package cohere

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCohereRerankResponseToBifrostRerankResponse(t *testing.T) {
	response := (&CohereRerankResponse{
		ID: "rerank-response-id",
		Results: []CohereRerankResult{
			{
				Index:          1,
				RelevanceScore: 0.62,
				Document: json.RawMessage(`{"text":"provider-doc-1","id":"doc-1","topic":"geography"}`),
			},
			{
				Index:          0,
				RelevanceScore: 0.91,
				Document: json.RawMessage(`{"text":"provider-doc-0"}`),
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
				Document: json.RawMessage(`{"text":"provider-doc-1"}`),
			},
			{
				Index:          0,
				RelevanceScore: 0.91,
				Document: json.RawMessage(`{"text":"provider-doc-0"}`),
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
	response, err := ToCohereRerankResponse(&schemas.BifrostRerankResponse{
		ID: "rerank-response-id",
		Results: []schemas.RerankResult{
			{
				Index:          0,
				RelevanceScore: 0.91,
				Document: &schemas.RerankDocument{
					Text: "provider-doc-0",
					Meta: map[string]interface{}{"topic": "geography"},
				},
			},
		},
		Usage: &schemas.BifrostLLMUsage{
			PromptTokens:     8,
			CompletionTokens: 0,
			TotalTokens:      8,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.Equal(t, "rerank-response-id", response.ID)
	require.Len(t, response.Results, 1)
	assert.Equal(t, 0, response.Results[0].Index)
	assert.Equal(t, 0.91, response.Results[0].RelevanceScore)

	var document map[string]interface{}
	require.NoError(t, json.Unmarshal(response.Results[0].Document, &document))
	assert.Equal(t, "provider-doc-0", document["text"])
	assert.Equal(t, map[string]interface{}{"topic": "geography"}, document["metadata"])
	require.NotNil(t, response.Meta)
	require.NotNil(t, response.Meta.Tokens)
	require.NotNil(t, response.Meta.Tokens.InputTokens)
	assert.Equal(t, 8, *response.Meta.Tokens.InputTokens)
}

func TestCohereRerankResponseToBifrostRerankResponsePreservesEmptyTextDocument(t *testing.T) {
	response := (&CohereRerankResponse{
		Results: []CohereRerankResult{
			{
				Index:          0,
				RelevanceScore: 0.42,
				Document:       json.RawMessage(`{"text":""}`),
			},
		},
	}).ToBifrostRerankResponse(nil, false)

	require.NotNil(t, response)
	require.Len(t, response.Results, 1)
	require.NotNil(t, response.Results[0].Document)
	assert.Equal(t, "", response.Results[0].Document.Text)
}

func TestToCohereRerankResponseNilDocument(t *testing.T) {
	response, err := ToCohereRerankResponse(&schemas.BifrostRerankResponse{
		ID: "rerank-response-id",
		Results: []schemas.RerankResult{
			{
				Index:          1,
				RelevanceScore: 0.55,
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Len(t, response.Results, 1)
	assert.Nil(t, response.Results[0].Document)
}

func TestToCohereRerankResponsePreservesEmptyTextAndID(t *testing.T) {
	documentID := "doc-7"
	response, err := ToCohereRerankResponse(&schemas.BifrostRerankResponse{
		ID: "rerank-response-id",
		Results: []schemas.RerankResult{
			{
				Index:          0,
				RelevanceScore: 0.91,
				Document: &schemas.RerankDocument{
					ID:   &documentID,
					Text: "",
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Len(t, response.Results, 1)

	var document map[string]interface{}
	require.NoError(t, json.Unmarshal(response.Results[0].Document, &document))
	assert.Equal(t, "", document["text"])
	assert.Equal(t, documentID, document["id"])
}

func TestCohereRerankRequestToBifrostRerankRequest(t *testing.T) {
	topN := 3
	req := &CohereRerankRequest{
		Model:     "rerank-v3.5",
		Query:     "capital of france",
		Documents: []string{"Paris is the capital of France.", "Berlin is the capital of Germany."},
		TopN:      &topN,
	}

	result := req.ToBifrostRerankRequest(nil)

	require.NotNil(t, result)
	assert.Equal(t, "rerank-v3.5", result.Model)
	assert.Equal(t, "capital of france", result.Query)
	require.Len(t, result.Documents, 2)
	require.NotNil(t, result.Params)
	require.NotNil(t, result.Params.TopN)
	assert.Equal(t, 3, *result.Params.TopN)
	assert.Nil(t, result.Params.ReturnDocuments)
}

func TestToCohereRerankRequestFormatsPlainTextDocumentsAsRawStrings(t *testing.T) {
	request := ToCohereRerankRequest(&schemas.BifrostRerankRequest{
		Model: "rerank-v3.5",
		Query: "capital of france",
		Documents: []schemas.RerankDocument{
			{Text: "Paris is the capital of France."},
		},
	})

	require.NotNil(t, request)
	require.Len(t, request.Documents, 1)
	assert.Equal(t, "Paris is the capital of France.", request.Documents[0])
}

func TestToCohereRerankRequestFormatsStructuredDocumentsAsYAMLStrings(t *testing.T) {
	documentID := "doc-1"
	request := ToCohereRerankRequest(&schemas.BifrostRerankRequest{
		Model: "rerank-v3.5",
		Query: "capital of france",
		Documents: []schemas.RerankDocument{
			{
				Text: "Paris is the capital of France.",
				ID:   &documentID,
				Meta: map[string]interface{}{
					"topic": "geography",
				},
			},
		},
	})

	require.NotNil(t, request)
	require.Len(t, request.Documents, 1)

	var parsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(request.Documents[0]), &parsed))
	assert.Equal(t, "Paris is the capital of France.", parsed["text"])
	assert.Equal(t, documentID, parsed["id"])

	metadata, ok := parsed["metadata"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "geography", metadata["topic"])
}

func TestToCohereRerankResponseMarshalFailure(t *testing.T) {
	_, err := ToCohereRerankResponse(&schemas.BifrostRerankResponse{
		Results: []schemas.RerankResult{
			{
				Index:          0,
				RelevanceScore: 0.91,
				Document: &schemas.RerankDocument{
					Text: "doc-0",
					Meta: map[string]interface{}{
						"bad": make(chan int),
					},
				},
			},
		},
	})
	require.Error(t, err)
}
