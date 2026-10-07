package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
)

func TestMatchRedirectURI(t *testing.T) {
	cases := []struct {
		name       string
		candidate  string
		registered []string
		want       bool
	}{
		{"exact non-loopback match", "https://app.example/cb", []string{"https://app.example/cb"}, true},
		{"non-loopback mismatch", "https://app.example/other", []string{"https://app.example/cb"}, false},
		{"loopback any port (127.0.0.1)", "http://127.0.0.1:55555/cb", []string{"http://127.0.0.1:1234/cb"}, true},
		{"loopback any port (localhost)", "http://localhost:9999/cb", []string{"http://localhost:3000/cb"}, true},
		{"loopback path must still match", "http://127.0.0.1:5/other", []string{"http://127.0.0.1:1/cb"}, false},
		{"loopback scheme must still match", "https://127.0.0.1:5/cb", []string{"http://127.0.0.1:1/cb"}, false},
		{"malformed candidate", "://bad", []string{"https://app.example/cb"}, false},
		{"no registered uris", "https://app.example/cb", nil, false},
		{"loopback query must match", "http://localhost:1234/cb?next=other", []string{"http://localhost:4321/cb?next=original"}, false},
		{"fragment is rejected", "http://localhost:1234/cb#tail", []string{"http://localhost:4321/cb#tail"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, matchRedirectURI(tc.candidate, tc.registered))
		})
	}
}

func TestOAuth2IssuerURL(t *testing.T) {
	t.Run("uses the configured issuer when set", func(t *testing.T) {
		store := &mockOAuth2Store{}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
		assert.Equal(t, testIssuer, oauth2IssuerURL(&fasthttp.RequestCtx{}, cfg))
	})

	t.Run("falls back to the request host only while discovery is disabled", func(t *testing.T) {
		cfg := &lib.Config{
			ConfigStore:  &mockOAuth2Store{},
			ClientConfig: &configstore.ClientConfig{MCPServerAuthMode: configtables.MCPServerAuthModeHeaders},
		}
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("http://mcp.local:8080/oauth2/authorize")
		ctx.Request.Header.SetHost("mcp.local:8080")
		got := oauth2IssuerURL(ctx, cfg)
		assert.Equal(t, "http://mcp.local:8080", got)
	})

	// Config validation makes issuer_url mandatory once discovery is on, so this
	// state is unreachable through normal loading; the resolver must still never
	// hand out a Host-derived issuer if it is ever reached.
	t.Run("never derives the issuer from the request host when discovery is enabled", func(t *testing.T) {
		for _, mode := range []configtables.MCPServerAuthMode{configtables.MCPServerAuthModeBoth, configtables.MCPServerAuthModeOAuth} {
			cfg := &lib.Config{
				ConfigStore:  &mockOAuth2Store{},
				ClientConfig: &configstore.ClientConfig{MCPServerAuthMode: mode},
			}
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("http://evil.example/oauth2/authorize")
			ctx.Request.Header.SetHost("evil.example")
			got := oauth2IssuerURL(ctx, cfg)
			assert.NotContains(t, got, "evil.example", string(mode))
		}
	})
}

func TestOAuth2ServerCfg_DefaultsWhenUnset(t *testing.T) {
	cfg := &lib.Config{
		ConfigStore:  &mockOAuth2Store{},
		ClientConfig: &configstore.ClientConfig{MCPServerAuthMode: configtables.MCPServerAuthModeBoth},
	}
	got := oauth2ServerCfg(cfg)
	assert.Equal(t, configtables.DefaultAuthCodeTTL, got.AuthCodeTTL)
	assert.Equal(t, configtables.DefaultAccessTokenTTL, got.AccessTokenTTL)
}
