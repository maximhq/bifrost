package oauth2

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// OAuthMetadata contains discovered OAuth configuration from authorization server
type OAuthMetadata struct {
	AuthorizationURL string   `json:"authorization_endpoint"`
	TokenURL         string   `json:"token_endpoint"`
	RegistrationURL  *string  `json:"registration_endpoint,omitempty"`
	ScopesSupported  []string `json:"scopes_supported,omitempty"`
	Issuer           string   `json:"issuer,omitempty"`
	ResponseTypes    []string `json:"response_types_supported,omitempty"`
	GrantTypes       []string `json:"grant_types_supported,omitempty"`
	TokenAuthMethods []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	PKCEMethods      []string `json:"code_challenge_methods_supported,omitempty"`
}

// ResourceMetadata contains metadata from protected resource
type ResourceMetadata struct {
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported,omitempty"`
	Scopes               []string `json:"scopes,omitempty"` // Alternative field name
}

// DiscoverOAuthMetadata performs OAuth 2.0 discovery for the given MCP server URL
// Following RFC 8414 (Authorization Server Discovery) and RFC 9728 (Protected Resource Metadata)
//
// Parameters:
//   - ctx: Context for the discovery requests
//   - serverURL: The MCP server URL to discover OAuth configuration from
//   - logger: Logger for discovery progress (can be nil for silent operation)
//
// The discovery process:
// 1. Attempt to connect to MCP server, expect 401 with WWW-Authenticate header
// 2. Parse WWW-Authenticate header for resource_metadata URL and scopes
// 3. Fetch resource metadata to get authorization server URLs
// 4. Try .well-known discovery if resource metadata is not available
// 5. Fetch authorization server metadata from discovered URLs
// 6. Return complete OAuth configuration
func DiscoverOAuthMetadata(ctx context.Context, serverURL string) (*OAuthMetadata, error) {
	if logger != nil {
		logger.Debug(fmt.Sprintf("[OAuth Discovery] Starting discovery for server: %s", serverURL))
	}

	// Step 1: Attempt to connect to MCP server, expect 401 with WWW-Authenticate header
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", serverURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to server: %w", err)
	}
	defer resp.Body.Close()

	logger.Debug(fmt.Sprintf("[OAuth Discovery] Server responded with status: %d", resp.StatusCode))

	// Step 2: Parse WWW-Authenticate header
	wwwAuth := resp.Header.Get("WWW-Authenticate")
	if wwwAuth == "" {
		wwwAuth = resp.Header.Get("www-authenticate")
	}

	resourceMetadataURL, scopesFromHeader := parseWWWAuthenticateHeader(wwwAuth)
	if resourceMetadataURL != "" {
		logger.Debug(fmt.Sprintf("[OAuth Discovery] Found resource_metadata URL: %s", resourceMetadataURL))
	}
	if len(scopesFromHeader) > 0 {
		logger.Debug(fmt.Sprintf("[OAuth Discovery] Found scopes in header: %v", scopesFromHeader))
	}

	// Step 3: Fetch resource metadata if available
	var authServers []string
	var resourceScopes []string

	if resourceMetadataURL != "" {
		authServers, resourceScopes, err = fetchResourceMetadata(ctx, resourceMetadataURL)
		if err != nil {
			// Log but continue to well-known discovery
			logger.Warn(fmt.Sprintf("[OAuth Discovery] Failed to fetch resource metadata: %v", err))
		} else {
			logger.Debug(fmt.Sprintf("[OAuth Discovery] Found %d authorization servers from resource metadata", len(authServers)))
		}
	}

	// Step 4: Try well-known discovery if no resource metadata
	if len(authServers) == 0 {
		logger.Debug("[OAuth Discovery] Attempting .well-known discovery")
		authServers, resourceScopes, err = attemptWellKnownDiscovery(ctx, serverURL)
		if err != nil {
			return nil, fmt.Errorf("OAuth discovery failed: %w", err)
		}
		logger.Debug(fmt.Sprintf("[OAuth Discovery] Found %d authorization servers from .well-known", len(authServers)))
	}

	// Step 5: Fetch authorization server metadata
	metadata, err := fetchAuthorizationServerMetadata(ctx, authServers)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch authorization server metadata: %w", err)
	}

	// Step 6: Merge scopes (priority: header > resource metadata > discovered)
	if len(scopesFromHeader) > 0 {
		metadata.ScopesSupported = scopesFromHeader
	} else if len(resourceScopes) > 0 {
		metadata.ScopesSupported = resourceScopes
	}

	logger.Debug(fmt.Sprintf("[OAuth Discovery] Successfully discovered OAuth metadata for %s", serverURL))
	logger.Debug(fmt.Sprintf("[OAuth Discovery] Authorization URL: %s", metadata.AuthorizationURL))
	logger.Debug(fmt.Sprintf("[OAuth Discovery] Token URL: %s", metadata.TokenURL))
	if metadata.RegistrationURL != nil {
		logger.Debug(fmt.Sprintf("[OAuth Discovery] Registration URL: %s", *metadata.RegistrationURL))
	}
	logger.Debug(fmt.Sprintf("[OAuth Discovery] Scopes: %v", metadata.ScopesSupported))

	return metadata, nil
}

