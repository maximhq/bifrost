package realtime

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The model is what tells a Scribe transcription session from an agent session: Scribe's frames are
// translated to OpenAI Realtime, an agent's are relayed as they are.
func TestElevenlabs_ScribeSessionIsTranslatedAndLogged(t *testing.T) {
	for _, model := range []string{"scribe_v2_realtime", "scribe_v3_live"} {
		t.Run(model, func(t *testing.T) {
			requireUpstream(t)
			t.Parallel()
			c := connect(t, dial{query: "intent=transcription"})

			c.send(sessionUpdate(map[string]any{
				"type": "transcription",
				"audio": map[string]any{"input": map[string]any{
					"transcription":  map[string]any{"model": "elevenlabs/" + model, "language": "en"},
					"turn_detection": map[string]any{"type": "server_vad", "silence_duration_ms": 600},
				}},
			}))
			c.until("session.updated")
			c.streamAudio("testdata/ask-weather.wav")
			frames := c.until("conversation.item.input_audio_transcription.completed")

			completed := frames[len(frames)-1]
			transcript := completed.Get("transcript").Str
			assert.Contains(t, strings.ToLower(transcript), "weather")
			assert.Equal(t, "duration", completed.Get("usage.type").Str, "Scribe reports no usage, so the translator reports the audio it sent")
			assert.Positive(t, completed.Get("usage.seconds").Float())
			item := completed.Get("item_id").Str
			assert.Equal(t, item, findType(frames, "input_audio_buffer.committed").Get("item_id").Str)
			for _, frame := range frames {
				if frame.Get("type").Str == "conversation.item.input_audio_transcription.delta" {
					assert.Equal(t, item, frame.Get("item_id").Str)
					assert.True(t, frame.Get("delta").Exists() && frame.Get("transcript").Exists(), "a delta event carries the delta and the whole partial: %s", frame.Raw)
				}
			}

			row := waitTurnRows(t, c.vk, 1)[0]
			assert.Equal(t, "success", row.Status)
			assert.Equal(t, "elevenlabs", row.Provider)
			assert.Equal(t, model, row.Model)
			assert.Equal(t, c.vk.ID, row.VirtualKeyID)
			assert.Contains(t, row.raw.Raw, transcript)
		})
	}
}

// An agent id is not a Scribe model, so its session keeps the ElevenLabs frames and close codes.
func TestElevenlabs_AgentSessionIsRelayedAsIs(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	c := connect(t, dial{query: "model=elevenlabs/agent_that_does_not_exist"})

	c.expectClosed()

	assert.Equal(t, int64(3000), c.closeCode.Load(), "ElevenLabs' own close code reaches the client")
}

// A Scribe model dialed as an agent id would fail with a confusing agent error, so it is refused.
func TestElevenlabs_ScribeModelNeedsTheTranscriptionIntent(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	c := connect(t, dial{query: "model=elevenlabs/scribe_v2_realtime"})

	c.expectError("invalid_request_error", "intent=transcription")
}

// Scribe fixes its settings when the connection opens; a session.update cannot change them.
func TestElevenlabs_ScribeSettingsCannotChangeAfterConnecting(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	c := connect(t, dial{query: "intent=transcription"})
	settings := func(language string) event {
		return sessionUpdate(map[string]any{
			"type": "transcription",
			"audio": map[string]any{"input": map[string]any{
				"transcription": map[string]any{"model": "elevenlabs/scribe_v2_realtime", "language": language},
			}},
		})
	}
	c.send(settings("en"))
	c.until("session.updated")

	c.send(settings("en"))
	c.until("session.updated") // repeating the settings is acknowledged
	c.send(settings("hi"))
	c.expectError(anyType, "fixes a session's settings when it connects")
	c.send(settings("en"))
	c.until("session.updated") // and the connection is still open
}

