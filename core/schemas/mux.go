package schemas

import (
	"fmt"
	"sync"
	"time"
)

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
		Type: ResponsesToolType(ct.Type),
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

// ToChatAssistantMessageToolCall converts a ResponsesToolMessage to ChatAssistantMessageToolCall format.
// This is useful for executing Responses API tool calls using the Chat API tool executor.
//
// Returns:
//   - *ChatAssistantMessageToolCall: The converted tool call in Chat API format
//
// Example:
//
//	responsesToolMsg := &ResponsesToolMessage{
//	    CallID:    Ptr("call-123"),
//	    Name:      Ptr("calculate"),
//	    Arguments: Ptr("{\"x\": 10, \"y\": 20}"),
//	}
//	chatToolCall := responsesToolMsg.ToChatAssistantMessageToolCall()
func (rtm *ResponsesToolMessage) ToChatAssistantMessageToolCall() *ChatAssistantMessageToolCall {
	if rtm == nil {
		return nil
	}

	toolCall := &ChatAssistantMessageToolCall{
		ID:   rtm.CallID,
		Type: Ptr("function"),
		Function: ChatAssistantMessageToolCallFunction{
			Name:      rtm.Name,
			Arguments: "{}", // Default to empty JSON object for valid JSON unmarshaling
		},
	}

	// Extract arguments string
	if rtm.Arguments != nil {
		toolCall.Function.Arguments = *rtm.Arguments
	}

	return toolCall
}

