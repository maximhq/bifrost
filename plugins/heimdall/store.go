package heimdall

import (
	"context"
	"errors"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// Run statuses.
const (
	StatusQueued      = "queued"
	StatusRunning     = "running"
	StatusComplete    = "complete"
	StatusCancelled   = "cancelled"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
)

// Run stages.
const (
	StagePlanning   = "planning"
	StageReplaying  = "replaying"
	StageEvaluating = "evaluating"
	StageDone       = "done"
)

var (
	errNotFound  = errors.New("not found")
	errRunActive = errors.New("a run is already active")
)

// store reads and writes Heimdall's tables through the config store's connection. It asks for the
// connection on every call and never keeps it: the config store may replace it.
type store struct{ cs configstore.ConfigStore }

func (s *store) db(ctx context.Context) *gorm.DB { return s.cs.DB().WithContext(ctx) }

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errNotFound
	}
	return err
}

// ---- runs ----

// createRunIfIdle inserts a run unless another is queued or running. Check and insert share one transaction.
func (s *store) createRunIfIdle(ctx context.Context, run *tables.TableHeimdallRun) error {
	return s.db(ctx).Transaction(func(tx *gorm.DB) error {
		var active int64
		if err := tx.Model(&tables.TableHeimdallRun{}).Where("status IN ?", []string{StatusQueued, StatusRunning}).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return errRunActive
		}
		return tx.Create(run).Error
	})
}

func (s *store) getRun(ctx context.Context, id string) (*tables.TableHeimdallRun, error) {
	var run tables.TableHeimdallRun
	if err := s.db(ctx).First(&run, "id = ?", id).Error; err != nil {
		return nil, notFound(err)
	}
	return &run, nil
}

// activeRun returns the queued or running run, or nil.
func (s *store) activeRun(ctx context.Context) (*tables.TableHeimdallRun, error) {
	var run tables.TableHeimdallRun
	err := s.db(ctx).Where("status IN ?", []string{StatusQueued, StatusRunning}).Order("created_at").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

// RunQuery selects runs to list.
type RunQuery struct {
	Status  string
	Verdict string
	Limit   int
	Offset  int
}

// listRuns returns runs newest first, and how many match in all.
func (s *store) listRuns(ctx context.Context, q RunQuery) ([]tables.TableHeimdallRun, int64, error) {
	tx := s.db(ctx).Model(&tables.TableHeimdallRun{})
	if q.Status != "" {
		tx = tx.Where("status = ?", q.Status)
	}
	if q.Verdict != "" {
		tx = tx.Where("verdict = ?", q.Verdict)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var runs []tables.TableHeimdallRun
	err := tx.Order("created_at DESC, id").Limit(q.Limit).Offset(q.Offset).Find(&runs).Error
	return runs, total, err
}

// updateRun sets columns of a run. It reports errNotFound when there is no such run.
func (s *store) updateRun(ctx context.Context, id string, fields map[string]any) error {
	res := s.db(ctx).Model(&tables.TableHeimdallRun{}).Where("id = ?", id).Updates(fields)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errNotFound
	}
	return nil
}

// markStaleRuns ends runs left queued or running by a process that is gone. There is no resume.
func (s *store) markStaleRuns(ctx context.Context, now time.Time) (int64, error) {
	res := s.db(ctx).Model(&tables.TableHeimdallRun{}).Where("status IN ?", []string{StatusQueued, StatusRunning}).
		Updates(map[string]any{"status": StatusInterrupted, "finished_at": now, "error_code": "interrupted", "error_message": "the process stopped before the run finished"})
	return res.RowsAffected, res.Error
}

// ---- run sessions ----

func (s *store) createRunSessions(ctx context.Context, rows []tables.TableHeimdallRunSession) error {
	if len(rows) == 0 {
		return nil
	}
	return s.db(ctx).CreateInBatches(&rows, 50).Error
}

func (s *store) updateRunSession(ctx context.Context, runID, sessionID string, fields map[string]any) error {
	res := s.db(ctx).Model(&tables.TableHeimdallRunSession{}).Where("run_id = ? AND session_id = ?", runID, sessionID).Updates(fields)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errNotFound
	}
	return nil
}

