package modelark

import (
	"strconv"
	"strings"
)

const defaultBaseURL = "https://ark.ap-southeast.bytepluses.com/api/v3"

// defaultVideoRatio keeps the source image's aspect ratio for image-to-video and
// lets the model pick its own for text-to-video, so callers who do not know
// ModelArk's ratio vocabulary still get a usable framing.
const defaultVideoRatio = "adaptive"

// maxVideoDownloadBytes bounds VideoDownload's buffered read of the pre-signed TOS
// URL. It is far above what ModelArk's short generated clips need, but keeps a
// misbehaving or malicious response from driving unbounded allocation.
const maxVideoDownloadBytes = 512 * 1024 * 1024

// resolutionFromSize maps a Bifrost "WIDTHxHEIGHT" size onto ModelArk's resolution
// field, which is a tier ("720p") rather than exact dimensions. Sizes already
// expressed as a tier pass through, and anything else is dropped so ModelArk
// applies the model default instead of rejecting the task.
//
// The tier is the SHORT edge, not the height: a portrait 720x1280 request is 720p,
// and sending "1280p" is refused with "the parameter resolution specified in the
// request is not valid for model ... in i2v".
func resolutionFromSize(size string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(size))
	if normalized == "" {
		return "", false
	}
	if tier, ok := strings.CutSuffix(normalized, "p"); ok {
		if edge, err := strconv.Atoi(tier); err == nil && edge > 0 {
			return normalized, true
		}
		return "", false
	}
	rawWidth, rawHeight, found := strings.Cut(normalized, "x")
	if !found {
		return "", false
	}
	width, err := strconv.Atoi(rawWidth)
	if err != nil || width <= 0 {
		return "", false
	}
	height, err := strconv.Atoi(rawHeight)
	if err != nil || height <= 0 {
		return "", false
	}
	return strconv.Itoa(min(width, height)) + "p", true
}
