package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// newTestOllamaProvider creates an OllamaProvider suitable for unit tests, with
// includeCustomModelFields controlling whether non-standard upstream fields are preserved.
func newTestOllamaProvider(includeCustomModelFields bool) *OllamaProvider {
	return &OllamaProvider{
		client: &fasthttp.Client{
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		},
		networkConfig:            schemas.NetworkConfig{},
		includeCustomModelFields: includeCustomModelFields,
	}
}

// customModelJSON is a minimal OpenAI-compatible /v1/models response for a single model that
// also carries non-standard fields a self-hosted Ollama-compatible backend might add.
const customModelJSON = `{"object":"list","data":[{"id":"llama3-custom","object":"model","owned_by":"ollama","max_model_len":8192,"root":"llama3-custom","quantization":"Q4_K_M"}]}`

func TestListModels_CustomFieldsPreservedWhenFlagEnabled(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(customModelJSON))
	}))
	defer server.Close()

	provider := newTestOllamaProvider(true)
	keys := []schemas.Key{
		{
			ID:    "key-1",
			Value: schemas.SecretVar{Val: "test-api-key"},
			OllamaKeyConfig: &schemas.OllamaKeyConfig{
				URL: schemas.SecretVar{Val: server.URL},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostListModelsRequest{Provider: schemas.Ollama, Unfiltered: true}

	resp, bifrostErr := provider.ListModels(ctx, keys, request)
	if bifrostErr != nil {
		t.Fatalf("ListModels returned error: %v", bifrostErr.Error)
	}

	var model *schemas.Model
	for i := range resp.Data {
		if resp.Data[i].ID == "ollama/llama3-custom" {
			model = &resp.Data[i]
		}
	}
	if model == nil {
		t.Fatalf("response missing ollama/llama3-custom, got: %v", resp.Data)
	}
	if len(model.ProviderExtra) == 0 {
		t.Fatal("expected ProviderExtra to be populated when includeCustomModelFields is true")
	}

	body, err := model.MarshalJSONWithProviderExtra()
	if err != nil {
		t.Fatalf("MarshalJSONWithProviderExtra failed: %v", err)
	}
	for _, want := range []string{`"max_model_len":8192`, `"root":"llama3-custom"`, `"quantization":"Q4_K_M"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("expected marshaled model to contain %s, got: %s", want, body)
		}
	}
}

func TestListModels_CustomFieldsDroppedWhenFlagDisabled(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(customModelJSON))
	}))
	defer server.Close()

	provider := newTestOllamaProvider(false)
	keys := []schemas.Key{
		{
			ID:    "key-1",
			Value: schemas.SecretVar{Val: "test-api-key"},
			OllamaKeyConfig: &schemas.OllamaKeyConfig{
				URL: schemas.SecretVar{Val: server.URL},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostListModelsRequest{Provider: schemas.Ollama, Unfiltered: true}

	resp, bifrostErr := provider.ListModels(ctx, keys, request)
	if bifrostErr != nil {
		t.Fatalf("ListModels returned error: %v", bifrostErr.Error)
	}

	var model *schemas.Model
	for i := range resp.Data {
		if resp.Data[i].ID == "ollama/llama3-custom" {
			model = &resp.Data[i]
		}
	}
	if model == nil {
		t.Fatalf("response missing ollama/llama3-custom, got: %v", resp.Data)
	}
	if len(model.ProviderExtra) != 0 {
		t.Fatalf("expected ProviderExtra to stay empty when includeCustomModelFields is false, got: %s", model.ProviderExtra)
	}
}
