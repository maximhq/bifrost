package live

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Real upstream smoke: the gateway talks to api.openai.com. Paid, run by hand with
// LIVE_UPSTREAM=real. Speech comes from macOS `say`; scenarios that need it skip elsewhere.
// Transports run one after the other, not in parallel, to keep the paid sessions apart.

func realHeaders() map[string]string {
	if envVirtualKey == "" {
		return nil
	}
	return map[string]string{"x-bf-vk": envVirtualKey}
}

const (
	realDelegationWait = 60 * time.Second
	realTurnWait       = 30 * time.Second
	// realPath pins the provider: a bare model on the generic route may resolve to another
	// configured provider (Azure serves gpt-* names too) that has no live support.
	realPath = "/openai/v1/live/sessions"
	// lookupPrompt makes the voice model delegate to the backend.
	lookupPrompt = "What is the weather in Paris right now? Please look it up."
)

// realSession is a session for the real upstream: a WebSocket session declares the PCM format its
// microphone sends; a WebRTC session speaks Opus on its track.
func realSession(t *testing.T, tr transport, backend string, extra map[string]any) map[string]any {
	t.Helper()
	audio := map[string]any{"output": map[string]any{"voice": "marin"}}
	if tr == wsTransport {
		audio["format"] = map[string]any{"type": "audio/pcm", "rate": 24000}
	}
	all := map[string]any{"audio": audio}
	for k, v := range extra {
		all[k] = v
	}
	return sessionFor(t, voiceModel, backend, all)
}

func realOptions(tr transport, session map[string]any) clientOptions {
	return clientOptions{path: realPath, headers: realHeaders(), session: session, microphone: tr == wsTransport}
}

// speak plays a prompt on the session's microphone in the transport's format. It skips where the
// speech tools cannot produce it.
func speak(t *testing.T, c *liveClient, tr transport, text string) {
	t.Helper()
	if tr == webrtcTransport {
		packets, ok := speakOpus(t, text)
		if !ok {
			t.Skip("afconvert cannot encode Opus here; WebRTC speech is unavailable")
		}
		c.SpeakOpus(packets)
		return
	}
	c.SpeakPCM(speakPCM(t, text))
}

func TestReal_SessionDelegatesAndLogs(t *testing.T) {
	requireReal(t)
	for _, tr := range transports {
		t.Run(string(tr), func(t *testing.T) {
			session := realSession(t, tr, backendModel, map[string]any{"store": true})
			c := openLive(t, tr, realOptions(tr, session))
			speak(t, c, tr, lookupPrompt)
			created := c.WaitWithin(realDelegationWait, "session.delegation.created")
			assert.NotEmpty(t, created.Get("delegation.id").Str)
			completed := waitNestedCompleted(t, c, realDelegationWait)
			assert.Greater(t, completed.Get("event.response.usage.total_tokens").Int(), int64(0))
			// The voice model speaks the answer after the delegation; hang up only once it has gone quiet.
			c.WaitQuiet(realTurnWait, 3*time.Second, "session.output_transcript.delta")
			c.CloseSession()

			row := findLiveLog(t, c.ProviderSessionID)
			assert.Greater(t, row.Get("token_usage.audio_seconds").Float(), 0.0)
			require.NotEmpty(t, row.Get("live_session.delegations").Array())
			assert.NotEmpty(t, row.Get("live_session.transcript").Array())
			assert.Equal(t, string(tr), row.Get("live_session.transport").Str)
		})
	}
}

func TestReal_WebRTCSessionRecordsAndServesContent(t *testing.T) {
	requireReal(t)
	session := realSession(t, webrtcTransport, backendModel, map[string]any{"store": true})
	c := createWebRTCSession(t, realOptions(webrtcTransport, session))
	if packets, ok := speakOpus(t, "Hi. In one sentence, what can you help me with?"); ok {
		c.SpeakOpus(packets)
		c.WaitQuiet(realTurnWait, 3*time.Second, "session.output_transcript.delta")
	}
	c.WaitWithin(realTurnWait, "session.usage.updated")
	c.CloseSession()

	row := findLiveLog(t, c.ProviderSessionID)
	assert.GreaterOrEqual(t, row.Get("token_usage.audio_seconds").Float(), 15.0, "OpenAI's WebRTC minimum")
	assert.Equal(t, "webrtc", row.Get("live_session.transport").Str)

	status, body, contentType := downloadContent(t, c.ProviderSessionID, realHeaders())
	assert.Equal(t, http.StatusOK, status, "%.200s", body)
	assert.Contains(t, contentType, "audio")
	assert.Greater(t, len(body), 44)
}

