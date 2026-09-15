package schemas

import (
	"context"
	"time"
)

// OauthProvider interface defines OAuth operations
type OAuth2Provider interface {
	// GetAccessToken retrieves the access token for a given oauth_config_id (server-level OAuth)
	GetAccessToken(ctx context.Context, oauthConfigID string) (string, error)

	// GetAdminAccessToken is GetAccessToken's admin-mode counterpart:
	// resolves the retained bootstrap-verification credential for a
	// per-user client's periodic tool-discovery refresh (see
	// ClientToolSyncer.performSync's per-user branch), rather than the
	// shared-mode production credential. Keyed by the MCP client ID, which
	// every retained admin credential carries.
	GetAdminAccessToken(ctx context.Context, mcpClientID string) (string, error)

	// ValidateToken checks if the token is still valid
	ValidateToken(ctx context.Context, oauthConfigID string) (bool, error)

	// RevokeToken revokes the OAuth token
	RevokeToken(ctx context.Context, oauthConfigID string) error

	// Per-user OAuth methods

	// GetUserAccessTokenByMode retrieves the upstream access token for a single
	// identity dimension determined by mode. No fallback chain — exactly one
	// identity column is queried. Filters status='active' so orphaned rows never
	// satisfy a lookup. identity is the user ID for MCPAuthModeUser, the VK row
	// ID for MCPAuthModeVK, and the raw session ID for MCPAuthModeSession.
	GetUserAccessTokenByMode(ctx context.Context, mode MCPAuthMode, identity, mcpClientID string) (string, error)

	// InitiateUserOAuthFlow creates or refreshes the per-user OAuth flow row
	// for a (mode, identity, mcp_client) binding and returns the auth landing
	// URL. flowMode tags the row's flow_mode and decides which identity column
	// gets populated from context (UserID for MCPAuthModeUser, the resolved VK
	// row ID for MCPAuthModeVK, the session ID for MCPAuthModeSession). For
	// MCPAuthModeUser flows where no UserID is available in context yet
	// (external MCP client OAuth init), the column is left NULL and stamped
	// at completion. Returns (flow initiation details, flow row ID, error).
	InitiateUserOAuthFlow(ctx context.Context, oauthConfigID string, mcpClientID string, redirectURI string, flowMode MCPAuthMode) (*OAuth2FlowInitiation, string, error)

	// CompleteUserOAuthFlow handles the OAuth callback for a per-user flow.
	// Returns the SessionID stored on the flow row (populated for session-mode,
	// empty otherwise).
	CompleteUserOAuthFlow(ctx context.Context, state string, code string) (string, error)

	// RefreshAccessToken refreshes any MCP OAuth token — the single shared
	// client credential or a per-identity credential alike — looked up by the
	// token row's own primary-key ID. The one refresh path for every kind of
	// MCP OAuth credential; GetAccessToken and GetUserAccessTokenByMode both
	// funnel their lazy pre-flight refresh through this.
	RefreshAccessToken(ctx context.Context, tokenID string) error

	// ForceRefreshAccessToken unconditionally refreshes the MCP OAuth
	// credential backing config, bypassing whichever lazy ExpiresAt gate
	// GetAccessToken / GetUserAccessTokenByMode would otherwise apply. Used
	// when a live upstream call was rejected despite Bifrost's own expiry
	// bookkeeping still considering the credential valid: the local
	// bookkeeping is what's stale here, not necessarily the token, so the
	// gate that would normally skip a refresh has to be skipped too.
	//
	// Branches internally on config.AuthType:
	//   - MCPAuthTypeOauth resolves the shared token linked to
	//     config.OauthConfigID, the same lookup GetAccessToken performs.
	//   - MCPAuthTypePerUserOauth derives (mode, identity) from ctx via
	//     ctx.MCPAuthMode() / ctx.MCPIdentity(mode) and resolves the
	//     per-identity token, the same lookup GetUserAccessTokenByMode
	//     performs.
	//
	// Either branch ends by calling RefreshAccessToken once the underlying
	// token row is resolved — the one refresh path for every kind of MCP
	// OAuth credential.
	ForceRefreshAccessToken(ctx *BifrostContext, config *MCPClientConfig) error

	// Delegated token exchange methods (MCPAuthTypeTokenExchange)

	// TokenExchangeAvailable reports whether delegated token exchange can
	// run (a TokenExchangeIdPResolver is installed and Available). Consulted
	// by the create/update path to reject token_exchange clients that could
	// never resolve.
	TokenExchangeAvailable() bool

	// GetExchangedAccessToken returns an upstream access token for config
	// (AuthType == token_exchange), exchanging the caller's identity-provider
	// token (BifrostContextKeyMCPInboundBearer) for one scoped to
	// config.TokenExchange.Audience. Results are cached in memory per
	// (auth mode, identity, mcp client) until shortly before expiry.
	// Returns ErrExchangeSubjectTokenMissing when the request carried no
	// caller token, ErrTokenExchangeUnavailable when no identity-provider
	// integration is configured, and *TokenExchangeRejectedError when the
	// provider refused the exchange.
	GetExchangedAccessToken(ctx *BifrostContext, config *MCPClientConfig) (string, error)
}

