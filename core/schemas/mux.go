package schemas

// =============================================================================
// BIDIRECTIONAL CONVERSION METHODS
// =============================================================================
//
// This section contains methods for converting between Chat Completions API
// and Responses API formats. These methods are attached to the structs themselves
// for easy conversion in both directions.
//
// Key Features:
// 1. Bidirectional: Convert to and from both formats
// 2. Data preservation: All relevant data is preserved during conversion
// 3. Aggregation/Spreading: Handle tool messages properly for each format
// 4. Validation: Ensure data integrity during conversion
//
// =============================================================================

// =============================================================================
// TOOL CONVERSION METHODS
// =============================================================================

// ToResponsesTool converts a ChatTool to ResponsesTool format
func (ct *ChatTool) ToResponsesTool() *ResponsesTool {
	if ct == nil {
		return &ResponsesTool{}
	}

	rt := &ResponsesTool{
		Type: string(ct.Type),
	}

	// Convert function tools
	if ct.Type == ChatToolTypeFunction && ct.Function != nil {
		rt.Name = &ct.Function.Name
		rt.Description = ct.Function.Description

		// Create ResponsesToolFunction if needed
		if ct.Function.Parameters != nil || ct.Function.Strict != nil {
			rt.ResponsesToolFunction = &ResponsesToolFunction{
				Parameters: ct.Function.Parameters,
				Strict:     ct.Function.Strict,
			}
		}
	}

	// Convert custom tools
	if ct.Type == ChatToolTypeCustom && ct.Custom != nil {
		if ct.Custom.Format != nil {
			rt.ResponsesToolCustom = &ResponsesToolCustom{
				Format: &ResponsesToolCustomFormat{
					Type: ct.Custom.Format.Type,
				},
			}
			if ct.Custom.Format.Grammar != nil {
				rt.ResponsesToolCustom.Format.Definition = &ct.Custom.Format.Grammar.Definition
				rt.ResponsesToolCustom.Format.Syntax = &ct.Custom.Format.Grammar.Syntax
			}
		}
	}

	return rt
}

// ToChatTool converts a ResponsesTool to ChatTool format
func (rt *ResponsesTool) ToChatTool() *ChatTool {
	if rt == nil {
		return &ChatTool{}
	}

	ct := &ChatTool{
		Type: ChatToolType(rt.Type),
	}

	// Convert function tools
	if rt.Type == "function" {
		ct.Function = &ChatToolFunction{}

		if rt.Name != nil {
			ct.Function.Name = *rt.Name
		}
		if rt.Description != nil {
			ct.Function.Description = rt.Description
		}
		if rt.ResponsesToolFunction != nil {
			ct.Function.Parameters = rt.ResponsesToolFunction.Parameters
			ct.Function.Strict = rt.ResponsesToolFunction.Strict
		}
	}

	// Convert custom tools
	if rt.Type == "custom" && rt.ResponsesToolCustom != nil {
		ct.Custom = &ChatToolCustom{}
		if rt.ResponsesToolCustom.Format != nil {
			ct.Custom.Format = &ChatToolCustomFormat{
				Type: rt.ResponsesToolCustom.Format.Type,
			}
			if rt.ResponsesToolCustom.Format.Definition != nil && rt.ResponsesToolCustom.Format.Syntax != nil {
				ct.Custom.Format.Grammar = &ChatToolCustomGrammarFormat{
					Definition: *rt.ResponsesToolCustom.Format.Definition,
					Syntax:     *rt.ResponsesToolCustom.Format.Syntax,
				}
			}
		}
	}

	return ct
}

// =============================================================================
// TOOL CHOICE CONVERSION METHODS
// =============================================================================

