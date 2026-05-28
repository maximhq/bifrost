// Package credstore implements schemas.CredentialStore: it routes credential
// resolution for MCP tool execution by auth type. Each MCPAuthType has a
// dedicated resolver; the Store dispatches based on MCPClientConfig.AuthType.
//
// The store knows nothing about storage lifecycle (orphaning, cascade) — that
// stays in the configstore layer where transactional atomicity holds.
package credstore

import (
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
func (s *CredStore) RequiresPerCallConnection(config *schemas.MCPClientConfig) bool {
	if config == nil {
		return false
	}
	r, ok := s.resolvers[config.AuthType]
	if !ok {
		return false
	}
	return r.RequiresPerCallConnection()
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
