package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/golang-jwt/jwt/v5"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

const testClaudeCodeResource = testIssuer + "/gateway/claude-code"

// newClaudeCodeGateway builds the gateway handler on a real sqlite store (full
// migrations, so the device-code columns exist) with the gateway enabled, plus a
// consent handler sharing the same config. /mcp is left on header auth: the
// gateway does not depend on the MCP auth mode.
func newClaudeCodeGateway(t testing.TB, enforceAuth bool) (*clientGatewayEndpoint, *OAuth2ConsentHandler, configstore.ConfigStore, *lib.Config) {
	t.Helper()
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, enforceAuth)
	cfg.ClientConfig.OAuth2ServerConfig.Gateways = &configtables.ClientGatewaysConfig{ClaudeCode: &configtables.ClaudeCodeGatewayConfig{Enabled: true}}
	return NewClientGatewayHandler(cfg, nil, nil, nil).endpoint("claude-code"), NewOAuth2ConsentHandler(cfg, nil, nil), store, cfg
}

// seedGatewayVK stores an active virtual key and returns it.
func seedGatewayVK(t testing.TB, store configstore.ConfigStore, id, value string) *configtables.TableVirtualKey {
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
func startDeviceSignIn(t *testing.T, h *clientGatewayEndpoint) (deviceCode, userCode string) {
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

func pollDeviceToken(h *clientGatewayEndpoint, deviceCode string) *fasthttp.RequestCtx {
	ctx := formPostCtx(url.Values{"grant_type": {deviceCodeGrantType}, "device_code": {deviceCode}}.Encode())
	h.handleToken(ctx)
	return ctx
}

// approveWithVK runs the verification page and consent page legs for userCode
// using a virtual key, and returns the consent redirect.
func approveWithVK(t *testing.T, h *clientGatewayEndpoint, consent *OAuth2ConsentHandler, userCode, vkValue string) string {
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

// otherGatewayProfile is a second client gateway for tests: Claude Code's
// protocol under another prefix, built-in client and audience.
type otherGatewayProfile struct{ claudeCodeProfile }

func (otherGatewayProfile) Name() string                                  { return "other-client" }
func (otherGatewayProfile) PathPrefix() string                            { return "/other-client" }
func (otherGatewayProfile) ClientID() string                              { return "bifrost-other-client" }
func (otherGatewayProfile) DisplayName() string                           { return "Other Client" }
func (otherGatewayProfile) Enabled(*configtables.OAuth2ServerConfig) bool { return true }

// TestClientGateway_GrantsStayOnTheirGateway pins that with several client
// gateways mounted, every grant is redeemable only on the gateway whose built-in
// client it was issued to: a device code, a refresh token and an access token
// from one gateway are all refused by another, and revoking at the wrong gateway
// leaves the grant alone.
func TestClientGateway_GrantsStayOnTheirGateway(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	cfg.ClientConfig.OAuth2ServerConfig.Gateways = &configtables.ClientGatewaysConfig{ClaudeCode: &configtables.ClaudeCodeGatewayConfig{Enabled: true}}
	gateways := NewClientGatewayHandler(cfg, nil, nil, nil, claudeCodeProfile{}, otherGatewayProfile{})
	claude, other := gateways.endpoint("claude-code"), gateways.endpoint("other-client")
	consent := NewOAuth2ConsentHandler(cfg, nil, nil)
	vk := seedGatewayVK(t, store, "vk-cross", "sk-bf-cross")

	deviceCode, userCode := startDeviceSignIn(t, claude)

	approveWithVK(t, claude, consent, userCode, vk.Value.GetValue())
	assert.Equal(t, "expired_token", oauthErrorCode(t, pollDeviceToken(other, deviceCode)), "device code redeemed at the wrong gateway")

	issued := pollDeviceToken(claude, deviceCode)
	require.Equal(t, fasthttp.StatusOK, issued.Response.StatusCode(), string(issued.Response.Body()))
	accessToken, _ := jsonBody(t, issued)["access_token"].(string)
	refreshToken, _ := jsonBody(t, issued)["refresh_token"].(string)

	refreshElsewhere := formPostCtx(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}.Encode())
	other.handleToken(refreshElsewhere)
	assert.Equal(t, "invalid_grant", oauthErrorCode(t, refreshElsewhere), "refresh token used at the wrong gateway")

	called := false
	elsewhere := bearerCtx(accessToken)
	other.BearerMiddleware(func(*fasthttp.RequestCtx) { called = true })(elsewhere)
	assert.False(t, called)
	assert.Equal(t, fasthttp.StatusUnauthorized, elsewhere.Response.StatusCode(), "access token accepted by the wrong gateway")

	revokeElsewhere := formPostCtx(url.Values{"token": {refreshToken}}.Encode())
	other.handleRevoke(revokeElsewhere)
	require.Equal(t, fasthttp.StatusOK, revokeElsewhere.Response.StatusCode())
	stillValid := formPostCtx(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}.Encode())
	claude.handleToken(stillValid)
	assert.Equal(t, fasthttp.StatusOK, stillValid.Response.StatusCode(), "revoking at the wrong gateway must not revoke the grant: %s", stillValid.Response.Body())
}

// TestClientGateway_SharedDeviceVerify pins the one /oauth2/device/verify every
// gateway shares: it hands out the consent page for a sign-in from any enabled
// gateway, and refuses a code whose own gateway was switched off after the
// sign-in started, even while another gateway stays on.
func TestClientGateway_SharedDeviceVerify(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	cfg.ClientConfig.OAuth2ServerConfig.Gateways = &configtables.ClientGatewaysConfig{ClaudeCode: &configtables.ClaudeCodeGatewayConfig{Enabled: true}}
	gateways := NewClientGatewayHandler(cfg, nil, nil, nil, claudeCodeProfile{}, otherGatewayProfile{})
	verify := func(userCode string) *fasthttp.RequestCtx {
		ctx := jsonPostCtx(`{"user_code":"` + userCode + `"}`)
		gateways.handleDeviceVerify(ctx)
		return ctx
	}

	_, claudeCode := startDeviceSignIn(t, gateways.endpoint("claude-code"))
	_, otherCode := startDeviceSignIn(t, gateways.endpoint("other-client"))
	for name, code := range map[string]string{"claude-code": claudeCode, "other-client": otherCode} {
		ctx := verify(code)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "%s: %s", name, ctx.Response.Body())
		assert.True(t, strings.HasPrefix(jsonBody(t, ctx)["consent_url"].(string), testIssuer+"/oauth/consent?flow="), name)
	}

	cfg.ClientConfig.OAuth2ServerConfig.Gateways.ClaudeCode.Enabled = false
	assert.Equal(t, fasthttp.StatusNotFound, verify(claudeCode).Response.StatusCode(), "code of a switched-off gateway")
	assert.Equal(t, fasthttp.StatusOK, verify(otherCode).Response.StatusCode(), "the other gateway is unaffected")
}

