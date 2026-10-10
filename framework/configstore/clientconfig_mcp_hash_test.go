package configstore

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerateMCPClientHash_NeedsSessionStickiness_NilVsFalseVsTrue pins the
// three-way distinction the config.json reconciliation-mismatch fix depends
// on:
//
//   - nil must hash identically to how the field hashed before it existed at
//     all (zero contribution) — this is what lets a pre-existing config.json
//     entry that still omits the field coincidentally match a migration-era
//     stale DB hash, so the migration's true backfill isn't clobbered by a
//     forced resync on the next restart.
//   - true and false must each hash distinctly from nil AND from each other —
//     an admin's explicit `needs_session_stickiness: false` in config.json
//     must drift the hash so the mismatch is actually detected, instead of
//     silently colliding with a stale hash and never taking effect.
func TestGenerateMCPClientHash_NeedsSessionStickiness_NilVsFalseVsTrue(t *testing.T) {
	base := func(stickiness *bool) tables.TableMCPClient {
		return tables.TableMCPClient{
			Name:                   "client",
			ConnectionType:         "http",
			AuthType:               "headers",
			NeedsSessionStickiness: stickiness,
		}
	}
	truePtr := true
	falsePtr := false

	nilHash, err := GenerateMCPClientHash(base(nil))
	require.NoError(t, err)
	trueHash, err := GenerateMCPClientHash(base(&truePtr))
	require.NoError(t, err)
	falseHash, err := GenerateMCPClientHash(base(&falsePtr))
	require.NoError(t, err)

	assert.NotEqual(t, nilHash, trueHash, "true must hash differently from nil")
	assert.NotEqual(t, nilHash, falseHash, "false must hash differently from nil — otherwise a genuine config.json edit to false is silently ignored")
	assert.NotEqual(t, trueHash, falseHash, "true and false must hash differently from each other")
}

// TestGenerateMCPClientHash_OpenAPIConfig pins which openapi_config fields take
// part in config.json reconciliation: anything the synthesized server is built
// from drifts the hash, server-computed metadata does not.
func TestGenerateMCPClientHash_OpenAPIConfig(t *testing.T) {
	str := func(s string) *string { return &s }
	base := func(mutate func(c *schemas.MCPOpenAPIConfig)) tables.TableMCPClient {
		cfg := &schemas.MCPOpenAPIConfig{
			Spec:    "openapi: 3.0.0\npaths: {}",
			BaseURL: str("https://api.example.com"),
			SecurityCredentials: map[string]schemas.MCPOpenAPICredential{
				"ApiKey": {Value: schemas.NewSecretVar("env.API_KEY")},
				"Basic":  {Username: schemas.NewSecretVar("u"), Password: schemas.NewSecretVar("p")},
			},
			SpecHash:       "h1",
			OperationCount: 3,
		}
		if mutate != nil {
			mutate(cfg)
		}
		return tables.TableMCPClient{Name: "petstore", ConnectionType: "openapi", AuthType: "none", OpenAPIConfig: cfg}
	}
	hashOf := func(c tables.TableMCPClient) string {
		h, err := GenerateMCPClientHash(c)
		require.NoError(t, err)
		return h
	}
	reference := hashOf(base(nil))

	assert.Equal(t, reference, hashOf(base(nil)), "deterministic")
	assert.Equal(t, reference, hashOf(base(func(c *schemas.MCPOpenAPIConfig) { c.SpecHash = "h2"; c.OperationCount = 9; c.SpecTitle = "x" })), "metadata does not drift the hash")

	for name, mutate := range map[string]func(c *schemas.MCPOpenAPIConfig){
		"spec":             func(c *schemas.MCPOpenAPIConfig) { c.Spec += "\n# edited" },
		"base url":         func(c *schemas.MCPOpenAPIConfig) { c.BaseURL = str("https://other.example.com") },
		"base url cleared": func(c *schemas.MCPOpenAPIConfig) { c.BaseURL = nil },
		"credential ref": func(c *schemas.MCPOpenAPIConfig) {
			c.SecurityCredentials["ApiKey"] = schemas.MCPOpenAPICredential{Value: schemas.NewSecretVar("env.OTHER")}
		},
		"credential value": func(c *schemas.MCPOpenAPIConfig) {
			c.SecurityCredentials["Basic"] = schemas.MCPOpenAPICredential{Username: schemas.NewSecretVar("u"), Password: schemas.NewSecretVar("p2")}
		},
		"credential added": func(c *schemas.MCPOpenAPIConfig) {
			c.SecurityCredentials["Bearer"] = schemas.MCPOpenAPICredential{Value: schemas.NewSecretVar("t")}
		},
		"include deprecated": func(c *schemas.MCPOpenAPIConfig) { c.IncludeDeprecated = true },
		"response cap":       func(c *schemas.MCPOpenAPIConfig) { c.MaxResponseBytes = 1024 },
	} {
		assert.NotEqual(t, reference, hashOf(base(mutate)), "%s must drift the hash", name)
	}

	// Spec given by reference instead of content hashes by that reference.
	fileOnly := hashOf(base(func(c *schemas.MCPOpenAPIConfig) { c.Spec = ""; c.SpecFile = str("specs/petstore.yaml") }))
	urlOnly := hashOf(base(func(c *schemas.MCPOpenAPIConfig) { c.Spec = ""; c.SpecURL = str("https://x/openapi.json") }))
	assert.NotEqual(t, reference, fileOnly)
	assert.NotEqual(t, fileOnly, urlOnly)

	// A non-openapi client is unaffected by the new block.
	plain := tables.TableMCPClient{Name: "remote", ConnectionType: "http", AuthType: "headers"}
	before, err := GenerateMCPClientHash(plain)
	require.NoError(t, err)
	after, err := GenerateMCPClientHash(plain)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}
