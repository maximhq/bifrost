package gemini

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Issue #5978: the IncompleteDetails fallback in ToGeminiResponsesResponse
// switched on "max_tokens", a string that never occurs — the schema constant is
// "max_output_tokens" — so truncated responses reported finishReason OTHER
// instead of MAX_TOKENS. Clients keying on MAX_TOKENS (raise limit and retry,
// truncation UX) misclassified every truncated stop.
func TestGeminiIncompleteReasonFinishReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		want   FinishReason
	}{
		{"truncation maps to MAX_TOKENS", schemas.ResponsesResponseIncompleteReasonMaxOutputTokens, FinishReasonMaxTokens},
		{"content filter maps to SAFETY", schemas.ResponsesResponseIncompleteReasonContentFilter, FinishReasonSafety},
		{"unknown reason falls back to OTHER", "some_future_reason", FinishReasonOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgType := schemas.ResponsesMessageTypeMessage
			role := schemas.ResponsesInputMessageRoleAssistant
			bifrostResp := &schemas.BifrostResponsesResponse{
				Output: []schemas.ResponsesMessage{{
					Type: &msgType,
					Role: &role,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{Type: schemas.ResponsesOutputMessageContentTypeText, Text: schemas.Ptr("truncated tex")},
						},
					},
				}},
				IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{Reason: tc.reason},
			}

			geminiResp := ToGeminiResponsesResponse(bifrostResp)
			if geminiResp == nil || len(geminiResp.Candidates) == 0 {
				t.Fatalf("no candidates: %+v", geminiResp)
			}
			if got := geminiResp.Candidates[0].FinishReason; got != tc.want {
				t.Errorf("finish reason = %q, want %q", got, tc.want)
			}
		})
	}
}
