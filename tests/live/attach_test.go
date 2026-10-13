package live

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Sidebands: a second connection that steers a session it does not own.

func TestAttach_WebSocketPrimaryCannotBeAttachedTo(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, virtualKeySpec{})
	c, s := openFakeSession(t, wsTransport, vk, backendModel, nil)

	_, status, err := attachSideband(t, s.ID(), vkHeaders(vk))
	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, status, "OpenAI attaches to WebRTC and SIP sessions only")
	c.CloseSession()
}

func TestAttach_UnknownSessionIs404(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, virtualKeySpec{})
	_, status, err := attachSideband(t, "live_nope", vkHeaders(vk))
	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, status)
}

func TestAttach_AnonymousSidebandIsRefused(t *testing.T) {
	requireFake(t)
	t.Parallel()
	_, status, err := attachSideband(t, "live_any", nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, status)
}

func TestAttach_SidebandSteersAWebRTCSession(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, virtualKeySpec{allowedModels: []string{voiceModel, backendModel, backendModel2}})
	primary, s := openFakeSession(t, webrtcTransport, vk, backendModel, nil)

	sideband, _, err := attachSideband(t, s.ID(), vkHeaders(vk))
	require.NoError(t, err)
	s.WaitConnections(t, 2)
	assert.Equal(t, s.ID(), sideband.WaitFor("session.started").Get("session.id").Str, "a sideband is told which session it joined")

	// An instruction from the sideband is acknowledged on both connections.
	sideband.Send(`{"type":"session.instructions.append","event_id":"steer_1","delegation_id":null,"content":"be brief"}`)
	assert.Equal(t, "be brief", s.WaitInbound(t, "session.instructions.append", 1).Get("content").Str)
	assert.Equal(t, "steer_1", sideband.WaitFor("session.instructions.appended").Get("client_event_id").Str)
	assert.Equal(t, "steer_1", primary.WaitFor("session.instructions.appended").Get("client_event_id").Str)

	// A backend switch from the sideband: checked against the key, then billed by the primary.
	sideband.Send(`{"type":"session.update","event_id":"switch","session":{"delegation":{"type":"responses","responses":{"model":"` + backendModel2 + `"}}}}`)
	assert.Equal(t, backendModel2, s.WaitInbound(t, "session.update", 1).Get("session.delegation.responses.model").Str)
	assert.Equal(t, backendModel2, primary.WaitFor("session.updated").Get("session.delegation.responses.model").Str)
	s.Play(delegation{ID: "item_1", ResponseID: "resp_1", Model: backendModel2, Created: true, Input: 10, Output: 5, Items: []string{messageItem("msg_1", "Steered.")}})
	primary.WaitFor("response.event")
	settle()
	primary.CloseSession()

	row := findLiveLog(t, s.ID())
	delegations := row.Get("live_session.delegations").Array()
	require.Len(t, delegations, 1)
	assert.Equal(t, backendModel2, delegations[0].Get("model").Str, "the primary bills the model the sideband switched to")
	assert.Equal(t, "webrtc", row.Get("live_session.transport").Str)
}

func TestAttach_SidebandUpdateIsCheckedAgainstTheKey(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, virtualKeySpec{})
	primary, s := openFakeSession(t, webrtcTransport, vk, backendModel, nil)
	sideband, _, err := attachSideband(t, s.ID(), vkHeaders(vk))
	require.NoError(t, err)
	sideband.WaitFor("session.started")

	sideband.Send(`{"type":"session.update","event_id":"switch","session":{"delegation":{"type":"responses","responses":{"model":"anthropic/claude-sonnet-4-5"}}}}`)
	assert.Contains(t, errorMessage(sideband.WaitFor("error")), "must be served by openai")
	assert.Empty(t, s.Inbound("session.update"), "the refused update never reached the provider")
	primary.CloseSession()
}

func TestAttach_SidebandOutlivesNothing(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, virtualKeySpec{})
	primary, s := openFakeSession(t, webrtcTransport, vk, backendModel, nil)
	sideband, _, err := attachSideband(t, s.ID(), vkHeaders(vk))
	require.NoError(t, err)
	sideband.WaitFor("session.started")
	s.WaitConnections(t, 2)

	// The primary hangs up: the session ends for the sideband too, and bills once.
	s.EmitUsage(20)
	primary.WaitFor("session.usage.updated")
	primary.CloseSession()
	sideband.WaitFor("session.closed")
	sideband.WaitGone()

	row := findLiveLog(t, s.ID())
	assert.Equal(t, 20.0, row.Get("token_usage.audio_seconds").Float())
	assert.Len(t, liveLogRows(t, s.ID()), 1, "a sideband is not a session of its own")
}

