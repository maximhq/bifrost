package jobaccounting

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// AccountJob settles one provider job: it advances the coordination state in
// stateStore (configstore) and writes the append-only aggregate cost row via
// logStore (logstore). Ownership is fenced on req.ClaimedBy (the runner id).
//
// Everything kind-specific — what the price is, what detail rides on the row —
// comes from settler. Everything here is the part every kind shares.
func AccountJob(ctx context.Context, stateStore JobStore, logStore AggregateLogStore, pricing PricingManager, settler Settler, req JobRequest) (*Outcome, error) {
	if stateStore == nil {
		return nil, fmt.Errorf("job accounting state store is nil")
	}
	if logStore == nil {
		return nil, fmt.Errorf("job accounting log store is nil")
	}
	if pricing == nil {
		return nil, fmt.Errorf("job accounting pricing manager is nil")
	}
	if settler == nil {
		return nil, fmt.Errorf("job accounting settler is nil")
	}
	if req.Provider == "" || req.ProviderJobID == "" {
		return nil, nil
	}
	if req.Job == nil {
		return nil, fmt.Errorf("job accounting coordination row is nil")
	}
	if req.ClaimedBy == "" {
		return nil, fmt.Errorf("job accounting runner id (ClaimedBy) is required")
	}

	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	job := req.Job
	if err := stateStore.UpsertProviderJob(ctx, job); err != nil {
		return nil, err
	}

	out := &Outcome{
		JobID:         job.ID,
		LogID:         AccountingLogID(settler.Kind(), req.Provider, req.ProviderJobID),
		Provider:      req.Provider,
		ProviderJobID: req.ProviderJobID,
	}

	runnerID := req.ClaimedBy
	claimed, err := stateStore.ClaimProviderJob(ctx, job.ID, runnerID, now.Add(-defaultClaimTTL), req.ForceClaim)
	if err != nil {
		return nil, err
	}
	out.Claimed = claimed
	if !claimed {
		// This call did not settle the job — either it was already settled by an
		// earlier call, or another runner is settling it right now. Either way, the
		// caller still wants to show a price if one already exists, so mirror the
		// already-written aggregate row for display. This never writes anything: no
		// job transition, no governance report, no count change.
		populateOutcomeFromExistingLog(ctx, logStore, settler, out)
		return out, nil
	}
	// The persisted row carries the only record of how far a previous attempt got
	// (AggregateLogWrittenAt / GovernanceReportedAt). Callers may hand us a freshly
	// built job with those markers unset, so if this read fails we cannot tell a
	// partially-settled job from a new one — proceeding would re-report usage that
	// already landed. Fail closed, releasing the claim so a later attempt can retry
	// cleanly rather than waiting out the claim TTL.
	persisted, err := stateStore.GetProviderJob(ctx, job.ID)
	if err != nil {
		_ = stateStore.FailProviderJob(ctx, job.ID, runnerID, err)
		return nil, err
	}
	if persisted != nil {
		mergeJobHints(job, persisted)
	}
	req.Job = job
	req.SourceLog = resolveSourceLog(ctx, logStore, job, req.BaseLog)
	if req.FallbackModel == "" {
		req.FallbackModel = job.Model
	}
	req.Scopes = pricingScopesForJob(req.Scopes, req.Provider, job)
	// job.ProviderStatus is fixed for the rest of this call — the caller set it
	// before invoking AccountJob and nothing here advances it.
	out.Status = job.ProviderStatus

	settlement, err := settler.Settle(ctx, pricing, req)
	if err != nil {
		_ = stateStore.FailProviderJob(ctx, job.ID, runnerID, err)
		return nil, err
	}
	if settlement == nil {
		settlement = &Settlement{UnpriceableReason: "no_settlement"}
	}

	out.Usage = settlement.Usage
	out.ModelBreakdowns = settlement.ModelBreakdowns
	out.PricedCount = settlement.PricedCount
	out.UnpricedCount = settlement.UnpricedCount
	out.FailedCount = settlement.FailedCount
	out.UnpricedModel = settlement.UnpricedModel

	if !settlement.Priced {
		out.UnpriceableReason = settlement.UnpriceableReason
		// The job ran and consumed work we could not price (typically the catalog has
		// no rates for the model yet). Log the row with an unknown cost rather than
		// dropping it: this mirrors the synchronous path, which always writes the row
		// and leaves cost NULL, and it puts the row in reach of the existing
		// missing-cost backfill. Skipping the write would lose the usage for good —
		// the raw provider results are not persisted anywhere else.
		if settlement.RecordUnpriced && job.AggregateLogWrittenAt == nil {
			entry := buildAggregateLog(req, settlement, out, now, userAgentFromContext(ctx))
			// Unknown, not zero — a zero cost would read as "this job was free".
			entry.Cost = nil
			if err := writeAggregateLog(ctx, stateStore, logStore, req, job, runnerID, entry); err != nil {
				return nil, err
			}
		}
		if err := stateStore.MarkProviderJobUnpriceable(ctx, job.ID, runnerID, settlement.UnpriceableReason, settlement.ReasonErr); err != nil {
			// Release the fence like every other failure path here. Best-effort by
			// nature: the mark above is itself a store write, so if it failed this one
			// probably will too — but leaving the job "processing" under a runner that
			// has already given up strands it for the whole claim TTL.
			_ = stateStore.FailProviderJob(ctx, job.ID, runnerID, err)
			return nil, err
		}
		return out, nil
	}

	out.Cost = settlement.Cost
	out.Complete = settlement.Complete

	entry := buildAggregateLog(req, settlement, out, now, userAgentFromContext(ctx))
	if !settlement.Complete {
		// Some work priced and some did not. out.Cost is therefore only the known part
		// of the bill, and persisting it as the row's cost would be a lie with
		// consequences: a positive cost puts the row outside the missing-cost backfill
		// (`cost IS NULL OR cost <= 0`), so the unpriced share could never be recovered
		// through the designed recovery path, and governance would be told a partial
		// number is the job's final bill. Write the row with an unknown cost instead —
		// the full usage and the per-model breakdowns are all on it, so a later
		// recalculation can complete it — and leave the job re-drivable rather than
		// marking it accounted.
		entry.Cost = nil
	}
	if job.AggregateLogWrittenAt == nil {
		if err := writeAggregateLog(ctx, stateStore, logStore, req, job, runnerID, entry); err != nil {
			return nil, err
		}
	}
	if !settlement.Complete {
		// Neither report nor complete: governance would receive a total that is known
		// to be short, and "accounted" is terminal forever. Park the job unpriceable —
		// which stops polling but stays claimable by a settlement holding inputs (see
		// ForceClaim) — so recalculation plus a re-drive remain the recovery path.
		out.UnpriceableReason = settlement.UnpriceableReason
		if err := stateStore.MarkProviderJobUnpriceable(ctx, job.ID, runnerID, out.UnpriceableReason, nil); err != nil {
			_ = stateStore.FailProviderJob(ctx, job.ID, runnerID, err)
			return nil, err
		}
		return out, nil
	}
	if req.UsageReporter != nil && job.GovernanceReportedAt == nil {
		if err := req.UsageReporter.ReportUsage(ctx, usageReportFor(req.Provider, entry, out)); err != nil {
			_ = stateStore.FailProviderJob(ctx, job.ID, runnerID, err)
			return nil, err
		}
		if err := stateStore.MarkProviderJobGovernanceReported(ctx, job.ID, runnerID); err != nil {
			_ = stateStore.FailProviderJob(ctx, job.ID, runnerID, err)
			return nil, err
		}
	}
	if err := stateStore.CompleteProviderJob(ctx, job.ID, runnerID); err != nil {
		_ = stateStore.FailProviderJob(ctx, job.ID, runnerID, err)
		return nil, err
	}
	out.Accounted = true
	return out, nil
}

