package compat

import (
	"maps"
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
)

func cloneBifrostReq(req *schemas.BifrostRequest) *schemas.BifrostRequest {
	if req == nil {
		return nil
	}

	cloned := *req

	if req.TextCompletionRequest != nil && req.TextCompletionRequest.Params != nil {
		cloned.TextCompletionRequest.Params = cloneTextCompletionParameters(req.TextCompletionRequest.Params)
	}
	if req.ChatRequest != nil && req.ChatRequest.Params != nil {
		cloned.ChatRequest.Params = cloneChatParameters(req.ChatRequest.Params)
	}
	if req.ResponsesRequest != nil && req.ResponsesRequest.Params != nil {
		cloned.ResponsesRequest.Params = cloneResponsesParameters(req.ResponsesRequest.Params)
	}

	return &cloned
}

func cloneTextCompletionParameters(params *schemas.TextCompletionParameters) *schemas.TextCompletionParameters {
	if params == nil {
		return nil
	}
	cloned := *params
	if params.LogitBias != nil {
		logitBias := cloneStringFloat64Map(*params.LogitBias)
		cloned.LogitBias = &logitBias
	}
	if params.Stop != nil {
		cloned.Stop = slices.Clone(params.Stop)
	}
	if params.StreamOptions != nil {
		streamOptions := *params.StreamOptions
		cloned.StreamOptions = &streamOptions
	}
	if params.ExtraParams != nil {
		cloned.ExtraParams = cloneAnyMap(params.ExtraParams)
	}
	return &cloned
}

func cloneChatParameters(params *schemas.ChatParameters) *schemas.ChatParameters {
	if params == nil {
		return nil
	}

	cloned := *params
	if params.Audio != nil {
		audio := *params.Audio
		cloned.Audio = &audio
	}
	if params.LogitBias != nil {
		logitBias := cloneStringFloat64Map(*params.LogitBias)
		cloned.LogitBias = &logitBias
	}
	if params.Metadata != nil {
		metadata := cloneAnyMap(*params.Metadata)
		cloned.Metadata = &metadata
	}
	if params.Modalities != nil {
		cloned.Modalities = slices.Clone(params.Modalities)
	}
	if params.Prediction != nil {
		prediction := *params.Prediction
		prediction.Content = cloneAnyValue(params.Prediction.Content)
		cloned.Prediction = &prediction
	}
	if params.Reasoning != nil {
		reasoning := *params.Reasoning
		cloned.Reasoning = &reasoning
	}
	if params.ResponseFormat != nil {
		responseFormat := cloneAnyValue(*params.ResponseFormat)
		cloned.ResponseFormat = &responseFormat
	}
	if params.StreamOptions != nil {
		streamOptions := *params.StreamOptions
		cloned.StreamOptions = &streamOptions
	}
	if params.Stop != nil {
		cloned.Stop = slices.Clone(params.Stop)
	}
	if params.ToolChoice != nil {
		cloned.ToolChoice = cloneChatToolChoice(params.ToolChoice)
	}
	if params.Tools != nil {
		cloned.Tools = make([]schemas.ChatTool, len(params.Tools))
		for i, tool := range params.Tools {
			cloned.Tools[i] = schemas.DeepCopyChatTool(tool)
		}
	}
	if params.WebSearchOptions != nil {
		cloned.WebSearchOptions = cloneChatWebSearchOptions(params.WebSearchOptions)
	}
	if params.ExtraParams != nil {
		cloned.ExtraParams = cloneAnyMap(params.ExtraParams)
	}
	return &cloned
}

func cloneChatToolChoice(choice *schemas.ChatToolChoice) *schemas.ChatToolChoice {
	if choice == nil {
		return nil
	}

	cloned := &schemas.ChatToolChoice{}
	if choice.ChatToolChoiceStr != nil {
		value := *choice.ChatToolChoiceStr
		cloned.ChatToolChoiceStr = &value
	}
	if choice.ChatToolChoiceStruct != nil {
		choiceStruct := *choice.ChatToolChoiceStruct
		if choice.ChatToolChoiceStruct.Function != nil {
			function := *choice.ChatToolChoiceStruct.Function
			choiceStruct.Function = &function
		}
		if choice.ChatToolChoiceStruct.Custom != nil {
			custom := *choice.ChatToolChoiceStruct.Custom
			choiceStruct.Custom = &custom
		}
		if choice.ChatToolChoiceStruct.AllowedTools != nil {
			allowedTools := *choice.ChatToolChoiceStruct.AllowedTools
			allowedTools.Tools = slices.Clone(choice.ChatToolChoiceStruct.AllowedTools.Tools)
			choiceStruct.AllowedTools = &allowedTools
		}
		cloned.ChatToolChoiceStruct = &choiceStruct
	}
	return cloned
}

func cloneChatWebSearchOptions(options *schemas.ChatWebSearchOptions) *schemas.ChatWebSearchOptions {
	if options == nil {
		return nil
	}

	cloned := *options
	if options.UserLocation != nil {
		userLocation := *options.UserLocation
		if options.UserLocation.Approximate != nil {
			approximate := *options.UserLocation.Approximate
			userLocation.Approximate = &approximate
		}
		cloned.UserLocation = &userLocation
	}
	return &cloned
}

