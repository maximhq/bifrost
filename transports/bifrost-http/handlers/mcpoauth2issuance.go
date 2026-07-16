package handlers

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/temptoken"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// OAuth2IssuanceHandler implements the three downstream OAuth2 endpoints:
//
//   - POST /oauth2/register   — RFC 7591 Dynamic Client Registration
//   - GET  /oauth2/authorize  — Authorization endpoint (PKCE-S256, RFC 8707)
//   - POST /oauth2/token      — Token endpoint (auth-code + refresh grants)
type OAuth2IssuanceHandler struct {
	store      *lib.Config
	tempTokens *temptoken.Service // optional; nil = no consent temp-token minted
	// identityResolver mirrors the consent handler's resolver. It is consulted
	// only to decide whether DisableVKIdentity is in effect (it is honored only
	// when user identity is available). May be nil — VK refresh is never blocked
	// then, matching availableModes which keeps offering vk without a resolver.
	identityResolver OAuth2IdentityResolver
}

// NewOAuth2IssuanceHandler creates a new issuance handler. identityResolver may
// be nil — the VK-refresh cutoff degrades to a no-op, consistent with the
// consent handler offering vk when no resolver is present.
func NewOAuth2IssuanceHandler(store *lib.Config, tempTokens *temptoken.Service, identityResolver OAuth2IdentityResolver) *OAuth2IssuanceHandler {
	return &OAuth2IssuanceHandler{store: store, tempTokens: tempTokens, identityResolver: identityResolver}
}

// RegisterRoutes wires the three OAuth2 issuance routes.
func (h *OAuth2IssuanceHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// These routes are public — no auth middleware applied.
	r.POST("/oauth2/register", h.handleRegister)
	r.GET("/oauth2/authorize", h.handleAuthorize)
	r.POST("/oauth2/token", h.handleToken)
}

// --- POST /oauth2/register (RFC 7591 DCR) ---

type dcrRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope"`
}

func (h *OAuth2IssuanceHandler) handleRegister(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		sendOAuthError(ctx, fasthttp.StatusServiceUnavailable, "server_error", "config store unavailable")
		return
	}

	var req dcrRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_request", "malformed request body")
		return
	}
	if len(req.RedirectURIs) == 0 {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required")
		return
	}
	// Registration is public and unauthenticated, so reject dangerous schemes
	// (javascript:, data:, etc.) here at the source. Only https is allowed, with
	// http permitted exclusively for loopback addresses (RFC 9700 §4.1.3).
	for _, uri := range req.RedirectURIs {
		if !isAllowedRedirectScheme(uri) {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must use https (or http for loopback addresses)")
			return
		}
	}
	// Only public clients supported.
	if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none" {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_client_metadata", "only token_endpoint_auth_method=none is supported")
		return
	}

	grantTypes := req.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = []string{"authorization_code"}
	}
	// The token endpoint only implements the authorization-code and refresh-token
	// grants. Reject anything else here so a registration never advertises a flow
	// that would later be refused at /oauth2/token.
	for _, gt := range grantTypes {
		if gt != "authorization_code" && gt != "refresh_token" {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_client_metadata", "unsupported grant_type")
			return
		}
	}
	responseTypes := req.ResponseTypes
	if len(responseTypes) == 0 {
		responseTypes = []string{"code"}
	}
	// The authorize endpoint only implements response_type=code.
	for _, rt := range responseTypes {
		if rt != "code" {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_client_metadata", "unsupported response_type")
			return
		}
	}
	scope := req.Scope
	if scope == "" {
		scope = "mcp"
	}

	clientID := uuid.New().String()
	client := &configtables.TableOAuth2Client{
		ID:           uuid.New().String(),
		ClientID:     clientID,
		ClientName:   req.ClientName,
		RedirectURIs: req.RedirectURIs,
		GrantTypes:   grantTypes,
		Scope:        scope,
		CreatedAt:    time.Now(),
	}
	if err := h.store.ConfigStore.CreateOAuth2Client(ctx, client); err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to register client")
		return
	}

	ctx.SetStatusCode(fasthttp.StatusCreated)
	ctx.SetContentType("application/json")
	data, err := sonic.Marshal(map[string]any{
		"client_id":                  clientID,
		"client_id_issued_at":        client.CreatedAt.Unix(),
		"grant_types":                grantTypes,
		"response_types":             responseTypes,
		"redirect_uris":              req.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"scope":                      scope,
	})
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to marshal response")
		return
	}
	ctx.SetBody(data)
}

