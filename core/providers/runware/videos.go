package runware

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
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
	// Resolve the task type before building the request; it decides which modality-specific
	// defaults apply below.
	taskType := runwareVideoTaskType(bifrostReq.Params)
	isVideo := taskType == taskTypeVideoInference
	// Tool task types operate on an existing video rather than generating one.
	isVideoTool := taskType == taskTypeUpscale || taskType == taskTypeRemoveBackground

	if bifrostReq.Input == nil {
		return nil, fmt.Errorf("input is required")
	}

	request := &RunwareInferenceRequest{
		TaskType:       taskType,
		TaskUUID:       uuid.New().String(),
		DeliveryMethod: new(deliveryMethodAsync),
		Model:          bifrostReq.Model,
		IncludeCost:    new(true),
	}

	// Image-to-video derives its dimensions from the reference image, and many models reject
	// width/height outright once a frame image is present, so the default is skipped there. Models
	// that mandate explicit dimensions keep it, and an explicit size always wins (applied below).
	isImageToVideo := isVideo && bifrostReq.Input.InputReference != nil && *bifrostReq.Input.InputReference != ""

	// Runware requires explicit width/height for video and rejects square sizes on some models;
	// default to 16:9 1080p when no size is given. Non-video task types do not use width/height.
	if isVideo && (!isImageToVideo || runwareVideoRequiresDimensions(bifrostReq.Model)) {
		request.Width = new(defaultRunwareVideoWidth)
		request.Height = new(defaultRunwareVideoHeight)
	}

	if bifrostReq.Input.Prompt != "" {
		request.PositivePrompt = &bifrostReq.Input.Prompt
	}

	// Input asset. Tool tasks operate on the source video; 3D takes the reference image as a
	// nested input in the singular or array form the model expects; video generation anchors it
	// to the first frame.
	switch {
	case isVideoTool:
		if bifrostReq.Input.VideoURI != nil && *bifrostReq.Input.VideoURI != "" {
			request.Inputs = &RunwareInputs{Video: bifrostReq.Input.VideoURI}
		}
	case bifrostReq.Input.InputReference != nil && *bifrostReq.Input.InputReference != "":
		sanitizedURL, err := schemas.SanitizeImageURL(*bifrostReq.Input.InputReference)
		if err != nil {
			return nil, fmt.Errorf("invalid input reference: %w", err)
		}
		switch {
		case taskType != taskType3DInference:
			request.Inputs = &RunwareInputs{FrameImages: []RunwareFrameImage{{Image: sanitizedURL, Frame: new("first")}}}
		case uses3DImageArrayInput(bifrostReq.Model):
			request.Inputs = &RunwareInputs{Images: []string{sanitizedURL}}
		default:
			request.Inputs = &RunwareInputs{Image: &sanitizedURL}
		}
	}

	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		request.NegativePrompt = params.NegativePrompt
		request.Seed = params.Seed

		// Size maps to width/height, which only apply to the video task type. An explicit size is
		// honoured even for image-to-video, where no default was set.
		if isVideo && params.Size != "" {
			width, height := parseRunwareSize(params.Size)
			request.Width, request.Height = &width, &height
		}

		if params.Seconds != nil && *params.Seconds != "" {
			seconds, err := strconv.ParseFloat(*params.Seconds, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid seconds value: %w", err)
			}
			request.Duration = &seconds
		}

		request.ExtraParams = params.ExtraParams

		// Promote the Runware-native fields to typed properties so they reach the wire without
		// extra-param passthrough, and drop them from ExtraParams so they are not sent twice.
		if v, ok := runwareSettings(request.ExtraParams["settings"]); ok {
			delete(request.ExtraParams, "settings")
			request.Settings = v
		}
		if v, ok := runwareSettings(request.ExtraParams["providerSettings"]); ok {
			delete(request.ExtraParams, "providerSettings")
			request.ProviderSettings = v
		}
		request.UpscaleFactor = params.UpscaleFactor
		request.TargetMegapixels = params.TargetMegapixels
		// The /videos schema carries no output_format, so the container (MP4/WEBM/MOV) is only
		// selectable through extra params. Video background removal needs an alpha-capable one.
		if v, ok := schemas.SafeExtractStringPointer(request.ExtraParams["outputFormat"]); ok {
			if format := runwareOutputFormat(v); format != nil {
				delete(request.ExtraParams, "outputFormat")
				request.OutputFormat = format
			}
		}
	}

	return request, nil
}

