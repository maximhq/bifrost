package handlers

import (
	"context"
	"crypto/rand"
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
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/temptoken"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// Client gateway protocol constants, shared by every client profile.
const (
	// deviceCodeGrantType is the RFC 8628 §3.4 grant type.
	deviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	// clientGatewayDeviceCodeTTL bounds how long a sign-in may wait for the browser.
	clientGatewayDeviceCodeTTL = 10 * time.Minute
	// clientGatewayDevicePollInterval is the polling interval handed to the client.
	clientGatewayDevicePollInterval = 5 * time.Second
	// clientGatewayUserCodeAlphabet is RFC 8628 §6.1's base-20 consonant set: no
	// vowels (no accidental words) and no easily confused characters.
	clientGatewayUserCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
	clientGatewayUserCodeLength   = 8
	// Per-IP limits for the two unauthenticated sign-in endpoints (RFC 8628 §5.1, §5.2).
	// Keyed by the TCP peer, so behind a reverse proxy every client of one process
	// shares a bucket; the limits leave room for that while keeping user-code
	// guessing negligible (20^8 codes, each valid for 10 minutes).
	clientGatewayDeviceAuthorizationLimit = 120
	clientGatewayDeviceVerifyLimit        = 120
	clientGatewayRateLimitWindow          = time.Minute
)

// ClientGatewayProfile is what one coding client's gateway protocol adds on top
// of the shared client gateway core. The core owns sign-in (RFC 8628 device
// grant through the OAuth consent page), refresh and revocation, the bearer
// check that turns a gateway token into the virtual key or user it stands for,
// the verified-token cache and the sign-in rate limits. A profile names the
// client and supplies only what its protocol needs beyond that.
type ClientGatewayProfile interface {
	// Name identifies the client in logs and messages, e.g. "claude-code".
	Name() string
	// PathPrefix is the base URL path the client is pointed at, e.g. "/claude-code".
	PathPrefix() string
	// ClientID names the built-in OAuth client every grant of this gateway is bound to.
	ClientID() string
	// DisplayName is shown on the consent page and in the OAuth grants list.
	DisplayName() string
	// Scope is the scope advertised and stamped on this gateway's tokens.
	Scope() string
	// Enabled reports whether the operator switched this gateway on.
	Enabled(cfg *configtables.OAuth2ServerConfig) bool
	// SignInHint tells the user how to sign in again, appended to auth errors.
	SignInHint() string
	// InferenceRoutes returns the integration routes served under prefix.
	InferenceRoutes(prefix string, store *lib.Config) []integrations.RouteConfig
	// WriteAuthError writes a bearer failure in the client's own error format.
	// unauthorized is true when the client should prompt the user to sign in again.
	WriteAuthError(ctx *fasthttp.RequestCtx, status int, unauthorized bool, message string)
	// RegisterExtraRoutes adds protocol endpoints beyond sign-in and inference,
	// such as managed settings or a telemetry sink, using ep for the prefix, the
	// store and the bearer check.
	RegisterExtraRoutes(r *router.Router, ep *clientGatewayEndpoint)
}

// clientGatewayProfiles lists every built-in client gateway.
var clientGatewayProfiles = []ClientGatewayProfile{claudeCodeProfile{}}

// clientGatewayProfileByClientID returns the profile whose built-in OAuth client
// is clientID, or nil when clientID belongs to no client gateway.
func clientGatewayProfileByClientID(clientID string) ClientGatewayProfile {
	for _, p := range clientGatewayProfiles {
		if p.ClientID() == clientID {
			return p
		}
	}
	return nil
}

// clientGatewayDeviceVerificationURL is the dashboard page where the user enters
// the code their client displays. It is shared by every client gateway.
func clientGatewayDeviceVerificationURL(issuer string) string {
	return strings.TrimRight(issuer, "/") + "/oauth/device"
}

// clientGatewayDeviceApprovedURL is where the consent page sends the browser after
// a device grant is approved; the page tells the user to return to their client.
func clientGatewayDeviceApprovedURL(issuer string) string {
	return clientGatewayDeviceVerificationURL(issuer) + "?approved=1"
}

// ClientGatewayHandler serves every client gateway. Per client it mounts, under
// the profile's prefix:
//
//   - GET  /.well-known/oauth-authorization-server — RFC 8414 metadata
//   - POST /oauth/device_authorization — RFC 8628 device authorization
//   - POST /oauth/token — device-code and refresh-token grants
//   - POST /oauth/revoke — RFC 7009 revocation on sign-out
//   - POST /device/verify — exchanges a user code for the consent page
//   - the profile's inference routes and extra routes, behind the bearer check
//
// Sign-in reuses the MCP OAuth2 authorization server: a device grant is an
// oauth2_authorize_requests row that the existing consent page approves (virtual
// key, signed-in user, or anonymous session), and the issued tokens are the same
// RS256 JWTs and rotating refresh tokens, with the gateway's own audience. It is
// independent of mcp_server_auth_mode: a gateway's routes answer 404 unless its
// profile is enabled and issuer_url is set.
//
// The rate limiters, poll tracker and verified-token cache are shared by every
// client gateway; token cache entries are keyed by audience, so they never cross.
type ClientGatewayHandler struct {
	store            *lib.Config
	issuance         *OAuth2IssuanceHandler
	tempTokens       *temptoken.Service     // optional; nil = consent page needs a dashboard session
	identityResolver OAuth2IdentityResolver // optional; nil = vk and session identities only
	vkCache          VirtualKeyCache        // optional; nil = virtual keys read from the config store

	deviceAuthLimiter *ipRateLimiter
	verifyLimiter     *ipRateLimiter
	polls             *devicePollTracker
	tokens            *gatewayTokenCache

	endpoints []*clientGatewayEndpoint
}

// clientGatewayEndpoint is one profile mounted on the shared handler.
type clientGatewayEndpoint struct {
	*ClientGatewayHandler
	profile ClientGatewayProfile
}

// NewClientGatewayHandler creates the client gateway handler for profiles, or
// for every built-in profile when none are given. tempTokens, identityResolver
// and vkCache may each be nil.
func NewClientGatewayHandler(store *lib.Config, tempTokens *temptoken.Service, identityResolver OAuth2IdentityResolver, vkCache VirtualKeyCache, profiles ...ClientGatewayProfile) *ClientGatewayHandler {
	if len(profiles) == 0 {
		profiles = clientGatewayProfiles
	}
	h := &ClientGatewayHandler{
		store:             store,
		issuance:          NewOAuth2IssuanceHandler(store, tempTokens, identityResolver),
		tempTokens:        tempTokens,
		identityResolver:  identityResolver,
		vkCache:           vkCache,
		deviceAuthLimiter: newIPRateLimiter(clientGatewayDeviceAuthorizationLimit, clientGatewayRateLimitWindow),
		verifyLimiter:     newIPRateLimiter(clientGatewayDeviceVerifyLimit, clientGatewayRateLimitWindow),
		polls:             newDevicePollTracker(),
		tokens:            newGatewayTokenCache(clientGatewayTokenCacheMaxEntries),
	}
	for _, p := range profiles {
		h.endpoints = append(h.endpoints, &clientGatewayEndpoint{ClientGatewayHandler: h, profile: p})
	}
	return h
}

// endpoint returns the mounted profile named name, or nil.
func (h *ClientGatewayHandler) endpoint(name string) *clientGatewayEndpoint {
	for _, ep := range h.endpoints {
		if ep.profile.Name() == name {
			return ep
		}
	}
	return nil
}

// RegisterRoutes wires every profile's sign-in and extra routes. They are
// registered without the API auth middleware: the sign-in endpoints are public
// by protocol, and the rest authenticate the gateway bearer themselves.
func (h *ClientGatewayHandler) RegisterRoutes(r *router.Router, _ ...schemas.BifrostHTTPMiddleware) {
	for _, ep := range h.endpoints {
		prefix := ep.profile.PathPrefix()
		r.GET(prefix+"/.well-known/oauth-authorization-server", ep.handleMetadata)
		r.POST(prefix+"/oauth/device_authorization", ep.handleDeviceAuthorization)
		r.POST(prefix+"/oauth/token", ep.handleToken)
		r.POST(prefix+"/oauth/revoke", ep.handleRevoke)
		r.POST(prefix+"/device/verify", ep.handleDeviceVerify)
		ep.profile.RegisterExtraRoutes(r, ep)
	}
}

// RegisterInferenceRoutes wires every profile's inference routes. The bearer
// check runs first and turns the gateway token into the identity it stands for,
// so the inference middlewares and governance then treat the request exactly
// like one that presented that virtual key (or user) directly.
func (h *ClientGatewayHandler) RegisterInferenceRoutes(r *router.Router, client *bifrost.Bifrost, accessResolver integrations.AccessResolver, middlewares ...schemas.BifrostHTTPMiddleware) {
	for _, ep := range h.endpoints {
		routes := ep.profile.InferenceRoutes(ep.profile.PathPrefix(), h.store)
		if len(routes) == 0 {
			continue
		}
		chain := append([]schemas.BifrostHTTPMiddleware{ep.BearerMiddleware}, middlewares...)
		integrations.NewGenericRouter(client, h.store, accessResolver, routes, nil, logger).RegisterRoutes(r, chain...)
	}
}

// enabled reports whether this gateway is switched on. It also requires a
// configured issuer_url: every URL and token claim the gateway hands out derives
// from it, and the Host-header fallback oauth2IssuerURL keeps for header-only
// deployments is caller-controlled, so a gateway never runs on it.
func (ep *clientGatewayEndpoint) enabled() bool {
	ep.store.Mu.RLock()
	defer ep.store.Mu.RUnlock()
	cc := ep.store.ClientConfig
	if cc == nil || !ep.profile.Enabled(cc.OAuth2ServerConfig) {
		return false
	}
	issuer := cc.OAuth2ServerConfig.IssuerURL
	return issuer.IsSet() && issuer.GetValue() != ""
}

// notEnabledMessage is the 404 message for a gateway that is switched off.
func (ep *clientGatewayEndpoint) notEnabledMessage() string {
	return fmt.Sprintf("the %s gateway is not enabled", ep.profile.Name())
}

// baseURL is the URL the client is pointed at: {issuer}{prefix}.
func (ep *clientGatewayEndpoint) baseURL(ctx *fasthttp.RequestCtx) string {
	return strings.TrimRight(oauth2IssuerURL(ctx, ep.store), "/") + ep.profile.PathPrefix()
}

// resourceURL is the RFC 8707 audience of every token this gateway issues. It
// differs from the /mcp resource and from every other gateway's, so a token is
// accepted only by the gateway that issued it.
func (ep *clientGatewayEndpoint) resourceURL(ctx *fasthttp.RequestCtx) string {
	return ep.baseURL(ctx)
}

// --- GET {prefix}/.well-known/oauth-authorization-server ---

// handleMetadata serves the RFC 8414 metadata the client reads at sign-in. Every
// endpoint sits under the base URL, since the client only uses same-origin ones.
func (ep *clientGatewayEndpoint) handleMetadata(ctx *fasthttp.RequestCtx) {
	if !ep.enabled() {
		sendOAuthError(ctx, fasthttp.StatusNotFound, "not_found", ep.notEnabledMessage())
		return
	}
	ctx.Response.Header.Set("Cache-Control", "no-store")
	base := ep.baseURL(ctx)
	data, err := sonic.Marshal(map[string]any{
		"issuer":                                base,
		"device_authorization_endpoint":         base + "/oauth/device_authorization",
		"token_endpoint":                        base + "/oauth/token",
		"revocation_endpoint":                   base + "/oauth/revoke",
		"grant_types_supported":                 []string{deviceCodeGrantType, "refresh_token"},
		"response_types_supported":              []string{},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{ep.profile.Scope()},
	})
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to marshal metadata")
		return
	}
	ctx.SetContentType("application/json")
	ctx.SetBody(data)
}

