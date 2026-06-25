package utils

import (
	"context"
	"net/http"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// FlattenHeaders converts an http.Header into a map[string]string suitable
// for mcp-go's transport.WithHTTPHeaders, used for both shared persistent
// connections (clientmanager) and ephemeral per-call connections
// (AcquireClientConn). Multi-value headers collapse to their first value.
func FlattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) == 0 {
			continue
		}
		out[k] = v[0]
	}
	return out
}

// BuildMCPCallbackBaseURL extracts the base URL set on the BifrostContext by
// the HTTP middleware (e.g. "https://host"). Per-user OAuth and per-user
// headers resolvers append their respective paths on top.
//
// Trailing slashes are stripped defensively. The sole writer today
// (lib/ctx.go BuildBaseURL) already normalizes, but OAuth providers match
// redirect URIs exactly — a `https://host//api/oauth/callback` produced by
// a future writer that forgets to trim would silently break every per-user
// OAuth flow. Guarding once on the read side keeps that invariant local
// to this function rather than spread across every potential writer.
func BuildMCPCallbackBaseURL(ctx *schemas.BifrostContext) string {
	if base, ok := ctx.Value(schemas.BifrostContextKeyMCPCallbackBaseURL).(string); ok && base != "" {
		return strings.TrimRight(base, "/")
	}
	return ""
}

// BuildOAuthRedirectURIFromContext returns the full OAuth callback URL
// ("<base>/api/oauth/callback") needed by the per-user OAuth flow, or empty
// if the base URL is unavailable.
func BuildOAuthRedirectURIFromContext(ctx *schemas.BifrostContext) string {
	base := BuildMCPCallbackBaseURL(ctx)
	if base == "" {
		return ""
	}
	return base + "/api/oauth/callback"
}

// StaticConfigHeaders returns the admin-configured static headers from
// config.Headers MINUS any header whose name is a credential — Authorization
// always, plus any name declared in config.PerUserHeaderKeys. These are the
// headers that are safe to expose to MCP connect-plugins via the
// PreConnectionHook gate — plugins may add, remove, or rewrite them.
//
// Why exclude:
//   - Authorization: credential by definition. The CredentialStore resolver
//     for the active auth type emits the final value (config bearer for
//     MCPAuthTypeHeaders; dynamic token for OAuth-flavored types).
//   - PerUserHeaderKeys: credential schema for MCPAuthTypePerUserHeaders. If
//     an admin accidentally (or deliberately) baked one of these names into
//     config.Headers with a static value, exposing it to plugins would leak
//     the static fallback. The per-user-headers resolver emits the caller's
//     value; the static fallback should never reach the wire (and never
//     reach plugins) for per-user-headers clients.
//
// Comparison is case-insensitive because HTTP headers are case-insensitive
// on the wire but case-sensitive in Go maps.
func StaticConfigHeaders(config *schemas.MCPClientConfig) http.Header {
	headers := make(http.Header)
	if config == nil {
		return headers
	}
	for key, value := range config.Headers {
		if strings.EqualFold(key, "Authorization") {
			continue
		}
		if matchesPerUserHeaderKey(key, config.PerUserHeaderKeys) {
			continue
		}
		headers.Add(key, value.GetValue())
	}
	return headers
}

// matchesPerUserHeaderKey reports whether name matches any entry in
// perUserKeys (case-insensitively). Used by StaticConfigHeaders to strip
// per-user credential keys from the plugin-visible static header set.
func matchesPerUserHeaderKey(name string, perUserKeys []string) bool {
	for _, key := range perUserKeys {
		if strings.EqualFold(name, key) {
			return true
		}
	}
	return false
}

// Canonical-form invariant for per-user-headers data
// =====================================================
// HTTP header names are case-insensitive on the wire (RFC 7230 §3.2),
// so anywhere the per-user-headers feature compares a schema key against
// a stored or submitted header name we'd need EqualFold lookups. Doing
// that defensively at every read site is fragile — a single missed call
// site re-introduces the bug (stored `authorization` looking missing
// against schema `Authorization`, etc.).
//
// Instead we enforce a write-time invariant: every external boundary
// that accepts a header key (or a credential header map) lowercases and
// trims via the helpers below before persisting. Downstream code can
// then assume canonical form and use plain map lookups.
//
// Write boundaries that MUST call these:
//   - createMCPClient / updateMCPClient / resolvePerUserHeaderKeys
//     (handlers/mcp.go) for MCPClientConfig.PerUserHeaderKeys
//   - flowSubmit (handlers/mcp_per_user_headers.go) for the
//     user-submitted credential.Headers map
//   - loadMCPClientConfigFromFile (lib/config.go) for the config.json
//     load path
//
// New write paths added in the future must canonicalize too — there is
// no defensive case-folding on the read side anymore.

// CanonicalizeHeaderKey returns the canonical lowercase + trimmed form
// of a single header key. Empty input returns empty.
func CanonicalizeHeaderKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// CanonicalizeHeaderKeys returns a new slice with every entry passed
// through CanonicalizeHeaderKey. Nil in → nil out so a caller that
// uses "nil means preserve existing" semantics (e.g.
// resolvePerUserHeaderKeys, UpdateMCPClientConfig) keeps that signal.
// The input slice is not mutated.
func CanonicalizeHeaderKeys(keys []string) []string {
	if keys == nil {
		return nil
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = CanonicalizeHeaderKey(k)
	}
	return out
}

// CanonicalizeHeaderMap returns a new map whose keys are passed through
// CanonicalizeHeaderKey. On collision (e.g. "Authorization" and
// "authorization" both present in the input), the last value wins —
// callers that need duplicate detection should run it on the raw input
// before calling this. Nil in → nil out.
func CanonicalizeHeaderMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[CanonicalizeHeaderKey(k)] = v
	}
	return out
}

// ExtractFilteredExtras returns just the per-request "extra" headers carried
// in the BifrostContext (BifrostContextKeyMCPExtraHeaders), scoped by the
// client's AllowedExtraHeaders. Static config headers are NOT included here —
// those live on the upstream transport via StaticConfigHeaders and apply
// automatically to every message it carries. This function exists for the
// per-message CredentialStore.RequestHeaders path on shared connections.
func ExtractFilteredExtras(ctx context.Context, config *schemas.MCPClientConfig) http.Header {
	headers := make(http.Header)
	if ctx == nil || config == nil {
		return headers
	}
	extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyMCPExtraHeaders).(map[string][]string)
	if !ok {
		return headers
	}
	for key, values := range extraHeaders {
		if !config.AllowedExtraHeaders.IsAllowed(key) {
			continue
		}
		for i, value := range values {
			if i == 0 {
				headers.Set(key, value)
			} else {
				headers.Add(key, value)
			}
		}
	}
	return headers
}
