package mcp_headers

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
)

// writtenRecorder collects the IDs passed to an OnCredentialWritten callback.
type writtenRecorder struct {
	mu  sync.Mutex
	ids []string
}

func (r *writtenRecorder) record(_ context.Context, credentialID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, credentialID)
}

func (r *writtenRecorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

// userHeaderCredential builds a user-mode credential for one fixed binding.
func userHeaderCredential(id, value string) *schemas.MCPHeadersUserCredential {
	userID := "user-1"
	return &schemas.MCPHeadersUserCredential{
		ID:          id,
		MCPClientID: "client-1",
		AuthMode:    schemas.MCPAuthModeUser,
		UserID:      &userID,
		Headers:     map[string]string{"x-api-key": value},
		Status:      schemas.MCPHeadersUserCredentialStatusActive,
	}
}

// TestOnCredentialWrittenReportsStoredRowID: a resubmission arrives with a
// fresh ID, but the store reuses the binding's row, so the callback gets that
// row's ID, the one other processes have cached.
func TestOnCredentialWrittenReportsStoredRowID(t *testing.T) {
	p := newTestProvider(newTestConfigStore())
	rec := &writtenRecorder{}
	p.SetOnCredentialWritten(rec.record)
	ctx := context.Background()

	first := userHeaderCredential("fresh-1", "old")
	require.NoError(t, p.UpsertCredential(ctx, first))
	require.NoError(t, p.UpsertCredential(ctx, userHeaderCredential("fresh-2", "new")))
	require.NoError(t, p.DeleteCredential(ctx, first.ID))

	assert.Equal(t, []string{"fresh-1", "fresh-1", "fresh-1"}, rec.got())
}

// TestOnCredentialWrittenReachesAnotherProcess: two providers over one store
// stand in for two processes. Without the callback the second keeps serving
// values the first replaced; with it wired to the second's
// EvictCredentialByID, resubmissions and deletions reach it.
func TestOnCredentialWrittenReachesAnotherProcess(t *testing.T) {
	store := newTestConfigStore()
	writer, reader := newTestProvider(store), newTestProvider(store)
	ctx := context.Background()
	read := func() (*schemas.MCPHeadersUserCredential, error) {
		return reader.GetCredentialByMode(ctx, schemas.MCPAuthModeUser, "user-1", "client-1")
	}

	require.NoError(t, writer.UpsertCredential(ctx, userHeaderCredential("fresh-1", "v1")))
	cred, err := read()
	require.NoError(t, err)
	require.Equal(t, "v1", cred.Headers["x-api-key"])

	require.NoError(t, writer.UpsertCredential(ctx, userHeaderCredential("fresh-2", "v2")))
	cred, err = read()
	require.NoError(t, err)
	require.Equal(t, "v1", cred.Headers["x-api-key"], "with no callback the other process keeps its cached values")

	writer.SetOnCredentialWritten(func(_ context.Context, credentialID string) {
		reader.EvictCredentialByID(credentialID)
	})
	resubmitted := userHeaderCredential("fresh-3", "v3")
	require.NoError(t, writer.UpsertCredential(ctx, resubmitted))
	cred, err = read()
	require.NoError(t, err)
	assert.Equal(t, "v3", cred.Headers["x-api-key"], "a resubmission replaces the other process's cached values")

	require.NoError(t, writer.DeleteCredential(ctx, resubmitted.ID))
	_, err = read()
	assert.ErrorIs(t, err, schemas.ErrHeadersCredentialNotFound, "a deletion stops the other process serving the credential")
}

// TestEvictCredentialByIDDoesNotCallOnCredentialWritten: applying another
// process's eviction must not announce it again, or processes would echo it
// back and forth.
func TestEvictCredentialByIDDoesNotCallOnCredentialWritten(t *testing.T) {
	p := newTestProvider(newTestConfigStore())
	rec := &writtenRecorder{}
	p.SetOnCredentialWritten(rec.record)

	p.EvictCredentialByID("row-1")

	assert.Empty(t, rec.got())
}
