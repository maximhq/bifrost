package schemas

// INTELLIGENT MULTIPLEXER (MUX) IMPLEMENTATION
//
// This file contains intelligent multiplexer methods that provide bidirectional
// synchronization between normal/standard fields and OpenAI Responses API fields.
//
// The mux logic follows this pattern:
// - When usingResponsesAPI = true:  Normal fields → Responses API fields (only if Responses API fields are nil)
// - When usingResponsesAPI = false: Responses API fields → Normal fields (only if Normal fields are nil)
//
// Key Benefits:
// 1. Conditional Transfer: Only transfers when destination is empty (nil pointers)
// 2. Recursive Handling: Automatically handles nested structures
// 3. Initialization: Creates embedded structs when needed
// 4. Conflict Avoidance: Skips transfer if both sides have data
//
// Usage Example:
//   tool := &Tool{Type: &[]string{"function"}[0], Function: &Function{Name: "test"}}
//   tool.Mux(true)  // Transfers to Responses API format
//   tool.Mux(false) // Transfers back to normal format

// Mux intelligently synchronizes data between normal fields and Responses API fields
// If usingResponsesAPI is true: transfers normal fields → Responses API fields (only if Responses API fields are nil)
// If usingResponsesAPI is false: transfers Responses API fields → normal fields (only if normal fields are nil)
func (t *Tool) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		if t.ResponsesAPIExtendedTool == nil {
			t.ResponsesAPIExtendedTool = &ResponsesAPIExtendedTool{}
		}
		// Map ChatCompletions tool → Responses tool
		if t.ChatCompletionsExtendedTool != nil && t.ChatCompletionsExtendedTool.Function != nil {
			fn := t.ChatCompletionsExtendedTool.Function
			if t.ResponsesAPIExtendedTool.Name == nil {
				name := fn.Name
				t.ResponsesAPIExtendedTool.Name = &name
			}
			if t.ResponsesAPIExtendedTool.ToolFunction == nil && fn.ToolFunction != nil {
				t.ResponsesAPIExtendedTool.ToolFunction = &ToolFunction{
					Parameters: fn.ToolFunction.Parameters,
					Strict:     fn.ToolFunction.Strict,
				}
			}
		}
	} else {
		// Responses → ChatCompletions
		if t.ChatCompletionsExtendedTool == nil {
			t.ChatCompletionsExtendedTool = &ChatCompletionsExtendedTool{}
		}
		if t.ResponsesAPIExtendedTool != nil && t.ResponsesAPIExtendedTool.ToolFunction != nil {
			if t.ChatCompletionsExtendedTool.Function == nil {
				t.ChatCompletionsExtendedTool.Function = &ChatCompletionsFunction{
					Name: *t.ResponsesAPIExtendedTool.Name,
					ToolFunction: &ToolFunction{
						Parameters: t.ResponsesAPIExtendedTool.ToolFunction.Parameters,
						Strict:     t.ResponsesAPIExtendedTool.ToolFunction.Strict,
					},
				}
			}
		}
	}
}

// Mux intelligently synchronizes data between normal fields and Responses API fields
func (tc *ToolChoiceStruct) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		if tc.ResponsesAPIExtendedToolChoice == nil {
			tc.ResponsesAPIExtendedToolChoice = &ResponsesAPIExtendedToolChoice{}
		}
		if tc.Type != nil {
			switch *tc.Type {
			case ToolChoiceTypeNone, ToolChoiceTypeAuto, ToolChoiceTypeRequired:
				if tc.ResponsesAPIExtendedToolChoice.Mode == nil {
					modeStr := string(*tc.Type)
					tc.ResponsesAPIExtendedToolChoice.Mode = &modeStr
				}
			case ToolChoiceTypeFunction:
				if tc.ChatCompletionsExtendedToolChoice != nil && tc.ChatCompletionsExtendedToolChoice.Name != nil && tc.ResponsesAPIExtendedToolChoice.Name == nil {
					tc.ResponsesAPIExtendedToolChoice.Name = tc.ChatCompletionsExtendedToolChoice.Name
				}
			}
		}
	} else {
		if tc.Type == nil && tc.ResponsesAPIExtendedToolChoice != nil {
			if tc.ResponsesAPIExtendedToolChoice.Mode != nil {
				switch *tc.ResponsesAPIExtendedToolChoice.Mode {
				case "none":
					tc.Type = &[]ToolChoiceType{ToolChoiceTypeNone}[0]
				case "auto":
					tc.Type = &[]ToolChoiceType{ToolChoiceTypeAuto}[0]
				case "required":
					tc.Type = &[]ToolChoiceType{ToolChoiceTypeRequired}[0]
				}
			} else if tc.ResponsesAPIExtendedToolChoice.Name != nil {
				tc.Type = &[]ToolChoiceType{ToolChoiceTypeFunction}[0]
				if tc.ChatCompletionsExtendedToolChoice == nil {
					tc.ChatCompletionsExtendedToolChoice = &ChatCompletionsExtendedToolChoice{}
				}
				if tc.ChatCompletionsExtendedToolChoice.Name == nil {
					tc.ChatCompletionsExtendedToolChoice.Name = tc.ResponsesAPIExtendedToolChoice.Name
				}
			}
		}
	}
}

