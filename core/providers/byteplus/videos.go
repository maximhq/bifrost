package byteplus

import (
	"maps"
	"math"
	"net/url"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToBytePlusVideoGenerationRequest converts a Bifrost video generation request to a Seedance task body.
// Frame images, reference media and a draft task become content items; resolution, ratio and the rest
// of Seedance's generation fields are read from size and extra params, and anything unrecognised passes through.
func ToBytePlusVideoGenerationRequest(bifrostReq *schemas.BifrostVideoGenerationRequest) (*BytePlusVideoGenerationRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil, providerUtils.InvalidRequestErrorf("prompt is required")
	}
	var draftTaskID string
	if bifrostReq.Params != nil {
		draftTaskID, _ = bifrostReq.Params.ExtraParams["draft_task_id"].(string)
	}
	prompt := strings.TrimSpace(bifrostReq.Input.Prompt)
	if prompt == "" && draftTaskID == "" {
		return nil, providerUtils.InvalidRequestErrorf("prompt is required")
	}
	req := &BytePlusVideoGenerationRequest{Model: bifrostReq.Model}
	if prompt != "" {
		req.Content = append(req.Content, BytePlusVideoContentItem{Type: "text", Text: bifrostReq.Input.Prompt})
	}
	if draftTaskID != "" {
		// Accept the draft's ID as Bifrost returned it, with its provider suffix.
		draftTaskID = providerUtils.StripVideoIDProviderSuffix(draftTaskID, schemas.BytePlus)
		req.Content = append(req.Content, BytePlusVideoContentItem{Type: "draft_task", DraftTask: &BytePlusDraftTask{ID: draftTaskID}})
	}
	hasFrameImage := false
	if bifrostReq.Input.InputReference != nil && *bifrostReq.Input.InputReference != "" {
		url, err := sanitizeSeedanceMediaURL(*bifrostReq.Input.InputReference)
		if err != nil {
			return nil, providerUtils.InvalidRequestErrorf("invalid input reference: %v", err)
		}
		req.Content = append(req.Content, BytePlusVideoContentItem{Type: "image_url", Role: "first_frame", ImageURL: &BytePlusVideoMediaURL{URL: url}})
		hasFrameImage = true
	}
	if bifrostReq.Input.VideoURI != nil && *bifrostReq.Input.VideoURI != "" {
		req.Content = append(req.Content, BytePlusVideoContentItem{Type: "video_url", Role: "reference_video", VideoURL: &BytePlusVideoMediaURL{URL: *bifrostReq.Input.VideoURI}})
	}

	params := bifrostReq.Params
	if params == nil {
		return req, nil
	}
	if params.Seconds != nil && *params.Seconds != "" {
		seconds, err := strconv.ParseFloat(*params.Seconds, 64)
		if err != nil {
			return nil, providerUtils.InvalidRequestErrorf("invalid seconds value: %v", err)
		}
		req.Duration = schemas.Ptr(int(math.Round(seconds)))
	}
	if params.Size != "" {
		resolution, ratio, err := bytePlusVideoSize(params.Size)
		if err != nil {
			return nil, err
		}
		req.Resolution = &resolution
		req.Ratio = &ratio
	}
	req.Seed = params.Seed
	req.GenerateAudio = params.Audio
	req.OutputFormat = params.OutputFormat
	if len(params.ExtraParams) == 0 {
		return req, nil
	}

	req.ExtraParams = make(map[string]interface{}, len(params.ExtraParams))
	maps.Copy(req.ExtraParams, params.ExtraParams)
	delete(req.ExtraParams, "draft_task_id")
	if v, ok := req.ExtraParams["last_frame"].(string); ok && v != "" {
		url, err := sanitizeSeedanceMediaURL(v)
		if err != nil {
			return nil, providerUtils.InvalidRequestErrorf("invalid last_frame: %v", err)
		}
		req.Content = append(req.Content, BytePlusVideoContentItem{Type: "image_url", Role: "last_frame", ImageURL: &BytePlusVideoMediaURL{URL: url}})
		hasFrameImage = true
		delete(req.ExtraParams, "last_frame")
	}
	if refs, err := schemas.ConvertViaJSON[[]string](req.ExtraParams["reference_images"]); err == nil && len(refs) > 0 {
		if hasFrameImage {
			return nil, providerUtils.InvalidRequestErrorf("reference_images cannot be combined with first or last frame images")
		}
		for _, ref := range refs {
			url, err := sanitizeSeedanceMediaURL(ref)
			if err != nil {
				return nil, providerUtils.InvalidRequestErrorf("invalid reference image: %v", err)
			}
			req.Content = append(req.Content, BytePlusVideoContentItem{Type: "image_url", Role: "reference_image", ImageURL: &BytePlusVideoMediaURL{URL: url}})
		}
		delete(req.ExtraParams, "reference_images")
	}
	if refs, err := schemas.ConvertViaJSON[[]string](req.ExtraParams["reference_videos"]); err == nil && len(refs) > 0 {
		for _, ref := range refs {
			req.Content = append(req.Content, BytePlusVideoContentItem{Type: "video_url", Role: "reference_video", VideoURL: &BytePlusVideoMediaURL{URL: ref}})
		}
		delete(req.ExtraParams, "reference_videos")
	}
	if refs, err := schemas.ConvertViaJSON[[]string](req.ExtraParams["reference_audios"]); err == nil && len(refs) > 0 {
		for _, ref := range refs {
			req.Content = append(req.Content, BytePlusVideoContentItem{Type: "audio_url", Role: "reference_audio", AudioURL: &BytePlusVideoMediaURL{URL: ref}})
		}
		delete(req.ExtraParams, "reference_audios")
	}
	if v, ok := req.ExtraParams["resolution"].(string); ok && v != "" {
		req.Resolution = &v
		delete(req.ExtraParams, "resolution")
	}
	if v, ok := req.ExtraParams["ratio"].(string); ok && v != "" {
		req.Ratio = &v
		delete(req.ExtraParams, "ratio")
	}
	if v, ok := req.ExtraParams["callback_url"].(string); ok && v != "" {
		req.CallbackURL = &v
		delete(req.ExtraParams, "callback_url")
	}
	return req, nil
}

