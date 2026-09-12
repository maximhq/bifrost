package logstore

// Regression tests for the correctness audit fixes. Each test names the defect
// it locks down; see docs/BIFROST-LOG-CAS-IMPLEMENTATION.md.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func casPointerCount(t *testing.T, cas *CasLogStore, logID, field string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, cas.db.Model(&casPayload{}).
		Where("log_id = ? AND field = ?", logID, field).Count(&n).Error)
	return n
}

func casManifestHashFor(t *testing.T, cas *CasLogStore, logID, field string) string {
	t.Helper()
	var hashes []string
	require.NoError(t, cas.db.Model(&casPayload{}).
		Where("log_id = ? AND field = ?", logID, field).Pluck("blob_hash", &hashes).Error)
	require.NotEmpty(t, hashes)
	return hashes[0]
}

func bigOutputJSON(t *testing.T, s string) string {
	t.Helper()
	data, err := sonic.Marshal([]schemas.ChatMessage{
		{Role: "assistant", Content: &schemas.ChatMessageContent{ContentStr: strPtr(s)}},
	})
	require.NoError(t, err)
	return string(data)
}

// Defect: Update stored payload blobs but never set has_object, so a row that
// first offloads a field via Update becomes permanently unhydratable.
func TestCas_UpdateSetsHasObject(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("flag-1", "tiny") // below threshold: row-only
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	row, err := inner.FindByID(ctx, "flag-1")
	require.NoError(t, err)
	require.False(t, row.HasObject)

	bigOut := bigOutputJSON(t, strings.Repeat("answer ", 60))
	require.NoError(t, cas.Update(ctx, "flag-1", map[string]interface{}{"output_message": bigOut}))

	row, err = inner.FindByID(ctx, "flag-1")
	require.NoError(t, err)
	assert.True(t, row.HasObject, "has_object must flip when a field moves into CAS")

	found, err := cas.FindByID(ctx, "flag-1")
	require.NoError(t, err)
	assert.Equal(t, bigOut, found.OutputMessage, "first offload via Update must stay readable")
}

// Defect: a large field updated to a small value kept its stale CAS pointer,
// and hydration resurrected the old content over the new value.
func TestCas_UpdateLargeToSmallDropsPointer(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("shrink-1", strings.Repeat("history ", 30))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	require.Positive(t, casPointerCount(t, cas, "shrink-1", "input_history"))

	require.NoError(t, cas.Update(ctx, "shrink-1", map[string]interface{}{"input_history": "short now"}))

	assert.Zero(t, casPointerCount(t, cas, "shrink-1", "input_history"),
		"downgraded field must lose its pointer")
	found, err := cas.FindByID(ctx, "shrink-1")
	require.NoError(t, err)
	assert.Equal(t, "short now", found.InputHistory, "new small value must win over old CAS content")
}

// Defect: CreateIfNotExists committed CAS pointer replacement before the ON
// CONFLICT DO NOTHING insert, so a duplicate create changed the existing
// log's payload while leaving its metadata untouched.
func TestCas_CreateIfNotExistsDuplicateIsNoop(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	e1 := bigChatEntry("dup-1", strings.Repeat("first content ", 30))
	require.NoError(t, e1.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, e1))
	blobsBefore := casBlobCount(t, cas)

	e2 := bigChatEntry("dup-1", strings.Repeat("completely different second content ", 30))
	require.NoError(t, e2.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, e2), "duplicate create must stay idempotent")

	assert.Equal(t, blobsBefore, casBlobCount(t, cas), "duplicate create must not write CAS blobs")
	found, err := cas.FindByID(ctx, "dup-1")
	require.NoError(t, err)
	assert.Equal(t, e1.InputHistory, found.InputHistory, "existing log payload must be untouched")
}

// Defect: a failing Create (unique violation) had already committed its CAS
// writes. Now the whole operation rolls back.
func TestCas_CreateDuplicateRollsBackCas(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	e1 := bigChatEntry("dupc-1", strings.Repeat("first content ", 30))
	require.NoError(t, e1.SerializeFields())
	require.NoError(t, cas.Create(ctx, e1))
	blobsBefore := casBlobCount(t, cas)

	e2 := bigChatEntry("dupc-1", strings.Repeat("second content ", 30))
	require.NoError(t, e2.SerializeFields())
	err := cas.Create(ctx, e2)
	require.Error(t, err, "duplicate Create must fail")
	assert.Equal(t, blobsBefore, casBlobCount(t, cas), "failed Create must leave no CAS side effects")
}

// Defect: BatchCreateIfNotExists looped independent CAS+insert commits,
// breaking the inner store's single-transaction batch semantics; and a
// duplicate ID inside the batch overwrote an existing row's payload.
func TestCas_BatchCreateIdempotent(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	existing := bigChatEntry("batch-0", strings.Repeat("original batch content ", 30))
	require.NoError(t, existing.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, existing))

	dup := bigChatEntry("batch-0", strings.Repeat("mutated batch content ", 30))
	require.NoError(t, dup.SerializeFields())
	fresh := bigChatEntry("batch-1", strings.Repeat("fresh batch content ", 30))
	require.NoError(t, fresh.SerializeFields())

	require.NoError(t, cas.BatchCreateIfNotExists(ctx, []*Log{fresh, dup}))

	found, err := cas.FindByID(ctx, "batch-0")
	require.NoError(t, err)
	assert.Equal(t, existing.InputHistory, found.InputHistory, "existing row must survive a duplicate in the batch")

	found, err = cas.FindByID(ctx, "batch-1")
	require.NoError(t, err)
	assert.Equal(t, fresh.InputHistory, found.InputHistory)
}

