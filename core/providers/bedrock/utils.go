package bedrock

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// convertParameters handles parameter conversion
func convertChatParameters(bifrostReq *schemas.BifrostChatRequest, bedrockReq *BedrockConverseRequest) {
	if bifrostReq.Params == nil {
		return
	}
	// Convert inference config
	if inferenceConfig := convertInferenceConfig(bifrostReq.Params); inferenceConfig != nil {
		bedrockReq.InferenceConfig = inferenceConfig
	}
	// Convert tool config
	if toolConfig := convertToolConfig(bifrostReq.Params); toolConfig != nil {
		bedrockReq.ToolConfig = toolConfig
	}
	// Add extra parameters
	if len(bifrostReq.Params.ExtraParams) > 0 {
		// Handle guardrail configuration
		if guardrailConfig, exists := bifrostReq.Params.ExtraParams["guardrailConfig"]; exists {
			if gc, ok := guardrailConfig.(map[string]interface{}); ok {
				config := &BedrockGuardrailConfig{}

				if identifier, ok := gc["guardrailIdentifier"].(string); ok {
					config.GuardrailIdentifier = identifier
				}
				if version, ok := gc["guardrailVersion"].(string); ok {
					config.GuardrailVersion = version
				}
				if trace, ok := gc["trace"].(string); ok {
					config.Trace = &trace
				}

				bedrockReq.GuardrailConfig = config
			}
		}
		// Handle additional model request field paths
		if bifrostReq.Params != nil && bifrostReq.Params.ExtraParams != nil {
			if requestFields, exists := bifrostReq.Params.ExtraParams["additionalModelRequestFieldPaths"]; exists {
				bedrockReq.AdditionalModelRequestFields = requestFields.(map[string]interface{})
			}

			// Handle additional model response field paths
			if responseFields, exists := bifrostReq.Params.ExtraParams["additionalModelResponseFieldPaths"]; exists {
				if fields, ok := responseFields.([]string); ok {
					bedrockReq.AdditionalModelResponseFieldPaths = fields
				}
			}
			// Handle performance configuration
			if perfConfig, exists := bifrostReq.Params.ExtraParams["performanceConfig"]; exists {
				if pc, ok := perfConfig.(map[string]interface{}); ok {
					config := &BedrockPerformanceConfig{}

					if latency, ok := pc["latency"].(string); ok {
						config.Latency = &latency
					}
					bedrockReq.PerformanceConfig = config
				}
			}
			// Handle prompt variables
			if promptVars, exists := bifrostReq.Params.ExtraParams["promptVariables"]; exists {
				if vars, ok := promptVars.(map[string]interface{}); ok {
					variables := make(map[string]BedrockPromptVariable)

					for key, value := range vars {
						if valueMap, ok := value.(map[string]interface{}); ok {
							variable := BedrockPromptVariable{}
							if text, ok := valueMap["text"].(string); ok {
								variable.Text = &text
							}
							variables[key] = variable
						}
					}

					if len(variables) > 0 {
						bedrockReq.PromptVariables = variables
					}
				}
			}
			// Handle request metadata
			if reqMetadata, exists := bifrostReq.Params.ExtraParams["requestMetadata"]; exists {
				if metadata, ok := reqMetadata.(map[string]string); ok {
					bedrockReq.RequestMetadata = metadata
				}
			}
		}
	}
}

// ensureChatToolConfigForConversation ensures toolConfig is present when tool content exists
func ensureChatToolConfigForConversation(bifrostReq *schemas.BifrostChatRequest, bedrockReq *BedrockConverseRequest) {
	if bedrockReq.ToolConfig != nil {
		return // Already has tool config
	}

	hasToolContent, tools := extractToolsFromConversationHistory(bifrostReq.Input)
	if hasToolContent && len(tools) > 0 {
		bedrockReq.ToolConfig = &BedrockToolConfig{Tools: tools}
	}
}

