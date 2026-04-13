package bedrock

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToBedrockRerankRequest(t *testing.T) {
	topN := 10
	maxTokensPerDoc := 512
	priority := 3

	req, err := ToBedrockRerankRequest(&schemas.BifrostRerankRequest{
		Model: "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0",
		Query: "capital of france",
		Documents: []schemas.RerankDocument{
			{Text: "Paris is the capital of France."},
			{Text: "Berlin is the capital of Germany."},
		},
		Params: &schemas.RerankParameters{
			TopN:            schemas.Ptr(topN),
			MaxTokensPerDoc: schemas.Ptr(maxTokensPerDoc),
			Priority:        schemas.Ptr(priority),
			ExtraParams: map[string]interface{}{
				"truncate": "END",
			},
		},
	}, "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0")
	require.NoError(t, err)
	require.NotNil(t, req)

	require.Len(t, req.Queries, 1)
	assert.Equal(t, "TEXT", req.Queries[0].Type)
	assert.Equal(t, "capital of france", req.Queries[0].TextQuery.Text)
	require.Len(t, req.Sources, 2)

	require.NotNil(t, req.RerankingConfiguration.BedrockRerankingConfiguration.NumberOfResults)
	assert.Equal(t, 2, *req.RerankingConfiguration.BedrockRerankingConfiguration.NumberOfResults, "top_n must be clamped to source count")

	fields := req.RerankingConfiguration.BedrockRerankingConfiguration.ModelConfiguration.AdditionalModelRequestFields
	require.NotNil(t, fields)
	assert.Equal(t, maxTokensPerDoc, fields["max_tokens_per_doc"])
	assert.Equal(t, priority, fields["priority"])
	assert.Equal(t, "END", fields["truncate"])
}

func TestBedrockRerankResponseToBifrostRerankResponse(t *testing.T) {
	response := (&BedrockRerankResponse{
		Results: []BedrockRerankResult{
			{
				Index:          2,
				RelevanceScore: 0.21,
				Document: &BedrockRerankResponseDocument{
					TextDocument: &BedrockRerankTextValue{Text: "doc-2"},
				},
			},
			{
				Index:          1,
				RelevanceScore: 0.95,
				Document: &BedrockRerankResponseDocument{
					TextDocument: &BedrockRerankTextValue{Text: "doc-1"},
				},
			},
			{
				Index:          0,
				RelevanceScore: 0.95,
				Document: &BedrockRerankResponseDocument{
					TextDocument: &BedrockRerankTextValue{Text: "doc-0"},
				},
			},
		},
	}).ToBifrostRerankResponse(nil, false)

	require.NotNil(t, response)
	require.Len(t, response.Results, 3)

	assert.Equal(t, 0, response.Results[0].Index)
	assert.Equal(t, 1, response.Results[1].Index)
	assert.Equal(t, 2, response.Results[2].Index)
	assert.Equal(t, "doc-0", response.Results[0].Document.Text)
	assert.Equal(t, "doc-1", response.Results[1].Document.Text)
}

func TestBedrockRerankResponseToBifrostRerankResponseReturnDocuments(t *testing.T) {
	requestDocs := []schemas.RerankDocument{
		{Text: "request-doc-0"},
		{Text: "request-doc-1"},
		{Text: "request-doc-2"},
	}

	response := (&BedrockRerankResponse{
		Results: []BedrockRerankResult{
			{
				Index:          2,
				RelevanceScore: 0.21,
				Document: &BedrockRerankResponseDocument{
					TextDocument: &BedrockRerankTextValue{Text: "provider-doc-2"},
				},
			},
			{
				Index:          1,
				RelevanceScore: 0.95,
				Document: &BedrockRerankResponseDocument{
					TextDocument: &BedrockRerankTextValue{Text: "provider-doc-1"},
				},
			},
			{
				Index:          0,
				RelevanceScore: 0.95,
				Document: &BedrockRerankResponseDocument{
					TextDocument: &BedrockRerankTextValue{Text: "provider-doc-0"},
				},
			},
		},
	}).ToBifrostRerankResponse(requestDocs, true)

	require.NotNil(t, response)
	require.Len(t, response.Results, 3)
	require.NotNil(t, response.Results[0].Document)
	require.NotNil(t, response.Results[1].Document)
	require.NotNil(t, response.Results[2].Document)

	assert.Equal(t, 0, response.Results[0].Index)
	assert.Equal(t, 1, response.Results[1].Index)
	assert.Equal(t, 2, response.Results[2].Index)
	assert.Equal(t, "request-doc-0", response.Results[0].Document.Text)
	assert.Equal(t, "request-doc-1", response.Results[1].Document.Text)
	assert.Equal(t, "request-doc-2", response.Results[2].Document.Text)
}

