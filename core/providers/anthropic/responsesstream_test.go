package anthropic

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestOpenAIChatToolStreamFallbackToAnthropicClosesToolBlock(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	state := schemas.AcquireChatToResponsesStreamState()
	defer schemas.ReleaseChatToResponsesStreamState(state)

	role := string(schemas.ChatMessageRoleAssistant)
	toolCallID := "call_abc123"
	toolName := "get_weather"
	finishReason := string(schemas.BifrostFinishReasonToolCalls)

	chunks := []*schemas.BifrostChatResponse{
		{
			ID:    "chatcmpl-test",
			Model: "test-model",
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{Role: &role},
				},
			}},
		},
		{
			ID:    "chatcmpl-test",
			Model: "test-model",
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{
						ToolCalls: []schemas.ChatAssistantMessageToolCall{{
							Index: 0,
							ID:    &toolCallID,
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      &toolName,
								Arguments: `{"city":`,
							},
						}},
					},
				},
			}},
		},
		{
			ID:    "chatcmpl-test",
			Model: "test-model",
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{
						ToolCalls: []schemas.ChatAssistantMessageToolCall{{
							Index:    0,
							Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: `"Paris"}`},
						}},
					},
				},
			}},
		},
		{
			ID:    "chatcmpl-test",
			Model: "test-model",
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason: &finishReason,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{},
				},
			}},
		},
	}

	var events []*AnthropicStreamEvent
	for _, chunk := range chunks {
		for _, responseEvent := range chunk.ToBifrostResponsesStreamResponse(state) {
			events = append(events, ToAnthropicResponsesStreamResponse(ctx, responseEvent)...)
		}
	}

	var toolStart, toolStop, messageDelta bool
	var toolInputJSON string
	for _, event := range events {
		switch event.Type {
		case AnthropicStreamEventTypeContentBlockStart:
			if event.ContentBlock != nil && event.ContentBlock.Type == AnthropicContentBlockTypeToolUse {
				toolStart = true
				if event.Index == nil {
					t.Fatal("tool_use content_block_start missing index")
				}
				if *event.Index != 0 {
					t.Fatalf("tool_use content_block_start index = %d, want 0", *event.Index)
				}
				if event.ContentBlock.ID == nil {
					t.Fatal("tool_use id is nil")
				}
				if !strings.HasPrefix(*event.ContentBlock.ID, "toolu_") {
					t.Fatalf("tool_use id = %q, want toolu_ prefix", *event.ContentBlock.ID)
				}
				if *event.ContentBlock.ID == toolCallID {
					t.Fatalf("tool_use id = %q, want normalized Anthropic-safe id", *event.ContentBlock.ID)
				}
				if event.ContentBlock.Name == nil || *event.ContentBlock.Name != toolName {
					t.Fatalf("tool_use name = %v, want %q", event.ContentBlock.Name, toolName)
				}
			}
		case AnthropicStreamEventTypeContentBlockStop:
			if event.Index != nil && *event.Index == 0 {
				toolStop = true
			}
		case AnthropicStreamEventTypeContentBlockDelta:
			if event.Index != nil && *event.Index == 0 && event.Delta != nil && event.Delta.PartialJSON != nil {
				toolInputJSON += *event.Delta.PartialJSON
			}
		case AnthropicStreamEventTypeMessageDelta:
			messageDelta = true
			if event.Delta == nil || event.Delta.StopReason == nil {
				t.Fatal("message_delta missing stop_reason")
			}
			if *event.Delta.StopReason != AnthropicStopReasonToolUse {
				t.Fatalf("message_delta stop_reason = %q, want %q", *event.Delta.StopReason, AnthropicStopReasonToolUse)
			}
		}
	}

	if !toolStart {
		t.Fatal("expected tool_use content_block_start")
	}
	if !toolStop {
		t.Fatal("expected tool_use content_block_stop for index 0")
	}
	if toolInputJSON != `{"city":"Paris"}` {
		t.Fatalf("tool_use input_json_delta = %q, want %q", toolInputJSON, `{"city":"Paris"}`)
	}
	if !messageDelta {
		t.Fatal("expected message_delta")
	}
}

