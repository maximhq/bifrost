package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestListModelsByKeyResponseShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		wantID      string
		wantOwnedBy string
		wantContext int
	}{
		{
			name:        "OpenAI envelope",
			body:        `{"object":"list","data":[{"id":"gpt-5","owned_by":"openai","context_window":128000}]}`,
			wantID:      "test/gpt-5",
			wantOwnedBy: "openai",
			wantContext: 128000,
		},
		{
			name:        "Together array",
			body:        `[{"id":"zai-org/GLM-5.2","organization":"Z.ai","context_length":131072}]`,
			wantID:      "test/zai-org/GLM-5.2",
			wantOwnedBy: "Z.ai",
			wantContext: 131072,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			response, bifrostErr := ListModelsByKey(
				schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
				&fasthttp.Client{},
				server.URL,
				schemas.Key{Models: schemas.WhiteList{"*"}},
				false,
				nil,
				schemas.ModelProvider("test"),
				false,
				false,
			)

			require.Nil(t, bifrostErr)
			require.Len(t, response.Data, 1)
			require.Equal(t, test.wantID, response.Data[0].ID)
			require.Equal(t, schemas.Ptr(test.wantOwnedBy), response.Data[0].OwnedBy)
			require.Equal(t, schemas.Ptr(test.wantContext), response.Data[0].ContextLength)
		})
	}
}

func TestModelRetrieveResponseShape(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"gpt-6-astra","object":"model","created":1686935002,"owned_by":"openai","shutdown_date":"2026-10-23"}`))
	}))
	defer server.Close()

	response, bifrostErr := HandleOpenAIModelRetrieveRequest(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		&fasthttp.Client{},
		server.URL+"/v1/models/gpt-6-astra",
		schemas.Key{},
		nil,
		schemas.ModelProvider("test"),
		false,
		false,
	)

	require.Nil(t, bifrostErr)
	require.Equal(t, "test/gpt-6-astra", response.ID)
	require.Equal(t, schemas.Ptr("openai"), response.OwnedBy)
	require.Equal(t, schemas.Ptr(int64(1686935002)), response.Created)
	require.Equal(t, schemas.Ptr("2026-10-23"), response.ShutdownDate)
}

func TestListModelsMetadataPassthrough(t *testing.T) {
	t.Parallel()

	// The same model described twice — OpenAI envelope vs top-level array —
	// must yield identical catalog entries, metadata included.
	const envelope = `{"object":"list","data":[{` +
		`"id":"kimi/k3",` +
		`"display_name":"K3",` +
		`"context_length":1048576,` +
		`"max_input_tokens":1040500,` +
		`"max_output_tokens":131072,` +
		`"reasoning":{"mandatory":false,"default_enabled":true,"supported_efforts":["low","high"],"default_effort":"high"},` +
		`"modalities":{"input":["text","image"],"output":["text"]}}]}`
	const array = `[{` +
		`"id":"kimi/k3",` +
		`"display_name":"K3",` +
		`"context_length":1048576,` +
		`"max_input_tokens":1040500,` +
		`"max_output_tokens":131072,` +
		`"reasoning":{"mandatory":false,"default_enabled":true,"supported_efforts":["low","high"],"default_effort":"high"},` +
		`"modalities":{"input":["text","image"],"output":["text"]}}]`

	for name, body := range map[string]string{"envelope": envelope, "array": array} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			response, bifrostErr := ListModelsByKey(
				schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
				&fasthttp.Client{}, server.URL,
				schemas.Key{Models: schemas.WhiteList{"*"}},
				false, nil,
				schemas.ModelProvider("test"),
				false, false,
			)

			require.Nil(t, bifrostErr)
			require.Len(t, response.Data, 1)
			entry := response.Data[0]
			require.Equal(t, "test/kimi/k3", entry.ID)
			require.Equal(t, schemas.Ptr(1048576), entry.ContextLength)
			require.Equal(t, schemas.Ptr(1040500), entry.MaxInputTokens)
			require.Equal(t, schemas.Ptr(131072), entry.MaxOutputTokens)
			require.Equal(t, schemas.Ptr("K3"), entry.DisplayName)
			require.Equal(t, &schemas.ModelReasoning{
				Mandatory:        schemas.Ptr(false),
				DefaultEnabled:   schemas.Ptr(true),
				SupportedEfforts: []string{"low", "high"},
				DefaultEffort:    schemas.Ptr("high"),
			}, entry.Reasoning)
			require.Equal(t, &schemas.Architecture{
				InputModalities:  []string{"text", "image"},
				OutputModalities: []string{"text"},
			}, entry.Architecture)
		})
	}
}

func TestListModelsContextWindowAliasPrecedence(t *testing.T) {
	t.Parallel()

	// Groq-style `context_window` wins over `context_length` when both appear.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m","context_window":8192,"context_length":4096}]}`))
	}))
	defer server.Close()

	response, bifrostErr := ListModelsByKey(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		&fasthttp.Client{}, server.URL,
		schemas.Key{Models: schemas.WhiteList{"*"}},
		false, nil,
		schemas.ModelProvider("test"),
		false, false,
	)

	require.Nil(t, bifrostErr)
	require.Len(t, response.Data, 1)
	require.Equal(t, schemas.Ptr(8192), response.Data[0].ContextLength)
}

func TestModelRetrieveMetadataPassthrough(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"kimi/k3","display_name":"K3","context_length":1048576,"max_output_tokens":131072,` +
			`"reasoning":{"supported_efforts":["low","high"]},"modalities":{"input":["text"],"output":["text"]}}`))
	}))
	defer server.Close()

	response, bifrostErr := HandleOpenAIModelRetrieveRequest(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		&fasthttp.Client{}, server.URL+"/v1/models/kimi/k3",
		schemas.Key{}, nil,
		schemas.ModelProvider("test"),
		false, false,
	)

	require.Nil(t, bifrostErr)
	require.Equal(t, "test/kimi/k3", response.ID)
	require.Equal(t, schemas.Ptr(1048576), response.ContextLength)
	require.Equal(t, schemas.Ptr(131072), response.MaxOutputTokens)
	require.Equal(t, schemas.Ptr("K3"), response.DisplayName)
	require.Equal(t, &schemas.ModelReasoning{SupportedEfforts: []string{"low", "high"}}, response.Reasoning)
	require.Equal(t, &schemas.Architecture{InputModalities: []string{"text"}, OutputModalities: []string{"text"}}, response.Architecture)
}

func TestModelRetrieveUpstreamErrorIsPropagated(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"The model 'nope' does not exist","type":"invalid_request_error","code":"model_not_found"}}`))
	}))
	defer server.Close()

	response, bifrostErr := HandleOpenAIModelRetrieveRequest(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		&fasthttp.Client{},
		server.URL+"/v1/models/nope",
		schemas.Key{},
		nil,
		schemas.ModelProvider("test"),
		false,
		false,
	)

	require.Nil(t, response)
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusNotFound), bifrostErr.StatusCode)
	require.Contains(t, bifrostErr.Error.Message, "does not exist")
}