// --- POST {prefix}/oauth/device_authorization (RFC 8628 §3.1) ---

// handleDeviceAuthorization starts a sign-in. Clients may send no client_id or
// scope (Claude Code sends only surface=claude_code), so the gateway binds the
// grant to its built-in client; unknown parameters are ignored as RFC 6749 §3.1
// requires.
func (ep *clientGatewayEndpoint) handleDeviceAuthorization(ctx *fasthttp.RequestCtx) {
	if !ep.enabled() {
		sendOAuthError(ctx, fasthttp.StatusNotFound, "not_found", ep.notEnabledMessage())
		return
	}
	if ep.store.ConfigStore == nil {
		sendOAuthError(ctx, fasthttp.StatusServiceUnavailable, "server_error", "config store unavailable")
		return
	}
	if !ep.deviceAuthLimiter.allow(ctx.RemoteIP().String(), time.Now()) {
		ctx.Response.Header.Set("Retry-After", "60")
		sendOAuthError(ctx, fasthttp.StatusTooManyRequests, "slow_down", "too many sign-in attempts from this address; wait a minute and try again")
		return
	}
	if err := ep.ensureClient(ctx); err != nil {
		logger.Error("%s gateway: failed to register the built-in oauth client: %v", ep.profile.Name(), err)
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to prepare sign-in")
		return
	}

	deviceCode, err := generateSecureToken(32)
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to generate device code")
		return
	}
	deviceCodeHash := hashSHA256Hex(deviceCode)
	resource := ep.resourceURL(ctx)
	now := time.Now()

	// A user-code collision trips the unique index; draw again rather than fail
	// the sign-in. With 20^8 codes this is vanishingly rare.
	var userCode string
	for attempt := 0; ; attempt++ {
		userCode, err = generateGatewayUserCode()
		if err != nil {
			sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to generate user code")
			return
		}
		userCodeHash := hashSHA256Hex(normalizeGatewayUserCode(userCode))
		req := &configtables.TableOAuth2AuthorizeRequest{
			ID:             uuid.New().String(),
			ClientID:       ep.profile.ClientID(),
			Scope:          ep.profile.Scope(),
			Resource:       resource,
			Status:         configtables.OAuth2AuthorizeRequestStatusPending,
			DeviceCodeHash: &deviceCodeHash,
			UserCodeHash:   &userCodeHash,
			ExpiresAt:      now.Add(clientGatewayDeviceCodeTTL),
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		err = ep.store.ConfigStore.CreateOAuth2AuthorizeRequest(ctx, req)
		if err == nil {
			break
		}
		if attempt == 2 {
			logger.Error("%s gateway: failed to create device authorization: %v", ep.profile.Name(), err)
			sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to create device authorization")
			return
		}
	}

	verificationURI := clientGatewayDeviceVerificationURL(oauth2IssuerURL(ctx, ep.store))
	data, err := sonic.Marshal(map[string]any{
		"device_code":               deviceCode,
		"user_code":                 userCode,
		"verification_uri":          verificationURI,
		"verification_uri_complete": verificationURI + "?user_code=" + url.QueryEscape(userCode),
		"expires_in":                int(clientGatewayDeviceCodeTTL.Seconds()),
		"interval":                  int(clientGatewayDevicePollInterval.Seconds()),
	})
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to marshal response")
		return
	}
	ctx.Response.Header.Set("Cache-Control", "no-store")
	ctx.SetContentType("application/json")
	ctx.SetBody(data)
}