// --- GET /oauth2/authorize ---

func (h *OAuth2IssuanceHandler) handleAuthorize(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		sendOAuthError(ctx, fasthttp.StatusServiceUnavailable, "server_error", "config store unavailable")
		return
	}

	q := ctx.QueryArgs()
	clientID := string(q.Peek("client_id"))
	redirectURIRaw := string(q.Peek("redirect_uri"))
	state := string(q.Peek("state"))
	codeChallenge := string(q.Peek("code_challenge"))
	codeChallengeMethod := string(q.Peek("code_challenge_method"))
	resource := string(q.Peek("resource"))
	scope := string(q.Peek("scope"))

	// Validate client exists before using redirect_uri.
	client, err := h.store.ConfigStore.GetOAuth2ClientByClientID(ctx, clientID)
	if err != nil || client == nil {
		if errors.Is(err, configstore.ErrNotFound) {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_client", "unknown client_id")
			return
		}
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to look up client")
		return
	}

	// Validate redirect_uri (loopback any-port per RFC 8252 §7.3).
	if !matchRedirectURI(redirectURIRaw, client.RedirectURIs) {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_redirect_uri", "redirect_uri not registered for this client")
		return
	}

	// From here errors redirect to the client.
	redirectError := func(errCode, description string) {
		redirectWithParams(ctx, redirectURIRaw, map[string]string{
			"error":             errCode,
			"error_description": description,
			"state":             state,
		})
	}

	// Constrain the requested scope to what the client registered for. scope is
	// later copied into the access-token and refresh-token state, so a client must
	// not be able to request a broader scope than it registered. An omitted scope
	// defaults to the client's registered scope.
	registeredScope := client.Scope
	if registeredScope == "" {
		registeredScope = "mcp"
	}
	if scope == "" {
		scope = registeredScope
	} else if !scopeWithinRegistered(scope, registeredScope) {
		redirectError("invalid_scope", "requested scope exceeds the scope registered for this client")
		return
	}

	if string(q.Peek("response_type")) != "code" {
		redirectError("unsupported_response_type", "only response_type=code is supported")
		return
	}
	if codeChallengeMethod != "S256" {
		redirectError("invalid_request", "code_challenge_method must be S256")
		return
	}
	if codeChallenge == "" {
		redirectError("invalid_request", "code_challenge is required")
		return
	}
	// RFC 8707: bind the grant to this server's single protected resource. /mcp
	// is the only resource we issue tokens for and token verification pins the
	// audience to it. A client that omits resource (e.g. one that doesn't fetch
	// the protected-resource metadata) defaults to the canonical /mcp resource
	// since there is exactly one; a client that does send it must match.
	canonicalResource := oauth2MCPResourceURL(ctx, h.store)
	if resource == "" {
		resource = canonicalResource
	} else if resource != canonicalResource {
		redirectError("invalid_target", "resource does not identify this MCP server")
		return
	}

	cfg := oauth2ServerCfg(h.store)
	authCodeTTL := cfg.AuthCodeTTL
	if authCodeTTL <= 0 {
		authCodeTTL = configtables.DefaultAuthCodeTTL
	} else if authCodeTTL > configtables.MaxAuthCodeTTL {
		// Defense in depth: the API rejects an over-max value at save and config
		// load rejects it at startup, so this should be unreachable — but clamp
		// anyway so a code can never be minted with a lifetime above the cap.
		authCodeTTL = configtables.MaxAuthCodeTTL
	}

	req := &configtables.TableOAuth2AuthorizeRequest{
		ID:                  uuid.New().String(),
		ClientID:            client.ClientID,
		RedirectURI:         redirectURIRaw,
		State:               state,
		Scope:               scope,
		Resource:            resource,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		Status:              configtables.OAuth2AuthorizeRequestStatusPending,
		ExpiresAt:           time.Now().Add(time.Duration(authCodeTTL) * time.Second),
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}
	if err := h.store.ConfigStore.CreateOAuth2AuthorizeRequest(ctx, req); err != nil {
		// Keep the DB error server-side — it can carry table/constraint names that
		// must not leak to the (untrusted) redirect target.
		logger.Error("failed to create oauth2 authorize request: %v", err)
		redirectError("server_error", "failed to create authorization request")
		return
	}

	// Mint a temp token scoping the consent page to this request. Without it the
	// consent-page API calls (GET/PUT /api/oauth2/consent/flows/{id}) have no auth
	// credential and fail with 401, leaving the user on a dead-end page — so a mint
	// failure must abort with a well-formed error redirect rather than be swallowed.
	tempToken := ""
	if h.tempTokens != nil {
		tok, err := h.tempTokens.Mint(ctx, temptoken.OAuth2ConsentScopeName, req.ID, time.Duration(authCodeTTL)*time.Second)
		if err != nil {
			logger.Error("failed to mint oauth2 consent temp token: %v", err)
			redirectError("server_error", "failed to prepare consent flow")
			return
		}
		tempToken = tok
	}

	base := oauth2IssuerURL(ctx, h.store)
	consentURL := fmt.Sprintf("%s/oauth/consent?flow=%s", base, url.QueryEscape(req.ID))
	if tempToken != "" {
		consentURL += "#t=" + url.QueryEscape(tempToken)
	}

	ctx.Response.Header.Set("Location", consentURL)
	ctx.SetStatusCode(fasthttp.StatusFound)
}