// The extension events are opt-in: a session that does not ask sees OpenAI event types only, and one that
// does gets the data Scribe sends after the segment as neutral Bifrost events that name the segment.
func TestElevenlabs_ScribeExtensionEventsAreOptIn(t *testing.T) {
	session := func(extensions bool) event {
		update := map[string]any{
			"type": "transcription",
			"audio": map[string]any{"input": map[string]any{
				"transcription":  map[string]any{"model": "elevenlabs/scribe_v2_realtime", "language": "en", "include_timestamps": true},
				"turn_detection": map[string]any{"type": "server_vad", "silence_duration_ms": 600},
			}},
		}
		if extensions {
			update["bifrost"] = map[string]any{"extensions": true}
		}
		return sessionUpdate(update)
	}

	t.Run("not asked for", func(t *testing.T) {
		requireUpstream(t)
		t.Parallel()
		c := connect(t, dial{query: "intent=transcription"})
		c.send(session(false))
		c.until("session.updated")
		c.streamAudio("testdata/ask-weather.wav")
		c.until("conversation.item.input_audio_transcription.completed")

		for _, frame := range c.drain(2 * time.Second) {
			assert.False(t, strings.HasPrefix(frame.Get("type").Str, "bifrost."), "an extension event the session did not ask for: %s", frame.Raw)
		}
	})

	t.Run("asked for", func(t *testing.T) {
		requireUpstream(t)
		t.Parallel()
		c := connect(t, dial{query: "intent=transcription"})
		c.send(session(true))
		updated := c.until("session.updated")
		assert.True(t, updated[len(updated)-1].Get("session.bifrost.extensions").Bool(), "the session reports the option back")
		c.streamAudio("testdata/ask-weather.wav")
		frames := c.until("conversation.item.input_audio_transcription.completed")
		item := frames[len(frames)-1].Get("item_id").Str

		words := c.until("bifrost.transcription.words")
		event := words[len(words)-1]
		assert.Equal(t, item, event.Get("item_id").Str, "the event names the segment it describes")
		assert.Contains(t, strings.ToLower(event.Get("words.0.text").Str), "what")
		assert.Less(t, event.Get("words.0.start").Float(), event.Get("words.0.end").Float())
		confidence := event.Get("words.0.confidence").Float()
		assert.Positive(t, confidence)
		assert.LessOrEqual(t, confidence, 1.0)
		assert.False(t, event.Get("words.0.characters").Exists(), "per-character timings are not forwarded")
	})
}

// scribeUpdate is a session.update for a Scribe transcription session. The language carries the fake's marker
// and directive in fake mode, and is a real language in the other.
func scribeUpdate(model, language string, transcription, input map[string]any) event {
	settings := map[string]any{"model": "elevenlabs/" + model, "language": language}
	for name, value := range transcription {
		settings[name] = value
	}
	input["transcription"] = settings
	return sessionUpdate(map[string]any{"type": "transcription", "audio": map[string]any{"input": input}})
}

// With turn detection off the client decides where a segment ends: a commit makes Scribe transcribe what it
// has heard, and its transcript completes the segment the client committed.
func TestElevenlabs_ACommitEndsTheSegment(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	c := connect(t, dial{query: "intent=transcription"})

	c.send(scribeUpdate("scribe_v2_realtime", "en", nil, map[string]any{"turn_detection": nil}))
	c.until("session.updated")
	<-c.streamAudio("testdata/ask-weather.wav")
	c.send(event{"type": "input_audio_buffer.commit"})
	frames := c.until("conversation.item.input_audio_transcription.completed")

	completed := frames[len(frames)-1]
	assert.Contains(t, strings.ToLower(completed.Get("transcript").Str), "weather")
	assert.Equal(t, completed.Get("item_id").Str, findType(frames, "input_audio_buffer.committed").Get("item_id").Str, "the commit the client made is the segment that completes")
}

