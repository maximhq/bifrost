package logstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestCasIntegerMigrationProductionSnapshot rehearses cas_integer_ref_ids_v1
// against a real production snapshot. Gated: set CAS_INTID_SNAPSHOT to the
// snapshot path and CAS_INTID_SNAPSHOT_OUT to a writable output directory.
//
// Evidence produced:
//   - edge set equality (hex pairs pre == id-mapped pairs post)
//   - content digest equality for every CAS payload field (byte-identical
//     hydration, computed independent of cas_refs, with hash validation)
//   - cas_blobs inventory equality (hash -> sha256(data))
//   - payload pointer equality
//   - orphan-sweep selection parity between old hex and new integer predicates
//   - EXPLAIN plan check on migrated data
//   - migration duration, sweep duration and size deltas
//   - a full write/hydrate/delete cycle on the migrated store
func TestCasIntegerMigrationProductionSnapshot(t *testing.T) {
	src := os.Getenv("CAS_INTID_SNAPSHOT")
	outDir := os.Getenv("CAS_INTID_SNAPSHOT_OUT")
	if src == "" {
		t.Skip("set CAS_INTID_SNAPSHOT (and CAS_INTID_SNAPSHOT_OUT) to rehearse the integer-id migration on a production snapshot")
	}
	if outDir == "" {
		outDir = t.TempDir()
	}
	require.NoError(t, os.MkdirAll(outDir, 0o755))
	ctx := context.Background()
	report := map[string]any{}

	work := filepath.Join(outDir, "migrated.db")
	raw, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(work, raw, 0o600))
	report["snapshot_bytes"] = int64(len(raw))

	// ---- pre-migration state (hash-only reads, independent of cas_refs) ----
	pre, err := gorm.Open(sqlite.Open(work), &gorm.Config{})
	require.NoError(t, err)
	edgesBefore := hexEdgeSet(t, pre)
	digestBefore := snapshotContentDigest(t, pre)
	blobsBefore := blobInventory(t, pre)
	pointersBefore := payloadPointers(t, pre)
	require.NoError(t, pre.Exec("VACUUM").Error) // compact baseline for the OLD layout
	report["pre_page_bytes_vacuumed"] = dbPageBytes(t, pre)
	report["pre_edges"] = len(edgesBefore)
	report["pre_payload_fields"] = len(digestBefore)
	report["pre_blobs"] = len(blobsBefore)
	sqlDB, err := pre.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	// ---- migration via the production startup path ----
	t0 := time.Now()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: work}, hybridTestLogger{})
	require.NoError(t, err)
	report["startup_migration_seconds"] = time.Since(t0).Seconds()
	cas, err := newCasLogStore(ctx, inner, &ContentAddressedConfig{Enabled: true}, hybridTestLogger{})
	require.NoError(t, err)
	defer cas.Close(ctx)

	// ---- post-migration equality ----
	post := cas.db
	require.NoError(t, verifyCasIntegerLayout(post))
	edgesAfter := intEdgeSetAsHex(t, post)
	require.Equal(t, len(edgesBefore), len(edgesAfter), "edge count must be preserved")
	require.Equal(t, edgesBefore, edgesAfter, "every edge must map to the same hash pair")
	digestAfter := snapshotContentDigest(t, post)
	require.Equal(t, len(digestBefore), len(digestAfter))
	require.Equal(t, digestBefore, digestAfter, "hydration must be byte-identical after migration")
	require.Equal(t, blobsBefore, blobInventory(t, post), "blob inventory must be untouched")
	require.Equal(t, pointersBefore, payloadPointers(t, post), "payload pointers must be untouched")

	// ---- orphan-sweep selection parity (old hex vs new integer predicates) ----
	var newRefs, oldRefs, newBlobs, oldBlobs int64
	require.NoError(t, post.Raw(
		"SELECT count(*) FROM cas_refs r WHERE NOT EXISTS (SELECT 1 FROM cas_payloads p JOIN cas_blobs b ON b.hash = p.blob_hash WHERE b.id = r.owner_id)",
	).Scan(&newRefs).Error)
	require.NoError(t, post.Raw(
		"SELECT count(*) FROM cas_refs r JOIN cas_blobs b1 ON b1.id = r.owner_id WHERE NOT EXISTS (SELECT 1 FROM cas_payloads WHERE cas_payloads.blob_hash = b1.hash)",
	).Scan(&oldRefs).Error)
	require.Equal(t, oldRefs, newRefs, "orphan refs selection must be identical under both layouts")
	require.NoError(t, post.Raw(
		"SELECT count(*) FROM cas_blobs b WHERE NOT EXISTS (SELECT 1 FROM cas_payloads p WHERE p.blob_hash = b.hash) AND NOT EXISTS (SELECT 1 FROM cas_refs r WHERE r.target_id = b.id)",
	).Scan(&newBlobs).Error)
	require.NoError(t, post.Raw(
		"SELECT count(*) FROM cas_blobs b WHERE NOT EXISTS (SELECT 1 FROM cas_payloads p WHERE p.blob_hash = b.hash) AND NOT EXISTS (SELECT 1 FROM cas_refs r JOIN cas_blobs b2 ON b2.id = r.target_id WHERE b2.hash = b.hash)",
	).Scan(&oldBlobs).Error)
	require.Equal(t, oldBlobs, newBlobs, "orphan blob selection must be identical under both layouts")
	report["orphan_refs"] = newRefs
	report["orphan_blobs"] = newBlobs

	// ---- sweep timing on migrated data (mutating) ----
	t1 := time.Now()
	require.NoError(t, cas.gcOrphanCas(ctx))
	report["gc_orphan_sweep_seconds"] = time.Since(t1).Seconds()

	// ---- EXPLAIN: the GC reverse probe must use the index ----
	var plan []struct{ Detail string }
	require.NoError(t, post.Raw("EXPLAIN QUERY PLAN SELECT 1 FROM cas_refs WHERE target_id = ?", int64(1)).Scan(&plan).Error)
	require.NotEmpty(t, plan)
	require.NotContains(t, plan[0].Detail, "SCAN", "reverse lookup must use the index: "+plan[0].Detail)

	// ---- write/hydrate/delete cycle on the migrated store ----
	blobsBeforeCycle := casBlobCount(t, cas)
	e := bigChatEntry("intid-snapshot-cycle", "history item", string(bytes.Repeat([]byte("cycle payload "), 400)))
	require.NoError(t, e.SerializeFields())
	require.NoError(t, cas.Create(ctx, e))
	got, err := cas.FindByID(ctx, e.ID)
	require.NoError(t, err)
	require.Equal(t, e.InputHistory, got.InputHistory)
	require.NoError(t, cas.DeleteLog(ctx, e.ID))
	require.Equal(t, blobsBeforeCycle, casBlobCount(t, cas), "the cycle must reclaim its blobs")

	// ---- sizes ----
	// Per-object breakdown is done post-hoc with Python's sqlite3 (dbstat is
	// not compiled into the CGO driver); here we record whole-file sizes.
	report["post_page_bytes"] = dbPageBytes(t, post)
	require.NoError(t, post.Exec("VACUUM").Error)
	report["post_page_bytes_vacuumed"] = dbPageBytes(t, post)

	out, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(outDir, "intid_snapshot_report.json"), out, 0o644))
	t.Logf("snapshot rehearsal report:\n%s", out)
}

