package tables

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Provider job kinds. The kind discriminates the job families sharing the
// batch_jobs table; jobaccounting.ProviderJobKind is the typed view of these.
const (
	ProviderJobKindBatch = "batch"
	ProviderJobKindVideo = "video"
)

// Accounting lifecycle states for a TableProviderJob.
const (
	ProviderJobAccountingStatusPending     = "pending"
	ProviderJobAccountingStatusProcessing  = "processing"
	ProviderJobAccountingStatusAccounted   = "accounted"
	ProviderJobAccountingStatusUnpriceable = "unpriceable"
	ProviderJobAccountingStatusError       = "error"
)

// TableProviderJob is the mutable coordination record for delayed accounting of a
// job the provider runs on its own schedule — a batch, a video generation.
//
// It lives in the config store (relational, single-writer-friendly) rather than
// the log store because the job lifecycle is a state machine that is UPDATE-d
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
type TableProviderJob struct {
	ID string `gorm:"primaryKey;type:varchar(512)" json:"id"`
	// Kind discriminates the job family. Rows written before the column existed
	// default to batch, which is what every one of them was.
	Kind     string `gorm:"type:varchar(50);not null;default:'batch';uniqueIndex:idx_batch_jobs_identity_v2,priority:2;index:idx_batch_jobs_sweeper_v2,priority:1" json:"kind"`
	Provider string `gorm:"type:varchar(255);uniqueIndex:idx_batch_jobs_identity_v2,priority:1;index:idx_batch_jobs_sweeper_v2,priority:2;not null" json:"provider"`
	// JobID is the provider-side identifier: a batch id, a video id. The column is
	// still named batch_id — renaming it would break old pods mid-rolling-deploy
	// for no gain.
	JobID    string `gorm:"column:batch_id;type:varchar(255);uniqueIndex:idx_batch_jobs_identity_v2,priority:3;not null" json:"batch_id"`
	Model    string `gorm:"type:varchar(255)" json:"model,omitempty"`
	Endpoint string `gorm:"type:varchar(255)" json:"endpoint,omitempty"`

	// Params is the kind's JSON-encoded request dimensions, captured at submission.
	// It is load-bearing for video: a provider's retrieve response usually reports
	// only that the job finished, so the duration and resolution the price depends
	// on exist nowhere else by the time settlement runs.
	Params *string `gorm:"type:text" json:"params,omitempty"`

	ProviderStatus string  `gorm:"type:varchar(50)" json:"provider_status,omitempty"`
	InputFileID    string  `gorm:"type:varchar(255)" json:"input_file_id,omitempty"`
	OutputFileID   *string `gorm:"type:varchar(255)" json:"output_file_id,omitempty"`
	ErrorFileID    *string `gorm:"type:varchar(255)" json:"error_file_id,omitempty"`
	ResultsURL     *string `gorm:"type:text" json:"results_url,omitempty"`

	NextCheckAt      *time.Time `gorm:"index:idx_batch_jobs_sweeper_v2,priority:4" json:"next_check_at,omitempty"`
	PollAttempts     int        `gorm:"default:0" json:"poll_attempts"`
	AccountingStatus string     `gorm:"type:varchar(50);index:idx_batch_jobs_sweeper_v2,priority:3;not null" json:"accounting_status"`

	// RunnerID fences an in-flight accounting attempt to the runner that claimed it.
	RunnerID *string `gorm:"type:varchar(255);index" json:"runner_id,omitempty"`
	// ClaimedAt is when RunnerID took the claim, and is the sole basis for deciding
	// a claim has gone stale. It is deliberately NOT UpdatedAt: UpdatedAt is
	// refreshed by UpsertProviderJob, which is unfenced and runs on every poll, so
	// using it would let routine polling keep a dead runner's claim looking fresh
	// forever and make the job unreclaimable. Only ClaimProviderJob sets this, and
	// only the finishing transitions clear it.
	ClaimedAt *time.Time `json:"claimed_at,omitempty"`

	UnpriceableReason     *string    `gorm:"type:varchar(255)" json:"unpriceable_reason,omitempty"`
	LastError             *string    `gorm:"type:text" json:"last_error,omitempty"`
	AggregateLogWrittenAt *time.Time `json:"aggregate_log_written_at,omitempty"`
	GovernanceReportedAt  *time.Time `json:"governance_reported_at,omitempty"`

	SelectedKeyID string  `gorm:"type:varchar(255)" json:"selected_key_id,omitempty"`
	VirtualKeyID  *string `gorm:"type:varchar(255)" json:"virtual_key_id,omitempty"`
	UserID        *string `gorm:"type:varchar(255);index" json:"user_id,omitempty"`
	TeamID        *string `gorm:"type:varchar(255)" json:"team_id,omitempty"`
	CustomerID    *string `gorm:"type:varchar(255)" json:"customer_id,omitempty"`
	BudgetIDs     *string `gorm:"type:text" json:"-"`
	RateLimitIDs  *string `gorm:"type:text" json:"-"`

	SourceLogID *string `gorm:"type:varchar(255)" json:"source_log_id,omitempty"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
}

// TableName returns the backing table name. It stays "batch_jobs" now that the
// table holds every provider job kind: renaming it is data-safe but breaks old
// pods mid-rolling-deploy for no benefit.
func (TableProviderJob) TableName() string {
	return "batch_jobs"
}

// ProviderJobID builds the stable primary key for a provider job. The identity is
// kind + provider + job ID so the sweeper, user-triggered settlement, and future
// reconcilers all resolve to the same cluster-safe row.
//
// The batch form is byte-identical to the pre-kind formula and must stay that way:
// it is the primary key of every batch row already in the table, so changing it
// would orphan them and settle each one a second time under a new id.
func ProviderJobID(kind, provider, jobID string) string {
	if kind == "" || kind == ProviderJobKindBatch {
		return "batch-job:" + provider + ":" + jobID
	}
	return kind + "-job:" + provider + ":" + jobID
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