// The gateway's key reaches ElevenLabs and the client's never does, and Scribe is told the model.
func TestElevenlabs_ScribeCredentialsAreTheGatewaysOwn(t *testing.T) {
	requireFake(t)
	t.Parallel()
	label, marker := fakeLabel("")
	c := connect(t, dial{query: "intent=transcription"})

	c.send(scribeUpdate("scribe_v2_realtime", label, nil, map[string]any{}))
	c.until("session.updated")

	up := fake.eleven.WaitScribe(t, marker)
	assert.Equal(t, fakeElevenKey, up.Header("xi-api-key"))
	requireNoLeak(t, c.vk, up.header)
	assert.Equal(t, "scribe_v2_realtime", up.Query().Get("model_id"))
}

// The settings of a session.update become the query Scribe is dialled with, which fixes them for the connection.
func TestElevenlabs_ScribeSessionUpdateBecomesTheQuery(t *testing.T) {
	requireFake(t)
	cases := []struct {
		name          string
		input         map[string]any
		transcription map[string]any
		want          map[string]string
		wantAll       map[string][]string
		absent        []string
		reported      string
	}{
		{
			name:     "defaults",
			input:    map[string]any{},
			want:     map[string]string{"audio_format": "pcm_24000", "commit_strategy": "vad"},
			absent:   []string{"vad_silence_threshold_secs"},
			reported: "server_vad",
		},
		{
			name:     "16 kHz audio and a silence threshold",
			input:    map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 16000}, "turn_detection": map[string]any{"type": "server_vad", "silence_duration_ms": 800}},
			want:     map[string]string{"audio_format": "pcm_16000", "commit_strategy": "vad", "vad_silence_threshold_secs": "0.8"},
			reported: "server_vad",
		},
		{
			name:     "semantic turn detection",
			input:    map[string]any{"turn_detection": map[string]any{"type": "semantic_vad"}},
			want:     map[string]string{"commit_strategy": "turn_prediction"},
			reported: "semantic_vad",
		},
		{
			name:  "a null turn detection hands commits to the client",
			input: map[string]any{"turn_detection": nil},
			want:  map[string]string{"commit_strategy": "manual"},
		},
		{
			name:  "mu-law telephony audio",
			input: map[string]any{"format": map[string]any{"type": "audio/pcmu"}},
			want:  map[string]string{"audio_format": "ulaw_8000"},
		},
		{
			name:          "timestamps, language detection and key terms",
			input:         map[string]any{},
			transcription: map[string]any{"include_timestamps": true, "include_language_detection": true, "keyterms": []string{"Paris", "Bifrost"}},
			want:          map[string]string{"include_timestamps": "true", "include_language_detection": "true"},
			wantAll:       map[string][]string{"keyterms": {"Paris", "Bifrost"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			label, marker := fakeLabel("")
			c := connect(t, dial{query: "intent=transcription"})

			c.send(scribeUpdate("scribe_v2_realtime", label, tc.transcription, tc.input))
			updated := c.until("session.updated")

			query := fake.eleven.WaitScribe(t, marker).Query()
			for name, want := range tc.want {
				assert.Equal(t, want, query.Get(name), "query %s: %v", name, query)
			}
			for name, want := range tc.wantAll {
				assert.ElementsMatch(t, want, query[name], "query %s: %v", name, query)
			}
			for _, name := range tc.absent {
				assert.False(t, query.Has(name), "query %s should be absent: %v", name, query)
			}
			if tc.reported != "" {
				assert.Equal(t, tc.reported, updated[len(updated)-1].Get("session.audio.input.turn_detection.type").Str)
			}
		})
	}
}

// When Scribe answers with an error frame the client gets an error event of the matching OpenAI type, and the
// session ends.
func TestElevenlabs_ScribeErrorFrameBecomesAnErrorEvent(t *testing.T) {
	requireFake(t)
	t.Parallel()
	label, _ := fakeLabel("error")
	c := connect(t, dial{query: "intent=transcription"})
	c.send(scribeUpdate("scribe_v2_realtime", label, nil, map[string]any{}))
	c.until("session.updated")

	c.streamAudio("testdata/ask-weather.wav")

	frames := c.collect("error")
	errObj := assertError(t, frames[len(frames)-1], "rate_limit_exceeded", "fake: the quota is exhausted")
	assert.Equal(t, "quota_exceeded", errObj.Get("code").Str, "Scribe's own message type is the code")
	c.drain(frameTimeout)
	assert.NotZero(t, c.closeCode.Load(), "the session ends after the error")
}

