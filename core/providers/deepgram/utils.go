package deepgram

// deepgramSpeechFormat is the Deepgram /v1/speak encoding + optional container pair.
// Container is omitted when empty so Deepgram can apply its encoding-specific default
// (e.g. linear16 → wav, opus → ogg). pcm must set container=none explicitly because
// Deepgram's linear16 default is wav, which would wrap raw PCM in a WAV header.
type deepgramSpeechFormat struct {
	Encoding  string
	Container string
}

var (
	// Maps Bifrost speech response formats to Deepgram /v1/speak encoding (+ container).
	bifrostToDeepgramSpeechFormat = map[string]deepgramSpeechFormat{
		"":     {Encoding: "mp3"},
		"mp3":  {Encoding: "mp3"},
		"opus": {Encoding: "opus"},
		"wav":  {Encoding: "linear16", Container: "wav"},
		"pcm":  {Encoding: "linear16", Container: "none"},
		"flac": {Encoding: "flac"},
		"aac":  {Encoding: "aac"},
	}

	// Maps Deepgram encoding values to Bifrost speech formats when container is unset
	// or does not disambiguate (linear16 defaults to wav on REST).
	deepgramSpeechFormatToBifrost = map[string]string{
		"mp3":      "mp3",
		"opus":     "opus",
		"linear16": "wav",
		"flac":     "flac",
		"aac":      "aac",
		"mulaw":    "mulaw",
		"alaw":     "alaw",
	}
)

// ConvertBifrostSpeechFormatToDeepgram converts Bifrost speech format to Deepgram
// encoding and container. container may be empty when Deepgram's default is correct.
func ConvertBifrostSpeechFormatToDeepgram(format string) (encoding, container string) {
	if deepgramFormat, ok := bifrostToDeepgramSpeechFormat[format]; ok {
		return deepgramFormat.Encoding, deepgramFormat.Container
	}
	return format, ""
}

// ConvertDeepgramSpeechFormatToBifrost converts Deepgram encoding (+ optional container)
// to Bifrost speech format. linear16 + container=none maps to pcm; linear16 otherwise to wav.
func ConvertDeepgramSpeechFormatToBifrost(encoding, container string) string {
	if encoding == "linear16" {
		if container == "none" {
			return "pcm"
		}
		return "wav"
	}
	if bifrostFormat, ok := deepgramSpeechFormatToBifrost[encoding]; ok {
		return bifrostFormat
	}
	return encoding
}