func TestNormalizeAnthropicToolUseID(t *testing.T) {
	t.Parallel()

	rawID := "functions.Read:0"
	normalized := normalizeAnthropicToolUseID(&rawID, nil)
	if normalized == nil {
		t.Fatal("normalized id is nil")
	}
	if !strings.HasPrefix(*normalized, "toolu_") {
		t.Fatalf("normalized id = %q, want toolu_ prefix", *normalized)
	}
	if *normalized == rawID {
		t.Fatalf("normalized id = %q, want different id", *normalized)
	}

	normalizedAgain := normalizeAnthropicToolUseID(&rawID, nil)
	if normalizedAgain == nil || *normalizedAgain != *normalized {
		t.Fatalf("normalization is not stable: first=%v second=%v", normalized, normalizedAgain)
	}

	scope := "turn-1"
	scoped := normalizeAnthropicToolUseID(&rawID, &scope)
	if scoped == nil {
		t.Fatal("scoped normalized id is nil")
	}
	if *scoped == *normalized {
		t.Fatalf("scoped normalized id = %q, want distinct from unscoped %q", *scoped, *normalized)
	}
	scopedAgain := normalizeAnthropicToolUseID(&rawID, &scope)
	if scopedAgain == nil || *scopedAgain != *scoped {
		t.Fatalf("scoped normalization is not stable: first=%v second=%v", scoped, scopedAgain)
	}

	anthropicID := "toolu_native123"
	preserved := normalizeAnthropicToolUseID(&anthropicID, &scope)
	if preserved == nil || *preserved != anthropicID {
		t.Fatalf("native Anthropic id was not preserved: %v", preserved)
	}

	toolResultBlock := convertBifrostFunctionCallOutputToAnthropicToolResultBlock(&schemas.ResponsesMessage{
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: &rawID,
		},
	}, normalized)
	if toolResultBlock == nil || toolResultBlock.ToolUseID == nil || *toolResultBlock.ToolUseID != *normalized {
		t.Fatalf("tool result id = %v, want matching normalized id %q", toolResultBlock, *normalized)
	}
}

func TestConvertBifrostMessagesToAnthropicMessagesScopesRepeatedToolIDs(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	rawID := "functions.Bash:0"
	firstMsgID := "fc_first"
	secondMsgID := "fc_second"
	toolName := "Bash"
	arguments := "{}"
	output := "ok"
	msgTypeFunctionCall := schemas.ResponsesMessageTypeFunctionCall
	msgTypeFunctionCallOutput := schemas.ResponsesMessageTypeFunctionCallOutput

	messages := []schemas.ResponsesMessage{
		{
			ID:   &firstMsgID,
			Type: &msgTypeFunctionCall,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    &rawID,
				Name:      &toolName,
				Arguments: &arguments,
			},
		},
		{
			Type: &msgTypeFunctionCallOutput,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &rawID,
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: &output,
				},
			},
		},
		{
			ID:   &secondMsgID,
			Type: &msgTypeFunctionCall,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    &rawID,
				Name:      &toolName,
				Arguments: &arguments,
			},
		},
		{
			Type: &msgTypeFunctionCallOutput,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &rawID,
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: &output,
				},
			},
		},
	}

	anthropicMessages, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, messages, true)

	var toolUseIDs []string
	var toolResultIDs []string
	for _, msg := range anthropicMessages {
		for _, block := range msg.Content.ContentBlocks {
			switch block.Type {
			case AnthropicContentBlockTypeToolUse:
				if block.ID != nil {
					toolUseIDs = append(toolUseIDs, *block.ID)
				}
			case AnthropicContentBlockTypeToolResult:
				if block.ToolUseID != nil {
					toolResultIDs = append(toolResultIDs, *block.ToolUseID)
				}
			}
		}
	}

	if len(toolUseIDs) != 2 {
		t.Fatalf("tool use ids = %v, want 2 ids", toolUseIDs)
	}
	if len(toolResultIDs) != 2 {
		t.Fatalf("tool result ids = %v, want 2 ids", toolResultIDs)
	}
	if toolUseIDs[0] == toolUseIDs[1] {
		t.Fatalf("repeated call id normalized to duplicate Anthropic id %q", toolUseIDs[0])
	}
	for i, id := range toolUseIDs {
		if !strings.HasPrefix(id, "toolu_") {
			t.Fatalf("tool use id %d = %q, want toolu_ prefix", i, id)
		}
		if id == rawID {
			t.Fatalf("tool use id %d = %q, want normalized id", i, id)
		}
		if toolResultIDs[i] != id {
			t.Fatalf("tool result id %d = %q, want matching tool use id %q", i, toolResultIDs[i], id)
		}
	}
}