// Defect: Update accepted the inner store's value-form Log by falling through
// to delegation, so its payload fields bypassed CAS entirely.
func TestCas_UpdateValueFormLogIntercepted(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("value-1", "tiny")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	bigOut := bigOutputJSON(t, strings.Repeat("value form answer ", 40))
	valueEntry := Log{OutputMessage: bigOut}
	require.NoError(t, cas.Update(ctx, "value-1", valueEntry))

	row, err := inner.FindByID(ctx, "value-1")
	require.NoError(t, err)
	assert.Empty(t, row.OutputMessage, "value-form update payload must not bypass CAS")
	found, err := cas.FindByID(ctx, "value-1")
	require.NoError(t, err)
	assert.Equal(t, bigOut, found.OutputMessage)
}

// Defect: gcOrphanCas required a blob to have neither incoming nor outgoing
// edges before deletion, so a dead manifest and its segments kept each other
// alive forever. The sweep must compute reachability from live log roots.
func TestCas_GcOrphanSweepBreaksMutualKeepAlive(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("orphan-1", strings.Repeat("orphan content ", 30))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	require.Positive(t, casBlobCount(t, cas))

	// Simulate a row deletion that bypasses DeleteLog (as Flush and direct
	// maintenance do): the pointers, refs and blobs all become orphans.
	require.NoError(t, cas.db.Exec("DELETE FROM logs WHERE id = ?", "orphan-1").Error)

	require.NoError(t, cas.gcOrphanCas(ctx))

	var blobs, payloads, refs int64
	require.NoError(t, cas.db.Model(&casBlob{}).Count(&blobs).Error)
	require.NoError(t, cas.db.Model(&casPayload{}).Count(&payloads).Error)
	require.NoError(t, cas.db.Model(&casRef{}).Count(&refs).Error)
	assert.Zero(t, payloads)
	assert.Zero(t, refs)
	assert.Zero(t, blobs, "dead manifest/segment pairs must not keep each other alive")
}

// Defect: replacing a field's content never reclaimed the old manifest (its
// comment claimed gcForManifests ran; it did not), leaking every revision.
func TestCas_UpdateReclaimsReplacedManifest(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("rev-1", strings.Repeat("first revision ", 40))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	oldManifest := casManifestHashFor(t, cas, "rev-1", "input_history")

	require.NoError(t, cas.Update(ctx, "rev-1", map[string]interface{}{
		"input_history": bigOutputJSON(t, strings.Repeat("second revision ", 40)),
	}))

	var n int64
	require.NoError(t, cas.db.Model(&casBlob{}).Where("hash = ?", oldManifest).Count(&n).Error)
	assert.Zero(t, n, "replaced manifest must be reclaimed unless still referenced")
}

// Defect: reads verified only codec and length. A blob replaced with valid
// zstd of the same length but different content was served without error.
func TestCas_TamperedSameLengthBlobDetected(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("tamper-1", strings.Repeat("legit content ", 30))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	// Replace one segment blob with valid zstd of the same length, different bytes.
	var seg casBlob
	require.NoError(t, cas.db.
		Where("hash IN (SELECT target_hash FROM cas_refs LIMIT 1)").
		First(&seg).Error)
	fake := strings.Repeat("z", int(seg.OrigLen))
	require.NoError(t, cas.db.Model(&casBlob{}).Where("hash = ?", seg.Hash).
		Updates(map[string]interface{}{"data": casEncoder.EncodeAll([]byte(fake), nil)}).Error)

	_, err := cas.FindByID(ctx, "tamper-1")
	require.Error(t, err, "hash mismatch must fail the read, not serve substituted content")
	assert.Contains(t, err.Error(), "hash mismatch")
}

// Defect: a missing or corrupt blob only produced a warning; FindByID
// returned success with empty content. Corruption must be an explicit error.
func TestCas_MissingManifestIsError(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("missing-1", strings.Repeat("content ", 30))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	manifest := casManifestHashFor(t, cas, "missing-1", "input_history")
	require.NoError(t, cas.db.Where("hash = ?", manifest).Delete(&casBlob{}).Error)

	_, err := cas.FindByID(ctx, "missing-1")
	require.Error(t, err, "missing manifest must surface as an error")
}

// Defect: a crafted manifest with a negative OrigLen was passed straight to
// make() and panicked. Persisted metadata is untrusted input.
func TestCas_ManifestNegativeOrigLenNoPanic(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("neg-1", strings.Repeat("content ", 30))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	evil := []byte(`{"v":1,"l":-1,"p":[]}`)
	evilHash := casHash(casManifestDomain, evil)
	require.NoError(t, cas.db.Create(&casBlob{
		Hash: evilHash, Codec: casCodecZstd,
		OrigLen: int64(len(evil)), Data: casEncoder.EncodeAll(evil, nil),
	}).Error)
	require.NoError(t, cas.db.Model(&casPayload{}).
		Where("log_id = ? AND field = ?", "neg-1", "input_history").
		Update("blob_hash", evilHash).Error)

	_, err := cas.FindByID(ctx, "neg-1")
	require.Error(t, err, "out-of-range manifest length must be an integrity error")
	assert.Contains(t, err.Error(), "out of range")
}

// Defect: Flush deleted processing rows directly, leaving their CAS blobs for
// a full sweep that itself never reclaimed manifest/segment pairs.
func TestCas_FlushReclaimsCas(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("flush-1", strings.Repeat("stale processing content ", 30))
	entry.Status = "processing"
	entry.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	require.Positive(t, casBlobCount(t, cas))

	require.NoError(t, cas.Flush(ctx, time.Now().UTC().Add(-time.Hour)))

	assert.Zero(t, casBlobCount(t, cas), "Flush must reclaim the CAS content of the rows it deletes")
}

