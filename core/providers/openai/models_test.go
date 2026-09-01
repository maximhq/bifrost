package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestListModelsByKeyAcceptsTogetherResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "/v1/models", request.URL.Path)
		require.Equal(t, "Bearer test-key", request.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`[{"id":"zai-org/GLM-5.2","object":"model","created":456,"type":"chat","display_name":"GLM-5.2","organization":"Z.ai","context_length":131072,"pricing":{"input":0.8,"output":2.56}}]`))
		require.NoError(t, err)
	}))
	defer server.Close()

	response, bifrostErr := ListModelsByKey(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		&fasthttp.Client{},
		server.URL+"/v1/models",
		schemas.Key{Value: *schemas.NewSecretVar("test-key"), Models: schemas.WhiteList{"*"}},
		false,
		nil,
		schemas.ModelProvider("together"),
		false,
		false,
	)

	require.Nil(t, bifrostErr)
	require.Len(t, response.Data, 1)
	require.Equal(t, "together/zai-org/GLM-5.2", response.Data[0].ID)
	require.Equal(t, schemas.Ptr("Z.ai"), response.Data[0].OwnedBy)
	require.Equal(t, schemas.Ptr(131072), response.Data[0].ContextLength)
}

func TestOpenAICompatibleListModelsResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want *OpenAIListModelsResponse
	}{
		{
			name: "OpenAI envelope",
			body: `{"object":"list","data":[{"id":"gpt-5","object":"model","owned_by":"openai","created":123,"context_window":128000}]}`,
			want: &OpenAIListModelsResponse{
				Object: "list",
				Data: []OpenAIModel{{
					ID:            "gpt-5",
					Object:        "model",
					OwnedBy:       "openai",
					Created:       schemas.Ptr(int64(123)),
					ContextWindow: schemas.Ptr(128000),
				}},
			},
		},
		{
			name: "Together array",
			body: `[{"id":"zai-org/GLM-5.2","object":"model","created":456,"type":"chat","display_name":"GLM-5.2","organization":"Z.ai","context_length":131072,"pricing":{"input":0.8,"output":2.56}}]`,
			want: &OpenAIListModelsResponse{
				Data: []OpenAIModel{{
					ID:            "zai-org/GLM-5.2",
					Object:        "model",
					OwnedBy:       "Z.ai",
					Created:       schemas.Ptr(int64(456)),
					ContextWindow: schemas.Ptr(131072),
				}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var wireResponse openAICompatibleListModelsResponse
			require.NoError(t, sonic.Unmarshal([]byte(test.body), &wireResponse))
			require.Equal(t, test.want, wireResponse.toOpenAIListModelsResponse())
		})
	}
}

func TestOpenAICompatibleListModelsResponsePreservesObjectDecoding(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`{}`, `null`, `{"data":null}`} {
		var canonical OpenAIListModelsResponse
		canonicalErr := sonic.Unmarshal([]byte(body), &canonical)

		var compatible openAICompatibleListModelsResponse
		compatibleErr := sonic.Unmarshal([]byte(body), &compatible)

		require.Equal(t, canonicalErr == nil, compatibleErr == nil, "body: %s", body)
		if canonicalErr == nil {
			require.Equal(t, canonical, *compatible.toOpenAIListModelsResponse(), "body: %s", body)
		}
	}
}
