package warp

import (
	"context"
	"errors"
	"github.com/maximhq/bifrost/framework/configstore"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// OwnerFromContext resolves the caller's history owner.
//
// It is read from the context, never from a request body. An owner id a caller
// can supply is not an access control, and history is the one part of Warp
// that holds what people actually asked. Transports put the user id on the
// context; a deployment with no identity resolves to the shared owner.
func OwnerFromContext(ctx context.Context) string {
	userID, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
	return schemas.WarpOwnerID(userID)
}

// ListConversations returns an owner's threads, most recent first. limit is
// clamped to [1, 100].
func (s *Service) ListConversations(ctx context.Context, ownerID string, limit int) ([]schemas.WarpConversation, error) {
	if s.conversations == nil {
		return nil, ErrUnavailable
	}
	limit = min(max(limit, 1), 100)
	rows, err := s.conversations.ListWarpConversations(ctx, ownerID, limit)
	if err != nil {
		return nil, err
	}

	// Counts come from one grouped query rather than a count per row, so opening
	// the history costs a constant two queries however long it is. A failed count
	// degrades to zeros rather than failing the list.
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	counts, err := s.conversations.CountWarpMessages(ctx, ids)
	if err != nil {
		s.warnf("failed to count warp messages: %v", err)
		counts = map[string]int{}
	}
	totals, err := s.conversations.SumWarpMessageUsage(ctx, ids)
	if err != nil {
		s.warnf("failed to sum warp message usage: %v", err)
		totals = map[string]configstore.WarpUsageTotals{}
	}

	conversations := make([]schemas.WarpConversation, 0, len(rows))
	for _, row := range rows {
		conversations = append(conversations, schemas.WarpConversation{
			ID:           row.ID,
			Title:        row.Title,
			MessageCount: counts[row.ID],
			TotalTokens:  totals[row.ID].TotalTokens,
			TotalCost:    totals[row.ID].Cost,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		})
	}
	return conversations, nil
}

// GetConversation returns one thread with its transcript. A thread that does
// not exist and a thread that belongs to someone else both return
// configstore.ErrWarpConversationNotFound; distinguishing them would confirm
// that another person's conversation exists.
func (s *Service) GetConversation(ctx context.Context, ownerID, id string) (*schemas.WarpConversationDetail, error) {
	if s.conversations == nil {
		return nil, ErrUnavailable
	}
	row, err := s.conversations.GetWarpConversation(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	detail := conversationDetailFromRow(row)
	return &detail, nil
}

// DeleteConversation removes a thread.
func (s *Service) DeleteConversation(ctx context.Context, ownerID, id string) error {
	if s.conversations == nil {
		return ErrUnavailable
	}
	return s.conversations.DeleteWarpConversation(ctx, ownerID, id)
}

// conversationDetailFromRow renders a stored thread for the API.
func conversationDetailFromRow(row *tables.TableWarpConversation) schemas.WarpConversationDetail {
	messages := make([]schemas.WarpStoredMessage, 0, len(row.Messages))
	for _, message := range row.Messages {
		stored := schemas.WarpStoredMessage{
			Role:         message.Role,
			Content:      message.Content,
			Error:        message.Error,
			FinishReason: message.FinishReason,
			TotalTokens:  message.TotalTokens,
			Cost:         message.Cost,
			CreatedAt:    message.CreatedAt,
		}
		if message.ToolCallsJSON != "" {
			// A transcript is still worth showing without its tool trace, so a
			// decode failure drops the trace rather than the message.
			_ = sonic.UnmarshalString(message.ToolCallsJSON, &stored.ToolCalls)
		}
		messages = append(messages, stored)
	}
	return schemas.WarpConversationDetail{
		WarpConversation: schemas.WarpConversation{
			ID:           row.ID,
			Title:        row.Title,
			MessageCount: len(row.Messages),
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		},
		Messages: messages,
	}
}

// recordTurn files a completed exchange and returns the thread id.
//
// It is the single bridge between the chat loop and history, so the streaming
// and buffered transports file identically. A turn with no answer and no error
// is not recorded: an aborted request that produced nothing would otherwise
// leave an empty thread in the list.
func (s *Service) recordTurn(ctx context.Context, turn *Turn, response ChatResponse) string {
	if s.conversations == nil || turn.question == "" {
		return turn.ConversationID
	}
	if response.Answer == "" && response.Error == nil && response.Question == nil {
		return turn.ConversationID
	}

	stored := schemas.WarpStoredMessage{Role: "assistant", Content: response.Answer}
	if response.Error != nil {
		stored.Error = response.Error.Message
	}
	// A turn that ended by asking is filed with the question as its content.
	// The thread id has already gone to the client on the done frame, so the
	// thread must exist now or every later turn will arrive for a thread that
	// was never created. Warp usually asks about the window or the scope
	// first, which made this the common case rather than the edge.
	if response.Answer == "" && response.Question != nil {
		stored.Content = response.Question.Question
		stored.FinishReason = response.FinishReason
	}
	// Only the partial marker is filed. A normal stop is the default reading of
	// any stored answer, and writing it on every row would say nothing.
	if response.FinishReason == FinishReasonPartial {
		stored.FinishReason = FinishReasonPartial
	}
	if response.Usage != nil {
		stored.TotalTokens = response.Usage.TotalTokens
		if response.Usage.Cost != nil {
			stored.Cost = response.Usage.Cost.TotalCost
		}
	}
	for _, call := range response.ToolCalls {
		stored.ToolCalls = append(stored.ToolCalls, schemas.WarpStoredToolCall{
			Name: call.Name, DurationMs: call.DurationMs, Failed: call.Failed,
		})
	}

	// A cancelled request still has an answer worth filing, and its context is
	// already done - so persistence gets its own short-lived context rather than
	// inheriting one that would refuse the write.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if saved := s.persistTurn(writeCtx, turn.ConversationID, turn.IsNew, turn.question, stored); saved != "" {
		return saved
	}
	return turn.ConversationID
}

// persistTurn saves one exchange, creating the thread on the first turn.
//
// It returns the conversation id so the client can keep appending, and never
// returns an error to the caller: history is a convenience, and failing a
// perfectly good answer because it could not be filed would trade the thing
// someone asked for against the thing they did not.
//
// isNew, rather than an empty id, decides whether the thread row gets created.
// The id is minted before the first model call so it can ride upstream as a
// logging header, which means it is never empty by the time it reaches here -
// and inferring "new" from emptiness would silently stop creating threads
// altogether, leaving every message orphaned.
func (s *Service) persistTurn(ctx context.Context, conversationID string, isNew bool, question string, answer schemas.WarpStoredMessage) string {
	if s.conversations == nil {
		return ""
	}
	owner := OwnerFromContext(ctx)
	now := time.Now().UTC()

	if conversationID == "" {
		conversationID = uuid.NewString()
		isNew = true
	}
	create := func() bool {
		if err := s.conversations.CreateWarpConversation(ctx, &tables.TableWarpConversation{
			ID:        conversationID,
			OwnerID:   owner,
			Title:     schemas.WarpConversationTitle(question),
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			s.warnf("failed to start warp conversation: %v", err)
			return false
		}
		// Prune on creation rather than on a timer: it is the only moment the
		// count can grow, and it keeps the cap enforced without a background job.
		if _, err := s.conversations.PruneWarpConversations(ctx, owner, schemas.WarpMaxConversationsPerOwner); err != nil {
			s.warnf("failed to prune warp conversations: %v", err)
		}
		return true
	}
	if isNew && !create() {
		return ""
	}

	toolCallsJSON := ""
	if len(answer.ToolCalls) > 0 {
		if encoded, err := sonic.MarshalString(answer.ToolCalls); err == nil {
			toolCallsJSON = encoded
		}
	}

	messages := []tables.TableWarpMessage{
		{ID: uuid.NewString(), Role: "user", Content: question, CreatedAt: now},
		{
			ID: uuid.NewString(), Role: "assistant", Content: answer.Content,
			ToolCallsJSON: toolCallsJSON, Error: answer.Error, FinishReason: answer.FinishReason,
			TotalTokens: answer.TotalTokens, Cost: answer.Cost, CreatedAt: now,
		},
	}
	err := s.conversations.AppendWarpMessages(ctx, owner, conversationID, messages)
	if errors.Is(err, configstore.ErrWarpConversationNotFound) && !isNew {
		// The client holds an id the server minted but never filed a thread
		// for - a first turn that was lost, or a thread pruned since. Starting
		// the thread now under that id keeps the conversation rather than
		// dropping every turn from here on.
		if !create() {
			return ""
		}
		err = s.conversations.AppendWarpMessages(ctx, owner, conversationID, messages)
	}
	if err != nil {
		s.warnf("failed to append warp messages: %v", err)
		return ""
	}
	return conversationID
}
