package oidc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeycloakClaims_UnmarshalJSON_FullClaims(t *testing.T) {
	raw := `{
		"sub": "550e8400-e29b-41d4-a716-446655440000",
		"email": "user@example.com",
		"email_verified": true,
		"name": "Test User",
		"preferred_username": "testuser",
		"organization_id": "org-uuid-123",
		"groups": ["admin", "developers"],
		"realm_access": {
			"roles": ["default-roles-stragixlabs", "uma_authorization"]
		}
	}`

	var claims KeycloakClaims
	err := json.Unmarshal([]byte(raw), &claims)
	require.NoError(t, err)

	assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", claims.Subject)
	assert.Equal(t, "user@example.com", claims.Email)
	assert.True(t, claims.EmailVerified)
	assert.Equal(t, "Test User", claims.Name)
	assert.Equal(t, "testuser", claims.PreferredUser)
	assert.Equal(t, "org-uuid-123", claims.OrgID)
	assert.Equal(t, []string{"admin", "developers"}, claims.Groups)
	require.NotNil(t, claims.RealmAccess)
	assert.Contains(t, claims.RealmAccess.Roles, "uma_authorization")
}

func TestKeycloakClaims_UnmarshalJSON_MissingOptionalFields(t *testing.T) {
	raw := `{
		"sub": "550e8400-e29b-41d4-a716-446655440000",
		"email": "user@example.com"
	}`

	var claims KeycloakClaims
	err := json.Unmarshal([]byte(raw), &claims)
	require.NoError(t, err)

	assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", claims.Subject)
	assert.Equal(t, "user@example.com", claims.Email)
	assert.Empty(t, claims.OrgID)
	assert.Nil(t, claims.Groups)
	assert.Nil(t, claims.RealmAccess)
}

func TestKeycloakClaims_UnmarshalJSON_EmptyGroups(t *testing.T) {
	raw := `{
		"sub": "user-id",
		"email": "user@example.com",
		"groups": []
	}`

	var claims KeycloakClaims
	err := json.Unmarshal([]byte(raw), &claims)
	require.NoError(t, err)

	assert.Empty(t, claims.Groups)
}

func TestContextKeys_UniquePrefix(t *testing.T) {
	// Verify all OIDC context keys use the correct prefix (D-03)
	keys := []string{
		BifrostContextKeyOIDCAuthenticated,
		BifrostContextKeyOIDCSub,
		BifrostContextKeyOIDCEmail,
		BifrostContextKeyOIDCOrgID,
		BifrostContextKeyOIDCGroups,
	}
	for _, key := range keys {
		assert.Contains(t, key, "BifrostContextKeyOIDC", "key %q must use OIDC prefix", key)
	}

	// Verify uniqueness
	unique := make(map[string]bool)
	for _, key := range keys {
		assert.False(t, unique[key], "duplicate context key: %s", key)
		unique[key] = true
	}
}