// writeAggregateLog persists the aggregate row, marks the job, and emits. Any
// failure releases the claim so a later attempt can retry cleanly.
func writeAggregateLog(ctx context.Context, stateStore JobStore, logStore AggregateLogStore, req JobRequest, job *cstables.TableProviderJob, runnerID string, entry *logstore.Log) error {
	if err := logStore.CreateIfNotExists(ctx, entry); err != nil {
		_ = stateStore.FailProviderJob(ctx, job.ID, runnerID, err)
		return err
	}
	if err := stateStore.MarkProviderJobAggregateLogWritten(ctx, job.ID, runnerID); err != nil {
		_ = stateStore.FailProviderJob(ctx, job.ID, runnerID, err)
		return err
	}
	if req.Emitter != nil {
		req.Emitter.EmitAggregateLog(ctx, entry)
	}
	return nil
}

// populateOutcomeFromExistingLog fills out from the aggregate log row already
// written for it, if any, so a caller that lost the settlement claim can still
// display the job's price. out.LogID is deterministic and set unconditionally
// before the claim attempt, so this looks up the same row settlement would have
// written — nothing here writes.
//
// A missing row (job never reached a write, or is still being settled by another
// runner) leaves out at its zero value: nothing to show yet, which is the correct
// display for "not priced".
func populateOutcomeFromExistingLog(ctx context.Context, logStore AggregateLogStore, settler Settler, out *Outcome) {
	entry, err := logStore.FindByID(ctx, out.LogID)
	if err != nil || entry == nil {
		return
	}
	if entry.TokenUsageParsed != nil {
		out.Usage = *entry.TokenUsageParsed
	}
	if entry.Cost != nil {
		out.Cost = *entry.Cost
		// buildAggregateLog only ever leaves Cost nil for an incomplete total, so a
		// non-nil Cost on the persisted row means everything priced when it was
		// written.
		out.Complete = true
	}
	settler.HydrateFromLog(entry, out)
}

