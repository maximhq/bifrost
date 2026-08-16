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

// ClaimSidekiqJob atomically claims a job for runnerID and transitions it to
// running. It is the cluster-wide mutual-exclusion primitive: the conditional
// UPDATE means at most one claim affects a row, so exactly one node — and exactly
// one goroutine — runs each job. A claim succeeds when the job is:
//   - pending (never started), or
//   - running but stale (updated_at < staleBefore, i.e. the owner's heartbeat
//     lapsed, so it is presumed dead and the job is orphaned/resumable).
//
// A job running under a live owner (fresh heartbeat) yields RowsAffected == 0, so
// it is not claimed. Note there is deliberately no "runner_id = runnerID" escape:
// resume after a crash is covered by the stale condition (a restarted process has
// a new runnerID anyway), and omitting it means a second concurrent claim on the
// same node (e.g. Enqueue racing a dispatcher tick) loses instead of double-running.
// The claim stamps runner_id, bumps the heartbeat, and increments the attempt
// counter (each claimed run is a fresh attempt). started_at is only set on first
// start; a resume keeps the original. Returns true when this claim won.
//
// In OSS (single-node) mode runnerID is empty and staleBefore is time.Now(), so
// any running job (e.g. from a crashed previous process) is immediately claimable.
func (s *RDBConfigStore) ClaimSidekiqJob(ctx context.Context, id, runnerID string, staleBefore time.Time) (bool, error) {
	now := time.Now()
	res := s.DB().WithContext(ctx).
		Model(&tables.TableSidekiqJob{}).
		Where("id = ? AND (status = ? OR (status = ? AND updated_at < ?))",
			id,
			tables.SidekiqStatusPending,
			tables.SidekiqStatusRunning, staleBefore).
		Updates(map[string]any{
			"status":     tables.SidekiqStatusRunning,
			"runner_id":  runnerID,
			"started_at": gorm.Expr("COALESCE(started_at, ?)", now),
			"updated_at": now,
			"attempts":   gorm.Expr("attempts + 1"),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// ClaimPartitionedSidekiqJob is ClaimSidekiqJob for jobs carrying a partitioning key.
// On top of the base claimable condition it enforces, atomically, that the job is
// next in line for its key:
//   - G1: no OTHER job with the same key is running under a live owner
//     (updated_at >= staleBefore, i.e. not itself stale), and
//   - G2: no OLDER pending job with the same key exists (FIFO by created_at, id).
//
// Together these give at-most-one-running plus FIFO execution per key, cluster-wide,
// while distinct keys stay parallel. A blocked job yields RowsAffected == 0 (not
// claimed) and is retried on the next dispatch tick. A stale (dead-owner) running
// job does not block its key, so a crashed node cannot deadlock the queue.
func (s *RDBConfigStore) ClaimPartitionedSidekiqJob(ctx context.Context, id, runnerID string, staleBefore time.Time, partitioningKey string, createdAt time.Time) (bool, error) {
	now := time.Now()
	res := s.DB().WithContext(ctx).
		Model(&tables.TableSidekiqJob{}).
		Where("id = ? AND (status = ? OR (status = ? AND updated_at < ?))",
			id,
			tables.SidekiqStatusPending,
			tables.SidekiqStatusRunning, staleBefore).
		// G1: mutual exclusion — no other same-key job running under a live owner.
		Where("NOT EXISTS (SELECT 1 FROM sidekiq r WHERE r.partitioning_key = ? AND r.id <> ? AND r.status = ? AND r.updated_at >= ?)",
			partitioningKey, id, tables.SidekiqStatusRunning, staleBefore).
		// G2: FIFO — no OLDER pending job for the same key. Excludes the row itself
		// (o.id <> id): the claim's createdAt (in-memory, nanosecond) can exceed the
		// truncated persisted created_at, which would else block the job on itself.
		Where("NOT EXISTS (SELECT 1 FROM sidekiq o WHERE o.partitioning_key = ? AND o.id <> ? AND o.status = ? AND (o.created_at < ? OR (o.created_at = ? AND o.id < ?)))",
			partitioningKey, id, tables.SidekiqStatusPending, createdAt, createdAt, id).
		Updates(map[string]any{
			"status":     tables.SidekiqStatusRunning,
			"runner_id":  runnerID,
			"started_at": gorm.Expr("COALESCE(started_at, ?)", now),
			"updated_at": now,
			"attempts":   gorm.Expr("attempts + 1"),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// HeartbeatSidekiqJob bumps the heartbeat (updated_at) for a job the caller still
// owns and is still running. Called on a fixed interval by the owning runner so a
// slow-but-alive job (one whose handler has not checkpointed recently) is not
// judged stale and re-claimed elsewhere. Fenced on runner_id: returns false when
// the caller no longer owns the job (it was reaped and re-claimed), which the
// runner treats as a signal to cancel its in-flight work.
func (s *RDBConfigStore) HeartbeatSidekiqJob(ctx context.Context, id, runnerID string) (bool, error) {
	res := s.DB().WithContext(ctx).
		Model(&tables.TableSidekiqJob{}).
		Where("id = ? AND runner_id = ? AND status = ?", id, runnerID, tables.SidekiqStatusRunning).
		Update("updated_at", time.Now())
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// UpdateSidekiqJobProgress persists a progress checkpoint: it replaces the metadata
// blob and bumps the heartbeat (updated_at) so the reaper does not treat the job as
// stale. Called after each processed page. Fenced on runner_id so only the current
// owner can advance the job; a stale runner that revives affects 0 rows.
func (s *RDBConfigStore) UpdateSidekiqJobProgress(ctx context.Context, id, runnerID, metadata string) error {
	res := s.DB().WithContext(ctx).
		Model(&tables.TableSidekiqJob{}).
		Where("id = ? AND runner_id = ? AND status = ?", id, runnerID, tables.SidekiqStatusRunning).
		Updates(map[string]any{
			"metadata":   metadata,
			"updated_at": time.Now(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("sidekiq job not found or no longer owned by caller")
	}
	return nil
}

// CompleteSidekiqJob marks a job completed, stamps completed_at, and stores the
// final metadata (counts, summary). Fenced on runner_id AND status = running so a
// job that was reaped and re-claimed elsewhere is not marked complete by its former
// runner, and — critically — so a job the reaper already flipped to failed (because
// this runner ran past the stale threshold) is not silently resurrected to completed,
// which would mask the staleness signal.
func (s *RDBConfigStore) CompleteSidekiqJob(ctx context.Context, id, runnerID, metadata string) error {
	now := time.Now()
	res := s.DB().WithContext(ctx).
		Model(&tables.TableSidekiqJob{}).
		Where("id = ? AND runner_id = ? AND status = ?", id, runnerID, tables.SidekiqStatusRunning).
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
		return errors.New("sidekiq job not found, no longer owned by caller, or no longer running")
	}
	return nil
}

// FailSidekiqJob marks a job failed, records the error, stamps completed_at, and
// preserves the latest metadata so a later resume can read the checkpoint cursor.
// Fenced on runner_id AND status = running so a former runner cannot overwrite a
// re-claimed job's state, and so the execute/panic paths cannot overwrite the
// last_error the reaper already wrote when it failed this job for going stale.
func (s *RDBConfigStore) FailSidekiqJob(ctx context.Context, id, runnerID, metadata, lastErr string) error {
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
		Where("id = ? AND runner_id = ? AND status = ?", id, runnerID, tables.SidekiqStatusRunning).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("sidekiq job not found, no longer owned by caller, or no longer running")
	}
	return nil
}

// CancelSidekiqJob flips a pending or running job to cancelled and stamps
// completed_at. It is the durable half of a cancel request: the row immediately
// stops looking in-flight, so it is never re-claimed by the dispatcher and never
// reaped as stale, even if the node currently running it dies before noticing.
//
// It is deliberately NOT fenced on runner_id — a cancel arrives on whichever node
// serves the HTTP request, which is often not the one that owns the job. The
// owning node learns about it either from its in-process cancel func (same node)
// or from its next heartbeat, which is fenced on status = running and so returns
// "ownership lost" once the status has moved to cancelled.
//
// Returns true when this call was the one that cancelled the job; false means the
// job did not exist or had already reached a terminal status, which callers treat
// as a no-op rather than an error.
func (s *RDBConfigStore) CancelSidekiqJob(ctx context.Context, id string) (bool, error) {
	now := time.Now()
	res := s.DB().WithContext(ctx).
		Model(&tables.TableSidekiqJob{}).
		Where("id = ? AND status IN ?", id, []string{tables.SidekiqStatusPending, tables.SidekiqStatusRunning}).
		Updates(map[string]any{
			"status":       tables.SidekiqStatusCancelled,
			"updated_at":   now,
			"completed_at": now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// FinalizeCancelledSidekiqJob stores the last metadata snapshot of a job that was
// cancelled mid-run, so the partial progress counters (and the handler's summary
// message) survive for the UI. It only touches the metadata: the status,
// completed_at and last_error written by CancelSidekiqJob stand.
//
// Fenced on runner_id and status = cancelled so a stale runner cannot resurrect
// counters onto a job that has since been re-claimed, and so a handler unwinding
// for some other reason cannot mistakenly write here. A zero-row result is not an
// error — the job may have been finalized already.
func (s *RDBConfigStore) FinalizeCancelledSidekiqJob(ctx context.Context, id, runnerID, metadata string) error {
	if metadata == "" {
		return nil
	}
	return s.DB().WithContext(ctx).
		Model(&tables.TableSidekiqJob{}).
		Where("id = ? AND runner_id = ? AND status = ?", id, runnerID, tables.SidekiqStatusCancelled).
		Updates(map[string]any{
			"metadata":   metadata,
			"updated_at": time.Now(),
		}).Error
}

// ListClaimableSidekiqJobs returns jobs eligible to be picked up: those that are
// pending (never started), or running but stale (heartbeat older than staleBefore,
// i.e. their owner is presumed dead). Ordered oldest-first. The dispatcher scans
// this list and attempts to claim each; the atomic ClaimSidekiqJob decides the one
// winner, so listing on every node is safe and needs no cross-node coordination.
func (s *RDBConfigStore) ListClaimableSidekiqJobs(ctx context.Context, staleBefore time.Time) ([]tables.TableSidekiqJob, error) {
	var jobs []tables.TableSidekiqJob
	err := s.DB().WithContext(ctx).
		Where("status = ? OR (status = ? AND updated_at < ?)",
			tables.SidekiqStatusPending,
			tables.SidekiqStatusRunning, staleBefore).
		Order("created_at ASC").
		Find(&jobs).Error
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

// GetInFlightSidekiqJobByKind returns the most recently created job of the given
// kind that is still pending or running, or nil when none is in flight. Callers
// use it to avoid enqueuing a duplicate while one of the same kind is active.
func (s *RDBConfigStore) GetInFlightSidekiqJobByKind(ctx context.Context, kind string) (*tables.TableSidekiqJob, error) {
	var job tables.TableSidekiqJob
	err := s.DB().WithContext(ctx).
		Where("kind = ? AND status IN ?", kind, []string{tables.SidekiqStatusPending, tables.SidekiqStatusRunning}).
		Order("created_at DESC").
		First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
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
