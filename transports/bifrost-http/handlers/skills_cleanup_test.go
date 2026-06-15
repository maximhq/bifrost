package handlers

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/objectstore"
)

type testLogger struct{}

func (testLogger) Debug(string, ...any) {}
func (testLogger) Info(string, ...any)  {}
func (testLogger) Warn(string, ...any)  {}
func (testLogger) Error(string, ...any) {}
func (testLogger) Fatal(msg string, args ...any) {
	panic("fatal called")
}
func (testLogger) SetLevel(schemas.LogLevel)              {}
func (testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func newTestConfigStore(t *testing.T) configstore.ConfigStore {
	t.Helper()
	store, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{
			Path: filepath.Join(t.TempDir(), "config.db"),
		},
	}, testLogger{})
	if err != nil {
		t.Fatalf("new config store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	return store
}

func TestCleanupOrphanSkillFilesDeletesDBFallbackBlobs(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	referencedBlobID := "referenced-blob"
	orphanBlobID := "orphan-blob"

	if err := store.CreateSkillFileBlob(ctx, &tables.TableSkillFileBlob{ID: referencedBlobID, Data: []byte("referenced")}); err != nil {
		t.Fatalf("create referenced blob: %v", err)
	}
	if err := store.CreateSkillFileBlob(ctx, &tables.TableSkillFileBlob{ID: orphanBlobID, Data: []byte("orphan")}); err != nil {
		t.Fatalf("create orphan blob: %v", err)
	}
	// Backdate the orphan blob so it exceeds the 24-hour grace period.
	if err := store.DB().WithContext(ctx).Model(&tables.TableSkillFileBlob{}).Where("id = ?", orphanBlobID).Update("created_at", time.Now().Add(-25*time.Hour)).Error; err != nil {
		t.Fatalf("backdate orphan blob: %v", err)
	}
	if err := store.CreateSkill(ctx, &tables.TableSkill{
		Name:        "cleanup-db-test",
		Description: "cleanup db test",
		SkillMDBody: "body",
		Files: []tables.TableSkillFile{{
			Path:          "reference.txt",
			SourceType:    tables.SkillSourceTypeUpload,
			BlobID:        &referencedBlobID,
			MimeType:      "text/plain",
			FileSizeBytes: 10,
		}},
	}, "1.0.0", nil); err != nil {
		t.Fatalf("create skill: %v", err)
	}

	result, err := CleanupOrphanSkillFiles(ctx, store, nil, false)
	if err != nil {
		t.Fatalf("cleanup orphan files: %v", err)
	}
	if result.DeletedDBBlobs != 1 {
		t.Fatalf("DeletedDBBlobs got %d, want 1", result.DeletedDBBlobs)
	}
	if result.DeletedStorageObjects != 0 {
		t.Fatalf("DeletedStorageObjects got %d, want 0", result.DeletedStorageObjects)
	}

	if err := store.DB().WithContext(ctx).First(&tables.TableSkillFileBlob{}, "id = ?", referencedBlobID).Error; err != nil {
		t.Fatalf("referenced blob should remain: %v", err)
	}
	var remaining int64
	if err := store.DB().WithContext(ctx).Model(&tables.TableSkillFileBlob{}).Where("id = ?", orphanBlobID).Count(&remaining).Error; err != nil {
		t.Fatalf("count orphan blob: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("orphan blob remained")
	}
}

func TestCleanupOrphanSkillFilesDeletesOnlyUnreferencedUploadObjects(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	objStore := objectstore.NewInMemoryObjectStore()
	referencedKey := configstore.SkillObjectPrefix + "uploads/referenced/file.txt"
	orphanKey := configstore.SkillObjectPrefix + "uploads/orphan/file.txt"
	outsidePrefixKey := "other/file.txt"

	for key := range map[string]struct{}{referencedKey: {}, orphanKey: {}, outsidePrefixKey: {}} {
		if err := objStore.Put(ctx, key, []byte(key), nil); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	// Backdate orphan object so it exceeds the 24-hour grace period.
	objStore.SetCreatedAt(orphanKey, time.Now().Add(-25*time.Hour))
	uploadID := "referenced"
	if err := store.CreateSkill(ctx, &tables.TableSkill{
		Name:        "cleanup-object-test",
		Description: "cleanup object test",
		SkillMDBody: "body",
		Files: []tables.TableSkillFile{{
			Path:          "file.txt",
			SourceType:    tables.SkillSourceTypeUpload,
			StorageKey:    &referencedKey,
			UploadID:      &uploadID,
			MimeType:      "text/plain",
			FileSizeBytes: 10,
		}},
	}, "1.0.0", objStore); err != nil {
		t.Fatalf("create skill: %v", err)
	}

	result, err := CleanupOrphanSkillFiles(ctx, store, objStore, false)
	if err != nil {
		t.Fatalf("cleanup orphan files: %v", err)
	}
	if result.DeletedStorageObjects != 1 {
		t.Fatalf("DeletedStorageObjects got %d, want 1", result.DeletedStorageObjects)
	}
	if _, err := objStore.Get(ctx, referencedKey); err != nil {
		t.Fatalf("referenced object should remain: %v", err)
	}
	if _, err := objStore.Get(ctx, outsidePrefixKey); err != nil {
		t.Fatalf("outside-prefix object should remain: %v", err)
	}
	if _, err := objStore.Get(ctx, orphanKey); err == nil {
		t.Fatalf("orphan object remained")
	}
}
