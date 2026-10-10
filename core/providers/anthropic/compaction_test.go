package anthropic

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// --- isCompactionItem tests ---

func TestIsCompactionItem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		item     *schemas.ResponsesMessage
		expected bool
	}{
		{
			name:     "nil item",
			item:     nil,
			expected: false,
		},
		{
			name: "nil type",
			item: &schemas.ResponsesMessage{
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{Type: schemas.ResponsesOutputMessageContentTypeCompaction},
					},
				},
			},
			expected: false,
		},
		{
			name: "message type with compaction content block",
			item: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeCompaction,
							ResponsesOutputMessageContentCompaction: &schemas.ResponsesOutputMessageContentCompaction{
								Summary: "Summary of conversation",
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "message type with text content block",
			item: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeText,
							Text: schemas.Ptr("Hello"),
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "function call type",
			item: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			},
			expected: false,
		},
		{
			name: "message type with nil content",
			item: &schemas.ResponsesMessage{
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Content: nil,
			},
			expected: false,
		},
		{
			name: "message type with empty content blocks",
			item: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isCompactionItem(tt.item)
			if result != tt.expected {
				t.Errorf("isCompactionItem() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// --- Streaming: Anthropic → Bifrost (inbound) ---

func TestToBifrostResponsesStream_CompactionContentBlockStart(t *testing.T) {
	t.Parallel()

	state := &AnthropicResponsesStreamState{
		ContentIndexToOutputIndex: make(map[int]int),
		ContentIndexToBlockType:   make(map[int]AnthropicContentBlockType),
		ToolArgumentBuffers:       make(map[int]string),
		MCPCallOutputIndices:      make(map[int]bool),
		ItemIDs:                   make(map[int]string),
		OutputItems:               make(map[int]*schemas.ResponsesMessage),
		ReasoningSignatures:       make(map[int]string),
		TextContentIndices:        make(map[int]bool),
		ReasoningContentIndices:   make(map[int]bool),
		CompactionContentIndices:  make(map[int]*schemas.CacheControl),
		CurrentOutputIndex:        0,
		CreatedAt:                 1234567890,
		HasEmittedCreated:         true,
		HasEmittedInProgress:      true,
	}

	// content_block_start with compaction type should return nil (defers to delta)
	chunk := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockStart,
		Index: schemas.Ptr(0),
		ContentBlock: &AnthropicContentBlock{
			Type: AnthropicContentBlockTypeCompaction,
			CacheControl: &schemas.CacheControl{
				Type: "ephemeral",
			},
		},
	}

	responses, err, isLast := chunk.ToBifrostResponsesStream(context.Background(), 0, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isLast {
		t.Error("should not be last chunk")
	}
	if len(responses) != 0 {
		t.Errorf("expected 0 responses for compaction content_block_start, got %d", len(responses))
	}

	// Verify state was tracked
	if _, exists := state.CompactionContentIndices[0]; !exists {
		t.Error("expected compaction to be tracked in CompactionContentIndices")
	}
	if blockType, exists := state.ContentIndexToBlockType[0]; !exists || blockType != AnthropicContentBlockTypeCompaction {
		t.Error("expected compaction block type tracked in ContentIndexToBlockType")
	}
}

func TestToBifrostResponsesStream_CompactionDelta(t *testing.T) {
	t.Parallel()

	state := &AnthropicResponsesStreamState{
		ContentIndexToOutputIndex: map[int]int{0: 0},
		ContentIndexToBlockType:   map[int]AnthropicContentBlockType{0: AnthropicContentBlockTypeCompaction},
		ToolArgumentBuffers:       make(map[int]string),
		MCPCallOutputIndices:      make(map[int]bool),
		ItemIDs:                   map[int]string{0: "cmp_0"},
		OutputItems:               make(map[int]*schemas.ResponsesMessage),
		ReasoningSignatures:       make(map[int]string),
		TextContentIndices:        make(map[int]bool),
		ReasoningContentIndices:   make(map[int]bool),
		CompactionContentIndices:  map[int]*schemas.CacheControl{0: {Type: "ephemeral"}},
		CurrentOutputIndex:        1,
		CreatedAt:                 1234567890,
		HasEmittedCreated:         true,
		HasEmittedInProgress:      true,
	}

	summary := "The user asked about building a website. We discussed HTML, CSS, and JavaScript."
	chunk := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockDelta,
		Index: schemas.Ptr(0),
		Delta: &AnthropicStreamDelta{
			Type:    AnthropicStreamDeltaTypeCompaction,
			Content: &summary,
		},
	}

	responses, err, isLast := chunk.ToBifrostResponsesStream(context.Background(), 0, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isLast {
		t.Error("should not be last chunk")
	}

	// Should emit output_item.added and output_item.done
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses for compaction delta, got %d", len(responses))
	}

	// First: output_item.added
	added := responses[0]
	if added.Type != schemas.ResponsesStreamResponseTypeOutputItemAdded {
		t.Errorf("first response type = %v, want %v", added.Type, schemas.ResponsesStreamResponseTypeOutputItemAdded)
	}
	if added.Item == nil || added.Item.Content == nil || len(added.Item.Content.ContentBlocks) == 0 {
		t.Fatal("output_item.added should have content blocks")
	}
	block := added.Item.Content.ContentBlocks[0]
	if block.Type != schemas.ResponsesOutputMessageContentTypeCompaction {
		t.Errorf("content block type = %v, want compaction", block.Type)
	}
	if block.ResponsesOutputMessageContentCompaction == nil {
		t.Fatal("expected compaction content to be non-nil")
	}
	if block.ResponsesOutputMessageContentCompaction.Summary != summary {
		t.Errorf("summary = %q, want %q", block.ResponsesOutputMessageContentCompaction.Summary, summary)
	}
	// Cache control should be preserved from content_block_start
	if block.CacheControl == nil || block.CacheControl.Type != "ephemeral" {
		t.Error("expected cache control to be preserved")
	}

	// Second: output_item.done
	done := responses[1]
	if done.Type != schemas.ResponsesStreamResponseTypeOutputItemDone {
		t.Errorf("second response type = %v, want %v", done.Type, schemas.ResponsesStreamResponseTypeOutputItemDone)
	}
}

func TestToBifrostResponsesStream_CompactionContentBlockStop(t *testing.T) {
	t.Parallel()

	state := &AnthropicResponsesStreamState{
		ContentIndexToOutputIndex: map[int]int{0: 0},
		ContentIndexToBlockType:   map[int]AnthropicContentBlockType{0: AnthropicContentBlockTypeCompaction},
		ToolArgumentBuffers:       make(map[int]string),
		MCPCallOutputIndices:      make(map[int]bool),
		ItemIDs:                   map[int]string{0: "cmp_0"},
		OutputItems:               make(map[int]*schemas.ResponsesMessage),
		ReasoningSignatures:       make(map[int]string),
		TextContentIndices:        make(map[int]bool),
		ReasoningContentIndices:   make(map[int]bool),
		CompactionContentIndices:  make(map[int]*schemas.CacheControl),
		CurrentOutputIndex:        1,
		CreatedAt:                 1234567890,
		HasEmittedCreated:         true,
		HasEmittedInProgress:      true,
	}

	chunk := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockStop,
		Index: schemas.Ptr(0),
	}

	responses, err, isLast := chunk.ToBifrostResponsesStream(context.Background(), 0, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isLast {
		t.Error("should not be last chunk")
	}
	// content_block_stop for compaction should return nil (done was already emitted with delta)
	if len(responses) != 0 {
		t.Errorf("expected 0 responses for compaction content_block_stop, got %d", len(responses))
	}
}

// --- Streaming: Bifrost → Anthropic (outbound, non-passthrough) ---

func TestToAnthropicResponsesStreamResponse_CompactionOutputItemAdded(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	summary := "Summary of the conversation about building a website"
	bifrostResp := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
		OutputIndex: schemas.Ptr(0),
		Item: &schemas.ResponsesMessage{
			ID:     schemas.Ptr("cmp_test123"),
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Status: schemas.Ptr("completed"),
			Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{
						Type: schemas.ResponsesOutputMessageContentTypeCompaction,
						ResponsesOutputMessageContentCompaction: &schemas.ResponsesOutputMessageContentCompaction{
							Summary: summary,
						},
						CacheControl: &schemas.CacheControl{Type: "ephemeral"},
					},
				},
			},
		},
	}

	events := ToAnthropicResponsesStreamResponse(ctx, bifrostResp)

	// Should emit: content_block_start (compaction) + content_block_delta (compaction_delta)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}

	// Event 1: content_block_start
	start := events[0]
	if start.Type != AnthropicStreamEventTypeContentBlockStart {
		t.Errorf("event[0] type = %v, want content_block_start", start.Type)
	}
	if start.ContentBlock == nil {
		t.Fatal("content_block_start should have ContentBlock")
	}
	if start.ContentBlock.Type != AnthropicContentBlockTypeCompaction {
		t.Errorf("ContentBlock.Type = %v, want compaction", start.ContentBlock.Type)
	}
	if start.ContentBlock.CacheControl == nil || start.ContentBlock.CacheControl.Type != "ephemeral" {
		t.Error("expected cache control to be preserved on content_block_start")
	}

	// Event 2: content_block_delta with compaction_delta
	delta := events[1]
	if delta.Type != AnthropicStreamEventTypeContentBlockDelta {
		t.Errorf("event[1] type = %v, want content_block_delta", delta.Type)
	}
	if delta.Delta == nil {
		t.Fatal("content_block_delta should have Delta")
	}
	if delta.Delta.Type != AnthropicStreamDeltaTypeCompaction {
		t.Errorf("Delta.Type = %v, want compaction_delta", delta.Delta.Type)
	}
	if delta.Delta.Content == nil || *delta.Delta.Content != summary {
		t.Errorf("Delta.Content = %v, want %q", delta.Delta.Content, summary)
	}
}

