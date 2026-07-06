package oauth2

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/temptoken"
)

const (
	maxTokenRetries = 3
	networkTimeout  = 30 * time.Second
)

// OAuth2Provider implements the schemas.OAuth2Provider interface
// It provides OAuth 2.0 authentication functionality with database persistence
type OAuth2Provider struct {
	configStore    configstore.ConfigStore
	mu             sync.RWMutex
	retryBaseDelay time.Duration // base delay for token endpoint retry backoff; doubles each attempt (1×, 2×, 4×)

	// tempTokens, when non-nil and enabled in client config, is used by
	// InitiateUserOAuthFlow to mint a short-lived mcp_auth temp token and
	// embed it in the returned auth-page URL as a fragment. Optional — when
	// nil or disabled, the URL is returned without a fragment and the page
	// works only for callers already authenticated to the dashboard.
	//
	// Held as an atomic.Pointer rather than under p.mu: it is written once at
	// startup and read on the request path, and p.mu is write-locked across
	// token-refresh network I/O (RefreshAccessToken/RevokeToken). Sharing p.mu
	// would stall flow init/cleanup reads behind unrelated refresh traffic.
	tempTokens atomic.Pointer[temptoken.Service]
}

// NewOAuth2Provider creates a new OAuth provider instance
func NewOAuth2Provider(configStore configstore.ConfigStore, logger schemas.Logger) *OAuth2Provider {
	if logger == nil {
		logger = bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	}
	SetLogger(logger)
	return &OAuth2Provider{
		configStore:    configStore,
		retryBaseDelay: time.Second,
	}
}

// SetTempTokenService installs the temp-token service used by
// InitiateUserOAuthFlow to mint the mcp_auth token embedded in the
// auth-page URL fragment. Called by server startup once both services
// have been constructed (the provider is built first by lib/config.go,
// the service later by the HTTP transport).
func (p *OAuth2Provider) SetTempTokenService(svc *temptoken.Service) {
	p.tempTokens.Store(svc)
}

// tempTokenService returns the current temp-token service. Lock-free: the
// pointer is read atomically so request-path callers never contend with the
// p.mu write lock held across token-refresh network I/O.
func (p *OAuth2Provider) tempTokenService() *temptoken.Service {
	return p.tempTokens.Load()
}

// mcpTempTokenAuthEnabled reports whether MCP per-user OAuth links may include temp-token auth.
func (p *OAuth2Provider) mcpTempTokenAuthEnabled(ctx context.Context) bool {
	if p.configStore == nil {
		return false
	}
	clientConfig, err := p.configStore.GetClientConfig(ctx)
	if err != nil {
		logger.Warn("Failed to read MCP temp-token auth setting: %v", err)
		return false
	}
	return clientConfig != nil && clientConfig.MCPEnableTempTokenAuth
}

// cleanupFlow deletes the flow row and any temp tokens minted for it. Called
// on every terminal transition (success or any failure) so the auth-page
// link stops working as soon as the work it authorized ends.
//
// Detached from the caller's context via WithoutCancel so a client cancellation
// (e.g. the browser closing the tab after the upstream OAuth bounce) can't
// short-circuit the deletes and leave the rows alive until the sweep. Mirrors
// the pattern used by markExpiredIfPermanent in this file.
func (p *OAuth2Provider) cleanupFlow(ctx context.Context, sessionID string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := p.configStore.DeleteOauthUserSession(cleanupCtx, sessionID); err != nil {
		logger.Warn("per-user OAuth flow row cleanup failed: session_id=%s err=%v", sessionID, err)
	}
	if svc := p.tempTokenService(); svc != nil {
		if _, err := svc.DeleteByResourceID(cleanupCtx, temptoken.MCPAuthScopeName, sessionID); err != nil {
			logger.Warn("per-user OAuth temp-token cleanup failed: session_id=%s err=%v", sessionID, err)
		}
	}
}

// GetAccessToken retrieves the access token for a given oauth_config_id
func (p *OAuth2Provider) GetAccessToken(ctx context.Context, oauthConfigID string) (string, error) {
	// Load oauth_config by ID
	oauthConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil {
		return "", fmt.Errorf("failed to load oauth config: %w", err)
	}
	if oauthConfig == nil {
		return "", schemas.ErrOAuth2ConfigNotFound
	}

	// Check if OAuth is authorized
	if oauthConfig.Status != "authorized" {
		return "", fmt.Errorf("oauth not authorized yet, status: %s", oauthConfig.Status)
	}

	// Check if token is linked
	if oauthConfig.TokenID == nil {
		return "", fmt.Errorf("no token linked to oauth config")
	}

	// Load oauth_token by TokenID
	token, err := p.configStore.GetOauthTokenByID(ctx, *oauthConfig.TokenID)
	if err != nil {
		return "", fmt.Errorf("failed to load oauth token: %w", err)
	}
	if token == nil {
		return "", fmt.Errorf("oauth token not found")
	}

	// Refresh only when the token has known expiry and a refresh token is available.
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) && strings.TrimSpace(token.RefreshToken) != "" {
		// Attempt automatic refresh
		if err := p.RefreshAccessToken(ctx, oauthConfigID); err != nil {
			p.markExpiredIfPermanent(ctx, oauthConfig, err)
			return "", fmt.Errorf("token expired and refresh failed: %w", err)
		}
		// Reload token after refresh
		token, err = p.configStore.GetOauthTokenByID(ctx, *oauthConfig.TokenID)
		if err != nil || token == nil {
			return "", fmt.Errorf("failed to reload token after refresh: %w", err)
		}
	}
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return "", fmt.Errorf("token expired and no refresh token is available; re-authorization required: %w", schemas.ErrOAuth2TokenExpired)
	}

	// Sanitize and return access token (trim whitespace/newlines that may cause header formatting issues)
	accessToken := strings.TrimSpace(token.AccessToken)
	if accessToken == "" {
		return "", fmt.Errorf("access token is empty after sanitization")
	}
	return accessToken, nil
}