// ToResponsesToolMessage converts a ChatToolMessage (tool execution result) to ResponsesToolMessage format.
// This creates a function_call_output message suitable for the Responses API.
//
// Returns:
//   - *ResponsesMessage: A ResponsesMessage with type=function_call_output containing the tool result
//
// Example:
//
//	chatToolMsg := &ChatMessage{
//	    Role: ChatMessageRoleTool,
//	    ChatToolMessage: &ChatToolMessage{
//	        ToolCallID: Ptr("call-123"),
//	    },
//	    Content: &ChatMessageContent{
//	        ContentStr: Ptr("Result: 30"),
//	    },
//	}
//	responsesMsg := chatToolMsg.ToResponsesToolMessage()
func (cm *ChatMessage) ToResponsesToolMessage() *ResponsesMessage {
	if cm == nil || cm.ChatToolMessage == nil {
		return nil
	}

	msgType := ResponsesMessageTypeFunctionCallOutput

	respMsg := &ResponsesMessage{
		Type: &msgType,
		ResponsesToolMessage: &ResponsesToolMessage{
			CallID: cm.ChatToolMessage.ToolCallID,
		},
	}

	// Extract output from content
	if cm.Content != nil {
		if cm.Content.ContentStr != nil {
			output := *cm.Content.ContentStr
			respMsg.ResponsesToolMessage.Output = &ResponsesToolMessageOutputStruct{
				ResponsesToolCallOutputStr: &output,
			}
		} else if len(cm.Content.ContentBlocks) > 0 {
			// For structured content blocks, convert to ResponsesMessageContentBlock
			respBlocks := make([]ResponsesMessageContentBlock, len(cm.Content.ContentBlocks))
			for i, block := range cm.Content.ContentBlocks {
				respBlocks[i] = ResponsesMessageContentBlock{
					Type:         ResponsesMessageContentBlockType(block.Type),
					Text:         block.Text,
					CacheControl: block.CacheControl,
				}

				// Map image
				if block.ImageURLStruct != nil {
					respBlocks[i].ResponsesInputMessageContentBlockImage = &ResponsesInputMessageContentBlockImage{
						ImageURL: &block.ImageURLStruct.URL,
						Detail:   block.ImageURLStruct.Detail,
					}
				}

				// Map file
				if block.File != nil {
					respBlocks[i].FileID = block.File.FileID
					respBlocks[i].ResponsesInputMessageContentBlockFile = &ResponsesInputMessageContentBlockFile{
						FileData: block.File.FileData,
						Filename: block.File.Filename,
						FileType: block.File.FileType,
					}
				}

				// Map audio
				if block.InputAudio != nil {
					format := ""
					if block.InputAudio.Format != nil {
						format = *block.InputAudio.Format
					}
					respBlocks[i].Audio = &ResponsesInputMessageContentBlockAudio{
						Data:   block.InputAudio.Data,
						Format: format,
					}
				}
			}
			respMsg.ResponsesToolMessage.Output = &ResponsesToolMessageOutputStruct{
				ResponsesFunctionToolCallOutputBlocks: respBlocks,
			}
		}
	}

	return respMsg
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
func (tc *ResponsesToolChoice) ToChatToolChoice() *ChatToolChoice {
	if tc == nil {
		return &ChatToolChoice{}
	}

	ctc := &ChatToolChoice{}

	// Handle string choice
	if tc.ResponsesToolChoiceStr != nil {
		ctc.ChatToolChoiceStr = tc.ResponsesToolChoiceStr
		return ctc
	}

	// Handle structured choice
	if tc.ResponsesToolChoiceStruct != nil {
		ctc.ChatToolChoiceStruct = &ChatToolChoiceStruct{
			Type: ChatToolChoiceType(tc.ResponsesToolChoiceStruct.Type),
		}

		// Handle mode-based choices (none, auto, required)
		if tc.ResponsesToolChoiceStruct.Mode != nil {
			switch *tc.ResponsesToolChoiceStruct.Mode {
			case "none":
				ctc.ChatToolChoiceStruct.Type = ChatToolChoiceTypeNone
			case "auto":
				ctc.ChatToolChoiceStruct.Type = ChatToolChoiceTypeAny
			case "required":
				ctc.ChatToolChoiceStruct.Type = ChatToolChoiceTypeRequired
			}
		}

		// Handle function choice
		if tc.ResponsesToolChoiceStruct.Type == ResponsesToolChoiceTypeFunction && tc.ResponsesToolChoiceStruct.Name != nil {
			ctc.ChatToolChoiceStruct.Function = ChatToolChoiceFunction{
				Name: *tc.ResponsesToolChoiceStruct.Name,
			}
		}

		// Handle custom choice
		if tc.ResponsesToolChoiceStruct.Type == ResponsesToolChoiceTypeCustom && tc.ResponsesToolChoiceStruct.Name != nil {
			ctc.ChatToolChoiceStruct.Custom = ChatToolChoiceCustom{
				Name: *tc.ResponsesToolChoiceStruct.Name,
			}
		}

		// Handle allowed tools
		if len(tc.ResponsesToolChoiceStruct.Tools) > 0 {
			ctc.ChatToolChoiceStruct.Type = ChatToolChoiceTypeAllowedTools
			tools := make([]ChatToolChoiceAllowedToolsTool, len(tc.ResponsesToolChoiceStruct.Tools))
			for i, tool := range tc.ResponsesToolChoiceStruct.Tools {
				tools[i] = ChatToolChoiceAllowedToolsTool{
					Type: tool.Type,
				}
				if tool.Name != nil {
					tools[i].Function = ChatToolChoiceFunction{Name: *tool.Name}
				}
			}
			// Copy the mode if present, otherwise default to "auto"
			mode := "auto"
			if tc.ResponsesToolChoiceStruct.Mode != nil && *tc.ResponsesToolChoiceStruct.Mode != "" {
				mode = *tc.ResponsesToolChoiceStruct.Mode
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
				ID:     Ptr("fc_" + GetRandomString(50)),
				Type:   &messageType,
				Role:   Ptr(ResponsesInputMessageRoleAssistant),
				Status: Ptr("completed"),
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
		role = "" // tool call output messages don't include a role field
	case ChatMessageRoleDeveloper:
		role = ResponsesInputMessageRoleDeveloper
	}

	rm := ResponsesMessage{
		Type: &messageType,
	}

	if role != "" {
		rm.Role = &role
	}

	if role == ResponsesInputMessageRoleAssistant && messageType != ResponsesMessageTypeFunctionCallOutput {
		rm.ID = Ptr("msg_" + GetRandomString(50))
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
	} else if cm.Content != nil && cm.Content.ContentStr != nil {
		// Convert regular string content (if input message then ContentStr, else ContentBlocks)
		// Skip setting content for function_call_output - content should only be in output field
		if messageType == ResponsesMessageTypeFunctionCallOutput {
			// Don't set content for function_call_output - it will be set in ResponsesToolMessage.Output
		} else if cm.Role == ChatMessageRoleAssistant {
			rm.Content = &ResponsesMessageContent{
				ContentBlocks: []ResponsesMessageContentBlock{
					{
						Type: ResponsesOutputMessageContentTypeText,
						Text: cm.Content.ContentStr,
						ResponsesOutputMessageContentText: &ResponsesOutputMessageContentText{
							LogProbs:    []ResponsesOutputMessageContentTextLogProb{},
							Annotations: []ResponsesOutputMessageContentTextAnnotation{},
						},
					},
				},
			}

			rm.Status = Ptr("completed")
		} else {
			rm.Content = &ResponsesMessageContent{
				ContentStr: cm.Content.ContentStr,
			}
		}
	} else if cm.Content != nil && cm.Content.ContentBlocks != nil {
		// Convert content blocks
		// Skip setting content blocks for function_call_output
		if messageType == ResponsesMessageTypeFunctionCallOutput {
			// Don't set content for function_call_output - it will be set in ResponsesToolMessage.Output
		} else {
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
	}

	// Handle tool messages
	if cm.ChatToolMessage != nil {
		rm.ResponsesToolMessage = &ResponsesToolMessage{}
		if cm.ChatToolMessage.ToolCallID != nil {
			rm.ResponsesToolMessage.CallID = cm.ChatToolMessage.ToolCallID
		}

		// If tool output content exists, add it to function_call_output
		// For function_call_output, get content from cm.Content since rm.Content is not set
		if messageType == ResponsesMessageTypeFunctionCallOutput && cm.Content != nil {
			// Prefer ContentStr if present
			if cm.Content.ContentStr != nil && *cm.Content.ContentStr != "" {
				rm.ResponsesToolMessage.Output = &ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: cm.Content.ContentStr,
				}
			} else if len(cm.Content.ContentBlocks) > 0 {
				// For structured content blocks, convert to ResponsesMessageContentBlock
				respBlocks := make([]ResponsesMessageContentBlock, len(cm.Content.ContentBlocks))
				for i, block := range cm.Content.ContentBlocks {
					respBlocks[i] = ResponsesMessageContentBlock{
						Type:         ResponsesMessageContentBlockType(block.Type),
						Text:         block.Text,
						CacheControl: block.CacheControl,
					}

					// Map image
					if block.ImageURLStruct != nil {
						respBlocks[i].ResponsesInputMessageContentBlockImage = &ResponsesInputMessageContentBlockImage{
							ImageURL: &block.ImageURLStruct.URL,
							Detail:   block.ImageURLStruct.Detail,
						}
					}

					// Map file
					if block.File != nil {
						respBlocks[i].FileID = block.File.FileID
						respBlocks[i].ResponsesInputMessageContentBlockFile = &ResponsesInputMessageContentBlockFile{
							FileData: block.File.FileData,
							Filename: block.File.Filename,
							FileType: block.File.FileType,
						}
					}

					// Map audio
					if block.InputAudio != nil {
						format := ""
						if block.InputAudio.Format != nil {
							format = *block.InputAudio.Format
						}
						respBlocks[i].Audio = &ResponsesInputMessageContentBlockAudio{
							Data:   block.InputAudio.Data,
							Format: format,
						}
					}
				}
				rm.ResponsesToolMessage.Output = &ResponsesToolMessageOutputStruct{
					ResponsesFunctionToolCallOutputBlocks: respBlocks,
				}
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
					if rm.ResponsesToolMessage.Output != nil {
						if rm.Content == nil {
							rm.Content = &ResponsesMessageContent{}
						}
						// If Content is not already set, extract from ResponsesFunctionToolCallOutput
						if rm.Content.ContentStr == nil && rm.Content.ContentBlocks == nil {
							if rm.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
								rm.Content.ContentStr = rm.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
							} else if rm.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks != nil {
								rm.Content.ContentBlocks = rm.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks
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
			if rm.Content.ContentStr != nil ||
				(len(rm.Content.ContentBlocks) == 1 &&
					(rm.Content.ContentBlocks[0].Type == ResponsesInputMessageContentBlockTypeText || rm.Content.ContentBlocks[0].Type == ResponsesOutputMessageContentTypeText)) {
				if rm.Content.ContentStr != nil {
					cm.Content = &ChatMessageContent{
						ContentStr: rm.Content.ContentStr,
					}
				} else {
					cm.Content = &ChatMessageContent{
						ContentStr: rm.Content.ContentBlocks[0].Text,
					}
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
						chatBlockType = ChatContentBlockTypeFile // "input_file" -> "file"
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
				cm.Content = &ChatMessageContent{
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

func (cu *BifrostLLMUsage) ToResponsesResponseUsage() *ResponsesResponseUsage {
	if cu == nil {
		return nil
	}

	usage := &ResponsesResponseUsage{
		InputTokens:  cu.PromptTokens,
		OutputTokens: cu.CompletionTokens,
		TotalTokens:  cu.TotalTokens,
		Cost:         cu.Cost,
	}

	if cu.PromptTokensDetails != nil {
		usage.InputTokensDetails = &ResponsesResponseInputTokens{
			TextTokens:   cu.PromptTokensDetails.TextTokens,
			AudioTokens:  cu.PromptTokensDetails.AudioTokens,
			ImageTokens:  cu.PromptTokensDetails.ImageTokens,
			CachedTokens: cu.PromptTokensDetails.CachedTokens,
		}
	}
	if cu.CompletionTokensDetails != nil {
		usage.OutputTokensDetails = &ResponsesResponseOutputTokens{
			TextTokens:               cu.CompletionTokensDetails.TextTokens,
			AcceptedPredictionTokens: cu.CompletionTokensDetails.AcceptedPredictionTokens,
			AudioTokens:              cu.CompletionTokensDetails.AudioTokens,
			ReasoningTokens:          cu.CompletionTokensDetails.ReasoningTokens,
			RejectedPredictionTokens: cu.CompletionTokensDetails.RejectedPredictionTokens,
			CitationTokens:           cu.CompletionTokensDetails.CitationTokens,
			NumSearchQueries:         cu.CompletionTokensDetails.NumSearchQueries,
			CachedTokens:             cu.CompletionTokensDetails.CachedTokens,
		}
	}

	return usage
}

func (ru *ResponsesResponseUsage) ToBifrostLLMUsage() *BifrostLLMUsage {
	if ru == nil {
		return nil
	}

	usage := &BifrostLLMUsage{
		PromptTokens:     ru.InputTokens,
		CompletionTokens: ru.OutputTokens,
		TotalTokens:      ru.TotalTokens,
		Cost:             ru.Cost,
	}

	if ru.InputTokensDetails != nil {
		usage.PromptTokensDetails = &ChatPromptTokensDetails{
			TextTokens:   ru.InputTokensDetails.TextTokens,
			AudioTokens:  ru.InputTokensDetails.AudioTokens,
			ImageTokens:  ru.InputTokensDetails.ImageTokens,
			CachedTokens: ru.InputTokensDetails.CachedTokens,
		}
	}
	if ru.OutputTokensDetails != nil {
		usage.CompletionTokensDetails = &ChatCompletionTokensDetails{
			TextTokens:               ru.OutputTokensDetails.TextTokens,
			AcceptedPredictionTokens: ru.OutputTokensDetails.AcceptedPredictionTokens,
			AudioTokens:              ru.OutputTokensDetails.AudioTokens,
			ReasoningTokens:          ru.OutputTokensDetails.ReasoningTokens,
			RejectedPredictionTokens: ru.OutputTokensDetails.RejectedPredictionTokens,
			CitationTokens:           ru.OutputTokensDetails.CitationTokens,
			NumSearchQueries:         ru.OutputTokensDetails.NumSearchQueries,
			CachedTokens:             ru.OutputTokensDetails.CachedTokens,
		}
	}

	return usage
}

// =============================================================================
// REQUEST CONVERSION METHODS
// =============================================================================

// ToResponsesRequest converts a BifrostChatRequest to BifrostResponsesRequest format
func (cr *BifrostChatRequest) ToResponsesRequest() *BifrostResponsesRequest {
	if cr == nil {
		return &BifrostResponsesRequest{}
	}

	brr := &BifrostResponsesRequest{
		Provider:  cr.Provider,
		Model:     cr.Model,
		Fallbacks: cr.Fallbacks, // Copy fallbacks as-is
	}

	// Convert Input messages using existing ChatMessage.ToResponsesMessages()
	var allResponsesMessages []ResponsesMessage
	for _, chatMsg := range cr.Input {
		responsesMessages := chatMsg.ToResponsesMessages()
		allResponsesMessages = append(allResponsesMessages, responsesMessages...)
	}
	brr.Input = allResponsesMessages

	// Convert Parameters
	if cr.Params != nil {
		brr.Params = &ResponsesParameters{
			// Map common fields
			ParallelToolCalls: cr.Params.ParallelToolCalls,
			PromptCacheKey:    cr.Params.PromptCacheKey,
			SafetyIdentifier:  cr.Params.SafetyIdentifier,
			ServiceTier:       cr.Params.ServiceTier,
			Store:             cr.Params.Store,
			Temperature:       cr.Params.Temperature,
			TopLogProbs:       cr.Params.TopLogProbs,
			TopP:              cr.Params.TopP,
			ExtraParams:       cr.Params.ExtraParams,

			// Map specific fields
			MaxOutputTokens: cr.Params.MaxCompletionTokens, // max_completion_tokens -> max_output_tokens
			Metadata:        cr.Params.Metadata,
		}

		// Convert StreamOptions
		if cr.Params.StreamOptions != nil {
			brr.Params.StreamOptions = &ResponsesStreamOptions{
				IncludeObfuscation: cr.Params.StreamOptions.IncludeObfuscation,
			}
		}

		// Convert Tools using existing ChatTool.ToResponsesTool()
		if len(cr.Params.Tools) > 0 {
			responsesTools := make([]ResponsesTool, 0, len(cr.Params.Tools))
			for _, chatTool := range cr.Params.Tools {
				responsesTool := chatTool.ToResponsesTool()
				responsesTools = append(responsesTools, *responsesTool)
			}
			brr.Params.Tools = responsesTools
		}

		// Convert ToolChoice using existing ChatToolChoice.ToResponsesToolChoice()
		if cr.Params.ToolChoice != nil {
			responsesToolChoice := cr.Params.ToolChoice.ToResponsesToolChoice()
			brr.Params.ToolChoice = responsesToolChoice
		}

		// Handle Reasoning from reasoning_effort
		if cr.Params.Reasoning != nil && (cr.Params.Reasoning.Effort != nil || cr.Params.Reasoning.MaxTokens != nil) {
			brr.Params.Reasoning = &ResponsesParametersReasoning{
				Effort:    cr.Params.Reasoning.Effort,
				MaxTokens: cr.Params.Reasoning.MaxTokens,
			}
		}

		// Handle Verbosity
		if cr.Params.Verbosity != nil {
			if brr.Params.Text == nil {
				brr.Params.Text = &ResponsesTextConfig{}
			}
			brr.Params.Text.Verbosity = cr.Params.Verbosity
		}
	}

	brr.RawRequestBody = cr.RawRequestBody

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

		// Handle Reasoning from Reasoning
		if brr.Params.Reasoning != nil {
			bcr.Params.Reasoning = &ChatReasoning{
				Effort:    brr.Params.Reasoning.Effort,
				MaxTokens: brr.Params.Reasoning.MaxTokens,
			}
		}

		// Handle Verbosity from Text config
		if brr.Params.Text != nil && brr.Params.Text.Verbosity != nil {
			bcr.Params.Verbosity = brr.Params.Text.Verbosity
		}
	}

	bcr.RawRequestBody = brr.RawRequestBody

	return bcr
}

// =============================================================================
// RESPONSE CONVERSION METHODS
// =============================================================================

// ToBifrostResponsesResponse converts the BifrostChatResponse to BifrostResponsesResponse format
// This converts Chat-style fields (Choices) to Responses API format
func (cr *BifrostChatResponse) ToBifrostResponsesResponse() *BifrostResponsesResponse {
	if cr == nil {
		return nil
	}

	// Create new BifrostResponsesResponse from Chat fields
	responsesResp := &BifrostResponsesResponse{
		ID:            Ptr(cr.ID),
		CreatedAt:     cr.Created,
		Model:         cr.Model,
		Citations:     cr.Citations,
		SearchResults: cr.SearchResults,
		Videos:        cr.Videos,
	}

	// Convert Choices to Output messages
	var outputMessages []ResponsesMessage
	for _, choice := range cr.Choices {
		if choice.ChatNonStreamResponseChoice != nil && choice.ChatNonStreamResponseChoice.Message != nil {
			// Convert ChatMessage to ResponsesMessages
			responsesMessages := choice.ChatNonStreamResponseChoice.Message.ToResponsesMessages()
			outputMessages = append(outputMessages, responsesMessages...)
		}
	}

	if len(outputMessages) > 0 {
		responsesResp.Output = outputMessages
	}

	// Convert Usage if needed
	if cr.Usage != nil {
		responsesResp.Usage = cr.Usage.ToResponsesResponseUsage()
	}

	// Copy other relevant fields
	responsesResp.ExtraFields = cr.ExtraFields
	responsesResp.ExtraFields.RequestType = ResponsesRequest

	return responsesResp
}

// ToBifrostChatResponse converts a BifrostResponsesResponse to BifrostChatResponse format
// This converts Responses API format to Chat-style fields (Choices)
func (responsesResp *BifrostResponsesResponse) ToBifrostChatResponse() *BifrostChatResponse {
	if responsesResp == nil {
		return nil
	}

	// Create new BifrostChatResponse from Responses fields
	chatResp := &BifrostChatResponse{
		Created:       responsesResp.CreatedAt,
		Object:        "chat.completion",
		Model:         responsesResp.Model,
		Citations:     responsesResp.Citations,
		SearchResults: responsesResp.SearchResults,
		Videos:        responsesResp.Videos,
	}

	// Create Choices from ResponsesResponse
	if len(responsesResp.Output) > 0 {
		// Convert ResponsesMessages back to ChatMessages
		chatMessages := ToChatMessages(responsesResp.Output)

		// Create choices from chat messages
		choices := make([]BifrostResponseChoice, 0, len(chatMessages))
		for i, chatMsg := range chatMessages {
			choice := BifrostResponseChoice{
				Index: i,
				ChatNonStreamResponseChoice: &ChatNonStreamResponseChoice{
					Message: &chatMsg,
				},
			}
			choices = append(choices, choice)
		}

		chatResp.Choices = choices
	}

	// Convert Usage if needed
	if responsesResp.Usage != nil {
		// Map Responses usage to Chat usage
		chatResp.Usage = responsesResp.Usage.ToBifrostLLMUsage()
	}

	// Copy other relevant fields
	chatResp.ExtraFields = responsesResp.ExtraFields
	chatResp.ExtraFields.RequestType = ChatCompletionRequest
	chatResp.ExtraFields.Provider = responsesResp.ExtraFields.Provider

	return chatResp
}

// ChatToResponsesStreamState tracks state during Chat-to-Responses streaming conversion
type ChatToResponsesStreamState struct {
	ToolArgumentBuffers   map[string]string // Maps tool call ID to accumulated argument JSON
	ItemIDs               map[string]string // Maps tool call ID to item ID
	ToolCallNames         map[string]string // Maps tool call ID to tool name
	ToolCallIndexToID     map[uint16]string // Maps tool call index to tool call ID (for lookups when ID is missing)
	MessageID             *string           // Message ID from first chunk
	Model                 *string           // Model name
	CreatedAt             int               // Timestamp for created_at consistency
	HasEmittedCreated     bool              // Whether we've emitted response.created
	HasEmittedInProgress  bool              // Whether we've emitted response.in_progress
	TextItemAdded         bool              // Whether text item has been added
	TextItemClosed        bool              // Whether text item has been closed
	TextItemHasContent    bool              // Whether text item has received any content deltas
	CurrentOutputIndex    int               // Current output index counter
	ToolCallOutputIndices map[string]int    // Maps tool call ID to output index
	SequenceNumber        int               // Monotonic sequence number across all chunks
}

// chatToResponsesStreamStatePool provides a pool for ChatToResponsesStreamState objects.
var chatToResponsesStreamStatePool = sync.Pool{
	New: func() interface{} {
		return &ChatToResponsesStreamState{
			ToolArgumentBuffers:   make(map[string]string),
			ItemIDs:               make(map[string]string),
			ToolCallNames:         make(map[string]string),
			ToolCallIndexToID:     make(map[uint16]string),
			CreatedAt:             int(time.Now().Unix()),
			CurrentOutputIndex:    0,
			ToolCallOutputIndices: make(map[string]int),
			SequenceNumber:        0,
			HasEmittedCreated:     false,
			HasEmittedInProgress:  false,
			TextItemAdded:         false,
			TextItemClosed:        false,
			TextItemHasContent:    false,
		}
	},
}

// AcquireChatToResponsesStreamState gets a ChatToResponsesStreamState from the pool.
func AcquireChatToResponsesStreamState() *ChatToResponsesStreamState {
	state := chatToResponsesStreamStatePool.Get().(*ChatToResponsesStreamState)
	// Clear maps (they're already initialized from New or previous flush)
	// Only initialize if nil (shouldn't happen, but defensive)
	if state.ToolArgumentBuffers == nil {
		state.ToolArgumentBuffers = make(map[string]string)
	} else {
		clear(state.ToolArgumentBuffers)
	}
	if state.ItemIDs == nil {
		state.ItemIDs = make(map[string]string)
	} else {
		clear(state.ItemIDs)
	}
	if state.ToolCallNames == nil {
		state.ToolCallNames = make(map[string]string)
	} else {
		clear(state.ToolCallNames)
	}
	if state.ToolCallIndexToID == nil {
		state.ToolCallIndexToID = make(map[uint16]string)
	} else {
		clear(state.ToolCallIndexToID)
	}
	if state.ToolCallOutputIndices == nil {
		state.ToolCallOutputIndices = make(map[string]int)
	} else {
		clear(state.ToolCallOutputIndices)
	}
	// Reset other fields
	state.CurrentOutputIndex = 0
	state.MessageID = nil
	state.Model = nil
	state.CreatedAt = int(time.Now().Unix())
	state.HasEmittedCreated = false
	state.HasEmittedInProgress = false
	state.TextItemAdded = false
	state.TextItemClosed = false
	state.TextItemHasContent = false
	state.SequenceNumber = 0
	return state
}

// ReleaseChatToResponsesStreamState returns a ChatToResponsesStreamState to the pool.
func ReleaseChatToResponsesStreamState(state *ChatToResponsesStreamState) {
	if state != nil {
		// Clear maps before returning to pool
		if state.ToolArgumentBuffers != nil {
			clear(state.ToolArgumentBuffers)
		}
		if state.ItemIDs != nil {
			clear(state.ItemIDs)
		}
		if state.ToolCallNames != nil {
			clear(state.ToolCallNames)
		}
		if state.ToolCallIndexToID != nil {
			clear(state.ToolCallIndexToID)
		}
		if state.ToolCallOutputIndices != nil {
			clear(state.ToolCallOutputIndices)
		}
		// Reset other fields
		state.CurrentOutputIndex = 0
		state.MessageID = nil
		state.Model = nil
		state.CreatedAt = int(time.Now().Unix())
		state.HasEmittedCreated = false
		state.HasEmittedInProgress = false
		state.TextItemAdded = false
		state.TextItemClosed = false
		state.TextItemHasContent = false
		state.SequenceNumber = 0
		chatToResponsesStreamStatePool.Put(state)
	}
}

// ToBifrostResponsesStreamResponse converts the BifrostChatResponse from Chat streaming format to Responses streaming format
// This converts Chat stream chunks (Choices with Deltas) to BifrostResponsesStreamResponse format
// Returns a slice of responses to support cases where a single event produces multiple responses
func (cr *BifrostChatResponse) ToBifrostResponsesStreamResponse(state *ChatToResponsesStreamState) []*BifrostResponsesStreamResponse {
	if cr == nil || state == nil {
		return nil
	}

	// If no choices to convert, return early
	if len(cr.Choices) == 0 {
		return nil
	}

	// Convert first streaming choice to BifrostResponsesStreamResponse
	// Note: Chat API typically has one choice per chunk in streaming
	choice := cr.Choices[0]
	if choice.ChatStreamResponseChoice == nil || choice.ChatStreamResponseChoice.Delta == nil {
		return nil
	}

	delta := choice.ChatStreamResponseChoice.Delta
	var responses []*BifrostResponsesStreamResponse

	// Store message ID and model from first chunk
	if state.MessageID == nil && cr.ID != "" {
		state.MessageID = &cr.ID
	}
	if state.Model == nil && cr.Model != "" {
		state.Model = &cr.Model
	}

	// Emit lifecycle events on first chunk with role
	if delta.Role != nil && !state.HasEmittedCreated {
		// Emit response.created
		response := &BifrostResponsesResponse{
			ID:        state.MessageID,
			CreatedAt: state.CreatedAt,
		}
		responses = append(responses, &BifrostResponsesStreamResponse{
			Type:           ResponsesStreamResponseTypeCreated,
			SequenceNumber: state.SequenceNumber,
			Response:       response,
			ExtraFields:    cr.ExtraFields,
		})
		state.SequenceNumber++
		state.HasEmittedCreated = true

		// Emit response.in_progress
		response = &BifrostResponsesResponse{
			ID:        state.MessageID,
			CreatedAt: state.CreatedAt,
		}
		responses = append(responses, &BifrostResponsesStreamResponse{
			Type:           ResponsesStreamResponseTypeInProgress,
			SequenceNumber: state.SequenceNumber,
			Response:       response,
			ExtraFields:    cr.ExtraFields,
		})
		state.SequenceNumber++
		state.HasEmittedInProgress = true
	}

	// Handle different types of streaming content
	hasContent := delta.Content != nil && *delta.Content != ""
	hasReasoning := delta.Reasoning != nil && *delta.Reasoning != ""

	// Create output items if we have content OR reasoning (for reasoning-only models)
	if hasContent || (hasReasoning && !state.TextItemAdded) {
		// Text content delta (or reasoning-only response)
		if !state.TextItemAdded {
			// Add text item if not already added
			outputIndex := 0
			// Generate stable ID for text item
			var itemID string
			if state.MessageID == nil {
				itemID = fmt.Sprintf("item_%d", outputIndex)
			} else {
				itemID = fmt.Sprintf("msg_%s_item_%d", *state.MessageID, outputIndex)
			}
			state.ItemIDs["text"] = itemID

			messageType := ResponsesMessageTypeMessage
			role := ResponsesInputMessageRoleAssistant

			item := &ResponsesMessage{
				ID:   &itemID,
				Type: &messageType,
				Role: &role,
				Content: &ResponsesMessageContent{
					ContentBlocks: []ResponsesMessageContentBlock{},
				},
			}

			responses = append(responses, &BifrostResponsesStreamResponse{
				Type:           ResponsesStreamResponseTypeOutputItemAdded,
				SequenceNumber: state.SequenceNumber,
				OutputIndex:    Ptr(outputIndex),
				ContentIndex:   Ptr(0),
				Item:           item,
				ExtraFields:    cr.ExtraFields,
			})
			state.SequenceNumber++
			state.TextItemAdded = true

			// Emit content_part.added with empty output_text part
			emptyText := ""
			part := &ResponsesMessageContentBlock{
				Type: ResponsesOutputMessageContentTypeText,
				Text: &emptyText,
				ResponsesOutputMessageContentText: &ResponsesOutputMessageContentText{
					LogProbs:    []ResponsesOutputMessageContentTextLogProb{},
					Annotations: []ResponsesOutputMessageContentTextAnnotation{},
				},
			}
			responses = append(responses, &BifrostResponsesStreamResponse{
				Type:           ResponsesStreamResponseTypeContentPartAdded,
				SequenceNumber: state.SequenceNumber,
				OutputIndex:    Ptr(outputIndex),
				ContentIndex:   Ptr(0),
				ItemID:         &itemID,
				Part:           part,
				ExtraFields:    cr.ExtraFields,
			})
			state.SequenceNumber++
		}

		// Emit text delta - at least one is required for lifecycle validation
		// Even for reasoning-only responses, we emit an empty delta on the first chunk
		if hasContent || (!state.TextItemHasContent && (hasReasoning || hasContent)) {
			itemID := state.ItemIDs["text"]

			var contentDelta string
			if hasContent {
				contentDelta = *delta.Content
			} else {
				// For reasoning-only responses, emit empty delta on first chunk
				contentDelta = ""
			}

			response := &BifrostResponsesStreamResponse{
				Type:           ResponsesStreamResponseTypeOutputTextDelta,
				SequenceNumber: state.SequenceNumber,
				OutputIndex:    Ptr(0),
				ContentIndex:   Ptr(0),
				Delta:          &contentDelta,
				LogProbs:       []ResponsesOutputMessageContentTextLogProb{},
				ExtraFields:    cr.ExtraFields,
			}
			if itemID != "" {
				response.ItemID = &itemID
			}
			responses = append(responses, response)
			state.SequenceNumber++
			state.TextItemHasContent = true
		}
	}

	if len(delta.ToolCalls) > 0 {
		// Tool call delta - handle function call arguments
		toolCall := delta.ToolCalls[0] // Take first tool call
		contentIndex := 1              // Tool calls use content_index:1

		// Determine tool call ID: use ID if present, otherwise look up by index
		var toolCallID string
		if toolCall.ID != nil && *toolCall.ID != "" {
			toolCallID = *toolCall.ID
		} else {
			// Look up ID by index for subsequent chunks that don't include the ID
			if id, exists := state.ToolCallIndexToID[toolCall.Index]; exists {
				toolCallID = id
			} else {
				// No ID and no mapping found - skip this chunk
				// This can happen if the stream is malformed or out of order
				return responses
			}
		}

		// Check if this is a new tool call (only when ID is present)
		if toolCall.ID != nil && *toolCall.ID != "" {
			if _, exists := state.ToolCallOutputIndices[toolCallID]; !exists {
				// Close text item if still open and has content
				if state.TextItemAdded && !state.TextItemClosed && state.TextItemHasContent {
					outputIndex := 0
					itemID := state.ItemIDs["text"]

					// Emit output_text.done (without accumulated text, just the event)
					emptyText := ""
					responses = append(responses, &BifrostResponsesStreamResponse{
						Type:           ResponsesStreamResponseTypeOutputTextDone,
						SequenceNumber: state.SequenceNumber,
						OutputIndex:    Ptr(outputIndex),
						ContentIndex:   Ptr(0),
						ItemID:         &itemID,
						Text:           &emptyText,
						LogProbs:       []ResponsesOutputMessageContentTextLogProb{},
						ExtraFields:    cr.ExtraFields,
					})
					state.SequenceNumber++

					// Emit content_part.done
					part := &ResponsesMessageContentBlock{
						Type: ResponsesOutputMessageContentTypeText,
						Text: &emptyText,
						ResponsesOutputMessageContentText: &ResponsesOutputMessageContentText{
							LogProbs:    []ResponsesOutputMessageContentTextLogProb{},
							Annotations: []ResponsesOutputMessageContentTextAnnotation{},
						},
					}
					responses = append(responses, &BifrostResponsesStreamResponse{
						Type:           ResponsesStreamResponseTypeContentPartDone,
						SequenceNumber: state.SequenceNumber,
						OutputIndex:    Ptr(outputIndex),
						ContentIndex:   Ptr(0),
						ItemID:         &itemID,
						Part:           part,
						ExtraFields:    cr.ExtraFields,
					})
					state.SequenceNumber++

					// Emit output_item.done
					statusCompleted := "completed"
					messageType := ResponsesMessageTypeMessage
					role := ResponsesInputMessageRoleAssistant
					doneItem := &ResponsesMessage{
						Type:   &messageType,
						Role:   &role,
						Status: &statusCompleted,
						Content: &ResponsesMessageContent{
							ContentBlocks: []ResponsesMessageContentBlock{},
						},
					}
					if itemID != "" {
						doneItem.ID = &itemID
					}
					responses = append(responses, &BifrostResponsesStreamResponse{
						Type:           ResponsesStreamResponseTypeOutputItemDone,
						SequenceNumber: state.SequenceNumber,
						OutputIndex:    Ptr(outputIndex),
						ContentIndex:   Ptr(0),
						Item:           doneItem,
						ExtraFields:    cr.ExtraFields,
					})
					state.SequenceNumber++
					state.TextItemClosed = true
				}

				// Assign new output index for tool call
				outputIndex := state.CurrentOutputIndex
				if outputIndex == 0 {
					outputIndex = 1 // Skip 0 if text is using it
				}
				state.CurrentOutputIndex = outputIndex + 1
				state.ToolCallOutputIndices[toolCallID] = outputIndex

				// Store tool call info and index mapping
				state.ItemIDs[toolCallID] = toolCallID
				state.ToolCallIndexToID[toolCall.Index] = toolCallID
				if toolCall.Function.Name != nil {
					state.ToolCallNames[toolCallID] = *toolCall.Function.Name
				}

				// Initialize argument buffer
				state.ToolArgumentBuffers[toolCallID] = ""

				// Emit output_item.added for function call
				statusInProgress := "in_progress"
				item := &ResponsesMessage{
					ID:     &toolCallID,
					Type:   Ptr(ResponsesMessageTypeFunctionCall),
					Status: &statusInProgress,
					ResponsesToolMessage: &ResponsesToolMessage{
						CallID:    &toolCallID,
						Name:      toolCall.Function.Name,
						Arguments: Ptr(""), // Arguments will be filled by deltas
					},
				}

				responses = append(responses, &BifrostResponsesStreamResponse{
					Type:           ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: state.SequenceNumber,
					OutputIndex:    Ptr(outputIndex),
					ContentIndex:   Ptr(contentIndex),
					Item:           item,
					ExtraFields:    cr.ExtraFields,
				})
				state.SequenceNumber++
			}
		}

		// Accumulate and emit function call arguments delta
		// This works for both chunks with ID and chunks without ID (using looked-up ID)
		if toolCall.Function.Arguments != "" {
			outputIndex := state.ToolCallOutputIndices[toolCallID]
			state.ToolArgumentBuffers[toolCallID] += toolCall.Function.Arguments

			itemID := state.ItemIDs[toolCallID]
			response := &BifrostResponsesStreamResponse{
				Type:           ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
				SequenceNumber: state.SequenceNumber,
				OutputIndex:    Ptr(outputIndex),
				ContentIndex:   Ptr(contentIndex),
				Delta:          &toolCall.Function.Arguments,
				ExtraFields:    cr.ExtraFields,
			}
			if itemID != "" {
				response.ItemID = &itemID
			}
			responses = append(responses, response)
			state.SequenceNumber++
		}
	}

	if delta.Reasoning != nil && *delta.Reasoning != "" {
		// Reasoning/thought content delta (for models that support reasoning)
		response := &BifrostResponsesStreamResponse{
			Type:           ResponsesStreamResponseTypeReasoningSummaryTextDelta,
			SequenceNumber: state.SequenceNumber,
			OutputIndex:    Ptr(0),
			Delta:          delta.Reasoning,
			ExtraFields:    cr.ExtraFields,
		}
		responses = append(responses, response)
		state.SequenceNumber++
	}

	if delta.Refusal != nil && *delta.Refusal != "" {
		// Refusal delta
		response := &BifrostResponsesStreamResponse{
			Type:           ResponsesStreamResponseTypeRefusalDelta,
			SequenceNumber: state.SequenceNumber,
			OutputIndex:    Ptr(0),
			Refusal:        delta.Refusal,
			ExtraFields:    cr.ExtraFields,
		}
		responses = append(responses, response)
		state.SequenceNumber++
	}

	// Check if this is a completion chunk with finish_reason
	if choice.FinishReason != nil {
		// Close text item if still open (regardless of whether it has content, to support reasoning-only responses)
		if state.TextItemAdded && !state.TextItemClosed {
			outputIndex := 0
			itemID := state.ItemIDs["text"]

			// Emit output_text.done (without accumulated text, just the event)
			emptyText := ""
			responses = append(responses, &BifrostResponsesStreamResponse{
				Type:           ResponsesStreamResponseTypeOutputTextDone,
				SequenceNumber: state.SequenceNumber,
				OutputIndex:    Ptr(outputIndex),
				ContentIndex:   Ptr(0),
				ItemID:         &itemID,
				Text:           &emptyText,
				LogProbs:       []ResponsesOutputMessageContentTextLogProb{},
				ExtraFields:    cr.ExtraFields,
			})
			state.SequenceNumber++

			// Emit content_part.done
			part := &ResponsesMessageContentBlock{
				Type: ResponsesOutputMessageContentTypeText,
				Text: &emptyText,
				ResponsesOutputMessageContentText: &ResponsesOutputMessageContentText{
					LogProbs:    []ResponsesOutputMessageContentTextLogProb{},
					Annotations: []ResponsesOutputMessageContentTextAnnotation{},
				},
			}
			responses = append(responses, &BifrostResponsesStreamResponse{
				Type:           ResponsesStreamResponseTypeContentPartDone,
				SequenceNumber: state.SequenceNumber,
				OutputIndex:    Ptr(outputIndex),
				ContentIndex:   Ptr(0),
				ItemID:         &itemID,
				Part:           part,
				ExtraFields:    cr.ExtraFields,
			})
			state.SequenceNumber++

			// Emit output_item.done
			statusCompleted := "completed"
			messageType := ResponsesMessageTypeMessage
			role := ResponsesInputMessageRoleAssistant
			doneItem := &ResponsesMessage{
				Type:   &messageType,
				Role:   &role,
				Status: &statusCompleted,
				Content: &ResponsesMessageContent{
					ContentBlocks: []ResponsesMessageContentBlock{},
				},
			}
			if itemID != "" {
				doneItem.ID = &itemID
			}
			responses = append(responses, &BifrostResponsesStreamResponse{
				Type:           ResponsesStreamResponseTypeOutputItemDone,
				SequenceNumber: state.SequenceNumber,
				OutputIndex:    Ptr(outputIndex),
				ContentIndex:   Ptr(0),
				Item:           doneItem,
				ExtraFields:    cr.ExtraFields,
			})
			state.SequenceNumber++
			state.TextItemClosed = true
		}

		// Close any open tool call items and emit function_call_arguments.done
		for toolCallID, args := range state.ToolArgumentBuffers {
			if args != "" {
				outputIndex := state.ToolCallOutputIndices[toolCallID]
				itemID := state.ItemIDs[toolCallID]
				contentIndex := 1 // Tool calls use content_index:1
				argsCopy := args
				// Emit function_call_arguments.done with full arguments (no item field, just item_id and arguments)
				response := &BifrostResponsesStreamResponse{
					Type:           ResponsesStreamResponseTypeFunctionCallArgumentsDone,
					SequenceNumber: state.SequenceNumber,
					OutputIndex:    Ptr(outputIndex),
					ContentIndex:   Ptr(contentIndex),
					Arguments:      &argsCopy,
					ExtraFields:    cr.ExtraFields,
				}
				if itemID != "" {
					response.ItemID = &itemID
				}
				responses = append(responses, response)
				state.SequenceNumber++

				// Emit output_item.done for function call
				statusCompleted := "completed"
				messageType := ResponsesMessageTypeFunctionCall
				callName, hasName := state.ToolCallNames[toolCallID]
				var callNamePtr *string
				if hasName && callName != "" {
					callNamePtr = &callName
				}
				argsValue := args
				outputItemDone := &ResponsesMessage{
					Type:   &messageType,
					Status: &statusCompleted,
					ResponsesToolMessage: &ResponsesToolMessage{
						CallID:    &toolCallID,
						Name:      callNamePtr,
						Arguments: &argsValue,
					},
				}
				if itemID != "" {
					outputItemDone.ID = &itemID
				}
				responses = append(responses, &BifrostResponsesStreamResponse{
					Type:           ResponsesStreamResponseTypeOutputItemDone,
					SequenceNumber: state.SequenceNumber,
					OutputIndex:    Ptr(outputIndex),
					ContentIndex:   Ptr(contentIndex),
					Item:           outputItemDone,
					ExtraFields:    cr.ExtraFields,
				})
				state.SequenceNumber++
			}
		}

		// Emit response.completed
		var usage *ResponsesResponseUsage
		if cr.Usage != nil {
			usage = cr.Usage.ToResponsesResponseUsage()
		}

		response := &BifrostResponsesResponse{
			ID:        state.MessageID,
			CreatedAt: state.CreatedAt,
			Usage:     usage,
		}

		if state.Model != nil {
			response.Model = *state.Model
		}

		responses = append(responses, &BifrostResponsesStreamResponse{
			Type:           ResponsesStreamResponseTypeCompleted,
			SequenceNumber: state.SequenceNumber,
			Response:       response,
			ExtraFields:    cr.ExtraFields,
		})
		state.SequenceNumber++
	}

	// Set RequestType for all responses
	for _, resp := range responses {
		if resp != nil {
			resp.ExtraFields.RequestType = ResponsesStreamRequest
			// Copy other extra fields
			resp.SearchResults = cr.SearchResults
			resp.Videos = cr.Videos
			resp.Citations = cr.Citations
		}
	}

	return responses
}
