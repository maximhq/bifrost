package runware

import (
	"strconv"
	"strings"
)

// Runware requires explicit pixel dimensions; default when the caller omits a size.
// Images default to a square; video defaults to 16:9 720p (video models reject square sizes).
const (
	defaultRunwareWidth  = 1024
	defaultRunwareHeight = 1024

	// 1080p 16:9: within Runware's 256-1920 x 256-1080 range, a multiple of 8, and accepted by
	// the widest set of video models (Kling Pro rejects 720p but accepts 1920x1080).
	defaultRunwareVideoWidth  = 1920
	defaultRunwareVideoHeight = 1080
)

// parseRunwareSize converts a Bifrost size string ("1024x1024") to width/height pixels.
// Falls back to the defaults when the value is empty or malformed.
func parseRunwareSize(size string) (width int, height int) {
	width, height = defaultRunwareWidth, defaultRunwareHeight
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(size)), "x", 2)
	if len(parts) != 2 {
		return width, height
	}
	if w, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && w > 0 {
		width = w
	}
	if h, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && h > 0 {
		height = h
	}
	return width, height
}

// runwareOutputType maps a Bifrost response_format to Runware's outputType.
// Returns nil to let Runware use its default (URL).
func runwareOutputType(responseFormat *string) *string {
	if responseFormat == nil {
		return nil
	}
	var out string
	switch strings.ToLower(*responseFormat) {
	case "b64_json", "base64", "base64data":
		out = "base64Data"
	case "url":
		out = "URL"
	default:
		return nil
	}
	return &out
}

// runwareOutputFormat maps a Bifrost output_format to Runware's outputFormat enum.
// Returns nil to let Runware use its default.
func runwareOutputFormat(outputFormat *string) *string {
	if outputFormat == nil {
		return nil
	}
	var out string
	switch strings.ToLower(*outputFormat) {
	case "png":
		out = "PNG"
	case "jpeg", "jpg":
		out = "JPG"
	case "webp":
		out = "WEBP"
	default:
		return nil
	}
	return &out
}
