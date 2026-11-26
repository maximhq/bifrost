package server

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedStore hides Chromem's type, so captures, which refuse a local Chromem
// store, are kept in it as in a shared one.
type sharedStore struct{ vectorstore.VectorStore }

// vectorStoreTestConfig is a config with a SQLite config store, its live store, and
// a registry of stores: the primary first, named, then the named ones, each
// opened by connect.
func vectorStoreTestConfig(t *testing.T, connect func(context.Context, *vectorstore.Config, schemas.Logger) (vectorstore.VectorStore, error), primary *vectorstore.Config, named ...vectorstore.Config) *lib.Config {
	t.Helper()
	cfg := libtest.Config(t, nil, nil)
	registry, err := vectorstore.OpenRegistry(context.Background(), primary, named, nil, vectorstore.RegistryOptions{Connect: connect})
	require.NoError(t, err)
	cfg.VectorStores = registry
	if p := registry.Primary(); p != nil {
		cfg.VectorStore = p.Store
	}
	return cfg
}

// setVectorStoreSettings writes a store's settings and reloads them, as a save does.
func setVectorStoreSettings(t *testing.T, cfg *lib.Config, name string, retentionDays, maxSizeMB int) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, cfg.ConfigStore.UpsertVectorStoreSettings(ctx, &configstoreTables.TableVectorStoreSettings{Name: name, RetentionDays: retentionDays, MaxSizeMB: maxSizeMB}))
	require.NoError(t, cfg.LiveStore.Refresh(ctx, []livestore.Ref{{Kind: livestore.KindVectorStoreSettings, ID: name}}))
}

// fillCaptures writes one router's captures to store, four wide, one an hour
// for the last 2,000 hours: about 2 MB by the estimate.
func fillCaptures(t *testing.T, store vectorstore.VectorStore, router string, now time.Time) string {
	t.Helper()
	ctx := context.Background()
	namespace := complexity.CaptureNamespace(router, schemas.OpenAI, "text-embedding-3-small", 4, 1)
	require.NoError(t, store.CreateNamespace(ctx, namespace, 4, nil))
	for hour := 1; hour <= 2000; hour++ {
		require.NoError(t, store.Add(ctx, namespace, fmt.Sprintf("capture-%04d", hour), []float32{1, 0, 0, 0}, map[string]interface{}{
			"kind": "request", "timestamp": now.Add(-time.Duration(hour) * time.Hour).Unix(),
		}))
	}
	return namespace
}

// sharedChromem opens a memory-only Chromem store behind sharedStore.
func sharedChromem(ctx context.Context, config *vectorstore.Config, logger schemas.Logger) (vectorstore.VectorStore, error) {
	inner, err := vectorstore.NewVectorStore(ctx, &vectorstore.Config{Enabled: true, Type: vectorstore.VectorStoreTypeChromem, Config: vectorstore.ChromemConfig{}}, logger)
	if err != nil {
		return nil, err
	}
	return sharedStore{inner}, nil
}

// storageLimitTestServer has one store, "shared", holding one router's captures
// (see fillCaptures), with the given storage limit.
func storageLimitTestServer(t *testing.T, maxSizeMB int, now time.Time) (*BifrostHTTPServer, vectorstore.VectorStore, string) {
	t.Helper()
	SetLogger(bifrost.NewNoOpLogger())
	cfg := vectorStoreTestConfig(t, sharedChromem, &vectorstore.Config{Name: "shared", Enabled: true, Type: vectorstore.VectorStoreTypeChromem})
	namespace := fillCaptures(t, cfg.VectorStore, "router-1", now)
	setVectorStoreSettings(t, cfg, "shared", 0, maxSizeMB)
	return &BifrostHTTPServer{Config: cfg}, cfg.VectorStore, namespace
}

// The pass finds the router's captures in the store, without the routing
// plugin, and, past 80% of the store's limit, trims them to 65% of it, oldest
// first: 1 MB at 1,040 bytes a vector keeps the newest 655.
func TestVectorStorageLimitTrimsCapturesInTheSharedStore(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	s, store, namespace := storageLimitTestServer(t, 1, now)
	ctx := context.Background()

	indexes, err := s.vectorStorageLimitIndexes(ctx, store)
	require.NoError(t, err)
	require.Len(t, indexes, 1)
	assert.Equal(t, namespace, indexes[0].Namespace)

	s.enforceVectorStorageLimits(ctx, now)
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
	s.enforceVectorStorageLimits(ctx, now)
	setVectorStoreSettings(t, s.Config, "shared", 0, 3)
	s.enforceVectorStorageLimits(ctx, now)
	kept, err := store.CountVectors(ctx, namespace, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 2000, kept)

	setVectorStoreSettings(t, s.Config, "shared", 0, -1)
	assert.Zero(t, s.vectorStorageLimitBytes("shared"), "a negative limit reads as none")
	setVectorStoreSettings(t, s.Config, "shared", 0, 512)
	assert.EqualValues(t, 512<<20, s.vectorStorageLimitBytes("shared"))
	assert.Zero(t, s.vectorStorageLimitBytes("no-such-store"), "a store without settings has no limit")
}

// Each store is trimmed by its own limit, and only what it holds: one over its
// limit is trimmed while another, under its own, is left alone. A name that
// shares another's connection is that store, under the owner's settings, so
// it is not trimmed again by a limit of its own.
func TestVectorStorageLimitAppliesPerStore(t *testing.T) {
	SetLogger(bifrost.NewNoOpLogger())
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	disk := t.TempDir()
	onDisk := func(name, dir string) vectorstore.Config {
		return vectorstore.Config{Name: name, Enabled: true, Type: vectorstore.VectorStoreTypeChromem, Config: vectorstore.ChromemConfig{Path: filepath.Join(disk, dir)}}
	}
	openShared := func(ctx context.Context, config *vectorstore.Config, logger schemas.Logger) (vectorstore.VectorStore, error) {
		inner, err := vectorstore.NewVectorStore(ctx, config, logger)
		if err != nil {
			return nil, err
		}
		return sharedStore{inner}, nil
	}
	primary := onDisk("tight", "a")
	cfg := vectorStoreTestConfig(t, openShared, &primary, onDisk("roomy", "b"), onDisk("alias", "a"))
	tight, _ := cfg.VectorStores.Get("tight")
	roomy, _ := cfg.VectorStores.Get("roomy")
	alias, _ := cfg.VectorStores.Get("alias")
	require.Equal(t, "tight", alias.SharedWith)
	tightNS := fillCaptures(t, tight.Store, "router-1", now)
	roomyNS := fillCaptures(t, roomy.Store, "router-2", now)
	setVectorStoreSettings(t, cfg, "tight", 0, 1)
	setVectorStoreSettings(t, cfg, "roomy", 0, 100)
	setVectorStoreSettings(t, cfg, "alias", 0, 0) // its own limit would be none: the owner's applies

	(&BifrostHTTPServer{Config: cfg}).enforceVectorStorageLimits(ctx, now)
	kept, err := tight.Store.CountVectors(ctx, tightNS, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 655, kept, "the store over its limit is trimmed")
	kept, err = roomy.Store.CountVectors(ctx, roomyNS, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 2000, kept, "a store under its own limit is left alone")
}
