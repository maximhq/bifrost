package credstore

import (
	"context"
	"fmt"
	"net/http"

	"github.com/maximhq/bifrost/core/schemas"
)

// sharedOAuthResolver handles MCPAuthTypeOauth — admin-once OAuth where the
// upstream token is shared across all callers. ConnectionHeaders returns
// only the Authorization header; static config headers are layered
// separately by the caller via utils.StaticConfigHeaders.
type sharedOAuthResolver struct {
	provider schemas.OAuth2Provider
}

func (r *sharedOAuthResolver) ConnectionHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	if config.OauthConfigID == nil || *config.OauthConfigID == "" {
		return nil, schemas.ErrOAuth2ConfigNotFound
	}
	if r.provider == nil {
		return nil, schemas.ErrOAuth2ProviderNotAvailable
	}
	accessToken, err := r.provider.GetAccessToken(ctx, *config.OauthConfigID)
	if err != nil {
		return nil, err
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+accessToken)
	return headers, nil
}

func (r *sharedOAuthResolver) RequiresPerCallConnection() bool { return false }

// ForceRefresh unconditionally refreshes the shared token, bypassing
// GetAccessToken's ExpiresAt gate. Resolution (the OauthConfigID lookup, the
// provider-availability check) now happens entirely inside the provider —
// see schemas.OAuth2Provider.ForceRefreshAccessToken.
func (r *sharedOAuthResolver) ForceRefresh(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) error {
	if r.provider == nil {
		return schemas.ErrOAuth2ProviderNotAvailable
	}
	return r.provider.ForceRefreshAccessToken(ctx, config)
}

// AdminConnectionHeaders delegates to ConnectionHeaders for a client
// currently running per-call (needs_session_stickiness nil/false) — there is
// no separate "admin" credential distinct from the one used for real calls,
// so the periodic connection checker's per-call discovery cycle
// (MCPManager.performAdminToolDiscovery) resolves exactly the same shared
// token a real tool call would. A sticky client should never reach this
// method at all (it holds a persistent connection instead, discovered via
// connectToMCPClient) — erroring rather than silently delegating turns a
// caller-chain bug into an immediate, loud failure instead of a
// quietly-redundant credential resolution.
func (r *sharedOAuthResolver) AdminConnectionHeaders(ctx context.Context, config *schemas.MCPClientConfig) (http.Header, error) {
	if !needsSessionStickiness(config) {
		return r.ConnectionHeaders(schemas.NewBifrostContext(ctx, schemas.NoDeadline), config)
	}
	return nil, fmt.Errorf("admin connection headers not supported for auth_type %q on a sticky connection", "oauth")
}
