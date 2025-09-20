package schemas

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Ptr creates a pointer to any value.
// This is a helper function for creating pointers to values.
func Ptr[T any](v T) *T {
	return &v
}

// ParseModelString extracts provider and model from a model string.
// For model strings like "anthropic/claude", it returns ("anthropic", "claude").
// For model strings like "claude", it returns ("", "claude").
func ParseModelString(model string, defaultProvider ModelProvider) (ModelProvider, string) {
	// Check if model contains a provider prefix (only split on first "/" to preserve model names with "/")
	if strings.Contains(model, "/") {
		parts := strings.SplitN(model, "/", 2)
		if len(parts) == 2 {
			extractedProvider := parts[0]
			extractedModel := parts[1]

			return ModelProvider(extractedProvider), extractedModel
		}
	}

	//TODO add model wise check for provider

	// No provider prefix found, return empty provider and the original model
	return defaultProvider, model
}

// MapFinishReasonToProvider maps OpenAI-compatible finish reasons to provider-specific format
func MapFinishReasonToProvider(finishReason string, targetProvider ModelProvider) string {
	switch targetProvider {
	case Anthropic:
		return mapFinishReasonToAnthropic(finishReason)
	default:
		// For OpenAI, Azure, and other providers, pass through as-is
		return finishReason
	}
}

// mapFinishReasonToAnthropic maps OpenAI finish reasons to Anthropic format
func mapFinishReasonToAnthropic(finishReason string) string {
	switch finishReason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		// Pass through other reasons like "pause_turn", "refusal", "stop_sequence", etc.
		return finishReason
	}
}

// ParameterSet represents a set of valid parameters using a map for O(1) lookup
type ParameterSet map[string]bool

// Marker to allowe all params
const AllowAllParams = "*"

// Pre-defined parameter groups (initialized once at startup)
var (

	allowAllParams = ParameterSet{
		AllowAllParams: true,
	}
	// Core parameters supported by most providers
	coreParams = ParameterSet{
		"max_tokens":  true,
		"temperature": true,
		"top_p":       true,
		"stream":      true,
		"tools":       true,
		"tool_choice": true,
	}

	// Extended parameter groups
	openAIParams = ParameterSet{
		"frequency_penalty":       true,
		"presence_penalty":        true,
		"n":                       true,
		"stop":                    true,
		"logprobs":                true,
		"top_logprobs":            true,
		"logit_bias":              true,
		"seed":                    true,
		"user":                    true,
		"response_format":         true,
		"parallel_tool_calls":     true,
		"max_completion_tokens":   true,
		"metadata":                true,
		"modalities":              true,
		"prediction":              true,
		"reasoning_effort":        true,
		"service_tier":            true,
		"store":                   true,
		"speed":                   true,
		"language":                true,
		"prompt":                  true,
		"include":                 true,
		"timestamp_granularities": true,
		"encoding_format":         true,
		"dimensions":              true,
		"stream_options":          true,
	}

	anthropicParams = ParameterSet{
		"stop_sequences": true,
		"system":         true,
		"metadata":       true,
		"mcp_servers":    true,
		"service_tier":   true,
		"thinking":       true,
		"top_k":          true,
	}

	cohereParams = ParameterSet{
		"frequency_penalty":  true,
		"presence_penalty":   true,
		"k":                  true,
		"p":                  true,
		"truncate":           true,
		"return_likelihoods": true,
		"logit_bias":         true,
		"stop_sequences":     true,
	}

	mistralParams = ParameterSet{
		"frequency_penalty":   true,
		"presence_penalty":    true,
		"safe_mode":           true,
		"n":                   true,
		"parallel_tool_calls": true,
		"prediction":          true,
		"prompt_mode":         true,
		"random_seed":         true,
		"response_format":     true,
		"safe_prompt":         true,
		"top_k":               true,
	}

	groqParams = ParameterSet{
		"n":                true,
		"reasoning_effort": true,
		"reasoning_format": true,
		"service_tier":     true,
		"stop":             true,
	}

	ollamaParams = ParameterSet{
		"num_ctx":          true,
		"num_gpu":          true,
		"num_thread":       true,
		"repeat_penalty":   true,
		"repeat_last_n":    true,
		"seed":             true,
		"tfs_z":            true,
		"mirostat":         true,
		"mirostat_tau":     true,
		"mirostat_eta":     true,
		"format":           true,
		"keep_alive":       true,
		"low_vram":         true,
		"main_gpu":         true,
		"min_p":            true,
		"num_batch":        true,
		"num_keep":         true,
		"num_predict":      true,
		"numa":             true,
		"penalize_newline": true,
		"raw":              true,
		"typical_p":        true,
		"use_mlock":        true,
		"use_mmap":         true,
		"vocab_only":       true,
	}

	openRouterParams = ParameterSet{
		"transforms": true,
		"models":     true,
		"route":      true,
		"provider":   true,
		"prediction": true,
		"top_a":      true,
		"min_p":      true,
	}

	vertexParams = ParameterSet{
		"task_type":            true,
		"title":                true,
		"autoTruncate":         true,
		"outputDimensionality": true,
	}

	bedrockParams = ParameterSet{
		"max_tokens_to_sample": true,
		"toolConfig":           true,
		"input_type":           true,
	}
)

