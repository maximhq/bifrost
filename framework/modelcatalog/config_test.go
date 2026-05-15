package modelcatalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDefaultURLWithEnv(t *testing.T) {
	t.Setenv(PricingURLEnvVar, " https://internal.example/datasheet ")
	if got := defaultPricingURL(); got != "https://internal.example/datasheet" {
		t.Fatalf("expected env pricing URL, got %q", got)
	}

	t.Setenv(ModelParametersURLEnvVar, "")
	if got := defaultModelParametersURL(); got != DefaultModelParametersURL {
		t.Fatalf("expected default model parameters URL, got %q", got)
	}
}

func TestLoadModelParametersFromURLUsesConfiguredURL(t *testing.T) {
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"test-model": map[string]any{
				"max_output_tokens": 4096,
			},
		})
	}))
	defer server.Close()

	mc := &ModelCatalog{
		modelParametersURL: server.URL,
		logger:             noOpLogger{},
	}

	params, err := mc.loadModelParametersFromURL(context.Background())
	if err != nil {
		t.Fatalf("expected model parameters load to succeed, got %v", err)
	}
	if !requested {
		t.Fatal("expected configured URL to be requested")
	}
	if _, ok := params["test-model"]; !ok {
		t.Fatal("expected test model parameters to be loaded")
	}
}
