package gemini

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Compile-time proof that the provider is a realtime provider that serves both clients: one that speaks
// OpenAI Realtime, through a translator, and one that speaks Live, through its own events and usage.
var (
	_ schemas.RealtimeProvider           = (*GeminiProvider)(nil)
	_ schemas.RealtimeTranslatorProvider = (*GeminiProvider)(nil)
	_ schemas.RealtimeUsageExtractor     = (*GeminiProvider)(nil)
)

func newLiveTestProvider(config schemas.ProviderConfig) *GeminiProvider {
	return NewGeminiProvider(&config, testNoopLogger{})
}

func TestGeminiRealtimeWebSocketURL(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		baseURL string
		want    string
	}{
		"default":                      {"", "wss://generativelanguage.googleapis.com" + liveEndpointPath},
		"explicit api version":         {"https://generativelanguage.googleapis.com/v1beta", "wss://generativelanguage.googleapis.com" + liveEndpointPath},
		"plain http proxy":             {"http://localhost:8080/v1beta/", "ws://localhost:8080" + liveEndpointPath},
		"proxy under a path":           {"https://gateway.example.com/google/v1beta", "wss://gateway.example.com/google" + liveEndpointPath},
		"base url without api version": {"https://gateway.example.com", "wss://gateway.example.com" + liveEndpointPath},
	} {
		provider := newLiveTestProvider(schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: tc.baseURL}})
		got, bifrostErr := provider.RealtimeWebSocketURL(nil, schemas.Key{Value: *schemas.NewSecretVar("secret-key")}, "gemini-3.8-live", "")
		require.Nil(t, bifrostErr, name)
		require.Equal(t, tc.want, got, name)
		require.NotContains(t, got, "secret-key", "the key authenticates through a header, never the URL")
	}
}

func TestGeminiRealtimeRefusals(t *testing.T) {
	t.Parallel()
	provider := newLiveTestProvider(schemas.ProviderConfig{})

	_, bifrostErr := provider.RealtimeWebSocketURL(nil, schemas.Key{}, "gemini-3.8-live", "transcription")
	require.NotNil(t, bifrostErr, "Live has no transcription-only session")

	_, bifrostErr = provider.ExchangeRealtimeWebRTCSDP(nil, schemas.Key{}, "gemini-3.8-live", "sdp", nil)
	require.NotNil(t, bifrostErr)
	require.False(t, provider.SupportsRealtimeWebRTC())

	gated := newLiveTestProvider(schemas.ProviderConfig{CustomProviderConfig: &schemas.CustomProviderConfig{
		BaseProviderType: schemas.Gemini,
		AllowedRequests:  &schemas.AllowedRequests{ChatCompletion: true},
	}})
	_, bifrostErr = gated.RealtimeWebSocketURL(nil, schemas.Key{}, "gemini-3.8-live", "")
	require.NotNil(t, bifrostErr, "allowed_requests must be able to switch realtime off")
	allowed := newLiveTestProvider(schemas.ProviderConfig{CustomProviderConfig: &schemas.CustomProviderConfig{
		BaseProviderType: schemas.Gemini,
		AllowedRequests:  &schemas.AllowedRequests{Realtime: true},
	}})
	_, bifrostErr = allowed.RealtimeWebSocketURL(nil, schemas.Key{}, "gemini-3.8-live", "")
	require.Nil(t, bifrostErr)
}

func TestGeminiRealtimeHeaders(t *testing.T) {
	t.Parallel()
	provider := newLiveTestProvider(schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{
		ExtraHeaders: schemas.NewSecretVarMap(map[string]string{"x-team": "voice"}),
	}})

	headers, bifrostErr := provider.RealtimeHeaders(nil, schemas.Key{Value: *schemas.NewSecretVar("secret-key")})
	require.Nil(t, bifrostErr)
	require.Equal(t, map[string]string{"x-goog-api-key": "secret-key", "x-team": "voice"}, headers)

	headers, _ = provider.RealtimeHeaders(nil, schemas.Key{})
	require.NotContains(t, headers, "x-goog-api-key", "no key, no header")
}