// sanitizeSeedanceMediaURL normalises an image reference, also accepting the asset:// IDs of media
// uploaded to Ark's asset library.
func sanitizeSeedanceMediaURL(rawURL string) (string, error) {
	return schemas.SanitizeImageURLWithAllowedSchemes(rawURL, "http", "https", "asset")
}

// seedanceRatios are the concrete ratios Seedance accepts besides "adaptive".
var seedanceRatios = []struct {
	name  string
	ratio float64
}{
	{"21:9", 21.0 / 9},
	{"16:9", 16.0 / 9},
	{"4:3", 4.0 / 3},
	{"1:1", 1},
	{"3:4", 3.0 / 4},
	{"9:16", 9.0 / 16},
}

// bytePlusVideoSize turns an OpenAI-style "WIDTHxHEIGHT" size into Seedance's resolution tier and nearest ratio.
func bytePlusVideoSize(size string) (string, string, error) {
	parts := strings.SplitN(strings.ToLower(size), "x", 2)
	if len(parts) != 2 {
		return "", "", providerUtils.InvalidRequestErrorf("invalid size %q, expected WIDTHxHEIGHT", size)
	}
	width, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || width <= 0 || height <= 0 {
		return "", "", providerUtils.InvalidRequestErrorf("invalid size %q, expected WIDTHxHEIGHT", size)
	}
	resolution := "720p"
	switch short := min(width, height); {
	case short <= 480:
		resolution = "480p"
	case short > 720:
		resolution = "1080p"
	}
	target := float64(width) / float64(height)
	ratio, bestDiff := seedanceRatios[0].name, math.Inf(1)
	for _, r := range seedanceRatios {
		if diff := math.Abs(math.Log(target / r.ratio)); diff < bestDiff {
			ratio, bestDiff = r.name, diff
		}
	}
	return resolution, ratio, nil
}