// ToResponsesToolChoice converts a ChatToolChoice to ResponsesToolChoice format
func (ctc *ChatToolChoice) ToResponsesToolChoice() *ResponsesToolChoice {
	if ctc == nil {
		return &ResponsesToolChoice{}
	}

	rtc := &ResponsesToolChoice{}

	// Handle string choice (e.g., "none", "auto", "required")
	if ctc.ChatToolChoiceStr != nil {
		rtc.ResponsesToolChoiceStr = ctc.ChatToolChoiceStr
		return rtc
	}

	// Handle structured choice
	if ctc.ChatToolChoiceStruct != nil {
		rtc.ResponsesToolChoiceStruct = &ResponsesToolChoiceStruct{
			Type: ResponsesToolChoiceType(ctc.ChatToolChoiceStruct.Type),
		}

		switch ctc.ChatToolChoiceStruct.Type {
		case ChatToolChoiceTypeNone, ChatToolChoiceTypeAny, ChatToolChoiceTypeRequired:
			// These map to mode field
			modeStr := string(ctc.ChatToolChoiceStruct.Type)
			rtc.ResponsesToolChoiceStruct.Mode = &modeStr

		case ChatToolChoiceTypeFunction:
			// Map function choice
			if ctc.ChatToolChoiceStruct.Function.Name != "" {
				rtc.ResponsesToolChoiceStruct.Name = &ctc.ChatToolChoiceStruct.Function.Name
			}

		case ChatToolChoiceTypeAllowedTools:
			// Map allowed tools
			if len(ctc.ChatToolChoiceStruct.AllowedTools.Tools) > 0 {
				tools := make([]ResponsesToolChoiceAllowedToolDef, len(ctc.ChatToolChoiceStruct.AllowedTools.Tools))
				for i, tool := range ctc.ChatToolChoiceStruct.AllowedTools.Tools {
					tools[i] = ResponsesToolChoiceAllowedToolDef{
						Type: tool.Type,
					}
					if tool.Function.Name != "" {
						name := tool.Function.Name
						tools[i].Name = &name
					}
				}
				rtc.ResponsesToolChoiceStruct.Tools = tools
			}
			// Copy the mode (e.g., "auto", "required")
			if ctc.ChatToolChoiceStruct.AllowedTools.Mode != "" {
				mode := ctc.ChatToolChoiceStruct.AllowedTools.Mode
				rtc.ResponsesToolChoiceStruct.Mode = &mode
			}

		case ChatToolChoiceTypeCustom:
			// Map custom choice
			if ctc.ChatToolChoiceStruct.Custom.Name != "" {
				rtc.ResponsesToolChoiceStruct.Name = &ctc.ChatToolChoiceStruct.Custom.Name
			}
		}
	}

	return rtc
}

// ToChatToolChoice converts a ResponsesToolChoice to ChatToolChoice format
func (rtc *ResponsesToolChoice) ToChatToolChoice() *ChatToolChoice {
	if rtc == nil {
		return &ChatToolChoice{}
	}

	ctc := &ChatToolChoice{}

	// Handle string choice
	if rtc.ResponsesToolChoiceStr != nil {
		ctc.ChatToolChoiceStr = rtc.ResponsesToolChoiceStr
		return ctc
	}

	// Handle structured choice
	if rtc.ResponsesToolChoiceStruct != nil {
		ctc.ChatToolChoiceStruct = &ChatToolChoiceStruct{
			Type: ChatToolChoiceType(rtc.ResponsesToolChoiceStruct.Type),
		}

		// Handle mode-based choices (none, auto, required)
		if rtc.ResponsesToolChoiceStruct.Mode != nil {
			switch *rtc.ResponsesToolChoiceStruct.Mode {
			case "none":
				ctc.ChatToolChoiceStruct.Type = ChatToolChoiceTypeNone
			case "auto":
				ctc.ChatToolChoiceStruct.Type = ChatToolChoiceTypeAny
			case "required":
				ctc.ChatToolChoiceStruct.Type = ChatToolChoiceTypeRequired
			}
		}

		// Handle function choice
		if rtc.ResponsesToolChoiceStruct.Type == ResponsesToolChoiceTypeFunction && rtc.ResponsesToolChoiceStruct.Name != nil {
			ctc.ChatToolChoiceStruct.Function = ChatToolChoiceFunction{
				Name: *rtc.ResponsesToolChoiceStruct.Name,
			}
		}

		// Handle custom choice
		if rtc.ResponsesToolChoiceStruct.Type == ResponsesToolChoiceTypeCustom && rtc.ResponsesToolChoiceStruct.Name != nil {
			ctc.ChatToolChoiceStruct.Custom = ChatToolChoiceCustom{
				Name: *rtc.ResponsesToolChoiceStruct.Name,
			}
		}

		// Handle allowed tools
		if len(rtc.ResponsesToolChoiceStruct.Tools) > 0 {
			ctc.ChatToolChoiceStruct.Type = ChatToolChoiceTypeAllowedTools
			tools := make([]ChatToolChoiceAllowedToolsTool, len(rtc.ResponsesToolChoiceStruct.Tools))
			for i, tool := range rtc.ResponsesToolChoiceStruct.Tools {
				tools[i] = ChatToolChoiceAllowedToolsTool{
					Type: tool.Type,
				}
				if tool.Name != nil {
					tools[i].Function = ChatToolChoiceFunction{Name: *tool.Name}
				}
			}
			// Copy the mode if present, otherwise default to "auto"
			mode := "auto"
			if rtc.ResponsesToolChoiceStruct.Mode != nil && *rtc.ResponsesToolChoiceStruct.Mode != "" {
				mode = *rtc.ResponsesToolChoiceStruct.Mode
			}
			ctc.ChatToolChoiceStruct.AllowedTools = ChatToolChoiceAllowedTools{
				Mode:  mode,
				Tools: tools,
			}
		}

		return ctc
	}

	return nil
}

