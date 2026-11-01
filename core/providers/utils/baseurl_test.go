package utils

import (
	"encoding/json"
	"errors"
	"net"
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

	t.Run("a rejected caller path keeps its own diagnostic", func(t *testing.T) {
		t.Setenv("BIFROST_TEST_PASSTHROUGH_BASE_URL", "https://secret-host.example.com")
		_, err := BuildPassthroughURLFromSecret(
			schemas.NewSecretVar("env.BIFROST_TEST_PASSTHROUGH_BASE_URL"), "//other-host/x", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `invalid passthrough path "//other-host/x"`, "the caller's mistake must not read as a base_url failure")
		assert.NotContains(t, err.Error(), "secret-host")
	})
}

// TestRedactBaseURL covers the free-text redaction used for errors that quote the
// endpoint: every form of a reference-resolved base_url is replaced by the reference,
// and a literal base_url is left readable.
func TestRedactBaseURL(t *testing.T) {
	const ref = "env.BIFROST_TEST_REDACT_BASE_URL"

	t.Run("literal and nil base_url are left as-is", func(t *testing.T) {
		text := "dial tcp: lookup api.example.com: no such host"
		assert.Equal(t, text, RedactBaseURL(schemas.NewSecretVar("https://api.example.com"), text))
		assert.Equal(t, text, RedactBaseURL(nil, text))
	})

	t.Run("every form of the resolved endpoint becomes the reference", func(t *testing.T) {
		t.Setenv("BIFROST_TEST_REDACT_BASE_URL", "https://secret-host.internal:8443/tenants/acme/")
		base := schemas.NewSecretVar(ref)
		for _, tc := range []struct{ text, want string }{
			{`Post "https://secret-host.internal:8443/tenants/acme/v1/chat": EOF`, `Post "` + ref + `/v1/chat": EOF`},
			{"failed to dial upstream websocket wss://secret-host.internal:8443/tenants/acme/v1/realtime?model=m: refused", "failed to dial upstream websocket wss://" + ref + "/v1/realtime?model=m: refused"},
			{"dial tcp secret-host.internal:8443: connect: connection refused", "dial tcp " + ref + ": connect: connection refused"},
			{"lookup secret-host.internal on 127.0.0.53:53: no such host", "lookup " + ref + " on 127.0.0.53:53: no such host"},
		} {
			got := RedactBaseURL(base, tc.text)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "secret-host")
			assert.NotContains(t, got, "acme")
		}
	})

	t.Run("an unresolved reference has nothing to redact", func(t *testing.T) {
		base := schemas.NewSecretVar("env.BIFROST_TEST_REDACT_BASE_URL_UNSET")
		assert.Equal(t, "connection refused", RedactBaseURL(base, "connection refused"))
	})
}

// TestRedactBaseURLError covers the BifrostError form: the message and the carried
// error's text are redacted, and the carried error still unwraps for errors.As.
func TestRedactBaseURLError(t *testing.T) {
	t.Setenv("BIFROST_TEST_REDACT_BASE_URL", "http://secret-host.internal:11434")
	base := schemas.NewSecretVar("env.BIFROST_TEST_REDACT_BASE_URL")
	dnsErr := &net.DNSError{Err: "no such host", Name: "secret-host.internal"}
	bifrostErr := NewBifrostUpstreamConnectionError(schemas.ErrProviderNetworkError, dnsErr)

	RedactBaseURLError(bifrostErr, base)

	body, err := json.Marshal(bifrostErr)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "secret-host")
	assert.Contains(t, string(body), "env.BIFROST_TEST_REDACT_BASE_URL")
	var unwrapped *net.DNSError
	assert.True(t, errors.As(bifrostErr.Error.Error, &unwrapped), "the original error must stay reachable")

	t.Run("nil error and literal base_url are no-ops", func(t *testing.T) {
		RedactBaseURLError(nil, base)
		literal := NewBifrostUpstreamConnectionError(schemas.ErrProviderNetworkError, dnsErr)
		RedactBaseURLError(literal, schemas.NewSecretVar("http://secret-host.internal:11434"))
		assert.Same(t, error(dnsErr), literal.Error.Error)
	})
}

// BenchmarkBaseURLRequestPath compares the former string operations with the
// SecretVar request path. Reference resolution and normalization happen once at
// provider construction, outside the timed loop. The legacy cases model the
// pre-change code with an already resolved URL, isolating the indirection cost.
func BenchmarkBaseURLRequestPath(b *testing.B) {
	const endpoint = "https://upstream.example.com/v1beta"
	const path = "/batches/123:cancel"
	const logPrefix = "gemini batch cancel url: "
	b.Setenv("BIFROST_BENCH_BASE_URL", endpoint)
	for _, tc := range []struct {
		name string
		base *schemas.SecretVar
	}{
		{"literal", schemas.NewSecretVar(endpoint)},
		{"reference", schemas.NewSecretVar("env.BIFROST_BENCH_BASE_URL")},
	} {
		b.Run(tc.name, func(b *testing.B) {
			base := tc.base.GetValue()
			fullURL := base + path
			b.Run("construct/before", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					benchmarkBaseURLResult = base + path
				}
			})
			b.Run("construct/after", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					benchmarkBaseURLResult = tc.base.GetValue() + path
				}
			})
			// The log argument is evaluated even when the logger discards debug
			// messages; include the prefix concatenation used by the call sites.
			b.Run("log/before", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					benchmarkBaseURLResult = logPrefix + fullURL
				}
			})
			b.Run("log/after", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					benchmarkBaseURLResult = logPrefix + LoggableURL(tc.base, fullURL)
				}
			})
			b.Run("passthrough/before", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					var err error
					benchmarkBaseURLResult, err = BuildPassthroughURL(base, path, "alt=media")
					if err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("passthrough/after", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					var err error
					benchmarkBaseURLResult, err = BuildPassthroughURLFromSecret(tc.base, path, "alt=media")
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

var benchmarkBaseURLResult string
