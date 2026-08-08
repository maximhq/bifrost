package schemas

import (
	"testing"
)

// Issue #5959: the chat-to-Responses streaming bridge closed the open text item
// when a tool call started (blocks must not overlap), but text resuming after
// the tool call reused the closed item's id without re-announcing it. Strict
// clients (Vercel AI SDK v5) deregister a text part on output_item.done and
// abort the stream when a delta arrives for an unregistered part ("text part
// ... not found"). Resumed text must open a fresh item, and every closed text
// run must still appear in the final response output.

func lifecycleChatChunk(id string, role, content *string, finish *string) *BifrostChatResponse {
	return &BifrostChatResponse{
		ID:     id,
		Object: "chat.completion.chunk",
		Choices: []BifrostResponseChoice{{
			FinishReason:             finish,
			ChatStreamResponseChoice: &ChatStreamResponseChoice{Delta: &ChatStreamResponseChoiceDelta{Role: role, Content: content}},
		}},
	}
}

// assertStreamLifecycle replays chunks through the bridge and enforces the
// client-visible invariant: a text delta's item id must have been announced
// via content_part.added and must not have been closed by output_item.done.
func assertStreamLifecycle(t *testing.T, chunks []*BifrostChatResponse) []*BifrostResponsesStreamResponse {
	t.Helper()
	state := AcquireChatToResponsesStreamState()
	defer ReleaseChatToResponsesStreamState(state)

	announced := map[string]bool{}
	closed := map[string]bool{}
	var events []*BifrostResponsesStreamResponse
	for _, chunk := range chunks {
		for _, ev := range chunk.ToBifrostResponsesStreamResponse(state) {
			events = append(events, ev)
			switch ev.Type {
			case ResponsesStreamResponseTypeContentPartAdded:
				if ev.ItemID != nil {
					announced[*ev.ItemID] = true
				}
			case ResponsesStreamResponseTypeOutputItemDone:
				if ev.Item != nil && ev.Item.ID != nil {
					closed[*ev.Item.ID] = true
				}
			case ResponsesStreamResponseTypeOutputTextDelta:
				if ev.ItemID == nil {
					t.Fatal("output_text.delta without item_id")
				}
				if !announced[*ev.ItemID] {
					t.Fatalf("output_text.delta for unannounced item %q", *ev.ItemID)
				}
				if closed[*ev.ItemID] {
					t.Fatalf("output_text.delta for closed item %q (Vercel AI SDK aborts with 'text part not found')", *ev.ItemID)
				}
			}
		}
	}
	return events
}

func TestBridgeTextResumesAfterToolCallOpensFreshItem(t *testing.T) {
	role := "assistant"
	first := "Let me check."
	second := "Done, here is the answer."
	callID := "call_9"
	fnType := "function"
	fnName := "bash"
	toolsDone := "tool_calls"

	toolChunk := lifecycleChatChunk("resp1", nil, nil, nil)
	toolChunk.Choices[0].ChatStreamResponseChoice.Delta.ToolCalls = []ChatAssistantMessageToolCall{{
		Index: 0, ID: &callID, Type: &fnType,
		Function: ChatAssistantMessageToolCallFunction{Name: &fnName, Arguments: `{"c":1}`},
	}}

	events := assertStreamLifecycle(t, []*BifrostChatResponse{
		lifecycleChatChunk("resp1", &role, &first, nil),
		toolChunk,
		lifecycleChatChunk("resp1", nil, &second, nil),
		lifecycleChatChunk("resp1", nil, nil, &toolsDone),
	})

	// The resumed run must be a distinct item from the first run.
	var textItemIDs []string
	seen := map[string]bool{}
	for _, ev := range events {
		if ev.Type == ResponsesStreamResponseTypeOutputTextDelta && ev.ItemID != nil && !seen[*ev.ItemID] {
			seen[*ev.ItemID] = true
			textItemIDs = append(textItemIDs, *ev.ItemID)
		}
	}
	if len(textItemIDs) != 2 {
		t.Fatalf("expected two distinct text items across the interleaved stream, got %v", textItemIDs)
	}

	// Both text runs and the tool call must appear in the completed output.
	final := events[len(events)-1]
	if final.Type != ResponsesStreamResponseTypeCompleted || final.Response == nil {
		t.Fatalf("last event is not response.completed: %+v", final)
	}
	var texts []string
	toolCalls := 0
	for _, out := range final.Response.Output {
		if out.Type != nil && *out.Type == ResponsesMessageTypeFunctionCall {
			toolCalls++
			continue
		}
		if out.Content != nil && len(out.Content.ContentBlocks) > 0 && out.Content.ContentBlocks[0].Text != nil {
			texts = append(texts, *out.Content.ContentBlocks[0].Text)
		}
	}
	if len(texts) != 2 || texts[0] != first || texts[1] != second {
		t.Errorf("completed output text runs = %v, want [%q %q]", texts, first, second)
	}
	if toolCalls != 1 {
		t.Errorf("completed output tool calls = %d, want 1", toolCalls)
	}
}

// TestBridgePlainTextLifecycleUnchanged guards the ordinary single-text-run
// stream: one announced item, deltas on it, closed once at finish.
func TestBridgePlainTextLifecycleUnchanged(t *testing.T) {
	role := "assistant"
	hello := "Hello"
	world := " world"
	stop := "stop"

	events := assertStreamLifecycle(t, []*BifrostChatResponse{
		lifecycleChatChunk("resp1", &role, &hello, nil),
		lifecycleChatChunk("resp1", nil, &world, nil),
		lifecycleChatChunk("resp1", nil, nil, &stop),
	})

	final := events[len(events)-1]
	if final.Type != ResponsesStreamResponseTypeCompleted || final.Response == nil || len(final.Response.Output) != 1 {
		t.Fatalf("expected completed with a single output item, got %+v", final)
	}
	text := final.Response.Output[0].Content.ContentBlocks[0].Text
	if text == nil || *text != "Hello world" {
		t.Errorf("completed text = %v, want 'Hello world'", text)
	}
}
