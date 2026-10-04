package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/temptoken"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// Claude Code gateway protocol constants. Claude Code's /login (Cloud gateway)
// is pointed at {issuer}/claude-code and derives every other URL from it.
const (
	claudeCodeGatewayPathPrefix = "/claude-code"
	// claudeCodeClientID names the built-in OAuth client every Claude Code
	// device grant and refresh token is bound to. Claude Code sends no client_id
	// at either endpoint, so the gateway supplies this one.
	claudeCodeClientID   = "bifrost-claude-code"
	claudeCodeClientName = "Claude Code"
	claudeCodeScope      = "claude-code"
	// deviceCodeGrantType is the RFC 8628 §3.4 grant type.
	deviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	// claudeCodeDeviceCodeTTL bounds how long a sign-in may wait for the browser.
	claudeCodeDeviceCodeTTL = 10 * time.Minute
	// claudeCodeDevicePollInterval is the polling interval handed to the client.
	claudeCodeDevicePollInterval = 5 * time.Second
	// claudeCodeUserCodeAlphabet is RFC 8628 §6.1's base-20 consonant set: no
	// vowels (no accidental words) and no easily confused characters.
	claudeCodeUserCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
	claudeCodeUserCodeLength   = 8
	// Per-IP limits for the two unauthenticated sign-in endpoints (RFC 8628 §5.1, §5.2).
	// Keyed by the TCP peer, so behind a reverse proxy every client of one process
	// shares a bucket; the limits leave room for that while keeping user-code
	// guessing negligible (20^8 codes, each valid for 10 minutes).
	claudeCodeDeviceAuthorizationLimit = 120
	claudeCodeDeviceVerifyLimit        = 120
	claudeCodeRateLimitWindow          = time.Minute
)

// ClaudeCodeGatewayHandler serves the Claude Code gateway protocol under
// /claude-code, so Claude Code can sign in to Bifrost with /login (Cloud
// gateway) instead of a static virtual key:
//
//   - GET  /claude-code/.well-known/oauth-authorization-server — RFC 8414 metadata
//   - POST /claude-code/oauth/device_authorization — RFC 8628 device authorization
//   - POST /claude-code/oauth/token — device-code and refresh-token grants
//   - POST /claude-code/oauth/revoke — RFC 7009 revocation on sign-out
//   - POST /claude-code/device/verify — exchanges a user code for the consent page
//   - GET  /claude-code/managed/settings — Claude Code managed settings (bearer)
//   - POST /claude-code/v1/{metrics,logs,traces} — OTLP sink (bearer, discarded)
//   - POST /claude-code/v1/messages[/count_tokens], GET /claude-code/v1/models — inference (bearer)
//
// Sign-in reuses the MCP OAuth2 authorization server: the device grant is an
// oauth2_authorize_requests row that the existing consent page approves (virtual
// key, signed-in user, or anonymous session), and the issued tokens are the same
// RS256 JWTs and rotating refresh tokens, with the /claude-code audience. It is
// independent of mcp_server_auth_mode: every route answers 404 unless the
// gateway is enabled in oauth2_server_config and issuer_url is set.
type ClaudeCodeGatewayHandler struct {
	store            *lib.Config
	issuance         *OAuth2IssuanceHandler
	tempTokens       *temptoken.Service     // optional; nil = consent page needs a dashboard session
	identityResolver OAuth2IdentityResolver // optional; nil = vk and session identities only
	vkCache          VirtualKeyCache        // optional; nil = virtual keys read from the config store

	deviceAuthLimiter *ipRateLimiter
	verifyLimiter     *ipRateLimiter
	polls             *devicePollTracker
}

// NewClaudeCodeGatewayHandler creates the Claude Code gateway handler.
// tempTokens, identityResolver and vkCache may each be nil.
func NewClaudeCodeGatewayHandler(store *lib.Config, tempTokens *temptoken.Service, identityResolver OAuth2IdentityResolver, vkCache VirtualKeyCache) *ClaudeCodeGatewayHandler {
	return &ClaudeCodeGatewayHandler{
		store:             store,
		issuance:          NewOAuth2IssuanceHandler(store, tempTokens, identityResolver),
		tempTokens:        tempTokens,
		identityResolver:  identityResolver,
		vkCache:           vkCache,
		deviceAuthLimiter: newIPRateLimiter(claudeCodeDeviceAuthorizationLimit, claudeCodeRateLimitWindow),
		verifyLimiter:     newIPRateLimiter(claudeCodeDeviceVerifyLimit, claudeCodeRateLimitWindow),
		polls:             newDevicePollTracker(),
	}
}

