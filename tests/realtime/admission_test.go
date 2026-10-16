package realtime

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Admission: who may open a session, on which routes, and what reaches the provider when they do.
// Credentials are refused with an HTTP status before the upgrade; a bad target is refused in-band,
// after it, as an error event.

func TestAdmission_NoCredentialIsRefusedBeforeTheUpgrade(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	model := newModel(t)

	status, _ := refused(t, dial{query: "model=openai/" + model, anonymous: true})

	assert.Equal(t, http.StatusUnauthorized, status)
	assert.False(t, upstream.dialed(model), "no upstream session on the operator's key")
}

func TestAdmission_UnknownVirtualKeyIsRefusedBeforeTheUpgrade(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	model := newModel(t)

	status, _ := refused(t, dial{query: "model=openai/" + model, key: "sk-bf-not-a-real-key"})

	assert.Equal(t, http.StatusUnauthorized, status)
	assert.False(t, upstream.dialed(model))
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
	c := connect(t, dial{query: "model=anthropic/" + newModel(t)})

	c.expectError("invalid_request_error", "provider does not support realtime: anthropic")
	c.expectClosed()
}

// A model outside the virtual key's allowlist never gets a turn to the provider.
func TestAdmission_ModelOutsideTheKeysAllowlistGetsNoTurn(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{allowedModels: []string{"regex:some-other-model-.*"}})
	s := openSession(t, dial{key: vk.Value})

	s.client.send(responseCreate())

	s.client.expectError(anyType, "is not allowed for virtual key")
	s.upstream.expectClosed() // without the response.create
	s.client.expectClosed()
}

// Browsers cannot set Authorization on a WebSocket, so they carry the key in a subprotocol.
func TestAdmission_BrowserSubprotocolCredential(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t, dial{anonymous: true, subprotocols: []string{"realtime", "openai-insecure-api-key." + suiteKey.Value}})

	assert.Equal(t, "realtime", s.client.subprotocol)
	assert.Equal(t, "Bearer "+upstreamKey, s.upstream.Authorization, "the browser's key is not forwarded")
	s.client.send(responseCreate())
	s.upstream.expect("response.create")
	s.upstream.send(textReply("resp_1", "Hi.", tokens{input: 2, output: 1})...)
	assert.Equal(t, suiteKey.ID, s.turnRow().VirtualKeyID)
}

// The OpenAI SDK drop-in routes default the provider to openai, so the model is written bare.
func TestAdmission_OpenAIIntegrationRoutes(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	for _, d := range []dial{
		{route: "/openai/v1/realtime", query: "model={model}"},
		{route: "/openai/realtime", query: "model={model}"},
		{route: "/openai/openai/realtime", query: "deployment={model}"},
	} {
		t.Run(d.route, func(t *testing.T) {
			t.Parallel()
			s := openSession(t, d)
			s.client.send(responseCreate())
			s.upstream.expect("response.create")
			reply := textReply("resp_1", "Routed.", tokens{input: 3, output: 1})
			s.upstream.send(reply...)
			s.client.expectRelayed(reply...)
			assert.Equal(t, "openai", s.turnRow().Provider)
		})
	}
}

// A key alias names a model the provider knows under another id; the provider is dialed with the id.
func TestAdmission_KeyAliasIsResolvedForTheProvider(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{allowedModels: []string{"rt-alias", "regex:rt-e2e-.*"}})
	c := connect(t, dial{query: "model=openai/rt-alias", key: vk.Value})

	up := upstream.session(t, "rt-e2e-alias-target") // config.json: rt-alias → rt-e2e-alias-target
	c.expect("session.created")
	assert.Equal(t, "rt-e2e-alias-target", up.Model)
}
