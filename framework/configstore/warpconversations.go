package configstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// ErrWarpConversationNotFound is returned for a thread that does not exist, or
// that belongs to someone else.
//
// The two cases are deliberately indistinguishable. Telling a caller "that
// exists but is not yours" confirms the existence of another person's thread,
// and thread ids are the only thing an attacker would need to enumerate.
var ErrWarpConversationNotFound = errors.New("warp conversation not found")

// WarpConversationStore is a narrow optional interface, following the pattern
// of NotificationStore and WarpStore: adding history must not force every
// ConfigStore test double in the tree to grow methods.
type WarpConversationStore interface {
	// ListWarpConversations returns an owner's threads, most recent first.
	ListWarpConversations(ctx context.Context, ownerID string, limit int) ([]tables.TableWarpConversation, error)
	// GetWarpConversation returns one thread with its messages in order, or
	// ErrWarpConversationNotFound when it does not exist for this owner.
	GetWarpConversation(ctx context.Context, ownerID, id string) (*tables.TableWarpConversation, error)
	// CreateWarpConversation starts a thread.
	CreateWarpConversation(ctx context.Context, conversation *tables.TableWarpConversation) error
	// AppendWarpMessages adds turns to a thread, bumping its updated time so it
	// sorts to the top of the list.
	AppendWarpMessages(ctx context.Context, ownerID, conversationID string, messages []tables.TableWarpMessage) error
	// DeleteWarpConversation removes a thread and its messages.
	DeleteWarpConversation(ctx context.Context, ownerID, id string) error
	// PruneWarpConversations drops an owner's oldest threads beyond keep.
	PruneWarpConversations(ctx context.Context, ownerID string, keep int) (int64, error)
	// CountWarpMessages returns message counts for the given threads in one
	// query, so a list view does not issue a count per row.
	CountWarpMessages(ctx context.Context, conversationIDs []string) (map[string]int, error)
}

// ListWarpConversations returns an owner's threads, most recent first.
//
// Messages are deliberately not preloaded: the list renders a title and a count,
// and pulling every transcript to draw a sidebar would make opening the history
// cost more than the conversation it lists.
func (s *RDBConfigStore) ListWarpConversations(ctx context.Context, ownerID string, limit int) ([]tables.TableWarpConversation, error) {
	if limit <= 0 {
		limit = 50
	}
	var conversations []tables.TableWarpConversation
	err := s.DB().WithContext(ctx).
		Where("owner_id = ?", ownerID).
		Order("updated_at DESC").
		Limit(limit).
		Find(&conversations).Error
	return conversations, err
}

// CountWarpMessages returns message counts for the given threads in one query,
// so the list view does not issue a count per row.
func (s *RDBConfigStore) CountWarpMessages(ctx context.Context, conversationIDs []string) (map[string]int, error) {
	counts := make(map[string]int, len(conversationIDs))
	if len(conversationIDs) == 0 {
		return counts, nil
	}
	type row struct {
		ConversationID string
		Total          int
	}
	var rows []row
	err := s.DB().WithContext(ctx).
		Model(&tables.TableWarpMessage{}).
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

// GetWarpConversation returns one thread with its messages in order.
func (s *RDBConfigStore) GetWarpConversation(ctx context.Context, ownerID, id string) (*tables.TableWarpConversation, error) {
	var conversation tables.TableWarpConversation
	err := s.DB().WithContext(ctx).
		// Ordering by position, not created_at: two turns written in the same
		// millisecond would otherwise come back arbitrarily ordered.
		Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("position ASC") }).
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
	return &conversation, nil
}

// CreateWarpConversation starts a thread.
func (s *RDBConfigStore) CreateWarpConversation(ctx context.Context, conversation *tables.TableWarpConversation) error {
	return s.DB().WithContext(ctx).Create(conversation).Error
}

// AppendWarpMessages adds turns to a thread and bumps its updated time.
//
// Both happen in one transaction. A thread whose messages landed but whose
// timestamp did not would sink down the history list despite being the most
// recent, which reads as the save having failed.
func (s *RDBConfigStore) AppendWarpMessages(ctx context.Context, ownerID, conversationID string, messages []tables.TableWarpMessage) error {
	if len(messages) == 0 {
		return nil
	}
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Re-assert ownership inside the transaction: the id arrived from the
		// request, and appending to a thread is a write.
		var existing tables.TableWarpConversation
		if err := tx.Where("id = ? AND owner_id = ?", conversationID, ownerID).First(&existing).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWarpConversationNotFound
			}
			return err
		}

		var nextPosition int64
		if err := tx.Model(&tables.TableWarpMessage{}).
			Where("conversation_id = ?", conversationID).
			Count(&nextPosition).Error; err != nil {
			return err
		}
		for i := range messages {
			messages[i].ConversationID = conversationID
			messages[i].Position = int(nextPosition) + i
		}
		if err := tx.Create(&messages).Error; err != nil {
			return err
		}
		return tx.Model(&existing).Update("updated_at", messages[len(messages)-1].CreatedAt).Error
	})
}

// DeleteWarpConversation removes a thread and its messages.
func (s *RDBConfigStore) DeleteWarpConversation(ctx context.Context, ownerID, id string) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("id = ? AND owner_id = ?", id, ownerID).Delete(&tables.TableWarpConversation{})
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
		return tx.Where("conversation_id = ?", id).Delete(&tables.TableWarpMessage{}).Error
	})
}

// PruneWarpConversations drops an owner's oldest threads beyond keep.
//
// Warp's history is a convenience, not a record of account. Without a cap the
// table grows for the life of the deployment, and nobody scrolls back past the
// last few dozen threads anyway.
func (s *RDBConfigStore) PruneWarpConversations(ctx context.Context, ownerID string, keep int) (int64, error) {
	if keep <= 0 {
		return 0, fmt.Errorf("keep must be positive")
	}
	var stale []tables.TableWarpConversation
	if err := s.DB().WithContext(ctx).
		Where("owner_id = ?", ownerID).
		Order("updated_at DESC").
		Offset(keep).
		Limit(1000).
		Find(&stale).Error; err != nil {
		return 0, err
	}
	if len(stale) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(stale))
	for _, conversation := range stale {
		ids = append(ids, conversation.ID)
	}
	var deleted int64
	err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("conversation_id IN ?", ids).Delete(&tables.TableWarpMessage{}).Error; err != nil {
			return err
		}
		result := tx.Where("id IN ?", ids).Delete(&tables.TableWarpConversation{})
		deleted = result.RowsAffected
		return result.Error
	})
	return deleted, err
}
