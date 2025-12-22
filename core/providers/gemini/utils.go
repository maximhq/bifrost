package gemini

import (
	"encoding/base64"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

func (r *GeminiGenerationRequest) convertGenerationConfigToResponsesParameters() *schemas.ResponsesParameters {
	params := &schemas.ResponsesParameters{
		ExtraParams: make(map[string]interface{}),
	}

	config := r.GenerationConfig

	if config.Temperature != nil {
		params.Temperature = config.Temperature
	}
	if config.TopP != nil {
		params.TopP = config.TopP
	}
	if config.Logprobs != nil {
		params.TopLogProbs = schemas.Ptr(int(*config.Logprobs))
	}
	if config.TopK != nil {
		params.ExtraParams["top_k"] = *config.TopK
	}
	if config.MaxOutputTokens > 0 {
		params.MaxOutputTokens = schemas.Ptr(int(config.MaxOutputTokens))
	}
	if config.ThinkingConfig != nil {
		params.Reasoning = &schemas.ResponsesParametersReasoning{}
		if strings.Contains(r.Model, "openai") {
			params.Reasoning.Summary = schemas.Ptr("auto")
		}
		if config.ThinkingConfig.ThinkingLevel != ThinkingLevelUnspecified {
			switch config.ThinkingConfig.ThinkingLevel {
			case ThinkingLevelLow:
				params.Reasoning.Effort = schemas.Ptr("low")
			case ThinkingLevelHigh:
				params.Reasoning.Effort = schemas.Ptr("high")
			}
		}
		if config.ThinkingConfig.ThinkingBudget != nil {
			params.Reasoning.MaxTokens = schemas.Ptr(int(*config.ThinkingConfig.ThinkingBudget))
			switch *config.ThinkingConfig.ThinkingBudget {
			case 0:
				params.Reasoning.Effort = schemas.Ptr("none")
			case -1:
				// dynamic thinking budget
				params.Reasoning.Effort = schemas.Ptr("medium")
				params.Reasoning.MaxTokens = schemas.Ptr(-1)
			}
		}
	}
	if config.CandidateCount > 0 {
		params.ExtraParams["candidate_count"] = config.CandidateCount
	}
	if len(config.StopSequences) > 0 {
		params.ExtraParams["stop_sequences"] = config.StopSequences
	}
	if config.PresencePenalty != nil {
		params.ExtraParams["presence_penalty"] = config.PresencePenalty
	}
	if config.FrequencyPenalty != nil {
		params.ExtraParams["frequency_penalty"] = config.FrequencyPenalty
	}
	if config.Seed != nil {
		params.ExtraParams["seed"] = int(*config.Seed)
	}
	if config.ResponseMIMEType != "" {
		switch config.ResponseMIMEType {
		case "application/json":
			params.Text = buildOpenAIResponseFormat(config.ResponseSchema, config.ResponseJSONSchema)
		case "text/plain":
			params.Text = &schemas.ResponsesTextConfig{
				Format: &schemas.ResponsesTextConfigFormat{
					Type: "text",
				},
			}
		}
	}
	if config.ResponseSchema != nil {
		params.ExtraParams["response_schema"] = config.ResponseSchema
	}
	if config.ResponseJSONSchema != nil {
		params.ExtraParams["response_json_schema"] = config.ResponseJSONSchema
	}
	if config.ResponseLogprobs {
		params.ExtraParams["response_logprobs"] = config.ResponseLogprobs
	}
	return params
}

// convertSchemaToFunctionParameters converts genai.Schema to schemas.FunctionParameters
func convertSchemaToFunctionParameters(schema *Schema) schemas.ToolFunctionParameters {
	params := schemas.ToolFunctionParameters{
		Type: strings.ToLower(string(schema.Type)),
	}

	if schema.Description != "" {
		params.Description = &schema.Description
	}

	if len(schema.Required) > 0 {
		params.Required = schema.Required
	}

	if len(schema.Properties) > 0 {
		params.Properties = schemas.Ptr(convertSchemaToMap(schema))
	}

	if len(schema.Enum) > 0 {
		params.Enum = schema.Enum
	}

	return params
}

func convertSchemaToMap(schema *Schema) schemas.OrderedMap {
	// Convert map[string]*Schema to map[string]interface{} using JSON marshaling
	data, err := sonic.Marshal(schema.Properties)
	if err != nil {
		return make(map[string]interface{})
	}

	var properties map[string]interface{}
	if err := sonic.Unmarshal(data, &properties); err != nil {
		return make(map[string]interface{})
	}

	result := convertTypeToLowerCase(properties)

	// Type assert back to map[string]interface{}
	if resultMap, ok := result.(map[string]interface{}); ok {
		return resultMap
	}
	return make(map[string]interface{})
}

// convertTypeToLowerCase recursively converts all 'type' fields to lowercase in a schema
func convertTypeToLowerCase(schema interface{}) interface{} {
	switch v := schema.(type) {
	case map[string]interface{}:
		// Process map
		newMap := make(map[string]interface{})
		for key, value := range v {
			if key == "type" {
				// Convert type field to lowercase if it's a string
				if strValue, ok := value.(string); ok {
					newMap[key] = strings.ToLower(strValue)
				} else {
					newMap[key] = value
				}
			} else {
				// Recursively process other fields
				newMap[key] = convertTypeToLowerCase(value)
			}
		}
		return newMap
	case []interface{}:
		// Process array
		newSlice := make([]interface{}, len(v))
		for i, item := range v {
			newSlice[i] = convertTypeToLowerCase(item)
		}
		return newSlice
	default:
		// Return primitive values as-is (strings, numbers, booleans, etc.)
		return v
	}
}

// isImageMimeType checks if a MIME type represents an image format
func isImageMimeType(mimeType string) bool {
	if mimeType == "" {
		return false
	}

	// Convert to lowercase for case-insensitive comparison
	mimeType = strings.ToLower(mimeType)

	// Remove any parameters (e.g., "image/jpeg; charset=utf-8" -> "image/jpeg")
	if idx := strings.Index(mimeType, ";"); idx != -1 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}

	// If it starts with "image/", it's an image
	if strings.HasPrefix(mimeType, "image/") {
		return true
	}

	// Check for common image formats that might not have the "image/" prefix
	commonImageTypes := []string{
		"jpeg",
		"jpg",
		"png",
		"gif",
		"webp",
		"bmp",
		"svg",
		"tiff",
		"ico",
		"avif",
	}

	// Check if the mimeType contains any of the common image type strings
	for _, imageType := range commonImageTypes {
		if strings.Contains(mimeType, imageType) {
			return true
		}
	}

	return false
}

