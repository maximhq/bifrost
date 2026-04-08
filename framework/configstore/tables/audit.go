package tables

import "time"

// TableAuditLog stores an immutable, append-only audit record for every
// admin-plane state-changing operation. Each record is SHA-256 hash-chained
// to the previous record to enable tamper detection.
type TableAuditLog struct {
	ID         string    `gorm:"primaryKey;type:varchar(255)" json:"id"`                    // UUID v4
	Seq        int64     `gorm:"autoIncrement;uniqueIndex;not null" json:"seq"`              // monotonic sequence
	Timestamp  time.Time `gorm:"index;not null" json:"timestamp"`
	ActorID    string    `gorm:"type:varchar(255);index;not null" json:"actor_id"`           // user ID or "system"
	ActorEmail string    `gorm:"type:varchar(512)" json:"actor_email"`
	ActorRole  string    `gorm:"type:varchar(255)" json:"actor_role"` // role at time of action
	Action     string    `gorm:"type:varchar(255);index;not null" json:"action"`            // "providers.create" | "vk.delete" | ...
	Resource   string    `gorm:"type:varchar(100);index" json:"resource"`                   // resource type
	ResourceID string    `gorm:"type:varchar(255);index" json:"resource_id"`                // affected entity ID
	RequestID  string    `gorm:"type:varchar(255);index" json:"request_id"`
	ClientIP   string    `gorm:"type:varchar(64)" json:"client_ip"`
	UserAgent  string    `gorm:"type:text" json:"user_agent"`
	OldValue   string    `gorm:"type:text" json:"old_value,omitempty"` // JSON snapshot before change
	NewValue   string    `gorm:"type:text" json:"new_value,omitempty"` // JSON snapshot after change
	Result     string    `gorm:"type:varchar(20);not null" json:"result"` // "success" | "failure"
	ErrorMsg   string    `gorm:"type:text" json:"error_msg,omitempty"`
	// Hash chaining for tamper detection
	Hash     string `gorm:"type:varchar(64);not null" json:"hash"`          // SHA-256 of this record
	PrevHash string `gorm:"type:varchar(64);not null" json:"prev_hash"`     // hash of previous record
}

// TableName sets the table name for TableAuditLog
func (TableAuditLog) TableName() string { return "audit_logs" }
