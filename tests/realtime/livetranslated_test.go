package realtime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Gemini and Vertex Live through /v1/realtime (Vertex against the real service only): an app that speaks OpenAI Realtime, served by the gateway's
// translator. Frames are translated both ways, the session's settings become Live's setup, and each turn
// is billed and logged as on any other realtime provider.

// translatedTarget is one Live model an OpenAI Realtime client can address.
type translatedTarget struct {
	name     string
	model    string // as the client writes it in the model query
	provider string
	wantPath string // the model path the upstream's setup carries; fake mode only
}

var translatedTargets = []translatedTarget{
	{"vertex", "vertex/" + vertexLiveModel, "vertex", ""},
	{"gemini", "gemini/" + geminiLiveModel, "gemini", "models/gemini-3.1-flash-live-preview"},
}

// geminiTranslated is the one target both modes serve.
var geminiTranslated = translatedTargets[1]

// servingTranslated lists the targets the mode can serve: Vertex AI has no fake, so in fake mode its tests are
// left to the real service.
func servingTranslated() []translatedTarget {
	if fake == nil {
		return translatedTargets
	}
	return []translatedTarget{geminiTranslated}
}

// eachTranslated runs a test on every target the mode serves, as parallel subtests named for them.
func eachTranslated(t *testing.T, run func(t *testing.T, target translatedTarget)) {
	t.Helper()
	for _, target := range servingTranslated() {
		t.Run(target.name, func(t *testing.T) {
			requireUpstream(t)
			t.Parallel()
			run(t, target)
		})
	}
}

// liveSession is a session.update for a Live model: audio out, since Live answers in nothing else, with
// the marker in the instructions for the fake to tell sessions apart.
func liveSession(marker string, extra map[string]any) map[string]any {
	config := map[string]any{
		"type": "realtime", "output_modalities": []string{"audio"},
		"instructions": "Answer in five words or fewer. Session " + marker,
	}
	for name, value := range extra {
		config[name] = value
	}
	return config
}

// openTranslated connects an OpenAI Realtime client to a Live model and configures it.
func openTranslated(t *testing.T, target translatedTarget, vk virtualKey, extra map[string]any) (*session, string) {
	t.Helper()
	s := openSession(t, dial{vk: vk, query: "model=" + target.model})
	marker := liveMarker()
	s.configure(liveSession(marker, extra))
	return s, marker
}

func TestLiveTranslated_TextTurnIsTranslatedAndLogged(t *testing.T) {
	eachTranslated(t, func(t *testing.T, target translatedTarget) {
		vk := createVirtualKey(t, keySpec{})
		s, marker := openTranslated(t, target, vk, nil)

		done := s.ask("Say hello.")

		assert.NotEmpty(t, replyText(done))
		row := s.turnRow()
		assert.Equal(t, "success", row.Status)
		assert.Equal(t, target.provider, row.Provider)
		assert.Equal(t, strings.TrimPrefix(target.model, target.provider+"/"), row.Model)
		assert.Equal(t, usageOf(done).input, row.TokenUsage.Input)
		assert.Equal(t, usageOf(done).output, row.TokenUsage.Output)
		assert.Equal(t, replyText(done), row.outputText())

		if fake == nil {
			return
		}
		up := fake.WaitSession(t, marker)
		assert.Equal(t, target.wantPath, up.Setup().Get("model").Str)
		assert.Equal(t, "echo: Say hello.", replyText(done))
		assert.Contains(t, up.Setup().Get("systemInstruction.parts.0.text").Str, "Answer in five words or fewer")
		assert.Equal(t, up.Turns()[0].Usage.Prompt, usageOf(done).input)
		assert.Equal(t, up.Turns()[0].Usage.Output, usageOf(done).output)
		assert.Equal(t, fakeGeminiKey, up.Header("X-Goog-Api-Key"))
		requireNoLeak(t, vk, up.header)
	})
}

// The user's message is one entry of the turn's input, as it is for OpenAI.
func TestLiveTranslated_UserMessageIsLoggedOnce(t *testing.T) {
	for _, target := range servingTranslated() {
		t.Run(target.name, func(t *testing.T) {
			requireUpstream(t)
			t.Parallel()
			vk := createVirtualKey(t, keySpec{})
			s, _ := openTranslated(t, target, vk, nil)

			s.ask("Say hello.")

			row := s.turnRow()
			assert.Len(t, row.raw.Get("input_history").Array(), 1, "input_history: %s", row.raw.Get("input_history").Raw)
		})
	}
}

