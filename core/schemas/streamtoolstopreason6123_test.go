package schemas

import "testing"

func terminalStopReasonFor(t *testing.T, chunks []*BifrostChatResponse) *string {
	t.Helper()
	state := AcquireChatToResponsesStreamState()
	defer ReleaseChatToResponsesStreamState(state)

	var stopReason *string
	for _, c := range chunks {
		for _, r := range c.ToBifrostResponsesStreamResponse(state) {
			if r.Type == ResponsesStreamResponseTypeCompleted && r.Response != nil && r.Response.StopReason != nil {
				stopReason = r.Response.StopReason
			}
		}
	}
	return stopReason
}

func bridgeStreamChunk6123(delta *ChatStreamResponseChoiceDelta, finish *string) *BifrostChatResponse {
	return &BifrostChatResponse{
		ID:    "chatcmpl-1",
		Model: "mimo-v2.5",
		Choices: []BifrostResponseChoice{{
			Index:                    0,
			FinishReason:             finish,
			ChatStreamResponseChoice: &ChatStreamResponseChoice{Delta: delta},
		}},
	}
}

func mixedTextToolChunks(finish string) []*BifrostChatResponse {
	role := "assistant"
	text := "I will check the time."
	name := "get_time"
	id := "call_1"
	return []*BifrostChatResponse{
		bridgeStreamChunk6123(&ChatStreamResponseChoiceDelta{Role: &role}, nil),
		bridgeStreamChunk6123(&ChatStreamResponseChoiceDelta{Content: &text}, nil),
		bridgeStreamChunk6123(&ChatStreamResponseChoiceDelta{ToolCalls: []ChatAssistantMessageToolCall{{
			Index: 0, ID: &id,
			Function: ChatAssistantMessageToolCallFunction{Name: &name, Arguments: "{}"},
		}}}, nil),
		bridgeStreamChunk6123(&ChatStreamResponseChoiceDelta{}, Ptr(finish)),
	}
}

// Regression for #6123: some OpenAI-compatible upstreams report finish_reason
// "stop" even when the turn emitted tool calls. The non-streaming converter
// compensates (#3640); the streaming bridge must too, or the Anthropic surface
// reports end_turn and agentic clients never execute the tool.
func TestStreamBridgeInfersToolCallsStopReason(t *testing.T) {
	got := terminalStopReasonFor(t, mixedTextToolChunks("stop"))
	if got == nil || *got != string(BifrostFinishReasonToolCalls) {
		t.Errorf("StopReason = %v, want %q", got, BifrostFinishReasonToolCalls)
	}
}

// An upstream that reports tool_calls correctly must keep working.
func TestStreamBridgeKeepsExplicitToolCallsStopReason(t *testing.T) {
	got := terminalStopReasonFor(t, mixedTextToolChunks("tool_calls"))
	if got == nil || *got != string(BifrostFinishReasonToolCalls) {
		t.Errorf("StopReason = %v, want %q", got, BifrostFinishReasonToolCalls)
	}
}

// A plain text turn ending with "stop" must stay "stop": the inference only
// fires when the stream actually carried tool calls.
func TestStreamBridgeTextOnlyStopUnaffected(t *testing.T) {
	role := "assistant"
	text := "hello"
	chunks := []*BifrostChatResponse{
		bridgeStreamChunk6123(&ChatStreamResponseChoiceDelta{Role: &role}, nil),
		bridgeStreamChunk6123(&ChatStreamResponseChoiceDelta{Content: &text}, nil),
		bridgeStreamChunk6123(&ChatStreamResponseChoiceDelta{}, Ptr("stop")),
	}
	got := terminalStopReasonFor(t, chunks)
	if got == nil || *got != string(BifrostFinishReasonStop) {
		t.Errorf("StopReason = %v, want %q", got, BifrostFinishReasonStop)
	}
}
