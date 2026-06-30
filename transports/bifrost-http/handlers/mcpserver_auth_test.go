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

	t.Run("user JWT without a session is allowed", func(t *testing.T) {
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
		// No resolver wired → falls back to the global server.
		assert.Equal(t, h.globalMCPServer, res.mcpServer)
	})

	t.Run("user JWT is scoped to the user's representative virtual key", func(t *testing.T) {
		activeVK := &configtables.TableVirtualKey{ID: "vk-row-1", Value: "sk-bf-user-rep", IsActive: new(true)}
		store := &mockOAuth2Store{
			signingKey: key,
			vksByID:    map[string]*configtables.TableVirtualKey{"vk-row-1": activeVK},
		}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
		h := newTestMCPHandler(cfg)
		// Resolver maps the user to a representative VK; pre-seed its server so the
		// scoped path does not build a real one.
		h.identityResolver = &fakeResolver{userVKID: "vk-row-1"}
		vkServer := server.NewMCPServer("vk", "v0")
		h.vkMCPServers[activeVK.Value] = vkServer

		raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeUser)
			c["sub"] = "user-1"
		})
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Authorization", "Bearer "+raw)

		res, err := h.getMCPServerForRequest(ctx)
		require.NoError(t, err)
		require.NotNil(t, res)
		// Served the user's scoped VK server, NOT the global (unscoped) server.
		assert.Equal(t, vkServer, res.mcpServer)
		assert.NotEqual(t, h.globalMCPServer, res.mcpServer)
		// User mode keeps the user identity — no VK identity is attributed.
		assert.Nil(t, res.jwtVK)
	})

	t.Run("user JWT is rejected when the user is no longer active", func(t *testing.T) {
		activeVK := &configtables.TableVirtualKey{ID: "vk-row-1", Value: "sk-bf-user-rep", IsActive: new(true)}
		store := &mockOAuth2Store{
			signingKey: key,
			vksByID:    map[string]*configtables.TableVirtualKey{"vk-row-1": activeVK},
		}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
		h := newTestMCPHandler(cfg)
		// The resolver still maps the user to a VK, but reports the user as gone.
		// The request must be rejected at request time rather than falling through
		// to the global (unscoped) server until the access token expires.
		h.identityResolver = &fakeResolver{userVKID: "vk-row-1", userInactive: true}

		raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeUser)
			c["sub"] = "user-1"
		})
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Authorization", "Bearer "+raw)

		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
	})

	t.Run("user JWT falls back to the global server when the user has no virtual key", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
		h := newTestMCPHandler(cfg)
		h.identityResolver = &fakeResolver{userVKID: ""} // user has no AP-managed VK

		raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeUser)
			c["sub"] = "user-1"
		})
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Authorization", "Bearer "+raw)

		res, err := h.getMCPServerForRequest(ctx)
		require.NoError(t, err)
		assert.Equal(t, h.globalMCPServer, res.mcpServer)
	})

	t.Run("user JWT with a matching session is accepted", func(t *testing.T) {
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

		_, err := h.getMCPServerForRequest(ctx)
		require.NoError(t, err)
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

	t.Run("both mode: session token with a header VK is rejected", func(t *testing.T) {
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

		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting credentials")
	})

	t.Run("both mode: vk token with a header VK is rejected", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
		h := newTestMCPHandler(cfg)

		raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeVK)
			c["sub"] = "vk-row-1"
		})
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Authorization", "Bearer "+raw)
		ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "sk-bf-header")

		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting credentials")
	})
}

