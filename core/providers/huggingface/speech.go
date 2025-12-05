package huggingface

import (
	"fmt"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func ToHuggingFaceSpeechRequest(request *schemas.BifrostSpeechRequest) (*HuggingFaceSpeechRequest, error) {
	if request == nil {
		return nil, nil
	}

	if request.Input == nil {
		return nil, fmt.Errorf("speech request input cannot be nil")
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(request.Model)
	if nameErr != nil {
		return nil, nameErr
	}

	// HuggingFace expects text in the Text field (for TTS - Text To Speech)
	hfRequest := &HuggingFaceSpeechRequest{
		Text:     request.Input.Input,
		Model:    modelName,
		Provider: string(inferenceProvider),
	}

	// Map parameters if present
	if request.Params != nil {
		hfRequest.Parameters = &HuggingFaceSpeechParameters{}

		// Map generation parameters from ExtraParams if available
		if request.Params.ExtraParams != nil {
			genParams := &HuggingFaceTranscriptionGenerationParameters{}

			if val, ok := request.Params.ExtraParams["do_sample"].(bool); ok {
				genParams.DoSample = &val
			}
			if v, ok := schemas.SafeExtractIntPointer(request.Params.ExtraParams["max_new_tokens"]); ok {
				genParams.MaxNewTokens = v
			}
			if v, ok := schemas.SafeExtractIntPointer(request.Params.ExtraParams["max_length"]); ok {
				genParams.MaxLength = v
			}
			if v, ok := schemas.SafeExtractIntPointer(request.Params.ExtraParams["min_length"]); ok {
				genParams.MinLength = v
			}
			if v, ok := schemas.SafeExtractIntPointer(request.Params.ExtraParams["min_new_tokens"]); ok {
				genParams.MinNewTokens = v
			}
			if v, ok := schemas.SafeExtractIntPointer(request.Params.ExtraParams["num_beams"]); ok {
				genParams.NumBeams = v
			}
			if v, ok := schemas.SafeExtractIntPointer(request.Params.ExtraParams["num_beam_groups"]); ok {
				genParams.NumBeamGroups = v
			}
			if val, ok := request.Params.ExtraParams["penalty_alpha"].(float64); ok {
				genParams.PenaltyAlpha = &val
			}
			if val, ok := request.Params.ExtraParams["temperature"].(float64); ok {
				genParams.Temperature = &val
			}
			if v, ok := schemas.SafeExtractIntPointer(request.Params.ExtraParams["top_k"]); ok {
				genParams.TopK = v
			}
			if val, ok := request.Params.ExtraParams["top_p"].(float64); ok {
				genParams.TopP = &val
			}
			if val, ok := request.Params.ExtraParams["typical_p"].(float64); ok {
				genParams.TypicalP = &val
			}
			if val, ok := request.Params.ExtraParams["use_cache"].(bool); ok {
				genParams.UseCache = &val
			}
			if val, ok := request.Params.ExtraParams["epsilon_cutoff"].(float64); ok {
				genParams.EpsilonCutoff = &val
			}
			if val, ok := request.Params.ExtraParams["eta_cutoff"].(float64); ok {
				genParams.EtaCutoff = &val
			}

			// Handle early_stopping (can be bool or string "never")
			if val, ok := request.Params.ExtraParams["early_stopping"].(bool); ok {
				genParams.EarlyStopping = &HuggingFaceTranscriptionEarlyStopping{BoolValue: &val}
			} else if val, ok := request.Params.ExtraParams["early_stopping"].(string); ok {
				genParams.EarlyStopping = &HuggingFaceTranscriptionEarlyStopping{StringValue: &val}
			}

			hfRequest.Parameters.GenerationParameters = genParams
		}
	}

	return hfRequest, nil
}

func (response *HuggingFaceSpeechResponse) ToBifrostSpeechResponse(requestedModel string, audioData []byte) (*schemas.BifrostSpeechResponse, error) {
	if response == nil {
		return nil, nil
	}

	if requestedModel == "" {
		return nil, fmt.Errorf("model name cannot be empty")
	}

	// Create the base Bifrost response with the downloaded audio data
	bifrostResponse := &schemas.BifrostSpeechResponse{
		Audio: audioData,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:       schemas.HuggingFace,
			ModelRequested: requestedModel,
		},
	}

	// Note: HuggingFace TTS API typically doesn't return usage information
	// or alignment data, so we leave those fields as nil

	return bifrostResponse, nil
}