// ensureClient makes sure this gateway's built-in OAuth client row exists, so
// the consent page and the connected-clients list can name the client. It is
// checked on every sign-in rather than cached, because the orphaned-client
// sweep deletes it whenever no grant of this gateway is left. A concurrent
// creator losing the unique-index race re-reads the winner's row.
func (ep *clientGatewayEndpoint) ensureClient(ctx context.Context) error {
	clientID := ep.profile.ClientID()
	_, err := ep.store.ConfigStore.GetOAuth2ClientByClientID(ctx, clientID)
	if errors.Is(err, configstore.ErrNotFound) {
		createErr := ep.store.ConfigStore.CreateOAuth2Client(ctx, &configtables.TableOAuth2Client{
			ID:           uuid.New().String(),
			ClientID:     clientID,
			ClientName:   ep.profile.DisplayName(),
			RedirectURIs: []string{}, // device grant only: /oauth2/authorize refuses every redirect_uri
			GrantTypes:   []string{deviceCodeGrantType, "refresh_token"},
			Scope:        ep.profile.Scope(),
			CreatedAt:    time.Now(),
		})
		if createErr != nil {
			_, err = ep.store.ConfigStore.GetOAuth2ClientByClientID(ctx, clientID)
		} else {
			err = nil
		}
	}
	return err
}

