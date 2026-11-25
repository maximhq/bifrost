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
	UnpriceableReasonRelayJobParams     = "relay_job_params"
	UnpriceableReasonRelayJobTimeout    = "relay_job_timeout"
	UnpriceableReasonRelayJobGone       = "relay_job_gone"
	UnpriceableReasonRelayJobDenied     = "relay_job_access_denied"
	UnpriceableReasonRelayJobRefused    = "relay_job_rejected"
	UnpriceableReasonRelayJobNoUsage    = "relay_job_no_usage"
	UnpriceableReasonRelayJobRowMissing = "relay_job_row_missing"
)

// endpointJobLogNamespace is the UUIDv5 namespace for relay settlement row ids.
var endpointJobLogNamespace = uuid.MustParse("6f1c2c58-3a3e-4a59-9a77-0b8f6e0d4b21")

func init() { RegisterLogNamespace(ProviderJobKindRelay, endpointJobLogNamespace) }

// relayPollFallback spaces polls for a job whose snapshot cannot be read.
const relayPollFallback = time.Minute

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

// NewRelayJobSweeper runs relay jobs on the shared sweeper. reporter may be nil when governance is off.
func NewRelayJobSweeper(store SweepStore, poller RelayJobPoller, logs RelayJobLogStore, reporter RelayUsageReporter, config SweeperConfig) *Sweeper {
	if config.ClaimedBy == "" {
		config.ClaimedBy = RunnerID("relay-sweeper", "")
	}
	settler := &RelaySettler{store: store, poller: poller, logs: logs, hasReporter: reporter != nil}
	return newSweeper(store, relayLogsRecorder{logs: logs, reporter: reporter}, nil, settler, nil, nil, config)
}

// relayVerdict is what a poll learned, for Settle to decide under the claim.
type relayVerdict struct {
	snap        *relay.JobSnapshot
	status      string
	succeeded   bool
	contentType string
	body        []byte
	reason      string // set when the job ended without a reply to price
	reasonErr   error

	usage   *schemas.RelayUsage // the priced usage, set by Settle
	written bool                // the settlement row already exists
}

// RelaySettler polls a relay job with its route's request and prices it with its route's usage.
type RelaySettler struct {
	store       JobStore
	poller      RelayJobPoller
	logs        RelayJobLogStore
	hasReporter bool
}

func (s *RelaySettler) Kind() ProviderJobKind { return ProviderJobKindRelay }

func (s *RelaySettler) SupportsProvider(schemas.ModelProvider) bool { return true }

// Backoff is the video ladder; the sweeper calls NextPollAt instead, which applies it to the job's own interval.
func (s *RelaySettler) Backoff(attempts int, interval time.Duration) time.Duration {
	return slowAfterManyPolls(attempts, interval)
}

// NextPollAt spaces polls at the job's own interval, slowing down after many attempts so a job left
// running for hours costs few calls.
func (s *RelaySettler) NextPollAt(job *cstables.TableProviderJob, now time.Time) time.Time {
	snap, err := relaySnapshot(job)
	if err != nil {
		return now.Add(relayPollFallback)
	}
	return now.Add(s.Backoff(job.PollAttempts, time.Duration(snap.Interval())*time.Second))
}

func relaySnapshot(job *cstables.TableProviderJob) (*relay.JobSnapshot, error) {
	if job.Params == nil {
		return nil, errors.New("job row carries no snapshot")
	}
	return relay.ParseJobSnapshot(*job.Params)
}

func relayPastDeadline(job *cstables.TableProviderJob, snap *relay.JobSnapshot) bool {
	return time.Since(job.CreatedAt) > time.Duration(snap.Timeout())*time.Second
}

