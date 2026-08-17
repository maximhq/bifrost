package warp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// memoryConversations is an in-memory WarpConversationStore that records the
// calls the service makes, so filing behaviour can be asserted without a
// database.
type memoryConversations struct {
	threads    map[string]*logstore.WarpConversation
	pruned     []int
	cutoffs    []time.Time
	appended   int
	failAppend error
}

func newMemoryConversations() *memoryConversations {
	return &memoryConversations{threads: map[string]*logstore.WarpConversation{}}
}

func (m *memoryConversations) ListWarpConversations(_ context.Context, ownerID string, limit int) ([]logstore.WarpConversation, error) {
	rows := []logstore.WarpConversation{}
	for _, thread := range m.threads {
		if thread.OwnerID == ownerID && len(rows) < limit {
			rows = append(rows, *thread)
		}
	}
	return rows, nil
}

func (m *memoryConversations) GetWarpConversation(_ context.Context, ownerID, id string) (*logstore.WarpConversation, error) {
	thread, ok := m.threads[id]
	if !ok || thread.OwnerID != ownerID {
		return nil, logstore.ErrWarpConversationNotFound
	}
	return thread, nil
}

func (m *memoryConversations) CreateWarpConversation(_ context.Context, conversation *logstore.WarpConversation) error {
	m.threads[conversation.ID] = conversation
	return nil
}

func (m *memoryConversations) AppendWarpMessages(_ context.Context, ownerID, conversationID string, messages []logstore.WarpMessage) error {
	thread, ok := m.threads[conversationID]
	if !ok || thread.OwnerID != ownerID {
		return logstore.ErrWarpConversationNotFound
	}
	if m.failAppend != nil {
		return m.failAppend
	}
	thread.Messages = append(thread.Messages, messages...)
	m.appended += len(messages)
	return nil
}

func (m *memoryConversations) DeleteWarpConversation(_ context.Context, ownerID, id string) error {
	if _, err := m.GetWarpConversation(context.Background(), ownerID, id); err != nil {
		return err
	}
	delete(m.threads, id)
	return nil
}

func (m *memoryConversations) PruneWarpConversations(_ context.Context, ownerID string, keep int) (int64, error) {
	m.pruned = append(m.pruned, keep)
	// Deletes like the real store - the owner's oldest threads beyond keep - so
	// a test can observe what a prune at the wrong moment actually costs.
	type row struct {
		id string
		at time.Time
	}
	rows := []row{}
	for id, thread := range m.threads {
		if thread.OwnerID == ownerID {
			rows = append(rows, row{id, thread.UpdatedAt})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].at.After(rows[j].at) })
	if keep >= len(rows) {
		return 0, nil
	}
	var deleted int64
	for _, r := range rows[keep:] {
		delete(m.threads, r.id)
		deleted++
	}
	return deleted, nil
}

func (m *memoryConversations) DeleteWarpConversationsOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	m.cutoffs = append(m.cutoffs, cutoff)
	// Capped at 1000 like the real store, which is what makes a single call
	// insufficient and the drain loop observable.
	const batch = 1000
	var deleted int64
	for id, thread := range m.threads {
		if deleted >= batch {
			break
		}
		if thread.UpdatedAt.Before(cutoff) {
			delete(m.threads, id)
			deleted++
		}
	}
	return deleted, nil
}

func (m *memoryConversations) CountWarpMessages(_ context.Context, ids []string) (map[string]int, error) {
	counts := map[string]int{}
	for _, id := range ids {
		if thread, ok := m.threads[id]; ok {
			counts[id] = len(thread.Messages)
		}
	}
	return counts, nil
}

func historyService(store *memoryConversations) *Service {
	return NewService(nil, WithConversationStore(store))
}

func ownerCtx(userID string) context.Context {
	return context.WithValue(context.Background(), schemas.BifrostContextKeyUserID, userID)
}

// A turn that produced nothing must not leave an empty thread behind.
func TestWarpRecordTurnSkipsEmptyTurns(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)
	id := service.recordTurn(ownerCtx("u1"), &Turn{question: "anything?"}, ChatResponse{})
	require.Empty(t, id)
	require.Empty(t, store.threads)
}

