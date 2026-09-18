package minimax

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

var supportedAudioFormats = map[string]struct{}{
	"mp3": {}, "pcm": {}, "flac": {}, "wav": {}, "pcmu_raw": {}, "pcmu_wav": {}, "opus": {},
}

func cloneExtraParams(src map[string]interface{}) map[string]interface{} {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func consumeExtra[T any](extra map[string]interface{}, key string, dst *T) error {
	value, ok := extra[key]
	if !ok {
		return nil
	}
	data, err := providerUtils.MarshalSorted(value)
	if err != nil {
		return fmt.Errorf("invalid MiniMax %s: %w", key, err)
	}
	if err := sonic.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("invalid MiniMax %s: %w", key, err)
	}
	delete(extra, key)
	return nil
}

// ToMiniMaxSpeechRequest converts the neutral Bifrost speech shape to MiniMax T2A v2.
// Standard Bifrost fields take precedence over provider-specific ExtraParams.
func ToMiniMaxSpeechRequest(bifrostReq *schemas.BifrostSpeechRequest, stream bool) (*MiniMaxSpeechRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil || strings.TrimSpace(bifrostReq.Input.Input) == "" {
		return nil, fmt.Errorf("speech input text is required")
	}

	extra := map[string]interface{}(nil)
	if bifrostReq.Params != nil {
		extra = cloneExtraParams(bifrostReq.Params.ExtraParams)
	}

	req := &MiniMaxSpeechRequest{
		Model:        bifrostReq.Model,
		Text:         bifrostReq.Input.Input,
		Stream:       stream,
		OutputFormat: defaultOutputFormat,
		ExtraParams:  extra,
	}

	if err := consumeExtra(extra, "voice_setting", &req.VoiceSetting); err != nil {
		return nil, err
	}
	if err := consumeExtra(extra, "audio_setting", &req.AudioSetting); err != nil {
		return nil, err
	}
	if err := consumeExtra(extra, "stream_options", &req.StreamOptions); err != nil {
		return nil, err
	}
	if err := consumeExtra(extra, "pronunciation_dict", &req.PronunciationDict); err != nil {
		return nil, err
	}
	if err := consumeExtra(extra, "timbre_weights", &req.TimbreWeights); err != nil {
		return nil, err
	}
	if err := consumeExtra(extra, "voice_modify", &req.VoiceModify); err != nil {
		return nil, err
	}
	if err := consumeExtra(extra, "language_boost", &req.LanguageBoost); err != nil {
		return nil, err
	}
	if err := consumeExtra(extra, "subtitle_enable", &req.SubtitleEnable); err != nil {
		return nil, err
	}
	if err := consumeExtra(extra, "subtitle_type", &req.SubtitleType); err != nil {
		return nil, err
	}
	// output_format is intentionally consumed but forced to hex below. Bifrost returns
	// audio bytes, while MiniMax's url mode would require a second network fetch.
	var ignoredOutputFormat string
	if err := consumeExtra(extra, "output_format", &ignoredOutputFormat); err != nil {
		return nil, err
	}
	delete(extra, "model")
	delete(extra, "text")
	delete(extra, "stream")

	if req.VoiceSetting == nil {
		req.VoiceSetting = &MiniMaxVoiceSetting{}
	}
	if bifrostReq.Params != nil {
		if bifrostReq.Params.VoiceConfig != nil && bifrostReq.Params.VoiceConfig.Voice != nil {
			req.VoiceSetting.VoiceID = *bifrostReq.Params.VoiceConfig.Voice
		}
		if bifrostReq.Params.Speed != nil {
			req.VoiceSetting.Speed = bifrostReq.Params.Speed
		}
		if bifrostReq.Params.LanguageCode != nil {
			req.LanguageBoost = bifrostReq.Params.LanguageCode
		}
		if bifrostReq.Params.ResponseFormat != "" {
			format := strings.ToLower(bifrostReq.Params.ResponseFormat)
			if _, ok := supportedAudioFormats[format]; !ok {
				return nil, fmt.Errorf("unsupported MiniMax audio format %q", format)
			}
			if stream && format == "wav" {
				return nil, fmt.Errorf("MiniMax does not support wav format in streaming mode")
			}
			if req.AudioSetting == nil {
				req.AudioSetting = &MiniMaxAudioSetting{}
			}
			req.AudioSetting.Format = &format
		}
	}

	if strings.TrimSpace(req.Model) == "" {
		return nil, fmt.Errorf("MiniMax speech model is required")
	}
	if strings.TrimSpace(req.VoiceSetting.VoiceID) == "" {
		return nil, fmt.Errorf("voice parameter is required")
	}

	if req.AudioSetting == nil {
		format := "mp3"
		req.AudioSetting = &MiniMaxAudioSetting{Format: &format}
	} else if req.AudioSetting.Format != nil {
		format := strings.ToLower(*req.AudioSetting.Format)
		if _, ok := supportedAudioFormats[format]; !ok {
			return nil, fmt.Errorf("unsupported MiniMax audio format %q", format)
		}
		if stream && format == "wav" {
			return nil, fmt.Errorf("MiniMax does not support wav format in streaming mode")
		}
		req.AudioSetting.Format = &format
	}

	if stream {
		if req.StreamOptions == nil {
			req.StreamOptions = &MiniMaxStreamOptions{}
		}
		// Without this flag MiniMax may put the complete, aggregated audio in the
		// terminal event, which would duplicate all preceding deltas.
		exclude := true
		req.StreamOptions.ExcludeAggregatedAudio = &exclude
	}

	return req, nil
}

func decodeMiniMaxAudio(audio string) ([]byte, error) {
	if audio == "" {
		return nil, nil
	}
	if len(audio)%2 != 0 {
		return nil, fmt.Errorf("MiniMax returned odd-length hex audio")
	}
	decoded := make([]byte, hex.DecodedLen(len(audio)))
	if _, err := hex.Decode(decoded, []byte(audio)); err != nil {
		return nil, fmt.Errorf("MiniMax returned invalid hex audio: %w", err)
	}
	return decoded, nil
}

func miniMaxUsage(extra *MiniMaxSpeechExtraInfo, request *schemas.BifrostSpeechRequest) *schemas.SpeechUsage {
	usage := &schemas.SpeechUsage{}
	if extra != nil && extra.UsageCharacters > 0 {
		usage.InputChars = extra.UsageCharacters
	} else if request != nil && request.Input != nil {
		usage.InputChars = len([]rune(request.Input.Input))
	}
	return usage
}

func miniMaxSubtitleFile(data *MiniMaxSpeechData) *string {
	if data == nil || strings.TrimSpace(data.SubtitleFile) == "" {
		return nil
	}
	return schemas.Ptr(data.SubtitleFile)
}
