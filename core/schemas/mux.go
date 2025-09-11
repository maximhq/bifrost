package schemas

// RESPONSES API ONLY CONVERTER IMPLEMENTATION
//
// This file contains ToResponsesOnly methods that convert structures to use only
// the Responses API format by extracting data from Chat Completions fields when needed.
//
// The ToResponsesOnly logic follows this pattern:
// 1. Check if Responses API fields already exist - if yes, keep them
// 2. If not, extract/convert from Chat Completions fields
// 3. Set all Chat Completions embedded fields to nil
//
// Key Benefits:
// 1. One-way conversion: Simplifies logic by focusing only on Responses API format
// 2. Data preservation: Only converts when Responses API fields are missing
// 3. Memory optimization: Clears Chat Completions fields after conversion
// 4. Recursive handling: Automatically handles nested structures
//
// Usage Example:
//   tool := &Tool{Type: &[]string{"function"}[0], ChatCompletionsExtendedTool: &ChatCompletionsExtendedTool{...}}
//   tool.ToResponsesOnly()  // Converts to Responses API format and clears Chat Completions fields

// ToResponsesOnly converts the Tool to use only Responses API format
func (t *Tool) ToResponsesOnly() {
	// If ResponsesAPI fields already exist, keep them as is
	if t.ResponsesAPIExtendedTool != nil {
		// Clear ChatCompletions fields
		t.ChatCompletionsExtendedTool = nil
		return
	}

	// Extract from ChatCompletions if available
	if t.ChatCompletionsExtendedTool != nil && t.ChatCompletionsExtendedTool.Function != nil {
		t.ResponsesAPIExtendedTool = &ResponsesAPIExtendedTool{}
		fn := t.ChatCompletionsExtendedTool.Function

		// Extract name
		name := fn.Name
		t.ResponsesAPIExtendedTool.Name = &name

		// Extract description if available
		if fn.Description != nil {
			t.ResponsesAPIExtendedTool.Description = fn.Description
		}

		// Extract ToolFunction if available
		if fn.ToolFunction != nil {
			t.ResponsesAPIExtendedTool.ToolFunction = &ToolFunction{
				Parameters: fn.ToolFunction.Parameters,
				Strict:     fn.ToolFunction.Strict,
			}
		}
	}

	// Clear ChatCompletions fields
	t.ChatCompletionsExtendedTool = nil
}

// ToResponsesOnly converts the ToolChoiceStruct to use only Responses API format
func (tc *ToolChoiceStruct) ToResponsesOnly() {
	// If ResponsesAPI fields already exist, keep them as is
	if tc.ResponsesAPIExtendedToolChoice != nil {
		// Clear ChatCompletions fields
		tc.ChatCompletionsExtendedToolChoice = nil
		return
	}

	// Extract from Type and ChatCompletions if available
	if tc.Type != nil {
		tc.ResponsesAPIExtendedToolChoice = &ResponsesAPIExtendedToolChoice{}

		switch *tc.Type {
		case ToolChoiceTypeNone, ToolChoiceTypeAuto, ToolChoiceTypeRequired:
			modeStr := string(*tc.Type)
			tc.ResponsesAPIExtendedToolChoice.Mode = &modeStr

		case ToolChoiceTypeFunction:
			// Extract name from ChatCompletions if available
			if tc.ChatCompletionsExtendedToolChoice != nil && tc.ChatCompletionsExtendedToolChoice.Name != nil {
				tc.ResponsesAPIExtendedToolChoice.Name = tc.ChatCompletionsExtendedToolChoice.Name
			}

		case ToolChoiceTypeAllowedTools:
			// Handle allowed tools - extract from ChatCompletions if available
			if tc.ChatCompletionsExtendedToolChoice != nil && tc.ChatCompletionsExtendedToolChoice.AllowedTools != nil {
				at := tc.ChatCompletionsExtendedToolChoice.AllowedTools
				tools := make([]ToolChoiceAllowedToolDef, len(at.Tools))
				for i, tool := range at.Tools {
					tools[i] = ToolChoiceAllowedToolDef{
						Type: tool.Type,
						Name: &tool.Function.Name,
					}
				}
				tc.ResponsesAPIExtendedToolChoice.ToolChoiceAllowedTools = &ToolChoiceAllowedTools{
					Mode:  at.Mode,
					Tools: tools,
				}
			}

		default:
			// For other types (file_search, web_search_preview, etc.), create appropriate structs
			switch *tc.Type {
			case ToolChoiceTypeFileSearch:
				tc.ResponsesAPIExtendedToolChoice.ToolChoiceHostedTool = &ToolChoiceHostedTool{}
			case ToolChoiceTypeWebSearchPreview:
				tc.ResponsesAPIExtendedToolChoice.ToolChoiceHostedTool = &ToolChoiceHostedTool{}
			case ToolChoiceTypeComputerUsePreview:
				tc.ResponsesAPIExtendedToolChoice.ToolChoiceHostedTool = &ToolChoiceHostedTool{}
			case ToolChoiceTypeCodeInterpreter:
				tc.ResponsesAPIExtendedToolChoice.ToolChoiceHostedTool = &ToolChoiceHostedTool{}
			case ToolChoiceTypeImageGeneration:
				tc.ResponsesAPIExtendedToolChoice.ToolChoiceHostedTool = &ToolChoiceHostedTool{}
			case ToolChoiceTypeMCP:
				tc.ResponsesAPIExtendedToolChoice.ToolChoiceMCPTool = &ToolChoiceMCPTool{}
			case ToolChoiceTypeCustom:
				tc.ResponsesAPIExtendedToolChoice.ToolChoiceCustomTool = &ToolChoiceCustomTool{}
			}
		}
	}

	// Clear ChatCompletions fields
	tc.ChatCompletionsExtendedToolChoice = nil
}