// parseWWWAuthenticateHeader extracts resource_metadata URL and scopes from WWW-Authenticate header
// Example header: Bearer resource_metadata="https://example.com/.well-known/oauth-protected-resource", scope="read write"
func parseWWWAuthenticateHeader(header string) (resourceMetadataURL string, scopes []string) {
	if header == "" {
		return "", nil
	}

	// Extract parameters from header
	// Pattern matches: param_name="value" or param_name=value
	paramPattern := regexp.MustCompile(`([a-zA-Z0-9_]+)\s*=\s*"?([^",]+)"?`)
	matches := paramPattern.FindAllStringSubmatch(header, -1)

	params := make(map[string]string)
	for _, match := range matches {
		if len(match) == 3 {
			params[strings.ToLower(match[1])] = strings.TrimSpace(match[2])
		}
	}

	resourceMetadataURL = params["resource_metadata"]

	if scopeValue := params["scope"]; scopeValue != "" {
		scopes = strings.Fields(scopeValue)
	}

	return resourceMetadataURL, scopes
}

// fetchResourceMetadata fetches OAuth metadata from resource metadata endpoint (RFC 9728)
func fetchResourceMetadata(ctx context.Context, metadataURL string) ([]string, []string, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", metadataURL, nil)
	if err != nil {
		return nil, nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("unexpected status %d from resource metadata endpoint", resp.StatusCode)
	}

	var data ResourceMetadata
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, nil, fmt.Errorf("failed to decode resource metadata: %w", err)
	}

	// Use scopes_supported first, fall back to scopes
	scopes := data.ScopesSupported
	if len(scopes) == 0 {
		scopes = data.Scopes
	}

	return data.AuthorizationServers, scopes, nil
}

// attemptWellKnownDiscovery tries standard .well-known endpoints for protected resource discovery
func attemptWellKnownDiscovery(ctx context.Context, serverURL string) ([]string, []string, error) {
	// Parse server URL to get base and path
	base, path := splitURL(serverURL)
	if base == "" {
		return nil, nil, fmt.Errorf("invalid server URL: %s", serverURL)
	}

	// Try different well-known locations
	var candidateURLs []string
	if path != "" {
		candidateURLs = append(candidateURLs, fmt.Sprintf("%s/.well-known/oauth-protected-resource/%s", base, path))
	}
	candidateURLs = append(candidateURLs, fmt.Sprintf("%s/.well-known/oauth-protected-resource", base))

	logger.Debug(fmt.Sprintf("[OAuth Discovery] Trying %d .well-known URLs", len(candidateURLs)))

	for _, candidateURL := range candidateURLs {
		logger.Debug(fmt.Sprintf("[OAuth Discovery] Trying: %s", candidateURL))
		authServers, scopes, err := fetchResourceMetadata(ctx, candidateURL)
		if err == nil && len(authServers) > 0 {
			logger.Debug(fmt.Sprintf("[OAuth Discovery] Found metadata at: %s", candidateURL))
			return authServers, scopes, nil
		}
	}

	// Fallback: assume server base is the authorization server
	logger.Debug(fmt.Sprintf("[OAuth Discovery] No .well-known found, assuming server base is auth server: %s", base))
	return []string{base}, nil, nil
}

