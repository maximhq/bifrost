package runs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/framework/evals/datasets"
)

// A run's work goes through the evaluation runner as three kinds of job. The job ID
// says which, and carries the attempt, because the queue drops a second message with
// an ID it has seen: a rerun must have a new one.
const (
	planJobPrefix   = "runplan:"   // runplan:<run id>: create the entries and queue them
	targetJobPrefix = "runtarget:" // runtarget:<entry id>:<attempt>: call the target for one entry
	resultJobPrefix = "runresult:" // runresult:<result id>:<attempt>: run one evaluator on one entry
	queueJobPrefix  = "runqueue:"  // runqueue:<run id>:<nonce>: queue the waiting entries of a rerun
	scoreJobPrefix  = "runscore:"  // runscore:<run id>:<nonce>: score stored outputs with evaluators added to a run
	resumeJobPrefix = "runresume:" // runresume:<run id>:<nonce>: put an active run's work back on a queue that lost it
	repairJobPrefix = "runrepair:" // runrepair:<entry id>:<attempt>: finish the results of an entry left evaluating on a key nothing consumes
)

func repairJobID(entryID string, attempt int) string {
	return repairJobPrefix + entryID + ":" + strconv.Itoa(attempt)
}

func planJobID(runID string) string { return planJobPrefix + runID }

func targetJobID(entryID string, attempt int) string {
	return targetJobPrefix + entryID + ":" + strconv.Itoa(attempt)
}

func resultJobID(resultID string, attempt int) string {
	return resultJobPrefix + resultID + ":" + strconv.Itoa(attempt)
}