var (
	// Maps Gemini finish reasons to Bifrost format
	geminiFinishReasonToBifrost = map[FinishReason]string{
		FinishReasonStop:                  "stop",
		FinishReasonMaxTokens:             "length",
		FinishReasonSafety:                "content_filter",
		FinishReasonRecitation:            "content_filter",
		FinishReasonLanguage:              "content_filter",
		FinishReasonOther:                 "stop",
		FinishReasonBlocklist:             "content_filter",
		FinishReasonProhibitedContent:     "content_filter",
		FinishReasonSPII:                  "content_filter",
		FinishReasonMalformedFunctionCall: "tool_calls",
		FinishReasonImageSafety:           "content_filter",
		FinishReasonUnexpectedToolCall:    "tool_calls",
	}
)

// ConvertGeminiFinishReasonToBifrost converts Gemini finish reasons to Bifrost format
func ConvertGeminiFinishReasonToBifrost(providerReason FinishReason) string {
	if bifrostReason, ok := geminiFinishReasonToBifrost[providerReason]; ok {
		return bifrostReason
	}
	return string(providerReason)
}

// extractUsageMetadata extracts usage metadata from the Gemini response
func (r *GenerateContentResponse) extractUsageMetadata() (int, int, int, int, int) {
	var inputTokens, outputTokens, totalTokens, cachedTokens, reasoningTokens int
	if r.UsageMetadata != nil {
		inputTokens = int(r.UsageMetadata.PromptTokenCount)
		outputTokens = int(r.UsageMetadata.CandidatesTokenCount)
		totalTokens = int(r.UsageMetadata.TotalTokenCount)
		cachedTokens = int(r.UsageMetadata.CachedContentTokenCount)
		reasoningTokens = int(r.UsageMetadata.ThoughtsTokenCount)
	}
	return inputTokens, outputTokens, totalTokens, cachedTokens, reasoningTokens
}

