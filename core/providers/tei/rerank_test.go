package tei

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRerankToTEIRerankRequest(t *testing.T) {
	returnDocuments := true
	topN := 1
	maxTokensPerDoc := 128
	priority := 5
	req := ToTEIRerankRequest(&schemas.BifrostRerankRequest{
		Query: "capital of france",
		Documents: []schemas.RerankDocument{
			{Text: "Paris is the capital of France."},
			{Text: "Berlin is the capital of Germany."},
		},
		Params: &schemas.RerankParameters{
			TopN:            &topN,
			MaxTokensPerDoc: &maxTokensPerDoc,
			Priority:        &priority,
			ReturnDocuments: &returnDocuments,
			ExtraParams: map[string]interface{}{
				"truncate": true,
			},
		},
	})

	require.NotNil(t, req)
	assert.Equal(t, "capital of france", req.Query)
	assert.Equal(t, []string{"Paris is the capital of France.", "Berlin is the capital of Germany."}, req.Texts)
	require.NotNil(t, req.TopN)
	assert.Equal(t, 1, *req.TopN)
	require.NotNil(t, req.MaxTokensPerDoc)
	assert.Equal(t, 128, *req.MaxTokensPerDoc)
	require.NotNil(t, req.Priority)
	assert.Equal(t, 5, *req.Priority)
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
	}, documents, true, nil)

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
	_, err := ToBifrostRerankResponse([]teiRank{{Index: 1, Score: 0.5}}, []schemas.RerankDocument{{Text: "doc"}}, false, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

func TestRerankToBifrostRerankResponseTopN(t *testing.T) {
	topN := 1
	resp, err := ToBifrostRerankResponse([]teiRank{
		{Index: 0, Score: 0.1},
		{Index: 1, Score: 0.9},
	}, []schemas.RerankDocument{{Text: "doc-0"}, {Text: "doc-1"}}, false, &topN)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, 1, resp.Results[0].Index)
}

func TestTEIProviderRerank(t *testing.T) {
	var upstreamBody string
	var upstreamPath string
	var upstreamMethod string
	var upstreamAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		upstreamMethod = r.Method
		upstreamAuthorization = r.Header.Get("Authorization")

		buf, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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
	largePayloadReader := strings.NewReader(`{"model":"tei/BAAI/bge-reranker-v2-m3","documents":[{"text":"raw"}]}`)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
	ctx.SetValue(
		schemas.BifrostContextKeyLargePayloadReader,
		largePayloadReader,
	)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentLength, 70)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentType, "application/json")
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
	assert.Equal(t, "/rerank", upstreamPath)
	assert.Equal(t, http.MethodPost, upstreamMethod)
	assert.Equal(t, "Bearer test-key", upstreamAuthorization)
	assert.Contains(t, upstreamBody, `"texts"`)
	assert.NotContains(t, upstreamBody, `"documents"`)
	assert.NotContains(t, upstreamBody, `"model"`)
	assert.Equal(t, "BAAI/bge-reranker-v2-m3", resp.Model)
	require.Len(t, resp.Results, 2)
	assert.Equal(t, 0.99, resp.Results[0].RelevanceScore)
	assert.NotNil(t, resp.ExtraFields.RawRequest)
	assert.NotNil(t, resp.ExtraFields.RawResponse)
	remainingBody, err := io.ReadAll(largePayloadReader)
	require.NoError(t, err)
	assert.Empty(t, remainingBody)
}

func TestTEIProviderRerankDisabled(t *testing.T) {
	var upstreamCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider, err := NewTEIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			AllowedRequests: &schemas.AllowedRequests{
				Embedding: true,
			},
		},
	}, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Rerank(ctx, schemas.Key{}, &schemas.BifrostRerankRequest{
		Model: "BAAI/bge-reranker-v2-m3",
		Query: "capital",
		Documents: []schemas.RerankDocument{
			{Text: "Paris"},
			{Text: "Berlin"},
		},
	})

	require.Nil(t, resp)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "not supported")
	assert.Equal(t, 0, upstreamCalls)
}

func TestTEIProviderBuildRequestURLUsesPathOverride(t *testing.T) {
	provider, err := NewTEIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: "http://localhost:8080",
		},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			RequestPathOverrides: map[schemas.RequestType]string{
				schemas.RerankRequest:    "custom/rerank",
				schemas.EmbeddingRequest: "http://tei.example.com/embed",
			},
		},
	}, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	assert.Equal(t, "http://localhost:8080/custom/rerank", provider.buildRequestURL(ctx, "/rerank", schemas.RerankRequest))
	assert.Equal(t, "http://tei.example.com/embed", provider.buildRequestURL(ctx, "/v1/embeddings", schemas.EmbeddingRequest))
}

func TestTEIProviderEmbedding(t *testing.T) {
	var upstreamPath string
	var upstreamBody string
	var upstreamCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		upstreamPath = r.URL.Path
		buf, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		upstreamBody = string(buf)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","model":"thenlper/gte-base","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`))
	}))
	defer server.Close()

	provider, err := NewTEIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
	}, nil)
	require.NoError(t, err)

	text := "What is Deep Learning?"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Embedding(ctx, schemas.Key{}, &schemas.BifrostEmbeddingRequest{
		Model: "thenlper/gte-base",
		Input: &schemas.EmbeddingInput{Text: &text},
	})

	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)
	assert.Equal(t, "/v1/embeddings", upstreamPath)
	assert.Contains(t, upstreamBody, `"input"`)
	assert.Equal(t, "thenlper/gte-base", resp.Model)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, []float64{0.1, 0.2}, resp.Data[0].Embedding.EmbeddingArray)
	assert.Equal(t, 1, upstreamCalls)
}

func TestTEIProviderEmbeddingDisabled(t *testing.T) {
	var upstreamCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider, err := NewTEIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
		},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			AllowedRequests: &schemas.AllowedRequests{
				Rerank: true,
			},
		},
	}, nil)
	require.NoError(t, err)

	text := "What is Deep Learning?"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Embedding(ctx, schemas.Key{}, &schemas.BifrostEmbeddingRequest{
		Model: "thenlper/gte-base",
		Input: &schemas.EmbeddingInput{Text: &text},
	})

	require.Nil(t, resp)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "not supported")
	assert.Equal(t, 0, upstreamCalls)
}
