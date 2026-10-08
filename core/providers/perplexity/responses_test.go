package perplexity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// TestWithWireModelForAgentAPI covers withWireModelForAgentAPI directly: the bare
// "sonar" rewrite from TestWireModelForAgentAPI, plus the dedicated "preset" sentinel
// that lets a `preset` pick Perplexity's own default model. Live-verified against
// api.perplexity.ai on 2026-09-24: sending any `model` value, even an empty string,
// always wins over a `preset`'s own default model — only a wire request with no
// `model` field at all lets the preset choose. Bare "sonar" is a real Perplexity
// model, so it is never touched by a preset (a caller who wants it keeps it
// regardless of any preset); the "preset" sentinel (not a real model — see
// perplexityAgentPresetModel) is the explicit opt-in for letting the preset decide.
func TestWithWireModelForAgentAPI(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		hasPreset bool
		preset    string
		want      string
	}{
		{name: "bare sonar, no preset param at all: rewritten as usual", model: "sonar", want: "perplexity/sonar"},
		{name: "bare sonar + preset: explicit sonar model still wins, unaffected", model: "sonar", hasPreset: true, preset: "fast", want: "perplexity/sonar"},
		{name: "preset sentinel, no preset param: left untouched (caller error)", model: "preset", want: "preset"},
		{name: "bare preset sentinel + non-empty preset: model cleared for preset to control", model: "preset", hasPreset: true, preset: "fast", want: ""},
		{name: "prefixed preset sentinel + non-empty preset: model cleared for preset to control", model: "perplexity/preset", hasPreset: true, preset: "fast", want: ""},
		{name: "preset sentinel + preset key present but empty value: left untouched", model: "preset", hasPreset: true, preset: "", want: "preset"},
		{name: "third-party model + preset: explicit model wins", model: "openai/gpt-5.6-sol", hasPreset: true, preset: "fast", want: "openai/gpt-5.6-sol"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &schemas.BifrostResponsesRequest{Model: tt.model}
			if tt.hasPreset {
				req.Params = &schemas.ResponsesParameters{ExtraParams: map[string]interface{}{"preset": tt.preset}}
			}
			got := withWireModelForAgentAPI(req)
			if got.Model != tt.want {
				t.Fatalf("withWireModelForAgentAPI(model=%q, preset=%q).Model = %q, want %q", tt.model, tt.preset, got.Model, tt.want)
			}
			// The caller's own request object must not be mutated.
			if req.Model != tt.model {
				t.Fatalf("caller's request.Model was mutated: got %q, want %q", req.Model, tt.model)
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

// TestResponses_AgentAPIOmitsModelWhenPresetSelectsIt verifies the fix end to end at
// the wire level: a Responses call using the perplexityAgentPresetModel sentinel
// ("preset") together with a `preset` reaches Perplexity's /v1/responses with no
// `model` field at all, so the preset's own default model is used server-side
// instead of Bifrost's required routing model always winning (live-verified
// against api.perplexity.ai on 2026-09-24). preset and max_steps still reach the
// wire via ExtraParams passthrough.
func TestResponses_AgentAPIOmitsModelWhenPresetSelectsIt(t *testing.T) {
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
		Model:    "preset",
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

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bifrostErr := provider.Responses(ctx, key, req); bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr.Error.Message)
	}

	// Model has no `omitempty` tag, so it still reaches the wire as an empty
	// string rather than being dropped from the JSON entirely — live-verified
	// against api.perplexity.ai that Perplexity treats {"model":""} the same as
	// no model key at all, so this still lets the preset's own default apply.
	if got, want := capturedBody["model"], ""; got != want {
		t.Fatalf("expected model=%q on the wire, got %#v", want, capturedBody["model"])
	}
	if got, want := capturedBody["preset"], "fast"; got != want {
		t.Fatalf("expected preset=%v to be forwarded, got %v (body: %#v)", want, got, capturedBody)
	}
	if got, want := capturedBody["max_steps"], float64(3); got != want {
		t.Fatalf("expected max_steps=%v to be forwarded, got %v (body: %#v)", want, got, capturedBody)
	}
	// The caller's own request object must not be mutated.
	if req.Model != "preset" {
		t.Fatalf("caller's request.Model was mutated: got %q, want \"preset\"", req.Model)
	}
}

var noopPostHookRunner schemas.PostHookRunner = func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

// drainStreamWithTimeout collects every chunk from stream until it closes,
// failing fast if the channel never closes instead of hanging until the test
// binary's own timeout.
func drainStreamWithTimeout(t *testing.T, stream chan *schemas.BifrostStreamChunk, timeout time.Duration) []*schemas.BifrostStreamChunk {
	t.Helper()

	var chunks []*schemas.BifrostStreamChunk
	done := make(chan struct{})
	go func() {
		defer close(done)
		for chunk := range stream {
			chunks = append(chunks, chunk)
		}
	}()

	select {
	case <-done:
		return chunks
	case <-time.After(timeout):
		t.Fatal("stream did not close within timeout")
		return nil
	}
}

const minimalResponsesAPIStreamCompletedEvent = `data: {"type":"response.completed","sequence_number":0,"response":` +
	`{"id":"resp_1","object":"response","created_at":1,"model":"openai/gpt-5.6-sol","status":"completed",` +
	`"output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hi","annotations":[]}]}],` +
	`"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"

// TestResponsesStream_AgentAPIRewritesBareSonarModel is the streaming counterpart of
// TestResponses_AgentAPIRewritesBareSonarModel: without a preset, bare "sonar" is
// still rewritten to "perplexity/sonar" on the wire for the streaming path.
func TestResponsesStream_AgentAPIRewritesBareSonarModel(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "json error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, minimalResponsesAPIStreamCompletedEvent)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
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
	stream, bifrostErr := provider.ResponsesStream(ctx, noopPostHookRunner, nil, key, req)
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned error synchronously: %v", bifrostErr.Error.Message)
	}
	drainStreamWithTimeout(t, stream, 5*time.Second)

	if got, want := capturedBody["model"], "perplexity/sonar"; got != want {
		t.Fatalf("expected model=%q on the wire, got %v", want, got)
	}
	// The caller's own request object must not be mutated.
	if req.Model != "sonar" {
		t.Fatalf("caller's request.Model was mutated: got %q, want \"sonar\"", req.Model)
	}
}

// TestResponsesStream_AgentAPIOmitsModelForPresetAndForwardsExtraParams is the
// streaming counterpart of TestResponses_AgentAPIForwardsExtraParamsAutomatically and
// TestResponses_AgentAPIOmitsModelWhenPresetSelectsIt: ResponsesStream must apply the
// same automatic BifrostContextKeyPassthroughExtraParams and
// withWireModelForAgentAPI(request) handling (including clearing model for the
// perplexityAgentPresetModel sentinel when a preset is set) as the unary Responses
// path, since streaming requests reach Perplexity's Agent API through the same
// /v1/responses endpoint (see openai.HandleOpenAIResponsesStreaming in
// ResponsesStream above).
func TestResponsesStream_AgentAPIOmitsModelForPresetAndForwardsExtraParams(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "json error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, minimalResponsesAPIStreamCompletedEvent)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	provider := newTestPerplexityProvider(t, server.URL)
	key := schemas.Key{ID: "test-key", Value: schemas.SecretVar{Val: "test-api-key"}}

	hello := "hi"
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Perplexity,
		Model:    "preset",
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

	stream, bifrostErr := provider.ResponsesStream(ctx, noopPostHookRunner, nil, key, req)
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned error synchronously: %v", bifrostErr.Error.Message)
	}
	drainStreamWithTimeout(t, stream, 5*time.Second)

	if capturedBody == nil {
		t.Fatal("mock server did not receive a request body")
	}
	// Model has no `omitempty` tag, so it still reaches the wire as an empty
	// string rather than being dropped from the JSON entirely — see the comment
	// on TestResponses_AgentAPIOmitsModelWhenPresetSelectsIt.
	if got, want := capturedBody["model"], ""; got != want {
		t.Fatalf("expected model=%q on the wire, got %#v", want, capturedBody["model"])
	}
	if got, want := capturedBody["preset"], "fast"; got != want {
		t.Fatalf("expected preset=%v to be forwarded, got %v (body: %#v)", want, got, capturedBody)
	}
	if got, want := capturedBody["max_steps"], float64(3); got != want {
		t.Fatalf("expected max_steps=%v to be forwarded, got %v (body: %#v)", want, got, capturedBody)
	}
	// The caller's own request object must not be mutated.
	if req.Model != "preset" {
		t.Fatalf("caller's request.Model was mutated: got %q, want \"preset\"", req.Model)
	}
}
