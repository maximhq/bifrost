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
	// onCreate stands in for a racing request that filed messages under the same
	// id between this request's create and its failing append.
	onCreate func(*logstore.WarpConversation)
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
	if m.onCreate != nil {
		m.onCreate(conversation)
	}
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

func (m *memoryConversations) SumWarpMessageUsage(_ context.Context, ids []string) (map[string]logstore.WarpUsageTotals, error) {
	totals := map[string]logstore.WarpUsageTotals{}
	for _, id := range ids {
		thread, ok := m.threads[id]
		if !ok {
			continue
		}
		var sum logstore.WarpUsageTotals
		for _, message := range thread.Messages {
			sum.TotalTokens += message.TotalTokens
			sum.Cost += message.Cost
		}
		totals[id] = sum
	}
	return totals, nil
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
	// A new thread that was never written back must not hand its generated id to
	// the client: the next request would send it as an existing conversation,
	// skip the create, and have nothing to append to.
	id := service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: "t-empty", IsNew: true, question: "anything?"}, ChatResponse{})
	require.Empty(t, id, "an unpersisted new thread has no id to give back")
	require.Empty(t, store.threads)

	// An existing thread keeps its id - it is still there, this turn simply had
	// nothing worth filing.
	id = service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: "t-existing", question: "anything?"}, ChatResponse{})
	require.Equal(t, "t-existing", id)
	require.Empty(t, store.threads)
}

// The first exchange creates the thread, titles it from the question, prunes
// the owner's backlog, and files both turns.
func TestWarpRecordTurnCreatesThreadOnFirstTurn(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)
	id := service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: "t-1", IsNew: true, question: "how much did we spend?"}, ChatResponse{
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
	first := service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: "t-1", IsNew: true, question: "q1"}, ChatResponse{Answer: "a1"})
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
	id := service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: "t-err", IsNew: true, question: "q"}, ChatResponse{Error: &ChatError{Code: ErrUpstream, Message: "boom"}})
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
	id := service.recordTurn(ctx, &Turn{ConversationID: "t-c", IsNew: true, question: "q"}, ChatResponse{Answer: "a"})
	require.NotEmpty(t, id)
}

func TestWarpListConversationsUsesCounts(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)
	id := service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: "t-u1", IsNew: true, question: "q"}, ChatResponse{Answer: "a"})
	service.recordTurn(ownerCtx("u2"), &Turn{ConversationID: "t-u2", IsNew: true, question: "other"}, ChatResponse{Answer: "a"})

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
	require.Equal(t, "t-x", service.recordTurn(context.Background(), &Turn{ConversationID: "t-x", IsNew: true, question: "q"}, ChatResponse{Answer: "a"}), "without history the id passes through untouched")
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

	// A store that refuses the append. An id the caller supplied but that never
	// reached storage must not come back as though the turn were filed - the
	// recovery path that re-creates a missing thread cannot help here, because
	// the write itself is what failed.
	store.failAppend = errors.New("append exploded")
	turn := &Turn{ConversationID: "c-1", question: "how much did we spend?"}
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

	saved := service.recordTurn(ownerCtx("u-1"), &Turn{ConversationID: "t-new", IsNew: true, question: "how much did we spend?"}, ChatResponse{Answer: "$412."})

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
	id := service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: "t-b", IsNew: true, question: "q"}, ChatResponse{Answer: "a"})
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

