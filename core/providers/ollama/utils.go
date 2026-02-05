// Package ollama implements the Ollama provider using native Ollama APIs.
// This file contains utility functions for converting between Bifrost and Ollama formats.
package ollama

import (
	"encoding/json"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// convertMessagesToOllama converts Bifrost messages to Ollama format.
// Ollama has specific semantics for tool calls:
// - Tool calls only appear on assistant messages
// - Assistant messages with tool_calls are function invocation requests and must have NO content or images
// - Tool responses must be separate messages with role="tool" and tool_name set
// - Ollama correlates tool calls and responses by function name directly, not by opaque IDs

// NOTE: Ollama does not provide tool call IDs. When multiple calls to the same function occur
// in a single turn, tool responses are correlated by function name only. This is a lossy conversion
// but accurately reflects Ollama's native semantics. Bifrost allows toolCallId to be optional,
// so IDs are intentionally omitted. Do not generate synthetic tool call IDs.
func convertMessagesToOllama(messages []schemas.ChatMessage) []OllamaMessage {
	var ollamaMessages []OllamaMessage
	// Map ToolCallID to function name for tool message correlation
	// This allows us to convert Bifrost tool messages (which use ToolCallID) to Ollama format (which uses tool_name)
	toolCallIDToName := make(map[string]string)

	for _, msg := range messages {
		ollamaMsg := OllamaMessage{
			Role: mapRoleToOllama(msg.Role),
		}

		if ollamaMsg.Role == "" {
			continue // Skip unsupported roles
		}

		// Check if this is an assistant message with tool calls
		hasToolCalls := msg.Role == schemas.ChatMessageRoleAssistant && msg.ChatAssistantMessage != nil && msg.ChatAssistantMessage.ToolCalls != nil

		// Convert content - but NOT for assistant messages with tool_calls
		// In Ollama, assistant messages with tool_calls are function invocation requests
		// and must contain no content or images, exactly as shown in native /api/chat behavior
		if !hasToolCalls {
			ollamaMsg.Content, ollamaMsg.Images = convertContentToOllama(msg.Content)
		} else {
			// Assistant message with tool_calls: no content or images
			ollamaMsg.Content = ""
			ollamaMsg.Images = nil
		}

		// Handle tool calls - ONLY on assistant messages per Ollama semantics
		if hasToolCalls {
			// Filter out thinking placeholder tool calls before converting
			var realToolCalls []schemas.ChatAssistantMessageToolCall
			var thinkingContent *string
			for _, tc := range msg.ChatAssistantMessage.ToolCalls {
				// Check if this is a thinking placeholder
				if tc.Function.Name != nil && *tc.Function.Name == "_thinking_placeholder" {
					// Extract thinking from ExtraContent
					if tc.ExtraContent != nil {
						if ollamaData, ok := tc.ExtraContent["ollama"].(map[string]interface{}); ok {
							if thinking, ok := ollamaData["thinking"].(string); ok && thinking != "" {
								thinkingContent = &thinking
							}
						}
					}
					continue // Skip the placeholder tool call
				}
				// Extract thinking from tool call's ExtraContent if present
				if tc.ExtraContent != nil {
					if ollamaData, ok := tc.ExtraContent["ollama"].(map[string]interface{}); ok {
						if thinking, ok := ollamaData["thinking"].(string); ok && thinking != "" {
							thinkingContent = &thinking
						}
					}
				}
				realToolCalls = append(realToolCalls, tc)
				// Map ToolCallID to function name for later tool message correlation
				if tc.ID != nil && tc.Function.Name != nil {
					toolCallIDToName[*tc.ID] = *tc.Function.Name
				}
			}
			if len(realToolCalls) > 0 {
				ollamaMsg.ToolCalls = convertToolCallsToOllama(realToolCalls)
			}
			// Set thinking if we found it
			if thinkingContent != nil {
				ollamaMsg.Thinking = thinkingContent
			}
		}

		// Handle tool response messages - set tool_name and tool_call_id per Ollama semantics
		// Ollama uses tool_name (function name) to correlate, and also supports tool_call_id
		if msg.Role == schemas.ChatMessageRoleTool && msg.ChatToolMessage != nil {
			if msg.Name != nil {
				ollamaMsg.ToolName = msg.Name
			} else if msg.ChatToolMessage.ToolCallID != nil {
				// Try to map ToolCallID to function name from previous assistant messages
				if functionName, found := toolCallIDToName[*msg.ChatToolMessage.ToolCallID]; found {
					ollamaMsg.ToolName = &functionName
				}
			}
			// Set tool_call_id if available
			if msg.ChatToolMessage.ToolCallID != nil {
				ollamaMsg.ToolCallID = msg.ChatToolMessage.ToolCallID
			}
		}

		if ollamaMsg.Role == "tool" && ollamaMsg.ToolName == nil {
			continue // Skip invalid tool messages that would be silently ignored by Ollama
		}
		ollamaMessages = append(ollamaMessages, ollamaMsg)
	}

	return ollamaMessages
}

// NOTE: Ollama does not provide tool call IDs. When multiple calls to the same function occur
// in a single turn, tool responses are correlated by function name only. This is a lossy conversion
// but accurately reflects Ollama's native semantics. Bifrost allows toolCallId to be optional,
// so IDs are intentionally omitted. Do not generate synthetic tool call IDs.
func convertMessagesFromOllama(messages []OllamaMessage) []schemas.ChatMessage {
	var bifrostMessages []schemas.ChatMessage

	for _, msg := range messages {
		bifrostMsg := schemas.ChatMessage{
			Role: schemas.ChatMessageRole(msg.Role),
		}

		// Check if this is an assistant message with tool calls
		hasToolCalls := msg.Role == "assistant" && len(msg.ToolCalls) > 0

		// Set content - but NOT for assistant messages with tool_calls
		// In Ollama, assistant messages with tool_calls are function invocation requests
		// and contain no content or images
		if !hasToolCalls {
			bifrostMsg.Content = &schemas.ChatMessageContent{
				ContentStr: &msg.Content,
			}
		}
		// If hasToolCalls is true, Content remains nil (no content for function invocation requests)

		// Handle assistant messages with tool calls
		if hasToolCalls {
			var toolCalls []schemas.ChatAssistantMessageToolCall
			for _, tc := range msg.ToolCalls {
				args, _ := json.Marshal(tc.Function.Arguments)
				bifrostTC := schemas.ChatAssistantMessageToolCall{
					Index: uint16(tc.Function.Index),
					Type:  schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr(tc.Function.Name),
						Arguments: string(args),
					},
				}
				// Set tool call ID if provided by Ollama
				if tc.ID != "" {
					bifrostTC.ID = schemas.Ptr(tc.ID)
				}
				toolCalls = append(toolCalls, bifrostTC)
			}
			bifrostMsg.ChatAssistantMessage = &schemas.ChatAssistantMessage{
				ToolCalls: toolCalls,
			}
		}

		// Handle thinking content for assistant messages
		// Store thinking in the first tool call's ExtraContent (if tool calls exist) or create assistant message
		// This preserves thinking for passthrough scenarios
		if msg.Role == "assistant" && msg.Thinking != nil && *msg.Thinking != "" {
			if bifrostMsg.ChatAssistantMessage == nil {
				bifrostMsg.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
			}
			// Store thinking in the first tool call's ExtraContent if tool calls exist
			// Otherwise, we'll need to store it somewhere - but ChatAssistantMessage doesn't have ExtraContent
			// So we'll store it in the first tool call's ExtraContent, or create a dummy tool call if none exist
			if len(bifrostMsg.ChatAssistantMessage.ToolCalls) > 0 {
				if bifrostMsg.ChatAssistantMessage.ToolCalls[0].ExtraContent == nil {
					bifrostMsg.ChatAssistantMessage.ToolCalls[0].ExtraContent = make(map[string]interface{})
				}
				bifrostMsg.ChatAssistantMessage.ToolCalls[0].ExtraContent["ollama"] = map[string]interface{}{
					"thinking": *msg.Thinking,
				}
			} else {
				// No tool calls - create a dummy tool call to store thinking
				// This is a workaround since ChatAssistantMessage doesn't have ExtraContent
				bifrostMsg.ChatAssistantMessage.ToolCalls = []schemas.ChatAssistantMessageToolCall{
					{
						Index: 0,
						Type:  schemas.Ptr("function"),
						Function: schemas.ChatAssistantMessageToolCallFunction{
							Name:      schemas.Ptr("_thinking_placeholder"),
							Arguments: "{}",
						},
						ExtraContent: map[string]interface{}{
							"ollama": map[string]interface{}{
								"thinking": *msg.Thinking,
							},
						},
					},
				}
			}
		}

		// Handle tool response messages
		// Use tool_call_id if provided, otherwise fall back to tool_name for correlation
		if msg.Role == "tool" && (msg.ToolName != nil || msg.ToolCallID != nil) {
			bifrostMsg.ChatToolMessage = &schemas.ChatToolMessage{}
			if msg.ToolCallID != nil {
				bifrostMsg.ChatToolMessage.ToolCallID = msg.ToolCallID
			} else if msg.ToolName != nil {
				toolCallID := *msg.ToolName
				bifrostMsg.ChatToolMessage.ToolCallID = &toolCallID
			}
			bifrostMsg.Name = msg.ToolName
		}

		// Handle images - but NOT for assistant messages with tool_calls
		// Assistant messages with tool_calls are function invocation requests and have no content/images
		if !hasToolCalls && len(msg.Images) > 0 {
			var contentBlocks []schemas.ChatContentBlock

			// Add text content if present
			if msg.Content != "" {
				contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
					Type: schemas.ChatContentBlockTypeText,
					Text: &msg.Content,
				})
			}

			// Add images
			for _, img := range msg.Images {
				dataURL := "data:image/jpeg;base64," + img
				contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
					Type: schemas.ChatContentBlockTypeImage,
					ImageURLStruct: &schemas.ChatInputImage{
						URL: dataURL,
					},
				})
			}

			bifrostMsg.Content = &schemas.ChatMessageContent{
				ContentBlocks: contentBlocks,
			}
		}

		bifrostMessages = append(bifrostMessages, bifrostMsg)
	}

	return bifrostMessages
}