// The first exchange creates the thread, titles it from the question, prunes
// the owner's backlog, and files both turns.
func TestWarpRecordTurnCreatesThreadOnFirstTurn(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)
	id := service.recordTurn(ownerCtx("u1"), &Turn{question: "how much did we spend?"}, ChatResponse{
		Answer:    "$12.",
		ToolCalls: []ChatToolCall{{Name: "query_metrics", DurationMs: 3}},
	})
	require.NotEmpty(t, id)
	thread := store.threads[id]
	require.Equal(t, schemas.WarpUserOwnerPrefix+"u1", thread.OwnerID, "authenticated owners are namespaced")
	require.Equal(t, "how much did we spend?", thread.Title)
	require.Len(t, thread.Messages, 2)
	require.Equal(t, "user", thread.Messages[0].Role)
	require.Contains(t, thread.Messages[1].ToolCallsJSON, "query_metrics")
	require.Equal(t, []int{schemas.WarpMaxConversationsPerOwner}, store.pruned)
}

// A later turn appends to the named thread rather than starting another.
func TestWarpRecordTurnAppendsToExistingThread(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)
	first := service.recordTurn(ownerCtx("u1"), &Turn{question: "q1"}, ChatResponse{Answer: "a1"})
	second := service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: first, question: "q2"}, ChatResponse{Answer: "a2"})
	require.Equal(t, first, second)
	require.Len(t, store.threads, 1)
	require.Equal(t, 4, store.appended)
}

// A failed turn is still a turn someone asked; it is filed with its error so a
// reopened thread shows what happened.
func TestWarpRecordTurnFilesErrorTurns(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)
	id := service.recordTurn(ownerCtx("u1"), &Turn{question: "q"}, ChatResponse{Error: &ChatError{Code: ErrUpstream, Message: "boom"}})
	require.NotEmpty(t, id)
	require.Equal(t, "boom", store.threads[id].Messages[1].Error)
}

// Filing must survive a request whose context is already cancelled: the answer
// was produced, and the reader who left may come back for it.
func TestWarpRecordTurnSurvivesCancelledContext(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)
	ctx, cancel := context.WithCancel(ownerCtx("u1"))
	cancel()
	id := service.recordTurn(ctx, &Turn{question: "q"}, ChatResponse{Answer: "a"})
	require.NotEmpty(t, id)
}

func TestWarpListConversationsUsesCounts(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)
	id := service.recordTurn(ownerCtx("u1"), &Turn{question: "q"}, ChatResponse{Answer: "a"})
	service.recordTurn(ownerCtx("u2"), &Turn{question: "other"}, ChatResponse{Answer: "a"})

	// These take a resolved owner, the same value warpOwnerFor hands the handler
	// - not the raw user id, which is what recordTurn namespaces on the way in.
	listed, err := service.ListConversations(context.Background(), schemas.WarpOwnerID("u1"), 0)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, id, listed[0].ID)
	require.Equal(t, 2, listed[0].MessageCount)

	// Someone else's thread is indistinguishable from a missing one.
	_, err = service.GetConversation(context.Background(), schemas.WarpOwnerID("u2"), id)
	require.True(t, errors.Is(err, logstore.ErrWarpConversationNotFound))
	require.True(t, errors.Is(service.DeleteConversation(context.Background(), schemas.WarpOwnerID("u2"), id), logstore.ErrWarpConversationNotFound))

	detail, err := service.GetConversation(context.Background(), schemas.WarpOwnerID("u1"), id)
	require.NoError(t, err)
	require.Len(t, detail.Messages, 2)
}

func TestWarpHistoryWithoutStoreIsUnavailable(t *testing.T) {
	service := NewService(nil)
	require.False(t, service.HasHistory())
	_, err := service.ListConversations(context.Background(), "u1", 10)
	require.ErrorIs(t, err, ErrUnavailable)
	require.Equal(t, "", service.recordTurn(context.Background(), &Turn{question: "q"}, ChatResponse{Answer: "a"}))
}

