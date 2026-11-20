package realtime

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Gemini Live clients on /genai: the Google GenAI SDK pointed at the gateway. The gateway relays Live
// frames as they are, addresses the setup to the model it resolved, authenticates upstream with its own
// credentials, and bills each turn to the client's virtual key.

// nativeTarget is one endpoint a Live client can reach through /genai.
type nativeTarget struct {
	name     string
	route    string
	model    string // the model name a client writes in setup, as the SDK would
	provider string // who serves it
	wantPath string // the model path the upstream's setup carries; fake mode only, so Gemini's
}

var nativeTargets = []nativeTarget{
	{"vertex v1beta1", vertexNativeV1beta1, vertexLiveModel, "vertex", ""},
	{"vertex v1", vertexNativeV1, vertexLiveModel, "vertex", ""},
	{"gemini", geminiNative, geminiLiveModel, "gemini", "models/gemini-3.1-flash-live-preview"},
}

// geminiTarget is the one target both modes serve.
var geminiTarget = nativeTargets[2]

// serving lists the targets the mode can serve. Vertex AI has no fake, so in fake mode only Gemini is left and
// the Vertex tests run against the real service.
func serving(targets ...nativeTarget) []nativeTarget {
	if fake == nil {
		return targets
	}
	var served []nativeTarget
	for _, target := range targets {
		if target.provider != "vertex" {
			served = append(served, target)
		}
	}
	return served
}

// eachNative runs a test on every target the mode serves, as parallel subtests named for them.
func eachNative(t *testing.T, targets []nativeTarget, run func(t *testing.T, target nativeTarget)) {
	t.Helper()
	for _, target := range serving(targets...) {
		t.Run(target.name, func(t *testing.T) {
			requireUpstream(t)
			t.Parallel()
			run(t, target)
		})
	}
}

// bothNative is the targets most tests run on: Vertex's current endpoint and Gemini's.
func bothNative() []nativeTarget { return []nativeTarget{nativeTargets[0], nativeTargets[2]} }

// The gateway's credentials reach the provider, and the client's never do.
func TestLiveNative_TextTurnIsRelayedAndLogged(t *testing.T) {
	eachNative(t, nativeTargets, func(t *testing.T, target nativeTarget) {
		vk := createVirtualKey(t, keySpec{})
		c := connectLive(t, nativeDial(target.route, "bearer", vk))
		c.open(target.model)

		reply := c.say("Say hello.")

		assert.NotEmpty(t, reply.transcript())
		assert.Positive(t, reply.audioBytes(), "the model's audio is relayed")
		row := waitTurnRows(t, vk, 1)[0]
		assert.Equal(t, "success", row.Status)
		assert.Equal(t, target.provider, row.Provider)
		assert.Equal(t, target.model, row.Model)
		assert.Equal(t, reply.usage().input, row.TokenUsage.Input)
		assert.Equal(t, reply.usage().output, row.TokenUsage.Output)

		if fake == nil {
			return
		}
		up := fake.WaitSession(t, c.marker)
		assert.Equal(t, target.wantPath, up.Setup().Get("model").Str, "the setup is addressed to the resolved model")
		assert.Equal(t, "echo: Say hello.", reply.transcript())
		assert.Equal(t, "Say hello.", up.WaitTurns(t, 1)[0].Prompt)
		assert.Equal(t, up.Turns()[0].Usage.Prompt, row.TokenUsage.Input)
		assert.Equal(t, up.Turns()[0].Usage.Output, row.TokenUsage.Output)
		assert.Equal(t, fakeGeminiKey, up.Header("X-Goog-Api-Key"))
		assert.Empty(t, up.Header("Authorization"))
		requireNoLeak(t, vk, up.header)
	})
}