// ==================== ROLE MAPPING UTILITIES ====================

// mapRoleToOllama maps Bifrost roles to Ollama roles.
func mapRoleToOllama(role schemas.ChatMessageRole) string {
	switch role {
	case schemas.ChatMessageRoleDeveloper:
		return "system" // Ollama doesn't support developer role, map to system
	case schemas.ChatMessageRoleSystem:
		return "system"
	case schemas.ChatMessageRoleUser:
		return "user"
	case schemas.ChatMessageRoleAssistant:
		return "assistant"
	case schemas.ChatMessageRoleTool:
		return "tool"
	default:
		return "" // Unsupported
	}
}

// ==================== CONTENT CONVERSION UTILITIES ====================

// convertContentToOllama extracts text and images from Bifrost content.
// Returns the combined text content and a slice of raw base64-encoded images.
// Note: Ollama expects raw base64 strings WITHOUT data URL prefixes.
func convertContentToOllama(content *schemas.ChatMessageContent) (string, []string) {
	if content == nil {
		return "", nil
	}

	// Simple string content - no images
	if content.ContentStr != nil {
		return *content.ContentStr, nil
	}

	// Content blocks - may contain text and/or images
	if content.ContentBlocks == nil {
		return "", nil
	}

	var textParts []string
	var images []string

	for _, block := range content.ContentBlocks {
		switch block.Type {
		case schemas.ChatContentBlockTypeText:
			if block.Text != nil {
				textParts = append(textParts, *block.Text)
			}

		case schemas.ChatContentBlockTypeImage:
			// Extract base64 image data
			// Note: ImageURLStruct.URL can be:
			// 1. A data URL: "data:image/jpeg;base64,<base64>"
			// 2. Raw base64: "<base64>"
			// 3. HTTP(S) URL: "https://..." (not supported by Ollama, skipped)
			if block.ImageURLStruct != nil && block.ImageURLStruct.URL != "" {
				imageData := extractBase64Image(block.ImageURLStruct.URL)
				if imageData != "" {
					images = append(images, imageData)
				}
			}
		}
	}

	return strings.Join(textParts, "\n"), images
}

