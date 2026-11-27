package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToGeminiModelResourceName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "already native", input: "models/gemini-2.5-pro", want: "models/gemini-2.5-pro"},
		{name: "provider prefixed", input: "gemini/gemini-2.5-pro", want: "models/gemini-2.5-pro"},
		{name: "bare model", input: "gemini-2.5-pro", want: "models/gemini-2.5-pro"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, toGeminiModelResourceName(tc.input))
		})
	}
}

func TestToGeminiListModelsResponse_UsesNativeModelResourceName(t *testing.T) {
	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{
			{ID: "gemini/gemini-2.5-pro"},
			{ID: "models/gemini-2.5-flash"},
		},
	}

	converted := ToGeminiListModelsResponse(resp)
	if assert.Len(t, converted.Models, 2) {
		assert.Equal(t, "models/gemini-2.5-pro", converted.Models[0].Name)
		assert.Equal(t, "models/gemini-2.5-flash", converted.Models[1].Name)
	}
}

func TestNormalizeModelName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "strips google prefix for gemini model",
			input: "google/gemini-2.5-pro",
			want:  "gemini-2.5-pro",
		},
		{
			name:  "strips google prefix for veo model",
			input: "google/veo-3.0-generate-preview",
			want:  "veo-3.0-generate-preview",
		},
		{
			name:  "strips google prefix for imagen model",
			input: "google/imagen-4.0-generate-001",
			want:  "imagen-4.0-generate-001",
		},
		{
			name:  "strips google prefix for gemma model",
			input: "google/gemma-3-27b-it",
			want:  "gemma-3-27b-it",
		},
		{
			name:  "trims spaces before normalizing",
			input: "  google/gemini-2.5-flash  ",
			want:  "gemini-2.5-flash",
		},
		{
			name:  "keeps unknown google model unchanged",
			input: "google/custom-model",
			want:  "google/custom-model",
		},
		{
			name:  "keeps non google prefixed model unchanged",
			input: "openai/gpt-4o",
			want:  "openai/gpt-4o",
		},
		{
			name:  "matches google prefix case-insensitively",
			input: "Google/gemini-2.5-flash",
			want:  "gemini-2.5-flash",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NormalizeModelName(tc.input))
		})
	}
}

// A retrieve must call models.get with x-goog-api-key, accept the native "models/{id}"
// resource name, map token limits onto the Bifrost shape, surface an upstream 404, and
// refuse a path-shaping id before anything is dispatched.
func TestModelRetrieve(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotAPIKey = r.URL.Path, r.Header.Get("x-goog-api-key")
		mu.Unlock()
		if r.URL.Path == "/models/nope" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"models/nope is not found","status":"NOT_FOUND"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro","description":"test","inputTokenLimit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["generateContent","countTokens"],"thinking":true}`))
	}))
	defer server.Close()

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
	}, testNoopLogger{})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("gemini-test")}

	for _, requested := range []string{"gemini-2.5-pro", "models/gemini-2.5-pro"} {
		response, bifrostErr := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: requested})
		require.Nil(t, bifrostErr, requested)

		mu.Lock()
		path, apiKey := gotPath, gotAPIKey
		mu.Unlock()
		assert.Equal(t, "/models/gemini-2.5-pro", path, requested)
		assert.Equal(t, "gemini-test", apiKey, requested)

		assert.Equal(t, "gemini/gemini-2.5-pro", response.ID)
		assert.Equal(t, schemas.Ptr("Gemini 2.5 Pro"), response.Name)
		assert.Equal(t, schemas.Ptr(1048576), response.ContextLength, "context window is the input limit, not input+output")
		assert.Equal(t, schemas.Ptr(1048576), response.MaxInputTokens)
		assert.Equal(t, schemas.Ptr(65536), response.MaxOutputTokens)
		assert.Equal(t, []string{"generateContent", "countTokens"}, response.SupportedMethods)
	}

	_, bifrostErr := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "nope"})
	require.NotNil(t, bifrostErr)
	assert.Equal(t, schemas.Ptr(http.StatusNotFound), bifrostErr.StatusCode)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "../models"})
	require.NotNil(t, bifrostErr)
	assert.Equal(t, schemas.Ptr(http.StatusBadRequest), bifrostErr.StatusCode)
}