// A Scribe connection that dies without a word ends the client's session with an error, not a hang.
func TestElevenlabs_ScribeDropEndsTheSession(t *testing.T) {
	requireFake(t)
	t.Parallel()
	label, _ := fakeLabel("drop")
	c := connect(t, dial{query: "intent=transcription"})
	c.send(scribeUpdate("scribe_v2_realtime", label, nil, map[string]any{}))
	c.until("session.updated")

	c.streamAudio("testdata/ask-weather.wav")

	frames := c.collect("error")
	assertError(t, frames[len(frames)-1], anyType, "")
	c.drain(frameTimeout)
	assert.EqualValues(t, 1011, c.closeCode.Load())
}

// A client that hangs up takes the Scribe connection with it.
func TestElevenlabs_ClientLeavingClosesTheScribeSession(t *testing.T) {
	requireFake(t)
	t.Parallel()
	label, marker := fakeLabel("")
	c := connect(t, dial{query: "intent=transcription"})
	c.send(scribeUpdate("scribe_v2_realtime", label, nil, map[string]any{}))
	c.until("session.updated")
	up := fake.eleven.WaitScribe(t, marker)

	c.leave()

	up.WaitEnded(t, frameTimeout)
}

// An agent's frames reach the client as ElevenLabs sent them, and the agent is addressed by the id in the model.
func TestElevenlabs_AgentSessionRelaysTheAgentsFrames(t *testing.T) {
	requireFake(t)
	t.Parallel()
	agent := "fake-agent-" + liveMarker()
	vk := createVirtualKey(t, keySpec{elevenModels: []string{agent}}) // an agent id is a model, and a turn needs the key to allow it
	c := connect(t, dial{query: "model=elevenlabs/" + agent, vk: vk})

	greeting := c.until("conversation_initiation_metadata")
	assert.Equal(t, "conv_fake", greeting[len(greeting)-1].Get("conversation_initiation_metadata_event.conversation_id").Str)
	c.send(event{"type": "user_message", "text": "Say hello."})
	reply := c.until("audio")

	response := findType(reply, "agent_response")
	assert.True(t, strings.HasPrefix(response.Get("agent_response_event.agent_response").Str, "echo: "), "the agent's frame arrives as it was sent: %s", response.Raw)
	assert.Positive(t, len(reply[len(reply)-1].Get("audio_event.audio_base_64").Str))
	assert.EqualValues(t, 1, reply[len(reply)-1].Get("audio_event.event_id").Int())
	up := fake.eleven.WaitAgent(t, agent)
	assert.Equal(t, fakeElevenKey, up.Header("xi-api-key"))
	requireNoLeak(t, c.vk, up.header)
	require.NotEmpty(t, up.Frames())
	assert.Equal(t, "user_message", up.Frames()[0].Get("type").Str)
}

// A client's frames reach the agent as the client sent them: a text message carries its text.
func TestElevenlabs_AgentSessionRelaysTheClientsFrames(t *testing.T) {
	requireFake(t)
	t.Parallel()
	agent := "fake-agent-" + liveMarker()
	vk := createVirtualKey(t, keySpec{elevenModels: []string{agent}})
	c := connect(t, dial{query: "model=elevenlabs/" + agent, vk: vk})
	c.until("conversation_initiation_metadata")

	c.send(event{"type": "user_message", "text": "Say hello."})
	c.until("agent_response")

	up := fake.eleven.WaitAgent(t, agent)
	require.NotEmpty(t, up.Frames())
	assert.JSONEq(t, `{"type":"user_message","text":"Say hello."}`, up.Frames()[0].Raw, "the client's frame reaches the agent as it was sent")
}

