package tables

import "time"

// TableNotification stores deployment notifications. RoleIDs is JSON rather
// than a foreign-key relation because enterprise roles are supplied by an
// optional overlay and do not exist in every OSS database.
type TableNotification struct {
	ID          string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	Audience    string    `gorm:"type:varchar(16);not null;index:idx_notifications_feed,priority:1" json:"audience"`
	RoleIDs     []uint    `gorm:"serializer:json;type:text" json:"role_ids"`
	Severity    string    `gorm:"type:varchar(16);not null" json:"severity"`
	Title       string    `gorm:"type:varchar(160);not null" json:"title"`
	Message     string    `gorm:"type:text;not null" json:"message"`
	ActionLabel string    `gorm:"type:varchar(64)" json:"action_label,omitempty"`
	ActionPath  string    `gorm:"type:varchar(2048)" json:"action_path,omitempty"`
	CreatedAt   time.Time `gorm:"not null;index:idx_notifications_feed,priority:2,sort:desc" json:"created_at"`
	ExpiresAt   time.Time `gorm:"not null;index" json:"expires_at"`
}

func (TableNotification) TableName() string { return "notifications" }
