package configstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	require.NoError(t, store.DeleteProviderObjectsByObjectIDSuffix(ctx, tables.ProviderObjectKindBatch, "vertex", "123"))

	objects, err := store.GetProviderObjectsByIDs(ctx, append([]string{gone}, kept...))
	require.NoError(t, err)
	ids := make([]string, 0, len(objects))
	for _, object := range objects {
		ids = append(ids, object.ID)
	}
	assert.ElementsMatch(t, kept, ids, "only the vertex batch whose name ends in /123 is forgotten")

	assert.Error(t, store.DeleteProviderObjectsByObjectIDSuffix(ctx, tables.ProviderObjectKindBatch, "vertex", ""), "an empty suffix would forget every full name")
}
