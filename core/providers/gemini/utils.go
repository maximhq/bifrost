package gemini

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// isGemini3Plus returns true if the model is Gemini 3.0 or higher
// Uses simple string operations for hot path performance
func isGemini3Plus(model string) bool {
	// Convert to lowercase for case-insensitive comparison
	model = strings.ToLower(model)

	// Find "gemini-" prefix
	idx := strings.Index(model, "gemini-")
	if idx == -1 {
		return false
	}

	// Get the part after "gemini-"
	afterPrefix := model[idx+7:] // len("gemini-") = 7
	if len(afterPrefix) == 0 {
		return false
	}

	// Check first character - if it's '3' or higher, it's 3.0+
	firstChar := afterPrefix[0]
	return firstChar >= '3'
}

// effortToThinkingLevel converts reasoning effort to Gemini ThinkingLevel string
// Pro models only support "low" or "high"
// Other models support "minimal", "low", "medium", and "high"
func effortToThinkingLevel(effort string, model string) string {
	isPro := strings.Contains(strings.ToLower(model), "pro")

	switch effort {
	case "none":
		return "" // Empty string for no thinking
	case "minimal":
		if isPro {
			return "low" // Pro models don't support minimal, use low
		}
		return "minimal"
	case "low":
		return "low"
	case "medium":
		if isPro {
			return "high" // Pro models don't support medium, use high
		}
		return "medium"
	case "high":
		return "high"
	default:
		if isPro {
			return "high"
		}
		return "medium"
	}
}

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

		// Determine max tokens for conversions
		maxTokens := DefaultCompletionMaxTokens
		if config.MaxOutputTokens > 0 {
			maxTokens = int(config.MaxOutputTokens)
		}
		minBudget := DefaultReasoningMinBudget

		// Priority: Budget first (if present), then Level
		if config.ThinkingConfig.ThinkingBudget != nil {
			// Budget is set - use it directly
			budget := int(*config.ThinkingConfig.ThinkingBudget)
			params.Reasoning.MaxTokens = schemas.Ptr(budget)

			// Also provide effort for compatibility
			effort := providerUtils.GetReasoningEffortFromBudgetTokens(budget, minBudget, maxTokens)
			params.Reasoning.Effort = schemas.Ptr(effort)

			// Handle special cases
			switch budget {
			case 0:
				params.Reasoning.Effort = schemas.Ptr("none")
			case DynamicReasoningBudget:
				params.Reasoning.Effort = schemas.Ptr("medium") // dynamic
			}
		} else if config.ThinkingConfig.ThinkingLevel != nil && *config.ThinkingConfig.ThinkingLevel != "" {
			// Level is set (only on 3.0+) - convert to effort and budget
			level := *config.ThinkingConfig.ThinkingLevel
			var effort string

			// Map Gemini thinking level to Bifrost effort
			switch level {
			case "minimal":
				effort = "minimal"
			case "low":
				effort = "low"
			case "medium":
				effort = "medium"
			case "high":
				effort = "high"
			default:
				effort = "medium"
			}

			params.Reasoning.Effort = schemas.Ptr(effort)

			// Also convert to budget for compatibility
			if effort != "none" {
				budget, _ := providerUtils.GetBudgetTokensFromReasoningEffort(effort, minBudget, maxTokens)
				params.Reasoning.MaxTokens = schemas.Ptr(budget)
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
			params.Text = buildOpenAIResponseFormat(config.ResponseJSONSchema, config.ResponseSchema)
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

	// Array schema fields
	if schema.Items != nil {
		itemsMap := convertSchemaToOrderedMap(schema.Items)
		params.Items = &itemsMap
	}
	if schema.MinItems != nil {
		params.MinItems = schema.MinItems
	}
	if schema.MaxItems != nil {
		params.MaxItems = schema.MaxItems
	}

	// Composition fields (anyOf)
	if len(schema.AnyOf) > 0 {
		anyOf := make([]schemas.OrderedMap, len(schema.AnyOf))
		for i, s := range schema.AnyOf {
			anyOf[i] = convertSchemaToOrderedMap(s)
		}
		params.AnyOf = anyOf
	}

	// String validation fields
	if schema.Format != "" {
		params.Format = &schema.Format
	}
	if schema.Pattern != "" {
		params.Pattern = &schema.Pattern
	}
	if schema.MinLength != nil {
		params.MinLength = schema.MinLength
	}
	if schema.MaxLength != nil {
		params.MaxLength = schema.MaxLength
	}

	// Number validation fields
	if schema.Minimum != nil {
		params.Minimum = schema.Minimum
	}
	if schema.Maximum != nil {
		params.Maximum = schema.Maximum
	}

	// Misc fields
	if schema.Title != "" {
		params.Title = &schema.Title
	}
	if schema.Default != nil {
		params.Default = schema.Default
	}
	if schema.Nullable != nil {
		params.Nullable = schema.Nullable
	}

	return params
}

// convertSchemaToOrderedMap converts a Gemini Schema to an OrderedMap
func convertSchemaToOrderedMap(schema *Schema) schemas.OrderedMap {
	if schema == nil {
		return schemas.OrderedMap{}
	}

	result := schemas.OrderedMap{}

	if schema.Type != "" {
		result["type"] = strings.ToLower(string(schema.Type))
	}
	if schema.Description != "" {
		result["description"] = schema.Description
	}
	if len(schema.Enum) > 0 {
		result["enum"] = schema.Enum
	}
	if len(schema.Required) > 0 {
		result["required"] = schema.Required
	}
	if len(schema.Properties) > 0 {
		props := make(map[string]interface{})
		for k, v := range schema.Properties {
			props[k] = convertSchemaToOrderedMap(v)
		}
		result["properties"] = props
	}
	if schema.Items != nil {
		result["items"] = convertSchemaToOrderedMap(schema.Items)
	}
	if len(schema.AnyOf) > 0 {
		anyOf := make([]interface{}, len(schema.AnyOf))
		for i, s := range schema.AnyOf {
			anyOf[i] = convertSchemaToOrderedMap(s)
		}
		result["anyOf"] = anyOf
	}
	if schema.Format != "" {
		result["format"] = schema.Format
	}
	if schema.Pattern != "" {
		result["pattern"] = schema.Pattern
	}
	if schema.MinLength != nil {
		result["minLength"] = *schema.MinLength
	}
	if schema.MaxLength != nil {
		result["maxLength"] = *schema.MaxLength
	}
	if schema.MinItems != nil {
		result["minItems"] = *schema.MinItems
	}
	if schema.MaxItems != nil {
		result["maxItems"] = *schema.MaxItems
	}
	if schema.Minimum != nil {
		result["minimum"] = *schema.Minimum
	}
	if schema.Maximum != nil {
		result["maximum"] = *schema.Maximum
	}
	if schema.Title != "" {
		result["title"] = schema.Title
	}
	if schema.Default != nil {
		result["default"] = schema.Default
	}
	if schema.Nullable != nil {
		result["nullable"] = *schema.Nullable
	}

	return result
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

// convertFileDataToBytes converts file data (data URL or base64) to raw bytes for Gemini API.
// Returns the bytes and an extracted mime type (if found in data URL).
func convertFileDataToBytes(fileData string) ([]byte, string) {
	var dataBytes []byte
	var mimeType string

	// Check if it's a data URL (e.g., "data:application/pdf;base64,...")
	if strings.HasPrefix(fileData, "data:") {
		urlInfo := schemas.ExtractURLTypeInfo(fileData)

		if urlInfo.DataURLWithoutPrefix != nil {
			// Decode the base64 content
			decoded, err := base64.StdEncoding.DecodeString(*urlInfo.DataURLWithoutPrefix)
			if err == nil {
				dataBytes = decoded
				if urlInfo.MediaType != nil {
					mimeType = *urlInfo.MediaType
				}
			}
		}
	} else {
		// Try to decode as plain base64
		decoded, err := base64.StdEncoding.DecodeString(fileData)
		if err == nil {
			dataBytes = decoded
		} else {
			// Not base64 - treat as plain text
			dataBytes = []byte(fileData)
		}
	}

	return dataBytes, mimeType
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
		FinishReasonMalformedFunctionCall: "stop",
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
func convertParamsToGenerationConfig(params *schemas.ChatParameters, responseModalities []string, model string) GenerationConfig {
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

		// Get max tokens for conversions
		maxTokens := DefaultCompletionMaxTokens
		if config.MaxOutputTokens > 0 {
			maxTokens = int(config.MaxOutputTokens)
		}
		minBudget := DefaultReasoningMinBudget

		hasMaxTokens := params.Reasoning.MaxTokens != nil
		hasEffort := params.Reasoning.Effort != nil
		supportsLevel := isGemini3Plus(model) // Check if model is 3.0+

		// PRIORITY RULE: If both max_tokens and effort are present, use ONLY max_tokens (budget)
		// This ensures we send only thinkingBudget to Gemini, not thinkingLevel

		// Handle "none" effort explicitly (only if max_tokens not present)
		if !hasMaxTokens && hasEffort && *params.Reasoning.Effort == "none" {
			config.ThinkingConfig.IncludeThoughts = false
			config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(0))
		} else if hasMaxTokens {
			// User provided max_tokens - use thinkingBudget (all Gemini models support this)
			// If both max_tokens and effort are present, we ignore effort and use ONLY max_tokens
			budget := *params.Reasoning.MaxTokens
			switch budget {
			case 0:
				config.ThinkingConfig.IncludeThoughts = false
				config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(0))
			case DynamicReasoningBudget: // Special case: -1 means dynamic budget
				config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(DynamicReasoningBudget))
			default:
				config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(budget))
			}
		} else if hasEffort {
			// User provided effort only (no max_tokens)
			if supportsLevel {
				// Gemini 3.0+ - use thinkingLevel (more native)
				level := effortToThinkingLevel(*params.Reasoning.Effort, model)
				config.ThinkingConfig.ThinkingLevel = &level
			} else {
				// Gemini < 3.0 - must convert effort to budget
				budgetTokens, err := providerUtils.GetBudgetTokensFromReasoningEffort(
					*params.Reasoning.Effort,
					minBudget,
					maxTokens,
				)
				if err == nil {
					config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(budgetTokens))
				}
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
					if schemaMap := extractSchemaMapFromResponseFormat(params.ResponseFormat); schemaMap != nil {
						config.ResponseMIMEType = "application/json"
						config.ResponseJSONSchema = schemaMap
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
		// Override with explicit response_json_schema if provided in ExtraParams
		if responseJsonSchema, ok := params.ExtraParams["response_json_schema"]; ok {
			config.ResponseJSONSchema = responseJsonSchema
		}
	}

	return config
}