func TestToAnthropicResponsesStreamResponse_CompactionOutputItemDone(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	bifrostResp := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemDone,
		OutputIndex: schemas.Ptr(0),
		ItemID:      schemas.Ptr("cmp_test123"),
		Item: &schemas.ResponsesMessage{
			ID:     schemas.Ptr("cmp_test123"),
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Status: schemas.Ptr("completed"),
			Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{
						Type: schemas.ResponsesOutputMessageContentTypeCompaction,
						ResponsesOutputMessageContentCompaction: &schemas.ResponsesOutputMessageContentCompaction{
							Summary: "Summary text",
						},
					},
				},
			},
		},
	}

	events := ToAnthropicResponsesStreamResponse(ctx, bifrostResp)

	// Should emit content_block_stop
	if len(events) != 1 {
		t.Fatalf("expected 1 event for output_item.done, got %d", len(events))
	}

	stop := events[0]
	if stop.Type != AnthropicStreamEventTypeContentBlockStop {
		t.Errorf("event type = %v, want content_block_stop", stop.Type)
	}
}

func TestToAnthropicResponsesStreamResponse_TextOutputItemAdded_NotAffectedByCompactionCheck(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	// Regular text message should still emit content_block_start with type=text
	bifrostResp := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
		OutputIndex: schemas.Ptr(0),
		Item: &schemas.ResponsesMessage{
			ID:     schemas.Ptr("msg_test123"),
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Status: schemas.Ptr("in_progress"),
			Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{
						Type: schemas.ResponsesOutputMessageContentTypeText,
						Text: schemas.Ptr(""),
					},
				},
			},
		},
	}

	events := ToAnthropicResponsesStreamResponse(ctx, bifrostResp)
	if len(events) == 0 {
		t.Fatal("expected at least 1 event")
	}

	start := events[0]
	if start.Type != AnthropicStreamEventTypeContentBlockStart {
		t.Errorf("event type = %v, want content_block_start", start.Type)
	}
	if start.ContentBlock == nil {
		t.Fatal("expected ContentBlock to be non-nil")
	}
	if start.ContentBlock.Type != AnthropicContentBlockTypeText {
		t.Errorf("ContentBlock.Type = %v, want text", start.ContentBlock.Type)
	}
}