func mergeJobHints(dst *cstables.TableProviderJob, src *cstables.TableProviderJob) {
	if dst == nil || src == nil {
		return
	}
	if dst.Model == "" {
		dst.Model = src.Model
	}
	if dst.Endpoint == "" {
		dst.Endpoint = src.Endpoint
	}

	if dst.Params == nil {
		dst.Params = src.Params
	}
	dst.AggregateLogWrittenAt = src.AggregateLogWrittenAt
	dst.GovernanceReportedAt = src.GovernanceReportedAt
	dst.SelectedKeyID = src.SelectedKeyID
	dst.VirtualKeyID = src.VirtualKeyID
	dst.UserID = src.UserID
	dst.TeamID = src.TeamID
	dst.CustomerID = src.CustomerID
	dst.BudgetIDs = src.BudgetIDs
	dst.RateLimitIDs = src.RateLimitIDs
	dst.SourceLogID = src.SourceLogID
}

func pricingScopesForJob(scopes *modelcatalog.PricingLookupScopes, provider schemas.ModelProvider, job *cstables.TableProviderJob) *modelcatalog.PricingLookupScopes {
	if scopes == nil {
		scopes = &modelcatalog.PricingLookupScopes{}
	} else {
		copied := *scopes
		scopes = &copied
	}
	if scopes.Provider == "" {
		scopes.Provider = string(provider)
	}
	if job == nil {
		return scopes
	}
	if scopes.SelectedKeyID == "" {
		scopes.SelectedKeyID = job.SelectedKeyID
	}
	if scopes.VirtualKeyID == "" && job.VirtualKeyID != nil {
		scopes.VirtualKeyID = *job.VirtualKeyID
	}
	return scopes
}

func usageReportFor(provider schemas.ModelProvider, entry *logstore.Log, out *Outcome) UsageReport {
	report := UsageReport{
		RequestID:    entry.ID,
		Provider:     provider,
		Model:        entry.Model,
		TokensUsed:   int64(entry.TotalTokens),
		BudgetIDs:    stringSliceFromParsedOrJSON(entry.BudgetIDsParsed, entry.BudgetIDs),
		RateLimitIDs: stringSliceFromParsedOrJSON(entry.RateLimitIDsParsed, entry.RateLimitIDs),
	}
	if entry.UserID != nil {
		report.UserID = *entry.UserID
	}
	if entry.VirtualKeyID != nil {
		report.VirtualKeyID = *entry.VirtualKeyID
	}
	report.ModelUsage = modelUsageFromBreakdowns(out.ModelBreakdowns)
	if entry.Cost != nil {
		report.Cost = *entry.Cost
	}
	return report
}

// modelUsageFromBreakdowns flattens the settled per-model split.
func modelUsageFromBreakdowns(breakdowns map[string]schemas.BatchModelBreakdown) []ModelUsage {
	if len(breakdowns) == 0 {
		return nil
	}
	usage := make([]ModelUsage, 0, len(breakdowns))
	for model, breakdown := range breakdowns {
		if model == "" {
			continue
		}
		cost := 0.0
		if breakdown.Cost != nil {
			cost = *breakdown.Cost
		}
		usage = append(usage, ModelUsage{Model: model, Cost: cost, TokensUsed: int64(breakdown.Usage.TotalTokens)})
	}
	if len(usage) == 0 {
		return nil
	}
	// These become billing keys, so order must be stable across retries.
	sort.Slice(usage, func(i, j int) bool { return usage[i].Model < usage[j].Model })
	return usage
}

