// Package ollama implements the Ollama provider using native Ollama APIs.
// This file contains converters for chat completion requests and responses.
package ollama

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToOllamaChatRequest converts a Bifrost chat request to Ollama native format.
func ToOllamaChatRequest(bifrostReq *schemas.BifrostChatRequest) *OllamaChatRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	ollamaReq := &OllamaChatRequest{
		Model:    bifrostReq.Model,
		Messages: convertMessagesToOllama(bifrostReq.Input),
	}

	// Convert parameters
	if bifrostReq.Params != nil {
		options := &OllamaOptions{}
		hasOptions := false

		// Map standard parameters
		if bifrostReq.Params.MaxCompletionTokens != nil {
			options.NumPredict = bifrostReq.Params.MaxCompletionTokens
			hasOptions = true
		}
		if bifrostReq.Params.Temperature != nil {
			options.Temperature = bifrostReq.Params.Temperature
			hasOptions = true
		}
		if bifrostReq.Params.TopP != nil {
			options.TopP = bifrostReq.Params.TopP
			hasOptions = true
		}
		if bifrostReq.Params.PresencePenalty != nil {
			options.PresencePenalty = bifrostReq.Params.PresencePenalty
			hasOptions = true
		}
		if bifrostReq.Params.FrequencyPenalty != nil {
			options.FrequencyPenalty = bifrostReq.Params.FrequencyPenalty
			hasOptions = true
		}
		if bifrostReq.Params.Stop != nil {
			options.Stop = bifrostReq.Params.Stop
			hasOptions = true
		}
		if bifrostReq.Params.Seed != nil {
			options.Seed = bifrostReq.Params.Seed
			hasOptions = true
		}

		// Handle extra parameters for Ollama-specific fields
		if bifrostReq.Params.ExtraParams != nil {
			// Top-k sampling
			if topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"]); ok {
				options.TopK = topK
				hasOptions = true
			}

			// Context window size
			if numCtx, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_ctx"]); ok {
				options.NumCtx = numCtx
				hasOptions = true
			}

			// Repeat penalty
			if repeatPenalty, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["repeat_penalty"]); ok {
				options.RepeatPenalty = repeatPenalty
				hasOptions = true
			}

			// Repeat last N
			if repeatLastN, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["repeat_last_n"]); ok {
				options.RepeatLastN = repeatLastN
				hasOptions = true
			}

			// Mirostat sampling
			if mirostat, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["mirostat"]); ok {
				options.Mirostat = mirostat
				hasOptions = true
			}
			if mirostatEta, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["mirostat_eta"]); ok {
				options.MirostatEta = mirostatEta
				hasOptions = true
			}
			if mirostatTau, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["mirostat_tau"]); ok {
				options.MirostatTau = mirostatTau
				hasOptions = true
			}

			// TFS-Z sampling
			if tfsZ, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["tfs_z"]); ok {
				options.TfsZ = tfsZ
				hasOptions = true
			}

			// Typical-P sampling
			if typicalP, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["typical_p"]); ok {
				options.TypicalP = typicalP
				hasOptions = true
			}

			// Performance options
			if numBatch, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_batch"]); ok {
				options.NumBatch = numBatch
				hasOptions = true
			}
			if numGPU, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_gpu"]); ok {
				options.NumGPU = numGPU
				hasOptions = true
			}
			if numThread, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_thread"]); ok {
				options.NumThread = numThread
				hasOptions = true
			}

			// Keep-alive duration
			if keepAlive, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["keep_alive"]); ok {
				ollamaReq.KeepAlive = keepAlive
			}
		}

		if hasOptions {
			ollamaReq.Options = options
		}

		// Handle response format (JSON mode)
		if bifrostReq.Params.ResponseFormat != nil {
			if rf, ok := (*bifrostReq.Params.ResponseFormat).(map[string]interface{}); ok {
				if t, exists := rf["type"]; exists && t == "json_object" {
					ollamaReq.Format = "json"
				} else if schema, exists := rf["json_schema"]; exists {
					// Pass JSON schema directly for structured output
					ollamaReq.Format = schema
				}
			}
		}

		// Convert tools
		if bifrostReq.Params.Tools != nil {
			ollamaReq.Tools = convertToolsToOllama(bifrostReq.Params.Tools)
		}
	}

	return ollamaReq
}

