package tables

import "time"

// TableAlertRule defines a threshold-based alerting rule.
type TableAlertRule struct {
	ID             string    `gorm:"primaryKey;type:text"`
	Name           string    `gorm:"uniqueIndex;not null"`
	Enabled        bool      `gorm:"default:true"`
	AlertType      string    `gorm:"type:text"` // "budget_threshold"|"rate_limit_threshold"|"error_rate"|"latency_p95"|"cost_spike"|"provider_down"|"guardrail_violation_spike"
	Severity       string    `gorm:"type:text"` // "info"|"warning"|"critical"
	ConditionJSON  string    `gorm:"type:text"` // JSON: alert-type-specific thresholds
	ScopeJSON      string    `gorm:"type:text"` // JSON: {type, id} — scope of evaluation
	ChannelIDs     string    `gorm:"type:text"` // JSON: []string (channel IDs to notify)
	RepeatInterval int       `gorm:"default:0"` // seconds; 0 = no repeat
	SilencedUntil  *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (TableAlertRule) TableName() string { return "alert_rules" }

// TableAlertChannel defines a notification delivery channel.
type TableAlertChannel struct {
	ID          string    `gorm:"primaryKey;type:text"`
	Name        string    `gorm:"not null"`
	Type        string    `gorm:"type:text"` // "slack"|"pagerduty"|"webhook"|"email"
	ConfigJSON  string    `gorm:"type:text"` // encrypted channel-specific config
	Enabled     bool      `gorm:"default:true"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (TableAlertChannel) TableName() string { return "alert_channels" }

// TableAlertState tracks the current state of each alert rule.
type TableAlertState struct {
	RuleID           string    `gorm:"primaryKey;type:text"`
	State            string    `gorm:"type:text"` // "inactive"|"firing"|"resolved"|"silenced"
	FiredAt          *time.Time
	ResolvedAt       *time.Time
	LastNotifiedAt   *time.Time
	ConsecErrorCount int
	UpdatedAt        time.Time
}

func (TableAlertState) TableName() string { return "alert_states" }

// TableAlertHistory records alert state transitions.
type TableAlertHistory struct {
	ID         string    `gorm:"primaryKey;type:text"`
	RuleID     string    `gorm:"index"`
	Timestamp  time.Time `gorm:"index"`
	Event      string    `gorm:"type:text"` // "fired"|"resolved"|"silenced"|"repeat"
	Severity   string    `gorm:"type:text"`
	MetricJSON string    `gorm:"type:text"` // snapshot of the metric value at event time
}

func (TableAlertHistory) TableName() string { return "alert_history" }
