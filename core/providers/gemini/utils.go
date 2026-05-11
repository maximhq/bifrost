package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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

	// Check first character - must be a digit, and '3' or higher for 3.0+
	firstChar := afterPrefix[0]
	if firstChar < '0' || firstChar > '9' {
		return false
	}
	return firstChar >= '3'
}

// supportsThinkingConfig returns true if the model supports ThinkingConfig.
// Only specific Gemini models support thinking:
// - gemini-*-thinking models (e.g., gemini-2.0-flash-thinking)
// - gemini-2.5-* models
// - gemini-3.* and higher models
func supportsThinkingConfig(model string) bool {
	modelLower := strings.ToLower(model)

	// Check for explicit "thinking" in model name
	if strings.Contains(modelLower, "thinking") {
		return true
	}

	// Check for gemini-2.5-* models
	if strings.Contains(modelLower, "gemini-2.5") {
		return true
	}

	// Check for Gemini 3.0+ models
	return isGemini3Plus(model)
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
	case "high", "xhigh", "max":
		return "high"
	default:
		if isPro {
			return "high"
		}
		return "medium"
	}
}

func getThinkingBudgetRange(model string, defaultMaxTokens int) thinkingBudgetRange {
	modelLower := strings.ToLower(model)
	for _, entry := range thinkingBudgetRanges {
		if strings.Contains(modelLower, entry.prefix) {
			return entry.r
		}
	}
	// Fallback for unknown thinking-capable models
	return thinkingBudgetRange{Min: DefaultReasoningMinBudget, Max: defaultMaxTokens}
}