// ParameterValidator provides fast parameter validation and filtering
type ParameterValidator struct {
	providerSchemas map[ModelProvider]ParameterSet
}

// NewParameterValidator creates a validator with pre-computed provider schemas
func NewParameterValidator() *ParameterValidator {
	return &ParameterValidator{
		providerSchemas: buildProviderSchemas(),
	}
}

// FilterParameters filters parameters for a provider using manual field checks (no reflection)
func (v *ParameterValidator) FilterParameters(provider ModelProvider, params *ModelParameters) *ModelParameters {
	if params == nil {
		return nil
	}

	schema, exists := v.providerSchemas[provider]
	if !exists {
		return params // Unknown provider, return all params
	}

	filtered := &ModelParameters{
		ExtraParams: make(map[string]interface{}),
	}

	// Return all params if the provider allows all params
	if schema[AllowAllParams] {
		return params
	}

	// Manual field filtering - fast and memory efficient
	v.filterStandardFields(schema, params, filtered)
	v.filterExtraParams(schema, params, filtered)

	// Return nil if no valid parameters
	if v.isEmpty(filtered) {
		return nil
	}

	return filtered
}

// filterStandardFields manually filters each field - faster than reflection
func (v *ParameterValidator) filterStandardFields(schema ParameterSet, source, target *ModelParameters) {
	if source.MaxTokens != nil && schema["max_tokens"] {
		target.MaxTokens = source.MaxTokens
	}
	if source.Temperature != nil && schema["temperature"] {
		target.Temperature = source.Temperature
	}
	if source.TopP != nil && schema["top_p"] {
		target.TopP = source.TopP
	}
	if source.TopK != nil && schema["top_k"] {
		target.TopK = source.TopK
	}
	if source.PresencePenalty != nil && schema["presence_penalty"] {
		target.PresencePenalty = source.PresencePenalty
	}
	if source.FrequencyPenalty != nil && schema["frequency_penalty"] {
		target.FrequencyPenalty = source.FrequencyPenalty
	}
	if source.StopSequences != nil && schema["stop_sequences"] {
		target.StopSequences = source.StopSequences
	}
	if source.Tools != nil && schema["tools"] {
		target.Tools = source.Tools
	}
	if source.ToolChoice != nil && schema["tool_choice"] {
		target.ToolChoice = source.ToolChoice
	}
	if source.User != nil && schema["user"] {
		target.User = source.User
	}
	if source.EncodingFormat != nil && schema["encoding_format"] {
		target.EncodingFormat = source.EncodingFormat
	}
	if source.Dimensions != nil && schema["dimensions"] {
		target.Dimensions = source.Dimensions
	}
	if source.ParallelToolCalls != nil && schema["parallel_tool_calls"] {
		target.ParallelToolCalls = source.ParallelToolCalls
	}
	if source.N != nil && schema["n"] {
		target.N = source.N
	}
	if source.Stop != nil && schema["stop"] {
		target.Stop = source.Stop
	}
	if source.MaxCompletionTokens != nil && schema["max_completion_tokens"] {
		target.MaxCompletionTokens = source.MaxCompletionTokens
	}
	if source.ReasoningEffort != nil && schema["reasoning_effort"] {
		target.ReasoningEffort = source.ReasoningEffort
	}
	if source.StreamOptions != nil && schema["stream_options"] {
		target.StreamOptions = source.StreamOptions
	}
	if source.Stream != nil && schema["stream"] {
		target.Stream = source.Stream
	}
	if source.LogProbs != nil && schema["logprobs"] {
		target.LogProbs = source.LogProbs
	}
	if source.TopLogProbs != nil && schema["top_logprobs"] {
		target.TopLogProbs = source.TopLogProbs
	}
	if source.ResponseFormat != nil && schema["response_format"] {
		target.ResponseFormat = source.ResponseFormat
	}
	if source.Seed != nil && schema["seed"] {
		target.Seed = source.Seed
	}
	if source.LogitBias != nil && schema["logit_bias"] {
		target.LogitBias = source.LogitBias
	}
}

// filterExtraParams filters the ExtraParams map
func (v *ParameterValidator) filterExtraParams(schema ParameterSet, source, target *ModelParameters) {
	if source.ExtraParams == nil {
		return
	}

	for key, value := range source.ExtraParams {
		if schema[key] {
			target.ExtraParams[key] = value
		}
	}
}