// --- Non-Streaming: stop_reason mapping ---

func TestToBifrostResponsesResponse_PreservesStopReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		stopReason         AnthropicStopReason
		expectedStopReason string
	}{
		{
			name:               "compaction stop reason",
			stopReason:         AnthropicStopReasonCompaction,
			expectedStopReason: "compaction",
		},
		{
			name:               "end_turn stop reason",
			stopReason:         AnthropicStopReasonEndTurn,
			expectedStopReason: "stop",
		},
		{
			name:               "tool_use stop reason",
			stopReason:         AnthropicStopReasonToolUse,
			expectedStopReason: "tool_calls",
		},
		{
			name:               "max_tokens stop reason",
			stopReason:         AnthropicStopReasonMaxTokens,
			expectedStopReason: "length",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()

			resp := &AnthropicMessageResponse{
				ID:         "msg_test",
				Type:       "message",
				Role:       "assistant",
				Model:      "claude-sonnet-4-6",
				StopReason: tt.stopReason,
				Content: []AnthropicContentBlock{
					{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("Hello")},
				},
			}

			bifrostResp := resp.ToBifrostResponsesResponse(ctx)

			if bifrostResp.StopReason == nil {
				t.Fatal("expected StopReason to be non-nil")
			}
			if *bifrostResp.StopReason != tt.expectedStopReason {
				t.Errorf("StopReason = %q, want %q", *bifrostResp.StopReason, tt.expectedStopReason)
			}
		})
	}
}