// RefreshAccessToken refreshes the access token for a given oauth_config_id
func (p *OAuth2Provider) RefreshAccessToken(ctx context.Context, oauthConfigID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Load oauth_config
	oauthConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil || oauthConfig == nil {
		return fmt.Errorf("oauth config not found: %w", err)
	}

	if oauthConfig.TokenID == nil {
		return fmt.Errorf("no token linked to oauth config")
	}

	// Load oauth_token
	token, err := p.configStore.GetOauthTokenByID(ctx, *oauthConfig.TokenID)
	if err != nil || token == nil {
		return fmt.Errorf("oauth token not found: %w", err)
	}

	if strings.TrimSpace(token.RefreshToken) == "" {
		return fmt.Errorf("no refresh token available")
	}

	// Call OAuth provider's token endpoint with refresh_token
	newTokenResponse, err := p.exchangeRefreshToken(
		ctx,
		oauthConfig.TokenURL,
		oauthConfig.GetResolvedClientID(),
		oauthConfig.GetResolvedClientSecret(),
		token.RefreshToken,
	)
	if err != nil {
		return fmt.Errorf("token refresh failed: %w", err)
	}
	// Update token in database (sanitize tokens to prevent header formatting issues)
	now := time.Now()
	token.ExpiresAt = nil
	if newTokenResponse.ExpiresIn > 0 {
		exp := now.Add(time.Duration(newTokenResponse.ExpiresIn) * time.Second)
		token.ExpiresAt = bifrost.Ptr(exp)
	}
	token.AccessToken = strings.TrimSpace(newTokenResponse.AccessToken)
	if newTokenResponse.RefreshToken != "" {
		token.RefreshToken = strings.TrimSpace(newTokenResponse.RefreshToken)
	}
	token.LastRefreshedAt = bifrost.Ptr(now)

	if err := p.configStore.UpdateOauthToken(ctx, token); err != nil {
		return fmt.Errorf("failed to update token: %w", err)
	}

	logger.Debug("OAuth token refreshed successfully oauth_config_id : %s", oauthConfigID)

	return nil
}

// ValidateToken checks if the token is still valid
func (p *OAuth2Provider) ValidateToken(ctx context.Context, oauthConfigID string) (bool, error) {
	oauthConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil || oauthConfig == nil {
		return false, nil
	}

	if oauthConfig.TokenID == nil {
		return false, nil
	}

	token, err := p.configStore.GetOauthTokenByID(ctx, *oauthConfig.TokenID)
	if err != nil || token == nil {
		return false, nil
	}

	// Tokens with unknown/non-expiring semantics are considered valid until upstream rejection.
	if token.ExpiresAt == nil {
		return true, nil
	}
	return time.Now().Before(*token.ExpiresAt), nil
}

// RevokeToken revokes the OAuth token
func (p *OAuth2Provider) RevokeToken(ctx context.Context, oauthConfigID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	oauthConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil || oauthConfig == nil {
		return fmt.Errorf("oauth config not found: %w", err)
	}

	if oauthConfig.TokenID == nil {
		return fmt.Errorf("no token linked to oauth config")
	}

	token, err := p.configStore.GetOauthTokenByID(ctx, *oauthConfig.TokenID)
	if err != nil || token == nil {
		return fmt.Errorf("oauth token not found: %w", err)
	}

	// Optionally call provider's revocation endpoint (if supported)
	// This is best-effort - we'll delete the token even if revocation fails

	// Delete token from database
	if err := p.configStore.DeleteOauthToken(ctx, token.ID); err != nil {
		return fmt.Errorf("failed to delete token: %w", err)
	}

	// Update oauth_config to remove token reference and mark as revoked
	oauthConfig.TokenID = nil
	oauthConfig.Status = "revoked"
	if err := p.configStore.UpdateOauthConfig(ctx, oauthConfig); err != nil {
		return fmt.Errorf("failed to update oauth config: %w", err)
	}

	logger.Debug("OAuth token revoked", "oauth_config_id", oauthConfigID)

	return nil
}

// StorePendingMCPClient stores an MCP client config that's waiting for OAuth completion
// The config is persisted in the database (oauth_configs.mcp_client_config_json) to support
// multi-instance deployments where OAuth callback may hit a different server instance.
func (p *OAuth2Provider) StorePendingMCPClient(oauthConfigID string, mcpClientConfig schemas.MCPClientConfig) error {
	ctx := context.Background()

	oauthConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil {
		return fmt.Errorf("failed to get oauth config: %w", err)
	}
	if oauthConfig == nil {
		return fmt.Errorf("oauth config not found: %s", oauthConfigID)
	}

	configJSON, err := json.Marshal(mcpClientConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal MCP client config: %w", err)
	}
	configStr := string(configJSON)
	oauthConfig.MCPClientConfigJSON = &configStr

	if err := p.configStore.UpdateOauthConfig(ctx, oauthConfig); err != nil {
		return fmt.Errorf("failed to update oauth config with MCP client config: %w", err)
	}

	logger.Debug("Stored pending MCP client config", "oauth_config_id", oauthConfigID)
	return nil
}

// GetPendingMCPClient retrieves an MCP client config by oauth_config_id
// Returns nil if no pending config is found or if the oauth config has expired
func (p *OAuth2Provider) GetPendingMCPClient(oauthConfigID string) (*schemas.MCPClientConfig, error) {
	ctx := context.Background()

	oauthConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil {
		return nil, fmt.Errorf("failed to get oauth config: %w", err)
	}
	if oauthConfig == nil {
		return nil, nil
	}

	// Check if expired
	if time.Now().After(oauthConfig.ExpiresAt) {
		return nil, nil
	}

	if oauthConfig.MCPClientConfigJSON == nil || *oauthConfig.MCPClientConfigJSON == "" {
		return nil, nil
	}

	var config schemas.MCPClientConfig
	if err := json.Unmarshal([]byte(*oauthConfig.MCPClientConfigJSON), &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal MCP client config: %w", err)
	}

	return &config, nil
}

// GetPendingMCPClientByState retrieves an MCP client config by OAuth state token
// This is useful when the callback only has the state parameter
func (p *OAuth2Provider) GetPendingMCPClientByState(state string) (*schemas.MCPClientConfig, string, error) {
	ctx := context.Background()

	oauthConfig, err := p.configStore.GetOauthConfigByState(ctx, state)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get oauth config by state: %w", err)
	}
	if oauthConfig == nil {
		return nil, "", nil
	}

	// Check if expired
	if time.Now().After(oauthConfig.ExpiresAt) {
		return nil, "", nil
	}

	if oauthConfig.MCPClientConfigJSON == nil || *oauthConfig.MCPClientConfigJSON == "" {
		return nil, oauthConfig.ID, nil
	}

	var config schemas.MCPClientConfig
	if err := json.Unmarshal([]byte(*oauthConfig.MCPClientConfigJSON), &config); err != nil {
		return nil, oauthConfig.ID, fmt.Errorf("failed to unmarshal MCP client config: %w", err)
	}

	return &config, oauthConfig.ID, nil
}

