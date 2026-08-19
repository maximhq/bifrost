package bedrock

import (
	"context"
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

func TestBedrockRerankRequestToBifrostRerankRequestNil(t *testing.T) {
	var req *BedrockRerankRequest
	assert.Nil(t, req.ToBifrostRerankRequest(nil))
}

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

// Bedrock's Rerank API names the reranker model with a full foundation-model ARN
// ("arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0" in the AWS
// docs' own example), while every other Bifrost surface names a Bedrock model by
// its bare ID. Rejecting the bare ID outright made rerank the one route where
// callers had to hand-write an ARN, so all three rerank drop-ins in the provider
// harness 400'd on "amazon.rerank-v1:0". Bifrost already resolves a region for
// every other Bedrock call, so it has everything it needs to build the ARN itself.
func TestResolveBedrockRerankModelARN(t *testing.T) {
	regionKey := func(region string) schemas.Key {
		return schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region: schemas.NewSecretVar(region),
		}}
	}

	tests := []struct {
		name  string
		key   schemas.Key
		model string
		want  string
	}{
		{
			name:  "bare model id uses the key region",
			key:   regionKey("us-west-2"),
			model: "amazon.rerank-v1:0",
			want:  "arn:aws:bedrock:us-west-2::foundation-model/amazon.rerank-v1:0",
		},
		{
			// A nil BedrockKeyConfig is valid (inherited IAM/env credentials), so this
			// must fall back to the default region rather than panic or return empty.
			name:  "nil key config falls back to the default region",
			key:   schemas.Key{},
			model: "amazon.rerank-v1:0",
			want:  "arn:aws:bedrock:" + DefaultBedrockRegion + "::foundation-model/amazon.rerank-v1:0",
		},
		{
			name:  "region prefix on the model wins over the key region",
			key:   regionKey("us-west-2"),
			model: "eu-central-1/cohere.rerank-v3-5:0",
			want:  "arn:aws:bedrock:eu-central-1::foundation-model/cohere.rerank-v3-5:0",
		},
		{
			// Hardcoding the "aws" partition silently breaks GovCloud and China, where
			// the ARN is otherwise well-formed and the failure surfaces only at runtime.
			name:  "govcloud region uses the aws-us-gov partition",
			key:   regionKey("us-gov-west-1"),
			model: "amazon.rerank-v1:0",
			want:  "arn:aws-us-gov:bedrock:us-gov-west-1::foundation-model/amazon.rerank-v1:0",
		},
		{
			name:  "china region uses the aws-cn partition",
			key:   regionKey("cn-north-1"),
			model: "amazon.rerank-v1:0",
			want:  "arn:aws-cn:bedrock:cn-north-1::foundation-model/amazon.rerank-v1:0",
		},
		{
			name:  "an explicit ARN passes through untouched",
			key:   regionKey("us-west-2"),
			model: "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0",
			want:  "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0",
		},
		{
			name:  "empty model stays empty so the caller can reject it",
			key:   regionKey("us-west-2"),
			model: "",
			want:  "",
		},
		{
			name:  "whitespace-only model stays empty",
			key:   regionKey("us-west-2"),
			model: "   ",
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			assert.Equal(t, tc.want, resolveBedrockRerankModelARN(ctx, tc.key, tc.model))
		})
	}
}

// A bare model ID must now reach the request builder as a synthesized ARN rather
// than being rejected before the call is ever built.
func TestBedrockRerankAcceptsBareModelIdentifier(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{
		Region: schemas.NewSecretVar("us-east-1"),
	}}

	modelARN := resolveBedrockRerankModelARN(ctx, key, "amazon.rerank-v1:0")
	req, err := ToBedrockRerankRequest(&schemas.BifrostRerankRequest{
		Model: "amazon.rerank-v1:0",
		Query: "capital of france",
		Documents: []schemas.RerankDocument{
			{Text: "Paris is the capital of France."},
		},
	}, modelARN)

	require.NoError(t, err)
	require.NotNil(t, req)
	assert.Equal(t,
		"arn:aws:bedrock:us-east-1::foundation-model/amazon.rerank-v1:0",
		req.RerankingConfiguration.BedrockRerankingConfiguration.ModelConfiguration.ModelARN)
}

// The model identifier is still required; only the ARN-shape requirement is lifted.
func TestBedrockRerankRejectsEmptyModelIdentifier(t *testing.T) {
	provider := &BedrockProvider{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	response, bifrostErr := provider.Rerank(ctx, schemas.Key{}, &schemas.BifrostRerankRequest{
		Model: "",
		Query: "capital of france",
		Documents: []schemas.RerankDocument{
			{Text: "Paris is the capital of France."},
		},
	})

	require.Nil(t, response)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	assert.Contains(t, bifrostErr.Error.Message, "model identifier")
}
