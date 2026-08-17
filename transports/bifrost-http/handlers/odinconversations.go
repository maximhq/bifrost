package handlers

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/valyala/fasthttp"
)

// odinOwnerFor resolves the caller's history owner.
//
// It is read from the request context, never from the request body. An owner id
// a caller can supply is not an access control, and history is the one part of
// Odin that holds what people actually asked.
func odinOwnerFor(ctx *fasthttp.RequestCtx) string {
	userID, _ := ctx.UserValue(schemas.BifrostContextKeyUserID).(string)
	return schemas.OdinOwnerID(userID)
}

// odinOwnerFromContext is the same resolution for a snapshotted context, used by
// the chat path after the RequestCtx is gone.
func odinOwnerFromContext(ctx context.Context) string {
	userID, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
	return schemas.OdinOwnerID(userID)
}

// listConversations returns the caller's threads, most recent first.
func (s *OdinService) listConversations(ctx *fasthttp.RequestCtx) {
	if s.conversations == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, ErrOdinUnavailable.Error())
		return
	}
	limit := 50
	if raw := string(ctx.QueryArgs().Peek("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			SendError(ctx, fasthttp.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(parsed, 100)
	}

	owner := odinOwnerFor(ctx)
	rows, err := s.conversations.ListOdinConversations(ctx, owner, limit)
	if err != nil {
		logger.Warn("failed to list odin conversations: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to list conversations")
		return
	}

	// Counts come from one grouped query rather than a count per row, so opening
	// the history costs a constant two queries however long it is.
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	counts := map[string]int{}
	if counter, ok := s.conversations.(interface {
		CountOdinMessages(context.Context, []string) (map[string]int, error)
	}); ok {
		if resolved, err := counter.CountOdinMessages(ctx, ids); err == nil {
			counts = resolved
		}
	}

	conversations := make([]schemas.OdinConversation, 0, len(rows))
	for _, row := range rows {
		conversations = append(conversations, schemas.OdinConversation{
			ID:           row.ID,
			Title:        row.Title,
			MessageCount: counts[row.ID],
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		})
	}
	SendJSON(ctx, map[string]any{"conversations": conversations})
}

// getConversation returns one thread with its transcript.
func (s *OdinService) getConversation(ctx *fasthttp.RequestCtx) {
	if s.conversations == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, ErrOdinUnavailable.Error())
		return
	}
	id, _ := ctx.UserValue("id").(string)
	if id == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "conversation id is required")
		return
	}

	row, err := s.conversations.GetOdinConversation(ctx, odinOwnerFor(ctx), id)
	if errors.Is(err, configstore.ErrOdinConversationNotFound) {
		// 404 for someone else's thread as well as a missing one. Distinguishing
		// them would confirm that another person's conversation exists.
		SendError(ctx, fasthttp.StatusNotFound, "Conversation not found")
		return
	}
	if err != nil {
		logger.Warn("failed to read odin conversation: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to read conversation")
		return
	}
	SendJSON(ctx, odinConversationDetail(row))
}

// deleteConversation removes a thread.
func (s *OdinService) deleteConversation(ctx *fasthttp.RequestCtx) {
	if s.conversations == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, ErrOdinUnavailable.Error())
		return
	}
	id, _ := ctx.UserValue("id").(string)
	if id == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "conversation id is required")
		return
	}

	err := s.conversations.DeleteOdinConversation(ctx, odinOwnerFor(ctx), id)
	if errors.Is(err, configstore.ErrOdinConversationNotFound) {
		SendError(ctx, fasthttp.StatusNotFound, "Conversation not found")
		return
	}
	if err != nil {
		logger.Warn("failed to delete odin conversation: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete conversation")
		return
	}
	ctx.SetStatusCode(fasthttp.StatusNoContent)
}

// odinConversationDetail renders a stored thread for the API.
func odinConversationDetail(row *tables.TableOdinConversation) schemas.OdinConversationDetail {
	messages := make([]schemas.OdinStoredMessage, 0, len(row.Messages))
	for _, message := range row.Messages {
		stored := schemas.OdinStoredMessage{
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
	return schemas.OdinConversationDetail{
		OdinConversation: schemas.OdinConversation{
			ID:           row.ID,
			Title:        row.Title,
			MessageCount: len(row.Messages),
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		},
		Messages: messages,
	}
}

// persistOdinTurn saves one exchange, creating the thread on the first turn.
//
// It returns the conversation id so the client can keep appending, and never
// returns an error to the caller: history is a convenience, and failing a
// perfectly good answer because it could not be filed would trade the thing
// someone asked for against the thing they did not.
func (s *OdinService) persistOdinTurn(ctx context.Context, conversationID, question string, answer schemas.OdinStoredMessage) string {
	if s.conversations == nil {
		return ""
	}
	owner := odinOwnerFromContext(ctx)
	now := time.Now().UTC()

	if conversationID == "" {
		conversationID = uuid.NewString()
		if err := s.conversations.CreateOdinConversation(ctx, &tables.TableOdinConversation{
			ID:        conversationID,
			OwnerID:   owner,
			Title:     schemas.OdinConversationTitle(question),
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			logger.Warn("failed to start odin conversation: %v", err)
			return ""
		}
		// Prune on creation rather than on a timer: it is the only moment the
		// count can grow, and it keeps the cap enforced without a background job.
		if _, err := s.conversations.PruneOdinConversations(ctx, owner, schemas.OdinMaxConversationsPerOwner); err != nil {
			logger.Warn("failed to prune odin conversations: %v", err)
		}
	}

	toolCallsJSON := ""
	if len(answer.ToolCalls) > 0 {
		if encoded, err := sonic.MarshalString(answer.ToolCalls); err == nil {
			toolCallsJSON = encoded
		}
	}

	if err := s.conversations.AppendOdinMessages(ctx, owner, conversationID, []tables.TableOdinMessage{
		{ID: uuid.NewString(), Role: "user", Content: question, CreatedAt: now},
		{
			ID: uuid.NewString(), Role: "assistant", Content: answer.Content,
			ToolCallsJSON: toolCallsJSON, Error: answer.Error, CreatedAt: now,
		},
	}); err != nil {
		logger.Warn("failed to append odin messages: %v", err)
		return ""
	}
	return conversationID
}

// recordOdinTurn files a completed exchange and returns the thread id.
//
// It is the single bridge between the chat transports and history, so both the
// streaming and buffered paths file identically. A turn with no answer and no
// error is not recorded: an aborted request that produced nothing would
// otherwise leave an empty thread in the list.
func (s *OdinService) recordOdinTurn(ctx context.Context, conversationID, question string, turn odinChatResponse) string {
	if s.conversations == nil || question == "" {
		return conversationID
	}
	if turn.Answer == "" && turn.Error == nil {
		return conversationID
	}

	stored := schemas.OdinStoredMessage{Role: "assistant", Content: turn.Answer}
	if turn.Error != nil {
		stored.Error = turn.Error.Message
	}
	for _, call := range turn.ToolCalls {
		stored.ToolCalls = append(stored.ToolCalls, schemas.OdinStoredToolCall{
			Name: call.Name, DurationMs: call.DurationMs, Failed: call.Failed,
		})
	}

	// A cancelled request still has an answer worth filing, and its context is
	// already done - so persistence gets its own short-lived context rather than
	// inheriting one that would refuse the write.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if saved := s.persistOdinTurn(writeCtx, conversationID, question, stored); saved != "" {
		return saved
	}
	return conversationID
}
