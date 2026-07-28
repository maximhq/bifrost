package deepgram

var (
	// Maps provider-specific finish reasons to Bifrost format
	bifrostToDeepgramSpeechFormat = map[string]string{
		"":     "mp3_44100_128",
		"mp3":  "mp3_44100_128",
		"opus": "opus_48000_128",
		"wav":  "pcm_44100",
		"pcm":  "pcm_44100",
	}

	// Maps Bifrost finish reasons to provider-specific format
	deepgramSpeechFormatToBifrost = map[string]string{
		"mp3_44100_128":  "mp3",
		"opus_48000_128": "opus",
		"pcm_44100":      "wav",
	}
)


// ConvertDeepgramSpeechFormatToBifrost converts Deepgram speech format to Bifrost format
func ConvertDeepgramSpeechFormatToBifrost(format string) string {
	if bifrostFormat, ok := deepgramSpeechFormatToBifrost[format]; ok {
		return bifrostFormat
	}
	return format
}
