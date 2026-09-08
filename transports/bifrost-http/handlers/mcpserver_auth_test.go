package handlers

import (
	"context"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// newTestMCPHandler builds an MCPServerHandler around the given config without going through
// NewMCPServerHandler (which needs a live tool manager). No admitter is wired, so admission
// resolves nothing; tests of the admission step install a fakeAdmitter.
func newTestMCPHandler(cfg *lib.Config) *MCPServerHandler {
	h := &MCPServerHandler{config: cfg}
	h.mcpServer.Store(server.NewMCPServer("test", "v0", server.WithToolCapabilities(true)))
	return h
}

// fakeAdmitter stands in for governance: it hands back a fixed access or a fixed refusal, and
// remembers the context it was asked about.
type fakeAdmitter struct {
	access  schemas.Access
	refusal *schemas.BifrostError
	seen    *schemas.BifrostContext
}

func (f *fakeAdmitter) AdmitMCPGatewayRequest(ctx *schemas.BifrostContext) (schemas.Access, *schemas.BifrostError) {
	f.seen = ctx
	return f.access, f.refusal
}

// newRequestCtx returns the pair a request is handled with: the fasthttp request, and a
// BifrostContext standing in for what ConvertToBifrostContext would have produced from it.
func newRequestCtx() (*fasthttp.RequestCtx, *schemas.BifrostContext) {
	return &fasthttp.RequestCtx{}, schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func stringFromCtx(ctx *schemas.BifrostContext, key schemas.BifrostContextKey) string {
	value, _ := ctx.Value(key).(string)
	return value
}

// restrictedAccess is the access of a caller granted the listed tools of one client.
func restrictedAccess(client string, tools ...string) schemas.Access {
	permit := grant.NewPermit(grant.PermitVirtualKey, "vk-1", "Caller Key", true, false, nil,
		[]schemas.MCPPermit{{Client: client + "-id", ClientName: client, Tools: tools}})
	return grant.NewAccess([]schemas.Permit{permit}, nil, "", nil)
}

// TestGetVKFromRequest verifies the VK value is extracted from each supported
// header, in priority order, and that non-VK values are ignored. This mirrors
// the inference-path header set (x-bf-vk, Authorization Bearer, x-api-key,
// x-goog-api-key) so MCP and inference stay at parity.
func TestGetVKFromRequest(t *testing.T) {
	const vk = "sk-bf-test-virtual-key"

	cases := []struct {
		name   string
		setup  func(*fasthttp.RequestCtx)
		wantVK string
	}{
		{
			name:   "x-bf-vk header",
			setup:  func(ctx *fasthttp.RequestCtx) { ctx.Request.Header.Set("x-bf-vk", vk) },
			wantVK: vk,
		},
		{
			name:   "Authorization Bearer header",
			setup:  func(ctx *fasthttp.RequestCtx) { ctx.Request.Header.Set("Authorization", "Bearer "+vk) },
			wantVK: vk,
		},
		{
			name:   "x-api-key header",
			setup:  func(ctx *fasthttp.RequestCtx) { ctx.Request.Header.Set("x-api-key", vk) },
			wantVK: vk,
		},
		{
			name:   "x-goog-api-key header",
			setup:  func(ctx *fasthttp.RequestCtx) { ctx.Request.Header.Set("x-goog-api-key", vk) },
			wantVK: vk,
		},
		{
			name:   "no header returns empty string",
			setup:  func(*fasthttp.RequestCtx) {},
			wantVK: "",
		},
		{
			name:   "non-VK Bearer token returns empty string",
			setup:  func(ctx *fasthttp.RequestCtx) { ctx.Request.Header.Set("Authorization", "Bearer regular-api-key-123") },
			wantVK: "",
		},
		{
			name:   "non-VK x-goog-api-key returns empty string",
			setup:  func(ctx *fasthttp.RequestCtx) { ctx.Request.Header.Set("x-goog-api-key", "regular-google-key") },
			wantVK: "",
		},
		{
			name: "x-bf-vk takes priority over x-goog-api-key",
			setup: func(ctx *fasthttp.RequestCtx) {
				ctx.Request.Header.Set("x-bf-vk", vk)
				ctx.Request.Header.Set("x-goog-api-key", "sk-bf-other")
			},
			wantVK: vk,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			tc.setup(ctx)
			assert.Equal(t, tc.wantVK, getVKFromRequest(ctx))
		})
	}
}

// TestAuthenticate_JWTPath covers the JWT branch of /mcp authentication across modes and identity
// kinds: what is verified, what is refused, and what identity is stamped for governance to resolve.
func TestAuthenticate_JWTPath(t *testing.T) {
	SetLogger(&mockLogger{})
	key, priv := newTestSigningKey(t)

	vkToken := func(sub string) string {
		return mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeVK)
			c["sub"] = sub
		})
	}
	userToken := func(sub string) string {
		return mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeUser)
			c["sub"] = sub
		})
	}
	sessionToken := func(sub string) string {
		return mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
			c["bf_mode"] = string(schemas.MCPAuthModeSession)
			c["sub"] = sub
		})
	}

	t.Run("oauth mode: vk JWT stamps the key's value for governance to resolve", func(t *testing.T) {
		activeVK := &configtables.TableVirtualKey{ID: "vk-row-1", Value: *schemas.NewSecretVar("sk-bf-active"), IsActive: new(true)}
		store := &mockOAuth2Store{signingKey: key, vksByID: map[string]*configtables.TableVirtualKey{"vk-row-1": activeVK}}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeOAuth, false))

		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set("Authorization", "Bearer "+vkToken("vk-row-1"))

		require.NoError(t, h.authenticate(ctx, bifrostCtx))
		assert.Equal(t, "sk-bf-active", stringFromCtx(bifrostCtx, schemas.BifrostContextKeyVirtualKey))
		assert.Empty(t, stringFromCtx(bifrostCtx, schemas.BifrostContextKeyUserID))
	})

	// Whether a key may still be used is governance's answer, read off the grant it resolves, so the
	// same key is refused the same way on every path. Authentication only stamps it.
	t.Run("vk JWT with an inactive key is stamped rather than refused here", func(t *testing.T) {
		inactiveVK := &configtables.TableVirtualKey{ID: "vk-row-1", Value: *schemas.NewSecretVar("sk-bf-x"), IsActive: new(false)}
		store := &mockOAuth2Store{signingKey: key, vksByID: map[string]*configtables.TableVirtualKey{"vk-row-1": inactiveVK}}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false))

		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set("Authorization", "Bearer "+vkToken("vk-row-1"))

		require.NoError(t, h.authenticate(ctx, bifrostCtx))
		assert.Equal(t, "sk-bf-x", stringFromCtx(bifrostCtx, schemas.BifrostContextKeyVirtualKey))
	})

	t.Run("vk JWT for an unknown key is rejected", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key, vksByID: map[string]*configtables.TableVirtualKey{}}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false))

		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set("Authorization", "Bearer "+vkToken("missing-vk"))

		require.Error(t, h.authenticate(ctx, bifrostCtx))
		assert.Empty(t, stringFromCtx(bifrostCtx, schemas.BifrostContextKeyVirtualKey))
	})

	t.Run("vk JWT is rejected once virtual-key identity is disabled and user mode is offered", func(t *testing.T) {
		activeVK := &configtables.TableVirtualKey{ID: "vk-row-1", Value: *schemas.NewSecretVar("sk-bf-active"), IsActive: new(true)}
		store := &mockOAuth2Store{signingKey: key, vksByID: map[string]*configtables.TableVirtualKey{"vk-row-1": activeVK}}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
		cfg.ClientConfig.OAuth2ServerConfig.DisableVKIdentity = true
		h := newTestMCPHandler(cfg)
		h.identityResolver = &fakeResolver{userModeAvailable: true}

		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set("Authorization", "Bearer "+vkToken("vk-row-1"))

		err := h.authenticate(ctx, bifrostCtx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no longer accepted")
		assert.NotEmpty(t, ctx.Response.Header.Peek("WWW-Authenticate"))
	})

	t.Run("user JWT without a session stamps the user", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false))

		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set("Authorization", "Bearer "+userToken("user-1"))

		require.NoError(t, h.authenticate(ctx, bifrostCtx))
		assert.Equal(t, "user-1", stringFromCtx(bifrostCtx, schemas.BifrostContextKeyUserID))
		assert.Empty(t, stringFromCtx(bifrostCtx, schemas.BifrostContextKeyVirtualKey), "user mode attributes no virtual key")
	})

	t.Run("user JWT is rejected when the user is no longer active", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false))
		h.identityResolver = &fakeResolver{userInactive: true}

		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set("Authorization", "Bearer "+userToken("user-1"))

		require.Error(t, h.authenticate(ctx, bifrostCtx))
		assert.Empty(t, stringFromCtx(bifrostCtx, schemas.BifrostContextKeyUserID))
	})

	t.Run("user JWT with a matching session is accepted", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false))

		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set("Authorization", "Bearer "+userToken("user-1"))
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, "user-1")

		require.NoError(t, h.authenticate(ctx, bifrostCtx))
	})

	t.Run("user JWT with a mismatched session is rejected", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false))

		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set("Authorization", "Bearer "+userToken("user-1"))
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, "someone-else")

		require.Error(t, h.authenticate(ctx, bifrostCtx))
	})

	t.Run("session JWT is rejected when auth is enforced", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, true))

		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set("Authorization", "Bearer "+sessionToken("session-abc"))

		require.Error(t, h.authenticate(ctx, bifrostCtx))
		assert.Empty(t, stringFromCtx(bifrostCtx, schemas.BifrostContextKeyMCPSessionID))
	})

	t.Run("session JWT stamps the session when auth is not enforced", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false))

		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set("Authorization", "Bearer "+sessionToken("session-abc"))

		require.NoError(t, h.authenticate(ctx, bifrostCtx))
		assert.Equal(t, "session-abc", stringFromCtx(bifrostCtx, schemas.BifrostContextKeyMCPSessionID))
	})

	t.Run("both mode: session token with a header VK is rejected", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false))

		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set("Authorization", "Bearer "+sessionToken("session-abc"))
		ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "sk-bf-header")

		err := h.authenticate(ctx, bifrostCtx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting credentials")
	})

	t.Run("both mode: vk token with a header VK is rejected", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false))

		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set("Authorization", "Bearer "+vkToken("vk-row-1"))
		ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "sk-bf-header")

		err := h.authenticate(ctx, bifrostCtx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting credentials")
	})
}