// fetchAuthorizationServerMetadata fetches OAuth endpoints from authorization server(s)
// Tries multiple authorization servers until one succeeds
func fetchAuthorizationServerMetadata(ctx context.Context, authServers []string) (*OAuthMetadata, error) {
	for _, issuer := range authServers {
		logger.Debug(fmt.Sprintf("[OAuth Discovery] Fetching metadata from authorization server: %s", issuer))
		metadata, err := fetchSingleAuthServerMetadata(ctx, issuer)
		if err == nil && metadata != nil {
			logger.Debug(fmt.Sprintf("[OAuth Discovery] Successfully fetched metadata from: %s", issuer))
			return metadata, nil
		}
		logger.Debug(fmt.Sprintf("[OAuth Discovery] Failed to fetch from %s: %v", issuer, err))
	}
	return nil, fmt.Errorf("failed to fetch metadata from any authorization server")
}

// fetchSingleAuthServerMetadata tries multiple well-known endpoints for a single authorization server
// Implements RFC 8414 discovery
func fetchSingleAuthServerMetadata(ctx context.Context, issuer string) (*OAuthMetadata, error) {
	base, path := splitURL(issuer)
	if base == "" {
		return nil, fmt.Errorf("invalid issuer URL: %s", issuer)
	}

	// Try different well-known endpoint patterns
	var candidateURLs []string
	if path != "" {
		candidateURLs = append(candidateURLs,
			fmt.Sprintf("%s/.well-known/oauth-authorization-server/%s", base, path),
			fmt.Sprintf("%s/.well-known/openid-configuration/%s", base, path),
		)
	}
	candidateURLs = append(candidateURLs,
		fmt.Sprintf("%s/.well-known/oauth-authorization-server", base),
		fmt.Sprintf("%s/.well-known/openid-configuration", base),
		strings.TrimSuffix(issuer, "/"), // Try the issuer URL itself
	)

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	for _, candidateURL := range candidateURLs {
		logger.Debug(fmt.Sprintf("[OAuth Discovery] Trying metadata endpoint: %s", candidateURL))
		req, err := http.NewRequestWithContext(ctx, "GET", candidateURL, nil)
		if err != nil {
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode == http.StatusOK {
			var metadata OAuthMetadata
			bodyBytes, err := io.ReadAll(resp.Body)
			resp.Body.Close()

			if err != nil {
				continue
			}

			if err := json.Unmarshal(bodyBytes, &metadata); err == nil {
				// Validate that we got at least authorization_endpoint
				if metadata.AuthorizationURL != "" {
					logger.Debug(fmt.Sprintf("[OAuth Discovery] Valid metadata found at: %s", candidateURL))
					return &metadata, nil
				}
			}
		} else {
			resp.Body.Close()
		}
	}

	return nil, fmt.Errorf("no valid metadata found for issuer: %s", issuer)
}

// splitURL splits a URL into base (scheme://host) and path
func splitURL(urlStr string) (base, path string) {
	// Parse URL
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return "", ""
	}

	// Build base URL (scheme + host)
	base = fmt.Sprintf("%s://%s", parsedURL.Scheme, parsedURL.Host)

	// Get path without leading slash
	path = strings.TrimPrefix(parsedURL.Path, "/")

	return base, path
}

