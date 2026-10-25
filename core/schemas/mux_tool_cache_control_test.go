package schemas

import "testing"

// A chat tool message whose content carries cache_control must keep the breakpoint on the
// function_call_output item, where providers read it. Before the fix it survived only on the
// output blocks, which the Bedrock converter does not turn into a cache point.
func TestToolMessageCacheControlMovesToTheItem(t *testing.T) {
	text := "file contents"
	callID := "call_1"
	cm := ChatMessage{
		Role: ChatMessageRoleTool,
		Content: &ChatMessageContent{ContentBlocks: []ChatContentBlock{
			{Type: ChatContentBlockTypeText, Text: &text, CacheControl: &CacheControl{Type: CacheControlTypeEphemeral}},
		}},
		ChatToolMessage: &ChatToolMessage{ToolCallID: &callID},
	}
	rms := cm.ToResponsesMessages()
	if len(rms) != 1 {
		t.Fatalf("got %d messages, want 1", len(rms))
	}
	if rms[0].Type == nil || *rms[0].Type != ResponsesMessageTypeFunctionCallOutput {
		t.Fatalf("got type %v, want function_call_output", rms[0].Type)
	}
	if rms[0].CacheControl == nil || rms[0].CacheControl.Type != CacheControlTypeEphemeral {
		t.Fatalf("item-level cache_control = %+v, want ephemeral", rms[0].CacheControl)
	}
}

func TestToolMessageWithoutMarkerStaysUnmarked(t *testing.T) {
	text := "file contents"
	callID := "call_2"
	for _, content := range []*ChatMessageContent{
		{ContentStr: &text},
		{ContentBlocks: []ChatContentBlock{{Type: ChatContentBlockTypeText, Text: &text}}},
	} {
		cm := ChatMessage{Role: ChatMessageRoleTool, Content: content, ChatToolMessage: &ChatToolMessage{ToolCallID: &callID}}
		if rms := cm.ToResponsesMessages(); rms[0].CacheControl != nil {
			t.Fatalf("unmarked tool message got cache_control %+v", rms[0].CacheControl)
		}
	}
}