func TestOpenAIChatToolStreamFallbackToAnthropicClosesMultipleToolBlocks(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	state := schemas.AcquireChatToResponsesStreamState()
	defer schemas.ReleaseChatToResponsesStreamState(state)

	role := string(schemas.ChatMessageRoleAssistant)
	firstToolCallID := "call_weather"
	firstToolName := "get_weather"
	secondToolCallID := "call_time"
	secondToolName := "get_time"
	finishReason := string(schemas.BifrostFinishReasonToolCalls)

	chunks := []*schemas.BifrostChatResponse{
		{
			ID:    "chatcmpl-test",
			Model: "test-model",
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{Role: &role},
				},
			}},
		},
		{
			ID:    "chatcmpl-test",
			Model: "test-model",
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{
						ToolCalls: []schemas.ChatAssistantMessageToolCall{
							{
								Index: 0,
								ID:    &firstToolCallID,
								Function: schemas.ChatAssistantMessageToolCallFunction{
									Name:      &firstToolName,
									Arguments: `{"city":"Paris"}`,
								},
							},
							{
								Index: 1,
								ID:    &secondToolCallID,
								Function: schemas.ChatAssistantMessageToolCallFunction{
									Name: &secondToolName,
								},
							},
						},
					},
				},
			}},
		},
		{
			ID:    "chatcmpl-test",
			Model: "test-model",
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason: &finishReason,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{},
				},
			}},
		},
	}

	var events []*AnthropicStreamEvent
	for _, chunk := range chunks {
		for _, responseEvent := range chunk.ToBifrostResponsesStreamResponse(state) {
			events = append(events, ToAnthropicResponsesStreamResponse(ctx, responseEvent)...)
		}
	}

	started := map[int]string{}
	stopped := map[int]bool{}
	firstStopPosition := -1
	secondStartPosition := -1
	for i, event := range events {
		switch event.Type {
		case AnthropicStreamEventTypeContentBlockStart:
			if event.ContentBlock != nil && event.ContentBlock.Type == AnthropicContentBlockTypeToolUse {
				if event.Index == nil {
					t.Fatal("tool_use content_block_start missing index")
				}
				if event.ContentBlock.ID == nil {
					t.Fatal("tool_use content_block_start missing id")
				}
				started[*event.Index] = *event.ContentBlock.ID
				if *event.Index == 1 {
					secondStartPosition = i
				}
			}
		case AnthropicStreamEventTypeContentBlockStop:
			if event.Index != nil {
				stopped[*event.Index] = true
				if *event.Index == 0 {
					firstStopPosition = i
				}
			}
		}
	}

	if !strings.HasPrefix(started[0], "toolu_") || started[0] == firstToolCallID {
		t.Fatalf("tool block 0 id = %q, want normalized toolu_ id", started[0])
	}
	if !strings.HasPrefix(started[1], "toolu_") || started[1] == secondToolCallID {
		t.Fatalf("tool block 1 id = %q, want normalized toolu_ id", started[1])
	}
	if started[0] == started[1] {
		t.Fatalf("normalized tool IDs must be distinct, got %q", started[0])
	}
	if !stopped[0] {
		t.Fatal("expected first tool_use content_block_stop for index 0")
	}
	if !stopped[1] {
		t.Fatal("expected second tool_use content_block_stop for index 1")
	}
	if firstStopPosition == -1 || secondStartPosition == -1 {
		t.Fatalf("expected first stop and second start positions, got stop=%d start=%d", firstStopPosition, secondStartPosition)
	}
	if firstStopPosition > secondStartPosition {
		t.Fatalf("first content_block_stop position = %d, want before second content_block_start position %d", firstStopPosition, secondStartPosition)
	}
}

