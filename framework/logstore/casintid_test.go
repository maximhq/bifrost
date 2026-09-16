package logstore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// After cas_integer_ref_ids_v1 the integer layout is the only layout the
// write path supports: cas_refs keys are cas_blobs ids, cas_blobs keeps the
// full SHA-256 unique for dedup and a non-reusing integer primary key.

func TestCasIntegerLayoutFreshDB(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	var ddl string
	require.NoError(t, cas.db.Raw("SELECT sql FROM sqlite_master WHERE name='cas_refs'").Scan(&ddl).Error)
	require.Contains(t, ddl, "owner_id")
	require.Contains(t, ddl, "target_id")
	require.Contains(t, ddl, "WITHOUT ROWID")
	require.NoError(t, cas.db.Raw("SELECT sql FROM sqlite_master WHERE name='cas_blobs'").Scan(&ddl).Error)
	require.Contains(t, ddl, "AUTOINCREMENT", "blob ids must never be reused; plain rowid reuse could alias a stale ref edge onto new content")
	require.True(t, cas.db.Migrator().HasIndex("cas_refs", "idx_cas_refs_target_id"))
	require.True(t, cas.db.Migrator().HasIndex("cas_blobs", "idx_cas_blobs_hash"))
	var n int64
	require.NoError(t, cas.db.Raw("SELECT count(*) FROM migrations WHERE id='cas_integer_ref_ids_v1'").Scan(&n).Error)
	require.EqualValues(t, 1, n)
}

// downgradeCasRefsToHex rebuilds cas_refs (and optionally cas_blobs) in the
// pre-integer layout so upgrade paths can be exercised against real data.
func downgradeCasRefsToHex(t *testing.T, cas *CasLogStore, withBlobs bool) {
	t.Helper()
	require.NoError(t, cas.db.Exec("CREATE TABLE cas_refs_hex (owner_hash TEXT NOT NULL, target_hash TEXT NOT NULL, PRIMARY KEY(owner_hash,target_hash)) WITHOUT ROWID").Error)
	require.NoError(t, cas.db.Exec("INSERT INTO cas_refs_hex SELECT b1.hash, b2.hash FROM cas_refs r JOIN cas_blobs b1 ON b1.id = r.owner_id JOIN cas_blobs b2 ON b2.id = r.target_id").Error)
	require.NoError(t, cas.db.Exec("DROP TABLE cas_refs").Error)
	require.NoError(t, cas.db.Exec("ALTER TABLE cas_refs_hex RENAME TO cas_refs").Error)
	require.NoError(t, cas.db.Exec("CREATE INDEX idx_cas_refs_target_hash ON cas_refs(target_hash)").Error)
	if withBlobs {
		require.NoError(t, cas.db.Exec("CREATE TABLE cas_blobs_hex (hash TEXT NOT NULL PRIMARY KEY, codec TEXT, orig_len INTEGER, data BLOB, created_at INTEGER)").Error)
		require.NoError(t, cas.db.Exec("INSERT INTO cas_blobs_hex SELECT hash, codec, orig_len, data, created_at FROM cas_blobs").Error)
		require.NoError(t, cas.db.Exec("DROP TABLE cas_blobs").Error)
		require.NoError(t, cas.db.Exec("ALTER TABLE cas_blobs_hex RENAME TO cas_blobs").Error)
	}
}

func TestCasIntegerMigrationFromHexLayout(t *testing.T) {
	cas, _ := newTestCas(t)
	ctx := context.Background()
	defer cas.Close(ctx)
	shared := strings.Repeat("shared context ", 40)
	require.NoError(t, cas.Create(ctx, bigChatEntry("hex-1", shared, "q1 "+strings.Repeat("x", 300))))
	require.NoError(t, cas.Create(ctx, bigChatEntry("hex-2", shared, "q2 "+strings.Repeat("y", 300))))
	before1, err := cas.FindByID(ctx, "hex-1")
	require.NoError(t, err)
	require.NotEmpty(t, before1.InputHistory)

	downgradeCasRefsToHex(t, cas, true)
	for _, id := range []string{"cas_refs_without_rowid_v1", "cas_reverse_lookup_indexes_v1", "cas_integer_ref_ids_v1"} {
		require.NoError(t, cas.db.Exec("DELETE FROM migrations WHERE id=?", id).Error)
	}
	var edgesBefore int64
	require.NoError(t, cas.db.Raw("SELECT count(*) FROM cas_refs").Scan(&edgesBefore).Error)
	require.Positive(t, edgesBefore)

	require.NoError(t, triggerMigrations(ctx, cas.db, hybridTestLogger{}))

	var edgesAfter int64
	require.NoError(t, cas.db.Raw("SELECT count(*) FROM cas_refs r JOIN cas_blobs b1 ON b1.id = r.owner_id JOIN cas_blobs b2 ON b2.id = r.target_id").Scan(&edgesAfter).Error)
	require.Equal(t, edgesBefore, edgesAfter, "every hex edge must survive as an integer edge")
	after1, err := cas.FindByID(ctx, "hex-1")
	require.NoError(t, err)
	require.Equal(t, before1.InputHistory, after1.InputHistory, "hydration must be byte-identical across the migration")

	// GC semantics on the migrated store: shared chunks survive one delete.
	require.NoError(t, cas.DeleteLog(ctx, "hex-1"))
	after2, err := cas.FindByID(ctx, "hex-2")
	require.NoError(t, err)
	require.NotEmpty(t, after2.InputHistory)
	require.Contains(t, after2.InputHistory, "shared context")
}