// =============================================================================
// MESSAGE CONVERSION METHODS
// =============================================================================

// ToResponsesMessages converts a ChatMessage to one or more ResponsesMessages
// This handles the expansion of assistant messages with tool calls into separate function_call messages
func (cm *ChatMessage) ToResponsesMessages() []ResponsesMessage {
	if cm == nil {
		return []ResponsesMessage{}
	}

	var messages []ResponsesMessage

	// Check if this is an assistant message with multiple tool calls that need expansion
	if cm.ChatAssistantMessage != nil && cm.ChatAssistantMessage.ToolCalls != nil && len(cm.ChatAssistantMessage.ToolCalls) > 0 {
		// Expand multiple tool calls into separate function_call items
		for _, tc := range cm.ChatAssistantMessage.ToolCalls {
			messageType := ResponsesMessageTypeFunctionCall

			var callID *string
			if tc.ID != nil && *tc.ID != "" {
				callID = tc.ID
			}

			var namePtr *string
			if tc.Function.Name != nil && *tc.Function.Name != "" {
				namePtr = tc.Function.Name
			}

			// Create a copy of the arguments string to avoid range loop variable capture
			var argumentsPtr *string
			if tc.Function.Arguments != "" {
				argumentsPtr = Ptr(tc.Function.Arguments)
			}

			rm := ResponsesMessage{
				Type: &messageType,
				Role: Ptr(ResponsesInputMessageRoleAssistant),
				ResponsesToolMessage: &ResponsesToolMessage{
					CallID:    callID,
					Name:      namePtr,
					Arguments: argumentsPtr,
				},
			}

			messages = append(messages, rm)
		}
		return messages
	}

	// Regular message conversion
	messageType := ResponsesMessageTypeMessage
	role := ResponsesInputMessageRoleUser

	// Determine message type and role
	switch cm.Role {
	case ChatMessageRoleAssistant:
		role = ResponsesInputMessageRoleAssistant
		// Check for refusal
		if cm.ChatAssistantMessage != nil && cm.ChatAssistantMessage.Refusal != nil {
			messageType = ResponsesMessageTypeRefusal
		}
	case ChatMessageRoleUser:
		role = ResponsesInputMessageRoleUser
	case ChatMessageRoleSystem:
		role = ResponsesInputMessageRoleSystem
	case ChatMessageRoleTool:
		messageType = ResponsesMessageTypeFunctionCallOutput
		role = ResponsesInputMessageRoleUser // Tool messages are typically user role in responses
	case ChatMessageRoleDeveloper:
		role = ResponsesInputMessageRoleDeveloper
	}

	rm := ResponsesMessage{
		Type: &messageType,
		Role: &role,
	}

	// Handle refusal content specifically - use content blocks with ResponsesOutputMessageContentRefusal
	if messageType == ResponsesMessageTypeRefusal && cm.ChatAssistantMessage != nil && cm.ChatAssistantMessage.Refusal != nil {
		refusalBlock := ResponsesMessageContentBlock{
			Type: ResponsesOutputMessageContentTypeRefusal,
			ResponsesOutputMessageContentRefusal: &ResponsesOutputMessageContentRefusal{
				Refusal: *cm.ChatAssistantMessage.Refusal,
			},
		}
		rm.Content = &ResponsesMessageContent{
			ContentBlocks: []ResponsesMessageContentBlock{refusalBlock},
		}
	} else if cm.Content.ContentStr != nil {
		// Convert regular string content
		rm.Content = &ResponsesMessageContent{
			ContentStr: cm.Content.ContentStr,
		}
	} else if cm.Content.ContentBlocks != nil {
		// Convert content blocks
		responseBlocks := make([]ResponsesMessageContentBlock, len(cm.Content.ContentBlocks))
		for i, block := range cm.Content.ContentBlocks {
			blockType := ResponsesMessageContentBlockType(block.Type)

			switch block.Type {
			case ChatContentBlockTypeText:
				if cm.Role == ChatMessageRoleAssistant {
					blockType = ResponsesOutputMessageContentTypeText
				} else {
					blockType = ResponsesInputMessageContentBlockTypeText
				}
			case ChatContentBlockTypeImage:
				blockType = ResponsesInputMessageContentBlockTypeImage
			case ChatContentBlockTypeFile:
				blockType = ResponsesInputMessageContentBlockTypeFile
			case ChatContentBlockTypeInputAudio:
				blockType = ResponsesInputMessageContentBlockTypeAudio
			}

			responseBlocks[i] = ResponsesMessageContentBlock{
				Type: blockType,
				Text: block.Text,
			}

			// Convert specific block types
			if block.ImageURLStruct != nil {
				responseBlocks[i].ResponsesInputMessageContentBlockImage = &ResponsesInputMessageContentBlockImage{
					ImageURL: &block.ImageURLStruct.URL,
					Detail:   block.ImageURLStruct.Detail,
				}
			}
			if block.File != nil {
				responseBlocks[i].ResponsesInputMessageContentBlockFile = &ResponsesInputMessageContentBlockFile{
					FileData: block.File.FileData,
					Filename: block.File.Filename,
				}
				responseBlocks[i].FileID = block.File.FileID
			}
			if block.InputAudio != nil {
				format := ""
				if block.InputAudio.Format != nil {
					format = *block.InputAudio.Format
				}
				responseBlocks[i].Audio = &ResponsesInputMessageContentBlockAudio{
					Data:   block.InputAudio.Data,
					Format: format,
				}
			}
		}
		rm.Content = &ResponsesMessageContent{
			ContentBlocks: responseBlocks,
		}
	}

	// Handle tool messages
	if cm.ChatToolMessage != nil {
		rm.ResponsesToolMessage = &ResponsesToolMessage{}
		if cm.ChatToolMessage.ToolCallID != nil {
			rm.ResponsesToolMessage.CallID = cm.ChatToolMessage.ToolCallID
		}

		// If tool output content exists, add it to function_call_output
		if rm.Content != nil && rm.Content.ContentStr != nil && *rm.Content.ContentStr != "" {
			rm.ResponsesToolMessage.ResponsesFunctionToolCallOutput = &ResponsesFunctionToolCallOutput{
				ResponsesFunctionToolCallOutputStr: rm.Content.ContentStr,
			}
		}
	}

	messages = append(messages, rm)
	return messages
}