func TestToBifrostResponsesResponse_EmptyStopReason(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	resp := &AnthropicMessageResponse{
		ID:      "msg_test",
		Type:    "message",
		Role:    "assistant",
		Model:   "claude-sonnet-4-6",
		Content: []AnthropicContentBlock{},
	}

	bifrostResp := resp.ToBifrostResponsesResponse(ctx)

	if bifrostResp.StopReason != nil {
		t.Errorf("expected nil StopReason for empty stop_reason, got %q", *bifrostResp.StopReason)
	}
}

func TestToAnthropicResponsesResponse_StopReasonFromBifrost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		stopReason     *string
		contentBlocks  []schemas.ResponsesMessage
		expectedReason AnthropicStopReason
	}{
		{
			name:           "compaction stop reason from bifrost",
			stopReason:     schemas.Ptr("compaction"),
			expectedReason: AnthropicStopReasonCompaction,
		},
		{
			name:           "end_turn mapped from stop",
			stopReason:     schemas.Ptr("stop"),
			expectedReason: AnthropicStopReasonEndTurn,
		},
		{
			name:           "tool_use mapped from tool_calls",
			stopReason:     schemas.Ptr("tool_calls"),
			expectedReason: AnthropicStopReasonToolUse,
		},
		{
			name:           "nil stop_reason defaults to end_turn",
			stopReason:     nil,
			expectedReason: AnthropicStopReasonEndTurn,
		},
		{
			name:       "nil stop_reason with tool_use content defaults to tool_use",
			stopReason: nil,
			contentBlocks: []schemas.ResponsesMessage{
				{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: schemas.Ptr("call_123"),
						Name:   schemas.Ptr("my_tool"),
					},
				},
			},
			expectedReason: AnthropicStopReasonToolUse,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()

			bifrostResp := &schemas.BifrostResponsesResponse{
				ID:         schemas.Ptr("resp_test"),
				Model:      "claude-sonnet-4-6",
				StopReason: tt.stopReason,
				Output:     tt.contentBlocks,
			}

			result := ToAnthropicResponsesResponse(ctx, bifrostResp)

			if result.StopReason != tt.expectedReason {
				t.Errorf("StopReason = %v, want %v", result.StopReason, tt.expectedReason)
			}
		})
	}
}

// --- Non-Streaming: compaction content block round-trip ---

