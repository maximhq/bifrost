package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// ExecutionStore is the part of the config store the runner needs for API executions.
type ExecutionStore interface {
	GetEvalExecution(ctx context.Context, id string) (*tables.TableEvalExecution, error)
	StartEvalExecution(ctx context.Context, id, keyID, claimID string, reclaimStartedBefore, recoverStartedAt time.Time) (bool, error)
	CompleteEvalExecution(ctx context.Context, id string, resultJSON json.RawMessage) error
	RouteEvalExecution(ctx context.Context, id, fromKey, toKey string) (bool, error)
	ListQueuedEvalExecutions(ctx context.Context, createdBefore, routedBefore time.Time, skipKeys []string, limit int) ([]tables.TableEvalExecution, error)
	ListUnfinishedEvalExecutions(ctx context.Context, afterCreatedAt time.Time, afterID string, limit int) ([]tables.TableEvalExecution, error)
	DeleteCompletedEvalExecutions(ctx context.Context, completedBefore time.Time) (int64, error)
}

// NewExecutionJobStore adapts stored executions to the runner's JobStore.
func NewExecutionJobStore(store ExecutionStore) JobStore {
	return &executionJobStore{store: store}
}

type executionJobStore struct {
	store ExecutionStore
}

// LoadJob reads an execution and decodes its definition snapshot and variables.
func (s *executionJobStore) LoadJob(ctx context.Context, id string) (*Job, error) {
	execution, err := s.store.GetEvalExecution(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			return nil, ErrJobNotFound
		}
		return nil, err
	}
	return jobOf(execution)
}

// jobOf decodes an execution's definition snapshot and variables into a job.
func jobOf(execution *tables.TableEvalExecution) (*Job, error) {
	job := &Job{
		ID: execution.ID, KeyID: execution.KeyID, CreatedAt: execution.CreatedAt, ClaimID: execution.ClaimID,
		Done: execution.Status == tables.EvalExecutionCompleted, Started: execution.Status == tables.EvalExecutionRunning,
	}
	if execution.StartedAt != nil {
		job.StartedAt = *execution.StartedAt
	}
	if execution.RoutedAt != nil {
		job.RoutedAt = *execution.RoutedAt
	}
	// A row that does not decode will not decode on a retry either.
	if err := json.Unmarshal(execution.Definition, &job.Definition); err != nil {
		return nil, fmt.Errorf("%w: execution %s has an unreadable definition: %v", ErrJobNotFound, execution.ID, err)
	}
	if len(execution.Variables) > 0 {
		if err := json.Unmarshal(execution.Variables, &job.Input.Variables); err != nil {
			return nil, fmt.Errorf("%w: execution %s has unreadable variables: %v", ErrJobNotFound, execution.ID, err)
		}
	}
	return job, nil
}

// RouteJob records the key whose queue a waiting execution is on.
func (s *executionJobStore) RouteJob(ctx context.Context, id, fromKey, toKey string) (bool, error) {
	return s.store.RouteEvalExecution(ctx, id, fromKey, toKey)
}

// StaleJobs returns executions that have waited since before the cutoff. One that no
// longer decodes is left out: it cannot be run, and the handler of its own message
// drops it. The store already leaves out one waiting on a key that is being consumed,
// unless it was routed there before routedBefore, so every row it returns is passed on:
// such a row is a job whose message may have been lost, or one that started on a key
// nothing consumes and whose claim is long expired.
func (s *executionJobStore) StaleJobs(ctx context.Context, queuedBefore, routedBefore time.Time, skipKeys []string, limit int) ([]*Job, error) {
	executions, err := s.store.ListQueuedEvalExecutions(ctx, queuedBefore, routedBefore, skipKeys, limit)
	if err != nil {
		return nil, err
	}
	jobs := make([]*Job, 0, len(executions))
	for i := range executions {
		if job, err := jobOf(&executions[i]); err == nil {
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

// UnfinishedJobs returns a page of the executions that have no result: queued, or
// started by a worker that never finished it. The cursor is the last row read, so a row
// that does not decode still moves it; it is the zero cursor on a short page, the last.
func (s *executionJobStore) UnfinishedJobs(ctx context.Context, after JobCursor, limit int) ([]*Job, JobCursor, error) {
	executions, err := s.store.ListUnfinishedEvalExecutions(ctx, after.CreatedAt, after.ID, limit)
	if err != nil {
		return nil, JobCursor{}, err
	}
	jobs := make([]*Job, 0, len(executions))
	for i := range executions {
		if job, err := jobOf(&executions[i]); err == nil {
			jobs = append(jobs, job)
		}
	}
	if limit <= 0 || len(executions) < limit {
		return jobs, JobCursor{}, nil
	}
	last := executions[len(executions)-1]
	return jobs, JobCursor{CreatedAt: last.CreatedAt, ID: last.ID}, nil
}

// PurgeJobs deletes executions that completed before the cutoff.
func (s *executionJobStore) PurgeJobs(ctx context.Context, completedBefore time.Time) (int64, error) {
	return s.store.DeleteCompletedEvalExecutions(ctx, completedBefore)
}

// StartJob marks the execution as running.
func (s *executionJobStore) StartJob(ctx context.Context, id, keyID string, claim JobClaim) (bool, error) {
	return s.store.StartEvalExecution(ctx, id, keyID, claim.DeliveryID, claim.StartedBefore, claim.RecoverStartedAt)
}

// CompleteJob stores the result.
func (s *executionJobStore) CompleteJob(ctx context.Context, id string, result Result) error {
	data, err := providerUtils.MarshalSorted(result)
	if err != nil {
		return err
	}
	return s.store.CompleteEvalExecution(ctx, id, data)
}

// DecodeResult reads a result stored by CompleteJob.
func DecodeResult(raw json.RawMessage) (Result, error) {
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return Result{}, err
	}
	return result, nil
}
