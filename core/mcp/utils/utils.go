package utils

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/maximhq/bifrost/core/schemas"
)

// ResolvePerUserOAuthToken looks up the per-user OAuth access token for the given client.
// If no token exists yet, it initiates an OAuth flow and returns an MCPUserOAuthRequiredError.
func ResolvePerUserOAuthToken(ctx *schemas.BifrostContext, client *schemas.MCPClientState, oauth2Provider schemas.OAuth2Provider) (string, error) {
	if oauth2Provider == nil {
		return "", fmt.Errorf("per-user OAuth requires an OAuth2Provider but none is configured")
	}

	virtualKeyID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string)
	userID, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
	sessionToken, _ := ctx.Value(schemas.BifrostContextKeyMCPUserSession).(string)

	// Optional X-Bf-User-Id header overrides user identity; if absent, falls back to virtual key
	if mcpUserID, _ := ctx.Value(schemas.BifrostContextKeyMCPUserID).(string); mcpUserID != "" {
		userID = mcpUserID
	}

	accessToken, err := oauth2Provider.GetUserAccessTokenByIdentity(ctx, virtualKeyID, userID, sessionToken, client.ExecutionConfig.ID)
	if err != nil && !errors.Is(err, schemas.ErrOAuth2TokenNotFound) {
		return "", fmt.Errorf("failed to get user access token for MCP server %s: %w", client.ExecutionConfig.Name, err)
	}
	if err != nil {
		// In LLM gateway mode with no identity, an OAuth flow would produce an orphaned token.
		isMCPGateway, _ := ctx.Value(schemas.BifrostContextKeyIsMCPGateway).(bool)
		if !isMCPGateway && userID == "" && virtualKeyID == "" {
			return "", fmt.Errorf(
				"per-user OAuth for %s requires a user identity: include X-Bf-User-Id or a Virtual Key in your request so the token can be linked to you",
				client.ExecutionConfig.Name,
			)
		}

		if client.ExecutionConfig.OauthConfigID == nil || *client.ExecutionConfig.OauthConfigID == "" {
			return "", fmt.Errorf("per-user OAuth requires an OAuth config but MCP client %s has none", client.ExecutionConfig.Name)
		}
		redirectURI := BuildRedirectURIFromContext(ctx)
		if redirectURI == "" {
			return "", fmt.Errorf("per-user OAuth requires a redirect URI but none is available in context")
		}
		flowInitiation, sessionID, flowErr := oauth2Provider.InitiateUserOAuthFlow(ctx, *client.ExecutionConfig.OauthConfigID, client.ExecutionConfig.ID, redirectURI)
		if flowErr != nil {
			return "", fmt.Errorf("failed to initiate per-user OAuth flow for %s: %w", client.ExecutionConfig.Name, flowErr)
		}
		return "", &schemas.MCPUserOAuthRequiredError{
			MCPClientID:   client.ExecutionConfig.ID,
			MCPClientName: client.ExecutionConfig.Name,
			AuthorizeURL:  flowInitiation.AuthorizeURL,
			SessionID:     sessionID,
			Message:       fmt.Sprintf("Authentication required for %s. Please visit the authorize URL to connect your account.", client.ExecutionConfig.Name),
		}
	}

	return accessToken, nil
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
