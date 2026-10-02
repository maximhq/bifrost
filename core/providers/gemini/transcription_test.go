package gemini

import (
	"reflect"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

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

// The conversion runs once per retry/fallback attempt on the same Bifrost
// request. Moving safety_settings, cached_content and labels into typed fields
// must not remove them from the request, or the next attempt is sent without them.
func TestToGeminiTranscriptionRequestExtraParamsSurviveRetries(t *testing.T) {
	extraParams := func() map[string]interface{} {
		return map[string]interface{}{
			"safety_settings": []interface{}{
				map[string]interface{}{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"},
			},
			"cached_content":     "cachedContents/abc123",
			"labels":             map[string]interface{}{"team": "platform"},
			"custom_passthrough": "keep-me",
		}
	}
	bifrostReq := &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Vertex,
		Model:    "gemini-2.5-flash",
		Input:    &schemas.TranscriptionInput{File: []byte("audio")},
		Params:   &schemas.TranscriptionParameters{ExtraParams: extraParams()},
	}

	for attempt := 1; attempt <= 3; attempt++ {
		req := ToGeminiTranscriptionRequest(bifrostReq)
		if len(req.SafetySettings) != 1 || req.CachedContent != "cachedContents/abc123" || req.Labels["team"] != "platform" {
			t.Fatalf("attempt %d: safetySettings = %v, cachedContent = %q, labels = %v, want all three set",
				attempt, req.SafetySettings, req.CachedContent, req.Labels)
		}
		if got := req.GetExtraParams(); !reflect.DeepEqual(got, map[string]interface{}{"custom_passthrough": "keep-me"}) {
			t.Fatalf("attempt %d: extra params = %v, want only custom_passthrough", attempt, got)
		}
	}
	if !reflect.DeepEqual(bifrostReq.Params.ExtraParams, extraParams()) {
		t.Errorf("request extra params changed to %v", bifrostReq.Params.ExtraParams)
	}
}
