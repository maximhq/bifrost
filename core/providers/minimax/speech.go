package minimax

import (
	"encoding/hex"
	"fmt"
	"maps"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// miniMaxSpeechFormats are the audio_setting.format values /v1/t2a_v2 accepts.
var miniMaxSpeechFormats = map[string]bool{
	"mp3": true, "pcm": true, "flac": true, "wav": true, "opus": true, "pcmu_raw": true, "pcmu_wav": true,
}

// ToMiniMaxSpeechRequest converts a Bifrost speech request to MiniMax's /v1/t2a_v2 body.
// Voice tuning (vol, pitch, emotion) and audio tuning (sample_rate, bitrate, channel) are read from extra params.
func ToMiniMaxSpeechRequest(bifrostReq *schemas.BifrostSpeechRequest, stream bool) (*MiniMaxSpeechRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil || strings.TrimSpace(bifrostReq.Input.Input) == "" {
		return nil, fmt.Errorf("speech input text is required")
	}
	req := &MiniMaxSpeechRequest{
		Model: bifrostReq.Model,
		Text:  bifrostReq.Input.Input,
	}
	if stream {
		req.Stream = true
		req.StreamOptions = &MiniMaxSpeechStreamOptions{ExcludeAggregatedAudio: true}
	} else {
		req.OutputFormat = schemas.Ptr("hex")
	}

	params := bifrostReq.Params
	if params == nil || params.VoiceConfig == nil || params.VoiceConfig.Voice == nil || *params.VoiceConfig.Voice == "" {
		if params != nil && params.VoiceConfig != nil && len(params.VoiceConfig.MultiVoiceConfig) > 0 {
			return nil, fmt.Errorf("minimax speech does not support multi-speaker voice config")
		}
		return nil, fmt.Errorf("voice is required (a MiniMax voice_id such as English_Graceful_Lady)")
	}
	req.VoiceSetting.VoiceID = *params.VoiceConfig.Voice
	req.VoiceSetting.Speed = params.Speed

	if format := strings.ToLower(params.ResponseFormat); format != "" {
		if !miniMaxSpeechFormats[format] {
			return nil, fmt.Errorf("unsupported response_format %q for minimax speech", params.ResponseFormat)
		}
		req.AudioSetting = &MiniMaxAudioSetting{Format: schemas.Ptr(format)}
	}

	if len(params.ExtraParams) > 0 {
		req.ExtraParams = make(map[string]interface{}, len(params.ExtraParams))
		maps.Copy(req.ExtraParams, params.ExtraParams)
		if v, ok := popFloat(req.ExtraParams, "vol"); ok {
			req.VoiceSetting.Vol = &v
		}
		if v, ok := popFloat(req.ExtraParams, "pitch"); ok {
			pitch := int(v)
			req.VoiceSetting.Pitch = &pitch
		}
		if v, ok := req.ExtraParams["emotion"].(string); ok && v != "" {
			req.VoiceSetting.Emotion = &v
			delete(req.ExtraParams, "emotion")
		}
		for _, field := range []string{"sample_rate", "bitrate", "channel"} {
			v, ok := popFloat(req.ExtraParams, field)
			if !ok {
				continue
			}
			if req.AudioSetting == nil {
				req.AudioSetting = &MiniMaxAudioSetting{}
			}
			n := int(v)
			switch field {
			case "sample_rate":
				req.AudioSetting.SampleRate = &n
			case "bitrate":
				req.AudioSetting.Bitrate = &n
			case "channel":
				req.AudioSetting.Channel = &n
			}
		}
		if v, ok := req.ExtraParams["language_boost"].(string); ok && v != "" {
			req.LanguageBoost = &v
			delete(req.ExtraParams, "language_boost")
		}
	}
	return req, nil
}

// ToBifrostSpeechResponse converts a non-streaming MiniMax speech response to Bifrost format.
func ToBifrostSpeechResponse(resp *MiniMaxSpeechResponse) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	if resp == nil {
		return nil, providerUtils.NewBifrostOperationError("speech response is nil", nil)
	}
	if bifrostErr := miniMaxBaseRespError(resp.BaseResp); bifrostErr != nil {
		return nil, bifrostErr
	}
	if resp.Data == nil || resp.Data.Audio == "" {
		return nil, providerUtils.NewBifrostOperationError("minimax speech response contained no audio", nil)
	}
	audio, err := hex.DecodeString(resp.Data.Audio)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to decode minimax hex audio", err)
	}
	return &schemas.BifrostSpeechResponse{Audio: audio, Usage: miniMaxSpeechUsage(resp)}, nil
}

func miniMaxSpeechUsage(resp *MiniMaxSpeechResponse) *schemas.SpeechUsage {
	if resp == nil || resp.ExtraInfo == nil {
		return nil
	}
	return &schemas.SpeechUsage{InputChars: resp.ExtraInfo.UsageCharacters}
}

// popFloat removes key from m and returns it as a float64 when it holds a number.
func popFloat(m map[string]interface{}, key string) (float64, bool) {
	raw, ok := m[key]
	if !ok {
		return 0, false
	}
	var v float64
	switch n := raw.(type) {
	case float64:
		v = n
	case float32:
		v = float64(n)
	case int:
		v = float64(n)
	case int64:
		v = float64(n)
	default:
		return 0, false
	}
	delete(m, key)
	return v, true
}