// RemovePendingMCPClient clears the pending MCP client config from the oauth config
// This is called after OAuth completion to clean up
func (p *OAuth2Provider) RemovePendingMCPClient(oauthConfigID string) error {
	ctx := context.Background()

	oauthConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil {
		return fmt.Errorf("failed to get oauth config: %w", err)
	}
	if oauthConfig == nil {
		return nil // Already removed or doesn't exist
	}

	oauthConfig.MCPClientConfigJSON = nil

	if err := p.configStore.UpdateOauthConfig(ctx, oauthConfig); err != nil {
		return fmt.Errorf("failed to clear pending MCP client config: %w", err)
	}

	logger.Debug("Removed pending MCP client config", "oauth_config_id", oauthConfigID)
	return nil
}

// InitiateOAuthFlow creates an OAuth config and returns the authorization URL
// Supports OAuth discovery and PKCE
func (p *OAuth2Provider) InitiateOAuthFlow(ctx context.Context, config *schemas.OAuth2Config) (*schemas.OAuth2FlowInitiation, error) {
	// Generate state token for CSRF protection
	state, err := generateSecureRandomString(32)
	if err != nil {
		return nil, fmt.Errorf("failed to generate state token: %w", err)
	}

	// Create oauth config ID
	oauthConfigID := uuid.New().String()

	// Determine OAuth endpoints (discovery or provided)
	authorizeURL := config.AuthorizeURL
	tokenURL := config.TokenURL
	registrationURL := config.RegistrationURL // Accept user-provided registration URL
	scopes := config.Scopes

	// Perform OAuth discovery ONLY if required URLs are missing
	// This allows users to:
	// 1. Provide all URLs manually (no discovery)
	// 2. Provide some URLs manually (partial discovery for missing ones)
	// 3. Provide no URLs (full discovery from server_url)
	needsDiscovery := (authorizeURL == "" || tokenURL == "")

	if needsDiscovery {
		if config.ServerURL == "" {
			return nil, fmt.Errorf("server_url is required for OAuth discovery when authorize_url or token_url is not provided")
		}

		logger.Debug("Performing OAuth discovery for missing endpoints", "server_url", config.ServerURL)

		metadata, err := DiscoverOAuthMetadata(ctx, config.ServerURL)
		if err != nil {
			return nil, fmt.Errorf("OAuth discovery failed: %w. Please provide authorize_url, token_url, and registration_url manually", err)
		}

		// Use discovered values only for missing fields (prefer user-provided values)
		if authorizeURL == "" {
			authorizeURL = metadata.AuthorizationURL
			if authorizeURL == "" {
				return nil, fmt.Errorf("authorize_url could not be discovered. Please provide it manually")
			}
			logger.Debug("Discovered authorize_url", "url", authorizeURL)
		}
		if tokenURL == "" {
			tokenURL = metadata.TokenURL
			if tokenURL == "" {
				return nil, fmt.Errorf("token_url could not be discovered. Please provide it manually")
			}
			logger.Debug("Discovered token_url", "url", tokenURL)
		}
		if registrationURL == nil && metadata.RegistrationURL != nil {
			registrationURL = metadata.RegistrationURL
			logger.Debug("Discovered registration_url", "url", *registrationURL)
		}
		// Merge scopes: use discovered scopes if user didn't provide any
		if len(scopes) == 0 && len(metadata.ScopesSupported) > 0 {
			scopes = metadata.ScopesSupported
			logger.Debug("Discovered scopes", "scopes", scopes)
		}

		logger.Debug("OAuth discovery completed successfully")
	}

	// Validate required fields after discovery
	if authorizeURL == "" {
		return nil, fmt.Errorf("authorize_url is required (provide manually or ensure server supports OAuth discovery)")
	}
	if tokenURL == "" {
		return nil, fmt.Errorf("token_url is required (provide manually or ensure server supports OAuth discovery)")
	}

	// Dynamic Client Registration (RFC 7591)
	// If client_id is NOT provided, attempt dynamic registration
	clientID := config.ClientID // storage value — may be "env.MY_VAR" reference or plain ID
	clientSecret := config.ClientSecret

	if clientID == "" {
		// Check if registration URL is available
		if registrationURL == nil || *registrationURL == "" {
			return nil, fmt.Errorf("client_id is required when the OAuth provider does not support dynamic client registration (RFC 7591). Please provide client_id manually or use an OAuth provider that supports dynamic registration")
		}

		logger.Debug("client_id not provided, attempting dynamic client registration (RFC 7591)")

		// Prepare registration request
		regReq := &DynamicClientRegistrationRequest{
			ClientName:              "Bifrost MCP Gateway",
			RedirectURIs:            []string{config.RedirectURI},
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			TokenEndpointAuthMethod: "none", // Public client with PKCE (no client secret needed)
		}

		// Add scopes if available
		if len(scopes) > 0 {
			regReq.Scope = strings.Join(scopes, " ")
		}

		// Perform dynamic registration
		regResp, err := RegisterDynamicClient(ctx, *registrationURL, regReq)
		if err != nil {
			return nil, fmt.Errorf("dynamic client registration failed: %w. Please provide client_id manually", err)
		}

		// Use dynamically registered credentials
		clientID = regResp.ClientID
		clientSecret = regResp.ClientSecret // May be empty for public clients

		logger.Debug("Dynamic client registration successful: client_id: %s, has_secret: %t", clientID, clientSecret != "")
	}

	// Generate PKCE challenge
	codeVerifier, codeChallenge, err := GeneratePKCEChallenge()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE challenge: %w", err)
	}

	// Serialize scopes
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize scopes: %w", err)
	}

	// Create oauth_config record (using dynamically registered or user-provided client_id)
	expiresAt := time.Now().Add(15 * time.Minute)
	oauthConfigRecord := &tables.TableOauthConfig{
		ID:              oauthConfigID,
		ClientID:        schemas.NewSecretVar(clientID), // May be from dynamic registration
		ClientSecret:    schemas.NewSecretVar(clientSecret),
		AuthorizeURL:    authorizeURL,
		TokenURL:        tokenURL,
		RegistrationURL: registrationURL,
		RedirectURI:     config.RedirectURI,
		Scopes:          string(scopesJSON),
		State:           state,
		CodeVerifier:    codeVerifier,
		CodeChallenge:   codeChallenge,
		Status:          "pending",
		ServerURL:       config.ServerURL,
		UseDiscovery:    config.UseDiscovery,
		ExpiresAt:       expiresAt,
	}

	if err := p.configStore.CreateOauthConfig(ctx, oauthConfigRecord); err != nil {
		return nil, fmt.Errorf("failed to create oauth config: %w", err)
	}

	// Resolve env var reference to actual value for use in the authorize URL.
	// The reference ("env.MY_VAR") is stored in DB; the resolved value is sent to the provider.
	resolvedClientID := schemas.NewSecretVar(clientID).GetValue()

	// Build authorize URL with PKCE (using dynamically registered or user-provided client_id)
	authURL := p.buildAuthorizeURLWithPKCE(
		authorizeURL,
		resolvedClientID,
		config.RedirectURI,
		state,
		codeChallenge,
		scopes,
	)

	logger.Debug("OAuth flow initiated successfully: oauth_config_id: %s, client_id: %s", oauthConfigID, resolvedClientID)

	return &schemas.OAuth2FlowInitiation{
		OauthConfigID: oauthConfigID,
		AuthorizeURL:  authURL,
		State:         state,
		ExpiresAt:     expiresAt,
	}, nil
}

