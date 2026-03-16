package governance

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeCtxWithHeaders builds a BifrostContext containing the given header map.
// Keys should be pre-lowercased to match the runtime behaviour of the HTTP transport layer,
// which stores all incoming request headers with lowercased keys.
func makeCtxWithHeaders(headers map[string]string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, headers)
	return ctx
}

// pluginWithHeaders creates a minimal GovernancePlugin with the supplied required-header list.
func pluginWithHeaders(requiredHeaders []string) *GovernancePlugin {
	return &GovernancePlugin{
		requiredHeaders: &requiredHeaders,
	}
}

// TestValidateRequiredHeaders_NilConfig verifies that a nil required-headers pointer is a no-op.
func TestValidateRequiredHeaders_NilConfig(t *testing.T) {
	p := &GovernancePlugin{requiredHeaders: nil}
	ctx := makeCtxWithHeaders(nil)
	assert.Nil(t, p.validateRequiredHeaders(ctx))
}

// TestValidateRequiredHeaders_EmptyList verifies that an empty required-headers list is a no-op.
func TestValidateRequiredHeaders_EmptyList(t *testing.T) {
	p := pluginWithHeaders([]string{})
	ctx := makeCtxWithHeaders(map[string]string{"x-tenant-id": "abc"})
	assert.Nil(t, p.validateRequiredHeaders(ctx))
}

// TestValidateRequiredHeaders_PresenceOnly_Present verifies that a header configured without a
// value constraint passes when the header is present with any value.
func TestValidateRequiredHeaders_PresenceOnly_Present(t *testing.T) {
	p := pluginWithHeaders([]string{"X-Tenant-ID"})
	ctx := makeCtxWithHeaders(map[string]string{"x-tenant-id": "anything"})
	assert.Nil(t, p.validateRequiredHeaders(ctx))
}

// TestValidateRequiredHeaders_PresenceOnly_Missing verifies that a missing header results in a
// 400 error with type "missing_required_headers".
func TestValidateRequiredHeaders_PresenceOnly_Missing(t *testing.T) {
	p := pluginWithHeaders([]string{"X-Tenant-ID"})
	ctx := makeCtxWithHeaders(map[string]string{})
	err := p.validateRequiredHeaders(ctx)
	require.NotNil(t, err)
	assert.Equal(t, 400, *err.StatusCode)
	assert.Equal(t, "missing_required_headers", *err.Type)
	assert.Contains(t, err.Error.Message, "missing required headers")
	assert.Contains(t, err.Error.Message, "X-Tenant-ID")
}

// TestValidateRequiredHeaders_WithValue_Correct verifies that a header configured with an exact
// value constraint passes when the header is present with the correct value.
func TestValidateRequiredHeaders_WithValue_Correct(t *testing.T) {
	p := pluginWithHeaders([]string{"X-Proxy-Token=secretval"})
	ctx := makeCtxWithHeaders(map[string]string{"x-proxy-token": "secretval"})
	assert.Nil(t, p.validateRequiredHeaders(ctx))
}

// TestValidateRequiredHeaders_WithValue_WrongValue verifies that a header with a wrong value
// results in a 400 error mentioning "invalid required header value".
func TestValidateRequiredHeaders_WithValue_WrongValue(t *testing.T) {
	p := pluginWithHeaders([]string{"X-Proxy-Token=secretval"})
	ctx := makeCtxWithHeaders(map[string]string{"x-proxy-token": "wrongval"})
	err := p.validateRequiredHeaders(ctx)
	require.NotNil(t, err)
	assert.Equal(t, 400, *err.StatusCode)
	assert.Equal(t, "missing_required_headers", *err.Type)
	assert.Contains(t, err.Error.Message, "invalid required header value")
	assert.Contains(t, err.Error.Message, "X-Proxy-Token")
}

// TestValidateRequiredHeaders_WithValue_Missing verifies that a header configured with a value
// constraint that is entirely absent results in a "missing required headers" error.
func TestValidateRequiredHeaders_WithValue_Missing(t *testing.T) {
	p := pluginWithHeaders([]string{"X-Proxy-Token=secretval"})
	ctx := makeCtxWithHeaders(map[string]string{})
	err := p.validateRequiredHeaders(ctx)
	require.NotNil(t, err)
	assert.Equal(t, 400, *err.StatusCode)
	assert.Contains(t, err.Error.Message, "missing required headers")
	assert.Contains(t, err.Error.Message, "X-Proxy-Token")
}

