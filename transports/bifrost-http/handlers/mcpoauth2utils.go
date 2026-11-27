package handlers

import (
	"net/url"
	"slices"
	"strings"

	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// oauth2RedirectAllowed applies the current operator policy on every flow step,
// including clients and pending flows created before a policy change. An
// AllowAnyRedirectURI entry approves any https callback that passes the shape
// checks; private-use scheme callbacks (RFC 8252 §7.1) need an exact entry,
// since any app on a device can claim the same scheme.
func oauth2RedirectAllowed(store *lib.Config, candidate string) bool {
	if !isAllowedRedirectScheme(candidate) {
		return false
	}
	u, _ := url.Parse(candidate)
	if (u.Scheme == "http" || u.Scheme == "https") && isLoopbackRedirectHost(u.Hostname()) {
		return true
	}
	if u.Scheme == "cursor" && u.Host == "anysphere.cursor-mcp" {
		return true
	}
	store.Mu.RLock()
	defer store.Mu.RUnlock()
	cfg := store.ClientConfig.OAuth2ServerConfig
	return cfg != nil && (slices.Contains(cfg.AllowedRedirectURIs, candidate) ||
		(u.Scheme == "https" && slices.Contains(cfg.AllowedRedirectURIs, configtables.AllowAnyRedirectURI)))
}

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
// A configured value is returned without trailing slashes, since every endpoint,
// claim and redirect is built by appending a path that starts with "/".
func oauth2IssuerURL(ctx *fasthttp.RequestCtx, store *lib.Config) string {
	store.Mu.RLock()
	cfg := store.ClientConfig.OAuth2ServerConfig
	oauthEnabled := store.ClientConfig.IsMCPOAuthDiscoveryEnabled()
	store.Mu.RUnlock()
	if cfg != nil && cfg.IssuerURL.IsSet() {
		return strings.TrimRight(cfg.IssuerURL.GetValue(), "/")
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
	return oauth2IssuerURL(ctx, store) + "/mcp"
}

// oauth2GatewayAudience identifies Bifrost's inference routes as a resource:
// the gateway itself, so the issuer URL.
func oauth2GatewayAudience(ctx *fasthttp.RequestCtx, store *lib.Config) string {
	return oauth2IssuerURL(ctx, store)
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

// oauth2ResourceMetadataURL returns the RFC 9728 §3.1 protected-resource metadata
// URL for the /mcp resource under issuer. The well-known segment sits at the origin
// root with the issuer's path inserted after it, so an issuer that carries
// server.base_path ("https://host/bifrost") advertises
// "https://host/.well-known/oauth-protected-resource/bifrost/mcp", which is also
// where a client that discovers by convention looks. The handlers accept any path
// under the well-known routes, so the proxy only has to forward "/.well-known/".
// The issuer path is spliced in as written (percent-escapes preserved), so an
// escaped delimiter cannot turn the rest of the URL into a query or fragment.
func oauth2ResourceMetadataURL(issuer string) string {
	const wellKnown = "/.well-known/oauth-protected-resource"
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return issuer + wellKnown + "/mcp"
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	return u.Scheme + "://" + u.Host + wellKnown + path + "/mcp"
}
