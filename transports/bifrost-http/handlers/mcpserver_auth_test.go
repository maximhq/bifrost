package handlers

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// newTestMCPHandler builds an MCPServerHandler around the given config without
// going through NewMCPServerHandler (which needs a live tool manager). Per-VK
// servers are looked up from vkMCPServers; tests pre-seed it to keep the VK
// path from building a real server.
func newTestMCPHandler(cfg *lib.Config) *MCPServerHandler {
	return &MCPServerHandler{
		globalMCPServer: server.NewMCPServer("test", "v0", server.WithToolCapabilities(true)),
		vkMCPServers:    map[string]*server.MCPServer{},
		config:          cfg,
	}
}

// TestGetMCPServerForRequest_JWTPath covers the JWT branch of /mcp auth across
// modes and identity kinds: the security contract for OAuth-authenticated calls.
func TestGetMCPServerForRequest_JWTPath(t *testing.T) {
	SetLogger(&mockLogger{})
	key, priv := newTestSigningKey(t)

	t.Run("oauth mode: valid vk JWT with active key is accepted", func(t *testing.T) {
		activeVK := &configtables.TableVirtualKey{ID: "vk-row-1", Value: "sk-bf-active", IsActive: new(true)}
		store := &mockOAuth2Store{
			signingKey: key,
			vksByID:    map[string]*configtables.TableVirtualKey{"vk-row-1": activeVK},
		}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeOAuth, false)
		h := newTestMCPHandler(cfg)
		// Pre-seed the per-VK server so the accepted path does not build one.
		h.vkMCPServers[activeVK.Value] = server.NewMCPServer("vk", "v0")

		raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeVK)
			c["sub"] = "vk-row-1"
		})
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Authorization", "Bearer "+raw)

		res, err := h.getMCPServerForRequest(ctx)
		require.NoError(t, err)
		require.NotNil(t, res)
		require.NotNil(t, res.jwtClaims)
		require.NotNil(t, res.jwtVK)
		assert.Equal(t, "vk-row-1", res.jwtVK.ID)
		assert.NotNil(t, res.mcpServer)
	})

	t.Run("vk JWT with inactive key is rejected", func(t *testing.T) {
		inactiveVK := &configtables.TableVirtualKey{ID: "vk-row-1", Value: "sk-bf-x", IsActive: new(false)}
		store := &mockOAuth2Store{
			signingKey: key,
			vksByID:    map[string]*configtables.TableVirtualKey{"vk-row-1": inactiveVK},
		}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
		h := newTestMCPHandler(cfg)

		raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeVK)
			c["sub"] = "vk-row-1"
		})
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Authorization", "Bearer "+raw)

		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inactive")
	})

	t.Run("vk JWT for unknown key is rejected", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key, vksByID: map[string]*configtables.TableVirtualKey{}}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
		h := newTestMCPHandler(cfg)

		raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeVK)
			c["sub"] = "missing-vk"
		})
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Authorization", "Bearer "+raw)

		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
	})

	t.Run("user JWT without a session is allowed with sessionValidated=false", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
		h := newTestMCPHandler(cfg)

		raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeUser)
			c["sub"] = "user-1"
		})
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Authorization", "Bearer "+raw)

		res, err := h.getMCPServerForRequest(ctx)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.False(t, res.sessionValidated)
		assert.Equal(t, h.globalMCPServer, res.mcpServer)
	})

	t.Run("user JWT with a matching session sets sessionValidated=true", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
		h := newTestMCPHandler(cfg)

		raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeUser)
			c["sub"] = "user-1"
		})
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Authorization", "Bearer "+raw)
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, "user-1")

		res, err := h.getMCPServerForRequest(ctx)
		require.NoError(t, err)
		assert.True(t, res.sessionValidated)
	})

	t.Run("user JWT with a mismatched session is rejected", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
		h := newTestMCPHandler(cfg)

		raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeUser)
			c["sub"] = "user-1"
		})
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Authorization", "Bearer "+raw)
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, "someone-else")

		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
	})

	t.Run("session JWT is rejected when auth is enforced", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, true)
		h := newTestMCPHandler(cfg)

		raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeSession)
			c["sub"] = "session-abc"
		})
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Authorization", "Bearer "+raw)

		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
	})

	t.Run("session JWT is accepted when auth is not enforced", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
		h := newTestMCPHandler(cfg)

		raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeSession)
			c["sub"] = "session-abc"
		})
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Authorization", "Bearer "+raw)

		res, err := h.getMCPServerForRequest(ctx)
		require.NoError(t, err)
		assert.Equal(t, h.globalMCPServer, res.mcpServer)
	})

	t.Run("both mode: JWT wins when a header VK is also present", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
		h := newTestMCPHandler(cfg)

		raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeSession)
			c["sub"] = "session-abc"
		})
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Authorization", "Bearer "+raw)
		ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "sk-bf-header")

		res, err := h.getMCPServerForRequest(ctx)
		require.NoError(t, err)
		require.NotNil(t, res.jwtClaims)
		assert.Equal(t, string(schemas.MCPAuthModeSession), res.jwtClaims.BfMode)
	})
}

