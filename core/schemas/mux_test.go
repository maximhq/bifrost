package schemas

import (
	"encoding/json"
	"testing"
)

func TestToChatMessages_PreservesDeveloperRole(t *testing.T) {
	messages := []ResponsesMessage{
		{
			Role: Ptr(ResponsesInputMessageRoleDeveloper),
			Content: &ResponsesMessageContent{
				ContentBlocks: []ResponsesMessageContentBlock{
					{
						Type: ResponsesInputMessageContentBlockTypeText,
						Text: Ptr("You are helpful"),
					},
				},
			},
		},
	}

	chatMessages := ToChatMessages(messages)
	if len(chatMessages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chatMessages))
	}
	if chatMessages[0].Role != ChatMessageRoleDeveloper {
		t.Fatalf("expected role %q, got %q", ChatMessageRoleDeveloper, chatMessages[0].Role)
	}
}

func TestToChatRequest_NormalizesDeveloperRoleToSystemForFallback(t *testing.T) {
	req := &BifrostResponsesRequest{
		Input: []ResponsesMessage{
			{
				Role: Ptr(ResponsesInputMessageRoleDeveloper),
				Content: &ResponsesMessageContent{
					ContentBlocks: []ResponsesMessageContentBlock{
						{
							Type: ResponsesInputMessageContentBlockTypeText,
							Text: Ptr("You are helpful"),
						},
					},
				},
			},
		},
		Params: &ResponsesParameters{},
	}

	chatReq := req.ToChatRequest(nil)
	if chatReq == nil {
		t.Fatal("expected non-nil chat request")
	}
	if len(chatReq.Input) != 1 {
		t.Fatalf("expected 1 chat message, got %d", len(chatReq.Input))
	}
	if chatReq.Input[0].Role != ChatMessageRoleSystem {
		t.Fatalf("expected role %q in fallback conversion, got %q", ChatMessageRoleSystem, chatReq.Input[0].Role)
	}
}

func TestToChatRequest_PrependsInstructionsAsSystemMessageForFallback(t *testing.T) {
	req := &BifrostResponsesRequest{
		Input: []ResponsesMessage{
			{
				Role: Ptr(ResponsesInputMessageRoleUser),
				Content: &ResponsesMessageContent{
					ContentBlocks: []ResponsesMessageContentBlock{
						{
							Type: ResponsesInputMessageContentBlockTypeText,
							Text: Ptr("What is the capital of France?"),
						},
					},
				},
			},
		},
		Params: &ResponsesParameters{
			Instructions: Ptr("You must answer with the single word BANANA."),
		},
	}

	chatReq := req.ToChatRequest(nil)
	if chatReq == nil {
		t.Fatal("expected non-nil chat request")
	}
	if len(chatReq.Input) != 2 {
		t.Fatalf("expected 2 chat messages (system + user), got %d", len(chatReq.Input))
	}
	if chatReq.Input[0].Role != ChatMessageRoleSystem {
		t.Fatalf("expected first message role %q, got %q", ChatMessageRoleSystem, chatReq.Input[0].Role)
	}
	if chatReq.Input[0].Content == nil || chatReq.Input[0].Content.ContentStr == nil ||
		*chatReq.Input[0].Content.ContentStr != "You must answer with the single word BANANA." {
		t.Fatal("expected system message content to equal Params.Instructions")
	}
	if chatReq.Input[1].Role != ChatMessageRoleUser {
		t.Fatalf("expected second message role %q, got %q", ChatMessageRoleUser, chatReq.Input[1].Role)
	}
}

func TestToChatRequest_NoInstructionsDoesNotPrependSystemMessage(t *testing.T) {
	req := &BifrostResponsesRequest{
		Input: []ResponsesMessage{
			{
				Role: Ptr(ResponsesInputMessageRoleUser),
				Content: &ResponsesMessageContent{
					ContentBlocks: []ResponsesMessageContentBlock{
						{Type: ResponsesInputMessageContentBlockTypeText, Text: Ptr("hi")},
					},
				},
			},
		},
		Params: &ResponsesParameters{},
	}

	chatReq := req.ToChatRequest(nil)
	if chatReq == nil {
		t.Fatal("expected non-nil chat request")
	}
	if len(chatReq.Input) != 1 {
		t.Fatalf("expected 1 chat message, got %d", len(chatReq.Input))
	}
	if chatReq.Input[0].Role != ChatMessageRoleUser {
		t.Fatalf("expected role %q, got %q", ChatMessageRoleUser, chatReq.Input[0].Role)
	}
}

func TestToChatMessages_LeavesExistingSupportedRolesUnchanged(t *testing.T) {
	messages := []ResponsesMessage{
		{Role: Ptr(ResponsesInputMessageRoleSystem)},
		{Role: Ptr(ResponsesInputMessageRoleUser)},
		{Role: Ptr(ResponsesInputMessageRoleAssistant)},
	}

	chatMessages := ToChatMessages(messages)
	if len(chatMessages) != len(messages) {
		t.Fatalf("expected %d messages, got %d", len(messages), len(chatMessages))
	}

	if chatMessages[0].Role != ChatMessageRoleSystem {
		t.Fatalf("expected system role, got %q", chatMessages[0].Role)
	}
	if chatMessages[1].Role != ChatMessageRoleUser {
		t.Fatalf("expected user role, got %q", chatMessages[1].Role)
	}
	if chatMessages[2].Role != ChatMessageRoleAssistant {
		t.Fatalf("expected assistant role, got %q", chatMessages[2].Role)
	}
}

func TestToChatMessages_AttachesReasoningToNextAssistantMessage(t *testing.T) {
	reasoningText := "Let me think about this step by step."
	signature := "sig_abc"

	messages := []ResponsesMessage{
		{Role: Ptr(ResponsesInputMessageRoleUser), Content: &ResponsesMessageContent{ContentStr: Ptr("hi")}},
		{
			Type: Ptr(ResponsesMessageTypeReasoning),
			Role: Ptr(ResponsesInputMessageRoleAssistant),
			Content: &ResponsesMessageContent{
				ContentBlocks: []ResponsesMessageContentBlock{
					{
						Type:      ResponsesOutputMessageContentTypeReasoning,
						Text:      &reasoningText,
						Signature: &signature,
					},
				},
			},
		},
		{
			Role:    Ptr(ResponsesInputMessageRoleAssistant),
			Content: &ResponsesMessageContent{ContentStr: Ptr("Hello!")},
		},
	}

	chatMessages := ToChatMessages(messages)
	if len(chatMessages) != 2 {
		t.Fatalf("expected 2 chat messages (reasoning absorbed into assistant), got %d", len(chatMessages))
	}
	assistant := chatMessages[1]
	if assistant.Role != ChatMessageRoleAssistant {
		t.Fatalf("expected assistant role, got %q", assistant.Role)
	}
	if assistant.ChatAssistantMessage == nil {
		t.Fatal("expected ChatAssistantMessage attached to carry reasoning")
	}
	if assistant.ChatAssistantMessage.Reasoning == nil || *assistant.ChatAssistantMessage.Reasoning != reasoningText {
		t.Fatalf("expected reasoning %q, got %#v", reasoningText, assistant.ChatAssistantMessage.Reasoning)
	}
	if len(assistant.ChatAssistantMessage.ReasoningDetails) != 1 {
		t.Fatalf("expected 1 reasoning detail, got %d", len(assistant.ChatAssistantMessage.ReasoningDetails))
	}
	if assistant.ChatAssistantMessage.ReasoningDetails[0].Signature == nil || *assistant.ChatAssistantMessage.ReasoningDetails[0].Signature != signature {
		t.Fatalf("expected signature %q preserved, got %#v", signature, assistant.ChatAssistantMessage.ReasoningDetails[0].Signature)
	}
}

