package schemas

import "testing"

// The chat surface carries a failed tool result as a bool (ChatToolMessage.IsError)
// while the Responses surface carries it as an error string plus a "incomplete"
// status (ResponsesToolMessage.Error / ResponsesMessage.Status). The mux converts
// between the two surfaces in both directions, so a marker that survives one hop
// but not the other still reaches the provider as a success. These tests pin both
// directions and the round trip.

// TestChatToResponsesCarriesToolError covers the chat -> Responses hop. There is
// no error text on the chat side to populate ResponsesToolMessage.Error, so the
// faithful carrier is Status "incomplete" — the same signal the Anthropic
// Responses converter already treats as is_error (providers/anthropic/responses.go).
func TestChatToResponsesCarriesToolError(t *testing.T) {
	cm := ChatMessage{
		Role:    ChatMessageRoleTool,
		Content: &ChatMessageContent{ContentStr: Ptr("ENOENT: no such file or directory")},
		ChatToolMessage: &ChatToolMessage{
			ToolCallID: Ptr("call_1"),
			IsError:    Ptr(true),
		},
	}

	rms := cm.ToResponsesMessages()
	if len(rms) != 1 {
		t.Fatalf("expected 1 responses message, got %d", len(rms))
	}
	if rms[0].Status == nil || *rms[0].Status != "incomplete" {
		t.Fatalf("failed tool result must convert to status incomplete, got %v", rms[0].Status)
	}

	// A successful tool result must not acquire the marker.
	ok := ChatMessage{
		Role:            ChatMessageRoleTool,
		Content:         &ChatMessageContent{ContentStr: Ptr("15 degrees")},
		ChatToolMessage: &ChatToolMessage{ToolCallID: Ptr("call_2")},
	}
	okRMs := ok.ToResponsesMessages()
	if len(okRMs) != 1 {
		t.Fatalf("expected 1 responses message, got %d", len(okRMs))
	}
	if okRMs[0].Status != nil && *okRMs[0].Status == "incomplete" {
		t.Fatal("successful tool result must not convert to status incomplete")
	}
}

// TestResponsesToChatCarriesToolError covers the Responses -> chat hop, for both
// spellings the Responses surface uses for a failed tool result.
func TestResponsesToChatCarriesToolError(t *testing.T) {
	tests := []struct {
		name string
		msg  ResponsesMessage
	}{
		{
			name: "error string",
			msg: ResponsesMessage{
				Type: Ptr(ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &ResponsesToolMessage{
					CallID: Ptr("call_1"),
					Error:  Ptr("ENOENT: no such file or directory"),
				},
			},
		},
		{
			name: "incomplete status",
			msg: ResponsesMessage{
				Type:   Ptr(ResponsesMessageTypeFunctionCallOutput),
				Status: Ptr("incomplete"),
				ResponsesToolMessage: &ResponsesToolMessage{
					CallID: Ptr("call_1"),
					Output: &ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: Ptr("ENOENT: no such file or directory"),
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cms := ToChatMessages([]ResponsesMessage{tt.msg})
			if len(cms) != 1 {
				t.Fatalf("expected 1 chat message, got %d", len(cms))
			}
			if cms[0].ChatToolMessage == nil {
				t.Fatal("expected ChatToolMessage to be attached")
			}
			if cms[0].ChatToolMessage.IsError == nil || !*cms[0].ChatToolMessage.IsError {
				t.Fatal("failed tool result must convert to IsError true")
			}
		})
	}

	// A completed tool result must not acquire the marker.
	okCMs := ToChatMessages([]ResponsesMessage{{
		Type:   Ptr(ResponsesMessageTypeFunctionCallOutput),
		Status: Ptr("completed"),
		ResponsesToolMessage: &ResponsesToolMessage{
			CallID: Ptr("call_2"),
			Output: &ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: Ptr("15 degrees")},
		},
	}})
	if len(okCMs) != 1 {
		t.Fatalf("expected 1 chat message, got %d", len(okCMs))
	}
	if okCMs[0].ChatToolMessage == nil {
		t.Fatal("expected ChatToolMessage to be attached")
	}
	if okCMs[0].ChatToolMessage.IsError != nil {
		t.Fatalf("successful tool result must leave IsError nil, got %v", *okCMs[0].ChatToolMessage.IsError)
	}
}

// TestToolErrorSurvivesSurfaceRoundTrip is the regression that matters in
// production: a chat request that falls back to a Responses-native provider and
// back must not lose the marker anywhere along the way.
func TestToolErrorSurvivesSurfaceRoundTrip(t *testing.T) {
	original := ChatMessage{
		Role:    ChatMessageRoleTool,
		Content: &ChatMessageContent{ContentStr: Ptr("ENOENT: no such file or directory")},
		ChatToolMessage: &ChatToolMessage{
			ToolCallID: Ptr("call_1"),
			IsError:    Ptr(true),
		},
	}

	roundTripped := ToChatMessages(original.ToResponsesMessages())
	if len(roundTripped) != 1 {
		t.Fatalf("expected 1 chat message after round trip, got %d", len(roundTripped))
	}
	if roundTripped[0].ChatToolMessage == nil {
		t.Fatal("expected ChatToolMessage to survive the round trip")
	}
	if roundTripped[0].ChatToolMessage.IsError == nil || !*roundTripped[0].ChatToolMessage.IsError {
		t.Fatal("is_error must survive a chat -> responses -> chat round trip")
	}
}
