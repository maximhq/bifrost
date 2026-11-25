package handlers

import (
	"net/url"
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
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
		if issuer, ok := additionalIssuerForHost(cfg, ctx); ok {
			return issuer
		}
		return strings.TrimRight(cfg.IssuerURL.GetValue(), "/")
	}
	if oauthEnabled {
		return ""
	}
	return lib.BuildBaseURL(ctx, "")
}

// additionalIssuerForHost returns the additional issuer whose hostname is the
// request's Host. The Host only chooses among issuers the operator configured,
// so a forged one falls back to the primary rather than naming its own issuer.
func additionalIssuerForHost(cfg *configtables.OAuth2ServerConfig, ctx *fasthttp.RequestCtx) (string, bool) {
	if len(cfg.AdditionalIssuerURLs) == 0 || ctx == nil {
		return "", false
	}
	host := trimDefaultPort(ctx.Host())
	for _, issuer := range cfg.AdditionalIssuerURLs {
		if value := issuer.GetValue(); value != "" && issuerHostIs(value, host) {
			return strings.TrimRight(value, "/"), true
		}
	}
	return "", false
}

// issuerHostIs reports whether issuer's host is host, ignoring case and a default
// port, as configstore.IssuerHost compares them. It slices the (validated) URL
// rather than parsing it, so picking an issuer allocates nothing per request.
func issuerHostIs(issuer string, host []byte) bool {
	_, rest, ok := strings.Cut(issuer, "://")
	if !ok {
		return false
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimSuffix(strings.TrimSuffix(rest, ":443"), ":80")
	if len(rest) != len(host) {
		return false
	}
	for i := 0; i < len(host); i++ {
		if lowerASCII(rest[i]) != lowerASCII(host[i]) {
			return false
		}
	}
	return true
}

// trimDefaultPort drops a trailing :443 or :80 from host.
func trimDefaultPort(host []byte) []byte {
	for _, port := range []string{":443", ":80"} {
		if len(host) > len(port) && string(host[len(host)-len(port):]) == port {
			return host[:len(host)-len(port)]
		}
	}
	return host
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// oauth2AcceptedIssuers are the issuers a Bifrost-issued token may name: the
// primary and every additional issuer, or the request's issuer when none is
// configured (header mode).
func oauth2AcceptedIssuers(ctx *fasthttp.RequestCtx, store *lib.Config) []string {
	store.Mu.RLock()
	cfg := store.ClientConfig.OAuth2ServerConfig
	store.Mu.RUnlock()
	if cfg == nil || !cfg.IssuerURL.IsSet() || len(cfg.AdditionalIssuerURLs) == 0 {
		return []string{oauth2IssuerURL(ctx, store)}
	}
	issuers := make([]string, 0, 1+len(cfg.AdditionalIssuerURLs))
	issuers = append(issuers, strings.TrimRight(cfg.IssuerURL.GetValue(), "/"))
	for _, issuer := range cfg.AdditionalIssuerURLs {
		if value := issuer.GetValue(); value != "" {
			issuers = append(issuers, strings.TrimRight(value, "/"))
		}
	}
	return issuers
}

// oauth2IssuerAccepted reports whether iss is an issuer a token may name: the
// request's issuer (selected), or any configured issuer. It allocates nothing,
// since every authenticated /mcp request runs it.
func oauth2IssuerAccepted(store *lib.Config, selected, iss string) bool {
	if iss == "" {
		return false
	}
	if iss == selected {
		return true
	}
	store.Mu.RLock()
	cfg := store.ClientConfig.OAuth2ServerConfig
	store.Mu.RUnlock()
	if cfg == nil || !cfg.IssuerURL.IsSet() {
		return false
	}
	if iss == strings.TrimRight(cfg.IssuerURL.GetValue(), "/") {
		return true
	}
	for _, issuer := range cfg.AdditionalIssuerURLs {
		if v := issuer.GetValue(); v != "" && iss == strings.TrimRight(v, "/") {
			return true
		}
	}
	return false
}

// oauth2IssuerForResource is the issuer a grant for resource belongs to: the
// configured issuer resource sits under. Tokens and the consent redirect take
// their issuer from the grant, not from whichever host a step of the sign-in
// arrived on. Falls back to the request's issuer.
func oauth2IssuerForResource(ctx *fasthttp.RequestCtx, store *lib.Config, resource string) string {
	for _, issuer := range oauth2AcceptedIssuers(ctx, store) {
		if resource == issuer || strings.HasPrefix(resource, issuer+"/") {
			return issuer
		}
	}
	return oauth2IssuerURL(ctx, store)
}

// oauth2ResourceServed reports whether resource is served where served is the
// request's resource: served itself, or the same path under another configured
// issuer, so a grant from one hostname is honoured on the others.
func oauth2ResourceServed(ctx *fasthttp.RequestCtx, store *lib.Config, resource, served string) bool {
	if resource == served {
		return true
	}
	selected := oauth2IssuerURL(ctx, store)
	suffix, ok := strings.CutPrefix(served, selected)
	if selected == "" || !ok {
		return false
	}
	for _, issuer := range oauth2AcceptedIssuers(ctx, store) {
		if resource == issuer+suffix {
			return true
		}
	}
	return false
}

// secretVarValues lists the resolved values of vars, for comparing two lists.
func secretVarValues(vars []*schemas.SecretVar) []string {
	values := make([]string, len(vars))
	for i, v := range vars {
		values[i] = v.GetValue()
	}
	return values
}

// oauth2BrowserBaseURL is the base of URLs a user's browser opens in the OAuth
// flow (authorize, consent, device sign-in): browser_base_url when set, else the
// issuer. Only those URLs move; the issuer, server-to-server endpoints and token
// claims stay on oauth2IssuerURL.
func oauth2BrowserBaseURL(ctx *fasthttp.RequestCtx, store *lib.Config) string {
	store.Mu.RLock()
	cfg := store.ClientConfig.OAuth2ServerConfig
	store.Mu.RUnlock()
	if cfg != nil && cfg.BrowserBaseURL.IsSet() {
		if base := strings.TrimRight(cfg.BrowserBaseURL.GetValue(), "/"); base != "" {
			return base
		}
	}
	return oauth2IssuerURL(ctx, store)
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
