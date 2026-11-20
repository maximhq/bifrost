package oauth2

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/providers/utils/tokencache"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	grantTypeClientCredentials = "client_credentials"
	// clientCredentialsCacheKeyVersion namespaces clientCredentialsCacheKey's hash.
	clientCredentialsCacheKeyVersion = "mcp-client-credentials-v1"
	// authMethodPrivateKeyJWT is the token_endpoint_auth_methods_supported value for a client assertion.
	authMethodPrivateKeyJWT = "private_key_jwt"
)

// clientCredentialsTokens returns the cache of access tokens for client_credentials
// MCP clients, creating it on first use.
func (p *OAuth2Provider) clientCredentialsTokens() *tokencache.Cache[string] {
	p.clientCredentialsOnce.Do(func() {
		p.clientCredentialsCache = tokencache.New[string](tokencache.Options{})
	})
	return p.clientCredentialsCache
}

// GetClientCredentialsAccessToken returns the shared access token for a
// client_credentials MCP client, minting one with the client_credentials grant
// when none is cached or the cached one is about to expire. Every caller of the
// client shares the token.
func (p *OAuth2Provider) GetClientCredentialsAccessToken(ctx context.Context, config *schemas.MCPClientConfig) (string, error) {
	cfg, serverURL, err := clientCredentialsFor(config)
	if err != nil {
		return "", err
	}
	entry, bErr := p.clientCredentialsTokens().Get(ctx, clientCredentialsCacheKey(config.ID, serverURL, cfg), func(mintCtx context.Context, _ *tokencache.Entry[string]) (*tokencache.Entry[string], *schemas.BifrostError) {
		mintCtx = withRequirePublicTarget(mintCtx, config.RequirePublicTarget)
		response, err := p.mintClientCredentialsToken(mintCtx, cfg, serverURL)
		if err != nil {
			return nil, clientCredentialsMintError(config.Name, err)
		}
		return clientCredentialsEntry(response), nil
	})
	if bErr != nil {
		return "", errors.New(bErr.Error.Message)
	}
	return entry.Value, nil
}

// forceRefreshClientCredentials drops the client's cached token and mints a new
// one, for when the upstream rejected the cached token before its expiry.
func (p *OAuth2Provider) forceRefreshClientCredentials(ctx context.Context, config *schemas.MCPClientConfig) error {
	cfg, serverURL, err := clientCredentialsFor(config)
	if err != nil {
		return err
	}
	p.clientCredentialsTokens().Invalidate(clientCredentialsCacheKey(config.ID, serverURL, cfg))
	_, err = p.GetClientCredentialsAccessToken(ctx, config)
	return err
}

// ResolveClientCredentials prepares a client_credentials block for storage (see
// PrepareClientCredentials), then requests one token with it, so credentials the
// authorization server refuses are reported on save rather than at the first call.
func (p *OAuth2Provider) ResolveClientCredentials(ctx context.Context, serverURL string, requirePublicTarget bool, cfg *schemas.MCPClientCredentialsConfig) (*schemas.MCPClientCredentialsConfig, error) {
	resolved, err := PrepareClientCredentials(ctx, serverURL, requirePublicTarget, cfg)
	if err != nil {
		return nil, err
	}
	if _, err := p.mintClientCredentialsToken(withRequirePublicTarget(ctx, requirePublicTarget), resolved, serverURL); err != nil {
		var permanent *PermanentOAuthError
		if errors.As(err, &permanent) {
			return nil, fmt.Errorf("the token endpoint refused the client credentials: %s", sanitizeTokenExchangeRejection(permanent.Body))
		}
		return nil, fmt.Errorf("could not obtain a token from %s: %w", resolved.TokenURL, err)
	}
	return resolved, nil
}

// PrepareClientCredentials validates a client_credentials block and fills in what
// was left empty. With no token_url it discovers the token endpoint from the MCP
// server at serverURL and checks that the authorization server advertises the
// client_credentials grant, the client authentication method and the signing
// algorithm Bifrost will use. It fills in token_url, auth_method and
// signing_algorithm, so later token requests use only stored values, never what
// the upstream advertises then.
func PrepareClientCredentials(ctx context.Context, serverURL string, requirePublicTarget bool, cfg *schemas.MCPClientCredentialsConfig) (*schemas.MCPClientCredentialsConfig, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	resolved := *cfg
	resolved.Scopes = slices.Clone(cfg.Scopes)
	resolved.TokenURL = strings.TrimSpace(cfg.TokenURL)

	var metadata *OAuthMetadata
	if resolved.TokenURL == "" {
		discovered, err := DiscoverTokenEndpointMetadata(withRequirePublicTarget(ctx, requirePublicTarget), serverURL)
		if err != nil {
			return nil, fmt.Errorf("could not discover the token endpoint for %s: %v; set client_credentials.token_url", serverURL, err)
		}
		if !schemas.IsHTTPSOrLoopbackURL(discovered.TokenURL) {
			return nil, fmt.Errorf("the authorization server for %s publishes a token endpoint that is not https", serverURL)
		}
		// RFC 8414: an omitted grant_types_supported means authorization_code and
		// implicit only, so the grant must be listed.
		if !slices.Contains(discovered.GrantTypes, grantTypeClientCredentials) {
			return nil, fmt.Errorf("the authorization server for %s does not advertise the client_credentials grant; set client_credentials.token_url to use its token endpoint anyway", serverURL)
		}
		metadata = discovered
		resolved.TokenURL = discovered.TokenURL
	}
	if err := resolveClientAuthentication(&resolved, metadata); err != nil {
		return nil, err
	}
	return &resolved, nil
}