// A turn that ends in a clarifying question must still be filed.
//
// The agent emits EventQuestion and then a done frame with FinishReason
// "question" and no delta, so the folded response has an empty Answer and no
// Error. recordTurn read that as "this turn produced nothing" and returned
// without storing anything - which meant the thread was never created and the
// question was never recorded. Reopening the conversation showed the user's
// message with no sign that Warp had answered at all, and the reply the person
// then typed arrived as the opening line of an empty thread.
func TestWarpRecordTurnFilesClarifyingQuestions(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)

	id := service.recordTurn(ownerCtx("u1"), &Turn{question: "how much did we spend?"}, ChatResponse{
		FinishReason: "question",
		Question: &Question{
			Question:   "Whose traffic do you mean?",
			Options:    []QuestionOpt{{Label: "Platform team", Hint: "team:platform"}},
			AllowOther: true,
		},
	})

	require.NotEmpty(t, id, "a question is a real turn and must be filed")
	require.Len(t, store.threads, 1)

	thread := store.threads[id]
	require.Len(t, thread.Messages, 2, "the question asked and the question back")
	require.Equal(t, "user", thread.Messages[0].Role)
	require.Equal(t, "assistant", thread.Messages[1].Role)
	require.Contains(t, thread.Messages[1].Content, "Whose traffic do you mean?",
		"the stored turn must carry what Warp actually asked")

	// The options survive structurally, not as prose: a reopened thread must
	// render the same selectable card the live turn showed, with the hints
	// intact so a pick still sends "team:platform" rather than its label.
	require.NotEmpty(t, thread.Messages[1].QuestionJSON, "the structured question must be persisted")
	detail, err := service.GetConversation(ownerCtx("u1"), schemas.WarpOwnerID("u1"), id)
	require.NoError(t, err)
	question := detail.Messages[1].Question
	require.NotNil(t, question, "a reopened question turn must carry its structured question")
	require.Equal(t, "Whose traffic do you mean?", question.Question)
	require.True(t, question.AllowOther)
	require.Len(t, question.Options, 1)
	require.Equal(t, "Platform team", question.Options[0].Label)
	require.Equal(t, "team:platform", question.Options[0].Hint)
}

// A turn that produced nothing at all is still not worth a thread: the empty
// check has to narrow, not disappear.
func TestWarpRecordTurnStillSkipsTrulyEmptyTurns(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)
	require.Empty(t, service.recordTurn(ownerCtx("u1"), &Turn{question: "anything?"}, ChatResponse{}))
	require.Empty(t, store.threads)
}

// A partial answer must stay marked as partial once filed. Reopening a thread
// and seeing "$12" with no hint that Warp ran out of steps would present a
// half-checked figure as a settled one.
func TestWarpRecordTurnKeepsFinishReason(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)
	id := service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: "t-partial", IsNew: true, question: "q"}, ChatResponse{
		Answer: "About $12.", FinishReason: FinishReasonPartial,
	})
	thread := store.threads[id]
	require.Len(t, thread.Messages, 2)
	require.Equal(t, FinishReasonPartial, thread.Messages[1].FinishReason)
	require.Empty(t, thread.Messages[0].FinishReason, "a user turn has no finish reason")

	detail := conversationDetailFromRow(thread)
	require.Equal(t, FinishReasonPartial, detail.Messages[1].FinishReason)
}

// What a thread cost is filed with each answer and summed for the list, so the
// history can show spend per conversation without loading transcripts.
func TestWarpRecordTurnFilesUsageAndListsTotals(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)
	usage := func(tokens int, cost float64) *schemas.BifrostLLMUsage {
		return &schemas.BifrostLLMUsage{TotalTokens: tokens, Cost: &schemas.BifrostCost{TotalCost: cost}}
	}
	id := service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: "t-cost", IsNew: true, question: "q1"}, ChatResponse{Answer: "a1", Usage: usage(120, 0.0123)})
	service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: id, question: "q2"}, ChatResponse{Answer: "a2", Usage: usage(80, 0.0077)})

	thread := store.threads[id]
	require.Len(t, thread.Messages, 4)
	require.Equal(t, 120, thread.Messages[1].TotalTokens)
	require.InDelta(t, 0.0123, thread.Messages[1].Cost, 1e-9)
	require.Zero(t, thread.Messages[0].Cost, "user turns cost nothing")

	// A resolved owner, the same value warpOwnerFor hands the handler.
	list, err := service.ListConversations(ownerCtx("u1"), schemas.WarpOwnerID("u1"), 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.InDelta(t, 0.02, list[0].TotalCost, 1e-9)
	require.Equal(t, 200, list[0].TotalTokens)

	detail := conversationDetailFromRow(thread)
	require.InDelta(t, 0.0077, detail.Messages[3].Cost, 1e-9)
	require.Equal(t, 80, detail.Messages[3].TotalTokens)
}