// MuxMessageContent handles message content synchronization
func (mc *MessageContent) Mux(usingResponsesAPI bool) {
	// Handle content blocks if they contain embedded Responses API structures
	if mc.ContentBlocks != nil {
		for i := range *mc.ContentBlocks {
			(*mc.ContentBlocks)[i].Mux(usingResponsesAPI)
		}
	}
}

// MuxContentBlocks handles content block synchronization for messages
func (cb *ContentBlock) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		if cb.ChatCompletionsExtendedContentBlock != nil && cb.ChatCompletionsExtendedContentBlock.InputImage != nil {
			if cb.ResponsesAPIExtendedContentBlock == nil {
				cb.ResponsesAPIExtendedContentBlock = &ResponsesAPIExtendedContentBlock{}
			}
			url := cb.ChatCompletionsExtendedContentBlock.InputImage.URL
			cb.ResponsesAPIExtendedContentBlock.InputMessageContentBlockImage = &InputMessageContentBlockImage{
				ImageURL: &url,
				Detail:   cb.ChatCompletionsExtendedContentBlock.InputImage.Detail,
			}
		}
		if cb.ChatCompletionsExtendedContentBlock != nil && cb.ChatCompletionsExtendedContentBlock.InputFile != nil {
			if cb.ResponsesAPIExtendedContentBlock == nil {
				cb.ResponsesAPIExtendedContentBlock = &ResponsesAPIExtendedContentBlock{}
			}
			cb.ResponsesAPIExtendedContentBlock.InputMessageContentBlockFile = &InputMessageContentBlockFile{
				FileData: cb.ChatCompletionsExtendedContentBlock.InputFile.FileData,
				FileID:   cb.ChatCompletionsExtendedContentBlock.InputFile.FileID,
				Filename: cb.ChatCompletionsExtendedContentBlock.InputFile.Filename,
			}
		}
	} else {
		if cb.ResponsesAPIExtendedContentBlock != nil && cb.ResponsesAPIExtendedContentBlock.InputMessageContentBlockImage != nil {
			if cb.ChatCompletionsExtendedContentBlock == nil {
				cb.ChatCompletionsExtendedContentBlock = &ChatCompletionsExtendedContentBlock{}
			}
			cb.ChatCompletionsExtendedContentBlock.InputImage = &InputImage{}
			if cb.ResponsesAPIExtendedContentBlock.InputMessageContentBlockImage.ImageURL != nil {
				cb.ChatCompletionsExtendedContentBlock.InputImage.URL = *cb.ResponsesAPIExtendedContentBlock.InputMessageContentBlockImage.ImageURL
			}
			cb.ChatCompletionsExtendedContentBlock.InputImage.Detail = cb.ResponsesAPIExtendedContentBlock.InputMessageContentBlockImage.Detail
		}
		if cb.ResponsesAPIExtendedContentBlock != nil && cb.ResponsesAPIExtendedContentBlock.InputMessageContentBlockFile != nil {
			if cb.ChatCompletionsExtendedContentBlock == nil {
				cb.ChatCompletionsExtendedContentBlock = &ChatCompletionsExtendedContentBlock{}
			}
			cb.ChatCompletionsExtendedContentBlock.InputFile = &InputFile{
				FileData: cb.ResponsesAPIExtendedContentBlock.InputMessageContentBlockFile.FileData,
				FileID:   cb.ResponsesAPIExtendedContentBlock.InputMessageContentBlockFile.FileID,
				Filename: cb.ResponsesAPIExtendedContentBlock.InputMessageContentBlockFile.Filename,
			}
		}
	}
}

