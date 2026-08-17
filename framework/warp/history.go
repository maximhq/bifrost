package warp

import (
	"context"
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

	conversations := make([]schemas.WarpConversation, 0, len(rows))
	for _, row := range rows {
		conversations = append(conversations, schemas.WarpConversation{
			ID:           row.ID,
			Title:        row.Title,
			MessageCount: counts[row.ID],
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
			Role:      message.Role,
			Content:   message.Content,
			Error:     message.Error,
			CreatedAt: message.CreatedAt,
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
	if response.Answer == "" && response.Error == nil {
		return turn.ConversationID
	}

	stored := schemas.WarpStoredMessage{Role: "assistant", Content: response.Answer}
	if response.Error != nil {
		stored.Error = response.Error.Message
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
	if isNew {
		if err := s.conversations.CreateWarpConversation(ctx, &tables.TableWarpConversation{
			ID:        conversationID,
			OwnerID:   owner,
			Title:     schemas.WarpConversationTitle(question),
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			s.warnf("failed to start warp conversation: %v", err)
			return ""
		}
		// Prune on creation rather than on a timer: it is the only moment the
		// count can grow, and it keeps the cap enforced without a background job.
		if _, err := s.conversations.PruneWarpConversations(ctx, owner, schemas.WarpMaxConversationsPerOwner); err != nil {
			s.warnf("failed to prune warp conversations: %v", err)
		}
	}

	toolCallsJSON := ""
	if len(answer.ToolCalls) > 0 {
		if encoded, err := sonic.MarshalString(answer.ToolCalls); err == nil {
			toolCallsJSON = encoded
		}
	}

	if err := s.conversations.AppendWarpMessages(ctx, owner, conversationID, []tables.TableWarpMessage{
		{ID: uuid.NewString(), Role: "user", Content: question, CreatedAt: now},
		{
			ID: uuid.NewString(), Role: "assistant", Content: answer.Content,
			ToolCallsJSON: toolCallsJSON, Error: answer.Error, CreatedAt: now,
		},
	}); err != nil {
		s.warnf("failed to append warp messages: %v", err)
		return ""
	}
	return conversationID
}
