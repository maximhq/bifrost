package logging

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/jobaccounting"
	"github.com/maximhq/bifrost/framework/logstore"
)

// responseFirstPoll is how soon the sweeper first reads a background response nobody has polled to
// the end.
const responseFirstPoll = time.Minute

// isResponseJobRequestType reports whether a request type either creates a Responses response or
// returns a stored one, and so can start or finish a background response.
func isResponseJobRequestType(requestType schemas.RequestType) bool {
	switch requestType {
	case schemas.ResponsesRequest, schemas.ResponsesRetrieveRequest, schemas.ResponsesRetrieveStreamRequest, schemas.ResponsesCancelRequest:
		return true
	default:
		return false
	}
}

// handleResponseJob records a Responses response created in the background and, once a read finds
// it finished, settles it.
//
// A background create answers queued or in_progress with no usage, so it has nothing to charge,
// and the retrieve that later carries the usage names no model and repeats that usage on every
// poll. Neither request row is charged for the run: the create records the response with the
// limits it was admitted under, and the run is charged once, as its own row under the create.
//
// A create that answers already finished was charged from its own response and is left alone, so
// an ordinary response is never recorded and never charged twice.
func (p *LoggerPlugin) handleResponseJob(ctx *schemas.BifrostContext, entry *logstore.Log, requestType schemas.RequestType, resp *schemas.BifrostResponsesResponse) {
	if entry == nil || resp == nil || p.batchStore == nil || resp.ID == nil || *resp.ID == "" || resp.Status == nil {
		return
	}
	status := *resp.Status
	if requestType == schemas.ResponsesRequest {
		// Usage on the create was charged from the create, so there is nothing left to settle.
		if jobaccounting.IsPendingResponseStatus(status) && (resp.Usage == nil || resp.Usage.TotalTokens == 0) {
			toolPricing, _ := ctx.Value(schemas.BifrostContextKeyResponsesToolPricingContext).(string)
			p.recordResponseJob(entry, *resp.ID, status, jobaccounting.ResponseJobParams(toolPricing, resp.ExtraFields.RoutingInfo))
		}
		return
	}
	// A read only ever finishes a response whose create was recorded. One that is still running has
	// nothing to settle, and writing on it would let a poll loop turn into a write loop.
	if !jobaccounting.IsTerminalResponseStatus(status) || p.pricingManager == nil || p.store == nil {
		return
	}
	p.accountResponseJob(entry, resp)
}

// recordResponseJob persists the coordination row for a background response at its create, with
// everything settlement needs: the model to price at, the key to read it back with, who and
// which limits to charge, and the tool settings its price depends on.
func (p *LoggerPlugin) recordResponseJob(entry *logstore.Log, responseID, status string, params *string) {
	ctx, cancel := context.WithTimeout(p.ctx, jobAccountingTimeout)
	defer cancel()

	next := time.Now().UTC().Add(responseFirstPoll)
	job := &tables.TableProviderJob{
		ID:               tables.ProviderJobID(tables.ProviderJobKindResponse, entry.Provider, responseID),
		Kind:             tables.ProviderJobKindResponse,
		Provider:         entry.Provider,
		JobID:            responseID,
		Model:            entry.Model,
		ProviderStatus:   status,
		AccountingStatus: tables.ProviderJobAccountingStatusPending,
		SelectedKeyID:    entry.SelectedKeyID,
		VirtualKeyID:     entry.VirtualKeyID,
		UserID:           entry.UserID,
		TeamID:           entry.TeamID,
		CustomerID:       entry.CustomerID,
		BudgetIDs:        stringSlicePtr(entry.BudgetIDsParsed),
		RateLimitIDs:     stringSlicePtr(entry.RateLimitIDsParsed),
		NextCheckAt:      &next,
	}
	if entry.ID != "" {
		sourceLogID := entry.ID
		job.SourceLogID = &sourceLogID
	}
	// What settlement needs from the request and cannot get from the read that settles the
	// response: the tool settings a price depends on, and the key alias the create resolved.
	// See jobaccounting.ResponseJobParams. Neither holds message content.
	job.Params = params
	if err := p.batchStore.UpsertProviderJob(ctx, job); err != nil {
		p.logger.Warn("failed to record background response for provider=%s response_id=%s: %v", job.Provider, job.JobID, err)
	}
}

// accountResponseJob settles a finished background response from the read that found it finished,
// rather than leaving the sweeper to read it again. Whichever read gets here first settles it;
// every later read finds the job settled and writes nothing.
func (p *LoggerPlugin) accountResponseJob(entry *logstore.Log, resp *schemas.BifrostResponsesResponse) {
	// Bounded for the same reason as video settlement: p.ctx has no deadline, and a stalled store
	// would otherwise hold the caller's response. The sweeper re-drives anything that times out.
	settleCtx, cancel := context.WithTimeout(p.ctx, jobAccountingTimeout)
	defer cancel()

	p.mu.Lock()
	usageReporter := p.batchUsageReporter
	p.mu.Unlock()

	outcome, err := jobaccounting.AccountResponseJob(settleCtx, p.batchStore, p.store, p.pricingManager, jobaccounting.JobRequest{
		Provider:      schemas.ModelProvider(entry.Provider),
		ProviderJobID: *resp.ID,
		BaseLog:       entry,
		Emitter:       p,
		UsageReporter: usageReporter,
		ClaimedBy:     p.batchRunnerID("logging"),
		// No scopes from this request: the reader may be another caller, and the response is
		// priced under the key and virtual key that created it, which the engine reads off the job.
		Payload: jobaccounting.ResponsePayload{Response: resp},
	})
	if err != nil {
		p.logger.Warn("failed to account background response for provider=%s response_id=%s: %v", entry.Provider, *resp.ID, err)
		return
	}
	if outcome != nil && outcome.Accounted {
		p.logger.Info("accounted background response for provider=%s response_id=%s cost=%f log_id=%s", entry.Provider, *resp.ID, outcome.Cost, outcome.LogID)
	}
}
