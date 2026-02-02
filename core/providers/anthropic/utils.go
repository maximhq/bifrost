package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/valyala/fasthttp"

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
		AnthropicStopReasonCompaction:   "compaction",
	}

	// Maps Bifrost finish reasons to provider-specific format
	bifrostToAnthropicFinishReason = map[string]AnthropicStopReason{
		"stop":       AnthropicStopReasonEndTurn, // canonical default
		"length":     AnthropicStopReasonMaxTokens,
		"tool_calls": AnthropicStopReasonToolUse,
		"compaction": AnthropicStopReasonCompaction,
	}
)

// SupportsNativeEffort returns true if the model supports Anthropic's native output_config.effort parameter.
// Currently supported on Claude Opus 4.5 and Opus 4.6.
func SupportsNativeEffort(model string) bool {
	model = strings.ToLower(model)
	if !strings.Contains(model, "opus") {
		return false
	}
	return strings.Contains(model, "4-5") || strings.Contains(model, "4.5") ||
		strings.Contains(model, "4-6") || strings.Contains(model, "4.6")
}

// SupportsAdaptiveThinking returns true if the model supports thinking.type: "adaptive".
// Currently only supported on Claude Opus 4.6.
func SupportsAdaptiveThinking(model string) bool {
	model = strings.ToLower(model)
	return strings.Contains(model, "opus") &&
		(strings.Contains(model, "4-6") || strings.Contains(model, "4.6"))
}

// MapBifrostEffortToAnthropic maps a Bifrost effort level to an Anthropic effort level.
// Anthropic supports "low", "medium", "high", "max"; Bifrost also has "minimal" which maps to "low".
func MapBifrostEffortToAnthropic(effort string) string {
	if effort == "minimal" {
		return "low"
	}
	return effort
}

// MapAnthropicEffortToBifrost maps an Anthropic effort level to a Bifrost effort level.
// Anthropic supports "max" (Opus 4.6+) which is not in Bifrost's enum; it maps to "high".
func MapAnthropicEffortToBifrost(effort string) string {
	if effort == "max" {
		return "high"
	}
	return effort
}

// setEffortOnOutputConfig merges the effort value into the request's OutputConfig,
// preserving any existing Format field (used for structured outputs).
func setEffortOnOutputConfig(req *AnthropicMessageRequest, effort string) {
	if req.OutputConfig == nil {
		req.OutputConfig = &AnthropicOutputConfig{}
	}
	req.OutputConfig.Effort = &effort
}

func getRequestBodyForResponses(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, providerName schemas.ModelProvider, isStreaming bool) ([]byte, *schemas.BifrostError) {
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
		// update model with provider model
		if modelVal, exists := requestBody["model"]; exists {
			if modelStr, ok := modelVal.(string); ok {
				_, model := schemas.ParseModelString(modelStr, schemas.Anthropic)
				requestBody["model"] = model
			}
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
		reqBody, err := ToAnthropicResponsesRequest(ctx, request)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err, providerName)
		}
		if reqBody == nil {
			return nil, providerUtils.NewBifrostOperationError("request body is not provided", nil, providerName)
		}
		addMissingBetaHeadersToContext(ctx, reqBody)
		if isStreaming {
			reqBody.Stream = schemas.Ptr(true)
		}
		// Convert struct to map
		jsonBody, err = sonic.Marshal(reqBody)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("failed to marshal request body: %w", err), providerName)
		}
		// Merge ExtraParams into the JSON if passthrough is enabled
		if ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams) != nil && ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true {
			extraParams := reqBody.GetExtraParams()
			if len(extraParams) > 0 {
				var jsonMap map[string]interface{}
				if err := sonic.Unmarshal(jsonBody, &jsonMap); err != nil {
					return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
				}
				// Merge ExtraParams recursively (handles nested maps)
				providerUtils.MergeExtraParams(jsonMap, extraParams)
				// Re-marshal the merged map
				jsonBody, err = sonic.Marshal(jsonMap)
				if err != nil {
					return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
				}
			}
		}
	}
	return jsonBody, nil
}