// Every way a client can present its virtual key opens the session: the OpenAI-style bearer, Bifrost's own
// header, and the two ways the Google SDKs carry an API key.
func TestLiveNative_EveryAuthMethodOpensASession(t *testing.T) {
	for _, auth := range []string{"bearer", "x-bf-vk", "x-goog-api-key", "query-key"} {
		for _, target := range serving(bothNative()...) {
			t.Run(auth+" "+target.name, func(t *testing.T) {
				requireUpstream(t)
				t.Parallel()
				vk := createVirtualKey(t, keySpec{})
				c := connectLive(t, nativeDial(target.route, auth, vk))
				c.open(target.model)

				assert.NotEmpty(t, c.say("Say hello.").transcript())

				row := waitTurnRows(t, vk, 1)[0]
				assert.Equal(t, vk.ID, row.VirtualKeyID, "the turn is billed to the key the client presented")
			})
		}
	}
}

// The setup names its model in whichever spelling the SDK uses.
func TestLiveNative_ModelSpellings(t *testing.T) {
	for _, target := range serving(bothNative()...) {
		spellings := []struct{ name, model string }{
			{"bare name", target.model},
			{"models prefix", "models/" + target.model},
			{"provider prefix", target.provider + "/" + target.model},
		}
		if target.provider == "vertex" {
			spellings = append(spellings, struct{ name, model string }{"vertex resource path", "projects/some-client-project/locations/europe-west4/publishers/google/models/" + target.model})
		}
		for _, spelling := range spellings {
			t.Run(target.provider+" "+spelling.name, func(t *testing.T) {
				requireUpstream(t)
				t.Parallel()
				vk := createVirtualKey(t, keySpec{})
				c := connectLive(t, nativeDial(target.route, "bearer", vk))
				c.open(spelling.model)

				assert.NotEmpty(t, c.say("Say hello.").transcript())

				assert.Equal(t, target.model, waitTurnRows(t, vk, 1)[0].Model)
				if fake != nil {
					assert.Equal(t, target.wantPath, fake.WaitSession(t, c.marker).Setup().Get("model").Str)
				}
			})
		}
	}
}

// A request with no credential is refused before the upgrade. A key the gateway does not know is upgraded
// and then closed with the reason, which is where a Google SDK reads it from; /v1/realtime answers both
// with a 401 instead.
func TestLiveNative_RefusesAClientWithoutAValidKey(t *testing.T) {
	eachNative(t, bothNative(), func(t *testing.T, target nativeTarget) {
		status, body := refused(t, dial{route: target.route, anonymous: true})
		assert.Equal(t, http.StatusUnauthorized, status, body.Raw)

		for _, auth := range []string{"bearer", "x-goog-api-key"} {
			c := connectLive(t, nativeDial(target.route, auth, virtualKey{Value: "sk-bf-not-a-real-key"}))
			c.sendLive(liveSetup(target.model, c.marker))
			c.expectCloseCode(1008)
			assert.Contains(t, c.closeReason(), "credential does not exist", auth)
		}
	})
}

func TestLiveNative_SetupMustComeFirst(t *testing.T) {
	eachNative(t, bothNative(), func(t *testing.T, target nativeTarget) {
		vk := createVirtualKey(t, keySpec{})
		c := connectLive(t, nativeDial(target.route, "bearer", vk))

		c.sendLive(userTurn("hello before setup"))

		c.expectCloseCode(1007)
		require.True(t, strings.Contains(strings.ToLower(c.closeReason()), "setup"), "close reason %q names the setup", c.closeReason())
	})
}

// A request limit counts turns, not sessions, so the key's second turn is the one refused. The refused turn
// is logged as an error row of its own, which the OpenAI route does not do.
func TestLiveNative_RequestLimitStopsTheNextTurn(t *testing.T) {
	eachNative(t, bothNative(), func(t *testing.T, target nativeTarget) {
		vk := createVirtualKey(t, keySpec{maxRequests: 1})
		c := connectLive(t, nativeDial(target.route, "bearer", vk))
		c.open(target.model)
		assert.NotEmpty(t, c.say("Say hello.").transcript())

		c.sendLive(userTurn("Say hello again."))

		c.expectCloseCode(1013)
		assert.Contains(t, strings.ToLower(c.closeReason()), "rate limit")
		rows := waitTurnRows(t, vk, 2)
		assert.Equal(t, "success", rows[0].Status)
		assert.Equal(t, "error", rows[1].Status)
		assert.Equal(t, http.StatusTooManyRequests, rows[1].Error.StatusCode)
	})
}