// runJobID names a job that works on a whole run. The nonce makes each one a new
// message: a run can be rerun, or given more evaluators, any number of times.
func runJobID(prefix, runID string) string {
	return prefix + runID + ":" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// runOfJob reads the run a whole-run job works on.
func runOfJob(id, prefix string) (runID string, ok bool) {
	rest, found := strings.CutPrefix(id, prefix)
	if !found {
		return "", false
	}
	runID, _, _ = strings.Cut(rest, ":")
	return runID, runID != ""
}

// isRunJob reports whether a job works on a whole run. Such a job is stored nowhere:
// it writes what it produces itself, and runs on the local topic.
func isRunJob(id string) bool {
	for _, prefix := range []string{planJobPrefix, queueJobPrefix, scoreJobPrefix, resumeJobPrefix, repairJobPrefix} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

// splitJobID reads a target or result job ID into its subject and attempt.
func splitJobID(id, prefix string) (subject string, attempt int, ok bool) {
	rest, found := strings.CutPrefix(id, prefix)
	if !found {
		return "", 0, false
	}
	subject, number, found := strings.Cut(rest, ":")
	if !found {
		return "", 0, false
	}
	attempt, err := strconv.Atoi(number)
	return subject, attempt, err == nil && subject != ""
}

// terminalResultStatuses are the statuses of a result that has its outcome stored.
var terminalResultStatuses = []string{tables.EvalRunResultCompleted, tables.EvalRunResultFailed, tables.EvalRunResultNotApplicable}

// noop is the work of a job that is only being queued. The runner routes a job by
// whether it brings its own work; the real work is attached when the job is loaded.
func noop(context.Context) evals.Result { return evals.Result{Score: evals.NAScore()} }

// JobStore is the runner's job store for an instance that also runs test runs. Jobs
// of a run are read from, and written to, the run's own tables; every other job is an
// API execution and goes to the store it wraps.
type JobStore struct {
	inner evals.JobStore
	svc   *Service
}

// NewJobStore wraps the store for API executions with the jobs of test runs.
func NewJobStore(inner evals.JobStore, svc *Service) *JobStore {
	return &JobStore{inner: inner, svc: svc}
}

// done is a job the runner should skip.
func done(id string) *evals.Job { return &evals.Job{ID: id, Done: true} }

// LoadJob builds the job a message names.
func (j *JobStore) LoadJob(ctx context.Context, id string) (*evals.Job, error) {
	switch {
	case strings.HasPrefix(id, planJobPrefix):
		return j.svc.loadPlanJob(ctx, id)
	case strings.HasPrefix(id, queueJobPrefix):
		return j.svc.loadRunJob(ctx, id, queueJobPrefix, j.svc.queueWaiting)
	case strings.HasPrefix(id, scoreJobPrefix):
		return j.svc.loadRunJob(ctx, id, scoreJobPrefix, j.svc.scoreStored)
	case strings.HasPrefix(id, resumeJobPrefix):
		return j.svc.loadResumeJob(ctx, id)
	case strings.HasPrefix(id, repairJobPrefix):
		return j.svc.loadRepairJob(ctx, id)
	case strings.HasPrefix(id, targetJobPrefix):
		return j.svc.loadTargetJob(ctx, id)
	case strings.HasPrefix(id, resultJobPrefix):
		return j.svc.loadResultJob(ctx, id)
	}
	return j.inner.LoadJob(ctx, id)
}

// StartJob claims the job for the worker that is about to run it, and reports whether
// it did. An entry or result that already ended is not claimed, so a late duplicate
// message does not run it again. An evaluator execution is claimed by the wrapped store.
//
// The claim follows evals.JobClaim: a waiting entry or result is claimed by the delivery
// on its key, a running one only by a redelivery of the message that started it, or by
// any delivery once it started before claim.StartedBefore.
func (j *JobStore) StartJob(ctx context.Context, id, keyID string, claim evals.JobClaim) (bool, error) {
	if entryID, attempt, ok := splitJobID(id, targetJobPrefix); ok {
		claimed, err := j.svc.store.ClaimEvalRunEntry(ctx, entryID, keyID, claim.DeliveryID, attempt, claim.StartedBefore, claim.RecoverStartedAt)
		if err != nil || claimed || !claim.RecoverStartedAt.IsZero() {
			return claimed, err // a recovery claim takes back only the running start it names
		}
		// An entry left evaluating by this attempt has its output stored; its job only
		// finishes the results (see loadTargetJob). It is claimed without touching its
		// status or output, and the target is not called again.
		return j.svc.store.TransitionEvalRunEntryAttempt(ctx, entryID, attempt, []string{tables.EvalRunEntryEvaluating}, map[string]any{"key_id": keyID})
	}
	if resultID, attempt, ok := splitJobID(id, resultJobPrefix); ok {
		claimed, err := j.svc.store.ClaimEvalRunResult(ctx, resultID, keyID, claim.DeliveryID, attempt, claim.StartedBefore, claim.RecoverStartedAt)
		if err != nil || claimed || !claim.RecoverStartedAt.IsZero() {
			return claimed, err
		}
		// A result this attempt already stored has its job finish the entry's progress
		// (see loadResultJob). It is claimed without touching what was stored.
		return j.svc.store.TransitionEvalRunResultAttempt(ctx, resultID, attempt, terminalResultStatuses, map[string]any{"key_id": keyID})
	}
	if isRunJob(id) {
		return true, nil
	}
	return j.inner.StartJob(ctx, id, keyID, claim)
}

// CompleteJob stores what a job produced when the job did not store it itself. A job
// loaded by this store completes through its own Complete; this is the path of a job
// the sweep found, which the runner ends with an error when no eval key can take it.
func (j *JobStore) CompleteJob(ctx context.Context, id string, result evals.Result) error {
	if resultID, attempt, ok := splitJobID(id, resultJobPrefix); ok {
		stored, err := j.svc.store.GetEvalRunResult(ctx, resultID)
		if errors.Is(err, configstore.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return j.svc.completeResult(ctx, resultRef{id: stored.ID, runID: stored.RunID, entryID: stored.EntryID, evaluatorID: stored.EvaluatorID}, attempt, result)
	}
	if entryID, _, ok := splitJobID(id, targetJobPrefix); ok {
		// A target job stores its own output. The runner only completes one with an
		// error, for an entry that waited on a key and has none left to run on.
		if result.Score.Status == evals.ScoreStatusError {
			return j.svc.failWaitingEntry(ctx, entryID, result.Reasoning)
		}
		return nil
	}
	if isRunJob(id) {
		return nil
	}
	return j.inner.CompleteJob(ctx, id, result)
}

// RouteJob records the eval key whose queue a waiting job is on.
func (j *JobStore) RouteJob(ctx context.Context, id, fromKey, toKey string) (bool, error) {
	if entryID, _, ok := splitJobID(id, targetJobPrefix); ok {
		return j.svc.store.RouteEvalRunEntry(ctx, entryID, fromKey, toKey)
	}
	if resultID, _, ok := splitJobID(id, resultJobPrefix); ok {
		return j.svc.store.RouteEvalRunResult(ctx, resultID, fromKey, toKey)
	}
	if isRunJob(id) {
		return true, nil
	}
	return j.inner.RouteJob(ctx, id, fromKey, toKey)
}

// StaleJobs returns the jobs of runs that may be stranded: waiting since before the
// cutoff on a key that is not in skipKeys, or never routed to a key although they
// call a model. A job waiting on a key that is being consumed is in no trouble, and a
// large run has thousands of them, so they are left out by the query, unless it was
// routed there before routedBefore: its message may have been lost between routing and
// publishing. Only what the runner needs to move a job is read: an entry's variables
// and output are not.
//
// routedBefore is also the cutoff for jobs that started on a key nothing consumes: see
// lostStartedJobs.
func (j *JobStore) StaleJobs(ctx context.Context, queuedBefore, routedBefore time.Time, skipKeys []string, limit int) ([]*evals.Job, error) {
	jobs, err := j.inner.StaleJobs(ctx, queuedBefore, routedBefore, skipKeys, limit)
	if err != nil {
		return nil, err
	}
	// Each run is decoded once for the sweep, however many of its jobs are stale.
	specs := map[string]*runSpec{}
	specFor := func(runID string) *runSpec {
		if spec, seen := specs[runID]; seen {
			return spec
		}
		var spec *runSpec
		if state, err := j.svc.store.GetEvalRunState(ctx, runID); err == nil {
			spec, _ = j.svc.spec(ctx, runID, state.UpdatedAt)
		}
		specs[runID] = spec
		return spec
	}
	onDeadKey := append(slices.Clone(skipKeys), "")
	filters := []struct {
		keyID   *string
		exclude []string
		targets []string
	}{
		// On a key nothing consumes, or routed to a consumed key before routedBefore: a
		// routed job whose message was lost between routing and publishing.
		{exclude: onDeadKey},
		// Never routed. Only runs whose target calls a model can be stuck that way; the
		// others wait on the local topic by design, and leaving them out in the query
		// keeps a long line of them from hiding a stuck entry behind the limit.
		{keyID: new(""), targets: []string{string(TargetPrompt), string(TargetModel)}},
	}
	for _, filter := range filters {
		unrouted := filter.keyID != nil
		entries, err := j.svc.store.ListEvalRunEntryRefs(ctx, configstore.EvalRunEntryListParams{
			Statuses: []string{tables.EvalRunEntryQueued}, QueuedBefore: queuedBefore, KeyID: filter.keyID, ExcludeKeyIDs: filter.exclude, RoutedBefore: routedBefore,
			TargetTypes: filter.targets, Limit: limit,
		})
		if err != nil {
			return nil, err
		}
		for i := range entries {
			spec := specFor(entries[i].RunID)
			if spec == nil {
				continue
			}
			model := modelOf(spec.target)
			if unrouted && model == nil {
				continue // calls no model: it waits on the local topic by design
			}
			jobs = append(jobs, &evals.Job{ID: targetJobID(entries[i].ID, entries[i].Attempt+1), KeyID: entries[i].KeyID, Model: model, Exec: noop, RoutedAt: timeOf(entries[i].RoutedAt)})
		}
		// The unrouted pass leaves out results whose evaluator calls no model, for the same
		// reason as entries: they wait on the local topic by design.
		results, err := j.svc.store.ListEvalRunResultRefs(ctx, configstore.EvalRunResultListParams{
			Statuses: []string{tables.EvalRunResultQueued}, QueuedBefore: queuedBefore, KeyID: filter.keyID, ExcludeKeyIDs: filter.exclude, RoutedBefore: routedBefore,
			NeedsModel: unrouted, Limit: limit,
		})
		if err != nil {
			return nil, err
		}
		for i := range results {
			spec := specFor(results[i].RunID)
			if spec == nil {
				continue
			}
			if definition := spec.definition(results[i].EvaluatorID); definition != nil {
				jobs = append(jobs, &evals.Job{ID: resultJobID(results[i].ID, results[i].Attempt+1), KeyID: results[i].KeyID, Definition: *definition, RoutedAt: timeOf(results[i].RoutedAt)})
			}
		}
	}
	lost, err := j.lostStartedJobs(ctx, routedBefore, onDeadKey, limit, specFor)
	if err != nil {
		return nil, err
	}
	jobs = append(jobs, lost...)
	repairs, err := j.evaluatingOnDeadKeys(ctx, routedBefore, onDeadKey, limit)
	if err != nil {
		return nil, err
	}
	return append(jobs, repairs...), nil
}

// evaluatingOnDeadKeys lists entries left evaluating, since before the cutoff, by a target
// job on a key nothing consumes: the gateway stored the output and died before it created
// or queued the results, and on a durable queue nothing delivers that key's message again.
// Each is a repair job that calls no model and was never routed, with CreatedAt the time
// the entry started, so the sweep publishes it on the local topic once that is older than
// its lost-message cutoff. The repair finishes the results from the stored output.
func (j *JobStore) evaluatingOnDeadKeys(ctx context.Context, startedBefore time.Time, onDeadKey []string, limit int) ([]*evals.Job, error) {
	if startedBefore.IsZero() {
		return nil, nil
	}
	entries, err := j.svc.store.ListEvalRunEntryRefs(ctx, configstore.EvalRunEntryListParams{
		Statuses: []string{tables.EvalRunEntryEvaluating}, ExcludeKeyIDs: onDeadKey, StartedBefore: startedBefore, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	jobs := make([]*evals.Job, 0, len(entries))
	for i := range entries {
		jobs = append(jobs, &evals.Job{ID: repairJobID(entries[i].ID, entries[i].Attempt), Exec: noop, CreatedAt: timeOf(entries[i].StartedAt)})
	}
	return jobs, nil
}

// lostStartedJobs lists entries and results that started before the cutoff on a key that
// is not consumed: on a durable queue nothing subscribes to a removed key's topic, so
// their claim is long expired and no delivery will finish them. They carry their claim,
// so the runner ends them through a claim that only holds while they are still the same
// start, and a gateway that restarted the job wins.
func (j *JobStore) lostStartedJobs(ctx context.Context, startedBefore time.Time, onDeadKey []string, limit int, specFor func(string) *runSpec) ([]*evals.Job, error) {
	if startedBefore.IsZero() {
		return nil, nil
	}
	var jobs []*evals.Job
	entries, err := j.svc.store.ListEvalRunEntryRefs(ctx, configstore.EvalRunEntryListParams{
		Statuses: []string{tables.EvalRunEntryRunning}, ExcludeKeyIDs: onDeadKey, StartedBefore: startedBefore, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	for i := range entries {
		spec := specFor(entries[i].RunID)
		if spec == nil {
			continue
		}
		jobs = append(jobs, &evals.Job{
			ID: targetJobID(entries[i].ID, entries[i].Attempt), KeyID: entries[i].KeyID, Model: modelOf(spec.target), Exec: noop,
			Started: true, ClaimID: entries[i].ClaimID, StartedAt: timeOf(entries[i].StartedAt), RoutedAt: timeOf(entries[i].RoutedAt),
		})
	}
	results, err := j.svc.store.ListEvalRunResultRefs(ctx, configstore.EvalRunResultListParams{
		Statuses: []string{tables.EvalRunResultRunning}, ExcludeKeyIDs: onDeadKey, StartedBefore: startedBefore, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	for i := range results {
		spec := specFor(results[i].RunID)
		if spec == nil {
			continue
		}
		if definition := spec.definition(results[i].EvaluatorID); definition != nil {
			jobs = append(jobs, &evals.Job{
				ID: resultJobID(results[i].ID, results[i].Attempt), KeyID: results[i].KeyID, Definition: *definition,
				Started: true, ClaimID: results[i].ClaimID, StartedAt: timeOf(results[i].StartedAt), RoutedAt: timeOf(results[i].RoutedAt),
			})
		}
	}
	return jobs, nil
}

// timeOf is a stored time, or the zero time when none is stored.
func timeOf(at *time.Time) time.Time {
	if at == nil {
		return time.Time{}
	}
	return *at
}

// UnfinishedJobs returns what a queue that forgets its messages lost on a restart, a
// page at a time: the wrapped store's unfinished jobs, then, on the wrapped store's last
// page, one job for every active run. A run's job puts all of the run's work back, so
// its entries and results are not listed one by one.
//
// The wrapped store's cursor is passed through as it is, and its zero cursor (the last
// page) is this store's zero cursor too, so the walk ends. The page length says nothing:
// the wrapped store leaves out a row it cannot decode, so a short page can be followed by
// more.
func (j *JobStore) UnfinishedJobs(ctx context.Context, after evals.JobCursor, limit int) ([]*evals.Job, evals.JobCursor, error) {
	var jobs []*evals.Job
	if resumer, ok := j.inner.(evals.JobResumer); ok {
		inner, innerNext, err := resumer.UnfinishedJobs(ctx, after, limit)
		if err != nil {
			return nil, evals.JobCursor{}, err
		}
		if innerNext != (evals.JobCursor{}) {
			return inner, innerNext, nil // more of the wrapped store's jobs to come
		}
		jobs = inner
	}
	for _, status := range []string{tables.EvalRunQueued, tables.EvalRunRunning} {
		active, _, err := j.svc.store.ListEvalRuns(ctx, configstore.EvalRunListParams{Status: status})
		if err != nil {
			return nil, evals.JobCursor{}, err
		}
		for i := range active {
			jobs = append(jobs, &evals.Job{ID: runJobID(resumeJobPrefix, active[i].ID), Exec: noop})
		}
	}
	return jobs, evals.JobCursor{}, nil
}

var (
	_ evals.JobStore   = (*JobStore)(nil)
	_ evals.JobResumer = (*JobStore)(nil)
)

// PurgeJobs removes old API executions. A run's results are kept until the run is deleted.
func (j *JobStore) PurgeJobs(ctx context.Context, completedBefore time.Time) (int64, error) {
	return j.inner.PurgeJobs(ctx, completedBefore)
}

// ============================================================================
// Planning
// ============================================================================

func (s *Service) loadPlanJob(ctx context.Context, id string) (*evals.Job, error) {
	runID := strings.TrimPrefix(id, planJobPrefix)
	run, err := s.store.GetEvalRun(ctx, runID)
	if errors.Is(err, configstore.ErrNotFound) {
		return nil, evals.ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	// Planning is finished once the run has a start time. Until then a redelivered
	// message picks up where the last attempt stopped.
	if run.StartedAt != nil || (run.Status != tables.EvalRunQueued && run.Status != tables.EvalRunRunning) {
		return done(id), nil
	}
	return &evals.Job{ID: id, Exec: func(ctx context.Context) evals.Result {
		if err := s.plan(ctx, run.ID); err != nil {
			s.failRun(ctx, run.ID, err)
		}
		return evals.Result{Score: evals.NAScore()}
	}}, nil
}

// entryBatches holds the slices planning fills, one page of rows at a time.
var entryBatches = sync.Pool{New: func() any {
	batch := make([]tables.TableEvalRunEntry, 0, planPageSize)
	return &batch
}}

// resetEntryBatch empties a planning batch before it goes back to the pool.
// The whole capacity is cleared: plan shortens the slice for each page, so a short last
// page would leave the rows of a longer earlier one reachable from the pool.
func resetEntryBatch(batch *[]tables.TableEvalRunEntry) {
	clear((*batch)[:cap(*batch)])
	*batch = (*batch)[:0]
}

// plan creates a run's entries from its pinned dataset version and queues a target
// job for each. It reads and writes a page of rows at a time. It can be run again
// after a crash: it continues after the last entry it stored, and queueing an entry
// twice is harmless.
func (s *Service) plan(ctx context.Context, runID string) error {
	run, err := s.store.GetEvalRun(ctx, runID)
	if err != nil {
		return err
	}
	version, err := s.datasets.Version(ctx, run.DatasetID, run.DatasetVersion)
	if err != nil {
		return fmt.Errorf("the dataset version the run reads is gone: %w", err)
	}
	columns := datasets.VersionColumns(version)
	// Planning pages by position and never needs the total, so the count is skipped.
	query := datasets.RowQuery{Version: run.DatasetVersion, Limit: planPageSize, SkipTotal: true}
	if run.SplitID != nil {
		query.SplitID = *run.SplitID
	}
	if query.After, err = s.store.GetEvalRunLastSeq(ctx, runID); err != nil {
		return err
	}

	batch := entryBatches.Get().(*[]tables.TableEvalRunEntry)
	defer func() {
		resetEntryBatch(batch)
		entryBatches.Put(batch)
	}()
	for {
		page, err := s.datasets.Rows(ctx, run.DatasetID, query)
		if err != nil {
			return err
		}
		*batch = (*batch)[:0]
		for i := range page.Rows {
			row := &page.Rows[i]
			*batch = append(*batch, tables.TableEvalRunEntry{
				RunID: runID, Seq: row.Seq, DatasetRowID: row.RowID, Status: tables.EvalRunEntryQueued,
				Variables: datasets.RowVariables(columns, row.Cells),
			})
		}
		if err := s.store.CreateEvalRunEntries(ctx, *batch); errors.Is(err, configstore.ErrNotFound) {
			return nil // the run was deleted: nothing is left to plan
		} else if err != nil {
			return err
		}
		if page.NextAfter == 0 {
			break
		}
		query.After = page.NextAfter
		stopped, err := s.isStopped(ctx, runID)
		if err != nil {
			return err
		}
		if stopped {
			return s.endStoppedPlan(ctx, runID)
		}
	}

	counts, err := s.store.CountEvalRunEntries(ctx, runID)
	if err != nil {
		return err
	}
	var total int64
	for _, count := range counts {
		total += count
	}
	if total == 0 {
		return errors.New("the run has no rows to test: the dataset version, or the split, is empty")
	}
	now := time.Now()
	if _, err := s.store.TransitionEvalRun(ctx, runID, []string{tables.EvalRunQueued, tables.EvalRunRunning},
		map[string]any{"status": tables.EvalRunRunning, "total": total, "updated_at": now}); err != nil {
		return err
	}

	target, err := targetOf(run)
	if err != nil {
		return err
	}
	if err := s.submitWaiting(ctx, runID, target); err != nil {
		return err
	}
	if _, err := s.store.TransitionEvalRun(ctx, runID, nil, map[string]any{"started_at": now}); err != nil {
		return err
	}
	return s.maybeFinalize(ctx, runID)
}

// endStoppedPlan finishes planning for a run that was stopped while it was being
// planned. Stop ended the entries that existed then; a page stored after it is still
// waiting, and nothing will ever run it. Those entries are stopped here and the run is
// ended. The run also gets its start time: it marks planning as done, which a later
// rerun of the run needs in order to finish.
func (s *Service) endStoppedPlan(ctx context.Context, runID string) error {
	now := time.Now()
	if _, err := s.store.TransitionEvalRunEntries(ctx, runID, []string{tables.EvalRunEntryQueued}, nil,
		map[string]any{"status": tables.EvalRunEntryStopped, "ended_at": now}); err != nil {
		return err
	}
	if _, err := s.store.TransitionEvalRun(ctx, runID, nil, map[string]any{"started_at": now}); err != nil {
		return err
	}
	return s.maybeFinalize(ctx, runID)
}

// submitWaiting queues a target job for every waiting entry of a run. It pages by
// position and reads only what queueing needs, so it holds one page of small rows
// whatever the run's size. Entries start running while it is still going; paging by
// position, not by offset, is what keeps it from skipping rows as their status moves.
func (s *Service) submitWaiting(ctx context.Context, runID string, target Target) error {
	params := configstore.EvalRunEntryListParams{RunID: runID, Statuses: []string{tables.EvalRunEntryQueued}, Limit: planPageSize}
	for {
		entries, err := s.store.ListEvalRunEntryRefs(ctx, params)
		if err != nil {
			return err
		}
		for i := range entries {
			if err := s.submitTarget(ctx, target, entries[i].ID, entries[i].Attempt+1); err != nil {
				return err
			}
		}
		if len(entries) < planPageSize {
			return nil
		}
		params.AfterSeq = entries[len(entries)-1].Seq
	}
}

// failRun ends a run that could not be planned, or whose walk failed for good. The
// writes use a context that outlives the caller's: a request that was canceled must not
// leave the run queued, which a restart would plan again. Its waiting entries are
// stopped in the same call, so nothing calls the target for a run reported failed.
func (s *Service) failRun(ctx context.Context, runID string, cause error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failWriteTimeout)
	defer cancel()
	now := time.Now()
	failed, err := s.store.TransitionEvalRun(ctx, runID, []string{tables.EvalRunQueued, tables.EvalRunRunning},
		map[string]any{"status": tables.EvalRunFailed, "error": cause.Error(), "ended_at": now, "updated_at": now})
	if err != nil || !failed {
		return
	}
	_, _ = s.store.TransitionEvalRunEntries(ctx, runID, []string{tables.EvalRunEntryQueued}, nil,
		map[string]any{"status": tables.EvalRunEntryStopped, "ended_at": now})
}

// failWriteTimeout bounds the detached writes that record a failure.
const failWriteTimeout = 10 * time.Second

func (s *Service) isStopped(ctx context.Context, runID string) (bool, error) {
	state, err := s.store.GetEvalRunState(ctx, runID)
	if errors.Is(err, configstore.ErrNotFound) {
		return true, nil // deleted: there is nothing left to do for it
	}
	if err != nil {
		return false, err
	}
	return state.Status == tables.EvalRunStopped, nil
}

// ============================================================================
// Jobs that work on a whole run
// ============================================================================

// loadRunJob builds a job that walks a run's entries: queueing the entries of a
// rerun, or scoring stored outputs with evaluators that were added. The walk is not
// done in the request that asked for it, so the request returns at once whatever the
// run's size, and a gateway that dies mid-walk leaves a message that is delivered
// again. Every step of a walk is safe to repeat. A walk that fails is delivered again.
func (s *Service) loadRunJob(ctx context.Context, id, prefix string, walk func(ctx context.Context, runID string, spec *runSpec) error) (*evals.Job, error) {
	runID, ok := runOfJob(id, prefix)
	if !ok {
		return nil, evals.ErrJobNotFound
	}
	state, err := s.store.GetEvalRunState(ctx, runID)
	if errors.Is(err, configstore.ErrNotFound) {
		return nil, evals.ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	if state.Status != tables.EvalRunRunning {
		return done(id), nil // finished, or stopped: Stop ends the entries itself
	}
	spec, err := s.spec(ctx, runID, state.UpdatedAt)
	if err != nil {
		return nil, jobError(err)
	}
	var failed error
	ran := false
	return &evals.Job{
		ID: id,
		Exec: func(ctx context.Context) evals.Result {
			ran = true
			if failed = walk(ctx, runID, spec); failed == nil {
				failed = s.maybeFinalize(ctx, runID)
			}
			return evals.Result{Score: evals.NAScore()}
		},
		Complete: func(ctx context.Context, result evals.Result) error {
			return s.completeRunJob(ctx, runID, ran, failed, result)
		},
	}, nil
}

// completeRunJob completes a whole-run job. A job that ran returns what its walk
// returned, so a failed walk is delivered again. A job completed without running is the
// dead letter of one that failed every delivery: a walk keeps no cursor of the work left,
// so the run ends failed with the reason instead of staying running with nothing queued.
func (s *Service) completeRunJob(ctx context.Context, runID string, ran bool, failed error, result evals.Result) error {
	if ran {
		return failed
	}
	if result.Score.Status == evals.ScoreStatusError {
		reason := result.Reasoning
		if reason == "" {
			reason = "the run's work failed every delivery"
		}
		s.failRun(ctx, runID, errors.New(reason))
	}
	return nil
}

// loadResumeJob builds the job that puts an active run's work back on a queue that
// lost it. See resumeRun.
func (s *Service) loadResumeJob(ctx context.Context, id string) (*evals.Job, error) {
	runID, ok := runOfJob(id, resumeJobPrefix)
	if !ok {
		return nil, evals.ErrJobNotFound
	}
	state, err := s.store.GetEvalRunState(ctx, runID)
	if errors.Is(err, configstore.ErrNotFound) {
		return nil, evals.ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	if !isActive(state.Status) {
		return done(id), nil
	}
	var failed error
	ran := false
	return &evals.Job{
		ID: id,
		Exec: func(ctx context.Context) evals.Result {
			ran = true
			failed = s.resumeRun(ctx, runID)
			return evals.Result{Score: evals.NAScore()}
		},
		Complete: func(ctx context.Context, result evals.Result) error {
			return s.completeRunJob(ctx, runID, ran, failed, result)
		},
	}, nil
}

// loadRepairJob builds the job that finishes the results of an entry left evaluating at
// the attempt it names (see evaluatingOnDeadKeys). It is safe to repeat, and has no work
// once the entry has moved on.
func (s *Service) loadRepairJob(ctx context.Context, id string) (*evals.Job, error) {
	entryID, attempt, ok := splitJobID(id, repairJobPrefix)
	if !ok {
		return nil, evals.ErrJobNotFound
	}
	entry, err := s.store.GetEvalRunEntry(ctx, entryID)
	if errors.Is(err, configstore.ErrNotFound) {
		return nil, evals.ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	if entry.Status != tables.EvalRunEntryEvaluating || entry.Attempt != attempt {
		return done(id), nil
	}
	state, err := s.store.GetEvalRunState(ctx, entry.RunID)
	if errors.Is(err, configstore.ErrNotFound) {
		return done(id), nil
	}
	if err != nil {
		return nil, err
	}
	if state.Status == tables.EvalRunStopped || state.Status == tables.EvalRunFailed {
		return done(id), s.stopEntry(ctx, entry.RunID, entry.ID)
	}
	spec, err := s.spec(ctx, entry.RunID, state.UpdatedAt)
	if err != nil {
		return nil, jobError(err)
	}
	var failed error
	return &evals.Job{
		ID: id,
		Exec: func(ctx context.Context) evals.Result {
			failed = s.finishResults(ctx, entry.RunID, entry.ID, spec.definitions)
			return evals.Result{Score: evals.NAScore()}
		},
		Complete: func(context.Context, evals.Result) error { return failed },
	}, nil
}

// resumeRun queues everything an active run still has to do. It is for a queue that
// forgets its messages when the gateway stops: the run's entries and results stay
// stored where they were, with no message left to move them on.
//
//   - A run that was not planned to the end is planned first.
//   - A waiting entry is queued. An entry whose target was being called is queued for
//     the same attempt, so the call is made again.
//   - An entry that was being scored gets the results it lacks, and every result that
//     was waiting or being scored is queued again.
//
// Every step is safe to repeat, so the job can be delivered more than once.
func (s *Service) resumeRun(ctx context.Context, runID string) error {
	state, err := s.store.GetEvalRunState(ctx, runID)
	if err != nil {
		return jobError(err)
	}
	if state.StartedAt == nil {
		if err := s.plan(ctx, runID); err != nil {
			s.failRun(ctx, runID, err)
			return nil
		}
		if state, err = s.store.GetEvalRunState(ctx, runID); err != nil {
			return jobError(err)
		}
	}
	if state.Status != tables.EvalRunRunning {
		return nil
	}
	spec, err := s.spec(ctx, runID, state.UpdatedAt)
	if err != nil {
		return jobError(err)
	}
	params := configstore.EvalRunEntryListParams{
		RunID: runID, Limit: planPageSize,
		Statuses: []string{tables.EvalRunEntryQueued, tables.EvalRunEntryRunning, tables.EvalRunEntryEvaluating},
	}
	scoring := make([]string, 0, planPageSize)
	for {
		entries, err := s.store.ListEvalRunEntryRefs(ctx, params)
		if err != nil {
			return err
		}
		scoring = scoring[:0]
		for i := range entries {
			var err error
			switch entries[i].Status {
			case tables.EvalRunEntryQueued:
				err = s.resumeTarget(ctx, spec.target, entries[i], entries[i].Attempt+1)
			case tables.EvalRunEntryRunning:
				err = s.resumeTarget(ctx, spec.target, entries[i], max(entries[i].Attempt, 1))
			default:
				scoring = append(scoring, entries[i].ID)
			}
			if err != nil {
				return err
			}
		}
		idle, err := s.ensureResults(ctx, runID, scoring, spec.definitions, true)
		if err != nil {
			return err
		}
		for _, entryID := range idle {
			if err := s.entryProgress(ctx, runID, entryID); err != nil {
				return err
			}
		}
		if len(entries) < planPageSize {
			break
		}
		params.AfterSeq = entries[len(entries)-1].Seq
	}
	return s.maybeFinalize(ctx, runID)
}

// jobError turns a failure to read a job's run into what the runner should do with
// the message: drop it when the run is gone or cannot be decoded, and try again otherwise.
func jobError(err error) error {
	if errors.Is(err, configstore.ErrNotFound) || errors.Is(err, errBadRun) {
		return fmt.Errorf("%w: %v", evals.ErrJobNotFound, err)
	}
	return err
}

// queueWaiting queues the entries a rerun put back to waiting.
func (s *Service) queueWaiting(ctx context.Context, runID string, spec *runSpec) error {
	return s.submitWaiting(ctx, runID, spec.target)
}

// scoreStored gives every entry that has an output a result for each of the run's
// evaluators it has none for, and queues them. It is how evaluators added to a
// finished run are run: the target is not called again.
func (s *Service) scoreStored(ctx context.Context, runID string, spec *runSpec) error {
	params := configstore.EvalRunEntryListParams{
		RunID: runID, Statuses: []string{tables.EvalRunEntryCompleted, tables.EvalRunEntryEvaluating}, Limit: planPageSize,
	}
	ids := make([]string, 0, planPageSize)
	for {
		entries, err := s.store.ListEvalRunEntryRefs(ctx, params)
		if err != nil {
			return err
		}
		ids = ids[:0]
		for i := range entries {
			ids = append(ids, entries[i].ID)
		}
		idle, err := s.ensureResults(ctx, runID, ids, spec.definitions, false)
		if err != nil {
			return err
		}
		// An entry with nothing queued (its new evaluators are all scored by a
		// reviewer, or could not be queued) has nothing to wait for.
		for _, entryID := range idle {
			if err := s.entryProgress(ctx, runID, entryID); err != nil {
				return err
			}
		}
		if len(entries) < planPageSize {
			return nil
		}
		params.AfterSeq = entries[len(entries)-1].Seq
	}
}

// ============================================================================
// Target jobs
// ============================================================================

// modelOf is the model a target calls, which decides the eval key its job waits on.
// Dataset and HTTP targets call no model and run on the local topic.
func modelOf(target Target) *evals.ModelRef {
	if target.Type == TargetPrompt || target.Type == TargetModel {
		return &evals.ModelRef{Provider: target.Provider, Model: target.Model}
	}
	return nil
}

// submitTarget queues the target call of one entry. An entry that cannot be queued,
// because its provider has no eval key any more, fails with the reason.
// resumeTarget puts an entry's target call back on the queue after a restart of a queue
// that forgets. An entry already routed to a key, or running on one, goes back through
// Runner.Resume from that key; Submit would only move a job that was never routed, and
// would leave it waiting with nothing queued.
//
// Both return an error only when the job could not be queued and the failure could not
// be recorded either: the entry is then still waiting with nothing queued, so the job
// that was queueing it must be delivered again.
func (s *Service) resumeTarget(ctx context.Context, target Target, entry configstore.EvalRunEntryRef, attempt int) error {
	if entry.KeyID == "" && entry.Status == tables.EvalRunEntryQueued {
		return s.submitTarget(ctx, target, entry.ID, attempt)
	}
	job := &evals.Job{ID: targetJobID(entry.ID, attempt), Model: modelOf(target), Exec: noop, KeyID: entry.KeyID, Started: entry.Status == tables.EvalRunEntryRunning}
	if err := s.runner.Resume(ctx, job); err != nil {
		_, writeErr := s.store.TransitionEvalRunEntry(ctx, entry.ID, []string{tables.EvalRunEntryQueued, tables.EvalRunEntryRunning},
			map[string]any{"status": tables.EvalRunEntryFailed, "error": err.Error(), "ended_at": time.Now()})
		return writeErr
	}
	return nil
}

func (s *Service) submitTarget(ctx context.Context, target Target, entryID string, attempt int) error {
	err := s.runner.Submit(ctx, &evals.Job{ID: targetJobID(entryID, attempt), Model: modelOf(target), Exec: noop})
	if err != nil {
		_, writeErr := s.store.TransitionEvalRunEntry(ctx, entryID, []string{tables.EvalRunEntryQueued},
			map[string]any{"status": tables.EvalRunEntryFailed, "error": err.Error(), "ended_at": time.Now()})
		return writeErr
	}
	return nil
}

// failWaitingEntry fails an entry whose target call can no longer be made, with the
// reason, and lets the run finish if it was the last. The entry is waiting, or its call
// had started on a worker that died.
func (s *Service) failWaitingEntry(ctx context.Context, entryID, reason string) error {
	entries, err := s.store.ListEvalRunEntryRefs(ctx, configstore.EvalRunEntryListParams{IDs: []string{entryID}})
	if err != nil || len(entries) == 0 {
		return err
	}
	failed, err := s.store.TransitionEvalRunEntry(ctx, entryID, []string{tables.EvalRunEntryQueued, tables.EvalRunEntryRunning},
		map[string]any{"status": tables.EvalRunEntryFailed, "error": reason, "ended_at": time.Now()})
	if err != nil || !failed {
		return err
	}
	return s.maybeFinalize(ctx, entries[0].RunID)
}

func (s *Service) loadTargetJob(ctx context.Context, id string) (*evals.Job, error) {
	entryID, attempt, ok := splitJobID(id, targetJobPrefix)
	if !ok {
		return nil, evals.ErrJobNotFound
	}
	entry, err := s.store.GetEvalRunEntry(ctx, entryID)
	if errors.Is(err, configstore.ErrNotFound) {
		return nil, evals.ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	// An entry that is being scored was reached by this very message: its output is
	// stored. If the message is here again, the gateway that handled it may have died
	// before it created the entry's results, or before it queued them.
	unfinished := entry.Status == tables.EvalRunEntryEvaluating && entry.Attempt == attempt
	// A message of an earlier attempt, or of an entry that has moved on, has no work left.
	if !unfinished && (entry.Attempt > attempt || (entry.Status != tables.EvalRunEntryQueued && entry.Status != tables.EvalRunEntryRunning)) {
		return done(id), nil
	}
	state, err := s.store.GetEvalRunState(ctx, entry.RunID)
	if errors.Is(err, configstore.ErrNotFound) {
		return done(id), nil
	}
	if err != nil {
		return nil, err
	}
	if state.Status == tables.EvalRunStopped || state.Status == tables.EvalRunFailed {
		// A failed run ends its entries as a stopped one does: nothing calls the target
		// for a run reported failed.
		return done(id), s.stopEntry(ctx, entry.RunID, entry.ID)
	}
	spec, err := s.spec(ctx, entry.RunID, state.UpdatedAt)
	if err != nil {
		return nil, jobError(err)
	}
	if unfinished {
		var failed error
		return &evals.Job{
			ID: id, KeyID: entry.KeyID,
			Exec: func(ctx context.Context) evals.Result {
				failed = s.finishResults(ctx, entry.RunID, entry.ID, spec.definitions)
				return evals.Result{Score: evals.NAScore()}
			},
			Complete: func(context.Context, evals.Result) error { return failed },
		}, nil
	}
	var failed error
	return &evals.Job{
		ID: id, KeyID: entry.KeyID, Model: modelOf(spec.target), Started: entry.Status == tables.EvalRunEntryRunning,
		ClaimID: entry.ClaimID, StartedAt: timeOf(entry.StartedAt), RoutedAt: timeOf(entry.RoutedAt),
		Exec: func(ctx context.Context) evals.Result {
			failed = s.runTarget(ctx, spec, entry, attempt)
			return evals.Result{Score: evals.NAScore()}
		},
		// A delivery is acknowledged only once what the call produced is stored: a failed
		// write is delivered again. The runner completes a job it could not run (no key
		// left) with an error score, without Exec, and the entry then fails with the reason.
		Complete: func(ctx context.Context, result evals.Result) error {
			if failed != nil {
				return failed
			}
			if result.Score.Status == evals.ScoreStatusError {
				return s.failWaitingEntry(ctx, entry.ID, result.Reasoning)
			}
			return nil
		},
	}, nil
}

// stopEntry ends an entry of a stopped run and lets the run finish if it was the last.
func (s *Service) stopEntry(ctx context.Context, runID, entryID string) error {
	if _, err := s.store.TransitionEvalRunEntry(ctx, entryID,
		[]string{tables.EvalRunEntryQueued, tables.EvalRunEntryRunning, tables.EvalRunEntryEvaluating},
		map[string]any{"status": tables.EvalRunEntryStopped, "ended_at": time.Now()}); err != nil {
		return err
	}
	return s.maybeFinalize(ctx, runID)
}

// failEntry ends an entry whose target could not produce an output. The write is
// fenced on the attempt that made the call. See runTarget.
func (s *Service) failEntry(ctx context.Context, runID, entryID string, attempt int, latency time.Duration, cause error) error {
	failed, err := s.store.TransitionEvalRunEntryAttempt(ctx, entryID, attempt, []string{tables.EvalRunEntryRunning}, map[string]any{
		"status": tables.EvalRunEntryFailed, "error": cause.Error(), "target_latency_ms": latency.Milliseconds(), "ended_at": time.Now(),
	})
	if err != nil || !failed {
		return err
	}
	return s.maybeFinalize(ctx, runID)
}

// runTarget calls the target for one entry, stores what it returned, and queues the
// evaluators. The runner has already marked the entry as running at attempt.
//
// Every write of the call's outcome is fenced on attempt. A stop does not wait for a call
// in flight, and a rerun then starts the entry again at the next attempt; the late call
// of the earlier attempt must not store its output over the new one. It returns an error
// when the outcome could not be stored, so the delivery is not acknowledged.
func (s *Service) runTarget(ctx context.Context, spec *runSpec, entry *tables.TableEvalRunEntry, attempt int) error {
	runID := entry.RunID
	start := time.Now()
	out, err := s.callTarget(ctx, spec, entry.Variables)
	latency := time.Since(start)

	// A stop that arrived during the call wins: the output is not scored.
	if stopped, stopErr := s.isStopped(ctx, runID); stopErr == nil && stopped {
		return s.stopEntry(ctx, runID, entry.ID)
	}
	if err != nil {
		return s.failEntry(ctx, runID, entry.ID, attempt, latency, err)
	}

	fields := map[string]any{
		"status": tables.EvalRunEntryEvaluating, "output": out.Text, "target_latency_ms": latency.Milliseconds(), "error": "",
	}
	if len(out.ToolCalls) > 0 {
		fields["tool_calls_json"] = string(out.ToolCalls)
	}
	if out.Usage != nil {
		fields["target_prompt_tokens"] = out.Usage.PromptTokens
		fields["target_completion_tokens"] = out.Usage.CompletionTokens
		fields["target_total_tokens"] = out.Usage.TotalTokens
	}
	if out.Cost != nil {
		fields["target_cost"] = *out.Cost
	}
	moved, err := s.store.TransitionEvalRunEntryAttempt(ctx, entry.ID, attempt, []string{tables.EvalRunEntryRunning}, fields)
	if err != nil || !moved {
		return err // !moved: another delivery, or a later attempt, got here first
	}
	if err := s.startResults(ctx, runID, entry.ID, attempt, spec.definitions); err != nil {
		if _, writeErr := s.store.TransitionEvalRunEntryAttempt(ctx, entry.ID, attempt, []string{tables.EvalRunEntryEvaluating},
			map[string]any{"status": tables.EvalRunEntryFailed, "error": err.Error(), "ended_at": time.Now()}); writeErr != nil {
			return writeErr
		}
		return s.maybeFinalize(ctx, runID)
	}
	return nil
}

// startResults creates one result per evaluator for an entry that has none yet, and
// queues the ones a machine scores. A human evaluator's result waits for a reviewer
// and holds nothing up. If nothing is left to wait for, the entry completes at once.
//
// The results are created only while the entry is still evaluating at attempt: a rerun
// that reset the entry since runTarget stored its output has the store refuse them
// (ErrNotFound), so no result is left on an entry that is waiting again.
func (s *Service) startResults(ctx context.Context, runID, entryID string, attempt int, definitions []evals.Definition) error {
	results := make([]tables.TableEvalRunResult, len(definitions))
	for i := range definitions {
		results[i] = tables.TableEvalRunResult{RunID: runID, EntryID: entryID, EvaluatorID: definitions[i].ID, Status: resultStatus(definitions[i])}
	}
	if err := s.store.CreateEvalRunResults(ctx, results, attempt); errors.Is(err, configstore.ErrNotFound) {
		return nil // the run or the entry was deleted, or a rerun reset the entry
	} else if err != nil {
		// Another delivery of the entry's message may have created them first: an
		// entry has one result per evaluator. Finishing what exists settles it.
		if finishErr := s.finishResults(ctx, runID, entryID, definitions); finishErr != nil {
			return err
		}
		return nil
	}
	waiting := false
	for i := range definitions {
		if definitions[i].Type == evals.EvaluatorTypeHuman {
			continue
		}
		queued, err := s.submitResult(ctx, definitions[i], results[i].ID, 1)
		if err != nil {
			return err
		}
		waiting = waiting || queued
	}
	if waiting {
		return nil // the entry completes when the last of them does
	}
	return s.entryProgress(ctx, runID, entryID)
}

// resultStatus is the status an evaluator's result starts in.
func resultStatus(definition evals.Definition) string {
	if definition.Type == evals.EvaluatorTypeHuman {
		return tables.EvalRunResultAwaitingReview
	}
	return tables.EvalRunResultQueued
}

// finishResults brings one entry's results to where they should be, and completes the
// entry when nothing is left to wait for.
func (s *Service) finishResults(ctx context.Context, runID, entryID string, definitions []evals.Definition) error {
	idle, err := s.ensureResults(ctx, runID, []string{entryID}, definitions, false)
	if err != nil || len(idle) == 0 {
		return err
	}
	return s.entryProgress(ctx, runID, entryID)
}

// ensureResults gives each of the entries a result for every evaluator it has none
// for, and queues every result that is waiting. It returns the entries that are left
// with nothing queued or running.
//
// It is safe to repeat. A result that exists is not created again, and queueing one
// that is already queued does nothing: its message carries the same ID, and the queue
// keeps the first. That is what lets a message that is delivered again, after a
// gateway died partway through an entry, finish the entry.
//
// lostRunning says that results marked as running have no worker: the queue that
// carried them forgot its messages. They are queued again for the same attempt.
func (s *Service) ensureResults(ctx context.Context, runID string, entryIDs []string, definitions []evals.Definition, lostRunning bool) ([]string, error) {
	if len(entryIDs) == 0 {
		return nil, nil
	}
	existing, err := s.store.ListEvalRunResultRefs(ctx, configstore.EvalRunResultListParams{RunID: runID, EntryIDs: entryIDs})
	if err != nil {
		return nil, err
	}
	type pair struct{ entryID, evaluatorID string }
	have := make(map[pair]struct{}, len(existing))
	open := make(map[string]int, len(entryIDs))
	for i := range existing {
		have[pair{existing[i].EntryID, existing[i].EvaluatorID}] = struct{}{}
		if existing[i].Status == tables.EvalRunResultQueued || existing[i].Status == tables.EvalRunResultRunning {
			open[existing[i].EntryID]++
		}
	}

	var missing []tables.TableEvalRunResult
	var missingOf []int // the definition each missing result is for
	for _, entryID := range entryIDs {
		for i := range definitions {
			if _, ok := have[pair{entryID, definitions[i].ID}]; !ok {
				missing = append(missing, tables.TableEvalRunResult{RunID: runID, EntryID: entryID, EvaluatorID: definitions[i].ID, Status: resultStatus(definitions[i])})
				missingOf = append(missingOf, i)
			}
		}
	}
	if err := s.store.CreateEvalRunResults(ctx, missing, 0); errors.Is(err, configstore.ErrNotFound) {
		return nil, nil // the run, or an entry, was deleted: nothing is left to queue
	} else if err != nil {
		return nil, err
	}
	for i := range missing {
		if missing[i].Status != tables.EvalRunResultQueued {
			continue
		}
		queued, err := s.submitResult(ctx, definitions[missingOf[i]], missing[i].ID, 1)
		if err != nil {
			return nil, err
		}
		if queued {
			open[missing[i].EntryID]++
		}
	}
	for i := range existing {
		var attempt int
		switch {
		case existing[i].Status == tables.EvalRunResultQueued:
			attempt = existing[i].Attempt + 1
		case lostRunning && existing[i].Status == tables.EvalRunResultRunning:
			attempt = max(existing[i].Attempt, 1)
		default:
			continue
		}
		for j := range definitions {
			if definitions[j].ID != existing[i].EvaluatorID {
				continue
			}
			var queued bool
			var err error
			if lostRunning && existing[i].KeyID != "" {
				// Routed to a key before the restart: resume it from there.
				queued, err = s.resumeResult(ctx, definitions[j], existing[i], attempt)
			} else {
				queued, err = s.submitResult(ctx, definitions[j], existing[i].ID, attempt)
			}
			if err != nil {
				return nil, err
			}
			if !queued {
				open[existing[i].EntryID]--
			}
		}
	}
	var idle []string
	for _, entryID := range entryIDs {
		if open[entryID] <= 0 {
			idle = append(idle, entryID)
		}
	}
	return idle, nil
}

// resumeResult puts a result that was routed to a key back on the queue after a restart,
// through Runner.Resume, and reports whether it is queued. See submitResult.
func (s *Service) resumeResult(ctx context.Context, definition evals.Definition, result configstore.EvalRunResultRef, attempt int) (bool, error) {
	job := &evals.Job{ID: resultJobID(result.ID, attempt), Definition: definition, KeyID: result.KeyID, Started: result.Status == tables.EvalRunResultRunning}
	if err := s.runner.Resume(ctx, job); err != nil {
		_, writeErr := s.store.TransitionEvalRunResult(ctx, result.ID, []string{tables.EvalRunResultQueued, tables.EvalRunResultRunning},
			map[string]any{"status": tables.EvalRunResultFailed, "error": err.Error(), "completed_at": time.Now()})
		return false, writeErr
	}
	return true, nil
}

// submitResult queues one evaluator on one entry and reports whether it is queued. An
// evaluator that cannot be queued, because its judge's provider has no eval key any
// more, fails with the reason. When that failure cannot be recorded either, it returns
// the error: the result is still waiting with nothing queued, and the job that was
// queueing it must be delivered again.
func (s *Service) submitResult(ctx context.Context, definition evals.Definition, resultID string, attempt int) (bool, error) {
	// The definition rides along only so the runner can route the job to an eval key.
	// The worker loads the job again by its ID.
	err := s.runner.Submit(ctx, &evals.Job{ID: resultJobID(resultID, attempt), Definition: definition})
	if err != nil {
		_, writeErr := s.store.TransitionEvalRunResult(ctx, resultID, []string{tables.EvalRunResultQueued},
			map[string]any{"status": tables.EvalRunResultFailed, "error": err.Error(), "completed_at": time.Now()})
		return false, writeErr
	}
	return true, nil
}

// ============================================================================
// Result jobs
// ============================================================================

// evaluatorVariables is what an entry gives its evaluators: the row's variables, the
// target's output, its tool calls ("[]" when it made none) and its context.
func evaluatorVariables(entry *tables.TableEvalRunEntry) map[string]string {
	variables := make(map[string]string, len(entry.Variables)+3)
	for name, value := range entry.Variables {
		variables[name] = value
	}
	variables[VariableOutput] = entry.Output
	switch {
	case entry.ToolCalls != nil && *entry.ToolCalls != "":
		variables[VariableToolCalls] = *entry.ToolCalls
	case variables[VariableToolCalls] == "":
		variables[VariableToolCalls] = noToolCalls
	}
	if entry.Context != nil && *entry.Context != "" {
		variables[VariableContext] = *entry.Context
	}
	return variables
}

// resultRef names a result and what it belongs to. A result job carries it from the
// moment it is loaded, so completing the job does not read the result again.
type resultRef struct {
	id, runID, entryID, evaluatorID string
}

func (s *Service) loadResultJob(ctx context.Context, id string) (*evals.Job, error) {
	resultID, attempt, ok := splitJobID(id, resultJobPrefix)
	if !ok {
		return nil, evals.ErrJobNotFound
	}
	result, err := s.store.GetEvalRunResult(ctx, resultID)
	if errors.Is(err, configstore.ErrNotFound) {
		return nil, evals.ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	if result.Attempt == attempt && slices.Contains(terminalResultStatuses, result.Status) {
		// This attempt stored its result, and the step after it, the entry's progress,
		// may have failed. The redelivery finishes that step; both steps are no-ops when
		// the entry and the run have already moved on.
		var failed error
		return &evals.Job{
			ID: id, KeyID: result.KeyID,
			Exec: func(ctx context.Context) evals.Result {
				if failed = s.entryProgress(ctx, result.RunID, result.EntryID); failed == nil {
					failed = s.maybeFinalize(ctx, result.RunID)
				}
				return evals.Result{Score: evals.NAScore()}
			},
			Complete: func(context.Context, evals.Result) error { return failed },
		}, nil
	}
	if result.Attempt > attempt || (result.Status != tables.EvalRunResultQueued && result.Status != tables.EvalRunResultRunning) {
		return done(id), nil
	}
	state, err := s.store.GetEvalRunState(ctx, result.RunID)
	if errors.Is(err, configstore.ErrNotFound) {
		return done(id), nil
	}
	if err != nil {
		return nil, err
	}
	if state.Status == tables.EvalRunStopped || state.Status == tables.EvalRunFailed {
		_, _ = s.store.TransitionEvalRunResult(ctx, result.ID, []string{tables.EvalRunResultQueued, tables.EvalRunResultRunning},
			map[string]any{"status": tables.EvalRunResultFailed, "error": "the run was stopped", "completed_at": time.Now()})
		return done(id), s.stopEntry(ctx, result.RunID, result.EntryID)
	}
	spec, err := s.spec(ctx, result.RunID, state.UpdatedAt)
	if err != nil {
		return nil, jobError(err)
	}
	definition := spec.definition(result.EvaluatorID)
	entry, err := s.store.GetEvalRunEntry(ctx, result.EntryID)
	if definition == nil || errors.Is(err, configstore.ErrNotFound) {
		return nil, evals.ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	ref := resultRef{id: result.ID, runID: result.RunID, entryID: result.EntryID, evaluatorID: result.EvaluatorID}
	return &evals.Job{
		ID: id, KeyID: result.KeyID, Definition: *definition, Input: evals.Input{Variables: evaluatorVariables(entry)},
		Started: result.Status == tables.EvalRunResultRunning,
		ClaimID: result.ClaimID, StartedAt: timeOf(result.StartedAt), RoutedAt: timeOf(result.RoutedAt),
		Complete: func(ctx context.Context, outcome evals.Result) error {
			return s.completeResult(ctx, ref, attempt, outcome)
		},
	}, nil
}

// callsOut reports whether an evaluator reaches a service that can fail for a moment:
// a judge model or an HTTP endpoint. Only those are retried.
func callsOut(evaluatorType evals.EvaluatorType) bool {
	return evaluatorType == evals.EvaluatorTypeAI || evaluatorType == evals.EvaluatorTypeAPI
}

// retryResult queues a failed evaluator call again when it is one that may succeed
// the next time and the run is still going. It reports whether it took the result.
func (s *Service) retryResult(ctx context.Context, ref resultRef, attempt int, reason string) (bool, error) {
	state, err := s.store.GetEvalRunState(ctx, ref.runID)
	if err != nil || state.Status != tables.EvalRunRunning {
		return false, nil
	}
	spec, err := s.spec(ctx, ref.runID, state.UpdatedAt)
	if err != nil {
		return false, nil
	}
	definition := spec.definition(ref.evaluatorID)
	if definition == nil || !callsOut(definition.Type) {
		return false, nil
	}
	again, err := s.store.TransitionEvalRunResult(ctx, ref.id, []string{tables.EvalRunResultQueued, tables.EvalRunResultRunning},
		map[string]any{"status": tables.EvalRunResultQueued, "key_id": "", "error": reason, "queued_at": time.Now()})
	if err != nil {
		return false, err
	}
	if !again {
		return true, nil
	}
	queued, err := s.submitResult(ctx, *definition, ref.id, attempt+1)
	if err != nil {
		return true, err
	}
	if !queued {
		// It could not be queued and is marked failed: the entry may have nothing
		// left to wait for.
		return true, s.entryProgress(ctx, ref.runID, ref.entryID)
	}
	return true, nil
}

// completeResult stores what an evaluator returned for an entry. An evaluator that
// calls out and failed is queued again, up to maxResultAttempts times.
func (s *Service) completeResult(ctx context.Context, ref resultRef, attempt int, result evals.Result) error {
	if result.Score.Status == evals.ScoreStatusError && attempt < maxResultAttempts {
		if retried, err := s.retryResult(ctx, ref, attempt, result.Reasoning); retried || err != nil {
			return err
		}
	}

	fields := map[string]any{"latency_ms": result.LatencyMs, "reasoning": result.Reasoning, "completed_at": time.Now(), "error": ""}
	switch result.Score.Status {
	case evals.ScoreStatusOK:
		scoreJSON, err := providerUtils.MarshalSorted(result.Score)
		if err != nil {
			return err
		}
		fields["status"], fields["score_json"], fields["pass"] = tables.EvalRunResultCompleted, string(scoreJSON), result.Pass
		fields["score_number"] = scoreNumber(result.Score)
	case evals.ScoreStatusNA:
		fields["status"] = tables.EvalRunResultNotApplicable
	default:
		fields["status"], fields["error"] = tables.EvalRunResultFailed, result.Reasoning
	}
	if result.Usage != nil {
		fields["prompt_tokens"], fields["completion_tokens"], fields["total_tokens"] = result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens
	}
	if result.Cost != nil {
		fields["cost"] = *result.Cost
	}
	// Fenced on the attempt: a late completion of an attempt that a retry or a rerun has
	// moved past changes nothing.
	wrote, err := s.store.TransitionEvalRunResultAttempt(ctx, ref.id, attempt, []string{tables.EvalRunResultQueued, tables.EvalRunResultRunning}, fields)
	if err != nil {
		return err
	}
	if !wrote {
		// The runner ends a job that never started (no eval key left for it) without a
		// claim, so the stored attempt is still the one before. Such an end is stored while
		// no delivery of this attempt or a later one has claimed the result.
		if wrote, err = s.store.TransitionEvalRunResultUnstarted(ctx, ref.id, attempt, fields); err != nil || !wrote {
			return err // a duplicate delivery: the result is already stored
		}
	}
	return s.entryProgress(ctx, ref.runID, ref.entryID)
}

// entryVerdict reads an entry's results once and reports whether any is still being
// worked on, and whether the entry passed: true when every evaluator that has a
// verdict passed, false when one did not, and nil when none has a verdict.
func (s *Service) entryVerdict(ctx context.Context, runID, entryID string) (open bool, passed *bool, err error) {
	results, err := s.store.ListEvalRunResultRefs(ctx, configstore.EvalRunResultListParams{RunID: runID, EntryIDs: []string{entryID}})
	if err != nil {
		return false, nil, err
	}
	failed := false
	for i := range results {
		if results[i].Status == tables.EvalRunResultQueued || results[i].Status == tables.EvalRunResultRunning {
			open = true
		}
		if results[i].Pass == nil {
			continue
		}
		if !*results[i].Pass {
			failed = true
		}
		passed = new(true)
	}
	if failed {
		passed = new(false)
	}
	return open, passed, nil
}

// entryPassed reports whether an entry passed. See entryVerdict.
func (s *Service) entryPassed(ctx context.Context, runID, entryID string) (*bool, error) {
	_, passed, err := s.entryVerdict(ctx, runID, entryID)
	return passed, err
}

// entryProgress completes an entry once none of its results is still being worked
// on, and then lets the run finish if it was the last entry.
func (s *Service) entryProgress(ctx context.Context, runID, entryID string) error {
	open, passed, err := s.entryVerdict(ctx, runID, entryID)
	if err != nil || open {
		return err
	}
	completed, err := s.store.TransitionEvalRunEntry(ctx, entryID, []string{tables.EvalRunEntryEvaluating},
		map[string]any{"status": tables.EvalRunEntryCompleted, "passed": passed, "ended_at": time.Now()})
	if err != nil || !completed {
		return err
	}
	return s.maybeFinalize(ctx, runID)
}

// maybeFinalize ends a run once no entry is waiting or being worked on. Moving the
// run out of "running" is one conditional write, so of several workers that see the
// last entry finish, on any instance, exactly one writes the summary.
//
// It runs after every entry that ends, so the usual answer, "not yet", has to be
// cheap: one lookup that stops at the first open entry. The run is read, and its
// entries counted, only when none is left.
//
// The final write is fenced on the run as it was read, and any write to the run (a
// rename included) moves the fence. When the write matches nothing, the whole check is
// made again on a fresh read, up to finalizeTries times: a rerun or added evaluators then
// show open entries or a status that is not finishing, and anything else is finished
// with what the new read saw. When every try lost, the error has the delivery retried.
func (s *Service) maybeFinalize(ctx context.Context, runID string) error {
	for range finalizeTries {
		settled, err := s.finalizeOnce(ctx, runID)
		if err != nil || settled {
			return err
		}
	}
	return fmt.Errorf("run %s changed during each of %d attempts to finish it", runID, finalizeTries)
}

// finalizeTries bounds the fresh reads maybeFinalize makes when its fenced write loses.
const finalizeTries = 3

// finalizeOnce is one check of maybeFinalize. settled is false only when the fenced final
// write matched nothing because the run changed since it was read.
func (s *Service) finalizeOnce(ctx context.Context, runID string) (settled bool, err error) {
	open, err := s.store.HasOpenEvalRunEntries(ctx, runID)
	if err != nil || open {
		return true, err
	}
	state, err := s.store.GetEvalRunState(ctx, runID)
	if errors.Is(err, configstore.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	finishing := state.Status == tables.EvalRunRunning && state.StartedAt != nil
	stopping := state.Status == tables.EvalRunStopped && state.EndedAt == nil
	if !finishing && !stopping {
		return true, nil
	}
	spec, err := s.spec(ctx, runID, state.UpdatedAt)
	if err != nil {
		return true, err
	}
	// The final write is fenced on the run as it was read: a rerun or added evaluators
	// that marked the run running since then write updated_at, and this write must not
	// end work it did not see.
	now := time.Now()
	if stopping {
		return s.writeSummary(ctx, runID, spec.definitions, []string{tables.EvalRunStopped}, &state.UpdatedAt, map[string]any{"ended_at": now, "updated_at": now})
	}
	counts, err := s.store.CountEvalRunEntries(ctx, runID)
	if err != nil {
		return true, err
	}
	status := tables.EvalRunCompleted
	if counts[tables.EvalRunEntryCompleted] == 0 {
		status = tables.EvalRunFailed
	}
	return s.writeSummary(ctx, runID, spec.definitions, []string{tables.EvalRunRunning}, &state.UpdatedAt, map[string]any{"status": status, "ended_at": now, "updated_at": now})
}

// ============================================================================
// Calling the target
// ============================================================================

// targetOutput is what a target returned for one row.
type targetOutput struct {
	Text      string
	ToolCalls []byte
	Usage     *evals.Usage
	Cost      *float64
}

// callTarget produces the output for one row.
func (s *Service) callTarget(ctx context.Context, spec *runSpec, variables map[string]string) (targetOutput, error) {
	target := spec.target
	switch target.Type {
	case TargetDataset:
		if variables[VariableOutput] == "" {
			return targetOutput{}, errors.New("the row has no output to score")
		}
		return targetOutput{Text: variables[VariableOutput]}, nil
	case TargetPrompt, TargetModel:
		if s.model == nil {
			return targetOutput{}, errors.New("no model client is configured")
		}
		if spec.promptErr != nil {
			return targetOutput{}, spec.promptErr
		}
		messages, err := spec.prompt.render(variables)
		if err != nil {
			return targetOutput{}, err
		}
		response, err := s.model(ctx, evals.ModelCallRequest{Provider: target.Provider, Model: target.Model, Messages: messages, Params: target.Params})
		if err != nil {
			return targetOutput{}, err
		}
		if response.Text == "" && len(response.ToolCalls) == 0 {
			return targetOutput{}, errors.New("the model returned an empty response")
		}
		return targetOutput{Text: response.Text, ToolCalls: response.ToolCalls, Usage: response.Usage, Cost: response.Cost}, nil
	case TargetHTTP:
		// An HTTP target is called the way an API evaluator is: same templates, same
		// SSRF-safe client, same per-endpoint concurrency. Its "score" is the output.
		result := s.engine.Run(ctx, spec.httpTarget, evals.Input{Variables: variables})
		if result.Score.Status != evals.ScoreStatusOK {
			reason := result.Reasoning
			if reason == "" {
				reason = "the endpoint returned no output"
			}
			return targetOutput{}, errors.New(reason)
		}
		text := scoreText(result.Score)
		if text == "" {
			return targetOutput{}, errors.New("the endpoint returned an empty output")
		}
		return targetOutput{Text: text}, nil
	}
	return targetOutput{}, fmt.Errorf("unknown target type %q", target.Type)
}

// scoreNumber is a score as a number, when it has one: the number itself, or 1 and 0
// for yes and no. A label has none.
func scoreNumber(score evals.Score) *float64 {
	switch {
	case score.Number != nil:
		return score.Number
	case score.Bool != nil && *score.Bool:
		return new(1.0)
	case score.Bool != nil:
		return new(0.0)
	}
	return nil
}

// scoreText renders a value an API call returned as text.
func scoreText(score evals.Score) string {
	switch {
	case score.String != nil:
		return *score.String
	case score.Number != nil:
		return strconv.FormatFloat(*score.Number, 'f', -1, 64)
	case score.Bool != nil:
		return strconv.FormatBool(*score.Bool)
	}
	return ""
}

func textMessage(role schemas.ChatMessageRole, text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: &text}}
}
