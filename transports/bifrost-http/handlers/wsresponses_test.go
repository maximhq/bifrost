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
// extractStreamEventType: lightweight type field extraction
// ---------------------------------------------------------------------------

func TestExtractStreamEventType_ValidTerminal(t *testing.T) {
	got := extractStreamEventType([]byte(`{"type":"response.completed","sequence_number":5}`))
	if got != schemas.ResponsesStreamResponseTypeCompleted {
		t.Errorf("got %q, want %q", got, schemas.ResponsesStreamResponseTypeCompleted)
	}
}

func TestExtractStreamEventType_ValidNonTerminal(t *testing.T) {
	got := extractStreamEventType([]byte(`{"type":"response.output_text.delta","delta":"hello"}`))
	if got != schemas.ResponsesStreamResponseTypeOutputTextDelta {
		t.Errorf("got %q, want %q", got, schemas.ResponsesStreamResponseTypeOutputTextDelta)
	}
}

func TestExtractStreamEventType_MalformedJSON(t *testing.T) {
	got := extractStreamEventType([]byte(`not json at all`))
	if got != "" {
		t.Errorf("expected empty string for malformed JSON, got %q", got)
	}
}

func TestExtractStreamEventType_MissingTypeField(t *testing.T) {
	got := extractStreamEventType([]byte(`{"sequence_number":1,"delta":"hello"}`))
	if got != "" {
		t.Errorf("expected empty string for missing type field, got %q", got)
	}
}

func TestExtractStreamEventType_UnknownExtraFields(t *testing.T) {
	// Simulates a large or provider-specific event with unknown nested structure.
	raw := []byte(`{"type":"response.completed","some_unknown_field":{"nested":{"deeply":"yes"}},"another_unknown":null}`)
	got := extractStreamEventType(raw)
	if got != schemas.ResponsesStreamResponseTypeCompleted {
		t.Errorf("got %q, want %q", got, schemas.ResponsesStreamResponseTypeCompleted)
	}
}

// ---------------------------------------------------------------------------
// synthesizeTerminalStreamResponse: minimal struct construction
// ---------------------------------------------------------------------------

func TestSynthesizeTerminalStreamResponse_FieldsPopulated(t *testing.T) {
	resp := synthesizeTerminalStreamResponse(schemas.OpenAI, "gpt-4o", schemas.ResponsesStreamResponseTypeCompleted)
	if resp == nil {
		t.Fatal("got nil response")
	}
	if resp.Type != schemas.ResponsesStreamResponseTypeCompleted {
		t.Errorf("Type = %q, want %q", resp.Type, schemas.ResponsesStreamResponseTypeCompleted)
	}
	if resp.ExtraFields.Provider != schemas.OpenAI {
		t.Errorf("Provider = %q, want %q", resp.ExtraFields.Provider, schemas.OpenAI)
	}
	if resp.ExtraFields.OriginalModelRequested != "gpt-4o" {
		t.Errorf("OriginalModelRequested = %q, want %q", resp.ExtraFields.OriginalModelRequested, "gpt-4o")
	}
	if resp.ExtraFields.RequestType != schemas.ResponsesStreamRequest {
		t.Errorf("RequestType = %v, want %v", resp.ExtraFields.RequestType, schemas.ResponsesStreamRequest)
	}
}

// ---------------------------------------------------------------------------
// isTerminalStreamType: terminal detection
// ---------------------------------------------------------------------------

func TestIsTerminalStreamType_TerminalTypes(t *testing.T) {
	terminals := []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeCompleted,
		schemas.ResponsesStreamResponseTypeFailed,
		schemas.ResponsesStreamResponseTypeIncomplete,
		schemas.ResponsesStreamResponseTypeError,
	}
	for _, tt := range terminals {
		if !isTerminalStreamType(tt) {
			t.Errorf("expected %q to be terminal", tt)
		}
	}
}

func TestIsTerminalStreamType_NonTerminalTypes(t *testing.T) {
	nonTerminals := []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeOutputTextDelta,
		schemas.ResponsesStreamResponseTypeCreated,
		schemas.ResponsesStreamResponseTypeInProgress,
		schemas.ResponsesStreamResponseType("codex.rate_limits"),
		schemas.ResponsesStreamResponseType(""),
	}
	for _, tt := range nonTerminals {
		if isTerminalStreamType(tt) {
			t.Errorf("expected %q to be non-terminal", tt)
		}
	}
}
