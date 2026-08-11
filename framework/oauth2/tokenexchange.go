package oauth2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

const (
	grantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	grantTypeJWTBearer     = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	// subjectTokenTypeAccessToken is the RFC 8693 subject_token_type declared
	// for delegated exchanges. The raw bearer stamped as the caller's
	// identity token (BifrostContextKeyMCPInboundBearer) must be a genuine
	// OAuth access token, not an ID token: identity-provider token-exchange
	// endpoints reject an ID token both as a type mismatch when declared
	// access_token (observed as Okta's "'subject_token' is invalid") and
	// outright when declared id_token (Okta's "'subject_token_type' is
	// invalid or not supported" — id_token isn't a supported
	// subject_token_type at all). Whatever stamps this context key is
	// responsible for ensuring it is a genuine access token, not merely an
	// identity-bearing JWT.
	subjectTokenTypeAccessToken = "urn:ietf:params:oauth:token-type:access_token"

	// exchangeExpirySafetyMargin is subtracted from an exchanged token's
	// expires_in before caching, so a token about to lapse mid-call is
	// treated as a miss and re-exchanged rather than sent upstream.
	exchangeExpirySafetyMargin = 30 * time.Second

	// defaultExchangedTokenTTL caches exchanged tokens whose response omitted
	// expires_in. Deliberately short: with no declared lifetime the only safe
	// assumption is a brief one, and re-exchanging is cheap.
	defaultExchangedTokenTTL = 5 * time.Minute
)

// SetTokenExchangeIdPResolver installs the identity-provider resolver that
// backs delegated token exchange. Called once at server startup after the
// identity integration is constructed; nil (never installed) leaves the
// token_exchange auth type unavailable.
func (p *OAuth2Provider) SetTokenExchangeIdPResolver(r schemas.TokenExchangeIdPResolver) {
	p.exchangeIdP.Store(&r)
}

// tokenExchangeIdPResolver returns the installed resolver, or nil. Lock-free
// for the same reason tempTokens is: p.mu is write-locked across token
// refresh network I/O and request-path reads must not wait on that.
func (p *OAuth2Provider) tokenExchangeIdPResolver() schemas.TokenExchangeIdPResolver {
	if ptr := p.exchangeIdP.Load(); ptr != nil {
		return *ptr
	}
	return nil
}

// TokenExchangeAvailable reports whether delegated token exchange can run.
func (p *OAuth2Provider) TokenExchangeAvailable() bool {
	r := p.tokenExchangeIdPResolver()
	return r != nil && r.Available()
}