// Poll asks the upstream for the job's status. It holds no claim, so every ending is handed to
// Settle: only there are the job's markers known to be current.
func (s *RelaySettler) Poll(ctx context.Context, listed *cstables.TableProviderJob) (*PollResult, error) {
	persisted, err := s.store.GetProviderJob(ctx, listed.ID)
	if err != nil {
		return nil, err
	}
	if persisted == nil {
		persisted = listed
	}
	// A full copy with no next check, so persisting it cannot rewind the job's schedule.
	job := *persisted
	job.NextCheckAt = nil

	if relayJobBusyOrDone(&job) {
		return &PollResult{Job: &job, Retry: true}, nil
	}
	settle := func(v *relayVerdict) (*PollResult, error) {
		return &PollResult{Job: &job, Terminal: true, Settleable: true, Payload: v}, nil
	}
	snap, snapErr := relaySnapshot(&job)
	if job.AggregateLogWrittenAt != nil {
		return settle(&relayVerdict{snap: snap})
	}
	if snapErr != nil {
		return settle(&relayVerdict{reason: UnpriceableReasonRelayJobParams, reasonErr: snapErr})
	}

	// Past its timeout a job still gets one last poll, so a job that finished just before the
	// deadline (or while the sweeper was behind) is charged rather than written off.
	pastDeadline := relayPastDeadline(&job, snap)
	pollCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	defer pollCtx.Cancel()
	upstreamID := schemas.RelayUpstreamJobID(job.JobID)
	resp, bErr := s.poller.PollJob(pollCtx, job.Provider, snap.PollRequest(upstreamID))
	if bErr == nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		status := resp.StatusCode
		if status == 400 && throttledReply(resp.Body) {
			// AWS JSON APIs (Textract among them) throttle with 400, which is otherwise terminal.
			status = 429
		}
		bErr = &schemas.BifrostError{StatusCode: schemas.Ptr(status), Error: &schemas.ErrorField{Message: fmt.Sprintf("job poll answered %d", resp.StatusCode)}}
	}
	if bErr != nil {
		// Every failure is retried until the timeout: a 404 from an eventually consistent API or a
		// 403 during a key rotation is often transient. Returned as an error so the sweeper logs it.
		if !pastDeadline {
			return nil, NewProviderCallError(bErr)
		}
		reason, pollErr := relayPollFailure(bErr)
		return settle(&relayVerdict{snap: snap, reason: reason, reasonErr: pollErr})
	}

	status := snap.Status(resp.Body)
	job.ProviderStatus = status
	done, succeeded := snap.Outcome(status)
	if !done {
		if !pastDeadline {
			return &PollResult{Job: &job, Retry: true}, nil
		}
		return settle(&relayVerdict{
			snap: snap, status: status, reason: UnpriceableReasonRelayJobTimeout,
			reasonErr: fmt.Errorf("job did not finish within %ds (last status %q)", snap.Timeout(), status),
		})
	}
	return settle(&relayVerdict{snap: snap, status: status, succeeded: succeeded, contentType: resp.ContentType, body: resp.Body})
}

// relayJobBusyOrDone reports a job another runner holds or has finished since it was listed.
func relayJobBusyOrDone(job *cstables.TableProviderJob) bool {
	switch job.AccountingStatus {
	case cstables.ProviderJobAccountingStatusAccounted, cstables.ProviderJobAccountingStatusUnpriceable:
		return true
	case cstables.ProviderJobAccountingStatusProcessing:
		return job.ClaimedAt != nil && time.Since(*job.ClaimedAt) < defaultClaimTTL
	}
	return false
}

// relayPollFailure names why a job's last poll failed.
func relayPollFailure(bErr *schemas.BifrostError) (string, error) {
	if bErr.Error != nil && errors.Is(bErr.Error.Error, relay.ErrRelayNotFound) {
		// A relay missing from the live set (mid reload, or pruned) is not the upstream forgetting the job.
		return UnpriceableReasonRelayJobTimeout, bErr.Error.Error
	}
	err := NewProviderCallError(bErr)
	switch ClassifyProviderCall(err) {
	case ProviderCallGone:
		return UnpriceableReasonRelayJobGone, err
	case ProviderCallAccessDenied:
		return UnpriceableReasonRelayJobDenied, err
	case ProviderCallRejected:
		return UnpriceableReasonRelayJobRefused, err
	}
	return UnpriceableReasonRelayJobTimeout, err
}