func TestCompactionContentBlock_NonStreamingRoundTrip(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	summary := "The user requested help building a web scraper using Python with BeautifulSoup."

	// Simulate Anthropic response with compaction block
	anthropicResp := &AnthropicMessageResponse{
		ID:         "msg_compaction_test",
		Type:       "message",
		Role:       "assistant",
		Model:      "claude-opus-4-6",
		StopReason: AnthropicStopReasonCompaction,
		Content: []AnthropicContentBlock{
			{
				Type: AnthropicContentBlockTypeCompaction,
				Content: &AnthropicContent{
					ContentStr: &summary,
				},
				CacheControl: &schemas.CacheControl{Type: "ephemeral"},
			},
		},
	}

	// Step 1: Anthropic → Bifrost
	bifrostResp := anthropicResp.ToBifrostResponsesResponse(ctx)

	if bifrostResp.StopReason == nil || *bifrostResp.StopReason != "compaction" {
		t.Fatalf("expected stop_reason='compaction', got %v", bifrostResp.StopReason)
	}
	if len(bifrostResp.Output) == 0 {
		t.Fatal("expected at least one output message")
	}

	// Find the compaction block
	var foundCompaction bool
	for _, msg := range bifrostResp.Output {
		if msg.Content != nil {
			for _, block := range msg.Content.ContentBlocks {
				if block.Type == schemas.ResponsesOutputMessageContentTypeCompaction {
					foundCompaction = true
					if block.ResponsesOutputMessageContentCompaction == nil {
						t.Fatal("expected compaction content to be non-nil")
					}
					if block.ResponsesOutputMessageContentCompaction.Summary != summary {
						t.Errorf("summary = %q, want %q", block.ResponsesOutputMessageContentCompaction.Summary, summary)
					}
				}
			}
		}
	}
	if !foundCompaction {
		t.Error("compaction block not found in Bifrost output")
	}

	// Step 2: Bifrost → Anthropic
	result := ToAnthropicResponsesResponse(ctx, bifrostResp)

	if result.StopReason != AnthropicStopReasonCompaction {
		t.Errorf("result StopReason = %v, want compaction", result.StopReason)
	}

	// Find compaction content block in result
	var foundResultCompaction bool
	for _, block := range result.Content {
		if block.Type == AnthropicContentBlockTypeCompaction {
			foundResultCompaction = true
			if block.Content == nil || block.Content.ContentStr == nil {
				t.Fatal("expected compaction content string")
			}
			if *block.Content.ContentStr != summary {
				t.Errorf("result summary = %q, want %q", *block.Content.ContentStr, summary)
			}
			if block.CacheControl == nil || block.CacheControl.Type != "ephemeral" {
				t.Error("expected cache control to be preserved")
			}
		}
	}
	if !foundResultCompaction {
		t.Error("compaction block not found in Anthropic result")
	}
}

// --- Streaming: compaction stop_reason in response.completed ---

func TestToAnthropicResponsesStreamResponse_CompletedWithCompactionStopReason(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	bifrostResp := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeCompleted,
		Response: &schemas.BifrostResponsesResponse{
			ID:         schemas.Ptr("resp_test"),
			Model:      "claude-opus-4-6",
			StopReason: schemas.Ptr("compaction"),
			Usage: &schemas.ResponsesResponseUsage{
				InputTokens:  1000,
				OutputTokens: 500,
				TotalTokens:  1500,
			},
		},
	}

	events := ToAnthropicResponsesStreamResponse(ctx, bifrostResp)

	// Should emit message_delta + message_stop
	if len(events) != 2 {
		t.Fatalf("expected 2 events for response.completed, got %d", len(events))
	}

	// message_delta should have stop_reason=compaction
	messageDelta := events[0]
	if messageDelta.Type != AnthropicStreamEventTypeMessageDelta {
		t.Errorf("event[0] type = %v, want message_delta", messageDelta.Type)
	}
	if messageDelta.Delta == nil || messageDelta.Delta.StopReason == nil {
		t.Fatal("expected Delta.StopReason in message_delta")
	}
	if *messageDelta.Delta.StopReason != AnthropicStopReasonCompaction {
		t.Errorf("StopReason = %v, want compaction", *messageDelta.Delta.StopReason)
	}

	// message_stop
	messageStop := events[1]
	if messageStop.Type != AnthropicStreamEventTypeMessageStop {
		t.Errorf("event[1] type = %v, want message_stop", messageStop.Type)
	}
}