// --- POST {prefix}/oauth/token ---

// handleToken dispatches the device-code and refresh-token grants.
func (ep *clientGatewayEndpoint) handleToken(ctx *fasthttp.RequestCtx) {
	if !ep.enabled() {
		sendOAuthError(ctx, fasthttp.StatusNotFound, "not_found", ep.notEnabledMessage())
		return
	}
	if ep.store.ConfigStore == nil {
		sendOAuthError(ctx, fasthttp.StatusServiceUnavailable, "server_error", "config store unavailable")
		return
	}
	switch grantType := string(ctx.FormValue("grant_type")); grantType {
	case deviceCodeGrantType:
		ep.handleDeviceCodeGrant(ctx, string(ctx.FormValue("device_code")))
	case "refresh_token":
		refreshToken := string(ctx.FormValue("refresh_token"))
		if refreshToken == "" {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_request", "refresh_token is required")
			return
		}
		// Bound to this gateway's built-in client and audience, so a refresh token
		// issued to any other client (an MCP client's, or another gateway's) is
		// refused here with invalid_grant.
		ep.issuance.refreshGrant(ctx, refreshToken, ep.profile.ClientID(), "", ep.resourceURL(ctx))
	default:
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "unsupported_grant_type", fmt.Sprintf("grant_type %q not supported", grantType))
	}
}

