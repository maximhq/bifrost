package utils

import (
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

// BuildRedirectURIFromContext extracts the OAuth redirect URI from context.
func BuildRedirectURIFromContext(ctx *schemas.BifrostContext) string {
	if uri, ok := ctx.Value(schemas.BifrostContextKeyOAuthRedirectURI).(string); ok && uri != "" {
		return uri
	}
	return ""
}

// StaticConfigHeaders returns the admin-configured static headers from
// config.Headers MINUS any Authorization header. These are the headers that
// are safe to expose to MCP connect-plugins via the PreConnectionHook gate —
// plugins may add, remove, or rewrite them.
//
// Authorization is excluded by design even when an admin sets it manually
// in config.Headers (e.g. for MCPAuthTypeHeaders with a hard-coded bearer):
// it is a credential, and credentials are layered AFTER the plugin gate
// runs. The CredentialStore resolver for the relevant auth type emits the
// final Authorization value (either from config or from a dynamic token).
func StaticConfigHeaders(config *schemas.MCPClientConfig) http.Header {
	headers := make(http.Header)
	if config == nil {
		return headers
	}
	for key, value := range config.Headers {
		if strings.EqualFold(key, "Authorization") {
			continue
		}
		headers.Add(key, value.GetValue())
	}
	return headers
}

// ExtractFilteredExtras returns just the per-request "extra" headers carried
// in the BifrostContext (BifrostContextKeyMCPExtraHeaders), scoped by the
// client's AllowedExtraHeaders. Static config headers are NOT included here —
// those live on the upstream transport via StaticConfigHeaders and apply
// automatically to every message it carries. This function exists for the
// per-message CredentialStore.RequestHeaders path on shared connections.
func ExtractFilteredExtras(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) http.Header {
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
