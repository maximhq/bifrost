package tables

import "time"

// Sidekiq job status values.
const (
	// SidekiqStatusPending marks a job that has been enqueued but not yet started.
	SidekiqStatusPending = "pending"
	// SidekiqStatusRunning marks a job whose goroutine is actively processing it.
	SidekiqStatusRunning = "running"
	// SidekiqStatusCompleted marks a job that finished successfully.
	SidekiqStatusCompleted = "completed"
	// SidekiqStatusFailed marks a job that errored or was reaped as stale.
	SidekiqStatusFailed = "failed"
)

// TableSidekiqJob is a generic, durable background-job record. It is intentionally
// not tied to any feature: callers store all job-specific data (provider, filters,
// resume cursor, running counts, errors) in the Metadata JSON blob. The runner only
// reads Status and UpdatedAt; everything else is opaque to it.
type TableSidekiqJob struct {
	ID          string     `gorm:"column:id;primaryKey;type:text" json:"id"`
	Kind        string     `gorm:"column:kind;not null;type:text;index:idx_sidekiq_status_updated,priority:3;index:idx_sidekiq_kind_status_created,priority:1" json:"kind"`
	Status      string     `gorm:"column:status;not null;default:pending;type:text;index:idx_sidekiq_status_updated,priority:1;index:idx_sidekiq_kind_status_created,priority:2" json:"status"`
	// RunnerID identifies the runner process that currently owns (claimed) this job.
	// Empty in OSS (single-node) mode; set to the node ID in enterprise cluster mode.
	// Progress/complete/fail/heartbeat writes are fenced on this value so a revived
	// stale node cannot stomp a job another node has re-claimed.
	RunnerID    string     `gorm:"column:runner_id;type:text;index" json:"runner_id,omitempty"`
	Metadata    string     `gorm:"column:metadata;type:text;default:'{}'" json:"metadata"`
	Attempts    int        `gorm:"column:attempts;not null;default:0" json:"attempts"`
	LastError   string     `gorm:"column:last_error;type:text" json:"last_error,omitempty"`
	CreatedAt   time.Time  `gorm:"column:created_at;not null;index:idx_sidekiq_kind_status_created,priority:3" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;not null;index:idx_sidekiq_status_updated,priority:2" json:"updated_at"`
	StartedAt       *time.Time `gorm:"column:started_at" json:"started_at,omitempty"`
	CreatedByUserID *string    `gorm:"column:created_by_user_id;type:varchar(255)" json:"created_by_user_id,omitempty"`
	CompletedAt     *time.Time `gorm:"column:completed_at" json:"completed_at,omitempty"`
}

// TableName returns the backing table name for sidekiq jobs.
func (TableSidekiqJob) TableName() string {
	return "sidekiq"
}
