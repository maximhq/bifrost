package bedrock

import (
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
			TopN:            &topN,
			MaxTokensPerDoc: &maxTokensPerDoc,
			Priority:        &priority,
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
	}).ToBifrostRerankResponse()

	require.NotNil(t, response)
	require.Len(t, response.Results, 3)

	assert.Equal(t, 0, response.Results[0].Index)
	assert.Equal(t, 1, response.Results[1].Index)
	assert.Equal(t, 2, response.Results[2].Index)
	assert.Equal(t, "doc-0", response.Results[0].Document.Text)
	assert.Equal(t, "doc-1", response.Results[1].Document.Text)
}

func TestResolveBedrockRerankModelARN(t *testing.T) {
	key := schemas.Key{
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Deployments: map[string]string{
				"cohere-rerank": "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0",
			},
		},
	}

	arn, deployment, err := resolveBedrockRerankModelARN("cohere-rerank", key)
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0", arn)
	assert.Equal(t, arn, deployment)

	_, _, err = resolveBedrockRerankModelARN("cohere.rerank-v3-5:0", key)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires an ARN")
}
