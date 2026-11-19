package gemini

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func speechResponseFor(provider schemas.ModelProvider, model string, audio []byte) *schemas.BifrostSpeechResponse {
	return &schemas.BifrostSpeechResponse{
		Audio:       audio,
		ExtraFields: schemas.BifrostResponseExtraFields{RoutingInfo: schemas.RoutingInfo{Provider: provider, Model: model}},
	}
}

// A GenAI client decodes the audio by the label it comes with, so headerless Gemini TTS PCM must not be called mp3.
func TestToGeminiSpeechResponseLabelsTheAudio(t *testing.T) {
	t.Parallel()
	pcm := []byte{0x14, 0x00, 0x0e, 0x00, 0x0a, 0x00, 0x07, 0x00} // samples with no header, as Gemini TTS returns them
	wav := append([]byte("RIFF\x24\x00\x00\x00WAVE"), pcm...)
	const l16 = "audio/L16;codec=pcm;rate=24000"

	cases := []struct {
		name     string
		provider schemas.ModelProvider
		model    string
		audio    []byte
		want     string
	}{
		{"gemini tts without a format is raw pcm", schemas.Gemini, "gemini-2.5-flash-preview-tts", pcm, l16},
		{"vertex gemini tts without a format is raw pcm", schemas.Vertex, "gemini-2.5-flash-tts", pcm, l16},
		{"a models/ prefix on the model changes nothing", schemas.Gemini, "models/gemini-3.1-flash-tts-preview", pcm, l16},
		{"gemini tts as wav keeps its header's label", schemas.Gemini, "gemini-2.5-flash-preview-tts", wav, "audio/wav"},
		{"vertex gemini tts as wav keeps its header's label", schemas.Vertex, "gemini-2.5-flash-tts", wav, "audio/wav"},
		{"chirp mp3 with an id3 tag", schemas.Vertex, "chirp-3-hd", []byte("ID3\x04\x00\x00chirp-mp3-frames"), "audio/mp3"},
		{"chirp mp3 frames", schemas.Vertex, "chirp-3-hd", []byte{0xFF, 0xF3, 0x44, 0xC4, 0x00}, "audio/mp3"},
		{"chirp wav", schemas.Vertex, "chirp-3-hd", wav, "audio/wav"},
		{"chirp opus", schemas.Vertex, "chirp-3-hd", []byte("OggS\x00\x02opus"), "audio/ogg"},
		{"another provider's audio is not touched", schemas.OpenAI, "tts-1", pcm, "audio/mp3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := ToGeminiSpeechResponse(speechResponseFor(tc.provider, tc.model, tc.audio))
			require.Len(t, response.Candidates, 1)
			assert.Equal(t, tc.want, response.Candidates[0].Content.Parts[0].InlineData.MIMEType)
		})
	}
	assert.Nil(t, ToGeminiSpeechResponse(nil))
}

// A key alias hides the model's name, and headerless PCM can begin with bytes that look like another format; neither changes how Gemini TTS audio is labelled.
func TestToGeminiSpeechResponseLabelsAliasedAndLookalikePCM(t *testing.T) {
	t.Parallel()
	const ttsModel = "gemini-2.5-flash-preview-tts"
	const l16 = "audio/L16;codec=pcm;rate=24000"
	silence := []byte{0x00, 0x00, 0x01, 0x00, 0x02, 0x00}
	mp3Fallback := []byte{0xff, 0xff, 0x01, 0x00, 0x02, 0x00} // first sample -1, which the mp3 fallback matches
	adtsLike := []byte{0xff, 0xf1, 0x00, 0x00, 0x01, 0x00}    // first sample matches the AAC signature
	adtsLikeMPEG2 := []byte{0xff, 0xf9, 0x00, 0x00, 0x01, 0x00}
	chirpMP3 := []byte("ID3\x04\x00\x00chirp-mp3-frames")

	cases := []struct {
		name    string
		routing schemas.RoutingInfo
		audio   []byte
		want    string
	}{
		{"an alias of a gemini tts model, silence first", schemas.RoutingInfo{Provider: schemas.Gemini, Model: "voice", ResolvedKeyAlias: &schemas.ResolvedKeyAlias{ModelID: ttsModel}}, silence, l16},
		{"an alias of a gemini tts model, samples the mp3 fallback matches", schemas.RoutingInfo{Provider: schemas.Gemini, Model: "voice", ResolvedKeyAlias: &schemas.ResolvedKeyAlias{ModelID: ttsModel}}, mp3Fallback, l16},
		{"an alias on vertex", schemas.RoutingInfo{Provider: schemas.Vertex, Model: "voice", ResolvedKeyAlias: &schemas.ResolvedKeyAlias{ModelID: "gemini-2.5-flash-tts"}}, silence, l16},
		{"an alias without a model id falls back to the model", schemas.RoutingInfo{Provider: schemas.Gemini, Model: ttsModel, ResolvedKeyAlias: &schemas.ResolvedKeyAlias{}}, silence, l16},
		{"the alias names the model, not the requested name", schemas.RoutingInfo{Provider: schemas.Vertex, Model: "gemini-voice", ResolvedKeyAlias: &schemas.ResolvedKeyAlias{ModelID: "chirp-3-hd"}}, chirpMP3, "audio/mp3"},
		{"pcm that opens like an adts frame", schemas.RoutingInfo{Provider: schemas.Gemini, Model: ttsModel}, adtsLike, l16},
		{"pcm that opens like an mpeg-2 adts frame", schemas.RoutingInfo{Provider: schemas.Vertex, Model: "gemini-2.5-flash-tts"}, adtsLikeMPEG2, l16},
		{"another provider's aac is not touched", schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "tts-1"}, adtsLike, "audio/aac"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := ToGeminiSpeechResponse(&schemas.BifrostSpeechResponse{
				Audio:       tc.audio,
				ExtraFields: schemas.BifrostResponseExtraFields{RoutingInfo: tc.routing},
			})
			require.Len(t, response.Candidates, 1)
			assert.Equal(t, tc.want, response.Candidates[0].Content.Parts[0].InlineData.MIMEType)
		})
	}
}

func BenchmarkGeminiSpeechMimeType(b *testing.B) {
	audio := make([]byte, 4096)
	routing := schemas.RoutingInfo{Provider: schemas.Gemini, Model: "voice", ResolvedKeyAlias: &schemas.ResolvedKeyAlias{ModelID: "gemini-2.5-flash-preview-tts"}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = geminiSpeechMimeType(audio, routing)
	}
}
