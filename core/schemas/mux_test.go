package schemas

import (
	"encoding/json"
	"reflect"
	"strings"
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

func TestToBifrostResponsesStreamResponse_NilDeltaWithFinishReasonStillCompletes(t *testing.T) {
	state := AcquireChatToResponsesStreamState()
	defer ReleaseChatToResponsesStreamState(state)

	role := string(ChatMessageRoleAssistant)
	part := "Hello"
	roleChunk := &BifrostChatResponse{
		ID:    "chatcmpl-test",
		Model: "test-model",
		Choices: []BifrostResponseChoice{
			{ChatStreamResponseChoice: &ChatStreamResponseChoice{Delta: &ChatStreamResponseChoiceDelta{Role: &role}}},
		},
	}
	contentChunk := &BifrostChatResponse{
		ID:    "chatcmpl-test",
		Model: "test-model",
		Choices: []BifrostResponseChoice{
			{ChatStreamResponseChoice: &ChatStreamResponseChoice{Delta: &ChatStreamResponseChoiceDelta{Content: &part}}},
		},
	}

	// Reproduces the real wire shape many OpenAI-compatible upstreams send on their
	// terminal chunk: {"choices":[{"delta":null,"finish_reason":"stop"}],"usage":{...}}
	var terminalChunk BifrostChatResponse
	terminalJSON := `{"id":"chatcmpl-test","model":"test-model","choices":[{"index":0,"delta":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`
	if err := json.Unmarshal([]byte(terminalJSON), &terminalChunk); err != nil {
		t.Fatalf("failed to unmarshal terminal chunk fixture: %v", err)
	}

	var all []*BifrostResponsesStreamResponse
	all = append(all, roleChunk.ToBifrostResponsesStreamResponse(state)...)
	all = append(all, contentChunk.ToBifrostResponsesStreamResponse(state)...)
	all = append(all, terminalChunk.ToBifrostResponsesStreamResponse(state)...)

	var completed *BifrostResponsesStreamResponse
	for _, evt := range all {
		if evt != nil && evt.Type == ResponsesStreamResponseTypeCompleted {
			completed = evt
		}
	}

	if completed == nil {
		t.Fatal("expected a Completed event even though the terminal chunk had delta:null")
	}
	if completed.Response == nil || completed.Response.Usage == nil {
		t.Fatal("expected Completed event to carry usage")
	}
	if completed.Response.Usage.InputTokens != 5 || completed.Response.Usage.OutputTokens != 2 {
		t.Fatalf("unexpected usage: %+v", completed.Response.Usage)
	}
	if completed.Response.StopReason == nil || *completed.Response.StopReason != "stop" {
		t.Fatalf("expected stop_reason stop, got %+v", completed.Response.StopReason)
	}
}

func TestToBifrostResponsesStreamResponse_MissingDeltaKeyWithFinishReasonStillCompletes(t *testing.T) {
	state := AcquireChatToResponsesStreamState()
	defer ReleaseChatToResponsesStreamState(state)

	role := string(ChatMessageRoleAssistant)
	part := "Hi"
	roleChunk := &BifrostChatResponse{
		ID:    "chatcmpl-test2",
		Model: "test-model",
		Choices: []BifrostResponseChoice{
			{ChatStreamResponseChoice: &ChatStreamResponseChoice{Delta: &ChatStreamResponseChoiceDelta{Role: &role}}},
		},
	}
	contentChunk := &BifrostChatResponse{
		ID:    "chatcmpl-test2",
		Model: "test-model",
		Choices: []BifrostResponseChoice{
			{ChatStreamResponseChoice: &ChatStreamResponseChoice{Delta: &ChatStreamResponseChoiceDelta{Content: &part}}},
		},
	}

	// Some upstreams omit the "delta" key entirely on the terminal chunk rather than
	// sending it as null, leaving ChatStreamResponseChoice itself nil after unmarshal.
	var terminalChunk BifrostChatResponse
	terminalJSON := `{"id":"chatcmpl-test2","model":"test-model","choices":[{"index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
	if err := json.Unmarshal([]byte(terminalJSON), &terminalChunk); err != nil {
		t.Fatalf("failed to unmarshal terminal chunk fixture: %v", err)
	}

	var all []*BifrostResponsesStreamResponse
	all = append(all, roleChunk.ToBifrostResponsesStreamResponse(state)...)
	all = append(all, contentChunk.ToBifrostResponsesStreamResponse(state)...)
	all = append(all, terminalChunk.ToBifrostResponsesStreamResponse(state)...)

	var completed *BifrostResponsesStreamResponse
	for _, evt := range all {
		if evt != nil && evt.Type == ResponsesStreamResponseTypeCompleted {
			completed = evt
		}
	}

	if completed == nil {
		t.Fatal("expected a Completed event even though the terminal chunk omitted the delta key")
	}
	if completed.Response == nil || completed.Response.Usage == nil {
		t.Fatal("expected Completed event to carry usage")
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

func TestToBifrostResponsesResponse_UnknownFinishReasonLeavesStatusUnset(t *testing.T) {
	unknown := "some_unmapped_reason"
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

func TestToBifrostResponsesResponse_MapsContentFilterToIncomplete(t *testing.T) {
	for _, finish := range []string{"content_filter", "guardrail_intervened"} {
		t.Run(finish, func(t *testing.T) {
			fr := finish
			resp := (&BifrostChatResponse{
				Choices: []BifrostResponseChoice{
					{FinishReason: &fr},
				},
			}).ToBifrostResponsesResponse()

			if resp == nil {
				t.Fatal("expected non-nil response")
			}
			if resp.Status == nil || *resp.Status != ResponsesResponseStatusIncomplete {
				t.Fatalf("expected status %q, got %v", ResponsesResponseStatusIncomplete, resp.Status)
			}
			if resp.IncompleteDetails == nil || resp.IncompleteDetails.Reason != ResponsesResponseIncompleteReasonContentFilter {
				t.Fatalf("expected incomplete_details.reason %q, got %+v", ResponsesResponseIncompleteReasonContentFilter, resp.IncompleteDetails)
			}
			if resp.StopReason == nil || *resp.StopReason != finish {
				t.Fatalf("expected stop_reason %q, got %v", finish, resp.StopReason)
			}
		})
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
	var addedOutputIndex *int
	for _, evt := range all {
		if evt == nil {
			continue
		}
		if evt.Type == ResponsesStreamResponseTypeOutputItemAdded && evt.Item != nil && evt.Item.CallID != nil {
			addedOutputIndex = evt.OutputIndex
		}
		if evt.Type == ResponsesStreamResponseTypeCompleted {
			completed = evt
		}
	}

	if addedOutputIndex == nil || *addedOutputIndex != 0 {
		t.Fatalf("tool-only output_item.added output_index = %v, want 0", addedOutputIndex)
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

func TestToBifrostResponsesStreamResponse_HandlesParallelToolCallsInOneChunk(t *testing.T) {
	state := AcquireChatToResponsesStreamState()
	defer ReleaseChatToResponsesStreamState(state)

	firstID, secondID := "call_weather", "call_time"
	firstName, secondName := "get_weather", "get_time"
	finishReason := string(BifrostFinishReasonToolCalls)

	chunks := []*BifrostChatResponse{
		streamChunk("chatcmpl-parallel", &ChatStreamResponseChoiceDelta{
			Role: Ptr(string(ChatMessageRoleAssistant)),
			ToolCalls: []ChatAssistantMessageToolCall{
				{Index: 0, ID: &firstID, Function: ChatAssistantMessageToolCallFunction{Name: &firstName, Arguments: `{"city":`}},
				{Index: 1, ID: &secondID, Function: ChatAssistantMessageToolCallFunction{Name: &secondName, Arguments: `{"zone":`}},
			},
		}, nil),
		streamChunk("chatcmpl-parallel", &ChatStreamResponseChoiceDelta{
			ToolCalls: []ChatAssistantMessageToolCall{
				{Index: 0, Function: ChatAssistantMessageToolCallFunction{Arguments: `"Paris"}`}},
				{Index: 1, Function: ChatAssistantMessageToolCallFunction{Arguments: `"UTC"}`}},
			},
		}, nil),
		streamChunk("chatcmpl-parallel", &ChatStreamResponseChoiceDelta{}, &finishReason),
	}

	var events []*BifrostResponsesStreamResponse
	for _, chunk := range chunks {
		events = append(events, chunk.ToBifrostResponsesStreamResponse(state)...)
	}

	expectedTypes := []ResponsesStreamResponseType{
		ResponsesStreamResponseTypeCreated,
		ResponsesStreamResponseTypeInProgress,
		ResponsesStreamResponseTypeOutputItemAdded,
		ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
		ResponsesStreamResponseTypeOutputItemAdded,
		ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
		ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
		ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
		ResponsesStreamResponseTypeFunctionCallArgumentsDone,
		ResponsesStreamResponseTypeOutputItemDone,
		ResponsesStreamResponseTypeFunctionCallArgumentsDone,
		ResponsesStreamResponseTypeOutputItemDone,
		ResponsesStreamResponseTypeCompleted,
	}
	if len(events) != len(expectedTypes) {
		t.Fatalf("event count = %d, want %d", len(events), len(expectedTypes))
	}
	for i, expectedType := range expectedTypes {
		if events[i].Type != expectedType || events[i].SequenceNumber != i {
			t.Fatalf("event[%d] = type %q sequence %d, want type %q sequence %d", i, events[i].Type, events[i].SequenceNumber, expectedType, i)
		}
	}
	for i, expectedIndex := range []int{0, 0, 1, 1} {
		event := events[8+i]
		if event.OutputIndex == nil || *event.OutputIndex != expectedIndex {
			t.Fatalf("terminal event[%d] output_index = %v, want %d", i, event.OutputIndex, expectedIndex)
		}
	}

	addedIndices := make(map[string]int)
	deltaCounts := make(map[string]int)
	doneIndices := make(map[string]int)
	var completed *BifrostResponsesStreamResponse
	for _, event := range events {
		switch event.Type {
		case ResponsesStreamResponseTypeOutputItemAdded:
			if event.Item != nil && event.Item.CallID != nil && event.OutputIndex != nil {
				addedIndices[*event.Item.CallID] = *event.OutputIndex
			}
		case ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
			if event.ItemID != nil {
				deltaCounts[*event.ItemID]++
			}
		case ResponsesStreamResponseTypeOutputItemDone:
			if event.Item != nil && event.Item.CallID != nil && event.OutputIndex != nil {
				doneIndices[*event.Item.CallID] = *event.OutputIndex
			}
		case ResponsesStreamResponseTypeCompleted:
			completed = event
		}
	}

	if len(addedIndices) != 2 {
		t.Fatalf("expected output_item.added for both tool calls, got %v", addedIndices)
	}
	if addedIndices[firstID] == addedIndices[secondID] {
		t.Fatalf("parallel tool calls must have distinct output indexes, got %v", addedIndices)
	}
	for _, id := range []string{firstID, secondID} {
		if deltaCounts[id] != 2 {
			t.Errorf("expected two argument deltas for %s, got %d", id, deltaCounts[id])
		}
		if doneIndices[id] != addedIndices[id] {
			t.Errorf("output_item.done index for %s = %d, want %d", id, doneIndices[id], addedIndices[id])
		}
	}

	if completed == nil || completed.Response == nil {
		t.Fatal("expected response.completed")
	}
	if len(completed.Response.Output) != 2 {
		t.Fatalf("expected both tool calls in completed output, got %d", len(completed.Response.Output))
	}
	for i, expected := range []struct {
		id   string
		name string
		args string
	}{
		{firstID, firstName, `{"city":"Paris"}`},
		{secondID, secondName, `{"zone":"UTC"}`},
	} {
		item := completed.Response.Output[i]
		if item.CallID == nil || *item.CallID != expected.id || item.Name == nil || *item.Name != expected.name || item.Arguments == nil || *item.Arguments != expected.args {
			t.Errorf("completed output[%d] = %+v, want id=%q name=%q args=%q", i, item.ResponsesToolMessage, expected.id, expected.name, expected.args)
		}
	}
}

func TestToBifrostResponsesStreamResponse_ClosesToolCallWithEmptyArguments(t *testing.T) {
	state := AcquireChatToResponsesStreamState()
	defer ReleaseChatToResponsesStreamState(state)

	callID, name := "call_empty", "no_arguments"
	finishReason := string(BifrostFinishReasonToolCalls)
	chunks := []*BifrostChatResponse{
		streamChunk("chatcmpl-empty", &ChatStreamResponseChoiceDelta{
			Role: Ptr(string(ChatMessageRoleAssistant)),
			ToolCalls: []ChatAssistantMessageToolCall{
				{Index: 0, ID: &callID, Function: ChatAssistantMessageToolCallFunction{Name: &name}},
			},
		}, nil),
		streamChunk("chatcmpl-empty", &ChatStreamResponseChoiceDelta{}, &finishReason),
	}

	var events []*BifrostResponsesStreamResponse
	for _, chunk := range chunks {
		events = append(events, chunk.ToBifrostResponsesStreamResponse(state)...)
	}

	expectedTypes := []ResponsesStreamResponseType{
		ResponsesStreamResponseTypeCreated,
		ResponsesStreamResponseTypeInProgress,
		ResponsesStreamResponseTypeOutputItemAdded,
		ResponsesStreamResponseTypeFunctionCallArgumentsDone,
		ResponsesStreamResponseTypeOutputItemDone,
		ResponsesStreamResponseTypeCompleted,
	}
	if len(events) != len(expectedTypes) {
		t.Fatalf("event count = %d, want %d", len(events), len(expectedTypes))
	}
	for i, expectedType := range expectedTypes {
		if events[i].Type != expectedType || events[i].SequenceNumber != i {
			t.Fatalf("event[%d] = type %q sequence %d, want type %q sequence %d", i, events[i].Type, events[i].SequenceNumber, expectedType, i)
		}
	}
	for _, event := range events[2:5] {
		if event.OutputIndex == nil || *event.OutputIndex != 0 {
			t.Fatalf("%s output_index = %v, want 0", event.Type, event.OutputIndex)
		}
	}
	if events[3].Arguments == nil || *events[3].Arguments != "{}" {
		t.Fatalf("function_call_arguments.done arguments = %v, want empty object", events[3].Arguments)
	}
	if events[4].Item == nil || events[4].Item.Arguments == nil || *events[4].Item.Arguments != "{}" {
		t.Fatalf("output_item.done arguments = %v, want empty object", events[4].Item)
	}
	completed := events[5]
	if completed.Response == nil || len(completed.Response.Output) != 1 || completed.Response.Output[0].Arguments == nil || *completed.Response.Output[0].Arguments != "{}" {
		t.Fatalf("completed output = %+v, want one tool call with empty object arguments", completed.Response)
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
	// "type" before "required" is non-alphabetical on purpose: a regression to
	// sorted marshaling would reorder them and fail the exact-bytes assertion below.
	schema := NewOrderedMapFromPairs(
		KV("type", "object"),
		KV("required", []interface{}{"country"}),
	)
	rr := &BifrostResponsesRequest{
		Params: &ResponsesParameters{
			Text: &ResponsesTextConfig{
				Format: &ResponsesTextConfigFormat{
					Type:        "json_schema",
					Name:        Ptr("CityInfo"),
					Description: Ptr("City schema"),
					Strict:      Ptr(true),
					JSONSchema:  &ResponsesTextConfigFormatJSONSchema{Schema: &JSONSchemaOrBool{SchemaMap: schema}},
				},
			},
		},
	}

	cr := rr.ToChatRequest()
	if cr.Params == nil || cr.Params.ResponseFormat == nil {
		t.Fatal("expected ResponseFormat to be set")
	}
	rfMap := responseFormatAsMap(t, cr.Params.ResponseFormat)
	if rfMap["type"] != "json_schema" {
		t.Fatalf("expected type json_schema, got %v", rfMap["type"])
	}
	jsObj := nestedMap(t, rfMap, "json_schema")
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
	schemaBytes, err := MarshalSorted(jsObj["schema"])
	if err != nil {
		t.Fatalf("failed to marshal schema: %v", err)
	}
	if want := `{"type":"object","required":["country"]}`; string(schemaBytes) != want {
		t.Fatalf("schema key order not preserved: want %s, got %s", want, string(schemaBytes))
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

	cr := rr.ToChatRequest()
	if cr.Params == nil || cr.Params.ResponseFormat == nil {
		t.Fatal("expected ResponseFormat to be set")
	}
	rfMap := responseFormatAsMap(t, cr.Params.ResponseFormat)
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
	cr := rr.ToChatRequest()
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
	cr := rr.ToChatRequest()

	if cr.Params == nil || cr.Params.ResponseFormat == nil {
		t.Fatal("expected ResponseFormat to survive round-trip")
	}
	rfMap := responseFormatAsMap(t, cr.Params.ResponseFormat)
	if rfMap["type"] != "json_schema" {
		t.Fatalf("type did not survive round-trip: got %v", rfMap["type"])
	}
	jsObj := nestedMap(t, rfMap, "json_schema")
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
	// "zip" before "age" is non-alphabetical on purpose: a regression to sorted
	// marshaling would reorder them and fail the exact-bytes assertion below.
	props := NewOrderedMapFromPairs(
		KV("zip", map[string]any{"type": "string"}),
		KV("age", map[string]any{"type": "integer"}),
	)
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
						Properties: props,
						Required:   []string{"zip", "age"},
						// Schema is intentionally nil here
					},
				},
			},
		},
	}

	cr := rr.ToChatRequest()
	if cr.Params == nil || cr.Params.ResponseFormat == nil {
		t.Fatal("expected ResponseFormat to be set")
	}

	rfMap := responseFormatAsMap(t, cr.Params.ResponseFormat)

	jsObj := nestedMap(t, rfMap, "json_schema")

	// Schema body must be present and non-empty
	schemaVal, ok := jsObj["schema"]
	if !ok {
		t.Fatalf("schema body silently dropped: json_schema=%v", jsObj)
	}

	// The schema is handed back as an OrderedMap so the key order the client
	// declared survives the Responses -> Chat rewrite.
	schemaMap, ok := SafeExtractOrderedMap(schemaVal)
	if !ok {
		t.Fatalf("expected schema to be an ordered map, got %T", schemaVal)
	}
	if schemaType, _ := schemaMap.Get("type"); schemaType != "object" {
		t.Fatalf("expected schema.type=object, got %v", schemaType)
	}
	schemaProps, _ := schemaMap.Get("properties")
	propsBytes, err := MarshalSorted(schemaProps)
	if err != nil {
		t.Fatalf("failed to marshal properties: %v", err)
	}
	if want := `{"zip":{"type":"string"},"age":{"type":"integer"}}`; string(propsBytes) != want {
		t.Fatalf("properties key order not preserved: want %s, got %s", want, string(propsBytes))
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
	reasoningOutputIndex, textOutputIndex := -1, -1
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

// responseFormatAsMap reads a converted chat response_format regardless of its
// representation. The Responses to Chat conversion emits raw JSON (the same
// representation the chat wire decode produces), so tests read it through the
// same accessor providers use rather than type-asserting a map.
func responseFormatAsMap(t *testing.T, responseFormat *interface{}) map[string]interface{} {
	t.Helper()
	om, ok := SafeExtractOrderedMap(*responseFormat)
	if !ok || om == nil {
		t.Fatalf("expected response_format to be readable as an object, got %T", *responseFormat)
	}
	return om.ToMap()
}

func nestedMap(t *testing.T, parent map[string]interface{}, key string) map[string]interface{} {
	t.Helper()
	om, ok := SafeExtractOrderedMap(parent[key])
	if !ok || om == nil {
		t.Fatalf("expected %q to be an object, got %T", key, parent[key])
	}
	return om.ToMap()
}

func responsesTextOutput(text string) []ResponsesMessage {
	return []ResponsesMessage{
		{
			Type: Ptr(ResponsesMessageTypeMessage),
			Role: Ptr(ResponsesInputMessageRoleAssistant),
			Content: &ResponsesMessageContent{
				ContentBlocks: []ResponsesMessageContentBlock{
					{Type: ResponsesOutputMessageContentTypeText, Text: &text},
				},
			},
		},
	}
}

func TestToBifrostChatResponse_MapsCompletedToStop(t *testing.T) {
	chatResp := (&BifrostResponsesResponse{
		Status: Ptr(ResponsesResponseStatusCompleted),
		Output: responsesTextOutput("ok"),
	}).ToBifrostChatResponse()

	if chatResp == nil || len(chatResp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if chatResp.Choices[0].FinishReason == nil {
		t.Fatal("expected finish_reason to be set")
	}
	if got := *chatResp.Choices[0].FinishReason; got != string(BifrostFinishReasonStop) {
		t.Fatalf("expected finish_reason %q, got %q", BifrostFinishReasonStop, got)
	}
}

func TestToBifrostChatResponse_MapsFunctionCallToToolCalls(t *testing.T) {
	chatResp := (&BifrostResponsesResponse{
		Status: Ptr(ResponsesResponseStatusCompleted),
		Output: []ResponsesMessage{
			{
				Type: Ptr(ResponsesMessageTypeFunctionCall),
				Role: Ptr(ResponsesInputMessageRoleAssistant),
				ResponsesToolMessage: &ResponsesToolMessage{
					CallID:    Ptr("call_1"),
					Name:      Ptr("search"),
					Arguments: Ptr(`{"q":"x"}`),
				},
			},
		},
	}).ToBifrostChatResponse()

	if chatResp == nil || len(chatResp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if chatResp.Choices[0].FinishReason == nil {
		t.Fatal("expected finish_reason to be set")
	}
	if got := *chatResp.Choices[0].FinishReason; got != string(BifrostFinishReasonToolCalls) {
		t.Fatalf("expected finish_reason %q, got %q", BifrostFinishReasonToolCalls, got)
	}
}

func TestToBifrostChatResponse_MapsIncompleteDetails(t *testing.T) {
	cases := map[string]string{
		ResponsesResponseIncompleteReasonMaxOutputTokens: string(BifrostFinishReasonLength),
		ResponsesResponseIncompleteReasonContentFilter:   "content_filter",
	}

	for reason, expected := range cases {
		t.Run(reason, func(t *testing.T) {
			chatResp := (&BifrostResponsesResponse{
				Status:            Ptr(ResponsesResponseStatusIncomplete),
				IncompleteDetails: &ResponsesResponseIncompleteDetails{Reason: reason},
				Output:            responsesTextOutput("partial"),
			}).ToBifrostChatResponse()

			if chatResp == nil || len(chatResp.Choices) == 0 {
				t.Fatal("expected at least one choice")
			}
			if chatResp.Choices[0].FinishReason == nil {
				t.Fatal("expected finish_reason to be set")
			}
			if got := *chatResp.Choices[0].FinishReason; got != expected {
				t.Fatalf("expected finish_reason %q, got %q", expected, got)
			}
		})
	}
}

func TestToBifrostChatResponse_PrefersExplicitStopReason(t *testing.T) {
	// A recognized stop_reason wins over the status-derived reason.
	chatResp := (&BifrostResponsesResponse{
		Status:     Ptr(ResponsesResponseStatusCompleted),
		StopReason: Ptr("guardrail_intervened"),
		Output:     responsesTextOutput("blocked"),
	}).ToBifrostChatResponse()

	if chatResp == nil || len(chatResp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if chatResp.Choices[0].FinishReason == nil || *chatResp.Choices[0].FinishReason != "guardrail_intervened" {
		t.Fatalf("expected finish_reason %q, got %v", "guardrail_intervened", chatResp.Choices[0].FinishReason)
	}
}

func TestToBifrostChatResponse_LeavesFinishReasonUnsetForNonTerminalState(t *testing.T) {
	for _, status := range []string{ResponsesResponseStatusInProgress, ResponsesResponseStatusQueued} {
		t.Run(status, func(t *testing.T) {
			// The stop_reason is deliberately set and recognized: an in-flight status
			// must outrank it, otherwise this test passes for the wrong reason.
			chatResp := (&BifrostResponsesResponse{
				Status:     Ptr(status),
				StopReason: Ptr(string(BifrostFinishReasonStop)),
				Output:     responsesTextOutput("partial"),
			}).ToBifrostChatResponse()

			if chatResp == nil || len(chatResp.Choices) == 0 {
				t.Fatal("expected at least one choice")
			}
			if chatResp.Choices[0].FinishReason != nil {
				t.Fatalf("expected finish_reason to be nil, got %q", *chatResp.Choices[0].FinishReason)
			}
		})
	}
}

func TestResponsesStreamToBifrostChatResponse_MapsIncompleteContentFilter(t *testing.T) {
	chunk := &BifrostResponsesStreamResponse{
		Type: ResponsesStreamResponseTypeIncomplete,
		Response: &BifrostResponsesResponse{
			Status:            Ptr(ResponsesResponseStatusIncomplete),
			IncompleteDetails: &ResponsesResponseIncompleteDetails{Reason: ResponsesResponseIncompleteReasonContentFilter},
		},
	}

	resp := chunk.ToBifrostChatResponse()
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if resp.Choices[0].FinishReason == nil || *resp.Choices[0].FinishReason != "content_filter" {
		t.Fatalf("expected finish_reason %q, got %v", "content_filter", resp.Choices[0].FinishReason)
	}
}

// Cohere never sets Status and Gemini sets it only on error, so a nil or "failed"
// status must still resolve through stop_reason rather than falling through to unset.
func TestToBifrostChatResponse_MapsStopReasonWithoutTerminalStatus(t *testing.T) {
	cases := []struct {
		name       string
		status     *string
		stopReason string
		expected   string
	}{
		{"nil status (cohere)", nil, string(BifrostFinishReasonStop), string(BifrostFinishReasonStop)},
		{"nil status tool calls", nil, string(BifrostFinishReasonToolCalls), string(BifrostFinishReasonToolCalls)},
		{"failed status (gemini safety)", Ptr(ResponsesResponseStatusFailed), "content_filter", "content_filter"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chatResp := (&BifrostResponsesResponse{
				Status:     tc.status,
				StopReason: Ptr(tc.stopReason),
				Output:     responsesTextOutput("hi"),
			}).ToBifrostChatResponse()

			if chatResp == nil || len(chatResp.Choices) == 0 {
				t.Fatal("expected at least one choice")
			}
			if chatResp.Choices[0].FinishReason == nil {
				t.Fatal("expected finish_reason to be set")
			}
			if got := *chatResp.Choices[0].FinishReason; got != tc.expected {
				t.Fatalf("expected finish_reason %q, got %q", tc.expected, got)
			}
		})
	}
}

// The Responses schema requires reasoning.summary to be an array. A nil slice marshals
// to null, which upstreams reject with "Invalid 'input': value did not match any
// expected variant" — breaking multi-turn tool loops that replay encrypted reasoning.
func TestToResponsesMessages_EncryptedReasoningMarshalsSummaryAsArray(t *testing.T) {
	encrypted := "rsn_abc123"
	cm := &ChatMessage{
		Role: ChatMessageRoleAssistant,
		ChatAssistantMessage: &ChatAssistantMessage{
			ReasoningDetails: []ChatReasoningDetails{
				{Index: 0, Type: BifrostReasoningDetailsTypeEncrypted, Data: &encrypted},
			},
		},
	}

	out := cm.ToResponsesMessages()
	if len(out) == 0 {
		t.Fatal("expected a reasoning message")
	}
	if out[0].Type == nil || *out[0].Type != ResponsesMessageTypeReasoning {
		t.Fatalf("expected a reasoning item, got %#v", out[0].Type)
	}
	if out[0].ResponsesReasoning == nil {
		t.Fatal("expected reasoning payload to be set")
	}
	if out[0].ResponsesReasoning.Summary == nil {
		t.Fatal("summary must be a non-nil slice so it marshals as [] rather than null")
	}

	encoded, err := json.Marshal(out[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"summary":[]`) {
		t.Fatalf(`expected "summary":[] in %s`, encoded)
	}
	if strings.Contains(string(encoded), `"summary":null`) {
		t.Fatalf("summary must never marshal as null: %s", encoded)
	}
}

