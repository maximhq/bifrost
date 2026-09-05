package warp

import (
	"context"
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// memoryConversations is an in-memory WarpConversationStore that records the
// calls the service makes, so filing behaviour can be asserted without a
// database.
type memoryConversations struct {
	threads  map[string]*tables.TableWarpConversation
	pruned   []int
	appended int
}

func newMemoryConversations() *memoryConversations {
	return &memoryConversations{threads: map[string]*tables.TableWarpConversation{}}
}

func (m *memoryConversations) ListWarpConversations(_ context.Context, ownerID string, limit int) ([]tables.TableWarpConversation, error) {
	rows := []tables.TableWarpConversation{}
	for _, thread := range m.threads {
		if thread.OwnerID == ownerID && len(rows) < limit {
			rows = append(rows, *thread)
		}
	}
	return rows, nil
}

func (m *memoryConversations) GetWarpConversation(_ context.Context, ownerID, id string) (*tables.TableWarpConversation, error) {
	thread, ok := m.threads[id]
	if !ok || thread.OwnerID != ownerID {
		return nil, configstore.ErrWarpConversationNotFound
	}
	return thread, nil
}

func (m *memoryConversations) CreateWarpConversation(_ context.Context, conversation *tables.TableWarpConversation) error {
	m.threads[conversation.ID] = conversation
	return nil
}

func (m *memoryConversations) AppendWarpMessages(_ context.Context, ownerID, conversationID string, messages []tables.TableWarpMessage) error {
	thread, ok := m.threads[conversationID]
	if !ok || thread.OwnerID != ownerID {
		return configstore.ErrWarpConversationNotFound
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

func (m *memoryConversations) PruneWarpConversations(_ context.Context, _ string, keep int) (int64, error) {
	m.pruned = append(m.pruned, keep)
	return 0, nil
}

func (m *memoryConversations) SumWarpMessageUsage(_ context.Context, ids []string) (map[string]configstore.WarpUsageTotals, error) {
	totals := map[string]configstore.WarpUsageTotals{}
	for _, id := range ids {
		thread, ok := m.threads[id]
		if !ok {
			continue
		}
		var sum configstore.WarpUsageTotals
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
	id := service.recordTurn(ownerCtx("u1"), &Turn{ConversationID: "t-empty", IsNew: true, question: "anything?"}, ChatResponse{})
	require.Equal(t, "t-empty", id, "the id is echoed so the client keeps its thread, but nothing is filed")
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
	require.Equal(t, "u1", thread.OwnerID)
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

	listed, err := service.ListConversations(context.Background(), "u1", 0)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, id, listed[0].ID)
	require.Equal(t, 2, listed[0].MessageCount)

	// Someone else's thread is indistinguishable from a missing one.
	_, err = service.GetConversation(context.Background(), "u2", id)
	require.True(t, errors.Is(err, configstore.ErrWarpConversationNotFound))
	require.True(t, errors.Is(service.DeleteConversation(context.Background(), "u2", id), configstore.ErrWarpConversationNotFound))

	detail, err := service.GetConversation(context.Background(), "u1", id)
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

	list, err := service.ListConversations(ownerCtx("u1"), "u1", 10)
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
	require.Equal(t, "u1", thread.OwnerID)
	require.Len(t, thread.Messages, 2)
}
