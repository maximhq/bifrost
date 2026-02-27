package cohere

import "github.com/maximhq/bifrost/core/schemas"

var (
	// Maps provider-specific finish reasons to Bifrost format
	cohereFinishReasonToBifrost = map[CohereFinishReason]string{
		FinishReasonComplete:     "stop",
		FinishReasonStopSequence: "stop",
		FinishReasonMaxTokens:    "length",
		FinishReasonToolCall:     "tool_calls",
	}
)

// ConvertCohereFinishReasonToBifrost converts provider finish reasons to Bifrost format
func ConvertCohereFinishReasonToBifrost(providerReason CohereFinishReason) string {
	if bifrostReason, ok := cohereFinishReasonToBifrost[providerReason]; ok {
		return bifrostReason
	}
	return string(providerReason)
}

// convertInterfaceToToolFunctionParameters converts an interface{} to ToolFunctionParameters
// This handles the conversion from Cohere's flexible parameter format to Bifrost's structured format
func convertInterfaceToToolFunctionParameters(params interface{}) *schemas.ToolFunctionParameters {
	if params == nil {
		return nil
	}

	// Try to convert from map[string]interface{}
	paramsMap, ok := params.(map[string]interface{})
	if !ok {
		return nil
	}

	result := &schemas.ToolFunctionParameters{}

	// Extract type
	if typeVal, ok := paramsMap["type"].(string); ok {
		result.Type = typeVal
	}

	// Extract description
	if descVal, ok := paramsMap["description"].(string); ok {
		result.Description = &descVal
	}

	// Extract required
	if requiredVal, ok := paramsMap["required"].([]interface{}); ok {
		required := make([]string, 0, len(requiredVal))
		for _, v := range requiredVal {
			if s, ok := v.(string); ok {
				required = append(required, s)
			}
		}
		result.Required = required
	}

	// Extract properties
	if orderedProps, ok := schemas.SafeExtractOrderedMap(paramsMap["properties"]); ok {
		result.Properties = orderedProps
	}

	// Extract enum
	if enumVal, ok := paramsMap["enum"].([]interface{}); ok {
		enum := make([]string, 0, len(enumVal))
		for _, v := range enumVal {
			if s, ok := v.(string); ok {
				enum = append(enum, s)
			}
		}
		result.Enum = enum
	}

	// Extract additionalProperties
	if addPropsVal, ok := paramsMap["additionalProperties"].(bool); ok {
		result.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
			AdditionalPropertiesBool: &addPropsVal,
		}
	}

	if addPropsVal, ok := schemas.SafeExtractOrderedMap(paramsMap["additionalProperties"]); ok {
		result.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
			AdditionalPropertiesMap: addPropsVal,
		}
	}

	// Extract $defs (JSON Schema draft 2019-09+)
	if defsVal, ok := schemas.SafeExtractOrderedMap(paramsMap["$defs"]); ok {
		result.Defs = defsVal
	}

	// Extract definitions (legacy JSON Schema draft-07)
	if defsVal, ok := schemas.SafeExtractOrderedMap(paramsMap["definitions"]); ok {
		result.Definitions = defsVal
	}

	// Extract $ref
	if refVal, ok := paramsMap["$ref"].(string); ok {
		result.Ref = &refVal
	}

	// Extract items (array element schema)
	if itemsVal, ok := schemas.SafeExtractOrderedMap(paramsMap["items"]); ok {
		result.Items = itemsVal
	}

	// Extract minItems
	if minItemsVal, ok := extractInt64(paramsMap["minItems"]); ok {
		result.MinItems = &minItemsVal
	}

	// Extract maxItems
	if maxItemsVal, ok := extractInt64(paramsMap["maxItems"]); ok {
		result.MaxItems = &maxItemsVal
	}

	// Extract anyOf
	if anyOfVal, ok := paramsMap["anyOf"].([]interface{}); ok {
		anyOf := make([]schemas.OrderedMap, 0, len(anyOfVal))
		for _, v := range anyOfVal {
			if m, ok := schemas.SafeExtractOrderedMap(v); ok {
				anyOf = append(anyOf, *m)
			}
		}
		result.AnyOf = anyOf
	}

	// Extract oneOf
	if oneOfVal, ok := paramsMap["oneOf"].([]interface{}); ok {
		oneOf := make([]schemas.OrderedMap, 0, len(oneOfVal))
		for _, v := range oneOfVal {
			if m, ok := schemas.SafeExtractOrderedMap(v); ok {
				oneOf = append(oneOf, *m)
			}
		}
		result.OneOf = oneOf
	}

	// Extract allOf
	if allOfVal, ok := paramsMap["allOf"].([]interface{}); ok {
		allOf := make([]schemas.OrderedMap, 0, len(allOfVal))
		for _, v := range allOfVal {
			if m, ok := schemas.SafeExtractOrderedMap(v); ok {
				allOf = append(allOf, *m)
			}
		}
		result.AllOf = allOf
	}

	// Extract format
	if formatVal, ok := paramsMap["format"].(string); ok {
		result.Format = &formatVal
	}

	// Extract pattern
	if patternVal, ok := paramsMap["pattern"].(string); ok {
		result.Pattern = &patternVal
	}

	// Extract minLength
	if minLengthVal, ok := extractInt64(paramsMap["minLength"]); ok {
		result.MinLength = &minLengthVal
	}

	// Extract maxLength
	if maxLengthVal, ok := extractInt64(paramsMap["maxLength"]); ok {
		result.MaxLength = &maxLengthVal
	}

	// Extract minimum
	if minVal, ok := extractFloat64(paramsMap["minimum"]); ok {
		result.Minimum = &minVal
	}

	// Extract maximum
	if maxVal, ok := extractFloat64(paramsMap["maximum"]); ok {
		result.Maximum = &maxVal
	}

	// Extract title
	if titleVal, ok := paramsMap["title"].(string); ok {
		result.Title = &titleVal
	}

	// Extract default
	if defaultVal, exists := paramsMap["default"]; exists {
		result.Default = defaultVal
	}

	// Extract nullable
	if nullableVal, ok := paramsMap["nullable"].(bool); ok {
		result.Nullable = &nullableVal
	}

	return result
}

