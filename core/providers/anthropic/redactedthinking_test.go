package anthropic

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Anthropic returns redacted_thinking blocks when reasoning is flagged by its
// safety systems, and requires thinking and redacted_thinking blocks to be
// replayed unmodified on the next turn during tool use. These tests pin the
// chat-completions round trip of that payload: response conversion preserves
// it as a reasoning.encrypted detail, request conversion re-materializes the
// block, and streaming surfaces the payload from content_block_start.

// A non-streaming response mixing a redacted_thinking block with a visible
// thinking block must yield two reasoning details in content order: a
// reasoning.encrypted entry carrying the data payload, then the signed text
// entry. The tool call must come through untouched.
func TestToBifrostChatResponse_PreservesRedactedThinking(t *testing.T) {
	response := &AnthropicMessageResponse{
		ID:    "msg_1",
		Role:  "assistant",
		Model: "claude-sonnet-4-20250514",
		Content: []AnthropicContentBlock{
			{Type: AnthropicContentBlockTypeRedactedThinking, Data: schemas.Ptr("ENCRYPTED_PAYLOAD")},
			{Type: AnthropicContentBlockTypeThinking, Thinking: schemas.Ptr("visible reasoning"), Signature: schemas.Ptr("sig-1")},
			{Type: AnthropicContentBlockTypeToolUse, ID: schemas.Ptr("toolu_1"), Name: schemas.Ptr("get_weather"), Input: json.RawMessage(`{"city":"paris"}`)},
		},
		StopReason: AnthropicStopReasonToolUse,
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result := response.ToBifrostChatResponse(ctx)

	if len(result.Choices) != 1 || result.Choices[0].Message == nil || result.Choices[0].Message.ChatAssistantMessage == nil {
		t.Fatalf("unexpected choices: %+v", result.Choices)
	}
	details := result.Choices[0].Message.ChatAssistantMessage.ReasoningDetails
	if len(details) != 2 {
		t.Fatalf("expected 2 reasoning details (encrypted + text), got %d: %+v", len(details), details)
	}
	if details[0].Type != schemas.BifrostReasoningDetailsTypeEncrypted {
		t.Errorf("details[0].Type = %q, want %q", details[0].Type, schemas.BifrostReasoningDetailsTypeEncrypted)
	}
	if details[0].Data == nil || *details[0].Data != "ENCRYPTED_PAYLOAD" {
		t.Errorf("details[0].Data = %v, want ENCRYPTED_PAYLOAD", details[0].Data)
	}
	if details[0].Index != 0 || details[1].Index != 1 {
		t.Errorf("detail indices = %d, %d, want 0, 1", details[0].Index, details[1].Index)
	}
	if details[1].Type != schemas.BifrostReasoningDetailsTypeText || details[1].Text == nil || *details[1].Text != "visible reasoning" {
		t.Errorf("details[1] mangled: %+v", details[1])
	}
	if details[1].Signature == nil || *details[1].Signature != "sig-1" {
		t.Errorf("details[1].Signature = %v, want sig-1", details[1].Signature)
	}
}

// Redacted blocks with no data payload (absent or empty string) carry
// nothing to replay, so response conversion must not produce reasoning
// details for them.
func TestToBifrostChatResponse_RedactedThinkingWithoutDataSkipped(t *testing.T) {
	response := &AnthropicMessageResponse{
		ID:    "msg_1",
		Role:  "assistant",
		Model: "claude-sonnet-4-20250514",
		Content: []AnthropicContentBlock{
			{Type: AnthropicContentBlockTypeRedactedThinking},
			{Type: AnthropicContentBlockTypeRedactedThinking, Data: schemas.Ptr("")},
			{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("hello")},
		},
		StopReason: AnthropicStopReasonEndTurn,
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result := response.ToBifrostChatResponse(ctx)

	msg := result.Choices[0].Message
	if msg.ChatAssistantMessage != nil && len(msg.ChatAssistantMessage.ReasoningDetails) != 0 {
		t.Errorf("expected no reasoning details for data-less or empty-data redacted blocks, got %+v", msg.ChatAssistantMessage.ReasoningDetails)
	}
}

// Request conversion must re-materialize a reasoning.encrypted detail as a
// redacted_thinking block (data only, no thinking or signature fields),
// placed before the visible thinking block and the tool_use block, so the
// assistant turn is replayed exactly as the model produced it.
func TestToAnthropicChatRequest_ReplaysRedactedThinking(t *testing.T) {
	toolID := "toolu_1"
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4-20250514",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("weather in paris?")}},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ReasoningDetails: []schemas.ChatReasoningDetails{
						{Index: 0, Type: schemas.BifrostReasoningDetailsTypeEncrypted, Data: schemas.Ptr("ENCRYPTED_PAYLOAD")},
						{Index: 1, Type: schemas.BifrostReasoningDetailsTypeText, Text: schemas.Ptr("visible reasoning"), Signature: schemas.Ptr("sig-1")},
					},
					ToolCalls: []schemas.ChatAssistantMessageToolCall{
						{Index: 0, Type: schemas.Ptr("function"), ID: &toolID,
							Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_weather"), Arguments: `{"city":"paris"}`}},
					},
				},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: &toolID},
				Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("22C sunny")},
			},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: schemas.Ptr(2048),
			Reasoning:           &schemas.ChatReasoning{MaxTokens: schemas.Ptr(1024)},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(result.Messages))
	}
	assistant := result.Messages[1]
	blocks := assistant.Content.ContentBlocks
	if len(blocks) != 3 {
		t.Fatalf("expected 3 assistant content blocks (redacted_thinking, thinking, tool_use), got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Type != AnthropicContentBlockTypeRedactedThinking {
		t.Errorf("blocks[0].Type = %q, want redacted_thinking", blocks[0].Type)
	}
	if blocks[0].Data == nil || *blocks[0].Data != "ENCRYPTED_PAYLOAD" {
		t.Errorf("blocks[0].Data = %v, want ENCRYPTED_PAYLOAD", blocks[0].Data)
	}
	if blocks[0].Thinking != nil || blocks[0].Signature != nil {
		t.Errorf("redacted block must carry only data, got thinking=%v signature=%v", blocks[0].Thinking, blocks[0].Signature)
	}
	if blocks[1].Type != AnthropicContentBlockTypeThinking || blocks[1].Thinking == nil || *blocks[1].Thinking != "visible reasoning" {
		t.Errorf("blocks[1] mangled: %+v", blocks[1])
	}
	if blocks[2].Type != AnthropicContentBlockTypeToolUse {
		t.Errorf("blocks[2].Type = %q, want tool_use", blocks[2].Type)
	}
}

