package handlers

import (
	"strings"

	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// oauth2IssuerURL resolves the effective AS issuer URL for a request.
// Uses the explicitly configured IssuerURL when set; falls back to deriving
// it from the request Host header for single-host / dev deployments.
func oauth2IssuerURL(ctx *fasthttp.RequestCtx, store *lib.Config) string {
	store.Mu.RLock()
	cfg := store.ClientConfig.OAuth2ServerConfig
	store.Mu.RUnlock()
	if cfg != nil && cfg.IssuerURL.IsSet() {
		return cfg.IssuerURL.GetValue()
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
