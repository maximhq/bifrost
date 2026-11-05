package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The capture's failed deletes land in complexity_vector_deletes and come
// back as they went in, namespace included, until the retry clears them.
func TestComplexityCaptureDeletesRoundTripThroughLogStore(t *testing.T) {
	ctx := context.Background()
	ls, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true, Type: logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")},
	}, noopTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ls.Close(ctx) })
	recorder := complexityCaptureDeletes{store: ls}

	require.NoError(t, recorder.AddCaptureDeletes(ctx, nil))
	require.NoError(t, recorder.AddCaptureDeletes(ctx, []complexity.CaptureDelete{
		{LogID: "a", Namespace: "BifrostComplexityRequests_abc"},
		{LogID: "b"},
	}))
	rows, err := recorder.ListCaptureDeletes(ctx, 10)
	require.NoError(t, err)
	assert.ElementsMatch(t, []complexity.CaptureDelete{
		{LogID: "a", Namespace: "BifrostComplexityRequests_abc"},
		{LogID: "b"},
	}, rows)

	require.NoError(t, recorder.DeleteCaptureDeletes(ctx, []string{"a"}))
	rows, err = recorder.ListCaptureDeletes(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, []complexity.CaptureDelete{{LogID: "b"}}, rows)

	// Warp's retry list is a different table: neither sees the other's rows.
	warp, err := ls.ListWarpVectorDeletes(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, warp)
}
