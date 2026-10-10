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
		assert.Equal(t, "env.BIFROST_TEST_LOGGABLE_BASE_URL/batches/123:cancel?alt=media", got)
		assert.NotContains(t, got, "secret-host")
		// The reference resolved to a path as well as a host, so the path must not
		// survive either - only the suffix the request itself added.
		assert.NotContains(t, got, "/v1beta")
	})

	t.Run("a resolved path and query are stripped, not just the host", func(t *testing.T) {
		t.Setenv("BIFROST_TEST_LOGGABLE_BASE_URL", "https://secret-host.example.com/tenants/acme/v1?token=abc")
		base := schemas.NewSecretVar("env.BIFROST_TEST_LOGGABLE_BASE_URL")
		got := LoggableURL(base, "https://secret-host.example.com/tenants/acme/v1?token=abc/models")
		assert.Equal(t, "env.BIFROST_TEST_LOGGABLE_BASE_URL/models", got)
		assert.NotContains(t, got, "acme")
		assert.NotContains(t, got, "token=abc")
	})

	t.Run("a rewritten URL falls back to the reference alone", func(t *testing.T) {
		t.Setenv("BIFROST_TEST_LOGGABLE_BASE_URL", "https://secret-host.example.com/v1beta")
		base := schemas.NewSecretVar("env.BIFROST_TEST_LOGGABLE_BASE_URL")
		got := LoggableURL(base, "https://secret-host.example.com/download/v1beta/files/abc:download?alt=media")
		assert.Equal(t, "env.BIFROST_TEST_LOGGABLE_BASE_URL", got)
	})

	t.Run("a rewritten URL cannot leak a secret path segment", func(t *testing.T) {
		// Gemini splices "/download" in front of the version segment, so a base that
		// carries a tenant ahead of it stops being a prefix of the request URL. The
		// tenant is still in that URL, which is why the fallback cannot echo any of it.
		t.Setenv("BIFROST_TEST_LOGGABLE_BASE_URL", "https://secret-host.example.com/tenants/acme/v1beta")
		base := schemas.NewSecretVar("env.BIFROST_TEST_LOGGABLE_BASE_URL")
		got := LoggableURL(base, "https://secret-host.example.com/tenants/acme/download/v1beta/files/abc:download?alt=media")
		assert.Equal(t, "env.BIFROST_TEST_LOGGABLE_BASE_URL", got)
		assert.NotContains(t, got, "acme")
		assert.NotContains(t, got, "secret-host")
	})

	t.Run("unparseable URL under a reference falls back to the reference alone", func(t *testing.T) {
		t.Setenv("BIFROST_TEST_LOGGABLE_BASE_URL", "https://secret-host.example.com")
		base := schemas.NewSecretVar("env.BIFROST_TEST_LOGGABLE_BASE_URL")
		assert.Equal(t, "env.BIFROST_TEST_LOGGABLE_BASE_URL", LoggableURL(base, "://not a url"))
	})
}

// TestBuildPassthroughURLFromSecretKeepsResolvedSecretsOutOfErrors covers the
// disclosure boundary on the passthrough routes: BuildPassthroughURL quotes the
// base URL in its error, and the passthrough handlers return that to the caller
// as a 400. A literal base_url is operator-visible configuration and stays
// readable; a value resolved from an env./vault. reference must not appear.
func TestBuildPassthroughURLFromSecretKeepsResolvedSecretsOutOfErrors(t *testing.T) {
	t.Run("a literal keeps its diagnostic", func(t *testing.T) {
		_, err := BuildPassthroughURLFromSecret(schemas.NewSecretVar("not-a-url"), "/v1/x", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not-a-url", "a literal base_url should stay readable in the error")
	})

	t.Run("a resolved reference is named, not echoed", func(t *testing.T) {
		t.Setenv("BIFROST_TEST_PASSTHROUGH_BASE_URL", "not-a-url-either")
		_, err := BuildPassthroughURLFromSecret(
			schemas.NewSecretVar("env.BIFROST_TEST_PASSTHROUGH_BASE_URL"), "/v1/x", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "env.BIFROST_TEST_PASSTHROUGH_BASE_URL")
		assert.NotContains(t, err.Error(), "not-a-url-either", "the resolved value must not reach the caller")
	})

	t.Run("a conformant reference still builds", func(t *testing.T) {
		t.Setenv("BIFROST_TEST_PASSTHROUGH_BASE_URL", "https://api.example.com")
		got, err := BuildPassthroughURLFromSecret(
			schemas.NewSecretVar("env.BIFROST_TEST_PASSTHROUGH_BASE_URL"), "/v1/x", "a=1")
		require.NoError(t, err)
		assert.Equal(t, "https://api.example.com/v1/x?a=1", got)
	})
}
