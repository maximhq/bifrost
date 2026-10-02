package handlers

import (
	"strings"

	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// oauth2IssuerURL resolves the effective AS issuer URL for a request. It is
// the explicitly configured IssuerURL whenever one is set. With MCP OAuth
// enabled (discovery/issuance live) the issuer must be a deployment constant:
// config validation already refuses to enable it without issuer_url, and this
// function never substitutes the request Host header in that mode, since Host
// is caller-controlled and would let one request poison the issuer, token and
// JWKS URLs that every client trusts. It returns "" if that invariant is ever
// bypassed, which yields relative URLs rather than attacker-chosen absolute
// ones. The Host-derived fallback remains only for headers mode, where no
// OAuth issuer identity is served and the value feeds nothing security-relevant.
func oauth2IssuerURL(ctx *fasthttp.RequestCtx, store *lib.Config) string {
	store.Mu.RLock()
	cfg := store.ClientConfig.OAuth2ServerConfig
	oauthEnabled := store.ClientConfig.IsMCPOAuthDiscoveryEnabled()
	store.Mu.RUnlock()
	if cfg != nil && cfg.IssuerURL.IsSet() {
		return cfg.IssuerURL.GetValue()
	}
	if oauthEnabled {
		return ""
	}
	return lib.BuildBaseURL(ctx, "")
}

// oauth2MCPResourceURL returns the canonical RFC 8707 resource identifier for
// the /mcp endpoint — the single protected resource this server issues tokens
// for. Discovery advertises it, the authorize endpoint pins the request's
// resource parameter to it, and /mcp token verification checks the audience
// against it; routing all three through here keeps them from drifting.
func oauth2MCPResourceURL(ctx *fasthttp.RequestCtx, store *lib.Config) string {
	// Trim a trailing slash so a slash-suffixed issuer_url can't produce "//mcp"
	// and drift this canonical resource away from what clients normalize to.
	return strings.TrimRight(oauth2IssuerURL(ctx, store), "/") + "/mcp"
}

// oauth2ServerCfg returns the OAuth2 AS-specific config under the read lock,
// falling back to sensible defaults when not yet configured.
func oauth2ServerCfg(store *lib.Config) *configtables.OAuth2ServerConfig {
	store.Mu.RLock()
	cfg := store.ClientConfig.OAuth2ServerConfig
	store.Mu.RUnlock()
	if cfg == nil {
		return configtables.DefaultOAuth2ServerConfig()
	}
	return cfg
}
