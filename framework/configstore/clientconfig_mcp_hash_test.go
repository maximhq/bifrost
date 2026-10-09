package configstore

import (
	"github.com/maximhq/bifrost/core/schemas"
	"testing"

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

// TestGenerateKeyHash_OAuthKeyConfigIsOrderIndependent pins that an unchanged OAuth key hashes
// the same on every call. extra_params is a map; an encoder that walks it in iteration order
// makes the hash differ between restarts, and reconcileProviderKeys then treats the stored key
// as edited in config.json and replaces it, undoing dashboard edits.
func TestGenerateKeyHash_OAuthKeyConfigIsOrderIndependent(t *testing.T) {
	build := func(order []string) schemas.Key {
		params := map[string]string{}
		for _, k := range order {
			params[k] = "v-" + k
		}
		return schemas.Key{
			ID:   "k1",
			Name: "oauth",
			OAuthKeyConfig: &schemas.OAuthKeyConfig{
				GrantType:    schemas.OAuthGrantClientCredentials,
				TokenURL:     *schemas.NewSecretVar("https://idp.example/oauth2/token"),
				ClientID:     schemas.NewSecretVar("id"),
				ClientSecret: schemas.NewSecretVar("secret"),
				ExtraParams:  params,
			},
		}
	}
	want, err := GenerateKeyHash(build([]string{"a", "b", "c", "d", "e"}))
	require.NoError(t, err)
	for i := 0; i < 40; i++ {
		got, err := GenerateKeyHash(build([]string{"e", "d", "c", "b", "a"}))
		require.NoError(t, err)
		require.Equal(t, want, got, "iteration %d: the hash must not depend on map iteration order", i)
	}
}

// TestGenerateKeyHash_AliasesIsOrderIndependent pins the same property for aliases, which is
// also a map: a key whose only change is the encoder's walk order must not be treated as edited.
func TestGenerateKeyHash_AliasesIsOrderIndependent(t *testing.T) {
	build := func(order []string) schemas.Key {
		aliases := schemas.KeyAliases{}
		for _, name := range order {
			aliases[name] = schemas.AliasConfig{ModelID: "upstream-" + name}
		}
		return schemas.Key{ID: "k1", Name: "aliased", Value: *schemas.NewSecretVar("sk"), Aliases: aliases}
	}
	want, err := GenerateKeyHash(build([]string{"a", "b", "c", "d", "e", "f"}))
	require.NoError(t, err)
	for i := 0; i < 40; i++ {
		got, err := GenerateKeyHash(build([]string{"f", "e", "d", "c", "b", "a"}))
		require.NoError(t, err)
		require.Equal(t, want, got, "iteration %d: the hash must not depend on map iteration order", i)
	}
}
