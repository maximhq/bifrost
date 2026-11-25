package jobaccounting

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
)

// ProviderJobKindResponse is a Responses API response created with background=true.
//
// The create answers at once with status queued or in_progress and no usage; the model runs
// afterwards, and the usage appears on GET /v1/responses/{id} once the response has finished. The
// create therefore has nothing to charge, and a retrieve names no model and repeats the full usage
// on every read. So neither request is charged for the run: the create records the response, and
// it is settled here once, as its own cost row under the create, by the first read that finds it
// finished or by the sweeper.
//
// A create that returns already finished was charged from its own response and never gets a row.
const ProviderJobKindResponse ProviderJobKind = cstables.ProviderJobKindResponse

// Reasons a background response ends unpriced.
const (
	// UnpriceableReasonResponseNoUsage is a completed response that reported no usage.
	UnpriceableReasonResponseNoUsage = "response_usage_missing"
	// UnpriceableReasonMissingResponsePricing is a response whose model has no rate yet. The row is
	// written with cost NULL so the missing-cost backfill can price it once the rate lands.
	UnpriceableReasonMissingResponsePricing = "missing_response_pricing"
	// UnpriceableReasonResponseGone is a response the provider no longer knows, such as one deleted
	// before it was read again.
	UnpriceableReasonResponseGone         = "response_gone"
	UnpriceableReasonResponseAccessDenied = "response_access_denied"
	// UnpriceableReasonResponseRejected is a read the provider will not serve, including a provider
	// that has no retrieve for stored responses.
	UnpriceableReasonResponseRejected = "response_request_rejected"
)

// responseLogNamespace keys the cost row of a settled background response. It is fixed forever: it
// is both the row write's idempotency key and the governance dedupe key.
var responseLogNamespace = uuid.MustParse("6f0a3c1e-2b7d-4f5a-9c41-8d2e7b3a5c90")

func init() { RegisterLogNamespace(ProviderJobKindResponse, responseLogNamespace) }

// IsTerminalResponseStatus reports whether a Responses status is one the provider will not advance:
// the response finished, failed, was cut short or was cancelled.
func IsTerminalResponseStatus(status string) bool {
	switch status {
	case schemas.ResponsesResponseStatusCompleted, schemas.ResponsesResponseStatusFailed,
		schemas.ResponsesResponseStatusIncomplete, schemas.ResponsesResponseStatusCancelled:
		return true
	default:
		return false
	}
}

// IsPendingResponseStatus reports whether a Responses status says the model has yet to finish. A
// response that reports no status is neither pending nor terminal, and is not tracked.
func IsPendingResponseStatus(status string) bool {
	return status == schemas.ResponsesResponseStatusQueued || status == schemas.ResponsesResponseStatusInProgress
}

// ResponsePoller retrieves a stored response again with the key that created it. A provider error
// must come back as a ProviderCallError carrying the status, so a response the provider has
// forgotten parks instead of using up its poll attempts.
type ResponsePoller interface {
	RetrieveResponse(ctx context.Context, job *cstables.TableProviderJob) (*schemas.BifrostResponsesResponse, error)
}

// ResponsePayload is the response kind's settlement input: the response as the read that found it
// finished returned it.
type ResponsePayload struct {
	Response *schemas.BifrostResponsesResponse
}

// ResponseSettler prices background responses. It is the Settler for ProviderJobKindResponse.
//
// poller may be nil on the inline settlement path, where the caller already holds the finished
// response.
type ResponseSettler struct {
	poller ResponsePoller
}

func NewResponseSettler(poller ResponsePoller) *ResponseSettler {
	return &ResponseSettler{poller: poller}
}

func (s *ResponseSettler) Kind() ProviderJobKind { return ProviderJobKindResponse }

// SupportsProvider accepts every provider. A row is only written for a create that answered with a
// pending status, and a provider with no retrieve parks on its first poll (see Poll) rather than
// staying due forever, which is what skipping it here would do.
func (s *ResponseSettler) SupportsProvider(provider schemas.ModelProvider) bool { return true }

