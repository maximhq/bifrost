package configstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// UpsertBatchJob inserts a new batch job or merges non-zero fields into an
// existing one. The identity row (id) is never overwritten; only the mutable
// lifecycle/hint columns are advanced. Terminal-but-not-completed provider states
// clear next_check_at so the sweeper stops polling.
func (s *RDBConfigStore) UpsertBatchJob(ctx context.Context, job *tables.TableBatchJob) error {
	if job == nil || job.ID == "" {
		return fmt.Errorf("batch job and id are required")
	}
	now := time.Now().UTC()
	if job.AccountingStatus == "" {
		job.AccountingStatus = tables.BatchJobAccountingStatusPending
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now

	db := s.DB().WithContext(ctx)
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoNothing: true,
	}).Create(job).Error; err != nil {
		return err
	}

	updates := map[string]interface{}{"updated_at": now}
	if job.Model != "" {
		updates["model"] = job.Model
	}
	if job.Endpoint != "" {
		updates["endpoint"] = job.Endpoint
	}
	if job.ProviderStatus != "" {
		updates["provider_status"] = job.ProviderStatus
	}
	if job.InputFileID != "" {
		updates["input_file_id"] = job.InputFileID
	}
	if job.OutputFileID != nil {
		updates["output_file_id"] = job.OutputFileID
	}
	if job.ErrorFileID != nil {
		updates["error_file_id"] = job.ErrorFileID
	}
	if job.ResultsURL != nil {
		updates["results_url"] = job.ResultsURL
	}
	if job.NextCheckAt != nil {
		updates["next_check_at"] = job.NextCheckAt
	}
	if job.PollAttempts > 0 {
		updates["poll_attempts"] = job.PollAttempts
	}
	if tables.IsTerminalBatchProviderStatus(job.ProviderStatus) &&
		job.ProviderStatus != string(schemas.BatchStatusCompleted) &&
		job.ProviderStatus != string(schemas.BatchStatusEnded) {
		updates["next_check_at"] = nil
	}

	return db.Model(&tables.TableBatchJob{}).Where("id = ?", job.ID).Updates(updates).Error
}

// GetBatchJob returns a batch job by its stable id, or ErrNotFound.
func (s *RDBConfigStore) GetBatchJob(ctx context.Context, jobID string) (*tables.TableBatchJob, error) {
	if jobID == "" {
		return nil, fmt.Errorf("batch job id is required")
	}
	var job tables.TableBatchJob
	if err := s.DB().WithContext(ctx).Where("id = ?", jobID).First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &job, nil
}

// ListDueBatchJobs returns non-terminal batch jobs whose next poll is due at or
// before now, oldest-due first. An empty provider matches every provider.
func (s *RDBConfigStore) ListDueBatchJobs(ctx context.Context, provider string, now time.Time, limit int) ([]*tables.TableBatchJob, error) {
	if limit <= 0 {
		limit = 100
	}
	query := s.DB().WithContext(ctx).
		Where("accounting_status NOT IN ?", []string{tables.BatchJobAccountingStatusAccounted, tables.BatchJobAccountingStatusUnpriceable}).
		Where("next_check_at IS NOT NULL AND next_check_at <= ?", now).
		Order("next_check_at ASC").
		Limit(limit)
	if provider != "" {
		query = query.Where("provider = ?", provider)
	}

	var jobs []*tables.TableBatchJob
	if err := query.Find(&jobs).Error; err != nil {
		return nil, err
	}
	return jobs, nil
}