// A spent budget refuses the turn after it. Any price at all overspends this one.
func TestLiveNative_BudgetStopsTheNextTurn(t *testing.T) {
	eachNative(t, bothNative(), func(t *testing.T, target nativeTarget) {
		vk := createVirtualKey(t, keySpec{budgetUSD: 1e-9})
		c := connectLive(t, nativeDial(target.route, "bearer", vk))
		c.open(target.model)
		assert.NotEmpty(t, c.say("Say hello.").transcript())
		row := waitTurnRows(t, vk, 1)[0]
		require.Positive(t, row.Cost, "the model has a price, so the turn is billed")
		waitBudgetUsage(t, vk.BudgetID, row.Cost)

		c.sendLive(userTurn("Say hello again."))

		c.expectCloseCode(1011)
		assert.Contains(t, strings.ToLower(c.closeReason()), "budget")
	})
}

// The key's model allowlist is checked when a turn starts, as on /v1/realtime: the session opens, and the
// first turn is refused before anything reaches the provider.
func TestLiveNative_ModelOutsideTheKeysAllowlistGetsNoTurn(t *testing.T) {
	eachNative(t, bothNative(), func(t *testing.T, target nativeTarget) {
		other := geminiLiveModel // the key allows the other target's model, not this one's
		if target.model == geminiLiveModel {
			other = vertexLiveModel
		}
		vk := createVirtualKey(t, keySpec{liveModels: []string{other}})
		c := connectLive(t, nativeDial(target.route, "bearer", vk))
		c.open(target.model)

		c.sendLive(userTurn("Say hello."))

		c.expectCloseCode(1008)
		assert.Contains(t, c.closeReason(), "is not allowed for virtual key")
		rows := waitTurnRows(t, vk, 1)
		assert.Equal(t, "error", rows[0].Status)
		assert.Equal(t, http.StatusForbidden, rows[0].Error.StatusCode)
		if fake != nil {
			assert.Empty(t, fake.WaitSession(t, c.marker).Turns(), "the refused turn never reaches the provider")
		}
	})
}

// A model none of the provider's keys serves is refused by the gateway before it dials. The target is one whose key names its models: the real Vertex key and the fake profile's Gemini key. The real
// Gemini key serves any model, so Google refuses there instead.
func TestLiveNative_ModelNoKeyServesIsRefusedBeforeDialling(t *testing.T) {
	keyed := nativeTargets[0]
	if fake != nil {
		keyed = geminiTarget
	}
	eachNative(t, []nativeTarget{keyed}, func(t *testing.T, target nativeTarget) {
		vk := createVirtualKey(t, keySpec{liveModels: []string{"gemini-no-such-live-model"}})
		c := connectLive(t, nativeDial(target.route, "bearer", vk))

		c.sendLive(liveSetup("gemini-no-such-live-model", c.marker))

		c.expectCloseCode(1007)
		assert.Contains(t, c.closeReason(), "no keys found that support model: gemini-no-such-live-model")
		if fake != nil {
			assert.Zero(t, fake.SessionCount(c.marker))
		}
	})
}

// A model a key serves but Google refuses is Google's refusal, relayed with its code and reason.
func TestLiveNative_ProvidersRefusalOfAModelIsRelayed(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{liveModels: []string{fakeUnknownModel}})
	c := connectLive(t, nativeDial(geminiTarget.route, "bearer", vk))

	c.sendLive(liveSetup(fakeUnknownModel, c.marker))

	c.expectCloseCode(1008)
	assert.Contains(t, c.closeReason(), "was not found")
	assert.Contains(t, c.closeReason(), fakeUnknownModel)
}