// GetExchangedAccessToken returns an upstream access token for a
// token_exchange client, exchanging the caller's identity-provider token for
// one scoped to the client's audience. Cached per (auth mode, identity, mcp
// client) binding in the same in-memory cache as per-user OAuth lookups —
// exchanged tokens have no database row, so the cached entry (with the
// expiry-as-miss validator) is the only local state, and a miss simply
// performs a fresh exchange.
func (p *OAuth2Provider) GetExchangedAccessToken(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (string, error) {
	if err := validateExchangeConfig(config); err != nil {
		return "", err
	}
	mode := ctx.MCPAuthMode()
	// Session identities are caller-asserted opaque strings with no
	// verification, so they can never anchor a delegated exchange; the
	// subject token requirement below would fail anyway (nothing stamps a
	// caller token without verifying it), but reject explicitly for a
	// clearer error.
	if mode != schemas.MCPAuthModeUser && mode != schemas.MCPAuthModeVK {
		return "", schemas.ErrExchangeSubjectTokenMissing
	}
	identity := ctx.MCPIdentity(mode)
	if identity == "" {
		return "", schemas.ErrExchangeSubjectTokenMissing
	}
	subjectToken, _ := ctx.Value(schemas.BifrostContextKeyMCPInboundBearer).(string)
	if subjectToken == "" {
		return "", schemas.ErrExchangeSubjectTokenMissing
	}

	// subjectTokenFingerprint binds the cache entry to the actual bearer
	// credential validated on this request, not just the (mode, identity)
	// binding — see userTokenCacheKey's doc comment on subjectDiscriminator
	// for why MCPAuthModeVK's identity alone isn't enough here.
	key := userTokenCacheKey(mode, identity, config.ID, subjectTokenFingerprint(subjectToken))
	if cached, ok := p.userTokens.Get(key); ok {
		return cached.accessToken, nil
	}
	filled, err := p.userTokens.Fill(ctx, key, func() (cachedUserToken, error) {
		return p.performExchange(ctx, config, func(idp *schemas.TokenExchangeIdP) (url.Values, error) {
			return buildSubjectExchangeForm(idp, config.TokenExchange, subjectToken)
		})
	})
	if err != nil {
		return "", err
	}
	return filled.accessToken, nil
}

// ExchangeAdminCredential performs a single uncached exchange of the admin's
// own token (a sample caller token, never associated with any identity
// binding), producing the admin bootstrap credential used for verification +
// tool discovery. The full token response is returned so the caller can
// retain it via RetainExchangeAdminCredential once verification succeeds.
func (p *OAuth2Provider) ExchangeAdminCredential(ctx context.Context, config *schemas.MCPClientConfig, subjectToken string) (*schemas.OAuth2TokenExchangeResponse, error) {
	if err := validateExchangeConfig(config); err != nil {
		return nil, err
	}
	subjectToken = strings.TrimSpace(subjectToken)
	if subjectToken == "" {
		return nil, schemas.ErrExchangeSubjectTokenMissing
	}
	return p.rawExchange(ctx, config, func(idp *schemas.TokenExchangeIdP) (url.Values, error) {
		return buildSubjectExchangeForm(idp, config.TokenExchange, subjectToken)
	})
}

// RetainExchangeAdminCredential persists the outcome of a successful admin
// verification as the retained auth_mode='admin' token row for config — the
// discovery credential the tool syncer and OAuthTokenRefreshWorker keep alive,
// mirroring what PromoteSharedOauthTokenToAdmin does for per-user OAuth
// bootstrap. Upserts: a repair replaces the existing row's credential.
func (p *OAuth2Provider) RetainExchangeAdminCredential(ctx context.Context, config *schemas.MCPClientConfig, response *schemas.OAuth2TokenExchangeResponse) error {
	if p.configStore == nil {
		return fmt.Errorf("config store is not available")
	}
	token := &tables.TableMCPOauthToken{
		ID:          uuid.NewString(),
		AuthMode:    "admin",
		MCPClientID: config.ID,
		AccessToken: strings.TrimSpace(response.AccessToken),
		TokenType:   response.TokenType,
		Status:      "active",
	}
	if response.RefreshToken != "" {
		token.RefreshToken = strings.TrimSpace(response.RefreshToken)
	}
	if response.ExpiresIn > 0 {
		exp := time.Now().Add(time.Duration(response.ExpiresIn) * time.Second)
		token.ExpiresAt = &exp
	}
	if err := p.configStore.CreateOauthToken(ctx, token); err != nil {
		return fmt.Errorf("failed to retain admin exchange credential: %w", err)
	}
	return nil
}

// isExchangeBackedTokenRow reports whether a token row is a token_exchange
// admin credential: bound directly to an MCP client with no oauth_configs
// template behind it. Such rows refresh through the identity-provider
// integration (refreshExchangeAdminToken) rather than a template config.
func isExchangeBackedTokenRow(token *tables.TableMCPOauthToken) bool {
	return token != nil && token.AuthMode == "admin" && token.MCPClientID != "" && token.OauthConfigID == ""
}

// refreshExchangeAdminToken is RefreshAccessToken's branch for
// token_exchange admin rows. Renewal runs on the refresh token the identity
// provider issued with the admin's exchange (typically requires
// "offline_access" in the configured scopes); without one the credential is
// unrenewable — the row flips to needs_reauth and an admin must re-verify
// with a fresh sample token (the same repair contract per-user OAuth's dead
// refresh tokens have).
func (p *OAuth2Provider) refreshExchangeAdminToken(ctx context.Context, token *tables.TableMCPOauthToken) error {
	clientConfig, err := p.configStore.GetMCPClientConfigByID(ctx, token.MCPClientID)
	if err != nil {
		return fmt.Errorf("failed to load MCP client for admin exchange refresh: %w", err)
	}
	if clientConfig == nil || clientConfig.AuthType != schemas.MCPAuthTypeTokenExchange {
		return fmt.Errorf("admin token row %s is not backed by a token_exchange client", token.ID)
	}
	if err := validateExchangeConfig(clientConfig); err != nil {
		return err
	}

	if strings.TrimSpace(token.RefreshToken) == "" {
		return p.markExchangeAdminNeedsReauth(token, fmt.Errorf("admin exchange credential has no refresh token to renew with"))
	}
	form := func(idp *schemas.TokenExchangeIdP) (url.Values, error) {
		clientID, clientSecret, err := exchangeClientCredentials(idp, clientConfig.TokenExchange)
		if err != nil {
			return nil, err
		}
		data := url.Values{}
		data.Set("grant_type", "refresh_token")
		data.Set("refresh_token", token.RefreshToken)
		data.Set("client_id", clientID)
		if clientSecret != "" {
			data.Set("client_secret", clientSecret)
		}
		return data, nil
	}

	response, err := p.rawExchange(ctx, clientConfig, form)
	if err != nil {
		var rejected *schemas.TokenExchangeRejectedError
		if errors.As(err, &rejected) {
			return p.markExchangeAdminNeedsReauth(token, err)
		}
		// Transient failure — keep the row so the next attempt retries.
		return fmt.Errorf("admin exchange credential renewal failed: %w", err)
	}

	now := time.Now()
	token.AccessToken = strings.TrimSpace(response.AccessToken)
	if response.RefreshToken != "" {
		token.RefreshToken = strings.TrimSpace(response.RefreshToken)
	}
	token.ExpiresAt = nil
	if response.ExpiresIn > 0 {
		exp := now.Add(time.Duration(response.ExpiresIn) * time.Second)
		token.ExpiresAt = &exp
	}
	token.LastRefreshedAt = &now
	if err := p.configStore.UpdateOauthToken(ctx, token); err != nil {
		return fmt.Errorf("failed to update admin exchange credential: %w", err)
	}
	p.EvictUserTokenByID(token.ID)
	logger.Debug("admin exchange credential renewed: token_id=%s mcp_client=%s", token.ID, token.MCPClientID)
	return nil
}

// markExchangeAdminNeedsReauth flips the admin row to needs_reauth (on a
// background context so a caller cancellation cannot leave a confirmed-dead
// credential looking active, mirroring RefreshAccessToken's permanent-error
// path) and signals re-verification via the ErrOAuth2TokenExpired sentinel.
func (p *OAuth2Provider) markExchangeAdminNeedsReauth(token *tables.TableMCPOauthToken, cause error) error {
	if markErr := p.configStore.MarkOauthUserTokenNeedsReauthByID(context.Background(), token.ID); markErr != nil {
		return fmt.Errorf("admin exchange credential is dead but status update failed (mcp_client=%s): %w", token.MCPClientID, markErr)
	}
	p.EvictUserTokenByID(token.ID)
	logger.Debug("admin exchange credential marked needs_reauth: mcp_client=%s cause=%v", token.MCPClientID, cause)
	return fmt.Errorf("admin exchange credential requires re-verification: %v: %w", cause, schemas.ErrOAuth2TokenExpired)
}

// EvictExchangedToken removes every cached exchanged token for one binding,
// regardless of which subject bearer token each entry was cached under (see
// userTokenCacheKey's doc comment on subjectDiscriminator) — the caller here
// (ForceRefreshAccessToken) knows the binding but not a specific token's
// fingerprint.
func (p *OAuth2Provider) EvictExchangedToken(mode schemas.MCPAuthMode, identity, mcpClientID string) {
	p.userTokens.EvictByBinding(mode, identity, mcpClientID)
}

// subjectTokenFingerprint derives a stable, non-reversible cache-key
// component from a caller's bearer token: a truncated SHA-256 hex digest.
// Never store or log the raw token; the fingerprint is only used to keep
// distinct subject tokens sharing a (mode, identity, mcpClientID) binding
// from colliding onto the same cache entry (see userTokenCacheKey's doc
// comment). Truncated to 16 hex chars (64 bits) — this only needs to avoid
// accidental collisions within one binding's small entry set, not resist a
// deliberate second-preimage search.
func subjectTokenFingerprint(subjectToken string) string {
	sum := sha256.Sum256([]byte(subjectToken))
	return hex.EncodeToString(sum[:8])
}

// performExchange adapts performExchangeCtx to the BifrostContext request path.
func (p *OAuth2Provider) performExchange(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig, form func(*schemas.TokenExchangeIdP) (url.Values, error)) (cachedUserToken, error) {
	return p.performExchangeCtx(ctx, config, form)
}

// rawExchange resolves the identity provider and posts the grant built by
// form. Provider rejections (PermanentOAuthError) surface as
// *schemas.TokenExchangeRejectedError so callers can distinguish "fix the
// credential" from transient failures.
func (p *OAuth2Provider) rawExchange(ctx context.Context, config *schemas.MCPClientConfig, form func(*schemas.TokenExchangeIdP) (url.Values, error)) (*schemas.OAuth2TokenExchangeResponse, error) {
	resolver := p.tokenExchangeIdPResolver()
	if resolver == nil || !resolver.Available() {
		return nil, schemas.ErrTokenExchangeUnavailable
	}
	idp, err := resolver.Resolve(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve identity provider for token exchange: %w", err)
	}
	if idp == nil || idp.TokenEndpoint == "" {
		return nil, schemas.ErrTokenExchangeUnavailable
	}

	formValues, err := form(idp)
	if err != nil {
		return nil, err
	}
	response, err := p.callTokenEndpoint(ctx, idp.TokenEndpoint, formValues)
	if err != nil {
		var permanent *PermanentOAuthError
		if errors.As(err, &permanent) {
			return nil, &schemas.TokenExchangeRejectedError{Detail: sanitizeTokenExchangeRejection(permanent.Body)}
		}
		return nil, fmt.Errorf("token exchange for MCP client %s failed: %w", config.Name, err)
	}
	return response, nil
}

// sanitizeTokenExchangeRejection extracts only the standard OAuth
// error/error_description fields (RFC 6749 §5.2) from a raw identity
// provider response body. The raw body is never returned as-is: it reaches
// TokenExchangeRejectedError.Detail, which is surfaced both in an
// externally-serialized API error (MCPAuthRequiredError.ExchangeError) and
// in a debug log, so anything beyond the standard error fields — stack
// traces, internal error pages, unrelated response content some identity
// providers include — must not pass through verbatim.
func sanitizeTokenExchangeRejection(rawBody string) string {
	var oauthErr struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal([]byte(rawBody), &oauthErr); err != nil || oauthErr.Error == "" {
		return "identity provider rejected the request"
	}
	if oauthErr.ErrorDescription == "" {
		return oauthErr.Error
	}
	return oauthErr.Error + ": " + oauthErr.ErrorDescription
}

// performExchangeCtx shapes a rawExchange result into a cache entry, with the
// expiry safety margin applied so a token about to lapse mid-call reads as a
// miss.
func (p *OAuth2Provider) performExchangeCtx(ctx context.Context, config *schemas.MCPClientConfig, form func(*schemas.TokenExchangeIdP) (url.Values, error)) (cachedUserToken, error) {
	response, err := p.rawExchange(ctx, config, form)
	if err != nil {
		return cachedUserToken{}, err
	}
	ttl := defaultExchangedTokenTTL
	if response.ExpiresIn > 0 {
		ttl = time.Duration(response.ExpiresIn) * time.Second
	}
	if margined := ttl - exchangeExpirySafetyMargin; margined > 0 {
		ttl = margined
	}
	expiresAt := time.Now().Add(ttl)
	return cachedUserToken{accessToken: response.AccessToken, expiresAt: &expiresAt}, nil
}

// validateExchangeConfig guards the invariants the create/update path
// enforces, so a stale or hand-built config fails cleanly here too.
func validateExchangeConfig(config *schemas.MCPClientConfig) error {
	if config.AuthType != schemas.MCPAuthTypeTokenExchange {
		return fmt.Errorf("MCP client %s does not use token exchange auth", config.Name)
	}
	if config.TokenExchange == nil || config.TokenExchange.Audience == "" {
		return fmt.Errorf("MCP client %s is missing its token_exchange audience", config.Name)
	}
	if config.TokenExchange.UseIdPCredentials {
		return nil
	}
	if strings.TrimSpace(config.TokenExchange.ClientID.GetValue()) == "" {
		return fmt.Errorf("MCP client %s is missing its token_exchange client_id", config.Name)
	}
	return nil
}

// exchangeClientCredentials resolves the exchange application's credentials.
// When cfg.UseIdPCredentials is true, this is the SSO login application's
// own client ID/secret (idp.IdPClientID/IdPClientSecret) — see that field's
// doc comment for why some providers require this. Otherwise it's the
// client's own token_exchange block (validateExchangeConfig guarantees a
// client ID is present in that case). Returns an error when
// UseIdPCredentials is true but the resolver couldn't supply a client ID —
// without this check the caller would silently send client_id= (empty) to
// the identity provider instead of a clear, actionable failure.
func exchangeClientCredentials(idp *schemas.TokenExchangeIdP, cfg *schemas.MCPTokenExchangeConfig) (clientID, clientSecret string, err error) {
	if cfg.UseIdPCredentials {
		if idp == nil || strings.TrimSpace(idp.IdPClientID) == "" {
			return "", "", fmt.Errorf("token_exchange.use_idp_credentials is set, but the identity-provider integration has no client ID configured")
		}
		return strings.TrimSpace(idp.IdPClientID), strings.TrimSpace(idp.IdPClientSecret), nil
	}
	clientID = strings.TrimSpace(cfg.ClientID.GetValue())
	if cfg.ClientSecret != nil {
		clientSecret = strings.TrimSpace(cfg.ClientSecret.GetValue())
	}
	return clientID, clientSecret, nil
}

// buildSubjectExchangeForm builds the delegated-exchange request body for the
// identity provider's grant shape.
func buildSubjectExchangeForm(idp *schemas.TokenExchangeIdP, cfg *schemas.MCPTokenExchangeConfig, subjectToken string) (url.Values, error) {
	clientID, clientSecret, err := exchangeClientCredentials(idp, cfg)
	if err != nil {
		return nil, err
	}
	data := url.Values{}
	data.Set("client_id", clientID)
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}
	switch idp.GrantShape {
	case schemas.TokenExchangeGrantJWTBearerOBO:
		data.Set("grant_type", grantTypeJWTBearer)
		data.Set("assertion", subjectToken)
		data.Set("requested_token_use", "on_behalf_of")
		// This grant shape addresses the resource through scope alone: the
		// conventional "<audience>/.default" form requests every permission
		// the exchange client holds on the resource when no explicit scopes
		// are configured.
		data.Set("scope", scopeParam(cfg, true))
	default:
		data.Set("grant_type", grantTypeTokenExchange)
		data.Set("subject_token", subjectToken)
		data.Set("subject_token_type", subjectTokenTypeAccessToken)
		data.Set("audience", cfg.Audience)
		if scope := scopeParam(cfg, false); scope != "" {
			data.Set("scope", scope)
		}
	}
	return data, nil
}

