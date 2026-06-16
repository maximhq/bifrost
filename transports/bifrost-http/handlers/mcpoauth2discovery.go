package handlers

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// OAuth2DiscoveryHandler serves the three well-known discovery endpoints that
// make Bifrost's /mcp endpoint a spec-compliant OAuth 2.1 protected resource:
//
//   - GET /.well-known/oauth-protected-resource[/{path}]   (RFC 9728 PRM)
//   - GET /.well-known/oauth-authorization-server[/{path}] (RFC 8414 AS metadata)
//   - GET /.well-known/jwks.json                           (RFC 7517 JWKS)
//
// All three return 404 when MCPServerAuthMode == "headers" (the default), so
// discovery is only available when the operator explicitly enables OAuth mode.
// Discoverability is the feature toggle.
type OAuth2DiscoveryHandler struct {
	store *lib.Config
}

// NewOAuth2DiscoveryHandler creates a new discovery handler.
func NewOAuth2DiscoveryHandler(store *lib.Config) *OAuth2DiscoveryHandler {
	return &OAuth2DiscoveryHandler{store: store}
}

// RegisterRoutes wires all well-known discovery routes. Routes are always
// registered; individual handlers 404 when discovery is disabled.
func (h *OAuth2DiscoveryHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// RFC 9728: both root and path-aware well-known forms are required.
	r.GET("/.well-known/oauth-protected-resource", lib.ChainMiddlewares(h.handlePRM, middlewares...))
	r.GET("/.well-known/oauth-protected-resource/{path:*}", lib.ChainMiddlewares(h.handlePRM, middlewares...))

	// RFC 8414: same two forms.
	r.GET("/.well-known/oauth-authorization-server", lib.ChainMiddlewares(h.handleASMetadata, middlewares...))
	r.GET("/.well-known/oauth-authorization-server/{path:*}", lib.ChainMiddlewares(h.handleASMetadata, middlewares...))

	// RFC 7517 JWKS.
	r.GET("/.well-known/jwks.json", lib.ChainMiddlewares(h.handleJWKS, middlewares...))
}

// discoveryEnabled reports whether OAuth discovery is active, reading the mode
// from the in-memory ClientConfig under the read lock.
func (h *OAuth2DiscoveryHandler) discoveryEnabled() bool {
	h.store.Mu.RLock()
	enabled := h.store.ClientConfig.IsMCPOAuthDiscoveryEnabled()
	h.store.Mu.RUnlock()
	return enabled
}

// handlePRM serves GET /.well-known/oauth-protected-resource[/{path}].
// RFC 9728 §3 Protected Resource Metadata.
func (h *OAuth2DiscoveryHandler) handlePRM(ctx *fasthttp.RequestCtx) {
	if !h.discoveryEnabled() {
		ctx.SetStatusCode(fasthttp.StatusNotFound)
		return
	}

	base := oauth2IssuerURL(ctx, h.store)
	doc := map[string]any{
		"resource":                 oauth2MCPResourceURL(ctx, h.store),
		"authorization_servers":    []string{base},
		"scopes_supported":         []string{"mcp"},
		"bearer_methods_supported": []string{"header"},
	}
	data, err := sonic.Marshal(doc)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("marshal protected resource metadata: %v", err))
		return
	}
	ctx.SetContentType("application/json")
	ctx.SetBody(data)
}

// handleASMetadata serves GET /.well-known/oauth-authorization-server[/{path}].
// RFC 8414 Authorization Server Metadata.
func (h *OAuth2DiscoveryHandler) handleASMetadata(ctx *fasthttp.RequestCtx) {
	if !h.discoveryEnabled() {
		ctx.SetStatusCode(fasthttp.StatusNotFound)
		return
	}

	base := oauth2IssuerURL(ctx, h.store)
	doc := map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth2/authorize",
		"token_endpoint":                        base + "/oauth2/token",
		"registration_endpoint":                 base + "/oauth2/register",
		"jwks_uri":                              base + "/.well-known/jwks.json",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"mcp"},
		// RFC 9207: we include iss in authorization responses.
		"authorization_response_iss_parameter_supported": true,
	}
	data, err := sonic.Marshal(doc)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("marshal authorization server metadata: %v", err))
		return
	}
	ctx.SetContentType("application/json")
	ctx.SetBody(data)
}

// handleJWKS serves GET /.well-known/jwks.json (RFC 7517).
func (h *OAuth2DiscoveryHandler) handleJWKS(ctx *fasthttp.RequestCtx) {
	if !h.discoveryEnabled() {
		ctx.SetStatusCode(fasthttp.StatusNotFound)
		return
	}

	if h.store.ConfigStore == nil {
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`{"keys":[]}`)
		return
	}

	key, err := h.store.ConfigStore.GetOAuth2SigningKey(ctx)
	if err != nil {
		logger.Error("oauth2 discovery: failed to load signing key: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to load signing key")
		return
	}

	var jwks []map[string]any
	if key != nil {
		pub, parseErr := parseRSAPublicKeyPEM(key.PublicKeyPEM)
		if parseErr != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to parse signing key: %v", parseErr))
			return
		}
		jwks = []map[string]any{rsaPublicKeyToJWK(key.KID, "RS256", pub)}
	}

	data, err := sonic.Marshal(map[string]any{"keys": jwks})
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("marshal jwks: %v", err))
		return
	}
	ctx.SetContentType("application/json")
	ctx.SetBody(data)
}

// parseRSAPublicKeyPEM decodes a PEM-encoded RSA public key.
func parseRSAPublicKeyPEM(pemStr string) (*rsa.PublicKey, error) {
	block, rest := pem.Decode([]byte(pemStr))
	if block == nil || len(rest) > 0 {
		return nil, fmt.Errorf("malformed public key PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("expected RSA public key, got %T", pub)
	}
	return rsaPub, nil
}

// rsaPublicKeyToJWK encodes an RSA public key as a JWK (RFC 7517 §6.3).
func rsaPublicKeyToJWK(kid, alg string, pub *rsa.PublicKey) map[string]any {
	return map[string]any{
		"kty": "RSA",
		"use": "sig",
		"kid": kid,
		"alg": alg,
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}
