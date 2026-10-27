package gemini

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// liveAudioRate is the PCM sample rate of OpenAI's pcm16 and of everything Live sends back.
	liveAudioRate = 24000
	// liveAudioFormat is the only audio format both protocols share.
	liveAudioFormat = "audio/pcm"
)

// liveOpenAIVoices are OpenAI's stock voices. Live has none of them, so they are left out of the
// setup and the model speaks in its default voice.
var liveOpenAIVoices = []string{"alloy", "ash", "ballad", "coral", "echo", "sage", "shimmer", "verse", "marin", "cedar"}

// liveSessionUpdate is the part of an OpenAI session.update that a Live session can honor. It reads
// both shapes of the protocol: the beta one with flat fields and the GA one under audio.input and
// audio.output. Anything else in the event is ignored.
type liveSessionUpdate struct {
	Instructions            string                          `json:"instructions"`
	Modalities              []string                        `json:"modalities"`
	OutputModalities        []string                        `json:"output_modalities"`
	Voice                   liveVoice                       `json:"voice"`
	Temperature             *float64                        `json:"temperature"`
	MaxOutputTokens         liveMaxTokens                   `json:"max_output_tokens"`
	MaxResponseOutputTokens liveMaxTokens                   `json:"max_response_output_tokens"`
	InputAudioFormat        string                          `json:"input_audio_format"`
	OutputAudioFormat       string                          `json:"output_audio_format"`
	InputAudioTranscription liveOptional[struct{}]          `json:"input_audio_transcription"`
	TurnDetection           liveOptional[liveTurnDetection] `json:"turn_detection"`
	Tools                   []liveTool                      `json:"tools"`
	ToolChoice              liveToolChoice                  `json:"tool_choice"`
	Reasoning               *liveReasoning                  `json:"reasoning"`
	Audio                   *liveAudioConfig                `json:"audio"`
}

type liveAudioConfig struct {
	Input  *liveAudioInput  `json:"input"`
	Output *liveAudioOutput `json:"output"`
}

type liveAudioInput struct {
	Format        liveSessionAudioFormat          `json:"format"`
	Transcription liveOptional[struct{}]          `json:"transcription"`
	TurnDetection liveOptional[liveTurnDetection] `json:"turn_detection"`
}

type liveAudioOutput struct {
	Format liveSessionAudioFormat `json:"format"`
	Voice  liveVoice              `json:"voice"`
}

type liveSessionAudioFormat struct {
	Type string `json:"type"`
	Rate int    `json:"rate"`
}

type liveTurnDetection struct {
	Type              string `json:"type,omitempty"`
	PrefixPaddingMs   int    `json:"prefix_padding_ms,omitempty"`
	SilenceDurationMs int    `json:"silence_duration_ms,omitempty"`
	InterruptResponse *bool  `json:"interrupt_response,omitempty"`
}

type liveReasoning struct {
	Effort string `json:"effort"`
}

// liveTool is an OpenAI realtime tool. The protocol sends functions flat; the chat completions
// shape, nested under function, is accepted as well.
type liveTool struct {
	Type        string                          `json:"type"`
	Name        string                          `json:"name,omitempty"`
	Description string                          `json:"description,omitempty"`
	Parameters  *schemas.ToolFunctionParameters `json:"parameters,omitempty"`
	Function    *schemas.ChatToolFunction       `json:"function,omitempty"`
}

// liveOptional tells a field that is absent from one that is set to null: a null turn_detection
// turns server-side detection off, while an absent one leaves the default.
type liveOptional[T any] struct {
	Set   bool
	Value *T
}

func (o *liveOptional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	o.Value = new(T)
	return json.Unmarshal(data, o.Value)
}

// liveVoice is a voice name, sent as a string or as an object carrying an id or a name.
type liveVoice string

func (v *liveVoice) UnmarshalJSON(data []byte) error {
	var name string
	if json.Unmarshal(data, &name) == nil {
		*v = liveVoice(name)
		return nil
	}
	var object struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	*v = liveVoice(cmp.Or(object.Name, object.ID))
	return nil
}

// liveMaxTokens is an output token limit, a number or "inf" for none.
type liveMaxTokens struct {
	Limit *int
}

func (m *liveMaxTokens) UnmarshalJSON(data []byte) error {
	var limit int
	if json.Unmarshal(data, &limit) == nil {
		m.Limit = &limit
	}
	return nil
}

func (m liveMaxTokens) MarshalJSON() ([]byte, error) {
	if m.Limit == nil {
		return []byte(`"inf"`), nil
	}
	return json.Marshal(*m.Limit)
}

// liveToolChoice is the tool_choice setting, a string or an object. Only the string none changes
// anything, because Live cannot force or forbid a call any other way.
type liveToolChoice string

