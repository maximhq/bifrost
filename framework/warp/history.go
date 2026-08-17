package warp

import (
	"context"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

const (
	// historySweepInterval is how often expired chats are swept.
	//
	// Hourly, because the setting is measured in days: a sweep that runs sooner
	// deletes nothing new, and one that runs much later makes "kept for 7 days"
	// mean something closer to 8. It is a single ranged delete against a table
	// bounded by WarpMaxConversationsPerOwner per owner, so the cost of running
	// it on an idle deployment is one query an hour.
	historySweepInterval = time.Hour

	// historySweepTimeout bounds one sweep, so a wedged store cannot pin the
	// loop and silently stop retention for the life of the process.
	historySweepTimeout = 2 * time.Minute
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
// logstore.ErrWarpConversationNotFound; distinguishing them would confirm
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

// SweepHistory expires every saved chat last touched before the configured
// retention window, across all owners.
//
// This is the age half of retention; PruneWarpConversations is the count half,
// and they exist for different reasons. The per-owner cap stops the table
// growing without bound, which is an operational concern. This one answers "how
// long do we keep what people typed", which is a policy one - and a deployment
// where nobody has chatted for months is exactly where the cap never fires and
// the old transcripts sit there anyway.
//
// The cutoff comes from Warp's own history_retention_days. It deliberately does
// not read logs_store.retention_days; see schemas.WarpDefaultHistoryRetentionDays.
func (s *Service) SweepHistory(ctx context.Context) (int64, error) {
	if s.conversations == nil || s.store == nil {
		return 0, ErrUnavailable
	}
	row, err := s.store.GetWarpConfig(ctx)
	if err != nil {
		return 0, err
	}
	// Deliberately not gated on IsConfigured or Enabled. Turning Warp off stops
	// new chats; it does not mean the transcripts already saved should be kept
	// forever, which is what skipping the sweep on a disabled deployment would
	// quietly do.
	config := configFromRow(row)
	cutoff := time.Now().UTC().AddDate(0, 0, -config.EffectiveHistoryRetentionDays())

	// Drained, not nibbled. The store caps one delete at 1000 rows and this runs
	// hourly, so a backlog larger than that outlived the retention window by
	// hours - and a backlog that grows faster than 1000/hour never clears at
	// all. Retention that applies only to the first thousand rows is not the
	// setting the operator chose.
	//
	// The cutoff is computed once, so a thread used during the sweep is spared
	// by the store's own staleness predicate rather than by a moving deadline.
	var total int64
	for {
		deleted, err := s.conversations.DeleteWarpConversationsOlderThan(ctx, cutoff)
		total += deleted
		if err != nil {
			return total, err
		}
		if deleted == 0 {
			return total, nil
		}
		// The caller's context still bounds the whole sweep, so a wedged store
		// cannot spin here.
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

// StartHistoryCleanup runs SweepHistory on a timer until Shutdown.
//
// On a timer rather than on write, because the thing being expired is age: a
// deployment nobody has used for a month is precisely where a write-triggered
// sweep never runs and the retention setting turns out to have been decorative.
// Calling it twice is a no-op, so a service rebuilt at route registration does
// not end up with two loops.
func (s *Service) StartHistoryCleanup() {
	if s.conversations == nil || s.store == nil {
		return
	}
	s.cleanupOnce.Do(func() {
		// Under the same lock Shutdown takes, for two reasons. The write to
		// s.stopCleanup and Shutdown's read of it were unsynchronised, which is a
		// data race; and Shutdown could read a nil stopCleanup, consume
		// cleanupStopOnce, and return - after which this goroutine started and ran
		// for the life of the process with nothing left able to stop it.
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		stop := make(chan struct{})
		s.stopCleanup = stop
		s.mu.Unlock()
		go func() {
			ticker := time.NewTicker(historySweepInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					// Its own context: the sweep outlives any request, and a
					// bounded one keeps a wedged store from pinning the loop.
					ctx, cancel := context.WithTimeout(context.Background(), historySweepTimeout)
					deleted, err := s.SweepHistory(ctx)
					cancel()
					if err != nil {
						s.warnf("failed to sweep warp history: %v", err)
					} else if deleted > 0 {
						s.infof("expired %d warp conversation(s) past the retention window", deleted)
					}
				case <-stop:
					return
				}
			}
		}()
	})
}

// stopHistoryCleanup ends the sweep loop. Safe to call without a matching
// start, and safe to call twice.
func (s *Service) stopHistoryCleanup() {
	s.cleanupStopOnce.Do(func() {
		if s.stopCleanup != nil {
			close(s.stopCleanup)
		}
	})
}

// conversationDetailFromRow renders a stored thread for the API.
func conversationDetailFromRow(row *logstore.WarpConversation) schemas.WarpConversationDetail {
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
	// The real thread size when the store reports one, so a bounded transcript
	// says how much it is standing in for. Fakes and stores that never set
	// TotalMessages fall back to the returned slice, which also keeps the
	// count honest if a store under-reports.
	total := row.TotalMessages
	if total < len(row.Messages) {
		total = len(row.Messages)
	}
	return schemas.WarpConversationDetail{
		WarpConversation: schemas.WarpConversation{
			ID:           row.ID,
			Title:        row.Title,
			MessageCount: total,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		},
		Messages:  messages,
		Truncated: total > len(row.Messages),
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
	// Only ever report an id that was actually filed. Falling back to the
	// caller's own id handed the client a thread its owner-scoped endpoint
	// cannot fetch - the exchange looks saved, and is not.
	return s.persistTurn(writeCtx, turn.ConversationID, turn.questionRole, turn.question, stored)
}

// persistTurn saves one exchange, creating the thread on the first turn.
//
// It returns the conversation id so the client can keep appending, and never
// returns an error to the caller: history is a convenience, and failing a
// perfectly good answer because it could not be filed would trade the thing
// someone asked for against the thing they did not.
func (s *Service) persistTurn(ctx context.Context, conversationID, questionRole, question string, answer schemas.WarpStoredMessage) string {
	if s.conversations == nil {
		return ""
	}
	owner := OwnerFromContext(ctx)
	now := time.Now().UTC()
	// NewTurn accepts an assistant turn as the final message, so the stored role
	// has to be the one the request carried. Hardcoding "user" attributed the
	// assistant's words to the person who asked when the thread was reopened.
	if questionRole == "" {
		questionRole = "user"
	}

	// Tracks a thread this call brought into existence, so a failed append does
	// not strand an empty conversation in the history list - a row that opens to
	// nothing and can only be cleared by hand.
	createdHere := false
	if conversationID == "" {
		conversationID = uuid.NewString()
		if err := s.conversations.CreateWarpConversation(ctx, &logstore.WarpConversation{
			ID:        conversationID,
			OwnerID:   owner,
			Title:     schemas.WarpConversationTitle(question),
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			s.warnf("failed to start warp conversation: %v", err)
			return ""
		}
		createdHere = true
	}

	toolCallsJSON := ""
	if len(answer.ToolCalls) > 0 {
		if encoded, err := sonic.MarshalString(answer.ToolCalls); err == nil {
			toolCallsJSON = encoded
		}
	}

	if err := s.conversations.AppendWarpMessages(ctx, owner, conversationID, []logstore.WarpMessage{
		{ID: uuid.NewString(), Role: questionRole, Content: question, CreatedAt: now},
		{
			ID: uuid.NewString(), Role: "assistant", Content: answer.Content,
			ToolCallsJSON: toolCallsJSON, Error: answer.Error, CreatedAt: now,
		},
	}); err != nil {
		s.warnf("failed to append warp messages: %v", err)
		if createdHere {
			if cleanupErr := s.conversations.DeleteWarpConversation(ctx, owner, conversationID); cleanupErr != nil {
				s.warnf("failed to remove empty warp conversation %s: %v", conversationID, cleanupErr)
			}
		}
		return ""
	}
	if createdHere {
		// Prune on creation rather than on a timer: it is the only moment the
		// count can grow, and it keeps the cap enforced without a background
		// job. After the first append, not before it: at the cap, pruning first
		// deleted the owner's oldest completed conversation to make room for a
		// thread whose messages might then fail to land - and the failure
		// cleanup can only remove the new empty thread, never restore the
		// pruned one. Pruning after, the new thread is the newest and complete,
		// so the prune can only ever take genuinely surplus old threads.
		if _, err := s.conversations.PruneWarpConversations(ctx, owner, schemas.WarpMaxConversationsPerOwner); err != nil {
			s.warnf("failed to prune warp conversations: %v", err)
		}
	}
	return conversationID
}
