package warp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// retentionTestService indexes two requests, one a year old, and returns the
// service with the vector store they landed in.
func retentionTestService(t *testing.T) (*Service, *fakeWarpVectorStore) {
	t.Helper()
	vectors := newFakeWarpVectorStore()
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: assignTestRow()}), WithLogReader(topicTestReader(t, map[string]logstore.Log{})),
		WithVectorStore(vectors), WithEmbeddingExecutor(assignTestEmbedding),
	)
	t.Cleanup(service.Shutdown)
	now := time.Now().UTC()
	for id, at := range map[string]time.Time{"fresh": now.Add(-time.Hour), "stale": now.Add(-365 * 24 * time.Hour)} {
		text := "how do I get a refund"
		outcome, err := service.indexer.Index(context.Background(), &logstore.Log{
			ID: id, Timestamp: at, Object: string(schemas.ChatCompletionRequest), Status: "success",
			InputHistoryParsed: []schemas.ChatMessage{chatTurn(schemas.ChatMessageRoleUser, text)},
		})
		require.NoError(t, err)
		require.Equal(t, IndexOutcomeIndexed, outcome)
	}
	require.Len(t, vectors.adds, 2)
	return service, vectors
}

// retentionTestIDs lists the ids left in the namespace the test indexes into.
func retentionTestIDs(vectors *fakeWarpVectorStore) []string {
	return vectors.idsIn(assignTestRow().LogVectorStoreNamespace)
}

// A log a person deleted leaves no vector behind: semantic search and topic
// runs would otherwise keep finding an id with nothing behind it. An id with
// no vector is not an error.
func TestWarpForgetLogsDropsTheirVectors(t *testing.T) {
	service, vectors := retentionTestService(t)
	service.ForgetLogs(context.Background(), []string{"stale", "never-indexed"})
	require.Equal(t, []string{"fresh"}, retentionTestIDs(vectors))
}

// Deleting a log while Warp is switched off still removes its vector: the
// namespace is the stored one either way, and a vector left behind would come
// back in semantic search and topic runs once Warp is on again.
func TestWarpForgetLogsWhileWarpIsDisabled(t *testing.T) {
	service, vectors := retentionTestService(t)
	service.store.(*recordingStore).row.Enabled = false
	service.ForgetLogs(context.Background(), []string{"stale"})
	require.Equal(t, []string{"fresh"}, retentionTestIDs(vectors))
}

// Retention is age: the hourly upkeep sweeps the vectors of logs older than
// the logs' retention, reading the retention each time, and leaves the rest.
// Without a retention there is no sweep.
func TestWarpSweepsVectorsOfExpiredLogs(t *testing.T) {
	service, vectors := retentionTestService(t)
	now := time.Now().UTC()

	swept, err := service.sweepExpiredVectors(context.Background(), now)
	require.NoError(t, err)
	require.Zero(t, swept, "no retention, no sweep")
	require.Len(t, vectors.adds, 2)

	days := 0
	service.SetLogRetention(func() int { return days })
	swept, err = service.sweepExpiredVectors(context.Background(), now)
	require.NoError(t, err)
	require.Zero(t, swept, "a retention of zero keeps everything")

	days = 30
	swept, err = service.sweepExpiredVectors(context.Background(), now)
	require.NoError(t, err)
	require.Equal(t, 1, swept)
	require.Equal(t, []string{"fresh"}, retentionTestIDs(vectors))

	// Only Warp's own vectors: another owner's entry in the namespace stays.
	require.NoError(t, vectors.Add(context.Background(), assignTestRow().LogVectorStoreNamespace, "other", []float32{1, 0, 0}, map[string]interface{}{"timestamp": int64(0)}))
	swept, err = service.sweepExpiredVectors(context.Background(), now)
	require.NoError(t, err)
	require.Zero(t, swept)
	require.Contains(t, retentionTestIDs(vectors), "other")
}

