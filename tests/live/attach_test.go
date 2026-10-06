package live

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
