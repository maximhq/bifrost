package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

type testWSHandlerStore struct {
	allowDirectKeys bool
}

func (s testWSHandlerStore) ShouldAllowDirectKeys() bool {
	return s.allowDirectKeys
}

func (s testWSHandlerStore) GetHeaderMatcher() *lib.HeaderMatcher {
	return nil
}

func (s testWSHandlerStore) GetAvailableProviders() []schemas.ModelProvider {
	return nil
}

func (s testWSHandlerStore) GetStreamChunkInterceptor() lib.StreamChunkInterceptor {
	return nil
}

func (s testWSHandlerStore) GetAsyncJobExecutor() *logstore.AsyncJobExecutor {
	return nil
}

func (s testWSHandlerStore) GetAsyncJobResultTTL() int {
	return 0
}

func (s testWSHandlerStore) GetKVStore() *kvstore.Store {
	return nil
}

func (s testWSHandlerStore) GetMCPHeaderCombinedAllowlist() schemas.WhiteList {
	return nil
}

func TestCreateBifrostContextFromAuth_BaggageSessionIDSetsGrouping(t *testing.T) {
	ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{}, &authHeaders{
		baggage: "foo=bar, session-id=rt-ws-123, baz=qux",
	})
	defer cancel()

	if got, _ := ctx.Value(schemas.BifrostContextKeyParentRequestID).(string); got != "rt-ws-123" {
		t.Fatalf("parent request id = %q, want %q", got, "rt-ws-123")
	}
}

func TestCreateBifrostContextFromAuth_EmptyBaggageSessionIDIgnored(t *testing.T) {
	ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{}, &authHeaders{
		baggage: "session-id=   ",
	})
	defer cancel()

	if got := ctx.Value(schemas.BifrostContextKeyParentRequestID); got != nil {
		t.Fatalf("parent request id should be unset, got %#v", got)
	}
}

// ---------------------------------------------------------------------------
// inferDefaultProviderFromPath
// ---------------------------------------------------------------------------

func TestInferDefaultProviderFromPath_OpenAIIntegrationPrefix(t *testing.T) {
	paths := []string{
		"/openai/v1/responses",
		"/openai/responses",
		"/openai/openai/responses",
		"/openai/v1/chat/completions",
	}
	for _, p := range paths {
		got := inferDefaultProviderFromPath(p)
		if got != schemas.OpenAI {
			t.Errorf("inferDefaultProviderFromPath(%q) = %q, want %q", p, got, schemas.OpenAI)
		}
	}
}

func TestInferDefaultProviderFromPath_UnifiedPathNoDefault(t *testing.T) {
	paths := []string{
		"/v1/responses",
		"/v1/chat/completions",
		"/",
		"",
		"/anthropic/v1/messages",
	}
	for _, p := range paths {
		got := inferDefaultProviderFromPath(p)
		if got != "" {
			t.Errorf("inferDefaultProviderFromPath(%q) = %q, want empty (no default)", p, got)
		}
	}
}

// ---------------------------------------------------------------------------
// convertEventToRequest: bare vs prefixed model on integration and unified paths
// ---------------------------------------------------------------------------

// buildMinimalEvent returns a minimal WebSocketResponsesEvent suitable for
// convertEventToRequest. input is valid JSON (e.g. a JSON array string).
func buildMinimalEvent(model string, inputJSON []byte) *schemas.WebSocketResponsesEvent {
	return &schemas.WebSocketResponsesEvent{
		Model: model,
		Input: inputJSON,
	}
}

var minimalInput = []byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]`)

// TestConvertEventToRequest_BareModelIntegrationPath verifies that a bare model
// string (e.g. "gpt-4o") is accepted when defaultProvider is schemas.OpenAI,
// matching the /openai/v1/responses integration path behavior.
func TestConvertEventToRequest_BareModelIntegrationPath(t *testing.T) {
	h := &WSResponsesHandler{config: nil}
	event := buildMinimalEvent("gpt-4o", minimalInput)

	req, err := h.convertEventToRequest(event, schemas.OpenAI)
	if err != nil {
		t.Fatalf("expected no error for bare model on integration path, got: %v", err)
	}
	if req.Provider != schemas.OpenAI {
		t.Errorf("Provider = %q, want %q", req.Provider, schemas.OpenAI)
	}
	if req.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", req.Model, "gpt-4o")
	}
}

// TestConvertEventToRequest_BareModelUnifiedPathRejected verifies that a bare
// model string is rejected when defaultProvider is empty, matching the unified
// /v1/responses path behavior (multi-provider, requires explicit prefix).
func TestConvertEventToRequest_BareModelUnifiedPathRejected(t *testing.T) {
	h := &WSResponsesHandler{config: nil}
	event := buildMinimalEvent("gpt-4o", minimalInput)

	_, err := h.convertEventToRequest(event, "")
	if err == nil {
		t.Fatal("expected error for bare model on unified path, got nil")
	}
}

// TestConvertEventToRequest_PrefixedModelIntegrationPath verifies that an
// explicitly prefixed model string (e.g. "openai/gpt-4o") works on the
// integration path (defaultProvider = schemas.OpenAI).
func TestConvertEventToRequest_PrefixedModelIntegrationPath(t *testing.T) {
	h := &WSResponsesHandler{config: nil}
	event := buildMinimalEvent("openai/gpt-4o", minimalInput)

	req, err := h.convertEventToRequest(event, schemas.OpenAI)
	if err != nil {
		t.Fatalf("expected no error for prefixed model on integration path, got: %v", err)
	}
	if req.Provider != schemas.OpenAI {
		t.Errorf("Provider = %q, want %q", req.Provider, schemas.OpenAI)
	}
	if req.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", req.Model, "gpt-4o")
	}
}

// TestConvertEventToRequest_PrefixedModelUnifiedPath verifies that an explicitly
// prefixed model string (e.g. "openai/gpt-4o") is accepted on the unified path
// (defaultProvider = ""), i.e. the current working mode is not broken.
func TestConvertEventToRequest_PrefixedModelUnifiedPath(t *testing.T) {
	h := &WSResponsesHandler{config: nil}
	event := buildMinimalEvent("openai/gpt-4o", minimalInput)

	req, err := h.convertEventToRequest(event, "")
	if err != nil {
		t.Fatalf("expected no error for prefixed model on unified path, got: %v", err)
	}
	if req.Provider != schemas.OpenAI {
		t.Errorf("Provider = %q, want %q", req.Provider, schemas.OpenAI)
	}
}

// TestConvertEventToRequest_AnthropicPrefixedModelUnifiedPath verifies that an
// Anthropic-prefixed model string works on the unified path without ambiguity.
func TestConvertEventToRequest_AnthropicPrefixedModelUnifiedPath(t *testing.T) {
	h := &WSResponsesHandler{config: nil}
	event := buildMinimalEvent("anthropic/claude-3-5-sonnet-20241022", minimalInput)

	req, err := h.convertEventToRequest(event, "")
	if err != nil {
		t.Fatalf("expected no error for anthropic-prefixed model on unified path, got: %v", err)
	}
	if req.Provider != schemas.Anthropic {
		t.Errorf("Provider = %q, want %q", req.Provider, schemas.Anthropic)
	}
	if req.Model != "claude-3-5-sonnet-20241022" {
		t.Errorf("Model = %q, want %q", req.Model, "claude-3-5-sonnet-20241022")
	}
}