// convertGeminiUsageMetadataToChatUsage converts Gemini usage metadata to Bifrost chat LLM usage
func convertGeminiUsageMetadataToChatUsage(metadata *GenerateContentResponseUsageMetadata) *schemas.BifrostLLMUsage {
	if metadata == nil {
		return nil
	}

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     int(metadata.PromptTokenCount),
		CompletionTokens: int(metadata.CandidatesTokenCount),
		TotalTokens:      int(metadata.TotalTokenCount),
	}

	// Add cached tokens if present
	if metadata.CachedContentTokenCount > 0 {
		usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
			CachedTokens: int(metadata.CachedContentTokenCount),
		}
	}

	// Add reasoning tokens if present
	if metadata.ThoughtsTokenCount > 0 {
		usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{
			ReasoningTokens: int(metadata.ThoughtsTokenCount),
		}
	}

	return usage
}

// convertGeminiUsageMetadataToResponsesUsage converts Gemini usage metadata to Bifrost responses usage
func convertGeminiUsageMetadataToResponsesUsage(metadata *GenerateContentResponseUsageMetadata) *schemas.ResponsesResponseUsage {
	if metadata == nil {
		return nil
	}

	usage := &schemas.ResponsesResponseUsage{
		TotalTokens:         int(metadata.TotalTokenCount),
		InputTokens:         int(metadata.PromptTokenCount),
		OutputTokens:        int(metadata.CandidatesTokenCount),
		OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{},
		InputTokensDetails:  &schemas.ResponsesResponseInputTokens{},
	}

	// Add cached tokens if present
	if metadata.CachedContentTokenCount > 0 {
		usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{
			CachedTokens: int(metadata.CachedContentTokenCount),
		}
	}

	if metadata.CandidatesTokensDetails != nil {
		for _, detail := range metadata.CandidatesTokensDetails {
			switch detail.Modality {
			case "AUDIO":
				usage.OutputTokensDetails.AudioTokens = int(detail.TokenCount)
			}
		}
	}

	if metadata.ThoughtsTokenCount > 0 {
		usage.OutputTokensDetails.ReasoningTokens = int(metadata.ThoughtsTokenCount)
	}

	return usage
}

// convertParamsToGenerationConfig converts Bifrost parameters to Gemini GenerationConfig
func convertParamsToGenerationConfig(params *schemas.ChatParameters, responseModalities []string) GenerationConfig {
	config := GenerationConfig{}

	// Add response modalities if specified
	if len(responseModalities) > 0 {
		var modalities []Modality
		for _, mod := range responseModalities {
			modalities = append(modalities, Modality(mod))
		}
		config.ResponseModalities = modalities
	}

	// Map standard parameters
	if params.Stop != nil {
		config.StopSequences = params.Stop
	}
	if params.MaxCompletionTokens != nil {
		config.MaxOutputTokens = int32(*params.MaxCompletionTokens)
	}
	if params.Temperature != nil {
		temp := float64(*params.Temperature)
		config.Temperature = &temp
	}
	if params.TopP != nil {
		topP := float64(*params.TopP)
		config.TopP = &topP
	}
	if params.PresencePenalty != nil {
		penalty := float64(*params.PresencePenalty)
		config.PresencePenalty = &penalty
	}
	if params.FrequencyPenalty != nil {
		penalty := float64(*params.FrequencyPenalty)
		config.FrequencyPenalty = &penalty
	}
	if params.Reasoning != nil {
		config.ThinkingConfig = &GenerationConfigThinkingConfig{
			IncludeThoughts: true,
		}
		if params.Reasoning.MaxTokens != nil {
			config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(*params.Reasoning.MaxTokens))
		} else if params.Reasoning.Effort != nil {
			switch *params.Reasoning.Effort {
			case "minimal", "low":
				config.ThinkingConfig.ThinkingLevel = ThinkingLevelLow
			case "medium", "high":
				config.ThinkingConfig.ThinkingLevel = ThinkingLevelHigh
			}
		}
	}

	// Handle response_format to response_schema conversion
	if params.ResponseFormat != nil {
		formatMap, ok := (*params.ResponseFormat).(map[string]interface{})
		if ok {
			formatType, typeOk := formatMap["type"].(string)
			if typeOk {
				switch formatType {
				case "json_schema":
					// OpenAI Structured Outputs: {"type": "json_schema", "json_schema": {...}}
					if schema := extractSchemaFromResponseFormat(params.ResponseFormat); schema != nil {
						config.ResponseMIMEType = "application/json"
						config.ResponseSchema = schema
					}
				case "json_object":
					// Maps to Gemini's responseMimeType without schema
					config.ResponseMIMEType = "application/json"
				}
			}
		}
	}

	if params.ExtraParams != nil {
		if topK, ok := params.ExtraParams["top_k"]; ok {
			if val, success := schemas.SafeExtractInt(topK); success {
				config.TopK = schemas.Ptr(val)
			}
		}
		if responseMimeType, ok := schemas.SafeExtractString(params.ExtraParams["response_mime_type"]); ok {
			config.ResponseMIMEType = responseMimeType
		}
		// Override with explicit response_schema if provided in ExtraParams
		if responseSchema, ok := params.ExtraParams["response_schema"]; ok {
			if schemaBytes, err := sonic.Marshal(responseSchema); err == nil {
				schema := &Schema{}
				if err := sonic.Unmarshal(schemaBytes, schema); err == nil {
					config.ResponseSchema = schema
				}
			}
		}
		if responseJsonSchema, ok := params.ExtraParams["response_json_schema"]; ok {
			config.ResponseJSONSchema = responseJsonSchema
		}
	}

	return config
}

