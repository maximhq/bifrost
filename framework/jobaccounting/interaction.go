package jobaccounting

import (
	"context"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
)

// ProviderJobKindInteraction is a run the provider stores and reports on every read: a Gemini
// Interaction (a Deep Research agent run, or a model run with store=true) reached through
// /genai_passthrough.
//
// Every read of a finished interaction, whether a GET poll, a stream resumed with last_event_id or
// a cancel, repeats the run's full usage. Billing any of those reads would bill the run again, so
// none of them is billed. The provider reports the run on BifrostPassthroughResponse.ProviderJob
// instead of PassthroughUsage, and the run is settled here once, as its own aggregate cost row
// under the request that created it. This is the same split video uses for its retrieves.
const ProviderJobKindInteraction ProviderJobKind = cstables.ProviderJobKindInteraction

// Reasons an interaction ends unpriced.
const (
	// UnpriceableReasonInteractionNoUsage is a finished run that reported no usage.
	UnpriceableReasonInteractionNoUsage = "interaction_usage_missing"
	// UnpriceableReasonMissingInteractionPricing is a run whose agent or model has no rate yet. The
	// row is written with cost NULL so the missing-cost backfill can price it once the rate lands.
	UnpriceableReasonMissingInteractionPricing = "missing_interaction_pricing"
	// UnpriceableReasonInteractionGone is a run the provider no longer knows, such as one deleted
	// before it was polled again.
	UnpriceableReasonInteractionGone         = "interaction_gone"
	UnpriceableReasonInteractionAccessDenied = "interaction_access_denied"
	UnpriceableReasonInteractionRejected     = "interaction_request_rejected"
)

// interactionLogNamespace keys the aggregate cost row of a settled interaction. It is fixed forever:
// it is both the aggregate write's idempotency key and the governance dedupe key.
var interactionLogNamespace = uuid.MustParse("d5b31050-4fb6-4fae-a9a6-63694e4970b4")

func init() { RegisterLogNamespace(ProviderJobKindInteraction, interactionLogNamespace) }

// interactionProviders serve the Interactions API through passthrough.
var interactionProviders = map[schemas.ModelProvider]struct{}{
	schemas.Gemini: {},
	schemas.Vertex: {},
}

// InteractionPoller reads a stored interaction again with the key that created it. A non-2xx
// answer must come back as a ProviderCallError carrying the status, so a run the provider has
// forgotten parks instead of burning its poll attempts.
type InteractionPoller interface {
	PollInteraction(ctx context.Context, job *cstables.TableProviderJob) (*schemas.BifrostPassthroughResponse, error)
}

// InteractionPayload is the interaction kind's settlement input: the run as the read that found it
// finished reported it, and the agent or model it named.
type InteractionPayload struct {
	Run   *schemas.BifrostPassthroughJob
	Model string
}

// InteractionJobParams is what the job row's params column holds for an interaction: what a poll
// needs beyond the path in Endpoint.
type InteractionJobParams struct {
	APIVersion string `json:"api_version,omitempty"`
}

// MarshalInteractionParams renders the params column for an interaction job row.
func MarshalInteractionParams(params InteractionJobParams) (*string, error) {
	data, err := sonic.Marshal(params)
	if err != nil {
		return nil, err
	}
	encoded := string(data)
	return &encoded, nil
}

// InteractionParamsFromJob reads the params column back. An unreadable blob yields empty params:
// the poll then uses the provider's default version rather than never settling the run.
func InteractionParamsFromJob(job *cstables.TableProviderJob) InteractionJobParams {
	var params InteractionJobParams
	if job == nil || job.Params == nil || *job.Params == "" {
		return params
	}
	_ = sonic.Unmarshal([]byte(*job.Params), &params)
	return params
}

// InteractionSettler prices stored interactions. It is the Settler for ProviderJobKindInteraction.
//
// poller may be nil on the inline settlement path, where the caller already holds the finished run.
type InteractionSettler struct {
	poller InteractionPoller
}

func NewInteractionSettler(poller InteractionPoller) *InteractionSettler {
	return &InteractionSettler{poller: poller}
}