func TestToAnthropicResponsesStreamResponse_DelaysToolStopUntilAfterLateArgumentDeltas(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	toolCallID := "functions.Read:0"
	toolName := "Read"
	outputIndex := 0
	emptyArgs := ""
	firstArgs := `{"file_path": "/`
	secondArgs := `Users/delm/config.json"}`

	stream := []*schemas.BifrostResponsesStreamResponse{
		{
			Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
			OutputIndex: &outputIndex,
			Item: &schemas.ResponsesMessage{
				ID:   &toolCallID,
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    &toolCallID,
					Name:      &toolName,
					Arguments: &emptyArgs,
				},
			},
		},
		{
			Type:        schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			OutputIndex: &outputIndex,
			ItemID:      &toolCallID,
			Arguments:   &emptyArgs,
		},
		{
			Type:        schemas.ResponsesStreamResponseTypeOutputItemDone,
			OutputIndex: &outputIndex,
			Item: &schemas.ResponsesMessage{
				ID:   &toolCallID,
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    &toolCallID,
					Name:      &toolName,
					Arguments: &emptyArgs,
				},
			},
		},
		{
			Type:        schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			OutputIndex: &outputIndex,
			ItemID:      &toolCallID,
			Arguments:   &firstArgs,
		},
		{
			Type:        schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			OutputIndex: &outputIndex,
			ItemID:      &toolCallID,
			Arguments:   &secondArgs,
		},
		{
			Type: schemas.ResponsesStreamResponseTypeCompleted,
			Response: &schemas.BifrostResponsesResponse{
				StopReason: schemas.Ptr(string(schemas.BifrostFinishReasonStop)),
			},
		},
	}

	var events []*AnthropicStreamEvent
	for _, responseEvent := range stream {
		events = append(events, ToAnthropicResponsesStreamResponse(ctx, responseEvent)...)
	}

	var stopPosition = -1
	var lastArgumentDeltaPosition = -1
	var messageDelta *AnthropicStreamEvent
	for i, event := range events {
		switch event.Type {
		case AnthropicStreamEventTypeContentBlockDelta:
			if event.Index != nil && *event.Index == outputIndex &&
				event.Delta != nil && event.Delta.Type == AnthropicStreamDeltaTypeInputJSON {
				lastArgumentDeltaPosition = i
			}
		case AnthropicStreamEventTypeContentBlockStop:
			if event.Index != nil && *event.Index == outputIndex {
				stopPosition = i
			}
		case AnthropicStreamEventTypeMessageDelta:
			messageDelta = event
		}
	}

	if stopPosition == -1 {
		t.Fatal("expected tool_use content_block_stop")
	}
	if lastArgumentDeltaPosition == -1 {
		t.Fatal("expected input_json_delta events")
	}
	if stopPosition < lastArgumentDeltaPosition {
		t.Fatalf("content_block_stop position = %d, want after last argument delta position %d", stopPosition, lastArgumentDeltaPosition)
	}
	if messageDelta == nil || messageDelta.Delta == nil || messageDelta.Delta.StopReason == nil {
		t.Fatal("expected message_delta stop_reason")
	}
	if *messageDelta.Delta.StopReason != AnthropicStopReasonToolUse {
		t.Fatalf("message_delta stop_reason = %q, want %q", *messageDelta.Delta.StopReason, AnthropicStopReasonToolUse)
	}
}

func TestToAnthropicResponsesStreamResponse_ClosesTextBeforeStartingToolFromResponsesEvents(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	textIndex := 0
	toolIndex := 1
	textItemID := "msg_item_0"
	toolCallID := "functions.Bash:0"
	toolName := "Bash"
	text := "I'll inspect the repo."
	toolArgs := `{"command":"ls -la","description":"List files"}`

	stream := []*schemas.BifrostResponsesStreamResponse{
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputItemAdded,
			OutputIndex:  &textIndex,
			ContentIndex: &textIndex,
			Item: &schemas.ResponsesMessage{
				ID:   &textItemID,
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			},
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputTextDelta,
			OutputIndex:  &textIndex,
			ContentIndex: &textIndex,
			ItemID:       &textItemID,
			Delta:        &text,
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputTextDone,
			OutputIndex:  &textIndex,
			ContentIndex: &textIndex,
			ItemID:       &textItemID,
			Text:         &text,
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeContentPartDone,
			OutputIndex:  &textIndex,
			ContentIndex: &textIndex,
			ItemID:       &textItemID,
			Part: &schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesOutputMessageContentTypeText,
				Text: &text,
			},
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputItemAdded,
			OutputIndex:  &toolIndex,
			ContentIndex: &toolIndex,
			Item: &schemas.ResponsesMessage{
				ID:   &toolCallID,
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    &toolCallID,
					Name:      &toolName,
					Arguments: schemas.Ptr(""),
				},
			},
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			OutputIndex:  &toolIndex,
			ContentIndex: &toolIndex,
			ItemID:       &toolCallID,
			Arguments:    &toolArgs,
		},
		{
			Type: schemas.ResponsesStreamResponseTypeCompleted,
			Response: &schemas.BifrostResponsesResponse{
				StopReason: schemas.Ptr(string(schemas.BifrostFinishReasonToolCalls)),
			},
		},
	}

	var events []*AnthropicStreamEvent
	for _, responseEvent := range stream {
		events = append(events, ToAnthropicResponsesStreamResponse(ctx, responseEvent)...)
	}

	assertAnthropicStreamBlocksDoNotOverlap(t, events)

	textStopPosition := eventPosition(events, AnthropicStreamEventTypeContentBlockStop, textIndex)
	toolStartPosition := eventPosition(events, AnthropicStreamEventTypeContentBlockStart, toolIndex)
	if textStopPosition == -1 {
		t.Fatal("expected text content_block_stop before tool start")
	}
	if toolStartPosition == -1 {
		t.Fatal("expected tool content_block_start")
	}
	if textStopPosition > toolStartPosition {
		t.Fatalf("text content_block_stop position = %d, want before tool content_block_start position %d", textStopPosition, toolStartPosition)
	}
	if countEvents(events, AnthropicStreamEventTypeContentBlockStop, textIndex) != 1 {
		t.Fatalf("text content block should stop exactly once, got %d stops", countEvents(events, AnthropicStreamEventTypeContentBlockStop, textIndex))
	}
}

