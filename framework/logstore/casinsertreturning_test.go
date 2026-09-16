package logstore

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"strings"
	"testing"
	"time"
)

func TestCasResolveBlobIDsTransactional(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(t.Context())
	hash := casHash(casDataDomain, []byte("mapping"))
	sentinel := fmt.Errorf("rollback mapping")
	require.ErrorIs(t, cas.db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, casInsertBlob(tx, &casBlob{Hash: hash, Data: []byte("mapping")}))
		ids, err := casResolveBlobIDs(tx, []string{hash, hash, "missing"})
		require.NoError(t, err)
		require.Len(t, ids, 1)
		require.Positive(t, ids[hash])
		// A write on the same transaction verifies query rows were released.
		require.NoError(t, tx.Exec("UPDATE cas_blobs SET orig_len=7 WHERE hash=?", hash).Error)
		return sentinel
	}), sentinel)
	ids, err := casResolveBlobIDs(cas.db, []string{hash})
	require.NoError(t, err)
	require.Empty(t, ids)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = casResolveBlobIDs(cas.db.WithContext(ctx), []string{hash})
	require.Error(t, err)
}

func TestCasBlobInsertTimestamps(t *testing.T) {
	for _, pg := range []bool{false, true} {
		name := "sqlite"
		if pg {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			cas, _ := boundaryStore(t, pg)
			defer cas.Close(t.Context())
			now := time.Unix(1700000000, 0)
			tx := cas.db.Session(&gorm.Session{NowFunc: func() time.Time { return now }})
			for i, stamp := range []int64{0, 123456789} {
				blob := casBlob{Hash: casHash(casDataDomain, []byte(name+string(rune('a'+i)))), Codec: casCodecZstd, OrigLen: 3, Data: []byte("abc"), CreatedAt: stamp}
				require.NoError(t, casInsertBlob(tx, &blob))
				var got casBlob
				require.NoError(t, tx.Where("hash = ?", blob.Hash).Take(&got).Error)
				want := stamp
				if want == 0 {
					want = now.Unix()
				}
				require.Equal(t, want, got.CreatedAt)
				require.Positive(t, got.ID)
				blob.CreatedAt = 999
				require.NoError(t, casInsertBlob(tx, &blob))
				var again casBlob
				require.NoError(t, tx.Where("hash = ?", blob.Hash).Take(&again).Error)
				require.Equal(t, got, again)
			}
		})
	}
}

func TestCasBlobInsertDoesNotReturnUnusedIDs(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(t.Context())
	inserts, returned := 0, 0
	capture := func(tx *gorm.DB) {
		sql := strings.ToUpper(strings.ReplaceAll(tx.Statement.SQL.String(), "`", ""))
		if strings.HasPrefix(sql, "INSERT INTO CAS_BLOBS") {
			inserts++
			if strings.Contains(sql, "RETURNING") {
				returned++
			}
		}
	}
	require.NoError(t, cas.db.Callback().Create().After("gorm:create").Register("cas_test:insert_sql", capture))
	require.NoError(t, cas.db.Callback().Raw().After("gorm:raw").Register("cas_test:insert_sql", capture))
	text := strings.Repeat("synthetic shared chunk ", 80)
	for _, id := range []string{"returning-a", "returning-b"} {
		require.NoError(t, cas.Create(t.Context(), bigChatEntry(id, text, text)))
		got, err := cas.FindByID(t.Context(), id)
		require.NoError(t, err)
		require.Contains(t, got.InputHistory, text)
	}
	require.Positive(t, inserts)
	require.Zero(t, returned, "CAS resolves ids by hash in the same transaction; blob inserts must not return unused ids")
	var bad int64
	require.NoError(t, cas.db.Raw("SELECT count(*) FROM cas_blobs WHERE id<=0 OR created_at<=0").Scan(&bad).Error)
	require.Zero(t, bad)
	require.NoError(t, cas.DeleteLog(t.Context(), "returning-a"))
	got, err := cas.FindByID(t.Context(), "returning-b")
	require.NoError(t, err)
	require.Contains(t, got.InputHistory, text)
	require.NoError(t, cas.DeleteLog(t.Context(), "returning-b"))
	require.NoError(t, cas.db.Raw("SELECT count(*) FROM cas_blobs").Scan(&bad).Error)
	require.Zero(t, bad)
}

