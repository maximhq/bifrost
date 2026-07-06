package schemas

import (
	"encoding/json"
	"testing"
)

func TestExtractRealtimeClientSecretModel(t *testing.T) {
	t.Parallel()

	root, err := ParseRealtimeClientSecretBody(json.RawMessage(`{"session":{"model":"openai/gpt-4o-realtime-preview"}}`))
	if err != nil {
		t.Fatalf("ParseRealtimeClientSecretBody() error = %v", err)
	}

	model, err := ExtractRealtimeClientSecretModel(root)
	if err != nil {
		t.Fatalf("ExtractRealtimeClientSecretModel() error = %v", err)
	}
	if model != "openai/gpt-4o-realtime-preview" {
		t.Fatalf("model = %q, want %q", model, "openai/gpt-4o-realtime-preview")
	}
}

func TestExtractRealtimeClientSecretModelFallbackTopLevel(t *testing.T) {
	t.Parallel()

	root, err := ParseRealtimeClientSecretBody(json.RawMessage(`{"model":"gpt-4o-realtime-preview"}`))
	if err != nil {
		t.Fatalf("ParseRealtimeClientSecretBody() error = %v", err)
	}

	model, err := ExtractRealtimeClientSecretModel(root)
	if err != nil {
		t.Fatalf("ExtractRealtimeClientSecretModel() error = %v", err)
	}
	if model != "gpt-4o-realtime-preview" {
		t.Fatalf("model = %q, want %q", model, "gpt-4o-realtime-preview")
	}
}