// RegisterRoutes wires the sign-in, managed-settings and telemetry routes. They
// are registered without the API auth middleware: the sign-in endpoints are
// public by protocol, and the rest authenticate the gateway bearer themselves.
func (h *ClaudeCodeGatewayHandler) RegisterRoutes(r *router.Router, _ ...schemas.BifrostHTTPMiddleware) {
	r.GET(claudeCodeGatewayPathPrefix+"/.well-known/oauth-authorization-server", h.handleMetadata)
	r.POST(claudeCodeGatewayPathPrefix+"/oauth/device_authorization", h.handleDeviceAuthorization)
	r.POST(claudeCodeGatewayPathPrefix+"/oauth/token", h.handleToken)
	r.POST(claudeCodeGatewayPathPrefix+"/oauth/revoke", h.handleRevoke)
	r.POST(claudeCodeGatewayPathPrefix+"/device/verify", h.handleDeviceVerify)
	r.GET(claudeCodeGatewayPathPrefix+"/managed/settings", h.BearerMiddleware(h.handleManagedSettings))
	for _, signal := range []string{"metrics", "logs", "traces"} {
		r.POST(claudeCodeGatewayPathPrefix+"/v1/"+signal, h.BearerMiddleware(h.handleOTLP))
	}
}

// RegisterInferenceRoutes wires /claude-code/v1/messages, /v1/messages/count_tokens
// and /v1/models onto the Anthropic integration. The bearer middleware runs
// first and turns the gateway token into the identity it stands for, so the
// inference middlewares and governance then treat the request exactly like one
// that presented that virtual key (or user) directly.
func (h *ClaudeCodeGatewayHandler) RegisterInferenceRoutes(r *router.Router, client *bifrost.Bifrost, accessResolver integrations.AccessResolver, middlewares ...schemas.BifrostHTTPMiddleware) {
	var routes []integrations.RouteConfig
	for _, route := range integrations.CreateAnthropicRouteConfigs(claudeCodeGatewayPathPrefix, logger) {
		// Claude Code only speaks the Messages API; leave the legacy completions route out.
		if strings.HasPrefix(route.Path, claudeCodeGatewayPathPrefix+"/v1/messages") {
			routes = append(routes, route)
		}
	}
	routes = append(routes, integrations.CreateAnthropicCountTokensRouteConfigs(claudeCodeGatewayPathPrefix, h.store)...)
	routes = append(routes, integrations.CreateAnthropicListModelsRouteConfigs(claudeCodeGatewayPathPrefix, h.store)...)
	chain := append([]schemas.BifrostHTTPMiddleware{h.BearerMiddleware}, middlewares...)
	integrations.NewGenericRouter(client, h.store, accessResolver, routes, nil, logger).RegisterRoutes(r, chain...)
}

// enabled reports whether the gateway is switched on. It also requires a
// configured issuer_url: every URL and token claim the gateway hands out derives
// from it, and the Host-header fallback oauth2IssuerURL keeps for header-only
// deployments is caller-controlled, so the gateway never runs on it.
func (h *ClaudeCodeGatewayHandler) enabled() bool {
	h.store.Mu.RLock()
	defer h.store.Mu.RUnlock()
	cc := h.store.ClientConfig
	if cc == nil || !cc.OAuth2ServerConfig.IsClaudeCodeGatewayEnabled() {
		return false
	}
	issuer := cc.OAuth2ServerConfig.IssuerURL
	return issuer.IsSet() && issuer.GetValue() != ""
}

// claudeCodeBaseURL is the URL Claude Code is pointed at: {issuer}/claude-code.
func claudeCodeBaseURL(ctx *fasthttp.RequestCtx, store *lib.Config) string {
	return strings.TrimRight(oauth2IssuerURL(ctx, store), "/") + claudeCodeGatewayPathPrefix
}

// claudeCodeResourceURL is the RFC 8707 audience of every gateway token. It
// differs from the /mcp resource, so a gateway token is refused on /mcp and an
// MCP token is refused here.
func claudeCodeResourceURL(ctx *fasthttp.RequestCtx, store *lib.Config) string {
	return claudeCodeBaseURL(ctx, store)
}