// Retention runs whether or not Warp is switched on: the logs keep expiring
// while it is off, and their vectors must go with them, from the stored
// namespace. A deployment that never configured an embedding model indexed
// nothing, and is skipped without a word.
func TestWarpSweepsExpiredVectorsWhileWarpIsDisabled(t *testing.T) {
	service, vectors := retentionTestService(t)
	service.SetLogRetention(func() int { return 30 })
	service.store.(*recordingStore).row.Enabled = false
	service.topicUpkeepTick(context.Background())
	require.Equal(t, []string{"fresh"}, retentionTestIDs(vectors))

	never := NewService(nil, WithConfigStore(&recordingStore{}), WithVectorStore(newFakeWarpVectorStore()))
	t.Cleanup(never.Shutdown)
	never.SetLogRetention(func() int { return 30 })
	swept, err := never.sweepExpiredVectors(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	require.Zero(t, swept)
}

// refusingVectors is a vector store whose deletes fail while down is set, or
// once the context they are given has ended.
type refusingVectors struct {
	*fakeWarpVectorStore
	down bool
}

func (r *refusingVectors) Delete(ctx context.Context, namespace, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.down {
		return errors.New("vector store unavailable")
	}
	return r.fakeWarpVectorStore.Delete(ctx, namespace, id)
}

// flakyConfigStore fails to read the configuration while err is set.
type flakyConfigStore struct {
	*recordingStore
	err error
}

func (f *flakyConfigStore) GetWarpConfig(ctx context.Context) (*tables.TableWarpConfig, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.recordingStore.GetWarpConfig(ctx)
}

// pendingVectorDeletes lists what is waiting to be retried, by log id.
func pendingVectorDeletes(t *testing.T, service *Service) map[string]string {
	t.Helper()
	rows, err := service.topicStore().ListWarpVectorDeletes(context.Background(), 100)
	require.NoError(t, err)
	byLog := make(map[string]string, len(rows))
	for _, row := range rows {
		byLog[row.LogID] = row.Namespace
	}
	return byLog
}

// A delete whose vector cannot be removed - the vector store is down, or the
// request was cancelled - still succeeds, since the log is already gone, and
// the vector used to stay behind for good: nothing retried it, and with no
// retention set nothing swept it. Now each one is recorded and the hourly
// upkeep retries it until it goes, whether or not a topics job is running.
func TestWarpForgetLogsRetriesWhatTheVectorStoreRefused(t *testing.T) {
	service, fake := retentionTestService(t)
	vectors := &refusingVectors{fakeWarpVectorStore: fake, down: true}
	service.vectorStore = vectors
	namespace := assignTestRow().LogVectorStoreNamespace

	service.ForgetLogs(context.Background(), []string{"stale"})
	require.Equal(t, []string{"fresh", "stale"}, retentionTestIDs(fake), "the store refused")
	require.Equal(t, map[string]string{"stale": namespace}, pendingVectorDeletes(t, service), "and the delete is kept to retry")

	// A request that ended before its deletes ran is retried the same way.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	vectors.down = false
	service.ForgetLogs(cancelled, []string{"fresh", "never-indexed"})
	require.Equal(t, map[string]string{"stale": namespace, "fresh": namespace, "never-indexed": namespace}, pendingVectorDeletes(t, service))

	vectors.down = true
	service.topicUpkeepTick(context.Background())
	require.Len(t, pendingVectorDeletes(t, service), 3, "a pass that still cannot delete keeps every row")

	vectors.down = false
	service.topicUpkeepTick(context.Background())
	require.Empty(t, retentionTestIDs(fake), "the vectors are gone")
	require.Empty(t, pendingVectorDeletes(t, service), "and so are the rows; a vector already gone counts as removed")
}

// A configuration that cannot be read when the logs are deleted leaves the
// namespace unknown. The deletes are kept without one, and the retry reads
// the namespace configured by then.
func TestWarpForgetLogsRetriesWhenTheNamespaceIsUnknown(t *testing.T) {
	service, fake := retentionTestService(t)
	config := &flakyConfigStore{recordingStore: service.store.(*recordingStore), err: errors.New("config store unavailable")}
	service.store = config

	service.ForgetLogs(context.Background(), []string{"stale"})
	require.Equal(t, map[string]string{"stale": ""}, pendingVectorDeletes(t, service))
	require.Equal(t, []string{"fresh", "stale"}, retentionTestIDs(fake))

	config.err = nil
	service.topicUpkeepTick(context.Background())
	require.Equal(t, []string{"fresh"}, retentionTestIDs(fake))
	require.Empty(t, pendingVectorDeletes(t, service))
}

// Nothing is recorded where nothing was indexed: a deployment with a vector
// store but no embedding model never wrote a Warp vector, so a delete there
// writes no row however the vector store is doing.
func TestWarpForgetLogsRecordsNothingWithoutAnEmbeddingModel(t *testing.T) {
	service, fake := retentionTestService(t)
	service.vectorStore = &refusingVectors{fakeWarpVectorStore: fake, down: true}
	service.store.(*recordingStore).row.EmbeddingModel = ""
	service.ForgetLogs(context.Background(), []string{"stale"})
	require.Empty(t, pendingVectorDeletes(t, service))
}
