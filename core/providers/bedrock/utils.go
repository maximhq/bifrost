package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// convertParameters handles parameter conversion
func convertChatParameters(ctx *context.Context, bifrostReq *schemas.BifrostChatRequest, bedrockReq *BedrockConverseRequest) error {
	// Parameters are optional - if not provided, just skip conversion
	if bifrostReq.Params == nil {
		return nil
	}
	// Convert inference config
	if inferenceConfig := convertInferenceConfig(bifrostReq.Params); inferenceConfig != nil {
		bedrockReq.InferenceConfig = inferenceConfig
	}

	// Check for response_format and convert to tool
	responseFormatTool := convertResponseFormatToTool(ctx, bifrostReq.Params)

	// Convert tool config
	if toolConfig := convertToolConfig(bifrostReq.Params); toolConfig != nil {
		bedrockReq.ToolConfig = toolConfig
	}

	// Convert reasoning config
	if bifrostReq.Params.Reasoning != nil {
		if bedrockReq.AdditionalModelRequestFields == nil {
			bedrockReq.AdditionalModelRequestFields = make(schemas.OrderedMap)
		}
		if bifrostReq.Params.Reasoning.MaxTokens != nil {
			if schemas.IsAnthropicModel(bifrostReq.Model) && *bifrostReq.Params.Reasoning.MaxTokens < anthropic.MinimumReasoningMaxTokens {
				return fmt.Errorf("reasoning.max_tokens must be >= %d for anthropic", anthropic.MinimumReasoningMaxTokens)
			}
			bedrockReq.AdditionalModelRequestFields["reasoning_config"] = map[string]any{
				"type":          "enabled",
				"budget_tokens": *bifrostReq.Params.Reasoning.MaxTokens,
			}
		} else if bifrostReq.Params.Reasoning.Effort != nil && *bifrostReq.Params.Reasoning.Effort != "none" {
			maxTokens := DefaultCompletionMaxTokens
			if bedrockReq.InferenceConfig != nil && bedrockReq.InferenceConfig.MaxTokens != nil {
				maxTokens = *bedrockReq.InferenceConfig.MaxTokens
			} else {
				if bedrockReq.InferenceConfig != nil {
					bedrockReq.InferenceConfig.MaxTokens = schemas.Ptr(DefaultCompletionMaxTokens)
				} else {
					bedrockReq.InferenceConfig = &BedrockInferenceConfig{
						MaxTokens: schemas.Ptr(DefaultCompletionMaxTokens),
					}
				}
			}
			minBudgetTokens := MinimumReasoningMaxTokens
			if schemas.IsAnthropicModel(bifrostReq.Model) {
				minBudgetTokens = anthropic.MinimumReasoningMaxTokens
			}
			budgetTokens, err := providerUtils.GetBudgetTokensFromReasoningEffort(*bifrostReq.Params.Reasoning.Effort, minBudgetTokens, maxTokens)
			if err != nil {
				return err
			}
			bedrockReq.AdditionalModelRequestFields["reasoning_config"] = map[string]any{
				"type":          "enabled",
				"budget_tokens": budgetTokens,
			}
		} else {
			bedrockReq.AdditionalModelRequestFields["reasoning_config"] = map[string]string{
				"type": "disabled",
			}
		}
	}

	// If response_format was converted to a tool, add it to the tool config
	if responseFormatTool != nil {
		if bedrockReq.ToolConfig == nil {
			bedrockReq.ToolConfig = &BedrockToolConfig{}
		}
		// Add the response format tool to the beginning of the tools list
		bedrockReq.ToolConfig.Tools = append([]BedrockTool{*responseFormatTool}, bedrockReq.ToolConfig.Tools...)
		// Force the model to use this specific tool
		bedrockReq.ToolConfig.ToolChoice = &BedrockToolChoice{
			Tool: &BedrockToolChoiceTool{
				Name: responseFormatTool.ToolSpec.Name,
			},
		}
	}
	if bifrostReq.Params.ServiceTier != nil {
		bedrockReq.ServiceTier = &BedrockServiceTier{
			Type: *bifrostReq.Params.ServiceTier,
		}
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
				if orderedFields, ok := schemas.SafeExtractOrderedMap(requestFields); ok {
					bedrockReq.AdditionalModelRequestFields = orderedFields
				}
			}

			// Handle additional model response field paths
			if responseFields, exists := bifrostReq.Params.ExtraParams["additionalModelResponseFieldPaths"]; exists {
				// Handle both []string and []interface{} types
				if fields, ok := responseFields.([]string); ok {
					bedrockReq.AdditionalModelResponseFieldPaths = fields
				} else if fieldsInterface, ok := responseFields.([]interface{}); ok {
					stringFields := make([]string, 0, len(fieldsInterface))
					for _, field := range fieldsInterface {
						if fieldStr, ok := field.(string); ok {
							stringFields = append(stringFields, fieldStr)
						}
					}
					if len(stringFields) > 0 {
						bedrockReq.AdditionalModelResponseFieldPaths = stringFields
					}
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
	return nil
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

// convertResponseFormatToTool converts a response_format parameter to a Bedrock tool
// Returns nil if no response_format is present or if it's not a json_schema type
func convertResponseFormatToTool(ctx *context.Context, params *schemas.ChatParameters) *BedrockTool {
	if params == nil || params.ResponseFormat == nil {
		return nil
	}

	// ResponseFormat is stored as interface{}, need to parse it
	responseFormatMap, ok := (*params.ResponseFormat).(map[string]interface{})
	if !ok {
		return nil
	}

	// Check if type is "json_schema"
	formatType, ok := responseFormatMap["type"].(string)
	if !ok || formatType != "json_schema" {
		return nil
	}

	// Extract json_schema object
	jsonSchemaObj, ok := responseFormatMap["json_schema"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Extract name and schema
	toolName, ok := jsonSchemaObj["name"].(string)
	if !ok || toolName == "" {
		toolName = "json_response"
	}

	schemaObj, ok := jsonSchemaObj["schema"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Extract description from schema if available
	description := "Returns structured JSON output"
	if desc, ok := schemaObj["description"].(string); ok && desc != "" {
		description = desc
	}

	// set bifrost context key structured output tool name
	toolName = fmt.Sprintf("bf_so_%s", toolName)
	(*ctx) = context.WithValue(*ctx, schemas.BifrostContextKeyStructuredOutputToolName, toolName)

	// Create the Bedrock tool
	return &BedrockTool{
		ToolSpec: &BedrockToolSpec{
			Name:        toolName,
			Description: schemas.Ptr(description),
			InputSchema: BedrockToolInputSchema{
				JSON: schemaObj,
			},
		},
	}
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
	if msg.Content != nil && msg.Content.ContentBlocks != nil {
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

// ToBedrockError converts a BifrostError to BedrockError
// This is a standalone function similar to ToAnthropicChatCompletionError
func ToBedrockError(bifrostErr *schemas.BifrostError) *BedrockError {
	if bifrostErr == nil || bifrostErr.Error == nil {
		return &BedrockError{
			Type:    "InternalServerError",
			Message: "unknown error",
		}
	}

	// Safely extract message from nested error
	message := ""
	if bifrostErr.Error != nil {
		message = bifrostErr.Error.Message
	}

	bedrockErr := &BedrockError{
		Message: message,
	}

	// Map error type/code
	if bifrostErr.Error.Code != nil {
		bedrockErr.Type = *bifrostErr.Error.Code
		bedrockErr.Code = bifrostErr.Error.Code
	} else if bifrostErr.Type != nil {
		bedrockErr.Type = *bifrostErr.Type
	} else {
		bedrockErr.Type = "InternalServerError"
	}

	return bedrockErr
}