func TestMixedTextThenToolCallsStreamMapsStopReasonToToolUse(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	state := schemas.AcquireChatToResponsesStreamState()
	defer schemas.ReleaseChatToResponsesStreamState(state)

	content := "\n\nNow let me fetch the source branch:"
	toolCallID := "chatcmpl-tool-123"
	toolName := "Bash"
	toolType := "function"
	arguments := `{"command":"git fetch origin feat-IGPS-15746-controller-health-endpoint"}`
	finishReason := string(schemas.BifrostFinishReasonToolCalls)

	chatChunks := []*schemas.BifrostChatResponse{
		{
			Choices: []schemas.BifrostResponseChoice{
				{
					Index: 0,
					ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
						Delta: &schemas.ChatStreamResponseChoiceDelta{Content: &content},
					},
				},
			},
		},
		{
			Choices: []schemas.BifrostResponseChoice{
				{
					Index: 0,
					ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
						Delta: &schemas.ChatStreamResponseChoiceDelta{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{
								{
									Index: 0,
									ID:    &toolCallID,
									Type:  &toolType,
									Function: schemas.ChatAssistantMessageToolCallFunction{
										Name:      &toolName,
										Arguments: arguments,
									},
								},
							},
						},
					},
				},
			},
		},
		{
			Choices: []schemas.BifrostResponseChoice{
				{
					Index:        0,
					FinishReason: &finishReason,
					ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
						Delta: &schemas.ChatStreamResponseChoiceDelta{},
					},
				},
			},
		},
	}

	var events []*AnthropicStreamEvent
	for _, chunk := range chatChunks {
		for _, responseEvent := range chunk.ToBifrostResponsesStreamResponse(state) {
			events = append(events, ToAnthropicResponsesStreamResponse(ctx, responseEvent)...)
		}
	}

	var messageDelta *AnthropicStreamEvent
	var hasMessageStop bool
	for _, event := range events {
		if event.Type == AnthropicStreamEventTypeMessageDelta {
			messageDelta = event
		}
		if event.Type == AnthropicStreamEventTypeMessageStop {
			hasMessageStop = true
		}
	}

	if messageDelta == nil || messageDelta.Delta == nil || messageDelta.Delta.StopReason == nil {
		t.Fatalf("expected message_delta stop_reason in events: %#v", events)
	}
	if *messageDelta.Delta.StopReason != AnthropicStopReasonToolUse {
		t.Fatalf("message_delta stop_reason = %q, want %q", *messageDelta.Delta.StopReason, AnthropicStopReasonToolUse)
	}
	if !hasMessageStop {
		t.Fatal("expected message_stop event in mixed text+tool_use stream")
	}
}

// TestReplayedCompactionBlockSurvivesConversion pins issue #8082: a compaction
// block replayed in an assistant turn must reach the provider. Bedrock requests
// use the grouped converter, which used to drop the block, so every later step
// resent the full history and compacted again.
func TestReplayedCompactionBlockSurvivesConversion(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	summary := "Summary of the earlier turns"
	messages := []AnthropicMessage{
		anthMsg(AnthropicMessageRoleUser, "Refactor the parser"),
		{Role: AnthropicMessageRoleAssistant, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{
			{Type: AnthropicContentBlockTypeCompaction, Content: &AnthropicContent{ContentStr: &summary}},
			{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("Continuing from the summary.")},
		}}},
		anthMsg(AnthropicMessageRoleUser, "Go on"),
	}

	for _, keepToolsGrouped := range []bool{false, true} {
		out := ConvertAnthropicMessagesToBifrostMessages(ctx, messages, nil, false, keepToolsGrouped)

		compactionIndex, textIndex := -1, -1
		for i := range out {
			if isCompactionItem(&out[i]) {
				compactionIndex = i
				block := out[i].Content.ContentBlocks[0]
				if block.ResponsesOutputMessageContentCompaction == nil || block.ResponsesOutputMessageContentCompaction.Summary != summary {
					t.Errorf("keepToolsGrouped=%v: compaction summary = %+v, want %q", keepToolsGrouped, block.ResponsesOutputMessageContentCompaction, summary)
				}
				if out[i].Status == nil || *out[i].Status != "completed" {
					t.Errorf("keepToolsGrouped=%v: replayed compaction item has status %v, want completed", keepToolsGrouped, out[i].Status)
				}
			} else if out[i].Content != nil && len(out[i].Content.ContentBlocks) > 0 && out[i].Content.ContentBlocks[0].Text != nil &&
				*out[i].Content.ContentBlocks[0].Text == "Continuing from the summary." {
				textIndex = i
			}
		}
		if compactionIndex == -1 {
			t.Fatalf("keepToolsGrouped=%v: compaction block was dropped", keepToolsGrouped)
		}
		if textIndex == -1 || compactionIndex > textIndex {
			t.Errorf("keepToolsGrouped=%v: compaction item at %d, text at %d; want the compaction first", keepToolsGrouped, compactionIndex, textIndex)
		}

		// Converting back must restore the compaction block in the assistant turn.
		anthropicMessages, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, out, true, schemas.ResolveModelCaps(schemas.Bedrock, "claude-opus-5-5"))
		var restored *AnthropicContentBlock
		for i := range anthropicMessages {
			if anthropicMessages[i].Role != AnthropicMessageRoleAssistant {
				continue
			}
			for j := range anthropicMessages[i].Content.ContentBlocks {
				if anthropicMessages[i].Content.ContentBlocks[j].Type == AnthropicContentBlockTypeCompaction {
					restored = &anthropicMessages[i].Content.ContentBlocks[j]
				}
			}
		}
		if restored == nil {
			t.Fatalf("keepToolsGrouped=%v: compaction block missing after converting back", keepToolsGrouped)
		}
		if restored.Content == nil || restored.Content.ContentStr == nil || *restored.Content.ContentStr != summary {
			t.Errorf("keepToolsGrouped=%v: restored compaction content = %+v, want %q", keepToolsGrouped, restored.Content, summary)
		}
	}
}