// Backoff stretches with the run, the same as the interaction kind. Most background responses
// finish within minutes and are settled by the caller's own polling; what reaches the sweeper late
// is long research work, so polls start at the sweep interval and slow to every ten minutes.
func (s *ResponseSettler) Backoff(attempts int, interval time.Duration) time.Duration {
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

func (s *ResponseSettler) Poll(ctx context.Context, job *cstables.TableProviderJob) (*PollResult, error) {
	if s.poller == nil {
		return nil, fmt.Errorf("response poller is nil")
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultProviderPollTimeout)
	defer cancel()

	resp, err := s.poller.RetrieveResponse(callCtx, job)
	if err != nil {
		if reason := responseUnreachableReason(err); reason != "" {
			return &PollResult{Terminal: true, UnpriceableReason: reason}, nil
		}
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("response poll returned nothing for job %s", job.ID)
	}
	if resp.ID != nil && *resp.ID != job.JobID {
		return nil, fmt.Errorf("response poll returned mismatched response id for job %s", job.ID)
	}
	status := responseStatus(resp)
	latest := *job
	if status != "" {
		latest.ProviderStatus = status
	}
	if !IsTerminalResponseStatus(status) {
		return &PollResult{Job: &latest}, nil
	}
	return &PollResult{
		Job:        &latest,
		Terminal:   true,
		Settleable: true,
		Payload:    ResponsePayload{Response: resp},
	}, nil
}

// Settle prices a finished response from the usage its final read reported, at the model the create
// named, through the same Responses pricing an ordinary create is charged with.
//
// The cost row carries the usage on token_usage and the Responses request type as its object, the
// same as an ordinary Responses row, so cost recalculation reprices it on the generic path and
// RepriceFromLog has nothing of its own to do.
func (s *ResponseSettler) Settle(ctx context.Context, pricing PricingManager, jobReq JobRequest) (*Settlement, error) {
	load, _ := jobReq.Payload.(ResponsePayload)
	resp := load.Response
	// The model recorded at the create is the one the limits were checked against and the one the
	// catalog knows; the response reports a dated snapshot name, used only when the row has none.
	model := jobReq.FallbackModel
	if model == "" && resp != nil {
		model = resp.Model
	}
	// The create named the model and was counted against the request limits when it ran. Settlement
	// charges tokens and cost only, or every background response would count as two requests.
	settlement := &Settlement{Model: model, Object: schemas.ResponsesRequest, SkipRequestCount: true}

	if resp == nil || resp.Usage == nil {
		// A response that failed or was cancelled before the model did billable work reports no
		// usage and owes nothing: a final price of zero, not an absent one, or the job would be
		// read again until its attempts ran out.
		switch responseStatus(resp) {
		case schemas.ResponsesResponseStatusFailed, schemas.ResponsesResponseStatusCancelled:
			settlement.Priced = true
			settlement.Complete = true
			return settlement, nil
		}
		settlement.UnpriceableReason = UnpriceableReasonResponseNoUsage
		return settlement, nil
	}

	// The usage that is saved is the usage that was priced, tool details restored: recalculating
	// costs later prices the row from its own usage, with no job row and no request to consult.
	priced := responsePricingResponse(jobReq, model, resp)
	if usage := priced.ResponsesResponse.Usage.ToBifrostLLMUsage(); usage != nil {
		settlement.Usage = *usage
		settlement.Usage.Cost = nil
	}
	// The row carries the tier and, when the create went through a key alias, the alias it was
	// priced through. The alias comes from the job, not from the create's log row: that row can
	// be gone or unreadable, and a later reprice rebuilds the alias from these columns alone.
	var tier *string
	if resp.ServiceTier != nil && *resp.ServiceTier != "" {
		tier = new(string(*resp.ServiceTier))
	}
	routing := priced.ResponsesResponse.ExtraFields.RoutingInfo
	if tier != nil || routing.ResolvedKeyAlias != nil {
		settlement.ApplyDebug = func(entry *logstore.Log) {
			if tier != nil {
				entry.ServiceTier = tier
			}
			if alias := routing.ResolvedKeyAlias; alias != nil {
				entry.Alias = new(routing.Model)
				entry.CanonicalModelName = alias.ModelName
				if alias.ModelFamily != nil {
					entry.AliasModelFamily = new(string(*alias.ModelFamily))
				}
			}
		}
	}

	breakdown := pricing.CalculateCostBreakdown(priced, jobReq.Scopes)
	if breakdown == nil {
		if settlement.Usage.TotalTokens == 0 && !hasBillableToolUsage(settlement.Usage.ToolUsage) {
			// Usage that counts no tokens and used no priced tool has nothing to price, whatever
			// the catalog holds. A tool is different: a container session is a flat fee that
			// owes money with no tokens at all, so it falls through to the unpriced path.
			settlement.Priced = true
			settlement.Complete = true
			return settlement, nil
		}
		settlement.UnpriceableReason = UnpriceableReasonMissingResponsePricing
		settlement.RecordUnpriced = true
		settlement.UnpricedModel = model
		return settlement, nil
	}
	settlement.Priced = true
	settlement.Complete = true
	settlement.Cost = breakdown.TotalCost
	// The row keeps the split as well as the total, the way a row written by an ordinary create
	// does. It is a copy: the breakdown can alias a cost the pricing manager still holds.
	split := *breakdown
	settlement.Usage.Cost = &split
	return settlement, nil
}

// responseJobRouting is the routing identity a background create resolved, saved on the job row
// under "routing" next to the tool settings (see ResponseJobParams).
type responseJobRouting struct {
	Model            string                    `json:"model,omitempty"`
	ResolvedKeyAlias *schemas.ResolvedKeyAlias `json:"resolved_key_alias,omitempty"`
}

// ResponseJobParams builds what a background Responses create saves on its job row for the
// settlement that prices it later, from a read that carries no request and resolves no alias:
//
//   - toolPricing, the tool settings the provider kept from the request (may be empty);
//   - the key alias the create resolved, when there was one. Pricing looks the alias's
//     canonical model name up first, and the wire model on the row can be a deployment id the
//     catalog does not know.
//
// It returns nil when there is nothing to save, which is every create with no priced tool and
// no alias.
func ResponseJobParams(toolPricing string, routing schemas.RoutingInfo) *string {
	if routing.ResolvedKeyAlias == nil {
		if toolPricing == "" {
			return nil
		}
		return &toolPricing
	}
	fields := map[string]json.RawMessage{}
	if toolPricing != "" {
		if err := sonic.Unmarshal([]byte(toolPricing), &fields); err != nil {
			fields = map[string]json.RawMessage{}
		}
	}
	saved, err := sonic.Marshal(responseJobRouting{Model: routing.Model, ResolvedKeyAlias: routing.ResolvedKeyAlias})
	if err != nil {
		if toolPricing == "" {
			return nil
		}
		return &toolPricing
	}
	fields["routing"] = saved
	out, err := schemas.MarshalSorted(fields)
	if err != nil {
		return nil
	}
	params := string(out)
	return &params
}

// hasBillableToolUsage reports whether a response's server tools did work that is charged apart
// from its tokens: a web search, generated images, or a container session.
func hasBillableToolUsage(usage *schemas.ToolUsage) bool {
	if usage == nil {
		return false
	}
	return len(usage.ContainerSessions) > 0 || usage.ImageGen.HasTokens() ||
		(usage.WebSearch != nil && usage.WebSearch.NumRequests > 0)
}

// responsePricingResponse rebuilds the response a create would have been priced from had it
// returned finished, so settlement resolves the same rate, tier and request type. The copy is
// shallow and only its routing fields are replaced: the caller's response is about to be returned
// to a client and must not change.
//
// What a server tool costs depends on the request too: the image tool's model, and whether a
// code container was started for this response. The create saved those settings on the job
// (see ResponseToolPricingParams), and they are restored onto a copy of the usage here.
func responsePricingResponse(jobReq JobRequest, model string, resp *schemas.BifrostResponsesResponse) *schemas.BifrostResponse {
	priced := *resp
	routing := schemas.RoutingInfo{Provider: jobReq.Provider, Model: model}
	if job := jobReq.Job; job != nil && job.Params != nil && *job.Params != "" {
		// The alias the create resolved: pricing looks its canonical name up before the wire model.
		var saved struct {
			Routing *responseJobRouting `json:"routing"`
		}
		if sonic.Unmarshal([]byte(*job.Params), &saved) == nil && saved.Routing != nil && saved.Routing.ResolvedKeyAlias != nil {
			routing.ResolvedKeyAlias = saved.Routing.ResolvedKeyAlias
			if saved.Routing.Model != "" {
				routing.Model = saved.Routing.Model
			}
		}
	}
	priced.ExtraFields = schemas.BifrostResponseExtraFields{
		RequestType: schemas.ResponsesRequest,
		Provider:    jobReq.Provider,
		RoutingInfo: routing,
	}
	if job := jobReq.Job; job != nil && job.Params != nil && *job.Params != "" && resp.Usage != nil {
		usage := *resp.Usage
		if usage.ToolUsage != nil {
			toolUsage := *usage.ToolUsage
			usage.ToolUsage = &toolUsage
		}
		priced.Usage = &usage
		openai.RecordResponsesServerToolUsage(&priced, []byte(*job.Params))
	}
	return &schemas.BifrostResponse{ResponsesResponse: &priced}
}

// RepriceFromLog claims nothing: a background response's cost row keeps its usage on token_usage
// and its request type as the object, so the generic per-request path reprices it correctly.
func (s *ResponseSettler) RepriceFromLog(entry *logstore.Log, pricing PricingManager) (*RepricedCost, error) {
	return nil, nil
}

// HydrateFromLog has nothing to add: the cost row's cost is mirrored by the engine.
func (s *ResponseSettler) HydrateFromLog(entry *logstore.Log, out *Outcome) {}

// AccountResponseJob settles a background response from a read that found it finished. It is the
// inline counterpart to the sweeper: whichever read sees the finished response first settles it,
// and every later read finds the job already settled and charges nothing.
//
// It never creates the coordination row. A response with no row was charged from its own create,
// or was created outside Bifrost, and reading it is not grounds to charge the reader.
func AccountResponseJob(ctx context.Context, stateStore JobStore, logStore AggregateLogStore, pricing PricingManager, req JobRequest) (*Outcome, error) {
	if stateStore == nil {
		return nil, fmt.Errorf("response accounting state store is nil")
	}
	if req.Provider == "" || req.ProviderJobID == "" {
		return nil, nil
	}
	load, _ := req.Payload.(ResponsePayload)
	status := responseStatus(load.Response)
	if !IsTerminalResponseStatus(status) {
		// Still queued or running: nothing to settle, and the sweeper is already scheduled.
		return nil, nil
	}
	job, err := stateStore.GetProviderJob(ctx, cstables.ProviderJobID(cstables.ProviderJobKindResponse, string(req.Provider), req.ProviderJobID))
	if err != nil || job == nil {
		return nil, nil
	}
	// The usual read of a finished response is a repeat. A settled job is final, so it costs that
	// one lookup and nothing else.
	if job.AccountingStatus == cstables.ProviderJobAccountingStatusAccounted {
		return nil, nil
	}
	if job.AccountingStatus == cstables.ProviderJobAccountingStatusUnpriceable {
		// A job parked because the sweeper ran out of poll attempts was only ever unread, not
		// unpriceable. This read holds its final usage and nothing else will settle it, so it
		// is claimed. A job parked for any other reason stays parked.
		if job.UnpriceableReason == nil || *job.UnpriceableReason != UnpriceableReasonMaxPollAttempts {
			return nil, nil
		}
		req.ForceClaim = true
	}
	job.ProviderStatus = status
	req.Job = job
	return AccountJob(ctx, stateStore, logStore, pricing, NewResponseSettler(nil), req)
}

// NewResponseSweeper binds the generic sweeper to the response settler. It settles the background
// responses no client polled to the end.
func NewResponseSweeper(store SweepStore, logStore AggregateLogStore, pricing PricingManager, poller ResponsePoller, emitter AggregateLogEmitter, usageReporter UsageReporter, config SweeperConfig) *Sweeper {
	if config.ClaimedBy == "" {
		config.ClaimedBy = "response-sweeper:" + newInstanceID()
	}
	return NewSweeper(store, logStore, pricing, NewResponseSettler(poller), emitter, usageReporter, config)
}

func responseStatus(resp *schemas.BifrostResponsesResponse) string {
	if resp == nil || resp.Status == nil {
		return ""
	}
	return *resp.Status
}

// responseUnreachableReason names why a failed poll is final, or returns empty when the call is
// worth retrying.
func responseUnreachableReason(err error) string {
	switch ClassifyProviderCall(err) {
	case ProviderCallGone:
		return UnpriceableReasonResponseGone
	case ProviderCallAccessDenied:
		return UnpriceableReasonResponseAccessDenied
	case ProviderCallRejected:
		return UnpriceableReasonResponseRejected
	default:
		return ""
	}
}