// TestAuthenticate_HeaderAndAnonPath covers header credentials, identities an upstream auth layer
// stamped, anonymous access, and oauth-strict rejection.
func TestAuthenticate_HeaderAndAnonPath(t *testing.T) {
	SetLogger(&mockLogger{})
	key, _ := newTestSigningKey(t)

	// The key is not looked up here: governance resolves it from the value the request converter
	// stamped, and refuses one that does not exist, is inactive or has expired.
	t.Run("headers mode: a header VK is accepted without being looked up", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, true))

		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "sk-bf-anything")

		require.NoError(t, h.authenticate(ctx, bifrostCtx))
	})

	t.Run("anonymous access is accepted when auth is not enforced", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false))

		ctx, bifrostCtx := newRequestCtx()
		require.NoError(t, h.authenticate(ctx, bifrostCtx))
	})

	t.Run("no credentials rejected when auth is enforced", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, true))

		ctx, bifrostCtx := newRequestCtx()
		require.Error(t, h.authenticate(ctx, bifrostCtx))
	})

	for _, mode := range []configtables.MCPServerAuthMode{configtables.MCPServerAuthModeHeaders, configtables.MCPServerAuthModeBoth} {
		t.Run(string(mode)+" mode: an identity stamped upstream satisfies enforced auth", func(t *testing.T) {
			store := &mockOAuth2Store{signingKey: key}
			h := newTestMCPHandler(newTestOAuth2Config(store, mode, true))

			ctx, bifrostCtx := newRequestCtx()
			bifrostCtx.SetValue(schemas.BifrostContextKeyUserID, "user-1")

			require.NoError(t, h.authenticate(ctx, bifrostCtx))
			assert.Equal(t, "user-1", stringFromCtx(bifrostCtx, schemas.BifrostContextKeyUserID))
		})
	}

	t.Run("oauth strict mode ignores an identity stamped upstream", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeOAuth, true))

		ctx, bifrostCtx := newRequestCtx()
		bifrostCtx.SetValue(schemas.BifrostContextKeyUserID, "user-1")

		err := h.authenticate(ctx, bifrostCtx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OAuth JWT")
	})

	t.Run("oauth strict mode rejects a header VK with WWW-Authenticate", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeOAuth, false))

		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "sk-bf-active")

		require.Error(t, h.authenticate(ctx, bifrostCtx))
		assert.NotEmpty(t, ctx.Response.Header.Peek("WWW-Authenticate"))
	})

	t.Run("oauth strict mode with no credentials sets WWW-Authenticate", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeOAuth, false))

		ctx, bifrostCtx := newRequestCtx()
		require.Error(t, h.authenticate(ctx, bifrostCtx))
		assert.NotEmpty(t, ctx.Response.Header.Peek("WWW-Authenticate"))
	})

	t.Run("headers mode: a JWT bearer is not treated as a credential", func(t *testing.T) {
		store := &mockOAuth2Store{signingKey: key}
		h := newTestMCPHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, true))

		// A JWT-looking bearer in headers mode: discovery is off, so the JWT path
		// is skipped and the token is not a VK; with auth enforced this rejects.
		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set("Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.payload.sig")

		require.Error(t, h.authenticate(ctx, bifrostCtx))
	})
}