// --- POST /oauth2/token ---

func (h *OAuth2IssuanceHandler) handleToken(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		sendOAuthError(ctx, fasthttp.StatusServiceUnavailable, "server_error", "config store unavailable")
		return
	}

	grantType := string(ctx.FormValue("grant_type"))
	switch grantType {
	case "authorization_code":
		h.handleTokenAuthCode(ctx)
	case "refresh_token":
		h.handleTokenRefresh(ctx)
	default:
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "unsupported_grant_type", fmt.Sprintf("grant_type %q not supported", grantType))
	}
}

func (h *OAuth2IssuanceHandler) handleTokenAuthCode(ctx *fasthttp.RequestCtx) {
	code := string(ctx.FormValue("code"))
	codeVerifier := string(ctx.FormValue("code_verifier"))
	redirectURI := string(ctx.FormValue("redirect_uri"))
	clientID := clientIDFromRequest(ctx)
	resource := string(ctx.FormValue("resource"))

	if code == "" || codeVerifier == "" || clientID == "" {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_request", "code, code_verifier and client_id are required")
		return
	}

	// Look up the authorize request by hashing the received code.
	codeHash := hashSHA256Hex(code)
	req, err := h.store.ConfigStore.GetOAuth2AuthorizeRequestByCodeHash(ctx, codeHash)
	if err != nil || req == nil {
		if errors.Is(err, configstore.ErrNotFound) {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "authorization code not found or already used")
			return
		}
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to look up authorization code")
		return
	}
	if time.Now().After(req.ExpiresAt) {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "authorization code expired")
		return
	}
	if req.ClientID != clientID {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "client_id mismatch")
		return
	}
	// The authorization request always binds a redirect_uri (it is validated
	// against the client's registered URIs at /oauth2/authorize), so per RFC 6749
	// §4.1.3 the token request must present it and it must match exactly. Accepting
	// a missing redirect_uri would let a code be exchanged outside its bound redirect.
	if redirectURI == "" || req.RedirectURI != redirectURI {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}
	if resource != "" && req.Resource != resource {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "resource mismatch")
		return
	}

	// Verify PKCE: SHA256(verifier) must equal stored challenge.
	if !verifyPKCES256(codeVerifier, req.CodeChallenge) {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}

	accessToken, refreshToken, refreshTokenObj, err := h.issueTokenPair(ctx, req.ID, req.ClientID, req.BfMode, req.BfSub, req.Scope, req.Resource)
	if err != nil {
		return
	}
	// Atomically mark the code as consumed and create the refresh token.
	// If this fails the authorize request stays "consented" and the client can retry.
	if err := h.store.ConfigStore.ConsumeOAuth2AuthorizeRequest(ctx, req.ID, refreshTokenObj); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			// The code was concurrently consumed or expired between lookup and consume.
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "authorization code not found or already used")
			return
		}
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to issue token")
		return
	}
	sendTokenResponse(ctx, accessToken, refreshToken, req.Scope, oauth2ServerCfg(h.store).AccessTokenTTL)
}