// TestToBifrostResponsesStream_CompactionToolChanges (#8207): a streamed compaction block
// may carry tool_changes on its content_block_start (the server-recorded net tool set of the
// compacted range). It must ride along to the item emitted on the compaction delta, and the
// Anthropic egress must put it back on the content_block_start it rebuilds.
func TestToBifrostResponsesStream_CompactionToolChanges(t *testing.T) {
	t.Parallel()

	state := &AnthropicResponsesStreamState{
		ContentIndexToOutputIndex: make(map[int]int),
		ContentIndexToBlockType:   make(map[int]AnthropicContentBlockType),
		ToolArgumentBuffers:       make(map[int]string),
		MCPCallOutputIndices:      make(map[int]bool),
		ItemIDs:                   make(map[int]string),
		OutputItems:               make(map[int]*schemas.ResponsesMessage),
		ReasoningSignatures:       make(map[int]string),
		TextContentIndices:        make(map[int]bool),
		ReasoningContentIndices:   make(map[int]bool),
		CompactionContentIndices:  make(map[int]*schemas.CacheControl),
		CurrentOutputIndex:        0,
		CreatedAt:                 1234567890,
		HasEmittedCreated:         true,
		HasEmittedInProgress:      true,
	}

	start := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockStart,
		Index: schemas.Ptr(0),
		ContentBlock: &AnthropicContentBlock{
			Type: AnthropicContentBlockTypeCompaction,
			ToolChanges: []AnthropicContentBlock{{
				Type: AnthropicContentBlockTypeToolRemoval,
				Tool: &AnthropicToolChangeTarget{Type: AnthropicToolChangeTargetTypeToolReference, Name: schemas.Ptr("get_weather")},
			}},
		},
	}
	if _, err, _ := start.ToBifrostResponsesStream(context.Background(), 0, state); err != nil {
		t.Fatalf("content_block_start: %v", err)
	}

	summary := "Summary of the compacted range."
	delta := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockDelta,
		Index: schemas.Ptr(0),
		Delta: &AnthropicStreamDelta{Type: AnthropicStreamDeltaTypeCompaction, Content: &summary},
	}
	responses, err, _ := delta.ToBifrostResponsesStream(context.Background(), 1, state)
	if err != nil {
		t.Fatalf("compaction delta: %v", err)
	}
	if len(responses) != 2 || responses[0].Item == nil || responses[0].Item.Content == nil || len(responses[0].Item.Content.ContentBlocks) == 0 {
		t.Fatalf("expected output_item.added with a compaction block, got %d responses", len(responses))
	}
	block := responses[0].Item.Content.ContentBlocks[0]
	cmp := block.ResponsesOutputMessageContentCompaction
	if cmp == nil || len(cmp.ToolChanges) != 1 {
		t.Fatalf("compaction tool_changes dropped on the streamed item: %+v", cmp)
	}
	if cmp.ToolChanges[0].Type != schemas.ResponsesInputMessageContentBlockTypeToolRemoval ||
		cmp.ToolChanges[0].ToolChange == nil || cmp.ToolChanges[0].ToolChange.Name == nil || *cmp.ToolChanges[0].ToolChange.Name != "get_weather" {
		t.Fatalf("tool_changes[0] = %+v, want a tool_removal referencing get_weather", cmp.ToolChanges[0])
	}

	// Egress: the rebuilt content_block_start carries tool_changes again.
	egressCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	events := ToAnthropicResponsesStreamResponse(egressCtx, &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
		OutputIndex: schemas.Ptr(0),
		Item:        responses[0].Item,
	})
	var startEvent *AnthropicStreamEvent
	for _, ev := range events {
		if ev != nil && ev.Type == AnthropicStreamEventTypeContentBlockStart {
			startEvent = ev
		}
	}
	if startEvent == nil || startEvent.ContentBlock == nil {
		t.Fatalf("no content_block_start emitted: %+v", events)
	}
	if len(startEvent.ContentBlock.ToolChanges) != 1 || startEvent.ContentBlock.ToolChanges[0].Tool == nil ||
		startEvent.ContentBlock.ToolChanges[0].Tool.Name == nil || *startEvent.ContentBlock.ToolChanges[0].Tool.Name != "get_weather" {
		t.Fatalf("egress content_block_start lost tool_changes: %+v", startEvent.ContentBlock)
	}
}

