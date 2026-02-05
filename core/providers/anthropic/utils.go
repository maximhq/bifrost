package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
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

func getRequestBodyForResponses(ctx context.Context, request *schemas.BifrostResponsesRequest, providerName schemas.ModelProvider, isStreaming bool) ([]byte, *schemas.BifrostError) {
	var jsonBody []byte
	var err error

	// Check if raw request body should be used
	if useRawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && useRawBody {
		jsonBody = request.GetRawRequestBody()
		// Unmarshal and check if model and region are present
		var requestBody map[string]interface{}
		if err := sonic.Unmarshal(jsonBody, &requestBody); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, fmt.Errorf("failed to unmarshal request body: %w", err), providerName)
		}
		// Add max_tokens if not present
		if _, exists := requestBody["max_tokens"]; !exists {
			requestBody["max_tokens"] = AnthropicDefaultMaxTokens
		}
		// Add stream if not present
		if isStreaming {
			requestBody["stream"] = true
		}
		jsonBody, err = sonic.Marshal(requestBody)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
		}
	} else {
		// Convert request to Anthropic format
		reqBody, err := ToAnthropicResponsesRequest(request)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err, providerName)
		}
		if reqBody == nil {
			return nil, providerUtils.NewBifrostOperationError("request body is not provided", nil, providerName)
		}

		if isStreaming {
			reqBody.Stream = schemas.Ptr(true)
		}

		// Convert struct to map
		jsonBody, err = sonic.Marshal(reqBody)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("failed to marshal request body: %w", err), providerName)
		}
	}

	return jsonBody, nil
}

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
		Type:         AnthropicContentBlockTypeImage,
		CacheControl: block.CacheControl,
		Source:       &AnthropicSource{},
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

// ConvertToAnthropicDocumentBlock converts a Bifrost file block to Anthropic document format
func ConvertToAnthropicDocumentBlock(block schemas.ChatContentBlock) AnthropicContentBlock {
	documentBlock := AnthropicContentBlock{
		Type:         AnthropicContentBlockTypeDocument,
		CacheControl: block.CacheControl,
		Source:       &AnthropicSource{},
	}

	if block.File == nil {
		return documentBlock
	}

	file := block.File

	// Set title if provided
	if file.Filename != nil {
		documentBlock.Title = file.Filename
	}

	// Handle file_data (base64 encoded data)
	if file.FileData != nil && *file.FileData != "" {
		fileData := *file.FileData

		// Check if it's plain text based on file type
		if file.FileType != nil && (*file.FileType == "text/plain" || *file.FileType == "txt") {
			documentBlock.Source.Type = "text"
			documentBlock.Source.Data = &fileData
			return documentBlock
		}

		if strings.HasPrefix(fileData, "data:") {
			urlTypeInfo := schemas.ExtractURLTypeInfo(fileData)

			if urlTypeInfo.DataURLWithoutPrefix != nil {
				// It's a data URL, extract the base64 content
				documentBlock.Source.Type = "base64"
				documentBlock.Source.Data = urlTypeInfo.DataURLWithoutPrefix

				// Set media type from data URL or file type
				if urlTypeInfo.MediaType != nil {
					documentBlock.Source.MediaType = urlTypeInfo.MediaType
				} else if file.FileType != nil {
					documentBlock.Source.MediaType = file.FileType
				}
				return documentBlock
			}
		}

		// Default to base64 for binary files
		documentBlock.Source.Type = "base64"
		documentBlock.Source.Data = &fileData

		// Set media type
		if file.FileType != nil {
			documentBlock.Source.MediaType = file.FileType
		} else {
			// Default to PDF if not specified
			mediaType := "application/pdf"
			documentBlock.Source.MediaType = &mediaType
		}
		return documentBlock
	}

	return documentBlock
}

