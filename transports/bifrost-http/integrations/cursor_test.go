package integrations

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Issue #7601: a truncated or filtered Responses stream ends with response.incomplete.
// The Cursor chat-chunk converter handled only response.completed, so the final chunk
// carrying finish_reason and usage was dropped, leaving the client to treat a
// truncated turn (including half-written tool-call arguments) as complete.
func TestCursorStreamIncompleteEmitsFinishReason(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   string
	}{
		{schemas.ResponsesResponseIncompleteReasonMaxOutputTokens, "length"},
		{schemas.ResponsesResponseIncompleteReasonContentFilter, "content_filter"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			_, out, err := convertResponsesStreamToChatChunk(&schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeIncomplete,
				Response: &schemas.BifrostResponsesResponse{
					Status:            schemas.Ptr(schemas.ResponsesResponseStatusIncomplete),
					IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{Reason: tc.reason},
					Usage:             &schemas.ResponsesResponseUsage{InputTokens: 20, OutputTokens: 16, TotalTokens: 36},
				},
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			chunk, ok := out.(*cursorChatChunk)
			if !ok || chunk == nil {
				t.Fatalf("response.incomplete produced no chat chunk (got %T)", out)
			}
			if len(chunk.Choices) != 1 || chunk.Choices[0].FinishReason == nil {
				t.Fatalf("final chunk carries no finish_reason: %+v", chunk)
			}
			if got := *chunk.Choices[0].FinishReason; got != tc.want {
				t.Errorf("finish_reason = %q, want %q", got, tc.want)
			}
			if chunk.Usage == nil || chunk.Usage.CompletionTokens != 16 {
				t.Errorf("final chunk must carry usage, got %+v", chunk.Usage)
			}
		})
	}
}

// A response.incomplete with no stop reason and no incomplete_details (Gemini returned no
// finish reason) must not read as a clean stop: finish_reason stays null, usage still lands.
func TestCursorStreamIncompleteWithoutReasonKeepsFinishReasonNull(t *testing.T) {
	_, out, err := convertResponsesStreamToChatChunk(&schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeIncomplete,
		Response: &schemas.BifrostResponsesResponse{
			Status: schemas.Ptr(schemas.ResponsesResponseStatusIncomplete),
			Usage:  &schemas.ResponsesResponseUsage{InputTokens: 20, OutputTokens: 16, TotalTokens: 36},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	chunk, ok := out.(*cursorChatChunk)
	if !ok || chunk == nil || len(chunk.Choices) != 1 {
		t.Fatalf("response.incomplete produced no chat chunk (got %T)", out)
	}
	if fr := chunk.Choices[0].FinishReason; fr != nil {
		t.Errorf("finish_reason = %q, want null for an incomplete response with unknown reason", *fr)
	}
	if chunk.Usage == nil || chunk.Usage.CompletionTokens != 16 {
		t.Errorf("final chunk must carry usage, got %+v", chunk.Usage)
	}
}

// An incomplete response with no reason but a function-call output item may carry truncated
// tool-call arguments: it must not read as a normal tool_calls finish.
func TestCursorStreamIncompleteWithToolCallKeepsFinishReasonNull(t *testing.T) {
	_, out, err := convertResponsesStreamToChatChunk(&schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeIncomplete,
		Response: &schemas.BifrostResponsesResponse{
			Status: schemas.Ptr(schemas.ResponsesResponseStatusIncomplete),
			Output: []schemas.ResponsesMessage{{Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall)}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	chunk, ok := out.(*cursorChatChunk)
	if !ok || chunk == nil || len(chunk.Choices) != 1 {
		t.Fatalf("response.incomplete produced no chat chunk (got %T)", out)
	}
	if fr := chunk.Choices[0].FinishReason; fr != nil {
		t.Errorf("finish_reason = %q, want null for an incomplete response with a tool call and unknown reason", *fr)
	}
}

// An unrecognized incomplete reason must not fall out of the switch as a clean "stop".
func TestCursorStreamIncompleteUnrecognizedReasonKeepsFinishReasonNull(t *testing.T) {
	_, out, err := convertResponsesStreamToChatChunk(&schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeIncomplete,
		Response: &schemas.BifrostResponsesResponse{
			Status:            schemas.Ptr(schemas.ResponsesResponseStatusIncomplete),
			IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{Reason: "quota_exceeded"},
			Usage:             &schemas.ResponsesResponseUsage{InputTokens: 20, OutputTokens: 16, TotalTokens: 36},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	chunk, ok := out.(*cursorChatChunk)
	if !ok || chunk == nil || len(chunk.Choices) != 1 {
		t.Fatalf("response.incomplete produced no chat chunk (got %T)", out)
	}
	if fr := chunk.Choices[0].FinishReason; fr != nil {
		t.Errorf("finish_reason = %q, want null for an unrecognized incomplete reason", *fr)
	}
	if chunk.Usage == nil || chunk.Usage.CompletionTokens != 16 {
		t.Errorf("final chunk must carry usage, got %+v", chunk.Usage)
	}
}