// Defect: HydrateBillingChunk inherited the RDB no-op while CAS offloads the
// modality payloads pricing reads, silently losing repricing inputs.
func TestCas_HydrateBillingChunk(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("bill-1", "tiny")
	entry.Object = "speech"
	entry.SpeechOutput = `{"duration":12,"segments":["` + strings.Repeat("audio ", 40) + `"]}`
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	row, err := inner.FindByID(ctx, "bill-1")
	require.NoError(t, err)
	require.True(t, row.HasObject)
	require.Empty(t, row.SpeechOutput, "speech payload must be offloaded")

	result, err := cas.HydrateBillingChunk(ctx, []*Log{row})
	require.NoError(t, err)
	assert.Contains(t, result.Hydrated, "bill-1")
	assert.Empty(t, result.Unpriceable)
	assert.Equal(t, entry.SpeechOutput, row.SpeechOutput, "billing hydration must recover the modality payload")
}

// Defect: the projection gate only recognized exact bare column names, so a
// valid "*" projection suppressed hydration entirely.
func TestCas_FindAllWildcardProjection(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("wild-1", strings.Repeat("context ", 40))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	logs, err := cas.FindAll(ctx, map[string]any{"id": "wild-1"}, "*")
	require.NoError(t, err)
	require.Len(t, logs, 1)
	assert.Equal(t, entry.InputHistory, logs[0].InputHistory, `"*" must hydrate, not suppress`)
}

// Design doc: fallback (opaque-blob) storage must be observable.
func TestCas_FallbackMetric(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("fb-1", "tiny")
	entry.Tools = "not json at all " + strings.Repeat("x", 200)
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	stats, err := cas.CasStorageStats(ctx)
	require.NoError(t, err)
	assert.Positive(t, stats.Fallbacks, "opaque-blob fallback must be counted")

	found, err := cas.FindByID(ctx, "fb-1")
	require.NoError(t, err)
	assert.Equal(t, entry.Tools, found.Tools, "fallback field must still round trip byte-identically")
}

// Defect: Update on a missing log wrote CAS content first and failed later;
// now the whole transaction rolls back and ErrNotFound is returned.
func TestCas_UpdateMissingRollsBackCas(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	bigOut := bigOutputJSON(t, strings.Repeat("ghost ", 60))
	err := cas.Update(ctx, "does-not-exist", map[string]interface{}{"output_message": bigOut})
	assert.ErrorIs(t, err, ErrNotFound)
	assert.Zero(t, casBlobCount(t, cas), "failed update must leave no CAS side effects")

	// Empty-map update on a missing row keeps the inner ErrNotFound semantics.
	err = cas.Update(ctx, "does-not-exist", map[string]interface{}{})
	assert.ErrorIs(t, err, ErrNotFound)
}

// Defect: struct updates cleared CAS'd fields to "" which gorm omits, so the
// row kept the old inline bytes; the explicit follow-up write must empty them.
func TestCas_UpdateStructClearsRowColumn(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("struct-1", "tiny history")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	bigOut := bigOutputJSON(t, strings.Repeat("struct form answer ", 40))
	updated := bigChatEntry("struct-1", "tiny history")
	updated.OutputMessageParsed = nil // keep the explicit big TEXT, don't re-serialize "ok"
	updated.OutputMessage = bigOut
	require.NoError(t, cas.Update(ctx, "struct-1", updated))

	row, err := inner.FindByID(ctx, "struct-1")
	require.NoError(t, err)
	assert.Empty(t, row.OutputMessage, "CAS'd column must be emptied in the row, not keep stale bytes")
	found, err := cas.FindByID(ctx, "struct-1")
	require.NoError(t, err)
	assert.Equal(t, bigOut, found.OutputMessage)
}

// --- round-2 review defects ---

// Defect: Update(map) with a payload field set to nil wrote NULL into the row
// but left the old CAS pointer in place, so FindByID resurrected the old CAS
// content over the NULL and has_object stayed stale.
func TestCas_UpdateMapNilClearsPointer(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("nilupd-1", strings.Repeat("nil update content ", 30))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	manifest := casManifestHashFor(t, cas, "nilupd-1", "input_history")

	require.NoError(t, cas.Update(ctx, "nilupd-1", map[string]interface{}{"input_history": nil}))

	assert.Zero(t, casPointerCount(t, cas, "nilupd-1", "input_history"),
		"payload field set to nil must drop its CAS pointer")
	var n int64
	require.NoError(t, cas.db.Model(&casBlob{}).Where("hash = ?", manifest).Count(&n).Error)
	assert.Zero(t, n, "cleared field's manifest must be reclaimed")
	row, err := inner.FindByID(ctx, "nilupd-1")
	require.NoError(t, err)
	assert.False(t, row.HasObject, "has_object must be recomputed after the nil clear")
	found, err := cas.FindByID(ctx, "nilupd-1")
	require.NoError(t, err)
	assert.Empty(t, found.InputHistory, "old CAS content must not be resurrected over the NULL")
}