// ToChatMessages converts a slice of ResponsesMessages back to ChatMessages
// This handles the aggregation of function_call messages back into assistant messages with tool calls
func ToChatMessages(rms []ResponsesMessage) []ChatMessage {
	if len(rms) == 0 {
		return []ChatMessage{}
	}

	var chatMessages []ChatMessage
	var currentToolCalls []ChatAssistantMessageToolCall

	for _, rm := range rms {
		if rm.Type != nil && *rm.Type == ResponsesMessageTypeReasoning {
			continue
		}

		// Handle function_call messages - collect them for aggregation
		if rm.Type != nil && *rm.Type == ResponsesMessageTypeFunctionCall {
			if rm.ResponsesToolMessage != nil {
				tc := ChatAssistantMessageToolCall{
					Type: Ptr("function"),
				}

				if rm.ResponsesToolMessage.CallID != nil {
					tc.ID = rm.ResponsesToolMessage.CallID
				}

				tc.Function = ChatAssistantMessageToolCallFunction{}
				if rm.ResponsesToolMessage.Name != nil {
					tc.Function.Name = rm.ResponsesToolMessage.Name
				}
				if rm.ResponsesToolMessage.Arguments != nil {
					tc.Function.Arguments = *rm.ResponsesToolMessage.Arguments
				}

				currentToolCalls = append(currentToolCalls, tc)
			}
			continue
		}

		// If we have collected tool calls, create an assistant message with them
		if len(currentToolCalls) > 0 {
			// Create a copy of the slice to avoid shared slice header issues
			toolCallsCopy := append([]ChatAssistantMessageToolCall(nil), currentToolCalls...)
			chatMessages = append(chatMessages, ChatMessage{
				Role: ChatMessageRoleAssistant,
				ChatAssistantMessage: &ChatAssistantMessage{
					ToolCalls: toolCallsCopy,
				},
			})
			currentToolCalls = nil // Reset for next batch
		}

		// Convert regular message
		cm := ChatMessage{}

		// Set role
		if rm.Role != nil {
			switch *rm.Role {
			case ResponsesInputMessageRoleAssistant:
				cm.Role = ChatMessageRoleAssistant
			case ResponsesInputMessageRoleUser:
				cm.Role = ChatMessageRoleUser
			case ResponsesInputMessageRoleSystem:
				cm.Role = ChatMessageRoleSystem
			case ResponsesInputMessageRoleDeveloper:
				cm.Role = ChatMessageRoleDeveloper
			}
		}

		// Handle special message types
		if rm.Type != nil {
			switch *rm.Type {
			case ResponsesMessageTypeFunctionCallOutput:
				cm.Role = ChatMessageRoleTool
				if rm.ResponsesToolMessage != nil && rm.ResponsesToolMessage.CallID != nil {
					cm.ChatToolMessage = &ChatToolMessage{
						ToolCallID: rm.ResponsesToolMessage.CallID,
					}

					// Extract content from ResponsesFunctionToolCallOutput if present
					// This is needed because OpenAI Responses API uses an "output" field
					// which is stored in ResponsesFunctionToolCallOutput
					if rm.ResponsesToolMessage.ResponsesFunctionToolCallOutput != nil {
						if rm.Content == nil {
							rm.Content = &ResponsesMessageContent{}
						}
						// If Content is not already set, extract from ResponsesFunctionToolCallOutput
						if rm.Content.ContentStr == nil && rm.Content.ContentBlocks == nil {
							if rm.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputStr != nil {
								rm.Content.ContentStr = rm.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputStr
							} else if rm.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks != nil {
								rm.Content.ContentBlocks = rm.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks
							}
						}
					}
				}
			case ResponsesMessageTypeRefusal:
				cm.ChatAssistantMessage = &ChatAssistantMessage{}
				// Extract refusal from content blocks or ContentStr
				if rm.Content != nil {
					if rm.Content.ContentBlocks != nil {
						// Look for refusal content block
						for _, block := range rm.Content.ContentBlocks {
							if block.Type == ResponsesOutputMessageContentTypeRefusal && block.ResponsesOutputMessageContentRefusal != nil {
								refusalText := block.ResponsesOutputMessageContentRefusal.Refusal
								cm.ChatAssistantMessage.Refusal = &refusalText
								break
							}
						}
					} else if rm.Content.ContentStr != nil {
						// Fallback to ContentStr for backward compatibility
						cm.ChatAssistantMessage.Refusal = rm.Content.ContentStr
					}
				}
			}
		}

		// Convert content (skip for refusal messages since refusal is already extracted)
		if rm.Content != nil && (rm.Type == nil || *rm.Type != ResponsesMessageTypeRefusal) {
			if rm.Content.ContentStr != nil {
				cm.Content = ChatMessageContent{
					ContentStr: rm.Content.ContentStr,
				}
			} else if rm.Content.ContentBlocks != nil {
				chatBlocks := make([]ChatContentBlock, len(rm.Content.ContentBlocks))
				for i, block := range rm.Content.ContentBlocks {
					// Map ResponsesMessageContentBlockType to ChatContentBlockType
					var chatBlockType ChatContentBlockType
					switch block.Type {
					case ResponsesInputMessageContentBlockTypeText:
						chatBlockType = ChatContentBlockTypeText // "input_text" -> "text"
					case ResponsesInputMessageContentBlockTypeImage:
						chatBlockType = ChatContentBlockTypeImage // "input_image" -> "image_url"
					case ResponsesInputMessageContentBlockTypeFile:
						chatBlockType = ChatContentBlockTypeFile // "input_file" -> "input_file" (same)
					case ResponsesInputMessageContentBlockTypeAudio:
						chatBlockType = ChatContentBlockTypeInputAudio // "input_audio" -> "input_audio" (same)
					default:
						// For unknown types, fall back to direct conversion
						chatBlockType = ChatContentBlockType(block.Type)
					}

					chatBlocks[i] = ChatContentBlock{
						Type: chatBlockType,
						Text: block.Text,
					}

					// Convert specific block types
					if block.ResponsesInputMessageContentBlockImage != nil {
						chatBlocks[i].ImageURLStruct = &ChatInputImage{
							Detail: block.ResponsesInputMessageContentBlockImage.Detail,
						}
						if block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
							chatBlocks[i].ImageURLStruct.URL = *block.ResponsesInputMessageContentBlockImage.ImageURL
						}
					}
					if block.ResponsesInputMessageContentBlockFile != nil {
						chatBlocks[i].File = &ChatInputFile{
							FileData: block.ResponsesInputMessageContentBlockFile.FileData,
							Filename: block.ResponsesInputMessageContentBlockFile.Filename,
							FileID:   block.FileID,
						}
					}
					if block.Audio != nil {
						chatBlocks[i].InputAudio = &ChatInputAudio{
							Data: block.Audio.Data,
						}
						if block.Audio.Format != "" {
							chatBlocks[i].InputAudio.Format = &block.Audio.Format
						}
					}
				}
				cm.Content = ChatMessageContent{
					ContentBlocks: chatBlocks,
				}
			}
		}

		chatMessages = append(chatMessages, cm)
	}

	// Handle any remaining tool calls at the end
	if len(currentToolCalls) > 0 {
		// Create a copy of the slice to avoid shared slice header issues
		toolCallsCopy := append([]ChatAssistantMessageToolCall(nil), currentToolCalls...)
		chatMessages = append(chatMessages, ChatMessage{
			Role: ChatMessageRoleAssistant,
			ChatAssistantMessage: &ChatAssistantMessage{
				ToolCalls: toolCallsCopy,
			},
		})
	}

	return chatMessages
}