// A provider that cannot speak Live at all is refused as on /v1/realtime.
func TestLiveNative_ProviderWithoutRealtimeIsRefused(t *testing.T) {
	eachNative(t, bothNative(), func(t *testing.T, target nativeTarget) {
		vk := createVirtualKey(t, keySpec{})
		c := connectLive(t, nativeDial(target.route, "bearer", vk))

		c.sendLive(liveSetup("anthropic/claude-haiku-4-5", c.marker))

		c.expectCloseCode(1007)
		assert.Contains(t, c.closeReason(), "provider does not support realtime: anthropic")
	})
}

// A provider that speaks only OpenAI Realtime is pointed to the route that serves it.
func TestLiveNative_OpenAIModelIsPointedToTheRealtimeRoute(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	c := connectLive(t, nativeDial(vertexNativeV1beta1, "bearer", vk))

	c.sendLive(liveSetup("openai/"+openaiModel, c.marker))

	c.expectCloseCode(1007)
	assert.Contains(t, c.closeReason(), "use /v1/realtime for it")
}

// A client that reconnects with the handle the provider gave it keeps its conversation.
func TestLiveNative_ResumptionKeepsTheConversation(t *testing.T) {
	for _, target := range serving(bothNative()...) {
		t.Run(target.name, func(t *testing.T) {
			requireUpstream(t)
			t.Parallel()
			vk := createVirtualKey(t, keySpec{})
			first := connectLive(t, nativeDial(target.route, "bearer", vk))
			first.open(target.model, map[string]any{"sessionResumption": map[string]any{}})
			first.say("Remember the code 7342.")
			if first.handle == "" {
				first.untilPredicate("a resumable handle", func(gjson.Result) bool { return first.handle != "" })
			}
			newest := first.handle
			require.NotEmpty(t, newest)
			first.leave()

			second := connectLive(t, nativeDial(target.route, "bearer", vk))
			second.open(target.model, map[string]any{"sessionResumption": map[string]any{"handle": newest}})
			reply := second.say("What code did I ask you to remember?")

			assert.Contains(t, reply.transcript(), "7342")
			waitTurnRows(t, vk, 2)
			if fake != nil {
				up := fake.WaitSession(t, second.marker)
				assert.Equal(t, newest, up.Setup().Get("sessionResumption.handle").Str, "the handle reaches the provider unchanged")
			}
		})
	}
}

// The model calls the app's function, the app answers, and the model finishes its turn.
func TestLiveNative_ToolCallRoundTrip(t *testing.T) {
	for _, target := range serving(bothNative()...) {
		t.Run(target.name, func(t *testing.T) {
			requireUpstream(t)
			t.Parallel()
			vk := createVirtualKey(t, keySpec{})
			c := connectLive(t, nativeDial(target.route, "bearer", vk))
			c.open(target.model, map[string]any{"tools": []any{map[string]any{"functionDeclarations": []any{map[string]any{
				"name": "get_weather", "description": "Get the current weather for a city.",
				"parameters": map[string]any{"type": "OBJECT", "properties": map[string]any{"city": map[string]any{"type": "STRING"}}, "required": []string{"city"}},
			}}}}})
			c.sendLive(userTurn("What is the weather in Paris right now? Use the get_weather function."))

			call := c.untilField("toolCall.functionCalls")
			function := call[len(call)-1].Get("toolCall.functionCalls.0")
			assert.Equal(t, "get_weather", function.Get("name").Str)
			c.sendLive(map[string]any{"toolResponse": map[string]any{"functionResponses": []any{map[string]any{
				"id": function.Get("id").Str, "name": "get_weather", "response": map[string]any{"output": "18 degrees and sunny"},
			}}}})
			reply := c.untilAnswered()

			assert.NotEmpty(t, reply.transcript())
			if fake != nil {
				up := fake.WaitSession(t, c.marker)
				assert.Contains(t, reply.transcript(), "18 degrees and sunny")
				answered := up.WaitFrame(t, "toolResponse")
				assert.Equal(t, function.Get("id").Str, answered.Get("toolResponse.functionResponses.0.id").Str, "the app's answer reaches the provider as sent")
			}
		})
	}
}

