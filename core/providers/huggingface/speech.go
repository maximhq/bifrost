package huggingface

import (
	"fmt"
	"maps"

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

	// Map parameters if present. The keys consumed below are taken from a copy so they do not
	// disappear from the caller's ExtraParams.
	var extraParams map[string]interface{}
	if request.Params != nil {
		hfRequest.Parameters = &HuggingFaceSpeechParameters{}

		extraParams = maps.Clone(request.Params.ExtraParams)

		// Map generation parameters from ExtraParams if available
		if extraParams != nil {
			genParams := &HuggingFaceTranscriptionGenerationParameters{}

			if val, ok := extraParams["do_sample"].(bool); ok {
				delete(extraParams, "do_sample")
				genParams.DoSample = &val
			}
			if v, ok := schemas.SafeExtractIntPointer(extraParams["max_new_tokens"]); ok {
				delete(extraParams, "max_new_tokens")
				genParams.MaxNewTokens = v
			}
			if v, ok := schemas.SafeExtractIntPointer(extraParams["max_length"]); ok {
				delete(extraParams, "max_length")
				genParams.MaxLength = v
			}
			if v, ok := schemas.SafeExtractIntPointer(extraParams["min_length"]); ok {
				delete(extraParams, "min_length")
				genParams.MinLength = v
			}
			if v, ok := schemas.SafeExtractIntPointer(extraParams["min_new_tokens"]); ok {
				delete(extraParams, "min_new_tokens")
				genParams.MinNewTokens = v
			}
			if v, ok := schemas.SafeExtractIntPointer(extraParams["num_beams"]); ok {
				delete(extraParams, "num_beams")
				genParams.NumBeams = v
			}
			if v, ok := schemas.SafeExtractIntPointer(extraParams["num_beam_groups"]); ok {
				delete(extraParams, "num_beam_groups")
				genParams.NumBeamGroups = v
			}
			if val, ok := extraParams["penalty_alpha"].(float64); ok {
				delete(extraParams, "penalty_alpha")
				genParams.PenaltyAlpha = &val
			}
			if val, ok := extraParams["temperature"].(float64); ok {
				delete(extraParams, "temperature")
				genParams.Temperature = &val
			}
			if v, ok := schemas.SafeExtractIntPointer(extraParams["top_k"]); ok {
				delete(extraParams, "top_k")
				genParams.TopK = v
			}
			if val, ok := extraParams["top_p"].(float64); ok {
				delete(extraParams, "top_p")
				genParams.TopP = &val
			}
			if val, ok := extraParams["typical_p"].(float64); ok {
				delete(extraParams, "typical_p")
				genParams.TypicalP = &val
			}
			if val, ok := extraParams["use_cache"].(bool); ok {
				delete(extraParams, "use_cache")
				genParams.UseCache = &val
			}
			if val, ok := extraParams["epsilon_cutoff"].(float64); ok {
				delete(extraParams, "epsilon_cutoff")
				genParams.EpsilonCutoff = &val
			}
			if val, ok := extraParams["eta_cutoff"].(float64); ok {
				delete(extraParams, "eta_cutoff")
				genParams.EtaCutoff = &val
			}

			// Handle early_stopping (can be bool or string "never")
			if val, ok := extraParams["early_stopping"].(bool); ok {
				delete(extraParams, "early_stopping")
				genParams.EarlyStopping = &HuggingFaceTranscriptionEarlyStopping{BoolValue: &val}
			} else if val, ok := extraParams["early_stopping"].(string); ok {
				delete(extraParams, "early_stopping")
				genParams.EarlyStopping = &HuggingFaceTranscriptionEarlyStopping{StringValue: &val}
			}

			hfRequest.Parameters.GenerationParameters = genParams
		}
	}
	hfRequest.ExtraParams = extraParams

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
	}

	// Note: HuggingFace TTS API typically doesn't return usage information
	// or alignment data, so we leave those fields as nil

	return bifrostResponse, nil
}
