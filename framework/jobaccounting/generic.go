package jobaccounting

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/providers/generic"
	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/tidwall/gjson"
)

const ProviderJobKindGenericPassthrough ProviderJobKind = cstables.ProviderJobKindGenericPassthrough

const (
	// UnpriceableReasonGenericJobTimeout is a job still running past its route's timeout_seconds.
	UnpriceableReasonGenericJobTimeout = "generic_job_timeout"
	// UnpriceableReasonGenericJobGone is a job the upstream no longer knows about.
	UnpriceableReasonGenericJobGone = "generic_job_gone"
	// UnpriceableReasonGenericJobAccessDenied is the starting key being refused on a poll.
	UnpriceableReasonGenericJobAccessDenied = "generic_job_access_denied"
	// UnpriceableReasonGenericJobRequestRejected is the upstream refusing the poll request itself.
	UnpriceableReasonGenericJobRequestRejected = "generic_job_request_rejected"
	// UnpriceableReasonGenericJobBadParams is a row whose captured snapshot cannot be read.
	UnpriceableReasonGenericJobBadParams = "generic_job_bad_params"
)

// genericJobLogNamespace keys the aggregate cost row for a settled generic job, fixed forever:
// it is both the aggregate write's idempotency key and the governance dedupe key.
var genericJobLogNamespace = uuid.MustParse("5f0c7a2e-9b41-4e8d-a3c6-2d7f1b9e8c04")

func init() { RegisterLogNamespace(ProviderJobKindGenericPassthrough, genericJobLogNamespace) }

// GenericJobPoller sends a job's poll request through the relay with the key that started the
// job and returns the upstream's answer. The transport binds it to the Bifrost client.
type GenericJobPoller interface {
	PollGenericJob(ctx context.Context, job *cstables.TableProviderJob, req generic.JobPollRequest) (*GenericPollResponse, error)
}

// GenericPollResponse is one poll's upstream reply.
type GenericPollResponse struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

// GenericJobSettler prices provider-side jobs that generic passthrough routes started. It is the
// Settler for ProviderJobKindGenericPassthrough. Everything it needs rides on the job row: the
// poll request, the status words and the usage mapping were captured from the route at start, so
// a provider config edited since does not change how a running job is settled, and the settler
// needs no provider config at all.
//
// poller may be nil on repricing, which reads rows and never the upstream.
type GenericJobSettler struct {
	poller GenericJobPoller
	now    func() time.Time
}

func NewGenericJobSettler(poller GenericJobPoller) *GenericJobSettler {
	return &GenericJobSettler{poller: poller, now: func() time.Time { return time.Now().UTC() }}
}

// GenericJobPayload is the settlement input: the terminal poll response.
type GenericJobPayload struct {
	Status      string
	ContentType string
	Body        []byte
	Failed      bool // ended on a failed status: priced at zero
}

func (s *GenericJobSettler) Kind() ProviderJobKind { return ProviderJobKindGenericPassthrough }

// SupportsProvider is true for every provider: the row carries its own settlement recipe.
func (s *GenericJobSettler) SupportsProvider(schemas.ModelProvider) bool { return true }

// Backoff keeps the sweeper's own tick for the first attempts (a Textract job finishes in
// seconds to minutes), then widens so a slow job does not poll the upstream every half minute
// for an hour.
func (s *GenericJobSettler) Backoff(attempts int, interval time.Duration) time.Duration {
	delay := interval
	switch {
	case attempts >= 40:
		delay = maxDuration(delay, 5*time.Minute)
	case attempts >= 20:
		delay = maxDuration(delay, 2*time.Minute)
	}
	return delay
}

func (s *GenericJobSettler) Poll(ctx context.Context, job *cstables.TableProviderJob) (*PollResult, error) {
	if s.poller == nil {
		return nil, fmt.Errorf("generic job poller is nil")
	}
	snap, err := generic.ParseJobSnapshot(derefString(job.Params))
	if err != nil {
		return &PollResult{Terminal: true, UnpriceableReason: UnpriceableReasonGenericJobBadParams}, nil
	}
	if s.now().Sub(job.CreatedAt) > time.Duration(snap.Timeout())*time.Second {
		return &PollResult{Terminal: true, UnpriceableReason: UnpriceableReasonGenericJobTimeout}, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, defaultProviderPollTimeout)
	defer cancel()
	resp, err := s.poller.PollGenericJob(callCtx, job, snap.PollRequest(job.JobID))
	if err == nil && resp != nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		err = &ProviderCallError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("poll returned HTTP %d", resp.StatusCode)}
	}
	if err != nil {
		if reason := genericJobUnreachableReason(err); reason != "" {
			return &PollResult{Terminal: true, UnpriceableReason: reason}, nil
		}
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("generic job poll returned nil response for job %s", job.ID)
	}

	status := snap.Status(resp.Body)
	latest := *job
	if status != "" {
		latest.ProviderStatus = status
	}
	done, succeeded := snap.Outcome(status)
	if !done {
		return &PollResult{Job: &latest}, nil
	}
	return &PollResult{
		Job:        &latest,
		Terminal:   true,
		Settleable: true,
		Payload:    GenericJobPayload{Status: status, ContentType: resp.ContentType, Body: resp.Body, Failed: !succeeded},
	}, nil
}

