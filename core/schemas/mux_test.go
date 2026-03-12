package schemas

import "testing"

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

	chatReq := req.ToChatRequest()
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

	chatReq := req.ToChatRequest()
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

	chatReq := req.ToChatRequest()
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

	chatReq := req.ToChatRequest()
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

			chatReq := req.ToChatRequest()
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

			chatReq := req.ToChatRequest()
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

func TestToBifrostResponsesResponse_UnknownFinishReasonLeavesStatusUnset(t *testing.T) {
	unknown := "content_filter"
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
}