// convertMessages converts Bifrost messages to Bedrock format
// Returns regular messages and system messages separately
func convertMessages(bifrostMessages []schemas.ChatMessage) ([]BedrockMessage, []BedrockSystemMessage, error) {
	var messages []BedrockMessage
	var systemMessages []BedrockSystemMessage

	for _, msg := range bifrostMessages {
		switch msg.Role {
		case schemas.ChatMessageRoleSystem:
			// Convert system message
			systemMsg, err := convertSystemMessage(msg)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to convert system message: %w", err)
			}
			systemMessages = append(systemMessages, systemMsg)

		case schemas.ChatMessageRoleUser, schemas.ChatMessageRoleAssistant:
			// Convert regular message
			bedrockMsg, err := convertMessage(msg)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to convert message: %w", err)
			}
			messages = append(messages, bedrockMsg)

		case schemas.ChatMessageRoleTool:
			// Convert tool message - this should be part of the conversation
			bedrockMsg, err := convertToolMessage(msg)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to convert tool message: %w", err)
			}
			messages = append(messages, bedrockMsg)

		default:
			return nil, nil, fmt.Errorf("unsupported message role: %s", msg.Role)
		}
	}

	return messages, systemMessages, nil
}

// convertSystemMessage converts a Bifrost system message to Bedrock format
func convertSystemMessage(msg schemas.ChatMessage) (BedrockSystemMessage, error) {
	systemMsg := BedrockSystemMessage{}

	// Convert content
	if msg.Content.ContentStr != nil {
		systemMsg.Text = msg.Content.ContentStr
	} else if msg.Content.ContentBlocks != nil {
		// For system messages, we only support text content
		// Combine all text blocks into a single string
		var textParts []string
		for _, block := range msg.Content.ContentBlocks {
			if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil {
				textParts = append(textParts, *block.Text)
			}
		}
		if len(textParts) > 0 {
			combined := strings.Join(textParts, "\n")
			systemMsg.Text = &combined
		}
	}

	return systemMsg, nil
}

// convertMessage converts a Bifrost message to Bedrock format
func convertMessage(msg schemas.ChatMessage) (BedrockMessage, error) {
	bedrockMsg := BedrockMessage{
		Role: BedrockMessageRole(msg.Role),
	}

	// Convert content
	var contentBlocks []BedrockContentBlock
	if msg.Content != nil {
		var err error
		contentBlocks, err = convertContent(*msg.Content)
		if err != nil {
			return BedrockMessage{}, fmt.Errorf("failed to convert content: %w", err)
		}
	}

	// Add tool calls if present (for assistant messages)
	if msg.ChatAssistantMessage != nil && msg.ChatAssistantMessage.ToolCalls != nil {
		for _, toolCall := range msg.ChatAssistantMessage.ToolCalls {
			toolUseBlock := convertToolCallToContentBlock(toolCall)
			contentBlocks = append(contentBlocks, toolUseBlock)
		}
	}

	bedrockMsg.Content = contentBlocks
	return bedrockMsg, nil
}

