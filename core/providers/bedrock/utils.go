package bedrock

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

var (
	invalidCharRegex = regexp.MustCompile(`[^a-zA-Z0-9\s\-\(\)\[\]]`)
	multiSpaceRegex  = regexp.MustCompile(`\s{2,}`)
)

// normalizeBedrockFilename normalizes a filename to meet Bedrock's requirements:
// - Only alphanumeric characters, whitespace, hyphens, parentheses, and square brackets
// - No more than one consecutive whitespace character
// - Trims leading and trailing whitespace
func normalizeBedrockFilename(filename string) string {
	if filename == "" {
		return "document"
	}

	// Replace invalid characters with underscores
	normalized := invalidCharRegex.ReplaceAllString(filename, "_")

	// Replace multiple consecutive whitespace with a single space
	normalized = multiSpaceRegex.ReplaceAllString(normalized, " ")

	// Trim leading and trailing whitespace
	normalized = strings.TrimSpace(normalized)

	// If the result is empty after normalization, return a default name
	if normalized == "" {
		return "document"
	}

	return normalized
}

// convertParameters handles parameter conversion
func convertChatParameters(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostChatRequest, bedrockReq *BedrockConverseRequest) error {
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
	if toolConfig := convertToolConfig(bifrostReq.Model, bifrostReq.Params); toolConfig != nil {
		bedrockReq.ToolConfig = toolConfig
	}

	// Convert reasoning config
	if bifrostReq.Params.Reasoning != nil {
		if bedrockReq.AdditionalModelRequestFields == nil {
			bedrockReq.AdditionalModelRequestFields = make(schemas.OrderedMap)
		}
		if bifrostReq.Params.Reasoning.MaxTokens != nil {
			tokenBudget := *bifrostReq.Params.Reasoning.MaxTokens
			if *bifrostReq.Params.Reasoning.MaxTokens == -1 {
				// bedrock does not support dynamic reasoning budget like gemini
				// setting it to default max tokens
				tokenBudget = anthropic.MinimumReasoningMaxTokens
			}
			if schemas.IsAnthropicModel(bifrostReq.Model) {
				if tokenBudget < anthropic.MinimumReasoningMaxTokens {
					return fmt.Errorf("reasoning.max_tokens must be >= %d for anthropic", anthropic.MinimumReasoningMaxTokens)
				}
				bedrockReq.AdditionalModelRequestFields["reasoning_config"] = map[string]any{
					"type":          "enabled",
					"budget_tokens": tokenBudget,
				}
			} else if schemas.IsNovaModel(bifrostReq.Model) {
				minBudgetTokens := MinimumReasoningMaxTokens
				defaultMaxTokens := DefaultCompletionMaxTokens
				if bedrockReq.InferenceConfig != nil && bedrockReq.InferenceConfig.MaxTokens != nil {
					defaultMaxTokens = *bedrockReq.InferenceConfig.MaxTokens
				} else if bedrockReq.InferenceConfig != nil {
					bedrockReq.InferenceConfig.MaxTokens = schemas.Ptr(DefaultCompletionMaxTokens)
				} else {
					bedrockReq.InferenceConfig = &BedrockInferenceConfig{
						MaxTokens: schemas.Ptr(DefaultCompletionMaxTokens),
					}
				}

				maxReasoningEffort := providerUtils.GetReasoningEffortFromBudgetTokens(tokenBudget, minBudgetTokens, defaultMaxTokens)
				typeStr := "enabled"
				switch maxReasoningEffort {
				case "high":
					if bedrockReq.InferenceConfig != nil {
						bedrockReq.InferenceConfig.MaxTokens = nil
						bedrockReq.InferenceConfig.Temperature = nil
						bedrockReq.InferenceConfig.TopP = nil
					}
				case "minimal":
					maxReasoningEffort = "low"
				case "none":
					typeStr = "disabled"
				}

				config := map[string]any{
					"type": typeStr,
				}
				if typeStr != "disabled" {
					config["maxReasoningEffort"] = maxReasoningEffort
				}

				bedrockReq.AdditionalModelRequestFields["reasoningConfig"] = config
			} else {
				bedrockReq.AdditionalModelRequestFields["reasoning_config"] = map[string]any{
					"type":          "enabled",
					"budget_tokens": tokenBudget,
				}
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
			if schemas.IsNovaModel(bifrostReq.Model) {
				effort := *bifrostReq.Params.Reasoning.Effort
				typeStr := "enabled"
				switch effort {
				case "high":
					if bedrockReq.InferenceConfig != nil {
						bedrockReq.InferenceConfig.MaxTokens = nil
						bedrockReq.InferenceConfig.Temperature = nil
						bedrockReq.InferenceConfig.TopP = nil
					}
				case "minimal":
					effort = "low"
				case "none":
					typeStr = "disabled"
				}

				config := map[string]any{
					"type": typeStr,
				}
				if typeStr != "disabled" {
					config["maxReasoningEffort"] = effort
				}

				bedrockReq.AdditionalModelRequestFields["reasoningConfig"] = config
			} else if schemas.IsAnthropicModel(bifrostReq.Model) {
				budgetTokens, err := providerUtils.GetBudgetTokensFromReasoningEffort(*bifrostReq.Params.Reasoning.Effort, anthropic.MinimumReasoningMaxTokens, maxTokens)
				if err != nil {
					return err
				}
				bedrockReq.AdditionalModelRequestFields["reasoning_config"] = map[string]any{
					"type":          "enabled",
					"budget_tokens": budgetTokens,
				}
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
				if metadata, ok := schemas.SafeExtractStringMap(reqMetadata); ok {
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

	for i := 0; i < len(bifrostMessages); i++ {
		msg := bifrostMessages[i]
		switch msg.Role {
		case schemas.ChatMessageRoleSystem:
			// Convert system message
			systemMsgs, err := convertSystemMessages(msg)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to convert system message: %w", err)
			}
			systemMessages = append(systemMessages, systemMsgs...)

		case schemas.ChatMessageRoleUser, schemas.ChatMessageRoleAssistant:
			// Convert regular message
			bedrockMsg, err := convertMessage(msg)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to convert message: %w", err)
			}
			messages = append(messages, bedrockMsg)

		case schemas.ChatMessageRoleTool:
			// Collect all consecutive tool messages and group them into a single user message
			var toolMessages []schemas.ChatMessage
			toolMessages = append(toolMessages, msg)

			// Look ahead for more consecutive tool messages
			for j := i + 1; j < len(bifrostMessages) && bifrostMessages[j].Role == schemas.ChatMessageRoleTool; j++ {
				toolMessages = append(toolMessages, bifrostMessages[j])
				i = j
			}

			// Convert all collected tool messages into a single Bedrock message
			bedrockMsg, err := convertToolMessages(toolMessages)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to convert tool messages: %w", err)
			}
			messages = append(messages, bedrockMsg)

		default:
			return nil, nil, fmt.Errorf("unsupported message role: %s", msg.Role)
		}
	}

	return messages, systemMessages, nil
}

// convertSystemMessages converts a Bifrost system message to Bedrock format
func convertSystemMessages(msg schemas.ChatMessage) ([]BedrockSystemMessage, error) {
	systemMsgs := []BedrockSystemMessage{}

	// Convert content
	if msg.Content.ContentStr != nil {
		systemMsgs = append(systemMsgs, BedrockSystemMessage{
			Text: msg.Content.ContentStr,
		})
	} else if msg.Content.ContentBlocks != nil {
		for _, block := range msg.Content.ContentBlocks {
			if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil {
				systemMsgs = append(systemMsgs, BedrockSystemMessage{
					Text: block.Text,
				})
				if block.CacheControl != nil {
					systemMsgs = append(systemMsgs, BedrockSystemMessage{
						CachePoint: &BedrockCachePoint{
							Type: BedrockCachePointTypeDefault,
						},
					})
				}
			}
		}
	}

	return systemMsgs, nil
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

// convertToolMessages converts multiple consecutive Bifrost tool messages to a single Bedrock message
func convertToolMessages(msgs []schemas.ChatMessage) (BedrockMessage, error) {
	if len(msgs) == 0 {
		return BedrockMessage{}, fmt.Errorf("no tool messages provided")
	}

	bedrockMsg := BedrockMessage{
		Role: "user",
	}

	var contentBlocks []BedrockContentBlock

	for _, msg := range msgs {
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
				// Bedrock does not accept primitives or arrays directly in the json field
				switch v := parsedOutput.(type) {
				case map[string]any:
					// Objects are valid as-is
					toolResultContent = append(toolResultContent, BedrockContentBlock{
						JSON: v,
					})
				case []any:
					// Arrays need to be wrapped
					toolResultContent = append(toolResultContent, BedrockContentBlock{
						JSON: map[string]any{"results": v},
					})
				default:
					// Primitives (string, number, boolean, null) need to be wrapped
					toolResultContent = append(toolResultContent, BedrockContentBlock{
						JSON: map[string]any{"value": v},
					})
				}
			}
		} else if msg.Content.ContentBlocks != nil {
			for _, block := range msg.Content.ContentBlocks {
				switch block.Type {
				case schemas.ChatContentBlockTypeText:
					if block.Text != nil {
						toolResultContent = append(toolResultContent, BedrockContentBlock{
							Text: block.Text,
						})
						// Cache point must be in a separate block
						if block.CacheControl != nil {
							toolResultContent = append(toolResultContent, BedrockContentBlock{
								CachePoint: &BedrockCachePoint{
									Type: BedrockCachePointTypeDefault,
								},
							})
						}
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
						// Cache point must be in a separate block
						if block.CacheControl != nil {
							toolResultContent = append(toolResultContent, BedrockContentBlock{
								CachePoint: &BedrockCachePoint{
									Type: BedrockCachePointTypeDefault,
								},
							})
						}
					}
				}
			}
		}

		if msg.ChatToolMessage == nil {
			return BedrockMessage{}, fmt.Errorf("tool message missing required ChatToolMessage")
		}

		if msg.ChatToolMessage.ToolCallID == nil {
			return BedrockMessage{}, fmt.Errorf("tool message missing required ToolCallID")
		}

		// Create tool result content block for this tool message
		toolResultBlock := BedrockContentBlock{
			ToolResult: &BedrockToolResult{
				ToolUseID: *msg.ChatToolMessage.ToolCallID,
				Content:   toolResultContent,
				Status:    schemas.Ptr("success"), // Default to success
			},
		}

		contentBlocks = append(contentBlocks, toolResultBlock)
	}

	bedrockMsg.Content = contentBlocks
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
			bedrockBlocks, err := convertContentBlock(block)
			if err != nil {
				return nil, fmt.Errorf("failed to convert content block: %w", err)
			}
			contentBlocks = append(contentBlocks, bedrockBlocks...)
		}
	}

	return contentBlocks, nil
}

