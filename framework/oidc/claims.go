package oidc

import "github.com/golang-jwt/jwt/v5"

// KeycloakClaims represents the JWT claims from a Keycloak access token.
// Custom claims (organization_id, groups) are added via Keycloak protocol mappers.
type KeycloakClaims struct {
	jwt.RegisteredClaims
	Email         string       `json:"email"`
	EmailVerified bool         `json:"email_verified"`
	Name          string       `json:"name"`
	PreferredUser string       `json:"preferred_username"`
	OrgID         string       `json:"organization_id"`
	Groups        []string     `json:"groups"`
	RealmAccess   *RealmAccess `json:"realm_access,omitempty"`
}

// RealmAccess represents Keycloak's realm_access claim.
type RealmAccess struct {
	Roles []string `json:"roles"`
}

// BifrostContextKeyOIDC* are context keys for OIDC-authenticated requests.
// These MUST NOT collide with existing BifrostContextKey* values (D-03).
const (
	BifrostContextKeyOIDCAuthenticated = "BifrostContextKeyOIDCAuthenticated"
	BifrostContextKeyOIDCSub           = "BifrostContextKeyOIDCSub"
	BifrostContextKeyOIDCEmail         = "BifrostContextKeyOIDCEmail"
	BifrostContextKeyOIDCOrgID         = "BifrostContextKeyOIDCOrgID"
	BifrostContextKeyOIDCGroups        = "BifrostContextKeyOIDCGroups"
)
