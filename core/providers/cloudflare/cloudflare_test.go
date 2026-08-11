package cloudflare_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/cloudflare"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestCloudflare runs the comprehensive provider test suite against Cloudflare
// Workers AI. Skips when CLOUDFLARE_API_KEY or CLOUDFLARE_ACCOUNT_ID is not
// set so CI can still pass without those secrets configured.
func TestCloudflare(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("CLOUDFLARE_API_KEY")) == "" {
		t.Skip("Skipping Cloudflare tests because CLOUDFLARE_API_KEY is not set")
	}
	if strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID")) == "" {
		t.Skip("Skipping Cloudflare tests because CLOUDFLARE_ACCOUNT_ID is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Cloudflare,
		ChatModel: "@cf/meta/llama-3.1-8b-instruct",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Cloudflare, Model: "@cf/meta/llama-3.1-8b-instruct"},
		},
		EmbeddingModel: "@cf/baai/bge-large-en-v1.5",
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             false, // not all Workers AI models support tools; keep narrow for first cut
			ToolCallsStreaming:    false,
			TextCompletion:        false, // /v1/completions is not part of the Workers AI OpenAI-compat surface
			TextCompletionStream:  false,
			ImageURL:              false,
			ImageBase64:           false,
			Embedding:             true,
			ListModels:            false, // Workers AI lists per-account; defer until we add an account-scoped fixture
		},
	}

	t.Run("CloudflareTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// TestCloudflareRequiresBaseURL exercises the constructor's contract that
// Cloudflare's OpenAI-compat surface needs the per-account URL because there
// is no global default that omits the account id.
func TestCloudflareRequiresBaseURL(t *testing.T) {
	t.Parallel()

	// No NetworkConfig.BaseURL set → must error.
	provider, err := cloudflare.NewCloudflareProvider(&schemas.ProviderConfig{}, nil)
	if err == nil {
		t.Fatalf("expected error when base URL is empty, got provider=%v", provider)
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("expected base_url error message, got %q", err.Error())
	}

	// Whitespace-only BaseURL is treated identically to empty.
	provider, err = cloudflare.NewCloudflareProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: "   "},
	}, nil)
	if err == nil {
		t.Fatalf("expected error when base URL is whitespace, got provider=%v", provider)
	}

	// A real-looking URL succeeds; trailing slash is normalized away.
	provider, err = cloudflare.NewCloudflareProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: "https://api.cloudflare.com/client/v4/accounts/abc123/ai/",
		},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error with valid base URL: %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
	if provider.GetProviderKey() != schemas.Cloudflare {
		t.Fatalf("expected provider key %q, got %q", schemas.Cloudflare, provider.GetProviderKey())
	}

	// Surrounding whitespace is also normalised — must not survive into the
	// stored config (would otherwise produce malformed request URLs).
	provider, err = cloudflare.NewCloudflareProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: "  https://api.cloudflare.com/client/v4/accounts/abc123/ai/  ",
		},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error with whitespace-padded base URL: %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider for whitespace-padded base URL")
	}
}

// TestCloudflareListModels verifies the native model-search route, pagination,
// authentication, and conversion of Cloudflare model metadata.
func TestCloudflareListModels(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/ai/models/search" {
			t.Errorf("expected /ai/models/search, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected bearer authorization header, got %q", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("per_page") != "50" {
			t.Errorf("expected per_page=50, got %q", r.URL.Query().Get("per_page"))
		}

		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil || page < 1 || page > 2 {
			t.Errorf("unexpected page query %q", r.URL.Query().Get("page"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requestCount.Add(1)

		modelName := "@cf/meta/llama-3.1-8b-instruct"
		if page == 2 {
			modelName = "@cf/baai/bge-large-en-v1.5"
		}
		response := cloudflare.CloudflareListModelsResponse{
			Success: true,
			Result: []cloudflare.CloudflareModel{
				{
					ID:          "model-" + strconv.Itoa(page),
					Name:        modelName,
					Description: "Workers AI model",
					Properties: []cloudflare.CloudflareModelProperty{
						{PropertyID: "context_window", Value: json.RawMessage(`"7968"`)},
					},
				},
			},
			ResultInfo: &cloudflare.CloudflareResultInfo{
				Page:       page,
				PerPage:    50,
				TotalCount: 2,
				TotalPages: 2,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	provider, err := cloudflare.NewCloudflareProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:             server.URL + "/ai",
			AllowPrivateNetwork: true,
		},
	}, nil)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{
		ID:     "key-1",
		Value:  *schemas.NewSecretVar("test-token"),
		Models: schemas.WhiteList{"*"},
	}
	response, bifrostErr := provider.ListModels(ctx, []schemas.Key{key}, &schemas.BifrostListModelsRequest{
		Provider:   schemas.Cloudflare,
		Unfiltered: true,
	})
	if bifrostErr != nil {
		t.Fatalf("list models failed: %v", bifrostErr)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("expected two paginated requests, got %d", requestCount.Load())
	}
	if len(response.Data) != 2 {
		t.Fatalf("expected two models, got %d", len(response.Data))
	}
	var llamaModel *schemas.Model
	for i := range response.Data {
		if response.Data[i].ID == "cloudflare/@cf/meta/llama-3.1-8b-instruct" {
			llamaModel = &response.Data[i]
			break
		}
	}
	if llamaModel == nil {
		t.Fatal("expected converted Llama model")
	}
	if llamaModel.ContextLength == nil || *llamaModel.ContextLength != 7968 {
		t.Errorf("expected context length 7968, got %v", llamaModel.ContextLength)
	}
	if llamaModel.OwnedBy == nil || *llamaModel.OwnedBy != "meta" {
		t.Errorf("expected owner meta, got %v", llamaModel.OwnedBy)
	}
	if len(response.KeyStatuses) != 1 || response.KeyStatuses[0].Status != schemas.KeyStatusSuccess {
		t.Errorf("expected successful key status, got %+v", response.KeyStatuses)
	}
}