func TestGeminiNewRealtimeTranslator(t *testing.T) {
	t.Parallel()
	provider := newLiveTestProvider(schemas.ProviderConfig{})

	translator, bifrostErr := provider.NewRealtimeTranslator(schemas.Key{}, "gemini-3.8-live")
	require.Nil(t, bifrostErr)
	require.NotNil(t, translator)
	other, _ := provider.NewRealtimeTranslator(schemas.Key{}, "gemini-3.8-live")
	require.NotSame(t, translator, other, "every connection needs its own translator, it holds the session's state")

	_, bifrostErr = provider.NewRealtimeTranslator(schemas.Key{}, "  ")
	require.NotNil(t, bifrostErr)
}

// liveSetupFor resolves a session.update body into the setup frame it produces.
func liveSetupFor(t *testing.T, session string) gjson.Result {
	t.Helper()
	// The session is read from the event's raw bytes, so it does not depend on how the schema decodes it.
	update, err := parseLiveSessionUpdate(&schemas.BifrostRealtimeEvent{RawData: json.RawMessage(`{"type":"session.update","session":` + session + `}`)})
	require.NoError(t, err)
	config, err := newLiveConfig("gemini-3.8-live", update)
	require.NoError(t, err)
	frame, err := schemas.MarshalSorted(&GeminiLiveClientMessage{Setup: config.setup})
	require.NoError(t, err)
	return gjson.ParseBytes(frame).Get("setup")
}

func TestLiveConfigReadsBothSpellingsOfTheProtocol(t *testing.T) {
	t.Parallel()

	beta := liveSetupFor(t, `{"modalities":["text"],"voice":"Puck","input_audio_format":"pcm16","input_audio_transcription":{"model":"whisper-1"},`+
		`"turn_detection":{"type":"server_vad","prefix_padding_ms":300,"silence_duration_ms":600},"max_response_output_tokens":128}`)
	ga := liveSetupFor(t, `{"type":"realtime","output_modalities":["text"],"max_output_tokens":128,"audio":{"input":{"format":{"type":"audio/pcm","rate":24000},`+
		`"transcription":{"model":"whisper-1"},"turn_detection":{"type":"server_vad","prefix_padding_ms":300,"silence_duration_ms":600}},"output":{"voice":"Puck"}}}`)

	require.JSONEq(t, beta.Raw, ga.Raw, "both shapes of the protocol must produce the same session")
	require.Equal(t, int64(300), ga.Get("realtimeInputConfig.automaticActivityDetection.prefixPaddingMs").Int())
	require.Equal(t, int64(600), ga.Get("realtimeInputConfig.automaticActivityDetection.silenceDurationMs").Int())
	require.True(t, ga.Get("inputAudioTranscription").Exists())
	require.Equal(t, int64(128), ga.Get("generationConfig.maxOutputTokens").Int())
}

func TestLiveConfigSettings(t *testing.T) {
	t.Parallel()

	t.Run("openai stock voices are left out", func(t *testing.T) {
		require.False(t, liveSetupFor(t, `{"voice":"alloy"}`).Get("generationConfig.speechConfig").Exists())
		require.False(t, liveSetupFor(t, `{"audio":{"output":{"voice":"Marin"}}}`).Get("generationConfig.speechConfig").Exists())
	})
	t.Run("a voice can be an object", func(t *testing.T) {
		require.Equal(t, "Kore", liveSetupFor(t, `{"voice":{"id":"Kore"}}`).Get("generationConfig.speechConfig.voice_config.prebuiltVoiceConfig.voiceName").String())
	})
	t.Run("no limit and no instructions send nothing", func(t *testing.T) {
		setup := liveSetupFor(t, `{"max_output_tokens":"inf","instructions":"  "}`)
		require.False(t, setup.Get("generationConfig.maxOutputTokens").Exists())
		require.False(t, setup.Get("systemInstruction").Exists())
	})
	t.Run("the reasoning effort becomes the thinking level", func(t *testing.T) {
		require.Equal(t, "high", liveSetupFor(t, `{"reasoning":{"effort":"HIGH"}}`).Get("generationConfig.thinkingConfig.thinkingLevel").String())
		require.False(t, liveSetupFor(t, `{}`).Get("generationConfig.thinkingConfig").Exists(), "models that do not think reject the field")
	})
	t.Run("server detection can be tuned or switched off", func(t *testing.T) {
		require.True(t, liveSetupFor(t, `{"turn_detection":null}`).Get("realtimeInputConfig.automaticActivityDetection.disabled").Bool())
		require.Equal(t, "NO_INTERRUPTION", liveSetupFor(t, `{"turn_detection":{"type":"server_vad","interrupt_response":false}}`).Get("realtimeInputConfig.activityHandling").String())
		require.False(t, liveSetupFor(t, `{"turn_detection":{"type":"semantic_vad"}}`).Get("realtimeInputConfig").Exists(), "the defaults are Live's own")
		require.False(t, liveSetupFor(t, `{}`).Get("realtimeInputConfig").Exists())
	})
	t.Run("tools", func(t *testing.T) {
		tools := `[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}},{"type":"function","function":{"name":"nested"}}]`
		setup := liveSetupFor(t, `{"tools":`+tools+`}`)
		require.Equal(t, "lookup", setup.Get("tools.0.functionDeclarations.0.name").String())
		require.Equal(t, "nested", setup.Get("tools.0.functionDeclarations.1.name").String(), "the chat completions shape is accepted too")
		require.False(t, liveSetupFor(t, `{"tool_choice":"none","tools":`+tools+`}`).Get("tools").Exists())
	})
	t.Run("a model with a resource prefix is not prefixed twice", func(t *testing.T) {
		config, err := newLiveConfig("models/gemini-3.8-live", &liveSessionUpdate{})
		require.NoError(t, err)
		require.Equal(t, "models/gemini-3.8-live", config.setup.Model)
	})
}

