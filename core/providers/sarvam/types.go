package sarvam

// SarvamSpeechRequest is the wire request for Sarvam's Bulbul text-to-speech
// API (POST /text-to-speech), which is a JSON body — not OpenAI-shaped.
// See memory/sarvamvoice/knowledge/text-to-speech.md for the field reference.
type SarvamSpeechRequest struct {
	Text                  string                 `json:"text"`
	TargetLanguageCode    string                 `json:"target_language_code"`
	Speaker               *string                `json:"speaker,omitempty"`
	Model                 *string                `json:"model,omitempty"` // "bulbul:v2" | "bulbul:v3"
	Pace                  *float64               `json:"pace,omitempty"`
	Pitch                 *float64               `json:"pitch,omitempty"`    // bulbul:v2 only
	Loudness              *float64               `json:"loudness,omitempty"` // bulbul:v2 only
	SpeechSampleRate      *int                   `json:"speech_sample_rate,omitempty"`
	EnablePreprocessing   *bool                  `json:"enable_preprocessing,omitempty"` // bulbul:v2 only
	OutputAudioCodec      *string                `json:"output_audio_codec,omitempty"`
	Temperature           *float64               `json:"temperature,omitempty"` // bulbul:v3 only
	DictID                *string                `json:"dict_id,omitempty"`     // bulbul:v3 only
	EnableCachedResponses *bool                  `json:"enable_cached_responses,omitempty"`
	ExtraParams           map[string]interface{} `json:"-"`
}

