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

// UpsertProviderJob inserts a new provider job or merges non-zero fields into an
// existing one. The identity row (id) is never overwritten; only the mutable
// lifecycle/hint columns are advanced. Terminal-but-not-completed provider states
// clear next_check_at so the sweeper stops polling.
func (s *RDBConfigStore) UpsertProviderJob(ctx context.Context, job *tables.TableProviderJob) error {
	if job == nil || job.ID == "" {
		return fmt.Errorf("provider job and id are required")
	}
	now := time.Now().UTC()
	if job.Kind == "" {
		job.Kind = tables.ProviderJobKindBatch
	}
	if job.AccountingStatus == "" {
		job.AccountingStatus = tables.ProviderJobAccountingStatusPending
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

	updates := map[string]any{"updated_at": now}
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

	return db.Model(&tables.TableProviderJob{}).Where("id = ?", job.ID).Updates(updates).Error
}

// GetProviderJob returns a provider job by its stable id, or ErrNotFound.
func (s *RDBConfigStore) GetProviderJob(ctx context.Context, jobID string) (*tables.TableProviderJob, error) {
	if jobID == "" {
		return nil, fmt.Errorf("provider job id is required")
	}
	var job tables.TableProviderJob
	if err := s.DB().WithContext(ctx).Where("id = ?", jobID).First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &job, nil
}

// ListDueProviderJobs returns non-terminal jobs of one kind whose next poll is due
// at or before now, oldest-due first. An empty provider matches every provider.
//
// The kind filter is mandatory, not a convenience: each kind has its own settler
// and its own way of talking to the provider, so a sweeper handed another kind's
// rows would poll them with the wrong client and burn their attempt budget. An
// empty kind means batch, matching rows written before the column existed.
func (s *RDBConfigStore) ListDueProviderJobs(ctx context.Context, kind, provider string, now time.Time, limit int) ([]*tables.TableProviderJob, error) {
	if limit <= 0 {
		limit = 100
	}
	if kind == "" {
		kind = tables.ProviderJobKindBatch
	}
	query := s.DB().WithContext(ctx).
		Where("kind = ?", kind).
		Where("accounting_status NOT IN ?", []string{tables.ProviderJobAccountingStatusAccounted, tables.ProviderJobAccountingStatusUnpriceable}).
		Where("next_check_at IS NOT NULL AND next_check_at <= ?", now).
		Order("next_check_at ASC").
		Limit(limit)
	if provider != "" {
		query = query.Where("provider = ?", provider)
	}

	var jobs []*tables.TableProviderJob
	if err := query.Find(&jobs).Error; err != nil {
		return nil, err
	}
	return jobs, nil
}

// ClaimProviderJob transitions a claimable provider job to "processing" under runnerID
// and returns true on success. A job is claimable when it is not already in a
// terminal accounting state and is either not currently processing or its claim
// has gone stale (claimed_at older than staleBefore).
//
// allowUnpriceable narrows "terminal" to just "accounted". "unpriceable" means the
// job stopped being polled — max attempts, a terminal provider status with nothing
// to fetch, usage with no rate — and every one of those reasons is answered by a
// caller that turns up holding the actual results. Refusing such a caller made the
// state a one-way door that permanently dropped the batch's money, so settlement
// paths with results in hand pass true. The sweeper's poll loop still skips these
// jobs (ListDueProviderJobs excludes them); "accounted" is terminal forever.
//
// Staleness is measured on claimed_at rather than updated_at because updated_at is
// refreshed by UpsertProviderJob, which is unfenced and runs on every sweep — using it
// would mean routine polling perpetually renews a dead runner's claim and the job
// could never be reclaimed. claimed_at is written only here.
func (s *RDBConfigStore) ClaimProviderJob(ctx context.Context, jobID, runnerID string, staleBefore time.Time, allowUnpriceable bool) (bool, error) {
	if jobID == "" {
		return false, fmt.Errorf("provider job id is required")
	}
	if runnerID == "" {
		return false, fmt.Errorf("runner id is required")
	}
	blocked := []string{tables.ProviderJobAccountingStatusAccounted, tables.ProviderJobAccountingStatusUnpriceable}
	if allowUnpriceable {
		blocked = []string{tables.ProviderJobAccountingStatusAccounted}
	}
	now := time.Now().UTC()
	res := s.DB().WithContext(ctx).Model(&tables.TableProviderJob{}).
		Where("id = ?", jobID).
		Where("accounting_status NOT IN ?", blocked).
		Where("(accounting_status <> ? OR claimed_at IS NULL OR claimed_at < ?)", tables.ProviderJobAccountingStatusProcessing, staleBefore).
		Updates(map[string]any{
			"accounting_status": tables.ProviderJobAccountingStatusProcessing,
			"runner_id":         runnerID,
			"claimed_at":        now,
			"last_error":        nil,
			"updated_at":        now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// markProviderJobTimestamp sets a single lifecycle timestamp column on a job this
// runner still owns and is still processing, clearing any prior error. Fenced on
// runner_id so a former owner cannot advance a re-claimed job.
func (s *RDBConfigStore) markProviderJobTimestamp(ctx context.Context, jobID, runnerID, column string) error {
	if jobID == "" {
		return fmt.Errorf("provider job id is required")
	}
	now := time.Now().UTC()
	res := s.DB().WithContext(ctx).Model(&tables.TableProviderJob{}).
		Where("id = ? AND runner_id = ? AND accounting_status = ?", jobID, runnerID, tables.ProviderJobAccountingStatusProcessing).
		Updates(map[string]any{
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

// MarkProviderJobAggregateLogWritten records that the durable aggregate cost log row
// has been created for this job (settlement idempotency marker).
func (s *RDBConfigStore) MarkProviderJobAggregateLogWritten(ctx context.Context, jobID, runnerID string) error {
	return s.markProviderJobTimestamp(ctx, jobID, runnerID, "aggregate_log_written_at")
}

// MarkProviderJobGovernanceReported records that usage has been reported to
// governance for this job (settlement idempotency marker).
func (s *RDBConfigStore) MarkProviderJobGovernanceReported(ctx context.Context, jobID, runnerID string) error {
	return s.markProviderJobTimestamp(ctx, jobID, runnerID, "governance_reported_at")
}

// CompleteProviderJob marks a claimed job accounted and releases the runner fence.
func (s *RDBConfigStore) CompleteProviderJob(ctx context.Context, jobID, runnerID string) error {
	return s.finishProviderJob(ctx, jobID, runnerID, tables.ProviderJobAccountingStatusAccounted, "", nil)
}

// MarkProviderJobUnpriceable marks a claimed job terminal-unpriceable with a reason.
func (s *RDBConfigStore) MarkProviderJobUnpriceable(ctx context.Context, jobID, runnerID, reason string, err error) error {
	return s.finishProviderJob(ctx, jobID, runnerID, tables.ProviderJobAccountingStatusUnpriceable, reason, err)
}

// FailProviderJob releases the runner fence after an accounting failure so a later
// /results call or reconciler pass can retry the job.
func (s *RDBConfigStore) FailProviderJob(ctx context.Context, jobID, runnerID string, err error) error {
	return s.finishProviderJob(ctx, jobID, runnerID, tables.ProviderJobAccountingStatusError, "", err)
}

func (s *RDBConfigStore) finishProviderJob(ctx context.Context, jobID, runnerID, status, reason string, err error) error {
	if jobID == "" {
		return fmt.Errorf("provider job id is required")
	}
	var lastError any
	if err != nil {
		lastError = err.Error()
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"accounting_status": status,
		"runner_id":         nil,
		"claimed_at":        nil,
		"last_error":        lastError,
		"updated_at":        now,
	}
	if status == tables.ProviderJobAccountingStatusAccounted {
		updates["unpriceable_reason"] = nil
	}
	if reason != "" {
		updates["unpriceable_reason"] = reason
	}
	res := s.DB().WithContext(ctx).Model(&tables.TableProviderJob{}).
		Where("id = ? AND runner_id = ? AND accounting_status = ?", jobID, runnerID, tables.ProviderJobAccountingStatusProcessing).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
