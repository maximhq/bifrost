package live

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real upstream smoke: the gateway talks to api.openai.com. Paid, run by hand with
// LIVE_UPSTREAM=real. Speech comes from macOS `say`; scenarios that need it skip elsewhere.

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
)

func TestReal_WebSocketSessionDelegatesAndLogs(t *testing.T) {
	requireReal(t)
	pcm := speakPCM(t, "What is the weather in Paris right now? Please look it up.")
	session := sessionFor(t, voiceModel, backendModel, map[string]any{
		"store": true,
		"audio": map[string]any{"output": map[string]any{"voice": "marin"}, "format": map[string]any{"type": "audio/pcm", "rate": 24000}},
	})
	c := startSession(t, clientOptions{path: realPath, headers: realHeaders(), session: session, microphone: true})
	c.SpeakPCM(pcm)
	created := c.WaitWithin(realDelegationWait, "session.delegation.created")
	assert.NotEmpty(t, created.Get("delegation.id").Str)
	completed := waitNestedCompleted(t, c, realDelegationWait)
	assert.Greater(t, completed.Get("event.response.usage.total_tokens").Int(), int64(0))
	// The voice model speaks the answer after the delegation; hang up only once it has gone quiet.
	c.WaitQuiet(realTurnWait, 3*time.Second, "session.output_transcript.delta")
	c.CloseSession()

	row := findLiveLog(t, c.ProviderSessionID)
	assert.Greater(t, row.Get("token_usage.audio_seconds").Float(), 0.0)
	assert.True(t, row.Get("live_session.usage_confirmed").Bool())
	require.NotEmpty(t, row.Get("live_session.delegations").Array())
	assert.NotEmpty(t, row.Get("live_session.transcript").Array())
	assert.Equal(t, "websocket", row.Get("live_session.transport").Str)
}

func TestReal_WebRTCSessionRecordsAndServesContent(t *testing.T) {
	requireReal(t)
	session := sessionFor(t, voiceModel, backendModel, map[string]any{"store": true})
	c, _ := createWebRTCSession(t, clientOptions{path: realPath, headers: realHeaders(), session: session})
	if packets, ok := speakOpus(t); ok {
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
	session := sessionFor(t, voiceModel, backendModel, nil)
	primary, _ := createWebRTCSession(t, clientOptions{path: realPath, headers: realHeaders(), session: session})
	sideband, _, err := attachSideband(t, primary.ProviderSessionID, realHeaders())
	require.NoError(t, err)
	assert.Equal(t, primary.ProviderSessionID, sideband.WaitWithin(realTurnWait, "session.started").Get("session.id").Str)

	sideband.Send(`{"type":"session.instructions.append","event_id":"steer_1","delegation_id":null,"content":"Answer in one short sentence."}`)
	assert.Equal(t, "steer_1", sideband.WaitWithin(realTurnWait, "session.instructions.appended").Get("client_event_id").Str)
	sideband.Send(`{"type":"session.update","event_id":"switch","session":{"delegation":{"type":"responses","responses":{"model":"` + backendModel2 + `"}}}}`)
	assert.Equal(t, backendModel2, primary.WaitWithin(realTurnWait, "session.updated").Get("session.delegation.responses.model").Str)
	primary.CloseSession()
	findLiveLog(t, primary.ProviderSessionID)
}

func TestReal_VoiceModelCannotChangeMidSession(t *testing.T) {
	requireReal(t)
	session := sessionFor(t, voiceModel, backendModel, nil)
	c := startSession(t, clientOptions{path: realPath, headers: realHeaders(), session: session})
	c.Send(`{"type":"session.update","event_id":"switch_voice","session":{"model":"gpt-live-transcribe"}}`)
	assert.Contains(t, errorMessage(c.WaitWithin(realTurnWait, "error")), "session.model", "OpenAI accepts session.model on session.start only")
	c.CloseSession()
}

func TestReal_ClientDelegationRunsTheAppsBackend(t *testing.T) {
	requireReal(t)
	pcm := speakPCM(t, "What is the weather in Paris right now? Please look it up.")
	session := sessionFor(t, voiceModel, "", map[string]any{
		"delegation": map[string]any{"type": "client"},
		"audio":      map[string]any{"output": map[string]any{"voice": "marin"}, "format": map[string]any{"type": "audio/pcm", "rate": 24000}},
	})
	var c *liveClient
	answered := make(chan string, 1)
	c = startSession(t, clientOptions{path: realPath, headers: realHeaders(), session: session, microphone: true, onClientTask: func(delegationID string) {
		response := callResponses(t, "/openai/v1/responses", realHeaders(), c.ProviderSessionID, backendModel,
			[]map[string]any{{"role": "user", "content": "What is the weather in Paris right now? Look it up and answer in one sentence."}})
		text := response.Get(`output.#(type=="message").content.0.text`).Str
		c.Commentary(delegationID, text)
		answered <- text
	}})
	c.SpeakPCM(pcm)
	created := c.WaitWithin(realDelegationWait, "session.delegation.created")
	assert.Equal(t, "client", created.Get("delegation.target").Str)
	select {
	case text := <-answered:
		assert.NotEmpty(t, text)
	case <-time.After(realDelegationWait):
		t.Fatal("the app's backend call did not complete")
	}
	c.WaitWithin(realTurnWait, "session.commentary.appended")
	c.WaitQuiet(realTurnWait, 3*time.Second, "session.output_transcript.delta")
	c.CloseSession()

	row := findLiveLog(t, c.ProviderSessionID)
	assert.Empty(t, row.Get("live_session.delegations").Array(), "the app ran the backend; the session billed voice only")
	assert.Greater(t, row.Get("token_usage.audio_seconds").Float(), 0.0)
	backendRow := findSessionRow(t, c.ProviderSessionID, "responses")
	assert.Greater(t, backendRow.Get("token_usage.total_tokens").Float(), 0.0)
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