// CompleteOAuthFlow handles the OAuth callback and exchanges code for tokens
// Supports PKCE verification
func (p *OAuth2Provider) CompleteOAuthFlow(ctx context.Context, state, code string) error {
	// Lookup oauth_config by state
	oauthConfig, err := p.configStore.GetOauthConfigByState(ctx, state)
	if err != nil {
		return fmt.Errorf("failed to lookup oauth config: %w", err)
	}
	if oauthConfig == nil {
		return fmt.Errorf("invalid state token")
	}

	// Check expiry
	if time.Now().After(oauthConfig.ExpiresAt) {
		oauthConfig.Status = "expired"
		p.configStore.UpdateOauthConfig(ctx, oauthConfig)
		return fmt.Errorf("oauth flow expired")
	}

	// Log token exchange attempt for debugging
	logger.Debug("Attempting token exchange",
		"token_url", oauthConfig.TokenURL,
		"client_id", oauthConfig.GetResolvedClientID(),
		"has_client_secret", oauthConfig.GetResolvedClientSecret() != "",
		"has_pkce_verifier", oauthConfig.CodeVerifier != "")

	// Exchange code for tokens with PKCE verifier
	tokenResponse, err := p.exchangeCodeForTokensWithPKCE(
		ctx,
		oauthConfig.TokenURL,
		code,
		oauthConfig.GetResolvedClientID(),
		oauthConfig.GetResolvedClientSecret(),
		oauthConfig.RedirectURI,
		oauthConfig.CodeVerifier, // PKCE verifier
	)
	if err != nil {
		oauthConfig.Status = "failed"
		p.configStore.UpdateOauthConfig(ctx, oauthConfig)
		logger.Error("Token exchange failed",
			"error", err.Error(),
			"client_id", oauthConfig.GetResolvedClientID(),
			"token_url", oauthConfig.TokenURL)
		return fmt.Errorf("token exchange failed: %w", err)
	}

	// Parse scopes
	var scopes []string
	if tokenResponse.Scope != "" {
		scopes = strings.Split(tokenResponse.Scope, " ")
	}
	scopesJSON, _ := json.Marshal(scopes)

	// Create oauth_token record (sanitize tokens to prevent header formatting issues)
	tokenID := uuid.New().String()
	var expiresAt *time.Time
	if tokenResponse.ExpiresIn > 0 {
		exp := time.Now().Add(time.Duration(tokenResponse.ExpiresIn) * time.Second)
		expiresAt = bifrost.Ptr(exp)
	}
	tokenRecord := &tables.TableOauthToken{
		ID:           tokenID,
		AccessToken:  strings.TrimSpace(tokenResponse.AccessToken),
		RefreshToken: strings.TrimSpace(tokenResponse.RefreshToken),
		TokenType:    tokenResponse.TokenType,
		ExpiresAt:    expiresAt,
		Scopes:       string(scopesJSON),
	}

	if err := p.configStore.CreateOauthToken(ctx, tokenRecord); err != nil {
		return fmt.Errorf("failed to create oauth token: %w", err)
	}

	// Update oauth_config: link token and set status="authorized"
	oauthConfig.TokenID = &tokenID
	oauthConfig.Status = "authorized"
	if err := p.configStore.UpdateOauthConfig(ctx, oauthConfig); err != nil {
		return fmt.Errorf("failed to update oauth config: %w", err)
	}

	logger.Debug("OAuth flow completed successfully", "oauth_config_id", oauthConfig.ID)

	return nil
}