// convertToolMessage converts a Bifrost tool message to Bedrock format
func convertToolMessage(msg schemas.ChatMessage) (BedrockMessage, error) {
	bedrockMsg := BedrockMessage{
		Role: "user", // Tool messages are typically treated as user messages in Bedrock
	}

	// Tool messages should have a tool_call_id
	if msg.ChatToolMessage == nil || msg.ChatToolMessage.ToolCallID == nil {
		return BedrockMessage{}, fmt.Errorf("tool message missing tool_call_id")
	}

	// Convert content to tool result
	var toolResultContent []BedrockContentBlock
	if msg.Content.ContentStr != nil {
		// Bedrock expects JSON to be a parsed object, not a string
		// Try to unmarshal the string content as JSON
		var parsedOutput interface{}
		if err := json.Unmarshal([]byte(*msg.Content.ContentStr), &parsedOutput); err != nil {
			// If it's not valid JSON, wrap it as a text block instead
			toolResultContent = append(toolResultContent, BedrockContentBlock{
				Text: msg.Content.ContentStr,
			})
		} else {
			// Use the parsed JSON object
			toolResultContent = append(toolResultContent, BedrockContentBlock{
				JSON: parsedOutput,
			})
		}
	} else if msg.Content.ContentBlocks != nil {
		for _, block := range msg.Content.ContentBlocks {
			switch block.Type {
			case schemas.ChatContentBlockTypeText:
				if block.Text != nil {
					toolResultContent = append(toolResultContent, BedrockContentBlock{
						Text: block.Text,
					})
				}
			case schemas.ChatContentBlockTypeImage:
				if block.ImageURLStruct != nil {
					imageSource, err := convertImageToBedrockSource(block.ImageURLStruct.URL)
					if err != nil {
						return BedrockMessage{}, fmt.Errorf("failed to convert image in tool result: %w", err)
					}
					toolResultContent = append(toolResultContent, BedrockContentBlock{
						Image: imageSource,
					})
				}
			}
		}
	}

	// Create tool result content block
	toolResultBlock := BedrockContentBlock{
		ToolResult: &BedrockToolResult{
			ToolUseID: *msg.ChatToolMessage.ToolCallID,
			Content:   toolResultContent,
			Status:    schemas.Ptr("success"), // Default to success
		},
	}

	bedrockMsg.Content = []BedrockContentBlock{toolResultBlock}
	return bedrockMsg, nil
}

// convertContent converts Bifrost message content to Bedrock content blocks
func convertContent(content schemas.ChatMessageContent) ([]BedrockContentBlock, error) {
	var contentBlocks []BedrockContentBlock

	if content.ContentStr != nil {
		// Simple text content
		contentBlocks = append(contentBlocks, BedrockContentBlock{
			Text: content.ContentStr,
		})
	} else if content.ContentBlocks != nil {
		// Multi-modal content
		for _, block := range content.ContentBlocks {
			bedrockBlock, err := convertContentBlock(block)
			if err != nil {
				return nil, fmt.Errorf("failed to convert content block: %w", err)
			}
			contentBlocks = append(contentBlocks, bedrockBlock)
		}
	}

	return contentBlocks, nil
}

// convertContentBlock converts a Bifrost content block to Bedrock format
func convertContentBlock(block schemas.ChatContentBlock) (BedrockContentBlock, error) {
	switch block.Type {
	case schemas.ChatContentBlockTypeText:
		return BedrockContentBlock{
			Text: block.Text,
		}, nil

	case schemas.ChatContentBlockTypeImage:
		if block.ImageURLStruct == nil {
			return BedrockContentBlock{}, fmt.Errorf("image_url block missing image_url field")
		}

		imageSource, err := convertImageToBedrockSource(block.ImageURLStruct.URL)
		if err != nil {
			return BedrockContentBlock{}, fmt.Errorf("failed to convert image: %w", err)
		}
		return BedrockContentBlock{
			Image: imageSource,
		}, nil

	case schemas.ChatContentBlockTypeInputAudio:
		// Bedrock doesn't support audio input in Converse API
		return BedrockContentBlock{}, fmt.Errorf("audio input not supported in Bedrock Converse API")

	default:
		return BedrockContentBlock{}, fmt.Errorf("unsupported content block type: %s", block.Type)
	}
}

