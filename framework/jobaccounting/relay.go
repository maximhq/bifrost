package jobaccounting

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
)

// ProviderJobKindRelay is an async job a relay route started.
const ProviderJobKindRelay ProviderJobKind = cstables.ProviderJobKindRelay

// Reasons a relay job ends unpriced.
const (
	UnpriceableReasonRelayJobParams  = "endpoint_job_params"
	UnpriceableReasonRelayJobTimeout = "endpoint_job_timeout"
	UnpriceableReasonRelayJobGone    = "endpoint_job_gone"
	UnpriceableReasonRelayJobDenied  = "endpoint_job_access_denied"
	UnpriceableReasonRelayJobRefused = "endpoint_job_rejected"
)

// endpointJobLogNamespace derives the settlement row's id from the job row's id, so a settlement
// retried after a crash writes (and charges) the same row once.
var endpointJobLogNamespace = uuid.MustParse("6f1c2c58-3a3e-4a59-9a77-0b8f6e0d4b21")

// RelayJobPoller sends a job poll with the endpoint's credential. *relay.Manager
// satisfies it.
type RelayJobPoller interface {
	PollJob(ctx *schemas.BifrostContext, endpointName string, poll relay.JobPollRequest) (*relay.JobPollResponse, *schemas.BifrostError)
}

// RelayJobLogStore writes the settlement row and reads it back. logstore.LogStore satisfies it.
type RelayJobLogStore interface {
	BatchCreateRelayLogsIfNotExists(ctx context.Context, entries []*logstore.RelayLog) ([]string, error)
	FindRelayLog(ctx context.Context, id string) (*logstore.RelayLog, error)
}

// RelayUsageReport is a settled job's charge, for governance. It carries no request count: the
// start call already counted against the request rate limits.
type RelayUsageReport struct {
	RequestID    string // the settlement row id; reporters must be idempotent on it
	RelayName    string
	Cost         float64
	BudgetIDs    []string
	RateLimitIDs []string
	VirtualKeyID string
	UserID       string
}

// RelayUsageReporter charges a settled job's cost to the budgets the start call was subject to.
type RelayUsageReporter interface {
	ReportRelayUsage(ctx context.Context, report RelayUsageReport) error
}

// RelayJobSweeperConfig tunes the sweeper.
type RelayJobSweeperConfig struct {
	Interval  time.Duration
	Limit     int
	ClaimedBy string
	Logger    schemas.Logger
}

// RelayJobSweeper polls the jobs relay routes started until each reaches a
// terminal status, then prices it with the job's usage mapping, writes one settlement row to the
// Relay Logs and charges the cost. Every step is fenced and idempotent, so concurrent nodes
// and retries settle a job exactly once.
type RelayJobSweeper struct {
	store    SweepStore
	poller   RelayJobPoller
	logs     RelayJobLogStore
	reporter RelayUsageReporter
	config   RelayJobSweeperConfig
}

// NewRelayJobSweeper builds a sweeper; reporter may be nil when governance is off.
func NewRelayJobSweeper(store SweepStore, poller RelayJobPoller, logs RelayJobLogStore, reporter RelayUsageReporter, config RelayJobSweeperConfig) *RelayJobSweeper {
	if config.Interval <= 0 {
		config.Interval = 30 * time.Second
	}
	if config.Limit <= 0 {
		config.Limit = defaultSweepLimit
	}
	if config.ClaimedBy == "" {
		config.ClaimedBy = "endpoint-job-sweeper:" + newInstanceID()
	}
	return &RelayJobSweeper{store: store, poller: poller, logs: logs, reporter: reporter, config: config}
}

