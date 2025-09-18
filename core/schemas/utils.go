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

// DefaultParameters defines the common parameters that most providers support
var DefaultParameters = map[string]bool{
	"max_tokens":  true,
	"temperature": true,
	"top_p":       true,
	"stream":      true,
	"tools":       true,
	"tool_choice": true,
}

// ProviderParameterSchema defines which parameters are valid for each provider
type ProviderParameterSchema struct {
	ValidParams map[string]bool // Parameters that are supported by this provider
}

// ParameterValidator validates and filters parameters for specific providers
type ParameterValidator struct {
	schemas map[ModelProvider]ProviderParameterSchema
}

// NewParameterValidator creates a new validator with provider schemas
func NewParameterValidator() *ParameterValidator {
	return &ParameterValidator{
		schemas: buildProviderSchemas(),
	}
}

// ValidateAndFilterParams filters out invalid parameters for the target provider
func (v *ParameterValidator) ValidateAndFilterParams(
	provider ModelProvider,
	params *ModelParameters,
) *ModelParameters {
	if params == nil {
		return nil
	}

	schema, exists := v.schemas[provider]
	if !exists {
		// Unknown provider, return all params (fallback behavior)
		return params
	}

	filteredParams := &ModelParameters{
		ExtraParams: make(map[string]interface{}),
	}

	// Filter standard parameters
	if params.MaxTokens != nil && schema.ValidParams["max_tokens"] {
		filteredParams.MaxTokens = params.MaxTokens
	}

	if params.Temperature != nil && schema.ValidParams["temperature"] {
		filteredParams.Temperature = params.Temperature
	}

	if params.TopP != nil && schema.ValidParams["top_p"] {
		filteredParams.TopP = params.TopP
	}

	if params.TopK != nil && schema.ValidParams["top_k"] {
		filteredParams.TopK = params.TopK
	}

	if params.PresencePenalty != nil && schema.ValidParams["presence_penalty"] {
		filteredParams.PresencePenalty = params.PresencePenalty
	}

	if params.FrequencyPenalty != nil && schema.ValidParams["frequency_penalty"] {
		filteredParams.FrequencyPenalty = params.FrequencyPenalty
	}

	if params.StopSequences != nil && schema.ValidParams["stop_sequences"] {
		filteredParams.StopSequences = params.StopSequences
	}

	if params.Tools != nil && schema.ValidParams["tools"] {
		filteredParams.Tools = params.Tools
	}

	if params.ToolChoice != nil && schema.ValidParams["tool_choice"] {
		filteredParams.ToolChoice = params.ToolChoice
	}

	if params.User != nil && schema.ValidParams["user"] {
		filteredParams.User = params.User
	}

	if params.EncodingFormat != nil && schema.ValidParams["encoding_format"] {
		filteredParams.EncodingFormat = params.EncodingFormat
	}

	if params.Dimensions != nil && schema.ValidParams["dimensions"] {
		filteredParams.Dimensions = params.Dimensions
	}

	// Parallel tool calls
	if params.ParallelToolCalls != nil && schema.ValidParams["parallel_tool_calls"] {
		filteredParams.ParallelToolCalls = params.ParallelToolCalls
	}

	// Filter extra parameters
	for key, value := range params.ExtraParams {
		if schema.ValidParams[key] {
			filteredParams.ExtraParams[key] = value
		}
	}

	// Check if all standard pointer fields are nil and ExtraParams is empty
	if hasNoValidFields(filteredParams) && len(filteredParams.ExtraParams) == 0 {
		return nil
	}

	return filteredParams
}

// hasNoValidFields checks if all standard pointer fields in ModelParameters are nil
func hasNoValidFields(params *ModelParameters) bool {
	return params.ToolChoice == nil &&
		params.Tools == nil &&
		params.Temperature == nil &&
		params.TopP == nil &&
		params.TopK == nil &&
		params.MaxTokens == nil &&
		params.StopSequences == nil &&
		params.PresencePenalty == nil &&
		params.FrequencyPenalty == nil &&
		params.ParallelToolCalls == nil &&
		params.EncodingFormat == nil &&
		params.Dimensions == nil &&
		params.User == nil
}

