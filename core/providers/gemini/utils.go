package gemini

import (
	"bytes"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// convertGenerationConfigToChatParameters converts Gemini GenerationConfig to ChatParameters
func (r *GeminiGenerationRequest) convertGenerationConfigToChatParameters() *schemas.ChatParameters {
	params := &schemas.ChatParameters{
		ExtraParams: make(map[string]interface{}),
	}

	config := r.GenerationConfig

	// Map generation config fields to parameters
	if config.Temperature != nil {
		params.Temperature = config.Temperature
	}
	if config.TopP != nil {
		params.TopP = config.TopP
	}
	if config.TopK != nil {
		params.ExtraParams["top_k"] = *config.TopK
	}
	if config.MaxOutputTokens > 0 {
		params.MaxCompletionTokens = schemas.Ptr(int(config.MaxOutputTokens))
	}
	if config.CandidateCount > 0 {
		params.ExtraParams["candidate_count"] = config.CandidateCount
	}
	if len(config.StopSequences) > 0 {
		params.Stop = config.StopSequences
	}
	if config.PresencePenalty != nil {
		params.PresencePenalty = config.PresencePenalty
	}
	if config.FrequencyPenalty != nil {
		params.FrequencyPenalty = config.FrequencyPenalty
	}
	if config.Seed != nil {
		params.Seed = schemas.Ptr(int(*config.Seed))
	}
	if config.ResponseMIMEType != "" {
		params.ExtraParams["response_mime_type"] = config.ResponseMIMEType

		// Convert Gemini's response format to OpenAI's response_format for compatibility
		switch config.ResponseMIMEType {
		case "application/json":
			params.ResponseFormat = buildOpenAIResponseFormat(config.ResponseSchema, config.ResponseJsonSchema)
		case "text/plain":
			// Gemini text/plain → OpenAI text format
			var responseFormat interface{} = map[string]interface{}{
				"type": "text",
			}
			params.ResponseFormat = &responseFormat
		}
	}
	if config.ResponseSchema != nil {
		params.ExtraParams["response_schema"] = config.ResponseSchema
	}
	if config.ResponseJsonSchema != nil {
		params.ExtraParams["response_json_schema"] = config.ResponseJsonSchema
	}
	if config.ResponseLogprobs {
		params.ExtraParams["response_logprobs"] = config.ResponseLogprobs
	}
	if config.Logprobs != nil {
		params.ExtraParams["logprobs"] = *config.Logprobs
	}

	return params
}

// convertSchemaToFunctionParameters converts genai.Schema to schemas.FunctionParameters
func (r *GeminiGenerationRequest) convertSchemaToFunctionParameters(schema *Schema) schemas.ToolFunctionParameters {
	params := schemas.ToolFunctionParameters{
		Type: string(schema.Type),
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

func convertSchemaToMap(schema *Schema) map[string]interface{} {
	// Convert map[string]*Schema to map[string]interface{} using JSON marshaling
	data, err := sonic.Marshal(schema.Properties)
	if err != nil {
		return make(map[string]interface{})
	}

	var properties map[string]interface{}
	if err := sonic.Unmarshal(data, &properties); err != nil {
		return make(map[string]interface{})
	}

	return properties
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

// ensureExtraParams ensures that bifrostReq.Params and bifrostReq.Params.ExtraParams are initialized
func ensureExtraParams(bifrostReq *schemas.BifrostChatRequest) {
	if bifrostReq.Params == nil {
		bifrostReq.Params = &schemas.ChatParameters{
			ExtraParams: make(map[string]interface{}),
		}
	}
	if bifrostReq.Params.ExtraParams == nil {
		bifrostReq.Params.ExtraParams = make(map[string]interface{})
	}
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
			config.ResponseJsonSchema = responseJsonSchema
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
								part.ThoughtSignature = []byte(thoughtSig)
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
			contents = append(contents, content)
		}
	}

	return contents
}

var (
	riff = []byte("RIFF")
	wave = []byte("WAVE")
	id3  = []byte("ID3")
	form = []byte("FORM")
	aiff = []byte("AIFF")
	aifc = []byte("AIFC")
	flac = []byte("fLaC")
	oggs = []byte("OggS")
	adif = []byte("ADIF")
)

// detectAudioMimeType attempts to detect the MIME type from audio file headers
// Gemini supports: WAV, MP3, AIFF, AAC, OGG Vorbis, FLAC
func detectAudioMimeType(audioData []byte) string {
	if len(audioData) < 4 {
		return "audio/mp3"
	}
	// WAV (RIFF/WAVE)
	if len(audioData) >= 12 &&
		bytes.Equal(audioData[:4], riff) &&
		bytes.Equal(audioData[8:12], wave) {
		return "audio/wav"
	}
	// MP3: ID3v2 tag (keep this check for MP3)
	if len(audioData) >= 3 && bytes.Equal(audioData[:3], id3) {
		return "audio/mp3"
	}
	// AAC: ADIF or ADTS (0xFFF sync) - check before MP3 frame sync to avoid misclassification
	if bytes.HasPrefix(audioData, adif) {
		return "audio/aac"
	}
	if len(audioData) >= 2 && audioData[0] == 0xFF && (audioData[1]&0xF6) == 0xF0 {
		return "audio/aac"
	}
	// AIFF / AIFC (map both to audio/aiff)
	if len(audioData) >= 12 && bytes.Equal(audioData[:4], form) &&
		(bytes.Equal(audioData[8:12], aiff) || bytes.Equal(audioData[8:12], aifc)) {
		return "audio/aiff"
	}
	// FLAC
	if bytes.HasPrefix(audioData, flac) {
		return "audio/flac"
	}
	// OGG container
	if bytes.HasPrefix(audioData, oggs) {
		return "audio/ogg"
	}
	// MP3: MPEG frame sync (cover common variants) - check after AAC to avoid misclassification
	if len(audioData) >= 2 && audioData[0] == 0xFF &&
		(audioData[1] == 0xFB || audioData[1] == 0xF3 || audioData[1] == 0xF2 || audioData[1] == 0xFA) {
		return "audio/mp3"
	}
	// Fallback within supported set
	return "audio/mp3"
}

// convertGeminiSchemaToJSONSchema converts Gemini Schema to JSON Schema format
// This converts uppercase type values (STRING, NUMBER, etc.) to lowercase (string, number, etc.)
// and converts the struct to a map[string]interface{} format
func convertGeminiSchemaToJSONSchema(geminiSchema *Schema) map[string]interface{} {
	if geminiSchema == nil {
		return nil
	}

	// First, marshal the schema to JSON and unmarshal to map to get all fields
	schemaBytes, err := sonic.Marshal(geminiSchema)
	if err != nil {
		return nil
	}

	var schemaMap map[string]interface{}
	if err := sonic.Unmarshal(schemaBytes, &schemaMap); err != nil {
		return nil
	}

	// Convert type from uppercase to lowercase
	if typeVal, ok := schemaMap["type"].(string); ok {
		schemaMap["type"] = convertGeminiTypeToJSONSchemaType(typeVal)
	}

	// Recursively convert nested properties
	if properties, ok := schemaMap["properties"].(map[string]interface{}); ok {
		convertedProps := make(map[string]interface{})
		for key, prop := range properties {
			if propMap, ok := prop.(map[string]interface{}); ok {
				// Check if this is a Schema struct that was marshaled
				if propType, hasType := propMap["type"].(string); hasType {
					// Convert the type
					propMap["type"] = convertGeminiTypeToJSONSchemaType(propType)
					// Recursively convert nested properties and items
					convertedProps[key] = convertNestedSchema(propMap)
				} else {
					convertedProps[key] = propMap
				}
			} else {
				convertedProps[key] = prop
			}
		}
		schemaMap["properties"] = convertedProps
	}

	// Recursively convert items
	if items, ok := schemaMap["items"]; ok {
		if itemsMap, ok := items.(map[string]interface{}); ok {
			schemaMap["items"] = convertNestedSchema(itemsMap)
		}
	}

	// Recursively convert anyOf
	if anyOf, ok := schemaMap["anyOf"].([]interface{}); ok {
		convertedAnyOf := make([]interface{}, 0, len(anyOf))
		for _, item := range anyOf {
			if itemMap, ok := item.(map[string]interface{}); ok {
				convertedAnyOf = append(convertedAnyOf, convertNestedSchema(itemMap))
			} else {
				convertedAnyOf = append(convertedAnyOf, item)
			}
		}
		schemaMap["anyOf"] = convertedAnyOf
	}

	return schemaMap
}

// convertNestedSchema recursively converts nested schema structures
func convertNestedSchema(schemaMap map[string]interface{}) map[string]interface{} {
	// Convert type if present
	if typeVal, ok := schemaMap["type"].(string); ok {
		schemaMap["type"] = convertGeminiTypeToJSONSchemaType(typeVal)
	}

	// Recursively convert properties
	if properties, ok := schemaMap["properties"].(map[string]interface{}); ok {
		convertedProps := make(map[string]interface{})
		for key, prop := range properties {
			if propMap, ok := prop.(map[string]interface{}); ok {
				convertedProps[key] = convertNestedSchema(propMap)
			} else {
				convertedProps[key] = prop
			}
		}
		schemaMap["properties"] = convertedProps
	}

	// Recursively convert items
	if items, ok := schemaMap["items"]; ok {
		if itemsMap, ok := items.(map[string]interface{}); ok {
			schemaMap["items"] = convertNestedSchema(itemsMap)
		}
	}

	// Recursively convert anyOf
	if anyOf, ok := schemaMap["anyOf"].([]interface{}); ok {
		convertedAnyOf := make([]interface{}, 0, len(anyOf))
		for _, item := range anyOf {
			if itemMap, ok := item.(map[string]interface{}); ok {
				convertedAnyOf = append(convertedAnyOf, convertNestedSchema(itemMap))
			} else {
				convertedAnyOf = append(convertedAnyOf, item)
			}
		}
		schemaMap["anyOf"] = convertedAnyOf
	}

	return schemaMap
}

// convertGeminiTypeToJSONSchemaType converts Gemini's uppercase type values to JSON Schema lowercase
func convertGeminiTypeToJSONSchemaType(geminiType string) string {
	switch geminiType {
	case "STRING":
		return "string"
	case "NUMBER":
		return "number"
	case "INTEGER":
		return "integer"
	case "BOOLEAN":
		return "boolean"
	case "ARRAY":
		return "array"
	case "OBJECT":
		return "object"
	case "NULL":
		return "null"
	case "TYPE_UNSPECIFIED":
		return "" // Empty string for unspecified
	default:
		// If already lowercase or unknown, return as-is
		return geminiType
	}
}

// buildOpenAIResponseFormat builds OpenAI response_format for JSON types
func buildOpenAIResponseFormat(responseSchema *Schema, responseJsonSchema interface{}) *interface{} {
	var schema interface{}
	name := "response_schema"

	// Prefer responseSchema over responseJsonSchema
	if responseSchema != nil {
		// Convert Gemini Schema to JSON Schema format
		schema = convertGeminiSchemaToJSONSchema(responseSchema)
		if responseSchema.Title != "" {
			name = responseSchema.Title
		}
	} else if responseJsonSchema != nil {
		if schemaMap, ok := responseJsonSchema.(map[string]interface{}); ok {
			// Create a deep copy to avoid modifying the original
			schemaBytes, err := sonic.Marshal(schemaMap)
			if err == nil {
				var copiedMap map[string]interface{}
				if err := sonic.Unmarshal(schemaBytes, &copiedMap); err == nil {
					// Recursively convert the schema to ensure all types are lowercase
					schema = convertNestedSchema(copiedMap)
					if title, ok := copiedMap["title"].(string); ok && title != "" {
						name = title
					}
				} else {
					schema = responseJsonSchema
				}
			} else {
				schema = responseJsonSchema
			}
		} else {
			schema = responseJsonSchema
		}
	} else {
		// No schema provided - use older json_object mode
		var format interface{} = map[string]interface{}{
			"type": "json_object",
		}
		return &format
	}

	// Has schema - use json_schema mode (Structured Outputs)
	var format interface{} = map[string]interface{}{
		"type": "json_schema",
		"json_schema": map[string]interface{}{
			"name":   name,
			"strict": false,
			"schema": schema,
		},
	}
	return &format
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