// isEmpty checks if all fields are nil/empty - manual check is faster
func (v *ParameterValidator) isEmpty(params *ModelParameters) bool {
	return params.MaxTokens == nil &&
		params.Temperature == nil &&
		params.TopP == nil &&
		params.TopK == nil &&
		params.PresencePenalty == nil &&
		params.FrequencyPenalty == nil &&
		params.StopSequences == nil &&
		params.Tools == nil &&
		params.ToolChoice == nil &&
		params.User == nil &&
		params.EncodingFormat == nil &&
		params.Dimensions == nil &&
		params.ParallelToolCalls == nil &&
		len(params.ExtraParams) == 0
}

// IsValidParameter checks if a parameter is valid for a provider (O(1) lookup)
func (v *ParameterValidator) IsValidParameter(provider ModelProvider, paramName string) bool {
	schema, exists := v.providerSchemas[provider]
	if !exists {
		return false
	}
	return schema[paramName]
}

// GetSupportedParameters returns all supported parameters for a provider
func (v *ParameterValidator) GetSupportedParameters(provider ModelProvider) []string {
	schema, exists := v.providerSchemas[provider]
	if !exists {
		return nil
	}

	params := make([]string, 0, len(schema))
	for param := range schema {
		params = append(params, param)
	}
	return params
}

// Helper function to merge parameter sets
func mergeParameterSets(sets ...ParameterSet) ParameterSet {
	totalSize := 0
	for _, set := range sets {
		totalSize += len(set)
	}

	result := make(ParameterSet, totalSize)
	for _, set := range sets {
		for param := range set {
			result[param] = true
		}
	}
	return result
}

// buildProviderSchemas creates provider-specific parameter schemas
func buildProviderSchemas() map[ModelProvider]ParameterSet {
	return map[ModelProvider]ParameterSet{
		OpenAI:     mergeParameterSets(coreParams, openAIParams),
		Azure:      mergeParameterSets(coreParams, openAIParams),
		Anthropic:  mergeParameterSets(coreParams, anthropicParams),
		Cohere:     mergeParameterSets(coreParams, cohereParams),
		Mistral:    mergeParameterSets(coreParams, mistralParams),
		Groq:       mergeParameterSets(coreParams, groqParams),
		Bedrock:    mergeParameterSets(coreParams, anthropicParams, mistralParams, bedrockParams),
		Vertex:     mergeParameterSets(coreParams, openAIParams, anthropicParams, vertexParams),
		Ollama:     mergeParameterSets(coreParams, ollamaParams),
		Cerebras:   mergeParameterSets(coreParams, openAIParams),
		SGL:        mergeParameterSets(coreParams, openAIParams),
		Parasail:   mergeParameterSets(coreParams, openAIParams),
		Gemini:     mergeParameterSets(coreParams, openAIParams, ParameterSet{"top_k": true, "stop_sequences": true}),
		OpenRouter: allowAllParams,
	}
}

// Global validator instance
var globalValidator = NewParameterValidator()

// Public API functions using the global validator
func ValidateAndFilterParamsForProvider(provider ModelProvider, params *ModelParameters) *ModelParameters {
	return globalValidator.FilterParameters(provider, params)
}

//* IMAGE UTILS *//

// dataURIRegex is a precompiled regex for matching data URI format patterns.
// It matches patterns like: data:image/png;base64,iVBORw0KGgo...
var dataURIRegex = regexp.MustCompile(`^data:([^;]+)(;base64)?,(.+)$`)

// base64Regex is a precompiled regex for matching base64 strings.
// It matches strings containing only valid base64 characters with optional padding.
var base64Regex = regexp.MustCompile(`^[A-Za-z0-9+/]*={0,2}$`)

// fileExtensionToMediaType maps common image file extensions to their corresponding media types.
// This map is used to infer media types from file extensions in URLs.
var fileExtensionToMediaType = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
	".bmp":  "image/bmp",
}

// SanitizeImageURL sanitizes and validates an image URL.
// It handles both data URLs and regular HTTP/HTTPS URLs.
// It also detects raw base64 image data and adds proper data URL headers.
func SanitizeImageURL(rawURL string) (string, error) {
	if rawURL == "" {
		return rawURL, fmt.Errorf("URL cannot be empty")
	}

	// Trim whitespace
	rawURL = strings.TrimSpace(rawURL)

	// Check if it's already a proper data URL
	if strings.HasPrefix(rawURL, "data:") {
		// Validate data URL format
		if !dataURIRegex.MatchString(rawURL) {
			return rawURL, fmt.Errorf("invalid data URL format")
		}
		return rawURL, nil
	}

	// Check if it looks like raw base64 image data
	if isLikelyBase64(rawURL) {
		// Detect the image type from the base64 data
		mediaType := detectImageTypeFromBase64(rawURL)

		// Remove any whitespace/newlines from base64 data
		cleanBase64 := strings.ReplaceAll(strings.ReplaceAll(rawURL, "\n", ""), " ", "")

		// Create proper data URL
		return fmt.Sprintf("data:%s;base64,%s", mediaType, cleanBase64), nil
	}

	// Parse as regular URL
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, fmt.Errorf("invalid URL format: %w", err)
	}

	// Validate scheme
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return rawURL, fmt.Errorf("URL must use http or https scheme")
	}

	// Validate host
	if parsedURL.Host == "" {
		return rawURL, fmt.Errorf("URL must have a valid host")
	}

	return parsedURL.String(), nil
}

