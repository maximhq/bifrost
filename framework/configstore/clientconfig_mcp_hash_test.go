package configstore

import (
	"github.com/maximhq/bifrost/core/schemas"
	"testing"

	"github.com/bytedance/sonic"

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

// TestGenerateMCPClientHash_EndpointSlug: an omitted slug leaves the hash exactly as before the field was
// hashed (no mass re-sync of stored clients), a declared one drifts it, and spellings that normalise
// to the same slug hash the same.
func TestGenerateMCPClientHash_EndpointSlug(t *testing.T) {
	base := func(slug string) tables.TableMCPClient {
		return tables.TableMCPClient{Name: "client", ConnectionType: "http", AuthType: "headers", EndpointSlug: slug}
	}
	hash := func(slug string) string {
		h, err := GenerateMCPClientHash(base(slug))
		require.NoError(t, err)
		return h
	}

	assert.Equal(t, "4e079178fdf0b46b348e3f1b415589e6bd519a73a280faf9200ad204ad389edf", hash(""),
		"an omitted slug must not move existing stored hashes")
	assert.NotEqual(t, hash(""), hash("custom"), "declaring a slug must drift the hash")
	assert.NotEqual(t, hash("custom"), hash("other"), "changing a declared slug must drift the hash")
	assert.Equal(t, hash("my-slug"), hash("My Slug"), "spellings of the same slug must hash the same")
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

// TestGenerateKeyHash_OAuthSecretsHashByReference pins that rotating the environment value
// behind an env-backed OAuth secret does not change the key's hash. The file itself is
// unchanged, so reconciliation must keep the stored key and its dashboard edits, exactly as
// it does for an env-backed top-level value. A changed literal is still an edit.
func TestGenerateKeyHash_OAuthSecretsHashByReference(t *testing.T) {
	build := func(secret, tokenURL string) schemas.Key {
		return schemas.Key{ID: "k1", Name: "oauth", OAuthKeyConfig: &schemas.OAuthKeyConfig{
			GrantType:    schemas.OAuthGrantClientCredentials,
			TokenURL:     *schemas.NewSecretVar(tokenURL),
			ClientID:     schemas.NewSecretVar("env.HASH_TEST_OAUTH_CLIENT_ID"),
			ClientSecret: schemas.NewSecretVar(secret),
		}}
	}
	t.Setenv("HASH_TEST_OAUTH_CLIENT_ID", "client-v1")
	t.Setenv("HASH_TEST_OAUTH_SECRET", "secret-v1")
	t.Setenv("HASH_TEST_OAUTH_TOKEN_URL", "https://idp-v1.example/token")
	before, err := GenerateKeyHash(build("env.HASH_TEST_OAUTH_SECRET", "env.HASH_TEST_OAUTH_TOKEN_URL"))
	require.NoError(t, err)

	t.Setenv("HASH_TEST_OAUTH_CLIENT_ID", "client-v2")
	t.Setenv("HASH_TEST_OAUTH_SECRET", "secret-v2")
	t.Setenv("HASH_TEST_OAUTH_TOKEN_URL", "https://idp-v2.example/token")
	after, err := GenerateKeyHash(build("env.HASH_TEST_OAUTH_SECRET", "env.HASH_TEST_OAUTH_TOKEN_URL"))
	require.NoError(t, err)
	require.Equal(t, before, after, "rotating env-backed OAuth secrets changed the hash of an unchanged file")

	literalA, err := GenerateKeyHash(build("literal-a", "https://idp.example/token"))
	require.NoError(t, err)
	literalB, err := GenerateKeyHash(build("literal-b", "https://idp.example/token"))
	require.NoError(t, err)
	require.NotEqual(t, literalA, literalB, "a changed literal secret must still change the hash")

	viaRef, err := GenerateKeyHash(build("env.HASH_TEST_OAUTH_SECRET", "https://idp.example/token"))
	require.NoError(t, err)
	viaLiteral, err := GenerateKeyHash(build("secret-v2", "https://idp.example/token"))
	require.NoError(t, err)
	require.NotEqual(t, viaRef, viaLiteral, "switching between a reference and a literal is an edit")
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

// TestLegacyAliasOrderKeyHash_ReproducesTheUnsortedEncoding pins that the legacy hash is
// rebuilt from the exact bytes sonic.Marshal used to feed the hasher before aliases were
// sorted: for two aliases sonic emits one of two orders, and one of them must be ours.
func TestLegacyAliasOrderKeyHash_ReproducesTheUnsortedEncoding(t *testing.T) {
	canonical := "gpt-4o"
	key := schemas.Key{ID: "k1", Name: "aliased", Value: *schemas.NewSecretVar("sk"), Weight: 1, Aliases: schemas.KeyAliases{
		"plain": {ModelID: "upstream-plain"},
		"rich":  {ModelID: "upstream-rich", ModelName: &canonical},
	}}
	unsorted, err := sonic.Marshal(key.Aliases)
	require.NoError(t, err)
	matched := false
	for _, order := range [][]string{{"plain", "rich"}, {"rich", "plain"}} {
		legacy, err := legacyAliasBytes(key.Aliases, order)
		require.NoError(t, err)
		if string(legacy) == string(unsorted) {
			matched = true
		}
	}
	require.True(t, matched, "no legacy ordering reproduces sonic's unsorted bytes %s", unsorted)
}

// TestKeyHashMatchesLegacyAliasOrder pins the upgrade path for keys stored before alias
// hashing was sorted: a config_hash produced under any alias order is recognised as this
// unchanged key, another key's hash is not, a single alias needs no search (both encodings
// coincide), and the search is bounded.
func TestKeyHashMatchesLegacyAliasOrder(t *testing.T) {
	build := func(names ...string) schemas.Key {
		aliases := schemas.KeyAliases{}
		for _, name := range names {
			aliases[name] = schemas.AliasConfig{ModelID: "upstream-" + name}
		}
		return schemas.Key{ID: "k1", Name: "aliased", Value: *schemas.NewSecretVar("sk"), Weight: 1, Aliases: aliases}
	}
	key := build("a", "b", "c", "d")
	sorted, err := GenerateKeyHash(key)
	require.NoError(t, err)

	identity, err := LegacyAliasOrderKeyHash(key, []string{"a", "b", "c", "d"})
	require.NoError(t, err)
	require.Equal(t, sorted, identity, "the sorted order is one of the legacy orders")

	for _, order := range [][]string{{"d", "c", "b", "a"}, {"b", "d", "a", "c"}} {
		legacy, err := LegacyAliasOrderKeyHash(key, order)
		require.NoError(t, err)
		require.NotEqual(t, sorted, legacy, "order %v must hash differently from the sorted form", order)
		ok, err := KeyHashMatchesLegacyAliasOrder(key, legacy)
		require.NoError(t, err)
		require.True(t, ok, "legacy hash for order %v must be recognised", order)
	}

	other, err := GenerateKeyHash(build("a", "b", "c", "x"))
	require.NoError(t, err)
	ok, err := KeyHashMatchesLegacyAliasOrder(key, other)
	require.NoError(t, err)
	require.False(t, ok, "another key's hash must not match")

	edited := key
	edited.Weight = 0.5
	editedLegacy, err := LegacyAliasOrderKeyHash(edited, []string{"d", "c", "b", "a"})
	require.NoError(t, err)
	ok, err = KeyHashMatchesLegacyAliasOrder(key, editedLegacy)
	require.NoError(t, err)
	require.False(t, ok, "a real file edit must still be detected under every legacy order")

	ok, err = KeyHashMatchesLegacyAliasOrder(build("a"), "not-a-hash")
	require.NoError(t, err)
	require.False(t, ok, "a single alias has one encoding, so a mismatch is a real change")

	many := build("a", "b", "c", "d", "e", "f", "g", "h", "i", "j")
	manyLegacy, err := LegacyAliasOrderKeyHash(many, []string{"j", "i", "h", "g", "f", "e", "d", "c", "b", "a"})
	require.NoError(t, err)
	ok, err = KeyHashMatchesLegacyAliasOrder(many, manyLegacy)
	require.NoError(t, err)
	require.False(t, ok, "above maxLegacyAliasPermutations the search is skipped and the file wins")
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

	// spec_url also drifts the hash beside an inline spec: relative servers (and
	// Swagger 2.0 docs without a host) resolve against it, so it picks the upstream.
	inlineWithURL := func(u string) string {
		return hashOf(base(func(c *schemas.MCPOpenAPIConfig) { c.SpecURL = str(u) }))
	}
	assert.NotEqual(t, reference, inlineWithURL("https://a.example/openapi.json"))
	assert.NotEqual(t, inlineWithURL("https://a.example/openapi.json"), inlineWithURL("https://b.example/openapi.json"), "changing only spec_url must drift the hash")

	// An empty base_url means the same as none: use the document's server.
	assert.Equal(t, hashOf(base(func(c *schemas.MCPOpenAPIConfig) { c.BaseURL = nil })), hashOf(base(func(c *schemas.MCPOpenAPIConfig) { c.BaseURL = str("") })), "an empty base_url must hash like a missing one")

	// Fields are framed, so a value cannot impersonate a following field.
	oneCred := func(value string, includeDeprecated bool) string {
		return hashOf(base(func(c *schemas.MCPOpenAPIConfig) {
			c.SecurityCredentials = map[string]schemas.MCPOpenAPICredential{"Z": {Value: schemas.NewSecretVar(value)}}
			c.IncludeDeprecated = includeDeprecated
		}))
	}
	assert.NotEqual(t, oneCred("k", true), oneCred("kopenapi_include_deprecated:true", false), "a credential value must not collide with the include_deprecated flag")

	// A non-openapi client is unaffected by the new block.
	plain := tables.TableMCPClient{Name: "remote", ConnectionType: "http", AuthType: "headers"}
	before, err := GenerateMCPClientHash(plain)
	require.NoError(t, err)
	after, err := GenerateMCPClientHash(plain)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// TestGenerateMCPClientHash_MaxProtocolVersion: an omitted cap leaves stored hashes as they were,
// a declared one drifts the hash so a config.json edit is synced.
func TestGenerateMCPClientHash_MaxProtocolVersion(t *testing.T) {
	hash := func(version string) string {
		h, err := GenerateMCPClientHash(tables.TableMCPClient{Name: "client", ConnectionType: "http", AuthType: "headers", MaxProtocolVersion: version})
		require.NoError(t, err)
		return h
	}
	before, err := GenerateMCPClientHash(tables.TableMCPClient{Name: "client", ConnectionType: "http", AuthType: "headers"})
	require.NoError(t, err)
	assert.Equal(t, before, hash(""))
	assert.NotEqual(t, before, hash(schemas.MCPProtocolVersion20250618))
	assert.NotEqual(t, hash(schemas.MCPProtocolVersion20250618), hash(schemas.MCPMaxProtocolVersionAuto))
}

// Unset allow-lists leave the hash as it was; [] and a list each drift it, so editing them in
// config.json is reconciled.
func TestGenerateMCPClientHash_ExposeLists(t *testing.T) {
	hash := func(prompts, resources schemas.WhiteList) string {
		h, err := GenerateMCPClientHash(tables.TableMCPClient{Name: "client", ConnectionType: "http", AuthType: "headers", PromptsToExpose: prompts, ResourcesToExpose: resources})
		require.NoError(t, err)
		return h
	}
	before, err := GenerateMCPClientHash(tables.TableMCPClient{Name: "client", ConnectionType: "http", AuthType: "headers"})
	require.NoError(t, err)
	assert.Equal(t, before, hash(nil, nil))
	assert.NotEqual(t, before, hash(schemas.WhiteList{}, nil))
	assert.NotEqual(t, hash(schemas.WhiteList{}, nil), hash(schemas.WhiteList{"review"}, nil))
	assert.NotEqual(t, hash(schemas.WhiteList{"review"}, nil), hash(nil, schemas.WhiteList{"review"}), "the two lists hash apart")
	assert.NotEqual(t, hash(schemas.WhiteList{"review,approve"}, nil), hash(schemas.WhiteList{"review", "approve"}, nil), "entry boundaries count")
}

// TestGenerateMCPClientHash_PerUserDiscovery pins the flag's hash contract:
// unset and false contribute nothing (a stored hash from before the field
// existed must keep matching a file entry that still omits it), and true
// drifts the hash so flipping per_user_discovery in config.json triggers
// reconciliation.
func TestGenerateMCPClientHash_PerUserDiscovery(t *testing.T) {
	base := func(perUserDiscovery *bool) tables.TableMCPClient {
		return tables.TableMCPClient{
			Name:             "client",
			ConnectionType:   "http",
			AuthType:         "per_user_headers",
			PerUserDiscovery: perUserDiscovery,
		}
	}

	unsetHash, err := GenerateMCPClientHash(base(nil))
	require.NoError(t, err)
	offHash, err := GenerateMCPClientHash(base(schemas.Ptr(false)))
	require.NoError(t, err)
	onHash, err := GenerateMCPClientHash(base(schemas.Ptr(true)))
	require.NoError(t, err)

	assert.Equal(t, unsetHash, offHash, "an omitted per_user_discovery must hash like false")
	assert.NotEqual(t, offHash, onHash, "flipping per_user_discovery must drift the hash")
}
