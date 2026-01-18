package schemas

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Ptr creates a pointer to any value.
// This is a helper function for creating pointers to values.
func Ptr[T any](v T) *T {
	return &v
}

// GetRandomString generates a random alphanumeric string of the given length.
func GetRandomString(length int) string {
	if length <= 0 {
		return ""
	}
	randomSource := rand.New(rand.NewSource(time.Now().UnixNano()))
	letters := []rune("abcdef0123456789")
	b := make([]rune, length)
	for i := range b {
		b[i] = letters[randomSource.Intn(len(letters))]
	}
	return string(b)
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

// IsAllDigitsASCII checks if a string contains only ASCII digits (0-9).
func IsAllDigitsASCII(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ParseFallbacks parses a slice of strings into a slice of Fallback structs
func ParseFallbacks(fallbacks []string) []Fallback {
	if len(fallbacks) == 0 {
		return nil
	}
	parsedFallbacks := make([]Fallback, 0, len(fallbacks))
	for _, fallback := range fallbacks {
		if fallback == "" {
			continue
		}
		fallbackProvider, fallbackModel := ParseModelString(fallback, "")
		if fallbackProvider != "" && fallbackModel != "" {
			parsedFallbacks = append(parsedFallbacks, Fallback{Provider: fallbackProvider, Model: fallbackModel})
		}
	}
	return parsedFallbacks
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
	jsonString, err := MarshalString(input)
	if err != nil {
		return "{}"
	}
	return jsonString
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

// SafeExtractStringMap safely extracts a map[string]string from an interface{} with type checking.
// Handles both direct map[string]string and JSON-deserialized map[string]interface{} cases.
func SafeExtractStringMap(value interface{}) (map[string]string, bool) {
	if value == nil {
		return nil, false
	}
	switch v := value.(type) {
	case map[string]string:
		return v, true
	case map[string]interface{}:
		result := make(map[string]string, len(v))
		for key, val := range v {
			if str, ok := SafeExtractString(val); ok {
				result[key] = str
			} else {
				return nil, false
			}
		}
		return result, true
	default:
		return nil, false
	}
}

func SafeExtractOrderedMap(value interface{}) (OrderedMap, bool) {
	if value == nil {
		return OrderedMap{}, false
	}
	switch v := value.(type) {
	case map[string]interface{}:
		orderedMap := OrderedMap(v)
		return orderedMap, true
	case *map[string]interface{}:
		if v != nil {
			orderedMap := OrderedMap(*v)
			return orderedMap, true
		}
		return OrderedMap{}, false
	case OrderedMap:
		return v, true
	case *OrderedMap:
		if v != nil {
			return *v, true
		}
		return OrderedMap{}, false
	}
	return OrderedMap{}, false
}

// GET DEEP COPY UNTIL

func DeepCopy(in interface{}) interface{} {
	var out interface{}
	b, err := json.Marshal(in)
	if err != nil {
		return in
	}
	if err = json.Unmarshal(b, &out); err != nil {
		return in
	}
	return out
}

// DeepCopyChatMessage creates a deep copy of a ChatMessage
// to prevent shared data mutation between different plugin accumulators
func DeepCopyChatMessage(original ChatMessage) ChatMessage {
	copy := ChatMessage{}

	// Copy primitive fields
	if original.Name != nil {
		copyName := *original.Name
		copy.Name = &copyName
	}

	copy.Role = original.Role

	// Deep copy Content if present
	if original.Content != nil {
		copy.Content = &ChatMessageContent{}

		if original.Content.ContentStr != nil {
			copyContentStr := *original.Content.ContentStr
			copy.Content.ContentStr = &copyContentStr
		}

		if original.Content.ContentBlocks != nil {
			copy.Content.ContentBlocks = make([]ChatContentBlock, len(original.Content.ContentBlocks))
			for i, block := range original.Content.ContentBlocks {
				copy.Content.ContentBlocks[i] = deepCopyChatContentBlock(block)
			}
		}
	}

	// Deep copy ChatToolMessage if present
	if original.ChatToolMessage != nil {
		copy.ChatToolMessage = &ChatToolMessage{}
		if original.ChatToolMessage.ToolCallID != nil {
			copyToolCallID := *original.ChatToolMessage.ToolCallID
			copy.ChatToolMessage.ToolCallID = &copyToolCallID
		}
	}

	// Deep copy ChatAssistantMessage if present
	if original.ChatAssistantMessage != nil {
		copy.ChatAssistantMessage = &ChatAssistantMessage{}

		if original.ChatAssistantMessage.Refusal != nil {
			copyRefusal := *original.ChatAssistantMessage.Refusal
			copy.ChatAssistantMessage.Refusal = &copyRefusal
		}

		// Deep copy Annotations
		if original.ChatAssistantMessage.Annotations != nil {
			copy.ChatAssistantMessage.Annotations = make([]ChatAssistantMessageAnnotation, len(original.ChatAssistantMessage.Annotations))
			for i, annotation := range original.ChatAssistantMessage.Annotations {
				copyAnnotation := ChatAssistantMessageAnnotation{
					Type: annotation.Type,
					URLCitation: ChatAssistantMessageAnnotationCitation{
						StartIndex: annotation.URLCitation.StartIndex,
						EndIndex:   annotation.URLCitation.EndIndex,
						Title:      annotation.URLCitation.Title,
					},
				}
				if annotation.URLCitation.URL != nil {
					copyURL := *annotation.URLCitation.URL
					copyAnnotation.URLCitation.URL = &copyURL
				}
				if annotation.URLCitation.Sources != nil {
					copySources := *annotation.URLCitation.Sources
					copyAnnotation.URLCitation.Sources = &copySources
				}
				if annotation.URLCitation.Type != nil {
					copyType := *annotation.URLCitation.Type
					copyAnnotation.URLCitation.Type = &copyType
				}
				copy.ChatAssistantMessage.Annotations[i] = copyAnnotation
			}
		}

		// Deep copy ToolCalls
		if original.ChatAssistantMessage.ToolCalls != nil {
			copy.ChatAssistantMessage.ToolCalls = make([]ChatAssistantMessageToolCall, len(original.ChatAssistantMessage.ToolCalls))
			for i, toolCall := range original.ChatAssistantMessage.ToolCalls {
				copyToolCall := ChatAssistantMessageToolCall{
					Index: toolCall.Index,
					Function: ChatAssistantMessageToolCallFunction{
						Arguments: toolCall.Function.Arguments,
					},
				}
				if toolCall.Type != nil {
					copyType := *toolCall.Type
					copyToolCall.Type = &copyType
				}
				if toolCall.ID != nil {
					copyID := *toolCall.ID
					copyToolCall.ID = &copyID
				}
				if toolCall.Function.Name != nil {
					copyName := *toolCall.Function.Name
					copyToolCall.Function.Name = &copyName
				}
				copy.ChatAssistantMessage.ToolCalls[i] = copyToolCall
			}
		}
	}

	return copy
}

// deepCopyChatContentBlock creates a deep copy of a ChatContentBlock
func deepCopyChatContentBlock(original ChatContentBlock) ChatContentBlock {
	copy := ChatContentBlock{
		Type: original.Type,
	}

	if original.Text != nil {
		copyText := *original.Text
		copy.Text = &copyText
	}

	if original.Refusal != nil {
		copyRefusal := *original.Refusal
		copy.Refusal = &copyRefusal
	}

	if original.ImageURLStruct != nil {
		copyImage := ChatInputImage{
			URL: original.ImageURLStruct.URL,
		}
		if original.ImageURLStruct.Detail != nil {
			copyDetail := *original.ImageURLStruct.Detail
			copyImage.Detail = &copyDetail
		}
		copy.ImageURLStruct = &copyImage
	}

	if original.InputAudio != nil {
		copyAudio := ChatInputAudio{
			Data: original.InputAudio.Data,
		}
		if original.InputAudio.Format != nil {
			copyFormat := *original.InputAudio.Format
			copyAudio.Format = &copyFormat
		}
		copy.InputAudio = &copyAudio
	}

	if original.File != nil {
		copyFile := ChatInputFile{}
		if original.File.FileData != nil {
			copyFileData := *original.File.FileData
			copyFile.FileData = &copyFileData
		}
		if original.File.FileID != nil {
			copyFileID := *original.File.FileID
			copyFile.FileID = &copyFileID
		}
		if original.File.Filename != nil {
			copyFilename := *original.File.Filename
			copyFile.Filename = &copyFilename
		}
		copy.File = &copyFile
	}

	return copy
}

// DeepCopyChatTool creates a deep copy of a ChatTool
// to prevent shared data mutation between different plugin accumulators
func DeepCopyChatTool(original ChatTool) ChatTool {
	copyTool := ChatTool{
		Type: original.Type,
	}

	// Deep copy Function if present
	if original.Function != nil {
		copyTool.Function = &ChatToolFunction{
			Name: original.Function.Name,
		}

		if original.Function.Description != nil {
			copyDescription := *original.Function.Description
			copyTool.Function.Description = &copyDescription
		}

		if original.Function.Parameters != nil {
			copyParams := &ToolFunctionParameters{
				Type: original.Function.Parameters.Type,
			}

			if original.Function.Parameters.Description != nil {
				copyParamDesc := *original.Function.Parameters.Description
				copyParams.Description = &copyParamDesc
			}

			if original.Function.Parameters.Required != nil {
				copyParams.Required = make([]string, len(original.Function.Parameters.Required))
				copy(copyParams.Required, original.Function.Parameters.Required)
			}

			if original.Function.Parameters.Properties != nil {
				// Deep copy the map
				copyProps := make(map[string]interface{}, len(*original.Function.Parameters.Properties))
				for k, v := range *original.Function.Parameters.Properties {
					copyProps[k] = DeepCopy(v)
				}
				orderedProps := OrderedMap(copyProps)
				copyParams.Properties = &orderedProps
			}

			if original.Function.Parameters.Enum != nil {
				copyParams.Enum = make([]string, len(original.Function.Parameters.Enum))
				copy(copyParams.Enum, original.Function.Parameters.Enum)
			}

			if original.Function.Parameters.AdditionalProperties != nil {
				copyAdditionalProps := *original.Function.Parameters.AdditionalProperties
				copyParams.AdditionalProperties = &copyAdditionalProps
			}

			copyTool.Function.Parameters = copyParams
		}

		if original.Function.Strict != nil {
			copyStrict := *original.Function.Strict
			copyTool.Function.Strict = &copyStrict
		}
	}

	// Deep copy Custom if present
	if original.Custom != nil {
		copyTool.Custom = &ChatToolCustom{}

		if original.Custom.Format != nil {
			copyFormat := &ChatToolCustomFormat{
				Type: original.Custom.Format.Type,
			}

			if original.Custom.Format.Grammar != nil {
				copyGrammar := &ChatToolCustomGrammarFormat{
					Definition: original.Custom.Format.Grammar.Definition,
					Syntax:     original.Custom.Format.Grammar.Syntax,
				}
				copyFormat.Grammar = copyGrammar
			}

			copyTool.Custom.Format = copyFormat
		}
	}

	// Deep copy CacheControl if present
	if original.CacheControl != nil {
		copyCacheControl := &CacheControl{
			Type: original.CacheControl.Type,
		}

		if original.CacheControl.TTL != nil {
			copyTTL := *original.CacheControl.TTL
			copyCacheControl.TTL = &copyTTL
		}

		copyTool.CacheControl = copyCacheControl
	}

	return copyTool
}

// DeepCopyResponsesMessage creates a deep copy of a ResponsesMessage
// to prevent shared data mutation between different plugin accumulators
func DeepCopyResponsesMessage(original ResponsesMessage) ResponsesMessage {
	copy := ResponsesMessage{}

	if original.ID != nil {
		copyID := *original.ID
		copy.ID = &copyID
	}

	if original.Type != nil {
		copyType := *original.Type
		copy.Type = &copyType
	}

	if original.Status != nil {
		copyStatus := *original.Status
		copy.Status = &copyStatus
	}

	if original.Role != nil {
		copyRole := *original.Role
		copy.Role = &copyRole
	}

	if original.Content != nil {
		copy.Content = &ResponsesMessageContent{}

		if original.Content.ContentStr != nil {
			copyContentStr := *original.Content.ContentStr
			copy.Content.ContentStr = &copyContentStr
		}

		if original.Content.ContentBlocks != nil {
			copy.Content.ContentBlocks = make([]ResponsesMessageContentBlock, len(original.Content.ContentBlocks))
			for i, block := range original.Content.ContentBlocks {
				copy.Content.ContentBlocks[i] = deepCopyResponsesMessageContentBlock(block)
			}
		}
	}

	// Deep copy ResponsesToolMessage if present (this is complex, using the existing pattern from streaming/responses.go)
	if original.ResponsesToolMessage != nil {
		copy.ResponsesToolMessage = &ResponsesToolMessage{}

		// Deep copy primitive fields
		if original.ResponsesToolMessage.CallID != nil {
			copyCallID := *original.ResponsesToolMessage.CallID
			copy.ResponsesToolMessage.CallID = &copyCallID
		}

		if original.ResponsesToolMessage.Name != nil {
			copyName := *original.ResponsesToolMessage.Name
			copy.ResponsesToolMessage.Name = &copyName
		}

		if original.ResponsesToolMessage.Arguments != nil {
			copyArguments := *original.ResponsesToolMessage.Arguments
			copy.ResponsesToolMessage.Arguments = &copyArguments
		}

		if original.ResponsesToolMessage.Error != nil {
			copyError := *original.ResponsesToolMessage.Error
			copy.ResponsesToolMessage.Error = &copyError
		}

		// Deep copy Output
		if original.ResponsesToolMessage.Output != nil {
			copy.ResponsesToolMessage.Output = &ResponsesToolMessageOutputStruct{}

			if original.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
				copyStr := *original.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
				copy.ResponsesToolMessage.Output.ResponsesToolCallOutputStr = &copyStr
			}

			if original.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks != nil {
				copy.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks = make([]ResponsesMessageContentBlock, len(original.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks))
				for i, block := range original.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks {
					copy.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks[i] = deepCopyResponsesMessageContentBlock(block)
				}
			}

			if original.ResponsesToolMessage.Output.ResponsesComputerToolCallOutput != nil {
				copyOutput := *original.ResponsesToolMessage.Output.ResponsesComputerToolCallOutput
				copy.ResponsesToolMessage.Output.ResponsesComputerToolCallOutput = &copyOutput
			}
		}

		// Deep copy Action
		if original.ResponsesToolMessage.Action != nil {
			copy.ResponsesToolMessage.Action = &ResponsesToolMessageActionStruct{}

			if original.ResponsesToolMessage.Action.ResponsesComputerToolCallAction != nil {
				copyAction := *original.ResponsesToolMessage.Action.ResponsesComputerToolCallAction
				// Deep copy Path slice
				if copyAction.Path != nil {
					copyAction.Path = make([]ResponsesComputerToolCallActionPath, len(copyAction.Path))
					for i, path := range original.ResponsesToolMessage.Action.ResponsesComputerToolCallAction.Path {
						copyAction.Path[i] = path // struct copy is fine for simple structs
					}
				}
				// Deep copy Keys slice
				if copyAction.Keys != nil {
					copyAction.Keys = make([]string, len(copyAction.Keys))
					for i, key := range original.ResponsesToolMessage.Action.ResponsesComputerToolCallAction.Keys {
						copyAction.Keys[i] = key
					}
				}
				copy.ResponsesToolMessage.Action.ResponsesComputerToolCallAction = &copyAction
			}

			if original.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction != nil {
				copyAction := *original.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
				copy.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction = &copyAction
			}

			if original.ResponsesToolMessage.Action.ResponsesLocalShellToolCallAction != nil {
				copyAction := *original.ResponsesToolMessage.Action.ResponsesLocalShellToolCallAction
				copy.ResponsesToolMessage.Action.ResponsesLocalShellToolCallAction = &copyAction
			}

			if original.ResponsesToolMessage.Action.ResponsesMCPApprovalRequestAction != nil {
				copyAction := *original.ResponsesToolMessage.Action.ResponsesMCPApprovalRequestAction
				copy.ResponsesToolMessage.Action.ResponsesMCPApprovalRequestAction = &copyAction
			}
		}

		// Deep copy embedded tool call structs (simplified version - add more as needed)
		if original.ResponsesToolMessage.ResponsesFileSearchToolCall != nil {
			copyToolCall := *original.ResponsesToolMessage.ResponsesFileSearchToolCall
			// Deep copy Queries slice
			if copyToolCall.Queries != nil {
				copyToolCall.Queries = make([]string, len(copyToolCall.Queries))
				for i, query := range original.ResponsesToolMessage.ResponsesFileSearchToolCall.Queries {
					copyToolCall.Queries[i] = query
				}
			}
			copy.ResponsesToolMessage.ResponsesFileSearchToolCall = &copyToolCall
		}

		// Add other embedded tool calls as needed...
	}

	// Deep copy ResponsesReasoning if present
	if original.ResponsesReasoning != nil {
		copyReasoning := *original.ResponsesReasoning
		copy.ResponsesReasoning = &copyReasoning
	}

	return copy
}

// deepCopyResponsesMessageContentBlock creates a deep copy of a ResponsesMessageContentBlock
func deepCopyResponsesMessageContentBlock(original ResponsesMessageContentBlock) ResponsesMessageContentBlock {
	copy := ResponsesMessageContentBlock{
		Type: original.Type,
	}

	// Copy FileID if present
	if original.FileID != nil {
		copyFileID := *original.FileID
		copy.FileID = &copyFileID
	}

	if original.Text != nil {
		copyText := *original.Text
		copy.Text = &copyText
	}

	// Deep copy ResponsesInputMessageContentBlockImage
	if original.ResponsesInputMessageContentBlockImage != nil {
		copyImage := &ResponsesInputMessageContentBlockImage{}
		if original.ResponsesInputMessageContentBlockImage.ImageURL != nil {
			copyImageURL := *original.ResponsesInputMessageContentBlockImage.ImageURL
			copyImage.ImageURL = &copyImageURL
		}
		if original.ResponsesInputMessageContentBlockImage.Detail != nil {
			copyDetail := *original.ResponsesInputMessageContentBlockImage.Detail
			copyImage.Detail = &copyDetail
		}
		copy.ResponsesInputMessageContentBlockImage = copyImage
	}

	// Deep copy ResponsesInputMessageContentBlockFile
	if original.ResponsesInputMessageContentBlockFile != nil {
		copyFile := &ResponsesInputMessageContentBlockFile{}
		if original.ResponsesInputMessageContentBlockFile.FileData != nil {
			copyFileData := *original.ResponsesInputMessageContentBlockFile.FileData
			copyFile.FileData = &copyFileData
		}
		if original.ResponsesInputMessageContentBlockFile.FileURL != nil {
			copyFileURL := *original.ResponsesInputMessageContentBlockFile.FileURL
			copyFile.FileURL = &copyFileURL
		}
		if original.ResponsesInputMessageContentBlockFile.Filename != nil {
			copyFilename := *original.ResponsesInputMessageContentBlockFile.Filename
			copyFile.Filename = &copyFilename
		}
		copy.ResponsesInputMessageContentBlockFile = copyFile
	}

	// Deep copy Audio
	if original.Audio != nil {
		copyAudio := &ResponsesInputMessageContentBlockAudio{
			Format: original.Audio.Format,
			Data:   original.Audio.Data,
		}
		copy.Audio = copyAudio
	}

	// Deep copy ResponsesOutputMessageContentText
	if original.ResponsesOutputMessageContentText != nil {
		copyText := &ResponsesOutputMessageContentText{}

		// Deep copy Annotations
		if original.ResponsesOutputMessageContentText.Annotations != nil {
			copyText.Annotations = make([]ResponsesOutputMessageContentTextAnnotation, len(original.ResponsesOutputMessageContentText.Annotations))
			for i, annotation := range original.ResponsesOutputMessageContentText.Annotations {
				copyAnnotation := ResponsesOutputMessageContentTextAnnotation{
					Type: annotation.Type,
				}
				if annotation.Index != nil {
					copyIndex := *annotation.Index
					copyAnnotation.Index = &copyIndex
				}
				if annotation.FileID != nil {
					copyFileID := *annotation.FileID
					copyAnnotation.FileID = &copyFileID
				}
				if annotation.Text != nil {
					copyText := *annotation.Text
					copyAnnotation.Text = &copyText
				}
				if annotation.StartIndex != nil {
					copyStartIndex := *annotation.StartIndex
					copyAnnotation.StartIndex = &copyStartIndex
				}
				if annotation.EndIndex != nil {
					copyEndIndex := *annotation.EndIndex
					copyAnnotation.EndIndex = &copyEndIndex
				}
				if annotation.Filename != nil {
					copyFilename := *annotation.Filename
					copyAnnotation.Filename = &copyFilename
				}
				if annotation.Title != nil {
					copyTitle := *annotation.Title
					copyAnnotation.Title = &copyTitle
				}
				if annotation.URL != nil {
					copyURL := *annotation.URL
					copyAnnotation.URL = &copyURL
				}
				if annotation.ContainerID != nil {
					copyContainerID := *annotation.ContainerID
					copyAnnotation.ContainerID = &copyContainerID
				}
				copyText.Annotations[i] = copyAnnotation
			}
		}

		// Deep copy LogProbs
		if original.ResponsesOutputMessageContentText.LogProbs != nil {
			copyText.LogProbs = make([]ResponsesOutputMessageContentTextLogProb, len(original.ResponsesOutputMessageContentText.LogProbs))
			for i, logProb := range original.ResponsesOutputMessageContentText.LogProbs {
				copyLogProb := ResponsesOutputMessageContentTextLogProb{
					LogProb: logProb.LogProb,
					Token:   logProb.Token,
				}
				// Deep copy Bytes slice
				if logProb.Bytes != nil {
					copyLogProb.Bytes = make([]int, len(logProb.Bytes))
					for k, b := range logProb.Bytes {
						copyLogProb.Bytes[k] = b
					}
				}
				// Deep copy TopLogProbs slice
				if logProb.TopLogProbs != nil {
					copyLogProb.TopLogProbs = make([]LogProb, len(logProb.TopLogProbs))
					for j, topLogProb := range logProb.TopLogProbs {
						copyTopLogProb := LogProb{
							LogProb: topLogProb.LogProb,
							Token:   topLogProb.Token,
						}
						// Deep copy Bytes slice in TopLogProb
						if topLogProb.Bytes != nil {
							copyTopLogProb.Bytes = make([]int, len(topLogProb.Bytes))
							for k, b := range topLogProb.Bytes {
								copyTopLogProb.Bytes[k] = b
							}
						}
						copyLogProb.TopLogProbs[j] = copyTopLogProb
					}
				}
				copyText.LogProbs[i] = copyLogProb
			}
		}

		copy.ResponsesOutputMessageContentText = copyText
	}

	// Deep copy ResponsesOutputMessageContentRefusal
	if original.ResponsesOutputMessageContentRefusal != nil {
		copyRefusal := &ResponsesOutputMessageContentRefusal{
			Refusal: original.ResponsesOutputMessageContentRefusal.Refusal,
		}
		copy.ResponsesOutputMessageContentRefusal = copyRefusal
	}

	return copy
}

// IsNovaModel checks if the model is a Nova model.
func IsNovaModel(model string) bool {
	return strings.Contains(model, "nova")
}

// IsAnthropicModel checks if the model is an Anthropic model.
func IsAnthropicModel(model string) bool {
	return strings.Contains(model, "anthropic.") || strings.Contains(model, "claude")
}

// IsMistralModel checks if the model is a Mistral or Codestral model.
func IsMistralModel(model string) bool {
	return strings.Contains(model, "mistral") || strings.Contains(model, "codestral")
}

func IsGeminiModel(model string) bool {
	return strings.Contains(model, "gemini")
}

// IsImagenModel checks if the model is an Imagen model.
func IsImagenModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "imagen")
}