func (h *OAuth2IssuanceHandler) handleTokenRefresh(ctx *fasthttp.RequestCtx) {
	refreshToken := string(ctx.FormValue("refresh_token"))
	clientID := clientIDFromRequest(ctx)
	resource := string(ctx.FormValue("resource"))

	if refreshToken == "" || clientID == "" {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_request", "refresh_token and client_id are required")
		return
	}

	tokenHash := hashSHA256Hex(refreshToken)
	rt, err := h.store.ConfigStore.GetOAuth2RefreshTokenByHash(ctx, tokenHash)
	if errors.Is(err, configstore.ErrNotFound) {
		// Token not found in the active set — check if it was previously issued
		// and revoked. A revoked token being re-presented indicates the token
		// family may be compromised (stolen token used before rotation). Revoke
		// all active tokens in the family to limit the damage (RFC 9700 §2.2.2).
		// Fail closed: if the lookup or the family revocation errors, surface
		// server_error rather than reporting invalid_grant while leaving a
		// potentially compromised family usable.
		revoked, lookupErr := h.store.ConfigStore.GetOAuth2RefreshTokenByHashAny(ctx, tokenHash)
		switch {
		case lookupErr != nil && !errors.Is(lookupErr, configstore.ErrNotFound):
			sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to verify refresh token revocation state")
			return
		case revoked != nil:
			if revokeErr := h.store.ConfigStore.RevokeOAuth2RefreshTokensByFamilyID(ctx, revoked.FamilyID); revokeErr != nil {
				sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to revoke refresh token family")
				return
			}
		}
		// Unknown token or a revoked one we just contained — either way the grant
		// is not usable.
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "refresh token not found or revoked")
		return
	}
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to look up refresh token")
		return
	}
	if rt.ClientID != clientID {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "client_id mismatch")
		return
	}

	// VK identity cutoff (refresh side): when virtual-key identity has been
	// disabled, vk-mode grants must not refresh. Live /mcp requests are already
	// rejected at request time (see getMCPServerForRequest); denying refresh here
	// closes the second path so a disabled grant can neither be used nor renewed.
	// Gated on user identity being available, identical to the consent flow's
	// availableModes — so this can never fire where vk is still the offered path.
	if schemas.MCPAuthMode(rt.BfMode) == schemas.MCPAuthModeVK {
		userModeAvailable := h.identityResolver != nil && h.identityResolver.IsUserModeAvailable()
		if userModeAvailable && oauth2ServerCfg(h.store).DisableVKIdentity {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "virtual-key identity is no longer accepted; re-authenticate")
			return
		}
	}

	// bf_sub liveness check: for VK-mode tokens, verify the VK still exists
	// and is active. A deleted or disabled VK should not be able to silently
	// obtain new access tokens via refresh. A transient lookup failure must stay
	// retriable (server_error) — only a missing/inactive VK invalidates the grant.
	if schemas.MCPAuthMode(rt.BfMode) == schemas.MCPAuthModeVK && h.store.ConfigStore != nil {
		vk, vkErr := h.store.ConfigStore.GetVirtualKey(ctx, rt.BfSub)
		if vkErr != nil && !errors.Is(vkErr, configstore.ErrNotFound) {
			sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to verify virtual key")
			return
		}
		if errors.Is(vkErr, configstore.ErrNotFound) || vk == nil || !vk.IsActiveValue() {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "virtual key is no longer active")
			return
		}
	}

	// bf_sub liveness check: for user-mode tokens, verify the user still exists
	// and is active, mirroring the VK-mode check above. A deleted or deactivated
	// user should not be able to silently obtain new access tokens via refresh.
	// The user is verified against the same in-memory source used on the /mcp
	// request path. A transient lookup failure stays retriable (server_error) —
	// only a gone/deactivated user invalidates the grant.
	if schemas.MCPAuthMode(rt.BfMode) == schemas.MCPAuthModeUser && h.identityResolver != nil {
		active, err := h.identityResolver.IsUserActive(ctx, rt.BfSub)
		if err != nil {
			sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to verify user")
			return
		}
		if !active {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "user is no longer active")
			return
		}
	}

	// RFC 8707: resource (audience URI) is distinct from scope. When the client
	// omits it on refresh, carry forward the original resource captured at
	// authorization — never substitute the scope string. When the client does
	// provide it, it must match the resource bound at authorization: issuing a
	// token for a different resource than originally authorized would let a
	// refresh-token holder escape the original audience binding. Mirrors the
	// auth-code handler's check.
	if resource == "" {
		resource = rt.Resource
	} else if resource != rt.Resource {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "resource mismatch")
		return
	}

	accessToken, newRefreshToken, newRefreshTokenObj, err := h.issueTokenPair(ctx, rt.FamilyID, rt.ClientID, rt.BfMode, rt.BfSub, rt.Scope, resource)
	if err != nil {
		return
	}
	// Carry the original grant's creation time forward across rotations so the
	// grant's "Created" timestamp stays anchored to when it was first authorized,
	// and stamp last_used_at to mark this refresh as the grant's latest activity.
	usedAt := time.Now()
	newRefreshTokenObj.CreatedAt = rt.CreatedAt
	newRefreshTokenObj.LastUsedAt = &usedAt
	// Atomically revoke the old token and create the new one.
	// If this fails the old token stays active and the client can retry the refresh.
	if err := h.store.ConfigStore.RotateOAuth2RefreshToken(ctx, rt.ID, newRefreshTokenObj); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			// The token was concurrently rotated/revoked between lookup and rotate.
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "refresh token not found or revoked")
			return
		}
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to rotate token")
		return
	}
	sendTokenResponse(ctx, accessToken, newRefreshToken, rt.Scope, oauth2ServerCfg(h.store).AccessTokenTTL)
}