// claudeCodeDeviceVerificationURL is the dashboard page where the user enters
// the code Claude Code displays.
func claudeCodeDeviceVerificationURL(issuer string) string {
	return strings.TrimRight(issuer, "/") + "/oauth/device"
}

// claudeCodeDeviceApprovedURL is where the consent page sends the browser after
// a device grant is approved; the page tells the user to return to Claude Code.
func claudeCodeDeviceApprovedURL(issuer string) string {
	return claudeCodeDeviceVerificationURL(issuer) + "?approved=1"
}

// --- GET /claude-code/.well-known/oauth-authorization-server ---

// handleMetadata serves the RFC 8414 metadata Claude Code reads at /login. Every
// endpoint sits under the base URL, since the client only uses same-origin ones.
func (h *ClaudeCodeGatewayHandler) handleMetadata(ctx *fasthttp.RequestCtx) {
	if !h.enabled() {
		sendOAuthError(ctx, fasthttp.StatusNotFound, "not_found", "the claude code gateway is not enabled")
		return
	}
	ctx.Response.Header.Set("Cache-Control", "no-store")
	base := claudeCodeBaseURL(ctx, h.store)
	data, err := sonic.Marshal(map[string]any{
		"issuer":                                base,
		"device_authorization_endpoint":         base + "/oauth/device_authorization",
		"token_endpoint":                        base + "/oauth/token",
		"revocation_endpoint":                   base + "/oauth/revoke",
		"grant_types_supported":                 []string{deviceCodeGrantType, "refresh_token"},
		"response_types_supported":              []string{},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{claudeCodeScope},
	})
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to marshal metadata")
		return
	}
	ctx.SetContentType("application/json")
	ctx.SetBody(data)
}

// --- POST /claude-code/oauth/device_authorization (RFC 8628 §3.1) ---

// handleDeviceAuthorization starts a sign-in. Claude Code sends only
// surface=claude_code (no client_id or scope), and unknown parameters are
// ignored as RFC 6749 §3.1 requires.
func (h *ClaudeCodeGatewayHandler) handleDeviceAuthorization(ctx *fasthttp.RequestCtx) {
	if !h.enabled() {
		sendOAuthError(ctx, fasthttp.StatusNotFound, "not_found", "the claude code gateway is not enabled")
		return
	}
	if h.store.ConfigStore == nil {
		sendOAuthError(ctx, fasthttp.StatusServiceUnavailable, "server_error", "config store unavailable")
		return
	}
	if !h.deviceAuthLimiter.allow(ctx.RemoteIP().String(), time.Now()) {
		ctx.Response.Header.Set("Retry-After", "60")
		sendOAuthError(ctx, fasthttp.StatusTooManyRequests, "slow_down", "too many sign-in attempts from this address; wait a minute and try again")
		return
	}
	if err := h.ensureClient(ctx); err != nil {
		logger.Error("claude code gateway: failed to register the built-in oauth client: %v", err)
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to prepare sign-in")
		return
	}

	deviceCode, err := generateSecureToken(32)
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to generate device code")
		return
	}
	deviceCodeHash := hashSHA256Hex(deviceCode)
	resource := claudeCodeResourceURL(ctx, h.store)
	now := time.Now()

	// A user-code collision trips the unique index; draw again rather than fail
	// the sign-in. With 20^8 codes this is vanishingly rare.
	var userCode string
	for attempt := 0; ; attempt++ {
		userCode, err = generateClaudeCodeUserCode()
		if err != nil {
			sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to generate user code")
			return
		}
		userCodeHash := hashSHA256Hex(normalizeClaudeCodeUserCode(userCode))
		req := &configtables.TableOAuth2AuthorizeRequest{
			ID:             uuid.New().String(),
			ClientID:       claudeCodeClientID,
			Scope:          claudeCodeScope,
			Resource:       resource,
			Status:         configtables.OAuth2AuthorizeRequestStatusPending,
			DeviceCodeHash: &deviceCodeHash,
			UserCodeHash:   &userCodeHash,
			ExpiresAt:      now.Add(claudeCodeDeviceCodeTTL),
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		err = h.store.ConfigStore.CreateOAuth2AuthorizeRequest(ctx, req)
		if err == nil {
			break
		}
		if attempt == 2 {
			logger.Error("claude code gateway: failed to create device authorization: %v", err)
			sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to create device authorization")
			return
		}
	}

	verificationURI := claudeCodeDeviceVerificationURL(oauth2IssuerURL(ctx, h.store))
	data, err := sonic.Marshal(map[string]any{
		"device_code":               deviceCode,
		"user_code":                 userCode,
		"verification_uri":          verificationURI,
		"verification_uri_complete": verificationURI + "?user_code=" + url.QueryEscape(userCode),
		"expires_in":                int(claudeCodeDeviceCodeTTL.Seconds()),
		"interval":                  int(claudeCodeDevicePollInterval.Seconds()),
	})
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to marshal response")
		return
	}
	ctx.Response.Header.Set("Cache-Control", "no-store")
	ctx.SetContentType("application/json")
	ctx.SetBody(data)
}