// buildProviderSchemas defines which parameters are valid for each provider
func buildProviderSchemas() map[ModelProvider]ProviderParameterSchema {
	// Define parameter groups to avoid repetition
	openAIParams := map[string]bool{
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

	anthropicParams := map[string]bool{
		"stop_sequences": true,
		"system":         true,
		"metadata":       true,
		"mcp_servers":    true,
		"service_tier":   true,
		"thinking":       true,
		"top_k":          true,
	}

	cohereParams := map[string]bool{
		"frequency_penalty":  true,
		"presence_penalty":   true,
		"k":                  true,
		"p":                  true,
		"truncate":           true,
		"return_likelihoods": true,
		"logit_bias":         true,
		"stop_sequences":     true,
	}

	mistralParams := map[string]bool{
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

	groqParams := map[string]bool{
		"n":                true,
		"reasoning_effort": true,
		"reasoning_format": true,
		"service_tier":     true,
		"stop":             true,
	}

	ollamaParams := map[string]bool{
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

	// Vertex supports both OpenAI and Anthropic models, plus its own specific parameters
	vertexParams := mergeWithDefaults(openAIParams)
	// Add Anthropic-specific parameters for Claude models on Vertex
	for k, v := range anthropicParams {
		vertexParams[k] = v
	}
	// Add Vertex-specific parameters
	vertexSpecificParams := map[string]bool{
		"task_type":            true, // For embeddings
		"title":                true, // For embeddings
		"autoTruncate":         true, // For embeddings
		"outputDimensionality": true, // For embeddings (maps to dimensions)
	}
	for k, v := range vertexSpecificParams {
		vertexParams[k] = v
	}

	// Bedrock supports both Anthropic and Mistral models, plus its own specific parameters
	bedrockParams := mergeWithDefaults(anthropicParams)
	// Add Mistral-specific parameters for Mistral models on Bedrock
	for k, v := range mistralParams {
		bedrockParams[k] = v
	}
	// Add Bedrock-specific parameters
	bedrockSpecificParams := map[string]bool{
		"max_tokens_to_sample": true, // Anthropic models use this instead of max_tokens
		"toolConfig":           true, // Bedrock-specific tool configuration
		"input_type":           true, // For Cohere embeddings
	}
	for k, v := range bedrockSpecificParams {
		bedrockParams[k] = v
	}

	geminiParams := mergeWithDefaults(openAIParams)
	geminiParams["top_k"] = true
	geminiParams["stop_sequences"] = true

	openRouterSpecificParams := map[string]bool{
		"transforms": true,
		"models":     true,
		"route":      true,
		"provider":   true,
		"prediction": true, // Reduce latency by providing the model with a predicted output
		"top_a":      true, // Range: [0, 1]
		"min_p":      true, // Range: [0, 1]
	}
	openRouterParams := mergeWithDefaults(openAIParams)
	for k, v := range openRouterSpecificParams {
		openRouterParams[k] = v
	}

	return map[ModelProvider]ProviderParameterSchema{
		OpenAI:     {ValidParams: mergeWithDefaults(openAIParams)},
		Azure:      {ValidParams: mergeWithDefaults(openAIParams)},
		Anthropic:  {ValidParams: mergeWithDefaults(anthropicParams)},
		Cohere:     {ValidParams: mergeWithDefaults(cohereParams)},
		Mistral:    {ValidParams: mergeWithDefaults(mistralParams)},
		Groq:       {ValidParams: mergeWithDefaults(groqParams)},
		Bedrock:    {ValidParams: bedrockParams},
		Vertex:     {ValidParams: vertexParams},
		Ollama:     {ValidParams: mergeWithDefaults(ollamaParams)},
		Cerebras:   {ValidParams: mergeWithDefaults(openAIParams)},
		SGL:        {ValidParams: mergeWithDefaults(openAIParams)},
		Parasail:   {ValidParams: mergeWithDefaults(openAIParams)},
		Gemini:     {ValidParams: geminiParams},
		OpenRouter: {ValidParams: openRouterParams},
	}
}

// mergeWithDefaults merges provider-specific parameters with default parameters
func mergeWithDefaults(providerParams map[string]bool) map[string]bool {
	result := make(map[string]bool, len(DefaultParameters)+len(providerParams))

	// Copy default parameters
	for k, v := range DefaultParameters {
		result[k] = v
	}

	// Add provider-specific parameters
	for k, v := range providerParams {
		result[k] = v
	}

	return result
}

// Global parameter validator instance
var globalParamValidator = NewParameterValidator()

// SetGlobalParameterValidator sets the shared ParameterValidator instance.
// It's primarily intended for test setup or one-time overrides.
// Note: calling this at runtime from multiple goroutines is not safe for concurrent use.
func SetGlobalParameterValidator(v *ParameterValidator) {
	if v != nil {
		globalParamValidator = v
	}
}

// ValidateAndFilterParamsForProvider is a convenience function that uses the global validator
// to filter parameters for a specific provider. This is the main function integrations should use.
func ValidateAndFilterParamsForProvider(
	provider ModelProvider,
	params *ModelParameters,
) *ModelParameters {
	return globalParamValidator.ValidateAndFilterParams(provider, params)
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
