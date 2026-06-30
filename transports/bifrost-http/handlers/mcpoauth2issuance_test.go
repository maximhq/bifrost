package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// newRealOAuth2Store builds a real sqlite-backed ConfigStore (full migrations,
// including the OAuth2 issuance tables) so issuance handlers exercise the actual
// atomic store semantics rather than a hand-rolled mock.
func newRealOAuth2Store(t *testing.T) configstore.ConfigStore {
	t.Helper()
	cs, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "oauth2.db")},
	}, &mockLogger{})
	require.NoError(t, err)
	require.NotNil(t, cs)
	return cs
}

func newIssuanceHandler(t *testing.T) (*OAuth2IssuanceHandler, configstore.ConfigStore, *lib.Config) {
	t.Helper()
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
	return NewOAuth2IssuanceHandler(cfg, nil, nil), store, cfg
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// initCtx prepares a RequestCtx for handler use. Init sets a (fake) server so
// that, when the handler passes ctx to the store as a context.Context, the
// database/sql layer's ctx.Done() call does not panic on a bare RequestCtx.
func initCtx(req *fasthttp.Request) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Init(req, nil, nil)
	return ctx
}

// bgCtx returns an initialized, empty RequestCtx for store-backed calls made
// directly from a test (e.g. verifying a minted token against the live store).
func bgCtx() *fasthttp.RequestCtx {
	var req fasthttp.Request
	return initCtx(&req)
}

func formPostCtx(body string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/x-www-form-urlencoded")
	req.SetBodyString(body)
	return initCtx(&req)
}

func getCtx(uri string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod("GET")
	req.SetRequestURI(uri)
	return initCtx(&req)
}

// seedClient registers a client directly via the store and returns its client_id.
func seedClient(t *testing.T, store configstore.ConfigStore, redirectURIs []string) string {
	t.Helper()
	client := &configtables.TableOAuth2Client{
		ID:           "client-row-1",
		ClientID:     "client-1",
		ClientName:   "Test Client",
		RedirectURIs: redirectURIs,
		GrantTypes:   []string{"authorization_code"},
		Scope:        "mcp",
		CreatedAt:    time.Now(),
	}
	require.NoError(t, store.CreateOAuth2Client(context.Background(), client))
	return client.ClientID
}

// seedConsentedRequest stores a consented authorize request bound to the given
// identity, with a code hash and PKCE challenge, returning the plaintext code.
func seedConsentedRequest(t *testing.T, store configstore.ConfigStore, id, clientID, code, challenge, bfMode, bfSub string, expires time.Time) {
	t.Helper()
	h := hashSHA256Hex(code)
	req := &configtables.TableOAuth2AuthorizeRequest{
		ID:                  id,
		ClientID:            clientID,
		RedirectURI:         "http://127.0.0.1/cb",
		State:               "state",
		Scope:               "mcp",
		Resource:            testMCPResource,
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
		Status:              configtables.OAuth2AuthorizeRequestStatusConsented,
		BfMode:              bfMode,
		BfSub:               bfSub,
		CodeHash:            &h,
		ExpiresAt:           expires,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}
	require.NoError(t, store.CreateOAuth2AuthorizeRequest(context.Background(), req))
}

