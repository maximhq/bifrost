package bedrock

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// convertParameters handles parameter conversion
func convertParameters(bifrostReq *schemas.BifrostRequest, bedrockReq *BedrockConverseRequest) {
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
		if requestFields, exists := bifrostReq.Params.ExtraParams["additionalModelRequestFieldPaths"]; exists {
			bedrockReq.AdditionalModelRequestFields = requestFields.(map[string]interface{})
		}

		// Handle additional model response field paths
		if responseFields, exists := bifrostReq.Params.ExtraParams["additionalModelResponseFieldPaths"]; exists {
			if fields, ok := responseFields.([]string); ok {
				bedrockReq.AdditionalModelResponseFieldPaths = &fields
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
					bedrockReq.PromptVariables = &variables
				}
			}
		}

		// Handle request metadata
		if reqMetadata, exists := bifrostReq.Params.ExtraParams["requestMetadata"]; exists {
			if metadata, ok := reqMetadata.(map[string]string); ok {
				bedrockReq.RequestMetadata = &metadata
			}
		}
	}
}

// ensureToolConfigForConversation ensures toolConfig is present when tool content exists
func ensureToolConfigForConversation(bifrostReq *schemas.BifrostRequest, bedrockReq *BedrockConverseRequest) {
	if bedrockReq.ToolConfig != nil {
		return // Already has tool config
	}

	hasToolContent, tools := extractToolsFromConversationHistory(*bifrostReq.Input.ChatCompletionInput)
	if hasToolContent && len(tools) > 0 {
		bedrockReq.ToolConfig = &BedrockToolConfig{Tools: &tools}
	}
}

