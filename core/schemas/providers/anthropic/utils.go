package anthropic

import (
	"encoding/json"

	"github.com/maximhq/bifrost/core/schemas"
)

var (
	// Maps provider-specific finish reasons to Bifrost format
	anthropicFinishReasonToBifrost = map[AnthropicStopReason]string{
		AnthropicStopReasonEndTurn:      "stop",
		AnthropicStopReasonMaxTokens:    "length",
		AnthropicStopReasonStopSequence: "stop",
		AnthropicStopReasonToolUse:      "tool_calls",
	}

	// Maps Bifrost finish reasons to provider-specific format
	bifrostToAnthropicFinishReason = map[string]AnthropicStopReason{
		"stop":       AnthropicStopReasonEndTurn, // canonical default
		"length":     AnthropicStopReasonMaxTokens,
		"tool_calls": AnthropicStopReasonToolUse,
	}
)

// ConvertAnthropicFinishReasonToBifrost converts provider finish reasons to Bifrost format
func ConvertAnthropicFinishReasonToBifrost(providerReason AnthropicStopReason) string {
	if bifrostReason, ok := anthropicFinishReasonToBifrost[providerReason]; ok {
		return bifrostReason
	}
	return string(providerReason)
}

// ConvertBifrostFinishReasonToAnthropic converts Bifrost finish reasons to provider format
func ConvertBifrostFinishReasonToAnthropic(bifrostReason string) AnthropicStopReason {
	if providerReason, ok := bifrostToAnthropicFinishReason[bifrostReason]; ok {
		return providerReason
	}
	return AnthropicStopReason(bifrostReason)
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

// Helper function to parse JSON input arguments back to interface{}
func parseJSONInput(jsonStr string) interface{} {
	if jsonStr == "" || jsonStr == "{}" {
		return map[string]interface{}{}
	}

	var result interface{}
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		// If parsing fails, return as string
		return jsonStr
	}

	return result
}
