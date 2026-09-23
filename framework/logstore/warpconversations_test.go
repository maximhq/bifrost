package logstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newWarpConversationStore returns a store with just the conversation tables.
// See newToolCallNamesStore for why the DSN is a named shared cache.
func newWarpConversationStore(t *testing.T) *RDBLogStore {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&WarpConversation{}, &WarpMessage{}))
	return &RDBLogStore{db: db}
}

// seedWarpConversation creates a thread with one exchange.
func seedWarpConversation(t *testing.T, store *RDBLogStore, ownerID, id, title string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.CreateWarpConversation(ctx, &WarpConversation{
		ID: id, OwnerID: ownerID, Title: title, CreatedAt: at, UpdatedAt: at,
	}))
	require.NoError(t, store.AppendWarpMessages(ctx, ownerID, id, []WarpMessage{
		{ID: id + "-u", Role: "user", Content: title, CreatedAt: at},
		{ID: id + "-a", Role: "assistant", Content: "answer", CreatedAt: at},
	}))
}

func TestWarpConversationRoundTrip(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedWarpConversation(t, store, "user-1", "c1", "what did we spend?", now)

	list, err := store.ListWarpConversations(ctx, "user-1", 50)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "what did we spend?", list[0].Title)

	detail, err := store.GetWarpConversation(ctx, "user-1", "c1")
	require.NoError(t, err)
	require.Len(t, detail.Messages, 2)
	require.Equal(t, "user", detail.Messages[0].Role)
	require.Equal(t, "assistant", detail.Messages[1].Role)
	require.Equal(t, 0, detail.Messages[0].Position)
	require.Equal(t, 1, detail.Messages[1].Position)
}

// The whole access-control story is the owner predicate. A thread must be
// invisible to anyone else, and indistinguishable from one that never existed -
// "that exists but is not yours" confirms another person's thread.
func TestWarpConversationIsScopedToItsOwner(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedWarpConversation(t, store, "user-1", "c1", "mine", now)
	seedWarpConversation(t, store, "user-2", "c2", "theirs", now)

	list, err := store.ListWarpConversations(ctx, "user-1", 50)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "c1", list[0].ID)

	_, err = store.GetWarpConversation(ctx, "user-1", "c2")
	require.ErrorIs(t, err, ErrWarpConversationNotFound, "another owner's thread must read as missing")

	require.ErrorIs(t, store.DeleteWarpConversation(ctx, "user-1", "c2"), ErrWarpConversationNotFound)
	require.ErrorIs(t,
		store.AppendWarpMessages(ctx, "user-1", "c2", []WarpMessage{{ID: "x", Role: "user", Content: "hi", CreatedAt: now}}),
		ErrWarpConversationNotFound, "appending to another owner's thread must fail")

	// The victim's thread is untouched by all of that.
	detail, err := store.GetWarpConversation(ctx, "user-2", "c2")
	require.NoError(t, err)
	require.Len(t, detail.Messages, 2)
}

// Without user identity every conversation shares one owner, so the history is
// common to the deployment. Same query, different owner - no second code path.
func TestWarpConversationGlobalOwnerSharesHistory(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedWarpConversation(t, store, "__global__", "g1", "shared question", now)

	list, err := store.ListWarpConversations(ctx, "__global__", 50)
	require.NoError(t, err)
	require.Len(t, list, 1)

	// A user-scoped caller must not see the shared history, and vice versa.
	userList, err := store.ListWarpConversations(ctx, "user-1", 50)
	require.NoError(t, err)
	require.Empty(t, userList)
}

func TestWarpConversationListIsMostRecentFirst(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	seedWarpConversation(t, store, "user-1", "old", "older", base)
	seedWarpConversation(t, store, "user-1", "new", "newer", base.Add(30*time.Minute))

	list, err := store.ListWarpConversations(ctx, "user-1", 50)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "new", list[0].ID)
}

