package tables

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClientGatewayTelemetryConfig_Validate pins what a switched-on telemetry
// block must hold: at least one destination, an http(s) endpoint, a value for every
// header (plain or an env./vault. reference), and no empty attribute keys. A
// switched-off block is not checked.
func TestClientGatewayTelemetryConfig_Validate(t *testing.T) {
	t.Setenv("BIFROST_TEST_OTEL_TOKEN", "secret")
	dest := func(endpoint string, headers map[string]string) ClientGatewayTelemetryDestination {
		d := ClientGatewayTelemetryDestination{Endpoint: schemas.NewSecretVar(endpoint)}
		if headers != nil {
			d.Headers = map[string]*schemas.SecretVar{}
			for k, v := range headers {
				d.Headers[k] = schemas.NewSecretVar(v)
			}
		}
		return d
	}
	for name, tc := range map[string]struct {
		cfg     *ClientGatewayTelemetryConfig
		wantErr string
	}{
		"nil":                        {cfg: nil},
		"disabled, empty":            {cfg: &ClientGatewayTelemetryConfig{Enabled: false}},
		"valid":                      {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{dest("https://otel.example.com", map[string]string{"authorization": "env.BIFROST_TEST_OTEL_TOKEN"})}, DropAttributes: []string{"prompt"}}},
		"env endpoint":               {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{dest("env.BIFROST_TEST_OTEL_ENDPOINT", nil)}}},
		"no destinations":            {cfg: &ClientGatewayTelemetryConfig{Enabled: true}, wantErr: "destinations must list at least one collector"},
		"missing endpoint":           {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{{}}}, wantErr: "endpoint is required"},
		"non-http endpoint":          {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{dest("ftp://otel.example.com", nil)}}, wantErr: "must be an http or https URL"},
		"plain-text header":          {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{dest("https://otel.example.com", map[string]string{"authorization": "Bearer abc"})}}},
		"duplicate endpoint, paused": {cfg: &ClientGatewayTelemetryConfig{Enabled: false, Destinations: []ClientGatewayTelemetryDestination{dest("https://otel.example.com", nil), dest("https://otel.example.com", nil)}}, wantErr: "repeats destinations[0]"},
		"duplicate endpoint":         {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{dest("https://otel.example.com", nil), dest("https://otel.example.com", nil)}}, wantErr: "repeats destinations[0]"},
		"empty header value":         {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{dest("https://otel.example.com", map[string]string{"authorization": " "})}}, wantErr: "needs a value"},
		"empty header name":          {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{dest("https://otel.example.com", map[string]string{" ": "env.X"})}}, wantErr: "empty header name"},
		"empty drop key":             {cfg: &ClientGatewayTelemetryConfig{Enabled: true, Destinations: []ClientGatewayTelemetryDestination{dest("https://otel.example.com", nil)}, DropAttributes: []string{""}}, wantErr: "empty attribute key"},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.cfg.Validate("telemetry")
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestClientGatewayTelemetryConfig_DropDuplicateDestinations pins that startup drops every entry
// of a repeated collector, matching endpoints the way Validate does.
func TestClientGatewayTelemetryConfig_DropDuplicateDestinations(t *testing.T) {
	t.Setenv("BIFROST_TEST_OTEL_ENDPOINT", "https://a.example.com")
	dest := func(endpoint string) ClientGatewayTelemetryDestination {
		return ClientGatewayTelemetryDestination{Endpoint: schemas.NewSecretVar(endpoint)}
	}
	cfg := &ClientGatewayTelemetryConfig{Destinations: []ClientGatewayTelemetryDestination{
		dest("https://a.example.com"), dest("env.BIFROST_TEST_OTEL_ENDPOINT"), dest("https://a.example.com "), {}, dest("env.BIFROST_TEST_OTEL_ENDPOINT"), {},
	}}
	assert.Equal(t, []int{0, 1, 2, 4}, cfg.DropDuplicateDestinations())
	require.Len(t, cfg.Destinations, 2, "only the two entries without an endpoint are left")
	assert.NoError(t, cfg.Validate("telemetry"), "what is left passes the duplicate check")
	assert.Nil(t, cfg.DropDuplicateDestinations(), "nothing more to drop")
	assert.Nil(t, (*ClientGatewayTelemetryConfig)(nil).DropDuplicateDestinations())
}

// TestOAuth2ServerConfig_StorageJSONKeepsReferencesOnly pins that the stored config keeps an
// env. or vault. reference without its resolved value, as MCP client headers are stored, so
// no resolved secret is written to config_client; a plain value and managed settings are kept
// as they are.
func TestOAuth2ServerConfig_StorageJSONKeepsReferencesOnly(t *testing.T) {
	t.Setenv("BIFROST_TEST_OTEL_TOKEN", "resolved-env-secret")
	t.Setenv("BIFROST_TEST_ISSUER", "https://issuer.example.com")
	var cfg OAuth2ServerConfig
	require.NoError(t, json.Unmarshal([]byte(`{"issuer_url":"env.BIFROST_TEST_ISSUER","gateways":{"claude_code":{"enabled":true,"managed_settings":{"env":{"ref":"env.POLICY_REF","value":"keep-me"}},"telemetry":{"enabled":true,"destinations":[{"endpoint":"https://otel.example.com","headers":{"authorization":"env.BIFROST_TEST_OTEL_TOKEN","x-team":"plain-team"}}]}}}}`), &cfg))
	stored, err := cfg.StorageJSON()
	require.NoError(t, err)
	assert.Contains(t, stored, `"value":"keep-me"`, "managed settings are written as is, even when they look like a secret")
	assert.NotContains(t, stored, "resolved-env-secret")
	assert.NotContains(t, stored, "https://issuer.example.com")
	assert.Contains(t, stored, `"ref":"env.BIFROST_TEST_OTEL_TOKEN"`)
	assert.Contains(t, stored, "plain-team")
	assert.Equal(t, "resolved-env-secret", cfg.ClaudeCodeGateway().Telemetry.Destinations[0].Headers["authorization"].GetValue(), "the live config keeps its resolved value")

	var reloaded OAuth2ServerConfig
	require.NoError(t, json.Unmarshal([]byte(stored), &reloaded))
	assert.Equal(t, "resolved-env-secret", reloaded.ClaudeCodeGateway().Telemetry.Destinations[0].Headers["authorization"].GetValue())
	assert.Equal(t, "https://issuer.example.com", reloaded.IssuerURL.GetValue())
}