// A session that asks for text gets the transcript of the model's speech as text events.
func TestLiveTranslated_TextOnlySessionGetsTheTranscriptAsText(t *testing.T) {
	eachTranslated(t, func(t *testing.T, target translatedTarget) {
		vk := createVirtualKey(t, keySpec{})
		s := openSession(t, dial{vk: vk, query: "model=" + target.model})
		s.configure(liveSession(liveMarker(), map[string]any{"output_modalities": []string{"text"}}))

		s.client.send(userMessage("Say hello."), responseCreate())
		frames := s.client.until("response.done")

		assert.Positive(t, countType(frames, "response.output_text.delta"))
		assert.Zero(t, countType(frames, "response.output_audio.delta"), "a text session is sent no audio")
		assert.NotEmpty(t, replyText(frames[len(frames)-1]))
	})
}

// The settings of a session.update become Live's setup, which Live fixes when the connection opens.
func TestLiveTranslated_SessionUpdateBecomesTheSetup(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	s := openSession(t, dial{vk: vk, query: "model=" + geminiTranslated.model})
	marker := liveMarker()

	s.configure(liveSession(marker, map[string]any{
		"temperature": 0.3,
		"tools":       []any{weatherTool},
		"audio": map[string]any{
			"input":  map[string]any{"transcription": map[string]any{}, "turn_detection": nil},
			"output": map[string]any{"voice": "Puck"},
		},
	}))
	s.ask("Say hello.")

	setup := fake.WaitSession(t, marker).Setup()
	// Google reads a proto field by either spelling, and the translator writes voice_config.
	voice := setup.Get("generationConfig.speechConfig.voice_config.prebuiltVoiceConfig.voiceName").Str + setup.Get("generationConfig.speechConfig.voiceConfig.prebuiltVoiceConfig.voiceName").Str
	assert.Equal(t, "Puck", voice)
	assert.InDelta(t, 0.3, setup.Get("generationConfig.temperature").Float(), 1e-9)
	assert.Equal(t, "get_weather", setup.Get("tools.0.functionDeclarations.0.name").Str)
	assert.True(t, setup.Get("inputAudioTranscription").Exists())
	assert.True(t, setup.Get("outputAudioTranscription").Exists())
	assert.True(t, setup.Get("realtimeInputConfig.automaticActivityDetection.disabled").Bool(), "a null turn_detection hands turns to the client")
}

// A function call ends one turn; the app answers it and asks for the next response.
func TestLiveTranslated_ToolCallRoundTrip(t *testing.T) {
	for _, target := range servingTranslated() {
		t.Run(target.name, func(t *testing.T) {
			requireUpstream(t)
			t.Parallel()
			vk := createVirtualKey(t, keySpec{})
			s, _ := openTranslated(t, target, vk, map[string]any{"tools": []any{weatherTool}})

			s.client.send(userMessage("What is the weather in Paris right now? Use the get_weather function."), responseCreate())
			call := functionCall(s.responseDone())
			require.Equal(t, "get_weather", call.Get("name").Str, "function call: %s", call.Raw)
			s.client.send(functionCallOutput(call.Get("call_id").Str, `{"conditions":"sunny"}`), responseCreate())
			done := s.responseDone()

			assert.NotEmpty(t, replyText(done))
			rows := s.turnRows(2)
			assert.Equal(t, []string{"get_weather"}, rows[0].toolCalls())
			assert.Contains(t, rows[1].inputText(), `{"conditions":"sunny"}`)
			if fake != nil {
				assert.Contains(t, replyText(done), "sunny")
			}
		})
	}
}

