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

// TestLoggableURL covers the log-safe rendering of provider request URLs: a literal
// base_url is logged verbatim, while a reference-resolved base_url has its resolved
// scheme and host replaced by the reference.
func TestLoggableURL(t *testing.T) {
	t.Run("literal base_url is logged as-is", func(t *testing.T) {
		base := schemas.NewSecretVar("https://api.example.com/v1beta")
		assert.Equal(t, "https://api.example.com/v1beta/batches/123:cancel", LoggableURL(base, "https://api.example.com/v1beta/batches/123:cancel"))
	})

	t.Run("nil base_url is logged as-is", func(t *testing.T) {
		assert.Equal(t, "https://api.example.com/x", LoggableURL(nil, "https://api.example.com/x"))
	})

	t.Run("reference-resolved base_url hides the resolved host", func(t *testing.T) {
		t.Setenv("BIFROST_TEST_LOGGABLE_BASE_URL", "https://secret-host.example.com/v1beta")
		base := schemas.NewSecretVar("env.BIFROST_TEST_LOGGABLE_BASE_URL")
		got := LoggableURL(base, "https://secret-host.example.com/v1beta/batches/123:cancel?alt=media")
		assert.Equal(t, "env.BIFROST_TEST_LOGGABLE_BASE_URL/v1beta/batches/123:cancel?alt=media", got)
		assert.NotContains(t, got, "secret-host")
	})

	t.Run("reference-resolved base_url hides a rewritten host too", func(t *testing.T) {
		t.Setenv("BIFROST_TEST_LOGGABLE_BASE_URL", "https://secret-host.example.com/v1beta")
		base := schemas.NewSecretVar("env.BIFROST_TEST_LOGGABLE_BASE_URL")
		got := LoggableURL(base, "https://secret-host.example.com/download/v1beta/files/abc:download?alt=media")
		assert.Equal(t, "env.BIFROST_TEST_LOGGABLE_BASE_URL/download/v1beta/files/abc:download?alt=media", got)
	})

	t.Run("unparseable URL under a reference falls back to the reference alone", func(t *testing.T) {
		t.Setenv("BIFROST_TEST_LOGGABLE_BASE_URL", "https://secret-host.example.com")
		base := schemas.NewSecretVar("env.BIFROST_TEST_LOGGABLE_BASE_URL")
		assert.Equal(t, "env.BIFROST_TEST_LOGGABLE_BASE_URL", LoggableURL(base, "://not a url"))
	})
}
