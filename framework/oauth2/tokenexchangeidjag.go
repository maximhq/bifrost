package oauth2

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/maximhq/bifrost/core/providers/utils/tokencache"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/lrucache"
)

// Wire values for the Cross App Access (ID-JAG) profile of token exchange.
const (
	requestedTokenTypeIDJAG = "urn:ietf:params:oauth:token-type:id-jag"
	// clientAssertionTypeJWTBearer marks a private_key_jwt client assertion (RFC 7523 §2.2).
	clientAssertionTypeJWTBearer = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	// resourceEndpointTTL bounds how long a discovered upstream token endpoint
	// is reused before discovery runs again.
	resourceEndpointTTL = time.Hour
)

// resourceEndpoint is a cached leg-2 token endpoint for one MCP client URL.
type resourceEndpoint struct {
	upstreamURL string
	endpoint    string
	authMethods []string // token_endpoint_auth_methods_supported; nil when configured or unpublished
	fetchedAt   time.Time
}

// idjagGrantKey identifies the ID-JAG held for one user and MCP client.
type idjagGrantKey struct {
	userID   string
	clientID string
}

// idjagGrant is an ID-JAG kept for reuse. The grant replaces a refresh token:
// the client may re-present it for a new upstream token until it expires, and
// Okta counts every grant it issues against a per-user monthly limit, so a
// new one is requested only when the held grant has expired or was refused.
type idjagGrant struct {
	assertion string
	expiresAt time.Time
}

// errNoAdminRenewSubject means an admin credential has neither a live SSO
// session nor a retained refresh token to renew with.
var errNoAdminRenewSubject = errors.New("admin ID-JAG credential has no identity-provider session or refresh token to renew with")

// idjagGrantCache holds the ID-JAGs kept for reuse in a bounded LRU, so idle
// (user, MCP client) pairs cannot grow it without limit. An expired grant is
// a miss and is dropped on lookup.
type idjagGrantCache struct {
	cache *lrucache.Cache[*idjagGrant]
}

// newIDJAGGrantCache returns a grant cache bounded to capacity entries.
func newIDJAGGrantCache(capacity int) *idjagGrantCache {
	return &idjagGrantCache{cache: lrucache.New(capacity, lrucache.WithValidator(func(g *idjagGrant) bool {
		return g != nil && time.Now().Before(g.expiresAt)
	}))}
}

// encode is the cache key for k; EncodeKey keeps a user ID from forging the
// boundary with the client ID.
func (k idjagGrantKey) encode() string {
	return lrucache.EncodeKey(k.userID, k.clientID)
}

// load returns the held grant for key while it is still usable.
func (c *idjagGrantCache) load(key idjagGrantKey) (*idjagGrant, bool) {
	if c == nil {
		return nil, false
	}
	return c.cache.Get(key.encode())
}

// store holds grant for key, replacing any previous one.
func (c *idjagGrantCache) store(key idjagGrantKey, grant *idjagGrant) {
	if c == nil {
		return
	}
	_, _ = c.cache.Fill(context.Background(), key.encode(), func() (*idjagGrant, string, error) { return grant, "", nil })
}

// delete drops the grant held for key.
func (c *idjagGrantCache) delete(key idjagGrantKey) {
	if c == nil {
		return
	}
	c.cache.Evict(key.encode())
}

// evictWhere drops every held grant whose key matches.
func (c *idjagGrantCache) evictWhere(match func(idjagGrantKey) bool) {
	if c == nil {
		return
	}
	c.cache.EvictWhere(func(raw string) bool {
		parts, ok := lrucache.DecodeKey(raw, 2)
		return ok && match(idjagGrantKey{userID: parts[0], clientID: parts[1]})
	})
}

// cachedIDJAG returns the held grant for key while it is still usable.
func (p *OAuth2Provider) cachedIDJAG(key idjagGrantKey) *idjagGrant {
	if key.userID == "" {
		return nil
	}
	grant, _ := p.idjagGrants.load(key)
	return grant
}

// evictIDJAGGrants drops every held grant the predicate matches.
func (p *OAuth2Provider) evictIDJAGGrants(match func(idjagGrantKey) bool) {
	p.idjagGrants.evictWhere(match)
}