// =============================================================================
// REQUEST CONVERSION METHODS
// =============================================================================

// ToResponsesRequest converts a BifrostChatRequest to BifrostResponsesRequest format
func (bcr *BifrostChatRequest) ToResponsesRequest() *BifrostResponsesRequest {
	if bcr == nil {
		return &BifrostResponsesRequest{}
	}

	brr := &BifrostResponsesRequest{
		Provider:  bcr.Provider,
		Model:     bcr.Model,
		Fallbacks: bcr.Fallbacks, // Copy fallbacks as-is
	}

	// Convert Input messages using existing ChatMessage.ToResponsesMessages()
	var allResponsesMessages []ResponsesMessage
	for _, chatMsg := range bcr.Input {
		responsesMessages := chatMsg.ToResponsesMessages()
		allResponsesMessages = append(allResponsesMessages, responsesMessages...)
	}
	brr.Input = allResponsesMessages

	// Convert Parameters
	if bcr.Params != nil {
		brr.Params = &ResponsesParameters{
			// Map common fields
			ParallelToolCalls: bcr.Params.ParallelToolCalls,
			PromptCacheKey:    bcr.Params.PromptCacheKey,
			SafetyIdentifier:  bcr.Params.SafetyIdentifier,
			ServiceTier:       bcr.Params.ServiceTier,
			Store:             bcr.Params.Store,
			Temperature:       bcr.Params.Temperature,
			TopLogProbs:       bcr.Params.TopLogProbs,
			TopP:              bcr.Params.TopP,
			ExtraParams:       bcr.Params.ExtraParams,

			// Map specific fields
			MaxOutputTokens: bcr.Params.MaxCompletionTokens, // max_completion_tokens -> max_output_tokens
			Metadata:        bcr.Params.Metadata,
		}

		// Convert StreamOptions
		if bcr.Params.StreamOptions != nil {
			brr.Params.StreamOptions = &ResponsesStreamOptions{
				IncludeObfuscation: bcr.Params.StreamOptions.IncludeObfuscation,
			}
		}

		// Convert Tools using existing ChatTool.ToResponsesTool()
		if len(bcr.Params.Tools) > 0 {
			responsesTools := make([]ResponsesTool, 0, len(bcr.Params.Tools))
			for _, chatTool := range bcr.Params.Tools {
				responsesTool := chatTool.ToResponsesTool()
				responsesTools = append(responsesTools, *responsesTool)
			}
			brr.Params.Tools = responsesTools
		}

		// Convert ToolChoice using existing ChatToolChoice.ToResponsesToolChoice()
		if bcr.Params.ToolChoice != nil {
			responsesToolChoice := bcr.Params.ToolChoice.ToResponsesToolChoice()
			brr.Params.ToolChoice = responsesToolChoice
		}

		// Handle Reasoning from reasoning_effort
		if bcr.Params.ReasoningEffort != nil {
			brr.Params.Reasoning = &ResponsesParametersReasoning{
				Effort: bcr.Params.ReasoningEffort,
			}
		}

		// Handle Verbosity
		if bcr.Params.Verbosity != nil {
			if brr.Params.Text == nil {
				brr.Params.Text = &ResponsesTextConfig{}
			}
			brr.Params.Text.Verbosity = bcr.Params.Verbosity
		}
	}

	return brr
}