func TestBedrockRerankRequestToBifrostRerankRequest(t *testing.T) {
	topN := 3
	bedrockReq := &BedrockRerankRequest{
		Queries: []BedrockRerankQuery{
			{
				Type:      bedrockRerankQueryTypeText,
				TextQuery: BedrockRerankTextRef{Text: "capital of france"},
			},
		},
		Sources: []BedrockRerankSource{
			{
				Type: bedrockRerankSourceTypeInline,
				InlineDocumentSource: BedrockRerankInlineSource{
					Type:         bedrockRerankInlineDocumentTypeText,
					TextDocument: &BedrockRerankTextValue{Text: "Paris is the capital of France."},
				},
			},
			{
				Type: bedrockRerankSourceTypeInline,
				InlineDocumentSource: BedrockRerankInlineSource{
					Type:         bedrockRerankInlineDocumentTypeText,
					TextDocument: &BedrockRerankTextValue{Text: "Berlin is the capital of Germany."},
				},
			},
		},
		RerankingConfiguration: BedrockRerankingConfiguration{
			Type: bedrockRerankConfigurationTypeBedrock,
			BedrockRerankingConfiguration: BedrockRerankingModelConfiguration{
				NumberOfResults: &topN,
				ModelConfiguration: BedrockRerankModelConfiguration{
					ModelARN: "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0",
					AdditionalModelRequestFields: map[string]interface{}{
						"truncate": "END",
					},
				},
			},
		},
	}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result := bedrockReq.ToBifrostRerankRequest(bifrostCtx)

	require.NotNil(t, result)
	assert.Equal(t, schemas.Bedrock, result.Provider)
	assert.Equal(t, "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0", result.Model)
	assert.Equal(t, "capital of france", result.Query)
	require.Len(t, result.Documents, 2)
	assert.Equal(t, "Paris is the capital of France.", result.Documents[0].Text)
	assert.Equal(t, "Berlin is the capital of Germany.", result.Documents[1].Text)
	require.NotNil(t, result.Params)
	require.NotNil(t, result.Params.TopN)
	assert.Equal(t, 3, *result.Params.TopN)
	require.NotNil(t, result.Params.ExtraParams)
	assert.Equal(t, "END", result.Params.ExtraParams["truncate"])
}

