package munsit

import (
	"net/url"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

var (
	// Maps provider-specific finish reasons to Bifrost format
	bifrostToMunsitSpeechFormat = map[string]string{
		"":     "mp3_44100_128",
		"mp3":  "mp3_44100_128",
		"opus": "opus_48000_128",
		"wav":  "pcm_44100",
		"pcm":  "pcm_44100",
	}

	// Maps Bifrost finish reasons to provider-specific format
	munsitSpeechFormatToBifrost = map[string]string{
		"mp3_44100_128":  "mp3",
		"opus_48000_128": "opus",
		"pcm_44100":      "wav",
	}
)

// ConvertBifrostSpeechFormatToMunsit converts Bifrost speech format to Munsit format
func ConvertBifrostSpeechFormatToMunsit(format string) string {
	if munsitFormat, ok := bifrostToMunsitSpeechFormat[format]; ok {
		return munsitFormat
	}
	return format
}

// ConvertMunsitSpeechFormatToBifrost converts Munsit speech format to Bifrost format
func ConvertMunsitSpeechFormatToBifrost(format string) string {
	if bifrostFormat, ok := munsitSpeechFormatToBifrost[format]; ok {
		return bifrostFormat
	}
	return format
}

// speechPathForModel builds /api/v1/text-to-speech/{model} with model as a single
// path-escaped segment. Rejects empty models and traversal/separator characters so
// path.Join cannot rewrite the speech prefix to another API path.
func speechPathForModel(model string) (string, *schemas.BifrostError) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", providerUtils.NewBifrostOperationError("model is required", nil)
	}
	if strings.ContainsAny(model, `/\\`) || strings.Contains(model, "..") {
		return "", providerUtils.NewBifrostOperationError("invalid model: path separators and traversal sequences are not allowed", nil)
	}
	lower := strings.ToLower(model)
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.Contains(lower, "%2e%2e") {
		return "", providerUtils.NewBifrostOperationError("invalid model: path separators and traversal sequences are not allowed", nil)
	}
	return "/api/v1/text-to-speech/" + url.PathEscape(model), nil
}