// convertBifrostToolsToGemini converts Bifrost tools to Gemini format
func convertBifrostToolsToGemini(bifrostTools []schemas.ChatTool) []Tool {
	var geminiTools []Tool

	for _, tool := range bifrostTools {
		if tool.Type == "" {
			continue
		}
		if tool.Type == "function" && tool.Function != nil {
			fd := &FunctionDeclaration{
				Name: tool.Function.Name,
			}
			if tool.Function.Parameters != nil {
				fd.Parameters = convertFunctionParametersToSchema(*tool.Function.Parameters)
			}
			if tool.Function.Description != nil {
				fd.Description = *tool.Function.Description
			}
			geminiTool := Tool{
				FunctionDeclarations: []*FunctionDeclaration{fd},
			}
			geminiTools = append(geminiTools, geminiTool)
		}
	}

	return geminiTools
}

// convertFunctionParametersToSchema converts Bifrost function parameters to Gemini Schema
func convertFunctionParametersToSchema(params schemas.ToolFunctionParameters) *Schema {
	schema := &Schema{
		Type: Type(params.Type),
	}

	if params.Description != nil {
		schema.Description = *params.Description
	}

	if len(params.Required) > 0 {
		schema.Required = params.Required
	}

	if params.Properties != nil && len(*params.Properties) > 0 {
		schema.Properties = make(map[string]*Schema)
		// Note: This is a simplified conversion. In practice, you'd need to
		// recursively convert nested schemas
		for k, v := range *params.Properties {
			// Convert interface{} to Schema - this would need more sophisticated logic
			if propMap, ok := v.(map[string]interface{}); ok {
				propSchema := &Schema{}
				if propType, ok := propMap["type"].(string); ok {
					propSchema.Type = Type(propType)
				}
				if propDesc, ok := propMap["description"].(string); ok {
					propSchema.Description = propDesc
				}
				schema.Properties[k] = propSchema
			}
		}
	}

	return schema
}

// convertToolChoiceToToolConfig converts Bifrost tool choice to Gemini tool config
func convertToolChoiceToToolConfig(toolChoice *schemas.ChatToolChoice) ToolConfig {
	config := ToolConfig{}
	functionCallingConfig := FunctionCallingConfig{}

	if toolChoice.ChatToolChoiceStr != nil {
		// Map string values to Gemini's enum values
		switch *toolChoice.ChatToolChoiceStr {
		case "none":
			functionCallingConfig.Mode = FunctionCallingConfigModeNone
		case "auto":
			functionCallingConfig.Mode = FunctionCallingConfigModeAuto
		case "any", "required":
			functionCallingConfig.Mode = FunctionCallingConfigModeAny
		default:
			functionCallingConfig.Mode = FunctionCallingConfigModeAuto
		}
	} else if toolChoice.ChatToolChoiceStruct != nil {
		switch toolChoice.ChatToolChoiceStruct.Type {
		case schemas.ChatToolChoiceTypeNone:
			functionCallingConfig.Mode = FunctionCallingConfigModeNone
		case schemas.ChatToolChoiceTypeFunction:
			functionCallingConfig.Mode = FunctionCallingConfigModeAny
		case schemas.ChatToolChoiceTypeRequired:
			functionCallingConfig.Mode = FunctionCallingConfigModeAny
		default:
			functionCallingConfig.Mode = FunctionCallingConfigModeAuto
		}

		// Handle specific function selection
		if toolChoice.ChatToolChoiceStruct.Function.Name != "" {
			functionCallingConfig.AllowedFunctionNames = []string{toolChoice.ChatToolChoiceStruct.Function.Name}
		}
	}

	config.FunctionCallingConfig = &functionCallingConfig
	return config
}

