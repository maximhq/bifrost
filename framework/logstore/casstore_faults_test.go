package logstore

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func openFaultTestCas(t *testing.T, path string) (*CasLogStore, *RDBLogStore) {
	t.Helper()
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: path}, hybridTestLogger{})
	require.NoError(t, err)
	cas, err := newCasLogStore(ctx, inner, &ContentAddressedConfig{Enabled: true, MinFieldBytes: 64, MinChunkBytes: 32}, hybridTestLogger{})
	require.NoError(t, err)
	return cas, inner
}

func TestCas_CrashReplayHelper(t *testing.T) {
	if os.Getenv("CAS_CRASH_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	path := os.Getenv("CAS_CRASH_DB")
	mode := os.Getenv("CAS_CRASH_MODE")
	cas, _ := openFaultTestCas(t, path)
	entry := bigChatEntry("crash-replay", strings.Repeat("crash replay body ", 200))
	require.NoError(t, entry.SerializeFields())
	if mode == "before-commit" {
		tx := cas.db.Begin()
		require.NoError(t, tx.Error)
		prepared, err := cas.prepareCreate(entry)
		require.NoError(t, err)
		inserted, err := cas.createRow(tx, &prepared.dbEntry, true)
		require.NoError(t, err)
		require.True(t, inserted)
		require.NoError(t, cas.casWriteFields(tx, entry.ID, prepared.toStore))
		os.Exit(91)
	}
	require.NoError(t, cas.CreateIfNotExists(context.Background(), entry))
	os.Exit(92)
}

func runCrashHelper(t *testing.T, path, mode string, expectedExit int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCas_CrashReplayHelper$")
	cmd.Env = append(os.Environ(), "CAS_CRASH_HELPER=1", "CAS_CRASH_DB="+path, "CAS_CRASH_MODE="+mode)
	err := cmd.Run()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, expectedExit, exitErr.ExitCode())
}

func TestCas_CrashBeforeCommitThenReplayIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash-before.db")
	cas, _ := openFaultTestCas(t, path)
	require.NoError(t, cas.Close(context.Background()))
	runCrashHelper(t, path, "before-commit", 91)

	cas, _ = openFaultTestCas(t, path)
	defer cas.Close(context.Background())
	logs, payloads, refs, blobs := casGraphCounts(t, cas)
	require.Zero(t, logs)
	require.Zero(t, payloads)
	require.Zero(t, refs)
	require.Zero(t, blobs)

	entry := bigChatEntry("crash-replay", strings.Repeat("crash replay body ", 200))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(context.Background(), entry))
	found, err := cas.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	require.Equal(t, entry.InputHistory, found.InputHistory)
	requireCasGraphReachable(t, cas)
}

func TestCas_CrashAfterCommitThenReplayIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash-after.db")
	cas, _ := openFaultTestCas(t, path)
	require.NoError(t, cas.Close(context.Background()))
	runCrashHelper(t, path, "after-commit", 92)

	cas, _ = openFaultTestCas(t, path)
	defer cas.Close(context.Background())
	entry := bigChatEntry("crash-replay", strings.Repeat("crash replay body ", 200))
	require.NoError(t, entry.SerializeFields())
	beforeLogs, beforePayloads, beforeRefs, beforeBlobs := casGraphCounts(t, cas)
	require.Equal(t, int64(1), beforeLogs)
	require.NoError(t, cas.CreateIfNotExists(context.Background(), entry))
	logs, payloads, refs, blobs := casGraphCounts(t, cas)
	require.Equal(t, beforeLogs, logs)
	require.Equal(t, beforePayloads, payloads)
	require.Equal(t, beforeRefs, refs)
	require.Equal(t, beforeBlobs, blobs)
	found, err := cas.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	require.Equal(t, entry.InputHistory, found.InputHistory)
	requireCasGraphReachable(t, cas)
}

func casGraphCounts(t *testing.T, cas *CasLogStore) (logs, payloads, refs, blobs int64) {
	t.Helper()
	require.NoError(t, cas.db.Model(&Log{}).Count(&logs).Error)
	require.NoError(t, cas.db.Model(&casPayload{}).Count(&payloads).Error)
	require.NoError(t, cas.db.Model(&casRef{}).Count(&refs).Error)
	require.NoError(t, cas.db.Model(&casBlob{}).Count(&blobs).Error)
	return
}

