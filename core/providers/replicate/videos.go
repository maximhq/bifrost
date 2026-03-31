package replicate

import (
	"fmt"
	"strconv"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func ToReplicateVideoGenerationInput(bifrostReq *schemas.BifrostVideoGenerationRequest) (*ReplicatePredictionRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil, fmt.Errorf("bifrost request or input is nil")
	}

	input := &ReplicatePredictionRequestInput{
		Prompt: &bifrostReq.Input.Prompt,
	}

	if bifrostReq.Input.InputReference != nil {
		// convert input reference to base64
		// if provider is openai, set input reference to base64
		sanitizedURL, err := schemas.SanitizeImageURL(*bifrostReq.Input.InputReference)
		if err != nil {
			return nil, fmt.Errorf("invalid input reference: %w", err)
		}
		if strings.HasPrefix(bifrostReq.Model, string(schemas.OpenAI)) {
			input.InputReference = schemas.Ptr(sanitizedURL)
		} else {
			input.Image = schemas.Ptr(sanitizedURL)
		}
	}

	// Map parameters if available
	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		if params.Seconds != nil {
			seconds, err := strconv.Atoi(*params.Seconds)
			if err != nil {
				return nil, fmt.Errorf("invalid seconds value: %w", err)
			}
			input.Duration = &seconds
		}

		if params.Seed != nil {
			input.Seed = params.Seed
		}

		if params.NegativePrompt != nil {
			input.NegativePrompt = params.NegativePrompt
		}

		if params.ExtraParams != nil {
			input.ExtraParams = params.ExtraParams
		}
	}

	request := &ReplicatePredictionRequest{
		Input: input,
	}

	// Check if model is a version ID and set version field accordingly
	if isVersionID(bifrostReq.Model) {
		request.Version = &bifrostReq.Model
	}

	if bifrostReq.Params != nil && bifrostReq.Params.ExtraParams != nil {
		request.ExtraParams = bifrostReq.Params.ExtraParams
		if webhook, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["webhook"]); ok {
			delete(request.ExtraParams, "webhook")
			request.Webhook = webhook
		}
		if webhookEventsFilter, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["webhook_events_filter"]); ok {
			delete(request.ExtraParams, "webhook_events_filter")
			request.WebhookEventsFilter = webhookEventsFilter
		}
	}

	return request, nil
}

func ToBifrostVideoGenerationResponse(prediction *ReplicatePredictionResponse) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	if prediction == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "prediction response is nil",
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				Provider: schemas.Replicate,
			},
		}
	}

	response := &schemas.BifrostVideoGenerationResponse{
		ID:        prediction.ID,
		CreatedAt: ParseReplicateTimestamp(prediction.CreatedAt),
		Model:     prediction.Model,
		Object:    "video",
	}

	// Map Replicate status to Bifrost video status.
	switch prediction.Status {
	case ReplicatePredictionStatusStarting:
		response.Status = schemas.VideoStatusQueued
	case ReplicatePredictionStatusProcessing:
		response.Status = schemas.VideoStatusInProgress
	case ReplicatePredictionStatusSucceeded:
		response.Status = schemas.VideoStatusCompleted
	case ReplicatePredictionStatusFailed, ReplicatePredictionStatusCanceled:
		response.Status = schemas.VideoStatusFailed
	default:
		response.Status = schemas.VideoStatusQueued
	}

	// Surface provider error details on failed terminal states.
	if response.Status == schemas.VideoStatusFailed {
		errorMsg := "prediction failed"
		errorCode := string(prediction.Status)
		if prediction.Error != nil && *prediction.Error != "" {
			errorMsg = *prediction.Error
		}
		response.Error = &schemas.VideoCreateError{
			Code:    errorCode,
			Message: errorMsg,
		}
	}

	if prediction.CompletedAt != nil {
		response.CompletedAt = schemas.Ptr(ParseReplicateTimestamp(*prediction.CompletedAt))
	}

	// Convert output to ImageData
	// Replicate output can be either a string (single URL) or array of strings
	if prediction.Output != nil {
		if prediction.Output.OutputStr != nil && *prediction.Output.OutputStr != "" {
			response.Videos = append(response.Videos, schemas.VideoOutput{
				Type:        schemas.VideoOutputTypeURL,
				URL:         schemas.Ptr(*prediction.Output.OutputStr),
				ContentType: "video/mp4",
			})
		} else if len(prediction.Output.OutputArray) > 0 {
			for _, url := range prediction.Output.OutputArray {
				response.Videos = append(response.Videos, schemas.VideoOutput{
					Type:        schemas.VideoOutputTypeURL,
					URL:         schemas.Ptr(url),
					ContentType: "video/mp4",
				})
			}
		}
	}

	return response, nil
}