// Mux intelligently synchronizes data between normal fields and Responses API fields
func (tm *ToolMessage) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		if tm.ResponsesAPIToolMessage == nil {
			tm.ResponsesAPIToolMessage = &ResponsesAPIToolMessage{}
		}
		if tm.ChatCompletionsToolMessage != nil && tm.ChatCompletionsToolMessage.ToolCallID != nil && tm.ResponsesAPIToolMessage.CallID == nil {
			tm.ResponsesAPIToolMessage.CallID = tm.ChatCompletionsToolMessage.ToolCallID
		}
	} else {
		if tm.ChatCompletionsToolMessage == nil {
			tm.ChatCompletionsToolMessage = &ChatCompletionsToolMessage{}
		}
		if tm.ChatCompletionsToolMessage.ToolCallID == nil && tm.ResponsesAPIToolMessage != nil && tm.ResponsesAPIToolMessage.CallID != nil {
			tm.ChatCompletionsToolMessage.ToolCallID = tm.ResponsesAPIToolMessage.CallID
		}
	}
}

// Mux intelligently synchronizes data between normal fields and Responses API fields
func (am *AssistantMessage) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		// Convert Chat Completion Assistant Message to Responses API Output Message

		if am.ResponsesAPIExtendedAssistantMessage == nil {
			am.ResponsesAPIExtendedAssistantMessage = &ResponsesAPIExtendedAssistantMessage{}
		}

		// If there is exactly one tool call, mirror its arguments into FunctionToolCall
		if am.ToolCalls != nil && len(*am.ToolCalls) == 1 {
			tc := (*am.ToolCalls)[0]
			if am.ResponsesAPIExtendedAssistantMessage.FunctionToolCall == nil {
				am.ResponsesAPIExtendedAssistantMessage.FunctionToolCall = &FunctionToolCall{
					Arguments: tc.Function.Arguments,
				}
			}
		}
	} else {
		// Transfer Responses API fields to normal fields
		// If FunctionToolCall exists and we don't yet have ToolCalls, synthesize a single tool call
		if am.ToolCalls == nil && am.ResponsesAPIExtendedAssistantMessage != nil && am.ResponsesAPIExtendedAssistantMessage.FunctionToolCall != nil {
			toolType := "function"
			calls := []ToolCall{
				{
					Type: &toolType,
					// ID and Name will be filled by the paired tool message if present
					Function: FunctionCall{
						Arguments: am.ResponsesAPIExtendedAssistantMessage.FunctionToolCall.Arguments,
					},
				},
			}
			am.ToolCalls = &calls
		}
	}

	// Recursively handle annotations
	for i := range am.Annotations {
		am.Annotations[i].Mux(usingResponsesAPI)
	}
}