// addSpeechConfigToGenerationConfig adds speech configuration to the generation config
func addSpeechConfigToGenerationConfig(config *GenerationConfig, voiceConfig *schemas.SpeechVoiceInput) {
	speechConfig := SpeechConfig{}

	// Handle single voice configuration
	if voiceConfig != nil && voiceConfig.Voice != nil {
		speechConfig.VoiceConfig = &VoiceConfig{
			PrebuiltVoiceConfig: &PrebuiltVoiceConfig{
				VoiceName: *voiceConfig.Voice,
			},
		}
	}

	// Handle multi-speaker voice configuration
	if voiceConfig != nil && len(voiceConfig.MultiVoiceConfig) > 0 {
		var speakerVoiceConfigs []*SpeakerVoiceConfig
		for _, vc := range voiceConfig.MultiVoiceConfig {
			speakerVoiceConfigs = append(speakerVoiceConfigs, &SpeakerVoiceConfig{
				Speaker: vc.Speaker,
				VoiceConfig: &VoiceConfig{
					PrebuiltVoiceConfig: &PrebuiltVoiceConfig{
						VoiceName: vc.Voice,
					},
				},
			})
		}

		speechConfig.MultiSpeakerVoiceConfig = &MultiSpeakerVoiceConfig{
			SpeakerVoiceConfigs: speakerVoiceConfigs,
		}
	}

	config.SpeechConfig = &speechConfig
}

// convertBifrostMessagesToGemini converts Bifrost messages to Gemini format
func convertBifrostMessagesToGemini(messages []schemas.ChatMessage) []Content {
	var contents []Content

	for _, message := range messages {
		var parts []*Part

		// Handle content
		if message.Content != nil {
			if message.Content.ContentStr != nil && *message.Content.ContentStr != "" {
				parts = append(parts, &Part{
					Text: *message.Content.ContentStr,
				})
			} else if message.Content.ContentBlocks != nil {
				for _, block := range message.Content.ContentBlocks {
					if block.Text != nil {
						parts = append(parts, &Part{
							Text: *block.Text,
						})
					}
					// Handle other content block types as needed
				}
			}
		}

		// Handle tool calls for assistant messages
		if message.ChatAssistantMessage != nil && message.ChatAssistantMessage.ToolCalls != nil {
			for _, toolCall := range message.ChatAssistantMessage.ToolCalls {
				// Convert tool call to function call part
				if toolCall.Function.Name != nil {
					// Create function call part - simplified implementation
					argsMap := make(map[string]any)
					if toolCall.Function.Arguments != "" {
						sonic.Unmarshal([]byte(toolCall.Function.Arguments), &argsMap)
					}
					// Handle ID: use it if available, otherwise fallback to function name
					callID := *toolCall.Function.Name
					if toolCall.ID != nil && strings.TrimSpace(*toolCall.ID) != "" {
						callID = *toolCall.ID
					}

					part := &Part{
						FunctionCall: &FunctionCall{
							ID:   callID,
							Name: *toolCall.Function.Name,
							Args: argsMap,
						},
					}

					// Preserve thought signature from extra_content (required for Gemini 3 Pro)
					if toolCall.ExtraContent != nil {
						if googleData, ok := toolCall.ExtraContent["google"].(map[string]interface{}); ok {
							if thoughtSig, ok := googleData["thought_signature"].(string); ok {
								// Decode the base64 string to raw bytes
								decoded, err := base64.StdEncoding.DecodeString(thoughtSig)
								if err == nil {
									part.ThoughtSignature = decoded
								}
							}
						}
					}

					parts = append(parts, part)
				}
			}
		}

		// Handle tool response messages
		if message.Role == schemas.ChatMessageRoleTool && message.ChatToolMessage != nil {
			// Parse the response content
			var responseData map[string]any
			var contentStr string

			if message.Content != nil {
				// Extract content string from ContentStr or ContentBlocks
				if message.Content.ContentStr != nil && *message.Content.ContentStr != "" {
					contentStr = *message.Content.ContentStr
				} else if message.Content.ContentBlocks != nil {
					// Fallback: try to extract text from content blocks
					var textParts []string
					for _, block := range message.Content.ContentBlocks {
						if block.Text != nil && *block.Text != "" {
							textParts = append(textParts, *block.Text)
						}
					}
					if len(textParts) > 0 {
						contentStr = strings.Join(textParts, "\n")
					}
				}
			}

			// Try to unmarshal as JSON
			if contentStr != "" {
				err := sonic.Unmarshal([]byte(contentStr), &responseData)
				if err != nil {
					// If unmarshaling fails, wrap the original string to preserve it
					responseData = map[string]any{
						"content": contentStr,
					}
				}
			} else {
				// If no content at all, use empty map to avoid nil
				responseData = map[string]any{}
			}

			// Use ToolCallID if available, ensuring it's not nil
			callID := ""
			if message.ChatToolMessage.ToolCallID != nil {
				callID = *message.ChatToolMessage.ToolCallID
			}

			parts = append(parts, &Part{
				FunctionResponse: &FunctionResponse{
					ID:       callID,
					Name:     callID, // Gemini uses name for correlation
					Response: responseData,
				},
			})
		}

		if len(parts) > 0 {
			content := Content{
				Parts: parts,
				Role:  string(message.Role),
			}
			if message.Role == schemas.ChatMessageRoleUser {
				content.Role = "user"
			} else {
				content.Role = "model"
			}
			contents = append(contents, content)
		}
	}

	return contents
}