// Appending must bump the thread to the top. Messages that landed while the
// timestamp did not would leave the newest thread sinking down the list, which
// reads as the save having failed.
func TestWarpConversationAppendBumpsOrder(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	seedWarpConversation(t, store, "user-1", "old", "older", base)
	seedWarpConversation(t, store, "user-1", "new", "newer", base.Add(30*time.Minute))

	require.NoError(t, store.AppendWarpMessages(ctx, "user-1", "old", []WarpMessage{
		{ID: "old-u2", Role: "user", Content: "follow up", CreatedAt: time.Now().UTC()},
	}))

	list, err := store.ListWarpConversations(ctx, "user-1", 50)
	require.NoError(t, err)
	require.Equal(t, "old", list[0].ID, "the thread just appended to must sort first")

	// Position restarts at 0 for each append, by design: it orders messages
	// within the append that wrote them, and CreatedAt orders the appends. What
	// has to hold is the rendered order, which is what this asserts - the
	// follow-up reads last even though its position is the lowest of the three.
	detail, err := store.GetWarpConversation(ctx, "user-1", "old")
	require.NoError(t, err)
	require.Len(t, detail.Messages, 3)
	require.Equal(t, "old-u2", detail.Messages[2].ID, "the newest turn must render last")
	require.Equal(t, 0, detail.Messages[2].Position, "position is scoped to its own append")
}

func TestWarpConversationDeleteRemovesMessages(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()

	seedWarpConversation(t, store, "user-1", "c1", "delete me", time.Now().UTC())
	require.NoError(t, store.DeleteWarpConversation(ctx, "user-1", "c1"))

	_, err := store.GetWarpConversation(ctx, "user-1", "c1")
	require.ErrorIs(t, err, ErrWarpConversationNotFound)

	// The transcript is the content someone asked to remove; an orphaned row
	// would be a leak of exactly that.
	var orphans int64
	require.NoError(t, store.db.Model(&WarpMessage{}).Where("conversation_id = ?", "c1").Count(&orphans).Error)
	require.Zero(t, orphans)
}

func TestWarpConversationPruneKeepsNewest(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-24 * time.Hour)

	for i := 0; i < 5; i++ {
		seedWarpConversation(t, store, "user-1", string(rune('a'+i)), "thread", base.Add(time.Duration(i)*time.Hour))
	}

	deleted, err := store.PruneWarpConversations(ctx, "user-1", 2)
	require.NoError(t, err)
	require.Equal(t, int64(3), deleted)

	list, err := store.ListWarpConversations(ctx, "user-1", 50)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "e", list[0].ID, "the newest must survive")

	var orphans int64
	require.NoError(t, store.db.Model(&WarpMessage{}).Where("conversation_id = ?", "a").Count(&orphans).Error)
	require.Zero(t, orphans, "pruning must take the transcripts with it")
}

func TestWarpConversationPruneIsNoOpBelowLimit(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()

	seedWarpConversation(t, store, "user-1", "c1", "only one", time.Now().UTC())
	deleted, err := store.PruneWarpConversations(ctx, "user-1", 10)
	require.NoError(t, err)
	require.Zero(t, deleted)
}

func TestWarpConversationMessageCounts(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedWarpConversation(t, store, "user-1", "c1", "one", now)
	seedWarpConversation(t, store, "user-1", "c2", "two", now)

	counts, err := store.CountWarpMessages(ctx, []string{"c1", "c2", "missing"})
	require.NoError(t, err)
	require.Equal(t, 2, counts["c1"])
	require.Equal(t, 2, counts["c2"])
	require.NotContains(t, counts, "missing")
}

// Prune selects the stale ids and then deletes them, and between those two
// steps a thread can receive a new message. Deleting it anyway destroys a live
// conversation the owner is actively using, so the delete has to re-assert
// staleness rather than trust the earlier read.
func TestWarpConversationPruneRevalidatesBeforeDeleting(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	// Three threads, oldest first; keep=2 makes "old" the prune candidate.
	seedWarpConversation(t, store, "u-1", "old", "oldest", base)
	seedWarpConversation(t, store, "u-1", "mid", "middle", base.Add(time.Hour))
	seedWarpConversation(t, store, "u-1", "new", "newest", base.Add(2*time.Hour))

	// The candidate is used again, which is exactly the race: it is no longer
	// among the oldest, so it must survive.
	require.NoError(t, store.AppendWarpMessages(ctx, "u-1", "old", []WarpMessage{
		{ID: "old-u2", Role: "user", Content: "still here", CreatedAt: base.Add(3 * time.Hour)},
	}))

	_, err := store.PruneWarpConversations(ctx, "u-1", 2)
	require.NoError(t, err)

	survived, err := store.GetWarpConversation(ctx, "u-1", "old")
	require.NoError(t, err, "a thread that was used again must not be pruned as stale")
	require.NotNil(t, survived)

	var messages int64
	require.NoError(t, store.db.Model(&WarpMessage{}).Where("conversation_id = ?", "old").Count(&messages).Error)
	require.EqualValues(t, 3, messages, "its messages must survive with it")
}