// Mux intelligently synchronizes data between normal fields and Responses API fields
func (a *Annotation) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		// Initialize ResponsesAPIExtendedOutputMessageTextAnnotation if nil
		if a.ResponsesAPIExtendedOutputMessageTextAnnotation == nil {
			a.ResponsesAPIExtendedOutputMessageTextAnnotation = &ResponsesAPIExtendedOutputMessageTextAnnotation{}
		}

		// Transfer citation data to Responses API format
		if a.ResponsesAPIExtendedOutputMessageTextAnnotation.StartIndex == nil {
			a.ResponsesAPIExtendedOutputMessageTextAnnotation.StartIndex = &a.Citation.StartIndex
		}
		if a.ResponsesAPIExtendedOutputMessageTextAnnotation.EndIndex == nil {
			a.ResponsesAPIExtendedOutputMessageTextAnnotation.EndIndex = &a.Citation.EndIndex
		}

		// Handle different citation types
		switch a.Type {
		case "url_citation":
			if a.ResponsesAPIExtendedOutputMessageTextAnnotation.OutputMessageTextAnnotationURLCitation == nil {
				a.ResponsesAPIExtendedOutputMessageTextAnnotation.OutputMessageTextAnnotationURLCitation = &OutputMessageTextAnnotationURLCitation{
					Title: a.Citation.Title,
				}
				if a.Citation.URL != nil {
					a.ResponsesAPIExtendedOutputMessageTextAnnotation.OutputMessageTextAnnotationURLCitation.URL = *a.Citation.URL
				}
			}
		}
	} else {
		// Transfer Responses API fields to normal fields
		if a.ResponsesAPIExtendedOutputMessageTextAnnotation != nil {
			// Extract citation information from Responses API format
			if a.Citation.StartIndex == 0 && a.ResponsesAPIExtendedOutputMessageTextAnnotation.StartIndex != nil {
				a.Citation.StartIndex = *a.ResponsesAPIExtendedOutputMessageTextAnnotation.StartIndex
			}
			if a.Citation.EndIndex == 0 && a.ResponsesAPIExtendedOutputMessageTextAnnotation.EndIndex != nil {
				a.Citation.EndIndex = *a.ResponsesAPIExtendedOutputMessageTextAnnotation.EndIndex
			}

			// Handle URL citation
			if a.ResponsesAPIExtendedOutputMessageTextAnnotation.OutputMessageTextAnnotationURLCitation != nil {
				urlCitation := a.ResponsesAPIExtendedOutputMessageTextAnnotation.OutputMessageTextAnnotationURLCitation
				if a.Citation.Title == "" {
					a.Citation.Title = urlCitation.Title
				}
				if a.Citation.URL == nil {
					a.Citation.URL = &urlCitation.URL
				}
				if a.Type == "" {
					a.Type = "url_citation"
				}
			}
		}
	}
}

// Mux intelligently synchronizes data between normal fields and Responses API fields
func (brc *BifrostResponseChoice) Mux(usingResponsesAPI bool) {
	// Handle both stream and non-stream response choices
	if brc.BifrostNonStreamResponseChoice != nil {
		brc.BifrostNonStreamResponseChoice.Message.Mux(usingResponsesAPI)
	}
	// Stream choices would be handled similarly if needed
}

// User Facing Fields

func (p *ModelParameters) Mux(usingResponsesAPI bool) {
}

// Mux intelligently synchronizes data between normal fields and Responses API fields
func (bm *BifrostMessage) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		// Convert chat completion message to Responses API message

		if bm.ResponsesAPIExtendedBifrostMessage == nil {
			bm.ResponsesAPIExtendedBifrostMessage = &ResponsesAPIExtendedBifrostMessage{}
		}

		messageType := "message"

		if bm.AssistantMessage != nil {
			if bm.ResponsesAPIExtendedAssistantMessage == nil {
				bm.ResponsesAPIExtendedAssistantMessage = &ResponsesAPIExtendedAssistantMessage{}
			}

			// Transfer simple text content from chat-completions content → responses assistant text
			if bm.Content.ContentStr != nil && *bm.Content.ContentStr != "" && bm.ResponsesAPIExtendedAssistantMessage.Text == "" {
				bm.ResponsesAPIExtendedAssistantMessage.Text = *bm.Content.ContentStr
			}

			if bm.AssistantMessage.Refusal != nil {
				messageType = "refusal"
			}

			// Thought field removed in segregated schemas

			if bm.AssistantMessage.ToolCalls != nil {
				messageType = "function_call"
			}
		}
		if bm.ToolMessage != nil {
			if bm.ResponsesAPIToolMessage == nil {
				bm.ResponsesAPIToolMessage = &ResponsesAPIToolMessage{}
			}
			messageType = "function_call_output"

			// Map ToolCallID → CallID
			if bm.ChatCompletionsToolMessage != nil && bm.ChatCompletionsToolMessage.ToolCallID != nil && bm.ResponsesAPIToolMessage.CallID == nil {
				bm.ResponsesAPIToolMessage.CallID = bm.ChatCompletionsToolMessage.ToolCallID
			}

			// If tool output content exists on chat-completions side, mirror into responses function_call_output
			if bm.Content.ContentStr != nil && *bm.Content.ContentStr != "" {
				if bm.ResponsesAPIToolMessage.FunctionToolCallOutput == nil {
					bm.ResponsesAPIToolMessage.FunctionToolCallOutput = &FunctionToolCallOutput{}
				}
				if bm.ResponsesAPIToolMessage.FunctionToolCallOutput.Output == "" {
					bm.ResponsesAPIToolMessage.FunctionToolCallOutput.Output = *bm.Content.ContentStr
				}
			}
		}

		bm.ResponsesAPIExtendedBifrostMessage.Type = &messageType
	}

	bm.Content.Mux(usingResponsesAPI)

	if bm.AssistantMessage != nil {
		bm.AssistantMessage.Mux(usingResponsesAPI)
	}

	if bm.ToolMessage != nil {
		bm.ToolMessage.Mux(usingResponsesAPI)
	}

	if !usingResponsesAPI {
		// Responses → Chat completions mapping
		if bm.AssistantMessage != nil && bm.ResponsesAPIExtendedAssistantMessage != nil {
			if (bm.Content.ContentStr == nil || *bm.Content.ContentStr == "") && bm.ResponsesAPIExtendedAssistantMessage.Text != "" {
				text := bm.ResponsesAPIExtendedAssistantMessage.Text
				bm.Content.ContentStr = &text
			}
		}
		if bm.ToolMessage != nil && bm.ResponsesAPIToolMessage != nil && bm.ResponsesAPIToolMessage.FunctionToolCallOutput != nil {
			out := bm.ResponsesAPIToolMessage.FunctionToolCallOutput.Output
			if out != "" && bm.Content.ContentStr == nil {
				bm.Content.ContentStr = &out
			}
		}
		// Map CallID → ToolCallID
		if bm.ChatCompletionsToolMessage != nil && bm.ChatCompletionsToolMessage.ToolCallID == nil && bm.ResponsesAPIToolMessage != nil && bm.ResponsesAPIToolMessage.CallID != nil {
			bm.ChatCompletionsToolMessage.ToolCallID = bm.ResponsesAPIToolMessage.CallID
		}
	}
}

