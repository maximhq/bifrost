package cloudflare_test

import (
	"os"
	"strings"
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
}
