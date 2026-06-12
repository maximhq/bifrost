package bedrock

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToBedrockRerankRequest verifies Bifrost rerank requests convert to the
// Bedrock wire format with documents, top_n, and model ARN in place.
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

// TestBedrockRerankResponseToBifrostRerankResponse verifies result ordering
// and document parsing when converting a Bedrock rerank response.
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

// TestBedrockRerankResponseToBifrostRerankResponseReturnDocuments verifies
// request documents are echoed back onto results when return_documents is set.
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

// TestBedrockRerankRequestToBifrostRerankRequest verifies the Bedrock rerank
// request converts back to the Bifrost request shape.
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
					TextDocument: BedrockRerankTextValue{Text: "Paris is the capital of France."},
				},
			},
			{
				Type: bedrockRerankSourceTypeInline,
				InlineDocumentSource: BedrockRerankInlineSource{
					Type:         bedrockRerankInlineDocumentTypeText,
					TextDocument: BedrockRerankTextValue{Text: "Berlin is the capital of Germany."},
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
	// A Bedrock rerank ModelARN has no Bifrost provider prefix, so ParseModelString
	// returns the empty default provider (intentional — provider routing is resolved
	// elsewhere) and the full ARN as the model.
	assert.Equal(t, schemas.ModelProvider(""), result.Provider)
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

// TestBedrockRerankRequestToBifrostRerankRequestNil verifies a nil request
// converts to nil instead of panicking.
func TestBedrockRerankRequestToBifrostRerankRequestNil(t *testing.T) {
	var req *BedrockRerankRequest
	assert.Nil(t, req.ToBifrostRerankRequest(nil))
}

// TestResolveBedrockDeployment verifies deployment resolution falls back to
// the requested model when no deployment mapping exists.
func TestResolveBedrockDeployment(t *testing.T) {
	key := schemas.Key{
		Aliases: schemas.KeyAliases{
			"cohere-rerank": {ModelID: "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0"},
		},
	}

	deployment := key.Aliases.Resolve("cohere-rerank")
	assert.Equal(t, "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0", deployment)
	assert.Equal(t, "cohere.rerank-v3-5:0", key.Aliases.Resolve("cohere.rerank-v3-5:0"))
	assert.Equal(t, "", key.Aliases.Resolve(""))
}

// TestBedrockRerankRequiresARNModelIdentifier verifies rerank rejects
// non-ARN model identifiers with a configuration error.
func TestBedrockRerankRequiresARNModelIdentifier(t *testing.T) {
	provider := &BedrockProvider{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{
		Aliases: schemas.KeyAliases{
			"cohere-rerank": {ModelID: "cohere.rerank-v3-5:0"},
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

// TestBedrockRerankResponseToBifrostRerankResponseSynthesizesQueryUsage verifies
// that the conversion synthesizes per-query usage (one billable query per call),
// since Bedrock's rerank response body carries no usage payload.
func TestBedrockRerankResponseToBifrostRerankResponseSynthesizesQueryUsage(t *testing.T) {
	response := (&BedrockRerankResponse{
		Results: []BedrockRerankResult{
			{Index: 0, RelevanceScore: 0.91},
		},
	}).ToBifrostRerankResponse(nil, false)

	require.NotNil(t, response)
	require.NotNil(t, response.Usage)
	require.NotNil(t, response.Usage.CompletionTokensDetails)
	require.NotNil(t, response.Usage.CompletionTokensDetails.NumSearchQueries)
	assert.Equal(t, 1, *response.Usage.CompletionTokensDetails.NumSearchQueries)
}