// An agent's turn starts when the agent first answers, so a key that does not allow the agent is stopped
// there: the client gets the error and none of the answer.
func TestElevenlabs_AgentOutsideTheKeysAllowlistIsStoppedWhenItAnswers(t *testing.T) {
	requireFake(t)
	t.Parallel()
	agent := "fake-agent-" + liveMarker()
	c := connect(t, dial{query: "model=elevenlabs/" + agent})
	c.until("conversation_initiation_metadata")

	c.send(event{"type": "user_message", "text": "Say hello."})

	frames := c.collect("error")
	assertError(t, frames[len(frames)-1], anyType, "is not allowed for virtual key")
	frames = append(frames, c.drain(frameTimeout)...)
	assert.False(t, findType(frames, "agent_response").Exists() || findType(frames, "audio").Exists(), "none of the refused answer reaches the client: %s", typesOf(frames))
	assert.NotZero(t, c.closeCode.Load(), "the session ends after the refusal")
}

// A client that hangs up takes the agent session with it.
func TestElevenlabs_ClientLeavingClosesTheAgentSession(t *testing.T) {
	requireFake(t)
	t.Parallel()
	agent := "fake-agent-" + liveMarker()
	c := connect(t, dial{query: "model=elevenlabs/" + agent})
	c.until("conversation_initiation_metadata")
	up := fake.eleven.WaitAgent(t, agent)

	c.leave()

	up.WaitEnded(t, frameTimeout)
}

// A segment is a turn against the key's limits, so a key allowed one request transcribes one segment.
func TestElevenlabs_RequestLimitStopsTheNextSegment(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{maxRequests: 1})
	segment := func() *client {
		c := connect(t, dial{query: "intent=transcription", vk: vk})
		c.send(scribeUpdate("scribe_v2_realtime", "en", nil, map[string]any{"turn_detection": map[string]any{"type": "server_vad", "silence_duration_ms": 600}}))
		c.until("session.updated")
		c.streamAudio("testdata/ask-weather.wav")
		return c
	}
	first := segment()
	first.until("conversation.item.input_audio_transcription.completed")
	waitTurnRows(t, vk, 1)

	frames := segment().collect("error")

	assertError(t, frames[len(frames)-1], "rate_limit_exceeded", "")
}

// A spent budget refuses the segment after it. Any price at all overspends this one.
func TestElevenlabs_BudgetStopsTheNextSegment(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{budgetUSD: 1e-9})
	segment := func() *client {
		c := connect(t, dial{query: "intent=transcription", vk: vk})
		c.send(scribeUpdate("scribe_v2_realtime", "en", nil, map[string]any{"turn_detection": map[string]any{"type": "server_vad", "silence_duration_ms": 600}}))
		c.until("session.updated")
		c.streamAudio("testdata/ask-weather.wav")
		return c
	}
	segment().until("conversation.item.input_audio_transcription.completed")
	row := waitTurnRows(t, vk, 1)[0]
	require.Positive(t, row.Cost, "the model has a price, so the segment is billed")
	waitBudgetUsage(t, vk.BudgetID, row.Cost)

	frames := segment().collect("error")

	assertError(t, frames[len(frames)-1], "insufficient_quota", "")
}

// The key's model allowlist is checked when a segment starts.
func TestElevenlabs_ModelOutsideTheKeysAllowlistGetsNoSegment(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{elevenModels: []string{"scribe_v3_live"}})
	c := connect(t, dial{query: "intent=transcription", vk: vk})
	c.send(scribeUpdate("scribe_v2_realtime", "en", nil, map[string]any{"turn_detection": map[string]any{"type": "server_vad", "silence_duration_ms": 600}}))
	c.until("session.updated")

	c.streamAudio("testdata/ask-weather.wav")

	frames := c.collect("error")
	assertError(t, frames[len(frames)-1], anyType, "is not allowed for virtual key")
}

