package configstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// CreateSidekiqJob inserts a new background job. The caller supplies the id, kind
// and metadata; status defaults to pending and timestamps are stamped here.
func (s *RDBConfigStore) CreateSidekiqJob(ctx context.Context, job *tables.TableSidekiqJob) error {
	if job == nil {
		return errors.New("sidekiq job is required")
	}
	if strings.TrimSpace(job.ID) == "" {
		return errors.New("sidekiq job id is required")
	}
	if strings.TrimSpace(job.Kind) == "" {
		return errors.New("sidekiq job kind is required")
	}
	now := time.Now()
	if job.Status == "" {
		job.Status = tables.SidekiqStatusPending
	}
	if job.Metadata == "" {
		job.Metadata = "{}"
	}
	job.CreatedAt = now
	job.UpdatedAt = now
	return s.DB().WithContext(ctx).Create(job).Error
}

// GetSidekiqJob returns a single job by id, or nil when it does not exist.
func (s *RDBConfigStore) GetSidekiqJob(ctx context.Context, id string) (*tables.TableSidekiqJob, error) {
	var job tables.TableSidekiqJob
	err := s.DB().WithContext(ctx).Where("id = ?", id).First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// MarkSidekiqJobRunning transitions a job to running, stamps started_at, bumps the
// heartbeat (updated_at), and increments the attempt counter. Safe to call on
// resume: each resumed run counts as a fresh attempt.
func (s *RDBConfigStore) MarkSidekiqJobRunning(ctx context.Context, id string) error {
	now := time.Now()
	res := s.DB().WithContext(ctx).
		Model(&tables.TableSidekiqJob{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":     tables.SidekiqStatusRunning,
			"started_at": now,
			"updated_at": now,
			"attempts":   gorm.Expr("attempts + 1"),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("sidekiq job not found or already in terminal state")
	}
	return nil
}

// UpdateSidekiqJobProgress persists a progress checkpoint: it replaces the metadata
// blob and bumps the heartbeat (updated_at) so the reaper does not treat the job as
// stale. Called after each processed page.
func (s *RDBConfigStore) UpdateSidekiqJobProgress(ctx context.Context, id, metadata string) error {
	res := s.DB().WithContext(ctx).
		Model(&tables.TableSidekiqJob{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"metadata":   metadata,
			"updated_at": time.Now(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("sidekiq job not found")
	}
	return nil
}

// CompleteSidekiqJob marks a job completed, stamps completed_at, and stores the
// final metadata (counts, summary).
func (s *RDBConfigStore) CompleteSidekiqJob(ctx context.Context, id, metadata string) error {
	now := time.Now()
	res := s.DB().WithContext(ctx).
		Model(&tables.TableSidekiqJob{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":       tables.SidekiqStatusCompleted,
			"metadata":     metadata,
			"updated_at":   now,
			"completed_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("sidekiq job not found")
	}
	return nil
}

// FailSidekiqJob marks a job failed, records the error, stamps completed_at, and
// preserves the latest metadata so a later resume can read the checkpoint cursor.
func (s *RDBConfigStore) FailSidekiqJob(ctx context.Context, id, metadata, lastErr string) error {
	now := time.Now()
	updates := map[string]any{
		"status":       tables.SidekiqStatusFailed,
		"last_error":   lastErr,
		"updated_at":   now,
		"completed_at": now,
	}
	if metadata != "" {
		updates["metadata"] = metadata
	}
	res := s.DB().WithContext(ctx).
		Model(&tables.TableSidekiqJob{}).
		Where("id = ?", id).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("sidekiq job not found")
	}
	return nil
}

// ListIncompleteSidekiqJobs returns jobs that are not in a terminal state
// (pending or running). Used by startup recovery to resume work that was
// interrupted by a restart or crash.
func (s *RDBConfigStore) ListIncompleteSidekiqJobs(ctx context.Context) ([]tables.TableSidekiqJob, error) {
	var jobs []tables.TableSidekiqJob
	err := s.DB().WithContext(ctx).
		Where("status IN ?", []string{tables.SidekiqStatusPending, tables.SidekiqStatusRunning}).
		Order("created_at ASC").
		Find(&jobs).Error
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

// MarkStaleSidekiqJobsFailed flips any running job whose heartbeat (updated_at) is
// older than staleBefore to failed. This is the safety net for a goroutine or node
// that died without marking its job: the job stops looking "running" and becomes
// eligible for inspection or a manual resume. Returns the number of jobs reaped.
func (s *RDBConfigStore) MarkStaleSidekiqJobsFailed(ctx context.Context, staleBefore time.Time) (int64, error) {
	now := time.Now()
	res := s.DB().WithContext(ctx).
		Model(&tables.TableSidekiqJob{}).
		Where("status = ? AND updated_at < ?", tables.SidekiqStatusRunning, staleBefore).
		Updates(map[string]any{
			"status":       tables.SidekiqStatusFailed,
			"last_error":   "job timed out: no heartbeat before stale threshold",
			"updated_at":   now,
			"completed_at": now,
		})
	return res.RowsAffected, res.Error
}