// idjagExpiry is when a grant stops being re-presented: the earlier of the
// identity provider's expires_in and the assertion's own exp, less the safety
// margin. Zero when neither is known, in which case the grant is not held.
func idjagExpiry(assertion string, expiresIn int, now time.Time) time.Time {
	var exp time.Time
	if expiresIn > 0 {
		exp = now.Add(time.Duration(expiresIn) * time.Second)
	}
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(assertion, claims); err == nil {
		if t, err := claims.GetExpirationTime(); err == nil && t != nil && (exp.IsZero() || t.Time.Before(exp)) {
			exp = t.Time
		}
	}
	if exp.IsZero() {
		return exp
	}
	return exp.Add(-exchangeExpirySafetyMargin)
}

// getIDJAGAccessToken is GetExchangedAccessToken for the id_jag profile. The
// user's ID or refresh token (never the inbound access token) is exchanged for
// an ID-JAG, which the upstream authorization server redeems for its own token.
// Only user identities qualify: the subject must name a person. The cache is
// keyed by user and client, so a hit skips loading the stored subject.
func (p *OAuth2Provider) getIDJAGAccessToken(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (string, error) {
	if ctx.MCPAuthMode() != schemas.MCPAuthModeUser {
		return "", schemas.ErrExchangeSubjectTokenMissing
	}
	identity := ctx.MCPIdentity(schemas.MCPAuthModeUser)
	if identity == "" {
		return "", schemas.ErrExchangeSubjectTokenMissing
	}
	key := userTokenCacheKey(schemas.MCPAuthModeUser, identity, config.ID, schemas.TokenExchangeProfileIDJAG)
	if cached, ok := p.userTokens.Get(key); ok {
		return cached.accessToken, nil
	}
	filled, err := p.userTokens.Fill(ctx, key, func() (cachedUserToken, error) {
		response, _, err := p.exchangeIDJAG(ctx, config, idjagGrantKey{userID: identity, clientID: config.ID}, func() (*schemas.IDJAGSubjectToken, error) {
			return p.idjagSubject(ctx, identity)
		})
		if err != nil {
			return cachedUserToken{}, err
		}
		return cachedTokenFromResponse(response), nil
	})
	if err != nil {
		return "", err
	}
	return filled.accessToken, nil
}

// idjagSubject loads the user's ID-JAG subject from their stored SSO session
// through the identity-provider resolver.
func (p *OAuth2Provider) idjagSubject(ctx context.Context, userID string) (*schemas.IDJAGSubjectToken, error) {
	if userID == "" {
		return nil, schemas.ErrExchangeSubjectTokenMissing
	}
	resolver, ok := p.tokenExchangeIdPResolver().(schemas.IDJAGSubjectResolver)
	if !ok {
		return nil, schemas.ErrExchangeSubjectTokenMissing
	}
	subject, err := resolver.IDJAGSubjectForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if subject == nil || subject.Token == "" {
		return nil, schemas.ErrExchangeSubjectTokenMissing
	}
	return subject, nil
}

// exchangeIDJAG returns an upstream token for the user named by key. A held
// grant is re-presented first (leg 2 only); otherwise subjectFn supplies the
// SSO subject and both legs run. The subject used is returned, nil when the
// grant was reused, so the admin path can retain its refresh token.
func (p *OAuth2Provider) exchangeIDJAG(ctx context.Context, config *schemas.MCPClientConfig, key idjagGrantKey, subjectFn func() (*schemas.IDJAGSubjectToken, error)) (*schemas.OAuth2TokenExchangeResponse, *schemas.IDJAGSubjectToken, error) {
	ctx = withRequirePublicTarget(ctx, config.RequirePublicTarget)
	// Resolved before any grant is requested, so an unusable upstream does not
	// spend one of the identity provider's limited ID-JAGs. Cached per client.
	if _, err := p.resourceTokenEndpoint(ctx, config); err != nil {
		return nil, nil, err
	}
	// Likewise a key that cannot sign: leg 2 would fail after the ID-JAG was spent.
	if te := config.TokenExchange; te.UsesResourcePrivateKey() {
		if err := tokencache.ValidatePrivateKey(te.ResourcePrivateKey.GetValue(), te.ResourceSigningAlgorithm); err != nil {
			return nil, nil, fmt.Errorf("authenticate to the upstream authorization server for MCP client %s: client assertion key: %w", config.Name, err)
		}
	}
	if grant := p.cachedIDJAG(key); grant != nil {
		response, err := p.redeemIDJAG(ctx, config, grant.assertion)
		var rejected *schemas.TokenExchangeRejectedError
		if err == nil || !errors.As(err, &rejected) {
			return response, nil, err
		}
		// The upstream refused the held grant; ask the identity provider again.
		p.idjagGrants.delete(key)
	}
	subject, err := subjectFn()
	if err != nil {
		return nil, nil, err
	}
	grant, err := p.issueIDJAG(ctx, config, subject)
	if err != nil {
		return nil, nil, err
	}
	if key.userID != "" && !grant.expiresAt.IsZero() {
		p.idjagGrants.store(key, grant)
	}
	response, err := p.redeemIDJAG(ctx, config, grant.assertion)
	if err != nil {
		var rejected *schemas.TokenExchangeRejectedError
		if errors.As(err, &rejected) {
			p.idjagGrants.delete(key)
		}
		return nil, nil, err
	}
	return response, subject, nil
}

// issueIDJAG is leg 1: the identity provider issues an ID-JAG for the
// configured audience. The assertion is never logged.
func (p *OAuth2Provider) issueIDJAG(ctx context.Context, config *schemas.MCPClientConfig, subject *schemas.IDJAGSubjectToken) (*idjagGrant, error) {
	assertion, err := p.rawExchange(ctx, config, func(idp *schemas.TokenExchangeIdP) (url.Values, error) {
		return buildIDJAGRequestForm(idp, config, subject)
	})
	if err != nil {
		return nil, err
	}
	if assertion.IssuedTokenType != "" && assertion.IssuedTokenType != requestedTokenTypeIDJAG {
		return nil, fmt.Errorf("identity provider issued %q instead of an ID-JAG for MCP client %s", assertion.IssuedTokenType, config.Name)
	}
	if strings.TrimSpace(assertion.AccessToken) == "" {
		return nil, fmt.Errorf("identity provider returned an empty ID-JAG for MCP client %s", config.Name)
	}
	return &idjagGrant{assertion: assertion.AccessToken, expiresAt: idjagExpiry(assertion.AccessToken, assertion.ExpiresIn, time.Now())}, nil
}

// redeemIDJAG is leg 2: the upstream authorization server exchanges the
// ID-JAG with the jwt-bearer grant for its own token.
func (p *OAuth2Provider) redeemIDJAG(ctx context.Context, config *schemas.MCPClientConfig, assertion string) (*schemas.OAuth2TokenExchangeResponse, error) {
	cfg := config.TokenExchange
	upstream, err := p.resourceTokenEndpoint(ctx, config)
	if err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(cfg.ResourceClientID.GetValue())
	form := url.Values{}
	form.Set("grant_type", grantTypeJWTBearer)
	form.Set("assertion", assertion)
	opts, err := resourceClientAuth(form, cfg, clientID, upstream)
	if err != nil {
		return nil, fmt.Errorf("authenticate to the upstream authorization server for MCP client %s: %w", config.Name, err)
	}
	response, err := p.callTokenEndpoint(ctx, upstream.endpoint, form, opts...)
	if err != nil {
		var permanent *PermanentOAuthError
		if errors.As(err, &permanent) {
			return nil, &schemas.TokenExchangeRejectedError{Detail: "upstream authorization server: " + sanitizeTokenExchangeRejection(permanent.Body)}
		}
		return nil, fmt.Errorf("ID-JAG redemption for MCP client %s failed: %w", config.Name, err)
	}
	return response, nil
}

// resourceClientAuth adds Bifrost's client authentication at the upstream
// authorization server to form, returning any request options it needs: a
// client assertion signed with the configured key (RFC 7523 §2.2; audience the
// configured one, else the token endpoint); else the client secret, sent as
// resource_client_auth_method says or, when unset, with HTTP Basic unless the
// upstream admits only the body; else client_id alone for a public client.
func resourceClientAuth(form url.Values, cfg *schemas.MCPTokenExchangeConfig, clientID string, upstream resourceEndpoint) ([]tokenRequestOption, error) {
	if cfg.UsesResourcePrivateKey() {
		clientAssertion, err := tokencache.SignAssertion(tokencache.JWTBearerConfig{
			PrivateKeyPEM: cfg.ResourcePrivateKey.GetValue(),
			Issuer:        clientID,
			Subject:       clientID,
			Audience:      cmp.Or(strings.TrimSpace(cfg.ResourceAssertionAudience), upstream.endpoint),
			KeyID:         cfg.ResourceKeyID,
			Algorithm:     cfg.ResourceSigningAlgorithm,
		})
		if err != nil {
			return nil, fmt.Errorf("sign the client assertion: %w", err)
		}
		form.Set("client_id", clientID)
		form.Set("client_assertion_type", clientAssertionTypeJWTBearer)
		form.Set("client_assertion", clientAssertion)
		return nil, nil
	}
	switch secret := strings.TrimSpace(cfg.ResourceClientSecret.GetValue()); {
	case secret == "":
		form.Set("client_id", clientID)
	case cfg.ResourceClientAuthMethod == schemas.ResourceClientAuthPost,
		cfg.ResourceClientAuthMethod == "" && onlyClientSecretPost(upstream.authMethods):
		form.Set("client_id", clientID)
		form.Set("client_secret", secret)
	default:
		return []tokenRequestOption{withBasicClientAuth(clientID, secret)}, nil
	}
	return nil, nil
}

// onlyClientSecretPost reports whether the upstream's metadata admits the
// client secret in the request body but not in HTTP Basic. Basic is the
// default otherwise: RFC 6749 §2.3.1 requires servers to support it and does
// not recommend the body.
func onlyClientSecretPost(methods []string) bool {
	return slices.Contains(methods, "client_secret_post") && !slices.Contains(methods, "client_secret_basic")
}

// buildIDJAGRequestForm builds the leg-1 RFC 8693 request asking the identity
// provider for an ID-JAG. Client credentials follow the same rules as the
// direct profile; identity providers accept the subject only from the
// application it was issued to, so use_idp_credentials is the usual setting.
func buildIDJAGRequestForm(idp *schemas.TokenExchangeIdP, config *schemas.MCPClientConfig, subject *schemas.IDJAGSubjectToken) (url.Values, error) {
	cfg := config.TokenExchange
	clientID, clientSecret, err := exchangeClientCredentials(idp, cfg)
	if err != nil {
		return nil, err
	}
	data := url.Values{}
	data.Set("grant_type", grantTypeTokenExchange)
	data.Set("requested_token_type", requestedTokenTypeIDJAG)
	data.Set("subject_token", subject.Token)
	data.Set("subject_token_type", subject.TokenType)
	data.Set("audience", cfg.Audience)
	// resource is sent only when configured: Okta answers invalid_target when
	// the agent's connection defines no resource indicator, which is the default.
	if resource := strings.TrimSpace(cfg.Resource); resource != "" {
		data.Set("resource", resource)
	}
	if scope := scopeParam(cfg, false); scope != "" {
		data.Set("scope", scope)
	}
	data.Set("client_id", clientID)
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}
	return data, nil
}