// ensureClient makes sure the built-in Claude Code OAuth client row exists, so
// the consent page and the connected-clients list can name the client. It is
// checked on every sign-in rather than cached, because the orphaned-client
// sweep deletes it whenever no Claude Code grant is left. A concurrent creator
// losing the unique-index race re-reads the winner's row.
func (h *ClaudeCodeGatewayHandler) ensureClient(ctx context.Context) error {
	_, err := h.store.ConfigStore.GetOAuth2ClientByClientID(ctx, claudeCodeClientID)
	if errors.Is(err, configstore.ErrNotFound) {
		createErr := h.store.ConfigStore.CreateOAuth2Client(ctx, &configtables.TableOAuth2Client{
			ID:           uuid.New().String(),
			ClientID:     claudeCodeClientID,
			ClientName:   claudeCodeClientName,
			RedirectURIs: []string{}, // device grant only: /oauth2/authorize refuses every redirect_uri
			GrantTypes:   []string{deviceCodeGrantType, "refresh_token"},
			Scope:        claudeCodeScope,
			CreatedAt:    time.Now(),
		})
		if createErr != nil {
			_, err = h.store.ConfigStore.GetOAuth2ClientByClientID(ctx, claudeCodeClientID)
		} else {
			err = nil
		}
	}
	return err
}

// --- POST /claude-code/oauth/token ---

// handleToken dispatches the device-code and refresh-token grants.
func (h *ClaudeCodeGatewayHandler) handleToken(ctx *fasthttp.RequestCtx) {
	if !h.enabled() {
		sendOAuthError(ctx, fasthttp.StatusNotFound, "not_found", "the claude code gateway is not enabled")
		return
	}
	if h.store.ConfigStore == nil {
		sendOAuthError(ctx, fasthttp.StatusServiceUnavailable, "server_error", "config store unavailable")
		return
	}
	switch grantType := string(ctx.FormValue("grant_type")); grantType {
	case deviceCodeGrantType:
		h.handleDeviceCodeGrant(ctx, string(ctx.FormValue("device_code")))
	case "refresh_token":
		refreshToken := string(ctx.FormValue("refresh_token"))
		if refreshToken == "" {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_request", "refresh_token is required")
			return
		}
		// Bound to the built-in client, so a refresh token issued to any other
		// client (an MCP client's) is refused here with invalid_grant.
		h.issuance.refreshGrant(ctx, refreshToken, claudeCodeClientID, "")
	default:
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "unsupported_grant_type", fmt.Sprintf("grant_type %q not supported", grantType))
	}
}

