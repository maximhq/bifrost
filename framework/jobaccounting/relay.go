package jobaccounting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
	settler := &RelaySettler{store: store, poller: poller, logs: logs, hasReporter: reporter != nil, logger: config.Logger}
	return newSweeper(store, relayLogsRecorder{logs: logs, reporter: reporter}, nil, settler, nil, nil, config)
}

// MaxRelayJobGenerations bounds how many times one upstream job id is reused after settlement
// before a start is keyed by its own request instead (reused_id_is_new_job).
const MaxRelayJobGenerations = 64

// RelayJobRowID is the coordination row id of a relay job, and its scoped job id, for generation
// gen of an upstream id: the first start takes generation 1, a reused id after settlement the next.
func RelayJobRowID(relayName, routeKey, upstreamID string, gen int) (id, scoped string) {
	// The start route is part of the key, so two job routes whose upstreams hand out the same ids
	// (each counting 1, 2, 3) never share a row.
	prefix := routeKey
	if gen > 1 {
		prefix += "g" + strconv.Itoa(gen)
	}
	scoped = schemas.RelayScopedJobID(prefix, upstreamID)
	return cstables.ProviderJobID(cstables.ProviderJobKindRelay, relayName, scoped), scoped
}

// RelayStatusCheckPayload is a caller's status check reply, for AccountRelayJob.
type RelayStatusCheckPayload struct {
	StatusCode  int
	ContentType string
	Body        []byte
	RequestID   string    // the caller's Relay Logs row
	SentAt      time.Time // when the caller's check started, to tell which generation it asked about
	// Matches are the open jobs' routes whose templates the caller's check matched. A job settles
	// only from its own route and template, so another job's check with the same id, or one from a
	// template changed since the start, does not.
	Matches []schemas.RelayJobMatch
	Logger  schemas.Logger // optional; where a settlement problem is reported
}

// AccountRelayJob settles a relay job a caller's status check found finished, as AccountVideoJob
// settles a video from a terminal retrieve, rather than waiting for the sweeper. The caller's
// reply only says the job looks finished: Bifrost polls it itself with the stored template and
// prices that reply, so a caller cannot shape what is charged. It never creates a job row, and an
// already-settled job takes the engine's mirror path. With no poller the sweeper settles it.
func AccountRelayJob(ctx context.Context, stateStore JobStore, logs RelayJobLogStore, reporter RelayUsageReporter, poller RelayJobPoller, req JobRequest) (*Outcome, error) {
	if stateStore == nil || logs == nil {
		return nil, fmt.Errorf("relay accounting stores are nil")
	}
	load, _ := req.Payload.(RelayStatusCheckPayload)
	if poller == nil || req.Provider == "" || req.ProviderJobID == "" || load.StatusCode < 200 || load.StatusCode >= 300 {
		return nil, nil
	}
	for _, match := range load.Matches {
		job := openRelayJob(ctx, stateStore, string(req.Provider), match.RouteKey, req.ProviderJobID)
		if job == nil {
			continue
		}
		// A reused id opens a new generation; a check sent before it opened describes an earlier one,
		// so it is left to the sweeper rather than settling this one. Another route's job may still fit.
		if !load.SentAt.IsZero() && job.CreatedAt.After(load.SentAt) {
			continue
		}
		snap, err := relaySnapshot(job)
		if err != nil || !samePoll(match.Poll, snap.Job.Poll) {
			continue
		}
		if done, _ := snap.Outcome(snap.Status(load.Body)); !done {
			// Not finished by this job's own statuses; another route's job may read the reply differently.
			continue
		}
		settler := &RelaySettler{store: stateStore, poller: poller, logs: logs, hasReporter: reporter != nil, logger: load.Logger}
		// Poll counts the request it sends on the row itself, whether or not it settles anything.
		polled, err := settler.Poll(ctx, job)
		if err != nil || polled == nil || !polled.Settleable {
			// Bifrost's own poll failed or still sees the job running: the sweeper retries this one.
			continue
		}
		verdict, _ := polled.Payload.(*relayVerdict)
		if verdict == nil || verdict.reason != "" {
			// Not a finished or failed answer (a poll error, or a timeout past the deadline): a caller's
			// check never writes a job off, so the sweeper's own last poll still gets to price it.
			continue
		}
		verdict.settledBy = load.RequestID
		req.Job = polled.Job
		req.ProviderJobID = polled.Job.JobID
		req.Payload = polled.Payload
		return accountJob(ctx, stateStore, relayLogsRecorder{logs: logs, reporter: reporter}, nil, settler, req)
	}
	return nil, nil
}

