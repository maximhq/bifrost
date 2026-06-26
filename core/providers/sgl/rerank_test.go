package sgl

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToSGLRerankRequestNil(t *testing.T) {
	t.Parallel()
	req := ToSGLRerankRequest(nil)
	assert.Nil(t, req)
}

// TestToSGLRerankRequest_NoModelField is the load-bearing test for this PR:
// sglang's V1RerankReqInput rejects unknown fields including `model`, so the
// converter must never emit one even when the Bifrost request has Model set.
func TestToSGLRerankRequest_NoModelField(t *testing.T) {
	t.Parallel()

	topN := 3
	returnDocs := true

	req := ToSGLRerankRequest(&schemas.BifrostRerankRequest{
		Model: "BAAI/bge-reranker-v2-m3",
		Query: "what is machine learning",
		Documents: []schemas.RerankDocument{
			{Text: "Machine learning is a subset of AI."},
			{Text: "The weather is sunny."},
		},
		Params: &schemas.RerankParameters{
			TopN:            &topN,
			ReturnDocuments: &returnDocs,
			ExtraParams: map[string]interface{}{
				"user": "test-user",
			},
		},
	})

	require.NotNil(t, req)
	assert.Equal(t, "what is machine learning", req.Query)
	assert.Equal(t, []string{"Machine learning is a subset of AI.", "The weather is sunny."}, req.Documents)
	require.NotNil(t, req.TopN)
	assert.Equal(t, 3, *req.TopN)
	require.NotNil(t, req.ReturnDocuments)
	assert.True(t, *req.ReturnDocuments)
	assert.Equal(t, "test-user", req.ExtraParams["user"])

	// Serialize and verify no `model` field is present in the wire body.
	body, err := sonic.Marshal(req)
	require.NoError(t, err)
	var asMap map[string]interface{}
	require.NoError(t, sonic.Unmarshal(body, &asMap))
	_, hasModel := asMap["model"]
	assert.False(t, hasModel, "sglang /v1/rerank rejects the `model` field; converter must not emit it. body=%s", string(body))

	// Spot-check expected fields exist.
	assert.Contains(t, asMap, "query")
	assert.Contains(t, asMap, "documents")
	assert.Contains(t, asMap, "top_n")
	assert.Contains(t, asMap, "return_documents")
}

func TestToSGLRerankRequest_OmitsOptionalFields(t *testing.T) {
	t.Parallel()

	req := ToSGLRerankRequest(&schemas.BifrostRerankRequest{
		Model:     "BAAI/bge-reranker-v2-m3",
		Query:     "q",
		Documents: []schemas.RerankDocument{{Text: "d"}},
	})
	require.NotNil(t, req)
	body, err := sonic.Marshal(req)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "top_n")
	assert.NotContains(t, string(body), "return_documents")
}

// TestToBifrostRerankResponse_BareArray verifies parsing of sglang's
// distinctive bare-array response shape using the `score` field.
func TestToBifrostRerankResponse_BareArray(t *testing.T) {
	t.Parallel()

	documents := []schemas.RerankDocument{
		{Text: "doc-0"},
		{Text: "doc-1"},
		{Text: "doc-2"},
	}

	// sglang returns a bare JSON array. Simulate decoding into []interface{}.
	const raw = `[
		{"index": 1, "score": 0.1, "document": "doc-1"},
		{"index": 0, "score": 0.9, "document": "doc-0"},
		{"index": 2, "score": 0.5, "document": "doc-2"}
	]`
	var items []interface{}
	require.NoError(t, sonic.Unmarshal([]byte(raw), &items))

	response, err := ToBifrostRerankResponse(items, documents, true)
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Len(t, response.Results, 3)

	// Sorted descending by score.
	assert.Equal(t, 0, response.Results[0].Index)
	assert.Equal(t, 0.9, response.Results[0].RelevanceScore)
	require.NotNil(t, response.Results[0].Document)
	assert.Equal(t, "doc-0", response.Results[0].Document.Text)

	assert.Equal(t, 2, response.Results[1].Index)
	assert.Equal(t, 0.5, response.Results[1].RelevanceScore)

	assert.Equal(t, 1, response.Results[2].Index)
	assert.Equal(t, 0.1, response.Results[2].RelevanceScore)
}

func TestToBifrostRerankResponse_OmitsDocumentsWhenNotRequested(t *testing.T) {
	t.Parallel()

	documents := []schemas.RerankDocument{{Text: "doc-0"}}
	items := []interface{}{
		map[string]interface{}{"index": 0, "score": 0.42},
	}

	response, err := ToBifrostRerankResponse(items, documents, false)
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Len(t, response.Results, 1)
	assert.Nil(t, response.Results[0].Document)
	assert.Equal(t, 0.42, response.Results[0].RelevanceScore)
}

func TestToBifrostRerankResponse_DuplicateIndices(t *testing.T) {
	t.Parallel()

	documents := []schemas.RerankDocument{{Text: "doc-0"}, {Text: "doc-1"}}
	items := []interface{}{
		map[string]interface{}{"index": 0, "score": 0.9},
		map[string]interface{}{"index": 0, "score": 0.8},
	}

	_, err := ToBifrostRerankResponse(items, documents, true)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "duplicate index"))
}

func TestToBifrostRerankResponse_OutOfRangeIndex(t *testing.T) {
	t.Parallel()

	documents := []schemas.RerankDocument{{Text: "doc-0"}}
	items := []interface{}{
		map[string]interface{}{"index": 1, "score": 0.9},
	}

	_, err := ToBifrostRerankResponse(items, documents, true)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "out of range"))
}

func TestToBifrostRerankResponse_MissingScore(t *testing.T) {
	t.Parallel()

	documents := []schemas.RerankDocument{{Text: "doc-0"}}
	items := []interface{}{
		map[string]interface{}{"index": 0},
	}

	_, err := ToBifrostRerankResponse(items, documents, false)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "score is required"))
}

func TestToBifrostRerankResponse_NilItems(t *testing.T) {
	t.Parallel()

	_, err := ToBifrostRerankResponse(nil, []schemas.RerankDocument{{Text: "d"}}, false)
	require.Error(t, err)
}

func TestToBifrostRerankResponse_EmptyResults(t *testing.T) {
	t.Parallel()

	response, err := ToBifrostRerankResponse([]interface{}{}, []schemas.RerankDocument{{Text: "d"}}, false)
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.Len(t, response.Results, 0)
}