// Settle decides the job's price under the claim. A settlement row that already exists wins over
// whatever the latest poll said, so a written charge is never dropped.
func (s *RelaySettler) Settle(ctx context.Context, _ PricingManager, req JobRequest) (*Settlement, error) {
	v, ok := req.Payload.(*relayVerdict)
	if !ok || v == nil {
		return nil, fmt.Errorf("relay job %s has no poll verdict", req.Job.ID)
	}
	job := req.Job
	row, err := s.logs.FindRelayLog(ctx, relayRowID(job))
	switch {
	case err == nil:
		v.written = true
		if row.Cost == nil {
			// A row with no cost records a job that ended unpriced; a retry keeps that ending.
			reason, reasonErr := relayRowReason(row)
			return &Settlement{RecordUnpriced: true, UnpriceableReason: reason, ReasonErr: reasonErr}, nil
		}
		return &Settlement{Priced: true, Complete: true, Cost: *row.Cost}, nil
	case !errors.Is(err, logstore.ErrNotFound):
		return nil, fmt.Errorf("read settlement row: %w", err)
	}

	if job.AggregateLogWrittenAt != nil {
		return s.settleMissingRow(job, v)
	}
	if v.reason != "" {
		return &Settlement{RecordUnpriced: true, UnpriceableReason: v.reason, ReasonErr: v.reasonErr}, nil
	}
	v.usage = &schemas.RelayUsage{}
	if v.succeeded {
		usage, priced := v.snap.Price(v.contentType, v.body)
		if !priced {
			v.reason = UnpriceableReasonRelayJobNoUsage
			v.reasonErr = fmt.Errorf("job %s reported %s without the usage its pricing reads", schemas.RelayUpstreamJobID(job.JobID), v.status)
			return &Settlement{RecordUnpriced: true, UnpriceableReason: v.reason, ReasonErr: v.reasonErr}, nil
		}
		v.usage = usage
	}
	return &Settlement{Priced: true, Complete: true, Cost: v.usage.Cost}, nil
}

// relayRowReason reads back why an unpriced job row has no cost.
func relayRowReason(row *logstore.RelayLog) (string, error) {
	if d := row.ErrorDetailsParsed; d != nil && d.Error != nil && d.Error.Type != nil {
		return *d.Error.Type, errors.New(d.Error.Message)
	}
	return UnpriceableReasonRelayJobNoUsage, nil
}

// settleMissingRow handles a job marked written whose row was since deleted by a user or retention.
func (s *RelaySettler) settleMissingRow(job *cstables.TableProviderJob, v *relayVerdict) (*Settlement, error) {
	if job.GovernanceReportedAt != nil || !s.hasReporter {
		return &Settlement{Priced: true, Complete: true}, nil
	}
	missing := fmt.Errorf("settlement row for job %s is missing", job.ID)
	if v.snap != nil && !relayPastDeadline(job, v.snap) {
		return nil, missing
	}
	return &Settlement{UnpriceableReason: UnpriceableReasonRelayJobRowMissing, ReasonErr: missing}, nil
}

func (s *RelaySettler) HydrateFromLog(*logstore.Log, *Outcome) {}

// RepriceFromLog owns no LLM log row: relay costs live in the Relay Logs.
func (s *RelaySettler) RepriceFromLog(*logstore.Log, PricingManager) (*RepricedCost, error) {
	return nil, nil
}

func relayRowID(job *cstables.TableProviderJob) string {
	return AccountingLogID(ProviderJobKindRelay, schemas.ModelProvider(job.Provider), job.JobID)
}

// relayLogsRecorder logs a settled job in the Relay Logs and bills it through the relay reporter.
type relayLogsRecorder struct {
	logs     RelayJobLogStore
	reporter RelayUsageReporter
}

func (l relayLogsRecorder) Ready() bool { return l.logs != nil }

func (l relayLogsRecorder) SourceLog(context.Context, *cstables.TableProviderJob, *logstore.Log) *logstore.Log {
	return nil
}

func (l relayLogsRecorder) MirrorSettled(context.Context, Settler, *Outcome) {}