// pollCounter is an optional JobStore method counting one more poll on a job without touching the
// rest of its row; the config store implements it.
type pollCounter interface {
	IncrementProviderJobPollAttempts(ctx context.Context, jobID string) error
}

// HasRelayJob reports whether a relay started a job with this upstream id on the route routeKey
// names, open or settled, for the gateway to relay only a real job's status check free: a caller
// still reads a finished job's results with it. One primary-key read, the job's first row.
// A store error is returned as it is, so the caller can tell its own not-found error from a failed
// lookup.
func HasRelayJob(ctx context.Context, store JobStore, relayName, routeKey, upstreamID string) (bool, error) {
	if store == nil {
		return false, nil
	}
	id, _ := RelayJobRowID(relayName, routeKey, upstreamID, 1)
	job, err := store.GetProviderJob(ctx, id)
	if err != nil {
		return false, err
	}
	return job != nil, nil
}

// samePoll compares two poll templates as they are stored, so an unset and an empty header map agree.
func samePoll(a, b schemas.RelayJobPoll) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// openRelayJob is the open job row for an upstream id: the generation a start recorded and no
// settlement has closed yet.
func openRelayJob(ctx context.Context, store JobStore, relayName, routeKey, upstreamID string) *cstables.TableProviderJob {
	for gen := 1; gen <= MaxRelayJobGenerations; gen++ {
		id, _ := RelayJobRowID(relayName, routeKey, upstreamID, gen)
		job, err := store.GetProviderJob(ctx, id)
		if err != nil || job == nil {
			return nil
		}
		if job.AccountingStatus != cstables.ProviderJobAccountingStatusAccounted && job.AccountingStatus != cstables.ProviderJobAccountingStatusUnpriceable {
			return job
		}
	}
	return nil
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
	httpStatus  int    // the final poll's HTTP status as answered, 0 when it got no answer
	polled      bool   // a poll was sent to end the job, answered or not
	settledBy   string // the caller's request whose status check settled the job, "" for a poll

	usage   *schemas.RelayUsage // the priced usage, set by Settle
	written bool                // the settlement row already exists
}

// RelaySettler polls a relay job with its route's request and prices it with its route's usage.
type RelaySettler struct {
	store       JobStore
	poller      RelayJobPoller
	logs        RelayJobLogStore
	hasReporter bool
	logger      schemas.Logger // optional; reports a poll count that could not be written
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
	// A relay that is not loaded refuses the poll locally; anything else reached the upstream.
	if bErr == nil || bErr.Error == nil || !errors.Is(bErr.Error.Error, relay.ErrRelayNotFound) {
		s.countPoll(ctx, &job)
		// A failed poll returns no job, so the sweeper reschedules its listed copy: that copy gets the
		// new count too, so the backoff grows from it.
		listed.PollAttempts = job.PollAttempts
	}
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
		// The status as the upstream answered it, not the 429 a throttling 400 is read as. Every error
		// the poll itself returns is the gateway's (no connection, a timeout, an unreadable reply), so
		// with no reply the upstream gave no status: 0, never the gateway's own 502 or 504.
		answered := 0
		if resp != nil {
			answered = resp.StatusCode
		}
		// A missing relay sends nothing, so there is no check to count and no status the upstream gave.
		if bErr.Error != nil && errors.Is(bErr.Error.Error, relay.ErrRelayNotFound) {
			return settle(&relayVerdict{snap: snap, reason: reason, reasonErr: pollErr})
		}
		return settle(&relayVerdict{snap: snap, reason: reason, reasonErr: pollErr, httpStatus: answered, polled: true})
	}

	status := snap.Status(resp.Body)
	job.ProviderStatus = status
	done, succeeded := snap.Outcome(status)
	if !done {
		if !pastDeadline {
			return &PollResult{Job: &job, Retry: true}, nil
		}
		return settle(&relayVerdict{
			snap: snap, status: status, reason: UnpriceableReasonRelayJobTimeout, httpStatus: resp.StatusCode, polled: true,
			reasonErr: fmt.Errorf("job did not finish within %ds (last status %q)", snap.Timeout(), status),
		})
	}
	return settle(&relayVerdict{snap: snap, status: status, succeeded: succeeded, contentType: resp.ContentType, body: resp.Body, httpStatus: resp.StatusCode, polled: true})
}