// convertBifrostToolsToGemini converts Bifrost tools to Gemini format
func convertBifrostToolsToGemini(bifrostTools []schemas.ChatTool) []Tool {
	geminiTool := Tool{}

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
			geminiTool.FunctionDeclarations = append(geminiTool.FunctionDeclarations, fd)
		}
	}

	if len(geminiTool.FunctionDeclarations) > 0 {
		return []Tool{geminiTool}
	}
	return []Tool{}
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

	if len(params.Enum) > 0 {
		schema.Enum = params.Enum
	}

	if params.Properties != nil && len(*params.Properties) > 0 {
		schema.Properties = make(map[string]*Schema)
		for k, v := range *params.Properties {
			schema.Properties[k] = convertPropertyToSchema(v)
		}
	}

	// Array schema fields
	if params.Items != nil {
		schema.Items = convertPropertyToSchema(*params.Items)
	}
	if params.MinItems != nil {
		schema.MinItems = params.MinItems
	}
	if params.MaxItems != nil {
		schema.MaxItems = params.MaxItems
	}

	// Composition fields (anyOf, oneOf, allOf)
	if len(params.AnyOf) > 0 {
		schema.AnyOf = make([]*Schema, len(params.AnyOf))
		for i, item := range params.AnyOf {
			schema.AnyOf[i] = convertPropertyToSchema(item)
		}
	}
	// Note: Gemini treats oneOf the same as anyOf, so we map it to AnyOf
	if len(params.OneOf) > 0 && len(schema.AnyOf) == 0 {
		schema.AnyOf = make([]*Schema, len(params.OneOf))
		for i, item := range params.OneOf {
			schema.AnyOf[i] = convertPropertyToSchema(item)
		}
	}
	// Note: Gemini doesn't have native allOf support, but we can still attempt to pass it through AnyOf
	// This is a best-effort conversion as allOf semantics differ from anyOf

	// String validation fields
	if params.Format != nil {
		schema.Format = *params.Format
	}
	if params.Pattern != nil {
		schema.Pattern = *params.Pattern
	}
	if params.MinLength != nil {
		schema.MinLength = params.MinLength
	}
	if params.MaxLength != nil {
		schema.MaxLength = params.MaxLength
	}

	// Number validation fields
	if params.Minimum != nil {
		schema.Minimum = params.Minimum
	}
	if params.Maximum != nil {
		schema.Maximum = params.Maximum
	}

	// Misc fields
	if params.Title != nil {
		schema.Title = *params.Title
	}
	if params.Default != nil {
		schema.Default = params.Default
	}
	if params.Nullable != nil {
		schema.Nullable = params.Nullable
	}

	return schema
}