type casWriteSQLCounts struct {
	blobInserts    int
	blobRows       int
	idLookups      int
	refInserts     int
	refRows        int
	payloadReads   int
	payloadDeletes int
	payloadInserts int
	payloadRows    int
}

func registerCasWriteSQLCounts(t *testing.T, db *gorm.DB, counts *casWriteSQLCounts) {
	t.Helper()
	capture := func(tx *gorm.DB) {
		sql := strings.ToUpper(strings.NewReplacer("`", "", "\"", "").Replace(tx.Statement.SQL.String()))
		sql = strings.Join(strings.Fields(sql), " ")
		switch {
		case strings.HasPrefix(sql, "INSERT INTO CAS_BLOBS"):
			counts.blobInserts++
			counts.blobRows += strings.Count(sql, "),(") + 1
		case strings.HasPrefix(sql, "SELECT ID,HASH FROM CAS_BLOBS"):
			counts.idLookups++
		case strings.HasPrefix(sql, "INSERT INTO CAS_REFS"):
			counts.refInserts++
			counts.refRows += strings.Count(sql, "),(") + 1
		case strings.HasPrefix(sql, "SELECT BLOB_HASH FROM CAS_PAYLOADS") && strings.Contains(sql, "LOG_ID") && strings.Contains(sql, "FIELD"):
			counts.payloadReads++
		case strings.HasPrefix(sql, "DELETE FROM CAS_PAYLOADS"):
			counts.payloadDeletes++
		case strings.HasPrefix(sql, "INSERT INTO CAS_PAYLOADS"):
			counts.payloadInserts++
			counts.payloadRows += strings.Count(sql, "),(") + 1
		}
	}
	for _, callback := range []struct {
		name     string
		register func(string, func(*gorm.DB)) error
	}{
		{"create", db.Callback().Create().After("gorm:create").Register},
		{"delete", db.Callback().Delete().After("gorm:delete").Register},
		{"query", db.Callback().Query().After("gorm:query").Register},
		{"raw", db.Callback().Raw().After("gorm:raw").Register},
		{"row", db.Callback().Row().After("gorm:row").Register},
	} {
		require.NoError(t, callback.register("cas_test:write_counts_"+callback.name, capture))
	}
}

func opaqueCASFields(prefix string) map[string]interface{} {
	return map[string]interface{}{
		"raw_request":               prefix + strings.Repeat(" request", 20),
		"raw_response":              prefix + strings.Repeat(" response", 20),
		"passthrough_request_body":  prefix + strings.Repeat(" pass-request", 20),
		"passthrough_response_body": prefix + strings.Repeat(" pass-response", 20),
	}
}

func TestCasWriteFieldsResolvesAllFieldIDsInOneQuery(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(t.Context())
	require.NoError(t, cas.Create(t.Context(), bigChatEntry("mapping-update", "small")))

	var counts casWriteSQLCounts
	registerCasWriteSQLCounts(t, cas.db, &counts)
	updates := opaqueCASFields("mapping-update")
	require.NoError(t, cas.Update(t.Context(), "mapping-update", updates))
	require.Equal(t, casWriteSQLCounts{blobInserts: 1, blobRows: 8, idLookups: 1, refInserts: 1, refRows: 4, payloadReads: 1, payloadDeletes: 1, payloadInserts: 1, payloadRows: 4}, counts)

	got, err := cas.FindByID(t.Context(), "mapping-update")
	require.NoError(t, err)
	require.Equal(t, updates["raw_request"], got.RawRequest)
	require.Equal(t, updates["raw_response"], got.RawResponse)
	require.Equal(t, updates["passthrough_request_body"], got.PassthroughRequestBody)
	require.Equal(t, updates["passthrough_response_body"], got.PassthroughResponseBody)
	requireCasGraphReachable(t, cas)
}

