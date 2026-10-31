package handlers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// OAuth2Extensions lets another module extend Bifrost's OAuth2 authorization
// server with grant types and discovery metadata of its own. Every method is
// consulted per request, so turning an extension on or off takes effect at once.
type OAuth2Extensions interface {
	// Active reports whether the extension is on. While it is, the token
	// endpoint and the authorization server metadata and keys are served for
	// its grants even with MCP OAuth off, which then serve only the extension.
	Active() bool
	// GrantHandler returns the handler for grantType at POST /oauth2/token, or
	// nil when the extension does not serve it.
	GrantHandler(grantType string) OAuth2GrantHandler
	// ExtendASMetadata adjusts the RFC 8414 authorization server metadata
	// document before it is served.
	ExtendASMetadata(ctx *fasthttp.RequestCtx, doc map[string]any)
}

// OAuth2GrantHandler serves one extension grant. It writes the token response
// or the OAuth error itself, using issuer for the server's identity and keys.
type OAuth2GrantHandler func(ctx *fasthttp.RequestCtx, issuer OAuth2TokenIssuer)

// AccessTokenSpec describes an access token for OAuth2TokenIssuer to sign.
type AccessTokenSpec struct {
	Mode     string   // bf_mode: user | vk | session
	Subject  string   // sub: the user or virtual key row ID
	Scope    string   // space-separated granted scopes
	Audience []string // aud: the resources the token is for
	TTL      int      // seconds; non-positive falls back to DefaultAccessTokenTTL
	// ExtraClaims are added to the token as is. They must not reuse a
	// registered or bf_ claim name the server sets itself.
	ExtraClaims map[string]any
}

// OAuth2TokenIssuer is the authorization server as an extension grant sees it:
// its identity, its token signer and its response writers.
type OAuth2TokenIssuer interface {
	// IssuerURL is the configured issuer, the iss of every token.
	IssuerURL(ctx *fasthttp.RequestCtx) string
	// MCPResourceURL is the /mcp resource identifier (RFC 8707).
	MCPResourceURL(ctx *fasthttp.RequestCtx) string
	// GatewayAudience identifies Bifrost's inference routes as a resource.
	GatewayAudience(ctx *fasthttp.RequestCtx) string
	// SignAccessToken signs an RS256 access token with the server's key.
	SignAccessToken(ctx *fasthttp.RequestCtx, spec AccessTokenSpec) (string, error)
	// WriteToken writes an RFC 6749 token response with no refresh token.
	WriteToken(ctx *fasthttp.RequestCtx, accessToken, scope string, ttl int)
	// WriteOAuthError writes an RFC 6749 error response.
	WriteOAuthError(ctx *fasthttp.RequestCtx, status int, code, description string)
}

// reservedTokenClaims are the claims SignAccessToken sets itself.
var reservedTokenClaims = map[string]struct{}{
	"iss": {}, "aud": {}, "sub": {}, "bf_mode": {}, "scope": {}, "iat": {}, "nbf": {}, "exp": {},
}

// SignAccessToken implements OAuth2TokenIssuer.
func (h *OAuth2IssuanceHandler) SignAccessToken(ctx *fasthttp.RequestCtx, spec AccessTokenSpec) (string, error) {
	for name := range spec.ExtraClaims {
		if _, reserved := reservedTokenClaims[name]; reserved {
			return "", errors.New("extra claim " + name + " is reserved")
		}
	}
	return h.signAccessToken(ctx, spec)
}

