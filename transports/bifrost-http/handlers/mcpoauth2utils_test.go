package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
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

// TestOAuth2IssuerURL_AdditionalIssuers pins how a request picks its issuer: the
// additional issuer whose hostname is the Host (case and default port ignored),
// else the primary, so a forged Host never names an issuer of its own.
func TestOAuth2IssuerURL_AdditionalIssuers(t *testing.T) {
	cfg := withTunnelIssuer(newTestOAuth2Config(&mockOAuth2Store{}, configtables.MCPServerAuthModeOAuth, false))
	cfg.ClientConfig.OAuth2ServerConfig.AdditionalIssuerURLs = append(cfg.ClientConfig.OAuth2ServerConfig.AdditionalIssuerURLs,
		schemas.NewSecretVar("https://other.test/bifrost/"))
	for host, want := range map[string]string{
		"bifrost-tunnel.test":     testTunnelIssuer,
		"BIFROST-TUNNEL.test:443": testTunnelIssuer,
		"other.test":              "https://other.test/bifrost",
		"bifrost.test":            testIssuer,
		"evil.test":               testIssuer,
		"":                        testIssuer,
	} {
		assert.Equal(t, want, oauth2IssuerURL(hostCtx(host), cfg), "host %q", host)
	}
	assert.Equal(t, []string{testIssuer, testTunnelIssuer, "https://other.test/bifrost"}, oauth2AcceptedIssuers(hostCtx("evil.test"), cfg))

	assert.Equal(t, testTunnelIssuer, oauth2IssuerForResource(hostCtx("bifrost.test"), cfg, testTunnelIssuer+"/mcp"), "the grant's issuer, not the host's")
	assert.True(t, oauth2ResourceServed(hostCtx("bifrost.test"), cfg, testTunnelIssuer+"/mcp", testMCPResource))
	assert.False(t, oauth2ResourceServed(hostCtx("bifrost.test"), cfg, "https://evil.test/mcp", testMCPResource))
	assert.False(t, oauth2ResourceServed(hostCtx("bifrost.test"), cfg, testTunnelIssuer+"/gateway/claude-code", testMCPResource), "another resource path")

	single := newTestOAuth2Config(&mockOAuth2Store{}, configtables.MCPServerAuthModeOAuth, false)
	assert.Equal(t, testIssuer, oauth2IssuerURL(hostCtx("bifrost-tunnel.test"), single), "no additional issuers: always the primary")
	assert.Equal(t, []string{testIssuer}, oauth2AcceptedIssuers(hostCtx("bifrost-tunnel.test"), single))
}

func TestOAuth2BrowserBaseURL(t *testing.T) {
	t.Run("is the issuer when unset", func(t *testing.T) {
		cfg := newTestOAuth2Config(&mockOAuth2Store{}, configtables.MCPServerAuthModeOAuth, false)
		assert.Equal(t, testIssuer, oauth2BrowserBaseURL(&fasthttp.RequestCtx{}, cfg))
	})

	t.Run("is the configured value, without a trailing slash, whatever the request host", func(t *testing.T) {
		cfg := newTestOAuth2Config(&mockOAuth2Store{}, configtables.MCPServerAuthModeOAuth, false)
		cfg.ClientConfig.OAuth2ServerConfig.BrowserBaseURL = schemas.NewSecretVar(testBrowserBase + "/")
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetHost("evil.example")
		assert.Equal(t, testBrowserBase, oauth2BrowserBaseURL(ctx, cfg))
		assert.Equal(t, testIssuer, oauth2IssuerURL(ctx, cfg), "the issuer does not move")
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

func TestAuditOAuthConfigBeforeRedactsURLCredentials(t *testing.T) {
	snap := auditOAuthConfigBefore(&configtables.TableOauthConfig{
		Status:       "authorized",
		AuthorizeURL: "https://user:pw-authorize@idp.example.com/authorize",
		TokenURL:     "https://user:pw-token@idp.example.com/token",
		ServerURL:    "https://user:pw-server@mcp.example.com",
	})
	for key, secret := range map[string]string{"authorize_url": "pw-authorize", "token_url": "pw-token", "server_url": "pw-server"} {
		assert.NotContains(t, snap[key], secret, key)
	}
	assert.Equal(t, "authorized", snap["status"])
}
