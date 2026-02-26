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
				delete(request.Params.ExtraParams, "do_sample")
				genParams.DoSample = &val
			}
			if v, ok := schemas.SafeExtractIntPointer(request.Params.ExtraParams["max_new_tokens"]); ok {
				delete(request.Params.ExtraParams, "max_new_tokens")
				genParams.MaxNewTokens = v
			}
			if v, ok := schemas.SafeExtractIntPointer(request.Params.ExtraParams["max_length"]); ok {
				delete(request.Params.ExtraParams, "max_length")
				genParams.MaxLength = v
			}
			if v, ok := schemas.SafeExtractIntPointer(request.Params.ExtraParams["min_length"]); ok {
				delete(request.Params.ExtraParams, "min_length")
				genParams.MinLength = v
			}
			if v, ok := schemas.SafeExtractIntPointer(request.Params.ExtraParams["min_new_tokens"]); ok {
				delete(request.Params.ExtraParams, "min_new_tokens")
				genParams.MinNewTokens = v
			}
			if v, ok := schemas.SafeExtractIntPointer(request.Params.ExtraParams["num_beams"]); ok {
				delete(request.Params.ExtraParams, "num_beams")
				genParams.NumBeams = v
			}
			if v, ok := schemas.SafeExtractIntPointer(request.Params.ExtraParams["num_beam_groups"]); ok {
				delete(request.Params.ExtraParams, "num_beam_groups")
				genParams.NumBeamGroups = v
			}
			if val, ok := request.Params.ExtraParams["penalty_alpha"].(float64); ok {
				delete(request.Params.ExtraParams, "penalty_alpha")
				genParams.PenaltyAlpha = &val
			}
			if val, ok := request.Params.ExtraParams["temperature"].(float64); ok {
				delete(request.Params.ExtraParams, "temperature")
				genParams.Temperature = &val
			}
			if v, ok := schemas.SafeExtractIntPointer(request.Params.ExtraParams["top_k"]); ok {
				delete(request.Params.ExtraParams, "top_k")
				genParams.TopK = v
			}
			if val, ok := request.Params.ExtraParams["top_p"].(float64); ok {
				delete(request.Params.ExtraParams, "top_p")
				genParams.TopP = &val
			}
			if val, ok := request.Params.ExtraParams["typical_p"].(float64); ok {
				delete(request.Params.ExtraParams, "typical_p")
				genParams.TypicalP = &val
			}
			if val, ok := request.Params.ExtraParams["use_cache"].(bool); ok {
				delete(request.Params.ExtraParams, "use_cache")
				genParams.UseCache = &val
			}
			if val, ok := request.Params.ExtraParams["epsilon_cutoff"].(float64); ok {
				delete(request.Params.ExtraParams, "epsilon_cutoff")
				genParams.EpsilonCutoff = &val
			}
			if val, ok := request.Params.ExtraParams["eta_cutoff"].(float64); ok {
				delete(request.Params.ExtraParams, "eta_cutoff")
				genParams.EtaCutoff = &val
			}

			// Handle early_stopping (can be bool or string "never")
			if val, ok := request.Params.ExtraParams["early_stopping"].(bool); ok {
				delete(request.Params.ExtraParams, "early_stopping")
				genParams.EarlyStopping = &HuggingFaceTranscriptionEarlyStopping{BoolValue: &val}
			} else if val, ok := request.Params.ExtraParams["early_stopping"].(string); ok {
				delete(request.Params.ExtraParams, "early_stopping")
				genParams.EarlyStopping = &HuggingFaceTranscriptionEarlyStopping{StringValue: &val}
			}

			hfRequest.Parameters.GenerationParameters = genParams
		}
	}
	hfRequest.ExtraParams = request.Params.ExtraParams

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
	bifrostResponse := schemas.AcquireBifrostSpeechResponse()
	bifrostResponse.Audio = audioData
	bifrostResponse.ExtraFields = schemas.BifrostResponseExtraFields{
		Provider:       schemas.HuggingFace,
		ModelRequested: requestedModel,
	}

	// Note: HuggingFace TTS API typically doesn't return usage information
	// or alignment data, so we leave those fields as nil

	return bifrostResponse, nil
}