// ConvertResponsesFileBlockToAnthropic converts a Responses file block directly to Anthropic document format
func ConvertResponsesFileBlockToAnthropic(fileBlock *schemas.ResponsesInputMessageContentBlockFile, cacheControl *schemas.CacheControl) AnthropicContentBlock {
	documentBlock := AnthropicContentBlock{
		Type:         AnthropicContentBlockTypeDocument,
		CacheControl: cacheControl,
		Source:       &AnthropicSource{},
	}

	if fileBlock == nil {
		return documentBlock
	}

	// Set title if provided
	if fileBlock.Filename != nil {
		documentBlock.Title = fileBlock.Filename
	}

	// Handle file_data (base64 encoded data or plain text)
	if fileBlock.FileData != nil && *fileBlock.FileData != "" {
		fileData := *fileBlock.FileData

		// Check if it's plain text based on file type
		if fileBlock.FileType != nil && (*fileBlock.FileType == "text/plain" || *fileBlock.FileType == "txt") {
			documentBlock.Source.Type = "text"
			documentBlock.Source.Data = &fileData
			documentBlock.Source.MediaType = schemas.Ptr("text/plain")
			return documentBlock
		}

		// Check if it's a data URL (e.g., "data:application/pdf;base64,...")
		if strings.HasPrefix(fileData, "data:") {
			urlTypeInfo := schemas.ExtractURLTypeInfo(fileData)

			if urlTypeInfo.DataURLWithoutPrefix != nil {
				// It's a data URL, extract the base64 content
				documentBlock.Source.Type = "base64"
				documentBlock.Source.Data = urlTypeInfo.DataURLWithoutPrefix

				// Set media type from data URL or file type
				if urlTypeInfo.MediaType != nil {
					documentBlock.Source.MediaType = urlTypeInfo.MediaType
				} else if fileBlock.FileType != nil {
					documentBlock.Source.MediaType = fileBlock.FileType
				}
				return documentBlock
			}
		}

		// Default to base64 for binary files (raw base64 without prefix)
		documentBlock.Source.Type = "base64"
		documentBlock.Source.Data = &fileData

		// Set media type
		if fileBlock.FileType != nil {
			documentBlock.Source.MediaType = fileBlock.FileType
		} else {
			// Default to PDF if not specified
			mediaType := "application/pdf"
			documentBlock.Source.MediaType = &mediaType
		}
		return documentBlock
	}

	// Handle file URL
	if fileBlock.FileURL != nil && *fileBlock.FileURL != "" {
		documentBlock.Source.Type = "url"
		documentBlock.Source.URL = fileBlock.FileURL

		if fileBlock.FileType != nil {
			documentBlock.Source.MediaType = fileBlock.FileType
		}
		return documentBlock
	}

	return documentBlock
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

// convertChatResponseFormatToAnthropicOutputFormat converts OpenAI Chat Completions response_format
// to Anthropic's output_format structure.
//
// OpenAI Chat Completions format:
//
//	{
//	  "type": "json_schema",
//	  "json_schema": {
//	    "name": "MySchema",
//	    "schema": {...},
//	    "strict": true
//	  }
//	}
//
// Anthropic's expected format (per https://docs.claude.com/en/docs/build-with-claude/structured-outputs):
//
//	{
//	  "type": "json_schema",
//	  "name": "MySchema",
//	  "schema": {...},
//	  "strict": true
//	}
func convertChatResponseFormatToAnthropicOutputFormat(responseFormat *interface{}) interface{} {
	if responseFormat == nil {
		return nil
	}

	formatMap, ok := (*responseFormat).(map[string]interface{})
	if !ok {
		return nil
	}

	formatType, ok := formatMap["type"].(string)
	if !ok || formatType != "json_schema" {
		return nil
	}

	// Extract the nested json_schema object
	jsonSchemaObj, ok := formatMap["json_schema"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Build the flattened Anthropic-compatible output_format structure
	outputFormat := map[string]interface{}{
		"type": formatType,
	}

	if schema, ok := jsonSchemaObj["schema"].(map[string]interface{}); ok {
		outputFormat["schema"] = schema
	}

	return outputFormat
}

// convertResponsesTextConfigToAnthropicOutputFormat converts OpenAI Responses API text config
// to Anthropic's output_format structure.
//
// OpenAI Responses API format:
//
//	{
//	  "text": {
//	    "format": {
//	      "type": "json_schema",
//	      "schema": {...}
//	    }
//	  }
//	}
//
// Anthropic's expected format (per https://docs.claude.com/en/docs/build-with-claude/structured-outputs):
//
//	{
//	  "type": "json_schema",
//	  "schema": {...}
//	}
func convertResponsesTextConfigToAnthropicOutputFormat(textConfig *schemas.ResponsesTextConfig) interface{} {
	if textConfig == nil || textConfig.Format == nil {
		return nil
	}

	format := textConfig.Format
	// Anthropic currently only supports json_schema type
	if format.Type != "json_schema" {
		return nil
	}

	// Build the Anthropic-compatible output_format structure
	outputFormat := map[string]interface{}{
		"type": format.Type,
	}

	if format.JSONSchema != nil {
		// Convert the schema structure
		schema := map[string]interface{}{}

		if format.JSONSchema.Type != nil {
			schema["type"] = *format.JSONSchema.Type
		}

		if format.JSONSchema.Properties != nil {
			schema["properties"] = *format.JSONSchema.Properties
		}

		if len(format.JSONSchema.Required) > 0 {
			schema["required"] = format.JSONSchema.Required
		}

		if format.JSONSchema.Type != nil && *format.JSONSchema.Type == "object" {
			schema["additionalProperties"] = false
		} else if format.JSONSchema.AdditionalProperties != nil {
			schema["additionalProperties"] = *format.JSONSchema.AdditionalProperties
		}

		outputFormat["schema"] = schema
	}

	return outputFormat
}

// convertAnthropicOutputFormatToResponsesTextConfig converts Anthropic's output_format structure
// to OpenAI Responses API text config.
//
// Anthropic format:
//
//	{
//	  "type": "json_schema",
//	  "schema": {...},
//	}
//
// OpenAI Responses API format:
//
//	{
//	  "text": {
//	    "format": {
//	      "type": "json_schema",
//	      "json_schema": {...},
//	      "name": "...",
//	      "strict": true
//	    }
//	  }
//	}
func convertAnthropicOutputFormatToResponsesTextConfig(outputFormat interface{}) *schemas.ResponsesTextConfig {
	if outputFormat == nil {
		return nil
	}

	// Try to convert to map
	formatMap, ok := outputFormat.(map[string]interface{})
	if !ok {
		return nil
	}

	// Extract type
	formatType, ok := formatMap["type"].(string)
	if !ok || formatType != "json_schema" {
		return nil
	}

	format := &schemas.ResponsesTextConfigFormat{
		Type: formatType,
	}

	// Extract schema if present
	if schemaMap, ok := formatMap["schema"].(map[string]interface{}); ok {
		jsonSchema := &schemas.ResponsesTextConfigFormatJSONSchema{}

		if schemaType, ok := schemaMap["type"].(string); ok {
			jsonSchema.Type = &schemaType
		}

		if properties, ok := schemaMap["properties"].(map[string]interface{}); ok {
			jsonSchema.Properties = &properties
		}

		if required, ok := schemaMap["required"].([]interface{}); ok {
			requiredStrs := make([]string, 0, len(required))
			for _, r := range required {
				if rStr, ok := r.(string); ok {
					requiredStrs = append(requiredStrs, rStr)
				}
			}
			if len(requiredStrs) > 0 {
				jsonSchema.Required = requiredStrs
			}
		}

		if additionalProps, ok := schemaMap["additionalProperties"].(bool); ok {
			jsonSchema.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
				AdditionalPropertiesBool: &additionalProps,
			}
		}

		if additionalProps, ok := schemas.SafeExtractOrderedMap(schemaMap["additionalProperties"]); ok {
			jsonSchema.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
				AdditionalPropertiesMap: &additionalProps,
			}
		}

		format.JSONSchema = jsonSchema
	}

	return &schemas.ResponsesTextConfig{
		Format: format,
	}
}
