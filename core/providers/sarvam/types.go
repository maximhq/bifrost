package sarvam

import (
	"encoding/json"
	"strings"

	"github.com/bytedance/sonic"
)

// SarvamSpeechRequest is the request body for Sarvam's /text-to-speech endpoint.
type SarvamSpeechRequest struct {
	Text                  string   `json:"text"`
	TargetLanguageCode    string   `json:"target_language_code"`
	Model                 string   `json:"model,omitempty"`
	Speaker               *string  `json:"speaker,omitempty"`
	Pace                  *float64 `json:"pace,omitempty"`
	Pitch                 *float64 `json:"pitch,omitempty"`
	Loudness              *float64 `json:"loudness,omitempty"`
	Temperature           *float64 `json:"temperature,omitempty"`
	SpeechSampleRate      *int     `json:"speech_sample_rate,omitempty"`
	OutputAudioCodec      *string  `json:"output_audio_codec,omitempty"`
	EnablePreprocessing   *bool    `json:"enable_preprocessing,omitempty"`
	DictID                *string  `json:"dict_id,omitempty"`
	EnableCachedResponses *bool    `json:"enable_cached_responses,omitempty"`

	ExtraParams map[string]interface{} `json:"-"`
}

// GetExtraParams satisfies providerUtils.RequestBodyWithExtraParams.
func (r *SarvamSpeechRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// SarvamSpeechResponse is the JSON response from Sarvam's /text-to-speech endpoint.
type SarvamSpeechResponse struct {
	RequestID string   `json:"request_id"`
	Audios    []string `json:"audios"`
}

// SarvamTranscriptionRequest holds the multipart fields for Sarvam's /speech-to-text endpoint.
type SarvamTranscriptionRequest struct {
	File            []byte
	Filename        string
	Model           string
	Mode            *string
	LanguageCode    *string
	InputAudioCodec *string
}

// SarvamTimestamps holds Sarvam's word-level timing as three parallel arrays.
type SarvamTimestamps struct {
	Words            []string  `json:"words"`
	StartTimeSeconds []float64 `json:"start_time_seconds"`
	EndTimeSeconds   []float64 `json:"end_time_seconds"`
}

// SarvamDiarizedEntry is a single speaker-attributed segment.
type SarvamDiarizedEntry struct {
	Transcript       string  `json:"transcript"`
	StartTimeSeconds float64 `json:"start_time_seconds"`
	EndTimeSeconds   float64 `json:"end_time_seconds"`
	SpeakerID        string  `json:"speaker_id"`
}

// SarvamDiarizedTranscript wraps the diarized entries.
type SarvamDiarizedTranscript struct {
	Entries []SarvamDiarizedEntry `json:"entries"`
}

// SarvamTranscriptionResponse is the JSON response from Sarvam's /speech-to-text endpoint.
type SarvamTranscriptionResponse struct {
	RequestID           string                    `json:"request_id"`
	Transcript          string                    `json:"transcript"`
	LanguageCode        *string                   `json:"language_code"`
	LanguageProbability *float64                  `json:"language_probability"`
	Timestamps          *SarvamTimestamps         `json:"timestamps"`
	DiarizedTranscript  *SarvamDiarizedTranscript `json:"diarized_transcript"`
}

// SarvamError models Sarvam's error responses.
type SarvamError struct {
	Error  *sarvamErrorBody `json:"error"`
	Detail json.RawMessage  `json:"detail"`
}

type sarvamErrorBody struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// Message returns the best available human-readable error message.
func (e SarvamError) Message() string {
	if e.Error != nil && e.Error.Message != "" {
		return e.Error.Message
	}
	if len(e.Detail) > 0 {
		var s string
		if err := sonic.Unmarshal(e.Detail, &s); err == nil && s != "" {
			return s
		}
		var arr []struct {
			Msg string `json:"msg"`
		}
		if err := sonic.Unmarshal(e.Detail, &arr); err == nil {
			msgs := make([]string, 0, len(arr))
			for _, a := range arr {
				if a.Msg != "" {
					msgs = append(msgs, a.Msg)
				}
			}
			if len(msgs) > 0 {
				return strings.Join(msgs, "; ")
			}
		}
	}
	return ""
}

// Code returns the provider error code when present.
func (e SarvamError) Code() string {
	if e.Error != nil {
		return e.Error.Code
	}
	return ""
}