// Sessions on one gateway do not hear each other: each gets its own transcript, upstream connection and row.
func TestElevenlabs_ConcurrentScribeSessionsStayApart(t *testing.T) {
	requireFake(t)
	t.Parallel()
	for i := range 8 {
		t.Run(fmt.Sprintf("session %d", i), func(t *testing.T) {
			t.Parallel()
			label, marker := fakeLabel("")
			c := connect(t, dial{query: "intent=transcription"})
			c.send(scribeUpdate("scribe_v2_realtime", label, nil, map[string]any{"turn_detection": map[string]any{"type": "server_vad", "silence_duration_ms": 600}}))
			c.until("session.updated")

			c.streamAudio("testdata/ask-weather.wav")
			frames := c.until("conversation.item.input_audio_transcription.completed")

			assert.Equal(t, fakeTranscript, frames[len(frames)-1].Get("transcript").Str)
			assert.Positive(t, fake.eleven.WaitScribe(t, marker).AudioBytes())
			rows := waitTurnRows(t, c.vk, 1)
			assert.Equal(t, c.vk.ID, rows[0].VirtualKeyID)
			assert.Contains(t, rows[0].raw.Raw, fakeTranscript, "the row holds this session's transcript")
		})
	}
}

// An ElevenLabs client's call, sent the way its SDK sends it: the key in xi-api-key, nothing else for auth.
func elevenlabsCall(t *testing.T, method, path, key, body string) (*http.Response, []byte) {
	t.Helper()
	return elevenlabsCallOn(t, "/elevenlabs", method, path, key, body)
}

// elevenlabsCallOn is an ElevenLabs client's call under a base URL: /elevenlabs, or /elevenlabs_passthrough for the
// endpoints Bifrost has no native route for.
func elevenlabsCallOn(t *testing.T, base, method, path, key, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, gatewayURL+base+path, strings.NewReader(body))
	require.NoError(t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("xi-api-key", key)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, data
}

// elevenlabsSpeechBody is a text-to-speech body on eleven_v4_turbo.
func elevenlabsSpeechBody(text string) string {
	return `{"text":` + strconv.Quote(text) + `,"model_id":"eleven_v4_turbo"}`
}

// Every text-to-speech endpoint an ElevenLabs SDK calls is relayed as it was sent, under the key's credential, and logged
// with its model and cost: the two Bifrost serves natively on /elevenlabs, and the streams on /elevenlabs_passthrough.
func TestElevenlabs_NativeRESTEndpointsAreRelayedAndLogged(t *testing.T) {
	for _, tc := range []struct {
		name, base, suffix string
		stream, timestamps bool
	}{
		{"text to speech", "/elevenlabs", "", false, false},
		{"text to speech with timestamps", "/elevenlabs", "/with-timestamps", false, true},
		{"text to speech stream", "/elevenlabs_passthrough", "/stream", true, false},
		{"text to speech stream with timestamps", "/elevenlabs_passthrough", "/stream/with-timestamps", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireFake(t)
			t.Parallel()
			vk := createVirtualKey(t, keySpec{})
			voice, marker := fakeLabel("")
			path, body := "/v1/text-to-speech/"+voice+tc.suffix, elevenlabsSpeechBody("Hello from the gateway.")
			query := "output_format=mp3_22050_32&enable_logging=false"

			resp, data := elevenlabsCallOn(t, tc.base, http.MethodPost, path+"?"+query, vk.Value, body)

			require.Equal(t, http.StatusOK, resp.StatusCode, string(data))
			assert.NotEmpty(t, data)
			assert.NotEmpty(t, resp.Header.Get("request-id"), "ElevenLabs' own headers reach the client")
			assert.Equal(t, "fake-history-item", resp.Header.Get("history-item-id"))
			assert.Equal(t, "11", resp.Header.Get("character-cost"), "the credits ElevenLabs charged, not the characters")
			if tc.timestamps {
				assert.Contains(t, string(data), `"audio_base64"`)
				assert.Contains(t, string(data), `"alignment"`)
			} else {
				assert.Equal(t, "audio/mpeg", resp.Header.Get("Content-Type"))
			}

			call := fake.eleven.WaitREST(t, marker, 1)[0]
			assert.Equal(t, path, call.Path())
			assert.Equal(t, "mp3_22050_32", call.Query().Get("output_format"))
			assert.Equal(t, "false", call.Query().Get("enable_logging"))
			assert.JSONEq(t, body, call.Body().Raw, "the body goes upstream as the client wrote it")
			assert.Equal(t, fakeElevenKey, call.Header("xi-api-key"), "ElevenLabs gets the provider key")
			for name, values := range call.Headers() {
				for _, value := range values {
					assert.NotContains(t, value, vk.Value, "the client's virtual key must not reach ElevenLabs in %s", name)
				}
			}

			row := waitSpeechRows(t, vk, 1)[0]
			assert.Equal(t, "success", row.Status)
			assert.Equal(t, "elevenlabs", row.Provider)
			assert.Equal(t, "eleven_v4_turbo", row.Model)
			assert.Equal(t, vk.ID, row.VirtualKeyID)
			assert.Positive(t, row.Cost, "the model has a price per character, so the call is billed")
		})
	}
}