func (c *liveToolChoice) UnmarshalJSON(data []byte) error {
	var choice string
	if json.Unmarshal(data, &choice) == nil {
		*c = liveToolChoice(choice)
	}
	return nil
}

// liveConfig is a session.update resolved into the setup frame plus the settings the translator
// needs later.
type liveConfig struct {
	setup     *GeminiLiveSetup
	audioMime string
	textOnly  bool // the client wants text, which Live only gives as the transcript of its audio
	manualVAD bool // the client marks utterances itself, so audio is bracketed by activityStart/End
	session   liveSession
}

// liveSession is the session object echoed back to the client.
type liveSession struct {
	ID               string           `json:"id"`
	Object           string           `json:"object"`
	Type             string           `json:"type"`
	Model            string           `json:"model"`
	OutputModalities []string         `json:"output_modalities"`
	Instructions     string           `json:"instructions"`
	Audio            liveSessionAudio `json:"audio"`
	Tools            []liveTool       `json:"tools"`
	ToolChoice       string           `json:"tool_choice"`
	MaxOutputTokens  liveMaxTokens    `json:"max_output_tokens"`
}

type liveSessionAudio struct {
	Input  liveSessionAudioInput  `json:"input"`
	Output liveSessionAudioOutput `json:"output"`
}

type liveSessionAudioInput struct {
	Format        liveSessionAudioFormat `json:"format"`
	TurnDetection *liveTurnDetection     `json:"turn_detection"`
}

type liveSessionAudioOutput struct {
	Format liveSessionAudioFormat `json:"format"`
	Voice  string                 `json:"voice,omitempty"`
}

// parseLiveSessionUpdate reads the session object of a session.update event.
func parseLiveSessionUpdate(event *schemas.BifrostRealtimeEvent) (*liveSessionUpdate, error) {
	raw := providerUtils.GetJSONField(event.RawData, "session").Raw
	if raw == "" {
		return &liveSessionUpdate{}, nil
	}
	update := &liveSessionUpdate{}
	if err := schemas.Unmarshal([]byte(raw), update); err != nil {
		return nil, fmt.Errorf("invalid session in session.update: %w", err)
	}
	return update, nil
}

// newLiveConfig resolves a session.update into a Live setup. An error names a setting Live cannot
// honor, so the client learns about it instead of getting a session that quietly differs.
func newLiveConfig(model string, update *liveSessionUpdate) (*liveConfig, error) {
	var input liveAudioInput
	var output liveAudioOutput
	if update.Audio != nil {
		if update.Audio.Input != nil {
			input = *update.Audio.Input
		}
		if update.Audio.Output != nil {
			output = *update.Audio.Output
		}
	}

	rate, err := liveInputRate(update.InputAudioFormat, input.Format)
	if err != nil {
		return nil, err
	}
	if err := checkLiveOutputFormat(update.OutputAudioFormat, output.Format); err != nil {
		return nil, err
	}

	setup := &GeminiLiveSetup{
		Model: "models/" + strings.TrimPrefix(model, "models/"),
		GenerationConfig: &GeminiLiveGenerationConfig{
			// Live models answer in audio only. Text, when asked for, is the transcript of it.
			ResponseModalities: []string{"AUDIO"},
			Temperature:        update.Temperature,
		},
		OutputAudioTranscription: &GeminiLiveAudioTranscription{},
	}
	if instructions := strings.TrimSpace(update.Instructions); instructions != "" {
		setup.SystemInstruction = &Content{Parts: []*Part{{Text: update.Instructions}}}
	}
	maxTokens := cmp.Or(update.MaxOutputTokens, update.MaxResponseOutputTokens)
	setup.GenerationConfig.MaxOutputTokens = maxTokens.Limit

	voice := cmp.Or(output.Voice, update.Voice)
	if name := string(voice); name != "" && !slices.Contains(liveOpenAIVoices, strings.ToLower(name)) {
		setup.GenerationConfig.SpeechConfig = &SpeechConfig{VoiceConfig: &VoiceConfig{PrebuiltVoiceConfig: &PrebuiltVoiceConfig{VoiceName: name}}}
	}
	if update.Reasoning != nil && update.Reasoning.Effort != "" {
		level := strings.ToLower(update.Reasoning.Effort)
		setup.GenerationConfig.ThinkingConfig = &GenerationConfigThinkingConfig{ThinkingLevel: &level}
	}
	if update.InputAudioTranscription.Value != nil || input.Transcription.Value != nil {
		setup.InputAudioTranscription = &GeminiLiveAudioTranscription{}
	}

	detection := input.TurnDetection
	if !detection.Set {
		detection = update.TurnDetection
	}
	manualVAD := detection.Set && detection.Value == nil
	setup.RealtimeInputConfig = liveInputConfig(detection)

	tools, err := liveTools(update.Tools)
	if err != nil {
		return nil, err
	}
	if update.ToolChoice != "none" {
		setup.Tools = tools
	}

	textOnly, _ := liveWantsTextOnly(update.OutputModalities, update.Modalities)
	config := &liveConfig{
		setup:     setup,
		audioMime: fmt.Sprintf("%s;rate=%d", liveAudioFormat, rate),
		textOnly:  textOnly,
		manualVAD: manualVAD,
		session: liveSession{
			Object:           "realtime.session",
			Type:             "realtime",
			Model:            model,
			OutputModalities: []string{"audio"},
			Instructions:     update.Instructions,
			Tools:            []liveTool{},
			ToolChoice:       cmp.Or(string(update.ToolChoice), "auto"),
			MaxOutputTokens:  maxTokens,
			Audio: liveSessionAudio{
				Input:  liveSessionAudioInput{Format: liveSessionAudioFormat{Type: liveAudioFormat, Rate: rate}},
				Output: liveSessionAudioOutput{Format: liveSessionAudioFormat{Type: liveAudioFormat, Rate: liveAudioRate}, Voice: string(voice)},
			},
		},
	}
	if update.Tools != nil {
		config.session.Tools = update.Tools
	}
	if textOnly {
		config.session.OutputModalities = []string{"text"}
	}
	if !manualVAD {
		config.session.Audio.Input.TurnDetection = cmp.Or(detection.Value, &liveTurnDetection{Type: "server_vad"})
	}
	return config, nil
}

