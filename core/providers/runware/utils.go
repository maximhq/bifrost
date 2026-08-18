package runware

import (
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
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

// runwareOutputFormat maps a Bifrost output_format to Runware's outputFormat enum, which is
// uppercase and spans both image and video containers. Returns nil to let Runware use its default.
func runwareOutputFormat(outputFormat *string) *string {
	if outputFormat == nil {
		return nil
	}
	var out string
	switch strings.ToLower(strings.TrimSpace(*outputFormat)) {
	case "png":
		out = "PNG"
	case "jpeg", "jpg":
		out = "JPG"
	case "webp":
		out = "WEBP"
	case "svg":
		out = "SVG"
	case "mp4":
		out = "MP4"
	case "webm":
		out = "WEBM"
	case "mov":
		out = "MOV"
	default:
		return nil
	}
	return &out
}

// runwareSettings coerces a "settings" extra param into Runware's nested settings object.
// Multipart form values arrive as a JSON string; JSON callers send an object directly.
func runwareSettings(value interface{}) (map[string]interface{}, bool) {
	switch v := value.(type) {
	case map[string]interface{}:
		return v, len(v) > 0
	case string:
		var settings map[string]interface{}
		if err := sonic.Unmarshal([]byte(v), &settings); err == nil && len(settings) > 0 {
			return settings, true
		}
	}
	return nil, false
}

// contentTypeForAssetURL infers a MIME type from an artifact URL's file extension. Runware's
// output URLs (outputs.files[].url) carry the real extension (e.g. ".glb"), so the extension is
// the most reliable signal — the request-side outputFormat parameter varies per model and is not
// always present. Falls back to application/octet-stream for unknown or extensionless URLs.
func contentTypeForAssetURL(url string) string {
	// Strip any query string / fragment before reading the extension.
	trimmed := url
	if i := strings.IndexAny(trimmed, "?#"); i >= 0 {
		trimmed = trimmed[:i]
	}
	dot := strings.LastIndex(trimmed, ".")
	if dot < 0 || dot < strings.LastIndex(trimmed, "/") {
		return "application/octet-stream"
	}
	switch strings.ToLower(trimmed[dot+1:]) {
	case "glb":
		return "model/gltf-binary"
	case "gltf":
		return "model/gltf+json"
	case "usdz":
		return "model/vnd.usdz+zip"
	case "obj":
		return "model/obj"
	case "stl":
		return "model/stl"
	case "fbx":
		return "application/octet-stream" // no registered MIME type for FBX
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mov":
		return "video/quicktime"
	default:
		return "application/octet-stream"
	}
}
