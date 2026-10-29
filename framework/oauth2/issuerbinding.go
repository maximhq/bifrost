package oauth2

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

var (
	// ErrIssuerMismatch marks authorization server metadata that names another issuer (RFC 8414 3.3).
	ErrIssuerMismatch = errors.New("authorization server metadata names another issuer")
	// ErrAuthorizationResponseIssuer marks an authorization response from an unexpected issuer (RFC 9207).
	ErrAuthorizationResponseIssuer = errors.New("the authorization response did not come from the expected authorization server")
	// ErrAuthorizationServerChanged marks pre-registered credentials whose authorization server changed (SEP-2352).
	ErrAuthorizationServerChanged = errors.New("the MCP server's authorization server changed")
)

// CheckAuthorizationResponseIssuer applies RFC 9207 2.4 to state's authorization response before its
// code or error is used. A flow with no recorded issuer is not checked.
func (p *OAuth2Provider) CheckAuthorizationResponseIssuer(ctx context.Context, state, iss string, hasIss bool) error {
	if state == "" {
		return nil
	}
	flow, err := p.configStore.GetOauthUserSessionByState(ctx, state)
	if err != nil {
		return fmt.Errorf("failed to look up oauth flow: %w", err)
	}
	if flow == nil {
		return nil
	}
	return checkAuthorizationResponseIssuer(flow.Issuer, flow.IssParameterSupported, iss, hasIss)
}

// checkFlowAuthorizationServer refuses to complete a flow whose config was rebound to another
// authorization server since it started: its code belongs to the server the flow recorded, and the
// config's token endpoint is now another server's (RFC 9207 mix-up).
func checkFlowAuthorizationServer(flow *tables.TableMCPOauthFlow, config *tables.TableOauthConfig) error {
	if flow.Issuer == "" || flow.Issuer == config.Issuer {
		return nil
	}
	return fmt.Errorf("%w from %q to %q while this sign-in was open: sign in again", ErrAuthorizationServerChanged, flow.Issuer, config.Issuer)
}

// checkAuthorizationResponseIssuer compares a present iss exactly, and requires one when advertised.
func checkAuthorizationResponseIssuer(expected string, required bool, iss string, hasIss bool) error {
	switch {
	case expected == "":
		return nil
	case hasIss && iss != expected:
		return fmt.Errorf("%w: iss %q, expected %q", ErrAuthorizationResponseIssuer, iss, expected)
	case !hasIss && required:
		return fmt.Errorf("%w: iss missing, expected %q", ErrAuthorizationResponseIssuer, expected)
	}
	return nil
}

// bindAuthorizationServer checks, as a sign-in starts, that the MCP server still points to the
// authorization server config's credentials belong to (SEP-2352). redirectURI is the callback the
// sign-in presents; "" uses the stored one.
func (p *OAuth2Provider) bindAuthorizationServer(ctx context.Context, config *tables.TableOauthConfig, mcpClientID, redirectURI string) (*tables.TableOauthConfig, error) {
	if config.ServerURL == "" {
		return config, nil
	}
	if _, err, _ := p.bindGroup.Do(config.ID, func() (any, error) {
		return p.bindAuthorizationServerOnce(ctx, config.ID, mcpClientID, redirectURI)
	}); err != nil {
		return nil, err
	}
	// Reloaded whether or not the check wrote: another replica or an admin edit may have changed the
	// row since the caller loaded it, and the callback loads the stored one.
	reloaded, err := p.configStore.GetOauthConfigByID(ctx, config.ID)
	if err == nil && reloaded == nil {
		return nil, schemas.ErrOAuth2ConfigNotFound
	}
	return reloaded, err
}

