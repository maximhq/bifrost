package runware

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToRunwareVideoGenerationRequest converts a Bifrost video generation request to a Runware
// task. An input reference image turns it into image-to-video generation.
//
// The task type defaults to videoInference but can be overridden via the "taskType" extra_param
// (e.g. "3dInference"), which lets the /videos endpoint drive any Runware async task type that
// shares the submit-then-poll lifecycle. Video-only defaults (width/height) are applied only for
// the video task type; other task types (3D uses "resolution", etc.) supply their own dimensions
// through extra_params.
func ToRunwareVideoGenerationRequest(bifrostReq *schemas.BifrostVideoGenerationRequest) (*RunwareInferenceRequest, error) {
	if bifrostReq.Input == nil {
		return nil, fmt.Errorf("input is required")
	}

	// Resolve the task type from extra_params before building the request; it decides which
	// modality-specific defaults apply below.
	taskType := taskTypeVideoInference
	if bifrostReq.Params != nil {
		if override, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["taskType"]); ok && override != "" {
			taskType = override
		}
	}
	isVideo := taskType == taskTypeVideoInference

	request := &RunwareInferenceRequest{
		TaskType:       taskType,
		TaskUUID:       uuid.New().String(),
		DeliveryMethod: new(deliveryMethodAsync),
		Model:          bifrostReq.Model,
	}

	// Runware requires explicit width/height for video and rejects square sizes on some models;
	// default to 16:9 1080p when no size is given. Non-video task types do not use width/height.
	if isVideo {
		request.Width = new(defaultRunwareVideoWidth)
		request.Height = new(defaultRunwareVideoHeight)
	}

	if bifrostReq.Input.Prompt != "" {
		request.PositivePrompt = &bifrostReq.Input.Prompt
	}

	// Input reference image (image-to-video): anchored to the first frame.
	if bifrostReq.Input.InputReference != nil && *bifrostReq.Input.InputReference != "" {
		sanitizedURL, err := schemas.SanitizeImageURL(*bifrostReq.Input.InputReference)
		if err != nil {
			return nil, fmt.Errorf("invalid input reference: %w", err)
		}
		request.FrameImages = []RunwareFrameImage{{InputImage: sanitizedURL, Frame: new("first")}}
	}

	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		request.NegativePrompt = params.NegativePrompt
		request.Seed = params.Seed

		// Size maps to width/height, which only apply to the video task type.
		if isVideo && params.Size != "" {
			*request.Width, *request.Height = parseRunwareSize(params.Size)
		}

		if params.Seconds != nil && *params.Seconds != "" {
			seconds, err := strconv.ParseFloat(*params.Seconds, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid seconds value: %w", err)
			}
			request.Duration = &seconds
		}

		request.ExtraParams = params.ExtraParams
	}

	return request, nil
}

// ToBifrostVideoGenerationResponse converts a Runware task result to a Bifrost video response.
// It handles both video tasks (videoURL) and other async task types that return artifacts under
// outputs.files[] (e.g. 3dInference), surfacing every asset as a VideoOutput URL so callers can
// consume them through the existing /videos response shape.
func ToBifrostVideoGenerationResponse(result *RunwareResult) *schemas.BifrostVideoGenerationResponse {
	response := &schemas.BifrostVideoGenerationResponse{
		ID:        result.TaskUUID,
		Object:    "video",
		CreatedAt: time.Now().Unix(),
	}

	switch strings.ToLower(result.Status) {
	case "success":
		response.Status = schemas.VideoStatusCompleted
	case "processing":
		response.Status = schemas.VideoStatusInProgress
	case "error":
		response.Status = schemas.VideoStatusFailed
		response.Error = &schemas.VideoCreateError{Code: result.Status, Message: "runware video task failed"}
	default:
		response.Status = schemas.VideoStatusQueued
	}

	if result.VideoURL != "" {
		response.Videos = append(response.Videos, schemas.VideoOutput{
			Type:        schemas.VideoOutputTypeURL,
			URL:         new(result.VideoURL),
			ContentType: "video/mp4",
		})
	}

	// Non-video task types (e.g. 3dInference) return their artifacts here. The content type is
	// derived from the URL extension since the output format varies per task type and model.
	if result.Outputs != nil {
		for _, file := range result.Outputs.Files {
			if file.URL == "" {
				continue
			}
			response.Videos = append(response.Videos, schemas.VideoOutput{
				Type:        schemas.VideoOutputTypeURL,
				URL:         new(file.URL),
				ContentType: contentTypeForAssetURL(file.URL),
			})
		}
	}

	// Some task types omit an explicit status and simply return artifacts once finished; the
	// presence of any asset on a non-failed task means the job is complete.
	if response.Status != schemas.VideoStatusFailed && response.Status != schemas.VideoStatusInProgress && len(response.Videos) > 0 {
		response.Status = schemas.VideoStatusCompleted
	}

	// Runware reports the exact task cost (only when the request sets includeCost). Surface it as
	// the provider-reported cost so pricing uses it verbatim — important for task types like 3D
	// that have no datasheet rate.
	if result.Cost > 0 {
		response.Usage = &schemas.VideoUsage{Cost: &schemas.BifrostCost{TotalCost: result.Cost}}
	}

	return response
}