// issueTokenPair mints a signed JWT access token and builds a refresh token row.
// It is a pure function — no DB writes. The caller is responsible for atomically
// persisting the refresh token row alongside any grant-specific side-effects
// (e.g. marking the auth code consumed, or revoking the previous refresh token).
//
// familyID traces the token back to its original authorization grant; all
// rotated descendants share the same ID for stolen-token detection (RFC 9700 §2.2.2).
//
// On error, issueTokenPair writes an OAuth error response to ctx and returns a
// non-nil error so the caller can return immediately without writing again.
func (h *OAuth2IssuanceHandler) issueTokenPair(
	ctx *fasthttp.RequestCtx,
	familyID, clientID, bfMode, bfSub, scope, resource string,
) (accessToken, refreshTokenPlain string, rt *configtables.TableOAuth2RefreshToken, err error) {
	cfg := oauth2ServerCfg(h.store)
	accessTokenTTL := cfg.AccessTokenTTL
	if accessTokenTTL <= 0 {
		accessTokenTTL = configtables.DefaultAccessTokenTTL
	}

	signingKey, err := h.store.GetOAuth2SigningKey(ctx)
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "signing key unavailable")
		return
	}
	privKey, err := parseRSAPrivateKeyPEM(signingKey.PrivateKeyPEM)
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "invalid signing key")
		return
	}

	issuer := oauth2IssuerURL(ctx, h.store)
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":     issuer,
		"aud":     jwt.ClaimStrings{resource},
		"sub":     bfSub,
		"bf_mode": bfMode,
		"scope":   scope,
		"iat":     now.Unix(),
		"nbf":     now.Unix(),
		"exp":     now.Add(time.Duration(accessTokenTTL) * time.Second).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = signingKey.KID
	accessToken, err = tok.SignedString(privKey)
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to sign access token")
		return
	}

	refreshTokenPlain, err = generateSecureToken(32)
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to generate refresh token")
		return
	}
	rt = &configtables.TableOAuth2RefreshToken{
		ID:        uuid.New().String(),
		TokenHash: hashSHA256Hex(refreshTokenPlain),
		FamilyID:  familyID,
		ClientID:  clientID,
		BfMode:    bfMode,
		BfSub:     bfSub,
		Scope:     scope,
		Resource:  resource,
		CreatedAt: now,
	}
	return
}