// convertImageToBedrockSource converts a Bifrost image URL to Bedrock image source
// Uses centralized utility functions like Anthropic converter
// Returns an error for URL-based images (non-base64) since Bedrock requires base64 data
func convertImageToBedrockSource(imageURL string) (*BedrockImageSource, error) {
	// Use centralized utility functions from schemas package
	sanitizedURL, err := schemas.SanitizeImageURL(imageURL)
	if err != nil {
		return nil, fmt.Errorf("failed to sanitize image URL: %w", err)
	}
	urlTypeInfo := schemas.ExtractURLTypeInfo(sanitizedURL)

	// Check if this is a URL-based image (not base64/data URI)
	if urlTypeInfo.Type != schemas.ImageContentTypeBase64 || urlTypeInfo.DataURLWithoutPrefix == nil {
		return nil, fmt.Errorf("only base64-encoded images (data URI format) are supported; remote image URLs are not allowed")
	}

	// Determine format from media type or default to jpeg
	format := "jpeg"
	if urlTypeInfo.MediaType != nil {
		switch *urlTypeInfo.MediaType {
		case "image/png":
			format = "png"
		case "image/gif":
			format = "gif"
		case "image/webp":
			format = "webp"
		case "image/jpeg", "image/jpg":
			format = "jpeg"
		}
	}

	imageSource := &BedrockImageSource{
		Format: format,
		Source: BedrockImageSourceData{
			Bytes: urlTypeInfo.DataURLWithoutPrefix,
		},
	}

	return imageSource, nil
}

// convertInferenceConfig converts Bifrost parameters to Bedrock inference config
func convertInferenceConfig(params *schemas.ChatParameters) *BedrockInferenceConfig {
	var config BedrockInferenceConfig
	if params.MaxCompletionTokens != nil {
		config.MaxTokens = params.MaxCompletionTokens
	}

	if params.Temperature != nil {
		config.Temperature = params.Temperature
	}

	if params.TopP != nil {
		config.TopP = params.TopP
	}

	if params.Stop != nil {
		config.StopSequences = params.Stop
	}

	return &config
}

// convertToolConfig converts Bifrost tools to Bedrock tool config
func convertToolConfig(params *schemas.ChatParameters) *BedrockToolConfig {
	if len(params.Tools) == 0 {
		return nil
	}

	var bedrockTools []BedrockTool
	for _, tool := range params.Tools {
		if tool.Function != nil {
			// Create the complete schema object that Bedrock expects
			var schemaObject interface{}
			if tool.Function.Parameters != nil {
				// Use the complete parameters object which includes type, properties, required, etc.
				schemaObject = map[string]interface{}{
					"type":       tool.Function.Parameters.Type,
					"properties": tool.Function.Parameters.Properties,
				}
				// Add required field if present
				if len(tool.Function.Parameters.Required) > 0 {
					schemaObject.(map[string]interface{})["required"] = tool.Function.Parameters.Required
				}
			} else {
				// Fallback to empty object schema if no parameters
				schemaObject = map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				}
			}

			// Use the tool description if available, otherwise use a generic description
			description := "Function tool"
			if tool.Function.Description != nil {
				description = *tool.Function.Description
			}

			bedrockTool := BedrockTool{
				ToolSpec: &BedrockToolSpec{
					Name:        tool.Function.Name,
					Description: schemas.Ptr(description),
					InputSchema: BedrockToolInputSchema{
						JSON: schemaObject,
					},
				},
			}
			bedrockTools = append(bedrockTools, bedrockTool)
		}
	}

	toolConfig := &BedrockToolConfig{
		Tools: bedrockTools,
	}

	// Convert tool choice
	if params.ToolChoice != nil {
		toolChoice := convertToolChoice(*params.ToolChoice)
		if toolChoice != nil {
			toolConfig.ToolChoice = toolChoice
		}
	}

	return toolConfig
}

