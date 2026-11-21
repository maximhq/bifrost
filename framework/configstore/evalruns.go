package configstore

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// Errors returned when a dataset is protected by the test runs that read it.
var (
	ErrEvalDatasetVersionInUse = errors.New("a test run reads this dataset version; delete the run first")
	ErrEvalDatasetHasRuns      = errors.New("test runs read this dataset; delete the runs first")
	// ErrEvalRunActive is returned when a run that is queued or running is deleted.
	ErrEvalRunActive = errors.New("the run is still active; stop it before deleting it")
)

// evalRunLive selects runs that have not been deleted.
const evalRunLive = "evals_runs.deleted_at_unix_nano = 0"

// EvalRunListParams filters and pages a run listing.
type EvalRunListParams struct {
	Search    string // case-insensitive substring of the name
	Status    string
	DatasetID string
	Tag       string // exact tag
	Limit     int    // 0 means no limit
	Offset    int
}

// EvalRunEntryListParams selects a page of a run's entries, in sequence order.
type EvalRunEntryListParams struct {
	RunID    string
	Statuses []string // any of these; empty means every status
	Passed   *bool    // only entries that passed, or only those that did not
	Search   string   // case-insensitive substring of the variables or the output
	// QueuedBefore selects entries queued before this time. It is how the sweep finds
	// entries whose queue message was lost. RunID may be empty with it.
	QueuedBefore time.Time
	IDs          []string
	// DatasetRowIDs selects the entries that came from these dataset rows.
	DatasetRowIDs []string
	// KeyID selects entries waiting on exactly this eval key; "" selects the ones
	// that were never routed to a key. Nil does not filter.
	KeyID *string
	// ExcludeKeyIDs leaves out entries waiting on any of these keys, unless they were
	// routed there before RoutedBefore: their message may have been lost.
	ExcludeKeyIDs []string
	RoutedBefore  time.Time
	// StartedBefore selects entries started before this time.
	StartedBefore time.Time
	// TargetTypes keeps only entries of runs with one of these target types. The sweep
	// uses it to look only at runs whose target calls a model.
	TargetTypes []string
	// BaselineRunID and Change together select the entries whose score moved against
	// another run: Change is EvalRunChangeWorse or EvalRunChangeBetter. An entry is
	// matched to the baseline's entry for the same dataset row, and counts when any
	// evaluator both runs have a numeric score for scored it lower, or higher. They
	// need RunID. Without Change the baseline does not narrow anything.
	BaselineRunID string
	Change        string
	// AfterSeq selects entries after this position, for paging without an offset.
	AfterSeq int64
	Offset   int
	Limit    int // 0 means no limit
}

// How a run's entry scored against the baseline's entry for the same dataset row.
const (
	EvalRunChangeWorse  = "worse"
	EvalRunChangeBetter = "better"
)

// EvalRunState is the part of a run that changes while it runs. A job reads it in
// place of the whole run, whose copied target and evaluators can be large.
type EvalRunState struct {
	ID        string
	Status    string
	UpdatedAt time.Time
	StartedAt *time.Time
	EndedAt   *time.Time
}

// EvalRunEntryRef is an entry without its variables and output: what is needed to
// queue it, move it, or count it.
type EvalRunEntryRef struct {
	ID        string
	RunID     string
	Seq       int64
	Status    string
	Attempt   int
	KeyID     string
	RoutedAt  *time.Time
	ClaimID   string
	StartedAt *time.Time
}

// EvalRunResultRef is a result without its reasoning and usage: what is needed to
// queue it, to tell whether its entry is finished, and to summarise a run.
type EvalRunResultRef struct {
	ID          string
	RunID       string
	EntryID     string
	EvaluatorID string
	Status      string
	Pass        *bool
	ScoreJSON   *string
	Attempt     int
	KeyID       string
	RoutedAt    *time.Time
	ClaimID     string
	StartedAt   *time.Time
}

// EvalRunEvaluatorCount is how many results of one evaluator share a status and a
// pass value. A run's live progress is built from these.
type EvalRunEvaluatorCount struct {
	EvaluatorID string
	Status      string
	Pass        *bool
	Count       int64
}

// EvalRunResultListParams selects results of a run, in ID order.
type EvalRunResultListParams struct {
	RunID        string
	EntryIDs     []string
	EvaluatorID  string
	Statuses     []string
	QueuedBefore time.Time // see EvalRunEntryListParams.QueuedBefore
	// KeyID, ExcludeKeyIDs and RoutedBefore filter on the eval key a result waits on.
	// See EvalRunEntryListParams.
	KeyID         *string
	ExcludeKeyIDs []string
	RoutedBefore  time.Time
	StartedBefore time.Time // results started before this time
	// NeedsModel keeps only results whose evaluator calls a model, before the limit.
	NeedsModel bool
	AfterID    string // results after this ID, for paging
	Limit      int    // 0 means no limit
}

// EvalRunTotals are the tokens and cost a run has spent so far.
type EvalRunTotals struct {
	TargetTokens    int64   `json:"target_tokens"`
	TargetCost      float64 `json:"target_cost"`
	EvaluatorTokens int64   `json:"evaluator_tokens"`
	EvaluatorCost   float64 `json:"evaluator_cost"`
}

// ============================================================================
// Runs
// ============================================================================

