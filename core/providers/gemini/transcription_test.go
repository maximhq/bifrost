package gemini

import "testing"

// Gemini bills the prompt even when it returns no transcript (for example a
// speechless clip), so usage must survive an empty-text response.
func TestToBifrostTranscriptionResponseKeepsUsageWhenTextEmpty(t *testing.T) {
	resp := &GenerateContentResponse{
		Candidates: []*Candidate{{Content: &Content{Role: string(RoleModel), Parts: []*Part{{Text: ""}}}}},
		UsageMetadata: &GenerateContentResponseUsageMetadata{
			PromptTokenCount: 12,
			TotalTokenCount:  12,
		},
	}

	got := resp.ToBifrostTranscriptionResponse()
	if got.Text != "" {
		t.Fatalf("text = %q, want empty", got.Text)
	}
	if got.Usage == nil {
		t.Fatal("usage dropped for an empty transcript")
	}
	if got.Usage.InputTokens == nil || *got.Usage.InputTokens != 12 {
		t.Errorf("input tokens = %v, want 12", got.Usage.InputTokens)
	}

	genai := ToGeminiTranscriptionResponse(got)
	if genai.UsageMetadata == nil || genai.UsageMetadata.PromptTokenCount != 12 {
		t.Errorf("genai usageMetadata = %+v, want promptTokenCount 12", genai.UsageMetadata)
	}
}