// handleDeviceCodeGrant answers a device's token poll (RFC 8628 §3.5): pending
// until the consent page approves the request, then tokens exactly once.
func (ep *clientGatewayEndpoint) handleDeviceCodeGrant(ctx *fasthttp.RequestCtx, deviceCode string) {
	if deviceCode == "" {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_request", "device_code is required")
		return
	}
	deviceCodeHash := hashSHA256Hex(deviceCode)
	req, err := ep.store.ConfigStore.GetOAuth2AuthorizeRequestByDeviceCodeHash(ctx, deviceCodeHash)
	if errors.Is(err, configstore.ErrNotFound) || (err == nil && req.ClientID != ep.profile.ClientID()) {
		// Unknown, swept after expiry, never issued, or issued by another gateway:
		// the client must restart.
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "expired_token", "the device code is unknown or has expired")
		return
	}
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to look up device code")
		return
	}
	now := time.Now()
	if now.After(req.ExpiresAt) {
		ep.polls.forget(deviceCodeHash)
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "expired_token", "the device code has expired")
		return
	}
	switch req.Status {
	case configtables.OAuth2AuthorizeRequestStatusPending:
		if ep.polls.tooSoon(deviceCodeHash, now, clientGatewayDevicePollInterval) {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "slow_down", "polling faster than the advertised interval")
			return
		}
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "authorization_pending", "waiting for the user to approve the sign-in")
		return
	case configtables.OAuth2AuthorizeRequestStatusRevoked:
		ep.polls.forget(deviceCodeHash)
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "access_denied", "the sign-in was denied")
		return
	case configtables.OAuth2AuthorizeRequestStatusConsented:
		// handled below
	default:
		// code_issued: the device code was already exchanged; it is single-use.
		ep.polls.forget(deviceCodeHash)
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "expired_token", "the device code has already been used")
		return
	}

	accessToken, refreshToken, refreshTokenObj, err := ep.issuance.issueTokenPair(ctx, req.ID, req.ClientID, req.BfMode, req.BfSub, req.Scope, req.Resource, ep.profile.DisplayName())
	if err != nil {
		return
	}
	// Atomically consume the request and persist the refresh token, so two
	// concurrent polls cannot both receive tokens for one approval.
	if err := ep.store.ConfigStore.ConsumeOAuth2AuthorizeRequest(ctx, req.ID, refreshTokenObj); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "expired_token", "the device code has already been used")
			return
		}
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "failed to issue token")
		return
	}
	ep.polls.forget(deviceCodeHash)
	sendTokenResponse(ctx, accessToken, refreshToken, req.Scope, oauth2ServerCfg(ep.store).AccessTokenTTL)
}

// --- POST {prefix}/oauth/revoke (RFC 7009) ---

// handleRevoke runs on sign-out. A refresh token revokes its whole grant
// family, which also stops the next refresh; access tokens are stateless JWTs
// and simply expire. Per RFC 7009 §2.2 an unknown token is not an error, and a
// token of another client is left alone.
func (ep *clientGatewayEndpoint) handleRevoke(ctx *fasthttp.RequestCtx) {
	if !ep.enabled() {
		sendOAuthError(ctx, fasthttp.StatusNotFound, "not_found", ep.notEnabledMessage())
		return
	}
	if ep.store.ConfigStore == nil {
		sendOAuthError(ctx, fasthttp.StatusServiceUnavailable, "server_error", "config store unavailable")
		return
	}
	token := string(ctx.FormValue("token"))
	if token == "" {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_request", "token is required")
		return
	}
	rt, err := ep.store.ConfigStore.GetOAuth2RefreshTokenByHash(ctx, hashSHA256Hex(token))
	switch {
	case errors.Is(err, configstore.ErrNotFound):
		// An access token or an already-revoked refresh token: nothing to do.
	case err != nil:
		sendOAuthError(ctx, fasthttp.StatusServiceUnavailable, "temporarily_unavailable", "failed to look up token")
		return
	case rt.ClientID == ep.profile.ClientID():
		if err := ep.store.ConfigStore.RevokeOAuth2RefreshTokensByFamilyID(ctx, rt.FamilyID); err != nil {
			sendOAuthError(ctx, fasthttp.StatusServiceUnavailable, "temporarily_unavailable", "failed to revoke token")
			return
		}
	}
	ctx.SetStatusCode(fasthttp.StatusOK)
}

// --- POST {prefix}/device/verify ---

type clientGatewayDeviceVerifyRequest struct {
	UserCode string `json:"user_code"`
}

type clientGatewayDeviceVerifyResponse struct {
	ConsentURL string `json:"consent_url"`
}

