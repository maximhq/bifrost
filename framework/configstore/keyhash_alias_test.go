package configstore

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerateKeyHash_LegacyAliasesPreserveByteShape proves that an
// unenriched alias (only ModelID set) marshals into the hasher as the legacy
// {"k":"v"} string-valued shape, which is what keeps config_hash byte-stable
// across the refactor. If MarshalJSON ever stops emitting the legacy form for
// ModelID-only entries, this test fires.
//
// Strategy: hash the same Key with two equivalent alias representations — the
// rich KeyAliases{"k": {ModelID: "v"}} and (a hand-rolled JSON for) the
// legacy "k": "v" shape — and confirm both feed identical bytes into the
// hasher by checking that the marshaled outputs match. We don't recompute the
// full SHA256 since GenerateKeyHash composes many field bytes; the marshaling
// stability of the alias map alone is the regression-prone surface.
func TestGenerateKeyHash_LegacyAliasesPreserveByteShape(t *testing.T) {
	key := schemas.Key{
		Name:    "openai-key",
		Value:   *schemas.NewSecretVar("sk-test"),
		Weight:  1.0,
		Aliases: schemas.KeyAliases{"best-model": {ModelID: "gpt-4o-deployment"}},
	}

	gotMarshal, err := sonic.Marshal(key.Aliases)
	require.NoError(t, err)
	assert.Equal(t,
		`{"best-model":"gpt-4o-deployment"}`,
		string(gotMarshal),
		"unenriched alias should marshal to the legacy string-valued wire shape; otherwise GenerateKeyHash drifts from pre-refactor rows",
	)

	// And GenerateKeyHash itself runs cleanly with the new types.
	hash, err := GenerateKeyHash(key)
	require.NoError(t, err)
	assert.NotEmpty(t, hash)
}

// TestGenerateKeyHash_RichAliasesProduceDifferentHash sanity-checks the other
// side: enriching an alias with ModelName/Family/etc. *does* change the hash,
// so genuine config changes are still detected.
func TestGenerateKeyHash_RichAliasesProduceDifferentHash(t *testing.T) {
	canonical := "gpt-4o"
	family := schemas.ModelFamilyOpenAI

	legacy := schemas.Key{
		Name:    "k",
		Value:   *schemas.NewSecretVar("sk"),
		Weight:  1.0,
		Aliases: schemas.KeyAliases{"x": {ModelID: "y"}},
	}
	rich := schemas.Key{
		Name:   "k",
		Value:  *schemas.NewSecretVar("sk"),
		Weight: 1.0,
		Aliases: schemas.KeyAliases{"x": {
			ModelID:     "y",
			ModelName:   &canonical,
			ModelFamily: &family,
		}},
	}

	legacyHash, err := GenerateKeyHash(legacy)
	require.NoError(t, err)
	richHash, err := GenerateKeyHash(rich)
	require.NoError(t, err)

	assert.NotEqual(t, legacyHash, richHash, "enriching an alias must change the key hash so config diffs are detected")
}

// TestGenerateKeyHash_ServiceTier pins that a key or alias service_tier changes the key hash, so a
// config.json edit is detected, while a key without one keeps its existing hash.
func TestGenerateKeyHash_ServiceTier(t *testing.T) {
	priority := schemas.BifrostServiceTierPriority
	flex := schemas.BifrostServiceTierFlex
	base := schemas.Key{Name: "k", Value: *schemas.NewSecretVar("sk"), Weight: 1.0, Aliases: schemas.KeyAliases{"x": {ModelID: "y"}}}

	baseHash, err := GenerateKeyHash(base)
	require.NoError(t, err)

	keyTier := base
	keyTier.ServiceTier = &priority
	keyTierHash, err := GenerateKeyHash(keyTier)
	require.NoError(t, err)
	assert.NotEqual(t, baseHash, keyTierHash, "a key service_tier must change the hash")

	keyTier.ServiceTier = &flex
	flexHash, err := GenerateKeyHash(keyTier)
	require.NoError(t, err)
	assert.NotEqual(t, keyTierHash, flexHash, "changing the key service_tier must change the hash")

	aliasTier := base
	aliasTier.Aliases = schemas.KeyAliases{"x": {ModelID: "y", ServiceTier: &flex}}
	aliasMarshal, err := sonic.Marshal(aliasTier.Aliases)
	require.NoError(t, err)
	assert.Equal(t, `{"x":{"model_id":"y","service_tier":"flex"}}`, string(aliasMarshal), "an alias carrying only a tier must not collapse to the legacy string shape")
	aliasTierHash, err := GenerateKeyHash(aliasTier)
	require.NoError(t, err)
	assert.NotEqual(t, baseHash, aliasTierHash, "an alias service_tier must change the hash")
}