// A title is bounded in characters, not bytes. Slicing by byte offset cuts a
// multi-byte rune in half, so a CJK or emoji question renders as mojibake in
// the history list - and the "..." was appended after the cut, putting the
// result three characters over the documented bound.
func TestWarpConversationTitleCountsRunes(t *testing.T) {
	for name, question := range map[string]string{
		"cjk":      strings.Repeat("日", 200),
		"emoji":    strings.Repeat("🚀", 200),
		"accented": strings.Repeat("é", 200),
		"mixed":    strings.Repeat("a日🚀é", 60),
	} {
		title := schemas.WarpConversationTitle(question)
		require.True(t, utf8.ValidString(title), "%s produced invalid UTF-8: %q", name, title)
		require.LessOrEqual(t, utf8.RuneCountInString(title), schemas.WarpConversationTitleChars,
			"%s exceeded the documented character bound", name)
	}

	// Short titles are returned whole, and a title of exactly the bound must not
	// be truncated even though it is longer than that in bytes.
	exact := strings.Repeat("日", schemas.WarpConversationTitleChars)
	require.Equal(t, exact, schemas.WarpConversationTitle(exact))
	require.Equal(t, "New chat", schemas.WarpConversationTitle("   "))
}

// The global owner is a sentinel for deployments with no user identity. An
// authenticated caller whose id happens to equal it would otherwise read,
// append to and delete the shared unauthenticated history.
func TestWarpOwnerIDReservesTheGlobalSentinel(t *testing.T) {
	require.Equal(t, schemas.WarpGlobalOwnerID, schemas.WarpOwnerID(""))
	require.Equal(t, schemas.WarpGlobalOwnerID, schemas.WarpOwnerID("   "))
	require.Equal(t, schemas.WarpUserOwnerPrefix+"u-1", schemas.WarpOwnerID("u-1"))

	owner := schemas.WarpOwnerID(schemas.WarpGlobalOwnerID)
	require.NotEqual(t, schemas.WarpGlobalOwnerID, owner,
		"an authenticated id equal to the sentinel must not share the unauthenticated bucket")
	require.NotEmpty(t, owner)

	// Namespacing only the sentinel just moves the collision: a user whose real
	// id is the namespaced form would land on the same owner. Every distinct
	// authenticated id must map to a distinct owner, and none of them to the
	// global bucket.
	ids := []string{"u-1", schemas.WarpGlobalOwnerID, "user:" + schemas.WarpGlobalOwnerID, "user:u-1", "__global__x"}
	seen := map[string]string{}
	for _, id := range ids {
		mapped := schemas.WarpOwnerID(id)
		require.NotEqual(t, schemas.WarpGlobalOwnerID, mapped,
			"authenticated id %q must not map to the unauthenticated bucket", id)
		if previous, clash := seen[mapped]; clash {
			t.Fatalf("ids %q and %q both map to owner %q", previous, id, mapped)
		}
		seen[mapped] = id
	}
}

// persistTurn returns "" when nothing was filed. Returning the caller's own id
// anyway hands the client a thread its owner-scoped endpoint cannot fetch, so
// the exchange looks saved and is not.
func TestWarpRecordTurnReturnsNoIDWhenPersistenceFails(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)

	// An id the caller made up: AppendWarpMessages rejects it as not theirs.
	turn := &Turn{ConversationID: "someone-elses-thread", question: "how much did we spend?"}
	saved := service.recordTurn(ownerCtx("u-1"), turn, ChatResponse{Answer: "$412."})

	require.Empty(t, saved, "an id that was never persisted must not be reported back to the client")
	require.Zero(t, store.appended)
}

// NewTurn accepts an assistant turn as the final message, but persistTurn
// hardcoded the stored role to "user" - so reopening the thread showed the
// assistant's words attributed to the person who asked.
func TestWarpRecordTurnPreservesFinalMessageRole(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)

	turn := &Turn{question: "how much did we spend?", questionRole: "assistant"}
	saved := service.recordTurn(ownerCtx("u-1"), turn, ChatResponse{Answer: "$412."})
	require.NotEmpty(t, saved)

	thread := store.threads[saved]
	require.Len(t, thread.Messages, 2)
	require.Equal(t, "assistant", thread.Messages[0].Role,
		"the stored role must match the role the request actually carried")
	require.Equal(t, "assistant", thread.Messages[1].Role)
}

