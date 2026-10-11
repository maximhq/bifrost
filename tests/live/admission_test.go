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

// openFakeSession starts a session on the fake upstream under a key and returns both ends.
func openFakeSession(t *testing.T, tr transport, vk virtualKey, backend string, extra map[string]any) (*liveClient, *fakeSession) {
	t.Helper()
	session := sessionFor(t, voiceModel, backend, extra)
	c := openLive(t, tr, clientOptions{headers: vkHeaders(vk), session: session})
	s := fake.WaitSession(t, session["instructions"].(string))
	require.Equal(t, s.ID(), c.ProviderSessionID, "the client sees the provider's session id")
	require.Equal(t, string(tr), s.Transport(), "the session reaches the provider on the transport the app chose")
	s.WaitConnections(t, 1)
	return c, s
}

func TestAdmission_AnonymousIsRefusedBeforeUpgrade(t *testing.T) {
	requireFake(t)
	t.Parallel()
	_, status, err := dialLive(t, "/v1/live/sessions", nil)
	require.Error(t, err, "no credential: the upgrade itself is refused")
	assert.Equal(t, http.StatusUnauthorized, status)
}

func TestAdmission_UnknownVirtualKeyIsRefused(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		session := sessionFor(t, voiceModel, backendModel, nil)
		refusal := openRefused(t, tr, clientOptions{headers: map[string]string{"x-bf-vk": "sk-bf-does-not-exist"}, session: session})
		assert.NotEmpty(t, errorMessage(refusal))
		assert.Equal(t, 0, fake.SessionCount(session["instructions"].(string)), "nothing reaches the provider")
	})
}

func TestAdmission_VoiceModelNotAllowedByVirtualKey(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{allowedModels: []string{"gpt-4o-mini"}})
		session := sessionFor(t, voiceModel, backendModel, nil)
		refusal := openRefused(t, tr, clientOptions{headers: vkHeaders(vk), session: session})
		assert.Contains(t, strings.ToLower(errorMessage(refusal)), "model")
		assert.Equal(t, 0, fake.SessionCount(session["instructions"].(string)), "nothing reaches the provider")
	})
}

func TestAdmission_BackendModelNotAllowedByVirtualKey(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{allowedModels: []string{voiceModel}})
		session := sessionFor(t, voiceModel, backendModel, nil)
		refusal := openRefused(t, tr, clientOptions{headers: vkHeaders(vk), session: session})
		assert.NotEmpty(t, errorMessage(refusal), "the backend model is admitted with the session, not on first use")
		assert.Equal(t, 0, fake.SessionCount(session["instructions"].(string)), "nothing reaches the provider")
	})
}

func TestAdmission_BackendOnAnotherProviderIsRefused(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		session := sessionFor(t, voiceModel, "anthropic/claude-sonnet-4-5", nil)
		refusal := openRefused(t, tr, clientOptions{headers: vkHeaders(vk), session: session})
		assert.Contains(t, errorMessage(refusal), "must be served by openai")
		assert.Equal(t, 0, fake.SessionCount(session["instructions"].(string)))
	})
}

func TestAdmission_ProviderPrefixesAreStrippedOnTheWire(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		c, s := openFakeSession(t, tr, vk, "openai/"+backendModel, map[string]any{"model": "openai/" + voiceModel})
		assert.Equal(t, voiceModel, s.StartModel(), "OpenAI receives the bare voice model")
		assert.Equal(t, backendModel, s.StartBackend(), "OpenAI receives the bare backend model")
		assert.Equal(t, "Bearer sk-fake-live-upstream", s.Auth(), "the provider key, never the virtual key, goes upstream")
		c.CloseSession()
	})
}

func TestAdmission_DirectKeyGoesUpstream(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		session := sessionFor(t, voiceModel, backendModel, nil)
		headers := map[string]string{"x-bf-direct-key": "true", "Authorization": "Bearer sk-direct-live"}
		c := openLive(t, tr, clientOptions{headers: headers, session: session})
		s := fake.WaitSession(t, session["instructions"].(string))
		assert.Equal(t, "Bearer sk-direct-live", s.Auth(), "the caller's own key goes upstream, not a registered one")
		c.CloseSession()
		row := findLiveLog(t, s.ID())
		assert.Equal(t, "header-provided", row.Get("selected_key_name").Str, "the log row names the caller's key")
	})
}

func TestAdmission_OpenAIIntegrationRouteServesSessions(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{})
		session := sessionFor(t, voiceModel, backendModel, nil)
		c := openLive(t, tr, clientOptions{path: "/openai/v1/live/sessions", headers: vkHeaders(vk), session: session})
		s := fake.WaitSession(t, session["instructions"].(string))
		assert.Equal(t, s.ID(), c.ProviderSessionID)
		c.CloseSession()
	})
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

// splitProvider is a second OpenAI-based provider on the fake with a key per model and one key
// for every model, so a virtual key can be limited to keys that serve one model each.
const splitProvider = "openai-split"

func TestAdmission_VirtualKeyKeysMustServeBothModels(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{provider: splitProvider, keyNames: []string{"split-voice", "split-backend"}})
		session := sessionFor(t, splitProvider+"/"+voiceModel, splitProvider+"/"+backendModel, nil)
		refusal := openRefused(t, tr, clientOptions{headers: vkHeaders(vk), session: session})
		assert.Contains(t, strings.ToLower(errorMessage(refusal)), "key", "no allowed key serves both models")
		assert.Equal(t, 0, fake.SessionCount(session["instructions"].(string)), "nothing reaches the provider")
	})
}

func TestAdmission_SessionUsesOnlyTheVirtualKeysKeys(t *testing.T) {
	requireFake(t)
	t.Parallel()
	forEachTransport(t, func(t *testing.T, tr transport) {
		vk := createVirtualKey(t, virtualKeySpec{provider: splitProvider, keyNames: []string{"split-voice", "split-backend"}})
		session := sessionFor(t, splitProvider+"/"+voiceModel, "", nil)
		c := openLive(t, tr, clientOptions{headers: vkHeaders(vk), session: session})
		s := fake.WaitSession(t, session["instructions"].(string))
		assert.Equal(t, "Bearer sk-fake-split-voice", s.Auth(), "the one allowed key that serves the voice model, not the provider's key for every model")
		s.WaitConnections(t, 1)
		c.CloseSession()
		row := findLiveLog(t, c.ProviderSessionID)
		assert.Equal(t, "split-voice", row.Get("selected_key_name").Str)
	})
}

// noLiveProvider is an OpenAI-based provider on the fake whose allowed requests leave live out.
const noLiveProvider = "openai-nolive"

func TestAdmission_ProviderWithoutLiveIsRefusedAsUnsupported(t *testing.T) {
	requireFake(t)
	t.Parallel()
	vk := createVirtualKey(t, virtualKeySpec{provider: noLiveProvider})
	session := sessionFor(t, noLiveProvider+"/"+voiceModel, "", nil)
	forEachTransport(t, func(t *testing.T, tr transport) {
		refusal := openRefused(t, tr, clientOptions{headers: vkHeaders(vk), session: session})
		assert.Equal(t, "unsupported_operation", refusal.Get("error.code").Str, "%s", refusal.Raw)
		assert.Contains(t, errorMessage(refusal), "not supported")
		if tr == wsTransport {
			assert.Equal(t, "invalid_request_error", refusal.Get("error.type").Str, "the provider's policy is the caller's problem, not a server fault: %s", refusal.Raw)
		}
		assert.Equal(t, 0, fake.SessionCount(session["instructions"].(string)), "nothing reaches the provider")
	})
}
