package lib

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSpeechContentType(t *testing.T) {
	wav := []byte("RIFF\x24\x00\x00\x00WAVEfmt ")
	ogg := []byte("OggS\x00\x02opus-frames")
	flac := []byte("fLaC\x00\x00\x00\x22")
	mp3 := []byte("ID3\x04\x00mp3-frames")
	raw := []byte{0x01, 0x02, 0x03, 0x04}
	for _, tc := range []struct {
		name   string
		format string
		audio  []byte
		want   string
	}{
		{"wav bytes win over the requested format", "mp3", wav, "audio/wav"},
		{"ogg bytes win over the requested format", "wav", ogg, "audio/ogg"},
		{"flac bytes win over the requested format", "", flac, "audio/flac"},
		{"mp3 bytes", "mp3", mp3, "audio/mpeg"},
		{"wav requested, no signature", "wav", raw, "audio/wav"},
		{"opus requested, no signature", "opus", raw, "audio/opus"},
		{"aac requested", "aac", raw, "audio/aac"},
		{"flac requested, no signature", "flac", raw, "audio/flac"},
		{"elevenlabs mp3 format", "mp3_44100_128", raw, "audio/mpeg"},
		{"elevenlabs pcm format", "pcm_16000", raw, "application/octet-stream"},
		{"elevenlabs ulaw format", "ulaw_8000", raw, "application/octet-stream"},
		{"empty format keeps the mp3 default", "", raw, "audio/mpeg"},
		{"unknown format keeps the mp3 default", "weird", raw, "audio/mpeg"},
		{"empty audio", "wav", nil, "audio/wav"},
		{"RIFF that is not WAVE", "", []byte("RIFF\x24\x00\x00\x00AVI "), "audio/mpeg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SpeechContentType(tc.format, tc.audio))
		})
	}
}

func TestSpeechAttachmentFilename(t *testing.T) {
	for format, want := range map[string]string{
		"": "speech.mp3", "mp3": "speech.mp3", "mp3_22050_32": "speech.mp3", "opus": "speech.opus", "aac": "speech.aac",
		"flac": "speech.flac", "wav": "speech.wav", "pcm_16000": "speech.pcm", "ulaw_8000": "speech.ulaw", "alaw_8000": "speech.alaw", "weird": "speech.mp3",
	} {
		assert.Equal(t, want, SpeechAttachmentFilename(format), "format %q", format)
	}
}