// Run sweeps on the configured interval until ctx is done.
func (s *RelayJobSweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()
	for {
		if err := s.SweepOnce(ctx); err != nil && s.config.Logger != nil {
			s.config.Logger.Warn("relay job sweep failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// SweepOnce handles every job due now.
func (s *RelayJobSweeper) SweepOnce(ctx context.Context) error {
	jobs, err := s.store.ListDueProviderJobs(ctx, string(ProviderJobKindRelay), "", time.Now().UTC(), s.config.Limit)
	if err != nil {
		return err
	}
	var errs []error
	for _, job := range jobs {
		if err := s.handle(ctx, job); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", job.ID, err))
		}
	}
	return errors.Join(errs...)
}

// endpointJobBackoff spaces polls at the job's own interval, slowing down after many attempts so a
// job left running for hours costs few calls.
func endpointJobBackoff(attempts, interval int) time.Duration {
	d := time.Duration(interval) * time.Second
	switch {
	case attempts >= 40:
		d = max(d, 5*time.Minute)
	case attempts >= 20:
		d = max(d, 2*time.Minute)
	}
	return d
}

func (s *RelayJobSweeper) handle(ctx context.Context, job *cstables.TableProviderJob) error {
	runner := s.config.ClaimedBy
	claimed, err := s.store.ClaimProviderJob(ctx, job.ID, runner, time.Now().UTC().Add(-defaultClaimTTL), false)
	if err != nil || !claimed {
		return err
	}
	// The listed row may predate another node's progress (a settlement row written or budgets
	// charged just before it stopped), so the markers are read again after the claim, as
	// AccountJob does. If the read fails, the claim is released rather than risk a second charge.
	persisted, err := s.store.GetProviderJob(ctx, job.ID)
	if err != nil {
		return errors.Join(err, s.store.FailProviderJob(ctx, job.ID, runner, err))
	}
	if persisted != nil {
		job = persisted
	}
	if job.Params == nil {
		return s.store.MarkProviderJobUnpriceable(ctx, job.ID, runner, UnpriceableReasonRelayJobParams, errors.New("job row carries no snapshot"))
	}
	snap, err := relay.ParseJobSnapshot(*job.Params)
	if err != nil {
		return s.store.MarkProviderJobUnpriceable(ctx, job.ID, runner, UnpriceableReasonRelayJobParams, err)
	}

	// A settlement interrupted after its row was written is finished without polling again.
	if job.AggregateLogWrittenAt == nil && time.Since(job.CreatedAt) > time.Duration(snap.Timeout())*time.Second {
		// The row may have been written just before a worker stopped; that settlement is finished
		// rather than dropped. The probe only runs once a job is past its timeout.
		_, err := s.logs.FindRelayLog(ctx, s.settlementRowID(job))
		switch {
		case errors.Is(err, logstore.ErrNotFound):
			return s.store.MarkProviderJobUnpriceable(ctx, job.ID, runner, UnpriceableReasonRelayJobTimeout, fmt.Errorf("job did not finish within %ds", snap.Timeout()))
		case err != nil:
			err = fmt.Errorf("read settlement row: %w", err)
			return errors.Join(err, s.store.FailProviderJob(ctx, job.ID, runner, err))
		}
		if err := s.store.MarkProviderJobAggregateLogWritten(ctx, job.ID, runner); err != nil {
			return err
		}
		job.AggregateLogWrittenAt = schemas.Ptr(time.Now().UTC())
	}
	if job.AggregateLogWrittenAt == nil {
		pollCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		defer pollCtx.Cancel()
		upstreamID := schemas.RelayUpstreamJobID(job.JobID)
		resp, bErr := s.poller.PollJob(pollCtx, job.Provider, snap.PollRequest(upstreamID))
		if bErr == nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
			bErr = &schemas.BifrostError{StatusCode: schemas.Ptr(resp.StatusCode), Error: &schemas.ErrorField{Message: fmt.Sprintf("job poll answered %d", resp.StatusCode)}}
		}
		if bErr != nil {
			return s.pollFailed(ctx, job, snap, NewProviderCallError(bErr))
		}
		status := snap.Status(resp.Body)
		done, succeeded := snap.Outcome(status)
		if !done {
			attempts := job.PollAttempts + 1
			next := time.Now().UTC().Add(endpointJobBackoff(attempts, snap.Interval()))
			if err := s.store.UpsertProviderJob(ctx, &cstables.TableProviderJob{ID: job.ID, Kind: job.Kind, Provider: job.Provider, JobID: job.JobID, ProviderStatus: status, NextCheckAt: &next, PollAttempts: attempts}); err != nil {
				return err
			}
			return s.store.FailProviderJob(ctx, job.ID, runner, nil) // releases the claim; due again at next_check_at
		}
		usage := &schemas.RelayUsage{}
		if succeeded {
			usage = snap.Price(resp.ContentType, resp.Body)
		}
		if err := s.writeSettlementRow(ctx, job, upstreamID, status, usage); err != nil {
			return errors.Join(err, s.store.FailProviderJob(ctx, job.ID, runner, err))
		}
		if err := s.store.MarkProviderJobAggregateLogWritten(ctx, job.ID, runner); err != nil {
			return err
		}
	}

	if job.GovernanceReportedAt == nil && s.reporter != nil {
		// The charge is read back from the settlement row, so an attempt resuming after a crash
		// reports exactly what the row records.
		row, err := s.logs.FindRelayLog(ctx, s.settlementRowID(job))
		if err != nil {
			err = fmt.Errorf("read settlement row: %w", err)
			return errors.Join(err, s.store.FailProviderJob(ctx, job.ID, runner, err))
		}
		if cost := deref64(row.Cost); cost > 0 {
			report := RelayUsageReport{
				RequestID: row.ID, RelayName: job.Provider, Cost: cost,
				BudgetIDs: decodeIDs(job.BudgetIDs), RateLimitIDs: decodeIDs(job.RateLimitIDs),
				VirtualKeyID: deref(job.VirtualKeyID), UserID: deref(job.UserID),
			}
			if err := s.reporter.ReportRelayUsage(ctx, report); err != nil {
				return errors.Join(err, s.store.FailProviderJob(ctx, job.ID, runner, err))
			}
		}
		if err := s.store.MarkProviderJobGovernanceReported(ctx, job.ID, runner); err != nil {
			return err
		}
	}
	return s.store.CompleteProviderJob(ctx, job.ID, runner)
}

func (s *RelayJobSweeper) pollFailed(ctx context.Context, job *cstables.TableProviderJob, snap *relay.JobSnapshot, err error) error {
	runner := s.config.ClaimedBy
	switch ClassifyProviderCall(err) {
	case ProviderCallGone:
		return s.store.MarkProviderJobUnpriceable(ctx, job.ID, runner, UnpriceableReasonRelayJobGone, err)
	case ProviderCallAccessDenied:
		return s.store.MarkProviderJobUnpriceable(ctx, job.ID, runner, UnpriceableReasonRelayJobDenied, err)
	case ProviderCallRejected:
		return s.store.MarkProviderJobUnpriceable(ctx, job.ID, runner, UnpriceableReasonRelayJobRefused, err)
	}
	attempts := job.PollAttempts + 1
	next := time.Now().UTC().Add(endpointJobBackoff(attempts, snap.Interval()))
	if upErr := s.store.UpsertProviderJob(ctx, &cstables.TableProviderJob{ID: job.ID, Kind: job.Kind, Provider: job.Provider, JobID: job.JobID, NextCheckAt: &next, PollAttempts: attempts}); upErr != nil {
		return upErr
	}
	return s.store.FailProviderJob(ctx, job.ID, runner, err)
}

// settlementTimestamp is stable across retries: ClickHouse keys rows by (timestamp, id), so a retry
// writing a fresh timestamp would add a second copy of the same settlement.
func settlementTimestamp(job *cstables.TableProviderJob) time.Time {
	if job.CreatedAt.IsZero() {
		return time.Now().UTC()
	}
	return job.CreatedAt.UTC()
}

func (s *RelayJobSweeper) settlementRowID(job *cstables.TableProviderJob) string {
	return uuid.NewSHA1(endpointJobLogNamespace, []byte(job.ID)).String()
}

// writeSettlementRow records the job's charge as a job row in the Relay Logs, carrying the
// start call's governance attribution so the charge is visible where the call was.
func (s *RelayJobSweeper) writeSettlementRow(ctx context.Context, job *cstables.TableProviderJob, upstreamID, status string, usage *schemas.RelayUsage) error {
	id := s.settlementRowID(job)
	entry := &logstore.RelayLog{
		ID:                 id,
		RequestID:          id,
		Timestamp:          settlementTimestamp(job),
		RelayName:          job.Provider,
		Kind:               logstore.RelayLogKindJob,
		Status:             logstore.RelayLogStatusSuccess,
		Billable:           true,
		JobID:              schemas.Ptr(upstreamID),
		JobStatus:          schemas.Ptr(status),
		ParentLogID:        job.SourceLogID,
		VirtualKeyID:       job.VirtualKeyID,
		UserID:             job.UserID,
		TeamID:             job.TeamID,
		CustomerID:         job.CustomerID,
		BudgetIDsParsed:    decodeIDs(job.BudgetIDs),
		RateLimitIDsParsed: decodeIDs(job.RateLimitIDs),
		Cost:               schemas.Ptr(usage.Cost),
		CostPerRequest:     schemas.Ptr(usage.CostPerRequest),
		CostPerUnit:        schemas.Ptr(usage.CostPerUnit),
		ExtractedCost:      usage.ExtractedCost,
		Units:              usage.Units,
		BilledUnits:        usage.BilledUnits,
		InputTokens:        usage.InputTokens,
		OutputTokens:       usage.OutputTokens,
		TotalTokens:        usage.TotalTokens,
	}
	_, err := s.logs.BatchCreateRelayLogsIfNotExists(ctx, []*logstore.RelayLog{entry})
	return err
}

func decodeIDs(raw *string) []string {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}
	var ids []string
	if err := sonic.Unmarshal([]byte(*raw), &ids); err != nil {
		return nil
	}
	return ids
}

func deref64(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
