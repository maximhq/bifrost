package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProviderResponseIDServedByListAndByID writes a row carrying the provider's response
// identifier and checks that both the list projection and FindByID return it, since a
// caller joining log rows to responses reads it from the list.
func TestProviderResponseIDServedByListAndByID(t *testing.T) {
	ctx := context.Background()
	store := newProjectionTestStore(t)
	responseID := "msg_011ChQDewfwJ3BMDLzXNoVjS"
	withID := &Log{
		ID: "with-response-id", Timestamp: time.Now().UTC(), Object: "chat_completion",
		Provider: "anthropic", Model: "claude-haiku-4-5", Status: "success",
		ProviderResponseID: &responseID,
	}
	withoutID := &Log{
		ID: "without-response-id", Timestamp: time.Now().UTC(), Object: "chat_completion",
		Provider: "anthropic", Model: "claude-haiku-4-5", Status: "error",
	}
	for _, entry := range []*Log{withID, withoutID} {
		require.NoError(t, entry.SerializeFields())
		require.NoError(t, store.Create(ctx, entry))
	}

	assert.True(t, containsColumn(store.listSelectColumns(), "provider_response_id"))

	list, err := store.SearchLogs(ctx, SearchFilters{RequestID: "with-response-id"}, PaginationOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, list.Logs, 1)
	require.NotNil(t, list.Logs[0].ProviderResponseID)
	assert.Equal(t, responseID, *list.Logs[0].ProviderResponseID)

	detail, err := store.FindByID(ctx, "with-response-id")
	require.NoError(t, err)
	require.NotNil(t, detail.ProviderResponseID)
	assert.Equal(t, responseID, *detail.ProviderResponseID)

	empty, err := store.FindByID(ctx, "without-response-id")
	require.NoError(t, err)
	assert.Nil(t, empty.ProviderResponseID)
}