// TestToAnthropicResponsesStream_CompactionToolChangesUseModelCaps: the streamed compaction
// egress must resolve the request's provider and model when it rebuilds inline definitions,
// so a recorded dynamic-filtering web_search keeps its dated type instead of the empty-caps
// default web_search_20250305.
func TestToAnthropicResponsesStream_CompactionToolChangesUseModelCaps(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	item := &schemas.ResponsesMessage{
		ID:   schemas.Ptr("cmp_1"),
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
			Type: schemas.ResponsesOutputMessageContentTypeCompaction,
			ResponsesOutputMessageContentCompaction: &schemas.ResponsesOutputMessageContentCompaction{
				Summary: "summary",
				ToolChanges: []schemas.ResponsesMessageContentBlock{{
					Type: schemas.ResponsesInputMessageContentBlockTypeToolAddition,
					ToolChange: &schemas.ResponsesToolChangeTarget{
						Type:       schemas.ResponsesToolChangeTargetTypeToolDefinition,
						Definition: &schemas.ResponsesTool{Type: schemas.ResponsesToolTypeWebSearch},
					},
				}},
			},
		}}},
	}
	resp := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
		OutputIndex: schemas.Ptr(0),
		Item:        item,
	}
	resp.ExtraFields.RoutingInfo = schemas.RoutingInfo{Provider: schemas.Anthropic, Model: "claude-opus-5-5"}
	events := ToAnthropicResponsesStreamResponse(ctx, resp)
	var start *AnthropicStreamEvent
	for _, ev := range events {
		if ev != nil && ev.Type == AnthropicStreamEventTypeContentBlockStart {
			start = ev
		}
	}
	if start == nil || start.ContentBlock == nil || len(start.ContentBlock.ToolChanges) != 1 {
		t.Fatalf("content_block_start with one tool_changes entry not emitted: %+v", events)
	}
	def := start.ContentBlock.ToolChanges[0].Tool
	if def == nil || def.Definition == nil || def.Definition.Type == nil {
		t.Fatalf("tool_changes[0] definition missing: %+v", start.ContentBlock.ToolChanges[0])
	}
	if got := *def.Definition.Type; got != AnthropicToolTypeWebSearch20260209 {
		t.Errorf("web_search type = %s, want %s (stream egress rebuilt the definition with empty ModelCaps)", got, AnthropicToolTypeWebSearch20260209)
	}
}