// GeneratePKCEChallenge generates code_verifier and code_challenge for PKCE (RFC 7636)
// Returns:
//   - verifier: Random 128-character string (stored securely, never sent to server)
//   - challenge: SHA256 hash of verifier, base64url encoded (sent in authorization request)
func GeneratePKCEChallenge() (verifier, challenge string, err error) {
	// Generate random 43-128 character string (we use 128 for maximum entropy)
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	const length = 128

	// Use crypto/rand for secure random generation
	randomBytes := make([]byte, length)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Convert to allowed charset
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[int(randomBytes[i])%len(charset)]
	}
	verifier = string(b)

	// Generate SHA256 hash and base64url encode
	hash := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(hash[:])

	logger.Debug("[OAuth PKCE] Generated code_verifier and code_challenge")

	return verifier, challenge, nil
}

// ValidatePKCEChallenge validates that a code_verifier matches the expected code_challenge
// Used during testing or debugging
func ValidatePKCEChallenge(verifier, challenge string) bool {
	hash := sha256.Sum256([]byte(verifier))
	expectedChallenge := base64.RawURLEncoding.EncodeToString(hash[:])
	return expectedChallenge == challenge
}

// DynamicClientRegistrationRequest represents the client registration request (RFC 7591)
type DynamicClientRegistrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope,omitempty"`
	LogoURI                 string   `json:"logo_uri,omitempty"`
	ClientURI               string   `json:"client_uri,omitempty"`
	Contacts                []string `json:"contacts,omitempty"`
}

// DynamicClientRegistrationResponse represents the server's response (RFC 7591)
type DynamicClientRegistrationResponse struct {
	ClientID                string `json:"client_id"`
	ClientSecret            string `json:"client_secret,omitempty"`
	ClientIDIssuedAt        int64  `json:"client_id_issued_at,omitempty"`
	ClientSecretExpiresAt   int64  `json:"client_secret_expires_at,omitempty"`
	RegistrationAccessToken string `json:"registration_access_token,omitempty"`
	RegistrationClientURI   string `json:"registration_client_uri,omitempty"`
}

// RegisterDynamicClient performs dynamic client registration with the OAuth provider (RFC 7591)
// This allows Bifrost to automatically register as an OAuth client without manual setup.
//
// Parameters:
//   - ctx: Context for the registration request
//   - registrationURL: The registration endpoint (discovered or user-provided)
//   - req: Client registration details
//
// Returns client_id and optional client_secret that can be used for OAuth flows.
func RegisterDynamicClient(ctx context.Context, registrationURL string, req *DynamicClientRegistrationRequest) (*DynamicClientRegistrationResponse, error) {
	logger.Debug(fmt.Sprintf("[Dynamic Registration] Registering client at: %s", registrationURL))
	logger.Debug(fmt.Sprintf("[Dynamic Registration] Client name: %s, Redirect URIs: %v", req.ClientName, req.RedirectURIs))

	// Serialize request
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal registration request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", registrationURL, strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create registration request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	// Send request
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("registration request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read registration response: %w", err)
	}

	// Check status code (201 Created or 200 OK are both valid per RFC 7591)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		logger.Error(fmt.Sprintf("[Dynamic Registration] Failed with status %d: %s", resp.StatusCode, string(respBody)))
		return nil, fmt.Errorf("registration failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response
	var regResp DynamicClientRegistrationResponse
	if err := json.Unmarshal(respBody, &regResp); err != nil {
		return nil, fmt.Errorf("failed to parse registration response: %w", err)
	}

	// Validate response
	if regResp.ClientID == "" {
		return nil, fmt.Errorf("registration response missing client_id")
	}

	logger.Debug(fmt.Sprintf("[Dynamic Registration] Successfully registered client_id: %s", regResp.ClientID))
	if regResp.ClientSecret != "" {
		logger.Debug("[Dynamic Registration] Client secret provided by server")
	} else {
		logger.Debug("[Dynamic Registration] No client secret provided (public client)")
	}

	return &regResp, nil
}
