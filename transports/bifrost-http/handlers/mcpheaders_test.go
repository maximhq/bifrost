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
