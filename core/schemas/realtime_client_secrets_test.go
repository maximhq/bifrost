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

// TestExtractRealtimeClientSecretModelLegacyRootModelPrefersOverAudioSibling
// is a regression test for a bug caught by codex review: a full realtime
// session using the legacy top-level "model" field (no session.model), with
// live input-audio transcription enabled as a completely standard sibling
// feature, was having its model extraction hijacked by the nested
// transcription model — losing the real conversational model entirely and
// silently reclassifying the session as transcription-only.
func TestExtractRealtimeClientSecretModelLegacyRootModelPrefersOverAudioSibling(t *testing.T) {
	t.Parallel()

	root, err := ParseRealtimeClientSecretBody(json.RawMessage(`{"model":"openai/gpt-4o-realtime-preview","session":{"audio":{"input":{"transcription":{"model":"openai/whisper-1"}}}}}`))
	if err != nil {
		t.Fatalf("ParseRealtimeClientSecretBody() error = %v", err)
	}

	model, bifrostErr := ExtractRealtimeClientSecretModel(root)
	if bifrostErr != nil {
		t.Fatalf("ExtractRealtimeClientSecretModel() error = %v", bifrostErr)
	}
	if model != "openai/gpt-4o-realtime-preview" {
		t.Fatalf("model = %q, want %q (legacy root model must win over an audio.input.transcription sibling)", model, "openai/gpt-4o-realtime-preview")
	}
}

func TestIsGATranscriptionSessionBodyExcludesNullTranscription(t *testing.T) {
	t.Parallel()

	// A full realtime session that explicitly disables input transcription
	// (audio.input.transcription: null) with a legacy root model must not be
	// misclassified as transcription-only.
	root, err := ParseRealtimeClientSecretBody(json.RawMessage(`{"model":"openai/gpt-4o-realtime-preview","session":{"audio":{"input":{"transcription":null}}}}`))
	if err != nil {
		t.Fatalf("ParseRealtimeClientSecretBody() error = %v", err)
	}
	if IsGATranscriptionSessionBody(root) {
		t.Fatal("explicit audio.input.transcription: null must not be classified as a GA transcription session")
	}
}

func TestIsGATranscriptionSessionBodyRootModelWins(t *testing.T) {
	t.Parallel()

	root, err := ParseRealtimeClientSecretBody(json.RawMessage(`{"model":"openai/gpt-4o-realtime-preview","session":{"audio":{"input":{"transcription":{"model":"openai/whisper-1"}}}}}`))
	if err != nil {
		t.Fatalf("ParseRealtimeClientSecretBody() error = %v", err)
	}
	if IsGATranscriptionSessionBody(root) {
		t.Fatal("a legacy root model must classify the body as a full realtime session, not transcription-only")
	}
}