// addMissingBetaHeadersToContext analyzes the Anthropic request and adds missing beta headers to the context
func addMissingBetaHeadersToContext(ctx *schemas.BifrostContext, req *AnthropicMessageRequest) error {
	headers := []string{}
	hasCachingScope := false
	if req.Tools != nil {
		for _, tool := range req.Tools {
			// Check for strict (structured-outputs)
			if tool.Strict != nil && *tool.Strict {
				headers = appendUniqueHeader(headers, AnthropicStructuredOutputsBetaHeader)
			}
			// Check for advanced-tool-use features
			if tool.DeferLoading != nil && *tool.DeferLoading {
				headers = appendUniqueHeader(headers, AnthropicAdvancedToolUseBetaHeader)
			}
			if len(tool.InputExamples) > 0 {
				headers = appendUniqueHeader(headers, AnthropicAdvancedToolUseBetaHeader)
			}
			if len(tool.AllowedCallers) > 0 {
				headers = appendUniqueHeader(headers, AnthropicAdvancedToolUseBetaHeader)
			}
			// Check for cache control with scope
			if !hasCachingScope && tool.CacheControl != nil && tool.CacheControl.Scope != nil {
				headers = appendUniqueHeader(headers, AnthropicPromptCachingScopeBetaHeader)
				hasCachingScope = true
			}
		}
	}
	// Check for compaction
	if req.ContextManagement != nil {
		for _, edit := range req.ContextManagement.Edits {
			if edit.Type == ContextManagementEditTypeCompact {
				headers = appendUniqueHeader(headers, AnthropicCompactionBetaHeader)
			}
			if edit.Type == ContextManagementEditTypeClearToolUses || edit.Type == ContextManagementEditTypeClearThinking {
				headers = appendUniqueHeader(headers, AnthropicContextManagementBetaHeader)
			}
		}
	}
	// Check for MCP servers
	if len(req.MCPServers) > 0 {
		headers = appendUniqueHeader(headers, AnthropicMCPClientBetaHeader)
	}
	// Check for output format (structured outputs)
	if req.OutputFormat != nil {
		headers = appendUniqueHeader(headers, AnthropicStructuredOutputsBetaHeader)
	}
	// Check for cache control with scope in system message (only if not already found)
	if !hasCachingScope && req.System != nil && req.System.ContentBlocks != nil {
		for _, block := range req.System.ContentBlocks {
			if block.CacheControl != nil && block.CacheControl.Scope != nil {
				headers = appendUniqueHeader(headers, AnthropicPromptCachingScopeBetaHeader)
				hasCachingScope = true
				break
			}
		}
	}
	// Check for cache control with scope in messages (only if not already found)
	if !hasCachingScope {
		for _, message := range req.Messages {
			if message.Content.ContentBlocks != nil {
				for _, block := range message.Content.ContentBlocks {
					if block.CacheControl != nil && block.CacheControl.Scope != nil {
						headers = appendUniqueHeader(headers, AnthropicPromptCachingScopeBetaHeader)
						hasCachingScope = true
						break
					}
				}
				if hasCachingScope {
					break
				}
			}
		}
	}
	if len(headers) == 0 {
		return nil
	}
	var extraHeaders map[string][]string
	if ctx.Value(schemas.BifrostContextKeyExtraHeaders) == nil {
		extraHeaders = map[string][]string{}
	} else {
		if ctxExtraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
			extraHeaders = ctxExtraHeaders
		}
	}
	if len(extraHeaders["anthropic-beta"]) == 0 {
		extraHeaders["anthropic-beta"] = headers
	} else {
		extraHeaders["anthropic-beta"] = append(extraHeaders["anthropic-beta"], headers...)
	}
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, extraHeaders)
	return nil
}

// appendUniqueHeader adds a header to the slice if not already present
func appendUniqueHeader(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}

// appendBetaHeader appends a beta header to the request, preserving any existing beta headers
func appendBetaHeader(req *fasthttp.Request, betaHeader string) {
	existing := string(req.Header.Peek("anthropic-beta"))
	if existing == "" {
		req.Header.Set("anthropic-beta", betaHeader)
		return
	}
	// Check if header already present
	for _, h := range strings.Split(existing, ",") {
		if strings.TrimSpace(h) == betaHeader {
			return
		}
	}
	req.Header.Set("anthropic-beta", existing+","+betaHeader)
}

