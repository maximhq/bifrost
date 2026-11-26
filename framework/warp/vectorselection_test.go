package warp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeVectorStores resolves the stores a test configures: primary is the
// top-level vector_store, named the others.
type fakeVectorStores struct {
	primary *vectorstore.NamedStore
	named   []*vectorstore.NamedStore
}

func (f *fakeVectorStores) Primary() *vectorstore.NamedStore { return f.primary }

func (f *fakeVectorStores) Get(name string) (*vectorstore.NamedStore, bool) {
	for _, entry := range append([]*vectorstore.NamedStore{f.primary}, f.named...) {
		if entry != nil && entry.Name == name {
			return entry, true
		}
	}
	return nil, false
}

// warpStoresForTest is a primary store "shared", a second connected store
// "warp-qdrant", one that failed at startup and one that is disabled.
func warpStoresForTest() (*fakeVectorStores, *fakeWarpVectorStore, *fakeWarpVectorStore) {
	shared, qdrant := newFakeWarpVectorStore(), newFakeWarpVectorStore()
	return &fakeVectorStores{
		primary: &vectorstore.NamedStore{Name: "shared", Type: "weaviate", ID: "weaviate+http://shared", Primary: true, Status: vectorstore.StoreConnected, Store: shared},
		named: []*vectorstore.NamedStore{
			{Name: "warp-qdrant", Type: "qdrant", ID: "qdrant://warp:6334", Status: vectorstore.StoreConnected, Store: qdrant},
			{Name: "down", Type: "redis", ID: "redis://down:6379/0", Status: vectorstore.StoreFailed, Err: errors.New("connection refused")},
			{Name: "off", Type: "qdrant", ID: "qdrant://off:6334", Status: vectorstore.StoreDisabled},
		},
	}, shared, qdrant
}

// Warp keeps its log index in the store config.json names, the primary one when
// it names none: saves provision the namespace there, logs are indexed there,
// and the settings view names it.
func TestWarpKeepsItsIndexInTheStoreConfigJSONNames(t *testing.T) {
	ctx := context.Background()
	for name, want := range map[string]ConnectedVectorStore{
		"":            {Name: "shared", Type: "weaviate", Primary: true},
		"warp-qdrant": {Name: "warp-qdrant", Type: "qdrant"},
	} {
		t.Run("named "+want.Name, func(t *testing.T) {
			stores, shared, qdrant := warpStoresForTest()
			chosen := map[string]*fakeWarpVectorStore{"shared": shared, "warp-qdrant": qdrant}[want.Name]
			store := &recordingStore{}
			service := serviceForTest(t, nil, WithConfigStore(store), WithVectorStores(stores, name),
				WithLogReader(topicTestReader(t, map[string]logstore.Log{})), WithEmbeddingExecutor(assignTestEmbedding))

			view, err := service.SaveConfig(ctx, validWarpConfigInput())
			require.NoError(t, err)
			assert.Equal(t, name, view.VectorStore)
			require.NotNil(t, view.ConnectedVectorStore)
			assert.Equal(t, want, *view.ConnectedVectorStore)
			assert.True(t, view.VectorStoreConnected)
			assert.Contains(t, chosen.created, schemas.WarpDefaultLogVectorStoreNamespace, "the namespace is provisioned in it")

			store.row.EmbeddingDimension = 3
			outcome, err := service.indexer.Index(ctx, &logstore.Log{
				ID: "log-1", Timestamp: time.Now(), Object: string(schemas.ChatCompletionRequest), Status: "success",
				InputHistoryParsed: []schemas.ChatMessage{chatTurn(schemas.ChatMessageRoleUser, "how do I get a refund")},
			})
			require.NoError(t, err)
			require.Equal(t, IndexOutcomeIndexed, outcome)
			assert.Equal(t, []string{"log-1"}, chosen.idsIn(schemas.WarpDefaultLogVectorStoreNamespace), "and logs are indexed in it")
		})
	}
}

