package realtime

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Client secrets: a server mints a short-lived token for a browser, which then opens the session
// with it. The gateway hands out a token of its own (ek_bf_…) that maps to the provider's token
// and to the virtual key that minted it, so the provider token never reaches the browser.

// mintClientSecret asks the gateway for an ephemeral token for a model, as an app's server does.
func mintClientSecret(t *testing.T, vk virtualKey, model string) gjson.Result {
	t.Helper()
	status, body := postJSON(t, "/v1/realtime/client_secrets", vk.Value, map[string]any{
		"session": map[string]any{"type": "realtime", "model": model},
	})
	require.Equal(t, http.StatusOK, status, "mint client secret: %s", body.Raw)
	return body
}

// browserDial opens a session the way a browser does with a client secret: browsers cannot set
// Authorization on a WebSocket, so the token travels in a subprotocol. vk is the minting key.
func browserDial(query string, vk virtualKey, token string) dial {
	return dial{query: query, vk: vk, anonymous: true, subprotocols: []string{"realtime", "openai-insecure-api-key." + token}}
}

// tokenShape describes a token for a failure message without printing the credential.
func tokenShape(token string) string {
	return fmt.Sprintf("%q… (%d chars)", token[:min(6, len(token))], len(token))
}

func TestClientSecret_BrowserSessionRunsOnTheMintingKey(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})

	minted := mintClientSecret(t, vk, "openai/"+openaiModel)
	token := minted.Get("value").Str
	assert.True(t, strings.HasPrefix(token, "ek_bf_"), "the browser gets the gateway's token, got %s", tokenShape(token))
	assert.Equal(t, openaiModel, minted.Get("session.model").Str, "the provider was asked for the bare model")

	s := openSession(t, browserDial("model=openai/"+openaiModel, vk, token))
	s.configure(textSession())
	done := s.ask("Say hello browser.")

	require.Equal(t, "completed", done.Get("response.status").Str, "response.done: %s", done.Raw)
	row := s.turnRow()
	assert.Equal(t, "success", row.Status)
	assert.Equal(t, vk.ID, row.VirtualKeyID, "the turn is billed to the key that minted the token")
}

// Revoking the virtual key revokes the tokens it minted, before any provider session opens.
func TestClientSecret_RevokedKeysTokenIsRefused(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{})
	token := mintClientSecret(t, vk, "openai/"+openaiModel).Get("value").Str

	require.NoError(t, deleteVirtualKey(vk.ID))

	status, _ := refused(t, dial{query: "model=openai/" + openaiModel, token: token})
	assert.Equal(t, http.StatusUnauthorized, status)
}