// ToChatRequest converts a BifrostResponsesRequest to BifrostChatRequest format
func (brr *BifrostResponsesRequest) ToChatRequest() *BifrostChatRequest {
	if brr == nil {
		return &BifrostChatRequest{}
	}

	bcr := &BifrostChatRequest{
		Provider:  brr.Provider,
		Model:     brr.Model,
		Fallbacks: brr.Fallbacks, // Copy fallbacks as-is
	}

	// Convert Input messages using existing ToChatMessages()
	bcr.Input = ToChatMessages(brr.Input)

	// Convert Parameters
	if brr.Params != nil {
		bcr.Params = &ChatParameters{
			// Map common fields
			ParallelToolCalls: brr.Params.ParallelToolCalls,
			PromptCacheKey:    brr.Params.PromptCacheKey,
			SafetyIdentifier:  brr.Params.SafetyIdentifier,
			ServiceTier:       brr.Params.ServiceTier,
			Store:             brr.Params.Store,
			Temperature:       brr.Params.Temperature,
			TopLogProbs:       brr.Params.TopLogProbs,
			TopP:              brr.Params.TopP,
			ExtraParams:       brr.Params.ExtraParams,

			// Map specific fields
			MaxCompletionTokens: brr.Params.MaxOutputTokens, // max_output_tokens -> max_completion_tokens
			Metadata:            brr.Params.Metadata,
		}

		// Convert StreamOptions
		if brr.Params.StreamOptions != nil {
			bcr.Params.StreamOptions = &ChatStreamOptions{
				IncludeObfuscation: brr.Params.StreamOptions.IncludeObfuscation,
				IncludeUsage:       Ptr(true), // Default for Chat API
			}
		}

		// Convert Tools using existing ResponsesTool.ToChatTool()
		if len(brr.Params.Tools) > 0 {
			chatTools := make([]ChatTool, 0, len(brr.Params.Tools))
			for _, responsesTool := range brr.Params.Tools {
				chatTool := responsesTool.ToChatTool()
				chatTools = append(chatTools, *chatTool)
			}
			bcr.Params.Tools = chatTools
		}

		// Convert ToolChoice using existing ResponsesToolChoice.ToChatToolChoice()
		if brr.Params.ToolChoice != nil {
			chatToolChoice := brr.Params.ToolChoice.ToChatToolChoice()
			bcr.Params.ToolChoice = chatToolChoice
		}

		// Handle ReasoningEffort from Reasoning
		if brr.Params.Reasoning != nil && brr.Params.Reasoning.Effort != nil {
			bcr.Params.ReasoningEffort = brr.Params.Reasoning.Effort
		}

		// Handle Verbosity from Text config
		if brr.Params.Text != nil && brr.Params.Text.Verbosity != nil {
			bcr.Params.Verbosity = brr.Params.Text.Verbosity
		}
	}

	return bcr
}