func TestCasBatchCreateResolvesIDsPerLog(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(t.Context())
	entries := []*Log{
		bigChatEntry("mapping-batch-a", "small"),
		bigChatEntry("mapping-batch-b", "small"),
	}
	for i, entry := range entries {
		fields := opaqueCASFields(fmt.Sprintf("mapping-batch-%d", i))
		entry.RawRequest = fields["raw_request"].(string)
		entry.RawResponse = fields["raw_response"].(string)
		entry.PassthroughRequestBody = fields["passthrough_request_body"].(string)
		entry.PassthroughResponseBody = fields["passthrough_response_body"].(string)
	}

	var counts casWriteSQLCounts
	registerCasWriteSQLCounts(t, cas.db, &counts)
	require.NoError(t, cas.BatchCreateIfNotExists(t.Context(), entries))
	require.Equal(t, 2, counts.idLookups, "the batch transaction must resolve once per inserted log, not across log boundaries")
	for _, entry := range entries {
		got, err := cas.FindByID(t.Context(), entry.ID)
		require.NoError(t, err)
		require.Equal(t, entry.RawRequest, got.RawRequest)
		require.Equal(t, entry.RawResponse, got.RawResponse)
	}
}

func TestCasResolveBlobIDsChunksDriverParameters(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(t.Context())
	const hashCount = 901
	hashes := make([]string, 0, hashCount)
	for i := 0; i < hashCount; i++ {
		hash := casHash(casDataDomain, []byte(fmt.Sprintf("chunk-boundary-%04d", i)))
		hashes = append(hashes, hash)
		require.NoError(t, casInsertBlob(cas.db, &casBlob{Hash: hash, Codec: casCodecZstd, OrigLen: 1, Data: []byte("x")}))
	}

	var counts casWriteSQLCounts
	registerCasWriteSQLCounts(t, cas.db, &counts)
	ids, err := casResolveBlobIDs(cas.db, hashes)
	require.NoError(t, err)
	require.Len(t, ids, hashCount)
	require.Equal(t, 2, counts.idLookups)
}

func TestCasWriteFieldsDeduplicatesBlobsAcrossFields(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(t.Context())
	require.NoError(t, cas.Create(t.Context(), bigChatEntry("blob-shared", "small")))
	shared := strings.Repeat("same opaque content ", 20)

	var counts casWriteSQLCounts
	registerCasWriteSQLCounts(t, cas.db, &counts)
	require.NoError(t, cas.Update(t.Context(), "blob-shared", map[string]interface{}{
		"raw_request":  shared,
		"raw_response": shared,
	}))
	require.Equal(t, 1, counts.blobInserts)
	require.Equal(t, 2, counts.blobRows, "one shared data blob and one shared manifest blob")
	require.Equal(t, 1, counts.idLookups)
	require.Equal(t, 1, counts.refInserts)
	require.Equal(t, 1, counts.refRows, "identical fields share one manifest-to-segment edge")
	require.Equal(t, 1, counts.payloadInserts)
	require.Equal(t, 2, counts.payloadRows)
	got, err := cas.FindByID(t.Context(), "blob-shared")
	require.NoError(t, err)
	require.Equal(t, shared, got.RawRequest)
	require.Equal(t, shared, got.RawResponse)
}

func TestCasInsertBlobsChunksDriverParameters(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(t.Context())
	const blobCount = casSQLParameterLimit/5 + 2
	blobs := make([]casBlob, 0, blobCount)
	for i := 0; i < blobCount; i++ {
		raw := []byte(fmt.Sprintf("blob-chunk-%04d", i))
		blobs = append(blobs, casBlob{
			Hash:    casHash(casDataDomain, raw),
			Codec:   casCodecZstd,
			OrigLen: int64(len(raw)),
			Data:    casEncoder.EncodeAll(raw, nil),
		})
	}
	var counts casWriteSQLCounts
	registerCasWriteSQLCounts(t, cas.db, &counts)
	require.NoError(t, casInsertBlobs(cas.db, blobs))
	require.Equal(t, 2, counts.blobInserts)
	require.Equal(t, blobCount, counts.blobRows)
	var stored int64
	require.NoError(t, cas.db.Model(&casBlob{}).Count(&stored).Error)
	require.EqualValues(t, blobCount, stored)
}