// convertToolChoice converts Bifrost tool choice to Bedrock format
func convertToolChoice(toolChoice schemas.ChatToolChoice) *BedrockToolChoice {
	// String variant
	if toolChoice.ChatToolChoiceStr != nil {
		switch schemas.ChatToolChoiceType(*toolChoice.ChatToolChoiceStr) {
		case schemas.ChatToolChoiceTypeAny, schemas.ChatToolChoiceTypeRequired:
			return &BedrockToolChoice{Any: &BedrockToolChoiceAny{}}
		case schemas.ChatToolChoiceTypeNone:
			// Bedrock doesn't have explicit "none" - omit ToolChoice
			return nil
		case schemas.ChatToolChoiceTypeFunction:
			// Not representable without a name; expect struct form instead.
			return nil
		}
	}
	// Struct variant
	if toolChoice.ChatToolChoiceStruct != nil {
		switch toolChoice.ChatToolChoiceStruct.Type {
		case schemas.ChatToolChoiceTypeFunction:
			name := toolChoice.ChatToolChoiceStruct.Function.Name
			if name != "" {
				return &BedrockToolChoice{
					Tool: &BedrockToolChoiceTool{Name: name},
				}
			}
			return nil
		case schemas.ChatToolChoiceTypeAny, schemas.ChatToolChoiceTypeRequired:
			return &BedrockToolChoice{Any: &BedrockToolChoiceAny{}}
		case schemas.ChatToolChoiceTypeNone:
			return nil
		}
	}
	return nil
}

// extractToolsFromConversationHistory analyzes conversation history for tool content
func extractToolsFromConversationHistory(messages []schemas.ChatMessage) (bool, []BedrockTool) {
	hasToolContent := false
	toolsMap := make(map[string]BedrockTool)

	for _, msg := range messages {
		hasToolContent = checkMessageForToolContent(msg, toolsMap) || hasToolContent
	}

	tools := make([]BedrockTool, 0, len(toolsMap))
	for _, tool := range toolsMap {
		tools = append(tools, tool)
	}

	return hasToolContent, tools
}

// checkMessageForToolContent checks a single message for tool content and updates the tools map
func checkMessageForToolContent(msg schemas.ChatMessage, toolsMap map[string]BedrockTool) bool {
	hasContent := false

	// Check assistant tool calls
	if msg.ChatAssistantMessage != nil && msg.ChatAssistantMessage.ToolCalls != nil {
		hasContent = true
		for _, toolCall := range msg.ChatAssistantMessage.ToolCalls {
			if toolCall.Function.Name != nil {
				if _, exists := toolsMap[*toolCall.Function.Name]; !exists {
					// Create a complete schema object for extracted tools
					schemaObject := map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					}

					toolsMap[*toolCall.Function.Name] = BedrockTool{
						ToolSpec: &BedrockToolSpec{
							Name:        *toolCall.Function.Name,
							Description: schemas.Ptr("Tool extracted from conversation history"),
							InputSchema: BedrockToolInputSchema{
								JSON: schemaObject,
							},
						},
					}
				}
			}
		}
	}

	// Check tool messages
	if msg.ChatToolMessage != nil && msg.ChatToolMessage.ToolCallID != nil {
		hasContent = true
	}

	// Check content blocks
	if msg.Content.ContentBlocks != nil {
		for _, block := range msg.Content.ContentBlocks {
			if block.Type == "tool_use" || block.Type == "tool_result" {
				hasContent = true
			}
		}
	}

	return hasContent
}

// convertToolCallToContentBlock converts a Bifrost tool call to a Bedrock content block
func convertToolCallToContentBlock(toolCall schemas.ChatAssistantMessageToolCall) BedrockContentBlock {
	toolUseID := ""
	if toolCall.ID != nil {
		toolUseID = *toolCall.ID
	}

	toolName := ""
	if toolCall.Function.Name != nil {
		toolName = *toolCall.Function.Name
	}

	// Parse JSON arguments to object
	var input interface{}
	if err := sonic.Unmarshal([]byte(toolCall.Function.Arguments), &input); err != nil {
		input = map[string]interface{}{} // Fallback to empty object
	}

	return BedrockContentBlock{
		ToolUse: &BedrockToolUse{
			ToolUseID: toolUseID,
			Name:      toolName,
			Input:     input,
		},
	}
}
