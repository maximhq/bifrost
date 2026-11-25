package configstore

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

func TestGenerateVirtualKeyHashCoversRelayAccess(t *testing.T) {
	hash := func(vk tables.TableVirtualKey) string {
		t.Helper()
		h, err := GenerateVirtualKeyHash(vk)
		require.NoError(t, err)
		return h
	}
	base := tables.TableVirtualKey{ID: "vk-1", Name: "prod", Value: *schemas.NewSecretVar("sk-bf-1")}
	plain := hash(base)

	granted := base
	granted.RelayGrants = []tables.TableVirtualKeyRelayGrant{{RelayName: "tavily"}, {RelayName: "exa"}}
	reordered := base
	reordered.RelayGrants = []tables.TableVirtualKeyRelayGrant{{RelayName: "exa"}, {RelayName: "tavily"}}
	require.NotEqual(t, plain, hash(granted), "declared grants are a change")
	require.Equal(t, hash(granted), hash(reordered), "file order does not move the hash")

	cleared := base
	cleared.RelayGrants = []tables.TableVirtualKeyRelayGrant{}
	require.NotEqual(t, hash(granted), hash(cleared), "clearing the grants is a change")
}

// TestGenerateRelayHash pins the relay hash formula and what moves it. The expected value is
// fixed: a change to it means every relay stored with the old hash re-syncs from config.json on
// the next start, overwriting its dashboard edits, so it must be deliberate.
func TestGenerateRelayHash(t *testing.T) {
	hash := func(cfg *schemas.RelayConfig) string {
		t.Helper()
		h, err := GenerateRelayHash(cfg)
		require.NoError(t, err)
		return h
	}
	base := hash(testRelay("tavily"))
	require.Equal(t, "3c41116c68d2f3846670e8bca7b6f0fc2639e1ef1f87515fb939ae6e1c102b3a", base, "the relay hash formula changed")

	// Map encoding is sorted, so iteration order never moves the hash.
	headers := testRelay("tavily")
	headers.ExtraHeaders = map[string]schemas.SecretVar{}
	for _, name := range []string{"X-A", "X-B", "X-C", "X-D", "X-E", "X-F"} {
		headers.ExtraHeaders[name] = *schemas.NewSecretVar("v-" + name)
	}
	headers.Routes[0].Headers = map[string]string{"X-Amz-Target": "Textract.*", "X-Other": "a", "X-Third": "b"}
	first := hash(headers)
	for range 20 {
		require.Equal(t, first, hash(headers), "map order does not move the hash")
	}

	// Every secret hashes its reference, so a rotated env value is not a config change.
	fromEnv := func() *schemas.RelayConfig {
		cfg := testRelay("tavily")
		ref := schemas.NewSecretVar("env.RELAY_HASH_TOKEN")
		cfg.Credential = &schemas.RelayCredential{Value: ref, AWS: &schemas.RelayAWSCredential{
			AccessKey: ref, SecretKey: ref, SessionToken: ref, Region: ref, RoleARN: ref, ExternalID: ref, SessionName: ref,
		}}
		cfg.ExtraHeaders = map[string]schemas.SecretVar{"X-Tenant": *ref}
		cfg.ProxyConfig = &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: ref, Username: ref, Password: ref, CACertPEM: ref}
		return cfg
	}
	t.Setenv("RELAY_HASH_TOKEN", "first-value")
	beforeRotation := hash(fromEnv())
	t.Setenv("RELAY_HASH_TOKEN", "rotated-value")
	require.Equal(t, beforeRotation, hash(fromEnv()), "a rotated env value keeps the hash")
	require.NotEqual(t, base, beforeRotation, "a reference and a literal hash apart")

	changes := map[string]func(*schemas.RelayConfig){
		"description":        func(c *schemas.RelayConfig) { c.Description = "web search" },
		"base_url":           func(c *schemas.RelayConfig) { c.BaseURL = "https://other.example.com" },
		"auth type":          func(c *schemas.RelayConfig) { c.Auth.Type = schemas.RelayAuthHeader },
		"literal credential": func(c *schemas.RelayConfig) { c.Credential.Value = schemas.NewSecretVar("tvly-other") },
		"extra header value": func(c *schemas.RelayConfig) { c.ExtraHeaders["X-Tenant"] = *schemas.NewSecretVar("other") },
		"aws credential": func(c *schemas.RelayConfig) {
			c.Credential.AWS = &schemas.RelayAWSCredential{Region: schemas.NewSecretVar("us-east-1")}
		},
		"proxy": func(c *schemas.RelayConfig) {
			c.ProxyConfig = &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://proxy:3128")}
		},
		"network block":            func(c *schemas.RelayConfig) { c.Network = &schemas.RelayNetworkConfig{TimeoutSeconds: 5} },
		"explicit zero fee":        func(c *schemas.RelayConfig) { c.Usage.CostPerRequest = schemas.Ptr(0.0) },
		"unset fee":                func(c *schemas.RelayConfig) { c.Usage.CostPerRequest = nil },
		"route disabled":           func(c *schemas.RelayConfig) { c.Routes[0].Enabled = schemas.Ptr(false) },
		"route enabled explicitly": func(c *schemas.RelayConfig) { c.Routes[0].Enabled = schemas.Ptr(true) },
		"added route": func(c *schemas.RelayConfig) {
			c.Routes = append(c.Routes, schemas.RelayRouteConfig{Method: "POST", Path: "/extract"})
		},
		"streaming": func(c *schemas.RelayConfig) {
			c.Streaming = &schemas.RelayStreamingConfig{Paths: []string{"/stream/**"}}
		},
		"skip_route_check":     func(c *schemas.RelayConfig) { c.SkipRouteCheck = true },
		"ignore_upstream_cost": func(c *schemas.RelayConfig) { c.IgnoreUpstreamCost = true },
	}
	for name, change := range changes {
		cfg := testRelay("tavily")
		change(cfg)
		require.NotEqual(t, base, hash(cfg), "%s is a change", name)
	}

	// Routes keep their order: the first match wins.
	ordered := testRelay("tavily")
	ordered.Routes = append(ordered.Routes, schemas.RelayRouteConfig{Method: "POST", Path: "/extract"})
	reordered := testRelay("tavily")
	reordered.Routes = append([]schemas.RelayRouteConfig{{Method: "POST", Path: "/extract"}}, reordered.Routes...)
	require.NotEqual(t, hash(ordered), hash(reordered), "reordering routes is a change")
}

