package huggingface

import (
	"fmt"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func ToHuggingFaceTranscriptionRequest(request *schemas.BifrostTranscriptionRequest) (*HuggingFaceTranscriptionRequest, error) {
	if request == nil {
		return nil, nil
	}

	if request.Input == nil {
		return nil, fmt.Errorf("transcription request input cannot be nil")
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(request.Model)
	if nameErr != nil {
		return nil, nameErr
	}

	// HuggingFace expects audio data in the Inputs field (for ASR - Automatic Speech Recognition)
	hfRequest := &HuggingFaceTranscriptionRequest{
		Inputs:   request.Input.File,
		Model:    modelName,
		Provider: string(inferenceProvider),
	}

	// Map parameters if present
	if request.Params != nil {
		hfRequest.Parameters = &HuggingFaceTranscriptionRequestParameters{}

		// Map generation parameters from ExtraParams if available
		if request.Params.ExtraParams != nil {
			genParams := &HuggingFaceTranscriptionGenerationParameters{}

			if val, ok := request.Params.ExtraParams["do_sample"].(bool); ok {
				genParams.DoSample = &val
			}
			if v, ok := extractIntFromInterface(request.Params.ExtraParams["max_new_tokens"]); ok {
				genParams.MaxNewTokens = &v
			}
			if v, ok := extractIntFromInterface(request.Params.ExtraParams["max_length"]); ok {
				genParams.MaxLength = &v
			}
			if v, ok := extractIntFromInterface(request.Params.ExtraParams["min_length"]); ok {
				genParams.MinLength = &v
			}
			if v, ok := extractIntFromInterface(request.Params.ExtraParams["min_new_tokens"]); ok {
				genParams.MinNewTokens = &v
			}
			if v, ok := extractIntFromInterface(request.Params.ExtraParams["num_beams"]); ok {
				genParams.NumBeams = &v
			}
			if v, ok := extractIntFromInterface(request.Params.ExtraParams["num_beam_groups"]); ok {
				genParams.NumBeamGroups = &v
			}
			if val, ok := request.Params.ExtraParams["penalty_alpha"].(float64); ok {
				genParams.PenaltyAlpha = &val
			}
			if val, ok := request.Params.ExtraParams["temperature"].(float64); ok {
				genParams.Temperature = &val
			}
			if v, ok := extractIntFromInterface(request.Params.ExtraParams["top_k"]); ok {
				genParams.TopK = &v
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

			// Handle return_timestamps
			if val, ok := request.Params.ExtraParams["return_timestamps"].(bool); ok {
				hfRequest.Parameters.ReturnTimestamps = &val
			}
		}
	}

	return hfRequest, nil
}

func (response *HuggingFaceTranscriptionResponse) ToBifrostTranscriptionResponse(requestedModel string) (*schemas.BifrostTranscriptionResponse, error) {
	if response == nil {
		return nil, nil
	}

	if requestedModel == "" {
		return nil, fmt.Errorf("model name cannot be empty")
	}

	// Create the base Bifrost response
	bifrostResponse := &schemas.BifrostTranscriptionResponse{
		Text: response.Text,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:       schemas.HuggingFace,
			ModelRequested: requestedModel,
		},
	}

	// Map chunks to segments if available
	if len(response.Chunks) > 0 {
		segments := make([]schemas.TranscriptionSegment, len(response.Chunks))
		for i, chunk := range response.Chunks {
			var start, end float64
			if len(chunk.Timestamp) >= 2 {
				start = chunk.Timestamp[0]
				end = chunk.Timestamp[1]
			}

			segments[i] = schemas.TranscriptionSegment{
				ID:    i,
				Start: start,
				End:   end,
				Text:  chunk.Text,
			}
		}
		bifrostResponse.Segments = segments
	}

	return bifrostResponse, nil
}
