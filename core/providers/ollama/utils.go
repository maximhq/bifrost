// Package ollama implements the Ollama provider using native Ollama APIs.
// This file contains utility functions for converting between Bifrost and Ollama formats.
package ollama

import (
	"encoding/base64"
	"encoding/json"
	"log"
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

		// Handle tool response messages - must set tool_name per Ollama semantics
		// Ollama uses tool_name (function name) to correlate, not tool_call_id
		// If Name is not set, try to map from ToolCallID to function name
		if msg.Role == schemas.ChatMessageRoleTool && msg.ChatToolMessage != nil {
			if msg.Name != nil {
				ollamaMsg.ToolName = msg.Name
			} else if msg.ChatToolMessage.ToolCallID != nil {
				// Try to map ToolCallID to function name from previous assistant messages
				if functionName, found := toolCallIDToName[*msg.ChatToolMessage.ToolCallID]; found {
					ollamaMsg.ToolName = &functionName
				} else {
					log.Printf("Error in Tool message without Name field and ToolCallID '%s' not found in previous tool calls - Ollama requires tool_name field", *msg.ChatToolMessage.ToolCallID)
				}
			} else {
				log.Printf("Error in Tool message without Name field or ToolCallID - Ollama requires tool_name field")
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
		// Ollama doesn't provide tool call IDs - ID field is optional in Bifrost, so we don't set it
		if hasToolCalls {
			var toolCalls []schemas.ChatAssistantMessageToolCall
			for i, tc := range msg.ToolCalls {
				args, _ := json.Marshal(tc.Function.Arguments)
				toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
					Index: uint16(i),
					Type:  schemas.Ptr("function"),
					// ID is intentionally not set - Ollama doesn't provide tool call IDs
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      &tc.Function.Name,
						Arguments: string(args),
					},
				})
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
		// Ollama uses tool_name (function name) to correlate, not tool_call_id
		// We set ToolCallID to the function name for Bifrost compatibility, even though Ollama doesn't use IDs
		if msg.Role == "tool" && msg.ToolName != nil {
			toolCallID := *msg.ToolName
			bifrostMsg.ChatToolMessage = &schemas.ChatToolMessage{
				ToolCallID: &toolCallID, // Set to function name for compatibility
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
			// 3. HTTP(S) URL: "https://..." (not supported by Ollama)
			if block.ImageURLStruct != nil && block.ImageURLStruct.URL != "" {
				imageData := extractBase64Image(block.ImageURLStruct.URL)
				if imageData != "" {
					images = append(images, imageData)
				}
				// extractBase64Image logs warnings for unsupported formats
			}
		}
	}

	return strings.Join(textParts, "\n"), images
}

// ==================== IMAGE UTILITIES ====================

// extractBase64Image extracts raw base64 data from various image URL formats.
// Ollama expects raw base64 strings without data URL prefixes.
//
// Supported formats:
//   - data:image/jpeg;base64,<base64> -> extracts <base64>
//   - data:image/png;base64,<base64>  -> extracts <base64>
//   - <raw-base64>                     -> returns as-is
//   - http(s)://...                    -> logs warning, returns empty (not supported)
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
			// Extract everything after the comma (the raw base64 data)
			base64Data := url[commaIndex+1:]
			// Validate it's actually base64
			if isValidBase64(base64Data) {
				return base64Data
			}
			log.Printf("Data URL contains invalid base64 data: %s", url[:min(50, len(url))])
			return ""
		}
		log.Printf("Malformed data URL (no comma separator): %s", url[:min(50, len(url))])
		return ""
	}

	// Check if it's a regular HTTP(S) URL
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		log.Printf("Ollama does not support HTTP(S) image URLs. Please convert to base64: %s", url[:min(100, len(url))])
		return ""
	}

	// Assume it's raw base64 - validate and return
	if isValidBase64(url) {
		return url
	}

	log.Printf("Image URL is neither a valid data URL nor base64: %s", url[:min(50, len(url))])
	return ""
}

// isValidBase64 checks if a string is valid base64 encoded data.
// This is more robust than just checking if it decodes, as it also validates
// that the string contains only valid base64 characters.
func isValidBase64(s string) bool {
	if len(s) < 4 {
		return false
	}

	// Try to decode - this validates both format and content
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		// Try with padding issues fixed
		decoded, err = base64.RawStdEncoding.DecodeString(s)
		if err != nil {
			return false
		}
	}

	// Sanity check: decoded data should be non-empty for images
	return len(decoded) > 0
}

// min returns the minimum of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ==================== TOOL CONVERSION UTILITIES ====================

// convertToolCallsToOllama converts Bifrost tool calls to Ollama format.
// Ollama tool calls don't require an ID field - they use function name for correlation
func convertToolCallsToOllama(toolCalls []schemas.ChatAssistantMessageToolCall) []OllamaToolCall {
	var ollamaToolCalls []OllamaToolCall

	for _, tc := range toolCalls {
		var args map[string]interface{}
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				log.Printf("Failed to unmarshal tool call arguments: %v. Raw arguments: %s", err, tc.Function.Arguments)
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

		ollamaToolCalls = append(ollamaToolCalls, OllamaToolCall{
			Function: OllamaToolCallFunction{
				Name:      name,
				Arguments: args,
			},
		})
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
				Description: &tool.Function.Description,
				Parameters:  tool.Function.Parameters,
			},
		}
		bifrostTools = append(bifrostTools, bifrostTool)
	}

	return bifrostTools
}
