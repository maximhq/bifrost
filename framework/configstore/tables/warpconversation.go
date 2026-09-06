package tables

import "time"

// TableWarpConversation is one saved Warp thread.
//
// OwnerID is the whole access-control story: reads always filter by it, and it
// is derived server-side from the caller's identity, never accepted from the
// request. On a deployment without authentication every conversation shares
// schemas.WarpGlobalOwnerID, which makes the history common to the deployment -
// the same query, just a different owner.
type TableWarpConversation struct {
	ID string `gorm:"type:varchar(36);primaryKey" json:"id"`
	// Indexed together with UpdatedAt because the only list query is "this
	// owner's threads, most recent first".
	OwnerID string `gorm:"type:varchar(255);not null;index:idx_warp_conversations_owner,priority:1" json:"owner_id"`
	Title   string `gorm:"type:varchar(255);not null" json:"title"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null;index:idx_warp_conversations_owner,priority:2,sort:desc" json:"updated_at"`

	// Messages is loaded only for a detail read. The list view reports a count
	// instead, so opening the history does not pull every transcript.
	Messages []TableWarpMessage `gorm:"foreignKey:ConversationID;constraint:OnDelete:CASCADE" json:"messages,omitempty"`
}

// TableName sets the table name for the Warp conversation model.
func (TableWarpConversation) TableName() string { return "warp_conversations" }

// TableWarpMessage is one persisted turn.
//
// Ordering is by Position rather than CreatedAt: two turns written in the same
// millisecond would otherwise come back in an arbitrary order, and a transcript
// that reorders itself on reload is worse than one that loads slowly.
type TableWarpMessage struct {
	ID             string `gorm:"type:varchar(36);primaryKey" json:"id"`
	ConversationID string `gorm:"type:varchar(36);not null;index:idx_warp_messages_thread,priority:1" json:"conversation_id"`
	Position       int    `gorm:"not null;index:idx_warp_messages_thread,priority:2" json:"position"`

	Role    string `gorm:"type:varchar(16);not null" json:"role"`
	Content string `gorm:"type:text" json:"content"`
	// ToolCallsJSON records what Warp queried, so a reopened thread shows the
	// same provenance the live one did. Serialised rather than a child table:
	// it is only ever read and written whole, alongside its message.
	ToolCallsJSON string `gorm:"type:text" json:"-"`
	Error         string `gorm:"type:text" json:"error,omitempty"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
}

// TableName sets the table name for the Warp message model.
func (TableWarpMessage) TableName() string { return "warp_messages" }
