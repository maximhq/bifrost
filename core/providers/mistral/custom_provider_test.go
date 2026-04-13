package mistral

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
	"github.com/valyala/fasthttp"
)

const customMistralProviderName = schemas.ModelProvider("custom-mistral")

func TestParseMistralError_UsesExportedConverterMetadata(t *testing.T) {
	t.Parallel()

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	resp.SetStatusCode(http.StatusBadRequest)
	resp.SetBodyString(`{"message":"invalid request","type":"invalid_request_error","code":"bad_request"}`)

	bifrostErr := ParseMistralError(resp)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)

	assert.Equal(t, "invalid request", bifrostErr.Error.Message)
	assert.Equal(t, schemas.Ptr("invalid_request_error"), bifrostErr.Error.Type)
	assert.Equal(t, schemas.Ptr("bad_request"), bifrostErr.Error.Code)
	assert.Equal(t, customMistralProviderName, bifrostErr.ExtraFields.Provider)
}

func TestMistralProvider_CustomAliasChatStreamUsesBaseCompatibilityAndAliasMetadata(t *testing.T) {
	t.Parallel()

	var capturedRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, sonic.Unmarshal(body, &capturedRequest))

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		_, err = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"mistral-small-latest\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"}}]}\n\n"))
		require.NoError(t, err)
		flusher.Flush()

		_, err = w.Write([]byte("data: [DONE]\n\n"))
		require.NoError(t, err)
		flusher.Flush()
	}))
	defer server.Close()

	provider := NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: string(customMistralProviderName),
			BaseProviderType:  schemas.Mistral,
		},
	}, &testLogger{})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyIsCustomProvider, true)

	request := &schemas.BifrostChatRequest{
		Provider: customMistralProviderName,
		Model:    "mistral-small-latest",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentStr: schemas.Ptr("hello"),
			},
		}},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: schemas.Ptr(32),
			ToolChoice: &schemas.ChatToolChoice{
				ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
					Type: schemas.ChatToolChoiceTypeFunction,
					Function: &schemas.ChatToolChoiceFunction{
						Name: "lookup",
					},
				},
			},
		},
	}

	postHookRunner := func(_ *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return response, err
	}

	stream, bifrostErr := provider.ChatCompletionStream(ctx, postHookRunner, schemas.Key{}, request)
	require.Nil(t, bifrostErr)

	var firstResponse *schemas.BifrostChatResponse
	for chunk := range stream {
		if chunk.BifrostError != nil {
			t.Fatalf("unexpected stream error: %s", chunk.BifrostError.Error.Message)
		}
		if chunk.BifrostChatResponse != nil {
			firstResponse = chunk.BifrostChatResponse
			break
		}
	}

	require.NotNil(t, firstResponse)
	assert.Equal(t, customMistralProviderName, firstResponse.ExtraFields.Provider)

	require.NotNil(t, capturedRequest)
	assert.Equal(t, float64(32), capturedRequest["max_tokens"])
	assert.NotContains(t, capturedRequest, "max_completion_tokens")
	assert.Equal(t, "any", capturedRequest["tool_choice"])
	assert.Equal(t, "mistral-small-latest", capturedRequest["model"])
	assert.Equal(t, true, capturedRequest["stream"])
	assert.Equal(t, customMistralProviderName, provider.GetProviderKey())
	assert.Equal(t, customMistralProviderName, request.Provider)
}

func TestMistralProvider_CustomAliasEmbeddingReportsAliasMetadata(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"codestral-embed","usage":{"prompt_tokens":1,"total_tokens":1}}`))
		require.NoError(t, err)
	}))
	defer server.Close()

	provider := NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: string(customMistralProviderName),
			BaseProviderType:  schemas.Mistral,
		},
	}, &testLogger{})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostEmbeddingRequest{
		Provider: customMistralProviderName,
		Model:    "codestral-embed",
		Input: &schemas.EmbeddingInput{
			Texts: []string{"hello"},
		},
	}

	response, bifrostErr := provider.Embedding(ctx, schemas.Key{}, request)
	require.Nil(t, bifrostErr)
	require.NotNil(t, response)

	assert.Equal(t, customMistralProviderName, response.ExtraFields.Provider)
	assert.Equal(t, "codestral-embed", response.ExtraFields.ResolvedModelUsed)
}
