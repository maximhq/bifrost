package configstore

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupProviderObjectTestStore extends the base test store with the provider_objects
// and batch_jobs tables through the same migration a live store runs.
func setupProviderObjectTestStore(t *testing.T) *RDBConfigStore {
	t.Helper()
	store := setupBatchJobTestStore(t)
	require.NoError(t, migrationAddProviderObjectsTable(context.Background(), store.DB(), testMigrationLogger))
	return store
}

func TestProviderObjectID(t *testing.T) {
	assert.Equal(t, "file:openai:file-abc", tables.ProviderObjectID(tables.ProviderObjectKindFile, "openai", "file-abc"))
	assert.NotEqual(t,
		tables.ProviderObjectID(tables.ProviderObjectKindVideo, "openai", "id_1"),
		tables.ProviderObjectID(tables.ProviderObjectKindFile, "openai", "id_1"),
		"the same provider id under two kinds is two objects")
}

// The first creator recorded for an object keeps it: a later write for the same id
// does not move the object to another key, and a delete forgets it.
func TestProviderObjectFirstCreatorWinsAndDeleteForgets(t *testing.T) {
	store := setupProviderObjectTestStore(t)
	ctx := context.Background()
	id := tables.ProviderObjectID(tables.ProviderObjectKindFile, "openai", "file-1")

	require.NoError(t, store.UpsertProviderObject(ctx, &tables.TableProviderObject{
		ID: id, Kind: tables.ProviderObjectKindFile, Provider: "openai", ObjectID: "file-1", VirtualKeyID: "vk-a",
	}))
	require.NoError(t, store.UpsertProviderObject(ctx, &tables.TableProviderObject{
		ID: id, Kind: tables.ProviderObjectKindFile, Provider: "openai", ObjectID: "file-1", VirtualKeyID: "vk-b",
	}))

	objects, err := store.GetProviderObjectsByIDs(ctx, []string{id, tables.ProviderObjectID(tables.ProviderObjectKindFile, "openai", "file-unknown")})
	require.NoError(t, err)
	require.Len(t, objects, 1, "ids with no row are absent, not reported")
	assert.Equal(t, "vk-a", objects[0].VirtualKeyID, "the first creator keeps the object")
	assert.False(t, objects[0].CreatedAt.IsZero())

	require.NoError(t, store.DeleteProviderObject(ctx, id))
	require.NoError(t, store.DeleteProviderObject(ctx, id), "deleting a forgotten object is not an error")
	objects, err = store.GetProviderObjectsByIDs(ctx, []string{id})
	require.NoError(t, err)
	assert.Empty(t, objects)

	empty, err := store.GetProviderObjectsByIDs(ctx, nil)
	require.NoError(t, err)
	assert.Nil(t, empty)
}

func TestUpsertProviderObjectRequiresIdentity(t *testing.T) {
	store := setupProviderObjectTestStore(t)
	ctx := context.Background()
	assert.Error(t, store.UpsertProviderObject(ctx, nil))
	assert.Error(t, store.UpsertProviderObject(ctx, &tables.TableProviderObject{ID: "x", Kind: "file", Provider: "openai", ObjectID: "f"}), "a creator is required")
	assert.Error(t, store.DeleteProviderObject(ctx, ""))
}

// A file is found through any of the three file columns of a batch row, and only on
// its own provider.
func TestGetProviderJobsByFileIDs(t *testing.T) {
	store := setupProviderObjectTestStore(t)
	ctx := context.Background()
	seed := func(provider, batchID, input string, output, errFile *string) {
		job := &tables.TableProviderJob{
			ID:               tables.ProviderJobID(tables.ProviderJobKindBatch, provider, batchID),
			Provider:         provider,
			JobID:            batchID,
			InputFileID:      input,
			OutputFileID:     output,
			ErrorFileID:      errFile,
			AccountingStatus: tables.ProviderJobAccountingStatusPending,
		}
		require.NoError(t, store.UpsertProviderJob(ctx, job))
	}
	out := "file-out"
	errFile := "file-err"
	seed("openai", "batch-1", "file-in", &out, &errFile)
	seed("azure", "batch-2", "file-in", nil, nil)

	ids := func(jobs []*tables.TableProviderJob) []string {
		got := make([]string, 0, len(jobs))
		for _, job := range jobs {
			got = append(got, job.JobID)
		}
		return got
	}
	for _, fileID := range []string{"file-in", "file-out", "file-err"} {
		jobs, err := store.GetProviderJobsByFileIDs(ctx, "openai", []string{fileID})
		require.NoError(t, err)
		assert.Equal(t, []string{"batch-1"}, ids(jobs), "file %s", fileID)
	}
	jobs, err := store.GetProviderJobsByFileIDs(ctx, "openai", []string{"file-in", "file-none"})
	require.NoError(t, err)
	assert.Equal(t, []string{"batch-1"}, ids(jobs))

	jobs, err = store.GetProviderJobsByFileIDs(ctx, "openai", []string{"file-none"})
	require.NoError(t, err)
	assert.Empty(t, jobs)

	jobs, err = store.GetProviderJobsByFileIDs(ctx, "", []string{"file-in"})
	require.NoError(t, err)
	assert.Nil(t, jobs, "no provider, no lookup")
}