// handleDeviceCodeGrant answers a device's token poll (RFC 8628 §3.5): pending
// until the consent page approves the request, then tokens exactly once.
func (h *ClaudeCodeGatewayHandler) handleDeviceCodeGrant(ctx *fasthttp.RequestCtx, deviceCode string) {
	if deviceCode == "" {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_request", "device_code is required")
		return
	}
	deviceCodeHash := hashSHA256Hex(deviceCode)
	req, err := h.store.ConfigStore.GetOAuth2AuthorizeRequestByDeviceCodeHash(ctx, deviceCodeHash)
	if errors.Is(err, configstore.ErrNotFound) {
		// Unknown, swept after expiry, or never issued: the client must restart.
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "expired_token", "the device code is unknown or has expired")
		return
	}
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to look up device code")
		return
	}
	now := time.Now()
	if now.After(req.ExpiresAt) {
		h.polls.forget(deviceCodeHash)
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "expired_token", "the device code has expired")
		return
	}
	switch req.Status {
	case configtables.OAuth2AuthorizeRequestStatusPending:
		if h.polls.tooSoon(deviceCodeHash, now, claudeCodeDevicePollInterval) {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "slow_down", "polling faster than the advertised interval")
			return
		}
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "authorization_pending", "waiting for the user to approve the sign-in")
		return
	case configtables.OAuth2AuthorizeRequestStatusRevoked:
		h.polls.forget(deviceCodeHash)
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "access_denied", "the sign-in was denied")
		return
	case configtables.OAuth2AuthorizeRequestStatusConsented:
		// handled below
	default:
		// code_issued: the device code was already exchanged; it is single-use.
		h.polls.forget(deviceCodeHash)
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "expired_token", "the device code has already been used")
		return
	}

	accessToken, refreshToken, refreshTokenObj, err := h.issuance.issueTokenPair(ctx, req.ID, req.ClientID, req.BfMode, req.BfSub, req.Scope, req.Resource)
	if err != nil {
		return
	}
	// Atomically consume the request and persist the refresh token, so two
	// concurrent polls cannot both receive tokens for one approval.
	if err := h.store.ConfigStore.ConsumeOAuth2AuthorizeRequest(ctx, req.ID, refreshTokenObj); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "expired_token", "the device code has already been used")
			return
		}
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to issue token")
		return
	}
	h.polls.forget(deviceCodeHash)
	sendTokenResponse(ctx, accessToken, refreshToken, req.Scope, oauth2ServerCfg(h.store).AccessTokenTTL)
}

// --- POST /claude-code/oauth/revoke (RFC 7009) ---

// handleRevoke runs on Claude Code sign-out. A refresh token revokes its whole
// grant family, which also stops the next refresh; access tokens are
// stateless JWTs and simply expire. Per RFC 7009 §2.2 an unknown token is not
// an error.
func (h *ClaudeCodeGatewayHandler) handleRevoke(ctx *fasthttp.RequestCtx) {
	if !h.enabled() {
		sendOAuthError(ctx, fasthttp.StatusNotFound, "not_found", "the claude code gateway is not enabled")
		return
	}
	if h.store.ConfigStore == nil {
		sendOAuthError(ctx, fasthttp.StatusServiceUnavailable, "server_error", "config store unavailable")
		return
	}
	token := string(ctx.FormValue("token"))
	if token == "" {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_request", "token is required")
		return
	}
	rt, err := h.store.ConfigStore.GetOAuth2RefreshTokenByHash(ctx, hashSHA256Hex(token))
	switch {
	case errors.Is(err, configstore.ErrNotFound):
		// An access token or an already-revoked refresh token: nothing to do.
	case err != nil:
		sendOAuthError(ctx, fasthttp.StatusServiceUnavailable, "temporarily_unavailable", "failed to look up token")
		return
	case rt.ClientID == claudeCodeClientID:
		if err := h.store.ConfigStore.RevokeOAuth2RefreshTokensByFamilyID(ctx, rt.FamilyID); err != nil {
			sendOAuthError(ctx, fasthttp.StatusServiceUnavailable, "temporarily_unavailable", "failed to revoke token")
			return
		}
	}
	ctx.SetStatusCode(fasthttp.StatusOK)
}

// --- POST /claude-code/device/verify ---

type claudeCodeDeviceVerifyRequest struct {
	UserCode string `json:"user_code"`
}

type claudeCodeDeviceVerifyResponse struct {
	ConsentURL string `json:"consent_url"`
}

