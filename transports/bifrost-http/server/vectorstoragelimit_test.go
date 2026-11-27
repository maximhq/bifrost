package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedStore hides Chromem's type, so captures, which refuse a local Chromem
// store, are kept in it as in a shared one.
type sharedStore struct{ vectorstore.VectorStore }

// storageLimitTestServer has a shared store holding one router's captures, four wide,
// one an hour for the last 2,000 hours: about 2 MB by the estimate.
func storageLimitTestServer(t *testing.T, maxSizeMB int, now time.Time) (*BifrostHTTPServer, vectorstore.VectorStore, string) {
	t.Helper()
	SetLogger(bifrost.NewNoOpLogger())
	inner, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true, Type: vectorstore.VectorStoreTypeChromem, Config: vectorstore.ChromemConfig{},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	store := sharedStore{inner}
	ctx := context.Background()
	namespace := complexity.CaptureNamespace("router-1", schemas.OpenAI, "text-embedding-3-small", 4, 1)
	require.NoError(t, store.CreateNamespace(ctx, namespace, 4, nil))
	for hour := 1; hour <= 2000; hour++ {
		require.NoError(t, store.Add(ctx, namespace, fmt.Sprintf("capture-%04d", hour), []float32{1, 0, 0, 0}, map[string]interface{}{
			"kind": "request", "timestamp": now.Add(-time.Duration(hour) * time.Hour).Unix(),
		}))
	}
	s := &BifrostHTTPServer{Config: &lib.Config{
		ClientConfig: &configstore.ClientConfig{VectorMaxSizeMB: maxSizeMB},
		VectorStore:  store,
	}}
	return s, store, namespace
}

// The pass finds the router's captures in the shared store, without the
// routing plugin, and, past 80% of the limit, trims them to 65% of it,
// oldest first: 1 MB at 1,040 bytes a vector keeps the newest 655.
func TestVectorStorageLimitTrimsCapturesInTheSharedStore(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	s, store, namespace := storageLimitTestServer(t, 1, now)
	ctx := context.Background()

	indexes, err := s.vectorStorageLimitIndexes(ctx)
	require.NoError(t, err)
	require.Len(t, indexes, 1)
	assert.Equal(t, namespace, indexes[0].Namespace)

	s.enforceVectorStorageLimit(ctx, now)
	kept, err := store.CountVectors(ctx, namespace, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 655, kept)
	_, err = store.GetChunk(ctx, namespace, "capture-0001")
	assert.NoError(t, err, "the newest is kept")
	_, err = store.GetChunk(ctx, namespace, "capture-0656")
	assert.Error(t, err, "the oldest go")
}

// Without a limit, or under 80% of it, the pass deletes nothing; the limit
// is read at each pass.
func TestVectorStorageLimitLeavesVectorsAloneWithoutALimit(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	s, store, namespace := storageLimitTestServer(t, 0, now)
	ctx := context.Background()
	s.enforceVectorStorageLimit(ctx, now)
	s.Config.ClientConfig.VectorMaxSizeMB = 3
	s.enforceVectorStorageLimit(ctx, now)
	kept, err := store.CountVectors(ctx, namespace, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 2000, kept)

	s.Config.ClientConfig.VectorMaxSizeMB = -1
	assert.Zero(t, s.vectorStorageLimitBytes(), "a negative limit reads as none")
	s.Config.ClientConfig.VectorMaxSizeMB = 512
	assert.EqualValues(t, 512<<20, s.vectorStorageLimitBytes())
}