// normalizeSchemaTypes recursively normalizes type values from uppercase to lowercase
func normalizeSchemaTypes(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}

	normalized := make(map[string]interface{}, len(schema))
	for k, v := range schema {
		normalized[k] = v
	}

	// Normalize type field if it exists
	if typeVal, ok := normalized["type"].(string); ok {
		normalized["type"] = strings.ToLower(typeVal)
	}

	// Recursively normalize properties (create new map only if present)
	if properties, ok := schema["properties"].(map[string]interface{}); ok {
		newProps := make(map[string]interface{}, len(properties))
		for key, prop := range properties {
			if propMap, ok := prop.(map[string]interface{}); ok {
				newProps[key] = normalizeSchemaTypes(propMap)
			} else {
				newProps[key] = prop
			}
		}
		normalized["properties"] = newProps
	}

	// Recursively normalize items (for arrays)
	if items, ok := schema["items"].(map[string]interface{}); ok {
		normalized["items"] = normalizeSchemaTypes(items)
	}

	// Recursively normalize anyOf
	if anyOf, ok := schema["anyOf"].([]interface{}); ok {
		newAnyOf := make([]interface{}, len(anyOf))
		for i, item := range anyOf {
			if itemMap, ok := item.(map[string]interface{}); ok {
				newAnyOf[i] = normalizeSchemaTypes(itemMap)
			} else {
				newAnyOf[i] = item
			}
		}
		normalized["anyOf"] = newAnyOf
	}

	// Recursively normalize oneOf
	if oneOf, ok := schema["oneOf"].([]interface{}); ok {
		newOneOf := make([]interface{}, len(oneOf))
		for i, item := range oneOf {
			if itemMap, ok := item.(map[string]interface{}); ok {
				newOneOf[i] = normalizeSchemaTypes(itemMap)
			} else {
				newOneOf[i] = item
			}
		}
		normalized["oneOf"] = newOneOf
	}

	return normalized
}

// buildJSONSchemaFromMap converts a schema map to ResponsesTextConfigFormatJSONSchema
// with individual fields properly populated (not nested under Schema field)
func buildJSONSchemaFromMap(schemaMap map[string]interface{}) *schemas.ResponsesTextConfigFormatJSONSchema {
	// Normalize types (OBJECT → object, STRING → string, etc.)
	normalizedSchemaMap := normalizeSchemaTypes(schemaMap)

	jsonSchema := &schemas.ResponsesTextConfigFormatJSONSchema{}

	// Extract type
	if typeVal, ok := normalizedSchemaMap["type"].(string); ok {
		jsonSchema.Type = schemas.Ptr(typeVal)
	}

	// Extract properties
	if properties, ok := normalizedSchemaMap["properties"].(map[string]interface{}); ok {
		jsonSchema.Properties = &properties
	}

	// Extract required fields
	if required, ok := normalizedSchemaMap["required"].([]interface{}); ok {
		requiredStrs := make([]string, 0, len(required))
		for _, r := range required {
			if str, ok := r.(string); ok {
				requiredStrs = append(requiredStrs, str)
			}
		}
		if len(requiredStrs) > 0 {
			jsonSchema.Required = requiredStrs
		}
	} else if requiredStrs, ok := normalizedSchemaMap["required"].([]string); ok && len(requiredStrs) > 0 {
		jsonSchema.Required = requiredStrs
	}

	// Extract description
	if description, ok := normalizedSchemaMap["description"].(string); ok {
		jsonSchema.Description = schemas.Ptr(description)
	}

	// Extract additionalProperties
	if additionalProps, ok := normalizedSchemaMap["additionalProperties"].(bool); ok {
		jsonSchema.AdditionalProperties = schemas.Ptr(additionalProps)
	}

	// Extract name/title
	if name, ok := normalizedSchemaMap["name"].(string); ok {
		jsonSchema.Name = schemas.Ptr(name)
	} else if title, ok := normalizedSchemaMap["title"].(string); ok {
		jsonSchema.Name = schemas.Ptr(title)
	}

	return jsonSchema
}

