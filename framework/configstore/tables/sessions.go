package tables

import "time"

// SessionsTable represents a session in the database
type SessionsTable struct {
	ID               int       `gorm:"primaryKey;autoIncrement" json:"id"`
	Token            string    `gorm:"type:varchar(255);not null;uniqueIndex" json:"token"`
	ExpiresAt        time.Time `gorm:"index;not null" json:"expires_at,omitempty"`
	CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"index;not null" json:"updated_at"`
	EncryptionStatus string    `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	TokenHash        string    `gorm:"type:varchar(64);index:idx_session_token_hash,unique" json:"-"`
}

// TableName sets the table name for each model
func (SessionsTable) TableName() string { return "sessions" }