// ToResponsesOnly handles message content conversion to Responses API format
func (mc *ChatMessageContent) ToResponsesOnly() {
	// Handle content blocks if they contain embedded structures
	if mc.ContentBlocks != nil {
		for i := range *mc.ContentBlocks {
			(*mc.ContentBlocks)[i].ToResponsesOnly()
		}
	}
}

// ToResponsesOnly converts ContentBlock to use only Responses API format
func (cb *ContentBlock) ToResponsesOnly() {
	// If ResponsesAPI fields already exist, keep them as is
	if cb.ResponsesAPIExtendedContentBlock != nil {
		// Clear ChatCompletions fields
		cb.ChatCompletionsExtendedContentBlock = nil
		return
	}

	// Extract from ChatCompletions if available
	if cb.ChatCompletionsExtendedContentBlock != nil {
		cb.ResponsesAPIExtendedContentBlock = &ResponsesAPIExtendedContentBlock{}

		// Handle image content
		if cb.ChatCompletionsExtendedContentBlock.ImageURL != nil {
			url := cb.ChatCompletionsExtendedContentBlock.ImageURL.URL
			cb.ResponsesAPIExtendedContentBlock.InputMessageContentBlockImage = &InputMessageContentBlockImage{
				ImageURL: &url,
				Detail:   cb.ChatCompletionsExtendedContentBlock.ImageURL.Detail,
			}
		}

		// Handle file content
		if cb.ChatCompletionsExtendedContentBlock.File != nil {
			cb.ResponsesAPIExtendedContentBlock.InputMessageContentBlockFile = &InputMessageContentBlockFile{
				FileData: cb.ChatCompletionsExtendedContentBlock.File.FileData,
				Filename: cb.ChatCompletionsExtendedContentBlock.File.Filename,
			}
			cb.ResponsesAPIExtendedContentBlock.FileID = cb.ChatCompletionsExtendedContentBlock.File.FileID
		}

		// Handle audio content
		if cb.ChatCompletionsExtendedContentBlock.InputAudio != nil {
			format := ""
			if cb.ChatCompletionsExtendedContentBlock.InputAudio.Format != nil {
				format = *cb.ChatCompletionsExtendedContentBlock.InputAudio.Format
			}
			cb.ResponsesAPIExtendedContentBlock.Audio = &InputMessageContentBlockAudio{
				Data:   cb.ChatCompletionsExtendedContentBlock.InputAudio.Data,
				Format: format,
			}
		}
	}

	// Clear ChatCompletions fields
	cb.ChatCompletionsExtendedContentBlock = nil
}

