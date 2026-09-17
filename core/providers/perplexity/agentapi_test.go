package perplexity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// noopTestLogger is a minimal schemas.Logger implementation for tests that don't
// want to pull in the core package (which itself imports this package, so
// importing it back here would create an import cycle).
type noopTestLogger struct{}

func (noopTestLogger) Debug(string, ...any)                   {}
func (noopTestLogger) Info(string, ...any)                    {}
func (noopTestLogger) Warn(string, ...any)                    {}
func (noopTestLogger) Error(string, ...any)                   {}
func (noopTestLogger) Fatal(string, ...any)                   {}
func (noopTestLogger) SetLevel(schemas.LogLevel)              {}
func (noopTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noopTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func newTestPerplexityProvider(t *testing.T, baseURL string) *PerplexityProvider {
	t.Helper()

	provider, err := NewPerplexityProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 10,
		},
	}, noopTestLogger{})
	if err != nil {
		t.Fatalf("failed to create Perplexity provider: %v", err)
	}
	return provider
}

const minimalResponsesAPIPayload = `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"openai/gpt-5.6-sol",` +
	`"output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hi","annotations":[]}]}],` +
	`"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`

// TestResponses_AgentAPIForwardsExtraParamsAutomatically verifies that Agent-API-only
// fields with no typed Bifrost equivalent (`preset`, `max_steps`) reach Perplexity's
// /v1/responses endpoint (the Agent API's OpenAI-SDK-compatible alias) without the
// caller having to set BifrostContextKeyPassthroughExtraParams themselves.
func TestResponses_AgentAPIForwardsExtraParamsAutomatically(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]interface{}
	var capturedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "json error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(minimalResponsesAPIPayload))
	}))
	defer server.Close()

	provider := newTestPerplexityProvider(t, server.URL)
	key := schemas.Key{ID: "test-key", Value: schemas.SecretVar{Val: "test-api-key"}}

	// A non-"sonar-*" model routes through the Agent API branch (see
	// isPerplexityResponsesSupported), not the chat/completions fallback.
	hello := "hi"
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Perplexity,
		Model:    "openai/gpt-5.6-sol",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: &hello},
			},
		},
		Params: &schemas.ResponsesParameters{
			ExtraParams: map[string]interface{}{
				"preset":    "fast",
				"max_steps": float64(3),
			},
		},
	}

	// Intentionally do NOT set BifrostContextKeyPassthroughExtraParams — the
	// provider must set it automatically for the Agent API path.
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	if _, bifrostErr := provider.Responses(ctx, key, req); bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr.Error.Message)
	}

	if capturedPath != "/v1/responses" {
		t.Fatalf("expected request to /v1/responses, got %q", capturedPath)
	}
	if capturedBody == nil {
		t.Fatal("mock server did not receive a request body")
	}
	if got, want := capturedBody["preset"], "fast"; got != want {
		t.Fatalf("expected preset=%v to be forwarded, got %v (body: %#v)", want, got, capturedBody)
	}
	if got, want := capturedBody["max_steps"], float64(3); got != want {
		t.Fatalf("expected max_steps=%v to be forwarded, got %v (body: %#v)", want, got, capturedBody)
	}
}

