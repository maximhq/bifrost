package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

const testClaudeCodeResource = testIssuer + "/claude-code"

// newClaudeCodeGateway builds the gateway handler on a real sqlite store (full
// migrations, so the device-code columns exist) with the gateway enabled, plus a
// consent handler sharing the same config. /mcp is left on header auth: the
// gateway does not depend on the MCP auth mode.
func newClaudeCodeGateway(t *testing.T, enforceAuth bool) (*ClaudeCodeGatewayHandler, *OAuth2ConsentHandler, configstore.ConfigStore, *lib.Config) {
	t.Helper()
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, enforceAuth)
	cfg.ClientConfig.OAuth2ServerConfig.ClaudeCodeGateway = &configtables.ClaudeCodeGatewayConfig{Enabled: true}
	return NewClaudeCodeGatewayHandler(cfg, nil, nil, nil), NewOAuth2ConsentHandler(cfg, nil, nil), store, cfg
}

// seedGatewayVK stores an active virtual key and returns it.
func seedGatewayVK(t *testing.T, store configstore.ConfigStore, id, value string) *configtables.TableVirtualKey {
	t.Helper()
	vk := &configtables.TableVirtualKey{ID: id, Name: id, Value: *schemas.NewSecretVar(value), IsActive: new(true)}
	require.NoError(t, store.CreateVirtualKey(context.Background(), vk))
	return vk
}

func jsonBody(t *testing.T, ctx *fasthttp.RequestCtx) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &out), string(ctx.Response.Body()))
	return out
}

func oauthErrorCode(t *testing.T, ctx *fasthttp.RequestCtx) string {
	t.Helper()
	code, _ := jsonBody(t, ctx)["error"].(string)
	return code
}

// startDeviceSignIn runs device_authorization the way Claude Code does (only
// surface=claude_code, no client_id) and returns the device and user codes.
func startDeviceSignIn(t *testing.T, h *ClaudeCodeGatewayHandler) (deviceCode, userCode string) {
	t.Helper()
	ctx := formPostCtx("surface=claude_code")
	h.handleDeviceAuthorization(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	body := jsonBody(t, ctx)
	deviceCode, _ = body["device_code"].(string)
	userCode, _ = body["user_code"].(string)
	require.NotEmpty(t, deviceCode)
	require.Regexp(t, `^[BCDFGHJKLMNPQRSTVWXZ]{4}-[BCDFGHJKLMNPQRSTVWXZ]{4}$`, userCode)
	assert.Equal(t, testIssuer+"/oauth/device", body["verification_uri"])
	assert.Equal(t, testIssuer+"/oauth/device?user_code="+url.QueryEscape(userCode), body["verification_uri_complete"])
	assert.EqualValues(t, 600, body["expires_in"])
	assert.EqualValues(t, 5, body["interval"])
	return deviceCode, userCode
}

func pollDeviceToken(h *ClaudeCodeGatewayHandler, deviceCode string) *fasthttp.RequestCtx {
	ctx := formPostCtx(url.Values{"grant_type": {deviceCodeGrantType}, "device_code": {deviceCode}}.Encode())
	h.handleToken(ctx)
	return ctx
}

// approveWithVK runs the verification page and consent page legs for userCode
// using a virtual key, and returns the consent redirect.
func approveWithVK(t *testing.T, h *ClaudeCodeGatewayHandler, consent *OAuth2ConsentHandler, userCode, vkValue string) string {
	t.Helper()
	verify := jsonPostCtx(`{"user_code":"` + strings.ToLower(userCode) + `"}`)
	h.handleDeviceVerify(verify)
	require.Equal(t, fasthttp.StatusOK, verify.Response.StatusCode(), string(verify.Response.Body()))
	consentURL, _ := jsonBody(t, verify)["consent_url"].(string)
	require.True(t, strings.HasPrefix(consentURL, testIssuer+"/oauth/consent?flow="), consentURL)
	parsed, err := url.Parse(consentURL)
	require.NoError(t, err)
	flowID := parsed.Query().Get("flow")

	submit := jsonPostCtx(`{"mode":"vk","value":"` + vkValue + `"}`)
	submit.SetUserValue("id", flowID)
	consent.flowSubmit(submit)
	require.Equal(t, fasthttp.StatusOK, submit.Response.StatusCode(), string(submit.Response.Body()))
	var resp consentFlowSubmitResponse
	require.NoError(t, json.Unmarshal(submit.Response.Body(), &resp))
	return resp.RedirectURL
}

func jsonPostCtx(body string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.SetBodyString(body)
	return initCtx(&req)
}

// bearerCtx builds a request carrying a gateway bearer.
func bearerCtx(token string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod("POST")
	req.Header.Set("Authorization", "Bearer "+token)
	return initCtx(&req)
}

func TestClaudeCodeGateway_DisabledAnswers404(t *testing.T) {
	for name, mutate := range map[string]func(*lib.Config){
		"toggle off":       func(cfg *lib.Config) { cfg.ClientConfig.OAuth2ServerConfig.ClaudeCodeGateway.Enabled = false },
		"toggle unset":     func(cfg *lib.Config) { cfg.ClientConfig.OAuth2ServerConfig.ClaudeCodeGateway = nil },
		"issuer url unset": func(cfg *lib.Config) { cfg.ClientConfig.OAuth2ServerConfig.IssuerURL = nil },
	} {
		t.Run(name, func(t *testing.T) {
			h, _, _, cfg := newClaudeCodeGateway(t, false)
			mutate(cfg)

			for route, ctx := range map[string]*fasthttp.RequestCtx{
				"metadata":             getCtx("/claude-code/.well-known/oauth-authorization-server"),
				"device_authorization": formPostCtx("surface=claude_code"),
				"token":                formPostCtx("grant_type=refresh_token&refresh_token=x"),
				"revoke":               formPostCtx("token=x"),
				"verify":               jsonPostCtx(`{"user_code":"BCDF-GHJK"}`),
			} {
				switch route {
				case "metadata":
					h.handleMetadata(ctx)
				case "device_authorization":
					h.handleDeviceAuthorization(ctx)
				case "token":
					h.handleToken(ctx)
				case "revoke":
					h.handleRevoke(ctx)
				case "verify":
					h.handleDeviceVerify(ctx)
				}
				assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode(), route)
			}

			called := false
			ctx := bearerCtx("eyJx")
			h.BearerMiddleware(func(*fasthttp.RequestCtx) { called = true })(ctx)
			assert.False(t, called)
			assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())
		})
	}
}

