package runway

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func ToRunwayVideoGenerationRequest(bifrostReq *schemas.BifrostVideoGenerationRequest) (*RunwayVideoGenerationRequest, error) {
	// three types of video generation requests in runway api
	// 1. image to video
	// 2. text to video
	// 3. video to video
	if bifrostReq.Input == nil {
		return nil, fmt.Errorf("input is required")
	}

	request := &RunwayVideoGenerationRequest{
		Model: bifrostReq.Model,
		Ratio: schemas.Ptr("1280:720"),
	}

	if isRunwayVeoModel(bifrostReq.Model) {
		request.Duration = schemas.Ptr(4)
	} else if isRunwayGenModel(bifrostReq.Model) {
		request.Duration = schemas.Ptr(2)
	}

	if bifrostReq.Input.Prompt != "" {
		request.PromptText = &bifrostReq.Input.Prompt
	}
	if bifrostReq.Input.InputReference != nil {
		sanitizedURL, err := schemas.SanitizeImageURL(*bifrostReq.Input.InputReference)
		if err != nil {
			return nil, fmt.Errorf("invalid input reference: %w", err)
		}
		request.PromptImage = &PromptImage{
			PromptImageStr: schemas.Ptr(sanitizedURL),
		}
	}

	if bifrostReq.Params != nil {
		if bifrostReq.Params.Seconds != nil {
			seconds, err := strconv.Atoi(*bifrostReq.Params.Seconds)
			if err != nil {
				return nil, fmt.Errorf("invalid seconds value: %w", err)
			}
			request.Duration = &seconds
		}

		if bifrostReq.Params.Size != "" {
			// convert 1280x720 to 1280:720
			request.Ratio = schemas.Ptr(strings.Replace(bifrostReq.Params.Size, "x", ":", 1))
		}

		if isRunwayVeoModel(bifrostReq.Model) {
			if bifrostReq.Params.Audio != nil {
				request.Audio = bifrostReq.Params.Audio
			}
		}

		if isRunwayGenModel(bifrostReq.Model) {
			if bifrostReq.Params.Seed != nil {
				request.Seed = bifrostReq.Params.Seed
			}
		}

		if bifrostReq.Params.VideoURI != nil {
			if !supportsVideoToVideo(bifrostReq.Model) {
				return nil, fmt.Errorf("video_uri is not supported for model %s", bifrostReq.Model)
			}
			request.VideoURI = bifrostReq.Params.VideoURI
		}

		if bifrostReq.Params.ExtraParams != nil {
			request.ExtraParams = bifrostReq.Params.ExtraParams
			// Handle references for video-to-video generation
			if refsVal := bifrostReq.Params.ExtraParams["references"]; refsVal != nil {
				if refs, ok := refsVal.([]Reference); ok && refs != nil {
					request.References = refs
					delete(request.ExtraParams, "references")
				} else if data, err := sonic.Marshal(refsVal); err == nil {
					var refs []Reference
					if sonic.Unmarshal(data, &refs) == nil {
						request.References = refs
						delete(request.ExtraParams, "references")
					}
				}
			}

			// Handle reference images for video generation
			if refImagesVal := bifrostReq.Params.ExtraParams["reference_images"]; refImagesVal != nil {
				if refImages, ok := refImagesVal.([]ReferenceImage); ok && refImages != nil {
					delete(request.ExtraParams, "reference_images")
					request.ReferenceImages = refImages
				} else if data, err := sonic.Marshal(refImagesVal); err == nil {
					var refImages []ReferenceImage
					if sonic.Unmarshal(data, &refImages) == nil {
						delete(request.ExtraParams, "reference_images")
						request.ReferenceImages = refImages
					}
				}
			}

			// add content moderation
			if isRunwayVeoModel(bifrostReq.Model) {
				if cmVal := bifrostReq.Params.ExtraParams["content_moderation"]; cmVal != nil {
					if cm, ok := cmVal.(*ContentModeration); ok && cm != nil {
						delete(request.ExtraParams, "content_moderation")
						request.ContentModeration = cm
					} else if data, err := sonic.Marshal(cmVal); err == nil {
						var cm ContentModeration
						if sonic.Unmarshal(data, &cm) == nil {
							delete(request.ExtraParams, "content_moderation")
							request.ContentModeration = &cm
						}
					}
				}
			}
		}
	}

	return request, nil
}

// ToBifrostVideoGenerationResponse converts Runway task details to Bifrost video generation response format.
func ToBifrostVideoGenerationResponse(taskDetails *RunwayTaskDetailsResponse) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	if taskDetails == nil {
		return nil, providerUtils.NewBifrostOperationError("task details is nil", nil, schemas.Runway)
	}

	response := &schemas.BifrostVideoGenerationResponse{
		ID:        taskDetails.ID,
		Object:    "video",
		CreatedAt: time.Now().Unix(),
	}

	// Map Runway task status to Bifrost video status
	switch taskDetails.Status {
	case RunwayTaskStatusPending, RunwayTaskStatusThrottled:
		response.Status = schemas.VideoStatusQueued
	case RunwayTaskStatusRunning:
		response.Status = schemas.VideoStatusInProgress
	case RunwayTaskStatusSucceeded:
		response.Status = schemas.VideoStatusCompleted
	case RunwayTaskStatusFailed, RunwayTaskStatusCancelled:
		response.Status = schemas.VideoStatusFailed
		// Set error message for failed tasks
		errorMsg := fmt.Sprintf("Task %s", taskDetails.Status)
		response.Error = &schemas.VideoCreateError{
			Code:    string(taskDetails.Status),
			Message: errorMsg,
		}
	default:
		response.Status = schemas.VideoStatusQueued
	}

	if len(taskDetails.Output) > 0 {
		response.Videos = make([]schemas.VideoOutput, len(taskDetails.Output))
		for i, url := range taskDetails.Output {
			response.Videos[i] = schemas.VideoOutput{
				Type:        schemas.VideoOutputTypeURL,
				URL:         schemas.Ptr(url),
				ContentType: "video/mp4",
			}
		}
	}

	// Parse created_at timestamp if available
	if taskDetails.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, taskDetails.CreatedAt); err == nil {
			response.CreatedAt = t.Unix()
		}
	}

	return response, nil
}
