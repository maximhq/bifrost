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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