func TestBedrockRerankRequestToBifrostRerankRequestWithJSONDocument(t *testing.T) {
	jsonContent := json.RawMessage(`{"title":"Paris","body":"Paris is the capital of France."}`)
	bedrockReq := &BedrockRerankRequest{
		Queries: []BedrockRerankQuery{
			{
				Type:      bedrockRerankQueryTypeText,
				TextQuery: BedrockRerankTextRef{Text: "capital of france"},
			},
		},
		Sources: []BedrockRerankSource{
			{
				Type: bedrockRerankSourceTypeInline,
				InlineDocumentSource: BedrockRerankInlineSource{
					Type:         bedrockRerankInlineDocumentTypeJSON,
					JSONDocument: jsonContent,
				},
			},
			{
				Type: bedrockRerankSourceTypeInline,
				InlineDocumentSource: BedrockRerankInlineSource{
					Type:         bedrockRerankInlineDocumentTypeText,
					TextDocument: &BedrockRerankTextValue{Text: "Berlin is the capital of Germany."},
				},
			},
		},
		RerankingConfiguration: BedrockRerankingConfiguration{
			Type: bedrockRerankConfigurationTypeBedrock,
			BedrockRerankingConfiguration: BedrockRerankingModelConfiguration{
				ModelConfiguration: BedrockRerankModelConfiguration{
					ModelARN: "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0",
				},
			},
		},
	}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result := bedrockReq.ToBifrostRerankRequest(bifrostCtx)

	require.NotNil(t, result)
	require.Len(t, result.Documents, 2)

	// First document: JSON type
	assert.JSONEq(t, string(jsonContent), string(result.Documents[0].JSONContent))
	assert.Empty(t, result.Documents[0].Text)

	// Second document: TEXT type
	assert.Equal(t, "Berlin is the capital of Germany.", result.Documents[1].Text)
	assert.Nil(t, result.Documents[1].JSONContent)
}

func TestBedrockRerankRequestToBifrostRerankRequestNil(t *testing.T) {
	var req *BedrockRerankRequest
	assert.Nil(t, req.ToBifrostRerankRequest(nil))
}