func (s *InteractionSettler) Kind() ProviderJobKind { return ProviderJobKindInteraction }

func (s *InteractionSettler) SupportsProvider(provider schemas.ModelProvider) bool {
	_, ok := interactionProviders[provider]
	return ok
}

// Backoff stretches with the run. A model run finishes in seconds and is usually settled inline;
// what reaches the sweeper is mostly Deep Research, which runs for minutes up to an hour, so polls
// start at the sweep interval and slow to every ten minutes. MaxPollAttempts then covers about 13 hours.
func (s *InteractionSettler) Backoff(attempts int, interval time.Duration) time.Duration {
	delay := interval
	switch {
	case attempts >= 60:
		delay = maxDuration(delay, 10*time.Minute)
	case attempts >= 30:
		delay = maxDuration(delay, 5*time.Minute)
	case attempts >= 10:
		delay = maxDuration(delay, 2*time.Minute)
	}
	return delay
}

func (s *InteractionSettler) Poll(ctx context.Context, job *cstables.TableProviderJob) (*PollResult, error) {
	if s.poller == nil {
		return nil, fmt.Errorf("interaction poller is nil")
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultProviderPollTimeout)
	defer cancel()

	resp, err := s.poller.PollInteraction(callCtx, job)
	if err != nil {
		if reason := interactionUnreachableReason(err); reason != "" {
			return &PollResult{Terminal: true, UnpriceableReason: reason}, nil
		}
		return nil, err
	}
	if resp == nil || resp.ProviderJob == nil {
		return nil, fmt.Errorf("interaction poll reported no run for job %s", job.ID)
	}
	run := resp.ProviderJob
	if run.ID != job.JobID {
		return nil, fmt.Errorf("interaction poll returned mismatched interaction id for job %s", job.ID)
	}

	latest := *job
	latest.ProviderStatus = run.Status
	if resp.ServedModel != "" {
		latest.Model = resp.ServedModel
	}
	if !run.Terminal {
		return &PollResult{Job: &latest}, nil
	}
	return &PollResult{
		Job:        &latest,
		Terminal:   true,
		Settleable: true,
		Payload:    InteractionPayload{Run: run, Model: resp.ServedModel},
	}, nil
}

// Settle prices a finished run from the usage its final read reported, at the agent or model it
// names, through the same passthrough pricing the request path uses. Interactions run at the
// standard rate, so this deliberately does not use the batch pricing the batch kind settles with.
//
// The aggregate row carries the usage on token_usage and the request type as its object, the same
// as an ordinary Responses row, so cost recalculation reprices it on the generic path and
// RepriceFromLog has nothing of its own to do.
func (s *InteractionSettler) Settle(ctx context.Context, pricing PricingManager, jobReq JobRequest) (*Settlement, error) {
	load, _ := jobReq.Payload.(InteractionPayload)
	model := load.Model
	if model == "" {
		model = jobReq.FallbackModel
	}
	settlement := &Settlement{Model: model, Object: schemas.ResponsesRequest}

	var usage *schemas.BifrostPassthroughUsage
	var status string
	if load.Run != nil {
		usage, status = load.Run.Usage, load.Run.Status
	}
	if usage == nil || usage.LLMUsage == nil {
		// A run that stopped before doing billable work reports no usage, and owes nothing: a final
		// price of zero, not an absent one, or the job would stay parked forever.
		if status == "failed" || status == "cancelled" {
			settlement.Priced = true
			settlement.Complete = true
			return settlement, nil
		}
		settlement.UnpriceableReason = UnpriceableReasonInteractionNoUsage
		return settlement, nil
	}

	settlement.Usage = *usage.LLMUsage
	settlement.Usage.Cost = nil
	if usage.ServiceTier != nil && *usage.ServiceTier != "" {
		tier := string(*usage.ServiceTier)
		settlement.ApplyDebug = func(entry *logstore.Log) { entry.ServiceTier = &tier }
	}

	breakdown := pricing.CalculateCostBreakdown(interactionPricingResponse(jobReq, model, usage), jobReq.Scopes)
	if breakdown == nil {
		settlement.UnpriceableReason = UnpriceableReasonMissingInteractionPricing
		settlement.RecordUnpriced = true
		settlement.UnpricedModel = model
		return settlement, nil
	}
	settlement.Priced = true
	settlement.Complete = true
	settlement.Cost = breakdown.TotalCost
	return settlement, nil
}

