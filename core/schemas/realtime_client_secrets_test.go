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

func TestExtractRealtimeClientSecretModelGATranscriptionShapeExplicitType(t *testing.T) {
	t.Parallel()

	root, err := ParseRealtimeClientSecretBody(json.RawMessage(`{"session":{"type":"transcription","audio":{"input":{"transcription":{"model":"openai/gpt-4o-transcribe"}}}}}`))
	if err != nil {
		t.Fatalf("ParseRealtimeClientSecretBody() error = %v", err)
	}

	model, bifrostErr := ExtractRealtimeClientSecretModel(root)
	if bifrostErr != nil {
		t.Fatalf("ExtractRealtimeClientSecretModel() error = %v", bifrostErr)
	}
	if model != "openai/gpt-4o-transcribe" {
		t.Fatalf("model = %q, want %q", model, "openai/gpt-4o-transcribe")
	}
}

func TestExtractRealtimeClientSecretModelGATranscriptionShapeNoExplicitType(t *testing.T) {
	t.Parallel()

	// Some clients may omit "type" and rely on the transcription shape alone
	// to imply it — extraction must not require an explicit type field.
	root, err := ParseRealtimeClientSecretBody(json.RawMessage(`{"session":{"audio":{"input":{"transcription":{"model":"openai/whisper-1"}}}}}`))
	if err != nil {
		t.Fatalf("ParseRealtimeClientSecretBody() error = %v", err)
	}

	model, bifrostErr := ExtractRealtimeClientSecretModel(root)
	if bifrostErr != nil {
		t.Fatalf("ExtractRealtimeClientSecretModel() error = %v", bifrostErr)
	}
	if model != "openai/whisper-1" {
		t.Fatalf("model = %q, want %q", model, "openai/whisper-1")
	}
}

func TestExtractRealtimeClientSecretModelFullSessionPrefersSessionModelOverAudio(t *testing.T) {
	t.Parallel()

	// A full realtime session (session.model set) must never be misread as a
	// transcription session even if it also happens to carry an audio object.
	root, err := ParseRealtimeClientSecretBody(json.RawMessage(`{"session":{"model":"openai/gpt-realtime","audio":{"input":{"transcription":{"model":"openai/whisper-1"}}}}}`))
	if err != nil {
		t.Fatalf("ParseRealtimeClientSecretBody() error = %v", err)
	}

	model, bifrostErr := ExtractRealtimeClientSecretModel(root)
	if bifrostErr != nil {
		t.Fatalf("ExtractRealtimeClientSecretModel() error = %v", bifrostErr)
	}
	if model != "openai/gpt-realtime" {
		t.Fatalf("model = %q, want %q (session.model must win)", model, "openai/gpt-realtime")
	}
}
