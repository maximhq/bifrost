package bedrock

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestConverseToolErrorReachesChatSurface pins the Bedrock ingress round trip.
// A Converse request is only ever converted to the Responses surface
// (transports/bifrost-http/integrations/bedrock.go routes every branch through
// ToBifrostResponsesRequest), so the chat surface is reached transitively via the
// mux. This asserts a toolResult status of "error" survives both hops rather than
// arriving at a chat-surface provider as a success.
func TestConverseToolErrorReachesChatSurface(t *testing.T) {
	ctx := &schemas.BifrostContext{}

	req := &BedrockConverseRequest{
		Messages: []BedrockMessage{
			{
				Role: "user",
				Content: []BedrockContentBlock{
					{
						ToolResult: &BedrockToolResult{
							ToolUseID: "toolu_01",
							Content:   []BedrockContentBlock{{Text: schemas.Ptr("ENOENT: no such file or directory")}},
							Status:    schemas.Ptr("error"),
						},
					},
				},
			},
		},
	}

	responsesReq, err := req.ToBifrostResponsesRequest(ctx)
	if err != nil {
		t.Fatalf("converse -> responses: %v", err)
	}
	if responsesReq == nil || len(responsesReq.Input) == 0 {
		t.Fatal("expected at least one responses message")
	}

	var toolOut *schemas.ResponsesMessage
	for i := range responsesReq.Input {
		if responsesReq.Input[i].ResponsesToolMessage != nil {
			toolOut = &responsesReq.Input[i]
			break
		}
	}
	if toolOut == nil {
		t.Fatal("expected a function_call_output message")
	}
	if toolOut.Status == nil || *toolOut.Status != "incomplete" {
		t.Fatalf("converse status error must map to incomplete, got %v", toolOut.Status)
	}

	// Second hop: the mux is what carries the marker onto the chat surface.
	chatReq := responsesReq.ToChatRequest()
	if chatReq == nil || len(chatReq.Input) == 0 {
		t.Fatal("expected at least one chat message")
	}

	var toolMsg *schemas.ChatMessage
	for i := range chatReq.Input {
		if chatReq.Input[i].ChatToolMessage != nil {
			toolMsg = &chatReq.Input[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("expected a tool message on the chat surface")
	}
	if toolMsg.ChatToolMessage.IsError == nil || !*toolMsg.ChatToolMessage.IsError {
		t.Fatal("a failed Bedrock tool result must reach the chat surface as IsError true")
	}
}

// TestConverseToolSuccessStaysUnmarkedOnChatSurface is the negative half: a
// successful Converse tool result must not acquire the marker on the way through.
func TestConverseToolSuccessStaysUnmarkedOnChatSurface(t *testing.T) {
	ctx := &schemas.BifrostContext{}

	req := &BedrockConverseRequest{
		Messages: []BedrockMessage{
			{
				Role: "user",
				Content: []BedrockContentBlock{
					{
						ToolResult: &BedrockToolResult{
							ToolUseID: "toolu_02",
							Content:   []BedrockContentBlock{{Text: schemas.Ptr("15 degrees")}},
							Status:    schemas.Ptr("success"),
						},
					},
				},
			},
		},
	}

	responsesReq, err := req.ToBifrostResponsesRequest(ctx)
	if err != nil {
		t.Fatalf("converse -> responses: %v", err)
	}

	chatReq := responsesReq.ToChatRequest()
	if chatReq == nil || len(chatReq.Input) == 0 {
		t.Fatal("expected at least one chat message")
	}

	// Locate the tool message first: asserting inside the loop alone would also pass
	// if the conversion dropped the successful tool result entirely, which is a
	// different bug wearing the same green.
	var toolMsg *schemas.ChatToolMessage
	for i := range chatReq.Input {
		if chatReq.Input[i].ChatToolMessage != nil {
			toolMsg = chatReq.Input[i].ChatToolMessage
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("expected the successful tool result to survive onto the chat surface")
	}
	if toolMsg.IsError != nil {
		t.Fatalf("successful tool result must leave IsError nil, got %v", *toolMsg.IsError)
	}
}
