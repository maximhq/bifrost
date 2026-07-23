package credstore

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/maximhq/bifrost/core/mcp/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// perUserOAuthResolver handles MCPAuthTypePerUserOauth — each caller's
// upstream token is keyed by (auth_mode, identity, mcp_client). On miss or
// expiry, an OAuth flow is initiated and a *MCPUserOAuthRequiredError is
// raised so the caller can complete authentication.
//
// ConnectionHeaders returns only the Authorization header — static config
// headers are layered separately by AcquireClientConn / clientmanager via
// utils.StaticConfigHeaders so the connect-plugin gate never observes the
// bearer token.
//
// RequiresPerCallConnection is true: per-user OAuth clients never hold a
// persistent upstream connection; AcquireClientConn opens a fresh ephemeral
// HTTP transport per call using the resolved Bearer + plugin-mutated static
// headers.
type perUserOAuthResolver struct {
	provider schemas.OAuth2Provider
}

func (r *perUserOAuthResolver) ConnectionHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	if r.provider == nil {
		return nil, fmt.Errorf("per-user OAuth requires an OAuth2Provider but none is configured")
	}

	mode := ctx.MCPAuthMode()
	identity := identityForMCPAuthMode(ctx, mode)
	if identity == "" {
		return nil, fmt.Errorf(
			"per-user OAuth for %s requires an identity: send a Virtual Key (x-bf-vk), authenticate as a user, or set x-bf-mcp-session-id to any opaque string you'll re-send on subsequent calls",
			config.Name,
		)
	}

	accessToken, err := r.provider.GetUserAccessTokenByMode(ctx, mode, identity, config.ID)
	// Both sentinels mean "this user must re-authenticate":
	//   - ErrOAuth2TokenNotFound: row missing (never authed, or purged after permanent refresh failure)
	//   - ErrOAuth2TokenExpired:  row present but tokens unusable (access expired + no refresh available)
	// Either way, fall through to the re-auth branch below to surface an inline auth URL.
	if err != nil && !errors.Is(err, schemas.ErrOAuth2TokenNotFound) && !errors.Is(err, schemas.ErrOAuth2TokenExpired) {
		return nil, fmt.Errorf("failed to get user access token for MCP server %s: %w", config.Name, err)
	}
	if err != nil {
		if config.OauthConfigID == nil || *config.OauthConfigID == "" {
			return nil, fmt.Errorf("per-user OAuth requires an OAuth config but MCP client %s has none", config.Name)
		}
		redirectURI := utils.BuildOAuthRedirectURIFromContext(ctx)
		if redirectURI == "" {
			return nil, fmt.Errorf("per-user OAuth requires a redirect URI but none is available in context")
		}
		// No identity gate here. A user-mode caller (e.g. an MCP client presenting
		// only a Bearer JWT) carries no dashboard session at tool-call time. The
		// flow row records the caller's identity (flow.UserID = bf_sub); the binding
		// is verified at the cookie-bearing UI step (flowStart → canAccessUserFlow)
		// before the upstream authorize URL — which carries the single-use state — is
		// ever revealed, and the callback binds the token to the flow's recorded
		// identity. So initiating the flow here grants nothing on its own.
		flowInitiation, sessionID, flowErr := r.provider.InitiateUserOAuthFlow(ctx, *config.OauthConfigID, config.ID, redirectURI, mode)
		if flowErr != nil {
			return nil, fmt.Errorf("failed to initiate per-user OAuth flow for %s: %w", config.Name, flowErr)
		}
		message := fmt.Sprintf("Authentication required for %s. Visit %s to connect your account.", config.Name, flowInitiation.AuthorizeURL)
		if schemas.MCPAuthURLHasTempTokenFragment(flowInitiation.AuthorizeURL) {
			message += schemas.MCPAuthTempTokenReminder
		}
		return nil, &schemas.MCPAuthRequiredError{
			Kind:          schemas.MCPAuthRequiredKindOAuth,
			MCPClientID:   config.ID,
			MCPClientName: config.Name,
			AuthorizeURL:  flowInitiation.AuthorizeURL,
			SessionID:     sessionID,
			// Include the URL in the message itself — plain-text clients
			// (curl, basic SDK wrappers) won't parse extra_fields, and a
			// "please visit the authorize URL" hint with no URL is useless
			// to them. The URL is already exposed via
			// extra_fields.mcp_auth_required.authorize_url, so embedding it
			// here doesn't widen the surface.
			Message: message,
		}
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+accessToken)
	return headers, nil
}

func (r *perUserOAuthResolver) RequiresPerCallConnection() bool { return true }
