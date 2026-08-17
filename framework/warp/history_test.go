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