func TestClaudeCodeGateway_DisabledAnswers404(t *testing.T) {
	for name, mutate := range map[string]func(*lib.Config){
		"toggle off":       func(cfg *lib.Config) { cfg.ClientConfig.OAuth2ServerConfig.Gateways.ClaudeCode.Enabled = false },
		"toggle unset":     func(cfg *lib.Config) { cfg.ClientConfig.OAuth2ServerConfig.Gateways.ClaudeCode = nil },
		"issuer url unset": func(cfg *lib.Config) { cfg.ClientConfig.OAuth2ServerConfig.IssuerURL = nil },
	} {
		t.Run(name, func(t *testing.T) {
			h, _, _, cfg := newClaudeCodeGateway(t, false)
			mutate(cfg)

			for route, ctx := range map[string]*fasthttp.RequestCtx{
				"metadata":             getCtx("/gateway/claude-code/.well-known/oauth-authorization-server"),
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
	ctx := getCtx("/gateway/claude-code/.well-known/oauth-authorization-server")
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

	// The token is bound to the /gateway/claude-code audience: valid here, refused on /mcp.
	signingKey, err := store.GetOAuth2SigningKey(bgCtx())
	require.NoError(t, err)
	claims, err := verifyBifrostJWT(bgCtx(), accessToken, cfg, signingKey, testClaudeCodeResource)
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
	var seenGatewayUser bool
	h.BearerMiddleware(func(ctx *fasthttp.RequestCtx) {
		seenVK = string(ctx.Request.Header.Peek("x-bf-vk"))
		seenAuth = string(ctx.Request.Header.Peek("Authorization"))
		seenAPIKey = string(ctx.Request.Header.Peek("x-api-key"))
		_, seenGatewayUser = lib.PreAuthenticated(ctx)
	})(inference)
	assert.Equal(t, vk.Value.GetValue(), seenVK)
	assert.Empty(t, seenAuth)
	assert.Empty(t, seenAPIKey)
	assert.False(t, seenGatewayUser, "a vk-mode request is authenticated by its x-bf-vk downstream, not by the user marker")

	// Claude Code refreshes with only grant_type and refresh_token.
	refresh := formPostCtx(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}.Encode())
	h.handleToken(refresh)
	require.Equal(t, fasthttp.StatusOK, refresh.Response.StatusCode(), string(refresh.Response.Body()))
	rotated, _ := jsonBody(t, refresh)["refresh_token"].(string)
	require.NotEmpty(t, rotated)
	refreshedClaims, err := verifyBifrostJWT(bgCtx(), jsonBody(t, refresh)["access_token"].(string), cfg, signingKey, testClaudeCodeResource)
	require.NoError(t, err)
	assert.Equal(t, vk.ID, refreshedClaims.Subject)

	// The grant is bound to /gateway/claude-code, so the /oauth2/token refresh path (the
	// handler it dispatches to when MCP OAuth is on) refuses it, even when the
	// caller names the built-in client.
	viaMCP := formPostCtx(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rotated}, "client_id": {claudeCodeClientID}}.Encode())
	h.issuance.handleTokenRefresh(viaMCP)
	assert.Equal(t, "invalid_grant", oauthErrorCode(t, viaMCP))

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
	past := time.Now().Add(-time.Hour)
	expired := &configtables.TableVirtualKey{ID: "vk-expired", Name: "vk-expired", Value: *schemas.NewSecretVar("sk-bf-expired"), IsActive: new(true), ExpiresAt: &past}
	require.NoError(t, store.CreateVirtualKey(context.Background(), expired))

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
		"expired virtual key":     mint(func(c jwt.MapClaims) { c["sub"] = "vk-expired" }),
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
			_, ok := lib.PreAuthenticated(c)
			assert.False(t, ok, "an anonymous session must not be marked as an authenticated user")
		})(ctx)
		assert.True(t, called)
	})
}

