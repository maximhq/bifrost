package utils

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/maximhq/bifrost/core/schemas"
)

// ResolvePerUserOAuthToken looks up the per-user OAuth access token for the given client.
// If no token exists yet, it initiates an OAuth flow and returns an MCPUserOAuthRequiredError.
//
// Mode-strict: derives AuthMode from context state
// and looks up exactly one identity column. No fallback chain.
func ResolvePerUserOAuthToken(ctx *schemas.BifrostContext, client *schemas.MCPClientState, oauth2Provider schemas.OAuth2Provider) (string, error) {
	if oauth2Provider == nil {
		return "", fmt.Errorf("per-user OAuth requires an OAuth2Provider but none is configured")
	}

	mode := ctx.MCPAuthMode()
	identity := identityForMCPAuthMode(ctx, mode)

	if identity == "" {
		// No identity column populated for the derived mode. We can neither
		// look up an existing token nor mint a flow whose result would be
		// findable on subsequent calls, so refuse early with actionable copy.
		return "", fmt.Errorf(
			"per-user OAuth for %s requires an identity: send a Virtual Key (x-bf-vk), authenticate as a user, or set x-bf-mcp-session-id to any opaque string you'll re-send on subsequent calls",
			client.ExecutionConfig.Name,
		)
	}

	accessToken, err := oauth2Provider.GetUserAccessTokenByMode(ctx, mode, identity, client.ExecutionConfig.ID)
	// Both sentinels mean "this user must re-authenticate":
	//   - ErrOAuth2TokenNotFound: row missing (never authed, or purged after permanent refresh failure)
	//   - ErrOAuth2TokenExpired:  row present but tokens unusable (access expired + no refresh available)
	// Either way, fall through to the re-auth branch below to surface an inline auth URL.
	if err != nil && !errors.Is(err, schemas.ErrOAuth2TokenNotFound) && !errors.Is(err, schemas.ErrOAuth2TokenExpired) {
		return "", fmt.Errorf("failed to get user access token for MCP server %s: %w", client.ExecutionConfig.Name, err)
	}
	if err != nil {
		if client.ExecutionConfig.OauthConfigID == nil || *client.ExecutionConfig.OauthConfigID == "" {
			return "", fmt.Errorf("per-user OAuth requires an OAuth config but MCP client %s has none", client.ExecutionConfig.Name)
		}
		redirectURI := BuildRedirectURIFromContext(ctx)
		if redirectURI == "" {
			return "", fmt.Errorf("per-user OAuth requires a redirect URI but none is available in context")
		}
		flowInitiation, sessionID, flowErr := oauth2Provider.InitiateUserOAuthFlow(ctx, *client.ExecutionConfig.OauthConfigID, client.ExecutionConfig.ID, redirectURI, mode)
		if flowErr != nil {
			return "", fmt.Errorf("failed to initiate per-user OAuth flow for %s: %w", client.ExecutionConfig.Name, flowErr)
		}
		return "", &schemas.MCPUserOAuthRequiredError{
			MCPClientID:   client.ExecutionConfig.ID,
			MCPClientName: client.ExecutionConfig.Name,
			AuthorizeURL:  flowInitiation.AuthorizeURL,
			SessionID:     sessionID,
			// Include the URL in the message itself — plain-text clients
			// (curl, basic SDK wrappers) won't parse extra_fields, and a
			// "please visit the authorize URL" hint with no URL is
			// useless to them. The URL is already exposed on the same
			// response via extra_fields.mcp_auth_required.authorize_url,
			// so embedding it here doesn't widen the surface.
			Message: fmt.Sprintf("Authentication required for %s. Visit %s to connect your account.", client.ExecutionConfig.Name, flowInitiation.AuthorizeURL),
		}
	}

	return accessToken, nil
}

// identityForMode returns the identity string to look up by, given the derived
// mode. Mirrors the priority used by ctx.AuthMode(): UserID for user mode,
// resolved VK ID for vk mode, session ID for session mode.
func identityForMCPAuthMode(ctx *schemas.BifrostContext, mode schemas.MCPAuthMode) string {
	switch mode {
	case schemas.MCPAuthModeUser:
		if v, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string); v != "" {
			return v
		}
	case schemas.MCPAuthModeVK:
		if v, _ := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string); v != "" {
			return v
		}
	case schemas.MCPAuthModeSession:
		if v, _ := ctx.Value(schemas.BifrostContextKeyMCPSessionID).(string); v != "" {
			return v
		}
	}
	return ""
}

// BuildPerUserOAuthHeaders clones the provided headers and adds the Bearer token,
// preserving any request-scoped extra headers already present.
func BuildPerUserOAuthHeaders(headers http.Header, accessToken string) http.Header {
	h := headers.Clone()
	h.Set("Authorization", "Bearer "+accessToken)
	return h
}

// BuildRedirectURIFromContext extracts the OAuth redirect URI from context.
func BuildRedirectURIFromContext(ctx *schemas.BifrostContext) string {
	if uri, ok := ctx.Value(schemas.BifrostContextKeyOAuthRedirectURI).(string); ok && uri != "" {
		return uri
	}
	return ""
}

// GetHeadersForToolExecution sets additional headers for tool execution.
// It returns the headers for the tool execution.
func GetHeadersForToolExecution(ctx *schemas.BifrostContext, client *schemas.MCPClientState) http.Header {
	if ctx == nil || client == nil || client.ExecutionConfig == nil {
		return make(http.Header)
	}
	headers := make(http.Header)
	if client.ExecutionConfig.Headers != nil {
		for key, value := range client.ExecutionConfig.Headers {
			headers.Add(key, value.GetValue())
		}
	}
	// Give priority to extra headers in the context
	if extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyMCPExtraHeaders).(map[string][]string); ok {
		filteredHeaders := make(http.Header)
		for key, values := range extraHeaders {
			if client.ExecutionConfig.AllowedExtraHeaders.IsAllowed(key) {
				for i, value := range values {
					if i == 0 {
						filteredHeaders.Set(key, value)
					} else {
						filteredHeaders.Add(key, value)
					}
				}
			}
		}
		// Add the filtered headers to the headers
		if len(filteredHeaders) > 0 {
			for k, values := range filteredHeaders {
				for i, v := range values {
					if i == 0 {
						headers.Set(k, v)
					} else {
						headers.Add(k, v)
					}
				}
			}
		}
	}
	return headers
}