// GetExtraParams implements the providerUtils.RequestBodyWithExtraParams interface.
func (r *SarvamSpeechRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// SarvamSpeechResponse is Sarvam's Bulbul TTS response: a JSON body carrying
// base64-encoded audio, not raw binary like OpenAI's /v1/audio/speech.
type SarvamSpeechResponse struct {
	RequestID *string  `json:"request_id"`
	Audios    []string `json:"audios"`
}

// SarvamError is Sarvam's error envelope, structurally close to but distinct
// from OpenAI's (uses "code" instead of "type", and carries a request_id).
type SarvamError struct {
	Error *SarvamErrorDetail `json:"error"`
}

type SarvamErrorDetail struct {
	RequestID *string `json:"request_id"`
	Message   string  `json:"message"`
	Code      string  `json:"code"`
}

// --- Sarvam TTS WebSocket streaming (wss://api.sarvam.ai/text-to-speech/ws) ---
// Schema sourced from Sarvam's AsyncAPI spec (api.sarvam.ai serves it at
// /asyncapi.json via docs.sarvam.ai); not in the REST OpenAPI spec.

// SarvamTTSWSConfigMessage is the required first client message, sent once
// after connecting (type: "config").
type SarvamTTSWSConfigMessage struct {
	Type string                `json:"type"`
	Data SarvamTTSWSConfigData `json:"data"`
}

type SarvamTTSWSConfigData struct {
	TargetLanguageCode  string   `json:"target_language_code"`
	Speaker             string   `json:"speaker"`
	Pitch               *float64 `json:"pitch,omitempty"`
	Pace                *float64 `json:"pace,omitempty"`
	Loudness            *float64 `json:"loudness,omitempty"`
	Temperature         *float64 `json:"temperature,omitempty"`
	SpeechSampleRate    *int     `json:"speech_sample_rate,omitempty"`
	EnablePreprocessing *bool    `json:"enable_preprocessing,omitempty"`
	OutputAudioCodec    *string  `json:"output_audio_codec,omitempty"`
	OutputAudioBitrate  *string  `json:"output_audio_bitrate,omitempty"`
	DictID              *string  `json:"dict_id,omitempty"`
}

// SarvamTTSWSTextMessage sends a text chunk for synthesis (type: "text").
type SarvamTTSWSTextMessage struct {
	Type string              `json:"type"`
	Data SarvamTTSWSTextData `json:"data"`
}

type SarvamTTSWSTextData struct {
	Text string `json:"text"`
}

// SarvamTTSWSSignalMessage covers the flush/ping client signals, which carry
// no data payload (type: "flush" | "ping").
type SarvamTTSWSSignalMessage struct {
	Type string `json:"type"`
}

// SarvamTTSWSServerMessage is the envelope for every server->client message;
// Type selects which of Data's shapes applies ("audio" | "event" | "error").
type SarvamTTSWSServerMessage struct {
	Type string                       `json:"type"`
	Data SarvamTTSWSServerMessageData `json:"data"`
}

type SarvamTTSWSServerMessageData struct {
	// type: "audio"
	ContentType string `json:"content_type,omitempty"`
	Audio       string `json:"audio,omitempty"` // base64
	RequestID   string `json:"request_id,omitempty"`
	// type: "event"
	EventType string `json:"event_type,omitempty"` // "final"
	Message   string `json:"message,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	// type: "error"
	Code int `json:"code,omitempty"`
}

// --- Sarvam STT WebSocket streaming (wss://api.sarvam.ai/speech-to-text/ws) ---
// Schema sourced from Sarvam's AsyncAPI spec, same source as the TTS WS types
// above. Unlike TTS WS, connection config is via query params, not a config
// message. Audio encoding is constrained to "audio/wav" only (AudioDataEncoding
// enum has a single value) - unlike the sync REST endpoint, which accepts many
// formats.

// SarvamSTTWSAudioMessage sends one chunk of WAV audio for transcription.
type SarvamSTTWSAudioMessage struct {
	Audio SarvamSTTWSAudioData `json:"audio"`
}

type SarvamSTTWSAudioData struct {
	Data       string `json:"data"` // base64
	SampleRate string `json:"sample_rate"`
	Encoding   string `json:"encoding"` // always "audio/wav"
}

// SarvamSTTWSFlushSignal tells the server to finalize any partial transcription.
type SarvamSTTWSFlushSignal struct {
	Type string `json:"type"` // "flush"
}

// SarvamSTTWSServerMessage is the envelope for every server->client message;
// Type selects which of Data's shapes applies ("data" | "error" | "events").
type SarvamSTTWSServerMessage struct {
	Type string                       `json:"type"`
	Data SarvamSTTWSServerMessageData `json:"data"`
}

type SarvamSTTWSServerMessageData struct {
	// type: "data"
	RequestID           string                    `json:"request_id,omitempty"`
	Transcript          string                    `json:"transcript,omitempty"`
	DiarizedTranscript  *SarvamDiarizedTranscript `json:"diarized_transcript,omitempty"`
	LanguageCode        *string                   `json:"language_code,omitempty"`
	LanguageProbability *float64                  `json:"language_probability,omitempty"`
	// type: "error"
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
	// type: "events"
	EventType string `json:"event_type,omitempty"`
}

// SarvamTranscriptionResponse is the wire response for Sarvam's Saaras/Saarika
// speech-to-text API (POST /speech-to-text). Field names and shape diverge
// from OpenAI's /v1/audio/transcriptions ("transcript"/"timestamps"/
// "diarized_transcript" vs "text"/"words"/"segments").
// See memory/sarvamvoice/knowledge/speech-to-text.md for the field reference.
type SarvamTranscriptionResponse struct {
	RequestID           *string                        `json:"request_id"`
	Transcript          string                         `json:"transcript"`
	Timestamps          *SarvamTranscriptionTimestamps `json:"timestamps"`
	DiarizedTranscript  *SarvamDiarizedTranscript      `json:"diarized_transcript"`
	LanguageCode        *string                        `json:"language_code"`
	LanguageProbability *float64                       `json:"language_probability"`
}

type SarvamTranscriptionTimestamps struct {
	Words            []string  `json:"words"`
	StartTimeSeconds []float64 `json:"start_time_seconds"`
	EndTimeSeconds   []float64 `json:"end_time_seconds"`
}

type SarvamDiarizedTranscript struct {
	Entries []SarvamDiarizedEntry `json:"entries"`
}

type SarvamDiarizedEntry struct {
	Transcript       string  `json:"transcript"`
	StartTimeSeconds float64 `json:"start_time_seconds"`
	EndTimeSeconds   float64 `json:"end_time_seconds"`
	SpeakerID        string  `json:"speaker_id"`
}
