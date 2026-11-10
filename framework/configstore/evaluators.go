package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/maximhq/bifrost/framework/queryscope"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// ErrEvaluatorNameExists is returned when a live evaluator already uses the name.
var ErrEvaluatorNameExists = errors.New("an evaluator with this name already exists")

// evaluatorLive is the predicate that selects evaluators that have not been deleted.
const evaluatorLive = "evals_evaluators.deleted_at_unix_nano = 0"

// evaluatorVersionRetries bounds how often a version save is retried after losing a
// race for the next version number.
const evaluatorVersionRetries = 5

// Values of EvaluatorListParams.Source.
const (
	EvaluatorSourceCustom  = "custom"  // written by a user
	EvaluatorSourceBuiltin = "builtin" // installed from the catalog
)

// Values of EvaluatorListParams.KeyStatus.
const (
	EvaluatorKeyStatusReady   = "ready"  // calls no model, or its provider has an eval key
	EvaluatorKeyStatusMissing = "no_key" // calls a model whose provider has no eval key
)

// EvaluatorListParams filters and pages an evaluator listing.
type EvaluatorListParams struct {
	Types  []string // any of these evaluator types; empty means every type
	Source string   // EvaluatorSourceCustom or EvaluatorSourceBuiltin; empty means both
	Search string   // case-insensitive substring of name or description
	Tag    string   // exact tag
	// KeyStatus keeps evaluators by whether they can run: EvaluatorKeyStatusReady or
	// EvaluatorKeyStatusMissing. Empty means both. KeyProviders is the set of providers
	// that have a key marked for evaluations; the caller supplies it, since key state
	// lives outside this store.
	KeyStatus    string
	KeyProviders []string
	Limit        int // 0 means no limit
	Offset       int
}

// likeEscaper escapes LIKE wildcards so user text is matched literally.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// ============================================================================
// Evals - Evaluators
// ============================================================================

// ListEvaluators returns live evaluators, newest first, with the total before paging.
func (s *RDBConfigStore) ListEvaluators(ctx context.Context, params EvaluatorListParams) ([]tables.TableEvaluator, int64, error) {
	query := s.ScopedDB(ctx).Model(&tables.TableEvaluator{}).Where(evaluatorLive)
	if len(params.Types) > 0 {
		query = query.Where("evals_evaluators.type IN ?", params.Types)
	}
	switch params.Source {
	case EvaluatorSourceCustom:
		query = query.Where("evals_evaluators.builtin_key IS NULL")
	case EvaluatorSourceBuiltin:
		query = query.Where("evals_evaluators.builtin_key IS NOT NULL")
	}
	if search := strings.TrimSpace(params.Search); search != "" {
		pattern := "%" + likeEscaper.Replace(strings.ToLower(search)) + "%"
		query = query.Where(`(LOWER(evals_evaluators.name) LIKE ? ESCAPE '\' OR LOWER(evals_evaluators.description) LIKE ? ESCAPE '\')`, pattern, pattern)
	}
	if params.Tag != "" {
		// Tags are stored as a JSON array, so a whole tag is matched by its quoted form.
		quoted, err := json.Marshal(params.Tag)
		if err != nil {
			return nil, 0, err
		}
		query = query.Where(`evals_evaluators.tags_json LIKE ? ESCAPE '\'`, "%"+likeEscaper.Replace(string(quoted))+"%")
	}

	if params.KeyStatus == EvaluatorKeyStatusReady || params.KeyStatus == EvaluatorKeyStatusMissing {
		// Blocked means: the version in use calls a provider that has no eval key.
		blocked := s.DB().WithContext(ctx).Model(&tables.TableEvaluatorVersion{}).
			Select("1").
			Where("evals_evaluator_versions.evaluator_id = evals_evaluators.id").
			Where("evals_evaluator_versions.version = evals_evaluators.current_version").
			Where("evals_evaluator_versions.model_provider <> ''")
		if len(params.KeyProviders) > 0 {
			blocked = blocked.Where("evals_evaluator_versions.model_provider NOT IN ?", params.KeyProviders)
		}
		if params.KeyStatus == EvaluatorKeyStatusMissing {
			query = query.Where("EXISTS (?)", blocked)
		} else {
			query = query.Where("NOT EXISTS (?)", blocked)
		}
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	query = query.Order("evals_evaluators.created_at DESC, evals_evaluators.id DESC")
	if params.Limit > 0 {
		query = query.Limit(params.Limit)
	}
	if params.Offset > 0 {
		query = query.Offset(params.Offset)
	}
	var evaluators []tables.TableEvaluator
	if err := query.Find(&evaluators).Error; err != nil {
		return nil, 0, err
	}
	return evaluators, total, nil
}

// GetEvaluator returns a live evaluator by ID. An evaluator outside the caller's scope
// reads as ErrNotFound, the same as one that does not exist.
func (s *RDBConfigStore) GetEvaluator(ctx context.Context, id string) (*tables.TableEvaluator, error) {
	return s.getEvaluator(ctx, nil, "evals_evaluators.id = ?", id)
}

// GetEvaluatorByName returns a live evaluator by name.
func (s *RDBConfigStore) GetEvaluatorByName(ctx context.Context, name string) (*tables.TableEvaluator, error) {
	return s.getEvaluator(ctx, nil, "evals_evaluators.name = ?", name)
}

// getEvaluator loads one live evaluator through the caller's scope.
func (s *RDBConfigStore) getEvaluator(ctx context.Context, tx *gorm.DB, where string, arg any) (*tables.TableEvaluator, error) {
	var evaluator tables.TableEvaluator
	if err := s.ScopedDB(ctx, tx).Where(evaluatorLive).Where(where, arg).First(&evaluator).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &evaluator, nil
}

// CreateEvaluator stores a new evaluator together with its first version.
func (s *RDBConfigStore) CreateEvaluator(ctx context.Context, evaluator *tables.TableEvaluator, version *tables.TableEvaluatorVersion) error {
	if evaluator == nil || version == nil {
		return errors.New("evaluator and its first version are required")
	}
	if evaluator.ID == "" {
		evaluator.ID = uuid.NewString()
	}
	if version.ID == "" {
		version.ID = uuid.NewString()
	}
	evaluator.CurrentVersion = 1
	evaluator.DeletedAt = 0
	version.EvaluatorID = evaluator.ID
	version.Version = 1

	err := s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(evaluator).Error; err != nil {
			return err
		}
		return tx.Create(version).Error
	})
	if isUniqueConstraintError(err) {
		return ErrEvaluatorNameExists
	}
	return err
}

