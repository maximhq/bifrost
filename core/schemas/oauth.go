package schemas

import (
	"context"
	"time"
)

// OauthProvider interface defines OAuth operations
type OAuth2Provider interface {
	// GetAccessToken retrieves the access token for a given oauth_config_id (server-level OAuth)
	GetAccessToken(ctx context.Context, oauthConfigID string) (string, error)

	// RefreshAccessToken refreshes the access token for a given oauth_config_id
	RefreshAccessToken(ctx context.Context, oauthConfigID string) error

	// ValidateToken checks if the token is still valid
	ValidateToken(ctx context.Context, oauthConfigID string) (bool, error)

	// RevokeToken revokes the OAuth token
	RevokeToken(ctx context.Context, oauthConfigID string) error

	// Per-user OAuth methods

	// GetUserAccessToken retrieves the access token for a per-user OAuth session.
	// If the token is expired, it automatically attempts a refresh.
	GetUserAccessToken(ctx context.Context, sessionToken string) (string, error)

	// GetUserAccessTokenByIdentity retrieves the upstream access token for a user
	// identified by virtualKeyID, userID, or sessionToken (fallback), for a specific
	// MCP client. Tokens looked up by identity persist across sessions.
	GetUserAccessTokenByIdentity(ctx context.Context, virtualKeyID, userID, sessionToken, mcpClientID string) (string, error)

	// InitiateUserOAuthFlow creates a per-user OAuth session and returns the authorization URL.
	// Returns (flow initiation details, session ID for polling, error).
	InitiateUserOAuthFlow(ctx context.Context, oauthConfigID string, mcpClientID string, redirectURI string) (*OAuth2FlowInitiation, string, error)

	// CompleteUserOAuthFlow handles the OAuth callback for a per-user flow.
	// Returns the session token that the user should send on subsequent requests.
	CompleteUserOAuthFlow(ctx context.Context, state string, code string) (string, error)

	// RefreshUserAccessToken refreshes a per-user OAuth access token.
	RefreshUserAccessToken(ctx context.Context, sessionToken string) error

	// RevokeUserToken revokes a per-user OAuth token and marks the session as revoked.
	RevokeUserToken(ctx context.Context, sessionToken string) error
}

// OauthConfig represents OAuth client configuration
type OAuth2Config struct {
	ID              string   `json:"id"`
	ClientID        string   `json:"client_id,omitempty"`        // Optional: Will be obtained via dynamic registration (RFC 7591) if not provided
	ClientSecret    string   `json:"client_secret,omitempty"`    // Optional: For public clients using PKCE, or obtained via dynamic registration
	AuthorizeURL    string   `json:"authorize_url,omitempty"`    // Optional: Will be discovered from ServerURL if not provided
	TokenURL        string   `json:"token_url,omitempty"`        // Optional: Will be discovered from ServerURL if not provided
	RegistrationURL *string  `json:"registration_url,omitempty"` // Optional: For dynamic client registration (RFC 7591), can be discovered
	RedirectURI     string   `json:"redirect_uri"`               // Required
	Scopes          []string `json:"scopes,omitempty"`           // Optional: Can be discovered
	ServerURL       string   `json:"server_url"`                 // MCP server URL for OAuth discovery (required if URLs not provided)
	UseDiscovery    bool     `json:"use_discovery,omitempty"`    // Deprecated: Discovery now happens automatically when URLs are missing
}

// OauthToken represents OAuth access and refresh tokens
type OAuth2Token struct {
	ID              string     `json:"id"`
	AccessToken     string     `json:"access_token"`
	RefreshToken    string     `json:"refresh_token"`
	TokenType       string     `json:"token_type"`
	ExpiresAt       time.Time  `json:"expires_at"`
	Scopes          []string   `json:"scopes"`
	LastRefreshedAt *time.Time `json:"last_refreshed_at,omitempty"`
}

// OauthFlowInitiation represents the response when initiating an OAuth flow
type OAuth2FlowInitiation struct {
	OauthConfigID string    `json:"oauth_config_id"`
	AuthorizeURL  string    `json:"authorize_url"`
	State         string    `json:"state"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// OAuth2TokenExchangeRequest represents the OAuth token exchange request
type OAuth2TokenExchangeRequest struct {
	GrantType    string `json:"grant_type"`
	Code         string `json:"code,omitempty"`
	RedirectURI  string `json:"redirect_uri,omitempty"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	CodeVerifier string `json:"code_verifier,omitempty"` // PKCE verifier for authorization_code grant
}

// OAuth2TokenExchangeResponse represents the OAuth token exchange response
type OAuth2TokenExchangeResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope,omitempty"`
}