// Ordering is (CreatedAt, Position), and that pair - not a running count - is
// what keeps a transcript readable.
//
// The property worth pinning is not that positions are dense. It is that an
// answer never renders above the question it answers, and that appends render
// in the order they were made. A dense global count would give the same result
// here, at the cost of a row lock held for the whole append; this asserts the
// outcome so the lock-free scheme is free to keep its per-append positions.
func TestWarpConversationTranscriptOrderSurvivesManyAppends(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.CreateWarpConversation(ctx, &WarpConversation{
		ID: "c-1", OwnerID: "u-1", Title: "t", CreatedAt: at, UpdatedAt: at,
	}))

	for i := 0; i < 5; i++ {
		require.NoError(t, store.AppendWarpMessages(ctx, "u-1", "c-1", []WarpMessage{
			{ID: fmt.Sprintf("m-%d-u", i), Role: "user", Content: "q", CreatedAt: at.Add(time.Duration(i) * time.Minute)},
			{ID: fmt.Sprintf("m-%d-a", i), Role: "assistant", Content: "a", CreatedAt: at.Add(time.Duration(i) * time.Minute)},
		}))
	}

	detail, err := store.GetWarpConversation(ctx, "u-1", "c-1")
	require.NoError(t, err)
	require.Len(t, detail.Messages, 10)
	for i, message := range detail.Messages {
		require.Equal(t, fmt.Sprintf("m-%d-%s", i/2, map[bool]string{true: "u", false: "a"}[i%2 == 0]), message.ID,
			"message %d is out of order", i)
	}
}