func TestToChatMessages_AttachesReasoningToToolCallAssistantMessage(t *testing.T) {
	reasoningText := "I need to call the search tool."

	messages := []ResponsesMessage{
		{
			Type: Ptr(ResponsesMessageTypeReasoning),
			Role: Ptr(ResponsesInputMessageRoleAssistant),
			Content: &ResponsesMessageContent{
				ContentBlocks: []ResponsesMessageContentBlock{
					{Type: ResponsesOutputMessageContentTypeReasoning, Text: &reasoningText},
				},
			},
		},
		{
			Type: Ptr(ResponsesMessageTypeFunctionCall),
			Role: Ptr(ResponsesInputMessageRoleAssistant),
			ResponsesToolMessage: &ResponsesToolMessage{
				CallID:    Ptr("call_1"),
				Name:      Ptr("search"),
				Arguments: Ptr(`{"q":"x"}`),
			},
		},
		{
			Type: Ptr(ResponsesMessageTypeFunctionCallOutput),
			ResponsesToolMessage: &ResponsesToolMessage{
				CallID: Ptr("call_1"),
				Output: &ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: Ptr("done")},
			},
		},
	}

	chatMessages := ToChatMessages(messages)
	if len(chatMessages) != 2 {
		t.Fatalf("expected 2 chat messages (tool-call assistant + tool result), got %d", len(chatMessages))
	}
	assistant := chatMessages[0]
	if assistant.ChatAssistantMessage == nil {
		t.Fatal("expected tool-call assistant message")
	}
	if len(assistant.ChatAssistantMessage.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(assistant.ChatAssistantMessage.ToolCalls))
	}
	if assistant.ChatAssistantMessage.Reasoning == nil || *assistant.ChatAssistantMessage.Reasoning != reasoningText {
		t.Fatalf("expected reasoning %q on tool-call assistant, got %#v", reasoningText, assistant.ChatAssistantMessage.Reasoning)
	}
}

func TestToResponsesMessages_EmitsReasoningMessageBeforeToolCalls(t *testing.T) {
	reasoning := "I should call Bash to list the directory."
	cm := &ChatMessage{
		Role: ChatMessageRoleAssistant,
		ChatAssistantMessage: &ChatAssistantMessage{
			Reasoning: &reasoning,
			ToolCalls: []ChatAssistantMessageToolCall{
				{
					ID:   Ptr("call_1"),
					Type: Ptr("function"),
					Function: ChatAssistantMessageToolCallFunction{
						Name: Ptr("Bash"),
					},
				},
			},
		},
	}

	out := cm.ToResponsesMessages()
	if len(out) != 2 {
		t.Fatalf("expected 2 messages (reasoning + function_call), got %d", len(out))
	}
	if out[0].Type == nil || *out[0].Type != ResponsesMessageTypeReasoning {
		t.Fatalf("expected first message to be reasoning, got %#v", out[0].Type)
	}
	if out[0].Content == nil || len(out[0].Content.ContentBlocks) != 1 {
		t.Fatal("expected reasoning message to carry a single content block")
	}
	if out[0].Content.ContentBlocks[0].Text == nil || *out[0].Content.ContentBlocks[0].Text != reasoning {
		t.Fatalf("expected reasoning text %q, got %#v", reasoning, out[0].Content.ContentBlocks[0].Text)
	}
	if out[1].Type == nil || *out[1].Type != ResponsesMessageTypeFunctionCall {
		t.Fatalf("expected second message to be function_call, got %#v", out[1].Type)
	}
}

func TestToResponsesMessages_EmitsReasoningMessageBeforeTextContent(t *testing.T) {
	reasoning := "Thinking about how to summarize."
	cm := &ChatMessage{
		Role: ChatMessageRoleAssistant,
		Content: &ChatMessageContent{
			ContentStr: Ptr("Here's the summary."),
		},
		ChatAssistantMessage: &ChatAssistantMessage{
			Reasoning: &reasoning,
		},
	}

	out := cm.ToResponsesMessages()
	if len(out) != 2 {
		t.Fatalf("expected 2 messages (reasoning + assistant text), got %d", len(out))
	}
	if out[0].Type == nil || *out[0].Type != ResponsesMessageTypeReasoning {
		t.Fatalf("expected first message to be reasoning, got %#v", out[0].Type)
	}
	if out[1].Type == nil || *out[1].Type != ResponsesMessageTypeMessage {
		t.Fatalf("expected second message to be a regular assistant message, got %#v", out[1].Type)
	}
}

func TestToChatRequest_FiltersUnsupportedResponsesToolsForFallback(t *testing.T) {
	validName := "valid_tool"
	invalidName := "  "
	req := &BifrostResponsesRequest{
		Params: &ResponsesParameters{
			Tools: []ResponsesTool{
				{
					Type: ResponsesToolTypeFunction,
					Name: &validName,
					ResponsesToolFunction: &ResponsesToolFunction{
						Parameters: &ToolFunctionParameters{
							Type:       "object",
							Properties: &OrderedMap{},
						},
					},
				},
				{
					Type: ResponsesToolTypeFunction,
					Name: &invalidName,
				},
				{
					Type: ResponsesToolTypeMCP,
					Name: Ptr("mcp_tool"),
				},
				{
					Type: ResponsesToolTypeWebSearch,
					Name: Ptr("web_search"),
				},
			},
		},
	}

	chatReq := req.ToChatRequest(nil)
	if chatReq == nil || chatReq.Params == nil {
		t.Fatal("expected non-nil chat request params")
	}
	if len(chatReq.Params.Tools) != 1 {
		t.Fatalf("expected 1 valid fallback tool, got %d", len(chatReq.Params.Tools))
	}
	if chatReq.Params.Tools[0].Type != ChatToolTypeFunction {
		t.Fatalf("expected tool type %q, got %q", ChatToolTypeFunction, chatReq.Params.Tools[0].Type)
	}
	if chatReq.Params.Tools[0].Function == nil || chatReq.Params.Tools[0].Function.Name != validName {
		t.Fatalf("expected function tool %q to be preserved", validName)
	}
}