func TestClaudeCodeGateway_Metadata(t *testing.T) {
	h, _, _, _ := newClaudeCodeGateway(t, false)
	ctx := getCtx("/claude-code/.well-known/oauth-authorization-server")
	h.handleMetadata(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	body := jsonBody(t, ctx)
	// Claude Code only uses endpoints on the same origin as the base URL it was given.
	assert.Equal(t, testClaudeCodeResource, body["issuer"])
	assert.Equal(t, testClaudeCodeResource+"/oauth/device_authorization", body["device_authorization_endpoint"])
	assert.Equal(t, testClaudeCodeResource+"/oauth/token", body["token_endpoint"])
	assert.Equal(t, testClaudeCodeResource+"/oauth/revoke", body["revocation_endpoint"])
	assert.ElementsMatch(t, []any{deviceCodeGrantType, "refresh_token"}, body["grant_types_supported"])
	assert.NotContains(t, body, "authorization_endpoint")
}

func TestClaudeCodeGateway_DeviceSignInWithVirtualKey(t *testing.T) {
	h, consent, store, cfg := newClaudeCodeGateway(t, true)
	vk := seedGatewayVK(t, store, "vk-cc-1", "sk-bf-claude-code")

	deviceCode, userCode := startDeviceSignIn(t, h)

	// Before approval the device keeps polling; polling inside the interval is slowed.
	first := pollDeviceToken(h, deviceCode)
	assert.Equal(t, fasthttp.StatusBadRequest, first.Response.StatusCode())
	assert.Equal(t, "authorization_pending", oauthErrorCode(t, first))
	tooFast := pollDeviceToken(h, deviceCode)
	assert.Equal(t, "slow_down", oauthErrorCode(t, tooFast))

	redirect := approveWithVK(t, h, consent, userCode, vk.Value.GetValue())
	assert.Equal(t, testIssuer+"/oauth/device?approved=1", redirect)

	// The built-in client was registered so the consent page can name it.
	client, err := store.GetOAuth2ClientByClientID(context.Background(), claudeCodeClientID)
	require.NoError(t, err)
	assert.Equal(t, "Claude Code", client.ClientName)

	h.polls.forget(hashSHA256Hex(deviceCode)) // skip the poll interval
	tokenCtx := pollDeviceToken(h, deviceCode)
	require.Equal(t, fasthttp.StatusOK, tokenCtx.Response.StatusCode(), string(tokenCtx.Response.Body()))
	tokens := jsonBody(t, tokenCtx)
	accessToken, _ := tokens["access_token"].(string)
	refreshToken, _ := tokens["refresh_token"].(string)
	require.NotEmpty(t, accessToken)
	require.NotEmpty(t, refreshToken)
	assert.Equal(t, "Bearer", tokens["token_type"])
	assert.EqualValues(t, configtables.DefaultAccessTokenTTL, tokens["expires_in"])

	// The token is bound to the /claude-code audience: valid here, refused on /mcp.
	signingKey, err := store.GetOAuth2SigningKey(bgCtx())
	require.NoError(t, err)
	claims, err := verifyOAuth2JWT(bgCtx(), accessToken, cfg, signingKey, testClaudeCodeResource)
	require.NoError(t, err)
	assert.Equal(t, string(schemas.MCPAuthModeVK), claims.BfMode)
	assert.Equal(t, vk.ID, claims.Subject)
	_, err = verifyMCPJWT(bgCtx(), accessToken, cfg, signingKey)
	assert.Error(t, err)

	// The device code is single-use.
	again := pollDeviceToken(h, deviceCode)
	assert.Equal(t, "expired_token", oauthErrorCode(t, again))

	// The bearer middleware turns the token into the virtual key it stands for
	// and strips the bearer before the request goes on.
	inference := bearerCtx(accessToken)
	inference.Request.Header.Set("x-api-key", "sk-bf-someone-else")
	var seenVK, seenAuth, seenAPIKey string
	h.BearerMiddleware(func(ctx *fasthttp.RequestCtx) {
		seenVK = string(ctx.Request.Header.Peek("x-bf-vk"))
		seenAuth = string(ctx.Request.Header.Peek("Authorization"))
		seenAPIKey = string(ctx.Request.Header.Peek("x-api-key"))
	})(inference)
	assert.Equal(t, vk.Value.GetValue(), seenVK)
	assert.Empty(t, seenAuth)
	assert.Empty(t, seenAPIKey)

	// Claude Code refreshes with only grant_type and refresh_token.
	refresh := formPostCtx(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}.Encode())
	h.handleToken(refresh)
	require.Equal(t, fasthttp.StatusOK, refresh.Response.StatusCode(), string(refresh.Response.Body()))
	rotated, _ := jsonBody(t, refresh)["refresh_token"].(string)
	require.NotEmpty(t, rotated)
	refreshedClaims, err := verifyOAuth2JWT(bgCtx(), jsonBody(t, refresh)["access_token"].(string), cfg, signingKey, testClaudeCodeResource)
	require.NoError(t, err)
	assert.Equal(t, vk.ID, refreshedClaims.Subject)

	// Sign-out revokes the grant, so the rotated refresh token stops working.
	revoke := formPostCtx(url.Values{"token": {rotated}, "token_type_hint": {"refresh_token"}}.Encode())
	h.handleRevoke(revoke)
	require.Equal(t, fasthttp.StatusOK, revoke.Response.StatusCode())
	afterRevoke := formPostCtx(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rotated}}.Encode())
	h.handleToken(afterRevoke)
	assert.Equal(t, "invalid_grant", oauthErrorCode(t, afterRevoke))
}