// BuildUpstreamAuthorizeURL reconstructs the upstream provider authorization
// URL for a pending per-user OAuth flow. Called by the frontend sessions tab
// when the user clicks "Authenticate" — at which point the flow row already
// exists (from a prior InitiateUserOAuthFlow), the CSRF state + PKCE verifier
// are stored on it, and we just need to hand the user the upstream redirect.
//
// The code_challenge is recomputed deterministically from the stored verifier
// so we don't have to persist it separately.
func (p *OAuth2Provider) BuildUpstreamAuthorizeURL(ctx context.Context, flowID string) (string, error) {
	flow, err := p.configStore.GetOauthUserSessionByID(ctx, flowID)
	if err != nil {
		return "", fmt.Errorf("failed to load pending oauth flow: %w", err)
	}
	if flow == nil {
		return "", schemas.ErrOAuth2NotPerUserSession
	}
	if flow.Status != "pending" {
		return "", fmt.Errorf("oauth flow %s is %s: %w", flowID, flow.Status, schemas.ErrOAuth2FlowNotPending)
	}
	if time.Now().After(flow.ExpiresAt) {
		return "", fmt.Errorf("oauth flow %s: %w", flowID, schemas.ErrOAuth2FlowExpired)
	}
	templateConfig, err := p.configStore.GetOauthConfigByID(ctx, flow.OauthConfigID)
	if err != nil {
		return "", fmt.Errorf("failed to load template oauth config: %w", err)
	}
	if templateConfig == nil {
		return "", schemas.ErrOAuth2ConfigNotFound
	}
	// Recompute the PKCE challenge from the stored verifier (deterministic).
	hash := sha256.Sum256([]byte(flow.CodeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(hash[:])

	var scopes []string
	if templateConfig.Scopes != "" {
		if err := json.Unmarshal([]byte(templateConfig.Scopes), &scopes); err != nil {
			return "", fmt.Errorf("failed to parse oauth scopes for flow %s: %w", flowID, err)
		}
	}
	redirectURI := flow.RedirectURI
	if redirectURI == "" {
		redirectURI = templateConfig.RedirectURI
	}
	return p.buildAuthorizeURLWithPKCE(
		templateConfig.AuthorizeURL,
		templateConfig.GetResolvedClientID(),
		redirectURI,
		flow.State,
		codeChallenge,
		scopes,
	), nil
}

// buildAuthorizeURLWithPKCE constructs the OAuth authorization URL with PKCE
// parameters, preserving any query params already present on the stored
// authorizeURL. Naive "authorizeURL + ?" + params.Encode() concatenation
// would produce malformed URLs like "...?existing=1?response_type=code" when
// the provider's stored URL is already parameterized.
func (p *OAuth2Provider) buildAuthorizeURLWithPKCE(authorizeURL, clientID, redirectURI, state, codeChallenge string, scopes []string) string {
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		// Fall back to the naive form rather than dropping the auth URL —
		// callers expect a non-empty return. The OAuth provider will reject
		// a malformed URL, which is the same end state as not redirecting.
		params := url.Values{}
		params.Set("response_type", "code")
		params.Set("client_id", clientID)
		params.Set("redirect_uri", redirectURI)
		params.Set("state", state)
		params.Set("code_challenge", codeChallenge)
		params.Set("code_challenge_method", "S256")
		if len(scopes) > 0 {
			params.Set("scope", strings.Join(scopes, " "))
		}
		return authorizeURL + "?" + params.Encode()
	}
	q := parsed.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256") // SHA-256 hashing
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

// exchangeCodeForTokensWithPKCE exchanges authorization code for access/refresh tokens with PKCE verifier
func (p *OAuth2Provider) exchangeCodeForTokensWithPKCE(ctx context.Context, tokenURL, code, clientID, clientSecret, redirectURI, codeVerifier string) (*schemas.OAuth2TokenExchangeResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("client_id", clientID)
	data.Set("code_verifier", codeVerifier) // PKCE verifier

	// Only include client_secret if provided (optional for public clients with PKCE)
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}

	return p.callTokenEndpoint(ctx, tokenURL, data)
}

// markExpiredIfPermanent marks oauth_config.status as "expired" when a refresh failure
// is a permanent auth rejection (PermanentOAuthError). Transient failures are ignored —
// the TokenRefreshWorker will retry on the next tick.
func (p *OAuth2Provider) markExpiredIfPermanent(ctx context.Context, oauthConfig *tables.TableOauthConfig, err error) {
	var permErr *PermanentOAuthError
	if errors.As(err, &permErr) {
		oauthConfig.Status = "expired"
		updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if updateErr := p.configStore.UpdateOauthConfig(updateCtx, oauthConfig); updateErr != nil {
			logger.Error("Failed to update oauth config status: %s, error: %s", oauthConfig.ID, updateErr.Error())
		}
	}
}

// exchangeRefreshToken exchanges refresh token for new access token
func (p *OAuth2Provider) exchangeRefreshToken(ctx context.Context, tokenURL, clientID, clientSecret, refreshToken string) (*schemas.OAuth2TokenExchangeResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)

	return p.callTokenEndpoint(ctx, tokenURL, data)
}

// PermanentOAuthError indicates the OAuth provider rejected the request in a way
// that requires user re-authorization (e.g. revoked refresh token, invalid_grant).
// Distinct from transient network failures which should be retried.
type PermanentOAuthError struct {
	StatusCode int
	Body       string
}

func (e *PermanentOAuthError) Error() string {
	return fmt.Sprintf("permanent oauth error (status %d): %s", e.StatusCode, e.Body)
}

// sleepIfNotLastAttempt waits with exponential backoff between retry attempts.
// No-ops on the final attempt to avoid sleeping before returning an error.
// Respects context cancellation so worker shutdown is not delayed.
func sleepIfNotLastAttempt(ctx context.Context, attempt int, baseDelay time.Duration) {
	if attempt < maxTokenRetries-1 {
		select {
		case <-time.After(time.Duration(1<<attempt) * baseDelay):
		case <-ctx.Done():
		}
	}
}

// callTokenEndpoint makes a POST request to the OAuth token endpoint with retry logic.
// Transport errors and 5xx responses are retried up to maxTokenRetries times with
// exponential backoff. HTTP 4xx responses are returned immediately as PermanentOAuthError.
func (p *OAuth2Provider) callTokenEndpoint(ctx context.Context, tokenURL string, data url.Values) (*schemas.OAuth2TokenExchangeResponse, error) {
	client := &http.Client{Timeout: networkTimeout}
	var lastErr error

	for attempt := range maxTokenRetries {
		req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(data.Encode()))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			// Propagate context cancellation immediately — no point retrying.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// Transport error (DNS failure, timeout, connection refused) — retry
			lastErr = fmt.Errorf("token request failed: %w", err)
			sleepIfNotLastAttempt(ctx, attempt, p.retryBaseDelay)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response: %w", err)
			sleepIfNotLastAttempt(ctx, attempt, p.retryBaseDelay)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			if resp.StatusCode == http.StatusUnauthorized {
				return nil, &PermanentOAuthError{StatusCode: resp.StatusCode, Body: string(body)}
			}
			// Per RFC 6749 §5.2, only invalid_grant and unauthorized_client within a 400
			// require user re-authorization. Other 400s (invalid_request, unsupported_grant_type,
			// etc.) are configuration or request errors — fail fast without expiring the config.
			if resp.StatusCode == http.StatusBadRequest {
				var oauthErr struct {
					Error string `json:"error"`
				}
				if json.Unmarshal(body, &oauthErr) == nil {
					if oauthErr.Error == "invalid_grant" || oauthErr.Error == "unauthorized_client" {
						return nil, &PermanentOAuthError{StatusCode: resp.StatusCode, Body: string(body)}
					}
				}
				return nil, fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(body))
			}
			// Transient error (rate limit, server error, etc.) — retry
			lastErr = fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(body))
			sleepIfNotLastAttempt(ctx, attempt, p.retryBaseDelay)
			continue
		}

		// Try to parse as JSON first
		var tokenResponse schemas.OAuth2TokenExchangeResponse
		if err := json.Unmarshal(body, &tokenResponse); err != nil {
			// Fall back to URL-encoded form data (GitHub's OAuth endpoint returns this format)
			formValues, parseErr := url.ParseQuery(string(body))
			if parseErr != nil {
				return nil, fmt.Errorf("failed to parse token response as JSON or form data: JSON error: %w, form error: %v", err, parseErr)
			}
			tokenResponse.AccessToken = formValues.Get("access_token")
			tokenResponse.RefreshToken = formValues.Get("refresh_token")
			tokenResponse.TokenType = formValues.Get("token_type")
			tokenResponse.Scope = formValues.Get("scope")
			if expiresIn := formValues.Get("expires_in"); expiresIn != "" {
				fmt.Sscanf(expiresIn, "%d", &tokenResponse.ExpiresIn)
			}
		}

		if tokenResponse.AccessToken == "" {
			return nil, fmt.Errorf("token response missing access_token, body: %s", string(body))
		}

		return &tokenResponse, nil
	}

	return nil, fmt.Errorf("token request failed after %d attempts: %w", maxTokenRetries, lastErr)
}

