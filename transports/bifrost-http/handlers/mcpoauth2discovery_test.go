package handlers

import (
	"encoding/json"
	"testing"

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