// Every response family carrying a Usage must be reachable through
// NormalizedUsage, or be listed as a known exclusion. DecisionResponse was added
// to the logging plugin's switch but not here, so decision rows silently lost
// their token counts.
func TestNormalizedUsageCoversEveryUsageBearingFamily(t *testing.T) {
	handled := map[string]bool{}
	for _, name := range normalizedUsageFamilies {
		handled[name] = true
	}
	// Pre-existing gaps, present in the hand-rolled switch this replaced too.
	// Streams are accumulated into their non-stream family before they reach a
	// logging or span path; video has never been wired to either.
	knownExcluded := map[string]string{
		"SpeechStreamResponse":          "accumulated into SpeechResponse",
		"TranscriptionStreamResponse":   "accumulated into TranscriptionResponse",
		"ImageGenerationStreamResponse": "accumulated into ImageGenerationResponse",
		"VideoGenerationResponse":       "never wired to logging or spans",
	}

	rt := reflect.TypeOf(BifrostResponse{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !strings.HasSuffix(f.Name, "Response") || f.Type.Kind() != reflect.Ptr {
			continue
		}
		inner := f.Type.Elem()
		if inner.Kind() != reflect.Struct {
			continue
		}
		if _, hasUsage := inner.FieldByName("Usage"); !hasUsage {
			continue
		}
		if handled[f.Name] {
			if _, excluded := knownExcluded[f.Name]; excluded {
				t.Errorf("%s is both handled and listed as excluded", f.Name)
			}
			continue
		}
		if _, excluded := knownExcluded[f.Name]; !excluded {
			t.Errorf("%s carries a Usage field but NormalizedUsage does not read it; "+
				"a request of that family would report zero tokens", f.Name)
		}
	}
	// A stale exclusion is drift too.
	for name := range knownExcluded {
		if _, ok := rt.FieldByName(name); !ok {
			t.Errorf("knownExcluded lists %s, which is no longer a BifrostResponse field", name)
		}
	}
}

// normalizedUsageFamilies lists what NormalizedUsage reads.
var normalizedUsageFamilies = []string{
	"TextCompletionResponse", "ChatResponse", "ResponsesResponse", "CompactionResponse",
	"EmbeddingResponse", "RerankResponse", "DecisionResponse", "TranscriptionResponse",
	"SpeechResponse", "ImageGenerationResponse", "PassthroughResponse",
}

// A provider may report total_tokens: 0 alongside real input/output counts.
// Speech derived the total in that case; transcription only did so when the
// pointer was nil, so it returned zero. Both now agree.
func TestNormalizedUsageDerivesTotalWhenReportedZero(t *testing.T) {
	i64 := func(v int) *int { return &v }

	for _, tc := range []struct {
		name      string
		resp      *BifrostResponse
		wantTotal int
	}{
		{
			"transcription, total reported zero",
			&BifrostResponse{TranscriptionResponse: &BifrostTranscriptionResponse{
				Usage: &TranscriptionUsage{InputTokens: i64(7), OutputTokens: i64(3), TotalTokens: i64(0)},
			}},
			10,
		},
		{
			"transcription, total nil",
			&BifrostResponse{TranscriptionResponse: &BifrostTranscriptionResponse{
				Usage: &TranscriptionUsage{InputTokens: i64(7), OutputTokens: i64(3)},
			}},
			10,
		},
		{
			"transcription, total reported and correct",
			&BifrostResponse{TranscriptionResponse: &BifrostTranscriptionResponse{
				Usage: &TranscriptionUsage{InputTokens: i64(7), OutputTokens: i64(3), TotalTokens: i64(10)},
			}},
			10,
		},
		{
			"transcription, provider total wins when non-zero",
			&BifrostResponse{TranscriptionResponse: &BifrostTranscriptionResponse{
				Usage: &TranscriptionUsage{InputTokens: i64(7), OutputTokens: i64(3), TotalTokens: i64(99)},
			}},
			99,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := tc.resp.NormalizedUsage()
			if u == nil {
				t.Fatal("NormalizedUsage returned nil")
			}
			if u.TotalTokens != tc.wantTotal {
				t.Errorf("TotalTokens = %d, want %d", u.TotalTokens, tc.wantTotal)
			}
		})
	}
}