// generateSecureRandomString generates a cryptographically secure random string
func generateSecureRandomString(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes)[:length], nil
}

// ---------- Per-User OAuth Methods ----------

// InitiateUserOAuthFlow creates or refreshes the per-user OAuth flow row for a
// given (mode, identity, mcp_client) binding and returns the auth landing URL.
//
// Determinism: there is exactly one flow row per binding. If one already exists
// (from a prior auth attempt), it's updated in place — fresh CSRF state, fresh
// PKCE verifier, status reset to 'pending'. Otherwise a new row is inserted.
// Reauth never duplicates rows; revoke deletes them.
//
// The function errors out cleanly on any misconfig (missing identity, unknown
// mode, missing template config) — no fallbacks, no generated identities.
func (p *OAuth2Provider) InitiateUserOAuthFlow(ctx context.Context, oauthConfigID string, mcpClientID string, redirectURI string, flowMode schemas.MCPAuthMode) (*schemas.OAuth2FlowInitiation, string, error) {
	// 1. Load template OAuth config.
	templateConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to load template oauth config: %w", err)
	}
	if templateConfig == nil {
		return nil, "", schemas.ErrOAuth2ConfigNotFound
	}

	// 2. Resolve identity from context for the given mode. Required (no
	//    fallbacks). Each mode populates exactly one of (vkId, uid, sessionID).
	var (
		vkId, uid           *string
		sessionID, lookupID string
	)
	switch flowMode {
	case schemas.MCPAuthModeUser:
		v, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
		if v == "" {
			return nil, "", fmt.Errorf("user-mode flow requires a user identity in context")
		}
		uid = &v
		lookupID = v
	case schemas.MCPAuthModeVK:
		v, _ := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string)
		if v == "" {
			return nil, "", fmt.Errorf("vk-mode flow requires a resolved virtual key in context")
		}
		vkId = &v
		lookupID = v
	case schemas.MCPAuthModeSession:
		v, _ := ctx.Value(schemas.BifrostContextKeyMCPSessionID).(string)
		if v == "" {
			return nil, "", fmt.Errorf("session-mode flow requires x-bf-mcp-session-id in context")
		}
		sessionID = v
		lookupID = v
	default:
		return nil, "", fmt.Errorf("unknown auth mode for flow: %s", flowMode)
	}

	// 3. Generate fresh CSRF state + PKCE verifier for this attempt.
	state, err := generateSecureRandomString(32)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate state token: %w", err)
	}
	codeVerifier, _, err := GeneratePKCEChallenge()
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate PKCE challenge: %w", err)
	}
	expiresAt := time.Now().Add(15 * time.Minute)

	// 4. Single canonical lookup: one flow row per (mode, identity, mcp_client).
	//
	//    Only treat a 'pending' hit as reusable. A 'claiming' row means an
	//    upstream callback is mid-flight for this binding — rotating its
	//    state/PKCE here would invalidate that callback's state token and
	//    leave the user with an "OAuth flow not found" error. Falling through
	//    to insert a fresh row lets both flows complete independently.
	var existing *tables.TableOauthUserSession
	found, lookupErr := p.configStore.GetOauthUserSessionByModeIdentityAndMCPClient(ctx, flowMode, lookupID, mcpClientID)
	if lookupErr != nil {
		return nil, "", fmt.Errorf("failed to look up existing flow row: %w", lookupErr)
	}
	if found != nil && found.Status == "pending" {
		existing = found
	}

	var rowID string
	if existing != nil {
		// Reauth path: update the same row in place. Identity columns already
		// match (we looked them up); only rotate the OAuth-dance fields.
		existing.OauthConfigID = oauthConfigID
		existing.State = state
		existing.RedirectURI = redirectURI
		existing.CodeVerifier = codeVerifier
		existing.Status = "pending"
		existing.ExpiresAt = expiresAt
		if err := p.configStore.UpdateOauthUserSession(ctx, existing); err != nil {
			return nil, "", fmt.Errorf("failed to update flow row for reauth: %w", err)
		}
		rowID = existing.ID
	} else {
		// First-time auth: insert a new row. SessionID is populated only for
		// session-mode (the caller's x-bf-mcp-session-id); vk/user-mode rows
		// have an empty SessionID — their identity lives in virtual_key_id /
		// user_id.
		row := &tables.TableOauthUserSession{
			ID:            uuid.New().String(),
			MCPClientID:   mcpClientID,
			OauthConfigID: oauthConfigID,
			State:         state,
			RedirectURI:   redirectURI,
			CodeVerifier:  codeVerifier,
			SessionID:     sessionID,
			VirtualKeyID:  vkId,
			UserID:        uid,
			FlowMode:      string(flowMode),
			Status:        "pending",
			ExpiresAt:     expiresAt,
		}
		if err := p.configStore.CreateOauthUserSession(ctx, row); err != nil {
			return nil, "", fmt.Errorf("failed to create flow row: %w", err)
		}
		rowID = row.ID
	}
	sessionID = rowID

	// Frontend URL the user (or their teammate) opens in a browser. The
	// upstream provider URL is reconstructed on demand by the /flows/:id/start
	// endpoint using the stored CSRF state + PKCE verifier — we don't pin it
	// here so the row stays the single source of truth for those fields.
	//
	// Derive the Bifrost base URL from the OAuth callback redirect URI passed
	// in by the caller — it always has the shape "{base}/api/oauth/callback".
	// Flow ID rides as a query param to match the flat-route convention used
	// elsewhere in the dashboard UI.
	frontendURL := strings.TrimSuffix(redirectURI, "/api/oauth/callback") + "/workspace/mcp-sessions/auth?flow=" + sessionID

	// Mint a mcp_auth temp token bound to this flow's row ID and embed it in
	// the URL as a fragment so a browser hitting the auth page without a
	// dashboard session can still call the per-user flow endpoints. The
	// fragment never leaves the browser (not in server logs, not in the
	// upstream-OAuth Referer), unlike a query param.
	//
	// User-mode flows skip the mint: the handler-side identity gate requires
	// caller's user_id to match flow.UserID, which is only populated by normal
	// SCIM enforcement on the auth-page route. Minting a temp token would
	// route the request through the temp-token middleware branch that
	// bypasses cookie resolution, leaving caller user_id empty and the gate
	// would 403 even legitimate users. VK and session-mode flows are
	// intentionally shareable and continue to mint.
	if svc := p.tempTokenService(); svc != nil && p.mcpTempTokenAuthEnabled(ctx) && flowMode != schemas.MCPAuthModeUser {
		ttl := time.Until(expiresAt)
		if ttl > 0 {
			plaintext, mintErr := svc.Mint(ctx, temptoken.MCPAuthScopeName, sessionID, ttl)
			if mintErr != nil {
				logger.Warn("Failed to mint mcp_auth temp token for flow %s: %v (link still usable for dashboard-authenticated callers)", sessionID, mintErr)
			} else {
				frontendURL = frontendURL + "#t=" + plaintext
			}
		}
	}

	logger.Debug("Per-user OAuth flow initiated: session_id=%s, mcp_client_id=%s", sessionID, mcpClientID)

	return &schemas.OAuth2FlowInitiation{
		OauthConfigID: oauthConfigID,
		AuthorizeURL:  frontendURL,
		State:         state,
		ExpiresAt:     expiresAt,
	}, sessionID, nil
}