// ToResponsesOnly converts ToolMessage to use only Responses API format
func (tm *ToolMessage) ToResponsesOnly() {
	// If ResponsesAPI fields already exist, keep them as is
	if tm.ResponsesAPIToolMessage != nil {
		// Clear ChatCompletions fields
		tm.ChatCompletionsToolMessage = nil
		return
	}

	// Extract from ChatCompletions if available
	if tm.ChatCompletionsToolMessage != nil {
		tm.ResponsesAPIToolMessage = &ResponsesAPIToolMessage{}

		// Extract ToolCallID to CallID
		if tm.ChatCompletionsToolMessage.ToolCallID != nil {
			tm.ResponsesAPIToolMessage.CallID = tm.ChatCompletionsToolMessage.ToolCallID
		}
	}

	// Clear ChatCompletions fields
	tm.ChatCompletionsToolMessage = nil
}

// ToResponsesOnly converts AssistantMessage to use only Responses API format
func (am *AssistantMessage) ToResponsesOnly() {
	// If ResponsesAPI fields already exist, keep them as is
	if am.ResponsesAPIExtendedAssistantMessage != nil {
		// Clear ChatCompletions fields
		am.ChatCompletionsAssistantMessage = nil
		// Still handle annotations recursively
		for i := range am.Annotations {
			am.Annotations[i].ToResponsesOnly()
		}
		return
	}

	// Extract from ChatCompletions if available
	if am.ChatCompletionsAssistantMessage != nil {
		am.ResponsesAPIExtendedAssistantMessage = &ResponsesAPIExtendedAssistantMessage{}
		// Handle annotations if present
		if len(am.ChatCompletionsAssistantMessage.Annotations) > 0 {
			am.Annotations = am.ChatCompletionsAssistantMessage.Annotations
		}
	}

	// Clear ChatCompletions fields
	am.ChatCompletionsAssistantMessage = nil

	// Recursively handle annotations
	for i := range am.Annotations {
		am.Annotations[i].ToResponsesOnly()
	}
}

// ToResponsesOnly converts Annotation to use only Responses API format
func (a *Annotation) ToResponsesOnly() {
	// If ResponsesAPI fields already exist, keep them as is
	if a.ResponsesAPIExtendedOutputMessageTextAnnotation != nil {
		return
	}

	// Extract from Citation data if available
	a.ResponsesAPIExtendedOutputMessageTextAnnotation = &ResponsesAPIExtendedOutputMessageTextAnnotation{
		StartIndex: &a.Citation.StartIndex,
		EndIndex:   &a.Citation.EndIndex,
	}

	// Handle different citation types
	switch a.Type {
	case "url_citation":
		urlCitation := &OutputMessageTextAnnotationURLCitation{
			Title: a.Citation.Title,
		}
		if a.Citation.URL != nil {
			urlCitation.URL = *a.Citation.URL
		}
		a.ResponsesAPIExtendedOutputMessageTextAnnotation.OutputMessageTextAnnotationURLCitation = urlCitation
	}
}

// ToResponsesOnly converts BifrostResponseChoice to use only Responses API format
func (brc *BifrostResponseChoice) ToResponsesOnly() {
	// Handle both stream and non-stream response choices
	if brc.BifrostNonStreamResponseChoice != nil {
		brc.BifrostNonStreamResponseChoice.Message.ToResponsesOnly()
	}
	// Stream choices would be handled similarly if needed
}

// User Facing Fields
// ModelParameters doesn't need conversion as it doesn't have embedded fields

