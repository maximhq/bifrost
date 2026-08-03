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

// resolutionFromSize maps a Bifrost "WIDTHxHEIGHT" size onto ModelArk's resolution
// field, which is a frame-height tier ("720p") rather than exact dimensions. Sizes
// already expressed as a tier pass through, and anything else is dropped so ModelArk
// applies the model default instead of rejecting the task.
func resolutionFromSize(size string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(size))
	if normalized == "" {
		return "", false
	}
	if strings.HasSuffix(normalized, "p") {
		return normalized, true
	}
	_, height, found := strings.Cut(normalized, "x")
	if !found {
		return "", false
	}
	if _, err := strconv.Atoi(height); err != nil {
		return "", false
	}
	return height + "p", true
}