// extractInt64 extracts an int64 from various numeric types
func extractInt64(v interface{}) (int64, bool) {
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

// extractFloat64 extracts a float64 from various numeric types
func extractFloat64(v interface{}) (float64, bool) {
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

// ConvertResponseFormatToCohere converts OpenAI-style response_format (interface{}) to Cohere's typed format
// Input can be a map with structure: { type: "json_schema", json_schema: { schema: {...} } }
// Output: CohereResponseFormat with flat structure: { type: "json_object", json_schema: {...} }
func convertResponseFormatToCohere(responseFormat *interface{}) *CohereResponseFormat {
	if responseFormat == nil {
		return nil
	}

	// Try to extract as map
	formatMap, ok := (*responseFormat).(map[string]interface{})
	if !ok {
		return nil
	}

	cohereFormat := &CohereResponseFormat{}

	// Extract type
	typeStr, _ := formatMap["type"].(string)
	switch typeStr {
	case "text":
		cohereFormat.Type = ResponseFormatTypeText
	case "json_object", "json_schema":
		cohereFormat.Type = ResponseFormatTypeJSONObject

		// Extract the nested schema
		// OpenAI format: { type: "json_schema", json_schema: { name: "X", strict: true, schema: {...} } }
		if jsonSchemaWrapper, ok := formatMap["json_schema"].(map[string]interface{}); ok {
			if schema, ok := jsonSchemaWrapper["schema"].(map[string]interface{}); ok {
				var schemaInterface interface{} = schema
				cohereFormat.JSONSchema = &schemaInterface
			}
		}
	default:
		return nil
	}

	return cohereFormat
}

// convertCohereResponseFormatToBifrost converts Cohere's typed response_format back to interface{}
func convertCohereResponseFormatToBifrost(cohereFormat *CohereResponseFormat) *interface{} {
	if cohereFormat == nil {
		return nil
	}

	result := make(map[string]interface{})

	if cohereFormat.JSONSchema != nil {
		result["type"] = "json_schema"
		result["json_schema"] = *cohereFormat.JSONSchema
	} else {
		result["type"] = string(cohereFormat.Type)
	}

	var resultInterface interface{} = result
	return &resultInterface
}