// The age sweep is the other half of retention, and it must expire on the
// cutoff alone - a thread inside the window survives however many others go.
func TestWarpConversationDeleteOlderThanExpiresOnAge(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedWarpConversation(t, store, "u-1", "ancient", "long ago", now.Add(-90*24*time.Hour))
	seedWarpConversation(t, store, "u-2", "old", "a while back", now.Add(-40*24*time.Hour))
	seedWarpConversation(t, store, "u-1", "recent", "yesterday", now.Add(-24*time.Hour))

	deleted, err := store.DeleteWarpConversationsOlderThan(ctx, now.Add(-30*24*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted, "the sweep crosses owners; retention is a deployment policy")

	_, err = store.GetWarpConversation(ctx, "u-1", "recent")
	require.NoError(t, err, "a thread inside the window must survive")
	_, err = store.GetWarpConversation(ctx, "u-1", "ancient")
	require.ErrorIs(t, err, ErrWarpConversationNotFound)

	var orphans int64
	require.NoError(t, store.db.Model(&WarpMessage{}).Where("conversation_id IN ?", []string{"ancient", "old"}).Count(&orphans).Error)
	require.Zero(t, orphans, "expiring a thread must take its transcript with it")
}

// The sweep re-asserts the cutoff on the delete, so a thread used between the
// select and the delete survives - and keeps its transcript. Deleting the
// messages by the candidate list would leave an empty conversation behind,
// which to its owner is indistinguishable from having lost the content.
func TestWarpConversationDeleteOlderThanSparesRevivedThreads(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	cutoff := now.Add(-30 * 24 * time.Hour)

	seedWarpConversation(t, store, "u-1", "revived", "old but used again", now.Add(-60*24*time.Hour))
	// The append bumps updated_at past the cutoff, exactly as a late reply would.
	require.NoError(t, store.AppendWarpMessages(ctx, "u-1", "revived", []WarpMessage{
		{ID: "revived-u2", Role: "user", Content: "still here", CreatedAt: now},
	}))

	deleted, err := store.DeleteWarpConversationsOlderThan(ctx, cutoff)
	require.NoError(t, err)
	require.Zero(t, deleted)

	detail, err := store.GetWarpConversation(ctx, "u-1", "revived")
	require.NoError(t, err)
	require.Len(t, detail.Messages, 3, "a spared thread must keep its transcript")
}

// An append that carries an older timestamp than the row already has must not
// move the conversation backwards. Two replicas can read the same row; the one
// that commits second used to write its own precomputed timestamp, which sorts
// an active thread down a list ordered by updated_at DESC - and can put it below
// the retention cutoff while somebody is still using it.
func TestWarpAppendUpdatedAtNeverGoesBackwards(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedWarpConversation(t, store, "u-1", "c-1", "hello", now.Add(-time.Hour))

	// The later append lands first.
	require.NoError(t, store.AppendWarpMessages(ctx, "u-1", "c-1", []WarpMessage{
		{ID: "late-u", Role: "user", Content: "recent", CreatedAt: now},
	}))
	listed, err := store.ListWarpConversations(ctx, "u-1", 10)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	after := listed[0].UpdatedAt

	// Then the straggler, stamped before it.
	require.NoError(t, store.AppendWarpMessages(ctx, "u-1", "c-1", []WarpMessage{
		{ID: "early-u", Role: "user", Content: "stale", CreatedAt: now.Add(-30 * time.Minute)},
	}))
	listed, err = store.ListWarpConversations(ctx, "u-1", 10)
	require.NoError(t, err)
	require.False(t, listed[0].UpdatedAt.Before(after),
		"a late-committing older append must not roll updated_at back")

	// The message itself keeps the timestamp it was given: only updated_at is
	// clamped, because backfills and seeding need to write genuinely old turns.
	detail, err := store.GetWarpConversation(ctx, "u-1", "c-1")
	require.NoError(t, err)
	for _, message := range detail.Messages {
		if message.ID == "early-u" {
			require.WithinDuration(t, now.Add(-30*time.Minute), message.CreatedAt, time.Second)
		}
	}
}

// Two concurrent appends can persist equal (created_at, position) pairs -
// position is append-local, and both writers can read the same base. Without a
// final tie-breaker the database returns those rows in an unspecified order,
// so the same thread can render in a different order on each load. id is the
// stable third key.
func TestWarpConversationEqualTimestampsOrderById(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.CreateWarpConversation(ctx, &WarpConversation{
		ID: "c-tie", OwnerID: "u-1", Title: "tie", CreatedAt: now, UpdatedAt: now,
	}))
	// Inserted directly, id-descending, to stand in for two concurrent appends
	// that read the same base position - Append would serialize them here.
	require.NoError(t, store.db.Create(&WarpMessage{
		ID: "m-z", ConversationID: "c-tie", Role: "user", Content: "second by id", CreatedAt: now, Position: 0,
	}).Error)
	require.NoError(t, store.db.Create(&WarpMessage{
		ID: "m-a", ConversationID: "c-tie", Role: "assistant", Content: "first by id", CreatedAt: now, Position: 0,
	}).Error)

	detail, err := store.GetWarpConversation(ctx, "u-1", "c-tie")
	require.NoError(t, err)
	require.Len(t, detail.Messages, 2)
	require.Equal(t, "m-a", detail.Messages[0].ID, "equal (created_at, position) must fall back to id, not insertion order")
	require.Equal(t, "m-z", detail.Messages[1].ID)
}

