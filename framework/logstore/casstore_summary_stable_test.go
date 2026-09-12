package logstore

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCas_HydrationPreservesPersistentContentSummary(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	entry := bigChatEntry("summary-stable", strings.Repeat("large context ", 40), "last user")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(context.Background(), entry))

	row, err := inner.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	require.NotEmpty(t, row.ContentSummary)
	persistedSummary := row.ContentSummary

	found, err := cas.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	require.Equal(t, persistedSummary, found.ContentSummary)
	require.Equal(t, entry.InputHistory, found.InputHistory)
}