func TestToAnthropicChatRequest_EncryptedDetailWithoutDataStaysThinking(t *testing.T) {
	// Encrypted reasoning details produced by other providers (e.g. gemini
	// thought signatures) carry a signature but no data, and a client may
	// send an empty data string; both must keep the historical
	// thinking-block mapping, not become redacted_thinking.
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4-20250514",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
			{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ReasoningDetails: []schemas.ChatReasoningDetails{
						{Index: 0, Type: schemas.BifrostReasoningDetailsTypeEncrypted, Signature: schemas.Ptr("gemini-sig")},
						{Index: 1, Type: schemas.BifrostReasoningDetailsTypeEncrypted, Data: schemas.Ptr("")},
					},
				},
			},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("and now?")}},
		},
		Params: &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(2048)},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, block := range result.Messages[1].Content.ContentBlocks {
		if block.Type == AnthropicContentBlockTypeRedactedThinking {
			t.Errorf("encrypted detail without a payload must not become redacted_thinking: %+v", block)
		}
	}
}

// A redacted_thinking content_block_start carries the complete payload (no
// deltas follow), so it must emit one chunk with a reasoning.encrypted
// detail. Starts with no payload, or for plain text blocks, stay silent.
func TestToBifrostChatCompletionStream_RedactedThinkingStart(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	state := NewAnthropicStreamState()

	chunk := &AnthropicStreamEvent{
		Type:         AnthropicStreamEventTypeContentBlockStart,
		Index:        schemas.Ptr(0),
		ContentBlock: &AnthropicContentBlock{Type: AnthropicContentBlockTypeRedactedThinking, Data: schemas.Ptr("ENCRYPTED_PAYLOAD")},
	}
	resp, bifrostErr, done := chunk.ToBifrostChatCompletionStream(ctx, "", state)
	if bifrostErr != nil || done {
		t.Fatalf("unexpected err=%v done=%v", bifrostErr, done)
	}
	if resp == nil {
		t.Fatalf("redacted_thinking content_block_start produced no chunk")
	}
	delta := resp.Choices[0].ChatStreamResponseChoice.Delta
	if len(delta.ReasoningDetails) != 1 {
		t.Fatalf("expected 1 reasoning detail, got %+v", delta.ReasoningDetails)
	}
	if delta.ReasoningDetails[0].Type != schemas.BifrostReasoningDetailsTypeEncrypted {
		t.Errorf("detail type = %q, want %q", delta.ReasoningDetails[0].Type, schemas.BifrostReasoningDetailsTypeEncrypted)
	}
	if delta.ReasoningDetails[0].Data == nil || *delta.ReasoningDetails[0].Data != "ENCRYPTED_PAYLOAD" {
		t.Errorf("detail data = %v, want ENCRYPTED_PAYLOAD", delta.ReasoningDetails[0].Data)
	}

	// A redacted block without data (or with empty data) and a plain text
	// block start still produce no chunk.
	for _, block := range []*AnthropicContentBlock{
		{Type: AnthropicContentBlockTypeRedactedThinking},
		{Type: AnthropicContentBlockTypeRedactedThinking, Data: schemas.Ptr("")},
		{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("")},
	} {
		chunk := &AnthropicStreamEvent{Type: AnthropicStreamEventTypeContentBlockStart, Index: schemas.Ptr(1), ContentBlock: block}
		resp, bifrostErr, done := chunk.ToBifrostChatCompletionStream(ctx, "", state)
		if resp != nil || bifrostErr != nil || done {
			t.Errorf("block %q: expected silent skip, got resp=%v err=%v done=%v", block.Type, resp, bifrostErr, done)
		}
	}
}