// A list filter looks up every item of a provider page, under every provider name that shares the
// upstream, so one lookup can carry tens of thousands of ids: more than SQLite binds in one
// statement (32766) and more than Postgres accepts (65535). The lookups split the ids across
// queries, and a job that names files in two of those queries comes back once.
func TestProviderLookupsAcceptMoreIDsThanOneStatementBinds(t *testing.T) {
	store := setupProviderObjectTestStore(t)
	ctx := context.Background()
	const total = 40000
	ids := make([]string, total)
	fileIDs := make([]string, total)
	for i := range ids {
		ids[i] = fmt.Sprintf("file:openai:file-%05d", i)
		fileIDs[i] = fmt.Sprintf("file-%05d", i)
	}

	for _, i := range []int{0, 17000, total - 1} {
		require.NoError(t, store.UpsertProviderObject(ctx, &tables.TableProviderObject{
			ID: ids[i], Kind: tables.ProviderObjectKindFile, Provider: "openai", ObjectID: fileIDs[i], VirtualKeyID: "vk-a",
		}))
	}
	objects, err := store.GetProviderObjectsByIDs(ctx, ids)
	require.NoError(t, err)
	got := make([]string, 0, len(objects))
	for _, object := range objects {
		got = append(got, object.ObjectID)
	}
	sort.Strings(got)
	assert.Equal(t, []string{fileIDs[0], fileIDs[17000], fileIDs[total-1]}, got)

	// The batch names its input file early in the list and its output file late, so the two
	// matches fall in different queries.
	output := fileIDs[total-1]
	batch := &tables.TableProviderJob{
		ID: tables.ProviderJobID(tables.ProviderJobKindBatch, "openai", "batch-wide"), Kind: tables.ProviderJobKindBatch,
		Provider: "openai", JobID: "batch-wide", InputFileID: fileIDs[1], OutputFileID: &output,
		AccountingStatus: tables.ProviderJobAccountingStatusPending,
	}
	require.NoError(t, store.UpsertProviderJob(ctx, batch))
	jobs, err := store.GetProviderJobsByFileIDs(ctx, "openai", fileIDs)
	require.NoError(t, err)
	require.Len(t, jobs, 1, "the batch is returned once though two queries matched it")
	assert.Equal(t, "batch-wide", jobs[0].JobID)

	jobIDs := make([]string, total)
	for i := range jobIDs {
		jobIDs[i] = tables.ProviderJobID(tables.ProviderJobKindBatch, "openai", fmt.Sprintf("batch-%05d", i))
	}
	jobIDs[total-1] = batch.ID
	jobs, err = store.GetProviderJobsByIDs(ctx, jobIDs)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, "batch-wide", jobs[0].JobID)
}

