package logstore

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCas_HiddenSmallPayloadStillPersisted(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	entry := bigChatEntry("hidden-small", strings.Repeat("large hidden context ", 20))
	entry.ContentHidden = true
	entry.Tools = `[{"name":"tiny"}]`
	require.Less(t, len(entry.Tools), cas.minFieldBytes)
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(context.Background(), entry))

	row, err := inner.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	require.Empty(t, row.Tools, "hidden row must not expose content inline")
	var pointers int64
	require.NoError(t, cas.db.Model(&casPayload{}).Where("log_id = ? AND field = ?", entry.ID, "tools").Count(&pointers).Error)
	require.Equal(t, int64(1), pointers, "hidden nonempty payload must have a durable CAS pointer even below the normal threshold")
	served, err := cas.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	require.Empty(t, served.Tools, "hidden payload must remain inaccessible through serving reads")
}