// Defect: DeleteLog/DeleteLogs ran the row deletion and the CAS cleanup in two
// separate transactions; a recreate of the same ID in between had its fresh
// CAS pointers deleted by the cleanup. Row deletion and cleanup now share one
// transaction, so an immediate same-ID recreate hydrates normally.
func TestCas_DeleteThenRecreateSameIDPayloadSurvives(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	e1 := bigChatEntry("race-1", strings.Repeat("first life ", 30))
	require.NoError(t, e1.SerializeFields())
	require.NoError(t, cas.Create(ctx, e1))
	require.NoError(t, cas.DeleteLog(ctx, "race-1"))
	assert.Zero(t, casBlobCount(t, cas), "delete must reclaim the CAS content")

	// Immediate same-ID recreate: its new pointers must survive.
	e2 := bigChatEntry("race-1", strings.Repeat("second life ", 30))
	require.NoError(t, e2.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, e2))
	found, err := cas.FindByID(ctx, "race-1")
	require.NoError(t, err, "recreated log must hydrate after the delete path's CAS cleanup")
	assert.Equal(t, e2.InputHistory, found.InputHistory)
	assert.Positive(t, casPointerCount(t, cas, "race-1", "input_history"))

	// Same for the batch delete path.
	require.NoError(t, cas.DeleteLogs(ctx, []string{"race-1"}))
	assert.Zero(t, casBlobCount(t, cas))
	e3 := bigChatEntry("race-1", strings.Repeat("third life ", 30))
	require.NoError(t, e3.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, e3))
	found, err = cas.FindByID(ctx, "race-1")
	require.NoError(t, err)
	assert.Equal(t, e3.InputHistory, found.InputHistory)
}

// Defect: a log with has_object=true whose cas_payloads rows were all gone
// hydrated zero fields and FindByID returned silent success with empty
// content. Missing pointers must be an explicit error.
func TestCas_MissingAllPayloadRowsIsError(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("noptrs-1", strings.Repeat("pointer content ", 30))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	require.Positive(t, casPointerCount(t, cas, "noptrs-1", "input_history"))

	require.NoError(t, cas.db.Where("log_id = ?", "noptrs-1").Delete(&casPayload{}).Error)

	_, err := cas.FindByID(ctx, "noptrs-1")
	require.Error(t, err, "has_object=true with no cas_payloads rows must fail the read")
	assert.Contains(t, err.Error(), "cas payloads")
}

// Defect: migrationBackfillCasHasObject used raw "has_object = 0"/"SET 1"
// literals, which fail on PostgreSQL boolean columns. The GORM-dialect-safe
// rewrite must repair flagged rows and leave pointer-less rows untouched
// (verified on SQLite; PostgreSQL still needs a live test).
func TestCas_MigrationBackfillHasObject(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	withPointer := bigChatEntry("backfill-1", strings.Repeat("backfill content ", 30))
	require.NoError(t, withPointer.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, withPointer))
	withoutPointer := bigChatEntry("backfill-2", "tiny")
	require.NoError(t, withoutPointer.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, withoutPointer))
	// Simulate the legacy bug the migration repairs.
	require.NoError(t, cas.db.Model(&Log{}).Where("id = ?", "backfill-1").Update("has_object", false).Error)
	require.NoError(t, cas.db.Model(&Log{}).Where("id = ?", "backfill-2").Update("has_object", false).Error)

	// The store-open migration pass already recorded this ID as applied; clear
	// the record (migrator.DefaultOptions table) so the migration re-runs.
	require.NoError(t, cas.db.Exec("DELETE FROM migrations WHERE id = ?", "cas_has_object_backfill").Error)

	require.NoError(t, migrationBackfillCasHasObject(ctx, cas.db, hybridTestLogger{}))

	var repaired, untouched Log
	require.NoError(t, cas.db.Where("id = ?", "backfill-1").First(&repaired).Error)
	assert.True(t, repaired.HasObject, "row with CAS pointers must be repaired")
	require.NoError(t, cas.db.Where("id = ?", "backfill-2").First(&untouched).Error)
	assert.False(t, untouched.HasObject, "pointer-less row must stay false")

	// The repaired row must be readable again.
	found, err := cas.FindByID(ctx, "backfill-1")
	require.NoError(t, err)
	assert.Equal(t, withPointer.InputHistory, found.InputHistory)
}

// Defect: FindFirst/FindAll only warned on hydration failure but still
// returned the damaged entry, so corrupt rows leaked empty payload fields
// into results as if they were real content. Failed entries must be dropped
// (FindFirst: ErrNotFound, matching the inner store's not-found shape) and
// counted in HydrateErrors.
func TestCas_FindSkipsCorruptEntries(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	healthy := bigChatEntry("aaa-healthy", strings.Repeat("healthy content ", 30))
	require.NoError(t, healthy.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, healthy))
	corrupt := bigChatEntry("zzz-corrupt", strings.Repeat("corrupt content ", 30))
	require.NoError(t, corrupt.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, corrupt))
	manifest := casManifestHashFor(t, cas, "zzz-corrupt", "input_history")
	require.NoError(t, cas.db.Where("hash = ?", manifest).Delete(&casBlob{}).Error)

	// FindAll drops the corrupt entry and keeps the healthy one.
	logs, err := cas.FindAll(ctx, map[string]any{"id": []string{"aaa-healthy", "zzz-corrupt"}})
	require.NoError(t, err)
	require.Len(t, logs, 1, "corrupt entry must be skipped, not served damaged")
	assert.Equal(t, "aaa-healthy", logs[0].ID)
	assert.Equal(t, healthy.InputHistory, logs[0].InputHistory)

	// FindFirst on a query whose only match is corrupt: ErrNotFound.
	_, err = cas.FindFirst(ctx, map[string]any{"id": "zzz-corrupt"})
	assert.ErrorIs(t, err, ErrNotFound, "unhydratable first match must surface as ErrNotFound")

	// FindFirst over both: the healthy entry is still reachable.
	first, err := cas.FindFirst(ctx, map[string]any{"status": "success"})
	require.NoError(t, err)
	assert.Equal(t, "aaa-healthy", first.ID)

	stats, err := cas.CasStorageStats(ctx)
	require.NoError(t, err)
	assert.Positive(t, stats.HydrateErrors, "skipped entries must be counted as hydrate errors")
}