// A delete that names a full resource name by its last segment forgets the row the full name
// is recorded under: whole, at a path boundary, with LIKE metacharacters literal, among the
// named kind and provider only.
func TestDeleteProviderObjectsByObjectIDSuffixForgetsTheFullName(t *testing.T) {
	store := setupProviderObjectTestStore(t)
	ctx := context.Background()
	seed := func(kind, provider, objectID string) string {
		id := tables.ProviderObjectID(kind, provider, objectID)
		require.NoError(t, store.UpsertProviderObject(ctx, &tables.TableProviderObject{
			ID: id, Kind: kind, Provider: provider, ObjectID: objectID, VirtualKeyID: "vk-a",
		}))
		return id
	}
	gone := seed(tables.ProviderObjectKindBatch, "vertex", "projects/p/locations/l/batchPredictionJobs/123")
	kept := []string{
		seed(tables.ProviderObjectKindBatch, "vertex", "projects/p/locations/l/batchPredictionJobs/9123"),
		seed(tables.ProviderObjectKindBatch, "vertex", "projects/p/locations/l/batchPredictionJobs/1_3"),
		seed(tables.ProviderObjectKindBatch, "vertex", "123"),
		seed(tables.ProviderObjectKindCachedContent, "vertex", "projects/p/locations/l/cachedContents/123"),
		seed(tables.ProviderObjectKindBatch, "gemini", "batches/123"),
	}
	// Another key's job with the same number in another project is not the deleter's to forget.
	otherKeys := tables.ProviderObjectID(tables.ProviderObjectKindBatch, "vertex", "projects/q/locations/l/batchPredictionJobs/123")
	require.NoError(t, store.UpsertProviderObject(ctx, &tables.TableProviderObject{
		ID: otherKeys, Kind: tables.ProviderObjectKindBatch, Provider: "vertex", ObjectID: "projects/q/locations/l/batchPredictionJobs/123", VirtualKeyID: "vk-b",
	}))
	kept = append(kept, otherKeys)

	require.NoError(t, store.DeleteProviderObjectsByObjectIDSuffix(ctx, tables.ProviderObjectKindBatch, "vertex", "123", "vk-a"))

	objects, err := store.GetProviderObjectsByIDs(ctx, append([]string{gone}, kept...))
	require.NoError(t, err)
	ids := make([]string, 0, len(objects))
	for _, object := range objects {
		ids = append(ids, object.ID)
	}
	assert.ElementsMatch(t, kept, ids, "only vk-a's vertex batch whose name ends in /123 is forgotten")

	assert.Error(t, store.DeleteProviderObjectsByObjectIDSuffix(ctx, tables.ProviderObjectKindBatch, "vertex", "", "vk-a"), "an empty suffix would forget every full name")
	assert.Error(t, store.DeleteProviderObjectsByObjectIDSuffix(ctx, tables.ProviderObjectKindBatch, "vertex", "123", ""), "an empty virtual key would forget every key's full name")
}

// A full resource name is also addressed by its last segment. The suffix lookup finds the
// ownership rows by that segment alone, whole and at a path boundary, with LIKE metacharacters
// taken literally, among the named kind and provider only, for several suffixes at once.
func TestGetProviderObjectsByObjectIDSuffixesMatchTheLastSegmentWhole(t *testing.T) {
	store := setupProviderObjectTestStore(t)
	ctx := context.Background()
	seed := func(kind, provider, objectID, vk string) {
		require.NoError(t, store.UpsertProviderObject(ctx, &tables.TableProviderObject{
			ID: tables.ProviderObjectID(kind, provider, objectID), Kind: kind, Provider: provider, ObjectID: objectID, VirtualKeyID: vk,
		}))
	}
	seed(tables.ProviderObjectKindCachedContent, "vertex", "projects/p/locations/l/cachedContents/123", "vk-a")
	seed(tables.ProviderObjectKindCachedContent, "vertex", "projects/q/locations/l/cachedContents/123", "vk-b")
	seed(tables.ProviderObjectKindCachedContent, "vertex", "projects/p/locations/l/cachedContents/9123", "vk-a")
	seed(tables.ProviderObjectKindCachedContent, "vertex", "projects/p/locations/l/cachedContents/1_3", "vk-a")
	seed(tables.ProviderObjectKindCachedContent, "vertex", "123", "vk-a")
	seed(tables.ProviderObjectKindBatch, "vertex", "projects/p/locations/l/batchPredictionJobs/123", "vk-a")
	seed(tables.ProviderObjectKindCachedContent, "gemini", "cachedContents/123", "vk-a")

	objects, err := store.GetProviderObjectsByObjectIDSuffixes(ctx, tables.ProviderObjectKindCachedContent, "vertex", []string{"123", "1_3", ""})
	require.NoError(t, err)
	ids := make([]string, 0, len(objects))
	for _, object := range objects {
		ids = append(ids, object.ObjectID)
	}
	assert.ElementsMatch(t, []string{
		"projects/p/locations/l/cachedContents/123",
		"projects/q/locations/l/cachedContents/123",
		"projects/p/locations/l/cachedContents/1_3",
	}, ids, "9123 is not 123, a bare 123 is not a resource name, the underscore is literal, and other kinds and providers are not consulted")

	objects, err = store.GetProviderObjectsByObjectIDSuffixes(ctx, tables.ProviderObjectKindCachedContent, "vertex", nil)
	require.NoError(t, err)
	assert.Nil(t, objects, "no suffixes match nothing")
}

