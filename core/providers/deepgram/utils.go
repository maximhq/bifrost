package deepgram

var (
	// Maps Bifrost speech response formats to Deepgram /v1/speak encoding values.
	// Deepgram uses encoding (linear16, mulaw, alaw, mp3, opus, flac, aac) with an
	// optional separate container query param — not ElevenLabs-style compound names.
	bifrostToDeepgramSpeechFormat = map[string]string{
		"":     "mp3",
		"mp3":  "mp3",
		"opus": "opus",
		"wav":  "linear16",
		"pcm":  "linear16",
		"flac": "flac",
		"aac":  "aac",
	}

	// Maps Deepgram encoding values to Bifrost speech formats.
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

// ConvertBifrostSpeechFormatToDeepgram converts Bifrost speech format to Deepgram encoding.
func ConvertBifrostSpeechFormatToDeepgram(format string) string {
	if deepgramFormat, ok := bifrostToDeepgramSpeechFormat[format]; ok {
		return deepgramFormat
	}
	return format
}

// ConvertDeepgramSpeechFormatToBifrost converts Deepgram encoding to Bifrost speech format.
func ConvertDeepgramSpeechFormatToBifrost(format string) string {
	if bifrostFormat, ok := deepgramSpeechFormatToBifrost[format]; ok {
		return bifrostFormat
	}
	return format
}