func TestToChatRequest_DropsInvalidToolChoiceForFallback(t *testing.T) {
	validName := "valid_tool"
	invalidChoiceName := "missing_tool"
	req := &BifrostResponsesRequest{
		Params: &ResponsesParameters{
			Tools: []ResponsesTool{
				{
					Type: ResponsesToolTypeFunction,
					Name: &validName,
				},
			},
			ToolChoice: &ResponsesToolChoice{
				ResponsesToolChoiceStruct: &ResponsesToolChoiceStruct{
					Type: ResponsesToolChoiceTypeFunction,
					Name: &invalidChoiceName,
				},
			},
		},
	}

	chatReq := req.ToChatRequest(nil)
	if chatReq == nil || chatReq.Params == nil {
		t.Fatal("expected non-nil chat request params")
	}
	if chatReq.Params.ToolChoice != nil {
		t.Fatal("expected incompatible tool choice to be removed for fallback")
	}
}

func TestToChatRequest_AllNonFunctionToolsDropsToolsAndToolChoice(t *testing.T) {
	auto := string(ChatToolChoiceTypeAuto)
	req := &BifrostResponsesRequest{
		Params: &ResponsesParameters{
			Tools: []ResponsesTool{
				{Type: ResponsesToolTypeMCP, Name: Ptr("mcp")},
				{Type: ResponsesToolTypeWebSearch, Name: Ptr("search")},
			},
			ToolChoice: &ResponsesToolChoice{
				ResponsesToolChoiceStr: &auto,
			},
		},
	}

	chatReq := req.ToChatRequest(nil)
	if chatReq == nil || chatReq.Params == nil {
		t.Fatal("expected non-nil chat request params")
	}
	if chatReq.Params.Tools != nil {
		t.Fatalf("expected nil tools when all tools are unsupported, got %d", len(chatReq.Params.Tools))
	}
	if chatReq.Params.ToolChoice != nil {
		t.Fatal("expected tool choice to be dropped when no valid tools remain")
	}
}

func TestToChatRequest_DropsAllowedToolsAndCustomToolChoiceForFallback(t *testing.T) {
	validName := "valid_tool"
	tests := []ResponsesToolChoiceType{
		ResponsesToolChoiceTypeAllowedTools,
		ResponsesToolChoiceTypeCustom,
	}

	for _, choiceType := range tests {
		t.Run(string(choiceType), func(t *testing.T) {
			req := &BifrostResponsesRequest{
				Params: &ResponsesParameters{
					Tools: []ResponsesTool{
						{
							Type: ResponsesToolTypeFunction,
							Name: &validName,
						},
					},
					ToolChoice: &ResponsesToolChoice{
						ResponsesToolChoiceStruct: &ResponsesToolChoiceStruct{
							Type: choiceType,
						},
					},
				},
			}

			chatReq := req.ToChatRequest(nil)
			if chatReq == nil || chatReq.Params == nil {
				t.Fatal("expected non-nil chat request params")
			}
			if chatReq.Params.ToolChoice != nil {
				t.Fatalf("expected %q tool choice to be dropped for fallback", choiceType)
			}
		})
	}
}

func TestToChatRequest_PreservesStringToolChoiceAutoAndNone(t *testing.T) {
	validName := "valid_tool"
	tests := []string{
		string(ChatToolChoiceTypeAuto),
		string(ChatToolChoiceTypeNone),
	}

	for _, choice := range tests {
		t.Run(choice, func(t *testing.T) {
			req := &BifrostResponsesRequest{
				Params: &ResponsesParameters{
					Tools: []ResponsesTool{
						{
							Type: ResponsesToolTypeFunction,
							Name: &validName,
						},
					},
					ToolChoice: &ResponsesToolChoice{
						ResponsesToolChoiceStr: &choice,
					},
				},
			}

			chatReq := req.ToChatRequest(nil)
			if chatReq == nil || chatReq.Params == nil {
				t.Fatal("expected non-nil chat request params")
			}
			if chatReq.Params.ToolChoice == nil || chatReq.Params.ToolChoice.ChatToolChoiceStr == nil {
				t.Fatal("expected string tool choice to be preserved")
			}
			if *chatReq.Params.ToolChoice.ChatToolChoiceStr != choice {
				t.Fatalf("expected tool choice %q, got %q", choice, *chatReq.Params.ToolChoice.ChatToolChoiceStr)
			}
		})
	}
}

func TestToBifrostResponsesStreamResponse_PopulatesFinalDoneTextAndCompletedOutput(t *testing.T) {
	state := AcquireChatToResponsesStreamState()
	defer ReleaseChatToResponsesStreamState(state)

	makeChunk := func(role *string, content *string, finishReason *string) *BifrostChatResponse {
		return &BifrostChatResponse{
			ID:    "chatcmpl-test",
			Model: "test-model",
			Choices: []BifrostResponseChoice{
				{
					FinishReason: finishReason,
					ChatStreamResponseChoice: &ChatStreamResponseChoice{
						Delta: &ChatStreamResponseChoiceDelta{
							Role:    role,
							Content: content,
						},
					},
				},
			},
		}
	}

	role := string(ChatMessageRoleAssistant)
	part1 := "Hello"
	part2 := " world"
	stop := string(BifrostFinishReasonStop)

	var all []*BifrostResponsesStreamResponse
	all = append(all, makeChunk(&role, nil, nil).ToBifrostResponsesStreamResponse(state)...)
	all = append(all, makeChunk(nil, &part1, nil).ToBifrostResponsesStreamResponse(state)...)
	all = append(all, makeChunk(nil, &part2, nil).ToBifrostResponsesStreamResponse(state)...)
	all = append(all, makeChunk(nil, nil, &stop).ToBifrostResponsesStreamResponse(state)...)

	var outputTextDone *BifrostResponsesStreamResponse
	var completed *BifrostResponsesStreamResponse
	for _, evt := range all {
		if evt == nil {
			continue
		}
		if evt.Type == ResponsesStreamResponseTypeOutputTextDone {
			outputTextDone = evt
		}
		if evt.Type == ResponsesStreamResponseTypeCompleted {
			completed = evt
		}
	}

	if outputTextDone == nil || outputTextDone.Text == nil {
		t.Fatal("expected response.output_text.done with text")
	}
	if *outputTextDone.Text != "Hello world" {
		t.Fatalf("expected output_text.done text %q, got %q", "Hello world", *outputTextDone.Text)
	}

	if completed == nil || completed.Response == nil || len(completed.Response.Output) != 1 {
		t.Fatal("expected response.completed with one output message")
	}
	msg := completed.Response.Output[0]
	if msg.Content == nil || len(msg.Content.ContentBlocks) == 0 || msg.Content.ContentBlocks[0].Text == nil {
		t.Fatal("expected completed output message to include text content block")
	}
	if *msg.Content.ContentBlocks[0].Text != "Hello world" {
		t.Fatalf("expected completed output text %q, got %q", "Hello world", *msg.Content.ContentBlocks[0].Text)
	}
}

