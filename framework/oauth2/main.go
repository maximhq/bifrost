package oauth2

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// OAuth2Provider implements the schemas.OAuth2Provider interface
// It provides OAuth 2.0 authentication functionality with database persistence
type OAuth2Provider struct {
	configStore configstore.ConfigStore
	mu          sync.RWMutex
}

// NewOAuth2Provider creates a new OAuth provider instance
func NewOAuth2Provider(configStore configstore.ConfigStore, logger schemas.Logger) *OAuth2Provider {
	if logger == nil {
		logger = bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	}
	SetLogger(logger)
	return &OAuth2Provider{
		configStore: configStore,
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

	// Check if token is expired
	if time.Now().After(token.ExpiresAt) {
		// Attempt automatic refresh
		if err := p.RefreshAccessToken(ctx, oauthConfigID); err != nil {
			return "", fmt.Errorf("token expired and refresh failed: %w", err)
		}
		// Reload token after refresh
		token, err = p.configStore.GetOauthTokenByID(ctx, *oauthConfig.TokenID)
		if err != nil || token == nil {
			return "", fmt.Errorf("failed to reload token after refresh: %w", err)
		}
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

	// Call OAuth provider's token endpoint with refresh_token
	newTokenResponse, err := p.exchangeRefreshToken(
		oauthConfig.TokenURL,
		oauthConfig.ClientID,
		oauthConfig.ClientSecret,
		token.RefreshToken,
	)
	if err != nil {
		return fmt.Errorf("token refresh failed: %w", err)
	}

	// Update token in database (sanitize tokens to prevent header formatting issues)
	now := time.Now()
	token.AccessToken = strings.TrimSpace(newTokenResponse.AccessToken)
	if newTokenResponse.RefreshToken != "" {
		token.RefreshToken = strings.TrimSpace(newTokenResponse.RefreshToken)
	}
	token.ExpiresAt = now.Add(time.Duration(newTokenResponse.ExpiresIn) * time.Second)
	token.LastRefreshedAt = &now

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

	// Simple expiry check
	return time.Now().Before(token.ExpiresAt), nil
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

	logger.Info("OAuth token revoked", "oauth_config_id", oauthConfigID)

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
	clientID := config.ClientID
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
		ClientID:        clientID, // May be from dynamic registration
		ClientSecret:    clientSecret,
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

	// Build authorize URL with PKCE (using dynamically registered or user-provided client_id)
	authURL := p.buildAuthorizeURLWithPKCE(
		authorizeURL,
		clientID, // May be from dynamic registration
		config.RedirectURI,
		state,
		codeChallenge,
		scopes,
	)

	logger.Debug("OAuth flow initiated successfully: oauth_config_id: %s, client_id: %s", oauthConfigID, clientID)

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
		"client_id", oauthConfig.ClientID,
		"has_client_secret", oauthConfig.ClientSecret != "",
		"has_pkce_verifier", oauthConfig.CodeVerifier != "")

	// Exchange code for tokens with PKCE verifier
	tokenResponse, err := p.exchangeCodeForTokensWithPKCE(
		oauthConfig.TokenURL,
		code,
		oauthConfig.ClientID,
		oauthConfig.ClientSecret,
		oauthConfig.RedirectURI,
		oauthConfig.CodeVerifier, // PKCE verifier
	)
	if err != nil {
		oauthConfig.Status = "failed"
		p.configStore.UpdateOauthConfig(ctx, oauthConfig)
		logger.Error("Token exchange failed",
			"error", err.Error(),
			"client_id", oauthConfig.ClientID,
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
	tokenRecord := &tables.TableOauthToken{
		ID:           tokenID,
		AccessToken:  strings.TrimSpace(tokenResponse.AccessToken),
		RefreshToken: strings.TrimSpace(tokenResponse.RefreshToken),
		TokenType:    tokenResponse.TokenType,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResponse.ExpiresIn) * time.Second),
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

// buildAuthorizeURLWithPKCE constructs the OAuth authorization URL with PKCE parameters
func (p *OAuth2Provider) buildAuthorizeURLWithPKCE(authorizeURL, clientID, redirectURI, state, codeChallenge string, scopes []string) string {
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("state", state)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256") // SHA-256 hashing
	if len(scopes) > 0 {
		params.Set("scope", strings.Join(scopes, " "))
	}

	return authorizeURL + "?" + params.Encode()
}

// exchangeCodeForTokens exchanges authorization code for access/refresh tokens
func (p *OAuth2Provider) exchangeCodeForTokens(tokenURL, code, clientID, clientSecret, redirectURI string) (*schemas.OAuth2TokenExchangeResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("client_id", clientID)
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}

	return p.callTokenEndpoint(tokenURL, data)
}

// exchangeCodeForTokensWithPKCE exchanges authorization code for access/refresh tokens with PKCE verifier
func (p *OAuth2Provider) exchangeCodeForTokensWithPKCE(tokenURL, code, clientID, clientSecret, redirectURI, codeVerifier string) (*schemas.OAuth2TokenExchangeResponse, error) {
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

	return p.callTokenEndpoint(tokenURL, data)
}

// exchangeRefreshToken exchanges refresh token for new access token
func (p *OAuth2Provider) exchangeRefreshToken(tokenURL, clientID, clientSecret, refreshToken string) (*schemas.OAuth2TokenExchangeResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)

	return p.callTokenEndpoint(tokenURL, data)
}

// callTokenEndpoint makes a POST request to the OAuth token endpoint
func (p *OAuth2Provider) callTokenEndpoint(tokenURL string, data url.Values) (*schemas.OAuth2TokenExchangeResponse, error) {
	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResponse schemas.OAuth2TokenExchangeResponse

	// Try to parse as JSON first
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		// If JSON parsing fails, try to parse as URL-encoded form data
		// (GitHub's OAuth endpoint may return application/x-www-form-urlencoded)
		formValues, parseErr := url.ParseQuery(string(body))
		if parseErr != nil {
			return nil, fmt.Errorf("failed to parse token response as JSON or form data: JSON error: %w, form error: %v", err, parseErr)
		}

		tokenResponse.AccessToken = formValues.Get("access_token")
		tokenResponse.RefreshToken = formValues.Get("refresh_token")
		tokenResponse.TokenType = formValues.Get("token_type")
		tokenResponse.Scope = formValues.Get("scope")

		// Parse expires_in if present
		if expiresIn := formValues.Get("expires_in"); expiresIn != "" {
			fmt.Sscanf(expiresIn, "%d", &tokenResponse.ExpiresIn)
		}
	}

	// Validate that we got an access token
	if tokenResponse.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token, body: %s", string(body))
	}

	return &tokenResponse, nil
}

// generateSecureRandomString generates a cryptographically secure random string
func generateSecureRandomString(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes)[:length], nil
}
