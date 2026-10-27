package handlers

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// The document's client_id equals its URL and redirect_uris holds the callback.
func TestClientMetadataDocument(t *testing.T) {
	cases := []struct {
		name        string
		externalURL string
		host        string
		proto       string
		wantBase    string
		wantAppType string
	}{
		{"external URL configured", "https://bifrost.example.com", "10.0.0.5:8080", "", "https://bifrost.example.com", "web"},
		{"from the request behind a TLS proxy", "", "gateway.example.com", "https", "https://gateway.example.com", "web"},
		{"local deployment", "", "localhost:8080", "", "http://localhost:8080", "native"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientConfig := &configstore.ClientConfig{}
			if tc.externalURL != "" {
				clientConfig.MCPExternalClientURL = schemas.NewSecretVar(tc.externalURL)
			}
			h := &OAuthHandler{store: &lib.Config{ClientConfig: clientConfig}}
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetHost(tc.host)
			if tc.proto != "" {
				ctx.Request.Header.Set("X-Forwarded-Proto", tc.proto)
			}
			ctx.Request.SetRequestURI(oauthClientMetadataPath)

			h.handleClientMetadataDocument(ctx)

			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
			assert.Equal(t, "application/json", string(ctx.Response.Header.ContentType()))
			var doc map[string]any
			require.NoError(t, json.Unmarshal(ctx.Response.Body(), &doc))
			assert.Equal(t, tc.wantBase+oauthClientMetadataPath, doc["client_id"], "client_id must equal the URL the document is served at")
			assert.Equal(t, []any{tc.wantBase + "/api/oauth/callback"}, doc["redirect_uris"])
			assert.Equal(t, "Bifrost MCP Gateway", doc["client_name"])
			assert.Equal(t, "none", doc["token_endpoint_auth_method"])
			assert.Equal(t, []any{"authorization_code", "refresh_token"}, doc["grant_types"])
			assert.Equal(t, []any{"code"}, doc["response_types"])
			assert.Equal(t, tc.wantAppType, doc["application_type"])
		})
	}
}

// BenchmarkClientMetadataDocument measures serving Bifrost's Client ID Metadata Document, with a
// configured external URL and with the base derived from the request.
func BenchmarkClientMetadataDocument(b *testing.B) {
	for name, externalURL := range map[string]string{"external URL": "https://bifrost.example.com", "from request": ""} {
		b.Run(name, func(b *testing.B) {
			clientConfig := &configstore.ClientConfig{}
			if externalURL != "" {
				clientConfig.MCPExternalClientURL = schemas.NewSecretVar(externalURL)
			}
			h := &OAuthHandler{store: &lib.Config{ClientConfig: clientConfig}}
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetHost("gateway.example.com")
			ctx.Request.SetRequestURI(oauthClientMetadataPath)
			b.ReportAllocs()
			for b.Loop() {
				ctx.Response.Reset()
				h.handleClientMetadataDocument(ctx)
			}
		})
	}
}