// CreateEvalRun stores a new run.
func (s *RDBConfigStore) CreateEvalRun(ctx context.Context, run *tables.TableEvalRun) error {
	if run == nil {
		return errors.New("run is required")
	}
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	run.DeletedAt = 0
	return s.DB().WithContext(ctx).Create(run).Error
}

// GetEvalRun returns a live run, or ErrNotFound.
func (s *RDBConfigStore) GetEvalRun(ctx context.Context, id string) (*tables.TableEvalRun, error) {
	var run tables.TableEvalRun
	if err := s.ScopedDB(ctx).Where("id = ?", id).Where(evalRunLive).First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &run, nil
}

// GetEvalRunState returns the status and timestamps of a live run, or ErrNotFound.
func (s *RDBConfigStore) GetEvalRunState(ctx context.Context, id string) (*EvalRunState, error) {
	var states []EvalRunState
	if err := s.ScopedDB(ctx).Model(&tables.TableEvalRun{}).Select("id, status, updated_at, started_at, ended_at").
		Where("id = ?", id).Where(evalRunLive).Limit(1).Scan(&states).Error; err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, ErrNotFound
	}
	return &states[0], nil
}

// ListEvalRuns returns live runs, newest first, with the total before paging.
func (s *RDBConfigStore) ListEvalRuns(ctx context.Context, params EvalRunListParams) ([]tables.TableEvalRun, int64, error) {
	query := s.ScopedDB(ctx).Model(&tables.TableEvalRun{}).Where(evalRunLive)
	if search := strings.TrimSpace(params.Search); search != "" {
		query = query.Where(`LOWER(evals_runs.name) LIKE ? ESCAPE '\'`, "%"+likeEscaper.Replace(strings.ToLower(search))+"%")
	}
	if params.Status != "" {
		query = query.Where("evals_runs.status = ?", params.Status)
	}
	if params.DatasetID != "" {
		query = query.Where("evals_runs.dataset_id = ?", params.DatasetID)
	}
	if params.Tag != "" {
		// Compared as whole array elements, as ListEvalDatasets does, so a tag holding the
		// wanted one in its JSON text, or the same letters in another case, does not match.
		if query.Dialector.Name() == "postgres" {
			query = query.Where(`EXISTS (SELECT 1 FROM jsonb_array_elements_text(evals_runs.tags_json::jsonb) AS tag(value) WHERE tag.value = ?)`, params.Tag)
		} else {
			query = query.Where(`EXISTS (SELECT 1 FROM json_each(evals_runs.tags_json) AS tag WHERE tag.value = ?)`, params.Tag)
		}
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	query = query.Order("evals_runs.created_at DESC, evals_runs.id DESC")
	if params.Limit > 0 {
		query = query.Limit(params.Limit)
	}
	if params.Offset > 0 {
		query = query.Offset(params.Offset)
	}
	var runs []tables.TableEvalRun
	if err := query.Find(&runs).Error; err != nil {
		return nil, 0, err
	}
	return runs, total, nil
}

// TransitionEvalRun writes fields to a run, but only while its status is one of from
// (any status when from is empty). It reports whether it wrote. Moving a run out of a
// status this way is a claim: of several callers, exactly one succeeds.
func (s *RDBConfigStore) TransitionEvalRun(ctx context.Context, id string, from []string, fields map[string]any) (bool, error) {
	query := s.DB().WithContext(ctx).Model(&tables.TableEvalRun{}).Where("id = ? AND deleted_at_unix_nano = 0", id)
	if len(from) > 0 {
		query = query.Where("status IN ?", from)
	}
	// The write skips the model's hooks, so updated_at is set here. Every write to a
	// run moves it, unless the caller names its own value: a job tells by this
	// timestamp that the run it works on has changed.
	if _, named := fields["updated_at"]; !named {
		fields = maps.Clone(fields)
		if fields == nil {
			fields = map[string]any{}
		}
		fields["updated_at"] = time.Now()
	}
	return evalRunWrite(query, fields)
}

// TransitionEvalRunIfUnchanged is TransitionEvalRun for a write based on an earlier read
// of the run: it writes only while the run's updated_at is still updatedAt, which every
// write to a run moves. A run finalized from a stale read is then left alone.
func (s *RDBConfigStore) TransitionEvalRunIfUnchanged(ctx context.Context, id string, from []string, updatedAt time.Time, fields map[string]any) (bool, error) {
	query := s.DB().WithContext(ctx).Model(&tables.TableEvalRun{}).Where("id = ? AND deleted_at_unix_nano = 0 AND updated_at = ?", id, updatedAt)
	if len(from) > 0 {
		query = query.Where("status IN ?", from)
	}
	if _, named := fields["updated_at"]; !named {
		fields = maps.Clone(fields)
		if fields == nil {
			fields = map[string]any{}
		}
		fields["updated_at"] = time.Now()
	}
	return evalRunWrite(query, fields)
}

// evalRunWrite applies fields as they are and reports whether a row matched.
func evalRunWrite(query *gorm.DB, fields map[string]any) (bool, error) {
	result := query.UpdateColumns(fields)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// DeleteEvalRun soft-deletes a run that is not active and removes its entries and
// results. It returns ErrEvalRunActive for a queued or running run: the status check is
// part of the tombstone write, so a run that another request has just claimed is kept.
// The tombstone write takes the run's row lock, which CreateEvalRunEntries and
// CreateEvalRunResults also take, so a write of a run's rows either commits before the
// delete and is removed with it, or sees the run gone.
func (s *RDBConfigStore) DeleteEvalRun(ctx context.Context, id string) error {
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		result := tx.Model(&tables.TableEvalRun{}).
			Where("id = ? AND deleted_at_unix_nano = 0 AND status NOT IN ?", id, []string{tables.EvalRunQueued, tables.EvalRunRunning}).
			UpdateColumns(map[string]any{"deleted_at_unix_nano": now.UnixNano(), "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var live int64
			if err := tx.Model(&tables.TableEvalRun{}).Where("id = ? AND deleted_at_unix_nano = 0", id).Count(&live).Error; err != nil {
				return err
			}
			if live > 0 {
				return ErrEvalRunActive
			}
			return ErrNotFound
		}
		if err := tx.Where("run_id = ?", id).Delete(&tables.TableEvalRunResult{}).Error; err != nil {
			return err
		}
		return tx.Where("run_id = ?", id).Delete(&tables.TableEvalRunEntry{}).Error
	})
}

// CountEvalRunsOnDataset counts live runs that read a dataset: one version of it, or
// any version when version is 0.
func (s *RDBConfigStore) CountEvalRunsOnDataset(ctx context.Context, datasetID string, version int) (int64, error) {
	query := s.DB().WithContext(ctx).Model(&tables.TableEvalRun{}).Where("dataset_id = ?", datasetID).Where(evalRunLive)
	if version > 0 {
		query = query.Where("dataset_version = ?", version)
	}
	var count int64
	err := query.Count(&count).Error
	return count, err
}

// evalRunDatasetVersions lists the versions of a dataset that live runs read.
func evalRunDatasetVersions(tx *gorm.DB, datasetID string) ([]int, error) {
	var versions []int
	err := tx.Model(&tables.TableEvalRun{}).Where("dataset_id = ?", datasetID).Where(evalRunLive).
		Distinct().Pluck("dataset_version", &versions).Error
	return versions, err
}

// ============================================================================
// Entries
// ============================================================================

// lockLiveEvalRun takes the row lock of a live run for the rest of tx, and returns
// ErrNotFound when the run is deleted. updated_at is not written: jobs read it as the
// signal that the run changed. SQLite write transactions already serialize.
func lockLiveEvalRun(tx *gorm.DB, runID string) error {
	var ids []string
	if err := withRowLock(tx, "UPDATE").Model(&tables.TableEvalRun{}).
		Where("id = ? AND deleted_at_unix_nano = 0", runID).Limit(1).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return ErrNotFound
	}
	return nil
}

