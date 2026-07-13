package cartesia

import (
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// cartesiaVersion is the required Cartesia-Version header value.
const cartesiaVersion = "2026-03-01"

// pcmS16leEncoding is the default PCM encoding used for wav/raw containers.
const pcmS16leEncoding = "pcm_s16le"

// cartesiaFormatSpec is the resolved container/encoding/sample-rate for a
// given Bifrost ResponseFormat string.
type cartesiaFormatSpec struct {
	container  string
	encoding   *string // nil for mp3 (encoding is implicit)
	sampleRate int
}

// bifrostToCartesiaFormat maps Bifrost ResponseFormat -> Cartesia output_format.
// Each spec gets its own encoding pointer via schemas.Ptr so no two output
// formats share a backing string across concurrent requests.
var bifrostToCartesiaFormat = map[string]cartesiaFormatSpec{
	"":    {container: "mp3", encoding: nil, sampleRate: 44100},
	"mp3": {container: "mp3", encoding: nil, sampleRate: 44100},
	"wav": {container: "wav", encoding: schemas.Ptr(pcmS16leEncoding), sampleRate: 44100},
	"pcm": {container: "raw", encoding: schemas.Ptr(pcmS16leEncoding), sampleRate: 44100},
	"raw": {container: "raw", encoding: schemas.Ptr(pcmS16leEncoding), sampleRate: 44100},
}

// resolveCartesiaOutputFormat maps a Bifrost ResponseFormat string to a Cartesia
// output_format object. For the streaming path (/tts/sse), Cartesia only supports
// the "raw" container, so any non-raw container is forced to raw+pcm_s16le.
// ExtraParams may override container/encoding/sample_rate/bit_rate for advanced use.
func resolveCartesiaOutputFormat(responseFormat string, forStreaming bool, extra map[string]interface{}) CartesiaOutputFormat {
	spec, ok := bifrostToCartesiaFormat[strings.ToLower(strings.TrimSpace(responseFormat))]
	if !ok {
		// Unknown format: default to mp3 (unary). Streaming is forced to raw below.
		spec = cartesiaFormatSpec{container: "mp3", encoding: nil, sampleRate: 44100}
	}

	out := CartesiaOutputFormat{
		Container:  spec.container,
		Encoding:   spec.encoding,
		SampleRate: spec.sampleRate,
	}

	// Streaming constraint: /tts/sse only supports container "raw".
	if forStreaming && out.Container != "raw" {
		out.Container = "raw"
		out.Encoding = schemas.Ptr(pcmS16leEncoding)
		out.BitRate = nil
	}

	applyOutputFormatOverrides(&out, extra, forStreaming)
	return out
}

// applyOutputFormatOverrides lets advanced users override the resolved output_format
// via ExtraParams. It reads from a nested "output_format" map first, then flat keys,
// so both {"output_format":{...}} and {"sample_rate":...} work. On the streaming path,
// an override attempting a non-raw container is ignored.
func applyOutputFormatOverrides(out *CartesiaOutputFormat, extra map[string]interface{}, forStreaming bool) {
	if extra == nil {
		return
	}

	// Prefer a nested output_format map if present.
	src := extra
	if nested, ok := extra["output_format"].(map[string]interface{}); ok && nested != nil {
		src = nested
	}

	if container, ok := schemas.SafeExtractStringPointer(src["container"]); ok {
		if !(forStreaming && *container != "raw") {
			out.Container = *container
		}
	}
	if encoding, ok := schemas.SafeExtractStringPointer(src["encoding"]); ok {
		out.Encoding = encoding
	}
	if sampleRate, ok := schemas.SafeExtractIntPointer(src["sample_rate"]); ok {
		out.SampleRate = *sampleRate
	}
	if bitRate, ok := schemas.SafeExtractIntPointer(src["bit_rate"]); ok {
		out.BitRate = bitRate
	}

	// mp3 has no encoding field; raw requires one.
	if out.Container == "mp3" {
		out.Encoding = nil
	}
}