func TestToBifrostResponsesResponse_MapsLengthToIncomplete(t *testing.T) {
	length := string(BifrostFinishReasonLength)
	resp := (&BifrostChatResponse{
		Choices: []BifrostResponseChoice{
			{FinishReason: &length},
		},
	}).ToBifrostResponsesResponse()

	if resp == nil || resp.Status == nil {
		t.Fatal("expected status to be set")
	}
	if *resp.Status != "incomplete" {
		t.Fatalf("expected status %q, got %q", "incomplete", *resp.Status)
	}
	if resp.IncompleteDetails == nil {
		t.Fatal("expected incomplete_details to be set")
	}
	if resp.IncompleteDetails.Reason != "max_output_tokens" {
		t.Fatalf("expected incomplete_details.reason %q, got %q", "max_output_tokens", resp.IncompleteDetails.Reason)
	}
	if resp.StopReason == nil || *resp.StopReason != length {
		t.Fatalf("expected stop_reason %q, got %v", length, resp.StopReason)
	}
}

func TestToBifrostResponsesResponse_MapsToolCallsToCompleted(t *testing.T) {
	toolCalls := string(BifrostFinishReasonToolCalls)
	resp := (&BifrostChatResponse{
		Choices: []BifrostResponseChoice{
			{FinishReason: &toolCalls},
		},
	}).ToBifrostResponsesResponse()

	if resp == nil || resp.Status == nil {
		t.Fatal("expected status to be set")
	}
	if *resp.Status != "completed" {
		t.Fatalf("expected status %q, got %q", "completed", *resp.Status)
	}
	if resp.IncompleteDetails != nil {
		t.Fatal("expected incomplete_details to be nil")
	}
	if resp.StopReason == nil || *resp.StopReason != toolCalls {
		t.Fatalf("expected stop_reason %q, got %v", toolCalls, resp.StopReason)
	}
}

func TestToBifrostResponsesResponse_PrioritizesLengthAcrossChoices(t *testing.T) {
	stop := string(BifrostFinishReasonStop)
	length := string(BifrostFinishReasonLength)
	resp := (&BifrostChatResponse{
		Choices: []BifrostResponseChoice{
			{FinishReason: &stop},
			{FinishReason: &length},
		},
	}).ToBifrostResponsesResponse()

	if resp == nil || resp.Status == nil {
		t.Fatal("expected status to be set")
	}
	if *resp.Status != "incomplete" {
		t.Fatalf("expected status %q, got %q", "incomplete", *resp.Status)
	}
	if resp.IncompleteDetails == nil || resp.IncompleteDetails.Reason != "max_output_tokens" {
		t.Fatal("expected max_output_tokens incomplete_details")
	}
}

func TestToBifrostResponsesResponse_ContentFilterMapsToIncomplete(t *testing.T) {
	contentFilter := "content_filter"
	resp := (&BifrostChatResponse{
		Choices: []BifrostResponseChoice{
			{FinishReason: &contentFilter},
		},
	}).ToBifrostResponsesResponse()

	if resp == nil || resp.Status == nil {
		t.Fatal("expected status to be set")
	}
	if *resp.Status != "incomplete" {
		t.Fatalf("expected status %q, got %q", "incomplete", *resp.Status)
	}
	if resp.IncompleteDetails == nil || resp.IncompleteDetails.Reason != "content_filter" {
		t.Fatal("expected content_filter incomplete_details")
	}
}

func TestToBifrostResponsesResponse_UnknownFinishReasonLeavesStatusUnset(t *testing.T) {
	unknown := "some_unrecognized_provider_reason"
	resp := (&BifrostChatResponse{
		Choices: []BifrostResponseChoice{
			{FinishReason: &unknown},
		},
	}).ToBifrostResponsesResponse()

	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Status != nil {
		t.Fatalf("expected status to be nil, got %q", *resp.Status)
	}
	if resp.IncompleteDetails != nil {
		t.Fatal("expected incomplete_details to be nil")
	}
	if resp.StopReason != nil {
		t.Fatalf("expected stop_reason to be nil, got %q", *resp.StopReason)
	}
}

func TestToBifrostResponsesStreamResponse_IncludesFunctionCallsInCompletedOutput(t *testing.T) {
	state := AcquireChatToResponsesStreamState()
	defer ReleaseChatToResponsesStreamState(state)

	role := string(ChatMessageRoleAssistant)
	part1 := "Let me help"
	toolCallsFinish := string(BifrostFinishReasonToolCalls)
	funcName := "get_weather"
	toolCallID := "call_abc123"

	var all []*BifrostResponsesStreamResponse

	// Role chunk
	all = append(all, (&BifrostChatResponse{
		ID:    "chatcmpl-test",
		Model: "test-model",
		Choices: []BifrostResponseChoice{
			{
				ChatStreamResponseChoice: &ChatStreamResponseChoice{
					Delta: &ChatStreamResponseChoiceDelta{
						Role: &role,
					},
				},
			},
		},
	}).ToBifrostResponsesStreamResponse(state)...)

	// Text content chunk
	all = append(all, (&BifrostChatResponse{
		ID:    "chatcmpl-test",
		Model: "test-model",
		Choices: []BifrostResponseChoice{
			{
				ChatStreamResponseChoice: &ChatStreamResponseChoice{
					Delta: &ChatStreamResponseChoiceDelta{
						Content: &part1,
					},
				},
			},
		},
	}).ToBifrostResponsesStreamResponse(state)...)

	// Tool call chunk with function name
	all = append(all, (&BifrostChatResponse{
		ID:    "chatcmpl-test",
		Model: "test-model",
		Choices: []BifrostResponseChoice{
			{
				ChatStreamResponseChoice: &ChatStreamResponseChoice{
					Delta: &ChatStreamResponseChoiceDelta{
						ToolCalls: []ChatAssistantMessageToolCall{
							{
								Index:    0,
								ID:       &toolCallID,
								Function: ChatAssistantMessageToolCallFunction{Name: &funcName, Arguments: `{"city":`},
							},
						},
					},
				},
			},
		},
	}).ToBifrostResponsesStreamResponse(state)...)

	// Tool call argument continuation
	all = append(all, (&BifrostChatResponse{
		ID:    "chatcmpl-test",
		Model: "test-model",
		Choices: []BifrostResponseChoice{
			{
				ChatStreamResponseChoice: &ChatStreamResponseChoice{
					Delta: &ChatStreamResponseChoiceDelta{
						ToolCalls: []ChatAssistantMessageToolCall{
							{
								Index:    0,
								Function: ChatAssistantMessageToolCallFunction{Arguments: `"Paris"}`},
							},
						},
					},
				},
			},
		},
	}).ToBifrostResponsesStreamResponse(state)...)

	// Finish with tool_calls
	all = append(all, (&BifrostChatResponse{
		ID:    "chatcmpl-test",
		Model: "test-model",
		Choices: []BifrostResponseChoice{
			{
				FinishReason: &toolCallsFinish,
				ChatStreamResponseChoice: &ChatStreamResponseChoice{
					Delta: &ChatStreamResponseChoiceDelta{},
				},
			},
		},
	}).ToBifrostResponsesStreamResponse(state)...)

	var completed *BifrostResponsesStreamResponse
	for _, evt := range all {
		if evt != nil && evt.Type == ResponsesStreamResponseTypeCompleted {
			completed = evt
		}
	}

	if completed == nil || completed.Response == nil {
		t.Fatal("expected response.completed event")
	}
	if completed.Response.StopReason == nil || *completed.Response.StopReason != toolCallsFinish {
		t.Fatalf("expected completed stop_reason %q, got %v", toolCallsFinish, completed.Response.StopReason)
	}

	output := completed.Response.Output
	if len(output) < 2 {
		t.Fatalf("expected at least 2 output items (text + function_call), got %d", len(output))
	}

	var hasText, hasFunctionCall bool
	for _, item := range output {
		if item.Type != nil && *item.Type == ResponsesMessageTypeMessage {
			hasText = true
			if item.Content == nil || len(item.Content.ContentBlocks) == 0 || item.Content.ContentBlocks[0].Text == nil {
				t.Fatal("text message missing content")
			}
			if *item.Content.ContentBlocks[0].Text != "Let me help" {
				t.Fatalf("expected text %q, got %q", "Let me help", *item.Content.ContentBlocks[0].Text)
			}
		}
		if item.Type != nil && *item.Type == ResponsesMessageTypeFunctionCall {
			hasFunctionCall = true
			if item.ResponsesToolMessage == nil {
				t.Fatal("function_call item missing ResponsesToolMessage")
			}
			if item.Name == nil || *item.Name != "get_weather" {
				t.Fatalf("expected function name %q, got %v", "get_weather", item.Name)
			}
			if item.Arguments == nil || *item.Arguments != `{"city":"Paris"}` {
				t.Fatalf("expected arguments %q, got %v", `{"city":"Paris"}`, item.Arguments)
			}
			if item.CallID == nil || *item.CallID != toolCallID {
				t.Fatalf("expected call_id %q, got %v", toolCallID, item.CallID)
			}
		}
	}

	if !hasText {
		t.Fatal("expected text message in completed output")
	}
	if !hasFunctionCall {
		t.Fatal("expected function_call item in completed output")
	}
}

