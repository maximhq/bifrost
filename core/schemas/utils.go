package schemas

import (
	"encoding/json"
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

// Helper function to convert interface{} to JSON string
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