func TestReal_SidebandSteersAWebRTCSession(t *testing.T) {
	requireReal(t)
	session := realSession(t, webrtcTransport, backendModel, nil)
	primary := createWebRTCSession(t, realOptions(webrtcTransport, session))
	sideband, _, err := attachSideband(t, primary.ProviderSessionID, realHeaders())
	require.NoError(t, err)
	// OpenAI tells only the connections present at the start about session.started; a sideband
	// that joins moments later is not replayed it. The ack of its first command proves it joined.
	sideband.Send(`{"type":"session.instructions.append","event_id":"steer_1","delegation_id":null,"content":"Answer in one short sentence."}`)
	assert.Equal(t, "steer_1", sideband.WaitWithin(realTurnWait, "session.instructions.appended").Get("client_event_id").Str)
	sideband.Send(`{"type":"session.update","event_id":"switch","session":{"delegation":{"type":"responses","responses":{"model":"` + backendModel2 + `"}}}}`)
	assert.Equal(t, backendModel2, primary.WaitWithin(realTurnWait, "session.updated").Get("session.delegation.responses.model").Str)
	primary.CloseSession()
	findLiveLog(t, primary.ProviderSessionID)
}

func TestReal_VoiceModelCannotChangeMidSession(t *testing.T) {
	requireReal(t)
	for _, tr := range transports {
		t.Run(string(tr), func(t *testing.T) {
			session := realSession(t, tr, backendModel, nil)
			c := openLive(t, tr, realOptions(tr, session))
			c.Send(`{"type":"session.update","event_id":"switch_voice","session":{"model":"gpt-live-transcribe"}}`)
			assert.Contains(t, errorMessage(c.WaitWithin(realTurnWait, "error")), "session.model", "OpenAI accepts session.model on session.start only")
			c.CloseSession()
		})
	}
}

func TestReal_ClientDelegationRunsTheAppsBackend(t *testing.T) {
	requireReal(t)
	for _, tr := range transports {
		t.Run(string(tr), func(t *testing.T) {
			session := realSession(t, tr, "", map[string]any{"delegation": map[string]any{"type": "client"}})
			var c *liveClient
			answered := make(chan appCall, 1)
			opts := realOptions(tr, session)
			opts.onClientTask = func(delegationID string) {
				response, err := callResponses("/openai/v1/responses", realHeaders(), c.ProviderSessionID, backendModel,
					[]map[string]any{{"role": "user", "content": "What is the weather in Paris right now? Look it up and answer in one sentence."}})
				if err == nil {
					c.Commentary(delegationID, response.Get(`output.#(type=="message").content.0.text`).Str)
				}
				answered <- appCall{response: response, err: err}
			}
			c = openLive(t, tr, opts)
			speak(t, c, tr, lookupPrompt)
			created := c.WaitWithin(realDelegationWait, "session.delegation.created")
			assert.Equal(t, "client", created.Get("delegation.target").Str)
			call := awaitWithin(t, answered, realDelegationWait, "the app's backend call")
			require.NoError(t, call.err)
			assert.NotEmpty(t, call.response.Get(`output.#(type=="message").content.0.text`).Str)
			c.WaitWithin(realTurnWait, "session.commentary.appended")
			c.WaitQuiet(realTurnWait, 3*time.Second, "session.output_transcript.delta")
			c.CloseSession()

			row := findLiveLog(t, c.ProviderSessionID)
			assert.Empty(t, row.Get("live_session.delegations").Array(), "the app ran the backend; the session billed voice only")
			assert.Greater(t, row.Get("token_usage.audio_seconds").Float(), 0.0)
			assert.Equal(t, string(tr), row.Get("live_session.transport").Str)
			backendRow := findSessionRow(t, c.ProviderSessionID, "responses")
			assert.Greater(t, backendRow.Get("token_usage.total_tokens").Float(), 0.0)
		})
	}
}