// handleDeviceVerify is called by the /oauth/device dashboard page with the code
// the user typed. It returns the consent page URL for the matching sign-in,
// carrying the same consent-scoped temp token /oauth2/authorize mints, so the
// user picks an identity exactly as an MCP client's user does.
func (h *ClaudeCodeGatewayHandler) handleDeviceVerify(ctx *fasthttp.RequestCtx) {
	if !h.enabled() {
		SendError(ctx, fasthttp.StatusNotFound, "the claude code gateway is not enabled")
		return
	}
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store unavailable")
		return
	}
	if !h.verifyLimiter.allow(ctx.RemoteIP().String(), time.Now()) {
		ctx.Response.Header.Set("Retry-After", "60")
		SendError(ctx, fasthttp.StatusTooManyRequests, "too many attempts from this address; wait a minute and try again")
		return
	}
	var body claudeCodeDeviceVerifyRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	userCode := normalizeClaudeCodeUserCode(body.UserCode)
	if len(userCode) != claudeCodeUserCodeLength {
		SendError(ctx, fasthttp.StatusBadRequest, "enter the 8-character code shown in Claude Code")
		return
	}
	req, err := h.store.ConfigStore.GetPendingOAuth2AuthorizeRequestByUserCodeHash(ctx, hashSHA256Hex(userCode))
	if errors.Is(err, configstore.ErrNotFound) {
		SendError(ctx, fasthttp.StatusNotFound, "this code is not valid or has expired; run /login in Claude Code again")
		return
	}
	if err != nil {
		logger.Error("claude code gateway: failed to look up user code: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to look up code")
		return
	}

	consentURL := fmt.Sprintf("%s/oauth/consent?flow=%s", oauth2IssuerURL(ctx, h.store), url.QueryEscape(req.ID))
	if h.tempTokens != nil {
		tok, err := h.tempTokens.Mint(ctx, temptoken.OAuth2ConsentScopeName, req.ID, time.Until(req.ExpiresAt))
		if err != nil {
			logger.Error("claude code gateway: failed to mint consent temp token: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, "failed to prepare consent flow")
			return
		}
		consentURL += "#t=" + url.QueryEscape(tok)
	}
	SendJSON(ctx, claudeCodeDeviceVerifyResponse{ConsentURL: consentURL})
}

// --- GET /claude-code/managed/settings ---

type claudeCodeManagedSettingsResponse struct {
	UUID     string         `json:"uuid"`
	Checksum string         `json:"checksum"`
	Settings map[string]any `json:"settings"`
}

// handleManagedSettings serves the configured managed-settings document. The
// checksum is the one Claude Code computes over the settings it holds
// ("sha256:" + hex SHA-256 of key-sorted, whitespace-free JSON), so its hourly
// If-None-Match poll is answered with 304 while nothing changed.
func (h *ClaudeCodeGatewayHandler) handleManagedSettings(ctx *fasthttp.RequestCtx) {
	h.store.Mu.RLock()
	var settings map[string]any
	if cfg := h.store.ClientConfig.OAuth2ServerConfig; cfg.IsClaudeCodeGatewayEnabled() {
		settings = cfg.ClaudeCodeGateway.ManagedSettings
	}
	h.store.Mu.RUnlock()
	if settings == nil {
		// No managed policy, which Claude Code distinguishes from an empty one.
		sendAnthropicError(ctx, fasthttp.StatusNotFound, "not_found_error", "no managed settings are configured on this gateway")
		return
	}
	checksum, err := claudeCodeSettingsChecksum(settings)
	if err != nil {
		sendAnthropicError(ctx, fasthttp.StatusInternalServerError, "api_error", "failed to encode managed settings")
		return
	}
	etag := `"` + checksum + `"`
	ctx.Response.Header.Set("ETag", etag)
	if string(ctx.Request.Header.Peek("If-None-Match")) == etag {
		ctx.SetStatusCode(fasthttp.StatusNotModified)
		return
	}
	SendJSON(ctx, claudeCodeManagedSettingsResponse{UUID: checksum, Checksum: checksum, Settings: settings})
}

// claudeCodeSettingsChecksum returns "sha256:" + the hex SHA-256 of settings
// serialized with sorted keys and no whitespace, matching the value Claude Code
// sends back in If-None-Match. encoding/json sorts map keys at every level;
// HTML escaping is turned off because the client's serializer does not escape.
func claudeCodeSettingsChecksum(settings map[string]any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(settings); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// --- POST /claude-code/v1/{metrics,logs,traces} ---

// handleOTLP accepts Claude Code's OTLP/HTTP exports. While signed in to a
// gateway the client sends telemetry here instead of to its configured
// collector; the protocol asks for 200 whether the gateway forwards or discards,
// since a 404 makes the exporter log an error on every flush. Bifrost discards
// them: request-level usage is already captured by the inference path.
func (h *ClaudeCodeGatewayHandler) handleOTLP(ctx *fasthttp.RequestCtx) {
	ctx.SetStatusCode(fasthttp.StatusOK)
}

// --- Bearer authentication ---

// BearerMiddleware authenticates the gateway bearer on every bearer route.
// It verifies the JWT against the /claude-code audience, re-checks the identity
// it names (the same cut-offs the /mcp path applies), and then rewrites the
// request to carry that identity the way a direct caller would: a vk-mode token
// becomes an x-bf-vk header, a user-mode token the authenticated user ID, and a
// session-mode token nothing (anonymous, refused while auth is enforced). The
// bearer itself is removed so it never reaches a provider.
//
// Failures use the Anthropic error envelope with x-should-retry: false, which
// makes Claude Code prompt for /login at once instead of retrying.
func (h *ClaudeCodeGatewayHandler) BearerMiddleware(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		if !h.enabled() {
			sendAnthropicError(ctx, fasthttp.StatusNotFound, "not_found_error", "the claude code gateway is not enabled")
			return
		}
		if status, msg := h.authenticate(ctx); status != 0 {
			if status == fasthttp.StatusUnauthorized {
				ctx.Response.Header.Set("x-should-retry", "false")
				sendAnthropicError(ctx, status, "authentication_error", msg)
				return
			}
			sendAnthropicError(ctx, status, "api_error", msg)
			return
		}
		next(ctx)
	}
}

