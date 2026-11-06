package logstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Warp's saved chats live here rather than in the config store, which was where
// they started.
//
// A transcript is not configuration. It is user-generated content: it grows with
// use, it carries the same prompt and answer text the logs do, and it wants the
// retention and cleanup machinery that already exists on this side. The config
// store holds providers, keys, budgets and settings - things an operator sets
// once and an install is worthless without. Deployments size, replicate and back
// the two up differently, and chat history belongs with the large, expendable
// one.
//
// ErrWarpConversationNotFound is returned for a thread that does not exist and
// for one belonging to somebody else, deliberately without distinction. Telling
// a caller "that exists but is not yours" confirms another person's thread, and
// a thread id is the only thing an attacker would need to enumerate.
var ErrWarpConversationNotFound = errors.New("warp conversation not found")

// WarpConversation is one saved Warp thread.
//
// OwnerID is the whole access-control story: reads always filter by it, and it
// is derived server-side from the caller's identity, never accepted from the
// request. On a deployment without authentication every conversation shares the
// global owner, which makes the history common to the deployment - the same
// query, just a different owner.
type WarpConversation struct {
	ID string `gorm:"type:varchar(36);primaryKey" json:"id"`
	// Indexed together with UpdatedAt because the only list query is "this
	// owner's threads, most recent first".
	OwnerID string `gorm:"type:varchar(255);not null;index:idx_warp_conversations_owner,priority:1" json:"owner_id"`
	Title   string `gorm:"type:varchar(255);not null" json:"title"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	// Two indexes, because there are two shapes of read. The composite one leads
	// with owner_id and serves the list ("this owner's threads, newest first").
	// The retention sweep filters on updated_at alone and crosses owners, so the
	// composite cannot serve it - a leading column it does not constrain forces a
	// full scan of a table that only grows.
	UpdatedAt time.Time `gorm:"not null;index:idx_warp_conversations_owner,priority:2,sort:desc;index:idx_warp_conversations_updated_at,sort:desc" json:"updated_at"`

	// Messages is loaded only for a detail read, bounded at
	// WarpMaxTranscriptMessages. The list view reports a count instead, so
	// opening the history does not pull every transcript.
	Messages []WarpMessage `gorm:"foreignKey:ConversationID;constraint:OnDelete:CASCADE" json:"messages,omitempty"`
	// TotalMessages is the thread's real message count, which can exceed
	// len(Messages) once the transcript bound applies. Populated on a detail
	// read; never stored.
	TotalMessages int `gorm:"-" json:"-"`
}

// WarpMaxTranscriptMessages bounds one detail read. A thread grows two
// messages per turn with no per-thread cap - the conversation-count prune and
// the age sweep delete whole threads, never trim live ones - so an unbounded
// preload made every GET of a long-lived conversation load and serialize an
// ever-larger transcript. The newest messages win, because they are the
// context a reopened chat needs, and TotalMessages says the bound applied
// rather than shortening the thread silently.
const WarpMaxTranscriptMessages = 500

// TableName sets the table name for the Warp conversation model.
func (WarpConversation) TableName() string { return "warp_conversations" }

// WarpMessage is one persisted turn.
//
// Ordering is (CreatedAt, Position), and Position is the index of the message
// within the append that wrote it - not a running count across the thread.
//
// That distinction is what makes this lock-free. A dense count has to be read
// and reserved atomically, which on Postgres meant holding a row lock for the
// whole append and on ClickHouse is not expressible at all. Turns are always
// written as a pair by one caller that knows their order, so CreatedAt orders
// the appends and Position orders within one - and an answer can never be shown
// above the question it answers, which is the property that actually matters.
type WarpMessage struct {
	ID             string `gorm:"type:varchar(36);primaryKey" json:"id"`
	ConversationID string `gorm:"type:varchar(36);not null;index:idx_warp_messages_thread,priority:1" json:"conversation_id"`

	CreatedAt time.Time `gorm:"not null;index:idx_warp_messages_thread,priority:2" json:"created_at"`
	Position  int       `gorm:"not null;index:idx_warp_messages_thread,priority:3" json:"position"`

	Role    string `gorm:"type:varchar(16);not null" json:"role"`
	Content string `gorm:"type:text" json:"content"`
	// ToolCallsJSON records what Warp queried, so a reopened thread shows the
	// same provenance the live one did. Serialised rather than a child table:
	// it is only ever read and written whole, alongside its message.
	ToolCallsJSON string `gorm:"type:text" json:"-"`
	// QuestionJSON is the structured clarifying question a turn ended with,
	// serialised whole like the tool trace: a reopened thread rebuilds the same
	// selectable card the live turn showed, hints included.
	QuestionJSON string `gorm:"type:text" json:"-"`
	Error        string `gorm:"type:text" json:"error,omitempty"`
	// FinishReason records how the turn ended ("partial" when Warp ran out of
	// research steps and answered with what it had). Empty for user turns and
	// for answers that settled normally.
	FinishReason string `gorm:"type:varchar(32)" json:"finish_reason,omitempty"`
	// TotalTokens and Cost are what the answer cost to produce. Filed per
	// message so the history list can sum a thread's spend in one query.
	TotalTokens int     `gorm:"not null;default:0" json:"total_tokens,omitempty"`
	Cost        float64 `gorm:"not null;default:0" json:"cost,omitempty"`
}

// TableName sets the table name for the Warp message model.
func (WarpMessage) TableName() string { return "warp_messages" }

// WarpConversationStore is the history surface, named as a group so a reader
// can see all of it at once and so warp can depend on this rather than on the
// whole LogStore.
//
// It is embedded in LogStore rather than left optional and type-asserted for.
// An optional interface fails by disabling the feature silently: a store that
// forgot a method still compiles, still starts, and Warp simply serves no
// history, which surfaces as "my chats disappeared" long after the change that
// caused it. Embedding turns the same mistake into a build error.
type WarpConversationStore interface {
	// ListWarpConversations returns an owner's threads, most recent first.
	ListWarpConversations(ctx context.Context, ownerID string, limit int) ([]WarpConversation, error)
	// GetWarpConversation returns one thread with its messages in order, or
	// ErrWarpConversationNotFound when it does not exist for this owner.
	GetWarpConversation(ctx context.Context, ownerID, id string) (*WarpConversation, error)
	// CreateWarpConversation starts a thread.
	CreateWarpConversation(ctx context.Context, conversation *WarpConversation) error
	// AppendWarpMessages adds turns to a thread, bumping its updated time so it
	// sorts to the top of the list.
	AppendWarpMessages(ctx context.Context, ownerID, conversationID string, messages []WarpMessage) error
	// DeleteWarpConversation removes a thread and its messages.
	DeleteWarpConversation(ctx context.Context, ownerID, id string) error
	// DeleteWarpConversationIfEmpty removes a thread only if it has no messages,
	// checking and deleting as one operation, and reports whether it did. It is
	// the cleanup for a thread whose first append failed: a separate count and
	// delete let a concurrent append land in between and be deleted with it.
	DeleteWarpConversationIfEmpty(ctx context.Context, ownerID, id string) (bool, error)
	// PruneWarpConversations drops an owner's oldest threads beyond keep.
	PruneWarpConversations(ctx context.Context, ownerID string, keep int) (int64, error)
	// DeleteWarpConversationsOlderThan drops threads last touched before the
	// cutoff, across all owners, and returns how many it removed.
	//
	// One call deletes at most one bounded batch, so a caller clearing a backlog
	// must loop until a call returns zero - which is what Service.SweepHistory
	// does. The batch bound is deliberate: it keeps a single statement's lock and
	// transaction small on a table that can hold a deployment's whole chat
	// history, and it gives the caller a place to check ctx between batches.
	DeleteWarpConversationsOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	// CountWarpMessages returns message counts for the given threads in one
	// query, so a list view does not issue a count per row.
	CountWarpMessages(ctx context.Context, conversationIDs []string) (map[string]int, error)
	// SumWarpMessageUsage returns each thread's total tokens and cost in one
	// query, for the same reason as CountWarpMessages.
	SumWarpMessageUsage(ctx context.Context, conversationIDs []string) (map[string]WarpUsageTotals, error)
}

// DistributedLocker is mutual exclusion shared by every Bifrost instance that
// uses the same stores. configstore.DistributedLockManager satisfies it; it is
// declared here, not imported, because configstore already imports logstore.
type DistributedLocker interface {
	// Acquire blocks until key is held or ctx ends. It returns a context to run
	// the critical section on, cancelled if the lock is lost while held, and the
	// release.
	Acquire(ctx context.Context, key string) (held context.Context, release func(), err error)
}

// WarpUsageTotals is what a thread has cost so far.
type WarpUsageTotals struct {
	TotalTokens int
	Cost        float64
}

// ListWarpConversations returns an owner's threads, most recent first.
//
// Messages are deliberately not preloaded: the list renders a title and a count,
// and pulling every transcript to draw a sidebar would make opening the history
// cost more than the conversation it lists.
// These use s.db directly rather than scopedLogsDB. That helper composes the
// dashboard's hidden-request-types filter with the caller's log access scope,
// both of which are about log rows: it adds an object_type predicate to a table
// that has no such column, and applies a row filter built for telemetry to
// content whose access control is owner_id. Conversations scope themselves, on
// every read and write, by the owner derived from the caller's identity.
func (s *RDBLogStore) ListWarpConversations(ctx context.Context, ownerID string, limit int) ([]WarpConversation, error) {
	if limit <= 0 {
		limit = 50
	}
	var conversations []WarpConversation
	err := s.db.WithContext(ctx).
		Where("owner_id = ?", ownerID).
		Order("updated_at DESC").
		Limit(limit).
		Find(&conversations).Error
	return conversations, err
}

// CountWarpMessages returns message counts for the given threads in one query,
// so the list view does not issue a count per row.
func (s *RDBLogStore) CountWarpMessages(ctx context.Context, conversationIDs []string) (map[string]int, error) {
	counts := make(map[string]int, len(conversationIDs))
	if len(conversationIDs) == 0 {
		return counts, nil
	}
	type row struct {
		ConversationID string
		Total          int
	}
	var rows []row
	err := s.db.WithContext(ctx).
		Model(&WarpMessage{}).
		Select("conversation_id, count(*) as total").
		Where("conversation_id IN ?", conversationIDs).
		Group("conversation_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		counts[r.ConversationID] = r.Total
	}
	return counts, nil
}

// SumWarpMessageUsage returns each thread's total tokens and cost in one
// grouped query. Threads with no messages, or that do not exist, are absent
// from the result rather than reported as zero.
func (s *RDBLogStore) SumWarpMessageUsage(ctx context.Context, conversationIDs []string) (map[string]WarpUsageTotals, error) {
	totals := make(map[string]WarpUsageTotals, len(conversationIDs))
	if len(conversationIDs) == 0 {
		return totals, nil
	}
	type row struct {
		ConversationID string
		TotalTokens    int
		Cost           float64
	}
	var rows []row
	err := s.db.WithContext(ctx).
		Model(&WarpMessage{}).
		Select("conversation_id, coalesce(sum(total_tokens), 0) as total_tokens, coalesce(sum(cost), 0) as cost").
		Where("conversation_id IN ?", conversationIDs).
		Group("conversation_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		totals[r.ConversationID] = WarpUsageTotals{TotalTokens: r.TotalTokens, Cost: r.Cost}
	}
	return totals, nil
}

// GetWarpConversation returns one thread with its newest messages in order,
// bounded at WarpMaxTranscriptMessages.
func (s *RDBLogStore) GetWarpConversation(ctx context.Context, ownerID, id string) (*WarpConversation, error) {
	var conversation WarpConversation
	err := s.db.WithContext(ctx).
		// The owner predicate is part of the lookup rather than a check after it,
		// so there is no path that loads someone else's thread and then decides
		// what to do with it.
		Where("id = ? AND owner_id = ?", id, ownerID).
		First(&conversation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWarpConversationNotFound
	}
	if err != nil {
		return nil, err
	}

	var total int64
	if err := s.db.WithContext(ctx).Model(&WarpMessage{}).Where("conversation_id = ?", id).Count(&total).Error; err != nil {
		return nil, err
	}
	conversation.TotalMessages = int(total)

	// Newest first with the bound, then reversed into render order. Selecting
	// ascending with a limit would keep the oldest slice of the thread, which
	// is exactly the context a reopened chat does not need.
	//
	// id is the final tie-breaker in both directions: position is append-local,
	// so two concurrent appends can persist equal (created_at, position) pairs,
	// and without a third key the database returns them in an unspecified
	// order - the same thread could render differently on each load.
	if err := s.db.WithContext(ctx).
		Where("conversation_id = ?", id).
		Order("created_at DESC, position DESC, id DESC").
		Limit(WarpMaxTranscriptMessages).
		Find(&conversation.Messages).Error; err != nil {
		return nil, err
	}
	slices.Reverse(conversation.Messages)
	return &conversation, nil
}

// CreateWarpConversation starts a thread.
func (s *RDBLogStore) CreateWarpConversation(ctx context.Context, conversation *WarpConversation) error {
	return s.db.WithContext(ctx).Create(conversation).Error
}

// AppendWarpMessages adds turns to a thread and bumps its updated time.
//
// Both happen in one transaction. A thread whose messages landed but whose
// timestamp did not would sink down the history list despite being the most
// recent, which reads as the save having failed.
//
// The conversation row is locked FOR UPDATE where the dialect has row locks, and
// the timestamps are assigned after the lock is held rather than by the caller.
//
// Position is scoped to this append (see WarpMessage), so ordinals cannot
// collide - but that never serialized anything. Two replicas could read the same
// row and the append that committed second could write the older precomputed
// timestamp, which moves an active conversation down a list ordered by
// updated_at DESC and can put it below the retention cutoff while it is still in
// use.
//
// SQLite needs no lock: it serializes writers at the database level. ClickHouse
// has its own implementation of this method.
func (s *RDBLogStore) AppendWarpMessages(ctx context.Context, ownerID, conversationID string, messages []WarpMessage) error {
	if len(messages) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Re-assert ownership inside the transaction: the id arrived from the
		// request, and appending to a thread is a write.
		query := tx.Where("id = ? AND owner_id = ?", conversationID, ownerID)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var existing WarpConversation
		if err := query.First(&existing).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWarpConversationNotFound
			}
			return err
		}
		for i := range messages {
			messages[i].ConversationID = conversationID
			messages[i].Position = i
		}
		if err := tx.Create(&messages).Error; err != nil {
			return err
		}
		// Monotonic, and decided under the lock rather than by the caller before
		// the call. Whoever commits second sees the first's updated_at here, so an
		// append carrying an older precomputed timestamp can no longer drag an
		// active conversation down a list ordered by updated_at DESC, or below the
		// retention cutoff while it is still in use.
		//
		// max rather than time.Now(): the message timestamps are the caller's to
		// choose - seeding and backfills depend on being able to write a thread
		// that is genuinely old - and this only has to refuse to go backwards.
		stamp := messages[len(messages)-1].CreatedAt
		if existing.UpdatedAt.After(stamp) {
			stamp = existing.UpdatedAt
		}
		return tx.Model(&existing).Update("updated_at", stamp).Error
	})
}

// DeleteWarpConversation removes a thread and its messages.
func (s *RDBLogStore) DeleteWarpConversation(ctx context.Context, ownerID, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("id = ? AND owner_id = ?", id, ownerID).Delete(&WarpConversation{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrWarpConversationNotFound
		}
		// Delete the messages explicitly rather than relying on the foreign-key
		// cascade: SQLite enforces foreign keys only when the pragma is on, and a
		// silently orphaned transcript is a leak of exactly the content someone
		// asked to remove.
		return tx.Where("conversation_id = ?", id).Delete(&WarpMessage{}).Error
	})
}

// DeleteWarpConversationIfEmpty removes a thread only if it has no messages.
//
// The conversation row is locked FOR UPDATE on postgres, the same lock
// AppendWarpMessages takes before inserting, so an append either commits first
// and the count sees its messages, or waits and then finds the thread gone.
// SQLite needs no lock: it serializes writers at the database level.
func (s *RDBLogStore) DeleteWarpConversationIfEmpty(ctx context.Context, ownerID, id string) (bool, error) {
	deleted := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Where("id = ? AND owner_id = ?", id, ownerID)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var existing WarpConversation
		if err := query.First(&existing).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWarpConversationNotFound
			}
			return err
		}
		var count int64
		if err := tx.Model(&WarpMessage{}).Where("conversation_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		if err := tx.Where("id = ? AND owner_id = ?", id, ownerID).Delete(&WarpConversation{}).Error; err != nil {
			return err
		}
		deleted = true
		return nil
	})
	return deleted, err
}

// PruneWarpConversations drops an owner's oldest threads beyond keep.
//
// Warp's history is a convenience, not a record of account. Without a cap the
// table grows for the life of the deployment, and nobody scrolls back past the
// last few dozen threads anyway.
func (s *RDBLogStore) PruneWarpConversations(ctx context.Context, ownerID string, keep int) (int64, error) {
	if keep <= 0 {
		return 0, fmt.Errorf("keep must be positive")
	}
	var deleted int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Selected inside the transaction, and the delete carries the staleness
		// predicate with it. Choosing the ids in one statement and deleting them
		// in another leaves a window in which a thread receives a new message and
		// is no longer among the oldest - and it was deleted anyway, destroying a
		// conversation its owner was still using.
		var stale []WarpConversation
		if err := tx.
			Where("owner_id = ?", ownerID).
			Order("updated_at DESC").
			Offset(keep).
			Limit(1000).
			Find(&stale).Error; err != nil {
			return err
		}
		if len(stale) == 0 {
			return nil
		}
		ids := make([]string, 0, len(stale))
		newest := stale[0].UpdatedAt
		for _, conversation := range stale {
			ids = append(ids, conversation.ID)
			if conversation.UpdatedAt.After(newest) {
				newest = conversation.UpdatedAt
			}
		}
		result := tx.Where("id IN ? AND owner_id = ? AND updated_at <= ?", ids, ownerID, newest).
			Delete(&WarpConversation{})
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected
		return deleteStrandedWarpMessages(tx, ids)
	})
	return deleted, err
}

// DeleteWarpConversationsOlderThan drops threads last touched before the cutoff,
// across all owners, and returns how many it removed.
//
// At most one batch of 1,000 per call - see the interface for why - so a caller
// clearing a backlog loops until a call returns zero. Service.SweepHistory does
// exactly that, and checks ctx between batches.
//
// This is the age-based half of retention, driven by Warp's own
// history_retention_days rather than logs_store.retention_days. The two are
// deliberately separate settings: how long you keep request telemetry and how
// long you keep somebody's saved chats are different questions, and answering
// them with one number means either transcripts vanish with the logs or logs
// are kept as long as transcripts.
func (s *RDBLogStore) DeleteWarpConversationsOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	var deleted int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stale []WarpConversation
		if err := tx.Where("updated_at < ?", cutoff).Limit(1000).Find(&stale).Error; err != nil {
			return err
		}
		if len(stale) == 0 {
			return nil
		}
		ids := make([]string, 0, len(stale))
		for _, conversation := range stale {
			ids = append(ids, conversation.ID)
		}
		// The cutoff is repeated on the delete so a thread that was used between
		// the select and here survives, the same way Prune re-asserts staleness.
		result := tx.Where("id IN ? AND updated_at < ?", ids, cutoff).Delete(&WarpConversation{})
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected
		return deleteStrandedWarpMessages(tx, ids)
	})
	return deleted, err
}

// warpThreadsRemoved returns the ids that are no longer among survivors.
func warpThreadsRemoved(ids, survivors []string) []string {
	spared := make(map[string]struct{}, len(survivors))
	for _, id := range survivors {
		spared[id] = struct{}{}
	}
	removed := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := spared[id]; !ok {
			removed = append(removed, id)
		}
	}
	return removed
}

// deleteStrandedWarpMessages removes the transcripts of threads that a bulk
// delete has just taken away.
//
// Both bulk deletes re-assert their staleness predicate at the moment of the
// delete, so a thread that received a message between the select and there
// survives the sweep. Its transcript has to survive with it: deleting messages
// by the candidate list would leave that owner an empty conversation where
// their history was, which looks exactly like data loss because it is. So the
// threads go first and the messages follow only for the ids that actually went.
func deleteStrandedWarpMessages(tx *gorm.DB, ids []string) error {
	var survivors []string
	if err := tx.Model(&WarpConversation{}).Where("id IN ?", ids).Pluck("id", &survivors).Error; err != nil {
		return err
	}
	removed := warpThreadsRemoved(ids, survivors)
	if len(removed) == 0 {
		return nil
	}
	return tx.Where("conversation_id IN ?", removed).Delete(&WarpMessage{}).Error
}