// A session on a custom provider: the bare attach and content routes only carry the session id,
// and with the standard OpenAI provider configured too they would pick it. The caller names the
// provider with x-model-provider (or ?provider=), as on the file, batch and video routes.
func TestAttach_NamedProviderRoutesACustomProviderSession(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, virtualKeySpec{provider: splitProvider, keyNames: []string{"split-all"}})
	session := sessionFor(t, splitProvider+"/"+voiceModel, splitProvider+"/"+backendModel, map[string]any{"store": true})
	primary := openLive(t, webrtcTransport, clientOptions{headers: vkHeaders(vk), session: session})
	s := fake.WaitSession(t, session["instructions"].(string))

	named := vkHeaders(vk)
	named["x-model-provider"] = splitProvider
	sideband, status, err := attachSideband(t, s.ID(), named)
	require.NoError(t, err, "attach on the named provider (HTTP %d)", status)
	s.WaitConnections(t, 2)
	assert.Equal(t, s.ID(), sideband.WaitFor("session.started").Get("session.id").Str)
	sideband.Close()
	primary.CloseSession()

	status, body, contentType := downloadContent(t, s.ID(), named)
	require.Equal(t, http.StatusOK, status, "%s", body)
	assert.Equal(t, "audio/wav", contentType)
	row := findContentLog(t, s.ID())
	assert.Equal(t, splitProvider, row.Get("provider").Str, "the download is logged on the provider that owns the session")
}

// A sideband names only a session id, yet it acts on the operator's key for the provider, so it
// is held to the caller's virtual key as the primary session is: the session must be the key's
// own, the provider must be allowed, and the key it dials with must be one the virtual key may use.
func TestAttach_SidebandIsHeldToTheVirtualKey(t *testing.T) {
	requireFake(t)
	t.Parallel()
	owner := createVirtualKey(t, virtualKeySpec{provider: splitProvider, keyNames: []string{"split-all"}})
	session := sessionFor(t, splitProvider+"/"+voiceModel, splitProvider+"/"+backendModel, nil)
	primary := openLive(t, webrtcTransport, clientOptions{headers: vkHeaders(owner), session: session})
	s := fake.WaitSession(t, session["instructions"].(string))
	defer primary.CloseSession()

	// Unnamed, the bare route resolves to the standard OpenAI provider, which this key may not
	// use: refused before anything is dialed on the operator's key.
	_, status, err := attachSideband(t, s.ID(), vkHeaders(owner))
	require.Error(t, err, "a provider the virtual key may not use")
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, 1, s.Connections(), "nothing reached the provider")

	// The session belongs to the virtual key that opened it: another key allowed on the provider
	// is told the session does not exist, before a key is selected or anything is dialed.
	other := createVirtualKey(t, virtualKeySpec{provider: splitProvider, keyNames: []string{"split-backend"}})
	foreign := vkHeaders(other)
	foreign["x-model-provider"] = splitProvider
	_, status, err = attachSideband(t, s.ID(), foreign)
	require.Error(t, err, "another virtual key's session")
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, 1, s.Connections(), "nothing reached the provider")

	// The opener attaches on a key its virtual key allows, never on another of the provider's.
	named := vkHeaders(owner)
	named["x-model-provider"] = splitProvider
	sideband, _, err := attachSideband(t, s.ID(), named)
	require.NoError(t, err)
	s.WaitConnections(t, 2)
	assert.Equal(t, s.ID(), sideband.WaitFor("session.started").Get("session.id").Str)
	assert.Equal(t, []string{"Bearer sk-fake-split-all"}, s.SidebandAuths(), "the sideband dials with the key the virtual key allows")
	sideband.Close()

	// Each attempt the key was admitted to is a row of its own naming the session and the key:
	// the refusal, and the sideband.
	refused := findSidebandLog(t, s.ID(), owner.ID, "error")
	assert.Equal(t, "error", refused.Get("status").Str)
	row := findSidebandLog(t, s.ID(), owner.ID, "success")
	assert.Equal(t, splitProvider, row.Get("provider").Str)
	assert.Equal(t, "split-all", row.Get("selected_key_name").Str)
	assert.False(t, row.Get("live_session").Exists(), "a sideband is not a session")
}