// TestClaudeCodeGateway_BearerUserMode pins the user-mode bearer path: an
// active user reaches the inference handlers attributed to that user, with the
// gateway token recorded as the credential (as /mcp does) and every other
// credential header stripped; a deactivated user is told to sign in again, and a
// failed liveness lookup is retriable rather than a sign-out.
func TestClaudeCodeGateway_BearerUserMode(t *testing.T) {
	h, _, store, _ := newClaudeCodeGateway(t, true)
	signingKey, err := store.GetOAuth2SigningKey(bgCtx())
	require.NoError(t, err)
	gatewayPriv, err := parseRSAPrivateKeyPEM(signingKey.PrivateKeyPEM)
	require.NoError(t, err)
	userToken := mintTestToken(t, gatewayPriv, signingKey.KID, func(c jwt.MapClaims) {
		c["aud"] = jwt.ClaimStrings{testClaudeCodeResource}
		c["bf_mode"] = string(schemas.MCPAuthModeUser)
		c["sub"] = "user-42"
	})

	t.Run("active user is attributed and credentials are stripped", func(t *testing.T) {
		h.identityResolver = &fakeResolver{userModeAvailable: true}
		ctx := bearerCtx(userToken)
		ctx.Request.Header.Set("x-api-key", "sk-ant-someone-else")
		ctx.Request.Header.Set("x-bf-vk", "sk-bf-someone-else")
		called := false
		h.BearerMiddleware(func(c *fasthttp.RequestCtx) {
			called = true
			assert.Equal(t, "user-42", c.UserValue(schemas.BifrostContextKeyUserID))
			assert.Equal(t, grant.NewCredential(grant.CredentialMCPToken, "user-42"), c.UserValue(schemas.BifrostContextKeyAuthCredential))
			assert.Empty(t, string(c.Request.Header.Peek("Authorization")))
			assert.Empty(t, string(c.Request.Header.Peek("x-api-key")))
			assert.Empty(t, string(c.Request.Header.Peek("x-bf-vk")))
			// The enterprise identity middleware runs next and finds no bearer
			// left; this marker is what tells it the user is already authenticated.
			method, ok := lib.PreAuthenticated(c)
			assert.True(t, ok, "user-mode request must be marked pre-authenticated")
			assert.Equal(t, lib.PreAuthClientGateway, method)
		})(ctx)
		assert.True(t, called, string(ctx.Response.Body()))
	})

	t.Run("deactivated user must sign in again", func(t *testing.T) {
		h.identityResolver = &fakeResolver{userModeAvailable: true, userInactive: true}
		ctx := bearerCtx(userToken)
		called := false
		h.BearerMiddleware(func(*fasthttp.RequestCtx) { called = true })(ctx)
		assert.False(t, called)
		assert.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.Equal(t, "false", string(ctx.Response.Header.Peek("x-should-retry")))
		assert.Equal(t, "authentication_error", jsonBody(t, ctx)["error"].(map[string]any)["type"])
	})

	t.Run("liveness lookup failure is retriable", func(t *testing.T) {
		h.identityResolver = &fakeResolver{userModeAvailable: true, userActiveErr: errors.New("idp unreachable")}
		ctx := bearerCtx(userToken)
		called := false
		h.BearerMiddleware(func(*fasthttp.RequestCtx) { called = true })(ctx)
		assert.False(t, called)
		assert.Equal(t, fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.Empty(t, string(ctx.Response.Header.Peek("x-should-retry")))
	})
}

// TestClientGateway_DeviceVerifyNeedsNoCredentials pins that /oauth2/device/verify stays public:
// a developer with no dashboard account calls it. Registering the routes with the OSS /api auth
// chain (setup lock, dashboard auth) must not put it behind either, in both auth states.
func TestClientGateway_DeviceVerifyNeedsNoCredentials(t *testing.T) {
	for name, authConfig := range map[string]*configstore.AuthConfig{
		"dashboard auth enabled": {AdminUserName: schemas.NewSecretVar("admin"), AdminPassword: schemas.NewSecretVar("hashed"), IsEnabled: true},
		"setup lock active":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			h, _, _, _ := newClaudeCodeGateway(t, false)
			_, userCode := startDeviceSignIn(t, h)
			am := newSetupLockedAuthMiddleware("setup-secret", authConfig)
			r := router.New()
			h.ClientGatewayHandler.RegisterRoutes(r, am.SetupLockMiddleware(), am.APIMiddleware())

			ctx := jsonPostCtx(`{"user_code":"` + userCode + `"}`)
			ctx.Request.SetRequestURI("/oauth2/device/verify")
			r.Handler(ctx)
			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			assert.Contains(t, jsonBody(t, ctx)["consent_url"], "/oauth/consent?flow=")
		})
	}
}

