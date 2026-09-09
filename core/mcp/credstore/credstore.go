// Package credstore implements schemas.CredentialStore: it routes credential
// resolution for MCP tool execution by auth type. Each MCPAuthType has a
// dedicated resolver; the Store dispatches based on MCPClientConfig.AuthType.
//
// The store knows nothing about storage lifecycle (orphaning, cascade) — that
// stays in the configstore layer where transactional atomicity holds.
package credstore

import (
	"context"
	"fmt"
	"net/http"

	"github.com/maximhq/bifrost/core/mcp/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// resolver is the internal interface each auth-type-specific resolver
// implements. RequestHeaders is identical across all auth types (extras only)
// and lives on CredStore directly, not here.
type resolver interface {
	ConnectionHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error)
	RequiresPerCallConnection() bool
	// ForceRefresh unconditionally refreshes the credential backing config.
	// See schemas.MCPCredentialStore.ForceRefresh for the full contract.
	ForceRefresh(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) error
	// AdminConnectionHeaders resolves the retained admin bootstrap-verification
	// credential's connection headers. See schemas.MCPCredentialStore.AdminConnectionHeaders
	// for the full contract.
	AdminConnectionHeaders(ctx context.Context, config *schemas.MCPClientConfig) (http.Header, error)
}

// CredStore routes credential resolution by MCPAuthType. Implements
// schemas.MCPCredentialStore.
type CredStore struct {
	resolvers map[schemas.MCPAuthType]resolver
	logger    schemas.Logger
}

// NewCredStore constructs the canonical MCPCredentialStore with one resolver
// per known MCPAuthType. The oauth2Provider is injected into the OAuth-
// flavored resolvers only; the None and StaticHeaders resolvers are
// stateless. The headersProvider is injected into the per-user-headers
// resolver — pass nil if the configstore-backed provider isn't wired up
// (the resolver returns a clear error rather than nil-pointering at use).
func NewCredStore(oauth2Provider schemas.OAuth2Provider, headersProvider schemas.MCPHeadersProvider, logger schemas.Logger) *CredStore {
	return &CredStore{
		resolvers: map[schemas.MCPAuthType]resolver{
			schemas.MCPAuthTypeNone:           &noneResolver{},
			schemas.MCPAuthTypeHeaders:        &sharedHeadersResolver{},
			schemas.MCPAuthTypeOauth:          &sharedOAuthResolver{provider: oauth2Provider},
			schemas.MCPAuthTypePerUserOauth:   &perUserOAuthResolver{provider: oauth2Provider},
			schemas.MCPAuthTypePerUserHeaders: &perUserHeadersResolver{provider: headersProvider},
			schemas.MCPAuthTypeTokenExchange:  &tokenExchangeResolver{provider: oauth2Provider},
		},
		logger: logger,
	}
}

// ConnectionHeaders implements schemas.MCPCredentialStore.
func (s *CredStore) ConnectionHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	r, err := s.resolverFor(config)
	if err != nil {
		return nil, err
	}
	return r.ConnectionHeaders(ctx, config)
}

// RequestHeaders implements schemas.MCPCredentialStore. Identical across auth
// types: just the filtered per-call context-extras. The auth-type lookup
// still runs so an unknown type errors loudly here too, instead of silently
// returning empty.
func (s *CredStore) RequestHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	if _, err := s.resolverFor(config); err != nil {
		return nil, err
	}
	return utils.ExtractFilteredExtras(ctx, config), nil
}

// RequiresPerCallConnection implements schemas.MCPCredentialStore. For
// unknown auth types it returns false (safe shared-mode default); the next
// ConnectionHeaders / RequestHeaders call from the caller will surface the
// actual "unsupported auth type" error.
//
// Per-user auth types are always per-call regardless of stickiness (their
// resolver hardcodes true — there's no "shared" mode for them to opt out
// of). Shared HTTP auth types (headers/oauth) additionally honor
// config.NeedsSessionStickiness: only explicitly true keeps them on the
// persistent-connection + connection-checker path (every pre-existing
// client is backfilled to true at the DB layer so this is a no-op for
// them); nil/false (the default for newly created clients) routes them
// through the per-call path too, same as the per-user types. SSE and STDIO
// ignore the flag — see needsSessionStickiness's doc comment.
func (s *CredStore) RequiresPerCallConnection(config *schemas.MCPClientConfig) bool {
	if config == nil {
		return false
	}
	r, ok := s.resolvers[config.AuthType]
	if !ok {
		return false
	}
	if r.RequiresPerCallConnection() {
		return true
	}
	return !needsSessionStickiness(config)
}

// needsSessionStickiness reports whether config wants a persistent shared
// connection (explicitly true) or a fresh per-call connection (nil/false,
// the default for newly created clients). Only meaningful for
// connection_type=http: SSE has no stateless mode (its session is
// inherently bound to the open stream) and STDIO needs a persistent
// subprocess, so both are treated as always sticky regardless of what the
// field says — write-time validation is responsible for rejecting an
// explicit false for those types in the first place, this is just a
// defensive second check at the point stickiness actually matters.
func needsSessionStickiness(config *schemas.MCPClientConfig) bool {
	if config.ConnectionType != schemas.MCPConnectionTypeHTTP {
		return true
	}
	return config.NeedsSessionStickiness != nil && *config.NeedsSessionStickiness
}

// ForceRefresh implements schemas.MCPCredentialStore.
func (s *CredStore) ForceRefresh(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) error {
	r, err := s.resolverFor(config)
	if err != nil {
		return err
	}
	return r.ForceRefresh(ctx, config)
}

// AdminConnectionHeaders implements schemas.MCPCredentialStore.
func (s *CredStore) AdminConnectionHeaders(ctx context.Context, config *schemas.MCPClientConfig) (http.Header, error) {
	r, err := s.resolverFor(config)
	if err != nil {
		return nil, err
	}
	return r.AdminConnectionHeaders(ctx, config)
}

// resolverFor returns the resolver matching config.AuthType, or an error if
// the type is truly unknown / config is nil. Empty AuthType is normalized to
// MCPAuthTypeHeaders — matching the DB column default
// (TableMCPClient.AuthType default 'headers') and clientmanager.UpdateClient's
// long-standing normalization. Programmatically-constructed configs that
// leave AuthType blank therefore behave as plain "headers" auth.
func (s *CredStore) resolverFor(config *schemas.MCPClientConfig) (resolver, error) {
	if config == nil {
		return nil, fmt.Errorf("MCP client config is nil")
	}
	authType := config.AuthType
	if authType == "" {
		authType = schemas.MCPAuthTypeHeaders
	}
	if r, ok := s.resolvers[authType]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("unsupported MCP auth type %q for client %q", config.AuthType, config.Name)
}