// The store is chosen in config.json and read at startup, so a save that names
// another one is refused rather than ignored. One that repeats it is a client
// saving back the configuration it read, and goes through.
func TestWarpSaveCannotChangeTheVectorStore(t *testing.T) {
	for configured, other := range map[string]string{"": "warp-qdrant", "warp-qdrant": ""} {
		t.Run("configured "+configured, func(t *testing.T) {
			stores, _, _ := warpStoresForTest()
			store := &recordingStore{row: validWarpConfigRow()}
			service := serviceForTest(t, nil, WithConfigStore(store), WithVectorStores(stores, configured))
			save := func(vectorStore string) error {
				_, err := service.SaveConfig(context.Background(), inputFromJSON(t, `{"enabled": true, "provider": "openai", "model": "gpt-4o", "vector_store": "`+vectorStore+`"}`))
				return err
			}

			err := save(other)
			require.ErrorIs(t, err, ErrInvalidConfig)
			assert.ErrorContains(t, err, "vector_store is set in config.json (warp.vector_store) and takes effect at restart")
			assert.Empty(t, store.upserted)

			require.NoError(t, save(configured), "repeating the store config.json names is a save of the rest")
			assert.Len(t, store.upserted, 1)
		})
	}
}

// A store config.json names that is not configured, or not usable at startup,
// leaves Warp with no store, never another one: the view says why, there is no
// semantic search, enabling Warp names the store, and a backfill or topics run
// says which store to fix. A Warp already on still saves its other settings,
// and turning Warp off is always allowed.
func TestWarpWithAnUnusableVectorStoreIsUnavailable(t *testing.T) {
	ctx := context.Background()
	for name, want := range map[string]string{
		"down":    `Warp's vector store "down" could not be reached at startup`,
		"off":     `Warp's vector store "off" is disabled in config.json`,
		"removed": `Warp's vector store "removed" is not configured in config.json`,
	} {
		t.Run(name, func(t *testing.T) {
			stores, shared, _ := warpStoresForTest()
			row := validWarpConfigRow()
			row.Enabled = false
			store := &recordingStore{row: row}
			service := serviceForTest(t, nil, WithConfigStore(store), WithVectorStores(stores, name),
				WithVectorStore(shared), WithEmbeddingExecutor(backfillEmbeddingExecutor), WithLogReader(&backfillLogReader{}))

			view, err := service.ConfigView(ctx)
			require.NoError(t, err)
			assert.Equal(t, name, view.VectorStore)
			assert.Nil(t, view.ConnectedVectorStore)
			assert.False(t, view.VectorStoreConnected)
			assert.Equal(t, want, view.VectorStoreError)
			_, semantic := service.researchDeps()
			assert.Nil(t, semantic, "no semantic search, and no other store stands in")

			_, err = service.SaveConfig(ctx, inputFromJSON(t, `{"enabled": true, "provider": "openai", "model": "gpt-4o"}`))
			require.ErrorIs(t, err, ErrNoVectorStore, "turning Warp on needs the store")
			var unavailable *VectorStoreUnavailableError
			require.ErrorAs(t, err, &unavailable)
			assert.Equal(t, name, unavailable.Name)

			store.row.Enabled = true // on before the restart that lost the store
			_, err = service.SaveConfig(ctx, inputFromJSON(t, `{"enabled": true, "provider": "openai", "model": "gpt-4o", "system_prompt_suffix": "prod keys start with prod-"}`))
			require.NoError(t, err, "a Warp already on still saves its other settings")
			require.NotNil(t, store.row.SystemPromptSuffix)
			assert.Equal(t, "prod keys start with prod-", *store.row.SystemPromptSuffix)
			assert.True(t, store.row.Enabled)
			_, err = service.SaveConfig(ctx, inputFromJSON(t, `{"enabled": false, "provider": "openai", "model": "gpt-4o"}`))
			require.NoError(t, err, "turning Warp off is allowed")

			_, err = service.BuildBackfillJobMeta(ctx, time.Now().Add(-time.Hour), time.Now(), false)
			require.ErrorIs(t, err, ErrNoVectorStore)
			assert.ErrorContains(t, err, want)
		})
	}
}

// While the store config.json names is configured but could not be used at
// startup, it still holds the index: a deleted log's delete is queued for a start
// that reaches it. A store not configured at all holds nothing reachable, so
// nothing is queued for it.
func TestWarpDeletesWaitForAStoreThatCouldNotBeReached(t *testing.T) {
	ctx := context.Background()
	for name, queued := range map[string]bool{"down": true, "removed": false} {
		t.Run(name, func(t *testing.T) {
			stores, _, _ := warpStoresForTest()
			service := serviceForTest(t, nil, WithConfigStore(&recordingStore{row: validWarpConfigRow()}), WithVectorStores(stores, name),
				WithLogReader(topicTestReader(t, map[string]logstore.Log{})))
			service.ForgetLogs(ctx, []string{"gone"})
			if !queued {
				assert.Empty(t, pendingVectorDeletes(t, service))
				return
			}
			assert.Equal(t, map[string]string{"gone": schemas.WarpDefaultLogVectorStoreNamespace}, pendingVectorDeletes(t, service))
			done, err := service.retryVectorDeletes(ctx)
			require.NoError(t, err)
			assert.Zero(t, done)
			assert.Contains(t, pendingVectorDeletes(t, service), "gone", "still queued while the store cannot be reached")
		})
	}
}

