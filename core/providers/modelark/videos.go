package modelark

import (
	"fmt"
	"strconv"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func ToModelArkVideoGenerationRequest(bifrostReq *schemas.BifrostVideoGenerationRequest) (*ModelArkVideoGenerationRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil, fmt.Errorf("input is required")
	}
	if bifrostReq.Input.Prompt == "" && bifrostReq.Input.InputReference == nil {
		return nil, fmt.Errorf("prompt or input reference is required")
	}

	// Text-to-video and image-to-video share the same endpoint; an image_url part in
	// the content array is what selects image-to-video.
	content := make([]ModelArkContentPart, 0, 2)
	if bifrostReq.Input.Prompt != "" {
		content = append(content, ModelArkContentPart{
			Type: "text",
			Text: bifrostReq.Input.Prompt,
		})
	}
	if bifrostReq.Input.InputReference != nil {
		sanitizedURL, err := schemas.SanitizeImageURL(*bifrostReq.Input.InputReference)
		if err != nil {
			return nil, fmt.Errorf("invalid input reference: %w", err)
		}
		content = append(content, ModelArkContentPart{
			Type:     "image_url",
			ImageURL: &ModelArkURLRef{URL: sanitizedURL},
		})
	}

	request := &ModelArkVideoGenerationRequest{
		Model:   bifrostReq.Model,
		Content: content,
		Ratio:   schemas.Ptr(defaultVideoRatio),
	}

	if bifrostReq.Params == nil {
		return request, nil
	}

	params := bifrostReq.Params
	if params.Seconds != nil {
		seconds, err := strconv.Atoi(*params.Seconds)
		if err != nil {
			return nil, fmt.Errorf("invalid seconds value: %w", err)
		}
		request.Duration = &seconds
	}

	if resolution, ok := resolutionFromSize(params.Size); ok {
		request.Resolution = &resolution
	}

	if params.Seed != nil {
		request.Seed = params.Seed
	}

	if params.Audio != nil {
		request.GenerateAudio = params.Audio
	}

	if params.ExtraParams != nil {
		request.ExtraParams = params.ExtraParams

		if ratio, ok := schemas.SafeExtractStringPointer(params.ExtraParams["ratio"]); ok {
			delete(request.ExtraParams, "ratio")
			request.Ratio = ratio
		}
		if watermark, ok := schemas.SafeExtractBoolPointer(params.ExtraParams["watermark"]); ok {
			delete(request.ExtraParams, "watermark")
			request.Watermark = watermark
		}
		if callbackURL, ok := schemas.SafeExtractStringPointer(params.ExtraParams["callback_url"]); ok {
			delete(request.ExtraParams, "callback_url")
			request.CallbackURL = callbackURL
		}
	}

	return request, nil
}

// ToBifrostVideoGenerationResponse converts ModelArk task details to Bifrost video generation response format.
func ToBifrostVideoGenerationResponse(taskDetails *ModelArkTaskDetailsResponse) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	if taskDetails == nil {
		return nil, providerUtils.NewBifrostOperationError("task details is nil", nil)
	}

	response := &schemas.BifrostVideoGenerationResponse{
		ID:        taskDetails.ID,
		Model:     taskDetails.Model,
		Object:    "video",
		CreatedAt: taskDetails.CreatedAt,
		Size:      taskDetails.Resolution,
	}

	// Map ModelArk task status to Bifrost video status
	switch taskDetails.Status {
	case ModelArkTaskStatusQueued:
		response.Status = schemas.VideoStatusQueued
	case ModelArkTaskStatusRunning:
		response.Status = schemas.VideoStatusInProgress
	case ModelArkTaskStatusSucceeded:
		response.Status = schemas.VideoStatusCompleted
	case ModelArkTaskStatusFailed, ModelArkTaskStatusCancelled:
		response.Status = schemas.VideoStatusFailed
	default:
		response.Status = schemas.VideoStatusQueued
	}

	if response.Status == schemas.VideoStatusFailed {
		videoErr := &schemas.VideoCreateError{
			Code:    string(taskDetails.Status),
			Message: fmt.Sprintf("Task %s", taskDetails.Status),
		}
		if taskDetails.Error != nil {
			if taskDetails.Error.Code != "" {
				videoErr.Code = taskDetails.Error.Code
			}
			if taskDetails.Error.Message != "" {
				videoErr.Message = taskDetails.Error.Message
			}
		}
		response.Error = videoErr
	}

	// updated_at is the only completion timestamp ModelArk reports, and it stops
	// moving once the task reaches a terminal state.
	if response.Status == schemas.VideoStatusCompleted && taskDetails.UpdatedAt > 0 {
		response.CompletedAt = schemas.Ptr(taskDetails.UpdatedAt)
	}

	if taskDetails.Duration > 0 {
		response.Seconds = schemas.Ptr(strconv.Itoa(taskDetails.Duration))
	}

	if taskDetails.Content != nil && taskDetails.Content.VideoURL != "" {
		response.Videos = []schemas.VideoOutput{
			{
				Type:        schemas.VideoOutputTypeURL,
				URL:         schemas.Ptr(taskDetails.Content.VideoURL),
				ContentType: "video/mp4",
			},
		}
	}

	return response, nil
}