// TestGetMCPServerForRequest_PreAuthenticatedUserPath covers the path where an
// upstream auth layer has already authenticated the caller as a user and stamped
// the user id onto the request context (BifrostContextKeyUserID). In headers/both
// modes the request is scoped to the user's representative virtual key, just like
// a user-mode token; oauth-strict ignores it (Bifrost-issued tokens only).
func TestGetMCPServerForRequest_PreAuthenticatedUserPath(t *testing.T) {
	SetLogger(&mockLogger{})
	key, _ := newTestSigningKey(t)

	activeVK := &configtables.TableVirtualKey{ID: "vk-row-1", Value: "sk-bf-user-rep", IsActive: new(true)}
	newStore := func() *mockOAuth2Store {
		return &mockOAuth2Store{
			signingKey: key,
			vksByID:    map[string]*configtables.TableVirtualKey{"vk-row-1": activeVK},
		}
	}

	for _, mode := range []configtables.MCPServerAuthMode{
		configtables.MCPServerAuthModeHeaders,
		configtables.MCPServerAuthModeBoth,
	} {
		t.Run(string(mode)+" mode: stamped user id is scoped to the user's virtual key", func(t *testing.T) {
			cfg := newTestOAuth2Config(newStore(), mode, true)
			h := newTestMCPHandler(cfg)
			h.identityResolver = &fakeResolver{userVKID: "vk-row-1"}
			vkServer := server.NewMCPServer("vk", "v0")
			h.vkMCPServers[activeVK.Value] = vkServer

			ctx := &fasthttp.RequestCtx{}
			ctx.SetUserValue(schemas.BifrostContextKeyUserID, "user-1")

			res, err := h.getMCPServerForRequest(ctx)
			require.NoError(t, err)
			require.NotNil(t, res)
			// Served the user's scoped VK server, NOT the global (unscoped) one.
			assert.Equal(t, vkServer, res.mcpServer)
			assert.NotEqual(t, h.globalMCPServer, res.mcpServer)
			// Identity stays the user; no VK identity or JWT claims are attributed.
			assert.Nil(t, res.jwtVK)
			assert.Nil(t, res.jwtClaims)
		})
	}

	t.Run("user with no virtual key is rejected (strict VK parity)", func(t *testing.T) {
		cfg := newTestOAuth2Config(newStore(), configtables.MCPServerAuthModeBoth, true)
		h := newTestMCPHandler(cfg)
		h.identityResolver = &fakeResolver{userVKID: ""} // no AP-managed VK

		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, "user-1")

		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no MCP access grant")
	})

	t.Run("stamped user id with a header VK is rejected as conflicting", func(t *testing.T) {
		cfg := newTestOAuth2Config(newStore(), configtables.MCPServerAuthModeBoth, true)
		h := newTestMCPHandler(cfg)
		h.identityResolver = &fakeResolver{userVKID: "vk-row-1"}
		h.vkMCPServers[activeVK.Value] = server.NewMCPServer("vk", "v0")

		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, "user-1")
		ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "sk-bf-header")

		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting credentials")
	})

	t.Run("inactive representative virtual key is rejected", func(t *testing.T) {
		inactiveVK := &configtables.TableVirtualKey{ID: "vk-row-1", Value: "sk-bf-x", IsActive: new(false)}
		store := &mockOAuth2Store{
			signingKey: key,
			vksByID:    map[string]*configtables.TableVirtualKey{"vk-row-1": inactiveVK},
		}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, true)
		h := newTestMCPHandler(cfg)
		h.identityResolver = &fakeResolver{userVKID: "vk-row-1"}

		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, "user-1")

		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inactive")
	})

	t.Run("oauth-strict mode ignores the stamped user id", func(t *testing.T) {
		cfg := newTestOAuth2Config(newStore(), configtables.MCPServerAuthModeOAuth, true)
		h := newTestMCPHandler(cfg)
		h.identityResolver = &fakeResolver{userVKID: "vk-row-1"}

		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, "user-1")

		// No bearer JWT, and header credentials are rejected in oauth-strict: the
		// user-id check does not run, so this falls through to the strict rejection.
		_, err := h.getMCPServerForRequest(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OAuth JWT")
	})

	t.Run("no resolver: stamped user id is ignored", func(t *testing.T) {
		cfg := newTestOAuth2Config(newStore(), configtables.MCPServerAuthModeHeaders, false)
		h := newTestMCPHandler(cfg)
		// identityResolver is nil (pure OSS, no IdP); enforce=false → anonymous.

		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, "user-1")

		res, err := h.getMCPServerForRequest(ctx)
		require.NoError(t, err)
		assert.Equal(t, h.globalMCPServer, res.mcpServer)
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