func dbPageBytes(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var pages, pageSize int64
	require.NoError(t, db.Raw("PRAGMA page_count").Scan(&pages).Error)
	require.NoError(t, db.Raw("PRAGMA page_size").Scan(&pageSize).Error)
	return pages * pageSize
}

func hexEdgeSet(t *testing.T, db *gorm.DB) map[[2]string]struct{} {
	t.Helper()
	out := map[[2]string]struct{}{}
	rows, err := db.Raw("SELECT owner_hash, target_hash FROM cas_refs").Rows()
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var o, g string
		require.NoError(t, rows.Scan(&o, &g))
		out[[2]string{o, g}] = struct{}{}
	}
	return out
}

func intEdgeSetAsHex(t *testing.T, db *gorm.DB) map[[2]string]struct{} {
	t.Helper()
	out := map[[2]string]struct{}{}
	rows, err := db.Raw("SELECT b1.hash, b2.hash FROM cas_refs r JOIN cas_blobs b1 ON b1.id = r.owner_id JOIN cas_blobs b2 ON b2.id = r.target_id").Rows()
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var o, g string
		require.NoError(t, rows.Scan(&o, &g))
		out[[2]string{o, g}] = struct{}{}
	}
	return out
}

// snapshotContentDigest reconstructs every CAS payload field from manifests
// and segments (never through cas_refs) and digests the assembled bytes.
// casDecodeBlob validates each blob's SHA-256 against its domain, so this is
// also an integrity pass over the whole snapshot.
func snapshotContentDigest(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	cache := map[string]*casBlob{}
	load := func(h string) (*casBlob, error) {
		if b, ok := cache[h]; ok {
			return b, nil
		}
		var b casBlob
		if err := db.Raw("SELECT hash, codec, orig_len, data FROM cas_blobs WHERE hash = ?", h).Scan(&b).Error; err != nil {
			return nil, err
		}
		if b.Hash == "" {
			return nil, fmt.Errorf("missing blob %s", h)
		}
		cache[h] = &b
		return &b, nil
	}
	out := map[string]string{}
	rows, err := db.Raw("SELECT log_id, field, blob_hash FROM cas_payloads ORDER BY log_id, field").Rows()
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var logID, field, mh string
		require.NoError(t, rows.Scan(&logID, &field, &mh))
		mb, err := load(mh)
		require.NoError(t, err, logID+"/"+field)
		raw, err := casDecodeBlob(*mb, casManifestDomain)
		require.NoError(t, err, logID+"/"+field)
		var m casManifest
		require.NoError(t, json.Unmarshal(raw, &m), logID+"/"+field)
		var buf bytes.Buffer
		for _, p := range m.Parts {
			if p.Hash != "" {
				sb, err := load(p.Hash)
				require.NoError(t, err, logID+"/"+field)
				pd, err := casDecodeBlob(*sb, casDataDomain)
				require.NoError(t, err, logID+"/"+field)
				buf.Write(pd)
			} else {
				buf.WriteString(p.Inline)
			}
		}
		require.Equal(t, m.OrigLen, int64(buf.Len()), logID+"/"+field)
		sum := sha256.Sum256(buf.Bytes())
		out[logID+"\x00"+field] = hex.EncodeToString(sum[:])
	}
	return out
}

func blobInventory(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, err := db.Raw("SELECT hash, data FROM cas_blobs").Rows()
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var h string
		var data []byte
		require.NoError(t, rows.Scan(&h, &data))
		sum := sha256.Sum256(data)
		out[h] = hex.EncodeToString(sum[:])
	}
	return out
}

func payloadPointers(t *testing.T, db *gorm.DB) map[[2]string]string {
	t.Helper()
	out := map[[2]string]string{}
	rows, err := db.Raw("SELECT log_id, field, blob_hash FROM cas_payloads").Rows()
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var l, f, b string
		require.NoError(t, rows.Scan(&l, &f, &b))
		out[[2]string{l, f}] = b
	}
	return out
}
