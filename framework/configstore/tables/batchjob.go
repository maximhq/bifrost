package tables

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Batch accounting lifecycle states for a TableBatchJob.
const (
	BatchJobAccountingStatusPending     = "pending"
	BatchJobAccountingStatusProcessing  = "processing"
	BatchJobAccountingStatusAccounted   = "accounted"
	BatchJobAccountingStatusUnpriceable = "unpriceable"
	BatchJobAccountingStatusError       = "error"
)

// TableBatchJob is the mutable coordination record for delayed batch accounting.
//
// It lives in the config store (relational, single-writer-friendly) rather than
// the log store because the batch lifecycle is a state machine that is UPDATE-d
// in place many times (poll rescheduling, claim/ownership, settlement markers) —
// a poor fit for the append-only log store and its ClickHouse backend. The
// append-only cost record is written separately as an aggregate row in the logs
// table.
//
// Ownership is fenced sidekiq-style: RunnerID identifies the runner that holds an
// in-flight accounting attempt, and staleness is UpdatedAt-based (no separate
// claim token) — a job stuck in "processing" past the caller's stale threshold
// can be re-claimed by another runner, and every advance/complete is fenced on
// RunnerID so a former owner cannot stomp a re-claimed job.
type TableBatchJob struct {
	ID       string `gorm:"primaryKey;type:varchar(512)" json:"id"`
	Provider string `gorm:"type:varchar(255);uniqueIndex:idx_batch_jobs_identity,priority:1;index:idx_batch_jobs_sweeper,priority:1;not null" json:"provider"`
	BatchID  string `gorm:"type:varchar(255);uniqueIndex:idx_batch_jobs_identity,priority:2;not null" json:"batch_id"`
	Model    string `gorm:"type:varchar(255)" json:"model,omitempty"`
	Endpoint string `gorm:"type:varchar(255)" json:"endpoint,omitempty"`

	ProviderStatus string  `gorm:"type:varchar(50)" json:"provider_status,omitempty"`
	InputFileID    string  `gorm:"type:varchar(255)" json:"input_file_id,omitempty"`
	OutputFileID   *string `gorm:"type:varchar(255)" json:"output_file_id,omitempty"`
	ErrorFileID    *string `gorm:"type:varchar(255)" json:"error_file_id,omitempty"`
	ResultsURL     *string `gorm:"type:text" json:"results_url,omitempty"`

	NextCheckAt      *time.Time `gorm:"index:idx_batch_jobs_sweeper,priority:3" json:"next_check_at,omitempty"`
	PollAttempts     int        `gorm:"default:0" json:"poll_attempts"`
	AccountingStatus string     `gorm:"type:varchar(50);index:idx_batch_jobs_sweeper,priority:2;not null" json:"accounting_status"`

	// RunnerID fences an in-flight accounting attempt to the runner that claimed it.
	RunnerID *string `gorm:"type:varchar(255);index" json:"runner_id,omitempty"`

	UnpriceableReason     *string    `gorm:"type:varchar(255)" json:"unpriceable_reason,omitempty"`
	LastError             *string    `gorm:"type:text" json:"last_error,omitempty"`
	AggregateLogWrittenAt *time.Time `json:"aggregate_log_written_at,omitempty"`
	GovernanceReportedAt  *time.Time `json:"governance_reported_at,omitempty"`

	SelectedKeyID string  `gorm:"type:varchar(255)" json:"selected_key_id,omitempty"`
	VirtualKeyID  *string `gorm:"type:varchar(255)" json:"virtual_key_id,omitempty"`
	BudgetIDs     *string `gorm:"type:text" json:"-"`
	RateLimitIDs  *string `gorm:"type:text" json:"-"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
}

// TableName returns the backing table name for batch jobs.
func (TableBatchJob) TableName() string {
	return "batch_jobs"
}

// BatchJobID builds the stable primary key for a batch job. The identity is
// provider + batch ID so the sweeper, user-triggered /results, and future
// reconcilers all resolve to the same cluster-safe row.
func BatchJobID(provider, batchID string) string {
	return "batch-job:" + provider + ":" + batchID
}

// IsTerminalBatchProviderStatus reports whether a provider batch status is
// terminal (the provider will not advance it further).
func IsTerminalBatchProviderStatus(status string) bool {
	switch schemas.BatchStatus(status) {
	case schemas.BatchStatusCompleted, schemas.BatchStatusFailed, schemas.BatchStatusExpired,
		schemas.BatchStatusCancelled, schemas.BatchStatusEnded, schemas.BatchStatusDeleted:
		return true
	default:
		return false
	}
}