// A stream mixing redacted and visible thinking blocks must keep one
// reasoning_details index per content block. The accumulator and replaying
// clients group reasoning deltas by that index; if a redacted block shares
// an index with a visible thinking block, the merged detail loses the
// encrypted type and the payload is dropped again on replay.
func TestToBifrostChatCompletionStream_MixedReasoningBlocksKeepDistinctIndices(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	state := NewAnthropicStreamState()

	detailIndexes := func(resp *schemas.BifrostChatResponse) []int {
		if resp == nil {
			t.Fatalf("expected a chunk, got nil")
		}
		var idxs []int
		for _, rd := range resp.Choices[0].ChatStreamResponseChoice.Delta.ReasoningDetails {
			idxs = append(idxs, rd.Index)
		}
		return idxs
	}

	// Block 0: redacted thinking.
	resp, _, _ := (&AnthropicStreamEvent{
		Type:         AnthropicStreamEventTypeContentBlockStart,
		Index:        schemas.Ptr(0),
		ContentBlock: &AnthropicContentBlock{Type: AnthropicContentBlockTypeRedactedThinking, Data: schemas.Ptr("ENC_A")},
	}).ToBifrostChatCompletionStream(ctx, "", state)
	if got := detailIndexes(resp); len(got) != 1 || got[0] != 0 {
		t.Errorf("redacted block 0: detail indices = %v, want [0]", got)
	}

	// Block 1: visible thinking, then its signature.
	resp, _, _ = (&AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockDelta,
		Index: schemas.Ptr(1),
		Delta: &AnthropicStreamDelta{Type: AnthropicStreamDeltaTypeThinking, Thinking: schemas.Ptr("visible")},
	}).ToBifrostChatCompletionStream(ctx, "", state)
	if got := detailIndexes(resp); len(got) != 1 || got[0] != 1 {
		t.Errorf("thinking delta block 1: detail indices = %v, want [1]", got)
	}
	resp, _, _ = (&AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockDelta,
		Index: schemas.Ptr(1),
		Delta: &AnthropicStreamDelta{Type: AnthropicStreamDeltaTypeSignature, Signature: schemas.Ptr("sig-1")},
	}).ToBifrostChatCompletionStream(ctx, "", state)
	if got := detailIndexes(resp); len(got) != 1 || got[0] != 1 {
		t.Errorf("signature delta block 1: detail indices = %v, want [1]", got)
	}

	// Block 2: a second redacted thinking block.
	resp, _, _ = (&AnthropicStreamEvent{
		Type:         AnthropicStreamEventTypeContentBlockStart,
		Index:        schemas.Ptr(2),
		ContentBlock: &AnthropicContentBlock{Type: AnthropicContentBlockTypeRedactedThinking, Data: schemas.Ptr("ENC_B")},
	}).ToBifrostChatCompletionStream(ctx, "", state)
	if got := detailIndexes(resp); len(got) != 1 || got[0] != 2 {
		t.Errorf("redacted block 2: detail indices = %v, want [2]", got)
	}
}