func TestHandleRegister_DCR(t *testing.T) {
	t.Run("valid registration returns 201 with defaults", func(t *testing.T) {
		h, _, _ := newIssuanceHandler(t)
		ctx := formPostCtx("")
		ctx.Request.SetBodyString(`{"client_name":"Cli","redirect_uris":["http://127.0.0.1:1234/cb"]}`)
		ctx.Request.Header.SetContentType("application/json")

		h.handleRegister(ctx)
		require.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode())

		var resp map[string]any
		require.NoError(t, json.Unmarshal(ctx.Response.Body(), &resp))
		assert.NotEmpty(t, resp["client_id"])
		assert.Equal(t, "none", resp["token_endpoint_auth_method"])
		assert.Equal(t, "mcp", resp["scope"])
		assert.Equal(t, []any{"authorization_code"}, resp["grant_types"])
	})

	t.Run("missing redirect_uris is rejected", func(t *testing.T) {
		h, _, _ := newIssuanceHandler(t)
		ctx := formPostCtx("")
		ctx.Request.SetBodyString(`{"client_name":"Cli"}`)
		ctx.Request.Header.SetContentType("application/json")

		h.handleRegister(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "invalid_redirect_uri")
	})

	t.Run("non-public auth method is rejected", func(t *testing.T) {
		h, _, _ := newIssuanceHandler(t)
		ctx := formPostCtx("")
		ctx.Request.SetBodyString(`{"redirect_uris":["http://127.0.0.1/cb"],"token_endpoint_auth_method":"client_secret_basic"}`)
		ctx.Request.Header.SetContentType("application/json")

		h.handleRegister(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "invalid_client_metadata")
	})

	t.Run("malformed JSON is rejected", func(t *testing.T) {
		h, _, _ := newIssuanceHandler(t)
		ctx := formPostCtx("")
		ctx.Request.SetBodyString(`{not json`)
		ctx.Request.Header.SetContentType("application/json")

		h.handleRegister(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "invalid_request")
	})

	t.Run("unsupported grant_type is rejected", func(t *testing.T) {
		h, _, _ := newIssuanceHandler(t)
		ctx := formPostCtx("")
		ctx.Request.SetBodyString(`{"redirect_uris":["http://127.0.0.1/cb"],"grant_types":["client_credentials"]}`)
		ctx.Request.Header.SetContentType("application/json")

		h.handleRegister(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "invalid_client_metadata")
	})

	t.Run("unsupported response_type is rejected", func(t *testing.T) {
		h, _, _ := newIssuanceHandler(t)
		ctx := formPostCtx("")
		ctx.Request.SetBodyString(`{"redirect_uris":["http://127.0.0.1/cb"],"response_types":["token"]}`)
		ctx.Request.Header.SetContentType("application/json")

		h.handleRegister(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "invalid_client_metadata")
	})
}

func TestHandleAuthorize(t *testing.T) {
	base := func(clientID, redirect string) url.Values {
		v := url.Values{}
		v.Set("client_id", clientID)
		v.Set("redirect_uri", redirect)
		v.Set("response_type", "code")
		v.Set("code_challenge", "challenge")
		v.Set("code_challenge_method", "S256")
		v.Set("resource", testMCPResource)
		v.Set("state", "xyz")
		return v
	}

	t.Run("happy path redirects to the consent page", func(t *testing.T) {
		h, store, _ := newIssuanceHandler(t)
		cid := seedClient(t, store, []string{"http://127.0.0.1:1234/cb"})
		ctx := getCtx("/oauth2/authorize?" + base(cid, "http://127.0.0.1:1234/cb").Encode())

		h.handleAuthorize(ctx)
		require.Equal(t, fasthttp.StatusFound, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Header.Peek("Location")), "/oauth/consent?flow=")
	})

	t.Run("loopback redirect matches on any port", func(t *testing.T) {
		h, store, _ := newIssuanceHandler(t)
		cid := seedClient(t, store, []string{"http://127.0.0.1:1234/cb"})
		// Registered port 1234, request uses 55555 — must still match (RFC 8252).
		ctx := getCtx("/oauth2/authorize?" + base(cid, "http://127.0.0.1:55555/cb").Encode())

		h.handleAuthorize(ctx)
		assert.Equal(t, fasthttp.StatusFound, ctx.Response.StatusCode())
	})

	t.Run("unknown client_id is rejected", func(t *testing.T) {
		h, _, _ := newIssuanceHandler(t)
		ctx := getCtx("/oauth2/authorize?" + base("nope", "http://127.0.0.1/cb").Encode())

		h.handleAuthorize(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "invalid_client")
	})

	t.Run("unregistered redirect_uri is rejected", func(t *testing.T) {
		h, store, _ := newIssuanceHandler(t)
		cid := seedClient(t, store, []string{"http://127.0.0.1/cb"})
		ctx := getCtx("/oauth2/authorize?" + base(cid, "https://evil.example/cb").Encode())

		h.handleAuthorize(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "invalid_redirect_uri")
	})

	// Once client + redirect validate, protocol errors redirect back to the client.
	redirectCases := []struct {
		name    string
		mutate  func(url.Values)
		errCode string
	}{
		{"non-code response_type", func(v url.Values) { v.Set("response_type", "token") }, "unsupported_response_type"},
		{"non-S256 challenge method", func(v url.Values) { v.Set("code_challenge_method", "plain") }, "invalid_request"},
		{"mismatched resource", func(v url.Values) { v.Set("resource", "https://evil.example/mcp") }, "invalid_target"},
		{"scope exceeds registered", func(v url.Values) { v.Set("scope", "mcp admin") }, "invalid_scope"},
	}
	for _, tc := range redirectCases {
		t.Run(tc.name+" redirects with error", func(t *testing.T) {
			h, store, _ := newIssuanceHandler(t)
			cid := seedClient(t, store, []string{"http://127.0.0.1/cb"})
			v := base(cid, "http://127.0.0.1/cb")
			tc.mutate(v)
			ctx := getCtx("/oauth2/authorize?" + v.Encode())

			h.handleAuthorize(ctx)
			require.Equal(t, fasthttp.StatusFound, ctx.Response.StatusCode())
			assert.Contains(t, string(ctx.Response.Header.Peek("Location")), "error="+tc.errCode)
		})
	}

	// RFC 8707: this server exposes exactly one protected resource (/mcp), so a
	// client that omits resource defaults to the canonical one and proceeds.
	t.Run("omitted resource defaults to canonical and proceeds", func(t *testing.T) {
		h, store, _ := newIssuanceHandler(t)
		cid := seedClient(t, store, []string{"http://127.0.0.1/cb"})
		v := base(cid, "http://127.0.0.1/cb")
		v.Del("resource")
		ctx := getCtx("/oauth2/authorize?" + v.Encode())

		h.handleAuthorize(ctx)
		require.Equal(t, fasthttp.StatusFound, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Header.Peek("Location")), "/oauth/consent?flow=")
	})
}

