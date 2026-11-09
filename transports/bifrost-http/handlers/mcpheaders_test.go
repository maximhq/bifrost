package handlers

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestHeadersCredentialRevoke_DropsItsDiscovery revokes a caller's header
// credential and their discovery with it, on every node.
func TestHeadersCredentialRevoke_DropsItsDiscovery(t *testing.T) {
	cfg, store := newSignInConfig(t)
	client := headersNotesClient()
	createSignInClient(t, store, client, true)
	cred := &tables.TableMCPPerUserHeaderCredential{MCPClientID: client.ID, AuthMode: "user", UserID: schemas.Ptr("alice"), Status: "active", HeadersJSON: `{"x-api-key":"k"}`}
	require.NoError(t, store.UpsertMCPPerUserHeaderCredential(context.Background(), cred))
	seedDiscovery(t, store, schemas.MCPAuthModeUser, "alice", client.ID)
	log := &evictionLog{}

	require.Equal(t, fasthttp.StatusNoContent, callRevoke(NewMCPPerUserHeadersHandler(&signInMCPManager{}, log, cfg, nil).revoke, cred.ID))

	assert.Nil(t, storedDiscovery(store, schemas.MCPAuthModeUser, "alice", client.ID))
	assert.Contains(t, log.logged(), "discovery user/alice/"+client.ID, "every node drops its cached copy")
}

// TestHeadersCredentialRevoke_LeavesOthersCredentialsToCallersTheServerAllows revokes a user's
// header credential: the user always may, anyone else only when the server lets them act on
// others' credentials. A refused revoke answers 403 and keeps the credential.
func TestHeadersCredentialRevoke_LeavesOthersCredentialsToCallersTheServerAllows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caller string
		others bool
		want   int
	}{
		{"by its user, kept to their own", "alice", false, fasthttp.StatusNoContent},
		{"by another user, kept to their own", "bob", false, fasthttp.StatusForbidden},
		{"by another user, allowed others'", "bob", true, fasthttp.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store := newSignInConfig(t)
			client := headersNotesClient()
			createSignInClient(t, store, client, true)
			cred := &tables.TableMCPPerUserHeaderCredential{MCPClientID: client.ID, AuthMode: "user", UserID: schemas.Ptr("alice"), Status: "active", HeadersJSON: `{"x-api-key":"k"}`}
			require.NoError(t, store.UpsertMCPPerUserHeaderCredential(context.Background(), cred))
			policy := credentialsPolicy{others: tc.others}

			status := callRevokeAs(NewMCPPerUserHeadersHandler(policy, &evictionLog{}, cfg, nil).revoke, cred.ID, tc.caller)

			require.Equal(t, tc.want, status)
			kept, err := store.GetMCPPerUserHeaderCredentialByID(context.Background(), cred.ID)
			require.NoError(t, err)
			assert.Equal(t, tc.want == fasthttp.StatusForbidden, kept != nil, "a refused revoke keeps the credential")
		})
	}
}
