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

	t.Run("falls back to the request host when issuer is unset", func(t *testing.T) {
		cfg := &lib.Config{
			ConfigStore:  &mockOAuth2Store{},
			ClientConfig: &configstore.ClientConfig{MCPServerAuthMode: configtables.MCPServerAuthModeBoth},
		}
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("http://mcp.local:8080/oauth2/authorize")
		ctx.Request.Header.SetHost("mcp.local:8080")
		got := oauth2IssuerURL(ctx, cfg)
		assert.Contains(t, got, "mcp.local:8080")
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
