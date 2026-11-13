package tables

import "time"

// TableComplexitySuggestionSchedule is a router's phrase-suggestion schedule
// as it runs: when the next run is due, and which run last took a slot. The
// schedule's settings stay in the router's configuration; this row is the
// state that settings produce, kept apart so it never enters the config the
// config.json reconciliation hashes.
//
// Nodes claim a due run by moving NextRunAt on while Version still matches,
// so exactly one of them starts it.
type TableComplexitySuggestionSchedule struct {
	RouterID     string     `gorm:"type:varchar(64);primaryKey" json:"router_id"`
	ScheduleHash string     `gorm:"type:varchar(64);not null" json:"schedule_hash"` // of the settings NextRunAt was computed from
	NextRunAt    time.Time  `gorm:"not null" json:"next_run_at"`
	LastRunAt    *time.Time `json:"last_run_at,omitempty"` // the slot the last claimed run was due at
	LastRunID    string     `gorm:"type:varchar(36);not null;default:''" json:"last_run_id"`
	Version      int64      `gorm:"not null;default:0" json:"version"` // bumped by every write; a claim needs the one it read
	UpdatedAt    time.Time  `gorm:"not null" json:"updated_at"`
}

// TableName sets the table name for TableComplexitySuggestionSchedule.
func (TableComplexitySuggestionSchedule) TableName() string {
	return "model_routing_suggestion_schedules"
}