// TestClientGateway_BearerRunsInsideOuterMiddlewares pins where the bearer check sits
// in the inference chain: after the outer middlewares (tracing, recovery), so its own
// rejections are traced and a panic in it is recovered, and before the auth
// middlewares, which must see the identity it resolved rather than the raw token.
func TestClientGateway_BearerRunsInsideOuterMiddlewares(t *testing.T) {
	h, _, _, _ := newClaudeCodeGateway(t, true)
	var outerSaw, authSaw bool
	outer := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) { outerSaw = true; next(ctx) }
	}
	auth := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) { authSaw = true; next(ctx) }
	}
	r := router.New()
	h.ClientGatewayHandler.RegisterInferenceRoutes(r, nil, nil, 1, outer, auth)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI(claudeCodeGatewayPathPrefix + "/v1/messages")
	r.Handler(ctx)

	assert.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.True(t, outerSaw, "the outer middlewares must see a request the gateway rejects, or its 401s go untraced")
	assert.False(t, authSaw, "the auth middlewares run after the bearer check, so a rejected request never reaches them")
}

func TestClaudeCodeGateway_DeviceAuthorizationRateLimit(t *testing.T) {
	h, _, _, _ := newClaudeCodeGateway(t, false)
	for i := 0; i < clientGatewayDeviceAuthorizationLimit; i++ {
		ctx := formPostCtx("surface=claude_code")
		h.handleDeviceAuthorization(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	ctx := formPostCtx("surface=claude_code")
	h.handleDeviceAuthorization(ctx)
	assert.Equal(t, fasthttp.StatusTooManyRequests, ctx.Response.StatusCode())
	assert.Equal(t, "slow_down", oauthErrorCode(t, ctx))
}

// TestClaudeCodeGatewayTokenCache pins the verified-token cache: an entry only
// answers for the resource it was verified against and only until the token
// expires, tokens without an expiry are not kept, and the cache never grows past
// its cap.
func TestClaudeCodeGatewayTokenCache(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	claims := func(exp time.Time) *jwtMCPClaims {
		return &jwtMCPClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: "vk-1", ExpiresAt: jwt.NewNumericDate(exp)}}
	}
	c := newGatewayTokenCache(2)
	c.put("tok-a", testClaudeCodeResource, claims(now.Add(time.Minute)), now)

	assert.NotNil(t, c.get("tok-a", testClaudeCodeResource, now.Add(30*time.Second)))
	assert.Nil(t, c.get("tok-a", "https://moved.example.com/gateway/claude-code", now), "another resource is a miss")
	assert.Nil(t, c.get("tok-a", testClaudeCodeResource, now.Add(time.Minute)), "an expired token is a miss")
	assert.Nil(t, c.get("tok-b", testClaudeCodeResource, now))

	c.put("tok-noexp", testClaudeCodeResource, &jwtMCPClaims{}, now)
	assert.Nil(t, c.get("tok-noexp", testClaudeCodeResource, now), "a token without exp is not cached")

	c.put("tok-b", testClaudeCodeResource, claims(now.Add(time.Minute)), now)
	c.put("tok-c", testClaudeCodeResource, claims(now.Add(time.Minute)), now)
	assert.Len(t, c.entries, 2, "a full cache does not grow")
	assert.Nil(t, c.get("tok-c", testClaudeCodeResource, now))

	later := now.Add(2 * time.Minute)
	c.put("tok-c", testClaudeCodeResource, claims(later.Add(time.Minute)), later)
	assert.NotNil(t, c.get("tok-c", testClaudeCodeResource, later), "expired entries are swept to make room")
	assert.LessOrEqual(t, len(c.entries), 2)
}

