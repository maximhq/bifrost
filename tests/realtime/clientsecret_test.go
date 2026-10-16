package realtime

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Client secrets: a server mints a short-lived token for a browser, which then opens the session
// with it. The gateway hands out a token of its own (ek_bf_…) that maps to the provider's token
// and to the virtual key that minted it, so the provider token never reaches the browser.

// mintClientSecret asks the gateway for an ephemeral token for a model, as an app's server does.
func mintClientSecret(t *testing.T, vk virtualKey, model string) string {
	t.Helper()
	status, body := postJSON(t, "/v1/realtime/client_secrets", vk.Value, map[string]any{
		"session": map[string]any{"type": "realtime", "model": "openai/" + model},
	})
	require.Equal(t, http.StatusOK, status, "mint client secret: %s", body.Raw)
	return body.Get("value").Str
}

func TestClientSecret_BrowserSessionRunsOnTheMintingKey(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	model := newModel(t)

	token := mintClientSecret(t, vk, model)
	forwarded := upstream.clientSecretRequest(t, model)
	assert.True(t, strings.HasPrefix(token, "ek_bf_"), "the browser gets the gateway's token, got %q", token)
	assert.Equal(t, model, forwarded.Get("session.model").Str, "the provider is asked for the bare model")

	c := connect(t, dial{query: "model=openai/" + model, key: token})
	up := upstream.session(t, model)
	c.expect("session.created")
	assert.True(t, strings.HasPrefix(up.Authorization, "Bearer ek_upstream_"), "the provider sees its own token, got %q", up.Authorization)

	c.send(responseCreate())
	up.expect("response.create")
	up.send(textReply("resp_1", "Hello browser.", tokens{input: 4, output: 3})...)
	assert.Equal(t, vk.ID, waitTurnRows(t, model, 1)[0].VirtualKeyID, "the turn is billed to the key that minted the token")
}

// Revoking the virtual key revokes the tokens it minted, before any provider session opens.
func TestClientSecret_RevokedKeysTokenIsRefused(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	model := newModel(t)
	token := mintClientSecret(t, vk, model)
	upstream.clientSecretRequest(t, model)

	deleteVirtualKey(vk.ID)

	status, _ := refused(t, dial{query: "model=openai/" + model, key: token})
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.False(t, upstream.dialed(model))
}
