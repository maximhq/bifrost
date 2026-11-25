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

// TestGenerateKeyHash_VertexBatchGCSConfig pins that a Vertex key's bucket list changes the key
// hash, so a config.json edit to the list is detected, while a key without one keeps its hash.
func TestGenerateKeyHash_VertexBatchGCSConfig(t *testing.T) {
	newKey := func(buckets ...schemas.GCSBucketConfig) schemas.Key {
		key := schemas.Key{Name: "k", Value: *schemas.NewSecretVar(""), Weight: 1.0, VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID: *schemas.NewSecretVar("my-gcp-project"),
			Region:    *schemas.NewSecretVar("us-central1"),
		}}
		if len(buckets) > 0 {
			key.VertexKeyConfig.BatchGCSConfig = &schemas.BatchGCSConfig{Buckets: buckets}
		}
		return key
	}
	hashOf := func(key schemas.Key) string {
		t.Helper()
		hash, err := GenerateKeyHash(key)
		require.NoError(t, err)
		return hash
	}

	// The hash this key had before a bucket list could be set, taken from the code as it was
	// then. A key that has no list must keep it, or an upgrade would read every stored Vertex
	// key as edited in config.json.
	const hashBeforeBucketLists = "798e7b76768f6f44e02575a83b65e6cd233574b19e08558b1a05b74110d0e851"
	baseHash := hashOf(newKey())
	assert.Equal(t, hashBeforeBucketLists, baseHash, "a key without a bucket list must keep the hash it had before the list existed")
	assert.Equal(t, baseHash, hashOf(newKey()), "a key without a bucket list must hash the same every time")

	listedHash := hashOf(newKey(schemas.GCSBucketConfig{BucketName: "team-files"}))
	assert.NotEqual(t, baseHash, listedHash, "adding a bucket list must change the hash")
	assert.NotEqual(t, listedHash, hashOf(newKey(schemas.GCSBucketConfig{BucketName: "team-batch"})), "changing a bucket name must change the hash")
	assert.NotEqual(t, listedHash, hashOf(newKey(schemas.GCSBucketConfig{BucketName: "team-files", Prefix: "jobs/"})), "changing a prefix must change the hash")
}
