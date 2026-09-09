package gemini

import (
	"strings"
	"testing"
)

func TestProcessGeminiStreamChunk(t *testing.T) {
	tests := []struct {
		name             string
		payload          string
		wantModelVersion string
		wantCandidates   int
		wantError        string
	}{
		{
			name:             "healthy object",
			payload:          `{"candidates":[{}],"modelVersion":"gemini-test"}`,
			wantModelVersion: "gemini-test",
			wantCandidates:   1,
		},
		{
			name:      "bare JSON string",
			payload:   `"message"`,
			wantError: "failed to parse Gemini stream response: unexpected JSON type string",
		},
		{
			name:      "bare JSON number",
			payload:   `42`,
			wantError: "failed to parse Gemini stream response: unexpected JSON type number",
		},
		{
			name:      "bare JSON boolean",
			payload:   `true`,
			wantError: "failed to parse Gemini stream response: unexpected JSON type boolean",
		},
		{
			name:      "bare JSON null",
			payload:   `null`,
			wantError: "failed to parse Gemini stream response: unexpected JSON type null",
		},
		{
			name:             "array-wrapped success object",
			payload:          `[{"candidates":[{}],"modelVersion":"gemini-array-test"}]`,
			wantModelVersion: "gemini-array-test",
			wantCandidates:   1,
		},
		{
			name:             "array skips non-object elements",
			payload:          `[null,"message",{"candidates":[{}],"modelVersion":"gemini-first-object-test"}]`,
			wantModelVersion: "gemini-first-object-test",
			wantCandidates:   1,
		},
		{
			name:      "array without object elements",
			payload:   `[null,"message",42]`,
			wantError: "failed to parse Gemini stream response: JSON array contains no object elements",
		},
		{
			name:      "array-wrapped error object",
			payload:   `[{"error":{"code":429,"message":"rate limited"}}]`,
			wantError: "gemini api error:",
		},
		{
			name:      "object with error key",
			payload:   `{"error":{"code":400,"message":"bad request"}}`,
			wantError: "gemini api error:",
		},
		{
			name:      "empty payload",
			payload:   "",
			wantError: "failed to parse Gemini stream response: empty payload",
		},
		{
			name:      "whitespace payload",
			payload:   " \t\n ",
			wantError: "failed to parse Gemini stream response: empty payload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := processGeminiStreamChunk([]byte(tt.payload))
			if tt.wantError != "" {
				if err == nil {
					t.Fatalf("processGeminiStreamChunk() error = nil, want error containing %q", tt.wantError)
				}
				if !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("processGeminiStreamChunk() error = %q, want error containing %q", err, tt.wantError)
				}
				if response != nil {
					t.Fatalf("processGeminiStreamChunk() response = %#v, want nil", response)
				}
				return
			}

			if err != nil {
				t.Fatalf("processGeminiStreamChunk() unexpected error: %v", err)
			}
			if response == nil {
				t.Fatal("processGeminiStreamChunk() response = nil, want non-nil")
			}
			if response.ModelVersion != tt.wantModelVersion {
				t.Errorf("processGeminiStreamChunk() ModelVersion = %q, want %q", response.ModelVersion, tt.wantModelVersion)
			}
			if len(response.Candidates) != tt.wantCandidates {
				t.Errorf("processGeminiStreamChunk() candidates = %d, want %d", len(response.Candidates), tt.wantCandidates)
			}
		})
	}
}

func TestProcessGeminiStreamChunkMergesArrayObjects(t *testing.T) {
	payload := []byte(`[
        {"candidates":[{"content":{"parts":[{"text":"first"}]}}]},
        {"candidates":[{"content":{"parts":[{"text":"second"}]},"finishReason":"STOP"}]}
    ]`)

	response, err := processGeminiStreamChunk(payload)
	if err != nil {
		t.Fatalf("processGeminiStreamChunk() unexpected error: %v", err)
	}
	if response == nil || len(response.Candidates) != 1 {
		t.Fatalf("processGeminiStreamChunk() response = %#v, want one candidate", response)
	}
	if got := len(response.Candidates[0].Content.Parts); got != 2 {
		t.Fatalf("merged parts = %d, want 2", got)
	}
	if got := response.Candidates[0].Content.Parts[1].Text; got != "second" {
		t.Fatalf("merged second part = %q, want %q", got, "second")
	}
	if got := response.Candidates[0].FinishReason; got != FinishReasonStop {
		t.Fatalf("merged finish reason = %q, want %q", got, FinishReasonStop)
	}
}

func TestProcessGeminiStreamChunkPropagatesLaterArrayError(t *testing.T) {
	payload := []byte(`[
        {"candidates":[{"content":{"parts":[{"text":"first"}]}}]},
        {"error":{"code":429,"message":"rate limited"}}
    ]`)

	response, err := processGeminiStreamChunk(payload)
	if response != nil {
		t.Fatalf("processGeminiStreamChunk() response = %#v, want nil", response)
	}
	if err == nil || !strings.Contains(err.Error(), "gemini api error:") {
		t.Fatalf("processGeminiStreamChunk() error = %v, want Gemini API error", err)
	}
}