// =============================================================================
// RESPONSE CONVERSION METHODS
// =============================================================================

// ToResponsesOnly converts the BifrostResponse to use only Responses API format
// This converts Chat-style fields (Choices) to embedded ResponsesResponse format
func (br *BifrostResponse) ToResponsesOnly() {
	// If ResponsesResponse already exists, keep it and clear Chat fields
	if br.ResponsesResponse != nil {
		br.Choices = nil
		return
	}

	// Create ResponsesResponse from Chat fields
	br.ResponsesResponse = &ResponsesResponse{
		CreatedAt: br.Created,
	}

	br.Created = 0

	// Convert Choices to Output messages
	var outputMessages []ResponsesMessage
	for _, choice := range br.Choices {
		if choice.BifrostNonStreamResponseChoice != nil {
			// Convert ChatMessage to ResponsesMessages
			responsesMessages := choice.BifrostNonStreamResponseChoice.Message.ToResponsesMessages()
			outputMessages = append(outputMessages, responsesMessages...)
		}
		// Note: Stream choices would need different handling if needed
	}

	if len(outputMessages) > 0 {
		br.ResponsesResponse.Output = outputMessages
	}

	// Convert Usage if needed
	if br.Usage != nil {
		if br.Usage.ResponsesExtendedResponseUsage == nil {
			br.Usage.ResponsesExtendedResponseUsage = &ResponsesExtendedResponseUsage{
				InputTokens:  br.Usage.PromptTokens,
				OutputTokens: br.Usage.CompletionTokens,
			}

			if br.Usage.TotalTokens == 0 {
				br.Usage.TotalTokens = br.Usage.PromptTokens + br.Usage.CompletionTokens
			}

			br.Usage.PromptTokens = 0
			br.Usage.CompletionTokens = 0
		}
	}

	// Clear Chat fields after conversion
	br.Choices = nil
	br.Object = "response"
	br.ExtraFields.RequestType = ResponsesRequest
}

