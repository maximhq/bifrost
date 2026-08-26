package schemas

import (
	"testing"
)

// OpenAI's Chat Completions API requires `content` on a role:"tool" message -- it is
// the tool's result, so unlike an assistant message it has no meaning without one.
//
// A function_call_output can legitimately carry no body. Anthropic marks `content`
// optional on a tool_result block (only `type` and `tool_use_id` are required), so a
// void tool, an errored tool with no body, and an explicit empty content array all
// produce a ResponsesToolMessageOutputStruct with neither field set.
//
// ToChatMessages used to leave cm.Content nil for those, and `omitempty` then dropped
// the key entirely -- the tool message went out as
// {"role":"tool","tool_call_id":"..."} with no content at all.
//
// Fixed here rather than at the Anthropic ingress on purpose. This is the layer that
// owns the chat wire contract, so one fix covers every producer of an empty tool
// output; seeding an empty string back at the ingress would also rewrite the Bedrock
// Converse wire from `"content":[]` to `"content":[{"text":""}]`, and an empty text
// block is a shape Anthropic rejects.
func TestToChatMessages_EmptyToolOutputCarriesEmptyStringContent(t *testing.T) {
	fcoType := ResponsesMessageTypeFunctionCallOutput

	newFCO := func(output *ResponsesToolMessageOutputStruct) ResponsesMessage {
		return ResponsesMessage{
			Type: &fcoType,
			ResponsesToolMessage: &ResponsesToolMessage{
				CallID: Ptr("toolu_empty"),
				Output: output,
			},
		}
	}

	tests := []struct {
		name string
		msg  ResponsesMessage
	}{
		{
			name: "output struct with neither field set",
			msg:  newFCO(&ResponsesToolMessageOutputStruct{}),
		},
		{
			name: "output blocks present but empty",
			msg:  newFCO(&ResponsesToolMessageOutputStruct{ResponsesFunctionToolCallOutputBlocks: []ResponsesMessageContentBlock{}}),
		},
		{
			name: "no output struct at all",
			msg:  newFCO(nil),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chatMessages := ToChatMessages([]ResponsesMessage{tt.msg})

			if len(chatMessages) != 1 {
				t.Fatalf("expected 1 chat message, got %d", len(chatMessages))
			}
			cm := chatMessages[0]
			if cm.Role != ChatMessageRoleTool {
				t.Fatalf("expected role tool, got %q", cm.Role)
			}
			if cm.Content == nil || cm.Content.ContentStr == nil {
				t.Fatalf("tool message must carry content; OpenAI rejects a tool message without it. got Content=%+v", cm.Content)
			}
			if *cm.Content.ContentStr != "" {
				t.Errorf("empty tool output must become an empty string, got %q", *cm.Content.ContentStr)
			}
		})
	}
}

// A tool output that DOES carry a body must be untouched by the empty-output
// backfill above.
func TestToChatMessages_NonEmptyToolOutputUnchanged(t *testing.T) {
	fcoType := ResponsesMessageTypeFunctionCallOutput

	chatMessages := ToChatMessages([]ResponsesMessage{{
		Type: &fcoType,
		ResponsesToolMessage: &ResponsesToolMessage{
			CallID: Ptr("toolu_ok"),
			Output: &ResponsesToolMessageOutputStruct{
				ResponsesToolCallOutputStr: Ptr("72 degrees and sunny"),
			},
		},
	}})

	if len(chatMessages) != 1 {
		t.Fatalf("expected 1 chat message, got %d", len(chatMessages))
	}
	cm := chatMessages[0]
	if cm.Content == nil || cm.Content.ContentStr == nil {
		t.Fatal("expected content to survive")
	}
	if *cm.Content.ContentStr != "72 degrees and sunny" {
		t.Errorf("tool output altered: got %q", *cm.Content.ContentStr)
	}
}