func TestToBedrockRerankResponse(t *testing.T) {
	response, err := ToBedrockRerankResponse(&schemas.BifrostRerankResponse{
		Results: []schemas.RerankResult{
			{
				Index:          1,
				RelevanceScore: 0.83,
				Document:       &schemas.RerankDocument{Text: "doc-1"},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Len(t, response.Results, 1)
	assert.Equal(t, 1, response.Results[0].Index)
	assert.Equal(t, 0.83, response.Results[0].RelevanceScore)
	require.NotNil(t, response.Results[0].Document)
	assert.Equal(t, bedrockRerankInlineDocumentTypeText, response.Results[0].Document.Type)
	require.NotNil(t, response.Results[0].Document.TextDocument)
	assert.Equal(t, "doc-1", response.Results[0].Document.TextDocument.Text)
}

func TestToBedrockRerankResponsePreservesResultOrderAndNilDocuments(t *testing.T) {
	response, err := ToBedrockRerankResponse(&schemas.BifrostRerankResponse{
		Results: []schemas.RerankResult{
			{
				Index:          2,
				RelevanceScore: 0.95,
			},
			{
				Index:          0,
				RelevanceScore: 0.12,
				Document:       &schemas.RerankDocument{Text: "doc-0"},
			},
		},
		Usage: &schemas.BifrostLLMUsage{
			PromptTokens:     9,
			CompletionTokens: 0,
			TotalTokens:      9,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Len(t, response.Results, 2)
	assert.Equal(t, 2, response.Results[0].Index)
	assert.Nil(t, response.Results[0].Document)
	assert.Equal(t, 0, response.Results[1].Index)
	require.NotNil(t, response.Results[1].Document)
	require.NotNil(t, response.Results[1].Document.TextDocument)
	assert.Equal(t, "doc-0", response.Results[1].Document.TextDocument.Text)
	assert.Nil(t, response.NextToken)
}

func TestResolveBedrockDeployment(t *testing.T) {
	key := schemas.Key{
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Deployments: map[string]string{
				"cohere-rerank": "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0",
			},
		},
	}

	deployment := resolveBedrockDeployment("cohere-rerank", key)
	assert.Equal(t, "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0", deployment)
	assert.Equal(t, "cohere.rerank-v3-5:0", resolveBedrockDeployment("cohere.rerank-v3-5:0", key))
	assert.Equal(t, "", resolveBedrockDeployment("", key))
}

func TestBedrockRerankRequiresARNModelIdentifier(t *testing.T) {
	provider := &BedrockProvider{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Deployments: map[string]string{
				"cohere-rerank": "cohere.rerank-v3-5:0",
			},
		},
	}

	response, bifrostErr := provider.Rerank(ctx, key, &schemas.BifrostRerankRequest{
		Model: "cohere-rerank",
		Query: "capital of france",
		Documents: []schemas.RerankDocument{
			{Text: "Paris is the capital of France."},
		},
	})

	require.Nil(t, response)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	assert.Contains(t, bifrostErr.Error.Message, "requires an ARN")
}

func TestToBedrockRerankRequestWithJSONDocument(t *testing.T) {
	jsonContent := json.RawMessage(`{"title":"Paris","body":"Paris is the capital of France."}`)
	req, err := ToBedrockRerankRequest(&schemas.BifrostRerankRequest{
		Model: "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0",
		Query: "capital of france",
		Documents: []schemas.RerankDocument{
			{JSONContent: jsonContent},
			{Text: "Berlin is the capital of Germany."},
		},
	}, "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0")
	require.NoError(t, err)
	require.NotNil(t, req)
	require.Len(t, req.Sources, 2)

	// First document: JSON type
	assert.Equal(t, bedrockRerankInlineDocumentTypeJSON, req.Sources[0].InlineDocumentSource.Type)
	assert.JSONEq(t, string(jsonContent), string(req.Sources[0].InlineDocumentSource.JSONDocument))
	assert.Nil(t, req.Sources[0].InlineDocumentSource.TextDocument)

	// Second document: TEXT type (fallback)
	assert.Equal(t, bedrockRerankInlineDocumentTypeText, req.Sources[1].InlineDocumentSource.Type)
	require.NotNil(t, req.Sources[1].InlineDocumentSource.TextDocument)
	assert.Equal(t, "Berlin is the capital of Germany.", req.Sources[1].InlineDocumentSource.TextDocument.Text)
	assert.Nil(t, req.Sources[1].InlineDocumentSource.JSONDocument)
}

func TestBedrockRerankResponseToBifrostWithJSONDocument(t *testing.T) {
	jsonContent := json.RawMessage(`{"title":"Paris","body":"Paris is the capital of France."}`)
	response := (&BedrockRerankResponse{
		Results: []BedrockRerankResult{
			{
				Index:          0,
				RelevanceScore: 0.91,
				Document: &BedrockRerankResponseDocument{
					Type:         bedrockRerankInlineDocumentTypeJSON,
					JSONDocument: jsonContent,
				},
			},
		},
	}).ToBifrostRerankResponse(nil, false)

	require.NotNil(t, response)
	require.Len(t, response.Results, 1)
	require.NotNil(t, response.Results[0].Document)
	assert.JSONEq(t, string(jsonContent), string(response.Results[0].Document.JSONContent))
	assert.Empty(t, response.Results[0].Document.Text)
}

func TestToBedrockRerankResponseWithJSONDocument(t *testing.T) {
	jsonContent := json.RawMessage(`{"title":"Paris","body":"Paris is the capital of France."}`)
	response, err := ToBedrockRerankResponse(&schemas.BifrostRerankResponse{
		Results: []schemas.RerankResult{
			{
				Index:          0,
				RelevanceScore: 0.91,
				Document:       &schemas.RerankDocument{JSONContent: jsonContent},
			},
			{
				Index:          1,
				RelevanceScore: 0.45,
				Document:       &schemas.RerankDocument{Text: "Berlin is the capital of Germany."},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Len(t, response.Results, 2)

	// JSON document preserved
	assert.Equal(t, bedrockRerankInlineDocumentTypeJSON, response.Results[0].Document.Type)
	assert.JSONEq(t, string(jsonContent), string(response.Results[0].Document.JSONDocument))
	assert.Nil(t, response.Results[0].Document.TextDocument)

	// Text document unchanged
	assert.Equal(t, bedrockRerankInlineDocumentTypeText, response.Results[1].Document.Type)
	require.NotNil(t, response.Results[1].Document.TextDocument)
	assert.Equal(t, "Berlin is the capital of Germany.", response.Results[1].Document.TextDocument.Text)
	assert.Nil(t, response.Results[1].Document.JSONDocument)
}