// Defect: the CAS create path kept hybrid's search summary (last user message,
// truncated to 2048 bytes), so keywords that only appear in the output or in
// early history were unfindable. The row summary must match the plain RDB
// path's full BuildContentSummary range.
func TestCas_SearchSummaryCoversOutput(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("search-1", "innocent question")
	entry.OutputMessageParsed = &schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{ContentStr: strPtr("the zanzibar unicorn appears here " + strings.Repeat("tail ", 60))},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	require.Positive(t, casPointerCount(t, cas, "search-1", "output_message"),
		"big output must be offloaded to CAS")

	row, err := inner.FindByID(ctx, "search-1")
	require.NoError(t, err)
	assert.Contains(t, row.ContentSummary, "zanzibar unicorn",
		"content summary must cover the output, not just the last user message")

	result, err := cas.SearchLogs(ctx, SearchFilters{ContentSearch: "zanzibar unicorn"}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1, "output-only keyword must be searchable in CAS mode")
	assert.Equal(t, "search-1", result.Logs[0].ID)
}

// --- round-3 review defects ---

// Defect: hydrateFields read cas_payloads/manifests/segments through the
// caller-scoped handle in separate autocommit SELECTs. A QueryScope carrying
// logs-column predicates (user_id/virtual_key_id, the shape the enterprise
// DAC wrapper builds) made every hydration fail with a SQL error — the CAS
// tables have no such columns. CAS reads now run unscoped inside one read
// transaction; row-level authorization stays at the log root, where the
// scoped inner-store fetch enforces it.
func TestCas_QueryScopeCompatibleHydration(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("scope-1", strings.Repeat("scoped history ", 30))
	entry.UserID = strPtr("u1")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	require.Positive(t, casPointerCount(t, cas, "scope-1", "input_history"))

	// Same closure shape the enterprise DAC wrapper builds (see
	// logstoreparity_test.go QueryScopeDAC): OR-joined IN predicates over
	// logs ownership columns.
	scopedCtx := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
		return db.Where("(user_id IN ? OR virtual_key_id IN ?)", []string{"u1"}, []string{"vk2"})
	})

	found, err := cas.FindByID(scopedCtx, "scope-1")
	require.NoError(t, err, "scoped read must hydrate, not SQL-error on CAS tables")
	assert.Equal(t, entry.InputHistory, found.InputHistory)

	logs, err := cas.FindAll(scopedCtx, map[string]any{"id": "scope-1"})
	require.NoError(t, err, "scoped FindAll must hydrate, not SQL-error on CAS tables")
	require.Len(t, logs, 1)
	assert.Equal(t, entry.InputHistory, logs[0].InputHistory)

	// Fail-closed scope: the scoped inner fetch must still hide the row
	// entirely — removing the scope from CAS reads must not widen visibility.
	hidden, err := cas.FindByID(queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
		return db.Where("1 = 0")
	}), "scope-1")
	assert.ErrorIs(t, err, ErrNotFound, "out-of-scope row must stay invisible")
	assert.Nil(t, hidden)
}

// Defect: hydration's separate autocommit SELECTs had no shared snapshot. A
// concurrent Update could commit and reclaim the old manifest between the
// pointer read and the manifest read, so a healthy row was misreported as
// "manifest missing". All CAS reads now share one read-transaction snapshot,
// so a reader sees either the complete old revision or the complete new one.
// Run with -race for the writer/reader overlap.
func TestCas_HydrateSnapshotConsistencyUnderConcurrentUpdates(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	const id = "snap-1"
	entry := bigChatEntry(id, "tiny")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	contentA := bigOutputJSON(t, strings.Repeat("alpha ", 120))
	contentB := bigOutputJSON(t, strings.Repeat("beta ", 120))
	// The row's pre-update output ("ok") is a legal observation until the
	// first writer commit lands; capture it as an allowed complete revision.
	initial, err := cas.FindByID(ctx, id)
	require.NoError(t, err)
	initialOut := initial.OutputMessage
	const iterations = 200
	done := make(chan error, 1)
	go func() {
		var werr error
		for i := 0; i < iterations; i++ {
			content := contentA
			if i%2 == 1 {
				content = contentB
			}
			if err := cas.Update(ctx, id, map[string]interface{}{"output_message": content}); err != nil {
				werr = err
				break
			}
		}
		done <- werr
	}()
	for i := 0; i < iterations; i++ {
		found, err := cas.FindByID(ctx, id)
		require.NoError(t, err,
			"hydrate must never observe a torn revision (old pointer, reclaimed manifest)")
		require.Contains(t, []string{contentA, contentB, initialOut}, found.OutputMessage,
			"hydrated content must be one complete revision, never a mix")
	}
	require.NoError(t, <-done)
}

// Defect: map Update with a []byte payload value passed straight into the row
// update (only bare string/nil were intercepted): a large []byte bypassed CAS
// entirely, and a small one changed the row while the stale pointer survived.
// []byte now normalizes to string and follows the string path.
func TestCas_UpdateMapByteSlicePayload(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("bytes-1", "tiny")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	big := []byte(bigOutputJSON(t, strings.Repeat("bytes answer ", 60)))
	require.NoError(t, cas.Update(ctx, "bytes-1", map[string]interface{}{"output_message": big}))
	assert.Positive(t, casPointerCount(t, cas, "bytes-1", "output_message"),
		"large []byte payload must be stored in CAS, not in the row")
	row, err := inner.FindByID(ctx, "bytes-1")
	require.NoError(t, err)
	assert.Empty(t, row.OutputMessage, "[]byte offload must empty the row column")
	found, err := cas.FindByID(ctx, "bytes-1")
	require.NoError(t, err)
	assert.Equal(t, string(big), found.OutputMessage)

	// Small []byte: row-resident downgrade, pointer dropped, no resurrection.
	require.NoError(t, cas.Update(ctx, "bytes-1", map[string]interface{}{"output_message": []byte("small now")}))
	assert.Zero(t, casPointerCount(t, cas, "bytes-1", "output_message"),
		"downgraded []byte field must lose its pointer")
	found, err = cas.FindByID(ctx, "bytes-1")
	require.NoError(t, err)
	assert.Equal(t, "small now", found.OutputMessage)
}