// TestClaudeCodeGateway_CachedTokenStillChecked pins that a token served from
// the verified-token cache is still refused once the issuer moves (its audience
// no longer matches) and once the virtual key behind it is deactivated.
func TestClaudeCodeGateway_CachedTokenStillChecked(t *testing.T) {
	h, _, store, cfg := newClaudeCodeGateway(t, true)
	vk := seedGatewayVK(t, store, "vk-cached", "sk-bf-cached")
	signingKey, err := store.GetOAuth2SigningKey(bgCtx())
	require.NoError(t, err)
	priv, err := parseRSAPrivateKeyPEM(signingKey.PrivateKeyPEM)
	require.NoError(t, err)
	token := mintTestToken(t, priv, signingKey.KID, func(c jwt.MapClaims) {
		c["aud"] = jwt.ClaimStrings{testClaudeCodeResource}
		c["sub"] = vk.ID
	})
	run := func() int {
		ctx := bearerCtx(token)
		h.BearerMiddleware(func(c *fasthttp.RequestCtx) { c.SetStatusCode(fasthttp.StatusOK) })(ctx)
		return ctx.Response.StatusCode()
	}

	require.Equal(t, fasthttp.StatusOK, run())
	require.NotNil(t, h.tokens.get(token, testClaudeCodeResource, time.Now()), "the verified token is cached")
	require.Equal(t, fasthttp.StatusOK, run(), "served from the cache")

	t.Run("issuer change", func(t *testing.T) {
		original := cfg.ClientConfig.OAuth2ServerConfig.IssuerURL
		cfg.ClientConfig.OAuth2ServerConfig.IssuerURL = schemas.NewSecretVar("https://moved.example.com")
		defer func() { cfg.ClientConfig.OAuth2ServerConfig.IssuerURL = original }()
		assert.Equal(t, fasthttp.StatusUnauthorized, run())
	})

	t.Run("deactivated virtual key", func(t *testing.T) {
		require.Equal(t, fasthttp.StatusOK, run())
		vk.IsActive = new(false)
		require.NoError(t, store.UpdateVirtualKey(context.Background(), vk))
		assert.Equal(t, fasthttp.StatusUnauthorized, run())
	})
}

// staticVKCache is a warm governance virtual-key cache for the benchmark.
type staticVKCache map[string]*configtables.TableVirtualKey