// resourceTokenEndpoint returns the upstream authorization server's token
// endpoint: the configured override, or one discovered from the MCP server's
// protected resource metadata. Discovery must show the server accepts the
// jwt-bearer grant, otherwise it cannot redeem an ID-JAG.
func (p *OAuth2Provider) resourceTokenEndpoint(ctx context.Context, config *schemas.MCPClientConfig) (resourceEndpoint, error) {
	if override := config.TokenExchange.ResourceTokenEndpointURL(); override != "" {
		return resourceEndpoint{endpoint: override}, nil
	}
	upstreamURL := ""
	if config.ConnectionString != nil {
		upstreamURL = strings.TrimSpace(config.ConnectionString.GetValue())
	}
	if upstreamURL == "" {
		return resourceEndpoint{}, fmt.Errorf("MCP client %s has no URL to discover its authorization server from; set token_exchange.resource_token_endpoint", config.Name)
	}
	if v, ok := p.resourceEndpoints.Load(config.ID); ok {
		cached := v.(resourceEndpoint)
		if cached.upstreamURL == upstreamURL && time.Since(cached.fetchedAt) < resourceEndpointTTL {
			return cached, nil
		}
	}
	metadata, err := DiscoverOAuthMetadata(ctx, upstreamURL)
	if err != nil {
		return resourceEndpoint{}, fmt.Errorf("discover authorization server for MCP client %s: %w", config.Name, err)
	}
	if metadata.TokenURL == "" {
		return resourceEndpoint{}, fmt.Errorf("authorization server for MCP client %s publishes no token endpoint", config.Name)
	}
	// The ID-JAG and Bifrost's client secret are posted here, so a discovered
	// endpoint must meet the same rule as a configured one.
	if !schemas.IsHTTPSOrLoopbackURL(metadata.TokenURL) {
		return resourceEndpoint{}, fmt.Errorf("authorization server for MCP client %s publishes a token endpoint that is not https", config.Name)
	}
	// RFC 8414: an omitted grant_types_supported means authorization_code and
	// implicit only, so the grant must be listed explicitly.
	if !slices.Contains(metadata.GrantTypes, grantTypeJWTBearer) {
		return resourceEndpoint{}, fmt.Errorf("authorization server for MCP client %s does not support the jwt-bearer grant needed to redeem an ID-JAG", config.Name)
	}
	discovered := resourceEndpoint{upstreamURL: upstreamURL, endpoint: metadata.TokenURL, authMethods: metadata.TokenAuthMethods, fetchedAt: time.Now()}
	p.resourceEndpoints.Store(config.ID, discovered)
	return discovered, nil
}