func TestLiveSetupHelpers(t *testing.T) {
	t.Parallel()

	for frame, want := range map[string]string{
		`{"setup":{"model":"models/gemini-3.8-live"}}`:        "gemini-3.8-live",
		`{"setup":{"model":"models/gemini/gemini-3.8-live"}}`: "gemini/gemini-3.8-live",
		`{"setup":{"model":" gemini-3.8-live "}}`:             "gemini-3.8-live",
	} {
		model, err := LiveSetupModel([]byte(frame))
		require.NoError(t, err, frame)
		require.Equal(t, want, model, frame)
	}
	for _, frame := range []string{`{"realtimeInput":{"text":"hi"}}`, `{"setup":{}}`, `{"setup":{"model":"models/"}}`, ``} {
		_, err := LiveSetupModel([]byte(frame))
		require.Error(t, err, "%q is not a setup naming a model", frame)
	}

	rewritten, err := LiveSetupWithModel([]byte(`{"setup":{"model":"models/gemini/alias","generationConfig":{"responseModalities":["AUDIO"]}}}`), "gemini-3.8-live")
	require.NoError(t, err)
	require.JSONEq(t, `{"setup":{"model":"models/gemini-3.8-live","generationConfig":{"responseModalities":["AUDIO"]}}}`, string(rewritten),
		"only the model changes, every other setting reaches Live as the client wrote it")
}

// nativeEvent classifies a frame the way the relay does for a client that speaks Live.
func nativeEvent(t *testing.T, frame string) *schemas.BifrostRealtimeEvent {
	t.Helper()
	event, err := newLiveTestProvider(schemas.ProviderConfig{}).ToBifrostRealtimeEvent(json.RawMessage(frame))
	require.NoError(t, err)
	require.Equal(t, frame, string(event.RawData), "the frame is relayed as it came in")
	return event
}