// convertChatResponseFormatToTool converts a response_format config to an Anthropic tool for structured output
// This is used when the provider is Vertex, which doesn't support native structured outputs
func convertChatResponseFormatToTool(ctx *schemas.BifrostContext, params *schemas.ChatParameters) *AnthropicTool {
	if params == nil || params.ResponseFormat == nil {
		return nil
	}

	// ResponseFormat is stored as interface{}, need to parse it
	responseFormatMap, ok := (*params.ResponseFormat).(map[string]interface{})
	if !ok {
		return nil
	}

	// Check if type is "json_schema"
	formatType, ok := responseFormatMap["type"].(string)
	if !ok || formatType != "json_schema" {
		return nil
	}

	// Extract json_schema object
	jsonSchemaObj, ok := responseFormatMap["json_schema"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Extract name and schema
	toolName, ok := jsonSchemaObj["name"].(string)
	if !ok || toolName == "" {
		toolName = "json_response"
	}

	schemaObj, ok := jsonSchemaObj["schema"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Extract description from schema if available
	description := "Returns structured JSON output"
	if desc, ok := schemaObj["description"].(string); ok && desc != "" {
		description = desc
	}

	// Set bifrost context key structured output tool name
	toolName = fmt.Sprintf("bf_so_%s", toolName)
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, toolName)

	// Create the Anthropic tool
	normalizedSchema := normalizeSchemaForAnthropic(schemaObj)
	schemaParams := convertMapToToolFunctionParameters(normalizedSchema)

	return &AnthropicTool{
		Name:        toolName,
		Description: schemas.Ptr(description),
		InputSchema: schemaParams,
	}
}

// convertResponsesTextFormatToTool converts a text config to an Anthropic tool for structured output
// This is used when the provider is Vertex, which doesn't support native structured outputs
func convertResponsesTextFormatToTool(ctx *schemas.BifrostContext, textConfig *schemas.ResponsesTextConfig) *AnthropicTool {
	if textConfig == nil || textConfig.Format == nil {
		return nil
	}

	format := textConfig.Format
	if format.Type != "json_schema" {
		return nil
	}

	toolName := "json_response"
	if format.Name != nil && strings.TrimSpace(*format.Name) != "" {
		toolName = strings.TrimSpace(*format.Name)
	}

	description := "Returns structured JSON output"
	if format.JSONSchema != nil && format.JSONSchema.Description != nil {
		description = *format.JSONSchema.Description
	}

	toolName = fmt.Sprintf("bf_so_%s", toolName)
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, toolName)

	var schemaParams *schemas.ToolFunctionParameters
	if format.JSONSchema != nil {
		schemaParams = convertJSONSchemaToToolParameters(format.JSONSchema)
	} else {
		return nil // Schema is required for tooling
	}

	return &AnthropicTool{
		Name:        toolName,
		Description: schemas.Ptr(description),
		InputSchema: schemaParams,
	}
}

// convertJSONSchemaToToolParameters directly converts ResponsesTextConfigFormatJSONSchema to ToolFunctionParameters
func convertJSONSchemaToToolParameters(schema *schemas.ResponsesTextConfigFormatJSONSchema) *schemas.ToolFunctionParameters {
	if schema == nil {
		return nil
	}

	// Default type to "object" if not specified
	schemaType := "object"
	if schema.Type != nil {
		schemaType = *schema.Type
	}

	params := &schemas.ToolFunctionParameters{
		Type:                 schemaType,
		Description:          schema.Description,
		Required:             schema.Required,
		Enum:                 schema.Enum,
		Ref:                  schema.Ref,
		MinItems:             schema.MinItems,
		MaxItems:             schema.MaxItems,
		Format:               schema.Format,
		Pattern:              schema.Pattern,
		MinLength:            schema.MinLength,
		MaxLength:            schema.MaxLength,
		Minimum:              schema.Minimum,
		Maximum:              schema.Maximum,
		Title:                schema.Title,
		Default:              schema.Default,
		Nullable:             schema.Nullable,
		AdditionalProperties: schema.AdditionalProperties,
	}

	// Convert map[string]any to OrderedMap for Properties
	if schema.Properties != nil {
		if orderedMap, ok := schemas.SafeExtractOrderedMap(*schema.Properties); ok {
			params.Properties = orderedMap
		}
	}

	// Convert map[string]any to OrderedMap for Defs
	if schema.Defs != nil {
		if orderedMap, ok := schemas.SafeExtractOrderedMap(*schema.Defs); ok {
			params.Defs = orderedMap
		}
	}

	// Convert map[string]any to OrderedMap for Definitions
	if schema.Definitions != nil {
		if orderedMap, ok := schemas.SafeExtractOrderedMap(*schema.Definitions); ok {
			params.Definitions = orderedMap
		}
	}

	// Convert map[string]any to OrderedMap for Items
	if schema.Items != nil {
		if orderedMap, ok := schemas.SafeExtractOrderedMap(*schema.Items); ok {
			params.Items = orderedMap
		}
	}

	// Convert []map[string]any to []OrderedMap for composition fields
	if len(schema.AnyOf) > 0 {
		params.AnyOf = make([]schemas.OrderedMap, 0, len(schema.AnyOf))
		for _, item := range schema.AnyOf {
			if orderedMap, ok := schemas.SafeExtractOrderedMap(item); ok {
				params.AnyOf = append(params.AnyOf, *orderedMap)
			}
		}
	}

	if len(schema.OneOf) > 0 {
		params.OneOf = make([]schemas.OrderedMap, 0, len(schema.OneOf))
		for _, item := range schema.OneOf {
			if orderedMap, ok := schemas.SafeExtractOrderedMap(item); ok {
				params.OneOf = append(params.OneOf, *orderedMap)
			}
		}
	}

	if len(schema.AllOf) > 0 {
		params.AllOf = make([]schemas.OrderedMap, 0, len(schema.AllOf))
		for _, item := range schema.AllOf {
			if orderedMap, ok := schemas.SafeExtractOrderedMap(item); ok {
				params.AllOf = append(params.AllOf, *orderedMap)
			}
		}
	}

	return params
}