// Mux intelligently synchronizes data between normal fields and Responses API fields
func (br *BifrostResponse) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		// Initialize ResponseAPIExtendedResponse if nil
		if br.ResponseAPIExtendedResponse == nil {
			br.ResponseAPIExtendedResponse = &ResponseAPIExtendedResponse{}
		}

		// Map params (ExtraFields.Params → Responses request params)
		if br.ResponseAPIExtendedResponse.ResponsesAPIExtendedRequestParams == nil {
			br.ResponseAPIExtendedResponse.ResponsesAPIExtendedRequestParams = &ResponsesAPIExtendedRequestParams{}
		}
		p := &br.ExtraFields.Params
		if p.Temperature != nil && br.ResponseAPIExtendedResponse.Temperature == nil {
			br.ResponseAPIExtendedResponse.Temperature = p.Temperature
		}
		if p.TopP != nil && br.ResponseAPIExtendedResponse.TopP == nil {
			br.ResponseAPIExtendedResponse.TopP = p.TopP
		}
		if p.TopLogProbs != nil && br.ResponseAPIExtendedResponse.TopLogProbs == nil {
			br.ResponseAPIExtendedResponse.TopLogProbs = p.TopLogProbs
		}
		if p.ParallelToolCalls != nil && br.ResponseAPIExtendedResponse.ParallelToolCalls == nil {
			br.ResponseAPIExtendedResponse.ParallelToolCalls = p.ParallelToolCalls
		}
		if p.MaxTokens != nil && br.ResponseAPIExtendedResponse.MaxOutputTokens == nil {
			// Map chat max_tokens → responses max_output_tokens
			br.ResponseAPIExtendedResponse.MaxOutputTokens = p.MaxTokens
		}
		if p.StreamOptions != nil {
			if br.ResponseAPIExtendedResponse.StreamOptions == nil {
				br.ResponseAPIExtendedResponse.StreamOptions = &StreamOptions{}
			}
			if p.StreamOptions.IncludeObfuscation != nil && br.ResponseAPIExtendedResponse.StreamOptions.IncludeObfuscation == nil {
				br.ResponseAPIExtendedResponse.StreamOptions.IncludeObfuscation = p.StreamOptions.IncludeObfuscation
			}
		}

		// Map usage (chat completions → responses)
		if br.Usage != nil {
			if br.Usage.ResponsesAPIExtendedResponseUsage == nil {
				br.Usage.ResponsesAPIExtendedResponseUsage = &ResponsesAPIExtendedResponseUsage{}
			}
			// If chat completions usage present, mirror to responses usage fields
			if br.Usage.ChatCompletionsExtendedUsage != nil {
				cu := br.Usage.ChatCompletionsExtendedUsage
				if br.Usage.PromptTokens > 0 && br.Usage.ResponsesAPIExtendedResponseUsage.InputTokens == 0 {
					br.Usage.ResponsesAPIExtendedResponseUsage.InputTokens = br.Usage.PromptTokens
				}
				if br.Usage.CompletionTokens > 0 && br.Usage.ResponsesAPIExtendedResponseUsage.OutputTokens == 0 {
					br.Usage.ResponsesAPIExtendedResponseUsage.OutputTokens = br.Usage.CompletionTokens
				}
				if cu.TokenDetails != nil {
					if br.Usage.ResponsesAPIExtendedResponseUsage.InputTokensDetails == nil {
						br.Usage.ResponsesAPIExtendedResponseUsage.InputTokensDetails = &ResponsesAPIResponseInputTokens{}
					}
					if br.Usage.ResponsesAPIExtendedResponseUsage.InputTokensDetails.CachedTokens == 0 && cu.TokenDetails.CachedTokens > 0 {
						br.Usage.ResponsesAPIExtendedResponseUsage.InputTokensDetails.CachedTokens = cu.TokenDetails.CachedTokens
					}
				}
				if cu.CompletionTokensDetails != nil {
					if br.Usage.ResponsesAPIExtendedResponseUsage.OutputTokensDetails == nil {
						br.Usage.ResponsesAPIExtendedResponseUsage.OutputTokensDetails = &ResponsesAPIResponseOutputTokens{}
					}
					if br.Usage.ResponsesAPIExtendedResponseUsage.OutputTokensDetails.ReasoningTokens == 0 && cu.CompletionTokensDetails.ReasoningTokens > 0 {
						br.Usage.ResponsesAPIExtendedResponseUsage.OutputTokensDetails.ReasoningTokens = cu.CompletionTokensDetails.ReasoningTokens
					}
				}
			}
			// Maintain total tokens if available on chat side
			if br.Usage.TotalTokens == 0 && br.Usage.ChatCompletionsExtendedUsage != nil {
				br.Usage.TotalTokens = br.Usage.PromptTokens + br.Usage.CompletionTokens
			}
		}

	} else {
		// Responses → Chat Completions normalization: choices/messages handled elsewhere; keep usage/params

		// Map params (responses request params → ExtraFields.Params)
		if br.ResponseAPIExtendedResponse != nil && br.ResponseAPIExtendedResponse.ResponsesAPIExtendedRequestParams != nil {
			rp := br.ResponseAPIExtendedResponse.ResponsesAPIExtendedRequestParams
			// Only fill if target fields are nil to avoid clobbering
			if br.ExtraFields.Params.Temperature == nil && rp.Temperature != nil {
				br.ExtraFields.Params.Temperature = rp.Temperature
			}
			if br.ExtraFields.Params.TopP == nil && rp.TopP != nil {
				br.ExtraFields.Params.TopP = rp.TopP
			}
			if br.ExtraFields.Params.TopLogProbs == nil && rp.TopLogProbs != nil {
				br.ExtraFields.Params.TopLogProbs = rp.TopLogProbs
			}
			if br.ExtraFields.Params.ParallelToolCalls == nil && rp.ParallelToolCalls != nil {
				br.ExtraFields.Params.ParallelToolCalls = rp.ParallelToolCalls
			}
			if br.ExtraFields.Params.MaxTokens == nil && rp.MaxOutputTokens != nil {
				br.ExtraFields.Params.MaxTokens = rp.MaxOutputTokens
			}
			if rp.StreamOptions != nil {
				if br.ExtraFields.Params.StreamOptions == nil {
					br.ExtraFields.Params.StreamOptions = &StreamOptions{}
				}
				if br.ExtraFields.Params.StreamOptions.IncludeObfuscation == nil && rp.StreamOptions.IncludeObfuscation != nil {
					br.ExtraFields.Params.StreamOptions.IncludeObfuscation = rp.StreamOptions.IncludeObfuscation
				}
			}
		}

		// Map usage (responses → chat completions)
		if br.Usage != nil && br.Usage.ResponsesAPIExtendedResponseUsage != nil {
			ru := br.Usage.ResponsesAPIExtendedResponseUsage
			if br.Usage.ChatCompletionsExtendedUsage == nil {
				br.Usage.ChatCompletionsExtendedUsage = &ChatCompletionsExtendedUsage{}
			}
			cu := br.Usage.ChatCompletionsExtendedUsage
			if br.Usage.PromptTokens == 0 && ru.InputTokens > 0 {
				br.Usage.PromptTokens = ru.InputTokens
			}
			if br.Usage.CompletionTokens == 0 && ru.OutputTokens > 0 {
				br.Usage.CompletionTokens = ru.OutputTokens
			}
			if ru.InputTokensDetails != nil {
				if cu.TokenDetails == nil {
					cu.TokenDetails = &TokenDetails{}
				}
				if cu.TokenDetails.CachedTokens == 0 && ru.InputTokensDetails.CachedTokens > 0 {
					cu.TokenDetails.CachedTokens = ru.InputTokensDetails.CachedTokens
				}
			}
			if ru.OutputTokensDetails != nil {
				if cu.CompletionTokensDetails == nil {
					cu.CompletionTokensDetails = &CompletionTokensDetails{}
				}
				if cu.CompletionTokensDetails.ReasoningTokens == 0 && ru.OutputTokensDetails.ReasoningTokens > 0 {
					cu.CompletionTokensDetails.ReasoningTokens = ru.OutputTokensDetails.ReasoningTokens
				}
			}
			if br.Usage.TotalTokens == 0 {
				br.Usage.TotalTokens = ru.InputTokens + ru.OutputTokens
			}
		}
	}

	// Recursively handle choices
	for i := range br.Choices {
		br.Choices[i].Mux(usingResponsesAPI)
	}
}