// buildOpenAIResponseFormat builds OpenAI response_format for JSON types
func buildOpenAIResponseFormat(responseSchema *Schema, responseJsonSchema interface{}) *schemas.ResponsesTextConfig {
	var schemaMap map[string]interface{}
	name := "response_schema"

	// Prefer responseSchema over responseJsonSchema
	if responseSchema != nil {
		// Convert Schema struct to map
		schemaBytes, err := sonic.Marshal(responseSchema)
		if err == nil {
			if err := sonic.Unmarshal(schemaBytes, &schemaMap); err == nil {
				if responseSchema.Title != "" {
					name = responseSchema.Title
				}
			}
			// If unmarshal failed, schemaMap remains nil - will try next option
		}
	}

	if schemaMap == nil && responseJsonSchema != nil {
		// Use responseJsonSchema directly if it's a map
		if m, ok := responseJsonSchema.(map[string]interface{}); ok {
			schemaMap = m
			if title, ok := m["title"].(string); ok && title != "" {
				name = title
			}
		}
	}

	// No schema provided - use json_object mode
	if schemaMap == nil {
		return &schemas.ResponsesTextConfig{
			Format: &schemas.ResponsesTextConfigFormat{
				Type: "json_object",
			},
		}
	}

	// Build JSONSchema with individual fields spread out
	jsonSchema := buildJSONSchemaFromMap(schemaMap)

	return &schemas.ResponsesTextConfig{
		Format: &schemas.ResponsesTextConfigFormat{
			Type:       "json_schema",
			Name:       schemas.Ptr(name),
			Strict:     schemas.Ptr(false),
			JSONSchema: jsonSchema,
		},
	}
}

// extractSchemaFromResponseFormat extracts Gemini Schema from OpenAI's response_format structure
func extractSchemaFromResponseFormat(responseFormat *interface{}) *Schema {
	formatMap, ok := (*responseFormat).(map[string]interface{})
	if !ok {
		return nil
	}

	formatType, ok := formatMap["type"].(string)
	if !ok || formatType != "json_schema" {
		return nil
	}

	jsonSchemaObj, ok := formatMap["json_schema"].(map[string]interface{})
	if !ok {
		return nil
	}

	schemaObj, ok := jsonSchemaObj["schema"]
	if !ok {
		return nil
	}

	schemaMap, ok := schemaObj.(map[string]interface{})
	if !ok {
		return nil
	}

	// Convert map to Gemini Schema type via JSON marshaling
	schemaBytes, err := sonic.Marshal(schemaMap)
	if err != nil {
		return nil
	}

	schema := &Schema{}
	if err := sonic.Unmarshal(schemaBytes, schema); err != nil {
		return nil
	}

	return schema
}

// extractFunctionResponseOutput extracts the output text from a FunctionResponse.
// It first tries to extract the "output" field if present, otherwise marshals the entire response.
// Returns an empty string if the response is nil or extraction fails.
func extractFunctionResponseOutput(funcResp *FunctionResponse) string {
	if funcResp == nil || funcResp.Response == nil {
		return ""
	}

	// Try to extract "output" field first
	if outputVal, ok := funcResp.Response["output"]; ok {
		if outputStr, ok := outputVal.(string); ok {
			return outputStr
		}
	}

	// If no "output" key, marshal the entire response
	if jsonResponse, err := sonic.Marshal(funcResp.Response); err == nil {
		return string(jsonResponse)
	}

	return ""
}