// List of grok reasoning models
var grokReasoningModels = []string{
	"grok-3",
	"grok-3-mini",
	"grok-4",
	"grok-4-fast-reasoning",
	"grok-4-1-fast-reasoning",
	"grok-code-fast-1",
}

// IsGrokReasoningModel checks if the given model is a grok reasoning model
func IsGrokReasoningModel(model string) bool {
	// Check if the model matches any of the reasoning models
	for _, reasoningModel := range grokReasoningModels {
		if strings.Contains(model, reasoningModel) {
			// Make sure it's not a non-reasoning variant. Safety check for variants
			if strings.Contains(model, "non-reasoning") {
				return false
			}
			return true
		}
	}
	return false
}

// Precompiled regexes for different kinds of version suffixes.
var (
	// Anthropic-style date: 20250514
	anthropicDateRe = regexp.MustCompile(`^\d{8}$`)

	// OpenAI-style date: 2024-09-12
	openAIDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

	// Generic tagged versions:
	//   v1, v1.2, v1.2.3, rc1, alpha, beta, preview, canary, experimental, etc.
	//
	// NOTE: we intentionally require 'v' for numeric semver-ish versions so that
	// things like "-4" or "-4.5" are NOT treated as version tags.
	taggedVersionRe = regexp.MustCompile(
		`^(?:v\d+(?:\.\d+){0,2}|rc\d+|alpha|beta|preview|canary|experimental)$`,
	)
)

