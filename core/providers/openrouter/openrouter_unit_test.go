package openrouter_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	provider "github.com/maximhq/bifrost/core/providers/openrouter"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type noopLogger struct{}

func (noopLogger) Debug(string, ...any)                   {}
func (noopLogger) Info(string, ...any)                    {}
func (noopLogger) Warn(string, ...any)                    {}
func (noopLogger) Error(string, ...any)                   {}
func (noopLogger) Fatal(string, ...any)                   {}
func (noopLogger) SetLevel(schemas.LogLevel)              {}
func (noopLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func TestChatCompletion_IncludesKeyLevelProviderConfig(t *testing.T) {
	t.Parallel()

	requestBody := runOpenRouterChatCompletion(t,
		schemas.Key{
			Value: *schemas.NewEnvVar("sk-openrouter"),
			OpenRouterKeyConfig: &schemas.OpenRouterKeyConfig{
				Provider: json.RawMessage(`{"order":["openai","anthropic"],"allow_fallbacks":false}`),
			},
		},
		&schemas.BifrostChatRequest{
			Provider: schemas.OpenRouter,
			Model:    "openai/gpt-4.1",
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
			}},
		},
	)

	providerValue, ok := requestBody["provider"].(map[string]any)
	require.True(t, ok, "request body should include provider object")
	assert.Equal(t, false, providerValue["allow_fallbacks"])
	assert.Equal(t, []any{"openai", "anthropic"}, providerValue["order"])
}

func TestChatCompletion_RequestProviderOverridesKeyConfig(t *testing.T) {
	t.Parallel()

	requestBody := runOpenRouterChatCompletion(t,
		schemas.Key{
			Value: *schemas.NewEnvVar("sk-openrouter"),
			OpenRouterKeyConfig: &schemas.OpenRouterKeyConfig{
				Provider: json.RawMessage(`{"order":["openai"],"allow_fallbacks":false}`),
			},
		},
		&schemas.BifrostChatRequest{
			Provider: schemas.OpenRouter,
			Model:    "openai/gpt-4.1",
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
			}},
			Params: &schemas.ChatParameters{
				ExtraParams: map[string]interface{}{
					"provider": map[string]interface{}{
						"order":           []string{"google"},
						"allow_fallbacks": true,
					},
				},
			},
		},
	)

	providerValue, ok := requestBody["provider"].(map[string]any)
	require.True(t, ok, "request body should include provider object")
	assert.Equal(t, true, providerValue["allow_fallbacks"])
	assert.Equal(t, []any{"google"}, providerValue["order"])
}

func TestChatCompletion_InvalidProviderJSON(t *testing.T) {
	t.Parallel()

	openRouterProvider := provider.NewOpenRouterProvider(&schemas.ProviderConfig{}, noopLogger{})
	request := &schemas.BifrostChatRequest{Provider: schemas.OpenRouter, Model: "openai/gpt-4.1"}
	key := schemas.Key{
		OpenRouterKeyConfig: &schemas.OpenRouterKeyConfig{
			Provider: json.RawMessage(`{"order":`),
		},
	}

	response, bifrostErr := openRouterProvider.ChatCompletion(schemas.NewBifrostContext(nil, schemas.NoDeadline), key, request)
	require.Nil(t, response)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	assert.Equal(t, schemas.ErrProviderRequestMarshal, bifrostErr.Error.Message)
	assert.Equal(t, schemas.OpenRouter, bifrostErr.ExtraFields.Provider)
}

func runOpenRouterChatCompletion(t *testing.T, key schemas.Key, request *schemas.BifrostChatRequest) map[string]any {
	t.Helper()

	bodyCh := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var requestBody map[string]any
		require.NoError(t, json.Unmarshal(body, &requestBody))
		bodyCh <- requestBody

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":123,"model":"openai/gpt-4.1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	openRouterProvider := provider.NewOpenRouterProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 5,
		},
	}, noopLogger{})

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	response, bifrostErr := openRouterProvider.ChatCompletion(ctx, key, request)
	require.Nil(t, bifrostErr)
	require.NotNil(t, response)

	return <-bodyCh
}