// TestGetMCPServerForRequest_HeaderAndAnonPath covers the legacy header-VK path,
// anonymous access, and oauth-strict header rejection.
func TestGetMCPServerForRequest_HeaderAndAnonPath(t *testing.T) {
	SetLogger(&mockLogger{})
	key, _ := newTestSigningKey(t)

	t.Run("headers mode: active header VK connects", func(t *testing.T) {
		activeVK := &configtables.TableVirtualKey{ID: "vk-row-1", Value: "sk-bf-active", IsActive: new(true)}
		store := &mockOAuth2Store{
			signingKey: key,
			vksByValue: map[string]*configtables.TableVirtualKey{"sk-bf-active": activeVK},
		}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, true)
		h := newTestMCPHandler(cfg)
		h.vkMCPServers[activeVK.Value] = server.NewMCPServer("vk", "v0")

		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "sk-bf-active")

		res, err := h.getMCPServerForRequest(ctx)
		require.NoError(t, err)
		assert.Nil(t, res.jwtClaims)
		assert.NotNil(t, res.mcpServer)
	})

	t.Run("inactive header VK is rejected at the shared chokepoint", func(t *testing.T) {
		inactiveVK := &configtables.TableVirtualKey{ID: "vk-row-1", Value: "sk-bf-x", IsActive: new(false)}
		store := &mockOAuth2Store{
			signingKey: key,
			vksByValue: map[string]*configtables.TableVirtualKey{"sk-bf-x": inactiveVK},
		}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, true)
		h := newTestMCPHandler(cfg) // not pre-seeded: must traverse ensureVKMCPServer

		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "sk-bf-x")

		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inactive")
	})

	t.Run("anonymous access yields the global server when auth is not enforced", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
		h := newTestMCPHandler(cfg)

		ctx := &fasthttp.RequestCtx{}
		res, err := h.getMCPServerForRequest(ctx)
		require.NoError(t, err)
		assert.Equal(t, h.globalMCPServer, res.mcpServer)
	})

	t.Run("no credentials rejected when auth is enforced", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, true)
		h := newTestMCPHandler(cfg)

		ctx := &fasthttp.RequestCtx{}
		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
	})

	t.Run("oauth strict mode rejects a header VK with WWW-Authenticate", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeOAuth, false)
		h := newTestMCPHandler(cfg)

		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "sk-bf-active")

		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
		assert.NotEmpty(t, ctx.Response.Header.Peek("WWW-Authenticate"))
	})

	t.Run("oauth strict mode with no credentials sets WWW-Authenticate", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeOAuth, false)
		h := newTestMCPHandler(cfg)

		ctx := &fasthttp.RequestCtx{}
		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
		assert.NotEmpty(t, ctx.Response.Header.Peek("WWW-Authenticate"))
	})

	t.Run("headers mode: a JWT bearer is not treated as a credential", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, true)
		h := newTestMCPHandler(cfg)

		// A JWT-looking bearer in headers mode: discovery is off, so the JWT path
		// is skipped and the token is not a VK — with auth enforced this rejects.
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.payload.sig")

		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
	})
}
