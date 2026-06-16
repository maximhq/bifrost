package tei

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRerankToTEIRerankRequest(t *testing.T) {
	returnDocuments := true
	req := ToTEIRerankRequest(&schemas.BifrostRerankRequest{
		Query: "capital of france",
		Documents: []schemas.RerankDocument{
			{Text: "Paris is the capital of France."},
			{Text: "Berlin is the capital of Germany."},
		},
		Params: &schemas.RerankParameters{
			ReturnDocuments: &returnDocuments,
			ExtraParams: map[string]interface{}{
				"truncate": true,
			},
		},
	})

	require.NotNil(t, req)
	assert.Equal(t, "capital of france", req.Query)
	assert.Equal(t, []string{"Paris is the capital of France.", "Berlin is the capital of Germany."}, req.Texts)
	require.NotNil(t, req.ReturnText)
	assert.True(t, *req.ReturnText)
	assert.Equal(t, true, req.ExtraParams["truncate"])
}

func TestRerankToBifrostRerankResponse(t *testing.T) {
	documents := []schemas.RerankDocument{
		{Text: "doc-0"},
		{Text: "doc-1"},
	}

	resp, err := ToBifrostRerankResponse([]teiRank{
		{Index: 1, Score: 0.1},
		{Index: 0, Score: 0.9},
	}, documents, true)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Results, 2)
	assert.Equal(t, 0, resp.Results[0].Index)
	assert.Equal(t, 0.9, resp.Results[0].RelevanceScore)
	require.NotNil(t, resp.Results[0].Document)
	assert.Equal(t, "doc-0", resp.Results[0].Document.Text)
	assert.Equal(t, 1, resp.Results[1].Index)
	assert.Equal(t, 0.1, resp.Results[1].RelevanceScore)
}

func TestRerankToBifrostRerankResponseOutOfRange(t *testing.T) {
	_, err := ToBifrostRerankResponse([]teiRank{{Index: 1, Score: 0.5}}, []schemas.RerankDocument{{Text: "doc"}}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

func TestTEIProviderRerank(t *testing.T) {
	var upstreamBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/rerank", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		buf, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		upstreamBody = string(buf)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"index":0,"score":0.99},{"index":1,"score":0.01}]`))
	}))
	defer server.Close()

	provider, err := NewTEIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
		SendBackRawRequest:  true,
		SendBackRawResponse: true,
	}, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Rerank(ctx, schemas.Key{
		Value: *schemas.NewEnvVar("test-key"),
	}, &schemas.BifrostRerankRequest{
		Model: "BAAI/bge-reranker-v2-m3",
		Query: "capital",
		Documents: []schemas.RerankDocument{
			{Text: "Paris"},
			{Text: "Berlin"},
		},
	})

	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)
	assert.Contains(t, upstreamBody, `"texts"`)
	assert.NotContains(t, upstreamBody, `"documents"`)
	assert.NotContains(t, upstreamBody, `"model"`)
	assert.Equal(t, "BAAI/bge-reranker-v2-m3", resp.Model)
	require.Len(t, resp.Results, 2)
	assert.Equal(t, 0.99, resp.Results[0].RelevanceScore)
	assert.NotNil(t, resp.ExtraFields.RawRequest)
	assert.NotNil(t, resp.ExtraFields.RawResponse)
}