// ==================== IMAGE UTILITIES ====================

// extractBase64Image extracts raw base64 data from various image URL formats.
// Ollama expects raw base64 strings without data URL prefixes.
// Returns empty string for unsupported formats (HTTP URLs, invalid data).
//
// Supported formats:
//   - data:image/jpeg;base64,<base64> -> extracts <base64>
//   - data:image/png;base64,<base64>  -> extracts <base64>
//   - <raw-base64>                     -> returns as-is
//   - http(s)://...                    -> returns empty (not supported)
func extractBase64Image(url string) string {
	if url == "" {
		return ""
	}

	// Handle data URLs: data:image/jpeg;base64,<base64-data>
	// Must strip the prefix to get raw base64 that Ollama expects
	if strings.HasPrefix(url, "data:") {
		// Find the comma that separates the metadata from the base64 data
		commaIndex := strings.Index(url, ",")
		if commaIndex != -1 && commaIndex < len(url)-1 {
			return url[commaIndex+1:]
		}
		return ""
	}

	// HTTP(S) URLs are not supported by Ollama
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		return ""
	}

	// Assume it's raw base64 - return as-is
	// Ollama will handle validation on its end
	return url
}

// ==================== TOOL CONVERSION UTILITIES ====================

// convertToolCallsToOllama converts Bifrost tool calls to Ollama format.
func convertToolCallsToOllama(toolCalls []schemas.ChatAssistantMessageToolCall) []OllamaToolCall {
	var ollamaToolCalls []OllamaToolCall

	for _, tc := range toolCalls {
		var args map[string]interface{}
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				args = map[string]interface{}{
					"_raw_arguments": tc.Function.Arguments,
				}
			}
		}
		if args == nil {
			args = make(map[string]interface{})
		}

		name := ""
		if tc.Function.Name != nil {
			name = *tc.Function.Name
		}

		ollamaTC := OllamaToolCall{
			Function: OllamaToolCallFunction{
				Index:     int(tc.Index),
				Name:      name,
				Arguments: args,
			},
		}

		// Set tool call ID if available
		if tc.ID != nil {
			ollamaTC.ID = *tc.ID
		}

		ollamaToolCalls = append(ollamaToolCalls, ollamaTC)
	}

	return ollamaToolCalls
}

// convertToolsToOllama converts Bifrost tools to Ollama format.
func convertToolsToOllama(tools []schemas.ChatTool) []OllamaTool {
	var ollamaTools []OllamaTool

	for _, tool := range tools {
		if tool.Function == nil {
			continue
		}

		ollamaTool := OllamaTool{
			Type: "function",
			Function: OllamaToolFunction{
				Name: tool.Function.Name,
			},
		}

		if tool.Function.Description != nil {
			ollamaTool.Function.Description = *tool.Function.Description
		}

		if tool.Function.Parameters != nil {
			ollamaTool.Function.Parameters = tool.Function.Parameters
		}

		ollamaTools = append(ollamaTools, ollamaTool)
	}

	return ollamaTools
}

// convertToolsFromOllama converts Ollama tools to Bifrost format.
func convertToolsFromOllama(tools []OllamaTool) []schemas.ChatTool {
	var bifrostTools []schemas.ChatTool

	for _, tool := range tools {
		bifrostTool := schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        tool.Function.Name,
				Description: schemas.Ptr(tool.Function.Description),
				Parameters:  tool.Function.Parameters,
			},
		}
		bifrostTools = append(bifrostTools, bifrostTool)
	}

	return bifrostTools
}
