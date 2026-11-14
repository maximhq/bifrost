package soniox

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// sonioxDefaultPCMRate is the rate of OpenAI's default input audio, 24 kHz PCM.
const sonioxDefaultPCMRate = 24000

// sonioxOptional tells a field that is absent from one that is set to null: a null turn_detection turns
// endpoint detection off, while an absent one leaves the default.
type sonioxOptional[T any] struct {
	Set   bool
	Value *T
}

func (o *sonioxOptional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	o.Value = new(T)
	return json.Unmarshal(data, o.Value)
}

type sonioxAudioFormat struct {
	Type string `json:"type,omitempty"`
	Rate int    `json:"rate,omitempty"`
}

// sonioxTranscription is audio.input.transcription. Model, language and prompt are OpenAI's; the other fields
// are Soniox's own start options, named as Soniox names them.
type sonioxTranscription struct {
	Model                        string         `json:"model,omitempty"`
	Language                     string         `json:"language,omitempty"`
	Prompt                       string         `json:"prompt,omitempty"`
	LanguageHints                []string       `json:"language_hints,omitempty"`
	Context                      *sonioxContext `json:"context,omitempty"`
	EnableSpeakerDiarization     bool           `json:"enable_speaker_diarization,omitempty"`
	EnableLanguageIdentification bool           `json:"enable_language_identification,omitempty"`
	ClientReferenceID            string         `json:"client_reference_id,omitempty"`
}

// sonioxContext is the context Soniox transcribes with: free text about the audio and terms to recognize.
type sonioxContext struct {
	Text  string   `json:"text,omitempty"`
	Terms []string `json:"terms,omitempty"`
}

type sonioxTurnDetection struct {
	Type              string `json:"type,omitempty"`
	SilenceDurationMs int    `json:"silence_duration_ms,omitempty"`
}

type sonioxSessionInput struct {
	Format        sonioxAudioFormat    `json:"format"`
	Transcription sonioxTranscription  `json:"transcription"`
	TurnDetection *sonioxTurnDetection `json:"turn_detection"`
}

type sonioxSessionIO struct {
	Input sonioxSessionInput `json:"input"`
}

// sonioxBifrostOptions echoes the Bifrost options a session has set.
type sonioxBifrostOptions struct {
	Extensions bool `json:"extensions"`
}

// sonioxSession is the session object of session.created and session.updated.
type sonioxSession struct {
	ID      string                `json:"id"`
	Object  string                `json:"object"`
	Type    string                `json:"type"`
	Audio   sonioxSessionIO       `json:"audio"`
	Bifrost *sonioxBifrostOptions `json:"bifrost,omitempty"`
}

// sonioxSessionUpdate is the part of a transcription session.update's session that Soniox reads.
type sonioxSessionUpdate struct {
	Bifrost struct {
		Extensions bool `json:"extensions"`
	} `json:"bifrost"`
	Audio struct {
		Input struct {
			Format        sonioxAudioFormat                   `json:"format"`
			Transcription sonioxTranscription                 `json:"transcription"`
			TurnDetection sonioxOptional[sonioxTurnDetection] `json:"turn_detection"`
		} `json:"input"`
	} `json:"audio"`
}

// sonioxStartConfig is the first frame of a connection. Soniox fixes it for the connection's life.
type sonioxStartConfig struct {
	Model                        string         `json:"model"`
	AudioFormat                  string         `json:"audio_format"`
	SampleRate                   int            `json:"sample_rate,omitempty"`
	NumChannels                  int            `json:"num_channels,omitempty"`
	LanguageHints                []string       `json:"language_hints,omitempty"`
	Context                      *sonioxContext `json:"context,omitempty"`
	EnableSpeakerDiarization     bool           `json:"enable_speaker_diarization,omitempty"`
	EnableLanguageIdentification bool           `json:"enable_language_identification,omitempty"`
	EnableEndpointDetection      bool           `json:"enable_endpoint_detection,omitempty"`
	MaxEndpointDelayMs           int            `json:"max_endpoint_delay_ms,omitempty"`
	ClientReferenceID            string         `json:"client_reference_id,omitempty"`
}

