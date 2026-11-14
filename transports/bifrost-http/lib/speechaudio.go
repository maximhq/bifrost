package lib

import (
	"bytes"
	"strings"
)

// SpeechContentType returns the media type of a speech response: the audio's own signature when it has
// one, since a provider can return a different container than was asked for, then the requested format.
// An empty or unknown format is mp3, the API default. Headerless formats (pcm, ulaw, alaw) have no
// audio type of their own and are sent as application/octet-stream.
func SpeechContentType(responseFormat string, audio []byte) string {
	switch {
	case len(audio) >= 12 && bytes.HasPrefix(audio, []byte("RIFF")) && bytes.Equal(audio[8:12], []byte("WAVE")):
		return "audio/wav"
	case bytes.HasPrefix(audio, []byte("OggS")):
		return "audio/ogg"
	case bytes.HasPrefix(audio, []byte("fLaC")):
		return "audio/flac"
	}
	switch {
	case strings.HasPrefix(responseFormat, "wav"):
		return "audio/wav"
	case strings.HasPrefix(responseFormat, "opus"):
		return "audio/opus"
	case strings.HasPrefix(responseFormat, "aac"):
		return "audio/aac"
	case strings.HasPrefix(responseFormat, "flac"):
		return "audio/flac"
	case strings.HasPrefix(responseFormat, "pcm"), strings.HasPrefix(responseFormat, "ulaw"), strings.HasPrefix(responseFormat, "alaw"):
		return "application/octet-stream"
	}
	return "audio/mpeg"
}

// SpeechAttachmentFilename returns the download filename for a speech response. The requested format
// may be OpenAI-style ("opus", "wav", "flac", ...) or ElevenLabs-style with a codec/rate prefix
// ("mp3_22050_32", "pcm_16000", "ulaw_8000"). Unknown or empty formats fall back to mp3 (the API default).
func SpeechAttachmentFilename(responseFormat string) string {
	ext := "mp3"
	switch {
	case strings.HasPrefix(responseFormat, "opus"):
		ext = "opus"
	case strings.HasPrefix(responseFormat, "aac"):
		ext = "aac"
	case strings.HasPrefix(responseFormat, "flac"):
		ext = "flac"
	case strings.HasPrefix(responseFormat, "wav"):
		ext = "wav"
	case strings.HasPrefix(responseFormat, "pcm"):
		ext = "pcm"
	case strings.HasPrefix(responseFormat, "ulaw"):
		ext = "ulaw"
	case strings.HasPrefix(responseFormat, "alaw"):
		ext = "alaw"
	}
	return "speech." + ext
}