func TestClaudeCodeGateway_DeviceCodeErrors(t *testing.T) {
	h, _, store, _ := newClaudeCodeGateway(t, false)

	t.Run("unknown device code", func(t *testing.T) {
		ctx := pollDeviceToken(h, "not-a-device-code")
		assert.Equal(t, "expired_token", oauthErrorCode(t, ctx))
	})

	t.Run("missing device code", func(t *testing.T) {
		ctx := pollDeviceToken(h, "")
		assert.Equal(t, "invalid_request", oauthErrorCode(t, ctx))
	})

	t.Run("expired device code", func(t *testing.T) {
		hash := hashSHA256Hex("expired-device-code")
		require.NoError(t, store.CreateOAuth2AuthorizeRequest(context.Background(), &configtables.TableOAuth2AuthorizeRequest{
			ID: "expired-device", ClientID: claudeCodeClientID, Resource: testClaudeCodeResource,
			Status: configtables.OAuth2AuthorizeRequestStatusPending, DeviceCodeHash: &hash,
			ExpiresAt: time.Now().Add(-time.Minute), CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}))
		ctx := pollDeviceToken(h, "expired-device-code")
		assert.Equal(t, "expired_token", oauthErrorCode(t, ctx))
	})

	t.Run("denied sign-in", func(t *testing.T) {
		hash := hashSHA256Hex("denied-device-code")
		require.NoError(t, store.CreateOAuth2AuthorizeRequest(context.Background(), &configtables.TableOAuth2AuthorizeRequest{
			ID: "denied-device", ClientID: claudeCodeClientID, Resource: testClaudeCodeResource,
			Status: configtables.OAuth2AuthorizeRequestStatusRevoked, DeviceCodeHash: &hash,
			ExpiresAt: time.Now().Add(time.Minute), CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}))
		ctx := pollDeviceToken(h, "denied-device-code")
		assert.Equal(t, "access_denied", oauthErrorCode(t, ctx))
	})

	t.Run("unsupported grant", func(t *testing.T) {
		ctx := formPostCtx("grant_type=authorization_code&code=x")
		h.handleToken(ctx)
		assert.Equal(t, "unsupported_grant_type", oauthErrorCode(t, ctx))
	})

	t.Run("unknown user code", func(t *testing.T) {
		ctx := jsonPostCtx(`{"user_code":"BCDF-GHJK"}`)
		h.handleDeviceVerify(ctx)
		assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())
	})

	t.Run("malformed user code", func(t *testing.T) {
		ctx := jsonPostCtx(`{"user_code":"BCD"}`)
		h.handleDeviceVerify(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	})
}