// Every field an ElevenLabs client sets reaches ElevenLabs, because the body goes upstream as it was sent and is not
// rebuilt from the fields the gateway knows.
func TestElevenlabs_NativeRESTForwardsEveryFieldAndOption(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	voice, marker := fakeLabel("")
	body := `{"text":"Hello.","model_id":"eleven_v4_turbo","language_code":"hi","voice_settings":{"stability":0.3,"similarity_boost":0.8,"style":0.2,"use_speaker_boost":true,"speed":1.1},` +
		`"pronunciation_dictionary_locators":[{"pronunciation_dictionary_id":"dict1","version_id":"v1"}],"seed":42,"previous_text":"Before.","next_text":"After.",` +
		`"previous_request_ids":["req-a"],"apply_text_normalization":"on","apply_language_text_normalization":true,"audio_effects":{"distance":0.5}}`

	resp, data := elevenlabsCall(t, http.MethodPost, "/v1/text-to-speech/"+voice+"?output_format=pcm_16000&enable_logging=false", vk.Value, body)

	require.Equal(t, http.StatusOK, resp.StatusCode, string(data))
	call := fake.eleven.WaitREST(t, marker, 1)[0]
	assert.JSONEq(t, body, call.Body().Raw)
	assert.Equal(t, "pcm_16000", call.Query().Get("output_format"))
	assert.Equal(t, "false", call.Query().Get("enable_logging"))
}

// A call the gateway refuses is refused in ElevenLabs' error shape, so an ElevenLabs SDK parses it, and nothing
// reaches ElevenLabs.
func TestElevenlabs_NativeRESTRefusalsAreElevenlabsErrors(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{elevenModels: []string{"eleven_v4_turbo"}})
	voice, marker := fakeLabel("")

	for _, tc := range []struct {
		name, key, body string
		status          int
		detail          string
	}{
		{"no key", "", elevenlabsSpeechBody("hi"), http.StatusUnauthorized, ""},
		{"a forged virtual key", "sk-bf-forged-" + marker, elevenlabsSpeechBody("hi"), http.StatusUnauthorized, ""},
		{"a model the key does not allow", vk.Value, `{"text":"hi","model_id":"eleven_multilingual_v2"}`, http.StatusForbidden, "model_blocked"},
		{"no model", vk.Value, `{"text":"hi"}`, http.StatusBadRequest, "invalid_request"},
		{"a model key that names the model ElevenLabs does not read", vk.Value, `{"text":"hi","model":"eleven_v4_turbo"}`, http.StatusBadRequest, "invalid_request"},
		{"a body that is not JSON", vk.Value, `{"text":`, http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, data := elevenlabsCall(t, http.MethodPost, "/v1/text-to-speech/"+voice, tc.key, tc.body)

			assert.Equal(t, tc.status, resp.StatusCode, string(data))
			detail := gjson.GetBytes(data, "detail")
			require.True(t, detail.IsObject(), "ElevenLabs writes errors as {detail:{status,message}}: %s", data)
			assert.NotEmpty(t, detail.Get("message").Str)
			if tc.detail != "" {
				assert.Equal(t, tc.detail, detail.Get("status").Str)
			} else {
				assert.NotEmpty(t, detail.Get("status").Str)
			}
		})
	}
	time.Sleep(rowSettleWindow)
	assert.Empty(t, fake.eleven.RESTCalls(marker), "a refused call must not reach ElevenLabs")
}

