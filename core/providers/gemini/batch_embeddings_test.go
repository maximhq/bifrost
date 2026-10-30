package gemini

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const embedBatchCreateResponse = `{"name":"batches/x","metadata":{"name":"batches/x","state":"BATCH_STATE_PENDING","createTime":"2026-07-01T00:00:00Z","batchStats":{"requestCount":"2","pendingRequestCount":"2"}}}`

// newEmbedBatchTestProvider returns a provider pointed at a stub that records the path and body of the create call.
func newEmbedBatchTestProvider(t *testing.T) (*GeminiProvider, *string, *[]byte) {
	t.Helper()
	var gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(embedBatchCreateResponse))
	}))
	t.Cleanup(ts.Close)
	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: ts.URL + "/v1beta"},
	}, testNoopLogger{})
	return provider, &gotPath, &gotBody
}

func TestGeminiBatchCreateEmbeddingsFileInput(t *testing.T) {
	t.Parallel()
	provider, gotPath, gotBody := newEmbedBatchTestProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	resp, err := provider.BatchCreate(ctx, schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}, &schemas.BifrostBatchCreateRequest{
		Provider:    schemas.Gemini,
		Model:       schemas.Ptr("gemini/gemini-embedding-2"),
		Endpoint:    schemas.BatchEndpointEmbeddings,
		InputFileID: "abc123",
		DisplayName: schemas.Ptr("titles"),
	})
	require.Nil(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "/v1beta/models/gemini-embedding-2:asyncBatchEmbedContent", *gotPath)
	assert.JSONEq(t, `{"batch":{"displayName":"titles","inputConfig":{"fileName":"files/abc123"}}}`, string(*gotBody))
	assert.Equal(t, "batches/x", resp.ID)
}

func TestGeminiBatchCreateEmbeddingsInlineRequests(t *testing.T) {
	t.Parallel()
	provider, gotPath, gotBody := newEmbedBatchTestProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, err := provider.BatchCreate(ctx, schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}, &schemas.BifrostBatchCreateRequest{
		Provider: schemas.Gemini,
		Model:    schemas.Ptr("gemini-embedding-2"),
		Endpoint: schemas.BatchEndpointEmbeddings,
		Requests: []schemas.BatchRequestItem{
			{CustomID: "p0", Body: map[string]interface{}{"input": "red shoes", "dimensions": float64(768)}},
			{CustomID: "p1", Body: map[string]interface{}{"input": []interface{}{"blue hat"}}},
		},
		DisplayName: schemas.Ptr("titles"),
	})
	require.Nil(t, err)

	assert.Equal(t, "/v1beta/models/gemini-embedding-2:asyncBatchEmbedContent", *gotPath)
	var sent struct {
		Batch struct {
			DisplayName string `json:"displayName"`
			InputConfig struct {
				Requests struct {
					Requests []struct {
						Request  GeminiEmbeddingRequest `json:"request"`
						Metadata struct {
							Key string `json:"key"`
						} `json:"metadata"`
					} `json:"requests"`
				} `json:"requests"`
			} `json:"inputConfig"`
		} `json:"batch"`
	}
	require.NoError(t, sonic.Unmarshal(*gotBody, &sent))
	assert.Equal(t, "titles", sent.Batch.DisplayName)
	items := sent.Batch.InputConfig.Requests.Requests
	require.Len(t, items, 2)
	assert.Equal(t, "p0", items[0].Metadata.Key)
	assert.Equal(t, "red shoes", items[0].Request.Content.Parts[0].Text)
	require.NotNil(t, items[0].Request.OutputDimensionality)
	assert.Equal(t, 768, *items[0].Request.OutputDimensionality)
	assert.Equal(t, "p1", items[1].Metadata.Key)
	assert.Equal(t, "blue hat", items[1].Request.Content.Parts[0].Text)
	assert.Nil(t, items[1].Request.OutputDimensionality)
}

func TestGeminiBatchCreateEmbeddingsRequiresModel(t *testing.T) {
	t.Parallel()
	provider, gotPath, _ := newEmbedBatchTestProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, err := provider.BatchCreate(ctx, schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}, &schemas.BifrostBatchCreateRequest{
		Provider:    schemas.Gemini,
		Endpoint:    schemas.BatchEndpointEmbeddings,
		InputFileID: "abc123",
	})
	require.NotNil(t, err)
	assert.Contains(t, err.Error.Message, "model is required")
	assert.Empty(t, *gotPath, "no request should reach Gemini")
}