// Telemetry values are hashed length-prefixed: concatenated, a drop attribute or header that
// contains the next field's text hashed like two shorter values, so reconciliation missed a
// real change.
func TestGenerateClientConfigHash_TelemetryValuesAreUnambiguous(t *testing.T) {
	hashOf := func(telemetry *tables.ClientGatewayTelemetryConfig) string {
		t.Helper()
		c := ClientConfig{EnableLogging: new(true), InitialPoolSize: 100, LogRetentionDays: 30, MaxRequestBodySizeMB: 50, EnforceVKObjectOwnership: true,
			OAuth2ServerConfig: &tables.OAuth2ServerConfig{Gateways: &tables.ClientGatewaysConfig{ClaudeCode: &tables.ClaudeCodeGatewayConfig{Enabled: true, Telemetry: telemetry}}}}
		h, err := c.GenerateClientConfigHash()
		require.NoError(t, err)
		return h
	}
	dest := func(headers map[string]string) []tables.ClientGatewayTelemetryDestination {
		d := tables.ClientGatewayTelemetryDestination{Endpoint: schemas.NewSecretVar("https://otel.example.com"), Headers: map[string]*schemas.SecretVar{}}
		for k, v := range headers {
			d.Headers[k] = schemas.NewSecretVar(v)
		}
		return []tables.ClientGatewayTelemetryDestination{d}
	}

	require.NotEqual(t,
		hashOf(&tables.ClientGatewayTelemetryConfig{Enabled: true, Destinations: dest(nil), DropAttributes: []string{"prompt", "completion"}}),
		hashOf(&tables.ClientGatewayTelemetryConfig{Enabled: true, Destinations: dest(nil), DropAttributes: []string{"promptgateways.claude_code.telemetry.drop:completion"}}),
		"two drop attributes must not hash like one that contains the separator")
	require.NotEqual(t,
		hashOf(&tables.ClientGatewayTelemetryConfig{Enabled: true, Destinations: dest(map[string]string{"a": "val:x", "b": "val:y"})}),
		hashOf(&tables.ClientGatewayTelemetryConfig{Enabled: true, Destinations: dest(map[string]string{"a": "val:xgateways.claude_code.telemetry.header:b=val:y"})}),
		"two headers must not hash like one whose value contains the next header")
}

