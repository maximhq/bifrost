package realtime

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Admission: who may open a session, on which routes, and what reaches the provider when they do.
// Credentials are refused with an HTTP status before the upgrade; a bad target is refused in-band,
// after it, as an error event.

func TestAdmission_NoCredentialIsRefusedBeforeTheUpgrade(t *testing.T) {
	requireGateway(t)
	t.Parallel()

	status, _ := refused(t, dial{query: "model=openai/" + openaiModel, anonymous: true})

	assert.Equal(t, http.StatusUnauthorized, status)
}

func TestAdmission_UnknownVirtualKeyIsRefusedBeforeTheUpgrade(t *testing.T) {
	requireGateway(t)
	t.Parallel()

	status, _ := refused(t, dial{query: "model=openai/" + openaiModel, token: "sk-bf-not-a-real-key"})

	assert.Equal(t, http.StatusUnauthorized, status)
}

func TestAdmission_MissingModelIsRefusedInBand(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	c := connect(t, dial{query: "voice=alloy"})

	c.expectError("invalid_request_error", "model")
	c.expectClosed()
}

func TestAdmission_ProviderWithoutRealtimeIsRefusedInBand(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	c := connect(t, dial{query: "model=anthropic/claude-haiku-4-5"})

	c.expectError("invalid_request_error", "provider does not support realtime: anthropic")
	c.expectClosed()
}

// A model outside the virtual key's allowlist never gets a turn to the provider.
func TestAdmission_ModelOutsideTheKeysAllowlistGetsNoTurn(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{allowedModels: []string{"gpt-4o-mini"}})
	s := openSession(t, dial{vk: vk})

	s.client.send(responseCreate())

	s.client.expectError(anyType, "is not allowed for virtual key")
	s.client.expectClosed()
	requireRowCountSettles(t, vk, 0, "no turn ran")
}

// Browsers cannot set Authorization on a WebSocket, so they carry the key in a subprotocol.
func TestAdmission_BrowserSubprotocolCredential(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	s := openSession(t, dial{vk: vk, anonymous: true, subprotocols: []string{"realtime", "openai-insecure-api-key." + vk.Value}})
	s.configure(textSession())

	assert.Equal(t, "realtime", s.client.subprotocol)
	s.ask("Say hi.")
	assert.Equal(t, vk.ID, s.turnRow().VirtualKeyID)
}

// The OpenAI SDK drop-in routes default the provider to openai, so the model is written bare.
func TestAdmission_OpenAIIntegrationRoutes(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	for _, d := range []dial{
		{route: "/openai/v1/realtime", query: "model=" + openaiModel},
		{route: "/openai/realtime", query: "model=" + openaiModel},
		{route: "/openai/openai/realtime", query: "deployment=" + openaiModel},
	} {
		t.Run(d.route, func(t *testing.T) {
			t.Parallel()
			s := openSession(t, d)
			s.configure(textSession())
			s.ask("Say routed.")
			row := s.turnRow()
			assert.Equal(t, "openai", row.Provider)
			assert.Equal(t, openaiModel, row.Model)
		})
	}
}

// A key alias names a model the provider knows under another id; the provider is dialed with the id.
func TestAdmission_KeyAliasIsResolvedForTheProvider(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{allowedModels: []string{"rt-alias", "gpt-realtime-mini"}})

	s := openSession(t, dial{query: "model=openai/rt-alias", vk: vk}) // config.json: rt-alias → gpt-realtime-mini

	model := s.created.Get("session.model").Str
	assert.True(t, strings.HasPrefix(model, "gpt-realtime-mini"), "the provider serves the alias target, got %q", model)
}