// convertPropertyToSchema recursively converts a property to Gemini Schema
func convertPropertyToSchema(prop interface{}) *Schema {
	schema := &Schema{}

	// Handle property as map[string]interface{} or schemas.OrderedMap
	var propMap map[string]interface{}
	switch v := prop.(type) {
	case map[string]interface{}:
		propMap = v
	case schemas.OrderedMap:
		propMap = map[string]interface{}(v)
	}
	if propMap != nil {
		if propType, exists := propMap["type"]; exists {
			if typeStr, ok := propType.(string); ok {
				schema.Type = Type(typeStr)
			}
		}

		if desc, exists := propMap["description"]; exists {
			if descStr, ok := desc.(string); ok {
				schema.Description = descStr
			}
		}

		if enum, exists := propMap["enum"]; exists {
			if enumSlice, ok := enum.([]interface{}); ok {
				var enumStrs []string
				for _, item := range enumSlice {
					if str, ok := item.(string); ok {
						enumStrs = append(enumStrs, str)
					}
				}
				schema.Enum = enumStrs
			} else if enumStrs, ok := enum.([]string); ok {
				schema.Enum = enumStrs
			}
		}

		// Handle nested properties for object types
		if props, exists := propMap["properties"]; exists {
			if propsMap, ok := props.(map[string]interface{}); ok {
				schema.Properties = make(map[string]*Schema)
				for key, nestedProp := range propsMap {
					schema.Properties[key] = convertPropertyToSchema(nestedProp)
				}
			}
		}

		// Handle array items
		if items, exists := propMap["items"]; exists {
			schema.Items = convertPropertyToSchema(items)
		}

		// Handle required fields
		if required, exists := propMap["required"]; exists {
			if reqSlice, ok := required.([]interface{}); ok {
				var reqStrs []string
				for _, item := range reqSlice {
					if str, ok := item.(string); ok {
						reqStrs = append(reqStrs, str)
					}
				}
				schema.Required = reqStrs
			} else if reqStrs, ok := required.([]string); ok {
				schema.Required = reqStrs
			}
		}

		// Handle anyOf composition
		if anyOf, exists := propMap["anyOf"]; exists {
			if anyOfSlice, ok := anyOf.([]interface{}); ok {
				schema.AnyOf = make([]*Schema, len(anyOfSlice))
				for i, item := range anyOfSlice {
					schema.AnyOf[i] = convertPropertyToSchema(item)
				}
			}
		}

		// Handle oneOf composition (Gemini treats it as anyOf)
		if oneOf, exists := propMap["oneOf"]; exists {
			if oneOfSlice, ok := oneOf.([]interface{}); ok && len(schema.AnyOf) == 0 {
				schema.AnyOf = make([]*Schema, len(oneOfSlice))
				for i, item := range oneOfSlice {
					schema.AnyOf[i] = convertPropertyToSchema(item)
				}
			}
		}

		// Handle string validation fields
		if format, exists := propMap["format"]; exists {
			if formatStr, ok := format.(string); ok {
				schema.Format = formatStr
			}
		}

		if pattern, exists := propMap["pattern"]; exists {
			if patternStr, ok := pattern.(string); ok {
				schema.Pattern = patternStr
			}
		}

		if minLength, exists := propMap["minLength"]; exists {
			if minLengthVal, ok := toInt64(minLength); ok {
				schema.MinLength = &minLengthVal
			}
		}

		if maxLength, exists := propMap["maxLength"]; exists {
			if maxLengthVal, ok := toInt64(maxLength); ok {
				schema.MaxLength = &maxLengthVal
			}
		}

		// Handle number validation fields
		if minimum, exists := propMap["minimum"]; exists {
			if minVal, ok := toFloat64(minimum); ok {
				schema.Minimum = &minVal
			}
		}

		if maximum, exists := propMap["maximum"]; exists {
			if maxVal, ok := toFloat64(maximum); ok {
				schema.Maximum = &maxVal
			}
		}

		// Handle array validation fields
		if minItems, exists := propMap["minItems"]; exists {
			if minItemsVal, ok := toInt64(minItems); ok {
				schema.MinItems = &minItemsVal
			}
		}

		if maxItems, exists := propMap["maxItems"]; exists {
			if maxItemsVal, ok := toInt64(maxItems); ok {
				schema.MaxItems = &maxItemsVal
			}
		}

		// Handle misc fields
		if title, exists := propMap["title"]; exists {
			if titleStr, ok := title.(string); ok {
				schema.Title = titleStr
			}
		}

		if defaultVal, exists := propMap["default"]; exists {
			schema.Default = defaultVal
		}

		if nullable, exists := propMap["nullable"]; exists {
			if nullableBool, ok := nullable.(bool); ok {
				schema.Nullable = &nullableBool
			}
		}
	}

	return schema
}

