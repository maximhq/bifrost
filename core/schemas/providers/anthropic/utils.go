package anthropic

import (
	"encoding/json"

	"github.com/maximhq/bifrost/core/schemas"
)

var (
	finishReasonMap = map[string]string{
		"end_turn":      "stop",
		"max_tokens":    "length",
		"stop_sequence": "stop",
		"tool_use":      "tool_calls",
	}
)

// MapAnthropicFinishReasonToOpenAI maps Anthropic finish reasons to OpenAI-compatible ones
func MapAnthropicFinishReasonToBifrost(anthropicReason string) string {
	if _, ok := finishReasonMap[anthropicReason]; ok {
		return finishReasonMap[anthropicReason]
	}
	return anthropicReason
}

func MapBifrostFinishReasonToAnthropic(bifrostReason string) string {
	for k, v := range finishReasonMap {
		if v == bifrostReason {
			return k
		}
	}
	return bifrostReason
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
func convertToAnthropicImageBlock(block schemas.ContentBlock) AnthropicContentBlock {
	imageBlock := AnthropicContentBlock{
		Type:   "image",
		Source: &AnthropicImageSource{},
	}

	if block.ImageURL == nil {
		return imageBlock
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

func (block AnthropicContentBlock) ToBifrostImageBlock() schemas.ContentBlock {
	return schemas.ContentBlock{
		Type: schemas.ContentBlockTypeImage,
		ChatCompletionsExtendedContentBlock: &schemas.ChatCompletionsExtendedContentBlock{
			ImageURL: &schemas.InputImage{
				URL: func() string {
					if block.Source.Data != nil {
						mime := "image/png"
						if block.Source.MediaType != nil && *block.Source.MediaType != "" {
							mime = *block.Source.MediaType
						}
						return "data:" + mime + ";base64," + *block.Source.Data
					}
					if block.Source.URL != nil {
						return *block.Source.URL
					}
					return ""
				}(),
			},
		},
	}
}