func stringSliceFromParsedOrJSON(parsed []string, raw *string) []string {
	if len(parsed) > 0 {
		return parsed
	}
	if raw == nil || *raw == "" {
		return nil
	}
	var values []string
	if err := sonic.Unmarshal([]byte(*raw), &values); err != nil {
		return nil
	}
	return values
}

// userAgentFromContext builds the user agent for bifrost workers.
func userAgentFromContext(ctx context.Context) string {
	if version, ok := ctx.Value(schemas.BifrostContextKeyRuntimeVersion).(string); ok && version != "" {
		return "bifrost/" + version
	}
	return "bifrost"
}

// batchAccountingLogNamespace is the UUIDv5 namespace for batch aggregate cost log
// ids.
//
// It must never change. The derived id is both the idempotency key for the
// aggregate write and the governance dedupe key, so a new namespace would make
// every already-settled batch look unsettled and re-report its usage. New kinds
// get their own namespace constant rather than perturbing this one.
var batchAccountingLogNamespace = uuid.MustParse("90a2f576-5276-481f-a753-2c78ccf97607")

// AccountingLogID derives the id of the aggregate cost log for a job.
//
// The id is deterministic by design: CreateIfNotExists keys on it so a replayed
// settlement is a no-op, and it is reused as UsageReport.RequestID so the
// governance reporter can dedupe. UUIDv5 gives that determinism while keeping the
// id a plain UUID like every other log id — ids travel back to the store through
// URL path parameters, and anything needing percent-encoding does not survive that
// round trip cleanly.
//
// The batch input string is reproduced byte for byte from before kinds existed;
// changing it would orphan every settled batch.
func AccountingLogID(kind ProviderJobKind, provider schemas.ModelProvider, providerJobID string) string {
	logNamespacesMu.RLock()
	namespace, ok := logNamespaces[kind]
	logNamespacesMu.RUnlock()
	if !ok {
		namespace = batchAccountingLogNamespace
	}
	return uuid.NewSHA1(namespace, []byte(string(provider)+":"+providerJobID)).String()
}

var (
	logNamespacesMu sync.RWMutex
	logNamespaces   = map[ProviderJobKind]uuid.UUID{
		ProviderJobKindBatch: batchAccountingLogNamespace,
	}
)

// RegisterLogNamespace binds a UUIDv5 namespace to a job kind. A kind must
// register before it settles anything, and its namespace must never change
// afterwards for the same reason the batch one cannot.
func RegisterLogNamespace(kind ProviderJobKind, namespace uuid.UUID) {
	logNamespacesMu.Lock()
	defer logNamespacesMu.Unlock()
	logNamespaces[kind] = namespace
}

func buildAggregateLog(req JobRequest, settlement *Settlement, out *Outcome, now time.Time, userAgent string) *logstore.Log {
	entry := &logstore.Log{
		ID:               out.LogID,
		Timestamp:        now,
		Object:           string(settlement.Object),
		Provider:         string(req.Provider),
		Model:            settlement.Model,
		Status:           "success",
		Cost:             &out.Cost,
		TokenUsageParsed: &out.Usage,
		PromptTokens:     out.Usage.PromptTokens,
		CompletionTokens: out.Usage.CompletionTokens,
		TotalTokens:      out.Usage.TotalTokens,
		CreatedAt:        now,
		UserAgent:        &userAgent,
	}
	if settlement.ApplyDebug != nil {
		settlement.ApplyDebug(entry)
	}
	// Attribution must not depend on who happens to settle the job first. The
	// sweeper reaches here with only the job; an inline call reaches here with the
	// caller's log entry too — and copying that wholesale meant the same job billed
	// the creator's budgets or the fetcher's depending on a race, so an admin reading
	// someone else's results absorbed the entire bill. The job's create-time
	// attribution is the one identity that does not move, so it wins whenever it
	// exists.
	//
	// The display names come from SourceLog — the creating request's own log row —
	// rather than from whoever is settling. Matching the two on virtual key was not
	// enough to keep identities apart: an access profile hands one virtual key to
	// many users, so two different people compared equal and the settling user's name
	// landed on the creator's cost row. A job first seen at settlement has no
	// create-time attribution to prefer, so it keeps using BaseLog.
	if req.Job != nil && hasJobAttribution(req.Job) {
		if req.SourceLog != nil {
			applyLogDenormalizations(entry, req.SourceLog)
		}
		applyJobAttribution(entry, req.Job)
		// Provenance, not attribution: which request triggered this settlement.
		if req.BaseLog != nil && req.BaseLog.ID != "" {
			entry.ParentRequestID = &req.BaseLog.ID
		}
		return entry
	}
	if req.BaseLog != nil {
		applyLogAttribution(entry, req.BaseLog)
		return entry
	}
	if req.Job != nil {
		applyJobAttribution(entry, req.Job)
	}
	return entry
}

