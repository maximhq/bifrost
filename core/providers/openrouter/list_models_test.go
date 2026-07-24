package openrouter_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openrouter"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// modelsJSON returns a minimal OpenRouter-shaped /v1/models (or /v1/embeddings/models)
// response listing the given model IDs (no "openrouter/" prefix, matching OpenRouter's wire format).
func modelsJSON(ids ...string) string {
	data := ""
	for i, id := range ids {
		if i > 0 {
			data += ","
		}
		data += fmt.Sprintf(`{"id":%q,"canonical_slug":%q}`, id, id)
	}
	return fmt.Sprintf(`{"data":[%s]}`, data)
}

func newTestOpenRouterProvider(baseURL string) *openrouter.OpenRouterProvider {
	return openrouter.NewOpenRouterProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
}

// TestListModels_MergesEmbeddingsCatalog verifies that OpenRouter's separate
// /v1/embeddings/models catalog (embedding models aren't listed in /v1/models,
// see issue #5224) is merged into the same ListModels response as chat models.
func TestListModels_MergesEmbeddingsCatalog(t *testing.T) {
	t.Parallel()

	var chatHits, embeddingHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		chatHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, modelsJSON("openai/gpt-4o"))
	})
	mux.HandleFunc("/v1/embeddings/models", func(w http.ResponseWriter, r *http.Request) {
		embeddingHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, modelsJSON("intfloat/multilingual-e5-large"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	provider := newTestOpenRouterProvider(server.URL)

	keys := []schemas.Key{{ID: "key-1", Value: schemas.SecretVar{Val: "test-key"}, Models: schemas.WhiteList{"*"}}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter, Unfiltered: true}

	resp, bifrostErr := provider.ListModels(ctx, keys, request)
	if bifrostErr != nil {
		t.Fatalf("ListModels returned error: %v", bifrostErr.Error)
	}

	if chatHits.Load() != 1 {
		t.Errorf("expected /v1/models to be queried once, got %d", chatHits.Load())
	}
	if embeddingHits.Load() != 1 {
		t.Errorf("expected /v1/embeddings/models to be queried once, got %d", embeddingHits.Load())
	}

	found := map[string]bool{}
	for _, m := range resp.Data {
		found[m.ID] = true
	}
	if !found["openrouter/openai/gpt-4o"] {
		t.Errorf("response missing chat model openrouter/openai/gpt-4o, got: %v", resp.Data)
	}
	if !found["openrouter/intfloat/multilingual-e5-large"] {
		t.Errorf("response missing embedding model openrouter/intfloat/multilingual-e5-large, got: %v", resp.Data)
	}
}

// TestListModels_EmbeddingsFetchIgnoresURLPathOverride verifies that a
// BifrostContextKeyURLPath override intended for the primary /v1/models
// request is not replayed against the embeddings fetch — otherwise it would
// hit the overridden path a second time (or an unrelated endpoint) instead of
// /v1/embeddings/models.
func TestListModels_EmbeddingsFetchIgnoresURLPathOverride(t *testing.T) {
	t.Parallel()

	var chatHits, embeddingHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/custom/chat-models", func(w http.ResponseWriter, r *http.Request) {
		chatHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, modelsJSON("openai/gpt-4o"))
	})
	mux.HandleFunc("/v1/embeddings/models", func(w http.ResponseWriter, r *http.Request) {
		embeddingHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, modelsJSON("intfloat/multilingual-e5-large"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	provider := newTestOpenRouterProvider(server.URL)

	keys := []schemas.Key{{ID: "key-1", Value: schemas.SecretVar{Val: "test-key"}, Models: schemas.WhiteList{"*"}}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyURLPath, "/custom/chat-models")
	request := &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter, Unfiltered: true}

	resp, bifrostErr := provider.ListModels(ctx, keys, request)
	if bifrostErr != nil {
		t.Fatalf("ListModels returned error: %v", bifrostErr.Error)
	}

	if chatHits.Load() != 1 {
		t.Errorf("expected the overridden chat path to be queried exactly once, got %d", chatHits.Load())
	}
	if embeddingHits.Load() != 1 {
		t.Errorf("expected /v1/embeddings/models to be queried once (not the overridden path), got %d", embeddingHits.Load())
	}

	found := map[string]bool{}
	for _, m := range resp.Data {
		found[m.ID] = true
	}
	if !found["openrouter/intfloat/multilingual-e5-large"] {
		t.Errorf("response missing embedding model despite URL path override, got: %v", resp.Data)
	}
}

// TestListModels_EmbeddingsCatalogFailureIsBestEffort verifies that a failure
// fetching /v1/embeddings/models does not fail chat-model discovery.
func TestListModels_EmbeddingsCatalogFailureIsBestEffort(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, modelsJSON("openai/gpt-4o"))
	})
	mux.HandleFunc("/v1/embeddings/models", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"internal error"}}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	provider := newTestOpenRouterProvider(server.URL)

	keys := []schemas.Key{{ID: "key-1", Value: schemas.SecretVar{Val: "test-key"}, Models: schemas.WhiteList{"*"}}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter, Unfiltered: true}

	resp, bifrostErr := provider.ListModels(ctx, keys, request)
	if bifrostErr != nil {
		t.Fatalf("ListModels should not fail when only the embeddings catalog errors, got: %v", bifrostErr.Error)
	}

	found := false
	for _, m := range resp.Data {
		if m.ID == "openrouter/openai/gpt-4o" {
			found = true
		}
	}
	if !found {
		t.Error("response missing chat model despite embeddings-catalog failure")
	}
}