// ExtractURLTypeInfo extracts type and media type information from a sanitized URL.
// For data URLs, it parses the media type and encoding.
// For regular URLs, it attempts to infer the media type from the file extension.
func ExtractURLTypeInfo(sanitizedURL string) URLTypeInfo {
	if strings.HasPrefix(sanitizedURL, "data:") {
		return extractDataURLInfo(sanitizedURL)
	}
	return extractRegularURLInfo(sanitizedURL)
}

// extractDataURLInfo extracts information from a data URL
func extractDataURLInfo(dataURL string) URLTypeInfo {
	// Parse data URL: data:[<mediatype>][;base64],<data>
	matches := dataURIRegex.FindStringSubmatch(dataURL)

	if len(matches) != 4 {
		return URLTypeInfo{Type: ImageContentTypeBase64}
	}

	mediaType := matches[1]
	isBase64 := matches[2] == ";base64"

	dataURLWithoutPrefix := dataURL
	if isBase64 {
		dataURLWithoutPrefix = dataURL[len("data:")+len(mediaType)+len(";base64,"):]
	}

	info := URLTypeInfo{
		MediaType:            &mediaType,
		DataURLWithoutPrefix: &dataURLWithoutPrefix,
	}

	if isBase64 {
		info.Type = ImageContentTypeBase64
	} else {
		info.Type = ImageContentTypeURL // Non-base64 data URL
	}

	return info
}

// extractRegularURLInfo extracts information from a regular HTTP/HTTPS URL
func extractRegularURLInfo(regularURL string) URLTypeInfo {
	info := URLTypeInfo{
		Type: ImageContentTypeURL,
	}

	// Try to infer media type from file extension
	parsedURL, err := url.Parse(regularURL)
	if err != nil {
		return info
	}

	path := strings.ToLower(parsedURL.Path)

	// Check for known file extensions using the map
	for ext, mediaType := range fileExtensionToMediaType {
		if strings.HasSuffix(path, ext) {
			info.MediaType = &mediaType
			break
		}
	}
	// For URLs without recognizable extensions, MediaType remains nil

	return info
}

// detectImageTypeFromBase64 detects the image type from base64 data by examining the header bytes
func detectImageTypeFromBase64(base64Data string) string {
	// Remove any whitespace or newlines
	cleanData := strings.ReplaceAll(strings.ReplaceAll(base64Data, "\n", ""), " ", "")

	// Check common image format signatures in base64
	switch {
	case strings.HasPrefix(cleanData, "/9j/") || strings.HasPrefix(cleanData, "/9k/"):
		// JPEG images typically start with /9j/ or /9k/ in base64 (FFD8 in hex)
		return "image/jpeg"
	case strings.HasPrefix(cleanData, "iVBORw0KGgo"):
		// PNG images start with iVBORw0KGgo in base64 (89504E470D0A1A0A in hex)
		return "image/png"
	case strings.HasPrefix(cleanData, "R0lGOD"):
		// GIF images start with R0lGOD in base64 (474946 in hex)
		return "image/gif"
	case strings.HasPrefix(cleanData, "Qk"):
		// BMP images start with Qk in base64 (424D in hex)
		return "image/bmp"
	case strings.HasPrefix(cleanData, "UklGR") && len(cleanData) >= 16 && cleanData[12:16] == "V0VC":
		// WebP images start with RIFF header (UklGR in base64) and have WEBP signature at offset 8-11 (V0VC in base64)
		return "image/webp"
	case strings.HasPrefix(cleanData, "PHN2Zy") || strings.HasPrefix(cleanData, "PD94bW"):
		// SVG images often start with <svg or <?xml in base64
		return "image/svg+xml"
	default:
		// Default to JPEG for unknown formats
		return "image/jpeg"
	}
}

// isLikelyBase64 checks if a string looks like base64 data
func isLikelyBase64(s string) bool {
	// Remove whitespace for checking
	cleanData := strings.ReplaceAll(strings.ReplaceAll(s, "\n", ""), " ", "")

	// Check if it contains only base64 characters using pre-compiled regex
	return base64Regex.MatchString(cleanData)
}
