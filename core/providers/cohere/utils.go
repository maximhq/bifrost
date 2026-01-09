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
		result.Properties = &orderedProps
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
			AdditionalPropertiesMap: &addPropsVal,
		}
	}

	return result
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