func TestCasInsertRefsChunksAndDeduplicates(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(t.Context())
	const refCount = casSQLParameterLimit/2 + 2
	refs := make([]casRef, 0, refCount+2)
	for i := 0; i < refCount; i++ {
		refs = append(refs, casRef{OwnerID: int64(i + 1), TargetID: int64(i + 1001)})
	}
	refs = append(refs, refs[0], refs[refCount-1])
	var counts casWriteSQLCounts
	registerCasWriteSQLCounts(t, cas.db, &counts)
	require.NoError(t, casInsertRefs(cas.db, refs))
	require.Equal(t, 2, counts.refInserts)
	require.Equal(t, refCount, counts.refRows)
	var stored int64
	require.NoError(t, cas.db.Model(&casRef{}).Count(&stored).Error)
	require.EqualValues(t, refCount, stored)
}

func TestCasReplacePayloadsChunksAndLastFieldWins(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(t.Context())
	const payloadCount = casSQLParameterLimit + 2
	payloads := make([]casPayload, 0, payloadCount+1)
	for i := 0; i < payloadCount; i++ {
		payloads = append(payloads, casPayload{
			LogID:    "payload-chunk",
			Field:    fmt.Sprintf("field-%04d", i),
			BlobHash: fmt.Sprintf("manifest-%04d", i),
		})
	}
	payloads = append(payloads, casPayload{LogID: "payload-chunk", Field: "field-0000", BlobHash: "manifest-last"})
	var counts casWriteSQLCounts
	registerCasWriteSQLCounts(t, cas.db, &counts)
	old, err := casReplacePayloads(cas.db, "payload-chunk", payloads)
	require.NoError(t, err)
	require.Empty(t, old)
	require.Equal(t, 2, counts.payloadReads)
	require.Equal(t, 2, counts.payloadDeletes)
	require.Equal(t, 4, counts.payloadInserts)
	require.Equal(t, payloadCount, counts.payloadRows)
	var stored []casPayload
	require.NoError(t, cas.db.Where("log_id = ?", "payload-chunk").Find(&stored).Error)
	require.Len(t, stored, payloadCount)
	var first casPayload
	require.NoError(t, cas.db.Where("log_id = ? AND field = ?", "payload-chunk", "field-0000").Take(&first).Error)
	require.Equal(t, "manifest-last", first.BlobHash)
}

func TestCasDuplicatePreparedFieldLastWinsAndGCs(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(t.Context())
	first := strings.Repeat("first revision ", 30)
	last := strings.Repeat("last revision ", 30)
	preparedFirst, err := casPrepareField("raw_request", []byte(first), 32)
	require.NoError(t, err)
	preparedLast, err := casPrepareField("raw_request", []byte(last), 32)
	require.NoError(t, err)
	entry := bigChatEntry("payload-last", "small")
	require.NoError(t, cas.Create(t.Context(), entry))
	require.NoError(t, cas.db.Transaction(func(tx *gorm.DB) error {
		old, err := casStorePreparedFields(tx, entry.ID, []*casPreparedField{preparedFirst, preparedLast})
		if err != nil {
			return err
		}
		if err := gcForManifests(tx, old); err != nil {
			return err
		}
		if err := tx.Model(&Log{}).Where("id = ?", entry.ID).Update("has_object", true).Error; err != nil {
			return err
		}
		return writeCASInventory(tx, entry.ID, "native")
	}))
	got, err := cas.FindByID(t.Context(), entry.ID)
	require.NoError(t, err)
	require.Equal(t, last, got.RawRequest)
	var firstManifest int64
	require.NoError(t, cas.db.Model(&casBlob{}).Where("hash = ?", preparedFirst.manifestHash).Count(&firstManifest).Error)
	require.Zero(t, firstManifest, "the superseded duplicate field manifest must be reclaimed")
	requireCasGraphReachable(t, cas)
}