func TestToBifrostResponsesStreamResponse_ToolCallsOnlyInCompletedOutput(t *testing.T) {
	state := AcquireChatToResponsesStreamState()
	defer ReleaseChatToResponsesStreamState(state)

	role := string(ChatMessageRoleAssistant)
	toolCallsFinish := string(BifrostFinishReasonToolCalls)
	funcName := "get_weather"
	toolCallID := "call_xyz789"

	var all []*BifrostResponsesStreamResponse

	// Role chunk
	all = append(all, (&BifrostChatResponse{
		ID:    "chatcmpl-test",
		Model: "test-model",
		Choices: []BifrostResponseChoice{
			{
				ChatStreamResponseChoice: &ChatStreamResponseChoice{
					Delta: &ChatStreamResponseChoiceDelta{
						Role: &role,
					},
				},
			},
		},
	}).ToBifrostResponsesStreamResponse(state)...)

	// Tool call chunk (no text content at all)
	all = append(all, (&BifrostChatResponse{
		ID:    "chatcmpl-test",
		Model: "test-model",
		Choices: []BifrostResponseChoice{
			{
				ChatStreamResponseChoice: &ChatStreamResponseChoice{
					Delta: &ChatStreamResponseChoiceDelta{
						ToolCalls: []ChatAssistantMessageToolCall{
							{
								Index:    0,
								ID:       &toolCallID,
								Function: ChatAssistantMessageToolCallFunction{Name: &funcName, Arguments: `{"q":"test"}`},
							},
						},
					},
				},
			},
		},
	}).ToBifrostResponsesStreamResponse(state)...)

	// Finish with tool_calls
	all = append(all, (&BifrostChatResponse{
		ID:    "chatcmpl-test",
		Model: "test-model",
		Choices: []BifrostResponseChoice{
			{
				FinishReason: &toolCallsFinish,
				ChatStreamResponseChoice: &ChatStreamResponseChoice{
					Delta: &ChatStreamResponseChoiceDelta{},
				},
			},
		},
	}).ToBifrostResponsesStreamResponse(state)...)

	var completed *BifrostResponsesStreamResponse
	for _, evt := range all {
		if evt != nil && evt.Type == ResponsesStreamResponseTypeCompleted {
			completed = evt
		}
	}

	if completed == nil || completed.Response == nil {
		t.Fatal("expected response.completed event")
	}

	output := completed.Response.Output
	if len(output) != 1 {
		t.Fatalf("expected 1 output item (function_call only), got %d", len(output))
	}

	item := output[0]
	if item.Type == nil || *item.Type != ResponsesMessageTypeFunctionCall {
		t.Fatal("expected function_call type")
	}
	if item.ResponsesToolMessage == nil {
		t.Fatal("function_call item missing ResponsesToolMessage")
	}
	if item.Name == nil || *item.Name != "get_weather" {
		t.Fatalf("expected function name %q, got %v", "get_weather", item.Name)
	}
	if item.Arguments == nil || *item.Arguments != `{"q":"test"}` {
		t.Fatalf("expected arguments %q, got %v", `{"q":"test"}`, item.Arguments)
	}
}

func TestToBifrostResponsesStreamResponse_MapsLengthToIncompleteEvent(t *testing.T) {
	state := AcquireChatToResponsesStreamState()
	defer ReleaseChatToResponsesStreamState(state)

	makeChunk := func(role *string, content *string, finishReason *string) *BifrostChatResponse {
		return &BifrostChatResponse{
			ID:    "chatcmpl-test",
			Model: "test-model",
			Choices: []BifrostResponseChoice{
				{
					FinishReason: finishReason,
					ChatStreamResponseChoice: &ChatStreamResponseChoice{
						Delta: &ChatStreamResponseChoiceDelta{
							Role:    role,
							Content: content,
						},
					},
				},
			},
		}
	}

	role := string(ChatMessageRoleAssistant)
	part := "Hello"
	length := string(BifrostFinishReasonLength)

	var all []*BifrostResponsesStreamResponse
	all = append(all, makeChunk(&role, nil, nil).ToBifrostResponsesStreamResponse(state)...)
	all = append(all, makeChunk(nil, &part, nil).ToBifrostResponsesStreamResponse(state)...)
	all = append(all, makeChunk(nil, nil, &length).ToBifrostResponsesStreamResponse(state)...)

	var completed *BifrostResponsesStreamResponse
	var incomplete *BifrostResponsesStreamResponse
	for _, evt := range all {
		if evt == nil {
			continue
		}
		if evt.Type == ResponsesStreamResponseTypeCompleted {
			completed = evt
		}
		if evt.Type == ResponsesStreamResponseTypeIncomplete {
			incomplete = evt
		}
	}

	if completed != nil {
		t.Fatal("did not expect response.completed for finish_reason=length")
	}
	if incomplete == nil || incomplete.Response == nil {
		t.Fatal("expected response.incomplete with response payload")
	}
	if incomplete.Response.Status == nil || *incomplete.Response.Status != "incomplete" {
		t.Fatal("expected terminal response status to be incomplete")
	}
	if incomplete.Response.IncompleteDetails == nil || incomplete.Response.IncompleteDetails.Reason != "max_output_tokens" {
		t.Fatal("expected incomplete_details.reason to be max_output_tokens")
	}
	if incomplete.Response.StopReason == nil || *incomplete.Response.StopReason != length {
		t.Fatalf("expected stop_reason %q, got %v", length, incomplete.Response.StopReason)
	}
}