// Defect: map Update with an unsupported payload value type (gorm.Expr, typed
// pointers, ...) changed the row column while leaving the stale CAS pointer,
// and hydration resurrected the old content over the new value. Such updates
// are now rejected up front (fail closed) before any CAS or row write.
func TestCas_UpdateMapUnsupportedPayloadTypeRejected(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("expr-1", strings.Repeat("expr guard content ", 30))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	err := cas.Update(ctx, "expr-1", map[string]interface{}{"input_history": gorm.Expr("NULL")})
	require.Error(t, err, "gorm.Expr on a payload field must be rejected, not bypass CAS")
	assert.Contains(t, err.Error(), "unsupported value type")
	// Rejection must be side-effect free: pointer intact, content unchanged.
	assert.Positive(t, casPointerCount(t, cas, "expr-1", "input_history"))
	found, err := cas.FindByID(ctx, "expr-1")
	require.NoError(t, err)
	assert.Equal(t, entry.InputHistory, found.InputHistory)

	// Non-payload columns keep full gorm expressiveness.
	require.NoError(t, cas.Update(ctx, "expr-1", map[string]interface{}{"status": gorm.Expr("'error'")}))
	row, err := cas.FindByID(ctx, "expr-1")
	require.NoError(t, err)
	assert.Equal(t, "error", row.Status)
}

// Defect: hydrateLogForBilling only checked token_usage. A modality row
// (speech/ocr/image_generation/...) whose modality payload pointer was lost
// hydrated zero billing fields yet returned success, was counted as Hydrated,
// and billing continued without its input. A missing modality payload after
// hydration must fail closed into Unpriceable, exactly like missing usage.
func TestCas_BillingModalityPointerMissingIsUnpriceable(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("ocrbill-1", strings.Repeat("history for the ocr row ", 30))
	entry.Object = "ocr"
	entry.OCROutput = `{"pages":[{"text":"` + strings.Repeat("page text ", 60) + `"}]}`
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	require.Positive(t, casPointerCount(t, cas, "ocrbill-1", "ocr_output"),
		"big ocr payload must be offloaded to CAS")
	require.Positive(t, casPointerCount(t, cas, "ocrbill-1", "input_history"),
		"row must keep another pointer so the missing modality pointer is a partial loss")

	// Simulate the loss this defect is about: only the modality pointer row
	// disappears (manual surgery / partial corruption). The input_history
	// pointer stays, so hydration of the billing field set finds no pointers
	// and the old code returned success with an empty ocr_output.
	require.NoError(t, cas.db.Where("log_id = ? AND field = ?", "ocrbill-1", "ocr_output").
		Delete(&casPayload{}).Error)

	row, err := inner.FindByID(ctx, "ocrbill-1")
	require.NoError(t, err)
	require.True(t, row.HasObject)

	result, err := cas.HydrateBillingChunk(ctx, []*Log{row})
	require.NoError(t, err)
	assert.Contains(t, result.Unpriceable, "ocrbill-1",
		"modality row without its billing payload must be Unpriceable, not billed from a stub")
	assert.NotContains(t, result.Hydrated, "ocrbill-1")
	assert.Empty(t, row.OCROutput, "nothing was recoverable for the billing column")
}

// Defect: the round-2 delete/recreate test was strictly sequential, so the
// old two-transaction delete could not fail it. This is the real race: a
// delete loop against a same-ID create loop on SQLite (writers serialize, so
// every interleaving is one of: delete-all-then-create-fresh, or
// create-then-delete-all). After each create, the invariant is asserted live:
// a present row must hydrate fully and correctly; once the delete loop
// drains, no CAS pointer may outlive its log row.
func TestCas_ConcurrentDeleteRecreateKeepsInvariants(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	const id = "delrace-1"
	e0 := bigChatEntry(id, strings.Repeat("seed life ", 30))
	require.NoError(t, e0.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, e0))

	const iterations = 100
	delErr := make(chan error, 1)
	go func() {
		var err error
		for i := 0; i < iterations; i++ {
			if err = cas.DeleteLog(ctx, id); err != nil {
				break
			}
		}
		delErr <- err
	}()
	for i := 0; i < iterations; i++ {
		e := bigChatEntry(id, strings.Repeat("recreated body ", 30))
		require.NoError(t, e.SerializeFields())
		require.NoError(t, cas.CreateIfNotExists(ctx, e))
		// Live invariant: whenever the read succeeds, the content must be a
		// complete revision — the untouched seed (the create hit the ON
		// CONFLICT before any delete drained) or the recreated entry. A
		// failure is only legal in the concurrent window where the delete
		// commits between the inner row fetch and the CAS hydration snapshot
		// (an inherent read-vs-delete race: the row read is a separate
		// snapshot by construction); the converged checks below then prove no
		// "row present, payload gone" state can persist.
		found, err := cas.FindByID(ctx, id)
		if err == nil {
			require.NotEmpty(t, found.InputHistory, "present row must never serve empty payload")
			require.Contains(t, []string{e0.InputHistory, e.InputHistory}, found.InputHistory,
				"present row must carry a complete revision, never a stale or torn payload")
		}
	}
	require.NoError(t, <-delErr)

	// Converged invariant (no more writers): either the row is gone —
	// FindByID is ErrNotFound AND no row exists — or it exists and hydrates
	// fully and correctly. This is what the old two-transaction delete broke:
	// a recreate interleaved between row delete and CAS cleanup kept a row
	// whose fresh pointers the cleanup then deleted, a persistent
	// "row present, payload gone" state.
	found, err := cas.FindByID(ctx, id)
	if err == nil {
		require.NotEmpty(t, found.InputHistory, "surviving row must hydrate fully")
	} else {
		require.ErrorIs(t, err, ErrNotFound, "after convergence only a deleted row may fail FindByID")
		var n int64
		require.NoError(t, cas.db.Model(&Log{}).Where("id = ?", id).Count(&n).Error)
		assert.Zero(t, n, "a persistent hydrate failure with the row still present is the delete/recreate bug")
	}

	// Converged invariant: no CAS pointer may reference a missing log row.
	var orphans int64
	require.NoError(t, cas.db.Raw(
		"SELECT COUNT(*) FROM cas_payloads WHERE NOT EXISTS (SELECT 1 FROM logs WHERE logs.id = cas_payloads.log_id)",
	).Scan(&orphans).Error)
	assert.Zero(t, orphans, "CAS pointers must not outlive their log row")
	// And the same for manifest reachability: no ref owned by a manifest no
	// pointer references.
	var dangling int64
	require.NoError(t, cas.db.Raw(
		"SELECT COUNT(*) FROM cas_refs WHERE NOT EXISTS (SELECT 1 FROM cas_payloads WHERE cas_payloads.blob_hash = cas_refs.owner_hash)",
	).Scan(&dangling).Error)
	assert.Zero(t, dangling)
}