// UpdateEvaluator changes an evaluator's name, description and tags. The type and the
// version pointer are not writable here: a config change goes through CreateEvaluatorVersion.
func (s *RDBConfigStore) UpdateEvaluator(ctx context.Context, evaluator *tables.TableEvaluator) error {
	return s.updateEvaluator(ctx, s.DB().WithContext(ctx), evaluator)
}

// updateEvaluator writes an evaluator's name, description and tags on tx.
func (s *RDBConfigStore) updateEvaluator(ctx context.Context, tx *gorm.DB, evaluator *tables.TableEvaluator) error {
	if evaluator == nil {
		return errors.New("evaluator is required")
	}
	if _, err := s.getEvaluator(ctx, tx, "evals_evaluators.id = ?", evaluator.ID); err != nil {
		return err
	}
	var tagsJSON *string
	if evaluator.Tags != nil {
		data, err := json.Marshal(evaluator.Tags)
		if err != nil {
			return err
		}
		tagsJSON = new(string(data))
	}
	result := tx.Model(&tables.TableEvaluator{}).
		Where("id = ? AND deleted_at_unix_nano = 0", evaluator.ID).
		Updates(map[string]any{
			"name":        evaluator.Name,
			"description": evaluator.Description,
			"tags_json":   tagsJSON,
			"updated_at":  time.Now(),
		})
	if result.Error != nil {
		if isUniqueConstraintError(result.Error) {
			return ErrEvaluatorNameExists
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateEvaluatorAndAddVersion writes an evaluator's metadata and appends its next version
// in one transaction, so a version that cannot be stored leaves the metadata as it was.
func (s *RDBConfigStore) UpdateEvaluatorAndAddVersion(ctx context.Context, evaluator *tables.TableEvaluator, version *tables.TableEvaluatorVersion) error {
	if version == nil {
		return s.UpdateEvaluator(ctx, evaluator)
	}
	if version.ID == "" {
		version.ID = uuid.NewString()
	}
	var lastErr error
	for range evaluatorVersionRetries {
		lastErr = s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := s.updateEvaluator(ctx, tx, evaluator); err != nil {
				return err
			}
			return s.appendEvaluatorVersion(ctx, tx, version)
		})
		if !isUniqueConstraintError(lastErr) || errors.Is(lastErr, ErrEvaluatorNameExists) {
			return lastErr
		}
	}
	return fmt.Errorf("failed to create evaluator version after %d attempts: %w", evaluatorVersionRetries, lastErr)
}

// DeleteEvaluator soft-deletes an evaluator. Its versions are kept so results recorded
// against them stay explainable, and its name becomes free for reuse.
func (s *RDBConfigStore) DeleteEvaluator(ctx context.Context, id string) error {
	if _, err := s.GetEvaluator(ctx, id); err != nil {
		return err
	}
	now := time.Now()
	result := s.DB().WithContext(ctx).Model(&tables.TableEvaluator{}).
		Where("id = ? AND deleted_at_unix_nano = 0", id).
		Updates(map[string]any{"deleted_at_unix_nano": now.UnixNano(), "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ============================================================================
// Evals - Evaluator versions
// ============================================================================

// CreateEvaluatorVersion appends the next version of an evaluator and moves the
// evaluator's pointer to it. The version number is assigned here.
func (s *RDBConfigStore) CreateEvaluatorVersion(ctx context.Context, version *tables.TableEvaluatorVersion) error {
	if version == nil {
		return errors.New("evaluator version is required")
	}
	if version.ID == "" {
		version.ID = uuid.NewString()
	}
	var lastErr error
	for range evaluatorVersionRetries {
		lastErr = s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return s.appendEvaluatorVersion(ctx, tx, version)
		})
		// Two writers picked the same number; the loser retries with a fresh read.
		if !isUniqueConstraintError(lastErr) {
			return lastErr
		}
	}
	return fmt.Errorf("failed to create evaluator version after %d attempts: %w", evaluatorVersionRetries, lastErr)
}

// appendEvaluatorVersion numbers and inserts the next version on tx and moves the pointer.
func (s *RDBConfigStore) appendEvaluatorVersion(ctx context.Context, tx *gorm.DB, version *tables.TableEvaluatorVersion) error {
	// A version is a write against its parent, so the parent must be live and in scope.
	if _, err := s.getEvaluator(ctx, tx, "evals_evaluators.id = ?", version.EvaluatorID); err != nil {
		return err
	}
	var latest int
	if err := tx.Model(&tables.TableEvaluatorVersion{}).
		Where("evaluator_id = ?", version.EvaluatorID).
		Select("COALESCE(MAX(version), 0)").
		Scan(&latest).Error; err != nil {
		return err
	}
	version.Version = latest + 1
	if err := tx.Create(version).Error; err != nil {
		return err
	}
	// Only ever move the pointer forward, so a slower writer cannot move it back.
	return tx.Model(&tables.TableEvaluator{}).
		Where("id = ? AND current_version < ?", version.EvaluatorID, version.Version).
		Updates(map[string]any{"current_version": version.Version, "updated_at": time.Now()}).Error
}

// GetEvaluatorVersion returns one version of an evaluator. Versions of a deleted
// evaluator stay readable.
func (s *RDBConfigStore) GetEvaluatorVersion(ctx context.Context, evaluatorID string, versionNumber int) (*tables.TableEvaluatorVersion, error) {
	var version tables.TableEvaluatorVersion
	if err := s.DB().WithContext(ctx).
		Where("evaluator_id = ? AND version = ?", evaluatorID, versionNumber).
		First(&version).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &version, nil
}

// ListEvaluatorVersions returns every version of an evaluator, newest first.
func (s *RDBConfigStore) ListEvaluatorVersions(ctx context.Context, evaluatorID string) ([]tables.TableEvaluatorVersion, error) {
	var versions []tables.TableEvaluatorVersion
	if err := s.DB().WithContext(ctx).
		Where("evaluator_id = ?", evaluatorID).
		Order("version DESC").
		Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

// ============================================================================
// Evals - Executions
// ============================================================================

// CreateEvalExecution stores a queued evaluator run.
func (s *RDBConfigStore) CreateEvalExecution(ctx context.Context, execution *tables.TableEvalExecution) error {
	if execution == nil {
		return errors.New("execution is required")
	}
	if execution.ID == "" {
		execution.ID = uuid.NewString()
	}
	execution.Status = tables.EvalExecutionQueued
	return s.DB().WithContext(ctx).Create(execution).Error
}

// GetEvalExecution returns an execution by ID.
func (s *RDBConfigStore) GetEvalExecution(ctx context.Context, id string) (*tables.TableEvalExecution, error) {
	var execution tables.TableEvalExecution
	if err := s.DB().WithContext(ctx).First(&execution, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// An execution is visible when its evaluator is: the scope is written for the evaluators
	// table, so it is applied there, deleted evaluators included. A draft has no evaluator to
	// scope by. With no scope on ctx (the runner's own reads) nothing is checked.
	if execution.EvaluatorID != nil && queryscope.FromContext(ctx) != nil {
		var visible int64
		if err := s.ScopedDB(ctx).Model(&tables.TableEvaluator{}).Where("evals_evaluators.id = ?", *execution.EvaluatorID).Count(&visible).Error; err != nil {
			return nil, err
		}
		if visible == 0 {
			return nil, ErrNotFound
		}
	}
	return &execution, nil
}

// StartEvalExecution claims an execution for the queue delivery claimID and marks it
// running on keyID ("" for none), reporting whether the claim held. It holds when the
// execution is queued on keyID, when it is already running under the same delivery (the
// queue redelivers a message whose worker died), or when it is running on keyID and
// started before reclaimStartedBefore (a gateway on a queue that forgets takes over the
// work of the one that stopped). A completed execution is never claimed, and another
// delivery of a running one is refused, so a duplicate message does not run it twice.
//
// A nonzero recoverStartedAt replaces all of that with one exact match: the execution is
// running on keyID under claimID and started at recoverStartedAt. The sweep ends a lost
// execution this way, so one restarted since the sweep read it is left to that start.
func (s *RDBConfigStore) StartEvalExecution(ctx context.Context, id, keyID, claimID string, reclaimStartedBefore, recoverStartedAt time.Time) (bool, error) {
	query := s.DB().WithContext(ctx).Model(&tables.TableEvalExecution{}).Where("id = ? AND key_id = ?", id, keyID)
	if recoverStartedAt.IsZero() {
		query = query.Where("status = ? OR (status = ? AND (claim_id = ? OR started_at < ?))",
			tables.EvalExecutionQueued, tables.EvalExecutionRunning, claimID, reclaimStartedBefore)
	} else {
		query = query.Where("status = ? AND claim_id = ? AND started_at = ?", tables.EvalExecutionRunning, claimID, recoverStartedAt)
	}
	result := query.Updates(map[string]any{"status": tables.EvalExecutionRunning, "claim_id": claimID, "started_at": time.Now()})
	if err := s.evalExecutionWriteOutcome(ctx, id, result); err != nil {
		return false, err
	}
	return result.RowsAffected > 0, nil
}

// RouteEvalExecution records the key whose queue a waiting execution was put on. It
// only moves an execution that is still queued and still on fromKey ("" for one not
// routed yet), and reports whether it did. That makes routing a claim: of several
// writers that try to move the same execution, exactly one succeeds.
func (s *RDBConfigStore) RouteEvalExecution(ctx context.Context, id, fromKey, toKey string) (bool, error) {
	result := s.DB().WithContext(ctx).Model(&tables.TableEvalExecution{}).
		Where("id = ? AND status = ? AND key_id = ?", id, tables.EvalExecutionQueued, fromKey).
		Updates(map[string]any{"key_id": toKey, "routed_at": time.Now()})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// ListQueuedEvalExecutions returns executions that were created before the cutoff and
// have still not started, oldest first, leaving out those routed to one of skipKeys
// unless that routing happened before routedBefore. The filter runs before the limit,
// so executions waiting their turn on a consumed key never fill a page and hide a
// stranded one behind them. limit bounds the page; 0 means no limit.
//
// It also returns executions running on a key that is not one of skipKeys (and not
// "", the local topic) that started before routedBefore: nothing consumes that key's
// topic, and the claim is long expired, so the sweep ends them.
func (s *RDBConfigStore) ListQueuedEvalExecutions(ctx context.Context, createdBefore, routedBefore time.Time, skipKeys []string, limit int) ([]tables.TableEvalExecution, error) {
	db := s.DB().WithContext(ctx)
	queued := db.Where("status = ? AND created_at < ?", tables.EvalExecutionQueued, createdBefore)
	lost := db.Where("status = ? AND key_id <> ? AND started_at < ?", tables.EvalExecutionRunning, "", routedBefore)
	if len(skipKeys) > 0 {
		queued = queued.Where("key_id NOT IN ? OR routed_at < ?", skipKeys, routedBefore)
		lost = lost.Where("key_id NOT IN ?", skipKeys)
	}
	query := db.Where(queued).Or(lost)
	query = query.Order("created_at ASC, id ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	var executions []tables.TableEvalExecution
	if err := query.Find(&executions).Error; err != nil {
		return nil, err
	}
	return executions, nil
}

// ListUnfinishedEvalExecutions returns a page of the executions that have not
// completed, oldest first, after the (afterCreatedAt, afterID) position, at most limit
// of them (0 means no limit). A gateway on a queue that forgets its messages reads it
// on start, a page at a time, so a large backlog is never loaded at once.
func (s *RDBConfigStore) ListUnfinishedEvalExecutions(ctx context.Context, afterCreatedAt time.Time, afterID string, limit int) ([]tables.TableEvalExecution, error) {
	query := s.DB().WithContext(ctx).Where("status <> ?", tables.EvalExecutionCompleted)
	if afterID != "" {
		query = query.Where("created_at > ? OR (created_at = ? AND id > ?)", afterCreatedAt, afterCreatedAt, afterID)
	}
	query = query.Order("created_at ASC, id ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	var executions []tables.TableEvalExecution
	err := query.Find(&executions).Error
	return executions, err
}

// DeleteCompletedEvalExecutions removes executions that completed before the cutoff and
// returns how many. An execution that has not completed is never removed.
func (s *RDBConfigStore) DeleteCompletedEvalExecutions(ctx context.Context, completedBefore time.Time) (int64, error) {
	result := s.DB().WithContext(ctx).
		Where("status = ? AND completed_at < ?", tables.EvalExecutionCompleted, completedBefore).
		Delete(&tables.TableEvalExecution{})
	return result.RowsAffected, result.Error
}

// CompleteEvalExecution stores an execution's result. The first result stands: a repeat
// completion changes nothing and is not an error.
func (s *RDBConfigStore) CompleteEvalExecution(ctx context.Context, id string, resultJSON json.RawMessage) error {
	result := s.DB().WithContext(ctx).Model(&tables.TableEvalExecution{}).
		Where("id = ? AND status <> ?", id, tables.EvalExecutionCompleted).
		Updates(map[string]any{"status": tables.EvalExecutionCompleted, "result_json": string(resultJSON), "completed_at": time.Now()})
	return s.evalExecutionWriteOutcome(ctx, id, result)
}

// evalExecutionWriteOutcome tells a write that matched nothing because the execution
// already completed (fine) from one whose execution does not exist (ErrNotFound).
func (s *RDBConfigStore) evalExecutionWriteOutcome(ctx context.Context, id string, result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	var count int64
	if err := s.DB().WithContext(ctx).Model(&tables.TableEvalExecution{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// evaluatorVersionsBatch bounds the number of (evaluator, version) pairs per query.
const evaluatorVersionsBatch = 100

// GetEvaluatorVersionsAt returns one version of each of several evaluators, keyed by
// evaluator ID. A listing uses it to read the current config of a page of evaluators in
// one query instead of one per row. Pairs that match nothing are absent from the result.
func (s *RDBConfigStore) GetEvaluatorVersionsAt(ctx context.Context, versionByEvaluator map[string]int) (map[string]tables.TableEvaluatorVersion, error) {
	out := make(map[string]tables.TableEvaluatorVersion, len(versionByEvaluator))
	if len(versionByEvaluator) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(versionByEvaluator))
	for id := range versionByEvaluator {
		ids = append(ids, id)
	}
	for start := 0; start < len(ids); start += evaluatorVersionsBatch {
		batch := ids[start:min(start+evaluatorVersionsBatch, len(ids))]
		query := s.DB().WithContext(ctx).Model(&tables.TableEvaluatorVersion{})
		for i, id := range batch {
			if i == 0 {
				query = query.Where("(evaluator_id = ? AND version = ?)", id, versionByEvaluator[id])
			} else {
				query = query.Or("(evaluator_id = ? AND version = ?)", id, versionByEvaluator[id])
			}
		}
		var versions []tables.TableEvaluatorVersion
		if err := query.Find(&versions).Error; err != nil {
			return nil, err
		}
		for _, version := range versions {
			out[version.EvaluatorID] = version
		}
	}
	return out, nil
}