// ToChatOnly converts the BifrostResponse to use only Chat API format
// This converts embedded ResponsesResponse format to Chat-style fields (Choices)
func (br *BifrostResponse) ToChatOnly() {
	if br == nil {
		return
	}

	// If Choices already exist, keep them and clear ResponsesResponse
	if len(br.Choices) > 0 {
		br.ResponsesResponse = nil
		return
	}

	// Create Choices from ResponsesResponse
	if br.ResponsesResponse != nil && len(br.ResponsesResponse.Output) > 0 {
		// Convert ResponsesMessages back to ChatMessages
		chatMessages := ToChatMessages(br.ResponsesResponse.Output)

		// Create choices from chat messages
		choices := make([]BifrostChatResponseChoice, 0, len(chatMessages))
		for i, chatMsg := range chatMessages {
			choice := BifrostChatResponseChoice{
				Index: i,
				BifrostNonStreamResponseChoice: &BifrostNonStreamResponseChoice{
					Message: chatMsg,
				},
			}
			choices = append(choices, choice)
		}

		br.Choices = choices

		// Update Created timestamp from ResponsesResponse
		if br.ResponsesResponse.CreatedAt > 0 {
			br.Created = br.ResponsesResponse.CreatedAt
		}
	}

	// Convert Usage if needed
	if br.Usage != nil && br.Usage.ResponsesExtendedResponseUsage != nil {
		// Map Responses usage to Chat usage
		br.Usage.PromptTokens = br.Usage.ResponsesExtendedResponseUsage.InputTokens
		br.Usage.CompletionTokens = br.Usage.ResponsesExtendedResponseUsage.OutputTokens
		if br.Usage.TotalTokens == 0 {
			br.Usage.TotalTokens = br.Usage.PromptTokens + br.Usage.CompletionTokens
		}
	}

	// Clear ResponsesResponse after conversion
	br.ResponsesResponse = nil
}