// A backfill checkpoint names the data it indexed into. After a restart where
// config.json names another store, or the same name reaches other data (another
// endpoint, or a memory-only store started afresh), the old run is not shown as
// covering the index, does not resume, and does not run. A checkpoint from before
// stores were recorded was written into the primary store.
func TestWarpBackfillCheckpointIsForTheDataItWasWrittenInto(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	cursor := start.Add(30 * time.Minute)
	store := &recordingStore{row: validWarpConfigRow()}
	serviceOn := func(stores *fakeVectorStores, name string, jobs BackfillJobStore) *Service {
		opts := []Option{WithConfigStore(store), WithLogReader(&backfillLogReader{}), WithVectorStores(stores, name), WithEmbeddingExecutor(backfillEmbeddingExecutor)}
		if jobs != nil {
			opts = append(opts, WithBackfillJobStore(jobs))
		}
		return serviceForTest(t, nil, opts...)
	}
	stores, _, _ := warpStoresForTest()
	built, err := serviceOn(stores, "", nil).BuildBackfillJobMeta(ctx, start, end, false)
	require.NoError(t, err)
	var stopped BackfillJobMeta
	require.NoError(t, sonic.Unmarshal([]byte(built), &stopped))
	assert.Equal(t, "weaviate+http://shared", stopped.VectorStoreID)
	stopped.CursorTime, stopped.Total, stopped.Scanned = &cursor, 10, 5
	stoppedJSON, err := marshalBackfillMeta(stopped)
	require.NoError(t, err)
	jobs := activeBackfillStore{active: &tables.TableSidekiqJob{ID: "stopped", Status: tables.SidekiqStatusFailed, Metadata: stoppedJSON}}
	resume := func(service *Service) BackfillJobMeta {
		t.Helper()
		metaJSON, err := service.BuildBackfillJobMeta(ctx, start, end, false)
		require.NoError(t, err)
		var meta BackfillJobMeta
		require.NoError(t, sonic.Unmarshal([]byte(metaJSON), &meta))
		return meta
	}

	same := serviceOn(stores, "", jobs)
	assert.True(t, same.BackfillMatchesStore(stoppedJSON))
	assert.Equal(t, 5, resume(same).Scanned, "the same store resumes from the checkpoint")
	legacy := `{"config_signature":"x","namespace":"BifrostWarpLogs"}`
	assert.True(t, same.BackfillMatchesStore(legacy), "a checkpoint from before stores were recorded is the primary store's")

	for name, service := range map[string]*Service{
		"another store": serviceOn(stores, "warp-qdrant", jobs),
		"other data under the same name": func() *Service {
			restarted, _, _ := warpStoresForTest()
			restarted.primary.ID = "chromem#restarted"
			return serviceOn(restarted, "", jobs)
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			assert.False(t, service.BackfillMatchesStore(stoppedJSON), "a finished run is not shown for data it never wrote")
			fresh := resume(service)
			assert.Zero(t, fresh.Scanned, "a stopped run does not resume")
			assert.Nil(t, fresh.CursorTime)
			_, err := service.RunBackfillJob(ctx, tables.TableSidekiqJob{Metadata: stoppedJSON}, func(string) error { return nil })
			require.ErrorContains(t, err, "Warp's vector store changed since this backfill was started")
		})
	}
}

// With the store config.json names unreachable, a delete is queued even when the configuration
// cannot be read: the retry reads the namespace once it can, as on a connected store.
func TestWarpDeletesWaitForAnUnreachableStoreWhenTheConfigurationCannotBeRead(t *testing.T) {
	stores, _, _ := warpStoresForTest()
	config := &flakyConfigStore{recordingStore: &recordingStore{row: validWarpConfigRow()}, err: errors.New("database is closed")}
	service := serviceForTest(t, nil, WithConfigStore(config), WithVectorStores(stores, "down"),
		WithLogReader(topicTestReader(t, map[string]logstore.Log{})))
	service.ForgetLogs(context.Background(), []string{"gone"})
	assert.Equal(t, map[string]string{"gone": ""}, pendingVectorDeletes(t, service), "queued, the namespace left to the retry")
}