// resolveSourceLog loads the log row of the request that created the job, for the
// display names the coordination row does not carry. Best-effort by design: the
// names are cosmetic, the ids on the job are what bills, so a log row that has been
// rotated away or cannot be read costs nothing but the labels.
func resolveSourceLog(ctx context.Context, logStore AggregateLogStore, job *cstables.TableProviderJob, baseLog *logstore.Log) *logstore.Log {
	if job == nil || job.SourceLogID == nil || *job.SourceLogID == "" {
		return nil
	}
	if baseLog != nil && baseLog.ID == *job.SourceLogID {
		return baseLog
	}
	source, err := logStore.FindByID(ctx, *job.SourceLogID)
	if err != nil {
		return nil
	}
	return source
}

// hasJobAttribution reports whether the job captured who to bill at create time.
// Jobs created outside Bifrost (first seen at settlement) carry none.
func hasJobAttribution(job *cstables.TableProviderJob) bool {
	if job.SelectedKeyID != "" || (job.VirtualKeyID != nil && *job.VirtualKeyID != "") {
		return true
	}
	return (job.BudgetIDs != nil && *job.BudgetIDs != "") || (job.RateLimitIDs != nil && *job.RateLimitIDs != "")
}

func applyLogAttribution(entry *logstore.Log, source *logstore.Log) {
	if source.ID != "" {
		entry.ParentRequestID = &source.ID
	}
	entry.SelectedKeyID = source.SelectedKeyID
	entry.VirtualKeyID = source.VirtualKeyID
	entry.BudgetIDsParsed = source.BudgetIDsParsed
	entry.RateLimitIDsParsed = source.RateLimitIDsParsed
	applyLogDenormalizations(entry, source)
}

// applyLogDenormalizations copies the display and grouping fields a log row
// carries but the coordination row does not persist. Deliberately excludes the ids
// the job owns (selected key, virtual key, budgets, rate limits): those decide who
// is billed, and mixing them with another request's would reintroduce exactly the
// attribution race this split exists to remove.
func applyLogDenormalizations(entry *logstore.Log, source *logstore.Log) {
	entry.SelectedKeyName = source.SelectedKeyName
	entry.VirtualKeyName = source.VirtualKeyName
	entry.RoutingRuleID = source.RoutingRuleID
	entry.RoutingRuleName = source.RoutingRuleName
	entry.UserID = source.UserID
	entry.UserName = source.UserName
	entry.TeamID = source.TeamID
	entry.TeamName = source.TeamName
	entry.CustomerID = source.CustomerID
	entry.CustomerName = source.CustomerName
	entry.BusinessUnitID = source.BusinessUnitID
	entry.BusinessUnitName = source.BusinessUnitName
	entry.TeamIDsParsed = source.TeamIDsParsed
	entry.TeamNamesParsed = source.TeamNamesParsed
	entry.CustomerIDsParsed = source.CustomerIDsParsed
	entry.CustomerNamesParsed = source.CustomerNamesParsed
	entry.BusinessUnitIDsParsed = source.BusinessUnitIDsParsed
	entry.BusinessUnitNamesParsed = source.BusinessUnitNamesParsed
	entry.ClusterNodeID = source.ClusterNodeID
	entry.Alias = source.Alias
	entry.CanonicalModelName = source.CanonicalModelName
	entry.AliasModelFamily = source.AliasModelFamily
}

func applyJobAttribution(entry *logstore.Log, job *cstables.TableProviderJob) {
	entry.SelectedKeyID = job.SelectedKeyID
	entry.VirtualKeyID = job.VirtualKeyID
	entry.BudgetIDs = job.BudgetIDs
	entry.RateLimitIDs = job.RateLimitIDs
	if job.UserID != nil {
		entry.UserID = job.UserID
	}
	if job.TeamID != nil {
		entry.TeamID = job.TeamID
	}
	if job.CustomerID != nil {
		entry.CustomerID = job.CustomerID
	}
}
