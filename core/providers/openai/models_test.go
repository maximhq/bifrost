package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestOpenAIListModelsResponseUnmarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantObject string
		wantModels []OpenAIModel
	}{
		{
			name: "OpenAI object response",
			body: `{
				"object": "list",
				"data": [
					{"id": "gpt-5", "object": "model", "owned_by": "openai", "created": 123}
				]
			}`,
			wantObject: "list",
			wantModels: []OpenAIModel{
				{ID: "gpt-5", Object: "model", OwnedBy: "openai", Created: int64Pointer(123)},
			},
		},
		{
			name: "compatible top-level array response",
			body: `[
				{"id": "zai-org/GLM-5.2", "object": "model", "owned_by": "together"},
				{"id": "deepseek-ai/DeepSeek-V4-Flash-0731", "object": "model", "owned_by": "together", "context_window": 1000000}
			]`,
			wantModels: []OpenAIModel{
				{ID: "zai-org/GLM-5.2", Object: "model", OwnedBy: "together"},
				{ID: "deepseek-ai/DeepSeek-V4-Flash-0731", Object: "model", OwnedBy: "together", ContextWindow: intPointer(1000000)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var response OpenAIListModelsResponse
			err := sonic.Unmarshal([]byte(tt.body), &response)

			require.NoError(t, err)
			assert.Equal(t, tt.wantObject, response.Object)
			assert.Equal(t, tt.wantModels, response.Data)
		})
	}
}

func TestOpenAIListModelsResponseUnmarshalJSONRejectsInvalidShapes(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`"invalid"`,
		`123`,
		`null`,
		`{}`,
		`{"error":{"message":"provider error"}}`,
		`{"data":null}`,
		`{"data":{}}`,
	} {
		var response OpenAIListModelsResponse
		err := sonic.Unmarshal([]byte(body), &response)
		require.Error(t, err, "body: %s", body)
	}
}

func TestListModelsByKeyAcceptsTopLevelArrayResponse(t *testing.T) {
	t.Parallel()

	responseBody := `[
		{"id": "zai-org/GLM-5.2", "object": "model", "owned_by": "together"},
		{"id": "deepseek-ai/DeepSeek-V4-Flash-0731", "object": "model", "owned_by": "together", "context_window": 1000000}
	]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "Bearer test-key", request.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	response, bifrostErr := ListModelsByKey(
		ctx,
		&fasthttp.Client{},
		server.URL,
		schemas.Key{
			Value:  *schemas.NewSecretVar("test-key"),
			Models: schemas.WhiteList{"*"},
		},
		false,
		nil,
		schemas.ModelProvider("compatible"),
		false,
		true,
	)

	require.Nil(t, bifrostErr)
	require.NotNil(t, response)
	require.Len(t, response.Data, 2)
	assert.Equal(t, "compatible/zai-org/GLM-5.2", response.Data[0].ID)
	assert.Equal(t, "compatible/deepseek-ai/DeepSeek-V4-Flash-0731", response.Data[1].ID)
	assert.Equal(t, intPointer(1000000), response.Data[1].ContextLength)
	require.IsType(t, json.RawMessage{}, response.ExtraFields.RawResponse)
	assert.JSONEq(t, responseBody, string(response.ExtraFields.RawResponse.(json.RawMessage)))
}

func int64Pointer(value int64) *int64 {
	return &value
}

func intPointer(value int) *int {
	return &value
}