// validateThinkingBudget returns an error if the explicit thinking budget is outside the
// model's allowed range. Budget 0 (disable) and -1 (dynamic) are always valid.
// Models not present in thinkingBudgetRanges are skipped — limits are only enforced
// for models whose ranges are explicitly known.
func validateThinkingBudget(model string, budget int) error {
	if budget == 0 || budget == DynamicReasoningBudget {
		return nil // 0 = disable thinking, -1 = dynamic
	}
	if budget < 0 {
		return fmt.Errorf("thinking budget %d is invalid; only 0 and -1 are supported special values", budget)
	}
	modelLower := strings.ToLower(model)

	var budgetRange thinkingBudgetRange
	found := false
	for _, entry := range thinkingBudgetRanges {
		if strings.Contains(modelLower, entry.prefix) {
			budgetRange = entry.r
			found = true
			break
		}
	}
	if !found {
		return nil // skip validation
	}
	if budget < budgetRange.Min {
		return fmt.Errorf("thinking budget %d is below the minimum of %d for model %s", budget, budgetRange.Min, model)
	}
	if budget > budgetRange.Max {
		return fmt.Errorf("thinking budget %d exceeds the maximum of %d for model %s", budget, budgetRange.Max, model)
	}
	return nil
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
		maxTokens := providerUtils.GetMaxOutputTokensOrDefault(r.Model, DefaultCompletionMaxTokens)
		if config.MaxOutputTokens > 0 {
			maxTokens = int(config.MaxOutputTokens)
		}
		budgetRange := getThinkingBudgetRange(r.Model, maxTokens)

		// Priority: Budget first (if present), then Level
		if config.ThinkingConfig.ThinkingBudget != nil {
			// Budget is set - use it directly
			budget := int(*config.ThinkingConfig.ThinkingBudget)
			params.Reasoning.MaxTokens = schemas.Ptr(budget)

			// Also provide effort for compatibility
			effort := providerUtils.GetReasoningEffortFromBudgetTokens(budget, budgetRange.Min, budgetRange.Max)
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

			switch strings.ToLower(level) {
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
		params.Properties = buildPropertiesOrderedMap(schema)
	}

	if len(schema.Enum) > 0 {
		params.Enum = schema.Enum
	}

	// Array schema fields
	if schema.Items != nil {
		params.Items = convertSchemaToOrderedMap(schema.Items)
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
			anyOf[i] = *convertSchemaToOrderedMap(s)
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
func convertSchemaToOrderedMap(schema *Schema) *schemas.OrderedMap {
	if schema == nil {
		return schemas.NewOrderedMap()
	}

	result := schemas.NewOrderedMap()

	if schema.Type != "" {
		result.Set("type", strings.ToLower(string(schema.Type)))
	}
	if schema.Description != "" {
		result.Set("description", schema.Description)
	}
	if len(schema.Enum) > 0 {
		result.Set("enum", schema.Enum)
	}
	if len(schema.Required) > 0 {
		result.Set("required", schema.Required)
	}
	if len(schema.Properties) > 0 {
		props := schemas.NewOrderedMapWithCapacity(len(schema.Properties))
		// Honor schema.PropertyOrdering first (Gemini's native ordering hint),
		// then any keys not listed there in alphabetical order for determinism.
		seen := make(map[string]struct{}, len(schema.Properties))
		for _, k := range schema.PropertyOrdering {
			if v, ok := schema.Properties[k]; ok {
				props.Set(k, convertSchemaToOrderedMap(v))
				seen[k] = struct{}{}
			}
		}
		remaining := make([]string, 0, len(schema.Properties)-len(seen))
		for k := range schema.Properties {
			if _, done := seen[k]; !done {
				remaining = append(remaining, k)
			}
		}
		sort.Strings(remaining)
		for _, k := range remaining {
			props.Set(k, convertSchemaToOrderedMap(schema.Properties[k]))
		}
		result.Set("properties", props)
	}
	if schema.Items != nil {
		result.Set("items", convertSchemaToOrderedMap(schema.Items))
	}
	if len(schema.AnyOf) > 0 {
		anyOf := make([]interface{}, len(schema.AnyOf))
		for i, s := range schema.AnyOf {
			anyOf[i] = convertSchemaToOrderedMap(s)
		}
		result.Set("anyOf", anyOf)
	}
	if schema.Format != "" {
		result.Set("format", schema.Format)
	}
	if schema.Pattern != "" {
		result.Set("pattern", schema.Pattern)
	}
	if schema.MinLength != nil {
		result.Set("minLength", *schema.MinLength)
	}
	if schema.MaxLength != nil {
		result.Set("maxLength", *schema.MaxLength)
	}
	if schema.MinItems != nil {
		result.Set("minItems", *schema.MinItems)
	}
	if schema.MaxItems != nil {
		result.Set("maxItems", *schema.MaxItems)
	}
	if schema.Minimum != nil {
		result.Set("minimum", *schema.Minimum)
	}
	if schema.Maximum != nil {
		result.Set("maximum", *schema.Maximum)
	}
	if schema.Title != "" {
		result.Set("title", schema.Title)
	}
	if schema.Default != nil {
		result.Set("default", schema.Default)
	}
	if schema.Nullable != nil {
		result.Set("nullable", *schema.Nullable)
	}

	return result
}

// buildPropertiesOrderedMap converts schema.Properties (a Go map, unordered by nature)
// into an *OrderedMap. Honors schema.PropertyOrdering (Gemini's native ordering hint)
// for the deterministic part, and appends remaining keys alphabetically. Each property
// value is recursively converted via convertSchemaToOrderedMap so the entire tree is
// order-preserving. No JSON round-trip — replaces the old convertSchemaToMap which
// went *Schema → bytes → map[string]any → OrderedMapFromMap (lying about order).
func buildPropertiesOrderedMap(schema *Schema) *schemas.OrderedMap {
	if schema == nil || len(schema.Properties) == 0 {
		return schemas.NewOrderedMap()
	}
	out := schemas.NewOrderedMapWithCapacity(len(schema.Properties))
	seen := make(map[string]struct{}, len(schema.Properties))
	for _, k := range schema.PropertyOrdering {
		if v, ok := schema.Properties[k]; ok {
			out.Set(k, convertSchemaToOrderedMap(v))
			seen[k] = struct{}{}
		}
	}
	remaining := make([]string, 0, len(schema.Properties)-len(seen))
	for k := range schema.Properties {
		if _, done := seen[k]; !done {
			remaining = append(remaining, k)
		}
	}
	sort.Strings(remaining)
	for _, k := range remaining {
		out.Set(k, convertSchemaToOrderedMap(schema.Properties[k]))
	}

	// convertTypeToLowerCase walks each property to lowercase any type fields.
	if normalized, ok := convertTypeToLowerCase(out).(*schemas.OrderedMap); ok {
		return normalized
	}
	return out
}

// convertTypeToLowerCase recursively converts all 'type' fields to lowercase in a schema.
//
// Operates on *schemas.OrderedMap to preserve insertion order end-to-end. Slices and
// primitive values are walked as-is. A legacy map[string]interface{} arm is kept as a
// safety net for any caller still passing unordered maps; it is converted to OrderedMap
// (alphabetical, since order was already lost) so downstream code only sees OrderedMap.
func convertTypeToLowerCase(schema interface{}) interface{} {
	switch v := schema.(type) {
	case *schemas.OrderedMap:
		if v == nil {
			return v
		}
		out := schemas.NewOrderedMapWithCapacity(v.Len())
		v.Range(func(key string, value interface{}) bool {
			if key == "type" {
				if strValue, ok := value.(string); ok {
					out.Set(key, strings.ToLower(strValue))
					return true
				}
			}
			out.Set(key, convertTypeToLowerCase(value))
			return true
		})
		return out
	case schemas.OrderedMap:
		return convertTypeToLowerCase(&v)
	case map[string]interface{}:
		// Legacy path: order was already lost upstream. Wrap into OrderedMap so the
		// rest of the pipeline doesn't have to handle bare maps.
		return convertTypeToLowerCase(schemas.OrderedMapFromMap(v))
	case []interface{}:
		newSlice := make([]interface{}, len(v))
		for i, item := range v {
			newSlice[i] = convertTypeToLowerCase(item)
		}
		return newSlice
	default:
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
		FinishReasonStop:                    "stop",
		FinishReasonMaxTokens:               "length",
		FinishReasonSafety:                  "content_filter",
		FinishReasonRecitation:              "content_filter",
		FinishReasonLanguage:                "content_filter",
		FinishReasonOther:                   "stop",
		FinishReasonBlocklist:               "content_filter",
		FinishReasonProhibitedContent:       "content_filter",
		FinishReasonSPII:                    "content_filter",
		FinishReasonMalformedFunctionCall:   "stop",
		FinishReasonImageSafety:             "content_filter",
		FinishReasonImageProhibitedContent:  "content_filter",
		FinishReasonImageOther:              "stop",
		FinishReasonNoImage:                 "stop",
		FinishReasonImageRecitation:         "content_filter",
		FinishReasonUnexpectedToolCall:      "stop",
		FinishReasonTooManyToolCalls:        "stop",
		FinishReasonMissingThoughtSignature: "stop",
		FinishReasonMalformedResponse:       "stop",
	}

	// Maps Bifrost canonical finish reasons back to the most representative Gemini finish reason
	bifrostToGeminiFinishReason = map[string]FinishReason{
		"stop":           FinishReasonStop,
		"length":         FinishReasonMaxTokens,
		"content_filter": FinishReasonSafety,
		"tool_calls":     FinishReasonStop,
	}
)

// ConvertGeminiFinishReasonToBifrost converts Gemini finish reasons to Bifrost format
func ConvertGeminiFinishReasonToBifrost(providerReason FinishReason) string {
	if bifrostReason, ok := geminiFinishReasonToBifrost[providerReason]; ok {
		return bifrostReason
	}
	return string(providerReason)
}

// ConvertBifrostFinishReasonToGemini converts Bifrost canonical finish reasons back to Gemini format.
func ConvertBifrostFinishReasonToGemini(bifrostReason string) FinishReason {
	if geminiReason, ok := bifrostToGeminiFinishReason[bifrostReason]; ok {
		return geminiReason
	}
	return FinishReasonStop
}

// ConvertGeminiUsageMetadataToChatUsage converts Gemini usage metadata to Bifrost chat LLM usage
func ConvertGeminiUsageMetadataToChatUsage(metadata *GenerateContentResponseUsageMetadata) *schemas.BifrostLLMUsage {
	if metadata == nil {
		return nil
	}

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     int(metadata.PromptTokenCount),
		CompletionTokens: int(metadata.CandidatesTokenCount),
		TotalTokens:      int(metadata.TotalTokenCount),
	}

	// Process prompt token details (modality breakdown + cached tokens)
	if len(metadata.PromptTokensDetails) > 0 || metadata.CachedContentTokenCount > 0 {
		if usage.PromptTokensDetails == nil {
			usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
		}

		// Map modality breakdowns from PromptTokensDetails
		for _, detail := range metadata.PromptTokensDetails {
			switch detail.Modality {
			case ModalityText:
				usage.PromptTokensDetails.TextTokens = int(detail.TokenCount)
			case ModalityAudio:
				usage.PromptTokensDetails.AudioTokens = int(detail.TokenCount)
			case ModalityImage:
				usage.PromptTokensDetails.ImageTokens = int(detail.TokenCount)
			}
		}

		// Add cached tokens if present
		if metadata.CachedContentTokenCount > 0 {
			usage.PromptTokensDetails.CachedReadTokens = int(metadata.CachedContentTokenCount)
		}
	}

	// Process completion token details (modality breakdown + reasoning tokens)
	if len(metadata.CandidatesTokensDetails) > 0 || metadata.ThoughtsTokenCount > 0 {
		if usage.CompletionTokensDetails == nil {
			usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{}
		}

		// Map modality breakdowns from CandidatesTokensDetails
		for _, detail := range metadata.CandidatesTokensDetails {
			switch detail.Modality {
			case ModalityText:
				usage.CompletionTokensDetails.TextTokens = int(detail.TokenCount)
			case ModalityAudio:
				usage.CompletionTokensDetails.AudioTokens = int(detail.TokenCount)
			case ModalityImage:
				usage.CompletionTokensDetails.ImageTokens = schemas.Ptr(int(detail.TokenCount))
			}
		}

		// Add reasoning tokens if present
		if metadata.ThoughtsTokenCount > 0 {
			usage.CompletionTokensDetails.ReasoningTokens = int(metadata.ThoughtsTokenCount)
			usage.CompletionTokens = usage.CompletionTokens + int(metadata.ThoughtsTokenCount)
		}
	}

	return usage
}

// convertGeminiUsageMetadataToSpeechUsage converts Gemini usage metadata to Bifrost speech usage
func convertGeminiUsageMetadataToSpeechUsage(metadata *GenerateContentResponseUsageMetadata) *schemas.SpeechUsage {
	if metadata == nil {
		return nil
	}

	usage := &schemas.SpeechUsage{
		InputTokens:  int(metadata.PromptTokenCount),
		OutputTokens: int(metadata.CandidatesTokenCount),
		TotalTokens:  int(metadata.TotalTokenCount),
	}

	// Process input token details (modality breakdown for audio+text)
	if len(metadata.PromptTokensDetails) > 0 {
		inputDetails := &schemas.SpeechUsageInputTokenDetails{}
		for _, detail := range metadata.PromptTokensDetails {
			switch detail.Modality {
			case ModalityText:
				inputDetails.TextTokens = int(detail.TokenCount)
			case ModalityAudio:
				inputDetails.AudioTokens = int(detail.TokenCount)
			}
		}
		usage.InputTokenDetails = inputDetails
	}

	return usage
}

// convertBifrostSpeechUsageToGeminiUsageMetadata converts Bifrost speech usage to Gemini usage metadata
func convertBifrostSpeechUsageToGeminiUsageMetadata(usage *schemas.SpeechUsage) *GenerateContentResponseUsageMetadata {
	if usage == nil {
		return nil
	}

	metadata := &GenerateContentResponseUsageMetadata{
		PromptTokenCount:     int32(usage.InputTokens),
		CandidatesTokenCount: int32(usage.OutputTokens),
		TotalTokenCount:      int32(usage.TotalTokens),
	}

	// Process input token details to PromptTokensDetails
	if usage.InputTokenDetails != nil {
		if usage.InputTokenDetails.TextTokens > 0 {
			metadata.PromptTokensDetails = append(metadata.PromptTokensDetails, &ModalityTokenCount{
				Modality:   ModalityText,
				TokenCount: int32(usage.InputTokenDetails.TextTokens),
			})
		}
		if usage.InputTokenDetails.AudioTokens > 0 {
			metadata.PromptTokensDetails = append(metadata.PromptTokensDetails, &ModalityTokenCount{
				Modality:   ModalityAudio,
				TokenCount: int32(usage.InputTokenDetails.AudioTokens),
			})
		}
	}

	return metadata
}

// convertGeminiUsageMetadataToTranscriptionUsage converts Gemini usage metadata to Bifrost transcription usage
func convertGeminiUsageMetadataToTranscriptionUsage(metadata *GenerateContentResponseUsageMetadata) *schemas.TranscriptionUsage {
	if metadata == nil {
		return nil
	}

	usage := &schemas.TranscriptionUsage{
		Type:         "tokens",
		InputTokens:  schemas.Ptr(int(metadata.PromptTokenCount)),
		OutputTokens: schemas.Ptr(int(metadata.CandidatesTokenCount)),
		TotalTokens:  schemas.Ptr(int(metadata.TotalTokenCount)),
	}

	// Process input token details (modality breakdown for audio+text)
	if len(metadata.PromptTokensDetails) > 0 {
		inputDetails := &schemas.TranscriptionUsageInputTokenDetails{}
		for _, detail := range metadata.PromptTokensDetails {
			switch detail.Modality {
			case ModalityText:
				inputDetails.TextTokens = int(detail.TokenCount)
			case ModalityAudio:
				inputDetails.AudioTokens = int(detail.TokenCount)
			}
		}
		usage.InputTokenDetails = inputDetails
	}

	return usage
}

// convertBifrostTranscriptionUsageToGeminiUsageMetadata converts Bifrost transcription usage to Gemini usage metadata
func convertBifrostTranscriptionUsageToGeminiUsageMetadata(usage *schemas.TranscriptionUsage) *GenerateContentResponseUsageMetadata {
	if usage == nil {
		return nil
	}

	metadata := &GenerateContentResponseUsageMetadata{}

	if usage.InputTokens != nil {
		metadata.PromptTokenCount = int32(*usage.InputTokens)
	}
	if usage.OutputTokens != nil {
		metadata.CandidatesTokenCount = int32(*usage.OutputTokens)
	}
	if usage.TotalTokens != nil {
		metadata.TotalTokenCount = int32(*usage.TotalTokens)
	}

	// Process input token details to PromptTokensDetails
	if usage.InputTokenDetails != nil {
		if usage.InputTokenDetails.TextTokens > 0 {
			metadata.PromptTokensDetails = append(metadata.PromptTokensDetails, &ModalityTokenCount{
				Modality:   ModalityText,
				TokenCount: int32(usage.InputTokenDetails.TextTokens),
			})
		}
		if usage.InputTokenDetails.AudioTokens > 0 {
			metadata.PromptTokensDetails = append(metadata.PromptTokensDetails, &ModalityTokenCount{
				Modality:   ModalityAudio,
				TokenCount: int32(usage.InputTokenDetails.AudioTokens),
			})
		}
	}

	return metadata
}

// convertGeminiUsageMetadataToImageUsage converts Gemini usage metadata to Bifrost image usage
func convertGeminiUsageMetadataToImageUsage(metadata *GenerateContentResponseUsageMetadata) *schemas.ImageUsage {
	if metadata == nil {
		return nil
	}

	usage := &schemas.ImageUsage{
		InputTokens:  int(metadata.PromptTokenCount),
		OutputTokens: int(metadata.CandidatesTokenCount),
		TotalTokens:  int(metadata.TotalTokenCount),
	}

	// Process input token details (modality breakdown)
	if len(metadata.PromptTokensDetails) > 0 {
		inputDetails := &schemas.ImageTokenDetails{}
		for _, detail := range metadata.PromptTokensDetails {
			switch detail.Modality {
			case ModalityText:
				inputDetails.TextTokens = int(detail.TokenCount)
			case ModalityImage:
				inputDetails.ImageTokens = int(detail.TokenCount)
			}
		}
		usage.InputTokensDetails = inputDetails
	}

	// Process output token details (modality breakdown)
	if len(metadata.CandidatesTokensDetails) > 0 {
		outputDetails := &schemas.ImageTokenDetails{}
		for _, detail := range metadata.CandidatesTokensDetails {
			switch detail.Modality {
			case ModalityText:
				outputDetails.TextTokens = int(detail.TokenCount)
			case ModalityImage:
				outputDetails.ImageTokens = int(detail.TokenCount)
			}
		}
		usage.OutputTokensDetails = outputDetails
	}

	return usage
}

// convertBifrostImageUsageToGeminiUsageMetadata converts Bifrost image usage to Gemini usage metadata
func convertBifrostImageUsageToGeminiUsageMetadata(usage *schemas.ImageUsage) *GenerateContentResponseUsageMetadata {
	if usage == nil {
		return nil
	}

	metadata := &GenerateContentResponseUsageMetadata{
		PromptTokenCount:     int32(usage.InputTokens),
		CandidatesTokenCount: int32(usage.OutputTokens),
		TotalTokenCount:      int32(usage.TotalTokens),
	}

	// Process input token details to PromptTokensDetails
	if usage.InputTokensDetails != nil {
		if usage.InputTokensDetails.TextTokens > 0 {
			metadata.PromptTokensDetails = append(metadata.PromptTokensDetails, &ModalityTokenCount{
				Modality:   ModalityText,
				TokenCount: int32(usage.InputTokensDetails.TextTokens),
			})
		}
		if usage.InputTokensDetails.ImageTokens > 0 {
			metadata.PromptTokensDetails = append(metadata.PromptTokensDetails, &ModalityTokenCount{
				Modality:   ModalityImage,
				TokenCount: int32(usage.InputTokensDetails.ImageTokens),
			})
		}
	}

	// Process output token details to CandidatesTokensDetails
	if usage.OutputTokensDetails != nil {
		if usage.OutputTokensDetails.TextTokens > 0 {
			metadata.CandidatesTokensDetails = append(metadata.CandidatesTokensDetails, &ModalityTokenCount{
				Modality:   ModalityText,
				TokenCount: int32(usage.OutputTokensDetails.TextTokens),
			})
		}
		if usage.OutputTokensDetails.ImageTokens > 0 {
			metadata.CandidatesTokensDetails = append(metadata.CandidatesTokensDetails, &ModalityTokenCount{
				Modality:   ModalityImage,
				TokenCount: int32(usage.OutputTokensDetails.ImageTokens),
			})
		}
	}

	return metadata
}

// ConvertGeminiUsageMetadataToResponsesUsage converts Gemini usage metadata to Bifrost responses usage
func ConvertGeminiUsageMetadataToResponsesUsage(metadata *GenerateContentResponseUsageMetadata) *schemas.ResponsesResponseUsage {
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

	// Process input token details (modality breakdown + cached tokens)
	if len(metadata.PromptTokensDetails) > 0 {
		for _, detail := range metadata.PromptTokensDetails {
			switch detail.Modality {
			case ModalityText:
				usage.InputTokensDetails.TextTokens = int(detail.TokenCount)
			case ModalityAudio:
				usage.InputTokensDetails.AudioTokens = int(detail.TokenCount)
			case ModalityImage:
				usage.InputTokensDetails.ImageTokens = int(detail.TokenCount)
			}
		}
	}

	// Add cached tokens if present
	if metadata.CachedContentTokenCount > 0 {
		usage.InputTokensDetails.CachedReadTokens = int(metadata.CachedContentTokenCount)
	}

	// Process output token details (modality breakdown + reasoning tokens)
	if len(metadata.CandidatesTokensDetails) > 0 {
		for _, detail := range metadata.CandidatesTokensDetails {
			switch detail.Modality {
			case ModalityText:
				usage.OutputTokensDetails.TextTokens = int(detail.TokenCount)
			case ModalityAudio:
				usage.OutputTokensDetails.AudioTokens = int(detail.TokenCount)
			case ModalityImage:
				usage.OutputTokensDetails.ImageTokens = schemas.Ptr(int(detail.TokenCount))
			}
		}
	}

	// Add reasoning tokens if present
	if metadata.ThoughtsTokenCount > 0 {
		usage.OutputTokensDetails.ReasoningTokens = int(metadata.ThoughtsTokenCount)
		usage.OutputTokens = usage.OutputTokens + int(metadata.ThoughtsTokenCount)
	}

	return usage
}

func ConvertBifrostResponsesUsageToGeminiUsageMetadata(usage *schemas.ResponsesResponseUsage) *GenerateContentResponseUsageMetadata {
	if usage == nil {
		return nil
	}
	metadata := &GenerateContentResponseUsageMetadata{
		PromptTokenCount:     int32(usage.InputTokens),
		CandidatesTokenCount: int32(usage.OutputTokens),
		TotalTokenCount:      int32(usage.TotalTokens),
	}
	if usage.OutputTokensDetails != nil {
		metadata.ThoughtsTokenCount = int32(usage.OutputTokensDetails.ReasoningTokens)
		metadata.CandidatesTokenCount = metadata.CandidatesTokenCount - metadata.ThoughtsTokenCount
	}

	promptTokensDetails := make([]*ModalityTokenCount, 0)
	candidatesTokensDetails := make([]*ModalityTokenCount, 0)

	if usage.InputTokensDetails != nil {
		if usage.InputTokensDetails.CachedReadTokens > 0 {
			metadata.CachedContentTokenCount = int32(usage.InputTokensDetails.CachedReadTokens)
		}
		promptTokensDetails = append(promptTokensDetails, &ModalityTokenCount{
			Modality:   ModalityText,
			TokenCount: int32(usage.InputTokensDetails.TextTokens),
		})
		if usage.InputTokensDetails.AudioTokens > 0 {
			promptTokensDetails = append(promptTokensDetails, &ModalityTokenCount{
				Modality:   ModalityAudio,
				TokenCount: int32(usage.InputTokensDetails.AudioTokens),
			})
		}
		if usage.InputTokensDetails.ImageTokens > 0 {
			promptTokensDetails = append(promptTokensDetails, &ModalityTokenCount{
				Modality:   ModalityImage,
				TokenCount: int32(usage.InputTokensDetails.ImageTokens),
			})
		}
	}
	metadata.PromptTokensDetails = promptTokensDetails
	if usage.OutputTokensDetails != nil {
		candidatesTokensDetails = append(candidatesTokensDetails, &ModalityTokenCount{
			Modality:   ModalityText,
			TokenCount: int32(usage.OutputTokensDetails.TextTokens),
		})
		if usage.OutputTokensDetails.AudioTokens > 0 {
			candidatesTokensDetails = append(candidatesTokensDetails, &ModalityTokenCount{
				Modality:   ModalityAudio,
				TokenCount: int32(usage.OutputTokensDetails.AudioTokens),
			})
		}
		if usage.OutputTokensDetails.ImageTokens != nil && *usage.OutputTokensDetails.ImageTokens > 0 {
			candidatesTokensDetails = append(candidatesTokensDetails, &ModalityTokenCount{
				Modality:   ModalityImage,
				TokenCount: int32(*usage.OutputTokensDetails.ImageTokens),
			})
		}
	}
	metadata.CandidatesTokensDetails = candidatesTokensDetails
	return metadata
}

// convertParamsToGenerationConfig converts Bifrost parameters to Gemini GenerationConfig
func convertParamsToGenerationConfig(params *schemas.ChatParameters, responseModalities []string, model string) (GenerationConfig, error) {
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
	// Only set ThinkingConfig if the model actually supports thinking
	if params.Reasoning != nil && supportsThinkingConfig(model) {
		config.ThinkingConfig = &GenerationConfigThinkingConfig{
			IncludeThoughts: true,
		}

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
				if err := validateThinkingBudget(model, budget); err != nil {
					return config, err
				}
				config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(budget))
			}
		} else if hasEffort {
			// User provided effort only (no max_tokens)
			if supportsLevel {
				// Gemini 3.0+ - use thinkingLevel (more native)
				level := effortToThinkingLevel(*params.Reasoning.Effort, model)
				config.ThinkingConfig.ThinkingLevel = &level
			} else {
				maxTokens := providerUtils.GetMaxOutputTokensOrDefault(model, DefaultCompletionMaxTokens)
				if config.MaxOutputTokens > 0 {
					maxTokens = int(config.MaxOutputTokens)
				}
				budgetRange := getThinkingBudgetRange(model, maxTokens)
				// Gemini < 3.0 - must convert effort to budget
				budgetTokens, err := providerUtils.GetBudgetTokensFromReasoningEffort(
					*params.Reasoning.Effort,
					budgetRange.Min,
					budgetRange.Max,
				)
				if err == nil {
					config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(budgetTokens))
				}
			}
		}
	}
	// Handle response_format to response_schema conversion. The outer guard accepts
	// *OrderedMap, plain map, or raw JSON bytes — extractSchemaMapFromResponseFormat
	// dispatches on the same set internally, but we read "type" here cheaply to gate
	// the switch without a full parse.
	if params.ResponseFormat != nil {
		formatType := readResponseFormatType(*params.ResponseFormat)
		switch formatType {
		case "json_schema":
			// OpenAI Structured Outputs: {"type": "json_schema", "json_schema": {...}}
			if schemaOM := extractSchemaMapFromResponseFormat(params.ResponseFormat); schemaOM != nil {
				config.ResponseMIMEType = "application/json"
				config.ResponseJSONSchema = schemaOM
			}
		case "json_object":
			// Maps to Gemini's responseMimeType without schema
			config.ResponseMIMEType = "application/json"
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
	// Mapping logprobs to generation config
	if params.LogProbs != nil {
		config.ResponseLogprobs = *params.LogProbs
	}
	// Mapping top_logprobs to generation config
	if params.TopLogProbs != nil {
		topLogProbs := *params.TopLogProbs
		if topLogProbs > 20 {
			topLogProbs = 20
		}
		if topLogProbs > 0 {
			config.ResponseLogprobs = true
			config.Logprobs = schemas.Ptr(int32(topLogProbs))
		}
	}
	return config, nil
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

	if params.Properties != nil && params.Properties.Len() > 0 {
		schema.Properties = make(map[string]*Schema)
		schema.PropertyOrdering = params.Properties.Keys()
		params.Properties.Range(func(k string, v interface{}) bool {
			schema.Properties[k] = convertPropertyToSchema(v)
			return true
		})
	}

	// Array schema fields
	if params.Items != nil {
		schema.Items = convertPropertyToSchema(params.Items)
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

	// Coerce all input forms (*OrderedMap, value OrderedMap, legacy plain map) into a
	// single *OrderedMap. Plain-map inputs are wrapped via OrderedMapFromMap as a
	// best-effort fallback — order was already lost upstream in that case.
	propOM := asOrderedMap(prop)
	if propOM == nil {
		return schema
	}

	get := func(key string) (interface{}, bool) { return propOM.Get(key) }

	if v, ok := get("type"); ok {
		if s, ok := v.(string); ok {
			schema.Type = Type(s)
		}
	}
	if v, ok := get("description"); ok {
		if s, ok := v.(string); ok {
			schema.Description = s
		}
	}

	if v, ok := get("enum"); ok {
		switch en := v.(type) {
		case []interface{}:
			out := make([]string, 0, len(en))
			for _, item := range en {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
			schema.Enum = out
		case []string:
			schema.Enum = en
		}
	}

	// Handle nested properties for object types — populates PropertyOrdering from the
	// OrderedMap's key sequence so order survives the next outbound MarshalJSON.
	if v, ok := get("properties"); ok {
		if propsOM := asOrderedMap(v); propsOM != nil {
			schema.Properties = make(map[string]*Schema, propsOM.Len())
			schema.PropertyOrdering = propsOM.Keys()
			propsOM.Range(func(key string, nestedProp interface{}) bool {
				schema.Properties[key] = convertPropertyToSchema(nestedProp)
				return true
			})
		}
	}

	if v, ok := get("items"); ok {
		schema.Items = convertPropertyToSchema(v)
	}

	if v, ok := get("required"); ok {
		switch req := v.(type) {
		case []interface{}:
			out := make([]string, 0, len(req))
			for _, item := range req {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
			schema.Required = out
		case []string:
			schema.Required = req
		}
	}

	if v, ok := get("anyOf"); ok {
		if slice, ok := v.([]interface{}); ok {
			schema.AnyOf = make([]*Schema, len(slice))
			for i, item := range slice {
				schema.AnyOf[i] = convertPropertyToSchema(item)
			}
		}
	}

	// Gemini treats oneOf the same as anyOf, so map it to AnyOf when AnyOf is unset.
	if v, ok := get("oneOf"); ok && len(schema.AnyOf) == 0 {
		if slice, ok := v.([]interface{}); ok {
			schema.AnyOf = make([]*Schema, len(slice))
			for i, item := range slice {
				schema.AnyOf[i] = convertPropertyToSchema(item)
			}
		}
	}

	if v, ok := get("format"); ok {
		if s, ok := v.(string); ok {
			schema.Format = s
		}
	}
	if v, ok := get("pattern"); ok {
		if s, ok := v.(string); ok {
			schema.Pattern = s
		}
	}
	if v, ok := get("minLength"); ok {
		if n, ok := toInt64(v); ok {
			schema.MinLength = &n
		}
	}
	if v, ok := get("maxLength"); ok {
		if n, ok := toInt64(v); ok {
			schema.MaxLength = &n
		}
	}
	if v, ok := get("minimum"); ok {
		if f, ok := toFloat64(v); ok {
			schema.Minimum = &f
		}
	}
	if v, ok := get("maximum"); ok {
		if f, ok := toFloat64(v); ok {
			schema.Maximum = &f
		}
	}
	if v, ok := get("minItems"); ok {
		if n, ok := toInt64(v); ok {
			schema.MinItems = &n
		}
	}
	if v, ok := get("maxItems"); ok {
		if n, ok := toInt64(v); ok {
			schema.MaxItems = &n
		}
	}
	if v, ok := get("title"); ok {
		if s, ok := v.(string); ok {
			schema.Title = s
		}
	}
	if v, ok := get("default"); ok {
		schema.Default = v
	}
	if v, ok := get("nullable"); ok {
		if b, ok := v.(bool); ok {
			schema.Nullable = &b
		}
	}

	// Honor Gemini's native ordering hint when present in the input — useful when
	// the caller already had a propertyOrdering they'd like to override our derived
	// order with.
	if v, ok := get("propertyOrdering"); ok {
		switch po := v.(type) {
		case []string:
			if len(po) > 0 {
				schema.PropertyOrdering = po
			}
		case []interface{}:
			out := make([]string, 0, len(po))
			for _, p := range po {
				if s, ok := p.(string); ok {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				schema.PropertyOrdering = out
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
func convertToolChoiceToToolConfig(toolChoice *schemas.ChatToolChoice) *ToolConfig {
	if toolChoice == nil || (toolChoice.ChatToolChoiceStr == nil && toolChoice.ChatToolChoiceStruct == nil) {
		return nil
	}
	config := &ToolConfig{}
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
		if toolChoice.ChatToolChoiceStruct.Function != nil && toolChoice.ChatToolChoiceStruct.Function.Name != "" {
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
			var responseData json.RawMessage
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

			// Try to use raw JSON if it's a valid JSON object (Gemini requires Struct/object)
			if contentStr != "" {
				var buf bytes.Buffer
				if err := json.Compact(&buf, []byte(contentStr)); err == nil && buf.Len() > 0 && buf.Bytes()[0] == '{' {
					// Valid JSON object — use raw bytes directly
					responseData = json.RawMessage(buf.Bytes())
				} else {
					// Not valid JSON or not an object — wrap to preserve content
					responseData, _ = providerUtils.MarshalSorted(map[string]any{
						"content": contentStr,
					})
				}
			} else {
				// If no content at all, use empty object to avoid nil
				responseData = json.RawMessage(`{}`)
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
					// Preserve original key ordering of tool arguments for prompt caching.
					var argsRaw json.RawMessage
					if toolCall.Function.Arguments != "" {
						var buf bytes.Buffer
						if err := json.Compact(&buf, []byte(toolCall.Function.Arguments)); err == nil {
							argsRaw = buf.Bytes()
						} else {
							argsRaw = json.RawMessage("{}")
						}
					} else {
						argsRaw = json.RawMessage("{}")
					}
					// Handle ID: use it if available, otherwise fallback to function name
					callID := *toolCall.Function.Name
					if toolCall.ID != nil && strings.TrimSpace(*toolCall.ID) != "" {
						callID = *toolCall.ID
					}

					// Extract thought signature from CallID if embedded (matches responses.go pattern)
					var thoughtSig string
					if strings.Contains(callID, thoughtSignatureSeparator) {
						parts := strings.SplitN(callID, thoughtSignatureSeparator, 2)
						if len(parts) == 2 {
							thoughtSig = parts[1]
						}
					}

					part := &Part{
						FunctionCall: &FunctionCall{
							ID:   callID,
							Name: *toolCall.Function.Name,
							Args: argsRaw,
						},
					}
					// Store the mapping for later use in FunctionResponse
					callIDToFunctionName[callID] = *toolCall.Function.Name

					// Decode thought signature if extracted from ID
					if thoughtSig != "" {
						decoded, err := base64.RawURLEncoding.DecodeString(thoughtSig)
						if err == nil {
							part.ThoughtSignature = decoded
						}
					}

					// Also check in reasoning details array for thought signature (fallback)
					if part.ThoughtSignature == nil && len(message.ChatAssistantMessage.ReasoningDetails) > 0 {
						// Extract base ID for lookup (strip signature if present)
						baseCallID := callID
						if strings.Contains(callID, thoughtSignatureSeparator) {
							splitParts := strings.SplitN(callID, thoughtSignatureSeparator, 2)
							if len(splitParts) == 2 {
								baseCallID = splitParts[0]
							}
						}
						lookupID := fmt.Sprintf("tool_call_%s", baseCallID)
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

					if part.ThoughtSignature == nil {
						part.ThoughtSignature = []byte(skipThoughtSignatureValidator)
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

// normalizeSchemaTypes recursively lowercases type values (OBJECT → object, STRING → string, ...).
//
// Operates on *schemas.OrderedMap end-to-end so insertion order is preserved through the
// rewrite. Walks Keys() in order, recurses into properties / items / anyOf / oneOf / allOf,
// and writes results back via Set() — which is in-place for existing keys, so the surrounding
// document order is never disturbed.
func normalizeSchemaTypes(schema *schemas.OrderedMap) *schemas.OrderedMap {
	if schema == nil {
		return nil
	}

	normalized := schemas.NewOrderedMapWithCapacity(schema.Len())
	schema.Range(func(k string, v interface{}) bool {
		normalized.Set(k, v)
		return true
	})

	if typeVal, ok := normalized.Get("type"); ok {
		if typeStr, ok := typeVal.(string); ok {
			normalized.Set("type", strings.ToLower(typeStr))
		}
	}

	if propsVal, ok := normalized.Get("properties"); ok {
		if propsOM := asOrderedMap(propsVal); propsOM != nil {
			newProps := schemas.NewOrderedMapWithCapacity(propsOM.Len())
			propsOM.Range(func(key string, prop interface{}) bool {
				if propOM := asOrderedMap(prop); propOM != nil {
					newProps.Set(key, normalizeSchemaTypes(propOM))
				} else {
					newProps.Set(key, prop)
				}
				return true
			})
			normalized.Set("properties", newProps)
		}
	}

	if itemsVal, ok := normalized.Get("items"); ok {
		if itemsOM := asOrderedMap(itemsVal); itemsOM != nil {
			normalized.Set("items", normalizeSchemaTypes(itemsOM))
		}
	}

	for _, branch := range []string{"anyOf", "oneOf", "allOf"} {
		branchVal, ok := normalized.Get(branch)
		if !ok {
			continue
		}
		if slice, ok := branchVal.([]interface{}); ok {
			newSlice := make([]interface{}, len(slice))
			for i, item := range slice {
				if itemOM := asOrderedMap(item); itemOM != nil {
					newSlice[i] = normalizeSchemaTypes(itemOM)
				} else {
					newSlice[i] = item
				}
			}
			normalized.Set(branch, newSlice)
		}
	}

	return normalized
}

// asOrderedMap coerces value/pointer/legacy-map forms into *OrderedMap. Returns nil if
// the value isn't map-shaped. The legacy map[string]interface{} arm wraps via
// OrderedMapFromMap (alphabetical, since order was already lost at that boundary) so
// downstream walkers only ever see *OrderedMap.
func asOrderedMap(v interface{}) *schemas.OrderedMap {
	switch m := v.(type) {
	case *schemas.OrderedMap:
		return m
	case schemas.OrderedMap:
		return &m
	case map[string]interface{}:
		return schemas.OrderedMapFromMap(m)
	default:
		return nil
	}
}

// buildJSONSchemaFromOrderedMap converts an order-preserving schema document into
// ResponsesTextConfigFormatJSONSchema. Properties / Defs / Definitions / Items are
// assigned as *OrderedMap (no map detour); AnyOf / OneOf / AllOf are []OrderedMap.
// This is the only path that produces a JSONSchema for downstream Gemini marshaling,
// so order survives all the way to the wire.
func buildJSONSchemaFromOrderedMap(schemaMap *schemas.OrderedMap) *schemas.ResponsesTextConfigFormatJSONSchema {
	if schemaMap == nil {
		return &schemas.ResponsesTextConfigFormatJSONSchema{}
	}
	// Normalize types (OBJECT → object, STRING → string, etc.) — preserves key order.
	normalized := normalizeSchemaTypes(schemaMap)

	jsonSchema := &schemas.ResponsesTextConfigFormatJSONSchema{}

	get := func(key string) (interface{}, bool) { return normalized.Get(key) }

	// Extract type
	if v, ok := get("type"); ok {
		if typeVal, ok := v.(string); ok {
			jsonSchema.Type = schemas.Ptr(typeVal)
		}
	}

	// Properties — order-preserving *OrderedMap
	if v, ok := get("properties"); ok {
		if om := asOrderedMap(v); om != nil {
			jsonSchema.Properties = om
		}
	}

	// Required: []interface{} → []string OR []string passthrough
	if v, ok := get("required"); ok {
		switch req := v.(type) {
		case []interface{}:
			out := make([]string, 0, len(req))
			for _, r := range req {
				if s, ok := r.(string); ok {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				jsonSchema.Required = out
			}
		case []string:
			if len(req) > 0 {
				jsonSchema.Required = req
			}
		}
	}

	if v, ok := get("description"); ok {
		if s, ok := v.(string); ok {
			jsonSchema.Description = schemas.Ptr(s)
		}
	}

	// additionalProperties: bool OR map/OrderedMap
	if v, ok := get("additionalProperties"); ok {
		if b, ok := v.(bool); ok {
			jsonSchema.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
				AdditionalPropertiesBool: &b,
			}
		} else if om, ok := schemas.SafeExtractOrderedMap(v); ok {
			jsonSchema.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
				AdditionalPropertiesMap: om,
			}
		}
	}

	// Name preference: name → title
	if v, ok := get("name"); ok {
		if s, ok := v.(string); ok {
			jsonSchema.Name = schemas.Ptr(s)
		}
	} else if v, ok := get("title"); ok {
		if s, ok := v.(string); ok {
			jsonSchema.Name = schemas.Ptr(s)
		}
	}

	if v, ok := get("$defs"); ok {
		if om := asOrderedMap(v); om != nil {
			jsonSchema.Defs = om
		}
	}
	if v, ok := get("definitions"); ok {
		if om := asOrderedMap(v); om != nil {
			jsonSchema.Definitions = om
		}
	}
	if v, ok := get("$ref"); ok {
		if s, ok := v.(string); ok {
			jsonSchema.Ref = schemas.Ptr(s)
		}
	}

	if v, ok := get("items"); ok {
		if om := asOrderedMap(v); om != nil {
			jsonSchema.Items = om
		}
	}

	if v, ok := get("minItems"); ok {
		if n, ok := toInt64(v); ok {
			jsonSchema.MinItems = &n
		}
	}
	if v, ok := get("maxItems"); ok {
		if n, ok := toInt64(v); ok {
			jsonSchema.MaxItems = &n
		}
	}

	jsonSchema.AnyOf = collectOrderedMapSlice(normalized, "anyOf")
	jsonSchema.OneOf = collectOrderedMapSlice(normalized, "oneOf")
	jsonSchema.AllOf = collectOrderedMapSlice(normalized, "allOf")

	if v, ok := get("format"); ok {
		if s, ok := v.(string); ok {
			jsonSchema.Format = schemas.Ptr(s)
		}
	}
	if v, ok := get("pattern"); ok {
		if s, ok := v.(string); ok {
			jsonSchema.Pattern = schemas.Ptr(s)
		}
	}
	if v, ok := get("minLength"); ok {
		if n, ok := toInt64(v); ok {
			jsonSchema.MinLength = &n
		}
	}
	if v, ok := get("maxLength"); ok {
		if n, ok := toInt64(v); ok {
			jsonSchema.MaxLength = &n
		}
	}
	if v, ok := get("minimum"); ok {
		if f, ok := toFloat64(v); ok {
			jsonSchema.Minimum = &f
		}
	}
	if v, ok := get("maximum"); ok {
		if f, ok := toFloat64(v); ok {
			jsonSchema.Maximum = &f
		}
	}
	if v, ok := get("title"); ok {
		if s, ok := v.(string); ok {
			jsonSchema.Title = schemas.Ptr(s)
		}
	}
	if v, ok := get("default"); ok {
		jsonSchema.Default = v
	}
	if v, ok := get("nullable"); ok {
		if b, ok := v.(bool); ok {
			jsonSchema.Nullable = &b
		}
	}

	if v, ok := get("enum"); ok {
		switch en := v.(type) {
		case []interface{}:
			out := make([]string, 0, len(en))
			for _, e := range en {
				if s, ok := e.(string); ok {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				jsonSchema.Enum = out
			}
		case []string:
			if len(en) > 0 {
				jsonSchema.Enum = en
			}
		}
	}

	// Gemini's native ordering hint — round-trip it if present.
	if v, ok := get("propertyOrdering"); ok {
		switch po := v.(type) {
		case []string:
			if len(po) > 0 {
				jsonSchema.PropertyOrdering = po
			}
		case []interface{}:
			out := make([]string, 0, len(po))
			for _, p := range po {
				if s, ok := p.(string); ok {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				jsonSchema.PropertyOrdering = out
			}
		}
	}

	return jsonSchema
}

// collectOrderedMapSlice extracts a composition branch (anyOf/oneOf/allOf) and
// converts each element to a value-form OrderedMap so the parent struct can hold
// it as []OrderedMap (matching the Phase 1 schema field type).
func collectOrderedMapSlice(om *schemas.OrderedMap, key string) []schemas.OrderedMap {
	v, ok := om.Get(key)
	if !ok {
		return nil
	}
	slice, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]schemas.OrderedMap, 0, len(slice))
	for _, item := range slice {
		if branch := asOrderedMap(item); branch != nil {
			out = append(out, *branch)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func NormalizeModelName(model string) string {
	model = strings.TrimSpace(model)
	if len(model) >= len("google/") && strings.EqualFold(model[:len("google/")], "google/") {
		strippedModel := model[len("google/"):]
		if schemas.IsGeminiModel(strippedModel) || schemas.IsVeoModel(strippedModel) || schemas.IsImagenModel(strippedModel) || schemas.IsGemmaModel(strippedModel) {
			return strippedModel
		}
	}
	return model
}

// buildOpenAIResponseFormat builds OpenAI response_format for JSON types.
//
// Two input arms:
//  1. responseJsonSchema (already-decoded user input): may arrive as *OrderedMap,
//     value-form OrderedMap, legacy map, or json.RawMessage / []byte. We coerce to
//     *OrderedMap without ever dropping into a plain map round-trip.
//  2. responseSchema (Gemini-side *Schema struct): walked field-by-field via
//     convertSchemaToOrderedMap — no Marshal/Unmarshal — and lowercased via
//     convertTypeToLowerCase. Preserves PropertyOrdering through the pipeline.
//
// Falls back to json_object mode whenever the input doesn't shape up to a usable
// schema document.
func buildOpenAIResponseFormat(responseJsonSchema interface{}, responseSchema *Schema) *schemas.ResponsesTextConfig {
	jsonObject := func() *schemas.ResponsesTextConfig {
		return &schemas.ResponsesTextConfig{
			Format: &schemas.ResponsesTextConfigFormat{Type: "json_object"},
		}
	}

	name := "json_response"
	var schemaOM *schemas.OrderedMap

	switch {
	case responseJsonSchema != nil:
		// Try direct OrderedMap / map forms first.
		if om := asOrderedMap(responseJsonSchema); om != nil {
			schemaOM = om
		} else if data, ok := bytesFromAny(responseJsonSchema); ok {
			// Raw JSON bytes — decode straight into OrderedMap, preserving order.
			parsed, err := providerUtils.UnmarshalOrdered(data)
			if err != nil {
				return jsonObject()
			}
			schemaOM = parsed
		} else {
			return jsonObject()
		}
	case responseSchema != nil:
		// *Schema → *OrderedMap via struct-field walk; no JSON round-trip.
		om := convertSchemaToOrderedMap(responseSchema)
		if normalized, ok := convertTypeToLowerCase(om).(*schemas.OrderedMap); ok {
			schemaOM = normalized
		} else {
			schemaOM = om
		}
	default:
		return jsonObject()
	}

	if schemaOM == nil {
		return jsonObject()
	}

	if v, ok := schemaOM.Get("title"); ok {
		if s, ok := v.(string); ok && s != "" {
			name = s
		}
	}

	jsonSchema := buildJSONSchemaFromOrderedMap(schemaOM)

	return &schemas.ResponsesTextConfig{
		Format: &schemas.ResponsesTextConfigFormat{
			Type:       "json_schema",
			Name:       schemas.Ptr(name),
			Strict:     schemas.Ptr(false),
			JSONSchema: jsonSchema,
		},
	}
}

// bytesFromAny extracts raw JSON bytes from common envelope types so callers can
// decode straight into OrderedMap (preserving order) instead of going through a
// plain-map intermediate.
func bytesFromAny(v interface{}) ([]byte, bool) {
	switch b := v.(type) {
	case []byte:
		return b, true
	case json.RawMessage:
		return b, true
	case string:
		return []byte(b), true
	default:
		return nil, false
	}
}

// readResponseFormatType returns the top-level "type" field of an OpenAI-style
// response_format envelope without decoding the whole document. Accepts the same
// input forms as extractSchemaMapFromResponseFormat (*OrderedMap, plain map,
// raw bytes/RawMessage/string). Returns "" when the field is absent or non-string.
func readResponseFormatType(v interface{}) string {
	if v == nil {
		return ""
	}
	if data, ok := bytesFromAny(v); ok {
		return providerUtils.GetJSONField(data, "type").String()
	}
	if om := asOrderedMap(v); om != nil {
		if t, ok := om.Get("type"); ok {
			if s, ok := t.(string); ok {
				return s
			}
		}
	}
	return ""
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
//
// Operates on *schemas.OrderedMap end-to-end so insertion order survives the rewrite.
// When the type-array → anyOf conversion fires, the new anyOf branches are themselves
// *OrderedMap so MarshalJSON emits a deterministic, byte-stable result.
func normalizeSchemaForGemini(schema *schemas.OrderedMap) *schemas.OrderedMap {
	if schema == nil {
		return nil
	}

	normalized := schemas.NewOrderedMapWithCapacity(schema.Len())
	schema.Range(func(k string, v interface{}) bool {
		normalized.Set(k, v)
		return true
	})

	// Handle type field if it's an array (e.g., ["string", "null"] or ["string", "integer"])
	if typeVal, exists := normalized.Get("type"); exists {
		types := extractTypesFromValue(typeVal)
		if len(types) > 1 {
			nonNullTypes := make([]string, 0, len(types))
			hasNull := false
			for _, t := range types {
				if t != "null" {
					nonNullTypes = append(nonNullTypes, t)
				} else {
					hasNull = true
				}
			}

			// Multiple non-null types: Gemini only supports ["type", "null"] but not
			// ["type1", "type2"], so convert to anyOf with one schema per branch.
			if len(nonNullTypes) > 1 {
				normalized.Delete("type")

				anyOfSchemas := make([]interface{}, 0, len(types))
				for _, t := range nonNullTypes {
					branch := schemas.NewOrderedMap()
					branch.Set("type", t)
					anyOfSchemas = append(anyOfSchemas, branch)
				}
				if hasNull {
					branch := schemas.NewOrderedMap()
					branch.Set("type", "null")
					anyOfSchemas = append(anyOfSchemas, branch)
				}

				normalized.Set("anyOf", anyOfSchemas)
				// enum at top level is incompatible with the anyOf rewrite.
				normalized.Delete("enum")
			} else if len(nonNullTypes) == 1 && hasNull {
				normalized.Set("type", []interface{}{nonNullTypes[0], "null"})
			} else if len(nonNullTypes) == 1 && !hasNull {
				normalized.Set("type", nonNullTypes[0])
			} else if len(nonNullTypes) == 0 && hasNull {
				normalized.Set("type", "null")
			}
		}
	}

	if propsVal, ok := normalized.Get("properties"); ok {
		if propsOM := asOrderedMap(propsVal); propsOM != nil {
			newProps := schemas.NewOrderedMapWithCapacity(propsOM.Len())
			propsOM.Range(func(key string, prop interface{}) bool {
				if propOM := asOrderedMap(prop); propOM != nil {
					newProps.Set(key, normalizeSchemaForGemini(propOM))
				} else {
					newProps.Set(key, prop)
				}
				return true
			})
			normalized.Set("properties", newProps)
		}
	}

	if itemsVal, ok := normalized.Get("items"); ok {
		if itemsOM := asOrderedMap(itemsVal); itemsOM != nil {
			normalized.Set("items", normalizeSchemaForGemini(itemsOM))
		}
	}

	for _, branch := range []string{"anyOf", "oneOf", "allOf"} {
		branchVal, ok := normalized.Get(branch)
		if !ok {
			continue
		}
		if slice, ok := branchVal.([]interface{}); ok {
			newSlice := make([]interface{}, 0, len(slice))
			for _, item := range slice {
				if itemOM := asOrderedMap(item); itemOM != nil {
					newSlice = append(newSlice, normalizeSchemaForGemini(itemOM))
				} else {
					newSlice = append(newSlice, item)
				}
			}
			normalized.Set(branch, newSlice)
		}
	}

	return normalized
}

// extractSchemaMapFromResponseFormat extracts the JSON schema document from OpenAI's
// response_format structure ({type, json_schema:{schema}}) and returns it as an
// order-preserving *OrderedMap suitable for direct assignment to
// GenerationConfig.ResponseJSONSchema.
//
// Three input arms (in priority order):
//  1. *OrderedMap / OrderedMap (preferred — already order-preserving end-to-end)
//  2. []byte / json.RawMessage / string — sliced via gjson.GetBytes for the
//     "json_schema.schema" subtree, then UnmarshalOrdered into *OrderedMap. This
//     lets us avoid a full document parse and preserve order from the wire.
//  3. map[string]interface{} (legacy) — order is already lost upstream; we wrap
//     via OrderedMapFromMap as a best-effort fallback.
func extractSchemaMapFromResponseFormat(responseFormat *interface{}) *schemas.OrderedMap {
	if responseFormat == nil || *responseFormat == nil {
		return nil
	}

	// Bytes arm — gjson sub-tree extraction, no full parse.
	if data, ok := bytesFromAny(*responseFormat); ok {
		if providerUtils.GetJSONField(data, "type").String() != "json_schema" {
			return nil
		}
		subtree := providerUtils.GetJSONSubtree(data, "json_schema.schema")
		if len(subtree) == 0 {
			return nil
		}
		schemaOM, err := providerUtils.UnmarshalOrdered(subtree)
		if err != nil || schemaOM == nil {
			return nil
		}
		return normalizeSchemaForGemini(schemaOM)
	}

	formatOM := asOrderedMap(*responseFormat)
	if formatOM == nil {
		return nil
	}

	if v, ok := formatOM.Get("type"); !ok {
		return nil
	} else if formatType, ok := v.(string); !ok || formatType != "json_schema" {
		return nil
	}

	jsonSchemaVal, ok := formatOM.Get("json_schema")
	if !ok {
		return nil
	}
	jsonSchemaOM := asOrderedMap(jsonSchemaVal)
	if jsonSchemaOM == nil {
		return nil
	}

	schemaVal, ok := jsonSchemaOM.Get("schema")
	if !ok {
		return nil
	}
	schemaOM := asOrderedMap(schemaVal)
	if schemaOM == nil {
		return nil
	}

	return normalizeSchemaForGemini(schemaOM)
}

// extractFunctionResponseOutput extracts the output text from a FunctionResponse.
// It first tries to extract the "output" field if present, otherwise marshals the entire response.
// Returns an empty string if the response is nil or extraction fails.
func extractFunctionResponseOutput(funcResp *FunctionResponse) string {
	if funcResp == nil || funcResp.Response == nil {
		return ""
	}

	// Try to extract "output" field first
	var respMap map[string]json.RawMessage
	if err := sonic.Unmarshal(funcResp.Response, &respMap); err == nil {
		if outputVal, ok := respMap["output"]; ok {
			var outputStr string
			if err := sonic.Unmarshal(outputVal, &outputStr); err == nil {
				return outputStr
			}
			return string(outputVal)
		}
	}

	// If no "output" key or unmarshal failed, return raw JSON
	return string(funcResp.Response)
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

	_, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, &client, req, resp)
	defer wait()
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

// tokenToBytes converts a token string to its UTF-8 byte representation as []int
func tokenToBytes(token string) []int {
	bytes := []byte(token)
	result := make([]int, len(bytes))
	for i, b := range bytes {
		result[i] = int(b)
	}
	return result
}

// ConvertGeminiLogprobsResultToBifrost converts a Gemini LogprobsResult to Bifrost BifrostLogProbs
func ConvertGeminiLogprobsResultToBifrost(result *LogprobsResult) *schemas.BifrostLogProbs {
	if result == nil || len(result.ChosenCandidates) == 0 {
		return nil
	}

	content := make([]schemas.ContentLogProb, len(result.ChosenCandidates))
	for i, chosen := range result.ChosenCandidates {
		content[i] = schemas.ContentLogProb{
			Token:   chosen.Token,
			LogProb: float64(chosen.LogProbability),
			Bytes:   tokenToBytes(chosen.Token),
		}
		if i < len(result.TopCandidates) && result.TopCandidates[i] != nil {
			for _, tc := range result.TopCandidates[i].Candidates {
				content[i].TopLogProbs = append(content[i].TopLogProbs, schemas.LogProb{
					Token:   tc.Token,
					LogProb: float64(tc.LogProbability),
					Bytes:   tokenToBytes(tc.Token),
				})
			}
		}
	}
	return &schemas.BifrostLogProbs{Content: content}
}
