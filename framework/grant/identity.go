package grant

import (
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
)

// CredentialKind names what a request presented to say who it is: one of the things this gateway
// identifies a caller by. These are the values a schemas.Credential carries in Kind. A raw provider
// key is not among them: it selects which key serves the request and says nothing about who made it.
type CredentialKind string

const (
	// CredentialVirtualKey marks a virtual key.
	CredentialVirtualKey CredentialKind = "virtual_key"
	// CredentialSessionToken marks a dashboard session token.
	CredentialSessionToken CredentialKind = "session_token"
	// CredentialIdentityToken marks a token the identity provider issued for the user.
	CredentialIdentityToken CredentialKind = "idp_jwt"
	// CredentialMCPToken marks a token this deployment's own authorization server issued to an MCP
	// client.
	CredentialMCPToken CredentialKind = "mcp_jwt"
	// CredentialAPIKey marks a management API key.
	CredentialAPIKey CredentialKind = "api_key"
)

// NewCredential is a schemas.Credential of the given kind.
func NewCredential(kind CredentialKind, value string) schemas.Credential {
	return schemas.Credential{Kind: string(kind), Value: value}
}

// Identity is the one implementation of schemas.Identity. It is built whole and never edited: a
// layer that learns more about the caller builds the replacement from what the getters return plus
// what it learned, which is what lets a request's context hand a identity out without a lock.
type Identity struct {
	credential schemas.Credential

	user       *schemas.UserRef
	virtualKey *schemas.EntityRef

	teams         []schemas.EntityRef
	customers     []schemas.EntityRef
	businessUnits []schemas.EntityRef

	project *schemas.EntityRef
}

// NewIdentity builds a Identity. See schemas.Identity for what each value means. The lists are
// copied, so a caller that keeps its slices cannot alter what the request is attributed to.
func NewIdentity(
	credential schemas.Credential,
	user *schemas.UserRef,
	virtualKey *schemas.EntityRef,
	teams []schemas.EntityRef,
	customers []schemas.EntityRef,
	businessUnits []schemas.EntityRef,
	project *schemas.EntityRef,
) *Identity {
	return &Identity{
		credential:    credential,
		user:          user,
		virtualKey:    virtualKey,
		teams:         slices.Clone(teams),
		customers:     slices.Clone(customers),
		businessUnits: slices.Clone(businessUnits),
		project:       project,
	}
}

// Credential implements schemas.Identity.
func (p *Identity) Credential() schemas.Credential {
	if p == nil {
		return schemas.Credential{}
	}
	return p.credential
}

// Presented implements schemas.Identity.
func (p *Identity) Presented() bool {
	return p != nil && p.credential.Kind != ""
}

// User implements schemas.Identity.
func (p *Identity) User() *schemas.UserRef {
	if p == nil {
		return nil
	}
	return p.user
}

// VirtualKey implements schemas.Identity.
func (p *Identity) VirtualKey() *schemas.EntityRef {
	if p == nil {
		return nil
	}
	return p.virtualKey
}

// Teams implements schemas.Identity.
func (p *Identity) Teams() []schemas.EntityRef {
	if p == nil {
		return nil
	}
	return p.teams
}

// Customers implements schemas.Identity.
func (p *Identity) Customers() []schemas.EntityRef {
	if p == nil {
		return nil
	}
	return p.customers
}

// BusinessUnits implements schemas.Identity.
func (p *Identity) BusinessUnits() []schemas.EntityRef {
	if p == nil {
		return nil
	}
	return p.businessUnits
}

// Project implements schemas.Identity.
func (p *Identity) Project() *schemas.EntityRef {
	if p == nil {
		return nil
	}
	return p.project
}