func TestGeminiBatchCreateChatStillUsesBatchGenerateContent(t *testing.T) {
	t.Parallel()
	provider, gotPath, _ := newEmbedBatchTestProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, _ = provider.BatchCreate(ctx, schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}, &schemas.BifrostBatchCreateRequest{
		Provider:    schemas.Gemini,
		Model:       schemas.Ptr("gemini-2.5-flash"),
		Endpoint:    schemas.BatchEndpointChatCompletions,
		InputFileID: "abc123",
	})
	assert.Equal(t, "/v1beta/models/gemini-2.5-flash:batchGenerateContent", *gotPath)
}

func TestToGeminiBatchEmbeddingRequest(t *testing.T) {
	t.Parallel()

	t.Run("NativeContentPassesThrough", func(t *testing.T) {
		req, err := ToGeminiBatchEmbeddingRequest(map[string]interface{}{
			"content":              map[string]interface{}{"parts": []interface{}{map[string]interface{}{"text": "hi"}}},
			"outputDimensionality": float64(256),
			"taskType":             "RETRIEVAL_DOCUMENT",
		})
		require.NoError(t, err)
		require.NotNil(t, req.Content)
		assert.Equal(t, "hi", req.Content.Parts[0].Text)
		require.NotNil(t, req.OutputDimensionality)
		assert.Equal(t, 256, *req.OutputDimensionality)
		require.NotNil(t, req.TaskType)
		assert.Equal(t, "RETRIEVAL_DOCUMENT", *req.TaskType)
	})

	t.Run("RejectsMultipleInputsPerLine", func(t *testing.T) {
		_, err := ToGeminiBatchEmbeddingRequest(map[string]interface{}{"input": []interface{}{"a", "b"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exactly one input")
	})

	t.Run("RejectsMissingInput", func(t *testing.T) {
		_, err := ToGeminiBatchEmbeddingRequest(map[string]interface{}{"dimensions": float64(8)})
		require.Error(t, err)
	})
}

func TestGeminiEmbeddingBatchResults(t *testing.T) {
	t.Parallel()

	t.Run("FileLine", func(t *testing.T) {
		var line GeminiBatchFileResultLine
		require.NoError(t, sonic.Unmarshal([]byte(`{"key":"p0","response":{"embedding":{"values":[0.5,-0.25,1]}}}`), &line))
		item, err := geminiFileResultToBatchResultItem(line, "fallback")
		require.NoError(t, err)
		assert.Equal(t, "p0", item.CustomID)
		require.NotNil(t, item.Response)
		assert.Equal(t, 200, item.Response.StatusCode)
		assert.Equal(t, "list", item.Response.Body["object"])
		data, ok := item.Response.Body["data"].([]map[string]interface{})
		require.True(t, ok)
		require.Len(t, data, 1)
		assert.Equal(t, []float64{0.5, -0.25, 1}, data[0]["embedding"])
		assert.Equal(t, 0, data[0]["index"])
	})

	t.Run("InlineResponse", func(t *testing.T) {
		var inline GeminiInlinedResponse
		require.NoError(t, sonic.Unmarshal([]byte(`{"metadata":{"key":"p1"},"response":{"embedding":{"values":[1,2]}}}`), &inline))
		require.NotNil(t, inline.Embedding)
		item := geminiInlineResponseToBatchResultItem(inline, "fallback")
		assert.Equal(t, "p1", item.CustomID)
		require.NotNil(t, item.Response)
		data, ok := item.Response.Body["data"].([]map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, []float64{1, 2}, data[0]["embedding"])
	})

	t.Run("InlineGenerateContentUnchanged", func(t *testing.T) {
		var inline GeminiInlinedResponse
		require.NoError(t, sonic.Unmarshal([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}]}}`), &inline))
		assert.Nil(t, inline.Embedding)
		item := geminiInlineResponseToBatchResultItem(inline, "r0")
		require.NotNil(t, item.Response)
		assert.Equal(t, "hello", item.Response.Body["text"])
	})

	t.Run("InlineError", func(t *testing.T) {
		var inline GeminiInlinedResponse
		require.NoError(t, sonic.Unmarshal([]byte(`{"metadata":{"key":"p2"},"error":{"code":400,"message":"bad"}}`), &inline))
		item := geminiInlineResponseToBatchResultItem(inline, "r0")
		require.NotNil(t, item.Error)
		assert.Equal(t, "400", item.Error.Code)
	})
}
