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