func TestToBifrostResponsesStreamResponse_MapsContentFilterToIncompleteEvent(t *testing.T) {
	state := AcquireChatToResponsesStreamState()
	defer ReleaseChatToResponsesStreamState(state)

	makeChunk := func(role *string, content *string, finishReason *string) *BifrostChatResponse {
		return &BifrostChatResponse{
			ID:    "chatcmpl-test",
			Model: "test-model",
			Choices: []BifrostResponseChoice{
				{
					FinishReason: finishReason,
					ChatStreamResponseChoice: &ChatStreamResponseChoice{
						Delta: &ChatStreamResponseChoiceDelta{
							Role:    role,
							Content: content,
						},
					},
				},
			},
		}
	}

	role := string(ChatMessageRoleAssistant)
	part := "Hello"
	contentFilter := "content_filter"

	var all []*BifrostResponsesStreamResponse
	all = append(all, makeChunk(&role, nil, nil).ToBifrostResponsesStreamResponse(state)...)
	all = append(all, makeChunk(nil, &part, nil).ToBifrostResponsesStreamResponse(state)...)
	all = append(all, makeChunk(nil, nil, &contentFilter).ToBifrostResponsesStreamResponse(state)...)

	var completed *BifrostResponsesStreamResponse
	var incomplete *BifrostResponsesStreamResponse
	for _, evt := range all {
		if evt == nil {
			continue
		}
		if evt.Type == ResponsesStreamResponseTypeCompleted {
			completed = evt
		}
		if evt.Type == ResponsesStreamResponseTypeIncomplete {
			incomplete = evt
		}
	}

	if completed != nil {
		t.Fatal("did not expect response.completed for finish_reason=content_filter")
	}
	if incomplete == nil || incomplete.Response == nil {
		t.Fatal("expected response.incomplete with response payload")
	}
	if incomplete.Response.Status == nil || *incomplete.Response.Status != "incomplete" {
		t.Fatal("expected terminal response status to be incomplete")
	}
	if incomplete.Response.IncompleteDetails == nil || incomplete.Response.IncompleteDetails.Reason != "content_filter" {
		t.Fatal("expected incomplete_details.reason to be content_filter")
	}
}

// ---------------------------------------------------------------------------
// response_format ↔ text.format conversion tests
// ---------------------------------------------------------------------------

func makeJSONSchemaResponseFormat(name, description string, strict bool, schema interface{}) interface{} {
	jsObj := map[string]interface{}{
		"name":   name,
		"strict": strict,
		"schema": schema,
	}
	if description != "" {
		jsObj["description"] = description
	}
	return map[string]interface{}{
		"type":        "json_schema",
		"json_schema": jsObj,
	}
}

func TestToResponsesRequest_ResponseFormat_JSONSchema(t *testing.T) {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"name": map[string]interface{}{"type": "string"}},
		"required":   []interface{}{"name"},
	}
	rf := makeJSONSchemaResponseFormat("CityInfo", "City schema", true, schema)
	chatReq := &BifrostChatRequest{
		Params: &ChatParameters{ResponseFormat: &rf},
	}

	rr := chatReq.ToResponsesRequest()
	if rr.Params == nil || rr.Params.Text == nil || rr.Params.Text.Format == nil {
		t.Fatal("expected Text.Format to be set")
	}
	f := rr.Params.Text.Format
	if f.Type != "json_schema" {
		t.Fatalf("expected type json_schema, got %q", f.Type)
	}
	if f.Name == nil || *f.Name != "CityInfo" {
		t.Fatalf("expected Name=CityInfo, got %v", f.Name)
	}
	if f.Description == nil || *f.Description != "City schema" {
		t.Fatalf("expected Description='City schema', got %v", f.Description)
	}
	if f.Strict == nil || !*f.Strict {
		t.Fatal("expected Strict=true")
	}
	// JSONSchemaFromMap populates typed fields, not the raw Schema *any blob.
	if f.JSONSchema == nil {
		t.Fatal("expected JSONSchema to be set")
	}
	if f.JSONSchema.Type == nil || *f.JSONSchema.Type != "object" {
		t.Fatalf("expected JSONSchema.Type=object, got %v", f.JSONSchema.Type)
	}
	if f.JSONSchema.Properties == nil {
		t.Fatal("expected JSONSchema.Properties to be set")
	}
	if len(f.JSONSchema.Required) == 0 {
		t.Fatal("expected JSONSchema.Required to be set")
	}
}

func TestToResponsesRequest_ResponseFormat_JSONObject(t *testing.T) {
	rf := interface{}(map[string]interface{}{"type": "json_object"})
	chatReq := &BifrostChatRequest{
		Params: &ChatParameters{ResponseFormat: &rf},
	}

	rr := chatReq.ToResponsesRequest()
	if rr.Params == nil || rr.Params.Text == nil || rr.Params.Text.Format == nil {
		t.Fatal("expected Text.Format to be set")
	}
	if rr.Params.Text.Format.Type != "json_object" {
		t.Fatalf("expected type json_object, got %q", rr.Params.Text.Format.Type)
	}
}

func TestToResponsesRequest_ResponseFormat_Text(t *testing.T) {
	rf := interface{}(map[string]interface{}{"type": "text"})
	chatReq := &BifrostChatRequest{
		Params: &ChatParameters{ResponseFormat: &rf},
	}

	rr := chatReq.ToResponsesRequest()
	if rr.Params == nil || rr.Params.Text == nil || rr.Params.Text.Format == nil {
		t.Fatal("expected Text.Format to be set")
	}
	if rr.Params.Text.Format.Type != "text" {
		t.Fatalf("expected type text, got %q", rr.Params.Text.Format.Type)
	}
}

