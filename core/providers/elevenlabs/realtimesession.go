package elevenlabs

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// scribePCMRates are the sample rates Scribe takes PCM at. OpenAI clients send 24 kHz by default.
var scribePCMRates = []int{8000, 16000, 22050, 24000, 44100, 48000}

// scribeStringList is a setting Scribe takes as one value or a list, such as entity_detection.
type scribeStringList []string

func (l *scribeStringList) UnmarshalJSON(data []byte) error {
	var one string
	if json.Unmarshal(data, &one) == nil {
		*l = scribeStringList{one}
		return nil
	}
	return json.Unmarshal(data, (*[]string)(l))
}

// scribeOptional tells a field that is absent from one that is set to null: a null turn_detection
// turns server-side detection off, while an absent one leaves the default.
type scribeOptional[T any] struct {
	Set   bool
	Value *T
}

func (o *scribeOptional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	o.Value = new(T)
	return json.Unmarshal(data, o.Value)
}

type scribeAudioFormat struct {
	Type string `json:"type,omitempty"`
	Rate int    `json:"rate,omitempty"`
}

// scribeTranscription is audio.input.transcription. Model, language and prompt are OpenAI's; the
// other fields are Scribe's own connect options, named as Scribe names them.
type scribeTranscription struct {
	Model                    string           `json:"model,omitempty"`
	Language                 string           `json:"language,omitempty"`
	SecondaryLanguages       []string         `json:"secondary_languages,omitempty"`
	Keyterms                 []string         `json:"keyterms,omitempty"`
	IncludeTimestamps        bool             `json:"include_timestamps,omitempty"`
	IncludeLanguageDetection bool             `json:"include_language_detection,omitempty"`
	NoVerbatim               bool             `json:"no_verbatim,omitempty"`
	EntityDetection          scribeStringList `json:"entity_detection,omitempty"`
	EntityRedaction          scribeStringList `json:"entity_redaction,omitempty"`
	EntityRedactionMode      string           `json:"entity_redaction_mode,omitempty"`
	TranscriptEdit           string           `json:"transcript_edit,omitempty"`
	FilterBackgroundAudio    bool             `json:"filter_background_audio,omitempty"`
	KeepaliveIntervalMs      int              `json:"keepalive_interval_ms,omitempty"`
	MinSpeechDurationMs      int              `json:"min_speech_duration_ms,omitempty"`
	MinSilenceDurationMs     int              `json:"min_silence_duration_ms,omitempty"`
	VADThreshold             *float64         `json:"vad_threshold,omitempty"`
}

type scribeTurnDetection struct {
	Type              string `json:"type,omitempty"`
	SilenceDurationMs int    `json:"silence_duration_ms,omitempty"`
}

// scribeSessionUpdate is the part of a transcription session.update's session that Scribe reads.
type scribeSessionUpdate struct {
	Bifrost struct {
		Extensions bool `json:"extensions"`
	} `json:"bifrost"`
	Audio struct {
		Input struct {
			Format        scribeAudioFormat                   `json:"format"`
			Transcription scribeTranscription                 `json:"transcription"`
			TurnDetection scribeOptional[scribeTurnDetection] `json:"turn_detection"`
		} `json:"input"`
	} `json:"audio"`
}

// scribeSettings is what a session.update fixes about a Scribe connection. Scribe takes every setting in
// the connect URL and cannot change one afterwards. extensions is Bifrost's own option, not part of the URL,
// so a later session.update may change it.
type scribeSettings struct {
	rate           int                // the sample rate of the audio the client sends
	bytesPerSecond int                // the size of a second of that audio
	query          string             // the connect parameters, without the model
	input          scribeSessionInput // the settings as the client is told them
	extensions     bool               // the client asked for the Bifrost extension events
}

