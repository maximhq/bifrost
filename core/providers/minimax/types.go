package minimax

import providerUtils "github.com/maximhq/bifrost/core/providers/utils"

const (
	defaultBaseURL      = "https://api.minimax.io"
	defaultSpeechPath   = "/v1/t2a_v2"
	defaultOutputFormat = "hex"
)

var supportedLanguageModels = []string{
	"MiniMax-M3",
	"MiniMax-M2.7",
	"MiniMax-M2.7-highspeed",
	"MiniMax-M2.5",
	"MiniMax-M2.5-highspeed",
	"MiniMax-M2.1",
	"MiniMax-M2.1-highspeed",
	"MiniMax-M2",
}

var supportedSpeechModels = []string{
	"speech-2.8-hd",
	"speech-2.8-turbo",
	"speech-2.6-hd",
	"speech-2.6-turbo",
	"speech-02-hd",
	"speech-02-turbo",
	"speech-01-hd",
	"speech-01-turbo",
}

type MiniMaxStreamOptions struct {
	ExcludeAggregatedAudio *bool `json:"exclude_aggregated_audio,omitempty"`
}

type MiniMaxVoiceSetting struct {
	VoiceID           string   `json:"voice_id"`
	Speed             *float64 `json:"speed,omitempty"`
	Vol               *float64 `json:"vol,omitempty"`
	Pitch             *int     `json:"pitch,omitempty"`
	Emotion           *string  `json:"emotion,omitempty"`
	TextNormalization *bool    `json:"text_normalization,omitempty"`
	LatexRead         *bool    `json:"latex_read,omitempty"`
}

type MiniMaxAudioSetting struct {
	SampleRate *int    `json:"sample_rate,omitempty"`
	Bitrate    *int    `json:"bitrate,omitempty"`
	Format     *string `json:"format,omitempty"`
	Channel    *int    `json:"channel,omitempty"`
	ForceCBR   *bool   `json:"force_cbr,omitempty"`
}

type MiniMaxPronunciationDict struct {
	Tone []string `json:"tone,omitempty"`
}

type MiniMaxTimbreWeight struct {
	VoiceID string `json:"voice_id"`
	Weight  int    `json:"weight"`
}

type MiniMaxVoiceModify struct {
	Pitch        *int    `json:"pitch,omitempty"`
	Intensity    *int    `json:"intensity,omitempty"`
	Timbre       *int    `json:"timbre,omitempty"`
	SoundEffects *string `json:"sound_effects,omitempty"`
}

type MiniMaxSpeechRequest struct {
	Model             string                    `json:"model"`
	Text              string                    `json:"text"`
	Stream            bool                      `json:"stream"`
	StreamOptions     *MiniMaxStreamOptions     `json:"stream_options,omitempty"`
	VoiceSetting      *MiniMaxVoiceSetting      `json:"voice_setting"`
	AudioSetting      *MiniMaxAudioSetting      `json:"audio_setting,omitempty"`
	PronunciationDict *MiniMaxPronunciationDict `json:"pronunciation_dict,omitempty"`
	TimbreWeights     []MiniMaxTimbreWeight     `json:"timbre_weights,omitempty"`
	LanguageBoost     *string                   `json:"language_boost,omitempty"`
	VoiceModify       *MiniMaxVoiceModify       `json:"voice_modify,omitempty"`
	SubtitleEnable    *bool                     `json:"subtitle_enable,omitempty"`
	SubtitleType      *string                   `json:"subtitle_type,omitempty"`
	OutputFormat      string                    `json:"output_format"`
	ExtraParams       map[string]interface{}    `json:"-"`
}

func (r *MiniMaxSpeechRequest) GetExtraParams() map[string]interface{} { return r.ExtraParams }

type MiniMaxBaseResponse struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

type MiniMaxSpeechData struct {
	Audio        string `json:"audio,omitempty"`
	AudioURL     string `json:"audio_url,omitempty"`
	SubtitleFile string `json:"subtitle_file,omitempty"`
	Status       int    `json:"status"`
}

type MiniMaxSpeechExtraInfo struct {
	AudioLength             int     `json:"audio_length,omitempty"`
	AudioSampleRate         int     `json:"audio_sample_rate,omitempty"`
	AudioSize               int     `json:"audio_size,omitempty"`
	Bitrate                 int     `json:"bitrate,omitempty"`
	WordCount               int     `json:"word_count,omitempty"`
	InvisibleCharacterRatio float64 `json:"invisible_character_ratio,omitempty"`
	UsageCharacters         int     `json:"usage_characters,omitempty"`
	AudioFormat             string  `json:"audio_format,omitempty"`
	AudioChannel            int     `json:"audio_channel,omitempty"`
}

type MiniMaxSpeechResponse struct {
	Data      *MiniMaxSpeechData      `json:"data,omitempty"`
	ExtraInfo *MiniMaxSpeechExtraInfo `json:"extra_info,omitempty"`
	TraceID   string                  `json:"trace_id,omitempty"`
	BaseResp  MiniMaxBaseResponse     `json:"base_resp"`
	Error     interface{}             `json:"error,omitempty"`
}

type MiniMaxErrorResponse struct {
	BaseResp MiniMaxBaseResponse `json:"base_resp"`
	TraceID  string              `json:"trace_id,omitempty"`
	Error    interface{}         `json:"error,omitempty"`
}

func (e MiniMaxErrorResponse) message() string {
	if e.BaseResp.StatusMsg != "" {
		return e.BaseResp.StatusMsg
	}
	if e.Error != nil {
		if b, err := providerUtils.MarshalSorted(e.Error); err == nil {
			return string(b)
		}
	}
	return "MiniMax API request failed"
}