// authenticate verifies the bearer and stamps the identity it carries. It
// returns a zero status on success, otherwise the HTTP status and message to
// send.
func (h *ClaudeCodeGatewayHandler) authenticate(ctx *fasthttp.RequestCtx) (int, string) {
	rawJWT := extractBearerJWT(ctx)
	if rawJWT == "" {
		return fasthttp.StatusUnauthorized, "sign in to this gateway with /login"
	}
	signingKey, err := h.store.GetOAuth2SigningKey(ctx)
	if err != nil {
		logger.Error("claude code gateway: failed to load oauth2 signing key: %v", err)
		return fasthttp.StatusServiceUnavailable, "signing key unavailable"
	}
	claims, err := verifyOAuth2JWT(ctx, rawJWT, h.store, signingKey, claudeCodeResourceURL(ctx, h.store))
	if err != nil {
		if strings.HasPrefix(err.Error(), "invalid token") || strings.HasPrefix(err.Error(), "token ") {
			return fasthttp.StatusUnauthorized, "the gateway session is invalid or expired; sign in again with /login"
		}
		logger.Error("claude code gateway: token verification failed: %v", err)
		return fasthttp.StatusServiceUnavailable, "token verification unavailable"
	}
	if claims.Subject == "" {
		return fasthttp.StatusUnauthorized, "the gateway session is invalid; sign in again with /login"
	}

	switch schemas.MCPAuthMode(claims.BfMode) {
	case schemas.MCPAuthModeVK:
		if oauth2ServerCfg(h.store).DisableVKIdentity && h.identityResolver != nil && h.identityResolver.IsUserModeAvailable() {
			return fasthttp.StatusUnauthorized, "virtual-key sign-in is no longer accepted; sign in again with /login"
		}
		vk, err := h.virtualKeyByID(ctx, claims.Subject)
		if err != nil {
			return fasthttp.StatusServiceUnavailable, "failed to verify virtual key"
		}
		if vk == nil || !vk.IsActiveValue() {
			return fasthttp.StatusUnauthorized, "the virtual key behind this session is no longer active; sign in again with /login"
		}
		stripGatewayCredentials(ctx)
		ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), vk.Value.GetValue())
	case schemas.MCPAuthModeUser:
		if h.identityResolver != nil {
			active, err := h.identityResolver.IsUserActive(ctx, claims.Subject)
			if err != nil {
				return fasthttp.StatusServiceUnavailable, "failed to verify user"
			}
			if !active {
				return fasthttp.StatusUnauthorized, "this user is no longer active; sign in again with /login"
			}
		}
		stripGatewayCredentials(ctx)
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, claims.Subject)
	case schemas.MCPAuthModeSession:
		h.store.Mu.RLock()
		enforceAuth := h.store.ClientConfig.EnforceAuthOnInference
		h.store.Mu.RUnlock()
		if enforceAuth {
			return fasthttp.StatusUnauthorized, "anonymous sessions are not accepted while authentication is enforced; sign in again with /login using a virtual key or your account"
		}
		stripGatewayCredentials(ctx)
	default:
		return fasthttp.StatusUnauthorized, "the gateway session is invalid; sign in again with /login"
	}
	return 0, ""
}