func (l relayLogsRecorder) Entry(req JobRequest, _ *Settlement, _ *Outcome, _ time.Time, _ string) LogsRecord {
	v, _ := req.Payload.(*relayVerdict)
	return &relayLogsRecord{logs: l.logs, reporter: l.reporter, job: req.Job, verdict: v}
}

type relayLogsRecord struct {
	logs     RelayJobLogStore
	reporter RelayUsageReporter
	job      *cstables.TableProviderJob
	verdict  *relayVerdict
	unpriced bool
}

// ClearCost leaves the row's cost blank: the job ended without a price, which is not $0.
func (e *relayLogsRecord) ClearCost() { e.unpriced = true }

// Write records the job's charge as a job row in the Relay Logs, carrying the start call's
// governance attribution so the charge is visible where the call was.
func (e *relayLogsRecord) Write(ctx context.Context) error {
	if e.verdict == nil {
		return fmt.Errorf("relay job %s has no poll verdict", e.job.ID)
	}
	if e.verdict.written {
		return nil
	}
	usage := e.verdict.usage
	if usage == nil {
		if !e.unpriced {
			return fmt.Errorf("relay job %s has no priced usage", e.job.ID)
		}
		usage = &schemas.RelayUsage{}
	}
	job := e.job
	jobStatus := e.verdict.status
	if jobStatus == "" {
		jobStatus = e.verdict.reason
	}
	id := relayRowID(job)
	entry := &logstore.RelayLog{
		ID:                 id,
		RequestID:          id,
		Timestamp:          settlementTimestamp(job),
		RelayName:          job.Provider,
		Kind:               logstore.RelayLogKindJob,
		Status:             logstore.RelayLogStatusSuccess,
		Billable:           true,
		JobID:              schemas.Ptr(schemas.RelayUpstreamJobID(job.JobID)),
		JobStatus:          schemas.Ptr(jobStatus),
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
	if e.unpriced {
		entry.Cost, entry.CostPerRequest, entry.CostPerUnit = nil, nil, nil
		message := e.verdict.reason
		if e.verdict.reasonErr != nil {
			message = e.verdict.reasonErr.Error()
		}
		entry.ErrorDetailsParsed = &schemas.BifrostError{Error: &schemas.ErrorField{Type: schemas.Ptr(e.verdict.reason), Message: message}}
	}
	_, err := e.logs.BatchCreateRelayLogsIfNotExists(ctx, []*logstore.RelayLog{entry})
	return err
}

func (e *relayLogsRecord) Emit(context.Context) {}

func (e *relayLogsRecord) CanReport() bool { return e.reporter != nil }

// Report charges what the row records, so an attempt resuming after a failure bills the same cost.
func (e *relayLogsRecord) Report(ctx context.Context) error {
	row, err := e.logs.FindRelayLog(ctx, relayRowID(e.job))
	if err != nil {
		return fmt.Errorf("read settlement row: %w", err)
	}
	cost := deref64(row.Cost)
	if cost <= 0 {
		return nil
	}
	return e.reporter.ReportRelayUsage(ctx, RelayUsageReport{
		RequestID: row.ID, RelayName: e.job.Provider, Cost: cost,
		BudgetIDs: decodeIDs(e.job.BudgetIDs), RateLimitIDs: decodeIDs(e.job.RateLimitIDs),
		VirtualKeyID: deref(e.job.VirtualKeyID), UserID: deref(e.job.UserID),
	})
}

// throttledReply reports whether a 400 reply names a throttling error, as AWS JSON APIs do.
func throttledReply(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, marker := range []string{"throttl", "toomanyrequests", "rate exceeded", "provisionedthroughputexceeded", "limitexceeded"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// settlementTimestamp is stable across retries: ClickHouse keys rows by (timestamp, id), so a retry
// writing a fresh timestamp would add a second copy of the same settlement.
func settlementTimestamp(job *cstables.TableProviderJob) time.Time {
	if job.CreatedAt.IsZero() {
		return time.Now().UTC()
	}
	return job.CreatedAt.UTC()
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