// toInt64 converts various numeric types to int64
func toInt64(v interface{}) (int64, bool) {
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

// toFloat64 converts various numeric types to float64
func toFloat64(v interface{}) (float64, bool) {
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
func convertBifrostMessagesToGemini(messages []schemas.ChatMessage) ([]Content, *Content) {
	var contents []Content
	var systemInstruction *Content

	// Track consecutive tool response messages to group them for parallel function calling
	// According to Gemini docs, all function responses must be in a single message
	var pendingToolResponseParts []*Part
	// Map callID to function name for correlating tool responses with function declarations
	callIDToFunctionName := make(map[string]string)

	for i, message := range messages {
		// Handle system messages separately - Gemini requires them in SystemInstruction field
		if message.Role == schemas.ChatMessageRoleSystem {
			if systemInstruction == nil {
				systemInstruction = &Content{}
			}

			// Extract system message content
			if message.Content != nil {
				if message.Content.ContentStr != nil && *message.Content.ContentStr != "" {
					systemInstruction.Parts = append(systemInstruction.Parts, &Part{
						Text: *message.Content.ContentStr,
					})
				} else if message.Content.ContentBlocks != nil {
					for _, block := range message.Content.ContentBlocks {
						if block.Text != nil && *block.Text != "" {
							systemInstruction.Parts = append(systemInstruction.Parts, &Part{
								Text: *block.Text,
							})
						}
					}
				}
			}
			continue
		}

		// Check if this is a tool response message
		isToolResponse := message.Role == schemas.ChatMessageRoleTool && message.ChatToolMessage != nil

		// If we have pending tool responses and current message is NOT a tool response,
		// flush the pending tool responses as a single Content (for parallel function calling)
		if len(pendingToolResponseParts) > 0 && !isToolResponse {
			contents = append(contents, Content{
				Parts: pendingToolResponseParts,
				Role:  "model", // Tool responses use "model" role in Gemini
			})
			pendingToolResponseParts = nil
		}

		// Handle tool response messages - collect them for grouping
		// According to Gemini parallel function calling docs, multiple function responses
		// must be sent in a single message with only functionResponse parts (no text parts)
		if isToolResponse {
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

			// Get the function name from our mapping (fallback to callID if not found)
			functionName := callID
			if mappedName, ok := callIDToFunctionName[callID]; ok {
				functionName = mappedName
			}

			// Add ONLY the functionResponse part (no text part)
			// This ensures the number of functionResponse parts equals functionCall parts
			pendingToolResponseParts = append(pendingToolResponseParts, &Part{
				FunctionResponse: &FunctionResponse{
					ID:       callID,
					Name:     functionName,
					Response: responseData,
				},
			})

			// If this is the last message, flush pending tool responses
			if i == len(messages)-1 && len(pendingToolResponseParts) > 0 {
				contents = append(contents, Content{
					Parts: pendingToolResponseParts,
					Role:  "model",
				})
				pendingToolResponseParts = nil
			}

			continue // Skip the normal content handling below
		}

		// For non-tool messages, proceed with normal handling
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
					} else if block.File != nil {
						// Handle file blocks - use FileURL if available (uploaded file)
						if block.File.FileURL != nil && *block.File.FileURL != "" {
							mimeType := "application/pdf"
							if block.File.FileType != nil {
								mimeType = *block.File.FileType
							}
							parts = append(parts, &Part{
								FileData: &FileData{
									FileURI:  *block.File.FileURL,
									MIMEType: mimeType,
								},
							})
						} else if block.File.FileData != nil {
							// Inline file data - convert to InlineData (Blob)
							fileData := *block.File.FileData
							mimeType := "application/pdf"
							if block.File.FileType != nil {
								mimeType = *block.File.FileType
							}

							// Convert file data to bytes for Gemini Blob
							dataBytes, extractedMimeType := convertFileDataToBytes(fileData)
							if extractedMimeType != "" {
								mimeType = extractedMimeType
							}

							if len(dataBytes) > 0 {
								parts = append(parts, &Part{
									InlineData: &Blob{
										MIMEType: mimeType,
										Data:     encodeBytesToBase64String(dataBytes),
									},
								})
							}
						}
					} else if block.ImageURLStruct != nil {
						// Handle image blocks
						imageURL := block.ImageURLStruct.URL

						// Sanitize and parse the image URL
						sanitizedURL, err := schemas.SanitizeImageURL(imageURL)
						if err != nil {
							// Skip this block if URL is invalid
							continue
						}

						urlInfo := schemas.ExtractURLTypeInfo(sanitizedURL)

						// Determine MIME type
						mimeType := "image/jpeg" // default
						if urlInfo.MediaType != nil {
							mimeType = *urlInfo.MediaType
						}

						if urlInfo.Type == schemas.ImageContentTypeBase64 {
							// Data URL - convert to InlineData (Blob)
							if urlInfo.DataURLWithoutPrefix != nil {
								decodedData, err := base64.StdEncoding.DecodeString(*urlInfo.DataURLWithoutPrefix)
								if err == nil && len(decodedData) > 0 {
									parts = append(parts, &Part{
										InlineData: &Blob{
											MIMEType: mimeType,
											Data:     encodeBytesToBase64String(decodedData),
										},
									})
								}
							}
						} else {
							// Regular URL - use FileData
							parts = append(parts, &Part{
								FileData: &FileData{
									MIMEType: mimeType,
									FileURI:  sanitizedURL,
								},
							})
						}
					} else if block.InputAudio != nil {
						// Decode the audio data (handles both standard and URL-safe base64)
						decodedData, err := decodeBase64StringToBytes(block.InputAudio.Data)
						if err != nil || len(decodedData) == 0 {
							continue
						}

						// Determine MIME type
						mimeType := "audio/mpeg" // default
						if block.InputAudio.Format != nil {
							format := strings.ToLower(strings.TrimSpace(*block.InputAudio.Format))
							if format != "" {
								if strings.HasPrefix(format, "audio/") {
									mimeType = format
								} else {
									mimeType = "audio/" + format
								}
							}
						}

						parts = append(parts, &Part{
							InlineData: &Blob{
								MIMEType: mimeType,
								Data:     encodeBytesToBase64String(decodedData),
							},
						})
					}
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
					// Store the mapping for later use in FunctionResponse
					callIDToFunctionName[callID] = *toolCall.Function.Name

					// check in reasoning details array for thought signature with id tool_call_<callID>
					if len(message.ChatAssistantMessage.ReasoningDetails) > 0 {
						lookupID := fmt.Sprintf("tool_call_%s", callID)
						for _, reasoningDetail := range message.ChatAssistantMessage.ReasoningDetails {
							if reasoningDetail.ID != nil && *reasoningDetail.ID == lookupID &&
								reasoningDetail.Type == schemas.BifrostReasoningDetailsTypeEncrypted &&
								reasoningDetail.Signature != nil {
								// Decode the base64 string to raw bytes
								decoded, err := base64.StdEncoding.DecodeString(*reasoningDetail.Signature)
								if err == nil {
									part.ThoughtSignature = decoded
								}
								break
							}
						}
					}

					parts = append(parts, part)
				}
			}
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

	return contents, systemInstruction
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
		jsonSchema.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
			AdditionalPropertiesBool: &additionalProps,
		}
	}

	if additionalProps, ok := schemas.SafeExtractOrderedMap(normalizedSchemaMap["additionalProperties"]); ok {
		jsonSchema.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
			AdditionalPropertiesMap: &additionalProps,
		}
	}

	// Extract name/title
	if name, ok := normalizedSchemaMap["name"].(string); ok {
		jsonSchema.Name = schemas.Ptr(name)
	} else if title, ok := normalizedSchemaMap["title"].(string); ok {
		jsonSchema.Name = schemas.Ptr(title)
	}

	// Extract $defs (JSON Schema draft 2019-09+)
	if defs, ok := normalizedSchemaMap["$defs"].(map[string]interface{}); ok {
		jsonSchema.Defs = &defs
	}

	// Extract definitions (legacy JSON Schema draft-07)
	if definitions, ok := normalizedSchemaMap["definitions"].(map[string]interface{}); ok {
		jsonSchema.Definitions = &definitions
	}

	// Extract $ref
	if ref, ok := normalizedSchemaMap["$ref"].(string); ok {
		jsonSchema.Ref = schemas.Ptr(ref)
	}

	// Extract items (array element schema)
	if items, ok := normalizedSchemaMap["items"].(map[string]interface{}); ok {
		jsonSchema.Items = &items
	}

	// Extract minItems
	if minItems, ok := toInt64(normalizedSchemaMap["minItems"]); ok {
		jsonSchema.MinItems = &minItems
	}

	// Extract maxItems
	if maxItems, ok := toInt64(normalizedSchemaMap["maxItems"]); ok {
		jsonSchema.MaxItems = &maxItems
	}

	// Extract anyOf
	if anyOf, ok := normalizedSchemaMap["anyOf"].([]interface{}); ok {
		anyOfMaps := make([]map[string]any, 0, len(anyOf))
		for _, item := range anyOf {
			if m, ok := item.(map[string]interface{}); ok {
				anyOfMaps = append(anyOfMaps, m)
			}
		}
		if len(anyOfMaps) > 0 {
			jsonSchema.AnyOf = anyOfMaps
		}
	}

	// Extract oneOf
	if oneOf, ok := normalizedSchemaMap["oneOf"].([]interface{}); ok {
		oneOfMaps := make([]map[string]any, 0, len(oneOf))
		for _, item := range oneOf {
			if m, ok := item.(map[string]interface{}); ok {
				oneOfMaps = append(oneOfMaps, m)
			}
		}
		if len(oneOfMaps) > 0 {
			jsonSchema.OneOf = oneOfMaps
		}
	}

	// Extract allOf
	if allOf, ok := normalizedSchemaMap["allOf"].([]interface{}); ok {
		allOfMaps := make([]map[string]any, 0, len(allOf))
		for _, item := range allOf {
			if m, ok := item.(map[string]interface{}); ok {
				allOfMaps = append(allOfMaps, m)
			}
		}
		if len(allOfMaps) > 0 {
			jsonSchema.AllOf = allOfMaps
		}
	}

	// Extract format
	if format, ok := normalizedSchemaMap["format"].(string); ok {
		jsonSchema.Format = schemas.Ptr(format)
	}

	// Extract pattern
	if pattern, ok := normalizedSchemaMap["pattern"].(string); ok {
		jsonSchema.Pattern = schemas.Ptr(pattern)
	}

	// Extract minLength
	if minLength, ok := toInt64(normalizedSchemaMap["minLength"]); ok {
		jsonSchema.MinLength = &minLength
	}

	// Extract maxLength
	if maxLength, ok := toInt64(normalizedSchemaMap["maxLength"]); ok {
		jsonSchema.MaxLength = &maxLength
	}

	// Extract minimum
	if minimum, ok := toFloat64(normalizedSchemaMap["minimum"]); ok {
		jsonSchema.Minimum = &minimum
	}

	// Extract maximum
	if maximum, ok := toFloat64(normalizedSchemaMap["maximum"]); ok {
		jsonSchema.Maximum = &maximum
	}

	// Extract title (separate from name)
	if title, ok := normalizedSchemaMap["title"].(string); ok {
		jsonSchema.Title = schemas.Ptr(title)
	}

	// Extract default
	if defaultVal, exists := normalizedSchemaMap["default"]; exists {
		jsonSchema.Default = defaultVal
	}

	// Extract nullable
	if nullable, ok := normalizedSchemaMap["nullable"].(bool); ok {
		jsonSchema.Nullable = &nullable
	}

	// Extract enum
	if enum, ok := normalizedSchemaMap["enum"].([]interface{}); ok {
		enumStrs := make([]string, 0, len(enum))
		for _, e := range enum {
			if str, ok := e.(string); ok {
				enumStrs = append(enumStrs, str)
			}
		}
		if len(enumStrs) > 0 {
			jsonSchema.Enum = enumStrs
		}
	} else if enumStrs, ok := normalizedSchemaMap["enum"].([]string); ok && len(enumStrs) > 0 {
		jsonSchema.Enum = enumStrs
	}

	return jsonSchema
}

