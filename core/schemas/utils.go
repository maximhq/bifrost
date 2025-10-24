package schemas

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
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
	// No provider prefix found, return empty provider and the original model
	return defaultProvider, model
}

// Shared finish reason mappings for Anthropic-compatible providers (Anthropic, Bedrock)
var (
	// Maps provider-specific finish reasons to Bifrost format
	anthropicFinishReasonToBifrost = map[string]string{
		"end_turn":      "stop",
		"max_tokens":    "length",
		"stop_sequence": "stop",
		"tool_use":      "tool_calls",
		"content_filtered": "content_filter",
	}

	// Maps Bifrost finish reasons to provider-specific format
	bifrostToAnthropicFinishReason = map[string]string{
		"stop":       "end_turn", // canonical default
		"length":     "max_tokens",
		"tool_calls": "tool_use",
		"content_filter": "content_filtered",
	}
)

// ConvertAnthropicFinishReasonToBifrost converts provider finish reasons to Bifrost format
func ConvertAnthropicFinishReasonToBifrost(providerReason string) string {
	if bifrostReason, ok := anthropicFinishReasonToBifrost[providerReason]; ok {
		return bifrostReason
	}
	return providerReason
}

// ConvertBifrostFinishReasonToAnthropic converts Bifrost finish reasons to provider format
func ConvertBifrostFinishReasonToAnthropic(bifrostReason string) string {
	if providerReason, ok := bifrostToAnthropicFinishReason[bifrostReason]; ok {
		return providerReason
	}
	return bifrostReason
}

// MapProviderFinishReasonToBifrost maps provider finish reasons to bifrost finish reasons
func MapProviderFinishReasonToBifrost(finishReason string, targetProvider ModelProvider) string {
	switch targetProvider {
	case Anthropic, Bedrock:
		return ConvertAnthropicFinishReasonToBifrost(finishReason)
	default:
		return finishReason
	}
}

