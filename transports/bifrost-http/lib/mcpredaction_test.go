package lib

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedactMCPClientConfig_SurfacesLiteralConnectionString pins the security
// boundary of the MCP redaction: the connection string is a server address and
// reads back verbatim, while everything that actually authenticates the
// connection stays masked.
func TestRedactMCPClientConfig_SurfacesLiteralConnectionString(t *testing.T) {
	c := &Config{}

	config := &schemas.MCPClientConfig{
		ID:                "mcp-1",
		Name:              "self",
		ConnectionType:    schemas.MCPConnectionTypeHTTP,
		ConnectionString:  schemas.NewSecretVar("https://mcp.internal.example.com/mcp"),
		OauthClientID:     schemas.NewSecretVar("oauth-client-id-value"),
		OauthClientSecret: schemas.NewSecretVar("oauth-client-secret-value"),
		Headers: map[string]schemas.SecretVar{
			"Authorization": *schemas.NewSecretVar("Bearer super-secret-token"),
		},
		TokenExchange: &schemas.MCPTokenExchangeConfig{
			Audience:     "api://example",
			ClientID:     schemas.NewSecretVar("exchange-client-id-value"),
			ClientSecret: schemas.NewSecretVar("exchange-client-secret-value"),
		},
		TLSConfig: &schemas.MCPTLSConfig{
			CACertPEM: schemas.NewSecretVar("-----BEGIN CERTIFICATE-----secret-pem-body-----END CERTIFICATE-----"),
		},
	}

	redacted := c.RedactMCPClientConfig(config)
	require.NotNil(t, redacted)

	assert.Equal(t, "https://mcp.internal.example.com/mcp", redacted.ConnectionString.GetValue(),
		"the connection target is an address, not a credential")

	// Everything below is a credential and must not survive the round-trip.
	assert.True(t, redacted.OauthClientID.IsRedacted(), "oauth_client_id leaked")
	assert.True(t, redacted.OauthClientSecret.IsRedacted(), "oauth_client_secret leaked")
	redactedHeader := redacted.Headers["Authorization"]
	assert.NotContains(t, redactedHeader.GetValue(), "super-secret-token", "header value leaked")
	assert.True(t, redacted.TokenExchange.ClientID.IsRedacted(), "token_exchange.client_id leaked")
	assert.True(t, redacted.TokenExchange.ClientSecret.IsRedacted(), "token_exchange.client_secret leaked")
	assert.NotContains(t, redacted.TLSConfig.CACertPEM.GetValue(), "secret-pem-body",
		"tls_config.ca_cert_pem leaked")

	// The live config must be untouched — the runtime dials from it.
	liveHeader := config.Headers["Authorization"]
	assert.Equal(t, "Bearer super-secret-token", liveHeader.GetValue())
	assert.Equal(t, "oauth-client-secret-value", config.OauthClientSecret.GetValue())
	assert.NotSame(t, config.ConnectionString, redacted.ConnectionString,
		"redacted copy must not alias the live connection string")
}

// TestRedactMCPClientConfig_MasksSecretBackedConnectionString covers the other
// half: a connection string sourced from env/vault keeps its resolved value
// hidden and its reference intact for the UI to render.
func TestRedactMCPClientConfig_MasksSecretBackedConnectionString(t *testing.T) {
	t.Setenv("MCP_REDACTION_TEST_URL", "https://mcp-secret.internal.example.com/mcp")

	c := &Config{}
	connectionString := schemas.NewSecretVar("env.MCP_REDACTION_TEST_URL")
	require.Equal(t, "https://mcp-secret.internal.example.com/mcp", connectionString.GetValue(),
		"setup: connection string should resolve from the environment")

	redacted := c.RedactMCPClientConfig(&schemas.MCPClientConfig{
		ID:               "mcp-1",
		Name:             "self",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: connectionString,
	})

	assert.NotContains(t, redacted.ConnectionString.GetValue(), "mcp-secret.internal.example.com",
		"resolved env value leaked through connection_string")
	assert.Equal(t, "env.MCP_REDACTION_TEST_URL", redacted.ConnectionString.GetRawRef(),
		"secret ref must be preserved so the UI can show it")
}
