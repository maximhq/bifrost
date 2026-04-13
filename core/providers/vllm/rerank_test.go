package vllm

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRerankToVLLMRerankRequestNil(t *testing.T) {
	req := ToVLLMRerankRequest(nil)
	assert.Nil(t, req)
}

func TestRerankToVLLMRerankRequest(t *testing.T) {
	topN := 2
	maxTokens := 128
	priority := 5

	req := ToVLLMRerankRequest(&schemas.BifrostRerankRequest{
		Model: "BAAI/bge-reranker-v2-m3",
		Query: "what is machine learning",
		Documents: []schemas.RerankDocument{
			{Text: "Machine learning is a subset of AI."},
			{Text: "The weather is sunny."},
		},
		Params: &schemas.RerankParameters{
			TopN:            &topN,
			MaxTokensPerDoc: &maxTokens,
			Priority:        &priority,
			ExtraParams: map[string]interface{}{
				"user": "test-user",
			},
		},
	})

	require.NotNil(t, req)
	assert.Equal(t, "BAAI/bge-reranker-v2-m3", req.Model)
	assert.Equal(t, "what is machine learning", req.Query)
	assert.Equal(t, []string{"Machine learning is a subset of AI.", "The weather is sunny."}, req.Documents)
	require.NotNil(t, req.TopN)
	assert.Equal(t, 2, *req.TopN)
	require.NotNil(t, req.MaxTokensPerDoc)
	assert.Equal(t, 128, *req.MaxTokensPerDoc)
	require.NotNil(t, req.Priority)
	assert.Equal(t, 5, *req.Priority)
	assert.Equal(t, "test-user", req.ExtraParams["user"])
}

func TestRerankToBifrostRerankResponse(t *testing.T) {
	documents := []schemas.RerankDocument{
		{Text: "doc-0"},
		{Text: "doc-1"},
		{Text: "doc-2"},
	}

	response, err := ToBifrostRerankResponse(map[string]interface{}{
		"id":    "rerank-id",
		"model": "BAAI/bge-reranker-v2-m3",
		"usage": map[string]interface{}{
			"prompt_tokens": 10,
			"total_tokens":  10,
		},
		"results": []interface{}{
			map[string]interface{}{"index": 1, "relevance_score": 0.1},
			map[string]interface{}{"index": 0, "relevance_score": 0.9},
		},
	}, documents, true)

	require.NoError(t, err)
	require.NotNil(t, response)
	assert.Equal(t, "rerank-id", response.ID)
	assert.Equal(t, "BAAI/bge-reranker-v2-m3", response.Model)
	require.NotNil(t, response.Usage)
	assert.Equal(t, 10, response.Usage.PromptTokens)
	assert.Equal(t, 10, response.Usage.TotalTokens)
	require.Len(t, response.Results, 2)
	assert.Equal(t, 0, response.Results[0].Index)
	assert.Equal(t, 0.9, response.Results[0].RelevanceScore)
	require.NotNil(t, response.Results[0].Document)
	assert.Equal(t, "doc-0", response.Results[0].Document.Text)
	assert.Equal(t, 1, response.Results[1].Index)
	assert.Equal(t, 0.1, response.Results[1].RelevanceScore)
}

func TestRerankToBifrostRerankResponseDuplicateIndices(t *testing.T) {
	documents := []schemas.RerankDocument{
		{Text: "doc-0"},
		{Text: "doc-1"},
	}

	_, err := ToBifrostRerankResponse(map[string]interface{}{
		"results": []interface{}{
			map[string]interface{}{"index": 0, "relevance_score": 0.9},
			map[string]interface{}{"index": 0, "relevance_score": 0.8},
		},
	}, documents, true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate index")
}

func TestRerankToBifrostRerankResponseOutOfRangeIndex(t *testing.T) {
	documents := []schemas.RerankDocument{
		{Text: "doc-0"},
	}

	_, err := ToBifrostRerankResponse(map[string]interface{}{
		"results": []interface{}{
			map[string]interface{}{"index": 1, "relevance_score": 0.9},
		},
	}, documents, true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

func TestRerankToBifrostRerankResponseEmptyResults(t *testing.T) {
	documents := []schemas.RerankDocument{
		{Text: "doc-0"},
	}

	response, err := ToBifrostRerankResponse(map[string]interface{}{
		"results": []interface{}{},
	}, documents, false)

	require.NoError(t, err)
	require.NotNil(t, response)
	assert.Len(t, response.Results, 0)
}

func TestRerankToBifrostRerankResponseZeroRelevanceScoreDoesNotFallback(t *testing.T) {
	documents := []schemas.RerankDocument{
		{Text: "doc-0"},
	}

	response, err := ToBifrostRerankResponse(map[string]interface{}{
		"results": []interface{}{
			map[string]interface{}{"index": 0, "relevance_score": 0.0, "score": 0.99},
		},
	}, documents, false)

	require.NoError(t, err)
	require.NotNil(t, response)
	require.Len(t, response.Results, 1)
	assert.Equal(t, 0.0, response.Results[0].RelevanceScore)
}

func TestRerankToBifrostRerankResponseMissingScore(t *testing.T) {
	_, err := ToBifrostRerankResponse(map[string]interface{}{
		"results": []interface{}{
			map[string]interface{}{"index": 0},
		},
	}, []schemas.RerankDocument{{Text: "doc-0"}}, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "relevance_score/score is required")
}

func TestRerankParseTypedVLLMUsageZeroUsage(t *testing.T) {
	usage, ok := parseTypedVLLMUsage(&VLLMRerankUsage{})
	assert.False(t, ok)
	assert.Nil(t, usage)
}

func TestRerankParseTypedVLLMUsageTotalOnly(t *testing.T) {
	total := 7
	usage, ok := parseTypedVLLMUsage(&VLLMRerankUsage{TotalTokens: &total})
	require.True(t, ok)
	require.NotNil(t, usage)
	assert.Equal(t, 0, usage.PromptTokens)
	assert.Equal(t, 0, usage.CompletionTokens)
	assert.Equal(t, 7, usage.TotalTokens)
}

func TestRerankToBifrostRerankResponseNullRelevanceScoreFallsBackToScore(t *testing.T) {
	response, err := ToBifrostRerankResponse(map[string]interface{}{
		"results": []interface{}{
			map[string]interface{}{"index": 0, "relevance_score": nil, "score": 0.25},
		},
	}, []schemas.RerankDocument{{Text: "doc-0"}}, false)

	require.NoError(t, err)
	require.NotNil(t, response)
	require.Len(t, response.Results, 1)
	assert.Equal(t, 0.25, response.Results[0].RelevanceScore)
}