// convertMapToToolFunctionParameters converts a map to ToolFunctionParameters
func convertMapToToolFunctionParameters(m map[string]interface{}) *schemas.ToolFunctionParameters {
	params := &schemas.ToolFunctionParameters{}

	if typeVal, ok := m["type"].(string); ok {
		params.Type = typeVal
	}
	if desc, ok := m["description"].(string); ok {
		params.Description = &desc
	}
	if props, ok := schemas.SafeExtractOrderedMap(m["properties"]); ok {
		params.Properties = props
	}
	if req, ok := m["required"].([]interface{}); ok {
		required := make([]string, 0, len(req))
		for _, r := range req {
			if str, ok := r.(string); ok {
				required = append(required, str)
			}
		}
		params.Required = required
	}
	if addProps, ok := m["additionalProperties"]; ok {
		if addPropsBool, ok := addProps.(bool); ok {
			params.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
				AdditionalPropertiesBool: &addPropsBool,
			}
		} else if addPropsMap, ok := schemas.SafeExtractOrderedMap(addProps); ok {
			params.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
				AdditionalPropertiesMap: addPropsMap,
			}
		}
	}
	if defs, ok := schemas.SafeExtractOrderedMap(m["$defs"]); ok {
		params.Defs = defs
	}
	if definitions, ok := schemas.SafeExtractOrderedMap(m["definitions"]); ok {
		params.Definitions = definitions
	}
	if ref, ok := m["$ref"].(string); ok {
		params.Ref = &ref
	}
	if items, ok := schemas.SafeExtractOrderedMap(m["items"]); ok {
		params.Items = items
	}
	if minItems, ok := anthropicExtractInt64(m["minItems"]); ok {
		params.MinItems = schemas.Ptr(minItems)
	}
	if maxItems, ok := anthropicExtractInt64(m["maxItems"]); ok {
		params.MaxItems = schemas.Ptr(maxItems)
	}
	if anyOf, ok := m["anyOf"].([]interface{}); ok {
		anyOfMaps := make([]schemas.OrderedMap, 0, len(anyOf))
		for _, item := range anyOf {
			if orderedMap, ok := schemas.SafeExtractOrderedMap(item); ok {
				anyOfMaps = append(anyOfMaps, *orderedMap)
			}
		}
		if len(anyOfMaps) > 0 {
			params.AnyOf = anyOfMaps
		}
	}
	if oneOf, ok := m["oneOf"].([]interface{}); ok {
		oneOfMaps := make([]schemas.OrderedMap, 0, len(oneOf))
		for _, item := range oneOf {
			if orderedMap, ok := schemas.SafeExtractOrderedMap(item); ok {
				oneOfMaps = append(oneOfMaps, *orderedMap)
			}
		}
		if len(oneOfMaps) > 0 {
			params.OneOf = oneOfMaps
		}
	}
	if allOf, ok := m["allOf"].([]interface{}); ok {
		allOfMaps := make([]schemas.OrderedMap, 0, len(allOf))
		for _, item := range allOf {
			if orderedMap, ok := schemas.SafeExtractOrderedMap(item); ok {
				allOfMaps = append(allOfMaps, *orderedMap)
			}
		}
		if len(allOfMaps) > 0 {
			params.AllOf = allOfMaps
		}
	}
	if format, ok := m["format"].(string); ok {
		params.Format = &format
	}
	if pattern, ok := m["pattern"].(string); ok {
		params.Pattern = &pattern
	}
	if minLength, ok := anthropicExtractInt64(m["minLength"]); ok {
		params.MinLength = schemas.Ptr(minLength)
	}
	if maxLength, ok := anthropicExtractInt64(m["maxLength"]); ok {
		params.MaxLength = schemas.Ptr(maxLength)
	}
	if minimum, ok := anthropicExtractFloat64(m["minimum"]); ok {
		params.Minimum = &minimum
	}
	if maximum, ok := anthropicExtractFloat64(m["maximum"]); ok {
		params.Maximum = &maximum
	}
	if title, ok := m["title"].(string); ok {
		params.Title = &title
	}
	if enumVal, ok := m["enum"]; ok {
		switch e := enumVal.(type) {
		case []interface{}:
			enumStrs := make([]string, 0, len(e))
			for _, v := range e {
				if s, ok := v.(string); ok {
					enumStrs = append(enumStrs, s)
				}
			}
			if len(enumStrs) > 0 {
				params.Enum = enumStrs
			}
		case []string:
			if len(e) > 0 {
				params.Enum = e
			}
		}
	}
	if def, ok := m["default"]; ok {
		params.Default = def
	}
	if nullable, ok := m["nullable"].(bool); ok {
		params.Nullable = &nullable
	}

	if params.Type == "" {
		params.Type = "object"
	}

	return params
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

	if block.Citations != nil {
		documentBlock.Citations = &AnthropicCitations{Config: block.Citations}
	}

	if block.File == nil {
		return documentBlock
	}

	file := block.File

	// Set title if provided
	if file.Filename != nil {
		documentBlock.Title = file.Filename
	}

	// Handle file URL
	if file.FileURL != nil && *file.FileURL != "" {
		documentBlock.Source.Type = "url"
		documentBlock.Source.URL = file.FileURL
		return documentBlock
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
func ConvertResponsesFileBlockToAnthropic(fileBlock *schemas.ResponsesInputMessageContentBlockFile, cacheControl *schemas.CacheControl, citations *schemas.Citations) AnthropicContentBlock {
	documentBlock := AnthropicContentBlock{
		Type:         AnthropicContentBlockTypeDocument,
		CacheControl: cacheControl,
		Source:       &AnthropicSource{},
	}

	if citations != nil {
		documentBlock.Citations = &AnthropicCitations{Config: citations}
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

// extractTypesFromValue extracts type strings from various formats (string, []string, []interface{})
func extractTypesFromValue(typeVal interface{}) []string {
	switch t := typeVal.(type) {
	case string:
		return []string{t}
	case []string:
		return t
	case []interface{}:
		types := make([]string, 0, len(t))
		for _, item := range t {
			if typeStr, ok := item.(string); ok {
				types = append(types, typeStr)
			}
		}
		return types
	default:
		return nil
	}
}

// filterEnumValuesByType filters enum values to only include those matching the specified JSON schema type.
// This ensures that when we split multi-type fields into anyOf branches, each branch only contains
// enum values compatible with its declared type.
func filterEnumValuesByType(enumValues []interface{}, schemaType string) []interface{} {
	if len(enumValues) == 0 {
		return nil
	}

	filtered := make([]interface{}, 0, len(enumValues))
	for _, val := range enumValues {
		// Determine the actual type of the enum value
		var actualType string
		switch val.(type) {
		case string:
			actualType = "string"
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			actualType = "integer"
		case float32, float64:
			// Check if it's actually an integer value in float form
			if fv, ok := val.(float64); ok && fv == float64(int64(fv)) {
				actualType = "integer"
			} else {
				actualType = "number"
			}
		case bool:
			actualType = "boolean"
		case nil:
			actualType = "null"
		default:
			// For other types (objects, arrays), include them in all branches
			filtered = append(filtered, val)
			continue
		}

		// Include the value if its type matches the schema type
		// Also handle "number" type which includes both integers and floats
		if actualType == schemaType || (schemaType == "number" && actualType == "integer") {
			filtered = append(filtered, val)
		}
	}

	return filtered
}

// normalizeSchemaForAnthropic recursively normalizes a JSON schema to be compatible with Anthropic's API.
// This handles cases where:
// 1. type is an array like ["string", "null"] - converted to single type
// 2. type is an array with multiple types like ["string", "integer"] - converted to anyOf
// 3. Enums with nullable types need special handling
func normalizeSchemaForAnthropic(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}

	normalized := make(map[string]interface{})
	for k, v := range schema {
		normalized[k] = v
	}

	// Handle type field if it's an array (e.g., ["string", "null"] or ["string", "integer"])
	if typeVal, exists := normalized["type"]; exists {
		types := extractTypesFromValue(typeVal)
		if len(types) > 0 {
			nonNullTypes := make([]string, 0, len(types))
			for _, t := range types {
				if t != "null" {
					nonNullTypes = append(nonNullTypes, t)
				}
			}

			if len(nonNullTypes) == 0 {
				// Only null type
				normalized["type"] = "null"
			} else if len(nonNullTypes) == 1 && len(types) == 1 {
				// Single type, no null (e.g., ["string"])
				// Just use the single type
				normalized["type"] = nonNullTypes[0]
			} else {
				// Multiple types OR single type with null
				// Convert to anyOf structure for correctness
				// Examples: ["string", "null"], ["string", "integer"], ["string", "integer", "null"]
				delete(normalized, "type")

				// Build anyOf with each non-null type
				anyOfSchemas := make([]interface{}, 0, len(types))
				for _, t := range nonNullTypes {
					typeSchema := map[string]interface{}{"type": t}

					// If there's an enum, filter enum values by type for each anyOf branch
					if enumVal, hasEnum := normalized["enum"]; hasEnum {
						// Convert enum to []interface{} if it's []string or other slice type
						var enumArray []interface{}
						switch e := enumVal.(type) {
						case []interface{}:
							enumArray = e
						case []string:
							enumArray = make([]interface{}, len(e))
							for i, v := range e {
								enumArray[i] = v
							}
						default:
							// If enum is not a slice, skip filtering
							typeSchema["enum"] = enumVal
							anyOfSchemas = append(anyOfSchemas, typeSchema)
							continue
						}

						filteredEnum := filterEnumValuesByType(enumArray, t)
						if len(filteredEnum) > 0 {
							typeSchema["enum"] = filteredEnum
						}
					}

					anyOfSchemas = append(anyOfSchemas, typeSchema)
				}

				// If original had null, add it to anyOf
				if len(nonNullTypes) < len(types) {
					anyOfSchemas = append(anyOfSchemas, map[string]interface{}{"type": "null"})
				}

				normalized["anyOf"] = anyOfSchemas

				// Remove enum from top level since it's now in anyOf branches
				delete(normalized, "enum")
			}
		}
	}

	// Recursively normalize properties
	if properties, ok := schema["properties"].(map[string]interface{}); ok {
		newProps := make(map[string]interface{})
		for key, prop := range properties {
			if propMap, ok := prop.(map[string]interface{}); ok {
				newProps[key] = normalizeSchemaForAnthropic(propMap)
			} else {
				newProps[key] = prop
			}
		}
		normalized["properties"] = newProps
	}

	// Recursively normalize items (for arrays)
	if items, ok := schema["items"].(map[string]interface{}); ok {
		normalized["items"] = normalizeSchemaForAnthropic(items)
	}

	// Recursively normalize anyOf
	if anyOf, ok := schema["anyOf"].([]interface{}); ok {
		newAnyOf := make([]interface{}, 0, len(anyOf))
		for _, item := range anyOf {
			if itemMap, ok := item.(map[string]interface{}); ok {
				newAnyOf = append(newAnyOf, normalizeSchemaForAnthropic(itemMap))
			} else {
				newAnyOf = append(newAnyOf, item)
			}
		}
		normalized["anyOf"] = newAnyOf
	}

	// Recursively normalize oneOf
	if oneOf, ok := schema["oneOf"].([]interface{}); ok {
		newOneOf := make([]interface{}, 0, len(oneOf))
		for _, item := range oneOf {
			if itemMap, ok := item.(map[string]interface{}); ok {
				newOneOf = append(newOneOf, normalizeSchemaForAnthropic(itemMap))
			} else {
				newOneOf = append(newOneOf, item)
			}
		}
		normalized["oneOf"] = newOneOf
	}

	// Recursively normalize allOf
	if allOf, ok := schema["allOf"].([]interface{}); ok {
		newAllOf := make([]interface{}, 0, len(allOf))
		for _, item := range allOf {
			if itemMap, ok := item.(map[string]interface{}); ok {
				newAllOf = append(newAllOf, normalizeSchemaForAnthropic(itemMap))
			} else {
				newAllOf = append(newAllOf, item)
			}
		}
		normalized["allOf"] = newAllOf
	}

	// Recursively normalize definitions/defs
	if definitions, ok := schema["definitions"].(map[string]interface{}); ok {
		newDefs := make(map[string]interface{})
		for key, def := range definitions {
			if defMap, ok := def.(map[string]interface{}); ok {
				newDefs[key] = normalizeSchemaForAnthropic(defMap)
			} else {
				newDefs[key] = def
			}
		}
		normalized["definitions"] = newDefs
	}

	if defs, ok := schema["$defs"].(map[string]interface{}); ok {
		newDefs := make(map[string]interface{})
		for key, def := range defs {
			if defMap, ok := def.(map[string]interface{}); ok {
				newDefs[key] = normalizeSchemaForAnthropic(defMap)
			} else {
				newDefs[key] = def
			}
		}
		normalized["$defs"] = newDefs
	}

	return normalized
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
	// Note: name, description, and strict are NOT included as they are not permitted
	// in Anthropic's GA structured outputs API (output_config.format)
	outputFormat := map[string]interface{}{
		"type": formatType,
	}

	if schema, ok := jsonSchemaObj["schema"].(map[string]interface{}); ok {
		// Normalize the schema to handle type arrays like ["string", "null"]
		normalizedSchema := normalizeSchemaForAnthropic(schema)
		outputFormat["schema"] = normalizedSchema
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

		// Normalize the schema to handle type arrays like ["string", "null"]
		normalizedSchema := normalizeSchemaForAnthropic(schema)
		outputFormat["schema"] = normalizedSchema
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

	// Extract name if present
	if name, ok := formatMap["name"].(string); ok && strings.TrimSpace(name) != "" {
		format.Name = schemas.Ptr(strings.TrimSpace(name))
	} else {
		format.Name = schemas.Ptr("output_format")
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
				AdditionalPropertiesMap: additionalProps,
			}
		}

		// Extract description
		if description, ok := schemaMap["description"].(string); ok {
			jsonSchema.Description = &description
		}

		// Extract $defs (JSON Schema draft 2019-09+)
		if defs, ok := schemaMap["$defs"].(map[string]interface{}); ok {
			jsonSchema.Defs = &defs
		}

		// Extract definitions (legacy JSON Schema draft-07)
		if definitions, ok := schemaMap["definitions"].(map[string]interface{}); ok {
			jsonSchema.Definitions = &definitions
		}

		// Extract $ref
		if ref, ok := schemaMap["$ref"].(string); ok {
			jsonSchema.Ref = &ref
		}

		// Extract items (array element schema)
		if items, ok := schemaMap["items"].(map[string]interface{}); ok {
			jsonSchema.Items = &items
		}

		// Extract minItems
		if minItems, ok := anthropicExtractInt64(schemaMap["minItems"]); ok {
			jsonSchema.MinItems = &minItems
		}

		// Extract maxItems
		if maxItems, ok := anthropicExtractInt64(schemaMap["maxItems"]); ok {
			jsonSchema.MaxItems = &maxItems
		}

		// Extract anyOf
		if anyOf, ok := schemaMap["anyOf"].([]interface{}); ok {
			anyOfMaps := make([]map[string]any, 0, len(anyOf))
			for _, item := range anyOf {
				if m, ok := item.(map[string]interface{}); ok {
					anyOfMaps = append(anyOfMaps, m)
				}
			}
			if len(anyOfMaps) > 0 {
				jsonSchema.AnyOf = anyOfMaps
			}
		}

		// Extract oneOf
		if oneOf, ok := schemaMap["oneOf"].([]interface{}); ok {
			oneOfMaps := make([]map[string]any, 0, len(oneOf))
			for _, item := range oneOf {
				if m, ok := item.(map[string]interface{}); ok {
					oneOfMaps = append(oneOfMaps, m)
				}
			}
			if len(oneOfMaps) > 0 {
				jsonSchema.OneOf = oneOfMaps
			}
		}

		// Extract allOf
		if allOf, ok := schemaMap["allOf"].([]interface{}); ok {
			allOfMaps := make([]map[string]any, 0, len(allOf))
			for _, item := range allOf {
				if m, ok := item.(map[string]interface{}); ok {
					allOfMaps = append(allOfMaps, m)
				}
			}
			if len(allOfMaps) > 0 {
				jsonSchema.AllOf = allOfMaps
			}
		}

		// Extract format
		if formatVal, ok := schemaMap["format"].(string); ok {
			jsonSchema.Format = &formatVal
		}

		// Extract pattern
		if pattern, ok := schemaMap["pattern"].(string); ok {
			jsonSchema.Pattern = &pattern
		}

		// Extract minLength
		if minLength, ok := anthropicExtractInt64(schemaMap["minLength"]); ok {
			jsonSchema.MinLength = &minLength
		}

		// Extract maxLength
		if maxLength, ok := anthropicExtractInt64(schemaMap["maxLength"]); ok {
			jsonSchema.MaxLength = &maxLength
		}

		// Extract minimum
		if minimum, ok := anthropicExtractFloat64(schemaMap["minimum"]); ok {
			jsonSchema.Minimum = &minimum
		}

		// Extract maximum
		if maximum, ok := anthropicExtractFloat64(schemaMap["maximum"]); ok {
			jsonSchema.Maximum = &maximum
		}

		// Extract title
		if title, ok := schemaMap["title"].(string); ok {
			jsonSchema.Title = &title
		}

		// Extract default
		if defaultVal, exists := schemaMap["default"]; exists {
			jsonSchema.Default = defaultVal
		}

		// Extract nullable
		if nullable, ok := schemaMap["nullable"].(bool); ok {
			jsonSchema.Nullable = &nullable
		}

		// Extract enum
		if enum, ok := schemaMap["enum"].([]interface{}); ok {
			enumStrs := make([]string, 0, len(enum))
			for _, e := range enum {
				if str, ok := e.(string); ok {
					enumStrs = append(enumStrs, str)
				}
			}
			if len(enumStrs) > 0 {
				jsonSchema.Enum = enumStrs
			}
		} else if enumStrs, ok := schemaMap["enum"].([]string); ok && len(enumStrs) > 0 {
			jsonSchema.Enum = enumStrs
		}

		format.JSONSchema = jsonSchema
	}

	return &schemas.ResponsesTextConfig{
		Format: format,
	}
}

// sanitizeWebSearchArguments sanitizes WebSearch tool arguments by removing conflicting domain filters.
// Anthropic only allows one of allowed_domains or blocked_domains, not both.
// This function handles empty and non-empty arrays:
// - If one array is empty, delete that one
// - If both arrays are filled, delete blocked_domains
// - If both arrays are empty, delete blocked_domains
func sanitizeWebSearchArguments(argumentsJSON string) string {
	var toolArgs map[string]interface{}
	if err := json.Unmarshal([]byte(argumentsJSON), &toolArgs); err != nil {
		return argumentsJSON // Return original if parse fails
	}

	allowedVal, hasAllowed := toolArgs["allowed_domains"]
	blockedVal, hasBlocked := toolArgs["blocked_domains"]

	// Only process if both fields exist
	if hasAllowed && hasBlocked {
		// Helper function to check if array is empty
		isEmptyArray := func(val interface{}) bool {
			if arr, ok := val.([]interface{}); ok {
				return len(arr) == 0
			}
			return false
		}

		allowedEmpty := isEmptyArray(allowedVal)
		blockedEmpty := isEmptyArray(blockedVal)

		var shouldDelete string
		if allowedEmpty && !blockedEmpty {
			// Delete allowed_domains if it's empty and blocked is not
			shouldDelete = "allowed_domains"
		} else if blockedEmpty && !allowedEmpty {
			// Delete blocked_domains if it's empty and allowed is not
			shouldDelete = "blocked_domains"
		} else {
			// Both are filled or both are empty: delete blocked_domains
			shouldDelete = "blocked_domains"
		}

		delete(toolArgs, shouldDelete)

		// Re-marshal the sanitized arguments
		if sanitizedBytes, err := json.Marshal(toolArgs); err == nil {
			return string(sanitizedBytes)
		}
	}

	return argumentsJSON
}

// attachWebSearchSourcesToCall finds a web_search_call by tool_use_id and attaches sources to it.
// It searches backwards through bifrostMessages to find the matching call and updates its action.
func attachWebSearchSourcesToCall(bifrostMessages []schemas.ResponsesMessage, toolUseID string, resultBlock AnthropicContentBlock, includeExtendedFields bool) {
	// Search backwards to find matching web_search_call
	for i := len(bifrostMessages) - 1; i >= 0; i-- {
		msg := &bifrostMessages[i]
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeWebSearchCall &&
			msg.ID != nil &&
			*msg.ID == toolUseID {

			if msg.ResponsesToolMessage == nil {
				msg.ResponsesToolMessage = &schemas.ResponsesToolMessage{}
			}

			// Found the matching web_search_call, add sources
			if resultBlock.Content != nil && len(resultBlock.Content.ContentBlocks) > 0 {
				sources := extractWebSearchSources(resultBlock.Content.ContentBlocks, includeExtendedFields)

				// Initialize action if needed
				if msg.ResponsesToolMessage.Action == nil {
					msg.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{}
				}
				if msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction == nil {
					msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction = &schemas.ResponsesWebSearchToolCallAction{
						Type: "search",
					}
				}
				msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Sources = sources
			}
			break
		}
	}
}

// extractWebSearchSources extracts search sources from Anthropic content blocks.
// When includeExtendedFields is true, it includes EncryptedContent, PageAge, and Title fields.
func extractWebSearchSources(contentBlocks []AnthropicContentBlock, includeExtendedFields bool) []schemas.ResponsesWebSearchToolCallActionSearchSource {
	sources := make([]schemas.ResponsesWebSearchToolCallActionSearchSource, 0, len(contentBlocks))

	for _, result := range contentBlocks {
		if result.Type == AnthropicContentBlockTypeWebSearchResult && result.URL != nil {
			source := schemas.ResponsesWebSearchToolCallActionSearchSource{
				Type: "url",
				URL:  *result.URL,
			}

			if includeExtendedFields {
				source.EncryptedContent = result.EncryptedContent
				source.PageAge = result.PageAge

				if result.Title != nil {
					source.Title = result.Title
				} else {
					source.Title = schemas.Ptr(*result.URL)
				}
			}

			sources = append(sources, source)
		}
	}

	return sources
}

// anthropicExtractInt64 extracts an int64 from various numeric types
func anthropicExtractInt64(v interface{}) (int64, bool) {
	switch val := v.(type) {
	case int:
		return int64(val), true
	case int64:
		return val, true
	case float64:
		return int64(val), true
	case float32:
		return int64(val), true
	default:
		return 0, false
	}
}

// anthropicExtractFloat64 extracts a float64 from various numeric types
func anthropicExtractFloat64(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	default:
		return 0, false
	}
}

// IsClaudeCodeMaxMode checks if the request is a Claude Code max mode request.
// In the max mode - we don't need to forward the key
func IsClaudeCodeMaxMode(ctx *schemas.BifrostContext) bool {
	userAgent, _ := ctx.Value(schemas.BifrostContextKeyUserAgent).(string)
	skipKeySelection, _ := ctx.Value(schemas.BifrostContextKeySkipKeySelection).(bool)
	return strings.Contains(strings.ToLower(userAgent), "claude-cli") && skipKeySelection
}

// IsClaudeCodeRequest checks if the request is a Claude Code request.
func IsClaudeCodeRequest(ctx *schemas.BifrostContext) bool {
	if userAgent, ok := ctx.Value(schemas.BifrostContextKeyUserAgent).(string); ok {
		return strings.Contains(strings.ToLower(userAgent), "claude-cli")
	}
	return false
}