func TestCasBatchedWriteFaultsPreserveOldRevision(t *testing.T) {
	for _, table := range []string{"cas_blobs", "cas_refs", "cas_payloads"} {
		t.Run(table, func(t *testing.T) {
			cas, _ := newTestCas(t)
			defer cas.Close(t.Context())
			oldFields := opaqueCASFields("fault-old")
			entry := bigChatEntry("batch-fault-"+table, "small")
			require.NoError(t, cas.Create(t.Context(), entry))
			require.NoError(t, cas.Update(t.Context(), entry.ID, oldFields))
			before := boundarySnapshot(t, cas.db)
			require.NoError(t, cas.db.Exec(fmt.Sprintf(
				"CREATE TRIGGER fail_batch BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT, 'injected batch failure'); END", table,
			)).Error)

			err := cas.Update(t.Context(), entry.ID, opaqueCASFields("fault-new"))
			require.Error(t, err)
			require.Equal(t, before, boundarySnapshot(t, cas.db))
			got, readErr := cas.FindByID(t.Context(), entry.ID)
			require.NoError(t, readErr)
			require.Equal(t, oldFields["raw_request"], got.RawRequest)
			require.Equal(t, oldFields["raw_response"], got.RawResponse)
			require.Equal(t, oldFields["passthrough_request_body"], got.PassthroughRequestBody)
			require.Equal(t, oldFields["passthrough_response_body"], got.PassthroughResponseBody)
			requireCasGraphReachable(t, cas)
		})
	}
}

func TestCasBatchedWriteMissingEndpointsRollback(t *testing.T) {
	for _, endpoint := range []string{"manifest", "segment"} {
		t.Run(endpoint, func(t *testing.T) {
			cas, _ := newTestCas(t)
			defer cas.Close(t.Context())
			entry := bigChatEntry("batch-missing-"+endpoint, "small")
			require.NoError(t, cas.Create(t.Context(), entry))
			before := boundarySnapshot(t, cas.db)
			content := strings.Repeat("missing endpoint content ", 30)
			prepared, err := casPrepareField("raw_request", []byte(content), 32)
			require.NoError(t, err)
			hash := prepared.manifestHash
			if endpoint == "segment" {
				require.NotEmpty(t, prepared.segmentHashes)
				hash = prepared.segmentHashes[0]
			}
			require.NoError(t, cas.db.Exec(fmt.Sprintf(
				"CREATE TRIGGER remove_endpoint AFTER INSERT ON cas_blobs WHEN NEW.hash='%s' BEGIN DELETE FROM cas_blobs WHERE id=NEW.id; END", hash,
			)).Error)

			err = cas.Update(t.Context(), entry.ID, map[string]interface{}{"raw_request": content})
			require.ErrorContains(t, err, endpoint+" blob id missing after insert")
			require.Equal(t, before, boundarySnapshot(t, cas.db))
			requireCasGraphReachable(t, cas)
		})
	}
}

func TestCasBatchedWriteCancellationRollsBack(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(t.Context())
	entry := bigChatEntry("batch-cancel", "small")
	require.NoError(t, cas.Create(t.Context(), entry))
	oldFields := opaqueCASFields("cancel-old")
	require.NoError(t, cas.Update(t.Context(), entry.ID, oldFields))
	before := boundarySnapshot(t, cas.db)
	ctx, cancel := context.WithCancel(t.Context())
	require.NoError(t, cas.db.Callback().Raw().After("gorm:raw").Register("cas_test:cancel_after_blob_batch", func(tx *gorm.DB) {
		sql := strings.ToUpper(strings.TrimSpace(tx.Statement.SQL.String()))
		if strings.HasPrefix(sql, "INSERT INTO CAS_BLOBS") && tx.Error == nil {
			cancel()
		}
	}))

	err := cas.Update(ctx, entry.ID, opaqueCASFields("cancel-new"))
	require.Error(t, err)
	require.Error(t, ctx.Err())
	require.Equal(t, before, boundarySnapshot(t, cas.db))
	got, readErr := cas.FindByID(t.Context(), entry.ID)
	require.NoError(t, readErr)
	require.Equal(t, oldFields["raw_request"], got.RawRequest)
	requireCasGraphReachable(t, cas)
}

