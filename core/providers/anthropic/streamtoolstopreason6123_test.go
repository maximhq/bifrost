package anthropic

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Regression for #6123, end to end: a chat-completions upstream that reports
// finish_reason "stop" on a mixed text + tool-call turn must still terminate
// the Anthropic streaming surface with stop_reason "tool_use", or agentic
// clients treat the turn as complete and never execute the tool.
func TestAnthropicStreamMixedTextToolTurnStopReason(t *testing.T) {
	state := schemas.AcquireChatToResponsesStreamState()
	defer schemas.ReleaseChatToResponsesStreamState(state)
	ctx := schemas.NewBifrostContext(nil, time.Time{})

	role := "assistant"
	text := "I will check the time."
	name := "get_time"
	id := "call_1"
	chunk := func(delta *schemas.ChatStreamResponseChoiceDelta, finish *string) *schemas.BifrostChatResponse {
		return &schemas.BifrostChatResponse{
			ID:    "chatcmpl-1",
			Model: "mimo-v2.5",
			Choices: []schemas.BifrostResponseChoice{{
				Index:                    0,
				FinishReason:             finish,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: delta},
			}},
		}
	}
	chunks := []*schemas.BifrostChatResponse{
		chunk(&schemas.ChatStreamResponseChoiceDelta{Role: &role}, nil),
		chunk(&schemas.ChatStreamResponseChoiceDelta{Content: &text}, nil),
		chunk(&schemas.ChatStreamResponseChoiceDelta{ToolCalls: []schemas.ChatAssistantMessageToolCall{{
			Index: 0, ID: &id,
			Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name, Arguments: "{}"},
		}}}, nil),
		// Upstream quirk under test: terminal chunk says "stop" despite the tool call.
		chunk(&schemas.ChatStreamResponseChoiceDelta{}, schemas.Ptr("stop")),
	}

	var sawToolUseBlock bool
	var terminalStopReason *AnthropicStopReason
	for _, c := range chunks {
		for _, r := range c.ToBifrostResponsesStreamResponse(state) {
			for _, ev := range ToAnthropicResponsesStreamResponse(ctx, r) {
				if ev.Type == AnthropicStreamEventTypeContentBlockStart && ev.ContentBlock != nil &&
					ev.ContentBlock.Type == AnthropicContentBlockTypeToolUse {
					sawToolUseBlock = true
				}
				if ev.Type == AnthropicStreamEventTypeMessageDelta && ev.Delta != nil && ev.Delta.StopReason != nil {
					terminalStopReason = ev.Delta.StopReason
				}
			}
		}
	}

	if !sawToolUseBlock {
		t.Fatal("stream never emitted a tool_use content block")
	}
	if terminalStopReason == nil {
		t.Fatal("stream never emitted message_delta with stop_reason")
	}
	if *terminalStopReason != AnthropicStopReasonToolUse {
		t.Errorf("stop_reason = %q, want %q", *terminalStopReason, AnthropicStopReasonToolUse)
	}
}