// resolveClientAuthentication fills in the auth method or signing algorithm left
// empty and refuses a choice the authorization server's metadata (when
// discovered) says it does not accept.
func resolveClientAuthentication(cfg *schemas.MCPClientCredentialsConfig, metadata *OAuthMetadata) error {
	var methods, algs []string
	if metadata != nil {
		methods, algs = metadata.TokenAuthMethods, metadata.SigningAlgs
	}
	if !cfg.ClientAuth().UsesPrivateKey() {
		if cfg.AuthMethod == "" {
			cfg.AuthMethod = schemas.ClientSecretBasic
			if onlyClientSecretPost(methods) {
				cfg.AuthMethod = schemas.ClientSecretPost
			}
		}
		if len(methods) > 0 && !slices.Contains(methods, cfg.AuthMethod) {
			return fmt.Errorf("the authorization server does not accept %s; it accepts %s", cfg.AuthMethod, strings.Join(methods, ", "))
		}
		return nil
	}
	if len(methods) > 0 && !slices.Contains(methods, authMethodPrivateKeyJWT) {
		return fmt.Errorf("the authorization server does not accept private_key_jwt; it accepts %s", strings.Join(methods, ", "))
	}
	// A key held in a secret reference is checked when it is used.
	if !cfg.PrivateKey.IsFromSecret() {
		key, ok := tokencache.ParsePEMPrivateKey(cfg.PrivateKey.GetValue())
		if !ok {
			return fmt.Errorf("client_credentials.private_key must be an RSA or EC private key in PEM form")
		}
		if cfg.SigningAlgorithm == "" {
			alg, err := tokencache.DefaultSigningAlgorithm(key, algs)
			if err != nil {
				return fmt.Errorf("client_credentials: %w", err)
			}
			cfg.SigningAlgorithm = alg
		} else if err := tokencache.KeyMatchesSigningAlgorithm(key, cfg.SigningAlgorithm, "client_credentials.signing_algorithm"); err != nil {
			return err
		}
	}
	if alg := cmp.Or(cfg.SigningAlgorithm, "RS256"); len(algs) > 0 && !slices.Contains(algs, alg) {
		return fmt.Errorf("the authorization server does not accept the %s signing algorithm; it accepts %s", alg, strings.Join(algs, ", "))
	}
	return nil
}

// mintClientCredentialsToken performs one client_credentials token request.
func (p *OAuth2Provider) mintClientCredentialsToken(ctx context.Context, cfg *schemas.MCPClientCredentialsConfig, serverURL string) (*schemas.OAuth2TokenExchangeResponse, error) {
	form := url.Values{}
	form.Set("grant_type", grantTypeClientCredentials)
	if len(cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	if resource := cfg.ResourceFor(serverURL); resource != "" {
		form.Set("resource", resource)
	}
	clientID := strings.TrimSpace(cfg.ClientID.GetValue())
	opts, err := upstreamClientAuthForm(form, cfg.ClientAuth(), clientID, cfg.TokenURL, nil)
	if err != nil {
		return nil, err
	}
	return p.callTokenEndpoint(ctx, cfg.TokenURL, form, opts...)
}

// clientCredentialsFor returns config's block and server URL, or why it cannot mint.
func clientCredentialsFor(config *schemas.MCPClientConfig) (*schemas.MCPClientCredentialsConfig, string, error) {
	if config == nil || config.ClientCredentials == nil {
		return nil, "", fmt.Errorf("client_credentials is not configured")
	}
	if strings.TrimSpace(config.ClientCredentials.TokenURL) == "" {
		return nil, "", fmt.Errorf("MCP client %s has no token endpoint; save it again to discover one", config.Name)
	}
	return config.ClientCredentials, strings.TrimSpace(config.ConnectionString.GetValue()), nil
}

// clientCredentialsCacheKey keys a client's token by its ID and everything the
// token request depends on, so an edited or rotated credential never reuses a
// token minted for the old one. Secrets enter only the hash.
func clientCredentialsCacheKey(clientID, serverURL string, cfg *schemas.MCPClientCredentialsConfig) string {
	return clientID + ":" + tokencache.CacheKey(clientCredentialsCacheKeyVersion,
		serverURL, cfg.TokenURL, cfg.ClientID.GetValue(), cfg.ClientSecret.GetValue(), cfg.AuthMethod,
		cfg.PrivateKey.GetValue(), cfg.KeyID, cfg.SigningAlgorithm, cfg.AssertionAudience,
		strings.Join(cfg.Scopes, " "), cfg.ResourceFor(serverURL))
}

// clientCredentialsEntry turns a token response into a cache entry; a response
// without expires_in gets tokencache's fallback lifetime.
func clientCredentialsEntry(response *schemas.OAuth2TokenExchangeResponse) *tokencache.Entry[string] {
	var expiry time.Time
	if response.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(response.ExpiresIn) * time.Second)
	}
	return tokencache.EntryFor(response.AccessToken, expiry, tokencache.DefaultRefreshMargin)
}

// clientCredentialsMintError maps a failed token request onto the cache's error
// type: a refusal keeps the endpoint's status, so the cache backs off as for a
// configuration error; anything else is status 0, a transient failure.
func clientCredentialsMintError(clientName string, err error) *schemas.BifrostError {
	status := 0
	message := fmt.Sprintf("token request for MCP client %s failed: %v", clientName, err)
	var permanent *PermanentOAuthError
	if errors.As(err, &permanent) {
		status = permanent.StatusCode
		message = fmt.Sprintf("the token endpoint refused the client credentials of MCP client %s: %s", clientName, sanitizeTokenExchangeRejection(permanent.Body))
	}
	return &schemas.BifrostError{StatusCode: &status, Error: &schemas.ErrorField{Message: message, Error: err}}
}