// The detail read is bounded at the repository: a long-lived thread grows two
// messages per turn with no per-thread cap, so an unbounded preload made every
// GET of that conversation load and serialize an ever-larger transcript. The
// newest messages win - they are the context a reopened chat needs - and the
// true total rides along so the API can say the transcript was truncated
// rather than silently shortening it.
func TestWarpConversationDetailIsBounded(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.CreateWarpConversation(ctx, &WarpConversation{
		ID: "c-long", OwnerID: "u-1", Title: "long", CreatedAt: now, UpdatedAt: now,
	}))
	total := WarpMaxTranscriptMessages + 6
	messages := make([]WarpMessage, 0, total)
	for i := range total {
		messages = append(messages, WarpMessage{
			ID: fmt.Sprintf("m-%04d", i), Role: "user", Content: fmt.Sprintf("turn %d", i), CreatedAt: now,
		})
	}
	require.NoError(t, store.AppendWarpMessages(ctx, "u-1", "c-long", messages))

	detail, err := store.GetWarpConversation(ctx, "u-1", "c-long")
	require.NoError(t, err)
	require.Len(t, detail.Messages, WarpMaxTranscriptMessages, "the transcript must be bounded at the repository")
	require.Equal(t, total, detail.TotalMessages, "the true total must ride along so the bound is visible")
	// The newest messages survive, still in ascending render order.
	require.Equal(t, "m-0006", detail.Messages[0].ID, "the oldest messages are the ones dropped")
	require.Equal(t, fmt.Sprintf("m-%04d", total-1), detail.Messages[len(detail.Messages)-1].ID)
}

// The history list shows what each thread cost. One grouped query returns the
// totals for every listed thread, the same way message counts are fetched, so
// the list never issues a sum per row.
func TestWarpConversationUsageTotals(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"c1", "c2"} {
		require.NoError(t, store.CreateWarpConversation(ctx, &WarpConversation{ID: id, OwnerID: "u", Title: id, CreatedAt: now, UpdatedAt: now}))
	}
	require.NoError(t, store.AppendWarpMessages(ctx, "u", "c1", []WarpMessage{
		{ID: "c1-u1", Role: "user", Content: "q", CreatedAt: now},
		{ID: "c1-a1", Role: "assistant", Content: "a", TotalTokens: 100, Cost: 0.01, CreatedAt: now},
		{ID: "c1-u2", Role: "user", Content: "q2", CreatedAt: now},
		{ID: "c1-a2", Role: "assistant", Content: "a2", TotalTokens: 50, Cost: 0.005, CreatedAt: now},
	}))
	require.NoError(t, store.AppendWarpMessages(ctx, "u", "c2", []WarpMessage{
		{ID: "c2-u1", Role: "user", Content: "q", CreatedAt: now},
		{ID: "c2-a1", Role: "assistant", Content: "a", CreatedAt: now},
	}))

	totals, err := store.SumWarpMessageUsage(ctx, []string{"c1", "c2", "missing"})
	require.NoError(t, err)
	require.InDelta(t, 0.015, totals["c1"].Cost, 1e-9)
	require.Equal(t, 150, totals["c1"].TotalTokens)
	require.Zero(t, totals["c2"].Cost)
	_, listed := totals["missing"]
	require.False(t, listed)

	empty, err := store.SumWarpMessageUsage(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

// The failed-first-append cleanup must never take a thread that has messages,
// whoever wrote them, and must say whether it deleted anything.
func TestWarpConversationDeleteIfEmpty(t *testing.T) {
	store := newWarpConversationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, store.CreateWarpConversation(ctx, &WarpConversation{ID: "c-empty", OwnerID: "u", Title: "empty", CreatedAt: now, UpdatedAt: now}))
	seedWarpConversation(t, store, "u", "c-full", "full", now)

	deleted, err := store.DeleteWarpConversationIfEmpty(ctx, "other", "c-empty")
	require.ErrorIs(t, err, ErrWarpConversationNotFound, "ownership is checked like every other write")
	require.False(t, deleted)

	deleted, err = store.DeleteWarpConversationIfEmpty(ctx, "u", "c-full")
	require.NoError(t, err)
	require.False(t, deleted, "a thread with messages is kept")
	full, err := store.GetWarpConversation(ctx, "u", "c-full")
	require.NoError(t, err)
	require.Len(t, full.Messages, 2)

	deleted, err = store.DeleteWarpConversationIfEmpty(ctx, "u", "c-empty")
	require.NoError(t, err)
	require.True(t, deleted)
	_, err = store.GetWarpConversation(ctx, "u", "c-empty")
	require.ErrorIs(t, err, ErrWarpConversationNotFound)
}