func TestCasBatchedPrimitiveBoundariesDialects(t *testing.T) {
	for _, pg := range []bool{false, true} {
		name := "sqlite"
		if pg {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			cas, _ := boundaryStore(t, pg)
			defer cas.Close(t.Context())
			now := time.Unix(1800000000, 0)
			db := cas.db.Session(&gorm.Session{NowFunc: func() time.Time { return now }})
			sentinelRaw := []byte("immutable sentinel")
			sentinel := casBlob{
				Hash:      casHash(casDataDomain, sentinelRaw),
				Codec:     casCodecZstd,
				OrigLen:   int64(len(sentinelRaw)),
				Data:      casEncoder.EncodeAll(sentinelRaw, nil),
				CreatedAt: 123456789,
			}
			require.NoError(t, casInsertBlob(db, &sentinel))

			const blobCount = casSQLParameterLimit/5 + 2
			blobs := make([]casBlob, 0, blobCount)
			blobs = append(blobs, casBlob{Hash: sentinel.Hash, Codec: "changed", OrigLen: 999, Data: []byte("changed"), CreatedAt: 999})
			for i := 1; i < blobCount; i++ {
				raw := []byte(fmt.Sprintf("%s-batch-blob-%04d", name, i))
				blobs = append(blobs, casBlob{Hash: casHash(casDataDomain, raw), Codec: casCodecZstd, OrigLen: int64(len(raw)), Data: casEncoder.EncodeAll(raw, nil)})
			}
			var counts casWriteSQLCounts
			registerCasWriteSQLCounts(t, db, &counts)
			require.NoError(t, casInsertBlobs(db, blobs))
			require.Equal(t, 2, counts.blobInserts)
			require.Equal(t, blobCount, counts.blobRows)
			var unchanged casBlob
			require.NoError(t, db.Where("hash = ?", sentinel.Hash).Take(&unchanged).Error)
			require.Equal(t, sentinel.Codec, unchanged.Codec)
			require.Equal(t, sentinel.OrigLen, unchanged.OrigLen)
			require.Equal(t, sentinel.Data, unchanged.Data)
			require.Equal(t, sentinel.CreatedAt, unchanged.CreatedAt)
			var badTimestamps int64
			require.NoError(t, db.Model(&casBlob{}).Where("hash <> ? AND created_at <> ?", sentinel.Hash, now.Unix()).Count(&badTimestamps).Error)
			require.Zero(t, badTimestamps)

			const refCount = casSQLParameterLimit/2 + 2
			refs := make([]casRef, 0, refCount)
			for i := 0; i < refCount; i++ {
				refs = append(refs, casRef{OwnerID: int64(i + 1), TargetID: int64(i + 10001)})
			}
			beforeRefStatements := counts.refInserts
			require.NoError(t, casInsertRefs(db, refs))
			require.Equal(t, 2, counts.refInserts-beforeRefStatements)

			const payloadCount = casSQLParameterLimit + 2
			payloads := make([]casPayload, 0, payloadCount)
			for i := 0; i < payloadCount; i++ {
				payloads = append(payloads, casPayload{LogID: "dialect-payloads", Field: fmt.Sprintf("field-%04d", i), BlobHash: sentinel.Hash})
			}
			beforeReads, beforeDeletes, beforeInserts := counts.payloadReads, counts.payloadDeletes, counts.payloadInserts
			_, err := casReplacePayloads(db, "dialect-payloads", payloads)
			require.NoError(t, err)
			require.Equal(t, 2, counts.payloadReads-beforeReads)
			require.Equal(t, 2, counts.payloadDeletes-beforeDeletes)
			require.Equal(t, 4, counts.payloadInserts-beforeInserts)

			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			before := boundarySnapshot(t, db)
			require.Error(t, casInsertBlobs(db.WithContext(ctx), blobs[:1]))
			require.Error(t, casInsertRefs(db.WithContext(ctx), refs[:1]))
			_, err = casReplacePayloads(db.WithContext(ctx), "dialect-payloads", payloads[:1])
			require.Error(t, err)
			require.Equal(t, before, boundarySnapshot(t, db))
		})
	}
}