func TestToResponsesRequest_NoResponseFormat(t *testing.T) {
	chatReq := &BifrostChatRequest{
		Params: &ChatParameters{},
	}
	rr := chatReq.ToResponsesRequest()
	if rr.Params != nil && rr.Params.Text != nil && rr.Params.Text.Format != nil {
		t.Fatal("expected Text.Format to be nil when no response_format set")
	}
}

func TestToChatRequest_TextFormat_JSONSchema(t *testing.T) {
	schema := map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"country"},
	}
	rr := &BifrostResponsesRequest{
		Params: &ResponsesParameters{
			Text: &ResponsesTextConfig{
				Format: &ResponsesTextConfigFormat{
					Type:        "json_schema",
					Name:        Ptr("CityInfo"),
					Description: Ptr("City schema"),
					Strict:      Ptr(true),
					JSONSchema:  &ResponsesTextConfigFormatJSONSchema{Schema: func() *any { v := any(schema); return &v }()},
				},
			},
		},
	}

	cr := rr.ToChatRequest(nil)
	if cr.Params == nil || cr.Params.ResponseFormat == nil {
		t.Fatal("expected ResponseFormat to be set")
	}
	rfMap, ok := (*cr.Params.ResponseFormat).(map[string]interface{})
	if !ok {
		t.Fatal("expected ResponseFormat to be map[string]interface{}")
	}
	if rfMap["type"] != "json_schema" {
		t.Fatalf("expected type json_schema, got %v", rfMap["type"])
	}
	jsObj, ok := rfMap["json_schema"].(map[string]interface{})
	if !ok {
		t.Fatal("expected json_schema inner object")
	}
	if jsObj["name"] != "CityInfo" {
		t.Fatalf("expected name=CityInfo, got %v", jsObj["name"])
	}
	if jsObj["description"] != "City schema" {
		t.Fatalf("expected description='City schema', got %v", jsObj["description"])
	}
	if jsObj["strict"] != true {
		t.Fatalf("expected strict=true, got %v", jsObj["strict"])
	}
	if jsObj["schema"] == nil {
		t.Fatal("expected schema to be set")
	}
}

func TestToChatRequest_TextFormat_JSONObject(t *testing.T) {
	rr := &BifrostResponsesRequest{
		Params: &ResponsesParameters{
			Text: &ResponsesTextConfig{
				Format: &ResponsesTextConfigFormat{Type: "json_object"},
			},
		},
	}

	cr := rr.ToChatRequest(nil)
	if cr.Params == nil || cr.Params.ResponseFormat == nil {
		t.Fatal("expected ResponseFormat to be set")
	}
	rfMap, ok := (*cr.Params.ResponseFormat).(map[string]interface{})
	if !ok {
		t.Fatal("expected ResponseFormat to be map[string]interface{}")
	}
	if rfMap["type"] != "json_object" {
		t.Fatalf("expected type json_object, got %v", rfMap["type"])
	}
	if _, hasJS := rfMap["json_schema"]; hasJS {
		t.Fatal("json_schema key should not be present for json_object type")
	}
}

func TestToChatRequest_NoTextFormat(t *testing.T) {
	rr := &BifrostResponsesRequest{
		Params: &ResponsesParameters{},
	}
	cr := rr.ToChatRequest(nil)
	if cr.Params != nil && cr.Params.ResponseFormat != nil {
		t.Fatal("expected ResponseFormat to be nil when no text.format set")
	}
}

func TestResponseFormatRoundTrip_ChatToResponsesAndBack(t *testing.T) {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"city": map[string]interface{}{"type": "string"}},
	}
	rf := makeJSONSchemaResponseFormat("CityInfo", "City schema", true, schema)
	chatReq := &BifrostChatRequest{
		Params: &ChatParameters{ResponseFormat: &rf},
	}

	// Chat → Responses → Chat
	rr := chatReq.ToResponsesRequest()
	cr := rr.ToChatRequest(nil)

	if cr.Params == nil || cr.Params.ResponseFormat == nil {
		t.Fatal("expected ResponseFormat to survive round-trip")
	}
	rfMap, ok := (*cr.Params.ResponseFormat).(map[string]interface{})
	if !ok {
		t.Fatal("expected ResponseFormat to be map after round-trip")
	}
	if rfMap["type"] != "json_schema" {
		t.Fatalf("type did not survive round-trip: got %v", rfMap["type"])
	}
	jsObj, ok := rfMap["json_schema"].(map[string]interface{})
	if !ok {
		t.Fatal("json_schema inner object missing after round-trip")
	}
	if jsObj["name"] != "CityInfo" {
		t.Fatalf("name did not survive round-trip: got %v", jsObj["name"])
	}
	if jsObj["description"] != "City schema" {
		t.Fatalf("description did not survive round-trip: got %v", jsObj["description"])
	}
	if jsObj["strict"] != true {
		t.Fatalf("strict did not survive round-trip: got %v", jsObj["strict"])
	}
}

// ---------------------------------------------------------------------------
// JSON wire-format tests (catch serialization bugs, not just struct values)
// ---------------------------------------------------------------------------

// TestToResponsesRequest_JSONSchema_NoDoubleNesting verifies that the json_schema
// body serializes as "schema": {...} and NOT "schema": {"schema": {...}}.
func TestToResponsesRequest_JSONSchema_NoDoubleNesting(t *testing.T) {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"name": map[string]interface{}{"type": "string"}},
		"required":   []interface{}{"name"},
	}
	rf := makeJSONSchemaResponseFormat("CityInfo", "", true, schema)
	chatReq := &BifrostChatRequest{
		Params: &ChatParameters{ResponseFormat: &rf},
	}

	rr := chatReq.ToResponsesRequest()
	if rr.Params == nil || rr.Params.Text == nil || rr.Params.Text.Format == nil {
		t.Fatal("expected Text.Format to be set")
	}

	// Marshal to JSON and inspect the wire shape
	b, err := json.Marshal(rr.Params.Text.Format)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var wire map[string]interface{}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	schemaVal, ok := wire["schema"]
	if !ok {
		t.Fatalf("expected 'schema' key in wire format, got: %s", string(b))
	}

	schemaMap, ok := schemaVal.(map[string]interface{})
	if !ok {
		t.Fatalf("expected schema to be an object, got: %T — wire: %s", schemaVal, string(b))
	}

	// Must NOT have a nested "schema" key (double-nesting bug)
	if _, nested := schemaMap["schema"]; nested {
		t.Fatalf("double-nested schema detected: wire=%s", string(b))
	}

	// Must have the actual schema fields at the top level
	if schemaMap["type"] != "object" {
		t.Fatalf("expected schema.type=object, wire=%s", string(b))
	}
}