// sendTokenResponse writes the RFC 6749 token response to ctx.
func sendTokenResponse(ctx *fasthttp.RequestCtx, accessToken, refreshToken, scope string, accessTokenTTL int) {
	if accessTokenTTL <= 0 {
		accessTokenTTL = configtables.DefaultAccessTokenTTL
	}
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	ctx.Response.Header.Set("Cache-Control", "no-store")
	ctx.Response.Header.Set("Pragma", "no-cache")
	data, err := sonic.Marshal(map[string]any{
		"access_token":  accessToken,
		"token_type":    "Bearer",
		"expires_in":    accessTokenTTL,
		"refresh_token": refreshToken,
		"scope":         scope,
	})
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to marshal token response")
		return
	}
	ctx.SetBody(data)
}

// clientIDFromRequest resolves the OAuth client_id for a token request. Public
// clients (token_endpoint_auth_method=none) may send it either as a request
// parameter or as the username of an HTTP Basic Authorization header
// (RFC 6749 §2.3.1); some clients default to the header form. Both are accepted;
// the Basic password is ignored since only public clients are supported.
func clientIDFromRequest(ctx *fasthttp.RequestCtx) string {
	if v := string(ctx.FormValue("client_id")); v != "" {
		return v
	}
	auth := string(ctx.Request.Header.Peek("Authorization"))
	const prefix = "Basic "
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(auth[len(prefix):])); err == nil {
			// Basic credentials are "client_id:client_secret", each
			// application/x-www-form-urlencoded; take and decode the username.
			username, _, _ := strings.Cut(string(decoded), ":")
			if id, uerr := url.QueryUnescape(username); uerr == nil {
				return id
			}
			return username
		}
	}
	return ""
}

// --- Helpers ---

// isLoopbackRedirectHost reports whether a redirect URI host is a loopback
// address per RFC 8252 §7.3: localhost, 127.0.0.1, or the IPv6 loopback [::1]
// (url.Hostname() already strips the brackets).
func isLoopbackRedirectHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// allowedPrivateUseRedirectSchemes is the default-deny allowlist of private-use
// ("custom") URI schemes accepted as redirect targets, in addition to https and
// http-loopback. Native apps register such schemes per RFC 8252 §7.1 (e.g.
// Cursor uses cursor://anysphere.cursor-mcp/oauth/callback). Everything not on
// this list is rejected so a dangerous scheme (javascript:, data:, file:, …) can
// never be written into the Location header built from a redirect URI. url.Parse
// lowercases the scheme, so keys here must be lowercase.
var allowedPrivateUseRedirectSchemes = map[string]struct{}{
	"cursor": {},
}