// ExpandAssistantToolCallsToResponsesItems converts a single Chat Completions-style assistant message
// (with multiple tool calls in AssistantMessage.ToolCalls) into multiple Responses API items, where
// each tool call becomes its own BifrostMessage with `Type = "function_call"` and a ToolMessage payload.
//
// Notes:
//   - This does NOT mutate the input message.
//   - Each returned message will have only ToolMessage populated (AssistantMessage will be nil),
//     to avoid embedded pointer conflicts during JSON marshalling.
//   - Function name and call_id (if present) are preserved on the item.
func expandAssistantToolCallsToResponsesItems(msg BifrostMessage) []BifrostMessage {
	if msg.AssistantMessage == nil || msg.AssistantMessage.ToolCalls == nil {
		return nil
	}

	toolCalls := *msg.AssistantMessage.ToolCalls
	if len(toolCalls) == 0 {
		return nil
	}

	items := make([]BifrostMessage, 0, len(toolCalls))
	for _, tc := range toolCalls {
		itemType := "function_call"

		var callID *string
		if tc.ID != nil && *tc.ID != "" {
			callID = tc.ID
		}

		var namePtr *string
		if tc.Function.Name != nil && *tc.Function.Name != "" {
			name := *tc.Function.Name
			namePtr = &name
		}

		// Build AssistantMessage with FunctionToolCall and a ToolMessage to carry name/call_id
		assistantMsg := &AssistantMessage{
			ResponsesAPIExtendedAssistantMessage: &ResponsesAPIExtendedAssistantMessage{
				FunctionToolCall: &FunctionToolCall{
					Arguments: tc.Function.Arguments,
				},
			},
		}

		toolMsg := &ToolMessage{
			ResponsesAPIToolMessage: &ResponsesAPIToolMessage{
				Name:   namePtr,
				CallID: callID,
			},
		}

		items = append(items, BifrostMessage{
			Role:             ModelChatMessageRoleAssistant,
			Content:          MessageContent{},
			AssistantMessage: assistantMsg,
			ToolMessage:      toolMsg,
			ResponsesAPIExtendedBifrostMessage: &ResponsesAPIExtendedBifrostMessage{
				Type: &itemType,
			},
		})
	}

	return items
}