// lockLiveEvalRuns locks every run of a batch of rows, in ID order so two writers never
// wait on each other in opposite orders, and returns ErrNotFound when one is deleted.
func lockLiveEvalRuns[T any](tx *gorm.DB, rows []T, runOf func(*T) string) error {
	runIDs := make([]string, 0, 1)
	for i := range rows {
		if runID := runOf(&rows[i]); !slices.Contains(runIDs, runID) {
			runIDs = append(runIDs, runID)
		}
	}
	slices.Sort(runIDs)
	for _, runID := range runIDs {
		if err := lockLiveEvalRun(tx, runID); err != nil {
			return err
		}
	}
	return nil
}

// CreateEvalRunEntries stores entries, in batches. It runs under the row lock of each
// run the entries belong to and returns ErrNotFound, storing nothing, when one was
// deleted: see DeleteEvalRun.
func (s *RDBConfigStore) CreateEvalRunEntries(ctx context.Context, entries []tables.TableEvalRunEntry) error {
	if len(entries) == 0 {
		return nil
	}
	now := time.Now()
	for i := range entries {
		if entries[i].ID == "" {
			entries[i].ID = uuid.NewString()
		}
		if entries[i].QueuedAt.IsZero() {
			entries[i].QueuedAt = now
		}
	}
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockLiveEvalRuns(tx, entries, func(e *tables.TableEvalRunEntry) string { return e.RunID }); err != nil {
			return err
		}
		return tx.CreateInBatches(&entries, evalDatasetBatch).Error
	})
}

