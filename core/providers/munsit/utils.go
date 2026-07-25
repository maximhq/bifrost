package munsit

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
