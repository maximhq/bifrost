package zro

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestZroBaseURL rejects insecure or malformed endpoints before requests can carry credentials.
func TestZroBaseURL(t *testing.T) {
	for _, endpoint := range []string{"http://example.com/v1", "ftp://example.com", "https:///v1", "https://user:password@example.com/v1", "https://example.com/v1?secret=value", "https://example.com/v1#fragment"} {
		t.Run(endpoint, func(t *testing.T) {
			_, err := NewZroProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: endpoint}}, nil)
			if err == nil {
				t.Fatal("expected invalid endpoint to be rejected")
			}
		})
	}
	for _, endpoint := range []string{"", "https://example.com/v1///"} {
		p, err := NewZroProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: endpoint}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := "https://example.com/v1"
		if endpoint == "" {
			want = "https://zro.moonmath.ai/v1"
		}
		if p.networkConfig.BaseURL != want {
			t.Fatalf("base URL = %q, want %q", p.networkConfig.BaseURL, want)
		}
	}
}

// TestZroStreamingOverride exercises request routing without a live API.
func TestZroStreamingOverride(t *testing.T) {
	paths := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"test rejection","type":"invalid_request_error"}}`))
	}))
	defer server.Close()
	p, err := NewZroProvider(&schemas.ProviderConfig{CustomProviderConfig: &schemas.CustomProviderConfig{CustomProviderKey: "custom-zro"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Inject an in-process HTTP fixture after production endpoint validation.
	p.networkConfig.BaseURL = server.URL
	p.streamingClient = &fasthttp.Client{ReadTimeout: time.Second, WriteTimeout: time.Second}
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyURLPath, "/custom/chat")
	_, apiErr := p.ChatCompletionStream(ctx, nil, nil, schemas.Key{}, &schemas.BifrostChatRequest{Model: "test-model"})
	if apiErr == nil {
		t.Fatal("expected fixture rejection")
	}
	select {
	case path := <-paths:
		if path != "/custom/chat" {
			t.Errorf("request path = %q, want /custom/chat", path)
		}
	case <-ctx.Done():
		t.Fatal("request did not reach fixture")
	}
}

// TestZroConfigIsolation checks that retained configuration cannot mutate provider headers.
func TestZroConfigIsolation(t *testing.T) {
	cfg := &schemas.ProviderConfig{CustomProviderConfig: &schemas.CustomProviderConfig{CustomProviderKey: "custom-zro"}, NetworkConfig: schemas.NetworkConfig{ExtraHeaders: map[string]string{"X-Test": "original"}}}
	p, err := NewZroProvider(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.NetworkConfig.ExtraHeaders["X-Test"] = "changed"
	if p.networkConfig.ExtraHeaders["X-Test"] != "original" {
		t.Error("provider shares headers with retained configuration")
	}
	if p.GetProviderKey() != schemas.ModelProvider("custom-zro") {
		t.Error("custom provider identity was lost")
	}
	_, unsupported := p.TextCompletion(nil, schemas.Key{}, nil)
	if unsupported.ExtraFields.RoutingInfo.Provider != schemas.ModelProvider("custom-zro") {
		t.Error("unsupported operation lost custom provider identity")
	}
	defaultProvider, err := NewZroProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if defaultProvider.GetProviderKey() != schemas.Zro {
		t.Error("default provider identity must remain zro")
	}
}