// convertMessages converts Bifrost messages to Bedrock format
// Returns regular messages and system messages separately
func convertMessages(bifrostMessages []schemas.BifrostMessage) ([]BedrockMessage, []BedrockSystemMessage, error) {
	var messages []BedrockMessage
	var systemMessages []BedrockSystemMessage

	for _, msg := range bifrostMessages {
		switch msg.Role {
		case schemas.ModelChatMessageRoleSystem:
			// Convert system message
			systemMsg, err := convertSystemMessage(msg)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to convert system message: %w", err)
			}
			systemMessages = append(systemMessages, systemMsg)

		case schemas.ModelChatMessageRoleUser, schemas.ModelChatMessageRoleAssistant:
			// Convert regular message
			bedrockMsg, err := convertMessage(msg)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to convert message: %w", err)
			}
			messages = append(messages, bedrockMsg)

		case schemas.ModelChatMessageRoleTool:
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
func convertSystemMessage(msg schemas.BifrostMessage) (BedrockSystemMessage, error) {
	systemMsg := BedrockSystemMessage{}

	// Convert content
	if msg.Content.ContentStr != nil {
		systemMsg.Text = msg.Content.ContentStr
	} else if msg.Content.ContentBlocks != nil {
		// For system messages, we only support text content
		// Combine all text blocks into a single string
		var textParts []string
		for _, block := range *msg.Content.ContentBlocks {
			if block.Type == schemas.ContentBlockTypeText && block.Text != nil {
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
func convertMessage(msg schemas.BifrostMessage) (BedrockMessage, error) {
	bedrockMsg := BedrockMessage{
		Role: string(msg.Role),
	}

	// Convert content
	contentBlocks, err := convertContent(msg.Content)
	if err != nil {
		return BedrockMessage{}, fmt.Errorf("failed to convert content: %w", err)
	}

	// Add tool calls if present (for assistant messages)
	if msg.AssistantMessage != nil && msg.AssistantMessage.ToolCalls != nil {
		for _, toolCall := range *msg.AssistantMessage.ToolCalls {
			toolUseBlock := convertToolCallToContentBlock(toolCall)
			contentBlocks = append(contentBlocks, toolUseBlock)
		}
	}

	bedrockMsg.Content = contentBlocks
	return bedrockMsg, nil
}

// convertToolMessage converts a Bifrost tool message to Bedrock format
func convertToolMessage(msg schemas.BifrostMessage) (BedrockMessage, error) {
	bedrockMsg := BedrockMessage{
		Role: "user", // Tool messages are typically treated as user messages in Bedrock
	}

	// Tool messages should have a tool_call_id
	if msg.ToolMessage == nil || msg.ToolMessage.ToolCallID == nil {
		return BedrockMessage{}, fmt.Errorf("tool message missing tool_call_id")
	}

	// Convert content to tool result
	var toolResultContent []BedrockToolResultContent
	if msg.Content.ContentStr != nil {
		toolResultContent = append(toolResultContent, BedrockToolResultContent{
			Text: msg.Content.ContentStr,
		})
	} else if msg.Content.ContentBlocks != nil {
		for _, block := range *msg.Content.ContentBlocks {
			switch block.Type {
			case schemas.ContentBlockTypeText:
				if block.Text != nil {
					toolResultContent = append(toolResultContent, BedrockToolResultContent{
						Text: block.Text,
					})
				}
			case schemas.ContentBlockTypeImage:
				if block.ImageURL != nil {
					imageSource := convertImageToBedrockSource(*block.ImageURL)
					toolResultContent = append(toolResultContent, BedrockToolResultContent{
						Image: imageSource,
					})
				}
			}
		}
	}

	// Create tool result content block
	toolResultBlock := BedrockContentBlock{
		ToolResult: &BedrockToolResult{
			ToolUseID: *msg.ToolMessage.ToolCallID,
			Content:   toolResultContent,
			Status:    schemas.Ptr("success"), // Default to success
		},
	}

	bedrockMsg.Content = []BedrockContentBlock{toolResultBlock}
	return bedrockMsg, nil
}

// convertContent converts Bifrost message content to Bedrock content blocks
func convertContent(content schemas.MessageContent) ([]BedrockContentBlock, error) {
	var contentBlocks []BedrockContentBlock

	if content.ContentStr != nil {
		// Simple text content
		contentBlocks = append(contentBlocks, BedrockContentBlock{
			Text: content.ContentStr,
		})
	} else if content.ContentBlocks != nil {
		// Multi-modal content
		for _, block := range *content.ContentBlocks {
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
func convertContentBlock(block schemas.ContentBlock) (BedrockContentBlock, error) {
	switch block.Type {
	case schemas.ContentBlockTypeText:
		return BedrockContentBlock{
			Text: block.Text,
		}, nil

	case schemas.ContentBlockTypeImage:
		if block.ImageURL == nil {
			return BedrockContentBlock{}, fmt.Errorf("image_url block missing image_url field")
		}

		imageSource := convertImageToBedrockSource(*block.ImageURL)
		return BedrockContentBlock{
			Image: imageSource,
		}, nil

	case schemas.ContentBlockTypeInputAudio:
		// Bedrock doesn't support audio input in Converse API
		return BedrockContentBlock{}, fmt.Errorf("audio input not supported in Bedrock Converse API")

	default:
		return BedrockContentBlock{}, fmt.Errorf("unsupported content block type: %s", block.Type)
	}
}

// convertImageToBedrockSource converts a Bifrost image URL to Bedrock image source
// Uses centralized utility functions like Anthropic converter
func convertImageToBedrockSource(imageURL string) *BedrockImageSource {
	// Use centralized utility functions from schemas package
	sanitizedURL, _ := schemas.SanitizeImageURL(imageURL)
	urlTypeInfo := schemas.ExtractURLTypeInfo(sanitizedURL)

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
	}

	// Set source data based on type
	if urlTypeInfo.Type == schemas.ImageContentTypeBase64 && urlTypeInfo.DataURLWithoutPrefix != nil {
		// Base64 data
		imageSource.Source = BedrockImageSourceData{
			Bytes: urlTypeInfo.DataURLWithoutPrefix,
		}
	} else {
		// For URLs, Bedrock requires base64 - this would need additional handling
		// For now, we'll use empty bytes (this may cause errors but is consistent with old behavior)
		emptyBytes := ""
		imageSource.Source = BedrockImageSourceData{
			Bytes: &emptyBytes,
		}
	}

	return imageSource
}

// convertInferenceConfig converts Bifrost parameters to Bedrock inference config
func convertInferenceConfig(params *schemas.ModelParameters) *BedrockInferenceConfig {
	var config BedrockInferenceConfig
	if params.MaxTokens != nil {
		config.MaxTokens = params.MaxTokens
	}

	if params.Temperature != nil {
		config.Temperature = params.Temperature
	}

	if params.TopP != nil {
		config.TopP = params.TopP
	}

	if params.StopSequences != nil {
		config.StopSequences = params.StopSequences
	}

	return &config
}

// convertToolConfig converts Bifrost tools to Bedrock tool config
func convertToolConfig(params *schemas.ModelParameters) *BedrockToolConfig {
	if params.Tools == nil || len(*params.Tools) == 0 {
		return nil
	}

	var bedrockTools []BedrockTool
	for _, tool := range *params.Tools {
		bedrockTool := BedrockTool{
			ToolSpec: &BedrockToolSpec{
				Name:        tool.Function.Name,
				Description: schemas.Ptr("Tool extracted from conversation history"),
				InputSchema: BedrockToolInputSchema{
					JSON: tool.Function.Parameters,
				},
			},
		}
		bedrockTools = append(bedrockTools, bedrockTool)
	}

	toolConfig := &BedrockToolConfig{
		Tools: &bedrockTools,
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

// convertFunctionParameters converts Bifrost function parameters to Bedrock input schema
func convertFunctionParameters(params schemas.FunctionParameters) map[string]interface{} {
	schema := map[string]interface{}{
		"type": params.Type,
	}

	if params.Description != nil {
		schema["description"] = *params.Description
	}

	if params.Properties != nil {
		schema["properties"] = params.Properties
	}

	if len(params.Required) > 0 {
		schema["required"] = params.Required
	}

	return schema
}

// convertToolChoice converts Bifrost tool choice to Bedrock format
func convertToolChoice(toolChoice schemas.ToolChoice) *BedrockToolChoice {
	// Check if it's a string choice
	if toolChoice.ToolChoiceStr != nil {
		switch schemas.ToolChoiceType(*toolChoice.ToolChoiceStr) {
		case schemas.ToolChoiceTypeAuto:
			return &BedrockToolChoice{
				Auto: &BedrockToolChoiceAuto{},
			}
		case schemas.ToolChoiceTypeAny, schemas.ToolChoiceTypeRequired:
			return &BedrockToolChoice{
				Any: &BedrockToolChoiceAny{},
			}
		case schemas.ToolChoiceTypeNone:
			// Bedrock doesn't have explicit "none" - just don't include tools
			return nil
		}
	}

	// Check if it's a struct choice
	if toolChoice.ToolChoiceStruct != nil {
		switch *toolChoice.ToolChoiceStruct.Type {
		case schemas.ToolChoiceTypeAuto:
			return &BedrockToolChoice{
				Auto: &BedrockToolChoiceAuto{},
			}
		case schemas.ToolChoiceTypeAny, schemas.ToolChoiceTypeRequired:
			return &BedrockToolChoice{
				Any: &BedrockToolChoiceAny{},
			}
		case schemas.ToolChoiceTypeFunction:
			return &BedrockToolChoice{
				Tool: &BedrockToolChoiceTool{
					Name: toolChoice.ToolChoiceStruct.Function.Name,
				},
			}
		case schemas.ToolChoiceTypeNone:
			// Bedrock doesn't have explicit "none" - just don't include tools
			return nil
		}
	}

	return nil
}

// extractToolsFromConversationHistory analyzes conversation history for tool content
func extractToolsFromConversationHistory(messages []schemas.BifrostMessage) (bool, []BedrockTool) {
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
func checkMessageForToolContent(msg schemas.BifrostMessage, toolsMap map[string]BedrockTool) bool {
	hasContent := false

	// Check assistant tool calls
	if msg.AssistantMessage != nil && msg.AssistantMessage.ToolCalls != nil {
		hasContent = true
		for _, toolCall := range *msg.AssistantMessage.ToolCalls {
			if toolCall.Function.Name != nil {
				if _, exists := toolsMap[*toolCall.Function.Name]; !exists {
					toolsMap[*toolCall.Function.Name] = BedrockTool{
						ToolSpec: &BedrockToolSpec{
							Name:        *toolCall.Function.Name,
							Description: schemas.Ptr("Tool extracted from conversation history"),
							InputSchema: BedrockToolInputSchema{
								JSON: map[string]interface{}{
									"type":       "object",
									"properties": map[string]interface{}{},
								},
							},
						},
					}
				}
			}
		}
	}

	// Check tool messages
	if msg.ToolMessage != nil && msg.ToolMessage.ToolCallID != nil {
		hasContent = true
	}

	// Check content blocks
	if msg.Content.ContentBlocks != nil {
		for _, block := range *msg.Content.ContentBlocks {
			if block.Type == "tool_use" || block.Type == "tool_result" {
				hasContent = true
			}
		}
	}

	return hasContent
}

// convertToolCallToContentBlock converts a Bifrost tool call to a Bedrock content block
func convertToolCallToContentBlock(toolCall schemas.ToolCall) BedrockContentBlock {
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