// interactionPricingResponse rebuilds the passthrough response the run's final read would have been
// priced from, so settlement resolves the same rate, tier and request type the request path would.
func interactionPricingResponse(jobReq JobRequest, model string, usage *schemas.BifrostPassthroughUsage) *schemas.BifrostResponse {
	path := "/interactions"
	if jobReq.Job != nil && jobReq.Job.Endpoint != "" {
		path = jobReq.Job.Endpoint
	}
	return &schemas.BifrostResponse{
		PassthroughResponse: &schemas.BifrostPassthroughResponse{
			PassthroughUsage: usage,
			ServedModel:      model,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:     schemas.PassthroughRequest,
				Provider:        jobReq.Provider,
				PassthroughPath: path,
				RoutingInfo:     schemas.RoutingInfo{Provider: jobReq.Provider, Model: model},
			},
		},
	}
}

// RepriceFromLog claims nothing: an interaction's aggregate row keeps its usage on token_usage and
// its request type as the object, so the generic per-request path reprices it correctly.
func (s *InteractionSettler) RepriceFromLog(entry *logstore.Log, pricing PricingManager) (*RepricedCost, error) {
	return nil, nil
}

// HydrateFromLog has nothing to add: the aggregate row's cost is mirrored by the engine.
func (s *InteractionSettler) HydrateFromLog(entry *logstore.Log, out *Outcome) {}

// AccountInteractionJob settles an interaction from a read that found it finished. It is the inline
// counterpart to the sweeper, the same as AccountVideoJob is for a video retrieve: whoever reads the
// finished run first (the create itself, when it ran synchronously) settles it, and every later
// read takes the engine's mirror path and bills nothing.
//
// It never creates the coordination row. A run whose create Bifrost did not see could belong to
// anyone holding the provider key, so a poll of it is not grounds to bill the caller.
func AccountInteractionJob(ctx context.Context, stateStore JobStore, logStore AggregateLogStore, pricing PricingManager, req JobRequest) (*Outcome, error) {
	if stateStore == nil {
		return nil, fmt.Errorf("interaction accounting state store is nil")
	}
	if req.Provider == "" || req.ProviderJobID == "" {
		return nil, nil
	}
	load, _ := req.Payload.(InteractionPayload)
	if load.Run == nil || !load.Run.Terminal {
		// Still running: nothing to settle, and the sweeper is already scheduled.
		return nil, nil
	}
	job, err := stateStore.GetProviderJob(ctx, cstables.ProviderJobID(cstables.ProviderJobKindInteraction, string(req.Provider), req.ProviderJobID))
	if err != nil || job == nil {
		return nil, nil
	}
	req.Job = job
	return AccountJob(ctx, stateStore, logStore, pricing, NewInteractionSettler(nil), req)
}

// NewInteractionSweeper binds the generic sweeper to the interaction settler. It settles runs no
// client read to the end: a background Deep Research run the caller stopped polling, or a stream
// that closed before the run finished.
func NewInteractionSweeper(store SweepStore, logStore AggregateLogStore, pricing PricingManager, poller InteractionPoller, emitter AggregateLogEmitter, usageReporter UsageReporter, config SweeperConfig) *Sweeper {
	if config.ClaimedBy == "" {
		config.ClaimedBy = "interaction-sweeper:" + newInstanceID()
	}
	return NewSweeper(store, logStore, pricing, NewInteractionSettler(poller), emitter, usageReporter, config)
}

// interactionUnreachableReason names why a failed poll is final, or returns empty when the call is
// worth retrying.
func interactionUnreachableReason(err error) string {
	switch ClassifyProviderCall(err) {
	case ProviderCallGone:
		return UnpriceableReasonInteractionGone
	case ProviderCallAccessDenied:
		return UnpriceableReasonInteractionAccessDenied
	case ProviderCallRejected:
		return UnpriceableReasonInteractionRejected
	default:
		return ""
	}
}