// A thread created for a turn whose messages then failed to append leaves an
// empty conversation in the history list - a row that opens to nothing.
func TestWarpRecordTurnLeavesNoEmptyThreadWhenAppendFails(t *testing.T) {
	store := newMemoryConversations()
	store.failAppend = errors.New("append exploded")
	service := historyService(store)

	turn := &Turn{question: "how much did we spend?"}
	saved := service.recordTurn(ownerCtx("u-1"), turn, ChatResponse{Answer: "$412."})

	require.Empty(t, saved)
	require.Empty(t, store.threads, "a thread with no messages must not survive a failed append")
}

// The sweep expires threads on the configured retention, and on nothing else.
func TestWarpSweepHistoryUsesConfiguredRetention(t *testing.T) {
	store := newMemoryConversations()
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: &tables.TableWarpConfig{
			ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
			HistoryRetentionDays: 7,
		}}),
		WithConversationStore(store))

	now := time.Now().UTC()
	store.threads["fresh"] = &logstore.WarpConversation{
		ID: "fresh", OwnerID: "u1", UpdatedAt: now.Add(-24 * time.Hour),
	}
	store.threads["stale"] = &logstore.WarpConversation{
		ID: "stale", OwnerID: "u1", UpdatedAt: now.Add(-30 * 24 * time.Hour),
	}

	deleted, err := service.SweepHistory(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	require.Contains(t, store.threads, "fresh", "a thread inside the window must survive")
	require.NotContains(t, store.threads, "stale")

	// More than one call is expected now that the sweep drains until empty; what
	// matters is that every batch uses the same deadline, so a thread cannot be
	// spared by one batch and caught by the next as the clock moves.
	require.NotEmpty(t, store.cutoffs)
	require.WithinDuration(t, now.Add(-7*24*time.Hour), store.cutoffs[0], time.Minute,
		"the cutoff must come from history_retention_days, not from any log setting")
	for _, cutoff := range store.cutoffs {
		require.Equal(t, store.cutoffs[0], cutoff, "the deadline must not move during a sweep")
	}
}

// A deployment with no history store, or none configured, must not error - the
// cleanup loop runs on a timer and would otherwise log on every tick.
func TestWarpSweepHistoryWithoutStoreIsUnavailable(t *testing.T) {
	_, err := NewService(nil).SweepHistory(context.Background())
	require.ErrorIs(t, err, ErrUnavailable)
}

// The sweep must drain, not nibble.
//
// DeleteWarpConversationsOlderThan caps one call at 1000 rows and SweepHistory
// runs hourly, so a deployment with a large backlog kept expired conversations
// well past the retention deadline - 5000 stale threads would take five hours
// to clear, and a bigger backlog never catches up at all. Retention that only
// applies to the first thousand is not the setting the operator chose.
func TestWarpSweepHistoryDrainsEveryBatch(t *testing.T) {
	store := newMemoryConversations()
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: &tables.TableWarpConfig{
			ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
			HistoryRetentionDays: 7,
		}}),
		WithConversationStore(store))

	stale := time.Now().UTC().Add(-30 * 24 * time.Hour)
	for i := range 2500 {
		id := fmt.Sprintf("stale-%d", i)
		store.threads[id] = &logstore.WarpConversation{ID: id, OwnerID: "u1", UpdatedAt: stale}
	}
	store.threads["fresh"] = &logstore.WarpConversation{
		ID: "fresh", OwnerID: "u1", UpdatedAt: time.Now().UTC(),
	}

	deleted, err := service.SweepHistory(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 2500, deleted, "every expired thread must go in one sweep")
	require.Len(t, store.threads, 1)
	require.Contains(t, store.threads, "fresh")
	require.Greater(t, len(store.cutoffs), 1, "draining means more than one call to the store")
}

// Two authenticated subjects that differ only in whitespace are two subjects.
// Trimming before namespacing folded them onto one owner, so one caller could
// list, open and delete the other's saved conversations.
func TestWarpOwnerIDKeepsDistinctSubjectsDistinct(t *testing.T) {
	require.NotEqual(t, schemas.WarpOwnerID("u-1"), schemas.WarpOwnerID(" u-1 "))
	require.Equal(t, schemas.WarpUserOwnerPrefix+"u-1", schemas.WarpOwnerID("u-1"))

	// Blank in any form is still the unauthenticated bucket - that is the one
	// case the trim is for.
	require.Equal(t, schemas.WarpGlobalOwnerID, schemas.WarpOwnerID(""))
	require.Equal(t, schemas.WarpGlobalOwnerID, schemas.WarpOwnerID("   "))
	require.Equal(t, schemas.WarpGlobalOwnerID, schemas.WarpOwnerID("\t\n"))

	// And no authenticated id can collide with it, however it is spelled.
	require.NotEqual(t, schemas.WarpGlobalOwnerID, schemas.WarpOwnerID(schemas.WarpGlobalOwnerID))
}