// A provider object is found by its kind and provider-side id under every provider name it was
// recorded under, including one no longer configured, and only rows of that kind and id are read.
func TestGetProviderObjectsByObjectIDSpansProviders(t *testing.T) {
	store := setupProviderObjectTestStore(t)
	ctx := context.Background()
	seed := func(kind, provider, objectID, vk string) {
		require.NoError(t, store.UpsertProviderObject(ctx, &tables.TableProviderObject{
			ID: tables.ProviderObjectID(kind, provider, objectID), Kind: kind, Provider: provider, ObjectID: objectID, VirtualKeyID: vk,
		}))
	}
	seed(tables.ProviderObjectKindLiveSession, "custom-live", "live_1", "vk-a")
	seed(tables.ProviderObjectKindLiveSession, "openai", "live_1", "vk-a")
	seed(tables.ProviderObjectKindLiveSession, "openai", "live_2", "vk-b")
	seed(tables.ProviderObjectKindFile, "openai", "live_1", "vk-c")

	objects, err := store.GetProviderObjectsByObjectID(ctx, tables.ProviderObjectKindLiveSession, "live_1")
	require.NoError(t, err)
	providers := make([]string, 0, len(objects))
	for _, object := range objects {
		assert.Equal(t, "live_1", object.ObjectID)
		assert.Equal(t, tables.ProviderObjectKindLiveSession, object.Kind)
		providers = append(providers, object.Provider)
	}
	assert.ElementsMatch(t, []string{"custom-live", "openai"}, providers, "every provider name the session was recorded under, and no other kind")

	objects, err = store.GetProviderObjectsByObjectID(ctx, tables.ProviderObjectKindLiveSession, "")
	require.NoError(t, err)
	assert.Empty(t, objects, "an empty id matches nothing")
}

// The ownership check on a live session's control routes looks rows up by kind and object id
// across providers; an index on that pair keeps it off a scan of every recorded session. The
// rollback drops only that index.
func TestProviderObjectsObjectIDIndexMigration(t *testing.T) {
	store := setupProviderObjectTestStore(t)
	db := store.DB()
	require.NoError(t, migrationAddProviderObjectsObjectIDIndex(context.Background(), db, testMigrationLogger))
	require.True(t, indexExists(t, db, "provider_objects", "idx_provider_objects_kind_object_id"), "the (kind, object_id) index is created")
	require.NoError(t, migrationAddProviderObjectsObjectIDIndex(context.Background(), db, testMigrationLogger), "running it again is a no-op")

	require.NoError(t, providerObjectsObjectIDIndexMigration(context.Background(), "add_provider_objects_object_id_index").Rollback(db))
	require.False(t, indexExists(t, db, "provider_objects", "idx_provider_objects_kind_object_id"), "the rollback drops the index")
	require.True(t, db.Migrator().HasTable("provider_objects"), "and keeps the table")
}

// BenchmarkGetProviderObjectsByIDs measures the ownership lookup of a typical list page
// (20 ids, half of them recorded) on SQLite, the common case the id-list binding must not
// slow down.
func BenchmarkGetProviderObjectsByIDs(b *testing.B) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(b.TempDir(), "bench.db")), inListGormConfig())
	require.NoError(b, err)
	require.NoError(b, db.AutoMigrate(&tables.TableProviderObject{}))
	store := newInListTestStore(db)
	ctx := context.Background()
	ids := make([]string, 0, 20)
	for i := range 20 {
		objectID := fmt.Sprintf("file-%02d", i)
		id := tables.ProviderObjectID(tables.ProviderObjectKindFile, "openai", objectID)
		ids = append(ids, id)
		if i%2 == 0 {
			require.NoError(b, store.UpsertProviderObject(ctx, &tables.TableProviderObject{
				ID: id, Kind: tables.ProviderObjectKindFile, Provider: "openai", ObjectID: objectID, VirtualKeyID: "vk-a",
			}))
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		objects, err := store.GetProviderObjectsByIDs(ctx, ids)
		if err != nil || len(objects) != 10 {
			b.Fatalf("lookup: %v, %d rows", err, len(objects))
		}
	}
}
