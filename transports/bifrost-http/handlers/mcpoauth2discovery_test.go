package handlers

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestDiscovery_GatedOnAuthMode(t *testing.T) {
	key, _ := newTestSigningKey(t)
	store := &mockOAuth2Store{signingKey: key}

	t.Run("headers mode returns 404 on all discovery endpoints", func(t *testing.T) {
		h := NewOAuth2DiscoveryHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false))
		for _, fn := range []func(*fasthttp.RequestCtx){h.handlePRM, h.handleASMetadata, h.handleJWKS} {
			ctx := &fasthttp.RequestCtx{}
			fn(ctx)
			assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())
		}
	})

	t.Run("oauth and both modes serve discovery", func(t *testing.T) {
		for _, mode := range []configtables.MCPServerAuthMode{configtables.MCPServerAuthModeBoth, configtables.MCPServerAuthModeOAuth} {
			h := NewOAuth2DiscoveryHandler(newTestOAuth2Config(store, mode, false))
			for _, fn := range []func(*fasthttp.RequestCtx){h.handlePRM, h.handleASMetadata, h.handleJWKS} {
				ctx := &fasthttp.RequestCtx{}
				fn(ctx)
				assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(mode))
			}
		}
	})
}

func TestDiscovery_ProtectedResourceMetadata(t *testing.T) {
	store := &mockOAuth2Store{}
	h := NewOAuth2DiscoveryHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeOAuth, false))
	ctx := &fasthttp.RequestCtx{}
	h.handlePRM(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())

	var doc map[string]any
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &doc))
	assert.Equal(t, testMCPResource, doc["resource"])
	assert.Equal(t, []any{testIssuer}, doc["authorization_servers"])
}

func TestDiscovery_AuthorizationServerMetadata(t *testing.T) {
	store := &mockOAuth2Store{}
	h := NewOAuth2DiscoveryHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeOAuth, false))
	ctx := &fasthttp.RequestCtx{}
	h.handleASMetadata(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())

	var doc map[string]any
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &doc))
	assert.Equal(t, testIssuer, doc["issuer"])
	assert.Equal(t, testIssuer+"/oauth2/authorize", doc["authorization_endpoint"])
	assert.Equal(t, testIssuer+"/oauth2/token", doc["token_endpoint"])
	assert.Equal(t, testIssuer+"/oauth2/register", doc["registration_endpoint"])
	assert.Equal(t, []any{"code"}, doc["response_types_supported"])
	assert.Equal(t, []any{"authorization_code", "refresh_token"}, doc["grant_types_supported"])
	assert.Equal(t, []any{"S256"}, doc["code_challenge_methods_supported"])
	assert.Equal(t, []any{"none"}, doc["token_endpoint_auth_methods_supported"])
	assert.Equal(t, true, doc["authorization_response_iss_parameter_supported"])
	// MCP 2025-11-25+: clients may use a Client ID Metadata Document URL as client_id.
	assert.Equal(t, true, doc["client_id_metadata_document_supported"])
}

// TestDiscovery_IssuerWithTrailingSlash pins that an issuer_url ending in "/" is
// served without it, so no URL built from the issuer carries "//".
func TestDiscovery_IssuerWithTrailingSlash(t *testing.T) {
	cfg := newTestOAuth2Config(&mockOAuth2Store{}, configtables.MCPServerAuthModeOAuth, false)
	cfg.ClientConfig.OAuth2ServerConfig.IssuerURL = schemas.NewSecretVar(testIssuer + "/")
	h := NewOAuth2DiscoveryHandler(cfg)

	ctx := &fasthttp.RequestCtx{}
	h.handleASMetadata(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	var doc map[string]any
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &doc))
	assert.Equal(t, testIssuer, doc["issuer"])
	assert.Equal(t, testIssuer+"/oauth2/authorize", doc["authorization_endpoint"])
	assert.Equal(t, testIssuer+"/oauth2/token", doc["token_endpoint"])
	assert.Equal(t, testIssuer+"/oauth2/register", doc["registration_endpoint"])
	assert.Equal(t, testIssuer+"/.well-known/jwks.json", doc["jwks_uri"])

	prm := &fasthttp.RequestCtx{}
	h.handlePRM(prm)
	require.Equal(t, fasthttp.StatusOK, prm.Response.StatusCode())
	var prmDoc map[string]any
	require.NoError(t, json.Unmarshal(prm.Response.Body(), &prmDoc))
	assert.Equal(t, testMCPResource, prmDoc["resource"])
	assert.Equal(t, []any{testIssuer}, prmDoc["authorization_servers"])

	assert.Equal(t, `Bearer resource_metadata="`+testIssuer+`/.well-known/oauth-protected-resource/mcp"`, wwwAuthenticateValue(&fasthttp.RequestCtx{}, cfg))
}