// StartHistoryCleanup racing Shutdown used to be two unsynchronised accesses to
// s.stopCleanup, and worse: Shutdown could read it as nil, consume
// cleanupStopOnce and return, after which the goroutine started and nothing was
// left that could stop it.
func TestWarpHistoryCleanupStartRacesShutdownSafely(t *testing.T) {
	for range 50 {
		// Both dependencies, or StartHistoryCleanup returns at its nil guard and
		// the test exercises nothing - which is exactly what it did at first.
		service := NewService(nil,
			WithConversationStore(newMemoryConversations()),
			WithConfigStore(&recordingStore{row: &tables.TableWarpConfig{ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o"}}),
		)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); service.StartHistoryCleanup() }()
		go func() { defer wg.Done(); service.Shutdown() }()
		wg.Wait()
		// Whichever order they landed in, a service that has shut down must not be
		// left with a cleanup loop running.
		service.Shutdown()
	}
}

// Pruning must never be what a failed save costs an existing thread.
//
// The prune runs at creation time, and it used to run before the first append:
// at the cap, that deleted the owner's oldest completed conversation to make
// room for a thread whose messages then failed to land - the cleanup removes
// only the new empty thread, and nothing can restore the pruned one.
func TestWarpFailedFirstAppendAtCapPreservesExistingThreads(t *testing.T) {
	store := newMemoryConversations()
	now := time.Now().UTC()
	for i := 0; i < schemas.WarpMaxConversationsPerOwner; i++ {
		id := fmt.Sprintf("old-%d", i)
		store.threads[id] = &logstore.WarpConversation{
			// The namespaced owner, as recordTurn stores it - the raw id would
			// dodge the prune and prove nothing.
			ID: id, OwnerID: schemas.WarpUserOwnerPrefix + "u-1", UpdatedAt: now.Add(-time.Duration(i+1) * time.Minute),
		}
	}
	store.failAppend = errors.New("append exploded")
	service := historyService(store)

	saved := service.recordTurn(ownerCtx("u-1"), &Turn{question: "how much did we spend?"}, ChatResponse{Answer: "$412."})

	require.Empty(t, saved)
	require.Len(t, store.threads, schemas.WarpMaxConversationsPerOwner,
		"a failed first append must not cost a pre-existing conversation")
	for i := 0; i < schemas.WarpMaxConversationsPerOwner; i++ {
		require.Contains(t, store.threads, fmt.Sprintf("old-%d", i))
	}
}

// A transcript the repository bounded must say so. MessageCount reports the
// thread's real size and Truncated marks that older messages were cut, so the
// client renders a bounded transcript as bounded rather than complete.
func TestWarpConversationDetailReportsTruncation(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)
	id := service.recordTurn(ownerCtx("u1"), &Turn{question: "q"}, ChatResponse{Answer: "a"})
	require.NotEmpty(t, id)
	// Stand in for the repository bound: the store reports more messages than
	// it returned.
	store.threads[id].TotalMessages = 12

	detail, err := service.GetConversation(context.Background(), schemas.WarpOwnerID("u1"), id)
	require.NoError(t, err)
	require.Equal(t, 12, detail.MessageCount, "the count is the thread's real size, not the returned slice")
	require.True(t, detail.Truncated, "a bounded transcript must be marked as such")

	// An unbounded read stays unmarked, including against fakes and stores
	// that never set TotalMessages.
	store.threads[id].TotalMessages = 0
	detail, err = service.GetConversation(context.Background(), schemas.WarpOwnerID("u1"), id)
	require.NoError(t, err)
	require.Equal(t, 2, detail.MessageCount)
	require.False(t, detail.Truncated)
}