// A turn that ends by asking something is still the start of a thread. The id
// has already gone to the client on the done frame, so if nothing is filed
// here every later turn arrives for a thread that does not exist and is
// dropped - which is how most chats went unrecorded, since Warp usually asks
// about the window or the scope first.
func TestWarpRecordTurnFilesQuestionTurns(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)
	id := service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: "t-q", IsNew: true, question: "what did we spend?"}, ChatResponse{
		FinishReason: "question",
		Question:     &Question{Question: "Which time range?", Options: []QuestionOpt{{Label: "Last 7 days", Hint: "-7d"}}},
	})
	thread := store.threads[id]
	require.NotNil(t, thread, "the thread must exist before the answer to the question arrives")
	require.Len(t, thread.Messages, 2)
	require.Equal(t, "Which time range?", thread.Messages[1].Content)
	require.Equal(t, "question", thread.Messages[1].FinishReason)
}

// The fold has to carry the question for the above to work: it is the only
// thing the JSON transport and the recorder see.
func TestWarpFoldCarriesQuestion(t *testing.T) {
	f := newFold()
	f.apply(Event{Type: EventQuestion, Question: &Question{Question: "Whose traffic?"}})
	f.apply(Event{Type: EventDone, FinishReason: "question"})
	result := f.result()
	require.NotNil(t, result.Question)
	require.Equal(t, "Whose traffic?", result.Question.Question)
	require.Equal(t, "question", result.FinishReason)
}

// A continuation for a thread the store has never seen is filed as a new
// thread under that id rather than dropped. The client only ever holds ids the
// server minted, and a dropped turn is a silently lost conversation.
func TestWarpRecordTurnRecreatesMissingThread(t *testing.T) {
	store := newMemoryConversations()
	service := historyService(store)
	id := service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: "t-lost", IsNew: false, question: "-7d"}, ChatResponse{Answer: "$12."})
	require.Equal(t, "t-lost", id)
	thread := store.threads["t-lost"]
	require.NotNil(t, thread)
	require.Equal(t, schemas.WarpOwnerID("u1"), thread.OwnerID, "authenticated owners are namespaced")
	require.Len(t, thread.Messages, 2)
}

// The sidebar and the opened thread must agree about what a conversation cost.
func TestConversationDetailReportsAggregateUsage(t *testing.T) {
	now := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
	detail := conversationDetailFromRow(&logstore.WarpConversation{
		ID: "conv-1", Title: "spend", CreatedAt: now, UpdatedAt: now,
		Messages: []logstore.WarpMessage{
			{ID: "m1", Role: "user", Content: "how much?", CreatedAt: now},
			{ID: "m2", Role: "assistant", Content: "a lot", TotalTokens: 120, Cost: 0.0021, CreatedAt: now},
			{ID: "m3", Role: "assistant", Content: "more", TotalTokens: 80, Cost: 0.0014, CreatedAt: now},
		},
	})
	require.Equal(t, 200, detail.TotalTokens)
	require.InDelta(t, 0.0035, detail.TotalCost, 1e-9)
	require.Equal(t, 3, detail.MessageCount)
}

// createdHere says this request created the row, not that it is the only one
// using it. A concurrent continuation can append successfully in between, and
// the cleanup then deleted that request's messages along with the thread.
func TestWarpRecordTurnCleanupSparesAConcurrentAppend(t *testing.T) {
	t.Run("deletes a thread that is still empty", func(t *testing.T) {
		store := newMemoryConversations()
		store.failAppend = errors.New("append failed")
		service := historyService(store)
		id := service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: "c-1", IsNew: true, question: "how much?"},
			ChatResponse{Answer: "a lot"})
		require.Empty(t, id)
		require.NotContains(t, store.threads, "c-1", "nothing was ever filed here")
	})

	t.Run("keeps a thread another request has already written to", func(t *testing.T) {
		store := newMemoryConversations()
		store.failAppend = errors.New("append failed")
		service := historyService(store)
		// The racing request got its messages in before this one's append failed.
		store.onCreate = func(conversation *logstore.WarpConversation) {
			conversation.Messages = []logstore.WarpMessage{{ID: "m-1", Role: "user", Content: "from the other request"}}
		}
		id := service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: "c-2", IsNew: true, question: "how much?"},
			ChatResponse{Answer: "a lot"})
		require.Empty(t, id)
		require.Contains(t, store.threads, "c-2", "deleting would take the other request's turn with it")
		require.Len(t, store.threads["c-2"].Messages, 1)
	})
}