func TestCasIntegerMigrationDanglingFailsClosed(t *testing.T) {
	cas, _ := newTestCas(t)
	ctx := context.Background()
	defer cas.Close(ctx)
	require.NoError(t, cas.Create(ctx, bigChatEntry("dangling", strings.Repeat("payload", 100))))
	downgradeCasRefsToHex(t, cas, false)
	require.NoError(t, cas.db.Exec("INSERT INTO cas_refs VALUES ('deadbeef-owner', 'deadbeef-target')").Error)
	require.NoError(t, cas.db.Exec("DELETE FROM migrations WHERE id='cas_integer_ref_ids_v1'").Error)

	err := migrationCasIntegerRefIDs(ctx, cas.db, hybridTestLogger{})
	require.ErrorContains(t, err, "missing blob endpoints")
	var n int64
	require.NoError(t, cas.db.Raw("SELECT count(*) FROM migrations WHERE id='cas_integer_ref_ids_v1'").Scan(&n).Error)
	require.Zero(t, n, "a failed migration must not record its marker")
	hex, err := casRefsHexLayout(cas.db)
	require.NoError(t, err)
	require.True(t, hex, "a failed migration must leave the layout untouched")
}

func TestCasIntegerIDsNotReused(t *testing.T) {
	cas, _ := newTestCas(t)
	ctx := context.Background()
	defer cas.Close(ctx)
	require.NoError(t, cas.Create(ctx, bigChatEntry("reuse-1", strings.Repeat("alpha ", 200))))
	var maxID int64
	require.NoError(t, cas.db.Raw("SELECT COALESCE(max(id),0) FROM cas_blobs").Scan(&maxID).Error)
	require.Positive(t, maxID)
	require.NoError(t, cas.DeleteLog(ctx, "reuse-1"))
	var blobs int64
	require.NoError(t, cas.db.Raw("SELECT count(*) FROM cas_blobs").Scan(&blobs).Error)
	require.Zero(t, blobs, "the only log's content must be fully reclaimed")
	require.NoError(t, cas.Create(ctx, bigChatEntry("reuse-2", strings.Repeat("beta ", 200))))
	var newMax int64
	require.NoError(t, cas.db.Raw("SELECT COALESCE(max(id),0) FROM cas_blobs").Scan(&newMax).Error)
	require.Greater(t, newMax, maxID, "reclaimed ids must never be reused")
}

func TestCasIntegerIDAllocationConcurrent(t *testing.T) {
	cas, _ := newTestCas(t)
	ctx := context.Background()
	defer cas.Close(ctx)
	const workers = 16
	const rounds = 20
	shared := strings.Repeat("shared history chunk ", 60)
	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				e := bigChatEntry(fmt.Sprintf("cc-%d-%d", w, r), shared, fmt.Sprintf("unique %d %d %s", w, r, strings.Repeat("z", 300)))
				if err := cas.Create(ctx, e); err != nil {
					errs <- fmt.Errorf("worker %d round %d: %w", w, r, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var dangling int64
	require.NoError(t, cas.db.Raw("SELECT count(*) FROM cas_refs r WHERE NOT EXISTS (SELECT 1 FROM cas_blobs b WHERE b.id = r.target_id) OR NOT EXISTS (SELECT 1 FROM cas_blobs b WHERE b.id = r.owner_id)").Scan(&dangling).Error)
	require.Zero(t, dangling)
	for w := 0; w < workers; w++ {
		got, err := cas.FindByID(ctx, fmt.Sprintf("cc-%d-%d", w, rounds-1))
		require.NoError(t, err)
		require.Contains(t, got.InputHistory, "shared history chunk")
	}
}