// convertMessagesToOllama converts Bifrost messages to Ollama format.
func convertMessagesToOllama(messages []schemas.ChatMessage) []OllamaMessage {
	var ollamaMessages []OllamaMessage

	for _, msg := range messages {
		ollamaMsg := OllamaMessage{
			Role: string(msg.Role),
		}

		// Handle content
		if msg.Content != nil {
			if msg.Content.ContentStr != nil {
				ollamaMsg.Content = *msg.Content.ContentStr
			} else if msg.Content.ContentBlocks != nil {
				var textParts []string
				var images []string

				for _, block := range msg.Content.ContentBlocks {
					switch block.Type {
					case schemas.ChatContentBlockTypeText:
						if block.Text != nil {
							textParts = append(textParts, *block.Text)
						}
					case schemas.ChatContentBlockTypeImage:
						if block.ImageURLStruct != nil {
							// Handle image URLs - extract base64 data
							imageData := extractBase64Image(block.ImageURLStruct.URL)
							if imageData != "" {
								images = append(images, imageData)
							}
						}
					}
				}

				ollamaMsg.Content = strings.Join(textParts, "\n")
				if len(images) > 0 {
					ollamaMsg.Images = images
				}
			}
		}

		// Handle tool calls for assistant messages
		if msg.ChatAssistantMessage != nil && msg.ChatAssistantMessage.ToolCalls != nil {
			for _, tc := range msg.ChatAssistantMessage.ToolCalls {
				var args map[string]interface{}
				if tc.Function.Arguments != "" {
					_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
				}
				if args == nil {
					args = make(map[string]interface{})
				}

				name := ""
				if tc.Function.Name != nil {
					name = *tc.Function.Name
				}

				ollamaMsg.ToolCalls = append(ollamaMsg.ToolCalls, OllamaToolCall{
					Function: OllamaToolCallFunction{
						Name:      name,
						Arguments: args,
					},
				})
			}
		}

		// Handle tool response messages
		if msg.Role == schemas.ChatMessageRoleTool && msg.ChatToolMessage != nil {
			// In Ollama, tool responses are regular messages with role "tool"
			// The content is the tool's response
			if msg.Content != nil && msg.Content.ContentStr != nil {
				ollamaMsg.Content = *msg.Content.ContentStr
			}
		}

		ollamaMessages = append(ollamaMessages, ollamaMsg)
	}

	return ollamaMessages
}

// extractBase64Image extracts base64 data from a data URL or returns the URL as-is for base64.
func extractBase64Image(url string) string {
	// Handle data URLs: data:image/jpeg;base64,/9j/4AAQ...
	if strings.HasPrefix(url, "data:") {
		parts := strings.SplitN(url, ",", 2)
		if len(parts) == 2 {
			return parts[1] // Return just the base64 data
		}
	}

	// If it's already base64 encoded (no prefix), return as-is
	if isBase64(url) {
		return url
	}

	// For regular URLs, Ollama doesn't support them directly
	// The caller should handle URL-to-base64 conversion if needed
	return ""
}

// isBase64 checks if a string is likely base64 encoded.
func isBase64(s string) bool {
	if len(s) < 4 {
		return false
	}
	_, err := base64.StdEncoding.DecodeString(s)
	return err == nil
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

// ToBifrostChatRequest converts an Ollama chat request to Bifrost format.
// This is used for passthrough/reverse conversion scenarios.
func (r *OllamaChatRequest) ToBifrostChatRequest() *schemas.BifrostChatRequest {
	if r == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(r.Model, schemas.Ollama)

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input:    convertMessagesFromOllama(r.Messages),
	}

	// Convert options to parameters
	if r.Options != nil {
		params := &schemas.ChatParameters{
			ExtraParams: make(map[string]interface{}),
		}

		if r.Options.NumPredict != nil {
			params.MaxCompletionTokens = r.Options.NumPredict
		}
		if r.Options.Temperature != nil {
			params.Temperature = r.Options.Temperature
		}
		if r.Options.TopP != nil {
			params.TopP = r.Options.TopP
		}
		if r.Options.Stop != nil {
			params.Stop = r.Options.Stop
		}
		if r.Options.PresencePenalty != nil {
			params.PresencePenalty = r.Options.PresencePenalty
		}
		if r.Options.FrequencyPenalty != nil {
			params.FrequencyPenalty = r.Options.FrequencyPenalty
		}
		if r.Options.Seed != nil {
			params.Seed = r.Options.Seed
		}

		// Map Ollama-specific parameters to ExtraParams
		if r.Options.TopK != nil {
			params.ExtraParams["top_k"] = *r.Options.TopK
		}
		if r.Options.NumCtx != nil {
			params.ExtraParams["num_ctx"] = *r.Options.NumCtx
		}
		if r.Options.RepeatPenalty != nil {
			params.ExtraParams["repeat_penalty"] = *r.Options.RepeatPenalty
		}

		bifrostReq.Params = params
	}

	// Convert tools
	if r.Tools != nil {
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.ChatParameters{}
		}
		bifrostReq.Params.Tools = convertToolsFromOllama(r.Tools)
	}

	return bifrostReq
}

// convertMessagesFromOllama converts Ollama messages to Bifrost format.
func convertMessagesFromOllama(messages []OllamaMessage) []schemas.ChatMessage {
	var bifrostMessages []schemas.ChatMessage

	for _, msg := range messages {
		bifrostMsg := schemas.ChatMessage{
			Role: schemas.ChatMessageRole(msg.Role),
			Content: &schemas.ChatMessageContent{
				ContentStr: &msg.Content,
			},
		}

		// Handle tool calls
		if len(msg.ToolCalls) > 0 {
			var toolCalls []schemas.ChatAssistantMessageToolCall
			for i, tc := range msg.ToolCalls {
				args, _ := json.Marshal(tc.Function.Arguments)
				toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
					Index: uint16(i),
					Type:  schemas.Ptr("function"),
					ID:    schemas.Ptr(tc.Function.Name),
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

		// Handle images
		if len(msg.Images) > 0 {
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