// signAccessToken signs every access token this server issues, so all grants
// share one format: RS256 under the server's key, with its kid.
func (h *OAuth2IssuanceHandler) signAccessToken(ctx *fasthttp.RequestCtx, spec AccessTokenSpec) (string, error) {
	ttl := spec.TTL
	if ttl <= 0 {
		ttl = configtables.DefaultAccessTokenTTL
	}
	signingKey, err := h.store.GetOAuth2SigningKey(ctx)
	if err != nil {
		return "", errSigningKeyUnavailable
	}
	privKey, err := parseRSAPrivateKeyPEM(signingKey.PrivateKeyPEM)
	if err != nil {
		return "", errInvalidSigningKey
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":     oauth2IssuerURL(ctx, h.store),
		"aud":     jwt.ClaimStrings(spec.Audience),
		"sub":     spec.Subject,
		"bf_mode": spec.Mode,
		"scope":   spec.Scope,
		"iat":     now.Unix(),
		"nbf":     now.Unix(),
		"exp":     now.Add(time.Duration(ttl) * time.Second).Unix(),
	}
	for name, value := range spec.ExtraClaims {
		claims[name] = value
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = signingKey.KID
	signed, err := tok.SignedString(privKey)
	if err != nil {
		return "", errSigningFailed
	}
	return signed, nil
}

// Signing failures, worded for the server_error description a caller writes.
var (
	errSigningKeyUnavailable = errors.New("signing key unavailable")
	errInvalidSigningKey     = errors.New("invalid signing key")
	errSigningFailed         = errors.New("failed to sign access token")
)

// IssuerURL implements OAuth2TokenIssuer.
func (h *OAuth2IssuanceHandler) IssuerURL(ctx *fasthttp.RequestCtx) string {
	return oauth2IssuerURL(ctx, h.store)
}

// MCPResourceURL implements OAuth2TokenIssuer.
func (h *OAuth2IssuanceHandler) MCPResourceURL(ctx *fasthttp.RequestCtx) string {
	return oauth2MCPResourceURL(ctx, h.store)
}

// GatewayAudience implements OAuth2TokenIssuer.
func (h *OAuth2IssuanceHandler) GatewayAudience(ctx *fasthttp.RequestCtx) string {
	return oauth2GatewayAudience(ctx, h.store)
}

// WriteToken implements OAuth2TokenIssuer.
func (h *OAuth2IssuanceHandler) WriteToken(ctx *fasthttp.RequestCtx, accessToken, scope string, ttl int) {
	sendTokenResponse(ctx, accessToken, "", scope, ttl)
}

// WriteOAuthError implements OAuth2TokenIssuer.
func (h *OAuth2IssuanceHandler) WriteOAuthError(ctx *fasthttp.RequestCtx, status int, code, description string) {
	sendOAuthError(ctx, status, code, description)
}

// extensionServing reports whether ext is on and the issuer is pinned. An
// extension never runs on an issuer derived from the request Host header, which
// the caller controls, so an unset issuer keeps it off.
func extensionServing(ext OAuth2Extensions, store *lib.Config) bool {
	if ext == nil || !ext.Active() {
		return false
	}
	store.Mu.RLock()
	cfg := store.ClientConfig.OAuth2ServerConfig
	store.Mu.RUnlock()
	return cfg != nil && strings.TrimSpace(cfg.IssuerURL.GetValue()) != ""
}

// SetExtensions installs the extensions /oauth2/token consults for grant types
// it does not serve itself. nil removes them.
func (h *OAuth2IssuanceHandler) SetExtensions(extensions OAuth2Extensions) {
	h.extensions = extensions
}

// SetExtensions installs the extensions the authorization server metadata
// document is passed through. nil removes them.
func (h *OAuth2DiscoveryHandler) SetExtensions(extensions OAuth2Extensions) {
	h.extensions = extensions
}

// MCPTokenClaims are the verified claims of a Bifrost-issued access token.
type MCPTokenClaims struct {
	Mode    string // bf_mode
	Subject string // sub
	Scope   string
	// Extra holds every claim beyond the registered ones and bf_mode/scope,
	// such as a grant's own ExtraClaims.
	Extra map[string]any
}

// MCPTokenValidator inspects a verified Bifrost-issued token on /mcp and
// returns an error to refuse it, for example once the grant that minted it has
// been revoked. It runs after signature, issuer and audience checks.
type MCPTokenValidator func(ctx context.Context, claims MCPTokenClaims) error
