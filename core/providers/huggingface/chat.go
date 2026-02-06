package huggingface

import (
	"fmt"

	"github.com/bytedance/sonic"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func ToHuggingFaceChatCompletionRequest(bifrostReq *schemas.BifrostChatRequest) (*HuggingFaceChatRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil, nil
	}

	// Create the HuggingFace request
	hfReq := &HuggingFaceChatRequest{
		Messages: bifrostReq.Input,
		Model:    bifrostReq.Model,
	}

	// Map parameters if present
	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		if params.FrequencyPenalty != nil {
			hfReq.FrequencyPenalty = params.FrequencyPenalty
		}
		if params.LogProbs != nil {
			hfReq.Logprobs = params.LogProbs
		}
		if params.MaxCompletionTokens != nil {
			hfReq.MaxTokens = params.MaxCompletionTokens
		}
		if params.PresencePenalty != nil {
			hfReq.PresencePenalty = params.PresencePenalty
		}
		if params.Seed != nil {
			hfReq.Seed = params.Seed
		}
		if len(params.Stop) > 0 {
			hfReq.Stop = params.Stop
		}
		if params.Temperature != nil {
			hfReq.Temperature = params.Temperature
		}
		if params.TopLogProbs != nil {
			hfReq.TopLogprobs = params.TopLogProbs
		}
		if params.TopP != nil {
			hfReq.TopP = params.TopP
		}

		// Handle response format
		if params.ResponseFormat != nil {
			// Convert the response format to HuggingFace format
			responseFormatJSON, err := sonic.Marshal(params.ResponseFormat)
			if err != nil {
				return nil, fmt.Errorf("failed to convert ResponseFormat (marshal): %w", err)
			}
			var hfResponseFormat HuggingFaceResponseFormat
			if err := sonic.Unmarshal(responseFormatJSON, &hfResponseFormat); err != nil {
				return nil, fmt.Errorf("failed to convert ResponseFormat (unmarshal): %w", err)
			}
			hfReq.ResponseFormat = &hfResponseFormat
		}

		// Handle stream options
		if params.StreamOptions != nil {
			hfReq.StreamOptions = &schemas.ChatStreamOptions{
				IncludeUsage: params.StreamOptions.IncludeUsage,
			}
		}

		hfReq.Tools = params.Tools

		// Handle tool choice
		if params.ToolChoice != nil {
			hfToolChoice := &HuggingFaceToolChoice{}
			if params.ToolChoice.ChatToolChoiceStr != nil {
				switch *params.ToolChoice.ChatToolChoiceStr {
				case "auto":
					auto := EnumStringTypeAuto
					hfToolChoice.EnumValue = &auto
				case "none":
					none := EnumStringTypeNone
					hfToolChoice.EnumValue = &none
				case "required":
					required := EnumStringTypeRequired
					hfToolChoice.EnumValue = &required
				}
			} else if params.ToolChoice.ChatToolChoiceStruct != nil {
				if params.ToolChoice.ChatToolChoiceStruct.Type == schemas.ChatToolChoiceTypeFunction {
					hfToolChoice.Function = &schemas.ChatToolChoiceFunction{
						Name: params.ToolChoice.ChatToolChoiceStruct.Function.Name,
					}
				}
			}
			if hfToolChoice.EnumValue != nil || hfToolChoice.Function != nil {
				hfReq.ToolChoice = hfToolChoice
			}
		}
		hfReq.ExtraParams = bifrostReq.Params.ExtraParams
	}

	return hfReq, nil
}