func TestGeminiNativeFramesFromLive(t *testing.T) {
	t.Parallel()
	provider := newLiveTestProvider(schemas.ProviderConfig{})

	for name, tc := range map[string]struct {
		frame      string
		eventType  schemas.RealtimeEventType
		startsTurn bool
	}{
		"audio with its transcript": {liveAudioFrame, schemas.RTEventResponseAudioTransDelta, true},
		"audio alone":               {liveAudioOnlyFrame, schemas.RTEventResponseAudioDelta, true},
		"a transcript alone":        {liveTranscriptFrame, schemas.RTEventResponseAudioTransDelta, true},
		"text parts":                {`{"serverContent":{"modelTurn":{"parts":[{"text":"Hel"},{"text":"lo"}]}}}`, schemas.RTEventResponseTextDelta, true},
		"a tool call":               {liveToolCallFrame, schemas.RTEventResponseOutputItemDone, true},
		"the end of a turn":         {liveTurnDoneUsage, schemas.RTEventResponseDone, false},
		"generation complete":       {liveGenerationDone, "", false},
		"setup complete":            {liveSetupComplete, "", false},
		"an empty frame":            {`{}`, "", false},
		"voice activity":            {`{"voiceActivity":{"voiceActivityType":"ACTIVITY_START"}}`, "", false},
		"the user's transcript":     {`{"serverContent":{"inputTranscription":{"text":"What is the capital of France?"}}}`, schemas.RTEventInputAudioTransCompleted, false},
	} {
		event := nativeEvent(t, tc.frame)
		require.Equal(t, tc.eventType, event.Type, name)
		require.Equal(t, tc.startsTurn, provider.ShouldStartRealtimeTurn(event), name)
	}

	require.Equal(t, "Hello ", nativeEvent(t, liveAudioFrame).Delta.Transcript)
	require.Equal(t, "Hello", nativeEvent(t, `{"serverContent":{"modelTurn":{"parts":[{"text":"Hel"},{"text":"lo"}]}}}`).Delta.Text)
	require.Nil(t, nativeEvent(t, `{"serverContent":{"modelTurn":{"parts":[{"text":"thinking","thought":true}]}}}`).Delta, "the model's reasoning is not its answer")
	require.JSONEq(t, `"What is the capital of France?"`, string(nativeEvent(t, `{"serverContent":{"inputTranscription":{"text":"What is the capital of France?"}}}`).ExtraParams["transcript"]))

	done := nativeEvent(t, `{"serverContent":{"modelTurn":{"parts":[{"text":"4"}]},"turnComplete":true},"usageMetadata":{"promptTokenCount":3}}`)
	require.Equal(t, schemas.RTEventResponseDone, done.Type, "a turn that ends in the same frame as its last words still ends")
	require.Equal(t, "4", done.Delta.Text, "and those words are not lost")
	require.Equal(t, schemas.RTEventResponseDone, provider.RealtimeTurnFinalEvent())
	for _, eventType := range []schemas.RealtimeEventType{schemas.RTEventResponseTextDelta, schemas.RTEventResponseAudioTransDelta, schemas.RTEventResponseDone} {
		require.True(t, provider.ShouldAccumulateRealtimeOutput(eventType), eventType)
	}
	require.False(t, provider.ShouldAccumulateRealtimeOutput(schemas.RTEventResponseAudioDelta))
}

func TestGeminiNativeFramesFromTheClient(t *testing.T) {
	t.Parallel()
	provider := newLiveTestProvider(schemas.ProviderConfig{})

	for name, tc := range map[string]struct {
		frame      string
		eventType  schemas.RealtimeEventType
		startsTurn bool
	}{
		"setup":                             {`{"setup":{"model":"models/gemini-3.8-live"}}`, "", false},
		"an audio chunk":                    {`{"realtimeInput":{"audio":{"data":"AQID","mimeType":"audio/pcm;rate=16000"}}}`, schemas.RTEventInputAudioAppend, false},
		"an audio chunk, snake case":        {`{"realtime_input":{"audio":{"data":"AQID","mime_type":"audio/pcm;rate=16000"}}}`, schemas.RTEventInputAudioAppend, false},
		"a video frame":                     {`{"realtimeInput":{"video":{"data":"AQID","mimeType":"image/jpeg"}}}`, "", false},
		"the end of the audio stream":       {`{"realtimeInput":{"audioStreamEnd":true}}`, schemas.RTEventInputAudioCommit, false},
		"the end of an activity":            {`{"realtimeInput":{"activityEnd":{}}}`, schemas.RTEventResponseCreate, true},
		"realtime text":                     {`{"realtimeInput":{"text":"Hello"}}`, schemas.RTEventResponseCreate, true},
		"a finished turn":                   {`{"clientContent":{"turns":[{"role":"user","parts":[{"text":"Hello"}]}],"turnComplete":true}}`, schemas.RTEventResponseCreate, true},
		"a finished turn, snake case":       {`{"client_content":{"turns":[{"role":"user","parts":[{"text":"Hello"}]}],"turn_complete":true}}`, schemas.RTEventResponseCreate, true},
		"context for a later turn":          {`{"clientContent":{"turns":[{"role":"user","parts":[{"text":"Hello"}]}]}}`, schemas.RTEventConversationItemCreate, false},
		"tool results":                      {`{"toolResponse":{"functionResponses":[{"id":"call_1","name":"get_weather","response":{"temp":18}}]}}`, schemas.RTEventResponseCreate, true},
		"a finished turn without any words": {`{"clientContent":{"turnComplete":true}}`, schemas.RTEventResponseCreate, true},
	} {
		event := nativeEvent(t, tc.frame)
		require.Equal(t, tc.eventType, event.Type, name)
		require.Equal(t, tc.startsTurn, provider.ShouldStartRealtimeTurn(event), name)
	}

	// The turn pipeline reads a turn's input from the response.create body, and a held turn from an item.
	turn := nativeEvent(t, `{"realtimeInput":{"text":"Hello"}}`)
	require.JSONEq(t, `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello"}]}]}`, string(turn.ExtraParams["response"]))
	held := nativeEvent(t, `{"clientContent":{"turns":[{"role":"user","parts":[{"text":"Hello"},{"text":"again"}]}]}}`)
	require.Equal(t, "user", held.Item.Role)
	require.JSONEq(t, `[{"type":"input_text","text":"Hello\nagain"}]`, string(held.Item.Content))
	require.Empty(t, nativeEvent(t, `{"toolResponse":{"functionResponses":[]}}`).ExtraParams, "tool results carry no text to log")

	relayed, err := provider.ToProviderRealtimeEvent(turn)
	require.NoError(t, err)
	require.Equal(t, string(turn.RawData), string(relayed), "the client's frame goes upstream untouched")
	_, err = provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{})
	require.Error(t, err)
}