// convertContentBlock converts a Bifrost content block to Bedrock format
func convertContentBlock(block schemas.ChatContentBlock) ([]BedrockContentBlock, error) {
	switch block.Type {
	case schemas.ChatContentBlockTypeText:
		if block.Text == nil {
			return []BedrockContentBlock{}, nil
		}
		blocks := []BedrockContentBlock{
			{
				Text: block.Text,
			},
		}
		// Cache point must be in a separate block
		if block.CacheControl != nil {
			blocks = append(blocks, BedrockContentBlock{
				CachePoint: &BedrockCachePoint{
					Type: BedrockCachePointTypeDefault,
				},
			})
		}
		return blocks, nil

	case schemas.ChatContentBlockTypeImage:
		if block.ImageURLStruct == nil {
			return nil, fmt.Errorf("image_url block missing image_url field")
		}

		imageSource, err := convertImageToBedrockSource(block.ImageURLStruct.URL)
		if err != nil {
			return nil, fmt.Errorf("failed to convert image: %w", err)
		}
		blocks := []BedrockContentBlock{
			{
				Image: imageSource,
			},
		}
		// Cache point must be in a separate block
		if block.CacheControl != nil {
			blocks = append(blocks, BedrockContentBlock{
				CachePoint: &BedrockCachePoint{
					Type: BedrockCachePointTypeDefault,
				},
			})
		}
		return blocks, nil

	case schemas.ChatContentBlockTypeFile:
		if block.File == nil {
			return nil, fmt.Errorf("file block missing file field")
		}

		documentSource := &BedrockDocumentSource{
			Name:   "document",
			Format: "pdf",
			Source: &BedrockDocumentSourceData{},
		}

		// Set filename (normalized for Bedrock)
		if block.File.Filename != nil {
			documentSource.Name = normalizeBedrockFilename(*block.File.Filename)
		}

		// Convert MIME type to Bedrock format (pdf or txt)
		isText := false
		if block.File.FileType != nil {
			fileType := *block.File.FileType
			if fileType == "text/plain" || fileType == "txt" {
				documentSource.Format = "txt"
				isText = true
			} else if strings.Contains(fileType, "pdf") || fileType == "pdf" {
				documentSource.Format = "pdf"
			}
		}

		// Handle file data - strip data URL prefix if present
		if block.File.FileData != nil {
			fileData := *block.File.FileData

			// Check if it's a data URL and extract raw base64
			if strings.HasPrefix(fileData, "data:") {
				urlInfo := schemas.ExtractURLTypeInfo(fileData)
				if urlInfo.DataURLWithoutPrefix != nil {
					documentSource.Source.Bytes = urlInfo.DataURLWithoutPrefix
					return []BedrockContentBlock{
						{
							Document: documentSource,
						},
					}, nil
				}
			}

			// Set text or bytes based on file type
			if isText {
				documentSource.Source.Text = &fileData // Plain text
				encoded := base64.StdEncoding.EncodeToString([]byte(fileData))
				documentSource.Source.Bytes = &encoded // Also sets Bytes
			} else {
				documentSource.Source.Bytes = &fileData
			}
		}

		return []BedrockContentBlock{
			{
				Document: documentSource,
			},
		}, nil
	case schemas.ChatContentBlockTypeInputAudio:
		// Bedrock doesn't support audio input in Converse API
		return nil, fmt.Errorf("audio input not supported in Bedrock Converse API")

	default:
		return nil, fmt.Errorf("unsupported content block type: %s", block.Type)
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
// Ref: https://aws.amazon.com/blogs/machine-learning/structured-data-response-with-amazon-bedrock-prompt-engineering-and-tool-use/
func convertResponseFormatToTool(ctx *schemas.BifrostContext, params *schemas.ChatParameters) *BedrockTool {
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
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, toolName)

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

// convertTextFormatToTool converts a text config to a Bedrock tool for structured outpute
func convertTextFormatToTool(ctx *schemas.BifrostContext, textConfig *schemas.ResponsesTextConfig) *BedrockTool {
	if textConfig == nil || textConfig.Format == nil {
		return nil
	}

	format := textConfig.Format
	if format.Type != "json_schema" {
		return nil
	}

	toolName := "json_response"
	if format.Name != nil && strings.TrimSpace(*format.Name) != "" {
		toolName = strings.TrimSpace(*format.Name)
	}

	description := "Returns structured JSON output"
	if format.JSONSchema.Description != nil {
		description = *format.JSONSchema.Description
	}

	toolName = fmt.Sprintf("bf_so_%s", toolName)
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, toolName)

	var schemaObj any
	if format.JSONSchema != nil {
		schemaObj = *format.JSONSchema
	} else {
		return nil // Schema is required for Bedrock tooling
	}

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
func convertToolConfig(model string, params *schemas.ChatParameters) *BedrockToolConfig {
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
				schemaMap := map[string]interface{}{
					"type":       tool.Function.Parameters.Type,
					"properties": tool.Function.Parameters.Properties,
				}
				// Add required field if present
				if len(tool.Function.Parameters.Required) > 0 {
					schemaMap["required"] = tool.Function.Parameters.Required
				}
				// Add description if present
				if tool.Function.Parameters.Description != nil {
					schemaMap["description"] = *tool.Function.Parameters.Description
				}
				// Add enum if present
				if len(tool.Function.Parameters.Enum) > 0 {
					schemaMap["enum"] = tool.Function.Parameters.Enum
				}
				// Add additionalProperties if present
				if tool.Function.Parameters.AdditionalProperties != nil {
					schemaMap["additionalProperties"] = tool.Function.Parameters.AdditionalProperties
				}
				// Add JSON Schema definition fields
				if tool.Function.Parameters.Defs != nil {
					schemaMap["$defs"] = tool.Function.Parameters.Defs
				}
				if tool.Function.Parameters.Definitions != nil {
					schemaMap["definitions"] = tool.Function.Parameters.Definitions
				}
				if tool.Function.Parameters.Ref != nil {
					schemaMap["$ref"] = *tool.Function.Parameters.Ref
				}
				// Add array schema fields
				if tool.Function.Parameters.Items != nil {
					schemaMap["items"] = tool.Function.Parameters.Items
				}
				if tool.Function.Parameters.MinItems != nil {
					schemaMap["minItems"] = *tool.Function.Parameters.MinItems
				}
				if tool.Function.Parameters.MaxItems != nil {
					schemaMap["maxItems"] = *tool.Function.Parameters.MaxItems
				}
				// Add composition fields
				if len(tool.Function.Parameters.AnyOf) > 0 {
					schemaMap["anyOf"] = tool.Function.Parameters.AnyOf
				}
				if len(tool.Function.Parameters.OneOf) > 0 {
					schemaMap["oneOf"] = tool.Function.Parameters.OneOf
				}
				if len(tool.Function.Parameters.AllOf) > 0 {
					schemaMap["allOf"] = tool.Function.Parameters.AllOf
				}
				// Add string validation fields
				if tool.Function.Parameters.Format != nil {
					schemaMap["format"] = *tool.Function.Parameters.Format
				}
				if tool.Function.Parameters.Pattern != nil {
					schemaMap["pattern"] = *tool.Function.Parameters.Pattern
				}
				if tool.Function.Parameters.MinLength != nil {
					schemaMap["minLength"] = *tool.Function.Parameters.MinLength
				}
				if tool.Function.Parameters.MaxLength != nil {
					schemaMap["maxLength"] = *tool.Function.Parameters.MaxLength
				}
				// Add number validation fields
				if tool.Function.Parameters.Minimum != nil {
					schemaMap["minimum"] = *tool.Function.Parameters.Minimum
				}
				if tool.Function.Parameters.Maximum != nil {
					schemaMap["maximum"] = *tool.Function.Parameters.Maximum
				}
				// Add misc fields
				if tool.Function.Parameters.Title != nil {
					schemaMap["title"] = *tool.Function.Parameters.Title
				}
				if tool.Function.Parameters.Default != nil {
					schemaMap["default"] = tool.Function.Parameters.Default
				}
				if tool.Function.Parameters.Nullable != nil {
					schemaMap["nullable"] = *tool.Function.Parameters.Nullable
				}
				schemaObject = schemaMap
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

			if tool.CacheControl != nil && !schemas.IsNovaModel(model) {
				bedrockTools = append(bedrockTools, BedrockTool{
					CachePoint: &BedrockCachePoint{
						Type: BedrockCachePointTypeDefault,
					},
				})
			}
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
	if bifrostErr.Error != nil && bifrostErr.Error.Code != nil {
		bedrockErr.Type = *bifrostErr.Error.Code
		bedrockErr.Code = bifrostErr.Error.Code
	} else if bifrostErr.Type != nil {
		bedrockErr.Type = *bifrostErr.Type
	} else {
		bedrockErr.Type = "InternalServerError"
	}

	return bedrockErr
}

// convertMapToToolFunctionParameters converts a map[string]interface{} to ToolFunctionParameters
// This handles the conversion from flexible parameter formats to Bifrost's structured format
func convertMapToToolFunctionParameters(paramsMap map[string]interface{}) *schemas.ToolFunctionParameters {
	if paramsMap == nil {
		return nil
	}

	params := &schemas.ToolFunctionParameters{}

	// Extract type
	if typeVal, ok := paramsMap["type"].(string); ok {
		params.Type = typeVal
	}

	// Extract description
	if descVal, ok := paramsMap["description"].(string); ok {
		params.Description = &descVal
	}

	// Extract properties
	if props, ok := schemas.SafeExtractOrderedMap(paramsMap["properties"]); ok {
		params.Properties = &props
	}

	// Extract required
	if required, ok := paramsMap["required"].([]interface{}); ok {
		reqStrings := make([]string, 0, len(required))
		for _, r := range required {
			if rStr, ok := r.(string); ok {
				reqStrings = append(reqStrings, rStr)
			}
		}
		params.Required = reqStrings
	} else if required, ok := paramsMap["required"].([]string); ok {
		params.Required = required
	}

	// Extract enum
	if enumVal, ok := paramsMap["enum"].([]interface{}); ok {
		enum := make([]string, 0, len(enumVal))
		for _, v := range enumVal {
			if s, ok := v.(string); ok {
				enum = append(enum, s)
			}
		}
		params.Enum = enum
	}

	// Extract additionalProperties
	if addPropsVal, ok := paramsMap["additionalProperties"].(bool); ok {
		params.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
			AdditionalPropertiesBool: &addPropsVal,
		}
	} else if addPropsVal, ok := schemas.SafeExtractOrderedMap(paramsMap["additionalProperties"]); ok {
		params.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
			AdditionalPropertiesMap: &addPropsVal,
		}
	}

	// Extract $defs (JSON Schema draft 2019-09+)
	if defsVal, ok := schemas.SafeExtractOrderedMap(paramsMap["$defs"]); ok {
		params.Defs = &defsVal
	}

	// Extract definitions (legacy JSON Schema draft-07)
	if defsVal, ok := schemas.SafeExtractOrderedMap(paramsMap["definitions"]); ok {
		params.Definitions = &defsVal
	}

	// Extract $ref
	if refVal, ok := paramsMap["$ref"].(string); ok {
		params.Ref = &refVal
	}

	// Extract items (array element schema)
	if itemsVal, ok := schemas.SafeExtractOrderedMap(paramsMap["items"]); ok {
		params.Items = &itemsVal
	}

	// Extract minItems
	if minItemsVal, ok := bedrockExtractInt64(paramsMap["minItems"]); ok {
		params.MinItems = &minItemsVal
	}

	// Extract maxItems
	if maxItemsVal, ok := bedrockExtractInt64(paramsMap["maxItems"]); ok {
		params.MaxItems = &maxItemsVal
	}

	// Extract anyOf
	if anyOfVal, ok := paramsMap["anyOf"].([]interface{}); ok {
		anyOf := make([]schemas.OrderedMap, 0, len(anyOfVal))
		for _, v := range anyOfVal {
			if m, ok := schemas.SafeExtractOrderedMap(v); ok {
				anyOf = append(anyOf, m)
			}
		}
		params.AnyOf = anyOf
	}

	// Extract oneOf
	if oneOfVal, ok := paramsMap["oneOf"].([]interface{}); ok {
		oneOf := make([]schemas.OrderedMap, 0, len(oneOfVal))
		for _, v := range oneOfVal {
			if m, ok := schemas.SafeExtractOrderedMap(v); ok {
				oneOf = append(oneOf, m)
			}
		}
		params.OneOf = oneOf
	}

	// Extract allOf
	if allOfVal, ok := paramsMap["allOf"].([]interface{}); ok {
		allOf := make([]schemas.OrderedMap, 0, len(allOfVal))
		for _, v := range allOfVal {
			if m, ok := schemas.SafeExtractOrderedMap(v); ok {
				allOf = append(allOf, m)
			}
		}
		params.AllOf = allOf
	}

	// Extract format
	if formatVal, ok := paramsMap["format"].(string); ok {
		params.Format = &formatVal
	}

	// Extract pattern
	if patternVal, ok := paramsMap["pattern"].(string); ok {
		params.Pattern = &patternVal
	}

	// Extract minLength
	if minLengthVal, ok := bedrockExtractInt64(paramsMap["minLength"]); ok {
		params.MinLength = &minLengthVal
	}

	// Extract maxLength
	if maxLengthVal, ok := bedrockExtractInt64(paramsMap["maxLength"]); ok {
		params.MaxLength = &maxLengthVal
	}

	// Extract minimum
	if minVal, ok := bedrockExtractFloat64(paramsMap["minimum"]); ok {
		params.Minimum = &minVal
	}

	// Extract maximum
	if maxVal, ok := bedrockExtractFloat64(paramsMap["maximum"]); ok {
		params.Maximum = &maxVal
	}

	// Extract title
	if titleVal, ok := paramsMap["title"].(string); ok {
		params.Title = &titleVal
	}

	// Extract default
	if defaultVal, exists := paramsMap["default"]; exists {
		params.Default = defaultVal
	}

	// Extract nullable
	if nullableVal, ok := paramsMap["nullable"].(bool); ok {
		params.Nullable = &nullableVal
	}

	return params
}

// bedrockExtractInt64 extracts an int64 from various numeric types
func bedrockExtractInt64(v interface{}) (int64, bool) {
	switch val := v.(type) {
	case int:
		return int64(val), true
	case int64:
		return val, true
	case float64:
		return int64(val), true
	case float32:
		return int64(val), true
	default:
		return 0, false
	}
}

// bedrockExtractFloat64 extracts a float64 from various numeric types
func bedrockExtractFloat64(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	default:
		return 0, false
	}
}