// virtualKeyByID resolves the key a vk-mode token names, preferring the
// governance in-memory store. It returns (nil, nil) when the key is gone.
func (h *ClaudeCodeGatewayHandler) virtualKeyByID(ctx context.Context, id string) (*configtables.TableVirtualKey, error) {
	if h.vkCache != nil {
		if vk, ok := h.vkCache.GetVirtualKeyByID(ctx, id); ok && vk != nil {
			return vk, nil
		}
	}
	if h.store.ConfigStore == nil {
		return nil, fmt.Errorf("config store unavailable")
	}
	vk, err := h.store.ConfigStore.GetVirtualKey(ctx, id)
	if errors.Is(err, configstore.ErrNotFound) {
		return nil, nil
	}
	return vk, err
}

// stripGatewayCredentials removes every header that could carry a credential
// other than the identity the gateway token resolved to.
func stripGatewayCredentials(ctx *fasthttp.RequestCtx) {
	for _, header := range []string{"Authorization", "x-api-key", "x-goog-api-key", "api-key", string(schemas.BifrostContextKeyVirtualKey)} {
		ctx.Request.Header.Del(header)
	}
}

// sendAnthropicError writes the Anthropic error envelope, which Claude Code's
// SDK surfaces to the user verbatim.
func sendAnthropicError(ctx *fasthttp.RequestCtx, status int, errType, message string) {
	data, err := sonic.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": errType, "message": message},
	})
	if err != nil {
		ctx.Error(message, status)
		return
	}
	ctx.SetStatusCode(status)
	ctx.SetContentType("application/json")
	ctx.SetBody(data)
}

// --- User codes ---

// generateClaudeCodeUserCode returns an 8-character base-20 user code shown as
// XXXX-XXXX (RFC 8628 §6.1).
func generateClaudeCodeUserCode() (string, error) {
	var b strings.Builder
	alphabetSize := big.NewInt(int64(len(claudeCodeUserCodeAlphabet)))
	for i := 0; i < claudeCodeUserCodeLength; i++ {
		if i == claudeCodeUserCodeLength/2 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, alphabetSize)
		if err != nil {
			return "", err
		}
		b.WriteByte(claudeCodeUserCodeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// normalizeClaudeCodeUserCode uppercases a typed user code and drops the
// separator and any whitespace, so "wdjb-mjht" and "WDJB MJHT" both match.
func normalizeClaudeCodeUserCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// --- Rate limiting and poll pacing ---

// ipRateLimiter is a fixed-window per-key counter for the unauthenticated
// sign-in endpoints. It is per process: behind several replicas the effective
// limit scales with the replica count, which is fine for abuse damping. Callers
// key it by ctx.RemoteIP(), never by X-Forwarded-For: Bifrost has no
// trusted-proxy setting, and a caller-controlled header would let anyone pick a
// fresh bucket per request.
type ipRateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	windows map[string]*rateWindow
}

type rateWindow struct {
	start time.Time
	count int
}

// newIPRateLimiter allows limit hits per key within each window.
func newIPRateLimiter(limit int, window time.Duration) *ipRateLimiter {
	return &ipRateLimiter{limit: limit, window: window, windows: make(map[string]*rateWindow)}
}

// allow records a hit for key and reports whether it is within the limit.
func (l *ipRateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.windows) > 4096 {
		for k, w := range l.windows {
			if now.Sub(w.start) >= l.window {
				delete(l.windows, k)
			}
		}
	}
	w, ok := l.windows[key]
	if !ok || now.Sub(w.start) >= l.window {
		l.windows[key] = &rateWindow{start: now, count: 1}
		return true
	}
	w.count++
	return w.count <= l.limit
}

// devicePollTracker remembers when each pending device code last polled, so a
// client polling faster than the advertised interval gets slow_down
// (RFC 8628 §3.5).
type devicePollTracker struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// newDevicePollTracker creates an empty poll tracker.
func newDevicePollTracker() *devicePollTracker {
	return &devicePollTracker{last: make(map[string]time.Time)}
}

// tooSoon records a poll and reports whether it came well inside the interval.
// A second of slack absorbs client timer jitter.
func (t *devicePollTracker) tooSoon(deviceCodeHash string, now time.Time, interval time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.last) > 4096 {
		for k, at := range t.last {
			if now.Sub(at) > claudeCodeDeviceCodeTTL {
				delete(t.last, k)
			}
		}
	}
	prev, seen := t.last[deviceCodeHash]
	t.last[deviceCodeHash] = now
	return seen && now.Sub(prev) < interval-time.Second
}

// forget drops a finished device code.
func (t *devicePollTracker) forget(deviceCodeHash string) {
	t.mu.Lock()
	delete(t.last, deviceCodeHash)
	t.mu.Unlock()
}
