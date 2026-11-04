package promptcompression

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func chatMsgs(n int) []schemas.ChatMessage {
	msgs := make([]schemas.ChatMessage, n)
	for i := range msgs {
		s := "hello"
		msgs[i] = schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &s}}
	}
	return msgs
}

func chatBlockMsg(b schemas.ChatContentBlock) schemas.ChatMessage {
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{b}}}
}

func TestChatProtectedPrefixMarkers(t *testing.T) {
	cases := map[string]schemas.ChatContentBlock{
		"cache_control":           {Type: schemas.ChatContentBlockTypeText, CacheControl: &schemas.CacheControl{Type: "ephemeral"}},
		"prompt_cache_breakpoint": {Type: schemas.ChatContentBlockTypeText, PromptCacheBreakpoint: &schemas.PromptCacheBreakpoint{}},
		"cache_point":             {Type: schemas.ChatContentBlockTypeText, CachePoint: &schemas.CachePoint{Type: "default"}},
	}
	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			msgs := chatMsgs(6)
			msgs[3] = chatBlockMsg(block)
			if got := chatProtectedPrefix(msgs, 0); got != 4 {
				t.Fatalf("got %d, want 4", got)
			}
		})
	}
}

func TestChatProtectedPrefixToolCallCacheControl(t *testing.T) {
	msgs := chatMsgs(6)
	msgs[3].ChatAssistantMessage = &schemas.ChatAssistantMessage{
		ToolCalls: []schemas.ChatAssistantMessageToolCall{
			{}, {CacheControl: &schemas.CacheControl{Type: "ephemeral"}},
		},
	}
	if got := chatProtectedPrefix(msgs, 0); got != 4 {
		t.Fatalf("got %d, want 4", got)
	}
}

func TestChatProtectedPrefixNoMarker(t *testing.T) {
	msgs := chatMsgs(5)
	if got := chatProtectedPrefix(msgs, 1); got != 1 {
		t.Fatalf("leading=1: got %d", got)
	}
	if got := chatProtectedPrefix(msgs, 0); got != 0 {
		t.Fatalf("leading=0: got %d", got)
	}
	if got := chatProtectedPrefix(msgs, 99); got != 5 {
		t.Fatalf("leading>len: got %d, want 5", got)
	}
}

func TestChatProtectedPrefixTwoMarkers(t *testing.T) {
	msgs := chatMsgs(8)
	b := schemas.ChatContentBlock{CacheControl: &schemas.CacheControl{Type: "ephemeral"}}
	msgs[2] = chatBlockMsg(b)
	msgs[5] = chatBlockMsg(b)
	if got := chatProtectedPrefix(msgs, 1); got != 6 {
		t.Fatalf("got %d, want 6", got)
	}
}

func TestChatProtectedPrefixLeadingBeatsEarlyMarker(t *testing.T) {
	msgs := chatMsgs(5)
	msgs[0] = chatBlockMsg(schemas.ChatContentBlock{CacheControl: &schemas.CacheControl{Type: "ephemeral"}})
	if got := chatProtectedPrefix(msgs, 2); got != 2 {
		t.Fatalf("got %d, want 2", got)
	}
}

func TestChatProtectedPrefixEmptyAndNil(t *testing.T) {
	if got := chatProtectedPrefix(nil, 3); got != 0 {
		t.Fatalf("nil: got %d", got)
	}
	if got := chatProtectedPrefix([]schemas.ChatMessage{}, 0); got != 0 {
		t.Fatalf("empty: got %d", got)
	}
	msgs := []schemas.ChatMessage{
		{Role: schemas.ChatMessageRoleTool},
		{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{}},
		{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{}},
	}
	if got := chatProtectedPrefix(msgs, 1); got != 1 {
		t.Fatalf("nil content: got %d", got)
	}
}

func respMsgs(n int) []schemas.ResponsesMessage {
	msgs := make([]schemas.ResponsesMessage, n)
	for i := range msgs {
		s := "hello"
		msgs[i] = schemas.ResponsesMessage{Content: &schemas.ResponsesMessageContent{ContentStr: &s}}
	}
	return msgs
}

func TestResponsesProtectedPrefixMessageLevel(t *testing.T) {
	msgs := respMsgs(5)
	msgs[2].CacheControl = &schemas.CacheControl{Type: "ephemeral"}
	if got := responsesProtectedPrefix(msgs, 0); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}

func TestResponsesProtectedPrefixBlockLevel(t *testing.T) {
	cases := map[string]schemas.ResponsesMessageContentBlock{
		"cache_control":           {CacheControl: &schemas.CacheControl{Type: "ephemeral"}},
		"prompt_cache_breakpoint": {PromptCacheBreakpoint: &schemas.PromptCacheBreakpoint{}},
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			msgs := respMsgs(6)
			msgs[3].Content = &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{b}}
			if got := responsesProtectedPrefix(msgs, 0); got != 4 {
				t.Fatalf("got %d, want 4", got)
			}
		})
	}
}

func TestResponsesProtectedPrefixToolOutputBlock(t *testing.T) {
	msgs := respMsgs(6)
	msgs[4].ResponsesToolMessage = &schemas.ResponsesToolMessage{
		Output: &schemas.ResponsesToolMessageOutputStruct{
			ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
				{PromptCacheBreakpoint: &schemas.PromptCacheBreakpoint{}},
			},
		},
	}
	if got := responsesProtectedPrefix(msgs, 0); got != 5 {
		t.Fatalf("got %d, want 5", got)
	}
}

func TestResponsesProtectedPrefixNoMarkerAndNils(t *testing.T) {
	msgs := respMsgs(4)
	if got := responsesProtectedPrefix(msgs, 2); got != 2 {
		t.Fatalf("got %d, want 2", got)
	}
	if got := responsesProtectedPrefix(msgs, 50); got != 4 {
		t.Fatalf("got %d, want 4", got)
	}
	if got := responsesProtectedPrefix(nil, 1); got != 0 {
		t.Fatalf("nil: got %d", got)
	}
	nils := []schemas.ResponsesMessage{
		{},
		{ResponsesToolMessage: &schemas.ResponsesToolMessage{}},
		{Content: &schemas.ResponsesMessageContent{}},
	}
	if got := responsesProtectedPrefix(nils, 0); got != 0 {
		t.Fatalf("nils: got %d", got)
	}
}

func TestResponsesProtectedPrefixLeadingBeatsEarlyMarker(t *testing.T) {
	msgs := respMsgs(5)
	msgs[0].CacheControl = &schemas.CacheControl{Type: "ephemeral"}
	if got := responsesProtectedPrefix(msgs, 2); got != 2 {
		t.Fatalf("got %d, want 2", got)
	}
}
