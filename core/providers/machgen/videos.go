package machgen

import (
	"strconv"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToMachGenVideoGenerationRequest converts a Bifrost video generation request to a MachGen
// generate request. The task type is inferred from the inputs unless task_type is passed
// explicitly in extra params.
func ToMachGenVideoGenerationRequest(bifrostReq *schemas.BifrostVideoGenerationRequest) (*MachGenGenerateRequest, error) {
	if bifrostReq.Input == nil {
		return nil, providerUtils.InvalidRequestErrorf("input is required")
	}

	request := &MachGenGenerateRequest{
		Model:       bifrostReq.Model,
		Prompt:      bifrostReq.Input.Prompt,
		VideoConfig: &MachGenVideoConfig{},
	}

	if ref := bifrostReq.Input.InputReference; ref != nil && *ref != "" {
		request.SrcImageURLs = []string{*ref}
	}
	if uri := bifrostReq.Input.VideoURI; uri != nil && *uri != "" {
		request.SrcVideoURLs = []string{*uri}
	}

	if params := bifrostReq.Params; params != nil {
		if params.Seconds != nil {
			seconds, err := strconv.Atoi(*params.Seconds)
			if err != nil {
				return nil, providerUtils.InvalidRequestErrorf("invalid seconds value %q: must be an integer", *params.Seconds)
			}
			request.VideoConfig.DurationSecs = &seconds
		}
		if params.Size != "" {
			height, aspect, err := parseSize(params.Size)
			if err != nil {
				return nil, err
			}
			request.VideoConfig.Height = height
			request.VideoConfig.AspectRatio = aspect
		}
		request.VideoConfig.Audio = params.Audio
		request.VideoConfig.NegativePrompt = params.NegativePrompt
		request.Seed = params.Seed

		// A src_video_urls in extra params replaces video_uri rather than appending to it,
		// so drop the one taken from input before lifting.
		if _, ok := params.ExtraParams["src_video_urls"]; ok {
			request.SrcVideoURLs = nil
		}
		if err := applyMachGenExtraParams(request, params.ExtraParams); err != nil {
			return nil, err
		}
	}

	if err := requireSourceURLs(request); err != nil {
		return nil, err
	}
	if request.TaskType == "" {
		request.TaskType = inferVideoTaskType(request)
	}
	return request, nil
}

// inferVideoTaskType picks the MachGen video task type from the references on the request.
// Reference video/audio or named subjects mean reference-to-video; images alone are I2V
// keyframes (start frame, optional end frame); nothing is plain T2V.
func inferVideoTaskType(request *MachGenGenerateRequest) string {
	switch {
	case len(request.SrcVideoURLs) > 0, len(request.SrcAudioURLs) > 0,
		len(request.SubjectToImageIDs) > 0, len(request.SubjectToVideoIDs) > 0, len(request.SubjectToAudioIDs) > 0:
		return taskTypeR2V
	case len(request.SrcImageURLs) > 0:
		return taskTypeI2V
	default:
		return taskTypeT2V
	}
}

// ToBifrostVideoGenerationResponse converts a MachGen task status to a Bifrost video response.
func ToBifrostVideoGenerationResponse(task *MachGenTaskStatusResponse) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	if task == nil {
		return nil, providerUtils.NewBifrostOperationError("task status is nil", nil)
	}

	response := &schemas.BifrostVideoGenerationResponse{
		ID:     task.TaskID,
		Object: "video",
	}

	switch task.Status {
	case MachGenTaskStatusRunning:
		response.Status = schemas.VideoStatusInProgress
	case MachGenTaskStatusCompleted:
		response.Status = schemas.VideoStatusCompleted
	case MachGenTaskStatusFailed:
		response.Status = schemas.VideoStatusFailed
		message := "MachGen task failed"
		if task.ErrorMsg != nil && *task.ErrorMsg != "" {
			message = *task.ErrorMsg
		}
		response.Error = &schemas.VideoCreateError{Code: string(task.Status), Message: message}
	default:
		response.Status = schemas.VideoStatusQueued
	}

	if meta := task.Metadata; meta != nil {
		response.Prompt = meta.Prompt
		response.Model = meta.Model
		if meta.DurationSecs != nil {
			response.Seconds = schemas.Ptr(strconv.FormatFloat(*meta.DurationSecs, 'f', -1, 64))
		}
		if meta.Width != nil && meta.Height != nil {
			response.Size = strconv.Itoa(*meta.Width) + "x" + strconv.Itoa(*meta.Height)
		}
	}

	if task.Status == MachGenTaskStatusCompleted && task.TaskOutput != nil && task.TaskOutput.Video != "" {
		response.Videos = []schemas.VideoOutput{{
			Type:        schemas.VideoOutputTypeURL,
			URL:         schemas.Ptr(task.TaskOutput.Video),
			ContentType: "video/mp4",
		}}
	}

	return response, nil
}