// Run session filters.
const (
	FilterAll        = "all"
	FilterCritical   = "critical"   // a critical finding counts against the candidate or waits for evidence
	FilterDiverged   = "diverged"   // the candidate departs from the recording somewhere
	FilterIneligible = "ineligible" // the session could not be replayed
)

// RunSessionQuery selects the sessions of a run.
type RunSessionQuery struct {
	Filter string
	Limit  int
	Offset int
}

// listRunSessions returns a run's sessions without their snapshots, and how many match in all.
func (s *store) listRunSessions(ctx context.Context, runID string, q RunSessionQuery) ([]tables.TableHeimdallRunSession, int64, error) {
	tx := s.db(ctx).Model(&tables.TableHeimdallRunSession{}).Where("run_id = ?", runID)
	switch q.Filter {
	case FilterCritical:
		tx = tx.Where("critical_count > 0")
	case FilterDiverged:
		tx = tx.Where("first_divergence >= 0")
	case FilterIneligible:
		tx = tx.Where("eligibility <> ?", CodeEligible)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []tables.TableHeimdallRunSession
	err := tx.Omit("snapshot_json").Order("critical_count DESC, session_id").Limit(q.Limit).Offset(q.Offset).Find(&rows).Error
	return rows, total, err
}

func (s *store) getRunSession(ctx context.Context, runID, sessionID string) (*tables.TableHeimdallRunSession, error) {
	var row tables.TableHeimdallRunSession
	if err := s.db(ctx).First(&row, "run_id = ? AND session_id = ?", runID, sessionID).Error; err != nil {
		return nil, notFound(err)
	}
	return &row, nil
}

// ---- replays ----

func (s *store) createReplay(ctx context.Context, row *tables.TableHeimdallReplay) error {
	return s.db(ctx).Create(row).Error
}

func (s *store) updateReplay(ctx context.Context, id string, fields map[string]any) error {
	return s.db(ctx).Model(&tables.TableHeimdallReplay{}).Where("id = ?", id).Updates(fields).Error
}

// listReplays returns a run's replays, those of one session when sessionID is set, in the order they ran.
func (s *store) listReplays(ctx context.Context, runID, sessionID string) ([]tables.TableHeimdallReplay, error) {
	tx := s.db(ctx).Where("run_id = ?", runID)
	if sessionID != "" {
		tx = tx.Where("session_id = ?", sessionID)
	}
	var rows []tables.TableHeimdallReplay
	err := tx.Order("created_at, arm, attempt").Find(&rows).Error
	return rows, err
}

// ---- retention ----

// expireContent removes what a run holds of the original conversations from runs created before cutoff: the
// sessions' snapshots and the replays' actions, which hold prompts, tool arguments and model text. Scores,
// comparisons and verdicts stay. It returns how many snapshots and replays were cleared.
func (s *store) expireContent(ctx context.Context, cutoff time.Time) (snapshots, replays int64, err error) {
	old := s.db(ctx).Model(&tables.TableHeimdallRun{}).Select("id").Where("created_at < ?", cutoff)
	res := s.db(ctx).Model(&tables.TableHeimdallRunSession{}).
		Where("run_id IN (?) AND snapshot_json IS NOT NULL", old).Update("snapshot_json", nil)
	if res.Error != nil {
		return 0, 0, res.Error
	}
	snapshots = res.RowsAffected
	res = s.db(ctx).Model(&tables.TableHeimdallReplay{}).
		Where("run_id IN (?) AND actions_json IS NOT NULL", old).Update("actions_json", nil)
	return snapshots, res.RowsAffected, res.Error
}