// TestClaudeCodeGateway_RefreshRejectsOtherClients pins that the gateway token
// endpoint only refreshes grants bound to the built-in Claude Code client: an
// MCP client's refresh token cannot be turned into a gateway token.
func TestClaudeCodeGateway_RefreshRejectsOtherClients(t *testing.T) {
	h, _, store, _ := newClaudeCodeGateway(t, false)
	cid := seedClient(t, store, []string{"http://127.0.0.1/cb"})
	seedConsentedRequest(t, store, "mcp-req", cid, "mcp-code", "challenge", "session", "sess-1", time.Now().Add(time.Minute))
	require.NoError(t, store.ConsumeOAuth2AuthorizeRequest(context.Background(), "mcp-req", &configtables.TableOAuth2RefreshToken{
		ID: "mcp-rt", TokenHash: hashSHA256Hex("mcp-refresh-token"), FamilyID: "mcp-req", ClientID: cid,
		BfMode: "session", BfSub: "sess-1", Resource: testMCPResource, CreatedAt: time.Now(),
	}))

	ctx := formPostCtx(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"mcp-refresh-token"}}.Encode())
	h.handleToken(ctx)
	assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	assert.Equal(t, "invalid_grant", oauthErrorCode(t, ctx))

	// Revoking it through the gateway is a no-op: only gateway grants are revocable there.
	revoke := formPostCtx("token=mcp-refresh-token")
	h.handleRevoke(revoke)
	assert.Equal(t, fasthttp.StatusOK, revoke.Response.StatusCode())
	rt, err := store.GetOAuth2RefreshTokenByHash(context.Background(), hashSHA256Hex("mcp-refresh-token"))
	require.NoError(t, err)
	assert.Nil(t, rt.RevokedAt)
}

func TestClaudeCodeGateway_BearerRejections(t *testing.T) {
	h, _, store, _ := newClaudeCodeGateway(t, true)
	signingKey, err := store.GetOAuth2SigningKey(bgCtx())
	require.NoError(t, err)
	gatewayPriv, err := parseRSAPrivateKeyPEM(signingKey.PrivateKeyPEM)
	require.NoError(t, err)

	inactive := &configtables.TableVirtualKey{ID: "vk-off", Name: "vk-off", Value: *schemas.NewSecretVar("sk-bf-off"), IsActive: new(false)}
	require.NoError(t, store.CreateVirtualKey(context.Background(), inactive))

	mint := func(mutate func(jwt.MapClaims)) string {
		return mintTestToken(t, gatewayPriv, signingKey.KID, func(c jwt.MapClaims) {
			c["aud"] = jwt.ClaimStrings{testClaudeCodeResource}
			if mutate != nil {
				mutate(c)
			}
		})
	}

	cases := map[string]string{
		"no bearer":               "",
		"virtual key as bearer":   "sk-bf-claude-code",
		"mcp audience":            mint(func(c jwt.MapClaims) { c["aud"] = jwt.ClaimStrings{testMCPResource} }),
		"expired":                 mint(func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() }),
		"inactive virtual key":    mint(func(c jwt.MapClaims) { c["sub"] = "vk-off" }),
		"deleted virtual key":     mint(func(c jwt.MapClaims) { c["sub"] = "vk-gone" }),
		"session while enforcing": mint(func(c jwt.MapClaims) { c["bf_mode"] = string(schemas.MCPAuthModeSession); c["sub"] = "sess-1" }),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			var req fasthttp.Request
			req.Header.SetMethod("POST")
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			ctx := initCtx(&req)
			called := false
			h.BearerMiddleware(func(*fasthttp.RequestCtx) { called = true })(ctx)
			assert.False(t, called)
			assert.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			assert.Equal(t, "false", string(ctx.Response.Header.Peek("x-should-retry")))
			body := jsonBody(t, ctx)
			assert.Equal(t, "error", body["type"])
			assert.Equal(t, "authentication_error", body["error"].(map[string]any)["type"])
		})
	}

	t.Run("session token allowed when auth is not enforced", func(t *testing.T) {
		cfg := h.store
		cfg.ClientConfig.EnforceAuthOnInference = false
		defer func() { cfg.ClientConfig.EnforceAuthOnInference = true }()
		ctx := bearerCtx(mint(func(c jwt.MapClaims) { c["bf_mode"] = string(schemas.MCPAuthModeSession); c["sub"] = "sess-1" }))
		called := false
		h.BearerMiddleware(func(c *fasthttp.RequestCtx) {
			called = true
			assert.Empty(t, string(c.Request.Header.Peek("Authorization")))
			assert.Empty(t, string(c.Request.Header.Peek("x-bf-vk")))
		})(ctx)
		assert.True(t, called)
	})
}

