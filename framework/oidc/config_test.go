package oidc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOIDCConfig_Validate_EmptyIssuerURL(t *testing.T) {
	cfg := &OIDCConfig{
		ClientID: "my-client",
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "issuer_url")
}

func TestOIDCConfig_Validate_EmptyClientID(t *testing.T) {
	cfg := &OIDCConfig{
		IssuerURL: "https://keycloak.example.com/realms/stragixlabs",
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client_id")
}

func TestOIDCConfig_Validate_DefaultScopes(t *testing.T) {
	cfg := &OIDCConfig{
		IssuerURL: "https://keycloak.example.com/realms/stragixlabs",
		ClientID:  "bifrost",
	}
	err := cfg.Validate()
	require.NoError(t, err)
	assert.Equal(t, []string{"openid", "profile", "email"}, cfg.Scopes)
}

func TestOIDCConfig_Validate_DefaultOrgClaim(t *testing.T) {
	cfg := &OIDCConfig{
		IssuerURL: "https://keycloak.example.com/realms/stragixlabs",
		ClientID:  "bifrost",
	}
	err := cfg.Validate()
	require.NoError(t, err)
	assert.Equal(t, "organization_id", cfg.OrgClaim)
}

func TestOIDCConfig_Validate_DefaultGroupsClaim(t *testing.T) {
	cfg := &OIDCConfig{
		IssuerURL: "https://keycloak.example.com/realms/stragixlabs",
		ClientID:  "bifrost",
	}
	err := cfg.Validate()
	require.NoError(t, err)
	assert.Equal(t, "groups", cfg.GroupsClaim)
}

func TestOIDCConfig_Validate_AllFieldsPopulated(t *testing.T) {
	cfg := &OIDCConfig{
		IssuerURL:    "https://keycloak.example.com/realms/stragixlabs",
		ClientID:     "bifrost",
		ClientSecret: "my-secret",
		Scopes:       []string{"openid", "custom"},
		OrgClaim:     "org",
		GroupsClaim:  "roles",
	}
	err := cfg.Validate()
	require.NoError(t, err)
	// Custom values should be preserved, not overwritten by defaults
	assert.Equal(t, []string{"openid", "custom"}, cfg.Scopes)
	assert.Equal(t, "org", cfg.OrgClaim)
	assert.Equal(t, "roles", cfg.GroupsClaim)
}

func TestOIDCConfig_Validate_BothMissing(t *testing.T) {
	cfg := &OIDCConfig{}
	err := cfg.Validate()
	require.Error(t, err)
	// Should fail on issuer_url first
	assert.Contains(t, err.Error(), "issuer_url")
}