// A sideband spends nothing: the session it joins already counted as the key's request, and the
// primary's meter is what budgets and limits govern. A key that may open no further session can
// still attach a control connection to the one it has.
func TestAttach_SidebandIsNotASpendingRequest(t *testing.T) {
	requireFake(t)
	t.Parallel()
	one := int64(1)
	vk := createVirtualKey(t, virtualKeySpec{requestMaxLimit: &one})
	primary, s := openFakeSession(t, webrtcTransport, vk, backendModel, nil)
	defer primary.CloseSession()
	// The first window closing is what counts the session as the key's one request.
	s.EmitUsage(30)
	primary.WaitFor("session.usage.updated")
	settle()

	sideband, status, err := attachSideband(t, s.ID(), vkHeaders(vk))
	require.NoError(t, err, "the key's one request is the session itself (HTTP %d)", status)
	s.WaitConnections(t, 2)
	assert.Equal(t, s.ID(), sideband.WaitFor("session.started").Get("session.id").Str)
	sideband.Close()
}

// Attach attempts log independently of the primary session, even before it ends or when no
// local session exists. The record describes the handshake, not another billable conversation.
func TestAttach_LogsItsOwnRequest(t *testing.T) {
	requireFake(t)
	vk := createVirtualKey(t, virtualKeySpec{})
	primary, s := openFakeSession(t, webrtcTransport, vk, backendModel, nil)
	defer primary.CloseSession()

	var sidebands []*liveClient
	for range 2 {
		sideband, status, err := attachSideband(t, s.ID(), vkHeaders(vk))
		require.NoError(t, err, "attach (HTTP %d)", status)
		sideband.WaitFor("session.started")
		sidebands = append(sidebands, sideband)
	}
	// A sideband's row is written once it detaches.
	for _, sideband := range sidebands {
		sideband.Close()
	}
	rows := waitAttachLogs(t, s.ID(), 2)
	assert.NotEqual(t, rows[0].Get("id").Str, rows[1].Get("id").Str)
	for _, row := range rows {
		assert.Equal(t, "live_sideband", row.Get("object").Str)
		assert.Equal(t, "success", row.Get("status").Str)
		assert.Equal(t, "openai", row.Get("provider").Str)
		assert.Equal(t, vk.ID, row.Get("virtual_key_id").Str)
		assert.NotEmpty(t, row.Get("selected_key_id").Str)
		assert.GreaterOrEqual(t, row.Get("latency").Float(), 0.0)
		assert.Zero(t, row.Get("cost").Float())
		assert.False(t, row.Get("token_usage").Exists())
		assert.False(t, row.Get("live_session").Exists())
	}
}

func TestAttach_FailedRequestIsLoggedWithoutAParentSession(t *testing.T) {
	requireFake(t)
	vk := createVirtualKey(t, virtualKeySpec{})
	sessionID := "live_missing_" + vk.ID
	_, status, err := attachSideband(t, sessionID, vkHeaders(vk))
	require.Error(t, err)
	require.Equal(t, http.StatusNotFound, status)
	row := waitAttachLogs(t, sessionID, 1)[0]
	assert.Equal(t, "error", row.Get("status").Str)
	assert.Equal(t, "openai", row.Get("provider").Str)
	assert.Equal(t, vk.ID, row.Get("virtual_key_id").Str)
	assert.Equal(t, int64(http.StatusNotFound), row.Get("error_details.status_code").Int())
	assert.Zero(t, row.Get("cost").Float())
}

func waitAttachLogs(t *testing.T, providerSessionID string, count int) []gjson.Result {
	t.Helper()
	var matches []gjson.Result
	require.Eventually(t, func() bool {
		status, raw, err := apiCall(http.MethodGet, "/api/logs?objects=live_sideband&limit=500&start_time="+suiteStart.Format(time.RFC3339Nano), nil, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "list logs: %s", raw)
		matches = nil
		for _, summary := range gjson.GetBytes(raw, "logs").Array() {
			row := fetchLog(t, summary.Get("id").Str)
			if row.Get("metadata.provider_session_id").Str == providerSessionID {
				matches = append(matches, row)
			}
		}
		return len(matches) == count
	}, rowWaitTimeout, 300*time.Millisecond, "expected %d live_sideband rows for %s", count, providerSessionID)
	return matches
}
