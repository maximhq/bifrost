package tables

import "time"

// TablePIIPolicy defines a PII redaction policy for a scope.
type TablePIIPolicy struct {
	ID            string    `gorm:"primaryKey;type:text"`
	Name          string    `gorm:"uniqueIndex;not null"`
	Enabled       bool      `gorm:"default:true"`
	Scope         string    `gorm:"type:text"` // "global"|"virtual_key"|"user_group"
	ScopeID       string    `gorm:"type:text"`
	RedactInput   bool      `gorm:"default:true"`
	RedactOutput  bool      `gorm:"default:false"`
	LogViolations bool      `gorm:"default:true"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (TablePIIPolicy) TableName() string { return "pii_policies" }

// TablePIIDetectorRule is a single entity-type rule within a PII policy.
type TablePIIDetectorRule struct {
	ID         string `gorm:"primaryKey;type:text"`
	PolicyID   string `gorm:"index;not null"`
	EntityType string `gorm:"type:text"` // "email"|"phone"|"ssn"|"credit_card"|"name"|"address"|"ip_address"|"custom"
	Mode       string `gorm:"type:text"` // "mask"|"hash"|"tokenize"|"partial"
	Pattern    string `gorm:"type:text"` // custom regex; empty = built-in detector
	Enabled    bool   `gorm:"default:true"`
}

func (TablePIIDetectorRule) TableName() string { return "pii_detector_rules" }

// TablePIITokenStore stores stable UUID tokens for the tokenize redaction mode.
type TablePIITokenStore struct {
	Token        string     `gorm:"primaryKey;type:text"` // UUID
	EntityType   string     `gorm:"index"`
	OriginalHash string     `gorm:"index"` // SHA-256 of original value (for dedup)
	CreatedAt    time.Time
	ExpiresAt    *time.Time
}

func (TablePIITokenStore) TableName() string { return "pii_token_store" }