func TestToAnthropicResponsesStreamResponse_NormalizesRepeatedProviderToolIDsPerMessage(t *testing.T) {
	t.Parallel()

	firstToolID := streamedToolUseIDForMessage(t, "resp_first", "functions.Bash:0", 0)
	secondToolID := streamedToolUseIDForMessage(t, "resp_second", "functions.Bash:0", 0)

	if firstToolID == "functions.Bash:0" || secondToolID == "functions.Bash:0" {
		t.Fatalf("tool IDs should be Anthropic-safe, got first=%q second=%q", firstToolID, secondToolID)
	}
	if !strings.HasPrefix(firstToolID, "toolu_") || !strings.HasPrefix(secondToolID, "toolu_") {
		t.Fatalf("tool IDs should use toolu_ prefix, got first=%q second=%q", firstToolID, secondToolID)
	}
	if firstToolID == secondToolID {
		t.Fatalf("repeated provider tool IDs should normalize differently per message, got %q", firstToolID)
	}
}

func streamedToolUseIDForMessage(t *testing.T, messageID string, rawToolID string, outputIndex int) string {
	t.Helper()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	toolName := "Bash"
	stream := []*schemas.BifrostResponsesStreamResponse{
		{
			Type: schemas.ResponsesStreamResponseTypeCreated,
			Response: &schemas.BifrostResponsesResponse{
				ID: &messageID,
			},
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputItemAdded,
			OutputIndex:  &outputIndex,
			ContentIndex: &outputIndex,
			Item: &schemas.ResponsesMessage{
				ID:   &rawToolID,
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    &rawToolID,
					Name:      &toolName,
					Arguments: schemas.Ptr(""),
				},
			},
		},
	}

	var events []*AnthropicStreamEvent
	for _, responseEvent := range stream {
		events = append(events, ToAnthropicResponsesStreamResponse(ctx, responseEvent)...)
	}

	for _, event := range events {
		if event.Type == AnthropicStreamEventTypeContentBlockStart &&
			event.ContentBlock != nil &&
			event.ContentBlock.Type == AnthropicContentBlockTypeToolUse &&
			event.ContentBlock.ID != nil {
			return *event.ContentBlock.ID
		}
	}

	t.Fatal("expected tool_use content_block_start")
	return ""
}

func assertAnthropicStreamBlocksDoNotOverlap(t *testing.T, events []*AnthropicStreamEvent) {
	t.Helper()

	var openIndex *int
	for position, event := range events {
		switch event.Type {
		case AnthropicStreamEventTypeContentBlockStart:
			if event.Index == nil {
				t.Fatalf("content_block_start at position %d missing index", position)
			}
			if openIndex != nil {
				t.Fatalf("content_block_start index %d at position %d before content_block_stop for index %d", *event.Index, position, *openIndex)
			}
			index := *event.Index
			openIndex = &index
		case AnthropicStreamEventTypeContentBlockStop:
			if event.Index == nil {
				t.Fatalf("content_block_stop at position %d missing index", position)
			}
			if openIndex == nil {
				t.Fatalf("content_block_stop index %d at position %d without open content block", *event.Index, position)
			}
			if *event.Index != *openIndex {
				t.Fatalf("content_block_stop index %d at position %d while index %d is open", *event.Index, position, *openIndex)
			}
			openIndex = nil
		}
	}
	if openIndex != nil {
		t.Fatalf("content block index %d was not stopped", *openIndex)
	}
}

func eventPosition(events []*AnthropicStreamEvent, eventType AnthropicStreamEventType, index int) int {
	for i, event := range events {
		if event.Type == eventType && event.Index != nil && *event.Index == index {
			return i
		}
	}
	return -1
}

func countEvents(events []*AnthropicStreamEvent, eventType AnthropicStreamEventType, index int) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType && event.Index != nil && *event.Index == index {
			count++
		}
	}
	return count
}