// Settle prices the terminal poll response with the mapping captured at start. A job that ended
// on a failed status is priced at zero: a real answer about the money, not the absence of one.
func (s *GenericJobSettler) Settle(ctx context.Context, pricing PricingManager, req JobRequest) (*Settlement, error) {
	load, _ := req.Payload.(GenericJobPayload)
	snap, err := generic.ParseJobSnapshot(derefString(req.Job.Params))
	if err != nil {
		return &Settlement{UnpriceableReason: UnpriceableReasonGenericJobBadParams, ReasonErr: err}, nil
	}
	settlement := &Settlement{Model: req.FallbackModel, Object: schemas.PassthroughRequest, Priced: true, Complete: true}
	poll := snap.PollRequest(req.ProviderJobID)
	if load.Failed {
		settlement.ApplyDebug = genericJobDebugFor(poll, &schemas.PassthroughJobAccounting{JobID: req.ProviderJobID, Status: load.Status, Failed: true})
		return settlement, nil
	}
	usage, detail := snap.Price(req.ProviderJobID, load.Status, load.ContentType, load.Body)
	if usage.Cost != nil {
		settlement.Cost = usage.Cost.TotalCost
	}
	if usage.LLMUsage != nil {
		settlement.Usage = *usage.LLMUsage
	}
	settlement.ApplyDebug = genericJobDebugFor(poll, detail)
	return settlement, nil
}

// genericJobDebugFor attaches the settlement detail to the aggregate row as passthrough params,
// the same column a relayed call's row uses, with job_accounting set to mark the settlement.
func genericJobDebugFor(poll generic.JobPollRequest, detail *schemas.PassthroughJobAccounting) func(*logstore.Log) {
	return func(entry *logstore.Log) {
		entry.ParamsParsed = &schemas.PassthroughLogParams{Method: poll.Method, Path: poll.Path, StatusCode: 200, JobAccounting: detail}
	}
}

// HydrateFromLog reads the settled status back off an aggregate row for a caller that lost the
// settlement claim and only wants to display it.
func (s *GenericJobSettler) HydrateFromLog(entry *logstore.Log, out *Outcome) {
	if detail := genericJobDetail(entry); detail.Exists() {
		out.Status = detail.Get("status").String()
	}
}

// RepriceFromLog answers for a generic job's settlement row with the cost it already carries:
// the rates were the operator's own, captured at start, so there is nothing in the catalog to
// recompute from. Returning the row keeps the generic recalculation path from pricing it to
// nothing.
func (s *GenericJobSettler) RepriceFromLog(entry *logstore.Log, pricing PricingManager) (*RepricedCost, error) {
	detail := genericJobDetail(entry)
	if !detail.Exists() {
		return nil, nil
	}
	if entry.ID != AccountingLogID(ProviderJobKindGenericPassthrough, schemas.ModelProvider(entry.Provider), detail.Get("job_id").String()) {
		return nil, nil
	}
	cost := 0.0
	if entry.Cost != nil {
		cost = *entry.Cost
	}
	return &RepricedCost{Cost: cost}, nil
}

// genericJobDetail reads the job_accounting block off a row's params, whether the row still
// holds the typed struct or was rehydrated from its JSON column.
func genericJobDetail(entry *logstore.Log) gjson.Result {
	if entry == nil {
		return gjson.Result{}
	}
	if params, ok := entry.ParamsParsed.(*schemas.PassthroughLogParams); ok {
		if params.JobAccounting == nil {
			return gjson.Result{}
		}
		return gjson.Parse(fmt.Sprintf(`{"job_id":%q,"status":%q}`, params.JobAccounting.JobID, params.JobAccounting.Status))
	}
	if entry.Params == "" {
		return gjson.Result{}
	}
	return gjson.Get(entry.Params, "job_accounting")
}

// genericJobUnreachableReason names why a failed poll is final, or returns empty when the call is
// worth retrying.
func genericJobUnreachableReason(err error) string {
	switch ClassifyProviderCall(err) {
	case ProviderCallGone:
		return UnpriceableReasonGenericJobGone
	case ProviderCallAccessDenied:
		return UnpriceableReasonGenericJobAccessDenied
	case ProviderCallRejected:
		return UnpriceableReasonGenericJobRequestRejected
	default:
		return ""
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// NewGenericJobSweeper binds the generic sweeper to the generic job settler.
func NewGenericJobSweeper(store SweepStore, logStore AggregateLogStore, pricing PricingManager, poller GenericJobPoller, emitter AggregateLogEmitter, usageReporter UsageReporter, config SweeperConfig) *Sweeper {
	if config.ClaimedBy == "" {
		config.ClaimedBy = "generic-job-sweeper:" + newInstanceID()
	}
	return NewSweeper(store, logStore, pricing, NewGenericJobSettler(poller), emitter, usageReporter, config)
}
