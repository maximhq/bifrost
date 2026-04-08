package tables

import "time"

// TableGuardrailPolicy defines a content policy that controls guardrail behaviour for a scope.
type TableGuardrailPolicy struct {
	ID         string    `gorm:"primaryKey;type:text"`
	Name       string    `gorm:"uniqueIndex;not null"`
	Enabled    bool      `gorm:"default:true"`
	Scope      string    `gorm:"type:text"`   // "global"|"virtual_key"|"user_group"
	ScopeID    string    `gorm:"type:text"`   // VK ID or user group ID; empty = global
	Action     string    `gorm:"type:text"`   // "block"|"warn"|"log_only"
	LayersJSON string    `gorm:"type:text"`   // JSON: which layers to enable
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (TableGuardrailPolicy) TableName() string { return "guardrail_policies" }

// TableGuardrailRule is a single matching rule within a policy.
type TableGuardrailRule struct {
	ID        string `gorm:"primaryKey;type:text"`
	PolicyID  string `gorm:"index;not null"`
	RuleType  string `gorm:"type:text"` // "keyword"|"regex"|"topic"|"custom"
	Pattern   string `gorm:"type:text"` // regex pattern or keyword list
	TopicName string `gorm:"type:text"` // for topic rules
	Direction string `gorm:"type:text"` // "input"|"output"|"both"
	Severity  string `gorm:"type:text"` // "low"|"medium"|"high"|"critical"
	Enabled   bool   `gorm:"default:true"`
}

func (TableGuardrailRule) TableName() string { return "guardrail_rules" }

// TableGuardrailViolation records each policy violation.
type TableGuardrailViolation struct {
	ID           string    `gorm:"primaryKey;type:text"`
	Timestamp    time.Time `gorm:"index"`
	RequestID    string    `gorm:"index"`
	PolicyID     string    `gorm:"index"`
	RuleID       string    `gorm:"index"`
	VirtualKeyID string    `gorm:"index"`
	Layer        string    `gorm:"type:text"` // "keyword"|"topic"|"ai"|"custom"
	Direction    string    `gorm:"type:text"` // "input"|"output"
	Action       string    `gorm:"type:text"` // "blocked"|"warned"|"logged"
	Pattern      string    `gorm:"type:text"` // matched pattern (redacted if sensitive)
	ModelUsed    string    `gorm:"type:text"`
}

func (TableGuardrailViolation) TableName() string { return "guardrail_violations" }
