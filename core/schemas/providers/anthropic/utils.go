package anthropic

import (
	"github.com/maximhq/bifrost/core/schemas"
)

var (
	finishReasonMap = map[string]string{
		"end_turn":      "stop",
		"max_tokens":    "length",
		"stop_sequence": "stop",
		"tool_use":      "tool_calls",
	}

	// reverseFinishReasonMap = func() map[string]string {
	// 	m := make(map[string]string, len(finishReasonMap))
	// 	for k, v := range finishReasonMap {
	// 		m[v] = k
	// 	}
	// 	return m
	// }()

	reverseFinishReasonMap = map[string]string{
		"stop":       "end_turn", // canonical default
		"length":     "max_tokens",
		"tool_calls": "tool_use",
	}
)

// MapAnthropicFinishReasonToOpenAI maps Anthropic finish reasons to OpenAI-compatible ones
func MapAnthropicFinishReasonToBifrost(anthropicReason string) string {
	if _, ok := finishReasonMap[anthropicReason]; ok {
		return finishReasonMap[anthropicReason]
	}
	return anthropicReason
}

// MapBifrostFinishReasonToAnthropic maps Bifrost finish reasons back to Anthropic
func MapBifrostFinishReasonToAnthropic(bifrostReason string) string {
	if mapped, ok := reverseFinishReasonMap[bifrostReason]; ok {
		return mapped
	}
	return bifrostReason
}

// ConvertToAnthropicImageBlock converts a Bifrost image block to Anthropic format
// Uses the same pattern as the original buildAnthropicImageSourceMap function
func ConvertToAnthropicImageBlock(block schemas.ChatContentBlock) AnthropicContentBlock {
	imageBlock := AnthropicContentBlock{
		Type:   "image",
		Source: &AnthropicImageSource{},
	}

	if block.ImageURLStruct == nil {
		return imageBlock
	}

	// Use the centralized utility functions from schemas package
	sanitizedURL, err := schemas.SanitizeImageURL(block.ImageURLStruct.URL)
	if err != nil {
		// Best-effort: treat as a regular URL without sanitization
		imageBlock.Source.Type = "url"
		imageBlock.Source.URL = &block.ImageURLStruct.URL
		return imageBlock
	}
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
		// Use the base64 data without the data URL prefix
		if urlTypeInfo.DataURLWithoutPrefix != nil {
			imageBlock.Source.Data = urlTypeInfo.DataURLWithoutPrefix
		} else {
			imageBlock.Source.Data = &formattedImgContent.URL
		}
	}

	return imageBlock
}

func (block AnthropicContentBlock) ToBifrostContentImageBlock() schemas.ChatContentBlock {
	return schemas.ChatContentBlock{
		Type: schemas.ChatContentBlockTypeImage,
		ImageURLStruct: &schemas.ChatInputImage{
			URL: getImageURLFromBlock(block),
		},
	}
}

func getImageURLFromBlock(block AnthropicContentBlock) string {
	if block.Source == nil {
		return ""
	}

	// Handle base64 data - convert to data URL
	if block.Source.Data != nil {
		mime := "image/png"
		if block.Source.MediaType != nil && *block.Source.MediaType != "" {
			mime = *block.Source.MediaType
		}
		return "data:" + mime + ";base64," + *block.Source.Data
	}

	// Handle regular URLs
	if block.Source.URL != nil {
		return *block.Source.URL
	}

	return ""
}