// bytePlusVideoStatus maps a Seedance task status onto the Bifrost video lifecycle.
func bytePlusVideoStatus(status BytePlusVideoTaskStatus) schemas.VideoStatus {
	switch status {
	case BytePlusVideoTaskStatusRunning:
		return schemas.VideoStatusInProgress
	case BytePlusVideoTaskStatusSucceeded:
		return schemas.VideoStatusCompleted
	case BytePlusVideoTaskStatusFailed, BytePlusVideoTaskStatusCancelled, BytePlusVideoTaskStatusExpired:
		return schemas.VideoStatusFailed
	default:
		return schemas.VideoStatusQueued
	}
}

// bytePlusVideoError builds the failure payload for a failed, cancelled or expired task.
func bytePlusVideoError(task *BytePlusVideoTask) *schemas.VideoCreateError {
	videoErr := &schemas.VideoCreateError{Code: string(task.Status), Message: "task " + string(task.Status)}
	if task.Error != nil {
		if task.Error.Code != "" {
			videoErr.Code = task.Error.Code
		}
		if task.Error.Message != "" {
			videoErr.Message = task.Error.Message
		}
	}
	return videoErr
}

// ToBifrostVideoGenerationResponse converts a Seedance task to Bifrost format.
func ToBifrostVideoGenerationResponse(task *BytePlusVideoTask) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	if task == nil {
		return nil, providerUtils.NewBifrostOperationError("video task is nil", nil)
	}
	resp := &schemas.BifrostVideoGenerationResponse{
		ID:        task.ID,
		Object:    "video",
		Model:     task.Model,
		CreatedAt: task.CreatedAt,
		Size:      task.Resolution,
		Status:    bytePlusVideoStatus(task.Status),
	}
	if task.Duration > 0 {
		resp.Seconds = schemas.Ptr(strconv.Itoa(task.Duration))
	}
	switch resp.Status {
	case schemas.VideoStatusCompleted:
		if task.UpdatedAt > 0 {
			resp.CompletedAt = schemas.Ptr(task.UpdatedAt)
		}
	case schemas.VideoStatusFailed:
		resp.Error = bytePlusVideoError(task)
	}
	if task.Content != nil && task.Content.VideoURL != "" {
		resp.Videos = []schemas.VideoOutput{{Type: schemas.VideoOutputTypeURL, URL: schemas.Ptr(task.Content.VideoURL), ContentType: seedanceVideoContentType(task.Content.VideoURL)}}
		// A task created with return_last_frame also returns its final frame, listed after the video
		// so VideoDownload, which reads the first output, still fetches the video.
		if task.Content.LastFrameURL != "" {
			resp.Videos = append(resp.Videos, schemas.VideoOutput{Type: schemas.VideoOutputTypeURL, URL: schemas.Ptr(task.Content.LastFrameURL), ContentType: "image/jpeg"})
		}
	}
	return resp, nil
}

// seedanceVideoContentType reports a mov output (output_format "mov") as QuickTime and anything else as MP4.
func seedanceVideoContentType(videoURL string) string {
	if parsed, err := url.Parse(videoURL); err == nil && strings.HasSuffix(strings.ToLower(parsed.Path), ".mov") {
		return "video/quicktime"
	}
	return "video/mp4"
}

// ToBifrostVideoObject converts a listed Seedance task to a Bifrost video list entry.
func ToBifrostVideoObject(task *BytePlusVideoTask, providerName schemas.ModelProvider) schemas.VideoObject {
	obj := schemas.VideoObject{
		ID:        providerUtils.AddVideoIDProviderSuffix(task.ID, providerName),
		Object:    "video",
		Model:     task.Model,
		Status:    bytePlusVideoStatus(task.Status),
		CreatedAt: task.CreatedAt,
		Size:      task.Resolution,
	}
	if task.Duration > 0 {
		obj.Seconds = schemas.Ptr(strconv.Itoa(task.Duration))
	}
	switch obj.Status {
	case schemas.VideoStatusCompleted:
		if task.UpdatedAt > 0 {
			obj.CompletedAt = schemas.Ptr(task.UpdatedAt)
		}
	case schemas.VideoStatusFailed:
		obj.Error = bytePlusVideoError(task)
	}
	return obj
}