// buildOpenAIResponseFormat builds OpenAI response_format for JSON types
func buildOpenAIResponseFormat(responseJsonSchema interface{}, responseSchema *Schema) *schemas.ResponsesTextConfig {
	name := "json_response"

	var schemaMap map[string]interface{}

	// Try to use responseJsonSchema first
	if responseJsonSchema != nil {
		// Use responseJsonSchema directly if it's a map
		var ok bool
		schemaMap, ok = responseJsonSchema.(map[string]interface{})
		if !ok {
			// If not a map, fall back to json_object mode
			return &schemas.ResponsesTextConfig{
				Format: &schemas.ResponsesTextConfigFormat{
					Type: "json_object",
				},
			}
		}
	} else if responseSchema != nil {
		// Convert responseSchema to map using JSON marshaling and type normalization
		data, err := sonic.Marshal(responseSchema)
		if err != nil {
			// If marshaling fails, fall back to json_object mode
			return &schemas.ResponsesTextConfig{
				Format: &schemas.ResponsesTextConfigFormat{
					Type: "json_object",
				},
			}
		}

		var rawMap map[string]interface{}
		if err := sonic.Unmarshal(data, &rawMap); err != nil {
			// If unmarshaling fails, fall back to json_object mode
			return &schemas.ResponsesTextConfig{
				Format: &schemas.ResponsesTextConfigFormat{
					Type: "json_object",
				},
			}
		}

		// Apply type normalization (convert types to lowercase)
		normalized := convertTypeToLowerCase(rawMap)
		var ok bool
		schemaMap, ok = normalized.(map[string]interface{})
		if !ok {
			// If type assertion fails, fall back to json_object mode
			return &schemas.ResponsesTextConfig{
				Format: &schemas.ResponsesTextConfigFormat{
					Type: "json_object",
				},
			}
		}
	} else {
		// No schema provided - use json_object mode
		return &schemas.ResponsesTextConfig{
			Format: &schemas.ResponsesTextConfigFormat{
				Type: "json_object",
			},
		}
	}

	// Extract name/title if present
	if title, ok := schemaMap["title"].(string); ok && title != "" {
		name = title
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

// extractTypesFromValue extracts type strings from various formats (string, []string, []interface{})
func extractTypesFromValue(typeVal interface{}) []string {
	switch t := typeVal.(type) {
	case string:
		return []string{t}
	case []string:
		return t
	case []interface{}:
		types := make([]string, 0, len(t))
		for _, item := range t {
			if typeStr, ok := item.(string); ok {
				types = append(types, typeStr)
			}
		}
		return types
	default:
		return nil
	}
}

// normalizeSchemaForGemini recursively normalizes a JSON schema to be compatible with Gemini's API.
// This handles cases where:
// 1. type is an array like ["string", "null"] - kept as-is (Gemini supports this)
// 2. type is an array with multiple non-null types like ["string", "integer"] - converted to anyOf
// 3. Enums with nullable types need special handling
func normalizeSchemaForGemini(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}

	normalized := make(map[string]interface{})
	for k, v := range schema {
		normalized[k] = v
	}

	// Handle type field if it's an array (e.g., ["string", "null"] or ["string", "integer"])
	if typeVal, exists := normalized["type"]; exists {
		types := extractTypesFromValue(typeVal)
		if len(types) > 1 {
			// Count non-null types
			nonNullTypes := make([]string, 0, len(types))
			hasNull := false
			for _, t := range types {
				if t != "null" {
					nonNullTypes = append(nonNullTypes, t)
				} else {
					hasNull = true
				}
			}

			// If we have multiple non-null types, we need to convert to anyOf
			// because Gemini only supports ["type", "null"] but not ["type1", "type2"]
			if len(nonNullTypes) > 1 {
				// Multiple non-null types - must use anyOf
				delete(normalized, "type")

				// Build anyOf with each non-null type
				anyOfSchemas := make([]interface{}, 0, len(types))
				for _, t := range nonNullTypes {
					typeSchema := map[string]interface{}{"type": t}
					anyOfSchemas = append(anyOfSchemas, typeSchema)
				}

				// If original had null, add it to anyOf
				if hasNull {
					anyOfSchemas = append(anyOfSchemas, map[string]interface{}{"type": "null"})
				}

				normalized["anyOf"] = anyOfSchemas

				// Remove enum from top level if present, as it may not be compatible with anyOf
				delete(normalized, "enum")
			} else if len(nonNullTypes) == 1 && hasNull {
				// Single non-null type with null - keep as array (Gemini supports this)
				normalized["type"] = []interface{}{nonNullTypes[0], "null"}
			} else if len(nonNullTypes) == 1 && !hasNull {
				// Single type only - simplify to string
				normalized["type"] = nonNullTypes[0]
			} else if len(nonNullTypes) == 0 && hasNull {
				// Only null type
				normalized["type"] = "null"
			}
		}
	}

	// Recursively normalize properties
	if properties, ok := schema["properties"].(map[string]interface{}); ok {
		newProps := make(map[string]interface{})
		for key, prop := range properties {
			if propMap, ok := prop.(map[string]interface{}); ok {
				newProps[key] = normalizeSchemaForGemini(propMap)
			} else {
				newProps[key] = prop
			}
		}
		normalized["properties"] = newProps
	}

	// Recursively normalize items (for arrays)
	if items, ok := schema["items"].(map[string]interface{}); ok {
		normalized["items"] = normalizeSchemaForGemini(items)
	}

	// Recursively normalize anyOf
	if anyOf, ok := schema["anyOf"].([]interface{}); ok {
		newAnyOf := make([]interface{}, 0, len(anyOf))
		for _, item := range anyOf {
			if itemMap, ok := item.(map[string]interface{}); ok {
				newAnyOf = append(newAnyOf, normalizeSchemaForGemini(itemMap))
			} else {
				newAnyOf = append(newAnyOf, item)
			}
		}
		normalized["anyOf"] = newAnyOf
	}

	// Recursively normalize oneOf
	if oneOf, ok := schema["oneOf"].([]interface{}); ok {
		newOneOf := make([]interface{}, 0, len(oneOf))
		for _, item := range oneOf {
			if itemMap, ok := item.(map[string]interface{}); ok {
				newOneOf = append(newOneOf, normalizeSchemaForGemini(itemMap))
			} else {
				newOneOf = append(newOneOf, item)
			}
		}
		normalized["oneOf"] = newOneOf
	}

	return normalized
}

// extractSchemaMapFromResponseFormat extracts the JSON schema map from OpenAI's response_format structure
// This returns the raw schema map to be used with ResponseJSONSchema
func extractSchemaMapFromResponseFormat(responseFormat *interface{}) map[string]interface{} {
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

	// Normalize the schema for Gemini compatibility
	return normalizeSchemaForGemini(schemaMap)
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

// decodeBase64StringToBytes decodes a base64-encoded string into raw bytes.
//
// It accepts both standard base64 and URL-safe base64 encodings.
// URL-safe characters ('_' and '-') are converted back to their
// standard equivalents ('/' and '+') before decoding.
//
// If the input is missing padding, decodeBase64StringToBytes appends the required
// '=' characters so that the length becomes a multiple of 4.
// Returns an error if the base64 input is invalid.
func decodeBase64StringToBytes(b64 string) ([]byte, error) {
	// Convert URL-safe base64 to standard base64
	standardBase64 := strings.ReplaceAll(strings.ReplaceAll(b64, "_", "/"), "-", "+")

	// Add padding if necessary to make length a multiple of 4
	switch len(standardBase64) % 4 {
	case 2:
		standardBase64 += "=="
	case 3:
		standardBase64 += "="
	}

	decoded, err := base64.StdEncoding.DecodeString(standardBase64)
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

// encodeBytesToBase64String encodes raw bytes into a standard base64 string.
//
// It uses standard base64 encoding (not URL-safe) to ensure compatibility
// with APIs and SDKs that expect RFC 4648 base64 format.
//
// If the input byte slice is empty or nil, an empty string is returned.
func encodeBytesToBase64String(bytes []byte) string {
	var base64str string

	if len(bytes) > 0 {
		// Use standard base64 encoding to match external SDK expectations
		base64str = base64.StdEncoding.EncodeToString(bytes)
	}

	return base64str
}

// downloadImageFromURL downloads an image from a URL and returns the base64-encoded string
func downloadImageFromURL(ctx context.Context, imageURL string) (string, error) {
	client := fasthttp.Client{
		ReadTimeout: time.Second * 30,
	}
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(imageURL)
	req.Header.SetMethod(http.MethodGet)

	_, bifrostErr := providerUtils.MakeRequestWithContext(ctx, &client, req, resp)
	if bifrostErr != nil {
		return "", fmt.Errorf("failed to download image: %v", bifrostErr)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return "", fmt.Errorf("failed to download image: status=%d", resp.StatusCode())
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return "", fmt.Errorf("failed to read image data: %w", err)
	}

	// Copy the body to avoid use-after-free
	imageCopy := append([]byte(nil), body...)

	return encodeBytesToBase64String(imageCopy), nil
}
