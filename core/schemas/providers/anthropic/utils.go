package anthropic

import (
	"encoding/json"
	"github.com/maximhq/bifrost/core/schemas"
)

// mapAnthropicFinishReasonToOpenAI maps Anthropic finish reasons to OpenAI-compatible ones
func MapAnthropicFinishReason(anthropicReason string) string {
	switch anthropicReason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	default:
		// Pass through Anthropic-specific reasons like "pause_turn", "refusal", etc.
		return anthropicReason
	}
}

// Helper function to convert interface{} to JSON string
func jsonifyInput(input interface{}) string {
	if input == nil {
		return "{}"
	}
	jsonBytes, err := json.Marshal(input)
	if err != nil {
		return "{}"
	}
	return string(jsonBytes)
}

// convertImageBlock converts a Bifrost image block to Anthropic format
// Uses the same pattern as the original buildAnthropicImageSourceMap function
func convertImageBlock(block schemas.ContentBlock) AnthropicContentBlock {
	imageBlock := AnthropicContentBlock{
		Type:   "image",
		Source: &AnthropicImageSource{},
	}

	// Use the centralized utility functions from schemas package
	sanitizedURL, _ := schemas.SanitizeImageURL(block.ImageURL.URL)
	urlTypeInfo := schemas.ExtractURLTypeInfo(sanitizedURL)

	formattedImgContent := &AnthropicImageContent{
		Type: urlTypeInfo.Type,
	}

	if urlTypeInfo.MediaType != nil {
		formattedImgContent.MediaType = *urlTypeInfo.MediaType
	}

	if urlTypeInfo.DataURLWithoutPrefix != nil {
		formattedImgContent.URL = *urlTypeInfo.DataURLWithoutPrefix
	} else {
		formattedImgContent.URL = sanitizedURL
	}

	// Convert to Anthropic source format
	if formattedImgContent.Type == schemas.ImageContentTypeURL {
		imageBlock.Source.Type = "url"
		imageBlock.Source.URL = &formattedImgContent.URL
	} else {
		if formattedImgContent.MediaType != "" {
			imageBlock.Source.MediaType = &formattedImgContent.MediaType
		}
		imageBlock.Source.Type = "base64"
		imageBlock.Source.Data = &formattedImgContent.URL // URL field contains base64 data string
	}

	return imageBlock
}	