package vertex

import (
	"maps"
	"strings"

	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// vertexTextToSpeechURL is the Cloud Text-to-Speech endpoint. It serves every Google voice, Chirp 3 HD included.
const vertexTextToSpeechURL = "https://texttospeech.googleapis.com/v1/text:synthesize"

// vertexSpeechEncodings maps a response_format to a Cloud Text-to-Speech audio encoding.
var vertexSpeechEncodings = map[string]string{
	"":     "MP3",
	"mp3":  "MP3",
	"wav":  "LINEAR16",
	"opus": "OGG_OPUS",
}

// toVertexGeminiSpeechRequest builds the Gemini TTS body for Vertex generateContent, which rejects contents without a role.
func toVertexGeminiSpeechRequest(request *schemas.BifrostSpeechRequest) (*gemini.GeminiGenerationRequest, error) {
	body, err := gemini.ToGeminiSpeechRequest(request)
	if err != nil {
		return nil, err
	}
	for i := range body.Contents {
		body.Contents[i].Role = string(gemini.RoleUser)
	}
	return body, nil
}

// ToVertexSpeechRequest converts a Bifrost speech request to a Cloud Text-to-Speech text:synthesize request.
// Cloud Text-to-Speech names the model inside the voice (hi-IN-Chirp3-HD-Erinome), so the request model only selects pricing.
func ToVertexSpeechRequest(bifrostReq *schemas.BifrostSpeechRequest) (*VertexSpeechRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil || bifrostReq.Input.Input == "" {
		return nil, providerUtils.InvalidRequestErrorf("input is required for speech completion")
	}
	params := bifrostReq.Params
	if params == nil {
		params = &schemas.SpeechParameters{}
	}

	var voice string
	if params.VoiceConfig != nil {
		if len(params.VoiceConfig.MultiVoiceConfig) > 0 {
			return nil, providerUtils.InvalidRequestErrorf("vertex speech does not support multi-speaker voices")
		}
		if params.VoiceConfig.Voice != nil {
			voice = strings.TrimSpace(*params.VoiceConfig.Voice)
		}
	}

	var language string
	if params.LanguageCode != nil {
		language = strings.TrimSpace(*params.LanguageCode)
	}
	if language == "" {
		// A voice name is <locale>-<model>-<voice>, so its first two segments are the locale.
		parts := strings.SplitN(voice, "-", 3)
		if len(parts) < 3 {
			return nil, providerUtils.InvalidRequestErrorf("language_code is required unless the voice name starts with a locale, such as hi-IN-Chirp3-HD-Erinome")
		}
		language = parts[0] + "-" + parts[1]
	}

	encoding, ok := vertexSpeechEncodings[strings.ToLower(params.ResponseFormat)]
	if !ok {
		return nil, providerUtils.InvalidRequestErrorf("vertex speech does not support response_format %q; use mp3, wav or opus", params.ResponseFormat)
	}

	request := &VertexSpeechRequest{
		Voice:       VertexSpeechVoice{LanguageCode: language, Name: voice},
		AudioConfig: VertexSpeechAudioConfig{AudioEncoding: encoding, SpeakingRate: params.Speed},
		ExtraParams: maps.Clone(params.ExtraParams),
	}
	if isVertexSSML(bifrostReq.Input.Input) {
		request.Input.SSML = bifrostReq.Input.Input
	} else {
		request.Input.Text = bifrostReq.Input.Input
	}

	extra := request.ExtraParams
	if value, ok := extra["pitch"]; ok {
		pitch, ok := schemas.SafeExtractFloat64Pointer(value)
		if !ok {
			return nil, providerUtils.InvalidRequestErrorf("pitch must be a number")
		}
		request.AudioConfig.Pitch = pitch
		delete(extra, "pitch")
	}
	if value, ok := extra["volume_gain_db"]; ok {
		gain, ok := schemas.SafeExtractFloat64Pointer(value)
		if !ok {
			return nil, providerUtils.InvalidRequestErrorf("volume_gain_db must be a number")
		}
		request.AudioConfig.VolumeGainDb = gain
		delete(extra, "volume_gain_db")
	}
	if value, ok := extra["sample_rate_hertz"]; ok {
		rate, ok := schemas.SafeExtractIntPointer(value)
		if !ok {
			return nil, providerUtils.InvalidRequestErrorf("sample_rate_hertz must be an integer")
		}
		request.AudioConfig.SampleRateHertz = rate
		delete(extra, "sample_rate_hertz")
	}
	if value, ok := extra["effects_profile_id"]; ok {
		profiles, ok := schemas.SafeExtractStringSlice(value)
		if !ok {
			return nil, providerUtils.InvalidRequestErrorf("effects_profile_id must be a list of strings")
		}
		request.AudioConfig.EffectsProfileID = profiles
		delete(extra, "effects_profile_id")
	}
	return request, nil
}

// isVertexSSML reports whether a text to speech input is already SSML.
func isVertexSSML(input string) bool {
	return strings.HasPrefix(strings.TrimSpace(input), "<speak")
}