func TestClaudeCodeGateway_ManagedSettings(t *testing.T) {
	h, _, _, cfg := newClaudeCodeGateway(t, false)

	t.Run("no policy configured", func(t *testing.T) {
		ctx := getCtx("/claude-code/managed/settings")
		h.handleManagedSettings(ctx)
		assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())
	})

	cfg.ClientConfig.OAuth2ServerConfig.ClaudeCodeGateway.ManagedSettings = map[string]any{
		"permissions": map[string]any{"deny": []any{"Bash(curl <url>)"}},
		"env":         map[string]any{"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1"},
	}
	// The checksum Claude Code computes: sha256 over key-sorted JSON with no
	// whitespace and no HTML escaping.
	canonical := `{"env":{"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY":"1"},"permissions":{"deny":["Bash(curl <url>)"]}}`
	sum := sha256.Sum256([]byte(canonical))
	wantChecksum := "sha256:" + hex.EncodeToString(sum[:])

	t.Run("policy is served with its checksum", func(t *testing.T) {
		ctx := getCtx("/claude-code/managed/settings")
		h.handleManagedSettings(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
		body := jsonBody(t, ctx)
		assert.Equal(t, wantChecksum, body["checksum"])
		assert.Equal(t, wantChecksum, body["uuid"])
		assert.Equal(t, `"`+wantChecksum+`"`, string(ctx.Response.Header.Peek("ETag")))
		assert.Contains(t, body["settings"], "permissions")
	})

	t.Run("unchanged policy answers 304", func(t *testing.T) {
		ctx := getCtx("/claude-code/managed/settings")
		ctx.Request.Header.Set("If-None-Match", `"`+wantChecksum+`"`)
		h.handleManagedSettings(ctx)
		assert.Equal(t, fasthttp.StatusNotModified, ctx.Response.StatusCode())
	})

	t.Run("an empty policy is still a policy", func(t *testing.T) {
		cfg.ClientConfig.OAuth2ServerConfig.ClaudeCodeGateway.ManagedSettings = map[string]any{}
		ctx := getCtx("/claude-code/managed/settings")
		h.handleManagedSettings(ctx)
		assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	})
}

func TestClaudeCodeGateway_DeviceAuthorizationRateLimit(t *testing.T) {
	h, _, _, _ := newClaudeCodeGateway(t, false)
	for i := 0; i < claudeCodeDeviceAuthorizationLimit; i++ {
		ctx := formPostCtx("surface=claude_code")
		h.handleDeviceAuthorization(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	ctx := formPostCtx("surface=claude_code")
	h.handleDeviceAuthorization(ctx)
	assert.Equal(t, fasthttp.StatusTooManyRequests, ctx.Response.StatusCode())
	assert.Equal(t, "slow_down", oauthErrorCode(t, ctx))
}

func TestClaudeCodeUserCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		code, err := generateClaudeCodeUserCode()
		require.NoError(t, err)
		require.Regexp(t, `^[BCDFGHJKLMNPQRSTVWXZ]{4}-[BCDFGHJKLMNPQRSTVWXZ]{4}$`, code)
		seen[code] = true
	}
	assert.Greater(t, len(seen), 45)
	assert.Equal(t, "WDJBMJHT", normalizeClaudeCodeUserCode(" wdjb-mjht "))
	assert.Equal(t, "WDJBMJHT", normalizeClaudeCodeUserCode("WDJB MJHT"))
}
