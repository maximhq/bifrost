package machgen

import (
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

const defaultBaseURL = "https://api.machgen.ai"

const (
	generatePath = "/api/v0/generate"
	tasksPath    = "/api/v0/tasks/"
	assetsPath   = "/api/v0/assets/"
	modelsPath   = "/api/v0/models"
)

const (
	taskTypeT2V = "T2V"
	taskTypeI2V = "I2V"
	taskTypeR2V = "R2V"
	taskTypeT2I = "T2I"
	taskTypeI2I = "I2I"
)

// machgenAspectRatios are the aspect ratio labels MachGen accepts.
var machgenAspectRatios = []struct {
	label string
	w, h  int
}{
	{"16:9", 16, 9}, {"9:16", 9, 16}, {"1:1", 1, 1}, {"4:3", 4, 3}, {"3:4", 3, 4},
	{"4:5", 4, 5}, {"3:2", 3, 2}, {"2:3", 2, 3}, {"21:9", 21, 9},
}

// parseSize splits an OpenAI-style "WIDTHxHEIGHT" size into MachGen's height + aspect ratio.
// MachGen derives width from height and the ratio label, rounding up (768p 16:9 is 1366 wide),
// so a width within one pixel of a named ratio maps to that label.
func parseSize(size string) (*int, *string, error) {
	w, h, ok := strings.Cut(strings.ToLower(size), "x")
	width, wErr := strconv.Atoi(strings.TrimSpace(w))
	height, hErr := strconv.Atoi(strings.TrimSpace(h))
	if !ok || wErr != nil || hErr != nil || width <= 0 || height <= 0 {
		return nil, nil, providerUtils.InvalidRequestErrorf("invalid size %q: expected WIDTHxHEIGHT, e.g. 1280x720", size)
	}
	for _, ratio := range machgenAspectRatios {
		diff := width*ratio.h - height*ratio.w
		if diff < 0 {
			diff = -diff
		}
		if diff < ratio.h {
			label := ratio.label
			return &height, &label, nil
		}
	}
	labels := make([]string, len(machgenAspectRatios))
	for i, ratio := range machgenAspectRatios {
		labels[i] = ratio.label
	}
	return nil, nil, providerUtils.InvalidRequestErrorf("size %q does not match a MachGen aspect ratio (%s)", size, strings.Join(labels, ", "))
}

// takeExtraParam moves a MachGen-native field out of extra params into dst, so callers can
// reach task types and reference media that have no counterpart in the Bifrost schema.
func takeExtraParam[T any](extra map[string]interface{}, key string, dst *T) error {
	val, ok := extra[key]
	if !ok || val == nil {
		return nil
	}
	converted, err := schemas.ConvertViaJSON[T](val)
	if err != nil {
		return providerUtils.InvalidRequestErrorf("invalid %s: %v", key, err)
	}
	*dst = converted
	delete(extra, key)
	return nil
}

// applyMachGenExtraParams lifts the MachGen-native fields from extra params onto the request.
// Whatever is left stays in ExtraParams and is merged into the body when passthrough is on.
func applyMachGenExtraParams(request *MachGenGenerateRequest, extra map[string]interface{}) error {
	if len(extra) == 0 {
		return nil
	}
	request.ExtraParams = extra
	if err := takeExtraParam(extra, "task_type", &request.TaskType); err != nil {
		return err
	}
	if err := takeExtraParam(extra, "enhance_prompt", &request.EnhancePrompt); err != nil {
		return err
	}
	var srcImages []string
	if err := takeExtraParam(extra, "src_image_urls", &srcImages); err != nil {
		return err
	}
	request.SrcImageURLs = append(request.SrcImageURLs, srcImages...)
	if err := takeExtraParam(extra, "src_video_urls", &request.SrcVideoURLs); err != nil {
		return err
	}
	if err := takeExtraParam(extra, "src_audio_urls", &request.SrcAudioURLs); err != nil {
		return err
	}
	if err := takeExtraParam(extra, "subject_to_image_ids", &request.SubjectToImageIDs); err != nil {
		return err
	}
	if err := takeExtraParam(extra, "subject_to_video_ids", &request.SubjectToVideoIDs); err != nil {
		return err
	}
	return takeExtraParam(extra, "subject_to_audio_ids", &request.SubjectToAudioIDs)
}