// newScribeSettings reads the settings a session names. An absent turn_detection is OpenAI's default,
// server-side detection, which Scribe calls vad; a null one hands commits to the client. The audio is
// OpenAI's default, 24 kHz PCM, unless the session says otherwise.
func newScribeSettings(session json.RawMessage) (*scribeSettings, error) {
	var update scribeSessionUpdate
	if len(session) > 0 {
		if err := schemas.Unmarshal(session, &update); err != nil {
			return nil, fmt.Errorf("invalid session in session.update: %w", err)
		}
	}
	input := update.Audio.Input
	params := url.Values{}
	settings := &scribeSettings{extensions: update.Bifrost.Extensions}

	switch input.Format.Type {
	case "", "audio/pcm":
		rate := cmp.Or(input.Format.Rate, 24000)
		if !slices.Contains(scribePCMRates, rate) {
			return nil, fmt.Errorf("scribe takes pcm audio at 8000, 16000, 22050, 24000, 44100 or 48000 Hz, not %d", rate)
		}
		params.Set("audio_format", "pcm_"+strconv.Itoa(rate))
		settings.rate, settings.bytesPerSecond = rate, rate*2
		settings.input.Format = scribeAudioFormat{Type: "audio/pcm", Rate: rate}
	case "audio/pcmu":
		params.Set("audio_format", "ulaw_8000")
		settings.rate, settings.bytesPerSecond = 8000, 8000
		settings.input.Format = scribeAudioFormat{Type: "audio/pcmu"}
	case "audio/pcma":
		return nil, errors.New("scribe has no A-law input; send audio/pcm or audio/pcmu")
	default:
		return nil, fmt.Errorf("scribe takes audio/pcm or audio/pcmu input, not %q", input.Format.Type)
	}

	detection := input.TurnDetection
	switch {
	case detection.Set && detection.Value == nil:
		params.Set("commit_strategy", "manual")
	default:
		strategy := "vad"
		settings.input.TurnDetection = &scribeTurnDetection{Type: "server_vad"}
		if detection.Value != nil {
			switch detection.Value.Type {
			case "", "server_vad":
			case "semantic_vad":
				strategy = "turn_prediction"
				settings.input.TurnDetection.Type = "semantic_vad"
			default:
				return nil, fmt.Errorf("scribe takes server_vad or semantic_vad turn detection, not %q", detection.Value.Type)
			}
			if silence := detection.Value.SilenceDurationMs; silence > 0 && strategy == "vad" {
				params.Set("vad_silence_threshold_secs", strconv.FormatFloat(float64(silence)/1000, 'f', -1, 64))
				settings.input.TurnDetection.SilenceDurationMs = silence
			}
		}
		params.Set("commit_strategy", strategy)
	}

	t := input.Transcription
	if t.Language != "" {
		params.Set("language_code", t.Language)
	}
	for name, values := range map[string][]string{
		"secondary_languages": t.SecondaryLanguages,
		"keyterms":            t.Keyterms,
		"entity_detection":    t.EntityDetection,
		"entity_redaction":    t.EntityRedaction,
	} {
		for _, value := range values {
			params.Add(name, value)
		}
	}
	for name, enabled := range map[string]bool{
		"include_timestamps":         t.IncludeTimestamps,
		"include_language_detection": t.IncludeLanguageDetection,
		"no_verbatim":                t.NoVerbatim,
		"filter_background_audio":    t.FilterBackgroundAudio,
	} {
		if enabled {
			params.Set(name, "true")
		}
	}
	for name, value := range map[string]string{
		"entity_redaction_mode": t.EntityRedactionMode,
		"transcript_edit":       t.TranscriptEdit,
	} {
		if value != "" {
			params.Set(name, value)
		}
	}
	for name, value := range map[string]int{
		"keepalive_interval_ms":   t.KeepaliveIntervalMs,
		"min_speech_duration_ms":  t.MinSpeechDurationMs,
		"min_silence_duration_ms": t.MinSilenceDurationMs,
	} {
		if value > 0 {
			params.Set(name, strconv.Itoa(value))
		}
	}
	if t.VADThreshold != nil {
		params.Set("vad_threshold", strconv.FormatFloat(*t.VADThreshold, 'f', -1, 64))
	}

	settings.input.Transcription = t
	settings.query = params.Encode()
	return settings, nil
}

// mergeScribeSession lays a session.update's fields over the settings sent before it, the way OpenAI
// applies one: a field the update leaves out keeps its value, an object is merged field by field, and
// a null is a value, since null turn_detection turns server-side detection off.
func mergeScribeSession(base, update json.RawMessage) (json.RawMessage, error) {
	if len(update) == 0 {
		return base, nil
	}
	if len(base) == 0 {
		return update, nil
	}
	var merged, patch map[string]json.RawMessage
	if err := schemas.Unmarshal(base, &merged); err != nil {
		return nil, fmt.Errorf("invalid session in session.update: %w", err)
	}
	if err := schemas.Unmarshal(update, &patch); err != nil {
		return nil, fmt.Errorf("invalid session in session.update: %w", err)
	}
	if merged == nil {
		merged = make(map[string]json.RawMessage, len(patch))
	}
	for key, value := range patch {
		if previous, ok := merged[key]; ok && gjson.ParseBytes(previous).IsObject() && gjson.ParseBytes(value).IsObject() {
			nested, err := mergeScribeSession(previous, value)
			if err != nil {
				return nil, err
			}
			value = nested
		}
		merged[key] = value
	}
	return schemas.MarshalSorted(merged)
}

// scribeNamesModel reports whether a session names its transcription model, which is what admits a
// transcription connection and so fixes its settings.
func scribeNamesModel(session json.RawMessage) bool {
	return gjson.GetBytes(session, "audio.input.transcription.model").String() != ""
}