// exchangeIDJAGAdminCredential runs the id_jag exchange for the signed-in admin.
// A verification is a check of the identity provider's policy, so any held
// grant is dropped and both legs always run. When the subject is a refresh
// token it is returned as the response's refresh token, so the retained
// admin credential can be renewed (see refreshIDJAGAdminToken).
func (p *OAuth2Provider) exchangeIDJAGAdminCredential(ctx context.Context, config *schemas.MCPClientConfig) (*schemas.OAuth2TokenExchangeResponse, error) {
	userID, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
	subject, err := p.idjagSubject(ctx, userID)
	if err != nil {
		return nil, err
	}
	key := idjagGrantKey{userID: userID, clientID: config.ID}
	p.idjagGrants.delete(key)
	response, _, err := p.exchangeIDJAG(ctx, config, key, func() (*schemas.IDJAGSubjectToken, error) {
		return subject, nil
	})
	if err != nil {
		return nil, err
	}
	response.RefreshToken = ""
	if subject.TokenType == schemas.TokenTypeRefreshToken {
		response.RefreshToken = subject.Token
	}
	return response, nil
}

// refreshIDJAGAdminToken renews an id_jag admin credential. The admin's live
// SSO session is the first choice of subject: identity providers that rotate
// refresh tokens invalidate the copy retained at verification as soon as that
// session next refreshes. The retained copy is the fallback when the admin
// has no session. Without either, or when a side refuses, the row needs
// re-verification.
func (p *OAuth2Provider) refreshIDJAGAdminToken(ctx context.Context, token *tables.TableMCPOauthToken, clientConfig *schemas.MCPClientConfig) error {
	userID := ""
	if token.UserID != nil {
		userID = *token.UserID
	}
	subjectFn := func() (*schemas.IDJAGSubjectToken, error) {
		live, err := p.idjagSubject(ctx, userID)
		if err == nil {
			return live, nil
		}
		// Only a missing session falls back. A failed lookup is returned as is, so
		// a stale retained token is not sent and the renewal is retried later.
		if !errors.Is(err, schemas.ErrExchangeSubjectTokenMissing) {
			return nil, err
		}
		if strings.TrimSpace(token.RefreshToken) == "" {
			return nil, errNoAdminRenewSubject
		}
		return &schemas.IDJAGSubjectToken{Token: token.RefreshToken, TokenType: schemas.TokenTypeRefreshToken}, nil
	}
	response, subject, err := p.exchangeIDJAG(ctx, clientConfig, idjagGrantKey{userID: userID, clientID: clientConfig.ID}, subjectFn)
	if err != nil {
		var rejected *schemas.TokenExchangeRejectedError
		if errors.Is(err, errNoAdminRenewSubject) || errors.As(err, &rejected) {
			return p.markExchangeAdminNeedsReauth(token, err)
		}
		return fmt.Errorf("admin ID-JAG credential renewal failed: %w", err)
	}
	if subject != nil && subject.TokenType == schemas.TokenTypeRefreshToken {
		token.RefreshToken = subject.Token
	}
	now := time.Now()
	token.AccessToken = strings.TrimSpace(response.AccessToken)
	token.ExpiresAt = nil
	if response.ExpiresIn > 0 {
		exp := now.Add(time.Duration(response.ExpiresIn) * time.Second)
		token.ExpiresAt = &exp
	}
	token.LastRefreshedAt = &now
	token.StatusReason = ""
	if err := p.configStore.UpdateOauthToken(ctx, token); err != nil {
		return fmt.Errorf("failed to update admin ID-JAG credential: %w", err)
	}
	p.EvictUserTokenByID(token.ID)
	return nil
}

// cachedTokenFromResponse shapes a token response into a cache entry with the
// expiry safety margin applied.
func cachedTokenFromResponse(response *schemas.OAuth2TokenExchangeResponse) cachedUserToken {
	ttl := defaultExchangedTokenTTL
	if response.ExpiresIn > 0 {
		ttl = time.Duration(response.ExpiresIn) * time.Second
	}
	if margined := ttl - exchangeExpirySafetyMargin; margined > 0 {
		ttl = margined
	}
	expiresAt := time.Now().Add(ttl)
	return cachedUserToken{accessToken: response.AccessToken, expiresAt: &expiresAt}
}