// isAllowedRedirectScheme reports whether a redirect URI uses a safe scheme:
//   - https for any host,
//   - http only for loopback addresses (localhost/127.0.0.1/[::1]), and
//   - a private-use ("custom") URI scheme on the allowedPrivateUseRedirectSchemes
//     allowlist that also carries an authority component (scheme://host/...).
//
// It is default-deny: any scheme not matching one of the above is rejected.
func isAllowedRedirectScheme(candidate string) bool {
	parsed, err := url.Parse(candidate)
	if err != nil {
		return false
	}
	switch parsed.Scheme {
	case "https":
		return true
	case "http":
		return isLoopbackRedirectHost(parsed.Hostname())
	default:
		if _, ok := allowedPrivateUseRedirectSchemes[parsed.Scheme]; !ok {
			return false
		}
		// Require an authority (scheme://host/...) so an opaque form of an
		// allowlisted scheme (e.g. "cursor:whatever") is still rejected.
		return parsed.Host != ""
	}
}

// matchRedirectURI validates redirect_uri against registered URIs.
// For loopback addresses (localhost / 127.0.0.1 / [::1]), port is ignored per RFC 8252 §7.3.
func matchRedirectURI(candidate string, registered []string) bool {
	parsed, err := url.Parse(candidate)
	if err != nil {
		return false
	}
	isLoopback := isLoopbackRedirectHost(parsed.Hostname())

	for _, r := range registered {
		rParsed, err := url.Parse(r)
		if err != nil {
			continue
		}
		if isLoopback && isLoopbackRedirectHost(rParsed.Hostname()) {
			// Loopback: match scheme + host (without port) + path.
			if parsed.Scheme == rParsed.Scheme && parsed.Path == rParsed.Path {
				return true
			}
		} else {
			// Non-loopback: exact match.
			if candidate == r {
				return true
			}
		}
	}
	return false
}

// redirectWithParams builds a redirect URL with the given query params and redirects.
func redirectWithParams(ctx *fasthttp.RequestCtx, base string, params map[string]string) {
	u, err := url.Parse(base)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		return
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	ctx.Response.Header.Set("Location", u.String())
	ctx.SetStatusCode(fasthttp.StatusFound)
}

// scopeWithinRegistered reports whether every space-delimited token in requested
// is present in the registered scope set. Callers default an empty requested scope
// to the registered scope before calling, so an empty requested scope is vacuously
// within bounds.
func scopeWithinRegistered(requested, registered string) bool {
	allowed := make(map[string]struct{})
	for s := range strings.FieldsSeq(registered) {
		allowed[s] = struct{}{}
	}
	for s := range strings.FieldsSeq(requested) {
		if _, ok := allowed[s]; !ok {
			return false
		}
	}
	return true
}

// verifyPKCES256 verifies a PKCE S256 code_verifier against a stored challenge.
func verifyPKCES256(verifier, challenge string) bool {
	h := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(h[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

// hashSHA256Hex returns the hex-encoded SHA-256 hash of the input.
func hashSHA256Hex(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])
}

// generateSecureToken returns a cryptographically secure URL-safe random token.
func generateSecureToken(length int) (string, error) {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// parseRSAPrivateKeyPEM decodes and parses a PKCS8 RSA private key PEM.
func parseRSAPrivateKeyPEM(pemStr string) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode([]byte(pemStr))
	if block == nil || len(rest) > 0 {
		return nil, fmt.Errorf("malformed private key PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("expected RSA private key, got %T", key)
	}
	return rsaKey, nil
}

// sendOAuthError writes an RFC 6749 §5.2 error response.
func sendOAuthError(ctx *fasthttp.RequestCtx, statusCode int, errCode, description string) {
	ctx.SetStatusCode(statusCode)
	ctx.SetContentType("application/json")
	data, err := sonic.Marshal(map[string]string{
		"error":             errCode,
		"error_description": description,
	})
	if err != nil {
		ctx.Error("failed to marshal error response", fasthttp.StatusInternalServerError)
		return
	}
	ctx.SetBody(data)
}