func evalRunEntryQuery(db *gorm.DB, params EvalRunEntryListParams) *gorm.DB {
	query := db.Model(&tables.TableEvalRunEntry{})
	if params.RunID != "" {
		query = query.Where("run_id = ?", params.RunID)
	}
	if len(params.Statuses) > 0 {
		query = query.Where("status IN ?", params.Statuses)
	}
	if params.Passed != nil {
		query = query.Where("passed = ?", *params.Passed)
	}
	if len(params.IDs) > 0 {
		query = query.Where("id IN ?", params.IDs)
	}
	if len(params.DatasetRowIDs) > 0 {
		query = query.Where("dataset_row_id IN ?", params.DatasetRowIDs)
	}
	if !params.QueuedBefore.IsZero() {
		query = query.Where("queued_at < ?", params.QueuedBefore)
	}
	if params.KeyID != nil {
		query = query.Where("key_id = ?", *params.KeyID)
	}
	if len(params.ExcludeKeyIDs) > 0 {
		query = query.Where("key_id NOT IN ? OR routed_at < ?", params.ExcludeKeyIDs, params.RoutedBefore)
	}
	if !params.StartedBefore.IsZero() {
		query = query.Where("started_at < ?", params.StartedBefore)
	}
	if len(params.TargetTypes) > 0 {
		query = query.Where("run_id IN (?)", db.Session(&gorm.Session{NewDB: true}).Model(&tables.TableEvalRun{}).Select("id").Where("target_type IN ?", params.TargetTypes))
	}
	if params.AfterSeq > 0 {
		query = query.Where("seq > ?", params.AfterSeq)
	}
	if params.RunID != "" && params.BaselineRunID != "" && (params.Change == EvalRunChangeWorse || params.Change == EvalRunChangeBetter) {
		// The comparison is the same join CountEvalRunScoreChanges counts with, so the
		// rows this lists are the rows that query counted.
		moved := "rr.score_number < rb.score_number"
		if params.Change == EvalRunChangeBetter {
			moved = "rr.score_number > rb.score_number"
		}
		query = query.Where(`id IN (
			SELECT er.id FROM evals_run_entries er
			JOIN evals_run_entries eb ON eb.run_id = ? AND eb.dataset_row_id = er.dataset_row_id
			JOIN evals_run_results rr ON rr.entry_id = er.id
			JOIN evals_run_results rb ON rb.entry_id = eb.id AND rb.evaluator_id = rr.evaluator_id
			WHERE er.run_id = ? AND `+moved+`)`, params.BaselineRunID, params.RunID)
	}
	if search := strings.TrimSpace(params.Search); search != "" {
		pattern := "%" + likeEscaper.Replace(strings.ToLower(search)) + "%"
		query = query.Where(`(LOWER(variables_json) LIKE ? ESCAPE '\' OR LOWER(output) LIKE ? ESCAPE '\')`, pattern, pattern)
	}
	return query
}

