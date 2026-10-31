package minimax

import (
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	miniMaxDefaultVideoDuration = 6
	miniMaxVideoMaxDuration     = 15
)

// miniMaxVideoRatios are the concrete ratios /v2/video_generation accepts besides "adaptive".
var miniMaxVideoRatios = []struct {
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

func isMiniMaxH3MaxModel(model string) bool {
	return strings.EqualFold(model, "MiniMax-H3-Max")
}

// ToMiniMaxVideoGenerationRequest converts a Bifrost video generation request to MiniMax's /v2/video_generation body.
func ToMiniMaxVideoGenerationRequest(bifrostReq *schemas.BifrostVideoGenerationRequest) (*MiniMaxVideoGenerationRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil || strings.TrimSpace(bifrostReq.Input.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if bifrostReq.Input.VideoURI != nil && *bifrostReq.Input.VideoURI != "" {
		return nil, fmt.Errorf("video_uri is not supported by minimax video generation")
	}

	req := &MiniMaxVideoGenerationRequest{
		Model:    bifrostReq.Model,
		Content:  []MiniMaxVideoContentItem{{Type: "text", Text: bifrostReq.Input.Prompt}},
		Duration: miniMaxDefaultVideoDuration,
	}
	hasFrameImage := false
	if bifrostReq.Input.InputReference != nil && *bifrostReq.Input.InputReference != "" {
		url, err := schemas.SanitizeImageURL(*bifrostReq.Input.InputReference)
		if err != nil {
			return nil, fmt.Errorf("invalid input reference: %w", err)
		}
		req.Content = append(req.Content, MiniMaxVideoContentItem{Type: "image_url", Role: "first_frame", ImageURL: &MiniMaxVideoMediaURL{URL: url}})
		hasFrameImage = true
	}

	var width, height int
	params := bifrostReq.Params
	if params != nil {
		if params.Seconds != nil && *params.Seconds != "" {
			seconds, err := strconv.ParseFloat(*params.Seconds, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid seconds value: %w", err)
			}
			req.Duration = int(math.Round(seconds))
		}
		if params.Size != "" {
			w, h, err := parseImageSize(params.Size)
			if err != nil {
				return nil, err
			}
			width, height = w, h
		}
		if len(params.ExtraParams) > 0 {
			req.ExtraParams = make(map[string]interface{}, len(params.ExtraParams))
			maps.Copy(req.ExtraParams, params.ExtraParams)
			if v, ok := req.ExtraParams["last_frame"].(string); ok && v != "" {
				url, err := schemas.SanitizeImageURL(v)
				if err != nil {
					return nil, fmt.Errorf("invalid last_frame: %w", err)
				}
				req.Content = append(req.Content, MiniMaxVideoContentItem{Type: "image_url", Role: "last_frame", ImageURL: &MiniMaxVideoMediaURL{URL: url}})
				hasFrameImage = true
				delete(req.ExtraParams, "last_frame")
			}
			if refs, err := schemas.ConvertViaJSON[[]string](req.ExtraParams["reference_images"]); err == nil && len(refs) > 0 {
				if hasFrameImage {
					return nil, fmt.Errorf("reference_images cannot be combined with first or last frame images")
				}
				for _, ref := range refs {
					url, err := schemas.SanitizeImageURL(ref)
					if err != nil {
						return nil, fmt.Errorf("invalid reference image: %w", err)
					}
					req.Content = append(req.Content, MiniMaxVideoContentItem{Type: "image_url", Role: "reference_image", ImageURL: &MiniMaxVideoMediaURL{URL: url}})
				}
				delete(req.ExtraParams, "reference_images")
			}
			if v, ok := req.ExtraParams["resolution"].(string); ok && v != "" {
				req.Resolution = v
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
		}
	}

	isMax := isMiniMaxH3MaxModel(bifrostReq.Model)
	minDuration := 4
	if isMax {
		minDuration = 5
	}
	req.Duration = max(minDuration, min(miniMaxVideoMaxDuration, req.Duration))

	if req.Resolution == "" {
		req.Resolution = miniMaxVideoResolution(width, height, isMax)
	}
	if req.Ratio == nil {
		switch {
		case width > 0 && height > 0:
			req.Ratio = schemas.Ptr(nearestMiniMaxVideoRatio(width, height))
		case hasFrameImage:
			req.Ratio = schemas.Ptr("adaptive")
		default:
			// Text-to-video requires a concrete ratio.
			req.Ratio = schemas.Ptr("16:9")
		}
	}
	return req, nil
}

// miniMaxVideoResolution picks a resolution tier from the requested size, staying inside what the model supports (H3: 768P/2K, H3-Max: 480P/768P).
func miniMaxVideoResolution(width, height int, isMax bool) string {
	short := min(width, height)
	switch {
	case short == 0:
		return "768P"
	case short <= 480 && isMax:
		return "480P"
	case short > 1080 && !isMax:
		return "2K"
	default:
		return "768P"
	}
}

func nearestMiniMaxVideoRatio(width, height int) string {
	target := float64(width) / float64(height)
	best := miniMaxVideoRatios[0].name
	bestDiff := math.Inf(1)
	for _, r := range miniMaxVideoRatios {
		if diff := math.Abs(math.Log(target / r.ratio)); diff < bestDiff {
			best, bestDiff = r.name, diff
		}
	}
	return best
}

// ToBifrostVideoGenerationResponse converts a queried MiniMax video task to Bifrost format.
func ToBifrostVideoGenerationResponse(task *MiniMaxVideoTask) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	if task == nil {
		return nil, providerUtils.NewBifrostOperationError("video task is nil", nil)
	}
	resp := &schemas.BifrostVideoGenerationResponse{
		ID:        task.ID,
		Object:    "video",
		Model:     task.Model,
		CreatedAt: task.CreatedAt,
		Size:      task.Resolution,
	}
	if task.Duration > 0 {
		resp.Seconds = schemas.Ptr(strconv.Itoa(task.Duration))
	}
	switch task.Status {
	case MiniMaxVideoTaskStatusQueued:
		resp.Status = schemas.VideoStatusQueued
	case MiniMaxVideoTaskStatusRunning:
		resp.Status = schemas.VideoStatusInProgress
	case MiniMaxVideoTaskStatusSucceeded:
		resp.Status = schemas.VideoStatusCompleted
		if task.UpdatedAt > 0 {
			resp.CompletedAt = schemas.Ptr(task.UpdatedAt)
		}
	case MiniMaxVideoTaskStatusFailed, MiniMaxVideoTaskStatusCancelled:
		resp.Status = schemas.VideoStatusFailed
		resp.Error = &schemas.VideoCreateError{Code: string(task.Status), Message: "task " + string(task.Status)}
		if task.Error != nil {
			if task.Error.Code != "" {
				resp.Error.Code = task.Error.Code
			}
			if task.Error.Message != "" {
				resp.Error.Message = task.Error.Message
			}
		}
	default:
		resp.Status = schemas.VideoStatusQueued
	}
	if task.Content != nil {
		if task.Content.URL != "" {
			resp.Videos = []schemas.VideoOutput{{Type: schemas.VideoOutputTypeURL, URL: schemas.Ptr(task.Content.URL), ContentType: "video/mp4"}}
		}
		resp.Prompt = task.Content.Prompt
	}
	return resp, nil
}