// TokenExchangeRejectedError reports that the identity provider refused a
// delegated token exchange: the subject token was invalid or revoked, the
// exchange client lacks permission for the audience, or the grant is
// disabled. Detail carries the provider's error/error_description body for
// display; it never contains tokens.
type TokenExchangeRejectedError struct {
	Detail string
}

func (e *TokenExchangeRejectedError) Error() string {
	return "identity provider rejected the token exchange: " + e.Detail
}

// TokenExchangeGrantShape selects the wire form of a delegated token
// exchange request; identity providers differ on the grant they expose.
type TokenExchangeGrantShape string

const (
	// TokenExchangeGrantRFC8693 is the standard token-exchange grant
	// (grant_type=urn:ietf:params:oauth:grant-type:token-exchange, RFC 8693).
	TokenExchangeGrantRFC8693 TokenExchangeGrantShape = "rfc8693"
	// TokenExchangeGrantJWTBearerOBO is the jwt-bearer on-behalf-of variant
	// (grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer with
	// requested_token_use=on_behalf_of) used by identity providers that do
	// not expose the RFC 8693 grant.
	TokenExchangeGrantJWTBearerOBO TokenExchangeGrantShape = "jwt_bearer_obo"
)

// TokenExchangeIdP is the identity-provider side of one delegated token
// exchange call: where to send it and which grant shape to use. The client
// credentials authorized to perform the exchange normally live on each MCP
// client's own token_exchange block, not here — IdPClientID/IdPClientSecret
// below are the one exception.
type TokenExchangeIdP struct {
	TokenEndpoint string
	GrantShape    TokenExchangeGrantShape
	// IdPClientID and IdPClientSecret are the SSO login application's own
	// credentials, from the deployment's identity-provider integration.
	// Populated only to satisfy MCPTokenExchangeConfig.UseIdPCredentials —
	// see that field's doc comment for why some providers (Microsoft Entra
	// ID) require reusing the SSO application itself rather than a
	// dedicated exchange application. Resolver implementations may leave
	// these empty if UseIdPCredentials is never set to true for any client
	// they serve.
	IdPClientID     string
	IdPClientSecret string
}

// TokenExchangeIdPResolver supplies the identity-provider details for
// delegated token exchange (MCPAuthTypeTokenExchange). Implementations
// derive the token endpoint and grant shape from the deployment's
// identity-provider integration. A nil resolver, or Available() == false,
// means the auth type is unavailable: creation of token_exchange clients is
// rejected and existing ones fail resolution.
type TokenExchangeIdPResolver interface {
	// Available reports whether delegated token exchange can run: an
	// identity-provider integration is configured.
	Available() bool

	// Resolve returns the endpoint and grant shape for exchanges made on
	// behalf of the given MCP client — implementations should honor a
	// client-level authorization-server override (config.TokenExchange)
	// where the provider supports one, falling back to the deployment's
	// default identity-provider integration otherwise. Called per exchange
	// (implementations should cache discovery internally) so provider
	// reconfiguration takes effect without restarts.
	Resolve(ctx context.Context, config *MCPClientConfig) (*TokenExchangeIdP, error)
}

// OauthConfig represents OAuth client configuration
type OAuth2Config struct {
	ID              string     `json:"id"`
	ClientID        *SecretVar `json:"client_id,omitempty"`        // Optional: Will be obtained via dynamic registration (RFC 7591) if not provided. Supports env./vault. references.
	ClientSecret    *SecretVar `json:"client_secret,omitempty"`    // Optional: For public clients using PKCE, or obtained via dynamic registration. Supports env./vault. references.
	AuthorizeURL    string     `json:"authorize_url,omitempty"`    // Optional: Will be discovered from ServerURL if not provided
	TokenURL        string     `json:"token_url,omitempty"`        // Optional: Will be discovered from ServerURL if not provided
	RegistrationURL *string    `json:"registration_url,omitempty"` // Optional: For dynamic client registration (RFC 7591), can be discovered
	RedirectURI     string     `json:"redirect_uri"`               // Required
	Scopes          []string   `json:"scopes,omitempty"`           // Optional: Can be discovered
	ServerURL       string     `json:"server_url"`                 // MCP server URL for OAuth discovery (required if URLs not provided)
	Resource        string     `json:"resource,omitempty"`         // Optional OAuth resource indicator (RFC 8707); omitted when empty
	UseDiscovery    bool       `json:"use_discovery,omitempty"`    // Deprecated: Discovery now happens automatically when URLs are missing
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
	Resource     string `json:"resource,omitempty"`      // OAuth resource indicator (RFC 8707)
}

// OAuth2TokenExchangeResponse represents the OAuth token exchange response
type OAuth2TokenExchangeResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope,omitempty"`
}