// liveWantsTextOnly reports whether the client asked for text and not audio, in either spelling of
// the setting. ok is false when it asked for neither.
func liveWantsTextOnly(modalities ...[]string) (textOnly, ok bool) {
	for _, requested := range modalities {
		if len(requested) > 0 {
			return !slices.Contains(requested, "audio"), true
		}
	}
	return false, false
}

// liveInputRate returns the sample rate of the audio the client sends, which Live resamples when
// the MIME type states it.
func liveInputRate(beta string, format liveSessionAudioFormat) (int, error) {
	if format.Type != "" && format.Type != liveAudioFormat {
		return 0, fmt.Errorf("input audio format %q is not supported by gemini live, use %s", format.Type, liveAudioFormat)
	}
	if beta != "" && beta != "pcm16" {
		return 0, fmt.Errorf("input audio format %q is not supported by gemini live, use pcm16", beta)
	}
	return cmp.Or(format.Rate, liveAudioRate), nil
}

// checkLiveOutputFormat rejects an output format other than the 24 kHz PCM Live always sends.
func checkLiveOutputFormat(beta string, format liveSessionAudioFormat) error {
	if (format.Type != "" && format.Type != liveAudioFormat) || (beta != "" && beta != "pcm16") || (format.Rate != 0 && format.Rate != liveAudioRate) {
		return fmt.Errorf("gemini live always answers in %d Hz pcm16 audio", liveAudioRate)
	}
	return nil
}

// liveInputConfig maps turn_detection onto Live's activity detection. A null turn_detection hands
// turn boundaries to the client.
func liveInputConfig(detection liveOptional[liveTurnDetection]) *GeminiLiveRealtimeInputConfig {
	if !detection.Set {
		return nil
	}
	if detection.Value == nil {
		return &GeminiLiveRealtimeInputConfig{AutomaticActivityDetection: &GeminiLiveActivityDetection{Disabled: true}}
	}
	config := &GeminiLiveRealtimeInputConfig{}
	activity := &GeminiLiveActivityDetection{}
	if padding := detection.Value.PrefixPaddingMs; padding > 0 {
		activity.PrefixPaddingMs = &padding
	}
	if silence := detection.Value.SilenceDurationMs; silence > 0 {
		activity.SilenceDurationMs = &silence
	}
	if *activity != (GeminiLiveActivityDetection{}) {
		config.AutomaticActivityDetection = activity
	}
	if interrupt := detection.Value.InterruptResponse; interrupt != nil && !*interrupt {
		config.ActivityHandling = "NO_INTERRUPTION"
	}
	if config.AutomaticActivityDetection == nil && config.ActivityHandling == "" {
		return nil
	}
	return config
}

// liveTools converts realtime function tools into Live function declarations.
func liveTools(tools []liveTool) ([]Tool, error) {
	functions := make([]schemas.ChatTool, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "function" {
			return nil, fmt.Errorf("tools of type %q are not supported by gemini live, only functions are", tool.Type)
		}
		function := tool.Function
		if function == nil {
			function = &schemas.ChatToolFunction{Name: tool.Name, Parameters: tool.Parameters}
			if tool.Description != "" {
				function.Description = &tool.Description
			}
		}
		if function.Name == "" {
			return nil, errors.New("a function tool needs a name")
		}
		functions = append(functions, schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: function})
	}
	if len(functions) == 0 {
		return nil, nil
	}
	return convertBifrostToolsToGemini(functions)
}