func requireCasGraphReachable(t *testing.T, cas *CasLogStore) {
	t.Helper()
	checks := []struct {
		name  string
		query string
	}{
		{"payload_missing_log", "SELECT COUNT(*) FROM cas_payloads p WHERE NOT EXISTS (SELECT 1 FROM logs l WHERE l.id=p.log_id)"},
		{"payload_missing_manifest", "SELECT COUNT(*) FROM cas_payloads p WHERE NOT EXISTS (SELECT 1 FROM cas_blobs b WHERE b.hash=p.blob_hash)"},
		{"ref_missing_owner", "SELECT COUNT(*) FROM cas_refs r WHERE NOT EXISTS (SELECT 1 FROM cas_blobs b WHERE b.hash=r.owner_hash)"},
		{"ref_missing_target", "SELECT COUNT(*) FROM cas_refs r WHERE NOT EXISTS (SELECT 1 FROM cas_blobs b WHERE b.hash=r.target_hash)"},
		{"has_object_without_pointer", "SELECT COUNT(*) FROM logs l WHERE l.has_object=1 AND NOT EXISTS (SELECT 1 FROM cas_payloads p WHERE p.log_id=l.id)"},
		{"pointer_without_has_object", "SELECT COUNT(*) FROM logs l WHERE l.has_object=0 AND EXISTS (SELECT 1 FROM cas_payloads p WHERE p.log_id=l.id)"},
	}
	for _, check := range checks {
		var count int64
		require.NoError(t, cas.db.Raw(check.query).Scan(&count).Error, check.name)
		require.Zero(t, count, check.name)
	}
}

func TestCas_SQLiteFullCreateRollsBackWholeGraph(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	var pageCount int64
	require.NoError(t, cas.db.Raw("PRAGMA page_count").Scan(&pageCount).Error)
	require.NoError(t, cas.db.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", pageCount)).Error)

	entry := bigChatEntry("full-create", strings.Repeat("disk full payload ", 20000))
	require.NoError(t, entry.SerializeFields())
	err := cas.Create(ctx, entry)
	require.Error(t, err, "SQLITE_FULL must be surfaced")

	logs, payloads, refs, blobs := casGraphCounts(t, cas)
	require.Zero(t, logs)
	require.Zero(t, payloads)
	require.Zero(t, refs)
	require.Zero(t, blobs)
}

func TestCas_SQLiteFullUpdatePreservesOldRevision(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	old := bigChatEntry("full-update", strings.Repeat("old revision ", 40))
	require.NoError(t, old.SerializeFields())
	require.NoError(t, cas.Create(ctx, old))
	beforeLogs, beforePayloads, beforeRefs, beforeBlobs := casGraphCounts(t, cas)

	var pageCount int64
	require.NoError(t, cas.db.Raw("PRAGMA page_count").Scan(&pageCount).Error)
	require.NoError(t, cas.db.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", pageCount)).Error)
	rng := rand.New(rand.NewSource(1))
	noise := make([]byte, 2<<20)
	_, readErr := rng.Read(noise)
	require.NoError(t, readErr)
	newPayload := string(noise)
	err := cas.Update(ctx, old.ID, map[string]interface{}{"input_history": newPayload})
	require.Error(t, err, "SQLITE_FULL must roll the replacement back")

	found, readErr := cas.FindByID(ctx, old.ID)
	require.NoError(t, readErr)
	require.Equal(t, old.InputHistory, found.InputHistory)
	logs, payloads, refs, blobs := casGraphCounts(t, cas)
	require.Equal(t, beforeLogs, logs)
	require.Equal(t, beforePayloads, payloads)
	require.Equal(t, beforeRefs, refs)
	require.Equal(t, beforeBlobs, blobs)
	requireCasGraphReachable(t, cas)
}

func TestCas_ConcurrentDuplicateCreatePersistsOnce(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()
	const workers = 12

	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry := bigChatEntry("duplicate-event", strings.Repeat("shared history ", 200))
			if err := entry.SerializeFields(); err != nil {
				errs <- err
				return
			}
			<-start
			errs <- cas.CreateIfNotExists(ctx, entry)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	var logs int64
	require.NoError(t, cas.db.Model(&Log{}).Where("id = ?", "duplicate-event").Count(&logs).Error)
	require.Equal(t, int64(1), logs)
	var pointers int64
	require.NoError(t, cas.db.Model(&casPayload{}).Where("log_id = ?", "duplicate-event").Count(&pointers).Error)
	require.Positive(t, pointers)
	requireCasGraphReachable(t, cas)
	found, err := cas.FindByID(ctx, "duplicate-event")
	require.NoError(t, err)
	require.NotEmpty(t, found.InputHistory)
}

func TestCas_GCConcurrentWithWritersConvergesReachable(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	for i := 0; i < 8; i++ {
		entry := bigChatEntry(fmt.Sprintf("gc-write-%d", i), strings.Repeat("seed history ", 80))
		require.NoError(t, entry.SerializeFields())
		require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		for i := 0; i < 80; i++ {
			id := fmt.Sprintf("gc-write-%d", i%8)
			err := cas.Update(ctx, id, map[string]interface{}{"input_history": strings.Repeat(fmt.Sprintf("revision-%d ", i), 100)})
			if err != nil {
				errs <- err
				return
			}
		}
		errs <- nil
	}()
	go func() {
		<-start
		for i := 0; i < 80; i++ {
			if err := cas.gcOrphanCas(ctx); err != nil {
				errs <- err
				return
			}
		}
		errs <- nil
	}()
	close(start)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	require.NoError(t, cas.gcOrphanCas(ctx))
	requireCasGraphReachable(t, cas)
	for i := 0; i < 8; i++ {
		found, err := cas.FindByID(ctx, fmt.Sprintf("gc-write-%d", i))
		require.NoError(t, err)
		require.NotEmpty(t, found.InputHistory)
	}
}
