package logging

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/jobaccounting"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// interactionFirstPoll is how soon the sweeper first reads a run that was still going when the
// request that reported it ended.
const interactionFirstPoll = time.Minute

// handleInteractionJob records and, once finished, settles the stored run a passthrough response
// reports on (a Gemini Interaction). The request row itself is never billed for the run: every read
// of a finished run repeats its full usage, so the provider reports that usage on ProviderJob rather
// than on PassthroughUsage, and the run is billed once, as its own row under the create.
//
// Callers check result.PassthroughResponse.ProviderJob first, so a request that reports no run pays
// only that nil check.
func (p *LoggerPlugin) handleInteractionJob(ctx *schemas.BifrostContext, entry *logstore.Log, result *schemas.BifrostResponse) {
	if entry == nil || result == nil || result.PassthroughResponse == nil || p.batchStore == nil {
		return
	}
	run := result.PassthroughResponse.ProviderJob
	if run == nil || run.ID == "" {
		return
	}
	if !p.recordInteractionJob(entry, run) {
		return
	}
	if run.Terminal && p.pricingManager != nil && p.store != nil {
		p.accountInteractionJob(ctx, entry, result)
	}
}

// recordInteractionJob persists the coordination row for a run, and reports whether one exists.
//
// Only the create writes a new row. A read may advance a run Bifrost saw created, but never start
// tracking one it did not: such a run could belong to anyone holding the provider key, and billing
// it to whoever polled it would charge the wrong caller.
func (p *LoggerPlugin) recordInteractionJob(entry *logstore.Log, run *schemas.BifrostPassthroughJob) bool {
	ctx, cancel := context.WithTimeout(p.ctx, jobAccountingTimeout)
	defer cancel()

	jobID := tables.ProviderJobID(tables.ProviderJobKindInteraction, entry.Provider, run.ID)
	if !run.Created {
		existing, err := p.batchStore.GetProviderJob(ctx, jobID)
		if err != nil || existing == nil {
			return false
		}
		// A settled or parked run is final. A read that started before it settled can finish after,
		// carrying the run as it was then, and must not rewrite the row back to running.
		if existing.AccountingStatus == tables.ProviderJobAccountingStatusAccounted ||
			existing.AccountingStatus == tables.ProviderJobAccountingStatusUnpriceable {
			return false
		}
	}

	job := &tables.TableProviderJob{
		ID:               jobID,
		Kind:             tables.ProviderJobKindInteraction,
		Provider:         entry.Provider,
		JobID:            run.ID,
		Model:            entry.Model,
		Endpoint:         run.PollPath,
		ProviderStatus:   run.Status,
		AccountingStatus: tables.ProviderJobAccountingStatusPending,
		SelectedKeyID:    entry.SelectedKeyID,
		VirtualKeyID:     entry.VirtualKeyID,
		UserID:           entry.UserID,
		TeamID:           entry.TeamID,
		CustomerID:       entry.CustomerID,
		BudgetIDs:        stringSlicePtr(entry.BudgetIDsParsed),
		RateLimitIDs:     stringSlicePtr(entry.RateLimitIDsParsed),
	}
	if run.Created {
		if entry.ID != "" {
			sourceLogID := entry.ID
			job.SourceLogID = &sourceLogID
		}
		if params, err := jobaccounting.MarshalInteractionParams(jobaccounting.InteractionJobParams{APIVersion: run.APIVersion}); err == nil {
			job.Params = params
		}
	}

	now := time.Now().UTC()
	next := now
	if !run.Terminal {
		next = now.Add(interactionFirstPoll)
	}
	job.NextCheckAt = &next

	if err := p.batchStore.UpsertProviderJob(ctx, job); err != nil {
		p.logger.Warn("failed to record interaction job for provider=%s interaction_id=%s: %v", job.Provider, job.JobID, err)
		return false
	}
	return true
}

// accountInteractionJob settles a finished run from the read that found it finished, rather than
// leaving the sweeper to read it all over again. Whichever read gets here first settles it; every
// later read takes the engine's mirror path and writes nothing.
func (p *LoggerPlugin) accountInteractionJob(ctx *schemas.BifrostContext, entry *logstore.Log, result *schemas.BifrostResponse) {
	run := result.PassthroughResponse.ProviderJob
	pricingScopes := modelcatalog.PricingLookupScopesFromContext(ctx, string(entry.Provider))

	// Bounded for the same reason as video settlement: p.ctx has no deadline, and a stalled store
	// would otherwise hold the caller's response. The sweeper re-drives anything that times out.
	settleCtx, cancel := context.WithTimeout(p.ctx, jobAccountingTimeout)
	defer cancel()

	p.mu.Lock()
	usageReporter := p.batchUsageReporter
	p.mu.Unlock()

	outcome, err := jobaccounting.AccountInteractionJob(settleCtx, p.batchStore, p.store, p.pricingManager, jobaccounting.JobRequest{
		Provider:      schemas.ModelProvider(entry.Provider),
		ProviderJobID: run.ID,
		FallbackModel: entry.Model,
		BaseLog:       entry,
		Emitter:       p,
		UsageReporter: usageReporter,
		ClaimedBy:     p.batchRunnerID("logging"),
		Scopes:        pricingScopes,
		Payload:       jobaccounting.InteractionPayload{Run: run, Model: result.PassthroughResponse.ServedModel},
	})
	if err != nil {
		p.logger.Warn("failed to account interaction for provider=%s interaction_id=%s: %v", entry.Provider, run.ID, err)
		return
	}
	if outcome != nil && outcome.Accounted {
		p.logger.Info("accounted interaction for provider=%s interaction_id=%s cost=%f log_id=%s", entry.Provider, run.ID, outcome.Cost, outcome.LogID)
	}
}