// ListEvalRunEntries returns a page of entries in sequence order, with the total the
// filters match.
func (s *RDBConfigStore) ListEvalRunEntries(ctx context.Context, params EvalRunEntryListParams) ([]tables.TableEvalRunEntry, int64, error) {
	var total int64
	if err := evalRunEntryQuery(s.DB().WithContext(ctx), params).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	query := evalRunEntryQuery(s.DB().WithContext(ctx), params).Order("run_id ASC, seq ASC")
	if params.Limit > 0 {
		query = query.Limit(params.Limit)
	}
	if params.Offset > 0 {
		query = query.Offset(params.Offset)
	}
	var entries []tables.TableEvalRunEntry
	if err := query.Find(&entries).Error; err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// ListEvalRunEntryRefs returns a page of entries in sequence order, without their
// variables and output and without counting what the filters match. Work that walks
// every entry of a run pages through it with AfterSeq.
func (s *RDBConfigStore) ListEvalRunEntryRefs(ctx context.Context, params EvalRunEntryListParams) ([]EvalRunEntryRef, error) {
	query := evalRunEntryQuery(s.DB().WithContext(ctx), params).
		Select("id, run_id, seq, status, attempt, key_id, routed_at, claim_id, started_at").Order("run_id ASC, seq ASC")
	if params.Limit > 0 {
		query = query.Limit(params.Limit)
	}
	if params.Offset > 0 {
		query = query.Offset(params.Offset)
	}
	var refs []EvalRunEntryRef
	if err := query.Scan(&refs).Error; err != nil {
		return nil, err
	}
	return refs, nil
}

// HasOpenEvalRunEntries reports whether any entry of a run is still waiting or being
// worked on. It stops at the first one, so it costs the same however large the run is.
func (s *RDBConfigStore) HasOpenEvalRunEntries(ctx context.Context, runID string) (bool, error) {
	var ids []string
	err := s.DB().WithContext(ctx).Model(&tables.TableEvalRunEntry{}).
		Where("run_id = ? AND status IN ?", runID, []string{tables.EvalRunEntryQueued, tables.EvalRunEntryRunning, tables.EvalRunEntryEvaluating}).
		Limit(1).Pluck("id", &ids).Error
	return len(ids) > 0, err
}

// GetEvalRunEntry returns one entry, or ErrNotFound.
func (s *RDBConfigStore) GetEvalRunEntry(ctx context.Context, id string) (*tables.TableEvalRunEntry, error) {
	var entry tables.TableEvalRunEntry
	if err := s.DB().WithContext(ctx).First(&entry, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &entry, nil
}

// TransitionEvalRunEntry writes fields to an entry while its status is one of from (any
// status when from is empty), and reports whether it wrote. See TransitionEvalRun.
func (s *RDBConfigStore) TransitionEvalRunEntry(ctx context.Context, id string, from []string, fields map[string]any) (bool, error) {
	query := s.DB().WithContext(ctx).Model(&tables.TableEvalRunEntry{}).Where("id = ?", id)
	if len(from) > 0 {
		query = query.Where("status IN ?", from)
	}
	return evalRunWrite(query, fields)
}

// TransitionEvalRunEntries writes fields to every entry of a run whose status is one of
// from, or to the listed entries only when ids is not empty. It returns how many changed.
func (s *RDBConfigStore) TransitionEvalRunEntries(ctx context.Context, runID string, from, ids []string, fields map[string]any) (int64, error) {
	var changed int64
	apply := func(chunk []string) error {
		query := s.DB().WithContext(ctx).Model(&tables.TableEvalRunEntry{}).Where("run_id = ?", runID)
		if len(from) > 0 {
			query = query.Where("status IN ?", from)
		}
		if chunk != nil {
			query = query.Where("id IN ?", chunk)
		}
		result := query.UpdateColumns(fields)
		changed += result.RowsAffected
		return result.Error
	}
	if len(ids) == 0 {
		return changed, apply(nil)
	}
	for start := 0; start < len(ids); start += evalDatasetBatch {
		if err := apply(ids[start:min(start+evalDatasetBatch, len(ids))]); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// RouteEvalRunEntry records the eval key whose queue a waiting entry's target call is
// on, and when. It moves only an entry that is still queued and still on fromKey, and
// reports whether it did, which makes routing a claim. The time is written on every
// move, a republish on the same key included: the sweep reads it to find a routed entry
// whose message was lost.
func (s *RDBConfigStore) RouteEvalRunEntry(ctx context.Context, id, fromKey, toKey string) (bool, error) {
	return evalRunWrite(s.DB().WithContext(ctx).Model(&tables.TableEvalRunEntry{}).
		Where("id = ? AND status = ? AND key_id = ?", id, tables.EvalRunEntryQueued, fromKey), map[string]any{"key_id": toKey, "routed_at": time.Now()})
}

// RouteEvalRunResult is RouteEvalRunEntry for a waiting evaluator result.
func (s *RDBConfigStore) RouteEvalRunResult(ctx context.Context, id, fromKey, toKey string) (bool, error) {
	return evalRunWrite(s.DB().WithContext(ctx).Model(&tables.TableEvalRunResult{}).
		Where("id = ? AND status = ? AND key_id = ?", id, tables.EvalRunResultQueued, fromKey), map[string]any{"key_id": toKey, "routed_at": time.Now()})
}

// evalRunClaim is the claim of a run's entry or result for one queue delivery. It holds
// when the row is queued on keyID, or running on keyID under the same delivery (the
// queue redelivers a message whose worker died), or running on keyID and started before
// startedBefore (a gateway on a queue that forgets takes over the work of one that
// stopped), or running with no recorded claim (the same rule as Runner.ownsStartedJob).
// The row's attempt must not be past the job's. A second message for a running row is
// another delivery and is refused, so the work runs once. See StartEvalExecution.
//
// A nonzero recoverStartedAt replaces the status rules with one exact match, as in
// StartEvalExecution: the row is running on keyID under claimID and started at
// recoverStartedAt. A row restarted since the caller read it is left to that start.
func evalRunClaim(query *gorm.DB, queued, running, keyID, claimID string, attempt int, startedBefore, recoverStartedAt time.Time) (bool, error) {
	query = query.Where("key_id = ? AND attempt <= ?", keyID, attempt)
	if recoverStartedAt.IsZero() {
		query = query.Where("status = ? OR (status = ? AND (claim_id = ? OR claim_id = '' OR started_at < ?))", queued, running, claimID, startedBefore)
	} else {
		query = query.Where("status = ? AND claim_id = ? AND started_at = ?", running, claimID, recoverStartedAt)
	}
	return evalRunWrite(query, map[string]any{"status": running, "attempt": attempt, "claim_id": claimID, "started_at": time.Now()})
}

// ClaimEvalRunEntry claims an entry's target call for a queue delivery and marks it
// running at attempt. See evalRunClaim.
func (s *RDBConfigStore) ClaimEvalRunEntry(ctx context.Context, id, keyID, claimID string, attempt int, startedBefore, recoverStartedAt time.Time) (bool, error) {
	return evalRunClaim(s.DB().WithContext(ctx).Model(&tables.TableEvalRunEntry{}).Where("id = ?", id),
		tables.EvalRunEntryQueued, tables.EvalRunEntryRunning, keyID, claimID, attempt, startedBefore, recoverStartedAt)
}

// ClaimEvalRunResult claims an evaluator result for a queue delivery and marks it
// running at attempt. See evalRunClaim.
func (s *RDBConfigStore) ClaimEvalRunResult(ctx context.Context, id, keyID, claimID string, attempt int, startedBefore, recoverStartedAt time.Time) (bool, error) {
	return evalRunClaim(s.DB().WithContext(ctx).Model(&tables.TableEvalRunResult{}).Where("id = ?", id),
		tables.EvalRunResultQueued, tables.EvalRunResultRunning, keyID, claimID, attempt, startedBefore, recoverStartedAt)
}

// TransitionEvalRunEntryAttempt is TransitionEvalRunEntry for the work of one attempt:
// it writes only while the entry is still at that attempt, so a late write of an earlier
// attempt, which a stop and a rerun have moved past, changes nothing.
func (s *RDBConfigStore) TransitionEvalRunEntryAttempt(ctx context.Context, id string, attempt int, from []string, fields map[string]any) (bool, error) {
	query := s.DB().WithContext(ctx).Model(&tables.TableEvalRunEntry{}).Where("id = ? AND attempt = ?", id, attempt)
	if len(from) > 0 {
		query = query.Where("status IN ?", from)
	}
	return evalRunWrite(query, fields)
}

// TransitionEvalRunResultAttempt is TransitionEvalRunEntryAttempt for a result.
func (s *RDBConfigStore) TransitionEvalRunResultAttempt(ctx context.Context, id string, attempt int, from []string, fields map[string]any) (bool, error) {
	query := s.DB().WithContext(ctx).Model(&tables.TableEvalRunResult{}).Where("id = ? AND attempt = ?", id, attempt)
	if len(from) > 0 {
		query = query.Where("status IN ?", from)
	}
	return evalRunWrite(query, fields)
}

// TransitionEvalRunResultUnstarted writes fields to a result that no delivery of attempt
// or a later one has claimed: it is queued, and its stored attempt is below attempt (the
// attempt a claim writes, see evalRunClaim). The runner ends a job that never started,
// such as one whose eval key is gone, without a claim, so the attempt-fenced write of
// TransitionEvalRunResultAttempt does not match it.
func (s *RDBConfigStore) TransitionEvalRunResultUnstarted(ctx context.Context, id string, attempt int, fields map[string]any) (bool, error) {
	return evalRunWrite(s.DB().WithContext(ctx).Model(&tables.TableEvalRunResult{}).
		Where("id = ? AND status = ? AND attempt < ?", id, tables.EvalRunResultQueued, attempt), fields)
}

// GetEvalRunLastSeq returns the highest entry sequence number of a run, 0 when it has
// no entries. Planning resumes after it.
func (s *RDBConfigStore) GetEvalRunLastSeq(ctx context.Context, runID string) (int64, error) {
	var last int64
	err := s.DB().WithContext(ctx).Model(&tables.TableEvalRunEntry{}).Where("run_id = ?", runID).
		Select("COALESCE(MAX(seq), 0)").Scan(&last).Error
	return last, err
}

// ListEvalRunRowIDs returns a page of the dataset rows that any of the runs has an
// entry for, in the order the rows have in the runs, with the total. Comparing runs
// reads rows through it, so a row that only some of the runs have is still listed.
func (s *RDBConfigStore) ListEvalRunRowIDs(ctx context.Context, runIDs []string, offset, limit int) ([]string, int64, error) {
	base := func() *gorm.DB {
		return s.DB().WithContext(ctx).Model(&tables.TableEvalRunEntry{}).Where("run_id IN ?", runIDs)
	}
	var total int64
	if err := base().Distinct("dataset_row_id").Count(&total).Error; err != nil {
		return nil, 0, err
	}
	query := base().Select("dataset_row_id, MIN(seq) AS first_seq").Group("dataset_row_id").Order("first_seq ASC, dataset_row_id ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	var rows []struct {
		DatasetRowID string
		FirstSeq     int64
	}
	if err := query.Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.DatasetRowID
	}
	return ids, total, nil
}

// CountEvalRunEntries counts a run's entries by status.
func (s *RDBConfigStore) CountEvalRunEntries(ctx context.Context, runID string) (map[string]int64, error) {
	return evalRunStatusCounts(s.DB().WithContext(ctx).Model(&tables.TableEvalRunEntry{}).Where("run_id = ?", runID))
}

func evalRunStatusCounts(query *gorm.DB) (map[string]int64, error) {
	var rows []struct {
		Status string
		Count  int64
	}
	if err := query.Select("status, COUNT(*) AS count").Group("status").Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(rows))
	for _, row := range rows {
		counts[row.Status] = row.Count
	}
	return counts, nil
}

// CountEvalRunEntriesPassed counts the completed entries of a run that passed.
func (s *RDBConfigStore) CountEvalRunEntriesPassed(ctx context.Context, runID string) (int64, error) {
	var count int64
	err := s.DB().WithContext(ctx).Model(&tables.TableEvalRunEntry{}).
		Where("run_id = ? AND status = ? AND passed = ?", runID, tables.EvalRunEntryCompleted, true).Count(&count).Error
	return count, err
}

// ListEvalRunTargetLatencies returns the target latency of every completed entry of a run.
func (s *RDBConfigStore) ListEvalRunTargetLatencies(ctx context.Context, runID string) ([]int64, error) {
	var latencies []int64
	err := s.DB().WithContext(ctx).Model(&tables.TableEvalRunEntry{}).
		Where("run_id = ? AND status = ?", runID, tables.EvalRunEntryCompleted).Pluck("target_latency_ms", &latencies).Error
	return latencies, err
}

// GetEvalRunTotals sums the tokens and cost a run's target and evaluator calls spent.
func (s *RDBConfigStore) GetEvalRunTotals(ctx context.Context, runID string) (EvalRunTotals, error) {
	var totals EvalRunTotals
	var target struct {
		Tokens int64
		Cost   float64
	}
	if err := s.DB().WithContext(ctx).Model(&tables.TableEvalRunEntry{}).Where("run_id = ?", runID).
		Select("COALESCE(SUM(target_total_tokens), 0) AS tokens, COALESCE(SUM(target_cost), 0) AS cost").Scan(&target).Error; err != nil {
		return totals, err
	}
	var evaluator struct {
		Tokens int64
		Cost   float64
	}
	if err := s.DB().WithContext(ctx).Model(&tables.TableEvalRunResult{}).Where("run_id = ?", runID).
		Select("COALESCE(SUM(total_tokens), 0) AS tokens, COALESCE(SUM(cost), 0) AS cost").Scan(&evaluator).Error; err != nil {
		return totals, err
	}
	totals.TargetTokens, totals.TargetCost = target.Tokens, target.Cost
	totals.EvaluatorTokens, totals.EvaluatorCost = evaluator.Tokens, evaluator.Cost
	return totals, nil
}

// ============================================================================
// Results
// ============================================================================

// CreateEvalRunResults stores results, in batches. An entry has one result per
// evaluator: a second one for the same pair is an error. It runs under the row lock of
// each run the results belong to and returns ErrNotFound, storing nothing, when a run or
// one of the entries is gone: see DeleteEvalRun.
//
// A nonzero entryAttempt also requires every entry to be evaluating at that attempt, and
// locks the entry rows (FOR UPDATE on Postgres; SQLite serializes writers). A target job
// creates its entry's results this way: a rerun that reset the entry since the job
// stored its output either waits for this insert and deletes the results with the entry
// (ResetEvalRunEntries takes the entry rows first), or resets first and the insert
// returns ErrNotFound. 0 checks only that the entries exist.
func (s *RDBConfigStore) CreateEvalRunResults(ctx context.Context, results []tables.TableEvalRunResult, entryAttempt int) error {
	if len(results) == 0 {
		return nil
	}
	now := time.Now()
	type ref struct{ runID, entryID string }
	seen := make(map[ref]struct{}, len(results))
	byRun := map[string][]string{}
	for i := range results {
		if results[i].ID == "" {
			results[i].ID = uuid.NewString()
		}
		if results[i].QueuedAt.IsZero() {
			results[i].QueuedAt = now
		}
		r := ref{results[i].RunID, results[i].EntryID}
		if _, ok := seen[r]; !ok {
			seen[r] = struct{}{}
			byRun[r.runID] = append(byRun[r.runID], r.entryID)
		}
	}
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockLiveEvalRuns(tx, results, func(r *tables.TableEvalRunResult) string { return r.RunID }); err != nil {
			return err
		}
		for runID, entryIDs := range byRun {
			for start := 0; start < len(entryIDs); start += evalDatasetBatch {
				chunk := entryIDs[start:min(start+evalDatasetBatch, len(entryIDs))]
				query := tx.Model(&tables.TableEvalRunEntry{}).Where("run_id = ? AND id IN ?", runID, chunk)
				if entryAttempt > 0 {
					query = withRowLock(query, "UPDATE").Where("status = ? AND attempt = ?", tables.EvalRunEntryEvaluating, entryAttempt)
				}
				var ids []string
				if err := query.Pluck("id", &ids).Error; err != nil {
					return err
				}
				if len(ids) != len(chunk) {
					return ErrNotFound
				}
			}
		}
		return tx.CreateInBatches(&results, evalDatasetBatch).Error
	})
}