func cloneResponsesParameters(params *schemas.ResponsesParameters) *schemas.ResponsesParameters {
	if params == nil {
		return nil
	}

	cloned := *params
	if params.Include != nil {
		cloned.Include = slices.Clone(params.Include)
	}
	if params.Metadata != nil {
		metadata := cloneAnyMap(*params.Metadata)
		cloned.Metadata = &metadata
	}
	if params.Reasoning != nil {
		reasoning := *params.Reasoning
		cloned.Reasoning = &reasoning
	}
	if params.StreamOptions != nil {
		streamOptions := *params.StreamOptions
		cloned.StreamOptions = &streamOptions
	}
	if params.Text != nil {
		cloned.Text = cloneResponsesTextConfig(params.Text)
	}
	if params.ToolChoice != nil {
		cloned.ToolChoice = cloneResponsesToolChoice(params.ToolChoice)
	}
	if params.Tools != nil {
		cloned.Tools = make([]schemas.ResponsesTool, len(params.Tools))
		for i, tool := range params.Tools {
			cloned.Tools[i] = cloneResponsesTool(tool)
		}
	}
	if params.ExtraParams != nil {
		cloned.ExtraParams = cloneAnyMap(params.ExtraParams)
	}
	return &cloned
}

func cloneResponsesTextConfig(text *schemas.ResponsesTextConfig) *schemas.ResponsesTextConfig {
	if text == nil {
		return nil
	}

	cloned := *text
	if text.Format != nil {
		format := *text.Format
		if text.Format.JSONSchema != nil {
			jsonSchema := *text.Format.JSONSchema
			if text.Format.JSONSchema.Schema != nil {
				schema := cloneAnyValue(*text.Format.JSONSchema.Schema)
				jsonSchema.Schema = &schema
			}
			if text.Format.JSONSchema.Properties != nil {
				properties := cloneAnyMap(*text.Format.JSONSchema.Properties)
				jsonSchema.Properties = &properties
			}
			if text.Format.JSONSchema.Required != nil {
				jsonSchema.Required = slices.Clone(text.Format.JSONSchema.Required)
			}
			if text.Format.JSONSchema.Defs != nil {
				defs := cloneAnyMap(*text.Format.JSONSchema.Defs)
				jsonSchema.Defs = &defs
			}
			if text.Format.JSONSchema.Definitions != nil {
				definitions := cloneAnyMap(*text.Format.JSONSchema.Definitions)
				jsonSchema.Definitions = &definitions
			}
			if text.Format.JSONSchema.Items != nil {
				items := cloneAnyMap(*text.Format.JSONSchema.Items)
				jsonSchema.Items = &items
			}
			if text.Format.JSONSchema.AnyOf != nil {
				jsonSchema.AnyOf = cloneAnyMapSlice(text.Format.JSONSchema.AnyOf)
			}
			if text.Format.JSONSchema.OneOf != nil {
				jsonSchema.OneOf = cloneAnyMapSlice(text.Format.JSONSchema.OneOf)
			}
			if text.Format.JSONSchema.AllOf != nil {
				jsonSchema.AllOf = cloneAnyMapSlice(text.Format.JSONSchema.AllOf)
			}
			if text.Format.JSONSchema.Default != nil {
				jsonSchema.Default = cloneAnyValue(text.Format.JSONSchema.Default)
			}
			if text.Format.JSONSchema.Enum != nil {
				jsonSchema.Enum = slices.Clone(text.Format.JSONSchema.Enum)
			}
			if text.Format.JSONSchema.PropertyOrdering != nil {
				jsonSchema.PropertyOrdering = slices.Clone(text.Format.JSONSchema.PropertyOrdering)
			}
			format.JSONSchema = &jsonSchema
		}
		cloned.Format = &format
	}
	return &cloned
}

func cloneResponsesToolChoice(choice *schemas.ResponsesToolChoice) *schemas.ResponsesToolChoice {
	if choice == nil {
		return nil
	}

	cloned := &schemas.ResponsesToolChoice{}
	if choice.ResponsesToolChoiceStr != nil {
		value := *choice.ResponsesToolChoiceStr
		cloned.ResponsesToolChoiceStr = &value
	}
	if choice.ResponsesToolChoiceStruct != nil {
		choiceStruct := *choice.ResponsesToolChoiceStruct
		if choice.ResponsesToolChoiceStruct.Tools != nil {
			choiceStruct.Tools = slices.Clone(choice.ResponsesToolChoiceStruct.Tools)
		}
		cloned.ResponsesToolChoiceStruct = &choiceStruct
	}
	return cloned
}

func cloneResponsesTool(tool schemas.ResponsesTool) schemas.ResponsesTool {
	data, err := schemas.MarshalSorted(tool)
	if err != nil {
		return tool
	}

	var cloned schemas.ResponsesTool
	if err := schemas.Unmarshal(data, &cloned); err != nil {
		return tool
	}

	return cloned
}

func cloneStringFloat64Map(input map[string]float64) map[string]float64 {
	if input == nil {
		return nil
	}

	cloned := make(map[string]float64, len(input))
	maps.Copy(cloned, input)
	return cloned
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}

	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = cloneAnyValue(value)
	}
	return cloned
}

func cloneAnyMapSlice(input []map[string]any) []map[string]any {
	if input == nil {
		return nil
	}

	cloned := make([]map[string]any, len(input))
	for i, value := range input {
		cloned[i] = cloneAnyMap(value)
	}
	return cloned
}

func cloneAnySlice(input []any) []any {
	if input == nil {
		return nil
	}

	cloned := make([]any, len(input))
	for i, value := range input {
		cloned[i] = cloneAnyValue(value)
	}
	return cloned
}

func cloneAnyValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case map[string]any:
		return cloneAnyMap(typed)
	case []any:
		return cloneAnySlice(typed)
	case []string:
		return slices.Clone(typed)
	case map[string]string:
		cloned := make(map[string]string, len(typed))
		maps.Copy(cloned, typed)
		return cloned
	default:
		return typed
	}
}