// scopeParam joins the configured scopes for the OAuth scope parameter.
// When defaultToAudience is set and no scopes are configured, it falls back
// to "<audience>/.default" (the resource-wide form used by grant shapes that
// have no separate audience parameter).
//
// One narrow case combines both instead of choosing one: per Microsoft's own
// OBO documentation, ".default" cannot be combined with other delegated
// scopes (AADSTS70011) *except* "offline_access" —
//
//	"you must not combine .default with other delegated scopes like
//	User.Read, Mail.Read, profile, or User.ReadWrite.All in the same
//	request. This will result in AADSTS70011 errors... offline_access is
//	sometimes accepted with .default to enable refresh tokens, but should
//	not be combined with any additional delegated scopes."
//	— https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-on-behalf-of-flow
//
// So when the configured scopes are exactly ["offline_access"] — the shape
// our own docs recommend adding for a self-renewing admin discovery
// credential — the audience-derived default is prepended rather than
// replaced. Without this, that recommendation would silently drop resource
// access entirely: a defaultToAudience grant shape has no separate audience
// parameter, so "scope=offline_access" alone requests nothing but a refresh
// token, for no resource. Any other configured scope is left as a full
// replacement, not combined: Microsoft's rule above forbids combining
// .default with genuinely custom scopes, so the caller has opted into
// naming exactly what they want and .default must not be added alongside it.
func scopeParam(cfg *schemas.MCPTokenExchangeConfig, defaultToAudience bool) string {
	audienceDefault := strings.TrimSuffix(cfg.Audience, "/") + "/.default"
	if defaultToAudience && len(cfg.Scopes) == 1 && strings.TrimSpace(cfg.Scopes[0]) == "offline_access" {
		return audienceDefault + " offline_access"
	}
	if len(cfg.Scopes) > 0 {
		return strings.Join(cfg.Scopes, " ")
	}
	if defaultToAudience {
		return audienceDefault
	}
	return ""
}