func TestHandleToken_AuthorizationCode(t *testing.T) {
	const verifier = "test-verifier-string-of-sufficient-length-1234567890"
	challenge := pkceChallenge(verifier)

	t.Run("happy path issues a verifiable token pair", func(t *testing.T) {
		h, store, cfg := newIssuanceHandler(t)
		cid := seedClient(t, store, []string{"http://127.0.0.1/cb"})
		seedConsentedRequest(t, store, "req-1", cid, "code-1", challenge, "session", "sess-1", time.Now().Add(time.Minute))

		body := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {"code-1"},
			"code_verifier": {verifier},
			"client_id":     {cid},
			"redirect_uri":  {"http://127.0.0.1/cb"},
		}.Encode()
		ctx := formPostCtx(body)
		h.handleToken(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

		var resp map[string]any
		require.NoError(t, json.Unmarshal(ctx.Response.Body(), &resp))
		assert.Equal(t, "Bearer", resp["token_type"])
		assert.NotEmpty(t, resp["refresh_token"])

		// The minted access token verifies under the same issuer/signing key.
		claims, err := verifyMCPJWT(bgCtx(), resp["access_token"].(string), cfg)
		require.NoError(t, err)
		assert.Equal(t, "session", claims.BfMode)
		assert.Equal(t, "sess-1", claims.Subject)
	})

	t.Run("PKCE mismatch is rejected", func(t *testing.T) {
		h, store, _ := newIssuanceHandler(t)
		cid := seedClient(t, store, []string{"http://127.0.0.1/cb"})
		seedConsentedRequest(t, store, "req-1", cid, "code-1", challenge, "session", "sess-1", time.Now().Add(time.Minute))

		ctx := formPostCtx(url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {"code-1"},
			"code_verifier": {"wrong-verifier"},
			"client_id":     {cid},
			"redirect_uri":  {"http://127.0.0.1/cb"},
		}.Encode())
		h.handleToken(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "invalid_grant")
	})

	t.Run("code is single-use", func(t *testing.T) {
		h, store, _ := newIssuanceHandler(t)
		cid := seedClient(t, store, []string{"http://127.0.0.1/cb"})
		seedConsentedRequest(t, store, "req-1", cid, "code-1", challenge, "session", "sess-1", time.Now().Add(time.Minute))

		body := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {"code-1"},
			"code_verifier": {verifier},
			"client_id":     {cid},
			"redirect_uri":  {"http://127.0.0.1/cb"},
		}.Encode()
		first := formPostCtx(body)
		h.handleToken(first)
		require.Equal(t, fasthttp.StatusOK, first.Response.StatusCode())

		second := formPostCtx(body)
		h.handleToken(second)
		assert.Equal(t, fasthttp.StatusBadRequest, second.Response.StatusCode())
		assert.Contains(t, string(second.Response.Body()), "invalid_grant")
	})

	t.Run("expired code is rejected", func(t *testing.T) {
		h, store, _ := newIssuanceHandler(t)
		cid := seedClient(t, store, []string{"http://127.0.0.1/cb"})
		seedConsentedRequest(t, store, "req-1", cid, "code-1", challenge, "session", "sess-1", time.Now().Add(-time.Minute))

		ctx := formPostCtx(url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {"code-1"},
			"code_verifier": {verifier},
			"client_id":     {cid},
			"redirect_uri":  {"http://127.0.0.1/cb"},
		}.Encode())
		h.handleToken(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "invalid_grant")
	})

	t.Run("client_id mismatch is rejected", func(t *testing.T) {
		h, store, _ := newIssuanceHandler(t)
		cid := seedClient(t, store, []string{"http://127.0.0.1/cb"})
		seedConsentedRequest(t, store, "req-1", cid, "code-1", challenge, "session", "sess-1", time.Now().Add(time.Minute))

		ctx := formPostCtx(url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {"code-1"},
			"code_verifier": {verifier},
			"client_id":     {"other-client"},
			"redirect_uri":  {"http://127.0.0.1/cb"},
		}.Encode())
		h.handleToken(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "invalid_grant")
	})

	t.Run("missing redirect_uri is rejected", func(t *testing.T) {
		h, store, _ := newIssuanceHandler(t)
		cid := seedClient(t, store, []string{"http://127.0.0.1/cb"})
		seedConsentedRequest(t, store, "req-1", cid, "code-1", challenge, "session", "sess-1", time.Now().Add(time.Minute))

		ctx := formPostCtx(url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {"code-1"},
			"code_verifier": {verifier},
			"client_id":     {cid},
		}.Encode())
		h.handleToken(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "invalid_grant")
	})

	t.Run("missing required fields are rejected", func(t *testing.T) {
		h, _, _ := newIssuanceHandler(t)
		ctx := formPostCtx(url.Values{"grant_type": {"authorization_code"}}.Encode())
		h.handleToken(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "invalid_request")
	})

	t.Run("unsupported grant_type is rejected", func(t *testing.T) {
		h, _, _ := newIssuanceHandler(t)
		ctx := formPostCtx(url.Values{"grant_type": {"password"}}.Encode())
		h.handleToken(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "unsupported_grant_type")
	})
}

