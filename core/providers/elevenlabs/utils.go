package elevenlabs

var (
	// Maps provider-specific finish reasons to Bifrost format
	bifrostToElevenlabsSpeechFormat = map[string]string{
		"":     "mp3_44100_128",
		"mp3":  "mp3_44100_128",
		"opus": "opus_48000_128",
		"wav":  "pcm_44100",
		"pcm":  "pcm_44100",
	}

	// Maps Bifrost finish reasons to provider-specific format
	elevenlabsSpeechFormatToBifrost = map[string]string{
		"mp3_44100_128":  "mp3",
		"opus_48000_128": "opus",
		"pcm_44100":      "wav",
	}
)

// ConvertBifrostSpeechFormatToElevenlabs converts Bifrost speech format to Elevenlabs format
func ConvertBifrostSpeechFormatToElevenlabs(format string) string {
	if elevenlabsFormat, ok := bifrostToElevenlabsSpeechFormat[format]; ok {
		return elevenlabsFormat
	}
	return format
}

// ConvertElevenlabsSpeechFormatToBifrost converts Elevenlabs speech format to Bifrost format
func ConvertElevenlabsSpeechFormatToBifrost(format string) string {
	if bifrostFormat, ok := elevenlabsSpeechFormatToBifrost[format]; ok {
		return bifrostFormat
	}
	return format
}
