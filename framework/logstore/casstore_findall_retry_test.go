package logstore

// Round-5 defect: FindAll ran the scoped inner query exactly ONCE and then
// retried ErrCasConcurrentModification by re-hydrating the SAME stale
// in-memory rows up to casRootRowRetryLimit times — never re-reading the root
// rows. A single ordinary update that committed between the inner read and
// hydration made every retry fail, silently dropping a still-existing,
// still-matching row from the result with a nil error. FindByID/FindFirst
// already re-ran their full path per retry; FindAll now does the same, and
// the tests below pin that behavior deterministically by hooking the inner
// store so an update (or delete) commits in the half-open window between the
// inner read and hydration.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findallRetryHookStore wraps the inner LogStore and fires a hook after each
// successful inner FindAll — i.e. after the rows have been read but before
// CasLogStore.FindAll hydrates them. Embedding the concrete inner store keeps
// the ScopedDB (scopedDBLogStore) method promoted, so the wrapper stays a
// valid inner store for newCasLogStore's contract.
type findallRetryHookStore struct {
	LogStore
	hook    func() // nil disables; consumed (set to nil) after firing once unless keep
	keep    bool   // re-arm the hook after firing (sustained-contention tests)
	queries int    // every inner FindAll call, hook or not
}

func (s *findallRetryHookStore) FindAll(ctx context.Context, query any, fields ...string) ([]*Log, error) {
	s.queries++
	logs, err := s.LogStore.FindAll(ctx, query, fields...)
	if err == nil && s.hook != nil {
		h := s.hook
		if !s.keep {
			s.hook = nil
		}
		h()
	}
	return logs, err
}

func findallRetryRow(t *testing.T, cas *CasLogStore, id, content, revision string) {
	t.Helper()
	require.NoError(t, cas.Update(context.Background(), id, map[string]interface{}{
		"input_history": content,
		"user_id":       revision,
	}))
}

// Deterministic half-open window: the inner FindAll returns the row, ONE
// update commits before hydration runs, then no further writes exist. The
// retried FindAll must re-run the original query (two inner reads total) and
// return the row with the complete new revision — never an empty list.
func TestCas_FindAllRetryRereadsRootRow(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	const id = "findall-retry-1"
	entry := bigChatEntry(id, "tiny")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	// Pre-seed a CAS-offloaded revision so the stale row read by the inner
	// query has HasObject=true — hydration (and thus the revision binding)
	// only runs for rows with CAS content.
	contentA := strings.Repeat("revision alpha payload ", 40)
	contentB := strings.Repeat("revision beta payload ", 40)
	findallRetryRow(t, cas, id, contentA, "rev-a")

	hooked := &findallRetryHookStore{LogStore: cas.LogStore, hook: func() {
		findallRetryRow(t, cas, id, contentB, "rev-b")
	}}
	cas.LogStore = hooked

	logs, err := cas.FindAll(ctx, map[string]any{"id": id})
	require.NoError(t, err)
	require.NotEmpty(t, logs,
		"after the only update has finished, the row still exists and matches the query; "+
			"an exhausted retry over stale in-memory objects must not silently drop it")
	require.Len(t, logs, 1)
	assert.Equal(t, "rev-b", casDerefString(logs[0].UserID),
		"the served row must be the fresh revision the retry re-read from the root table")
	assert.Equal(t, contentB, logs[0].InputHistory,
		"the served row must carry the complete new payload, not a torn or stale one")
	assert.Equal(t, 2, hooked.queries,
		"the retry must re-run the full original query path, not re-hydrate the stale objects")
	require.Nil(t, hooked.hook, "the update must fire exactly once: no further writes exist")
}

// The retry legitimately serves a changed result set: if the row is deleted
// inside the window, the re-run query no longer sees it and an empty result
// is the fresh truth. This pins that re-reading happens (two inner reads) and
// that deletion — unlike a mere update — is the only way the row disappears.
func TestCas_FindAllRetryAcceptsDeletedRow(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	const id = "findall-retry-2"
	entry := bigChatEntry(id, "tiny")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	// Pre-seed a CAS-offloaded revision so hydration runs on the stale row
	// and the deletion is detected by the revision binding.
	findallRetryRow(t, cas, id, strings.Repeat("revision alpha payload ", 40), "rev-a")

	hooked := &findallRetryHookStore{LogStore: cas.LogStore, hook: func() {
		require.NoError(t, cas.DeleteLog(ctx, id))
	}}
	cas.LogStore = hooked

	logs, err := cas.FindAll(ctx, map[string]any{"id": id})
	require.NoError(t, err)
	assert.Empty(t, logs, "a deleted row may legitimately vanish from the retried result")
	assert.Equal(t, 2, hooked.queries,
		"the concurrent-modification retry must re-run the original query, and the re-run must see the deletion")
}

// Under SUSTAINED concurrent modification (every inner read loses the race)
// the retry budget still exhausts after casRootRowRetryLimit full query
// attempts and degrades the way the path always has: warn + skip the losing
// rows, nil error, no partially hydrated logs served.
func TestCas_FindAllRetryBudgetExhaustionDegrades(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	const id = "findall-retry-3"
	entry := bigChatEntry(id, "tiny")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	// Pre-seed a CAS-offloaded revision so hydration runs on every attempt's
	// rows and each one hits the revision binding.
	contentB := strings.Repeat("revision gamma payload ", 40)
	findallRetryRow(t, cas, id, strings.Repeat("revision alpha payload ", 40), "rev-a")

	// The hook re-arms on every inner read and alternates between two
	// revisions, so every attempt's rows are already stale (user_id flipped)
	// by the time hydration runs — sustained contention, never converging.
	contentC := strings.Repeat("revision delta payload ", 40)
	flip := false
	hooked := &findallRetryHookStore{LogStore: cas.LogStore, keep: true,
		hook: func() {
			flip = !flip
			if flip {
				findallRetryRow(t, cas, id, contentB, "rev-g")
			} else {
				findallRetryRow(t, cas, id, contentC, "rev-h")
			}
		}}
	cas.LogStore = hooked

	logs, err := cas.FindAll(ctx, map[string]any{"id": id})
	require.NoError(t, err, "exhaustion degrades per row (warn + skip), never errors")
	assert.Empty(t, logs, "the perpetually-losing row is skipped after budget exhaustion")
	assert.Equal(t, casRootRowRetryLimit, hooked.queries,
		"the budget counts FULL query-path attempts, each re-reading the root rows")
}
