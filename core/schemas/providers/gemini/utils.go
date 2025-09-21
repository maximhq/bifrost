package gemini

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// convertEmbeddingParameters converts Gemini embedding request parameters to ModelParameters
func (r *GeminiGenerationRequest) convertEmbeddingParameters() *schemas.ModelParameters {
	params := &schemas.ModelParameters{
		ExtraParams: make(map[string]interface{}),
	}

	// Check for parameters from batch embedding requests first
	if len(r.Requests) > 0 {
		// Use parameters from the first request in the batch
		firstReq := r.Requests[0]
		if firstReq.TaskType != nil {
			params.ExtraParams["taskType"] = *firstReq.TaskType
		}
		if firstReq.Title != nil {
			params.ExtraParams["title"] = *firstReq.Title
		}
		if firstReq.OutputDimensionality != nil {
			params.Dimensions = firstReq.OutputDimensionality
		}
	} else {
		// Fallback to top-level embedding parameters for single requests
		if r.TaskType != nil {
			params.ExtraParams["taskType"] = *r.TaskType
		}
		if r.Title != nil {
			params.ExtraParams["title"] = *r.Title
		}
		if r.OutputDimensionality != nil {
			params.Dimensions = r.OutputDimensionality
		}
	}

	return params
}

// convertGenerationConfigToParams converts Gemini GenerationConfig to ModelParameters
func (r *GeminiGenerationRequest) convertGenerationConfigToParams() *schemas.ModelParameters {
	params := &schemas.ModelParameters{
		ExtraParams: make(map[string]interface{}),
	}

	config := r.GenerationConfig

	// Map generation config fields to parameters
	if config.Temperature != nil {
		temp := float64(*config.Temperature)
		params.Temperature = &temp
	}
	if config.TopP != nil {
		params.TopP = schemas.Ptr(float64(*config.TopP))
	}
	if config.TopK != nil {
		params.TopK = schemas.Ptr(int(*config.TopK))
	}
	if config.MaxOutputTokens > 0 {
		maxTokens := int(config.MaxOutputTokens)
		params.MaxTokens = &maxTokens
	}
	if config.CandidateCount > 0 {
		params.ExtraParams["candidate_count"] = config.CandidateCount
	}
	if len(config.StopSequences) > 0 {
		params.StopSequences = &config.StopSequences
	}
	if config.PresencePenalty != nil {
		params.PresencePenalty = schemas.Ptr(float64(*config.PresencePenalty))
	}
	if config.FrequencyPenalty != nil {
		params.FrequencyPenalty = schemas.Ptr(float64(*config.FrequencyPenalty))
	}
	if config.Seed != nil {
		params.ExtraParams["seed"] = *config.Seed
	}
	if config.ResponseMIMEType != "" {
		params.ExtraParams["response_mime_type"] = config.ResponseMIMEType
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
func (r *GeminiGenerationRequest) convertSchemaToFunctionParameters(schema *Schema) schemas.FunctionParameters {
	params := schemas.FunctionParameters{
		Type: string(schema.Type),
	}

	if schema.Description != "" {
		params.Description = &schema.Description
	}

	if len(schema.Required) > 0 {
		params.Required = schema.Required
	}

	if len(schema.Properties) > 0 {
		params.Properties = make(map[string]interface{})
		for k, v := range schema.Properties {
			params.Properties[k] = v
		}
	}

	if len(schema.Enum) > 0 {
		params.Enum = &schema.Enum
	}

	return params
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
func ensureExtraParams(bifrostReq *schemas.BifrostRequest) {
	if bifrostReq.Params == nil {
		bifrostReq.Params = &schemas.ModelParameters{
			ExtraParams: make(map[string]interface{}),
		}
	}
	if bifrostReq.Params.ExtraParams == nil {
		bifrostReq.Params.ExtraParams = make(map[string]interface{})
	}
}

// extractUsageMetadata extracts usage metadata from the Gemini response
func (r *GenerateContentResponse) extractUsageMetadata() (int, int, int) {
	var inputTokens, outputTokens, totalTokens int
	if r.UsageMetadata != nil {
		inputTokens = int(r.UsageMetadata.PromptTokenCount)
		outputTokens = int(r.UsageMetadata.CandidatesTokenCount)
		totalTokens = int(r.UsageMetadata.TotalTokenCount)
	}
	return inputTokens, outputTokens, totalTokens
}

// convertParamsToGenerationConfig converts Bifrost parameters to Gemini GenerationConfig
func convertParamsToGenerationConfig(params *schemas.ModelParameters, responseModalities []string) GenerationConfig {
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
	if params.StopSequences != nil {
		config.StopSequences = *params.StopSequences
	}
	if params.MaxTokens != nil {
		config.MaxOutputTokens = int32(*params.MaxTokens)
	}
	if params.Temperature != nil {
		temp := float32(*params.Temperature)
		config.Temperature = &temp
	}
	if params.TopP != nil {
		topP := float32(*params.TopP)
		config.TopP = &topP
	}
	if params.TopK != nil {
		topK := float32(*params.TopK)
		config.TopK = &topK
	}
	if params.PresencePenalty != nil {
		penalty := float32(*params.PresencePenalty)
		config.PresencePenalty = &penalty
	}
	if params.FrequencyPenalty != nil {
		penalty := float32(*params.FrequencyPenalty)
		config.FrequencyPenalty = &penalty
	}

	return config
}

// convertBifrostToolsToGemini converts Bifrost tools to Gemini format
func convertBifrostToolsToGemini(bifrostTools []schemas.Tool) []Tool {
	var geminiTools []Tool

	for _, tool := range bifrostTools {
		if tool.Type == "function" {
			geminiTool := Tool{
				FunctionDeclarations: []*FunctionDeclaration{
					{
						Name:        tool.Function.Name,
						Description: tool.Function.Description,
						Parameters:  convertFunctionParametersToSchema(tool.Function.Parameters),
					},
				},
			}
			geminiTools = append(geminiTools, geminiTool)
		}
	}

	return geminiTools
}

// convertFunctionParametersToSchema converts Bifrost function parameters to Gemini Schema
func convertFunctionParametersToSchema(params schemas.FunctionParameters) *Schema {
	schema := &Schema{
		Type: Type(params.Type),
	}

	if params.Description != nil {
		schema.Description = *params.Description
	}

	if len(params.Required) > 0 {
		schema.Required = params.Required
	}

	if len(params.Properties) > 0 {
		schema.Properties = make(map[string]*Schema)
		// Note: This is a simplified conversion. In practice, you'd need to
		// recursively convert nested schemas
		for k, v := range params.Properties {
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
func convertToolChoiceToToolConfig(toolChoice *schemas.ToolChoice) ToolConfig {
	config := ToolConfig{}
	functionCallingConfig := FunctionCallingConfig{}

	if toolChoice.ToolChoiceStr != nil {
		// Map string values to Gemini's enum values
		switch *toolChoice.ToolChoiceStr {
		case "none":
			functionCallingConfig.Mode = FunctionCallingConfigModeNone
		case "auto":
			functionCallingConfig.Mode = FunctionCallingConfigModeAuto
		case "any", "required":
			functionCallingConfig.Mode = FunctionCallingConfigModeAny
		default:
			functionCallingConfig.Mode = FunctionCallingConfigModeAuto
		}
	} else if toolChoice.ToolChoiceStruct != nil {
		switch toolChoice.ToolChoiceStruct.Type {
		case schemas.ToolChoiceTypeNone:
			functionCallingConfig.Mode = FunctionCallingConfigModeNone
		case schemas.ToolChoiceTypeAuto:
			functionCallingConfig.Mode = FunctionCallingConfigModeAuto
		case schemas.ToolChoiceTypeRequired, schemas.ToolChoiceTypeFunction:
			functionCallingConfig.Mode = FunctionCallingConfigModeAny
		default:
			functionCallingConfig.Mode = FunctionCallingConfigModeAuto
		}

		// Handle specific function selection
		if toolChoice.ToolChoiceStruct.Function.Name != "" {
			functionCallingConfig.AllowedFunctionNames = []string{toolChoice.ToolChoiceStruct.Function.Name}
		}
	}

	config.FunctionCallingConfig = &functionCallingConfig
	return config
}

// addSpeechConfigToGenerationConfig adds speech configuration to the generation config
func addSpeechConfigToGenerationConfig(config *GenerationConfig, voiceConfig schemas.SpeechVoiceInput) {
	speechConfig := SpeechConfig{}

	// Handle single voice configuration
	if voiceConfig.Voice != nil {
		speechConfig.VoiceConfig = &VoiceConfig{
			PrebuiltVoiceConfig: &PrebuiltVoiceConfig{
				VoiceName: *voiceConfig.Voice,
			},
		}
	}

	// Handle multi-speaker voice configuration
	if len(voiceConfig.MultiVoiceConfig) > 0 {
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
func convertBifrostMessagesToGemini(messages []schemas.BifrostMessage) []CustomContent {
	var contents []CustomContent

	for _, message := range messages {
		var parts []*CustomPart

		// Handle content
		if message.Content.ContentStr != nil && *message.Content.ContentStr != "" {
			parts = append(parts, &CustomPart{
				Text: *message.Content.ContentStr,
			})
		} else if message.Content.ContentBlocks != nil {
			for _, block := range *message.Content.ContentBlocks {
				if block.Text != nil {
					parts = append(parts, &CustomPart{
						Text: *block.Text,
					})
				}
				// Handle other content block types as needed
			}
		}

		// Handle tool calls for assistant messages
		if message.AssistantMessage != nil && message.AssistantMessage.ToolCalls != nil {
			for _, toolCall := range *message.AssistantMessage.ToolCalls {
				// Convert tool call to function call part
				if toolCall.Function.Name != nil {
					// Create function call part - simplified implementation
					argsMap := make(map[string]any)
					if toolCall.Function.Arguments != "" {
						sonic.Unmarshal([]byte(toolCall.Function.Arguments), &argsMap)
					}
					parts = append(parts, &CustomPart{
						FunctionCall: &FunctionCall{
							ID:   *toolCall.ID,
							Name: *toolCall.Function.Name,
							Args: argsMap,
						},
					})
				}
			}
		}

		// Handle thinking content
		if message.AssistantMessage != nil && message.AssistantMessage.Thought != nil && *message.AssistantMessage.Thought != "" {
			parts = append(parts, &CustomPart{
				Text:    *message.AssistantMessage.Thought,
				Thought: true,
			})
		}

		if len(parts) > 0 {
			content := CustomContent{
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
// safeFloat32Conversion safely converts various numeric types to float32
func safeFloat32Conversion(value interface{}) (float32, bool) {
	if value == nil {
		return 0, false
	}

	switch v := value.(type) {
	case int:
		return float32(v), true
	case int64:
		return float32(v), true
	case float64:
		return float32(v), true
	case float32:
		return v, true
	case json.Number:
		if val, err := v.Float64(); err == nil {
			return float32(val), true
		}
		return 0, false
	case string:
		if val, err := strconv.ParseFloat(v, 32); err == nil {
			return float32(val), true
		}
		return 0, false
	default:
		return 0, false
	}
}

// safeStringSliceConversion safely converts various types to []string
func safeStringSliceConversion(value interface{}) ([]string, bool) {
	if value == nil {
		return nil, false
	}

	switch v := value.(type) {
	case []string:
		return v, true
	case []interface{}:
		var result []string
		for _, item := range v {
			if str, ok := item.(string); ok {
				result = append(result, str)
			} else {
				return nil, false // If any item is not a string, fail
			}
		}
		return result, true
	default:
		return nil, false
	}
}

// normalizeAudioMIMEType converts audio format tokens to proper MIME types
func normalizeAudioMIMEType(format string) string {
	if format == "" {
		return "application/octet-stream"
	}

	// Normalize to lowercase
	format = strings.ToLower(format)

	// If already a proper MIME type (contains slash), use as-is
	if strings.Contains(format, "/") {
		return format
	}

	// Map common audio format tokens to MIME types
	switch format {
	case "wav":
		return "audio/wav"
	case "mp3":
		return "audio/mpeg"
	case "m4a":
		return "audio/mp4"
	case "aac":
		return "audio/aac"
	case "ogg":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	case "webm":
		return "audio/webm"
	default:
		return "application/octet-stream"
	}
}