// bindAuthorizationServerOnce reports whether the config changed. An unbound config adopts the issuer
// when the token endpoint matches; on a change, a Client ID Metadata Document id carries over,
// dynamically registered credentials are registered again, and pre-registered ones are refused.
// bindGroup coordinates callers in this process; ExpectedIssuer coordinates replicas.
func (p *OAuth2Provider) bindAuthorizationServerOnce(ctx context.Context, oauthConfigID, mcpClientID, redirectURI string) (bool, error) {
	config, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil {
		return false, fmt.Errorf("failed to load oauth config: %w", err)
	}
	if config == nil {
		return false, fmt.Errorf("oauth config %s not found", oauthConfigID)
	}
	ctx = withRequirePublicTarget(ctx, p.requirePublicTargetFor(ctx, mcpClientID, config))
	metadata, err := DiscoverOAuthMetadata(ctx, config.ServerURL)
	if err != nil {
		logger.Warn("[OAuth] Could not confirm the authorization server for %s, signing in with the stored one: %v", config.ServerURL, err)
		return false, nil
	}

	// Only the binding is written, and only while issuer and token_url are what was read: saving the
	// whole row would undo a credential edit made while discovery ran.
	switch {
	case config.Issuer == "":
		if !sameTokenEndpoint(metadata.TokenURL, config.TokenURL) {
			return false, nil
		}
		return p.configStore.BindOAuthConfigIssuer(ctx, config.ID, "", config.TokenURL, metadata.Issuer, metadata.IssParameterSupported)
	case config.Issuer == metadata.Issuer:
		if config.IssParameterSupported == metadata.IssParameterSupported {
			return false, nil
		}
		return p.configStore.BindOAuthConfigIssuer(ctx, config.ID, config.Issuer, config.TokenURL, config.Issuer, metadata.IssParameterSupported)
	case slices.Contains(metadata.AuthorizationServers, config.Issuer):
		// Another advertised server answered first; the bound one is still the MCP server's.
		bound, err := fetchSingleAuthServerMetadata(ctx, config.Issuer, hasAuthorizationEndpoint, true)
		if err != nil {
			logger.Warn("[OAuth] Could not reach the authorization server %s for %s, signing in with the stored one: %v", config.Issuer, config.ServerURL, err)
			return false, nil
		}
		if config.IssParameterSupported == bound.IssParameterSupported {
			return false, nil
		}
		return p.configStore.BindOAuthConfigIssuer(ctx, config.ID, config.Issuer, config.TokenURL, config.Issuer, bound.IssParameterSupported)
	}

	scopes, err := config.ScopeList()
	if err != nil {
		return false, fmt.Errorf("failed to parse stored oauth scopes: %w", err)
	}
	if redirectURI == "" {
		redirectURI = config.RedirectURI
	}
	previousIssuer := config.Issuer
	fields := configstore.MCPOAuthConfigFields{
		ClientID:              config.ClientID,
		ClientSecret:          config.ClientSecret,
		AuthorizeURL:          metadata.AuthorizationURL,
		TokenURL:              metadata.TokenURL,
		Resource:              config.Resource,
		Scopes:                scopes,
		Issuer:                metadata.Issuer,
		IssParameterSupported: metadata.IssParameterSupported,
		DynamicallyRegistered: config.DynamicallyRegistered,
		ExpectedIssuer:        &previousIssuer,
	}
	if metadata.RegistrationURL != nil {
		fields.RegistrationURL = *metadata.RegistrationURL
	}
	clientID := config.GetResolvedClientID()
	switch {
	case IsClientMetadataURL(clientID) && metadata.ClientIDMetadataDocumentSupported:
		// A Client ID Metadata Document id is valid at any authorization server.
	case config.DynamicallyRegistered || IsClientMetadataURL(clientID):
		if fields.RegistrationURL == "" {
			return false, fmt.Errorf("%w from %q to %q, which offers no dynamic client registration: enter a client_id registered with %q", ErrAuthorizationServerChanged, config.Issuer, metadata.Issuer, metadata.Issuer)
		}
		fields.ClientID, fields.ClientSecret, err = registerDynamicClient(ctx, fields.RegistrationURL, redirectURI, scopes)
		if err != nil {
			return false, fmt.Errorf("%w from %q to %q, and registering with it failed: %w", ErrAuthorizationServerChanged, config.Issuer, metadata.Issuer, err)
		}
		fields.DynamicallyRegistered = true
	default:
		return false, fmt.Errorf("%w from %q to %q, and its client credentials were registered with %q: enter credentials registered with %q", ErrAuthorizationServerChanged, config.Issuer, metadata.Issuer, config.Issuer, metadata.Issuer)
	}
	if _, err := p.configStore.RotateMCPOAuthConfig(ctx, config, fields); err != nil {
		// Another replica moved it first: sign in with its registration, which it has already announced.
		if errors.Is(err, configstore.ErrOAuthConfigChanged) {
			return true, nil
		}
		return false, fmt.Errorf("failed to store the new authorization server: %w", err)
	}
	logger.Warn("[OAuth] The authorization server for %s changed from %s to %s; oauth config %s now uses client_id %s, and tokens from the previous server must re-authenticate",
		config.ServerURL, previousIssuer, metadata.Issuer, config.ID, config.GetResolvedClientID())
	// Tokens and a shared connection from the previous server are now stale everywhere.
	if hook := p.credentialsRotated.Load(); hook != nil && mcpClientID != "" {
		(*hook)(ctx, mcpClientID)
	}
	return true, nil
}
