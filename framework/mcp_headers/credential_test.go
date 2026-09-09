package mcp_headers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// TestGetCredentialByMode_AdminMode_EmptyIdentityReachesStore pins the
// widened guard: MCPAuthModeAdmin with an empty identity must no longer be
// rejected locally — it must reach the store layer and, when a row exists,
// succeed.
func TestGetCredentialByMode_AdminMode_EmptyIdentityReachesStore(t *testing.T) {
	store := newTestConfigStore()
	now := time.Now()
	store.credentials["client-1"] = &tables.TableMCPPerUserHeaderCredential{
		ID:          "cred-admin",
		MCPClientID: "client-1",
		AuthMode:    "admin",
		Status:      "active",
		HeadersJSON: `{"X-Api-Key":"v1"}`,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	provider := newTestProvider(store)

	cred, err := provider.GetCredentialByMode(context.Background(), schemas.MCPAuthModeAdmin, "", "client-1")
	require.NoError(t, err)
	require.NotNil(t, cred)
	assert.Equal(t, "cred-admin", cred.ID)
	assert.Equal(t, "v1", cred.Headers["X-Api-Key"])
	assert.Equal(t, 1, store.credentialLookupCalls, "admin mode with an empty identity must reach the store layer")
}

// TestGetCredentialByMode_UserMode_EmptyIdentityStillRejected verifies the
// admin-only widening left every other mode's guard intact: MCPAuthModeUser
// with an empty identity must still return ErrHeadersCredentialNotFound
// without ever reaching the store — even though the store double would
// happily return a matching row for this mcp_client_id if called, proving
// the rejection comes from the guard itself, not from an absent row.
func TestGetCredentialByMode_UserMode_EmptyIdentityStillRejected(t *testing.T) {
	store := newTestConfigStore()
	store.credentials["client-1"] = &tables.TableMCPPerUserHeaderCredential{
		ID:          "cred-user",
		MCPClientID: "client-1",
		AuthMode:    "user",
		Status:      "active",
		HeadersJSON: `{}`,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	provider := newTestProvider(store)

	cred, err := provider.GetCredentialByMode(context.Background(), schemas.MCPAuthModeUser, "", "client-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, schemas.ErrHeadersCredentialNotFound)
	assert.Nil(t, cred)
	assert.Equal(t, 0, store.credentialLookupCalls, "the guard must short-circuit before reaching the store for non-admin modes with an empty identity")
}