func (c staticVKCache) GetVirtualKeyByID(_ context.Context, id string) (*configtables.TableVirtualKey, bool) {
	vk, ok := c[id]
	return vk, ok
}

// BenchmarkClaudeCodeGatewayBearerMiddleware measures what the bearer
// middleware adds to every gateway request in steady state: the enabled check,
// RS256 verification against the cached signing key, the identity re-check
// (a warm virtual-key cache hit, or the user resolver), and the header rewrite.
func BenchmarkClaudeCodeGatewayBearerMiddleware(b *testing.B) {
	h, _, store, _ := newClaudeCodeGateway(b, true)
	vk := seedGatewayVK(b, store, "vk-bench", "sk-bf-bench")
	h.vkCache = staticVKCache{vk.ID: vk}
	h.identityResolver = &fakeResolver{userModeAvailable: true}
	signingKey, err := store.GetOAuth2SigningKey(bgCtx())
	require.NoError(b, err)
	priv, err := parseRSAPrivateKeyPEM(signingKey.PrivateKeyPEM)
	require.NoError(b, err)
	// Warm the process-lifetime signing-key cache, as any running server has.
	_, err = h.store.GetOAuth2SigningKey(bgCtx())
	require.NoError(b, err)

	for _, mode := range []struct {
		name string
		sub  string
		bf   schemas.MCPAuthMode
	}{
		{"vk", vk.ID, schemas.MCPAuthModeVK},
		{"user", "user-bench", schemas.MCPAuthModeUser},
	} {
		token := "Bearer " + mintTestToken(b, priv, signingKey.KID, func(c jwt.MapClaims) {
			c["aud"] = jwt.ClaimStrings{testClaudeCodeResource}
			c["bf_mode"] = string(mode.bf)
			c["sub"] = mode.sub
		})
		b.Run(mode.name, func(b *testing.B) {
			var req fasthttp.Request
			req.Header.SetMethod("POST")
			ctx := initCtx(&req)
			passed := 0
			next := h.BearerMiddleware(func(*fasthttp.RequestCtx) { passed++ })
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ctx.Request.Header.Set("Authorization", token)
				next(ctx)
			}
			b.StopTimer()
			require.Equal(b, b.N, passed, string(ctx.Response.Body()))
		})
	}
}

// TestClaudeCodeIPRateLimiter pins the sign-in limiter's per-key window and its
// memory bound: past maxKeys live windows a new address is refused rather than
// tracked, existing addresses keep their own counts, and once their windows
// expire a sweep frees room again.
func TestClaudeCodeIPRateLimiter(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)

	t.Run("per-key fixed window", func(t *testing.T) {
		l := newIPRateLimiter(2, time.Minute)
		assert.True(t, l.allow("a", start))
		assert.True(t, l.allow("a", start.Add(time.Second)))
		assert.False(t, l.allow("a", start.Add(2*time.Second)), "third hit inside the window")
		assert.True(t, l.allow("b", start.Add(2*time.Second)), "another address has its own window")
		assert.True(t, l.allow("a", start.Add(time.Minute)), "a new window starts once the old one ends")
	})

	t.Run("bounded number of tracked addresses", func(t *testing.T) {
		l := newIPRateLimiter(5, time.Minute)
		l.maxKeys = 3
		for _, key := range []string{"a", "b", "c"} {
			require.True(t, l.allow(key, start))
		}
		assert.False(t, l.allow("d", start.Add(time.Second)), "a new address past the cap is refused")
		assert.True(t, l.allow("a", start.Add(time.Second)), "a tracked address keeps its budget")
		assert.Len(t, l.windows, 3)

		later := start.Add(time.Minute + time.Second)
		assert.True(t, l.allow("d", later), "expired windows are swept to make room")
		assert.LessOrEqual(t, len(l.windows), 3)
	})
}

func TestClaudeCodeUserCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		code, err := generateGatewayUserCode()
		require.NoError(t, err)
		require.Regexp(t, `^[BCDFGHJKLMNPQRSTVWXZ]{4}-[BCDFGHJKLMNPQRSTVWXZ]{4}$`, code)
		seen[code] = true
	}
	assert.Greater(t, len(seen), 45)
	assert.Equal(t, "WDJBMJHT", normalizeGatewayUserCode(" wdjb-mjht "))
	assert.Equal(t, "WDJBMJHT", normalizeGatewayUserCode("WDJB MJHT"))
}