// ToResponsesOnly converts ChatMessage to use only Responses API format
func (bm *ChatMessage) ToResponsesOnly() {
	// If ResponsesAPI fields already exist, keep them as is
	if bm.ResponsesAPIExtendedBifrostMessage != nil {
		// Still process content and embedded messages
		bm.Content.ToResponsesOnly()
		if bm.AssistantMessage != nil {
			bm.AssistantMessage.ToResponsesOnly()
		}
		if bm.ToolMessage != nil {
			bm.ToolMessage.ToResponsesOnly()
		}
		return
	}

	// Extract from ChatCompletions and determine message type
	bm.ResponsesAPIExtendedBifrostMessage = &ResponsesAPIExtendedBifrostMessage{}
	messageType := "message"

	if bm.AssistantMessage != nil {
		// Initialize ResponsesAPI fields for AssistantMessage if needed
		if bm.ResponsesAPIExtendedAssistantMessage == nil {
			bm.ResponsesAPIExtendedAssistantMessage = &ResponsesAPIExtendedAssistantMessage{}
		}

		// Transfer simple text content from chat-completions content → responses assistant text
		if bm.Content.ContentStr != nil && *bm.Content.ContentStr != "" && bm.ResponsesAPIExtendedAssistantMessage.Text == "" {
			bm.ResponsesAPIExtendedAssistantMessage.Text = *bm.Content.ContentStr
		}

		// Check for refusal
		if bm.AssistantMessage.ChatCompletionsAssistantMessage != nil && bm.AssistantMessage.ChatCompletionsAssistantMessage.Refusal != nil {
			messageType = "refusal"
		}

		// Check for tool calls
		if bm.AssistantMessage.ChatCompletionsAssistantMessage != nil && bm.AssistantMessage.ChatCompletionsAssistantMessage.ToolCalls != nil {
			messageType = "function_call"
		}
	}

	if bm.ToolMessage != nil {
		messageType = "function_call_output"

		// Initialize ResponsesAPI fields for ToolMessage if needed
		if bm.ResponsesAPIToolMessage == nil {
			bm.ResponsesAPIToolMessage = &ResponsesAPIToolMessage{}
		}

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

	// Recursively handle content and embedded messages
	bm.Content.ToResponsesOnly()

	if bm.AssistantMessage != nil {
		bm.AssistantMessage.ToResponsesOnly()
	}

	if bm.ToolMessage != nil {
		bm.ToolMessage.ToResponsesOnly()
	}
}

// ToResponsesOnly converts BifrostResponse to use only Responses API format
func (br *BifrostResponse) ToResponsesOnly() {
	// If ResponsesAPI fields already exist, keep them as is
	if br.ResponseAPIExtendedResponse != nil {
		// Clear ChatCompletions fields
		br.ChatCompletionsExtendedResponse = nil
		if br.Usage != nil {
			br.Usage.ChatCompletionsExtendedUsage = nil
		}
		// Still process output items if they exist
		if br.ResponseAPIExtendedResponse.Output != nil {
			for i := range br.ResponseAPIExtendedResponse.Output {
				(br.ResponseAPIExtendedResponse.Output)[i].ToResponsesOnly()
			}
		}
		return
	}

	// Extract from ChatCompletions and ExtraFields if available
	br.ResponseAPIExtendedResponse = &ResponseAPIExtendedResponse{}

	// Map params (ExtraFields.Params → Responses request params)
	br.ResponseAPIExtendedResponse.ResponsesAPIExtendedRequestParams = &ResponsesAPIExtendedRequestParams{}
	p := &br.ExtraFields.Params
	if p.Temperature != nil {
		br.ResponseAPIExtendedResponse.Temperature = p.Temperature
	}
	if p.TopP != nil {
		br.ResponseAPIExtendedResponse.TopP = p.TopP
	}
	if p.TopLogProbs != nil {
		br.ResponseAPIExtendedResponse.TopLogProbs = p.TopLogProbs
	}
	if p.ParallelToolCalls != nil {
		br.ResponseAPIExtendedResponse.ParallelToolCalls = p.ParallelToolCalls
	}
	if p.MaxTokens != nil {
		// Map chat max_tokens → responses max_output_tokens
		br.ResponseAPIExtendedResponse.MaxOutputTokens = p.MaxTokens
	}
	if p.StreamOptions != nil {
		br.ResponseAPIExtendedResponse.StreamOptions = &StreamOptions{
			IncludeObfuscation: p.StreamOptions.IncludeObfuscation,
		}
	}

	// Map usage (chat completions → responses)
	if br.Usage != nil {
		br.Usage.ResponsesAPIExtendedResponseUsage = &ResponsesAPIExtendedResponseUsage{}
		// If chat completions usage present, mirror to responses usage fields
		if br.Usage.ChatCompletionsExtendedUsage != nil {
			cu := br.Usage.ChatCompletionsExtendedUsage
			if br.Usage.PromptTokens > 0 {
				br.Usage.ResponsesAPIExtendedResponseUsage.InputTokens = br.Usage.PromptTokens
			}
			if br.Usage.CompletionTokens > 0 {
				br.Usage.ResponsesAPIExtendedResponseUsage.OutputTokens = br.Usage.CompletionTokens
			}
			if cu.TokenDetails != nil {
				br.Usage.ResponsesAPIExtendedResponseUsage.InputTokensDetails = &ResponsesAPIResponseInputTokens{
					CachedTokens: cu.TokenDetails.CachedTokens,
				}
			}
			if cu.CompletionTokensDetails != nil {
				br.Usage.ResponsesAPIExtendedResponseUsage.OutputTokensDetails = &ResponsesAPIResponseOutputTokens{
					ReasoningTokens: cu.CompletionTokensDetails.ReasoningTokens,
				}
			}
		}
		// Ensure total tokens calculation
		if br.Usage.TotalTokens == 0 {
			br.Usage.TotalTokens = br.Usage.PromptTokens + br.Usage.CompletionTokens
		}
		// Clear ChatCompletions usage
		br.Usage.ChatCompletionsExtendedUsage = nil
	}

	// Convert choices to ResponsesAPI Output format
	// In Responses API, multiple tool calls are expanded into separate output items
	var outputItems []ChatMessage

	for _, choice := range br.Choices {
		if choice.BifrostNonStreamResponseChoice != nil {
			msg := choice.BifrostNonStreamResponseChoice.Message

			// Check if this is an assistant message with multiple tool calls
			if msg.AssistantMessage != nil && msg.AssistantMessage.ChatCompletionsAssistantMessage != nil &&
				msg.AssistantMessage.ChatCompletionsAssistantMessage.ToolCalls != nil &&
				len(*msg.AssistantMessage.ChatCompletionsAssistantMessage.ToolCalls) > 0 {

				// Expand multiple tool calls into separate function_call items
				expanded := expandAssistantToolCallsToResponsesItems(msg)
				for i := range expanded {
					expanded[i].ToResponsesOnly()
					outputItems = append(outputItems, expanded[i])
				}
			} else {
				// Regular message - convert to Responses API format
				msg.ToResponsesOnly()
				outputItems = append(outputItems, msg)
			}
		}
	}

	// Set the Output field if we have items
	if len(outputItems) > 0 {
		br.ResponseAPIExtendedResponse.Output = outputItems
	}

	// Clear ChatCompletions fields
	br.ChatCompletionsExtendedResponse = nil
}

// ExpandAssistantToolCallsToResponsesItems converts a single Chat Completions-style assistant message
// (with multiple tool calls in AssistantMessage.ToolCalls) into multiple Responses API items, where
// each tool call becomes its own ChatMessage with `Type = "function_call"` and a ToolMessage payload.
//
// Notes:
//   - This does NOT mutate the input message.
//   - Each returned message will have only ToolMessage populated (AssistantMessage will be nil),
//     to avoid embedded pointer conflicts during JSON marshalling.
//   - Function name and call_id (if present) are preserved on the item.
func expandAssistantToolCallsToResponsesItems(msg ChatMessage) []ChatMessage {
	if msg.AssistantMessage == nil || msg.AssistantMessage.ChatCompletionsAssistantMessage == nil ||
		msg.AssistantMessage.ChatCompletionsAssistantMessage.ToolCalls == nil {
		return nil
	}

	toolCalls := *msg.AssistantMessage.ChatCompletionsAssistantMessage.ToolCalls
	if len(toolCalls) == 0 {
		return nil
	}

	items := make([]ChatMessage, 0, len(toolCalls))
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

		toolMsg := &ToolMessage{
			ResponsesAPIToolMessage: &ResponsesAPIToolMessage{
				CallID: callID,
			},
		}

		items = append(items, ChatMessage{
			Name:        namePtr,
			Role:        ChatMessageRoleAssistant,
			Content:     ChatMessageContent{},
			ToolMessage: toolMsg,
			ResponsesAPIExtendedBifrostMessage: &ResponsesAPIExtendedBifrostMessage{
				Type:      &itemType,
				Arguments: Ptr(tc.Function.Arguments),
			},
		})
	}

	return items
}