// ClaimBatchJob transitions a claimable batch job to "processing" under runnerID
// and returns true on success. A job is claimable when it is not already in a
// terminal accounting state and is either not currently processing or has gone
// stale (updated_at older than staleBefore). Modeled on ClaimSidekiqJob.
func (s *RDBConfigStore) ClaimBatchJob(ctx context.Context, jobID, runnerID string, staleBefore time.Time) (bool, error) {
	if jobID == "" {
		return false, fmt.Errorf("batch job id is required")
	}
	now := time.Now().UTC()
	res := s.DB().WithContext(ctx).Model(&tables.TableBatchJob{}).
		Where("id = ?", jobID).
		Where("accounting_status NOT IN ?", []string{tables.BatchJobAccountingStatusAccounted, tables.BatchJobAccountingStatusUnpriceable}).
		Where("(accounting_status <> ? OR updated_at < ?)", tables.BatchJobAccountingStatusProcessing, staleBefore).
		Updates(map[string]interface{}{
			"accounting_status": tables.BatchJobAccountingStatusProcessing,
			"runner_id":         runnerID,
			"last_error":        nil,
			"updated_at":        now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// markBatchJobTimestamp sets a single lifecycle timestamp column on a job this
// runner still owns and is still processing, clearing any prior error. Fenced on
// runner_id so a former owner cannot advance a re-claimed job.
func (s *RDBConfigStore) markBatchJobTimestamp(ctx context.Context, jobID, runnerID, column string) error {
	if jobID == "" {
		return fmt.Errorf("batch job id is required")
	}
	now := time.Now().UTC()
	res := s.DB().WithContext(ctx).Model(&tables.TableBatchJob{}).
		Where("id = ? AND runner_id = ? AND accounting_status = ?", jobID, runnerID, tables.BatchJobAccountingStatusProcessing).
		Updates(map[string]interface{}{
			column:       now,
			"last_error": nil,
			"updated_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkBatchJobAggregateLogWritten records that the durable aggregate cost log row
// has been created for this job (settlement idempotency marker).
func (s *RDBConfigStore) MarkBatchJobAggregateLogWritten(ctx context.Context, jobID, runnerID string) error {
	return s.markBatchJobTimestamp(ctx, jobID, runnerID, "aggregate_log_written_at")
}

// MarkBatchJobGovernanceReported records that batch usage has been reported to
// governance for this job (settlement idempotency marker).
func (s *RDBConfigStore) MarkBatchJobGovernanceReported(ctx context.Context, jobID, runnerID string) error {
	return s.markBatchJobTimestamp(ctx, jobID, runnerID, "governance_reported_at")
}

// CompleteBatchJob marks a claimed job accounted and releases the runner fence.
func (s *RDBConfigStore) CompleteBatchJob(ctx context.Context, jobID, runnerID string) error {
	return s.finishBatchJob(ctx, jobID, runnerID, tables.BatchJobAccountingStatusAccounted, "", nil)
}

// MarkBatchJobUnpriceable marks a claimed job terminal-unpriceable with a reason.
func (s *RDBConfigStore) MarkBatchJobUnpriceable(ctx context.Context, jobID, runnerID, reason string, err error) error {
	return s.finishBatchJob(ctx, jobID, runnerID, tables.BatchJobAccountingStatusUnpriceable, reason, err)
}

// FailBatchJob releases the runner fence after an accounting failure so a later
// /results call or reconciler pass can retry the job.
func (s *RDBConfigStore) FailBatchJob(ctx context.Context, jobID, runnerID string, err error) error {
	return s.finishBatchJob(ctx, jobID, runnerID, tables.BatchJobAccountingStatusError, "", err)
}

func (s *RDBConfigStore) finishBatchJob(ctx context.Context, jobID, runnerID, status, reason string, err error) error {
	if jobID == "" {
		return fmt.Errorf("batch job id is required")
	}
	var lastError any
	if err != nil {
		lastError = err.Error()
	}
	now := time.Now().UTC()
	updates := map[string]interface{}{
		"accounting_status": status,
		"runner_id":         nil,
		"last_error":        lastError,
		"updated_at":        now,
	}
	if status == tables.BatchJobAccountingStatusAccounted {
		updates["unpriceable_reason"] = nil
	}
	if reason != "" {
		updates["unpriceable_reason"] = reason
	}
	res := s.DB().WithContext(ctx).Model(&tables.TableBatchJob{}).
		Where("id = ? AND runner_id = ? AND accounting_status = ?", jobID, runnerID, tables.BatchJobAccountingStatusProcessing).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