// handleDeviceVerify is called by the /oauth/device dashboard page with the code
// the user typed. It returns the consent page URL for the matching sign-in,
// carrying the same consent-scoped temp token /oauth2/authorize mints, so the
// user picks an identity exactly as an MCP client's user does.
func (ep *clientGatewayEndpoint) handleDeviceVerify(ctx *fasthttp.RequestCtx) {
	if !ep.enabled() {
		SendError(ctx, fasthttp.StatusNotFound, ep.notEnabledMessage())
		return
	}
	if ep.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store unavailable")
		return
	}
	if !ep.verifyLimiter.allow(ctx.RemoteIP().String(), time.Now()) {
		ctx.Response.Header.Set("Retry-After", "60")
		SendError(ctx, fasthttp.StatusTooManyRequests, "too many attempts from this address; wait a minute and try again")
		return
	}
	var body clientGatewayDeviceVerifyRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	userCode := normalizeGatewayUserCode(body.UserCode)
	if len(userCode) != clientGatewayUserCodeLength {
		SendError(ctx, fasthttp.StatusBadRequest, "enter the 8-character code shown in your terminal")
		return
	}
	req, err := ep.store.ConfigStore.GetPendingOAuth2AuthorizeRequestByUserCodeHash(ctx, hashSHA256Hex(userCode))
	if errors.Is(err, configstore.ErrNotFound) || (err == nil && req.ClientID != ep.profile.ClientID()) {
		SendError(ctx, fasthttp.StatusNotFound, "this code is not valid or has expired; "+ep.profile.SignInHint())
		return
	}
	if err != nil {
		logger.Error("%s gateway: failed to look up user code: %v", ep.profile.Name(), err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to look up code")
		return
	}

	consentURL := fmt.Sprintf("%s/oauth/consent?flow=%s", oauth2IssuerURL(ctx, ep.store), url.QueryEscape(req.ID))
	if ep.tempTokens != nil {
		tok, err := ep.tempTokens.Mint(ctx, temptoken.OAuth2ConsentScopeName, req.ID, time.Until(req.ExpiresAt))
		if err != nil {
			logger.Error("%s gateway: failed to mint consent temp token: %v", ep.profile.Name(), err)
			SendError(ctx, fasthttp.StatusInternalServerError, "failed to prepare consent flow")
			return
		}
		consentURL += "#t=" + url.QueryEscape(tok)
	}
	SendJSON(ctx, clientGatewayDeviceVerifyResponse{ConsentURL: consentURL})
}

// --- Bearer authentication ---

// BearerMiddleware authenticates the gateway bearer on every bearer route.
// It verifies the JWT against this gateway's audience, re-checks the identity it
// names (the same cut-offs the /mcp path applies), and then rewrites the request
// to carry that identity the way a direct caller would: a vk-mode token becomes
// an x-bf-vk header, a user-mode token the authenticated user ID, and a
// session-mode token nothing (anonymous, refused while auth is enforced). The
// bearer itself is removed so it never reaches a provider.
//
// Failures are written in the profile's error format; a 401 tells the client to
// prompt the user to sign in again instead of retrying.
func (ep *clientGatewayEndpoint) BearerMiddleware(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		if !ep.enabled() {
			ep.profile.WriteAuthError(ctx, fasthttp.StatusNotFound, false, ep.notEnabledMessage())
			return
		}
		if status, msg := ep.authenticate(ctx); status != 0 {
			ep.profile.WriteAuthError(ctx, status, status == fasthttp.StatusUnauthorized, msg)
			return
		}
		next(ctx)
	}
}

