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

// TestRedactMCPClientConfig_OpenAPIConfig: the upstream API credentials inside
// openapi_config are masked and the (large) spec document is left out of reads,
// while the server-computed metadata that describes the spec survives and the
// live config is untouched.
func TestRedactMCPClientConfig_OpenAPIConfig(t *testing.T) {
	c := &Config{}
	base := "https://petstore.example.com/v1"
	config := &schemas.MCPClientConfig{
		ID:             "oa-1",
		Name:           "petstore",
		ConnectionType: schemas.MCPConnectionTypeOpenAPI,
		OpenAPIConfig: &schemas.MCPOpenAPIConfig{
			Spec:    "openapi: 3.0.3\npaths: {}",
			BaseURL: &base,
			SecurityCredentials: map[string]schemas.MCPOpenAPICredential{
				"ApiKeyAuth": {Value: schemas.NewSecretVar("super-secret-api-key")},
				"BasicAuth":  {Username: schemas.NewSecretVar("service-user"), Password: schemas.NewSecretVar("service-password")},
			},
			SpecTitle:      "Petstore",
			OpenAPIVersion: "3.0.3",
			OperationCount: 4,
			SpecSize:       25,
			SpecHash:       "abc",
		},
	}

	redacted := c.RedactMCPClientConfig(config)
	require.NotNil(t, redacted.OpenAPIConfig)
	assert.Empty(t, redacted.OpenAPIConfig.Spec, "the document is not part of a client read")
	assert.Equal(t, base, *redacted.OpenAPIConfig.BaseURL, "the upstream address is not a credential")
	assert.True(t, redacted.OpenAPIConfig.SecurityCredentials["ApiKeyAuth"].Value.IsRedacted(), "api key leaked")
	assert.True(t, redacted.OpenAPIConfig.SecurityCredentials["BasicAuth"].Username.IsRedacted(), "basic username leaked")
	assert.True(t, redacted.OpenAPIConfig.SecurityCredentials["BasicAuth"].Password.IsRedacted(), "basic password leaked")
	assert.Equal(t, "Petstore", redacted.OpenAPIConfig.SpecTitle)
	assert.Equal(t, 4, redacted.OpenAPIConfig.OperationCount)
	assert.Equal(t, "abc", redacted.OpenAPIConfig.SpecHash)

	assert.Equal(t, "openapi: 3.0.3\npaths: {}", config.OpenAPIConfig.Spec, "live config keeps the spec")
	assert.Equal(t, "super-secret-api-key", config.OpenAPIConfig.SecurityCredentials["ApiKeyAuth"].Value.GetValue(), "live config keeps the credential")
	assert.NotSame(t, config.OpenAPIConfig, redacted.OpenAPIConfig)
}

// TestRedactMCPClientConfig_OpenAPISpecURLCredentials: a spec_url can carry a
// signed query or embedded password; a client read masks those parts and
// keeps the rest of the address, and the masked form is stable so an update
// can recognise it.
func TestRedactMCPClientConfig_OpenAPISpecURLCredentials(t *testing.T) {
	c := &Config{}
	specURL := "https://user:hunter2@specs.example.com/petstore.json?token=abc123&v=2"
	config := &schemas.MCPClientConfig{
		ID: "oa-1", Name: "petstore", ConnectionType: schemas.MCPConnectionTypeOpenAPI,
		OpenAPIConfig: &schemas.MCPOpenAPIConfig{Spec: "x", SpecURL: &specURL},
	}
	redacted := c.RedactMCPClientConfig(config)
	got := *redacted.OpenAPIConfig.SpecURL
	assert.NotContains(t, got, "abc123")
	assert.NotContains(t, got, "hunter2")
	assert.NotContains(t, got, "user:", "the username of a password-bearing userinfo can be a token too")
	assert.NotContains(t, RedactURLCredentials("https://api-key:@example.com/spec.json"), "api-key", "an empty password still marks the username as the credential")
	assert.Contains(t, got, "specs.example.com/petstore.json")
	assert.Equal(t, got, RedactURLCredentials(specURL), "the masked form is stable")
	assert.Equal(t, specURL, *config.OpenAPIConfig.SpecURL, "live config keeps the real URL")

	plain := "https://specs.example.com/petstore.json"
	assert.Equal(t, plain, RedactURLCredentials(plain), "a URL without credentials is shown as is")

	// A userinfo with no password is itself the credential (a token).
	tokenOnly := "https://ghp_secret123@api.example.com/v1"
	masked := RedactURLCredentials(tokenOnly)
	assert.NotContains(t, masked, "ghp_secret123")
	assert.Contains(t, masked, "api.example.com/v1")
	assert.Equal(t, masked, RedactURLCredentials(tokenOnly), "the masked form is stable")
	baseURL := tokenOnly
	config.OpenAPIConfig.BaseURL = &baseURL
	assert.NotContains(t, *c.RedactMCPClientConfig(config).OpenAPIConfig.BaseURL, "ghp_secret123")
}

// TestRedactMCPClientConfig_OpenAPIBaseURLCredentials: a base_url can carry an
// API key in its query or a password, which tool calls send upstream; client
// reads mask them the same way as spec_url.
func TestRedactMCPClientConfig_OpenAPIBaseURLCredentials(t *testing.T) {
	c := &Config{}
	base := "https://svc:pw123@api.example.com/v1?api_key=secret-key"
	config := &schemas.MCPClientConfig{
		ID: "oa-1", Name: "petstore", ConnectionType: schemas.MCPConnectionTypeOpenAPI,
		OpenAPIConfig: &schemas.MCPOpenAPIConfig{Spec: "x", BaseURL: &base},
	}
	got := *c.RedactMCPClientConfig(config).OpenAPIConfig.BaseURL
	assert.NotContains(t, got, "secret-key")
	assert.NotContains(t, got, "pw123")
	assert.Contains(t, got, "api.example.com/v1")
	assert.Equal(t, base, *config.OpenAPIConfig.BaseURL, "live config keeps the real URL")
}