func TestHandleToken_RefreshRotationAndReplay(t *testing.T) {
	// Seed an initial active refresh token via the real consume path so the row
	// is created exactly as issuance would.
	seedRefresh := func(t *testing.T, store configstore.ConfigStore, plain string) {
		t.Helper()
		seedConsentedRequest(t, store, "fam-1", "client-1", "auth-code", pkceChallenge("v"), "session", "sess-1", time.Now().Add(time.Minute))
		rt := &configtables.TableOAuth2RefreshToken{
			ID: "rt-1", TokenHash: hashSHA256Hex(plain), FamilyID: "fam-1", ClientID: "client-1",
			BfMode: "session", BfSub: "sess-1", Scope: "mcp", Resource: testMCPResource, CreatedAt: time.Now(),
		}
		require.NoError(t, store.ConsumeOAuth2AuthorizeRequest(context.Background(), "fam-1", rt))
	}

	t.Run("rotation issues a new pair and carries the family", func(t *testing.T) {
		h, store, _ := newIssuanceHandler(t)
		seedClient(t, store, []string{"http://127.0.0.1/cb"})
		seedRefresh(t, store, "refresh-plain-1")

		ctx := formPostCtx(url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {"refresh-plain-1"},
			"client_id":     {"client-1"},
		}.Encode())
		h.handleToken(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

		var resp map[string]any
		require.NoError(t, json.Unmarshal(ctx.Response.Body(), &resp))
		newRefresh := resp["refresh_token"].(string)
		assert.NotEqual(t, "refresh-plain-1", newRefresh)

		// The new token is active under the same family.
		active, err := store.GetOAuth2RefreshTokenByHash(context.Background(), hashSHA256Hex(newRefresh))
		require.NoError(t, err)
		assert.Equal(t, "fam-1", active.FamilyID)
		// The old token is no longer active.
		_, err = store.GetOAuth2RefreshTokenByHash(context.Background(), hashSHA256Hex("refresh-plain-1"))
		assert.ErrorIs(t, err, configstore.ErrNotFound)
	})

	t.Run("replaying a rotated token revokes the whole family", func(t *testing.T) {
		h, store, _ := newIssuanceHandler(t)
		seedClient(t, store, []string{"http://127.0.0.1/cb"})
		seedRefresh(t, store, "refresh-plain-1")

		rotate := formPostCtx(url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {"refresh-plain-1"}, "client_id": {"client-1"},
		}.Encode())
		h.handleToken(rotate)
		require.Equal(t, fasthttp.StatusOK, rotate.Response.StatusCode())
		var resp map[string]any
		require.NoError(t, json.Unmarshal(rotate.Response.Body(), &resp))
		newRefresh := resp["refresh_token"].(string)

		// Replay the now-revoked original.
		replay := formPostCtx(url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {"refresh-plain-1"}, "client_id": {"client-1"},
		}.Encode())
		h.handleToken(replay)
		assert.Equal(t, fasthttp.StatusBadRequest, replay.Response.StatusCode())
		assert.Contains(t, string(replay.Response.Body()), "invalid_grant")

		// The family is now fully revoked: the freshly issued token no longer works.
		after := formPostCtx(url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {newRefresh}, "client_id": {"client-1"},
		}.Encode())
		h.handleToken(after)
		assert.Equal(t, fasthttp.StatusBadRequest, after.Response.StatusCode())
		assert.Contains(t, string(after.Response.Body()), "invalid_grant")
	})

	t.Run("client_id mismatch is rejected", func(t *testing.T) {
		h, store, _ := newIssuanceHandler(t)
		seedClient(t, store, []string{"http://127.0.0.1/cb"})
		seedRefresh(t, store, "refresh-plain-1")

		ctx := formPostCtx(url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {"refresh-plain-1"}, "client_id": {"other"},
		}.Encode())
		h.handleToken(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "invalid_grant")
	})

	t.Run("missing fields are rejected", func(t *testing.T) {
		h, _, _ := newIssuanceHandler(t)
		ctx := formPostCtx(url.Values{"grant_type": {"refresh_token"}}.Encode())
		h.handleToken(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "invalid_request")
	})
}