// CompleteUserOAuthFlow handles the OAuth callback for a per-user flow.
// It looks up the session by state, exchanges code for tokens, and returns a session token.
func (p *OAuth2Provider) CompleteUserOAuthFlow(ctx context.Context, state string, code string) (string, error) {
	// Atomically claim session by state to prevent concurrent callback races
	session, err := p.configStore.ClaimOauthUserSessionByState(ctx, state)
	if err != nil {
		return "", fmt.Errorf("failed to claim per-user oauth session: %w", err)
	}
	if session == nil {
		// State not found or already claimed — not a per-user session
		return "", schemas.ErrOAuth2NotPerUserSession
	}

	// Check expiry. Flow rows are deleted on every terminal transition
	// (authorized / failed / expired) so the table doesn't accumulate
	// dead rows. The UI sees 404 on flow-detail and renders "expired
	// or already completed" — no audit trail to preserve here.
	if time.Now().After(session.ExpiresAt) {
		p.cleanupFlow(ctx, session.ID)
		return "", fmt.Errorf("per-user oauth flow expired")
	}

	// Load template OAuth config for token_url, client_id, etc. Split the
	// nil-config case from a real lookup failure — otherwise wrapping a nil
	// error with %w produces a useless "%!w(<nil>)" string and the real cause
	// (config row missing) is hidden.
	templateConfig, err := p.configStore.GetOauthConfigByID(ctx, session.OauthConfigID)
	if err != nil {
		p.cleanupFlow(ctx, session.ID)
		return "", fmt.Errorf("failed to load template oauth config: %w", err)
	}
	if templateConfig == nil {
		_ = p.configStore.DeleteOauthUserSession(ctx, session.ID)
		return "", schemas.ErrOAuth2ConfigNotFound
	}
	// Exchange code for tokens with PKCE verifier
	// Use the redirect URI stored in the session (same one used in authorize step)
	// to satisfy OAuth spec requirement that redirect_uri must match
	redirectURI := session.RedirectURI
	if redirectURI == "" {
		redirectURI = templateConfig.RedirectURI
	}
	tokenResponse, err := p.exchangeCodeForTokensWithPKCE(
		ctx,
		templateConfig.TokenURL,
		code,
		templateConfig.GetResolvedClientID(),
		templateConfig.GetResolvedClientSecret(),
		redirectURI,
		session.CodeVerifier,
	)
	if err != nil {
		p.cleanupFlow(ctx, session.ID)
		return "", fmt.Errorf("per-user token exchange failed: %w", err)
	}

	// SessionID is carried from the flow row as-is: populated for session-mode
	// flows (the caller's x-bf-mcp-session-id) and empty for vk/user-mode.
	// No fallback generation — identity for vk/user lives in their own columns.
	sessionID := session.SessionID

	// Parse scopes
	var scopes []string
	if tokenResponse.Scope != "" {
		scopes = strings.Split(tokenResponse.Scope, " ")
	}
	scopesJSON, _ := json.Marshal(scopes)

	// Resolve identity columns from the flow row, honoring its flow_mode. Only
	// the column that matches the mode is populated on the token row; the others
	// stay nil to maintain the single-identity invariant.
	flowMode := schemas.MCPAuthMode(session.FlowMode)
	var (
		tokenVKID   *string
		tokenUserID *string
	)
	switch flowMode {
	case schemas.MCPAuthModeUser:
		tokenUserID = session.UserID
		if tokenUserID == nil || *tokenUserID == "" {
			p.cleanupFlow(ctx, session.ID)
			return "", fmt.Errorf("user-mode oauth flow has no user_id at completion")
		}
	case schemas.MCPAuthModeVK:
		tokenVKID = session.VirtualKeyID
		if tokenVKID == nil || *tokenVKID == "" {
			p.cleanupFlow(ctx, session.ID)
			return "", fmt.Errorf("vk-mode oauth flow has no virtual_key_id at completion")
		}
	case schemas.MCPAuthModeSession:
		// Both identity columns left nil; row is keyed by session_id.
	default:
		// Bogus flow_mode on the row — drop it so a fresh flow can start
		// instead of leaving the row stuck in 'claiming' forever.
		_ = p.configStore.DeleteOauthUserSession(ctx, session.ID)
		return "", fmt.Errorf("invalid flow mode on session: %s", session.ID)
	}

	// Create per-user OAuth token record
	var expiresAt *time.Time
	if tokenResponse.ExpiresIn > 0 {
		exp := time.Now().Add(time.Duration(tokenResponse.ExpiresIn) * time.Second)
		expiresAt = bifrost.Ptr(exp)
	}
	tokenRecord := &tables.TableOauthUserToken{
		ID:            uuid.New().String(),
		SessionID:     sessionID,
		VirtualKeyID:  tokenVKID,
		UserID:        tokenUserID,
		MCPClientID:   session.MCPClientID,
		OauthConfigID: session.OauthConfigID,
		AccessToken:   strings.TrimSpace(tokenResponse.AccessToken),
		RefreshToken:  strings.TrimSpace(tokenResponse.RefreshToken),
		TokenType:     tokenResponse.TokenType,
		ExpiresAt:     expiresAt,
		Scopes:        string(scopesJSON),
		AuthMode:      string(flowMode),
		Status:        "active",
	}
	if err := p.configStore.CreateOauthUserToken(ctx, tokenRecord); err != nil {
		// The flow row has already been claimed and the auth code has been
		// exchanged; leaving the row in 'claiming' state would block the
		// next initiation for this binding indefinitely. Drop it so the
		// caller can restart cleanly.
		_ = p.configStore.DeleteOauthUserSession(ctx, session.ID)
		return "", fmt.Errorf("failed to create per-user oauth token: %w", err)
	}

	// Token row is written; the flow row's purpose ends here. cleanupFlow
	// deletes both the flow row (transient PKCE/state carrier — the token
	// row is the durable record now) and the mcp_auth temp token bound to
	// it, so the auth-page link stops working immediately. The UI shows
	// "expired or already completed" on the resulting 404, which is the
	// truthful read.
	p.cleanupFlow(ctx, session.ID)

	logger.Debug("Per-user OAuth flow completed: session_id=%s, mcp_client_id=%s", session.ID, session.MCPClientID)

	return sessionID, nil
}

