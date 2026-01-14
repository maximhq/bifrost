package huggingface

import (
	"encoding/base64"
	"fmt"

	"github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func ToHuggingFaceTranscriptionRequest(request *schemas.BifrostTranscriptionRequest) (*HuggingFaceTranscriptionRequest, error) {
	if request == nil {
		return nil, nil
	}

	if request.Input == nil {
		return nil, fmt.Errorf("transcription request input cannot be nil")
	}

	if len(request.Input.File) == 0 {
		return nil, fmt.Errorf("transcription request audio file cannot be empty")
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(request.Model)
	if nameErr != nil {
		return nil, nameErr
	}

	var hfRequest *HuggingFaceTranscriptionRequest
	// HuggingFace expects audio data in the Inputs field (for ASR - Automatic Speech Recognition)
	if inferenceProvider != falAI {
		hfRequest = &HuggingFaceTranscriptionRequest{
			Inputs:   request.Input.File,
			Model:    schemas.Ptr(modelName),
			Provider: schemas.Ptr(string(inferenceProvider)),
		}
	} else {
		encoded := base64.StdEncoding.EncodeToString(request.Input.File)
		mimeType := getMimeTypeForAudioType(utils.DetectAudioMimeType(request.Input.File))
		if mimeType == "audio/wav" {
			return nil, fmt.Errorf("fal-ai provider does not support audio/wav format; please use a different format like mp3 or ogg")
		}
		encoded = fmt.Sprintf("data:%s;base64,%s", mimeType, encoded)
		hfRequest = &HuggingFaceTranscriptionRequest{
			AudioURL: encoded,
		}
	}
	// Map parameters if present
	if request.Params != nil {
		hfRequest.Parameters = &HuggingFaceTranscriptionRequestParameters{}
		genParams := &HuggingFaceTranscriptionGenerationParameters{}

		if v, ok := schemas.SafeExtractIntPointer(request.Params.MaxNewTokens); ok {
			genParams.MaxNewTokens = v
		}
		if v, ok := schemas.SafeExtractIntPointer(request.Params.MaxLength); ok {
			genParams.MaxLength = v
		}
		if v, ok := schemas.SafeExtractIntPointer(request.Params.MinLength); ok {
			genParams.MinLength = v
		}
		if v, ok := schemas.SafeExtractIntPointer(request.Params.MinNewTokens); ok {
			genParams.MinNewTokens = v
		}

		if request.Params.ExtraParams != nil {
			extra := request.Params.ExtraParams
			if val, ok := extra["do_sample"].(bool); ok {
				genParams.DoSample = &val
			}
			if v, ok := schemas.SafeExtractIntPointer(extra["num_beams"]); ok {
				genParams.NumBeams = v
			}
			if v, ok := schemas.SafeExtractIntPointer(extra["num_beam_groups"]); ok {
				genParams.NumBeamGroups = v
			}
			if val, ok := extra["penalty_alpha"].(float64); ok {
				genParams.PenaltyAlpha = &val
			}
			if val, ok := extra["temperature"].(float64); ok {
				genParams.Temperature = &val
			}
			if v, ok := schemas.SafeExtractIntPointer(extra["top_k"]); ok {
				genParams.TopK = v
			}
			if val, ok := extra["top_p"].(float64); ok {
				genParams.TopP = &val
			}
			if val, ok := extra["typical_p"].(float64); ok {
				genParams.TypicalP = &val
			}
			if val, ok := extra["use_cache"].(bool); ok {
				genParams.UseCache = &val
			}
			if val, ok := extra["epsilon_cutoff"].(float64); ok {
				genParams.EpsilonCutoff = &val
			}
			if val, ok := extra["eta_cutoff"].(float64); ok {
				genParams.EtaCutoff = &val
			}

			// Handle early_stopping (can be bool or string "never")
			if val, ok := extra["early_stopping"].(bool); ok {
				genParams.EarlyStopping = &HuggingFaceTranscriptionEarlyStopping{BoolValue: &val}
			} else if val, ok := extra["early_stopping"].(string); ok {
				genParams.EarlyStopping = &HuggingFaceTranscriptionEarlyStopping{StringValue: &val}
			}

			// Handle return_timestamps
			if val, ok := extra["return_timestamps"].(bool); ok {
				hfRequest.Parameters.ReturnTimestamps = &val
			}
		}

		hfRequest.Parameters.GenerationParameters = genParams
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