// --- round-4 review defects ---

// Defect: hydrateLogForBilling ran stripNonBillingPayloadBytes BEFORE the
// modality-integrity check, and strip clears ImageGenerationOutput
// (rdb.go), so a healthy image_generation row whose billing input was fully
// recovered was misjudged Unpriceable. The integrity checks now run on the
// pre-strip column — the exact data billing reads — so only rows whose
// billing input is genuinely empty after hydration fail.
func TestCas_BillingImageGenerationHydratedNotUnpriceable(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("imgbill-1", "tiny")
	entry.Object = "image_generation"
	entry.ImageGenerationOutputParsed = &schemas.BifrostImageGenerationResponse{
		Data: []schemas.ImageData{{B64JSON: strings.Repeat("aGVsbG8=", 32), RevisedPrompt: "a shiny red apple"}},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	require.Positive(t, casPointerCount(t, cas, "imgbill-1", "image_generation_output"),
		"large image payload must be offloaded to CAS")

	row, err := inner.FindByID(ctx, "imgbill-1")
	require.NoError(t, err)
	require.True(t, row.HasObject)
	require.Empty(t, row.ImageGenerationOutput, "image payload must be offloaded from the row")

	result, err := cas.HydrateBillingChunk(ctx, []*Log{row})
	require.NoError(t, err)
	assert.Contains(t, result.Hydrated, "imgbill-1",
		"recovered image billing input must be Hydrated, not Unpriceable")
	assert.NotContains(t, result.Unpriceable, "imgbill-1")
	assert.True(t, row.billingPayloadsHydrated)
	require.NotNil(t, row.ImageGenerationOutputParsed,
		"parsed billing input must survive the billing hydration")
	assert.NotEmpty(t, row.ImageGenerationOutputParsed.Data,
		"pricing reads the image count from the parsed structure")
	assert.Empty(t, row.ImageGenerationOutputParsed.Data[0].B64JSON,
		"strip still releases the base64 bytes pricing never reads")
	assert.Empty(t, row.ImageGenerationOutput,
		"serialized column is cleared by strip after the checks")
}

// Defect: billingPayloadsHydrated was set before the integrity checks ran, so
// a row that failed them kept the success marker; billingRowNeedsHydration
// then skipped it on a second HydrateBillingChunk call, and the caller —
// which blocks only on Unpriceable — billed it from the lossy stub. The
// marker must only be set after every check passes.
func TestCas_BillingFailureMarkerNotSetOnUnpriceable(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("ocrbill-2", strings.Repeat("history for the ocr row ", 30))
	entry.Object = "ocr"
	entry.OCROutput = `{"pages":[{"text":"` + strings.Repeat("page text ", 60) + `"}]}`
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	// Simulate the partial loss: only the modality pointer disappears.
	require.NoError(t, cas.db.Where("log_id = ? AND field = ?", "ocrbill-2", "ocr_output").
		Delete(&casPayload{}).Error)

	row, err := inner.FindByID(ctx, "ocrbill-2")
	require.NoError(t, err)
	require.True(t, row.HasObject)

	for call := 1; call <= 2; call++ {
		result, err := cas.HydrateBillingChunk(ctx, []*Log{row})
		require.NoError(t, err)
		assert.Contains(t, result.Unpriceable, "ocrbill-2",
			"call %d: modality input still unrecoverable, must stay Unpriceable", call)
		assert.NotContains(t, result.Hydrated, "ocrbill-2",
			"call %d: a failed row must never be counted Hydrated", call)
	}
}

// Round-5 unified read snapshot: the root row AND the CAS side tables are
// read inside one transaction (REPEATABLE READ on PostgreSQL, one WAL
// snapshot on SQLite), so there is no second observation point and no
// revision-verification/retry machinery anymore: a reader sees one complete
// revision or another complete revision, never old metadata bound to new
// content. This replaces round-4's verifyRootRowRevision +
// ErrCasConcurrentModification bounded-retry loop, whose manual
// binding-column list could never be complete (model and other scalar
// columns were missing) and whose comparison of database columns against
// possibly business-modified in-memory objects produced false positives.
// Run with -race.
func TestCas_UnifiedReadSnapshotUnderConcurrentUpdates(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	const id = "revbind-1"
	entry := bigChatEntry(id, "tiny")
	entry.UserID = strPtr("rev-init")
	entry.Model = "model-init"
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	contentA := strings.Repeat("revision alpha payload ", 40)
	contentB := strings.Repeat("revision beta payload ", 40)
	// The pre-update row (serialized chat history, model-init, rev-init) is a
	// legal observation until the first writer commit lands.
	initial, err := cas.FindByID(ctx, id)
	require.NoError(t, err)
	initialOut := initial.InputHistory

	// check asserts that metadata and hydrated content come from ONE
	// revision: the ownership column (what QueryScope predicates run over),
	// the scalar model column (deliberately outside round-4's manual
	// binding list) and the CAS'd payload must all pair up.
	check := func(t *testing.T, userID, model, content string) {
		t.Helper()
		switch userID {
		case "rev-init":
			assert.Equal(t, "model-init", model,
				"initial metadata revision must stay paired with its model")
			assert.Equal(t, initialOut, content,
				"initial metadata must be served with the initial content")
		case "rev-a":
			assert.Equal(t, "model-a", model,
				"metadata version A must be served with model version A")
			assert.Equal(t, contentA, content,
				"metadata version A must be served with content version A")
		case "rev-b":
			assert.Equal(t, "model-b", model,
				"metadata version B must be served with model version B")
			assert.Equal(t, contentB, content,
				"metadata version B must be served with content version B")
		default:
			t.Fatalf("served an unknown revision %q: metadata and content are torn", userID)
		}
	}

	const writerIterations = 300
	const readerIterations = 100
	done := make(chan error, 1)
	go func() {
		var werr error
		for i := 0; i < writerIterations; i++ {
			content, revision, model := contentA, "rev-a", "model-a"
			if i%2 == 1 {
				content, revision, model = contentB, "rev-b", "model-b"
			}
			// The CAS'd payload, an ownership column AND the scalar model
			// column change in ONE writer transaction, so a consistent read
			// must always see all three from the same revision.
			if err := cas.Update(ctx, id, map[string]interface{}{
				"input_history": content,
				"user_id":       revision,
				"model":         model,
			}); err != nil {
				werr = err
				break
			}
			time.Sleep(1 * time.Millisecond)
		}
		done <- werr
	}()
	for i := 0; i < readerIterations; i++ {
		found, err := cas.FindByID(ctx, id)
		// No retry budget exists to exhaust: the unified snapshot must always
		// serve one complete revision, never a torn one, never a hydrate
		// false alarm (manifest missing, corruption...).
		require.NoError(t, err,
			"the unified read snapshot must always serve one complete revision")
		check(t, casDerefString(found.UserID), found.Model, found.InputHistory)

		// The list path upholds the same binding when it serves the row.
		// Sampled every few rounds to keep the reader loop fast.
		if i%5 == 0 {
			logs, err := cas.FindAll(ctx, map[string]any{"id": id})
			require.NoError(t, err)
			if len(logs) == 1 {
				check(t, casDerefString(logs[0].UserID), logs[0].Model, logs[0].InputHistory)
			}
		}
	}
	require.NoError(t, <-done)
}

// casDerefString reads a nullable string column for test assertions.
func casDerefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Defect: updateFromMap only recognized DB column names, while gorm's Updates
// also accepts Go field names ("OutputMessage" resolves to output_message via
// LookUpField). A Go-name key slipped past the CAS interception: the row
// column changed, the stale pointer survived, and hydration resurrected the
// OLD content over the new value. Keys are now normalized to column names
// before any CAS decision, so both spellings behave identically.
func TestCas_UpdateMapGoFieldNameGoesToCAS(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("gofield-1", "tiny")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	bigOut := bigOutputJSON(t, strings.Repeat("go field name update ", 40))
	require.NoError(t, cas.Update(ctx, "gofield-1", map[string]interface{}{"OutputMessage": bigOut}))

	assert.Positive(t, casPointerCount(t, cas, "gofield-1", "output_message"),
		"Go field-name key must route the payload through CAS")
	row, err := inner.FindByID(ctx, "gofield-1")
	require.NoError(t, err)
	assert.Empty(t, row.OutputMessage, "row column must be emptied once CAS is authoritative")
	found, err := cas.FindByID(ctx, "gofield-1")
	require.NoError(t, err)
	assert.Equal(t, bigOut, found.OutputMessage,
		"the new value must win; the old pointer must not resurrect stale content")
}

// Same defect, expression flavor: gorm.Expr under a Go field-name key used to
// bypass the payload type rejection that the column-name spelling enforces.
// Normalization happens first, so both spellings are rejected identically.
func TestCas_UpdateMapGoFieldNameExprRejected(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("gofield-2", strings.Repeat("expr guard content ", 30))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	require.Positive(t, casPointerCount(t, cas, "gofield-2", "input_history"))

	err := cas.Update(ctx, "gofield-2", map[string]interface{}{"InputHistory": gorm.Expr("NULL")})
	require.Error(t, err, "gorm.Expr on a payload field must be rejected, whichever spelling is used")
	assert.Contains(t, err.Error(), "unsupported value type")
	// Rejection must be side-effect free: pointer intact, content unchanged.
	assert.Positive(t, casPointerCount(t, cas, "gofield-2", "input_history"))
	found, err := cas.FindByID(ctx, "gofield-2")
	require.NoError(t, err)
	assert.Equal(t, entry.InputHistory, found.InputHistory)
}