// GetUserAccessTokenByMode retrieves the upstream access token using exactly
// one identity column determined by mode. No fallback chain. Filters
// status='active' so orphaned rows never satisfy a lookup.
func (p *OAuth2Provider) GetUserAccessTokenByMode(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (string, error) {
	token, err := p.configStore.GetOauthUserTokenByMode(ctx, mode, identity, mcpClientID)
	if err != nil {
		return "", fmt.Errorf("failed to load per-user oauth token (mode=%s): %w", mode, err)
	}
	if token == nil {
		return "", schemas.ErrOAuth2TokenNotFound
	}

	// Refresh only when known-expired and refresh token exists.
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) && strings.TrimSpace(token.RefreshToken) != "" {
		if err := p.RefreshUserAccessToken(ctx, token.ID); err != nil {
			return "", fmt.Errorf("per-user token expired and refresh failed: %w", err)
		}
		token, err = p.configStore.GetOauthUserTokenByMode(ctx, mode, identity, mcpClientID)
		if err != nil || token == nil {
			return "", fmt.Errorf("failed to reload per-user token after refresh")
		}
	}
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return "", fmt.Errorf("per-user token expired and no refresh token is available; re-authorization required: %w", schemas.ErrOAuth2TokenExpired)
	}

	accessToken := strings.TrimSpace(token.AccessToken)
	if accessToken == "" {
		return "", fmt.Errorf("per-user access token is empty after sanitization")
	}
	return accessToken, nil
}

// RefreshUserAccessToken refreshes a per-user OAuth access token, looked up
// by the token row's primary-key ID.
func (p *OAuth2Provider) RefreshUserAccessToken(ctx context.Context, tokenID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	token, err := p.configStore.GetOauthUserTokenByID(ctx, tokenID)
	if err != nil || token == nil {
		return fmt.Errorf("per-user oauth token not found: %w", err)
	}

	if token.RefreshToken == "" {
		return fmt.Errorf("no refresh token available for per-user oauth token")
	}

	// Load template OAuth config for token_url, client_id, etc. Split nil
	// from a real lookup failure so a missing config doesn't surface as a
	// useless "%!w(<nil>)" wrapped error.
	templateConfig, err := p.configStore.GetOauthConfigByID(ctx, token.OauthConfigID)
	if err != nil {
		return fmt.Errorf("failed to load template oauth config for refresh: %w", err)
	}
	if templateConfig == nil {
		return schemas.ErrOAuth2ConfigNotFound
	}

	// Exchange refresh token
	newTokenResponse, err := p.exchangeRefreshToken(
		ctx,
		templateConfig.TokenURL,
		templateConfig.GetResolvedClientID(),
		templateConfig.GetResolvedClientSecret(),
		token.RefreshToken,
	)
	if err != nil {
		// Permanent rejection (HTTP 401, or 400 with invalid_grant /
		// unauthorized_client per RFC 6749 §5.2) means the refresh token is
		// dead — revoked, expired, or bound to a grant the AS no longer
		// honors. Flip the row to 'needs_reauth' and signal re-authentication
		// via the ErrOAuth2TokenExpired sentinel, which ResolvePerUserOAuthToken
		// converts into an inline MCPUserOAuthRequiredError with auth URL.
		// Keeping the row (rather than deleting) preserves binding identity +
		// audit history; the OAuth callback at re-auth completion upserts
		// the same row back to 'active' with fresh access/refresh tokens.
		var permErr *PermanentOAuthError
		if errors.As(err, &permErr) {
			// Use context.Background for the terminal status flip — the
			// caller's ctx may already be canceled (request abort, upstream
			// timeout) by the time we get the 'invalid_grant' / 401, and if
			// the UPDATE fails because of that, the row stays 'active' and
			// every subsequent inference call keeps retrying a dead refresh
			// token instead of surfacing the re-auth requirement.
			if markErr := p.configStore.MarkOauthUserTokenNeedsReauthByID(context.Background(), token.ID); markErr != nil {
				return fmt.Errorf("per-user oauth refresh permanently rejected but status update failed (mcp_client=%s upstream_status=%d): %w",
					token.MCPClientID, permErr.StatusCode, markErr)
			}
			logger.Debug("Per-user OAuth refresh permanently rejected; row marked needs_reauth: mcp_client=%s upstream_status=%d",
				token.MCPClientID, permErr.StatusCode)
			return fmt.Errorf("refresh token rejected by upstream OAuth server, re-authentication required: %w", schemas.ErrOAuth2TokenExpired)
		}
		// Transient failure (5xx, network blip, non-permanent 400) — keep the
		// row so the next call retries refresh.
		return fmt.Errorf("per-user token refresh failed: %w", err)
	}

	// Update token
	now := time.Now()
	token.ExpiresAt = nil
	if newTokenResponse.ExpiresIn > 0 {
		exp := now.Add(time.Duration(newTokenResponse.ExpiresIn) * time.Second)
		token.ExpiresAt = bifrost.Ptr(exp)
	}
	token.AccessToken = strings.TrimSpace(newTokenResponse.AccessToken)
	if newTokenResponse.RefreshToken != "" {
		token.RefreshToken = strings.TrimSpace(newTokenResponse.RefreshToken)
	}
	token.LastRefreshedAt = bifrost.Ptr(now)

	if err := p.configStore.UpdateOauthUserToken(ctx, token); err != nil {
		return fmt.Errorf("failed to update per-user token after refresh: %w", err)
	}

	logger.Debug("Per-user OAuth token refreshed: token_id=%s", token.ID)
	return nil
}