// sonioxSettings is what a session.update fixes about a Soniox connection. extensions is Bifrost's own option,
// not part of the start frame, so a later session.update may change it.
type sonioxSettings struct {
	frame          []byte             // the start frame
	input          sonioxSessionInput // the settings as the client is told them
	bytesPerSecond int                // the audio the connection takes for each second of speech
	extensions     bool               // the client asked for the Bifrost extension events
}

// newSonioxSettings reads the settings a session names. An absent turn_detection is OpenAI's default,
// server-side detection, which Soniox calls endpoint detection; a null one hands commits to the client. The
// audio is OpenAI's default, 24 kHz PCM, unless the session says otherwise.
func newSonioxSettings(model string, session []byte) (*sonioxSettings, error) {
	var update sonioxSessionUpdate
	if len(session) > 0 {
		if err := schemas.Unmarshal(session, &update); err != nil {
			return nil, fmt.Errorf("invalid session in session.update: %w", err)
		}
	}
	input := update.Audio.Input
	config := &sonioxStartConfig{Model: model, NumChannels: 1}
	settings := &sonioxSettings{extensions: update.Bifrost.Extensions}

	switch input.Format.Type {
	case "", "audio/pcm":
		rate := cmp.Or(input.Format.Rate, sonioxDefaultPCMRate)
		config.AudioFormat, config.SampleRate = "pcm_s16le", rate
		settings.input.Format = sonioxAudioFormat{Type: "audio/pcm", Rate: rate}
		settings.bytesPerSecond = 2 * rate
	case "audio/pcmu":
		config.AudioFormat, config.SampleRate = "mulaw", 8000
		settings.input.Format = sonioxAudioFormat{Type: "audio/pcmu"}
		settings.bytesPerSecond = 8000
	case "audio/pcma":
		config.AudioFormat, config.SampleRate = "alaw", 8000
		settings.input.Format = sonioxAudioFormat{Type: "audio/pcma"}
		settings.bytesPerSecond = 8000
	default:
		return nil, fmt.Errorf("soniox takes audio/pcm, audio/pcmu or audio/pcma input, not %q", input.Format.Type)
	}

	if detection := input.TurnDetection; !detection.Set || detection.Value != nil {
		config.EnableEndpointDetection = true
		settings.input.TurnDetection = &sonioxTurnDetection{Type: "server_vad"}
		if value := detection.Value; value != nil {
			// Soniox has one endpoint detection, so a type OpenAI does not define is reported as the server_vad applied.
			if value.Type == "semantic_vad" {
				settings.input.TurnDetection.Type = value.Type
			}
			if value.SilenceDurationMs > 0 {
				config.MaxEndpointDelayMs = value.SilenceDurationMs
				settings.input.TurnDetection.SilenceDurationMs = value.SilenceDurationMs
			}
		}
	}

	t := input.Transcription
	if t.Language != "" {
		config.LanguageHints = append(config.LanguageHints, t.Language)
	}
	for _, hint := range t.LanguageHints {
		if !slices.Contains(config.LanguageHints, hint) {
			config.LanguageHints = append(config.LanguageHints, hint)
		}
	}
	text, terms := t.Prompt, []string(nil)
	if t.Context != nil {
		text, terms = cmp.Or(t.Context.Text, t.Prompt), t.Context.Terms
	}
	if text != "" || len(terms) > 0 {
		config.Context = &sonioxContext{Text: text, Terms: terms}
	}
	config.EnableSpeakerDiarization = t.EnableSpeakerDiarization
	config.EnableLanguageIdentification = t.EnableLanguageIdentification
	config.ClientReferenceID = t.ClientReferenceID

	frame, err := schemas.MarshalSorted(config)
	if err != nil {
		return nil, err
	}
	settings.frame = frame
	settings.input.Transcription = t
	return settings, nil
}

// sonioxNamesModel reports whether a session names its transcription model, which is what admits a
// transcription connection and so fixes its settings.
func sonioxNamesModel(session []byte) bool {
	return providerUtils.GetJSONField(session, "audio.input.transcription.model").String() != ""
}