func TestGeminiNativeUsage(t *testing.T) {
	t.Parallel()
	provider := newLiveTestProvider(schemas.ProviderConfig{})

	usage := provider.ExtractRealtimeTurnUsage([]byte(liveTurnDoneUsage))
	require.NotNil(t, usage)
	require.Equal(t, 548, usage.PromptTokens)
	require.Equal(t, 70, usage.CompletionTokens, "thinking tokens count as output, like the chat converter counts them")
	require.Equal(t, 618, usage.TotalTokens)
	require.Equal(t, 306, usage.PromptTokensDetails.TextTokens)
	require.Equal(t, 222, usage.PromptTokensDetails.AudioTokens)
	require.Equal(t, 30, usage.CompletionTokensDetails.AudioTokens)
	require.Equal(t, 40, usage.CompletionTokensDetails.ReasoningTokens)

	// Both routes bill the same turn the same way.
	translator := newLiveTestTranslator(t)
	events := liveReceiveAll(t, translator, liveTurnDoneUsage)
	done := liveFind(t, events, "response.done").Get("response.usage")
	require.Equal(t, done.Get("input_tokens").Int(), int64(usage.PromptTokens))
	require.Equal(t, done.Get("output_tokens").Int(), int64(usage.CompletionTokens))
	require.Equal(t, done.Get("total_tokens").Int(), int64(usage.TotalTokens))

	require.Nil(t, provider.ExtractRealtimeTurnUsage([]byte(liveAudioFrame)), "only the frame that ends a turn carries usage")
	require.Nil(t, provider.ExtractRealtimeTurnOutput([]byte(liveTurnDoneUsage)))
}

func BenchmarkGeminiNativeAudioFrame(b *testing.B) {
	provider := newLiveTestProvider(schemas.ProviderConfig{})
	frame := json.RawMessage(`{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"` + strings.Repeat("AQADAAQAEgA=", 1400) + `"}}]},"outputTranscription":{"text":"Hello "}}}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for b.Loop() {
		event, _ := provider.ToBifrostRealtimeEvent(frame)
		provider.ShouldStartRealtimeTurn(event)
	}
}

func BenchmarkGeminiNativeAudioChunkFromTheClient(b *testing.B) {
	provider := newLiveTestProvider(schemas.ProviderConfig{})
	frame := json.RawMessage(`{"realtimeInput":{"audio":{"data":"` + strings.Repeat("AQADAAQAEgA=", 1400) + `","mimeType":"audio/pcm;rate=16000"}}}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for b.Loop() {
		event, _ := provider.ToBifrostRealtimeEvent(frame)
		provider.ShouldStartRealtimeTurn(event)
	}
}
