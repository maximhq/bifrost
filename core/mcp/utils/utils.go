package utils

import (
	"net/http"

	"github.com/maximhq/bifrost/core/schemas"
)

// FlattenHeaders converts an http.Header into a map[string]string suitable
// for mcp-go's transport.WithHTTPHeaders, which is the API used for both
// the persistent shared upstream connection (in clientmanager) and the
// ephemeral per-call connection (in OpenConnectionAndExecuteTool).
// Multi-value headers collapse to their first value — matching the legacy
// MCPClientConfig.HttpHeaders / ExecuteToolWithUserToken behavior, which
// built map[string]string directly from config.Headers with a single value
// per key.
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

// ExtractFilteredExtras returns just the per-request "extra" headers carried
// in the BifrostContext (BifrostContextKeyMCPExtraHeaders), scoped by the
// client's AllowedExtraHeaders. Static config headers are NOT included here —
// those live on the upstream transport and apply automatically to every
// message it carries. This function exists for the per-message
// CredentialStore.RequestHeaders path on shared connections.
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

// GetHeadersForToolExecution returns the union of the client's static config
// headers and the filtered per-request extras carried in the BifrostContext.
// Used by CredentialStore resolvers to assemble the "stable + per-call" base
// before adding any auth-specific headers (e.g. a Bearer token).
func GetHeadersForToolExecution(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) http.Header {
	if ctx == nil || config == nil {
		return make(http.Header)
	}
	headers := make(http.Header)
	if config.Headers != nil {
		for key, value := range config.Headers {
			headers.Add(key, value.GetValue())
		}
	}
	// Layer per-request extras on top.
	for key, values := range ExtractFilteredExtras(ctx, config) {
		for i, v := range values {
			if i == 0 {
				headers.Set(key, v)
			} else {
				headers.Add(key, v)
			}
		}
	}
	return headers
}