// ToRunwareVideoEditRequest converts a Bifrost video edit request to a Runware task operating on
// an existing video. The source goes under inputs.video for every task type; the neutral "type"
// parameter picks between prompt-driven editing, upscaling and background removal.
//
// Output geometry and duration are intentionally not sent: Runware's edit models expose no
// width/height/duration, deriving both from the source video.
func ToRunwareVideoEditRequest(bifrostReq *schemas.BifrostVideoEditRequest) (*RunwareInferenceRequest, error) {
	if bifrostReq.Input == nil {
		return nil, fmt.Errorf("input is required")
	}
	video := runwareVideoInput(bifrostReq.Input.Video)
	if video == "" {
		return nil, fmt.Errorf("source video is required")
	}
	// Runware keys every task off an AIR model identifier, so it cannot infer one from the source.
	if bifrostReq.Model == "" {
		return nil, fmt.Errorf("model is required for runware video edit")
	}

	request := &RunwareInferenceRequest{
		TaskType:       runwareVideoEditTaskType(bifrostReq.Params),
		TaskUUID:       uuid.New().String(),
		DeliveryMethod: new(deliveryMethodAsync),
		Model:          bifrostReq.Model,
		Inputs:         &RunwareInputs{Video: &video},
		IncludeCost:    new(true),
	}

	if bifrostReq.Input.Prompt != "" {
		request.PositivePrompt = &bifrostReq.Input.Prompt
	}

	if bifrostReq.Params == nil {
		return request, nil
	}
	params := bifrostReq.Params

	request.Seed = params.Seed
	request.OutputFormat = runwareOutputFormat(params.OutputFormat)
	request.UpscaleFactor = params.UpscaleFactor
	request.TargetMegapixels = params.TargetMegapixels

	request.ExtraParams = params.ExtraParams

	// Promote the Runware-native fields to typed properties so they reach the wire without
	// extra-param passthrough, and drop them from ExtraParams so they are not sent twice.
	if v, ok := runwareSettings(request.ExtraParams["settings"]); ok {
		delete(request.ExtraParams, "settings")
		request.Settings = v
	}
	if v, ok := runwareSettings(request.ExtraParams["providerSettings"]); ok {
		delete(request.ExtraParams, "providerSettings")
		request.ProviderSettings = v
	}

	return request, nil
}

// runwareVideoInput resolves the source video to the reference Runware expects. IDs and URLs pass
// through untouched; raw bytes become a base64 data URI, which Runware decodes even though its
// published schema advertises only UUID and URL for inputs.video.
func runwareVideoInput(video schemas.VideoInput) string {
	switch {
	case video.ID != "":
		return video.ID
	case video.URL != "":
		return video.URL
	case len(video.Video) > 0:
		return providerUtils.FileBytesToBase64DataURL(video.Video)
	}
	return ""
}

// runwareVideoEditTaskType maps the neutral edit type onto a Runware task type. Prompt-driven
// editing runs as videoInference, the same task type that generates a video from scratch.
func runwareVideoEditTaskType(params *schemas.VideoEditParameters) string {
	if params == nil || params.Type == nil {
		return taskTypeVideoInference
	}
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(*params.Type)), "-", "_") {
	case "upscale":
		return taskTypeUpscale
	case "background_removal", "remove_background", "remove_bg":
		return taskTypeRemoveBackground
	}
	return taskTypeVideoInference
}

// runwareVideoTaskType resolves the Runware task type for a /videos request. The neutral "type"
// parameter selects the operation, the taskType extra param stays as a raw escape hatch for task
// types Bifrost does not model, and video generation is the default.
func runwareVideoTaskType(params *schemas.VideoGenerationParameters) string {
	if params == nil {
		return taskTypeVideoInference
	}
	if params.Type != nil {
		switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(*params.Type)), "-", "_") {
		case "3d":
			return taskType3DInference
		case "upscale":
			return taskTypeUpscale
		case "background_removal", "remove_background", "remove_bg":
			return taskTypeRemoveBackground
		}
	}
	if override, ok := schemas.SafeExtractString(params.ExtraParams["taskType"]); ok && override != "" {
		return override
	}
	return taskTypeVideoInference
}

// findRunwareTaskError returns the envelope error belonging to a task, matching on taskUUID and
// falling back to the first error when the envelope does not attribute one.
func findRunwareTaskError(taskErrors []RunwareError, taskUUID string) *RunwareError {
	for i := range taskErrors {
		if taskErrors[i].TaskUUID == taskUUID {
			return &taskErrors[i]
		}
	}
	if len(taskErrors) > 0 {
		return &taskErrors[0]
	}
	return nil
}

// ToBifrostVideoGenerationResponse converts a Runware task result to a Bifrost video response.
// It handles both video tasks (videoURL) and other async task types that return artifacts under
// outputs.files[] (e.g. 3dInference), surfacing every asset as a VideoOutput URL so callers can
// consume them through the existing /videos response shape.
func ToBifrostVideoGenerationResponse(result *RunwareResult, taskErrors []RunwareError) *schemas.BifrostVideoGenerationResponse {
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
		// Runware reports the reason in the envelope's errors[], not on the task result, so a
		// failed job would otherwise surface with no actionable detail.
		response.Error = &schemas.VideoCreateError{Code: "error", Message: "runware task failed"}
		if taskErr := findRunwareTaskError(taskErrors, result.TaskUUID); taskErr != nil {
			if taskErr.Code != "" {
				response.Error.Code = taskErr.Code
			}
			if taskErr.Message != "" {
				response.Error.Message = taskErr.Message
			}
		}
	default:
		response.Status = schemas.VideoStatusQueued
	}

	if result.VideoURL != "" {
		// outputFormat accepts MP4, WEBM and MOV, so derive the type from the URL rather than
		// assuming MP4; fall back to MP4 for URLs that carry no usable extension.
		contentType := contentTypeForAssetURL(result.VideoURL)
		if contentType == "application/octet-stream" {
			contentType = "video/mp4"
		}
		response.Videos = append(response.Videos, schemas.VideoOutput{
			ID:          result.VideoUUID,
			Type:        schemas.VideoOutputTypeURL,
			URL:         new(result.VideoURL),
			ContentType: contentType,
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
				ID:          file.UUID,
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