// An answer to a tool call that arrives while the model speaks a turn started after the call stops that turn,
// so two replies never run at once.
func TestLiveNative_AToolAnswerStopsATurnStartedAfterTheCall(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	c := connectLive(t, nativeDial(geminiNative, "bearer", vk))
	c.open(geminiLiveModel, map[string]any{"tools": []any{map[string]any{"functionDeclarations": []any{map[string]any{
		"name": "get_weather", "description": "Get the current weather for a city.",
		"parameters": map[string]any{"type": "OBJECT", "properties": map[string]any{"city": map[string]any{"type": "STRING"}}, "required": []string{"city"}},
	}}}}})
	c.sendLive(userTurn("What is the weather in Paris? [fake:slow]"))
	call := c.untilField("toolCall.functionCalls")
	function := call[len(call)-1].Get("toolCall.functionCalls.0")

	c.sendLive(userTurn("Count slowly to five. [fake:slow]"))
	c.sendLive(map[string]any{"toolResponse": map[string]any{"functionResponses": []any{map[string]any{
		"id": function.Get("id").Str, "name": "get_weather", "response": map[string]any{"output": "18 degrees and sunny"},
	}}}})
	frames := append(c.untilPredicate("the end of a turn", turnDone), c.drain(2*time.Second)...)

	var ended, interrupted int
	for _, frame := range frames {
		if turnDone(frame) {
			ended++
		}
		if frame.Get("serverContent.interrupted").Bool() {
			interrupted++
		}
	}
	assert.Equal(t, 1, ended, "only the reply to the tool's answer ends: %s", typesOf(frames))
	assert.Equal(t, 1, interrupted, "the turn the answer cut short is reported as interrupted")
}

// Spoken input is relayed as audio, and the provider's transcription of it comes back.
func TestLiveNative_SpokenQuestionGetsASpokenAnswer(t *testing.T) {
	for _, target := range serving(bothNative()...) {
		t.Run(target.name, func(t *testing.T) {
			requireUpstream(t)
			t.Parallel()
			vk := createVirtualKey(t, keySpec{})
			c := connectLive(t, nativeDial(target.route, "bearer", vk))
			c.open(target.model)

			speech := speech16k(t)
			sent := c.streamAudio(speech, 2*time.Second)

			reply := liveReply(c.untilPredicate("the end of the turn", turnDone))
			assert.Positive(t, reply.audioBytes())
			assert.NotEmpty(t, reply.transcript())
			row := waitTurnRows(t, vk, 1)[0]
			assert.Equal(t, "success", row.Status)
			if fake != nil {
				// The fake answers when it detects the pause, so it has heard the speech and part of the silence after it.
				var heard int
				_, err := fmt.Sscanf(reply.inputTranscript(), "heard %d bytes", &heard)
				require.NoError(t, err, "the fake transcribes the audio it was sent: %q", reply.inputTranscript())
				assert.GreaterOrEqual(t, heard, len(speech))
				assert.LessOrEqual(t, heard, sent)
			}
		})
	}
}

// The provider ends a session mid-turn: the client gets the provider's own close, and the turn is an error row.
func TestLiveNative_ProviderCloseIsRelayedAndLogged(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	c := connectLive(t, nativeDial(geminiTarget.route, "bearer", vk))
	c.open(geminiTarget.model)

	c.sendLive(userTurn("Say hello. [fake:close 1011 upstream exploded]"))

	c.expectCloseCode(1011)
	assert.Equal(t, "upstream exploded", c.closeReason())
	row := waitTurnRows(t, vk, 1)[0]
	assert.Equal(t, "error", row.Status)
	assert.Equal(t, http.StatusBadGateway, row.Error.StatusCode)
}

// A provider connection that dies without a close frame ends the client's session with 1011, not a hang.
func TestLiveNative_ProviderDropEndsTheSession(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	c := connectLive(t, nativeDial(geminiTarget.route, "bearer", vk))
	c.open(geminiTarget.model)

	c.sendLive(userTurn("Say hello. [fake:drop]"))

	c.expectCloseCode(1011)
	assert.Contains(t, c.closeReason(), "interrupted")
	assert.Equal(t, "error", waitTurnRows(t, vk, 1)[0].Status)
}

