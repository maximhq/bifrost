package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClickHouseProviderRequestIDParity(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.Create(ctx, providerRequestIDTestLog("ch-match", "req-ch-exact", now)))
	require.NoError(t, store.Create(ctx, providerRequestIDTestLog("ch-other", "req-ch-exact-suffix", now.Add(-time.Second))))

	result, err := store.SearchLogs(ctx, SearchFilters{ProviderRequestID: "req-ch-exact"}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	require.Equal(t, "ch-match", result.Logs[0].ID)
	require.Equal(t, "req-ch-exact", result.Logs[0].ProviderRequestID)
	require.Equal(t, "x-request-id", result.Logs[0].ProviderRequestIDHeader)

	detail, err := store.FindByID(ctx, "ch-match")
	require.NoError(t, err)
	require.Len(t, detail.ProviderRequestIDTrailParsed, 1)
	require.Equal(t, "req-ch-exact", detail.ProviderRequestIDTrailParsed[0].RequestID)
}