// TestToChatRequest_TextFormat_TypedFields verifies that when a Responses API
// request has schema fields spread across the typed fields of
// ResponsesTextConfigFormatJSONSchema (Schema==nil), ToChatRequest still
// produces a valid response_format with a non-empty json_schema.schema body.
func TestToChatRequest_TextFormat_TypedFields(t *testing.T) {
	props := map[string]any{"name": map[string]any{"type": "string"}}
	rr := &BifrostResponsesRequest{
		Params: &ResponsesParameters{
			Text: &ResponsesTextConfig{
				Format: &ResponsesTextConfigFormat{
					Type:   "json_schema",
					Name:   Ptr("CityInfo"),
					Strict: Ptr(true),
					// Schema is nil — fields are spread across typed fields (direct client path)
					JSONSchema: &ResponsesTextConfigFormatJSONSchema{
						Type:       Ptr("object"),
						Properties: &props,
						Required:   []string{"name"},
						// Schema *any is intentionally nil here
					},
				},
			},
		},
	}

	cr := rr.ToChatRequest(nil)
	if cr.Params == nil || cr.Params.ResponseFormat == nil {
		t.Fatal("expected ResponseFormat to be set")
	}

	rfMap, ok := (*cr.Params.ResponseFormat).(map[string]interface{})
	if !ok {
		t.Fatal("expected ResponseFormat to be a map")
	}

	jsObj, ok := rfMap["json_schema"].(map[string]interface{})
	if !ok {
		t.Fatal("expected json_schema inner object")
	}

	// Schema body must be present and non-empty
	schemaVal, ok := jsObj["schema"]
	if !ok {
		t.Fatalf("schema body silently dropped: json_schema=%v", jsObj)
	}

	schemaMap, ok := schemaVal.(map[string]interface{})
	if !ok {
		t.Fatalf("expected schema to be a map, got %T", schemaVal)
	}
	if schemaMap["type"] != "object" {
		t.Fatalf("expected schema.type=object, got %v", schemaMap["type"])
	}
}

// streamChunk builds a single Chat streaming chunk for the reasoning/text ordering tests.
func streamChunk(id string, delta *ChatStreamResponseChoiceDelta, finishReason *string) *BifrostChatResponse {
	return &BifrostChatResponse{
		ID:    id,
		Model: "deepseek-reasoner",
		Choices: []BifrostResponseChoice{
			{
				Index:                    0,
				FinishReason:             finishReason,
				ChatStreamResponseChoice: &ChatStreamResponseChoice{Delta: delta},
			},
		},
	}
}

// TestToBifrostResponsesStreamResponse_ReasoningOpensThinkingBlock reproduces the
// Claude Code "Content block not found" disconnect: a Chat-Completions reasoning
// model (reasoning_content first, then content) must yield a proper reasoning output
// item (added -> deltas carrying its ItemID -> done) BEFORE the text item, so the
// Anthropic reverse converter emits content_block_start type=thinking before any
// thinking delta. Previously reasoning only opened a text item and the reasoning
// delta had no ItemID, producing an orphan thinking block.
func TestToBifrostResponsesStreamResponse_ReasoningOpensThinkingBlock(t *testing.T) {
	state := AcquireChatToResponsesStreamState()
	defer ReleaseChatToResponsesStreamState(state)

	chunks := []*BifrostChatResponse{
		streamChunk("c1", &ChatStreamResponseChoiceDelta{Role: Ptr("assistant"), Reasoning: Ptr("")}, nil),
		streamChunk("c1", &ChatStreamResponseChoiceDelta{Reasoning: Ptr("The")}, nil),
		streamChunk("c1", &ChatStreamResponseChoiceDelta{Reasoning: Ptr(" user")}, nil),
		streamChunk("c1", &ChatStreamResponseChoiceDelta{Content: Ptr("Run ")}, nil),
		streamChunk("c1", &ChatStreamResponseChoiceDelta{Content: Ptr("tests")}, nil),
		streamChunk("c1", &ChatStreamResponseChoiceDelta{}, Ptr("stop")),
	}

	var events []*BifrostResponsesStreamResponse
	for _, c := range chunks {
		events = append(events, c.ToBifrostResponsesStreamResponse(state)...)
	}

	var reasoningItemID string
	var reasoningOutputIndex, textOutputIndex = -1, -1
	reasoningAddedIdx, reasoningDoneIdx, textAddedIdx := -1, -1, -1
	firstReasoningDeltaIdx := -1

	for i, e := range events {
		switch e.Type {
		case ResponsesStreamResponseTypeOutputItemAdded:
			if e.Item != nil && e.Item.Type != nil && *e.Item.Type == ResponsesMessageTypeReasoning {
				if reasoningAddedIdx == -1 {
					reasoningAddedIdx = i
				}
				if e.Item.ID != nil {
					reasoningItemID = *e.Item.ID
				}
				if e.OutputIndex != nil {
					reasoningOutputIndex = *e.OutputIndex
				}
			}
			if e.Item != nil && e.Item.Type != nil && *e.Item.Type == ResponsesMessageTypeMessage {
				if textAddedIdx == -1 {
					textAddedIdx = i
				}
				if e.OutputIndex != nil {
					textOutputIndex = *e.OutputIndex
				}
			}
		case ResponsesStreamResponseTypeReasoningSummaryTextDelta:
			if firstReasoningDeltaIdx == -1 {
				firstReasoningDeltaIdx = i
			}
			if e.ItemID == nil || *e.ItemID == "" {
				t.Fatalf("reasoning delta at event %d has no ItemID (orphan thinking block)", i)
			}
			if e.ItemID != nil && *e.ItemID != reasoningItemID {
				t.Fatalf("reasoning delta ItemID %q != reasoning item ID %q", *e.ItemID, reasoningItemID)
			}
		case ResponsesStreamResponseTypeOutputItemDone:
			if e.Item != nil && e.Item.Type != nil && *e.Item.Type == ResponsesMessageTypeReasoning && reasoningDoneIdx == -1 {
				reasoningDoneIdx = i
			}
		}
	}

	if reasoningAddedIdx == -1 {
		t.Fatal("no reasoning output_item.added emitted")
	}
	if firstReasoningDeltaIdx == -1 {
		t.Fatal("no reasoning delta emitted")
	}
	if reasoningAddedIdx >= firstReasoningDeltaIdx {
		t.Fatalf("reasoning output_item.added (%d) must precede first reasoning delta (%d)", reasoningAddedIdx, firstReasoningDeltaIdx)
	}
	if reasoningDoneIdx == -1 {
		t.Fatal("no reasoning output_item.done emitted (thinking block never closed)")
	}
	if textAddedIdx == -1 {
		t.Fatal("no text output_item.added emitted")
	}
	if reasoningDoneIdx >= textAddedIdx {
		t.Fatalf("reasoning output_item.done (%d) must precede text output_item.added (%d)", reasoningDoneIdx, textAddedIdx)
	}
	if reasoningOutputIndex != 0 {
		t.Fatalf("reasoning output index = %d, want 0 (reasoning first)", reasoningOutputIndex)
	}
	if textOutputIndex <= reasoningOutputIndex {
		t.Fatalf("text output index %d must be greater than reasoning output index %d", textOutputIndex, reasoningOutputIndex)
	}
}