// waitNestedCompleted waits for the delegation's terminal event among the nested stream.
func waitNestedCompleted(t *testing.T, c *liveClient, timeout time.Duration) (frame gjsonResult) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f := c.WaitWithin(time.Until(deadline), "response.event")
		if f.Get("event.type").Str == "response.completed" {
			return f
		}
	}
	t.Fatalf("no nested response.completed within %s", timeout)
	return frame
}

// Long conversation on the real upstream: fifteen minutes per transport with a prompt every
// eighty seconds, lookups, function calls the app answers, a backend switch half way (from a
// sideband on WebRTC) and a recording. Gated by LIVE_LONG=1; roughly a dollar per transport.

const (
	longRealDuration = 15 * time.Minute
	longRealTurnGap  = 80 * time.Second
)

// appFunctionTools are the functions the app offers the backend, in the Responses tool shape.
func appFunctionTools() []any {
	return []any{
		functionTool("get_calendar", "The user's calendar for a day.", map[string]any{"date": map[string]any{"type": "string", "description": "ISO date, or 'today'"}}, "date"),
		functionTool("convert_currency", "Convert an amount between currencies at today's rate.", map[string]any{"amount": map[string]any{"type": "number"}, "from": map[string]any{"type": "string"}, "to": map[string]any{"type": "string"}}, "amount", "from", "to"),
		functionTool("book_table", "Book a restaurant table for the user.", map[string]any{"restaurant": map[string]any{"type": "string"}, "time": map[string]any{"type": "string"}, "party_size": map[string]any{"type": "integer"}}, "restaurant", "time", "party_size"),
	}
}

func functionTool(name, description string, properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "function", "name": name, "description": description, "strict": true,
		"parameters": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}
}

// answerAppFunction is the app's side of a function call.
func answerAppFunction(name string, args gjson.Result) any {
	switch name {
	case "get_calendar":
		return map[string]any{"date": args.Get("date").Str, "events": []any{
			map[string]any{"time": "10:00", "title": "Design review"},
			map[string]any{"time": "13:00", "title": "Lunch with Sam"},
			map[string]any{"time": "16:30", "title": "Dentist"},
		}}
	case "convert_currency":
		return map[string]any{"amount": args.Get("amount").Float(), "from": args.Get("from").Str, "to": args.Get("to").Str, "rate": 1.08, "converted": args.Get("amount").Float() * 1.08}
	case "book_table":
		return map[string]any{"status": "confirmed", "confirmation_id": "RSV-4821", "restaurant": args.Get("restaurant").Str, "time": args.Get("time").Str, "party_size": args.Get("party_size").Int()}
	}
	return map[string]any{"error": "unknown function " + name}
}

// speechFrame is the event that shows the assistant speaking: on WebSocket the audio itself,
// which streams far more often than transcript text; on WebRTC the audio rides the media track,
// so the transcript is all the data channel shows.
func speechFrame(tr transport) string {
	if tr == webrtcTransport {
		return "session.output_transcript.delta"
	}
	return "session.output_audio.delta"
}

// finishTurn waits for the assistant to finish answering a prompt. The voice model may delegate
// any prompt, so a delegation that starts within the window is waited to its terminal event
// first; then the speech goes quiet. seen is the speech frame count from before the prompt: a
// short answer can be over before the delegation window ends.
func finishTurn(t *testing.T, c *liveClient, tr transport, lookup bool, seen int) {
	t.Helper()
	window := 10 * time.Second
	if lookup {
		window = realDelegationWait
	}
	created := len(c.Frames("session.delegation.created"))
	deadline := time.Now().Add(window)
	for len(c.Frames("session.delegation.created")) == created && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if len(c.Frames("session.delegation.created")) > created {
		waitNestedCompleted(t, c, realDelegationWait)
	}
	c.WaitQuietSince(2*realTurnWait, 3*time.Second, speechFrame(tr), seen)
}

