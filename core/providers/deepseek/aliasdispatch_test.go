package deepseek_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// aliasTestAccount serves a single DeepSeek provider whose base URL points at a
// local mock, with a key carrying the same alias shape the harness gateway uses
// (tests/integrations/python/config.json): a user-facing model name mapped to
// the wire model with use_anthropic_endpoints enabled.
type aliasTestAccount struct {
	baseURL string
}

func (a *aliasTestAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.DeepSeek}, nil
}

func (a *aliasTestAccount) GetKeysForProvider(ctx context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	enabled := true
	return []schemas.Key{{
		Name:   "DeepSeek API Key",
		Value:  schemas.SecretVar{Val: "test-api-key"},
		Models: []string{"*"},
		Weight: 1,
		Aliases: schemas.KeyAliases{
			"deepseek-v4-flash-anthropic": schemas.AliasConfig{
				ModelID:               "deepseek-v4-flash",
				UseAnthropicEndpoints: &enabled,
			},
		},
	}}, nil
}

func (a *aliasTestAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        a.baseURL,
			DefaultRequestTimeoutInSeconds: 5,
			StreamIdleTimeoutInSeconds:     5,
			MaxConnsPerHost:                1,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 1,
			BufferSize:  1,
		},
	}, nil
}

// TestChatCompletion_AliasRoutesToAnthropicMountWithEffort drives the incident's
// full path through the real dispatcher: a key-level alias with
// use_anthropic_endpoints routes the request to /anthropic/v1/messages, where
// the reasoning effort must leave as output_config.effort — not as a
// thinking.budget_tokens the mount documents as ignored. This pins the exact
// config shape the provider harness gateway relies on (folder 59), which no
// other test exercises.
func TestChatCompletion_AliasRoutesToAnthropicMountWithEffort(t *testing.T) {
	t.Parallel()

	var capturedPath string
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "decode body", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, newAnthropicResponse())
	}))
	defer server.Close()

	b, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: &aliasTestAccount{baseURL: server.URL},
		Logger:  testLogger{},
	})
	if err != nil {
		t.Fatalf("bifrost.Init: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := b.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash-anthropic",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: new("What is 17 * 23?")},
		}},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: new(128000),
			Reasoning:           &schemas.ChatReasoning{Effort: new("max")},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionRequest: %v", bifrostErr.Error.Message)
	}

	if capturedPath != "/anthropic/v1/messages" {
		t.Fatalf("alias must route to the Anthropic mount, got path %q (body: %#v)", capturedPath, captured)
	}
	if got := captured["model"]; got != "deepseek-v4-flash" {
		t.Fatalf("alias must rewrite the wire model, got %#v", got)
	}
	outputConfig, ok := captured["output_config"].(map[string]any)
	if !ok {
		t.Fatalf("outbound body missing output_config: %#v", captured)
	}
	if got := outputConfig["effort"]; got != "max" {
		t.Fatalf("output_config.effort = %v, want max", got)
	}
	if thinking, ok := captured["thinking"]; ok {
		t.Fatalf("no thinking field may be synthesized for an effort-only request, got %#v", thinking)
	}
}