// GetEvalRunResult returns one result, or ErrNotFound.
func (s *RDBConfigStore) GetEvalRunResult(ctx context.Context, id string) (*tables.TableEvalRunResult, error) {
	var result tables.TableEvalRunResult
	if err := s.DB().WithContext(ctx).First(&result, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &result, nil
}

// ListEvalRunResults returns results in ID order.
func (s *RDBConfigStore) ListEvalRunResults(ctx context.Context, params EvalRunResultListParams) ([]tables.TableEvalRunResult, error) {
	var results []tables.TableEvalRunResult
	if err := evalRunResultQuery(s.DB().WithContext(ctx), params).Find(&results).Error; err != nil {
		return nil, err
	}
	return results, nil
}

// ListEvalRunResultRefs is ListEvalRunResults without each result's reasoning, error
// and usage, which are most of a result's size.
func (s *RDBConfigStore) ListEvalRunResultRefs(ctx context.Context, params EvalRunResultListParams) ([]EvalRunResultRef, error) {
	var refs []EvalRunResultRef
	err := evalRunResultQuery(s.DB().WithContext(ctx), params).
		Select("id, run_id, entry_id, evaluator_id, status, pass, score_json, attempt, key_id, routed_at, claim_id, started_at").Scan(&refs).Error
	return refs, err
}

func evalRunResultQuery(db *gorm.DB, params EvalRunResultListParams) *gorm.DB {
	query := db.Model(&tables.TableEvalRunResult{})
	if params.RunID != "" {
		query = query.Where("run_id = ?", params.RunID)
	}
	if len(params.EntryIDs) > 0 {
		query = query.Where("entry_id IN ?", params.EntryIDs)
	}
	if params.EvaluatorID != "" {
		query = query.Where("evaluator_id = ?", params.EvaluatorID)
	}
	if len(params.Statuses) > 0 {
		query = query.Where("status IN ?", params.Statuses)
	}
	if !params.QueuedBefore.IsZero() {
		query = query.Where("queued_at < ?", params.QueuedBefore)
	}
	if params.KeyID != nil {
		query = query.Where("key_id = ?", *params.KeyID)
	}
	if len(params.ExcludeKeyIDs) > 0 {
		query = query.Where("key_id NOT IN ? OR routed_at < ?", params.ExcludeKeyIDs, params.RoutedBefore)
	}
	if !params.StartedBefore.IsZero() {
		query = query.Where("started_at < ?", params.StartedBefore)
	}
	if params.NeedsModel {
		// An evaluator's type and metric never change, so any version with a model provider
		// says the evaluator calls a model.
		query = query.Where("evaluator_id IN (?)", db.Model(&tables.TableEvaluatorVersion{}).Select("evaluator_id").Where("model_provider <> ''"))
	}
	if params.AfterID != "" {
		query = query.Where("id > ?", params.AfterID)
	}
	query = query.Order("id ASC")
	if params.Limit > 0 {
		query = query.Limit(params.Limit)
	}
	return query
}

// TransitionEvalRunResult writes fields to a result while its status is one of from (any
// status when from is empty), and reports whether it wrote. See TransitionEvalRun.
func (s *RDBConfigStore) TransitionEvalRunResult(ctx context.Context, id string, from []string, fields map[string]any) (bool, error) {
	query := s.DB().WithContext(ctx).Model(&tables.TableEvalRunResult{}).Where("id = ?", id)
	if len(from) > 0 {
		query = query.Where("status IN ?", from)
	}
	return evalRunWrite(query, fields)
}

// CountEvalRunResults counts an entry's results by status.
func (s *RDBConfigStore) CountEvalRunResults(ctx context.Context, entryID string) (map[string]int64, error) {
	return evalRunStatusCounts(s.DB().WithContext(ctx).Model(&tables.TableEvalRunResult{}).Where("entry_id = ?", entryID))
}

// CountEvalRunResultsByEvaluator counts a run's results by evaluator, status and pass.
func (s *RDBConfigStore) CountEvalRunResultsByEvaluator(ctx context.Context, runID string) ([]EvalRunEvaluatorCount, error) {
	var counts []EvalRunEvaluatorCount
	err := s.DB().WithContext(ctx).Model(&tables.TableEvalRunResult{}).Where("run_id = ?", runID).
		Select("evaluator_id, status, pass, COUNT(*) AS count").Group("evaluator_id, status, pass").Scan(&counts).Error
	return counts, err
}

// EvalRunScoreChange counts, for one evaluator, the dataset rows a run scored higher
// and lower than a baseline run. Compared is how many rows both runs have a numeric
// score for.
type EvalRunScoreChange struct {
	EvaluatorID string
	Improved    int64
	Regressed   int64
	Compared    int64
}

// CountEvalRunScoreChanges compares a run with a baseline run row by row, per
// evaluator. Rows are matched on the dataset row they came from.
func (s *RDBConfigStore) CountEvalRunScoreChanges(ctx context.Context, baseRunID, runID string) ([]EvalRunScoreChange, error) {
	var changes []EvalRunScoreChange
	err := s.DB().WithContext(ctx).Raw(`
		SELECT rb.evaluator_id AS evaluator_id,
			SUM(CASE WHEN rr.score_number > rb.score_number THEN 1 ELSE 0 END) AS improved,
			SUM(CASE WHEN rr.score_number < rb.score_number THEN 1 ELSE 0 END) AS regressed,
			COUNT(*) AS compared
		FROM evals_run_results rb
		JOIN evals_run_entries eb ON eb.id = rb.entry_id
		JOIN evals_run_entries er ON er.run_id = ? AND er.dataset_row_id = eb.dataset_row_id
		JOIN evals_run_results rr ON rr.entry_id = er.id AND rr.evaluator_id = rb.evaluator_id
		WHERE rb.run_id = ? AND rb.score_number IS NOT NULL AND rr.score_number IS NOT NULL
		GROUP BY rb.evaluator_id
		ORDER BY rb.evaluator_id`, runID, baseRunID).Scan(&changes).Error
	return changes, err
}

// ResetEvalRunEntries deletes the results of the listed entries of a run and writes
// fields to those entries, in one transaction, so a failure leaves every entry either
// untouched or fully reset. ids must not be empty.
func (s *RDBConfigStore) ResetEvalRunEntries(ctx context.Context, runID string, ids []string, fields map[string]any) (int64, error) {
	if len(ids) == 0 {
		return 0, errors.New("no entries to reset")
	}
	var changed int64
	err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		changed = 0
		for start := 0; start < len(ids); start += evalDatasetBatch {
			chunk := ids[start:min(start+evalDatasetBatch, len(ids))]
			// The entries are written first: that takes their row locks, so a result insert
			// fenced on an entry's attempt (CreateEvalRunResults) that holds them commits
			// before the delete below runs, and its results are deleted with the rest.
			result := tx.Model(&tables.TableEvalRunEntry{}).Where("run_id = ? AND id IN ?", runID, chunk).UpdateColumns(fields)
			if result.Error != nil {
				return result.Error
			}
			changed += result.RowsAffected
			if err := tx.Where("run_id = ? AND entry_id IN ?", runID, chunk).Delete(&tables.TableEvalRunResult{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return changed, err
}

// DeleteEvalRunResults removes results of a run: those of the listed entries, or of
// every entry when entryIDs is empty, and only one evaluator's when evaluatorID is set.
func (s *RDBConfigStore) DeleteEvalRunResults(ctx context.Context, runID string, entryIDs []string, evaluatorID string) (int64, error) {
	var removed int64
	apply := func(chunk []string) error {
		query := s.DB().WithContext(ctx).Where("run_id = ?", runID)
		if chunk != nil {
			query = query.Where("entry_id IN ?", chunk)
		}
		if evaluatorID != "" {
			query = query.Where("evaluator_id = ?", evaluatorID)
		}
		result := query.Delete(&tables.TableEvalRunResult{})
		removed += result.RowsAffected
		return result.Error
	}
	if len(entryIDs) == 0 {
		return removed, apply(nil)
	}
	for start := 0; start < len(entryIDs); start += evalDatasetBatch {
		if err := apply(entryIDs[start:min(start+evalDatasetBatch, len(entryIDs))]); err != nil {
			return removed, err
		}
	}
	return removed, nil
}