// TestValidateRequiredHeaders_Mixed verifies correct behaviour when a mix of presence-only and
// value-constrained entries are configured, and multiple checks fail at once.
func TestValidateRequiredHeaders_Mixed(t *testing.T) {
	p := pluginWithHeaders([]string{"X-Tenant-ID", "X-Proxy-Token=secretval"})

	t.Run("all pass", func(t *testing.T) {
		ctx := makeCtxWithHeaders(map[string]string{
			"x-tenant-id":   "tenant-123",
			"x-proxy-token": "secretval",
		})
		assert.Nil(t, p.validateRequiredHeaders(ctx))
	})

	t.Run("one missing one wrong value", func(t *testing.T) {
		ctx := makeCtxWithHeaders(map[string]string{
			"x-proxy-token": "badval",
		})
		err := p.validateRequiredHeaders(ctx)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, "missing required headers")
		assert.Contains(t, err.Error.Message, "X-Tenant-ID")
		assert.Contains(t, err.Error.Message, "invalid required header value")
		assert.Contains(t, err.Error.Message, "X-Proxy-Token")
	})
}

// TestValidateRequiredHeaders_CaseInsensitiveName verifies that header name matching is
// case-insensitive: "X-PROXY-TOKEN=secret" matches the "x-proxy-token" header stored in context.
func TestValidateRequiredHeaders_CaseInsensitiveName(t *testing.T) {
	p := pluginWithHeaders([]string{"X-PROXY-TOKEN=secret"})
	ctx := makeCtxWithHeaders(map[string]string{"x-proxy-token": "secret"})
	assert.Nil(t, p.validateRequiredHeaders(ctx))
}

// TestValidateRequiredHeaders_EmptyRequiredValue verifies that "Header=" (empty value after =)
// requires the header to be present with an empty value.
func TestValidateRequiredHeaders_EmptyRequiredValue(t *testing.T) {
	p := pluginWithHeaders([]string{"X-Empty="})

	t.Run("header absent", func(t *testing.T) {
		ctx := makeCtxWithHeaders(map[string]string{})
		err := p.validateRequiredHeaders(ctx)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, "missing required headers")
	})

	t.Run("header present with empty value", func(t *testing.T) {
		ctx := makeCtxWithHeaders(map[string]string{"x-empty": ""})
		assert.Nil(t, p.validateRequiredHeaders(ctx))
	})

	t.Run("header present with non-empty value", func(t *testing.T) {
		ctx := makeCtxWithHeaders(map[string]string{"x-empty": "notempty"})
		err := p.validateRequiredHeaders(ctx)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, "invalid required header value")
	})
}

// TestValidateRequiredHeaders_EnvVar_Resolved verifies that an env.VAR_NAME reference in
// the value part is resolved at request time.
func TestValidateRequiredHeaders_EnvVar_Resolved(t *testing.T) {
	t.Setenv("TEST_PROXY_TOKEN", "resolved-secret")

	p := pluginWithHeaders([]string{"X-Proxy-Token=env.TEST_PROXY_TOKEN"})

	t.Run("correct value passes", func(t *testing.T) {
		ctx := makeCtxWithHeaders(map[string]string{"x-proxy-token": "resolved-secret"})
		assert.Nil(t, p.validateRequiredHeaders(ctx))
	})

	t.Run("literal env ref string fails", func(t *testing.T) {
		ctx := makeCtxWithHeaders(map[string]string{"x-proxy-token": "env.TEST_PROXY_TOKEN"})
		err := p.validateRequiredHeaders(ctx)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, "invalid required header value")
		assert.Contains(t, err.Error.Message, "X-Proxy-Token")
	})

	t.Run("empty header value fails when env var resolves to non-empty", func(t *testing.T) {
		ctx := makeCtxWithHeaders(map[string]string{"x-proxy-token": ""})
		err := p.validateRequiredHeaders(ctx)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, "invalid required header value")
	})
}

// TestValidateRequiredHeaders_EnvVar_NotSet verifies that an unresolvable env.VAR_NAME reference
// causes a 400 "invalid required header value" error (not a panic or missing-header error).
func TestValidateRequiredHeaders_EnvVar_NotSet(t *testing.T) {
	p := pluginWithHeaders([]string{"X-Proxy-Token=env.NONEXISTENT_VAR_XYZ_12345"})
	ctx := makeCtxWithHeaders(map[string]string{"x-proxy-token": "anyval"})
	err := p.validateRequiredHeaders(ctx)
	require.NotNil(t, err)
	assert.Equal(t, 400, *err.StatusCode)
	assert.Contains(t, err.Error.Message, "invalid required header value")
	assert.Contains(t, err.Error.Message, "X-Proxy-Token")
}