// authenticate verifies the bearer and stamps the identity it carries. It
// returns a zero status on success, otherwise the HTTP status and message to
// send.
func (ep *clientGatewayEndpoint) authenticate(ctx *fasthttp.RequestCtx) (int, string) {
	hint := ep.profile.SignInHint()
	rawJWT := extractBearerJWT(ctx)
	if rawJWT == "" {
		return fasthttp.StatusUnauthorized, "not signed in to this gateway; " + hint
	}
	// A token already verified for this audience is not verified again until it
	// expires: RS256 verification is most of this middleware's cost, and every
	// request a session makes carries the same token. The identity it names is
	// still re-checked below on every request.
	resource := ep.resourceURL(ctx)
	now := time.Now()
	claims := ep.tokens.get(rawJWT, resource, now)
	if claims == nil {
		signingKey, err := ep.store.GetOAuth2SigningKey(ctx)
		if err != nil {
			logger.Error("%s gateway: failed to load oauth2 signing key: %v", ep.profile.Name(), err)
			return fasthttp.StatusServiceUnavailable, "signing key unavailable"
		}
		claims, err = verifyBifrostJWT(ctx, rawJWT, ep.store, signingKey, resource)
		if err != nil {
			if errors.Is(err, errOAuth2TokenRejected) {
				return fasthttp.StatusUnauthorized, "the gateway session is invalid or expired; " + hint
			}
			logger.Error("%s gateway: token verification failed: %v", ep.profile.Name(), err)
			return fasthttp.StatusServiceUnavailable, "token verification unavailable"
		}
		ep.tokens.put(rawJWT, resource, claims, now)
	}
	if claims.Subject == "" {
		return fasthttp.StatusUnauthorized, "the gateway session is invalid; " + hint
	}

	switch schemas.MCPAuthMode(claims.BfMode) {
	case schemas.MCPAuthModeVK:
		if oauth2ServerCfg(ep.store).DisableVKIdentity && ep.identityResolver != nil && ep.identityResolver.IsUserModeAvailable() {
			return fasthttp.StatusUnauthorized, "virtual-key sign-in is no longer accepted; " + hint
		}
		vk, err := ep.virtualKeyByID(ctx, claims.Subject)
		if err != nil {
			return fasthttp.StatusServiceUnavailable, "failed to verify virtual key"
		}
		if vk == nil || !vk.IsActiveValue() {
			return fasthttp.StatusUnauthorized, "the virtual key behind this session is no longer active; " + hint
		}
		stripGatewayCredentials(ctx)
		ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), vk.Value.GetValue())
	case schemas.MCPAuthModeUser:
		if ep.identityResolver != nil {
			active, err := ep.identityResolver.IsUserActive(ctx, claims.Subject)
			if err != nil {
				return fasthttp.StatusServiceUnavailable, "failed to verify user"
			}
			if !active {
				return fasthttp.StatusUnauthorized, "this user is no longer active; " + hint
			}
		}
		stripGatewayCredentials(ctx)
		// Attribute the request to the user and record the token it authenticated
		// by, as injectJWTContext does for the same token on /mcp; SettleIdentity
		// reads both when the request context is built.
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, claims.Subject)
		ctx.SetUserValue(schemas.BifrostContextKeyAuthCredential, grant.NewCredential(grant.CredentialMCPToken, claims.Subject))
	case schemas.MCPAuthModeSession:
		ep.store.Mu.RLock()
		enforceAuth := ep.store.ClientConfig.EnforceAuthOnInference
		ep.store.Mu.RUnlock()
		if enforceAuth {
			return fasthttp.StatusUnauthorized, "anonymous sessions are not accepted while authentication is enforced; " + hint + " using a virtual key or your account"
		}
		stripGatewayCredentials(ctx)
	default:
		return fasthttp.StatusUnauthorized, "the gateway session is invalid; " + hint
	}
	return 0, ""
}