// AggregateFunctionCallItemsToAssistant converts multiple Responses API items of type
// "function_call" into a single Chat Completions-style assistant message that aggregates
// them under AssistantMessage.ToolCalls.
//
// Only items that:
// - have ResponsesAPIExtendedBifrostMessage with Type=="function_call", and
// - have ToolMessage with a FunctionToolCall payload
// are considered. Others are ignored.
func aggregateFunctionCallItemsToAssistant(items []BifrostMessage) *BifrostMessage {
	if len(items) == 0 {
		return nil
	}

	accumulated := make([]ToolCall, 0, len(items))
	for _, it := range items {
		if it.ResponsesAPIExtendedBifrostMessage == nil || it.ResponsesAPIExtendedBifrostMessage.Type == nil {
			continue
		}
		if *it.ResponsesAPIExtendedBifrostMessage.Type != "function_call" {
			continue
		}
		if it.AssistantMessage == nil || it.AssistantMessage.ResponsesAPIExtendedAssistantMessage == nil {
			continue
		}
		ame := it.AssistantMessage.ResponsesAPIExtendedAssistantMessage
		if ame.FunctionToolCall == nil {
			continue
		}

		// Build a ToolCall entry
		toolType := "function"
		var namePtr *string
		// Name/CallID live on ToolMessage; be tolerant if missing
		var name string
		if it.ToolMessage != nil && it.ToolMessage.ResponsesAPIToolMessage != nil && it.ToolMessage.ResponsesAPIToolMessage.Name != nil && *it.ToolMessage.ResponsesAPIToolMessage.Name != "" {
			name = *it.ToolMessage.ResponsesAPIToolMessage.Name
			namePtr = &name
		}

		accumulated = append(accumulated, ToolCall{
			Type: &toolType,
			ID: func() *string {
				if it.ToolMessage != nil && it.ToolMessage.ResponsesAPIToolMessage != nil {
					return it.ToolMessage.ResponsesAPIToolMessage.CallID
				}
				return nil
			}(),
			Function: FunctionCall{
				Name:      namePtr,
				Arguments: ame.FunctionToolCall.Arguments,
			},
		})
	}

	if len(accumulated) == 0 {
		return nil
	}

	// Create a single assistant message with multiple tool_calls
	return &BifrostMessage{
		Role:    ModelChatMessageRoleAssistant,
		Content: MessageContent{},
		AssistantMessage: &AssistantMessage{
			ChatCompletionsAssistantMessage: &ChatCompletionsAssistantMessage{
				ToolCalls: &accumulated,
			},
		},
	}
}

