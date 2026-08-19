package credstore

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// sharedHeadersResolver handles MCPAuthTypeHeaders — the admin-configured
// static headers on the MCP client. ConnectionHeaders here returns ONLY the
// Authorization header (if admin set one in config.Headers); other static
// headers are layered by the caller via utils.StaticConfigHeaders so they
// remain plugin-mutable.
//
// CredStore.resolverFor also normalizes empty AuthType to "headers" so this
// resolver covers the legacy DB default.
type sharedHeadersResolver struct{}

func (r *sharedHeadersResolver) ConnectionHeaders(_ *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	headers := http.Header{}
	if config == nil {
		return headers, nil
	}
	// Headers are case-insensitive on the wire but case-sensitive in Go maps;
	// match case-insensitively (consistent with utils.StaticConfigHeaders'
	// Authorization exclusion) to keep the security guarantee tight.
	for key, value := range config.Headers {
		if strings.EqualFold(key, "Authorization") {
			headers.Set("Authorization", value.GetValue())
			break
		}
	}
	return headers, nil
}

func (r *sharedHeadersResolver) RequiresPerCallConnection() bool { return false }

// ForceRefresh is a no-op — static admin-configured headers have nothing to refresh.
func (r *sharedHeadersResolver) ForceRefresh(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) error {
	return nil
}

// AdminConnectionHeaders delegates to ConnectionHeaders for a client
// currently running per-call (needs_session_stickiness nil/false) — there is
// no separate "admin" credential distinct from the one used for real calls,
// so the periodic connection checker's per-call discovery cycle
// (MCPManager.performAdminToolDiscovery) resolves exactly the same
// Authorization header a real tool call would. Static (non-Authorization)
// config headers are layered separately by VerifyHeadersConnection, same as
// the normal call path. A sticky client should never reach this method at
// all (it holds a persistent connection instead, discovered via
// connectToMCPClient) — erroring rather than silently delegating turns a
// caller-chain bug into an immediate, loud failure instead of a
// quietly-redundant credential resolution.
func (r *sharedHeadersResolver) AdminConnectionHeaders(ctx context.Context, config *schemas.MCPClientConfig) (http.Header, error) {
	if !needsSessionStickiness(config) {
		return r.ConnectionHeaders(schemas.NewBifrostContext(ctx, schemas.NoDeadline), config)
	}
	return nil, fmt.Errorf("admin connection headers not supported for auth_type %q on a sticky connection", "headers")
}