// virtualKeyByID resolves the key a vk-mode token names, preferring the
// governance in-memory store. It returns (nil, nil) when the key is gone and an
// error only for a transient lookup failure, so a database blip is a retriable
// 503 rather than a sign-out.
func (h *ClientGatewayHandler) virtualKeyByID(ctx context.Context, id string) (*configtables.TableVirtualKey, error) {
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

// --- User codes ---

// generateGatewayUserCode returns an 8-character base-20 user code shown as
// XXXX-XXXX (RFC 8628 §6.1).
func generateGatewayUserCode() (string, error) {
	var b strings.Builder
	alphabetSize := big.NewInt(int64(len(clientGatewayUserCodeAlphabet)))
	for i := 0; i < clientGatewayUserCodeLength; i++ {
		if i == clientGatewayUserCodeLength/2 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, alphabetSize)
		if err != nil {
			return "", err
		}
		b.WriteByte(clientGatewayUserCodeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// normalizeGatewayUserCode uppercases a typed user code and drops the separator
// and any whitespace, so "wdjb-mjht" and "WDJB MJHT" both match.
func normalizeGatewayUserCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// --- Verified token cache ---

// clientGatewayTokenCacheMaxEntries caps the verified-token cache. An entry is the
// token string (under 1 KB) plus its resource and parsed claims, so the cap
// bounds the cache at a few MB. One signed-in client session holds one
// access token at a time, so the cap is the number of concurrently active
// sessions per process that skip re-verification; beyond it tokens are simply
// verified on every request, as without the cache.
const clientGatewayTokenCacheMaxEntries = 4096

// gatewayTokenCache remembers gateway access tokens whose signature, issuer,
// lifetime and audience were verified, keyed by the raw token, until each one
// expires. An entry only answers for the resource it was verified against, so
// an issuer_url change (which moves the resource) sends every token back
// through full verification, which then refuses it. Access tokens have no
// revocation of their own (refresh tokens do); the identity a token names is
// re-checked by the caller on every request, cached or not.
type gatewayTokenCache struct {
	mu        sync.RWMutex
	max       int
	lastSweep time.Time
	entries   map[string]gatewayTokenEntry
}

type gatewayTokenEntry struct {
	resource string
	expires  time.Time
	claims   *jwtMCPClaims
}

// newGatewayTokenCache creates a cache holding at most max tokens.
func newGatewayTokenCache(max int) *gatewayTokenCache {
	return &gatewayTokenCache{max: max, entries: make(map[string]gatewayTokenEntry)}
}

// get returns the verified claims for rawToken if it was verified for resource
// and has not expired, otherwise nil.
func (c *gatewayTokenCache) get(rawToken, resource string, now time.Time) *jwtMCPClaims {
	c.mu.RLock()
	e, ok := c.entries[rawToken]
	c.mu.RUnlock()
	if !ok || e.resource != resource || !now.Before(e.expires) {
		return nil
	}
	return e.claims
}

// put records claims verified for rawToken against resource. A token without an
// expiry is never cached. When the cache is full, expired entries are swept (at
// most once a minute); if it is still full the token is not cached.
func (c *gatewayTokenCache) put(rawToken, resource string, claims *jwtMCPClaims, now time.Time) {
	if claims == nil || claims.ExpiresAt == nil {
		return
	}
	expires := claims.ExpiresAt.Time
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[rawToken]; !ok && len(c.entries) >= c.max {
		if now.Sub(c.lastSweep) >= time.Minute {
			c.lastSweep = now
			for k, e := range c.entries {
				if !now.Before(e.expires) {
					delete(c.entries, k)
				}
			}
		}
		if len(c.entries) >= c.max {
			return
		}
	}
	c.entries[rawToken] = gatewayTokenEntry{resource: resource, expires: expires, claims: claims}
}

// --- Rate limiting and poll pacing ---

// clientGatewayRateLimiterMaxKeys caps how many addresses one limiter tracks. Each
// entry is a key string plus a small window struct (well under 200 bytes), so
// the cap bounds a limiter at roughly 2 MB however many addresses call.
const clientGatewayRateLimiterMaxKeys = 10000

// ipRateLimiter is a fixed-window per-key counter for the unauthenticated
// sign-in endpoints. It is per process: behind several replicas the effective
// limit scales with the replica count, which is fine for abuse damping. Callers
// key it by ctx.RemoteIP(), never by X-Forwarded-For: Bifrost has no
// trusted-proxy setting, and a caller-controlled header would let anyone pick a
// fresh bucket per request.
//
// Memory is bounded: expired windows are swept at most once per window (so a
// request never pays for a full scan more often than that), and once maxKeys
// live windows are tracked a new address is refused until a sweep frees room.
// Refusing beats evicting here: eviction would hand a fresh budget to whichever
// address was dropped, which is exactly what an address-rotating caller wants.
type ipRateLimiter struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	maxKeys   int
	lastSweep time.Time
	windows   map[string]*rateWindow
}

type rateWindow struct {
	start time.Time
	count int
}

// newIPRateLimiter allows limit hits per key within each window.
func newIPRateLimiter(limit int, window time.Duration) *ipRateLimiter {
	return &ipRateLimiter{limit: limit, window: window, maxKeys: clientGatewayRateLimiterMaxKeys, windows: make(map[string]*rateWindow)}
}

// allow records a hit for key and reports whether it is within the limit.
func (l *ipRateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.windows[key]
	if ok && now.Sub(w.start) < l.window {
		w.count++
		return w.count <= l.limit
	}
	if !ok && len(l.windows) >= l.maxKeys {
		l.sweepLocked(now)
		if len(l.windows) >= l.maxKeys {
			return false
		}
	}
	l.windows[key] = &rateWindow{start: now, count: 1}
	return true
}

// sweepLocked drops expired windows, at most once per window.
func (l *ipRateLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	l.lastSweep = now
	for k, w := range l.windows {
		if now.Sub(w.start) >= l.window {
			delete(l.windows, k)
		}
	}
}

// devicePollTracker remembers when each pending device code last polled, so a
// client polling faster than the advertised interval gets slow_down
// (RFC 8628 §3.5). It is only consulted for a device code that matched a
// pending row, so its size is bounded by the pending sign-ins the
// device-authorization limiter lets through; stale entries (finished elsewhere,
// or abandoned) are swept at most once per device-code lifetime.
type devicePollTracker struct {
	mu        sync.Mutex
	lastSweep time.Time
	last      map[string]time.Time
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
	if now.Sub(t.lastSweep) >= clientGatewayDeviceCodeTTL {
		t.lastSweep = now
		for k, at := range t.last {
			if now.Sub(at) > clientGatewayDeviceCodeTTL {
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