// TestGenerateClientConfigHash_GatewayAfterRedirectURIsIsUnambiguous pins that a redirect URI
// ending in the gateway marker does not hash like that URI plus a real gateway block.
func TestGenerateClientConfigHash_GatewayAfterRedirectURIsIsUnambiguous(t *testing.T) {
	hashOf := func(uri string, gateways *tables.ClientGatewaysConfig) string {
		t.Helper()
		c := ClientConfig{EnableLogging: new(true), InitialPoolSize: 100, LogRetentionDays: 30, MaxRequestBodySizeMB: 50, EnforceVKObjectOwnership: true,
			OAuth2ServerConfig: &tables.OAuth2ServerConfig{AllowedRedirectURIs: []string{uri}, Gateways: gateways}}
		h, err := c.GenerateClientConfigHash()
		require.NoError(t, err)
		return h
	}
	require.NotEqual(t,
		hashOf("https://example.test/callback", &tables.ClientGatewaysConfig{ClaudeCode: &tables.ClaudeCodeGatewayConfig{Enabled: true}}),
		hashOf("https://example.test/callbackgateways.claude_code.enabled:true", nil))
}

// bifrost_url and bifrost_mcp_allow_writes are hashed so that where one ends is
// unambiguous: written as plain "prefix + value", a URL whose path ends in the
// next field's text hashed the same as a shorter URL with that field set. Both
// unset keeps the bytes configs had before the fields existed.
func TestGenerateClientConfigHash_BifrostURLAndAllowWritesAreUnambiguous(t *testing.T) {
	base := func() ClientConfig {
		return ClientConfig{EnableLogging: new(true), InitialPoolSize: 100, LogRetentionDays: 30, MaxRequestBodySizeMB: 50, EnforceVKObjectOwnership: true}
	}

	short := base()
	short.BifrostURL = schemas.NewSecretVar("https://gw.example.com/a")
	short.BifrostMCPAllowWrites = new(true)
	long := base()
	long.BifrostURL = schemas.NewSecretVar("https://gw.example.com/abifrostMCPAllowWrites:true")
	shortHash, err := short.GenerateClientConfigHash()
	require.NoError(t, err)
	longHash, err := long.GenerateClientConfigHash()
	require.NoError(t, err)
	require.NotEqual(t, shortHash, longHash, "a URL ending in the next field's text must not hash like a shorter URL with that field set")

	unset := base()
	unsetHash, err := unset.GenerateClientConfigHash()
	require.NoError(t, err)
	require.Equal(t, "66ee28323ac8b742b63ca063f251b2f585209a32980345cf26dd0b81298ddf4c", unsetHash, "both unset keeps the hash configs had before the fields existed")
}

// The entity access profile mode is part of what decides whether a changed config.json replaces the
// stored client config: intersect must change the hash, while unset and an explicit union must hash
// the same,
// so an upgrade that starts writing the default does not churn every deployment's hash.
func TestGenerateClientConfigHashCoversEntityAccessProfileMode(t *testing.T) {
	hash := func(mode tables.EntityAccessProfileMode) string {
		t.Helper()
		h, err := (&ClientConfig{EntityAccessProfileMode: mode}).GenerateClientConfigHash()
		require.NoError(t, err)
		return h
	}
	unset := hash("")
	require.Equal(t, unset, hash(tables.EntityAccessProfileModeUnion), "union is the default and hashes as unset")
	require.NotEqual(t, unset, hash(tables.EntityAccessProfileModeIntersect), "intersect is a change")
}

// Turning send_back_cost on is a config change; leaving it off must hash as it
// did before the field existed, so an upgrade does not churn stored hashes.
func TestGenerateClientConfigHashCoversSendBackCost(t *testing.T) {
	before, err := (&ClientConfig{}).GenerateClientConfigHash()
	require.NoError(t, err)
	off, err := (&ClientConfig{SendBackCost: false}).GenerateClientConfigHash()
	require.NoError(t, err)
	on, err := (&ClientConfig{SendBackCost: true}).GenerateClientConfigHash()
	require.NoError(t, err)
	require.Equal(t, before, off, "off is the default and hashes as unset")
	require.NotEqual(t, off, on, "on is a change")
}
