package live

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Admission: what the gateway refuses before a session costs anything, and how it rewrites
// the session it forwards.

// openFakeSession starts a WebSocket session on the fake upstream under a key and returns both ends.
func openFakeSession(t *testing.T, vk virtualKey, backend string, extra map[string]any) (*liveClient, *fakeSession) {
	t.Helper()
	session := sessionFor(t, voiceModel, backend, extra)
	c := startSession(t, clientOptions{headers: vkHeaders(vk), session: session})
	s := fake.WaitSession(t, session["instructions"].(string))
	require.Equal(t, s.ID(), c.ProviderSessionID, "the client sees the provider's session id")
	return c, s
}

func TestAdmission_AnonymousIsRefusedBeforeUpgrade(t *testing.T) {
	requireFake(t)
	t.Parallel()
	_, resp, err := dialLive(t, "/v1/live/sessions", nil)
	require.Error(t, err, "no credential: the upgrade itself is refused")
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAdmission_UnknownVirtualKeyIsRefusedAfterUpgrade(t *testing.T) {
	requireFake(t)
	t.Parallel()
	session := sessionFor(t, voiceModel, backendModel, nil)
	c := openSession(t, clientOptions{headers: map[string]string{"x-bf-vk": "sk-bf-does-not-exist"}, session: session})
	frame := c.WaitFor("error")
	assert.NotEmpty(t, errorMessage(frame))
	assert.Equal(t, 0, fake.SessionCount(session["instructions"].(string)), "nothing reaches the provider")
}

func TestAdmission_VoiceModelNotAllowedByVirtualKey(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, virtualKeySpec{allowedModels: []string{"gpt-4o-mini"}})
	session := sessionFor(t, voiceModel, backendModel, nil)
	c := openSession(t, clientOptions{headers: vkHeaders(vk), session: session})
	frame := c.WaitFor("error")
	assert.Contains(t, strings.ToLower(errorMessage(frame)), "model")
	assert.Equal(t, 0, fake.SessionCount(session["instructions"].(string)), "nothing reaches the provider")
}

func TestAdmission_BackendModelNotAllowedByVirtualKey(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, virtualKeySpec{allowedModels: []string{voiceModel}})
	session := sessionFor(t, voiceModel, backendModel, nil)
	c := openSession(t, clientOptions{headers: vkHeaders(vk), session: session})
	frame := c.WaitFor("error")
	assert.NotEmpty(t, errorMessage(frame), "the backend model is admitted with the session, not on first use")
	assert.Equal(t, 0, fake.SessionCount(session["instructions"].(string)), "nothing reaches the provider")
}

func TestAdmission_BackendOnAnotherProviderIsRefused(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, virtualKeySpec{})
	session := sessionFor(t, voiceModel, "anthropic/claude-sonnet-4-5", nil)
	c := openSession(t, clientOptions{headers: vkHeaders(vk), session: session})
	frame := c.WaitFor("error")
	assert.Contains(t, errorMessage(frame), "must be served by openai")
	assert.Equal(t, 0, fake.SessionCount(session["instructions"].(string)))
}

func TestAdmission_ProviderPrefixesAreStrippedOnTheWire(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, virtualKeySpec{})
	c, s := openFakeSession(t, vk, "openai/"+backendModel, map[string]any{"model": "openai/" + voiceModel})
	assert.Equal(t, voiceModel, s.StartModel(), "OpenAI receives the bare voice model")
	assert.Equal(t, backendModel, s.StartBackend(), "OpenAI receives the bare backend model")
	assert.Equal(t, "Bearer sk-fake-live-upstream", s.Auth(), "the provider key, never the virtual key, goes upstream")
	c.CloseSession()
}

func TestAdmission_OpenAIIntegrationRouteServesSessions(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, virtualKeySpec{})
	session := sessionFor(t, voiceModel, backendModel, nil)
	c := startSession(t, clientOptions{path: "/openai/v1/live/sessions", headers: vkHeaders(vk), session: session})
	s := fake.WaitSession(t, session["instructions"].(string))
	assert.Equal(t, s.ID(), c.ProviderSessionID)
	c.CloseSession()
}

func TestAdmission_WebRTCCreateIsValidatedAndAuthenticated(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, virtualKeySpec{})
	status, raw, err := apiCall(http.MethodPost, "/v1/live/sessions", map[string]any{"session": map[string]any{"model": voiceModel}}, vkHeaders(vk))
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, status, "no transport: %s", raw)
	assert.Contains(t, string(raw), "transport")

	status, raw, err = apiCall(http.MethodPost, "/v1/live/sessions", map[string]any{"session": map[string]any{"model": voiceModel}, "transport": map[string]any{"type": "webrtc", "sdp": "v=0"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, status, "no credential: %s", raw)
}