// The provider's goAway reaches the client, so an SDK can reconnect with its resumption handle, and the
// provider's close when the time runs out is relayed as it was.
func TestLiveNative_GoAwayAndTheCloseAfterItAreRelayed(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	c := connectLive(t, nativeDial(geminiTarget.route, "bearer", vk))
	c.open(geminiTarget.model)

	c.sendLive(userTurn("Say hello. [fake:goaway]"))

	frames := c.untilField("goAway")
	assert.Equal(t, "1s", frames[len(frames)-1].Get("goAway.timeLeft").Str)
	reply := liveReply(c.untilPredicate("the end of the turn", turnDone))
	assert.Equal(t, "echo: Say hello.", reply.transcript())
	c.expectCloseCode(fakeGeminiGoAwayCode)
	assert.Equal(t, fakeGeminiGoAwayReason, c.closeReason())
	assert.Equal(t, "success", waitTurnRows(t, vk, 1)[0].Status, "the turn finished before the session ended")
}

// A user who speaks over the model stops it: the provider says it was interrupted and the app hears the new
// answer.
func TestLiveNative_BargeInInterruptsTheModel(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	c := connectLive(t, nativeDial(geminiTarget.route, "bearer", vk))
	c.open(geminiTarget.model)
	c.sendLive(userTurn("Tell me a long story about the sea. [fake:slow]"))
	c.untilField("serverContent.modelTurn")

	c.sendLive(userTurn("Stop."))

	interrupted := c.untilField("serverContent.interrupted")
	assert.True(t, interrupted[len(interrupted)-1].Get("serverContent.interrupted").Bool())
	reply := liveReply(c.untilPredicate("the end of the turn", turnDone))
	assert.Equal(t, "echo: Stop.", reply.transcript())
	turns := fake.WaitSession(t, c.marker).WaitTurns(t, 2)
	assert.True(t, turns[0].Interrupted)
	assert.False(t, turns[1].Interrupted)
	// The interrupted turn and the answer that replaced it are one row, billed the usage the provider reported when it finished.
	row := waitTurnRows(t, vk, 1)[0]
	assert.Equal(t, "success", row.Status)
	assert.Equal(t, turns[1].Usage.Prompt, row.TokenUsage.Input)
	assert.Equal(t, turns[1].Usage.Output, row.TokenUsage.Output)
	assert.True(t, strings.HasSuffix(row.outputText(), "echo: Stop."), "the row's output ends with the answer the user heard last: %q", row.outputText())
}

// A client that hangs up takes the provider session with it.
func TestLiveNative_ClientLeavingClosesTheProviderSession(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	c := connectLive(t, nativeDial(geminiTarget.route, "bearer", vk))
	c.open(geminiTarget.model)
	c.say("Say hello.")
	up := fake.WaitSession(t, c.marker)

	c.leave()

	up.WaitEnded(t, frameTimeout)
}

// Sessions on one gateway do not hear each other: each client gets its own answer and its own row.
func TestLiveNative_ConcurrentSessionsStayApart(t *testing.T) {
	requireFake(t)
	t.Parallel()
	const sessions = 16
	for i := range sessions {
		t.Run(fmt.Sprintf("session %d", i), func(t *testing.T) {
			t.Parallel()
			vk := createVirtualKey(t, keySpec{})
			c := connectLive(t, nativeDial(geminiTarget.route, "bearer", vk))
			c.open(geminiTarget.model)
			prompt := fmt.Sprintf("Say number %d.", i)

			for range 3 {
				assert.Equal(t, "echo: "+prompt, c.say(prompt).transcript())
			}

			rows := waitTurnRows(t, vk, 3)
			for _, row := range rows {
				assert.Equal(t, "echo: "+prompt, row.outputText())
				assert.Equal(t, vk.ID, row.VirtualKeyID)
			}
		})
	}
}
