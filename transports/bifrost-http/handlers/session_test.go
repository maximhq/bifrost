package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func setupSessionHandler(t *testing.T) *SessionHandler {
	t.Helper()

	store, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{
			Path: t.TempDir() + "/config.db",
		},
	}, &mockLogger{})
	require.NoError(t, err)

	hashedPassword, err := encrypt.Hash("correct-password")
	require.NoError(t, err)
	require.NoError(t, store.UpdateAuthConfig(context.Background(), &configstore.AuthConfig{
		AdminUserName: schemas.NewEnvVar("admin"),
		AdminPassword: schemas.NewEnvVar(hashedPassword),
		IsEnabled:     true,
	}))

	return NewSessionHandler(store, nil)
}

func loginRequestCtx(username string, password string) *fasthttp.RequestCtx {
	return newTestRequestCtx(fmt.Sprintf(`{"username":%q,"password":%q}`, username, password))
}

func loginRequestCtxWithForwardedIP(username string, password string, forwardedIP string) *fasthttp.RequestCtx {
	ctx := loginRequestCtx(username, password)
	ctx.Request.Header.Set("X-Forwarded-For", forwardedIP)
	return ctx
}

func TestSessionLogin_RateLimitsFailedPasswordAttempts(t *testing.T) {
	handler := setupSessionHandler(t)

	for i := 0; i < maxLoginAttemptsPerKey-1; i++ {
		ctx := loginRequestCtx("admin", "wrong-password")
		handler.login(ctx)
		require.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
	}

	ctx := loginRequestCtx("admin", "wrong-password")
	handler.login(ctx)
	require.Equal(t, fasthttp.StatusTooManyRequests, ctx.Response.StatusCode())
	require.NotEmpty(t, ctx.Response.Header.Peek("Retry-After"))

	ctx = loginRequestCtx("admin", "correct-password")
	handler.login(ctx)
	require.Equal(t, fasthttp.StatusTooManyRequests, ctx.Response.StatusCode())
}

func TestSessionLogin_SuccessClearsFailedAttempts(t *testing.T) {
	handler := setupSessionHandler(t)

	for i := 0; i < maxLoginAttemptsPerKey-1; i++ {
		ctx := loginRequestCtx("admin", "wrong-password")
		handler.login(ctx)
		require.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
	}

	ctx := loginRequestCtx("admin", "correct-password")
	handler.login(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())

	for i := 0; i < maxLoginAttemptsPerKey-1; i++ {
		ctx = loginRequestCtx("admin", "wrong-password")
		handler.login(ctx)
		require.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
	}
}

func TestSessionLogin_UsesForwardedIPWhenPresent(t *testing.T) {
	handler := setupSessionHandler(t)

	for i := 0; i < maxLoginAttemptsPerKey-1; i++ {
		ctx := loginRequestCtxWithForwardedIP(fmt.Sprintf("unknown-user-%d", i), "wrong-password", "203.0.113.10, 10.0.0.1")
		handler.login(ctx)
		require.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
	}

	ctx := loginRequestCtxWithForwardedIP("admin", "wrong-password", "203.0.113.10, 10.0.0.1")
	handler.login(ctx)
	require.Equal(t, fasthttp.StatusTooManyRequests, ctx.Response.StatusCode())

	ctx = loginRequestCtxWithForwardedIP("admin", "correct-password", "198.51.100.7")
	handler.login(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
}