func TestDiscovery_JWKS(t *testing.T) {
	key, _ := newTestSigningKey(t)
	store := &mockOAuth2Store{signingKey: key}
	h := NewOAuth2DiscoveryHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeOAuth, false))
	ctx := &fasthttp.RequestCtx{}
	h.handleJWKS(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())

	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &doc))
	require.Len(t, doc.Keys, 1)
	jwk := doc.Keys[0]
	assert.Equal(t, "RSA", jwk["kty"])
	assert.Equal(t, "RS256", jwk["alg"])
	assert.Equal(t, "sig", jwk["use"])
	assert.Equal(t, key.KID, jwk["kid"])
	assert.NotEmpty(t, jwk["n"])
	assert.NotEmpty(t, jwk["e"])
}

// TestDiscovery_ResponsesAreNotCacheable pins Cache-Control: no-store on every
// discovery document so a shared cache in front of Bifrost can never hold one.
func TestDiscovery_ResponsesAreNotCacheable(t *testing.T) {
	key, _ := newTestSigningKey(t)
	store := &mockOAuth2Store{signingKey: key}
	h := NewOAuth2DiscoveryHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeOAuth, false))
	for i, fn := range []func(*fasthttp.RequestCtx){h.handlePRM, h.handleASMetadata, h.handleJWKS} {
		ctx := &fasthttp.RequestCtx{}
		fn(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
		assert.Equal(t, "no-store", string(ctx.Response.Header.Peek("Cache-Control")), "document %d", i)
	}
}

// TestDiscovery_ASMetadataExtensions checks an extension can add to the
// authorization server metadata, and that nothing changes without one.
func TestDiscovery_ASMetadataExtensions(t *testing.T) {
	store := &mockOAuth2Store{}
	h := NewOAuth2DiscoveryHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeOAuth, false))
	h.SetExtensions(fakeGrantExtension{grantType: "urn:test:grant"})
	ctx := &fasthttp.RequestCtx{}
	h.handleASMetadata(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	var doc map[string]any
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &doc))
	assert.Equal(t, []any{"authorization_code", "refresh_token", "urn:test:grant"}, doc["grant_types_supported"])
}

// TestDiscovery_ExtensionOnlyServer checks that with MCP OAuth off, an active
// extension keeps the authorization server metadata and keys up, advertising
// only its own grants and no MCP login endpoints, while /mcp's protected
// resource metadata stays off; with the extension off, or no pinned issuer,
// nothing is served.
func TestDiscovery_ExtensionOnlyServer(t *testing.T) {
	key, _ := newTestSigningKey(t)
	store := &mockOAuth2Store{signingKey: key}
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	h := NewOAuth2DiscoveryHandler(cfg)
	h.SetExtensions(fakeGrantExtension{grantType: "urn:test:grant"})

	as := &fasthttp.RequestCtx{}
	h.handleASMetadata(as)
	require.Equal(t, fasthttp.StatusOK, as.Response.StatusCode())
	var doc map[string]any
	require.NoError(t, json.Unmarshal(as.Response.Body(), &doc))
	assert.Equal(t, []any{"urn:test:grant"}, doc["grant_types_supported"])
	assert.Equal(t, testIssuer+"/oauth2/token", doc["token_endpoint"])
	assert.NotContains(t, doc, "authorization_endpoint")
	assert.NotContains(t, doc, "registration_endpoint")
	jwks := &fasthttp.RequestCtx{}
	h.handleJWKS(jwks)
	assert.Equal(t, fasthttp.StatusOK, jwks.Response.StatusCode())
	prm := &fasthttp.RequestCtx{}
	h.handlePRM(prm)
	assert.Equal(t, fasthttp.StatusNotFound, prm.Response.StatusCode(), "/mcp does not take Bifrost tokens with MCP OAuth off")

	issuer := cfg.ClientConfig.OAuth2ServerConfig.IssuerURL
	cfg.ClientConfig.OAuth2ServerConfig.IssuerURL = nil
	unpinned := &fasthttp.RequestCtx{}
	h.handleASMetadata(unpinned)
	assert.Equal(t, fasthttp.StatusNotFound, unpinned.Response.StatusCode(), "never served on a Host-derived issuer")
	cfg.ClientConfig.OAuth2ServerConfig.IssuerURL = issuer

	h.SetExtensions(fakeGrantExtension{grantType: "urn:test:grant", off: true})
	for name, serve := range map[string]func(*fasthttp.RequestCtx){"metadata": h.handleASMetadata, "jwks": h.handleJWKS} {
		ctx := &fasthttp.RequestCtx{}
		serve(ctx)
		assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode(), name)
	}
}