// countPoll records one sent poll. The row is raised atomically, so polls from other nodes or a
// caller's check are never lost to a write of an older count; the settlement write leaves the
// column alone for relay jobs. job is the copy read just before the poll, so +1 tracks the row.
func (s *RelaySettler) countPoll(ctx context.Context, job *cstables.TableProviderJob) {
	job.PollAttempts++
	counter, ok := s.store.(pollCounter)
	if !ok {
		return
	}
	// The count is diagnostic only, so a failed write is logged, never retried (a retry after an
	// uncertain write could count the poll twice) and never holds up the job's schedule or charge.
	if err := counter.IncrementProviderJobPollAttempts(ctx, job.ID); err != nil && s.logger != nil {
		s.logger.Warn("relay job %s: could not count a sent poll: %v", job.ID, err)
	}
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
	entry.JobDebugParsed = &logstore.RelayJobDebug{Polls: job.PollAttempts, LastStatus: job.ProviderStatus, LastHTTPStatus: e.verdict.httpStatus,
		SettledByRequestID: e.verdict.settledBy, SettledAt: time.Now().UTC()}
	if e.verdict.status != "" {
		entry.JobDebugParsed.LastStatus = e.verdict.status
	}
	entry.JobDebugParsed.Outcome = logstore.RelayJobOutcomeFailed
	if e.verdict.succeeded {
		entry.JobDebugParsed.Outcome = logstore.RelayJobOutcomeSucceeded
	}
	if e.unpriced {
		entry.JobDebugParsed.Outcome = logstore.RelayJobOutcomeUnpriced
		entry.Cost, entry.CostPerRequest, entry.CostPerUnit = nil, nil, nil
		message := e.verdict.reason
		if e.verdict.reasonErr != nil {
			message = e.verdict.reasonErr.Error()
		}
		entry.ErrorDetailsParsed = &schemas.BifrostError{Error: &schemas.ErrorField{Type: schemas.Ptr(e.verdict.reason), Message: message}}
		entry.JobDebugParsed.LastError = message
	}
	if job.SourceLogID != nil {
		// The start call's row is gone only if a user or retention removed it; the job row stands alone then.
		if start, err := e.logs.FindRelayLog(ctx, *job.SourceLogID); err == nil {
			copyStartAttribution(entry, start)
		}
	}
	_, err := e.logs.BatchCreateRelayLogsIfNotExists(ctx, []*logstore.RelayLog{entry})
	return err
}

// copyStartAttribution gives a job row what its start call's row resolved at request time, as an
// LLM job row copies its source log: the model, the route and the display names behind the ids.
func copyStartAttribution(entry, start *logstore.RelayLog) {
	entry.Model = start.Model
	entry.RoutePath = start.RoutePath
	entry.UserName, entry.UserEmail = start.UserName, start.UserEmail
	entry.VirtualKeyName = start.VirtualKeyName
	entry.TeamName, entry.CustomerName = start.TeamName, start.CustomerName
	entry.BusinessUnitID, entry.BusinessUnitName = start.BusinessUnitID, start.BusinessUnitName
	entry.ProjectID, entry.ProjectName = start.ProjectID, start.ProjectName
	entry.SessionID, entry.AgentCorrelationID, entry.ParentRequestID = start.SessionID, start.AgentCorrelationID, start.ParentRequestID
	entry.UserAgent, entry.App = start.UserAgent, start.App
	entry.TeamIDsParsed, entry.TeamNamesParsed = start.TeamIDsParsed, start.TeamNamesParsed
	entry.CustomerIDsParsed, entry.CustomerNamesParsed = start.CustomerIDsParsed, start.CustomerNamesParsed
	entry.BusinessUnitIDsParsed, entry.BusinessUnitNamesParsed = start.BusinessUnitIDsParsed, start.BusinessUnitNamesParsed
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