// Speech the app streams is answered by Live's own turn detection, and the transcript of it is the turn's input.
func TestLiveTranslated_SpokenQuestionStartsAResponse(t *testing.T) {
	for _, target := range servingTranslated() {
		t.Run(target.name, func(t *testing.T) {
			requireUpstream(t)
			t.Parallel()
			vk := createVirtualKey(t, keySpec{})
			s, _ := openTranslated(t, target, vk, map[string]any{"audio": map[string]any{"input": map[string]any{"transcription": map[string]any{}}}})

			s.client.streamAudio("testdata/ask-weather.wav") // "What's the weather like in Paris right now?"
			frames := s.client.untilAll("response.done", "conversation.item.input_audio_transcription.completed")

			assert.Positive(t, countType(frames, "response.output_audio.delta"))
			row := s.turnRow()
			assert.Equal(t, "success", row.Status)
			assert.Equal(t, replyText(findType(frames, "response.done")), row.outputText())
			heard := findType(frames, "conversation.item.input_audio_transcription.completed").Get("transcript").Str
			if fake != nil {
				assert.True(t, strings.HasPrefix(heard, "heard "), "the fake transcribes the audio it was sent: %q", heard)
			} else {
				assert.Contains(t, strings.ToLower(heard), "weather")
			}
		})
	}
}

// Limits and allowlists answer as they do on every realtime route: an error event, then the close.
func TestLiveTranslated_RequestLimitStopsTheNextTurn(t *testing.T) {
	eachTranslated(t, func(t *testing.T, target translatedTarget) {
		vk := createVirtualKey(t, keySpec{maxRequests: 1})
		s, _ := openTranslated(t, target, vk, nil)
		s.ask("Say first.")
		s.turnRow()

		s.client.send(userMessage("Say second."), responseCreate())

		frames := s.client.until("error")
		assertError(t, frames[len(frames)-1], "rate_limit_exceeded", "")
		s.client.expectClosed()
	})
}

func TestLiveTranslated_BudgetStopsTheNextTurn(t *testing.T) {
	eachTranslated(t, func(t *testing.T, target translatedTarget) {
		vk := createVirtualKey(t, keySpec{budgetUSD: 1e-9})
		s, _ := openTranslated(t, target, vk, nil)
		s.ask("Say first.")
		row := s.turnRow()
		require.Positive(t, row.Cost, "the model has a price, so the turn is billed")
		waitBudgetUsage(t, vk.BudgetID, row.Cost)

		s.client.send(userMessage("Say second."), responseCreate())

		frames := s.client.collect("error")
		assertError(t, frames[len(frames)-1], "insufficient_quota", "")
		s.client.expectClosed()
	})
}

func TestLiveTranslated_ModelOutsideTheKeysAllowlistGetsNoTurn(t *testing.T) {
	eachTranslated(t, func(t *testing.T, target translatedTarget) {
		other := geminiLiveModel // the key allows the other target's model, not this one's
		if target.provider == "gemini" {
			other = vertexLiveModel
		}
		vk := createVirtualKey(t, keySpec{liveModels: []string{other}})
		s, marker := openTranslated(t, target, vk, nil)

		s.client.send(userMessage("Say hello."), responseCreate())

		frames := s.client.collect("error")
		assertError(t, frames[len(frames)-1], anyType, "is not allowed for virtual key")
		s.client.expectClosed()
		if fake != nil {
			assert.Empty(t, fake.WaitSession(t, marker).Turns(), "the refused turn never reaches the provider")
		}
	})
}

// A client that hangs up takes the provider session with it.
func TestLiveTranslated_ClientLeavingClosesTheProviderSession(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	s, marker := openTranslated(t, geminiTranslated, vk, nil)
	s.ask("Say hello.")
	up := fake.WaitSession(t, marker)

	s.client.leave()

	up.WaitEnded(t, frameTimeout)
}

// Sessions on one gateway do not hear each other: each client gets its own answer and its own row.
func TestLiveTranslated_ConcurrentSessionsStayApart(t *testing.T) {
	requireFake(t)
	t.Parallel()
	const sessions = 16
	for i := range sessions {
		t.Run(fmt.Sprintf("session %d", i), func(t *testing.T) {
			t.Parallel()
			vk := createVirtualKey(t, keySpec{})
			s, _ := openTranslated(t, geminiTranslated, vk, nil)
			prompt := fmt.Sprintf("Say number %d.", i)

			for range 3 {
				assert.Equal(t, "echo: "+prompt, replyText(s.ask(prompt)))
			}

			for _, row := range s.turnRows(3) {
				assert.Equal(t, "echo: "+prompt, row.outputText())
				assert.Equal(t, vk.ID, row.VirtualKeyID)
			}
		})
	}
}