// ElevenLabs' own error reaches the client untouched.
func TestElevenlabs_NativeRESTUpstreamErrorIsRelayedAsIs(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	voice, _ := fakeLabel("error")

	resp, data := elevenlabsCall(t, http.MethodPost, "/v1/text-to-speech/"+voice, vk.Value, elevenlabsSpeechBody("hi"))

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.JSONEq(t, `{"detail":{"status":"too_many_concurrent_requests","message":"fake: slow down"}}`, string(data))
	assert.Equal(t, "error", waitSpeechRows(t, vk, 1)[0].Status)
}

// The key's request limit counts ElevenLabs calls like any other request.
func TestElevenlabs_NativeRESTRequestLimitStopsTheNextCall(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{maxRequests: 1})
	voice, _ := fakeLabel("")

	first, data := elevenlabsCall(t, http.MethodPost, "/v1/text-to-speech/"+voice, vk.Value, elevenlabsSpeechBody("Say first."))
	require.Equal(t, http.StatusOK, first.StatusCode, string(data))
	second, data := elevenlabsCall(t, http.MethodPost, "/v1/text-to-speech/"+voice, vk.Value, elevenlabsSpeechBody("Say second."))

	assert.Equal(t, http.StatusTooManyRequests, second.StatusCode)
	assert.Equal(t, "request_limited", gjson.GetBytes(data, "detail.status").Str)
}

// A call is billed by the characters it sends, and the budget refuses the next one once it is spent.
func TestElevenlabs_NativeRESTBudgetStopsTheNextCall(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{budgetUSD: 1e-9})
	voice, _ := fakeLabel("")

	first, data := elevenlabsCall(t, http.MethodPost, "/v1/text-to-speech/"+voice, vk.Value, elevenlabsSpeechBody("Say first."))
	require.Equal(t, http.StatusOK, first.StatusCode, string(data))
	row := waitSpeechRows(t, vk, 1)[0]
	require.Positive(t, row.Cost, "the model has a price, so the call is billed")
	waitBudgetUsage(t, vk.BudgetID, row.Cost)

	second, data := elevenlabsCall(t, http.MethodPost, "/v1/text-to-speech/"+voice, vk.Value, elevenlabsSpeechBody("Say second."))

	assert.Equal(t, http.StatusPaymentRequired, second.StatusCode)
	assert.Equal(t, "budget_exceeded", gjson.GetBytes(data, "detail.status").Str)
}

// The model list is a bare array of models, which is what the ElevenLabs SDK reads.
func TestElevenlabs_NativeRESTModelListIsABareArray(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})

	resp, data := elevenlabsCall(t, http.MethodGet, "/v1/models", vk.Value, "")

	require.Equal(t, http.StatusOK, resp.StatusCode, string(data))
	models := gjson.ParseBytes(data)
	require.True(t, models.IsArray(), "ElevenLabs lists models as a bare array: %s", data)
	assert.Contains(t, models.Get("#.model_id").Raw, "eleven_v4_turbo")
}

// A voice cannot name another endpoint: the call is refused, and nothing reaches ElevenLabs.
func TestElevenlabs_NativeRESTVoiceCannotLeaveItsPath(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	for _, path := range []string{
		"/v1/text-to-speech/..%2Fvoices",
		"/v1/text-to-speech/%2e%2e%2Fmodels",
		"/v1/text-to-speech/voice/other",
	} {
		resp, data := elevenlabsCall(t, http.MethodPost, path, vk.Value, elevenlabsSpeechBody("hi"))
		assert.GreaterOrEqual(t, resp.StatusCode, 400, "POST %s: %s", path, data)
	}
}