// normalizeMessagesMux is a pipeline that converts between Chat Completions-style
// and Responses API-style lists while populating both sets of fields via Mux.
//
// If usingResponsesAPI == true:
//   - Expands any assistant message with multiple tool_calls into multiple
//     items with Type = "function_call" (one per tool).
//   - Calls Mux(true) on every resulting message to populate Responses fields.
//
// If usingResponsesAPI == false:
//   - Aggregates consecutive items with Type = "function_call" into a single
//     assistant message with tool_calls[].
//   - Calls Mux(false) on every resulting message to populate Chat Completions fields.
func NormalizeMessagesMux(usingResponsesAPI bool, messages []BifrostMessage) []BifrostMessage {
	if len(messages) == 0 {
		return messages
	}

	if usingResponsesAPI {
		// Expand assistant tool_calls to function_call items
		out := make([]BifrostMessage, 0, len(messages))
		for _, m := range messages {
			if m.AssistantMessage != nil && m.AssistantMessage.ToolCalls != nil && len(*m.AssistantMessage.ToolCalls) > 0 {
				expanded := expandAssistantToolCallsToResponsesItems(m)
				for i := range expanded {
					expanded[i].Mux(true)
					out = append(out, expanded[i])
				}
				continue
			}
			m.Mux(true)
			out = append(out, m)
		}
		return out
	}

	// Aggregate consecutive function_call items
	out := make([]BifrostMessage, 0, len(messages))
	pending := make([]BifrostMessage, 0)

	flushPending := func() {
		if len(pending) == 0 {
			return
		}
		if agg := aggregateFunctionCallItemsToAssistant(pending); agg != nil {
			agg.Mux(false)
			out = append(out, *agg)
		}
		pending = pending[:0]
	}

	for _, m := range messages {
		if m.ResponsesAPIExtendedBifrostMessage != nil && m.ResponsesAPIExtendedBifrostMessage.Type != nil && *m.ResponsesAPIExtendedBifrostMessage.Type == "function_call" {
			pending = append(pending, m)
			continue
		}
		flushPending()
		m.Mux(false)
		out = append(out, m)
	}
	flushPending()
	return out
}
