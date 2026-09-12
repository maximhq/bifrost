package logstore

// Round-5 defect: normalizeUpdateMapKeys rewrote Go field-name keys to column
// names, but two distinct input keys could normalize to the SAME database
// column ("OutputMessage" and "output_message"). With Go's randomized map
// iteration order the last writer won, so the effective value — and, for a
// string-vs-gorm.Expr clash, whether the write was accepted at all — was
// nondeterministic. Conflicting maps are now rejected before any write, even
// when both values are identical. A schema.Parse failure is likewise fatal:
// the old silent passthrough let Go field-name keys bypass the CAS
// interception entirely.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// casRefCount counts rows in cas_refs so tests can prove the CAS tables were
// left untouched by a rejected update.
func casRefCount(t *testing.T, cas *CasLogStore) int64 {
	t.Helper()
	var n int64
	require.NoError(t, cas.db.Model(&casRef{}).Count(&n).Error)
	return n
}

// casPayloadTotal counts rows in cas_payloads across all logs/fields.
func casPayloadTotal(t *testing.T, cas *CasLogStore) int64 {
	t.Helper()
	var n int64
	require.NoError(t, cas.db.Model(&casPayload{}).Count(&n).Error)
	return n
}

// Defect: {"OutputMessage": A, "output_message": B} both target the
// output_message column; the winner used to depend on map iteration order.
// The update must be rejected before any write, with an error naming the
// column and both original keys, and the row plus all three CAS tables must
// be left exactly as they were.
func TestCas_UpdateMapAliasConflictDifferentValuesRejected(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("conflict-1", strings.Repeat("history ", 40))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	require.Positive(t, casPointerCount(t, cas, "conflict-1", "input_history"))

	blobsBefore := casBlobCount(t, cas)
	refsBefore := casRefCount(t, cas)
	payloadsBefore := casPayloadTotal(t, cas)
	rowBefore, err := inner.FindByID(ctx, "conflict-1")
	require.NoError(t, err)

	bigA := bigOutputJSON(t, strings.Repeat("alias winner A ", 40))
	bigB := bigOutputJSON(t, strings.Repeat("alias winner B ", 40))
	err = cas.Update(ctx, "conflict-1", map[string]interface{}{
		"OutputMessage":  bigA,
		"output_message": bigB,
	})
	require.Error(t, err, "two keys normalizing to the same column must be rejected")
	assert.Contains(t, err.Error(), "output_message", "error must name the conflicting column")
	assert.Contains(t, err.Error(), "OutputMessage", "error must name the first original key")
	assert.Contains(t, err.Error(), "output_message", "error must name the second original key")
	assert.Contains(t, err.Error(), "ambiguous update rejected")

	// Zero side effects: the row and all three CAS tables are unchanged.
	rowAfter, err := inner.FindByID(ctx, "conflict-1")
	require.NoError(t, err)
	assert.Equal(t, rowBefore, rowAfter, "rejected update must not touch the log row")
	assert.Equal(t, blobsBefore, casBlobCount(t, cas), "cas_blobs must be unchanged")
	assert.Equal(t, refsBefore, casRefCount(t, cas), "cas_refs must be unchanged")
	assert.Equal(t, payloadsBefore, casPayloadTotal(t, cas), "cas_payloads must be unchanged")
	assert.Zero(t, casPointerCount(t, cas, "conflict-1", "output_message"),
		"neither conflicting value may reach CAS")

	found, err := cas.FindByID(ctx, "conflict-1")
	require.NoError(t, err)
	assert.Equal(t, entry.InputHistory, found.InputHistory, "existing content must be intact")
}

// Same defect, identical values: accepting "harmless" duplicates would still
// require value-equivalence reasoning (including gorm.Expr) to be safe, so
// the rule is a flat rejection regardless of the values.
func TestCas_UpdateMapAliasConflictSameValueRejected(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("conflict-2", strings.Repeat("history ", 40))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	payloadsBefore := casPayloadTotal(t, cas)

	same := bigOutputJSON(t, strings.Repeat("identical value ", 40))
	err := cas.Update(ctx, "conflict-2", map[string]interface{}{
		"OutputMessage":  same,
		"output_message": same,
	})
	require.Error(t, err, "identical values must still be rejected: the rule is key-based, not value-based")
	assert.Contains(t, err.Error(), "both normalize to column")
	assert.Contains(t, err.Error(), "output_message")

	assert.Equal(t, payloadsBefore, casPayloadTotal(t, cas), "cas_payloads must be unchanged")
	assert.Zero(t, casPointerCount(t, cas, "conflict-2", "output_message"))
	found, err := cas.FindByID(ctx, "conflict-2")
	require.NoError(t, err)
	assert.Equal(t, entry.InputHistory, found.InputHistory)
}

// Same defect, mixed types: a string under one spelling and a gorm.Expr under
// the other used to be accepted or rejected depending on which key was
// iterated last. The conflict check runs before the payload type check, so
// the ambiguity error wins deterministically.
func TestCas_UpdateMapAliasConflictStringVsExprRejected(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("conflict-3", strings.Repeat("history ", 40))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	payloadsBefore := casPayloadTotal(t, cas)

	big := bigOutputJSON(t, strings.Repeat("expr clash ", 40))
	err := cas.Update(ctx, "conflict-3", map[string]interface{}{
		"OutputMessage":  big,
		"output_message": gorm.Expr("NULL"),
	})
	require.Error(t, err, "string vs gorm.Expr on the same column must be rejected")
	assert.Contains(t, err.Error(), "both normalize to column")
	assert.Contains(t, err.Error(), "output_message")

	assert.Equal(t, payloadsBefore, casPayloadTotal(t, cas), "cas_payloads must be unchanged")
	assert.Zero(t, casPointerCount(t, cas, "conflict-3", "output_message"))
	found, err := cas.FindByID(ctx, "conflict-3")
	require.NoError(t, err)
	assert.Equal(t, entry.InputHistory, found.InputHistory)
}

// A conflict between two non-payload spellings (e.g. user_id) must also be
// rejected before the row write, proving the guard is not payload-specific.
func TestCas_UpdateMapAliasConflictNonPayloadRejected(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := bigChatEntry("conflict-4", "tiny")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	err := cas.Update(ctx, "conflict-4", map[string]interface{}{
		"UserID":  "alice",
		"user_id": "bob",
	})
	require.Error(t, err, "non-payload alias conflicts must be rejected too")
	assert.Contains(t, err.Error(), "both normalize to column \"user_id\"")

	row, err := inner.FindByID(ctx, "conflict-4")
	require.NoError(t, err)
	assert.Empty(t, casDerefString(row.UserID), "rejected update must not touch the row")
}