// SplitModelAndVersion splits a model id into (base, versionSuffix).
// If no known version suffix is found, versionSuffix will be empty and
// base will be the original id.
//
// Examples:
//
//	"claude-sonnet-4"                 -> ("claude-sonnet-4", "")
//	"claude-sonnet-4-20250514"        -> ("claude-sonnet-4", "20250514")
//	"gpt-4.1-2024-09-12"              -> ("gpt-4.1", "2024-09-12")
//	"gpt-4.1-mini-2024-09-12"         -> ("gpt-4.1-mini", "2024-09-12")
//	"some-model-v2"                   -> ("some-model", "v2")
//	"text-embedding-3-large-beta"     -> ("text-embedding-3-large", "beta")
//	"claude-sonnet-4.5"               -> ("claude-sonnet-4.5", "")
func SplitModelAndVersion(id string) (base, version string) {
	if id == "" {
		return "", ""
	}

	parts := strings.Split(id, "-")
	n := len(parts)
	if n == 0 {
		return "", ""
	}

	// 1. Try OpenAI-style date: last 3 parts, e.g. "2024-09-12".
	if n >= 3 {
		last3 := strings.Join(parts[n-3:], "-")
		if openAIDateRe.MatchString(last3) {
			base := strings.Join(parts[:n-3], "-")
			return base, last3
		}
	}

	// 2. Try Anthropic-style date (20250514) or tagged versions (v1, beta, etc.) in last part.
	if n >= 2 {
		last := parts[n-1]
		if anthropicDateRe.MatchString(last) || taggedVersionRe.MatchString(last) {
			base := strings.Join(parts[:n-1], "-")
			return base, last
		}
	}

	// 3. No recognized version suffix.
	return id, ""
}

// BaseModelName returns the model id with any recognized version suffix stripped.
//
// This is your "model name without version".
func BaseModelName(id string) string {
	base, _ := SplitModelAndVersion(id)
	return base
}

// SameBaseModel reports whether two model ids refer to the same base model,
// ignoring any recognized version suffixes.
//
// This works even if both sides are versioned, or both unversioned.
func SameBaseModel(a, b string) bool {
	// Fast path: exact match.
	if a == b {
		return true
	}

	// Compare normalized base names.
	return BaseModelName(a) == BaseModelName(b)
}