func TestReal_LongConversation(t *testing.T) {
	requireReal(t)
	requireLong(t)
	prompts := []struct {
		text   string
		lookup bool // the voice model should delegate: give the backend time
	}{
		{"What is the weather in Paris right now? Please look it up.", true},
		{"What is on my calendar today?", true},
		{"Tell me a short joke.", false},
		{"Convert two hundred and fifty euros to US dollars.", true},
		{"Book a table for four at Chez Panisse at eight tonight.", true},
		{"What is the latest news about the Mars sample return mission? Look it up.", true},
		{"What did you book for me earlier?", false},
		{"Convert one hundred dollars to Japanese yen.", true},
		{"What is the weather in Tokyo right now? Look it up.", true},
		{"Thanks, that is all for today.", false},
	}
	for _, tr := range transports {
		t.Run(string(tr), func(t *testing.T) {
			session := realSession(t, tr, backendModel, map[string]any{"store": true})
			session["delegation"].(map[string]any)["responses"].(map[string]any)["tools"] = append([]any{map[string]any{"type": "web_search"}}, appFunctionTools()...)
			opts := realOptions(tr, session)
			var mu sync.Mutex
			var calls []string
			opts.onFunctionCall = func(name string, args gjson.Result) any {
				mu.Lock()
				calls = append(calls, name)
				mu.Unlock()
				return answerAppFunction(name, args)
			}
			c := openLive(t, tr, opts)
			start := time.Now()
			for i, p := range prompts {
				time.Sleep(time.Until(start.Add(time.Duration(i) * longRealTurnGap)))
				if i == len(prompts)/2 {
					// Half way: the rest of the call delegates to another model. On WebRTC the
					// switch comes from a sideband, as an operator's console would send it.
					update := `{"type":"session.update","event_id":"switch","session":{"delegation":{"type":"responses","responses":{"model":"` + backendModel2 + `"}}}}`
					if tr == webrtcTransport {
						// Mid-conversation OpenAI streams the session's audio events to a sideband and
						// may not send it session.started at all; the switch landing on the primary
						// is the proof the sideband is attached.
						sideband, _, err := attachSideband(t, c.ProviderSessionID, realHeaders())
						require.NoError(t, err)
						sideband.Send(update)
					} else {
						c.Send(update)
					}
					assert.Equal(t, backendModel2, c.WaitWithin(realTurnWait, "session.updated").Get("session.delegation.responses.model").Str)
				}
				seen := len(c.Frames(speechFrame(tr)))
				speak(t, c, tr, p.text)
				finishTurn(t, c, tr, p.lookup, seen)
				require.Empty(t, c.Frames("error"), "turn %d: %s", i+1, c.errorMessages())
			}
			time.Sleep(time.Until(start.Add(longRealDuration)))
			_, closed := c.CloseSessionOrDrop()

			row := findLiveLog(t, c.ProviderSessionID)
			assert.Equal(t, "success", row.Get("status").Str, "a provider drop at close is not the session's fault")
			assert.GreaterOrEqual(t, row.Get("token_usage.audio_seconds").Float(), longRealDuration.Seconds()-60, "the whole call is billed, from the last usage report at worst")
			assert.Equal(t, string(tr), row.Get("live_session.transport").Str)
			delegations := row.Get("live_session.delegations").Array()
			assert.GreaterOrEqual(t, len(delegations), 4, "lookups across the whole call are logged")
			models := map[string]bool{}
			for _, d := range delegations {
				models[d.Get("model").Str] = true
			}
			assert.True(t, models[backendModel] && models[backendModel2], "delegations ran on both backends: %v", models)
			assert.GreaterOrEqual(t, len(row.Get("live_session.transcript").Array()), len(prompts), "every turn is in the transcript")
			mu.Lock()
			answered := append([]string(nil), calls...)
			mu.Unlock()
			assert.NotEmpty(t, answered, "the backend called at least one of the app's functions")
			for _, name := range answered {
				assert.Contains(t, row.Get("tool_call_names").Raw, name)
			}
			assert.Len(t, liveLogRows(t, c.ProviderSessionID), 1, "one row for the whole call")

			if !closed {
				t.Log("recording not checked: the provider dropped the session before finalizing it")
				return
			}
			started := time.Now()
			status, body, _ := downloadContentWithin(t, c.ProviderSessionID, realHeaders(), 5*time.Minute)
			assert.Equal(t, http.StatusOK, status, "%.200s", body)
			t.Logf("recording: %d bytes after %s", len(body), time.Since(started).Round(time.Second))
		})
	}
}