// MapBifrostFinishReasonToProvider maps bifrost finish reasons to provider finish reasons
func MapBifrostFinishReasonToProvider(bifrostReason string, targetProvider ModelProvider) string {
	switch targetProvider {
	case Anthropic, Bedrock:
		return ConvertBifrostFinishReasonToAnthropic(bifrostReason)
	default:
		return bifrostReason
	}
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

// ImageContentType represents the type of image content
type ImageContentType string

const (
	ImageContentTypeBase64 ImageContentType = "base64"
	ImageContentTypeURL    ImageContentType = "url"
)

// URLTypeInfo contains extracted information about a URL
type URLTypeInfo struct {
	Type                 ImageContentType
	MediaType            *string
	DataURLWithoutPrefix *string // URL without the prefix (eg data:image/png;base64,iVBORw0KGgo...)
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

// JsonifyInput converts an interface{} to a JSON string
func JsonifyInput(input interface{}) string {
	if input == nil {
		return "{}"
	}
	jsonBytes, err := json.Marshal(input)
	if err != nil {
		return "{}"
	}
	return string(jsonBytes)
}

//* SAFE EXTRACTION UTILITIES *//

// SafeExtractString safely extracts a string value from an interface{} with type checking
func SafeExtractString(value interface{}) (string, bool) {
	if value == nil {
		return "", false
	}
	switch v := value.(type) {
	case string:
		return v, true
	case *string:
		if v != nil {
			return *v, true
		}
		return "", false
	case json.Number:
		return string(v), true
	default:
		return "", false
	}
}

// SafeExtractInt safely extracts an int value from an interface{} with type checking
func SafeExtractInt(value interface{}) (int, bool) {
	if value == nil {
		return 0, false
	}
	switch v := value.(type) {
	case int:
		return v, true
	case int8:
		return int(v), true
	case int16:
		return int(v), true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case uint:
		return int(v), true
	case uint8:
		return int(v), true
	case uint16:
		return int(v), true
	case uint32:
		return int(v), true
	case uint64:
		return int(v), true
	case float32:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		if intVal, err := v.Int64(); err == nil {
			return int(intVal), true
		}
		return 0, false
	case string:
		if intVal, err := strconv.Atoi(v); err == nil {
			return intVal, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// SafeExtractFloat64 safely extracts a float64 value from an interface{} with type checking
func SafeExtractFloat64(value interface{}) (float64, bool) {
	if value == nil {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case json.Number:
		if floatVal, err := v.Float64(); err == nil {
			return floatVal, true
		}
		return 0, false
	case string:
		if floatVal, err := strconv.ParseFloat(v, 64); err == nil {
			return floatVal, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// SafeExtractBool safely extracts a bool value from an interface{} with type checking
func SafeExtractBool(value interface{}) (bool, bool) {
	if value == nil {
		return false, false
	}
	switch v := value.(type) {
	case bool:
		return v, true
	case *bool:
		if v != nil {
			return *v, true
		}
		return false, false
	case string:
		if boolVal, err := strconv.ParseBool(v); err == nil {
			return boolVal, true
		}
		return false, false
	case int:
		return v != 0, true
	case int8:
		return v != 0, true
	case int16:
		return v != 0, true
	case int32:
		return v != 0, true
	case int64:
		return v != 0, true
	case uint:
		return v != 0, true
	case uint8:
		return v != 0, true
	case uint16:
		return v != 0, true
	case uint32:
		return v != 0, true
	case uint64:
		return v != 0, true
	case float32:
		return v != 0, true
	case float64:
		return v != 0, true
	default:
		return false, false
	}
}

// SafeExtractStringSlice safely extracts a []string value from an interface{} with type checking
func SafeExtractStringSlice(value interface{}) ([]string, bool) {
	if value == nil {
		return nil, false
	}
	switch v := value.(type) {
	case []string:
		return v, true
	case []interface{}:
		var result []string
		for _, item := range v {
			if str, ok := SafeExtractString(item); ok {
				result = append(result, str)
			} else {
				return nil, false // If any item is not a string, fail
			}
		}
		return result, true
	case []*string:
		var result []string
		for _, item := range v {
			if item != nil {
				result = append(result, *item)
			}
		}
		return result, true
	default:
		return nil, false
	}
}

// SafeExtractStringPointer safely extracts a *string value from an interface{} with type checking
func SafeExtractStringPointer(value interface{}) (*string, bool) {
	if value == nil {
		return nil, false
	}
	switch v := value.(type) {
	case *string:
		return v, true
	case string:
		return &v, true
	case json.Number:
		str := string(v)
		return &str, true
	default:
		return nil, false
	}
}

// SafeExtractIntPointer safely extracts an *int value from an interface{} with type checking
func SafeExtractIntPointer(value interface{}) (*int, bool) {
	if value == nil {
		return nil, false
	}
	if intVal, ok := SafeExtractInt(value); ok {
		return &intVal, true
	}
	return nil, false
}

// SafeExtractFloat64Pointer safely extracts a *float64 value from an interface{} with type checking
func SafeExtractFloat64Pointer(value interface{}) (*float64, bool) {
	if value == nil {
		return nil, false
	}
	if floatVal, ok := SafeExtractFloat64(value); ok {
		return &floatVal, true
	}
	return nil, false
}

// SafeExtractBoolPointer safely extracts a *bool value from an interface{} with type checking
func SafeExtractBoolPointer(value interface{}) (*bool, bool) {
	if value == nil {
		return nil, false
	}
	if boolVal, ok := SafeExtractBool(value); ok {
		return &boolVal, true
	}
	return nil, false
}

// SafeExtractFromMap safely extracts a value from a map[string]interface{} with type checking
func SafeExtractFromMap(m map[string]interface{}, key string) (interface{}, bool) {
	if m == nil {
		return nil, false
	}
	value, exists := m[key]
	return value, exists
}

// paginationCursor represents the internal cursor structure for pagination.
type paginationCursor struct {
	Offset int    `json:"o"`
	LastID string `json:"l,omitempty"`
}

// encodePaginationCursor creates an opaque base64-encoded page token from cursor data.
// Returns empty string if offset is 0 or negative.
func encodePaginationCursor(offset int, lastID string) (string, error) {
	if offset <= 0 {
		return "", nil
	}

	cursor := paginationCursor{
		Offset: offset,
		LastID: lastID,
	}

	jsonData, err := sonic.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("failed to marshal pagination cursor: %w", err)
	}

	// Use URL-safe base64 encoding without padding for opaque token
	encoded := base64.RawURLEncoding.EncodeToString(jsonData)
	return encoded, nil
}

// decodePaginationCursor extracts cursor data from an opaque base64-encoded page token.
// Returns cursor with 0 offset for empty or invalid tokens.
func decodePaginationCursor(token string) paginationCursor {
	if token == "" {
		return paginationCursor{}
	}

	// Decode base64
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return paginationCursor{}
	}

	var cursor paginationCursor
	if err := sonic.Unmarshal(decoded, &cursor); err != nil {
		return paginationCursor{}
	}

	if cursor.Offset < 0 {
		return paginationCursor{}
	}

	return cursor
}

// validatePaginationCursor validates that the cursor matches the expected position in the data.
// Returns true if the cursor is valid, false otherwise.
func validatePaginationCursor(cursor paginationCursor, data []Model) bool {
	if cursor.LastID == "" {
		return true
	}

	if cursor.Offset <= 0 || cursor.Offset > len(data) {
		return false
	}

	prevIndex := cursor.Offset - 1
	if prevIndex >= 0 && prevIndex < len(data) {
		return data[prevIndex].ID == cursor.LastID
	}

	return true
}

// ApplyPagination applies offset-based pagination to a BifrostListModelsResponse.
// Uses opaque tokens with LastID validation to ensure cursor integrity.
// Returns the paginated response with properly set NextPageToken.
func ApplyPagination(response *BifrostListModelsResponse, pageSize int, pageToken string) *BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	totalItems := len(response.Data)

	if pageSize <= 0 {
		return response
	}

	cursor := decodePaginationCursor(pageToken)
	offset := cursor.Offset

	// Validate cursor integrity if LastID is present
	if cursor.LastID != "" && !validatePaginationCursor(cursor, response.Data) {
		// Invalid cursor: reset to beginning
		offset = 0
	}

	if offset >= totalItems {
		// Return empty page, no next token
		return &BifrostListModelsResponse{
			Data:          []Model{},
			ExtraFields:   response.ExtraFields,
			NextPageToken: "",
		}
	}

	endIndex := offset + pageSize
	if endIndex > totalItems {
		endIndex = totalItems
	}

	paginatedData := response.Data[offset:endIndex]

	paginatedResponse := &BifrostListModelsResponse{
		Data:        paginatedData,
		ExtraFields: response.ExtraFields,
	}

	if endIndex < totalItems {
		// Get the last item ID for cursor validation
		var lastID string
		if len(paginatedData) > 0 {
			lastID = paginatedData[len(paginatedData)-1].ID
		}

		nextToken, err := encodePaginationCursor(endIndex, lastID)
		if err == nil {
			paginatedResponse.NextPageToken = nextToken
		}
	} else {
		paginatedResponse.NextPageToken = ""
	}

	return paginatedResponse
}