func TestHandleToken_RefreshVKIdentityDisabled(t *testing.T) {
	seedVKRefresh := func(t *testing.T, store configstore.ConfigStore, plain string) {
		t.Helper()
		seedConsentedRequest(t, store, "fam-vk", "client-1", "auth-code-vk", pkceChallenge("v"), "vk", "vk-1", time.Now().Add(time.Minute))
		rt := &configtables.TableOAuth2RefreshToken{
			ID: "rt-vk", TokenHash: hashSHA256Hex(plain), FamilyID: "fam-vk", ClientID: "client-1",
			BfMode: "vk", BfSub: "vk-1", Scope: "mcp", Resource: testMCPResource, CreatedAt: time.Now(),
		}
		require.NoError(t, store.ConsumeOAuth2AuthorizeRequest(context.Background(), "fam-vk", rt))
	}

	refreshReq := func() *fasthttp.RequestCtx {
		return formPostCtx(url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {"refresh-vk-1"}, "client_id": {"client-1"},
		}.Encode())
	}

	t.Run("vk refresh rejected when disabled and user mode available", func(t *testing.T) {
		store := newRealOAuth2Store(t)
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeOAuth, false)
		cfg.ClientConfig.OAuth2ServerConfig.DisableVKIdentity = true
		h := NewOAuth2IssuanceHandler(cfg, nil, &fakeResolver{userModeAvailable: true})
		seedClient(t, store, []string{"http://127.0.0.1/cb"})
		seedVKRefresh(t, store, "refresh-vk-1")

		ctx := refreshReq()
		h.handleToken(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		body := string(ctx.Response.Body())
		assert.Contains(t, body, "invalid_grant")
		assert.Contains(t, body, "no longer accepted")
	})

	t.Run("vk refresh not blocked by flag when user mode unavailable", func(t *testing.T) {
		store := newRealOAuth2Store(t)
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeOAuth, false)
		cfg.ClientConfig.OAuth2ServerConfig.DisableVKIdentity = true
		// nil resolver → user mode unavailable → the flag is ignored and the flow
		// falls through to the VK liveness check, which rejects the (unseeded) VK
		// with a different message. That proves the cutoff did not fire.
		h := NewOAuth2IssuanceHandler(cfg, nil, nil)
		seedClient(t, store, []string{"http://127.0.0.1/cb"})
		seedVKRefresh(t, store, "refresh-vk-1")

		ctx := refreshReq()
		h.handleToken(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		body := string(ctx.Response.Body())
		assert.NotContains(t, body, "no longer accepted")
		assert.Contains(t, body, "no longer active")
	})
}
