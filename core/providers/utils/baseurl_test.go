package utils

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNormalizeBaseURL covers the provider-constructor normalization of
// NetworkConfig.BaseURL: defaults apply only when nothing is configured, trailing
// slashes are trimmed, an env. reference survives normalization, and the caller's
// original SecretVar is never mutated in place.
func TestNormalizeBaseURL(t *testing.T) {
	t.Run("nil base_url takes the default", func(t *testing.T) {
		nc := schemas.NetworkConfig{}
		NormalizeBaseURL(&nc, "https://api.example.com/")
		assert.Equal(t, "https://api.example.com", nc.BaseURL.GetValue())
		assert.False(t, nc.BaseURL.IsFromSecret())
	})

	t.Run("empty plain base_url takes the default", func(t *testing.T) {
		nc := schemas.NetworkConfig{BaseURL: schemas.NewSecretVar("")}
		NormalizeBaseURL(&nc, "https://api.example.com")
		assert.Equal(t, "https://api.example.com", nc.BaseURL.GetValue())
	})

	t.Run("nil base_url with no default stays nil", func(t *testing.T) {
		nc := schemas.NetworkConfig{}
		NormalizeBaseURL(&nc, "")
		assert.Nil(t, nc.BaseURL)
		assert.Empty(t, nc.BaseURL.GetValue())
	})

	t.Run("configured literal wins over the default and loses trailing slashes", func(t *testing.T) {
		nc := schemas.NetworkConfig{BaseURL: schemas.NewSecretVar("https://custom.example.com/v1///")}
		NormalizeBaseURL(&nc, "https://api.example.com")
		assert.Equal(t, "https://custom.example.com/v1", nc.BaseURL.GetValue())
	})

	t.Run("env. reference keeps its reference after normalization", func(t *testing.T) {
		t.Setenv("BIFROST_TEST_NORMALIZE_BASE_URL", "https://resolved.example.com/")
		nc := schemas.NetworkConfig{BaseURL: schemas.NewSecretVar("env.BIFROST_TEST_NORMALIZE_BASE_URL")}
		NormalizeBaseURL(&nc, "https://api.example.com")
		assert.Equal(t, "https://resolved.example.com", nc.BaseURL.GetValue())
		assert.True(t, nc.BaseURL.IsFromEnv())
		assert.Equal(t, "env.BIFROST_TEST_NORMALIZE_BASE_URL", nc.BaseURL.GetRawRef())
		assert.Equal(t, "env.BIFROST_TEST_NORMALIZE_BASE_URL", schemas.SecretVarAsString(nc.BaseURL))
	})

	t.Run("the caller's SecretVar is cloned, not mutated", func(t *testing.T) {
		original := schemas.NewSecretVar("https://shared.example.com/")
		nc := schemas.NetworkConfig{BaseURL: original}
		NormalizeBaseURL(&nc, "")
		require.NotSame(t, original, nc.BaseURL)
		assert.Equal(t, "https://shared.example.com/", original.GetValue())
		assert.Equal(t, "https://shared.example.com", nc.BaseURL.GetValue())
	})

	t.Run("nil network config is a no-op", func(t *testing.T) {
		NormalizeBaseURL(nil, "https://api.example.com")
	})
}