// TestResponses_AgentAPIForwardsPerplexitySpecificWebSearchFilters verifies that a
// web_search tool populated with Perplexity Agent API filters (search_domain_filter,
// search_recency_filter, ...) reaches the wire with Perplexity's own field names,
// not the generic allowed_domains/blocked_domains used by other providers.
func TestResponses_AgentAPIForwardsPerplexitySpecificWebSearchFilters(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "json error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(minimalResponsesAPIPayload))
	}))
	defer server.Close()

	provider := newTestPerplexityProvider(t, server.URL)
	key := schemas.Key{ID: "test-key", Value: schemas.SecretVar{Val: "test-api-key"}}

	hello := "latest ai agent developments"
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Perplexity,
		Model:    "openai/gpt-5.6-sol",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: &hello},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Type: schemas.ResponsesToolTypeWebSearch,
					ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{
						Filters: &schemas.ResponsesToolWebSearchFilters{
							SearchDomainFilter:  []string{"example.com"},
							SearchRecencyFilter: schemas.Ptr("month"),
						},
					},
				},
				{Type: schemas.ResponsesToolTypeFinanceSearch},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bifrostErr := provider.Responses(ctx, key, req); bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr.Error.Message)
	}

	toolsRaw, ok := capturedBody["tools"].([]interface{})
	if !ok || len(toolsRaw) != 2 {
		t.Fatalf("expected 2 tools on the wire, got %#v", capturedBody["tools"])
	}

	webSearch, ok := toolsRaw[0].(map[string]interface{})
	if !ok || webSearch["type"] != "web_search" {
		t.Fatalf("expected first tool to be web_search, got %#v", toolsRaw[0])
	}
	filters, ok := webSearch["filters"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected web_search.filters object, got %#v", webSearch["filters"])
	}
	if domains, ok := filters["search_domain_filter"].([]interface{}); !ok || len(domains) != 1 || domains[0] != "example.com" {
		t.Fatalf("expected search_domain_filter=[example.com], got %#v", filters["search_domain_filter"])
	}
	if filters["search_recency_filter"] != "month" {
		t.Fatalf("expected search_recency_filter=month, got %#v", filters["search_recency_filter"])
	}

	financeSearch, ok := toolsRaw[1].(map[string]interface{})
	if !ok || financeSearch["type"] != "finance_search" {
		t.Fatalf("expected second tool to be finance_search, got %#v", toolsRaw[1])
	}
}

// TestWireModelForAgentAPI locks in the live-verified (api.perplexity.ai, 2026-09-17)
// requirement that Perplexity's Agent API wants its own bare "sonar" model spelled
// "perplexity/sonar" on the wire, while every other model string (including one that
// already carries the prefix) is left untouched.
func TestWireModelForAgentAPI(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "bare sonar gets prefixed", model: "sonar", want: "perplexity/sonar"},
		{name: "already-prefixed sonar is unchanged", model: "perplexity/sonar", want: "perplexity/sonar"},
		{name: "sonar-pro is untouched (routes to chat/completions, not this path)", model: "sonar-pro", want: "sonar-pro"},
		{name: "third-party model is untouched", model: "openai/gpt-5.6-sol", want: "openai/gpt-5.6-sol"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wireModelForAgentAPI(tt.model); got != tt.want {
				t.Fatalf("wireModelForAgentAPI(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

// TestResponses_AgentAPIRewritesBareSonarModel verifies the fix end to end: a Responses
// call for the bare "sonar" model reaches Perplexity's /v1/responses (the Agent API
// alias) as "perplexity/sonar" on the wire. Before this fix, Bifrost forwarded "sonar"
// verbatim, which Perplexity's live API rejects with `validation failed: model "sonar"
// is not supported` even though isPerplexityResponsesSupported("sonar") routes it here.
func TestResponses_AgentAPIRewritesBareSonarModel(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "json error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(minimalResponsesAPIPayload))
	}))
	defer server.Close()

	provider := newTestPerplexityProvider(t, server.URL)
	key := schemas.Key{ID: "test-key", Value: schemas.SecretVar{Val: "test-api-key"}}

	hello := "hi"
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Perplexity,
		Model:    "sonar",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: &hello},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bifrostErr := provider.Responses(ctx, key, req); bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr.Error.Message)
	}

	if got, want := capturedBody["model"], "perplexity/sonar"; got != want {
		t.Fatalf("expected model=%q on the wire, got %v", want, got)
	}
	// The caller's own request object must not be mutated.
	if req.Model != "sonar" {
		t.Fatalf("caller's request.Model was mutated: got %q, want \"sonar\"", req.Model)
	}
}