// TestAdmit covers the step after authentication: governance's answer is either a refusal, mapped to
// the status it carries, or an access whose tool list is stamped for the tool filter and the executor.
func TestAdmit(t *testing.T) {
	SetLogger(&mockLogger{})
	key, _ := newTestSigningKey(t)
	newHandler := func(mode configtables.MCPServerAuthMode, admitter MCPGatewayAdmitter) *MCPServerHandler {
		h := newTestMCPHandler(newTestOAuth2Config(&mockOAuth2Store{signingKey: key}, mode, false))
		h.admitter = admitter
		return h
	}
	includeTools := func(ctx *schemas.BifrostContext) any { return ctx.Value(schemas.MCPContextKeyIncludeTools) }

	t.Run("a refusal is answered with the status it carries", func(t *testing.T) {
		for _, tc := range []struct {
			status  int
			message string
		}{
			{fasthttp.StatusForbidden, "virtual key is inactive"},
			{fasthttp.StatusPaymentRequired, "Budget exceeded"},
			{fasthttp.StatusTooManyRequests, "Rate limit exceeded"},
		} {
			h := newHandler(configtables.MCPServerAuthModeHeaders, &fakeAdmitter{refusal: &schemas.BifrostError{
				StatusCode: new(tc.status), Error: &schemas.ErrorField{Message: tc.message},
			}})
			ctx, bifrostCtx := newRequestCtx()
			refusal := h.admit(ctx, bifrostCtx)
			require.NotNil(t, refusal)
			assert.Equal(t, tc.status, refusal.status)
			assert.Contains(t, refusal.message, tc.message)
			assert.Nil(t, includeTools(bifrostCtx), "a refused request has no tool list")
		}
	})

	t.Run("a 401 refusal points at the authorization server when discovery is enabled", func(t *testing.T) {
		refusal := &schemas.BifrostError{StatusCode: new(fasthttp.StatusUnauthorized), Error: &schemas.ErrorField{Message: "access not found"}}

		h := newHandler(configtables.MCPServerAuthModeBoth, &fakeAdmitter{refusal: refusal})
		ctx, bifrostCtx := newRequestCtx()
		require.Equal(t, fasthttp.StatusUnauthorized, h.admit(ctx, bifrostCtx).status)
		assert.NotEmpty(t, ctx.Response.Header.Peek("WWW-Authenticate"))

		h = newHandler(configtables.MCPServerAuthModeHeaders, &fakeAdmitter{refusal: refusal})
		ctx, bifrostCtx = newRequestCtx()
		require.Equal(t, fasthttp.StatusUnauthorized, h.admit(ctx, bifrostCtx).status)
		assert.Empty(t, ctx.Response.Header.Peek("WWW-Authenticate"))
	})

	t.Run("a refusal without a status is forbidden", func(t *testing.T) {
		h := newHandler(configtables.MCPServerAuthModeHeaders, &fakeAdmitter{refusal: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "no"}}})
		ctx, bifrostCtx := newRequestCtx()
		assert.Equal(t, fasthttp.StatusForbidden, h.admit(ctx, bifrostCtx).status)
	})

	t.Run("restricted access stamps the tools it grants", func(t *testing.T) {
		h := newHandler(configtables.MCPServerAuthModeHeaders, &fakeAdmitter{access: restrictedAccess("sentry", "find_projects", "search_issues")})
		ctx, bifrostCtx := newRequestCtx()
		require.Nil(t, h.admit(ctx, bifrostCtx))
		assert.Equal(t, []string{"sentry-find_projects", "sentry-search_issues"}, includeTools(bifrostCtx))
	})

	t.Run("access granting no tool stamps an empty list, which permits none", func(t *testing.T) {
		h := newHandler(configtables.MCPServerAuthModeHeaders, &fakeAdmitter{access: restrictedAccess("sentry")})
		ctx, bifrostCtx := newRequestCtx()
		require.Nil(t, h.admit(ctx, bifrostCtx))
		assert.Equal(t, []string{}, includeTools(bifrostCtx))
	})

	t.Run("a caller's list narrows within the access and cannot widen it", func(t *testing.T) {
		h := newHandler(configtables.MCPServerAuthModeHeaders, &fakeAdmitter{access: restrictedAccess("sentry", "find_projects", "search_issues")})
		ctx, bifrostCtx := newRequestCtx()
		bifrostCtx.SetValue(schemas.MCPContextKeyIncludeTools, []string{"sentry-find_projects", "sentry-delete_project", "github-*"})
		require.Nil(t, h.admit(ctx, bifrostCtx))
		assert.Equal(t, []string{"sentry-find_projects"}, includeTools(bifrostCtx))
	})

	t.Run("no access leaves the caller's list alone and stamps nothing of its own", func(t *testing.T) {
		// Nothing restricts a request that carries no access: it presented nothing, or the deployment
		// has nothing to resolve access with.
		h := newHandler(configtables.MCPServerAuthModeHeaders, &fakeAdmitter{})
		ctx, bifrostCtx := newRequestCtx()
		bifrostCtx.SetValue(schemas.MCPContextKeyIncludeTools, []string{"sentry-*"})
		require.Nil(t, h.admit(ctx, bifrostCtx))
		assert.Equal(t, []string{"sentry-*"}, includeTools(bifrostCtx))

		ctx, bifrostCtx = newRequestCtx()
		require.Nil(t, h.admit(ctx, bifrostCtx))
		assert.Nil(t, includeTools(bifrostCtx), "an empty list would read as no tool at all")
	})

	t.Run("nothing resolved stamps nothing", func(t *testing.T) {
		h := newHandler(configtables.MCPServerAuthModeHeaders, &fakeAdmitter{})
		ctx, bifrostCtx := newRequestCtx()
		require.Nil(t, h.admit(ctx, bifrostCtx))
		assert.Nil(t, includeTools(bifrostCtx))

		h = newHandler(configtables.MCPServerAuthModeHeaders, nil)
		ctx, bifrostCtx = newRequestCtx()
		require.Nil(t, h.admit(ctx, bifrostCtx))
		assert.Nil(t, includeTools(bifrostCtx))
	})

	t.Run("governance is asked about the request's own context, after authentication", func(t *testing.T) {
		admitter := &fakeAdmitter{access: restrictedAccess("sentry", "find_projects")}
		h := newHandler(configtables.MCPServerAuthModeHeaders, admitter)
		ctx, bifrostCtx := newRequestCtx()
		ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "sk-bf-active")
		require.Nil(t, h.admit(ctx, bifrostCtx))
		assert.Same(t, bifrostCtx, admitter.seen)
	})

	t.Run("a request that fails authentication is refused before governance is asked", func(t *testing.T) {
		admitter := &fakeAdmitter{access: restrictedAccess("sentry", "find_projects")}
		h := newTestMCPHandler(newTestOAuth2Config(&mockOAuth2Store{signingKey: key}, configtables.MCPServerAuthModeHeaders, true))
		h.admitter = admitter
		ctx, bifrostCtx := newRequestCtx()
		refusal := h.admit(ctx, bifrostCtx)
		require.NotNil(t, refusal)
		assert.Equal(t, fasthttp.StatusUnauthorized, refusal.status)
		assert.Nil(t, admitter.seen)
	})
}
