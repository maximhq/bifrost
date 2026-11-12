package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// ExecutionStore is the part of the config store the runner needs for API executions.
type ExecutionStore interface {
	GetEvalExecution(ctx context.Context, id string) (*tables.TableEvalExecution, error)
	StartEvalExecution(ctx context.Context, id, keyID string) error
	CompleteEvalExecution(ctx context.Context, id string, resultJSON json.RawMessage) error
	RouteEvalExecution(ctx context.Context, id, fromKey, toKey string) (bool, error)
	ListQueuedEvalExecutions(ctx context.Context, createdBefore time.Time, limit int) ([]tables.TableEvalExecution, error)
	ListUnfinishedEvalExecutions(ctx context.Context) ([]tables.TableEvalExecution, error)
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
		ID: execution.ID, KeyID: execution.KeyID,
		Done: execution.Status == tables.EvalExecutionCompleted, Started: execution.Status == tables.EvalExecutionRunning,
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
// drops it. So is one waiting on a key that is being consumed.
func (s *executionJobStore) StaleJobs(ctx context.Context, queuedBefore time.Time, skipKeys []string, limit int) ([]*Job, error) {
	executions, err := s.store.ListQueuedEvalExecutions(ctx, queuedBefore, limit)
	if err != nil {
		return nil, err
	}
	jobs := make([]*Job, 0, len(executions))
	for i := range executions {
		if executions[i].KeyID != "" && slices.Contains(skipKeys, executions[i].KeyID) {
			continue
		}
		if job, err := jobOf(&executions[i]); err == nil {
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

// UnfinishedJobs returns every execution that has no result: queued, or started by a
// worker that never finished it.
func (s *executionJobStore) UnfinishedJobs(ctx context.Context) ([]*Job, error) {
	executions, err := s.store.ListUnfinishedEvalExecutions(ctx)
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

// PurgeJobs deletes executions that completed before the cutoff.
func (s *executionJobStore) PurgeJobs(ctx context.Context, completedBefore time.Time) (int64, error) {
	return s.store.DeleteCompletedEvalExecutions(ctx, completedBefore)
}

// StartJob marks the execution as running.
func (s *executionJobStore) StartJob(ctx context.Context, id, keyID string) error {
	return s.store.StartEvalExecution(ctx, id, keyID)
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
