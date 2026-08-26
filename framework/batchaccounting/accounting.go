package batchaccounting

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const (
	defaultClaimTTL = 5 * time.Minute

	UnpriceableReasonNoResults           = "no_results"
	UnpriceableReasonNoUsage             = "no_usage"
	UnpriceableReasonMissingModel        = "missing_model"
	UnpriceableReasonMissingBatchPricing = "missing_batch_pricing"
	UnpriceableReasonMaxPollAttempts     = "max_poll_attempts"
	UnpriceableReasonResultParseErrors   = "result_parse_errors"
)

// BatchJobStore is the mutable coordination-state store for delayed batch
// accounting. It is satisfied by configstore.ConfigStore (the batch lifecycle is
// a state machine, which belongs in the relational config store rather than the
// append-only log store). Ownership is fenced on a runner id.
type BatchJobStore interface {
	UpsertBatchJob(ctx context.Context, job *cstables.TableBatchJob) error
	GetBatchJob(ctx context.Context, jobID string) (*cstables.TableBatchJob, error)
	// ClaimBatchJob fences the job on runnerID. allowUnpriceable relaxes the
	// terminal-state guard to admit "unpriceable" jobs: that state means "stop
	// polling", not "refuse money", so a caller that actually holds results may
	// re-drive one. Only settlement paths with results in hand pass true — the
	// sweeper's poll loop must keep skipping unpriceable jobs (it does, via
	// ListDueBatchJobs). "accounted" is terminal forever either way.
	ClaimBatchJob(ctx context.Context, jobID, runnerID string, staleBefore time.Time, allowUnpriceable bool) (bool, error)
	MarkBatchJobAggregateLogWritten(ctx context.Context, jobID, runnerID string) error
	MarkBatchJobGovernanceReported(ctx context.Context, jobID, runnerID string) error
	CompleteBatchJob(ctx context.Context, jobID, runnerID string) error
	MarkBatchJobUnpriceable(ctx context.Context, jobID, runnerID, reason string, err error) error
	FailBatchJob(ctx context.Context, jobID, runnerID string, err error) error
}

// AggregateLogStore writes the append-only aggregate batch cost record. It is
// satisfied by logstore.LogStore — the cost record lives in the log store next to
// every other request cost row.
type AggregateLogStore interface {
	CreateIfNotExists(ctx context.Context, entry *logstore.Log) error
	// FindByID reads the aggregate cost row back, keyed by the same deterministic
	// id CreateIfNotExists wrote it under. Used only to mirror an already-settled
	// batch's price onto a caller that did not win this call's settlement claim
	// (see the !claimed branch in AccountBatchResults) — never to drive a write.
	FindByID(ctx context.Context, id string) (*logstore.Log, error)
}

type AggregateLogEmitter interface {
	EmitBatchAggregateLog(ctx context.Context, entry *logstore.Log)
}

// UsageReporter receives the settled usage/cost for a batch so it can be billed.
//
// Implementations MUST be idempotent on BatchUsageReport.RequestID: settlement is
// at-least-once, so the same report can be delivered more than once when the
// durable "reported" marker fails to persist after a successful report. See the
// package doc for the exact window and its limits.
type UsageReporter interface {
	ReportBatchUsage(ctx context.Context, usage BatchUsageReport) error
}

type BatchUsageReport struct {
	RequestID    string
	Provider     schemas.ModelProvider
	Model        string
	Cost         float64
	TokensUsed   int64
	BudgetIDs    []string
	RateLimitIDs []string
	UserID       string
	VirtualKeyID string
	ModelUsage   []BatchModelUsage
}

// BatchModelUsage is one model's share of a settled batch.
type BatchModelUsage struct {
	Model      string
	Cost       float64
	TokensUsed int64
}

type PricingManager interface {
	CalculateBatchCostDetailsForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *modelcatalog.PricingLookupScopes) modelcatalog.BatchCostDetails
}

type Request struct {
	Provider      schemas.ModelProvider
	BatchID       string
	FallbackModel string
	Endpoint      schemas.BatchEndpoint
	Results       []schemas.BatchResultItem
	ParseErrors   []schemas.BatchError
	RequestCounts *schemas.BatchRequestCounts
	BatchJob      *cstables.TableBatchJob
	BaseLog       *logstore.Log
	SourceLog     *logstore.Log
	Emitter       AggregateLogEmitter
	UsageReporter UsageReporter
	ClaimedBy     string
	Scopes        *modelcatalog.PricingLookupScopes
	Now           time.Time
}

type Summary struct {
	JobID           string                    `json:"job_id"`
	LogID           string                    `json:"log_id"`
	Provider        schemas.ModelProvider     `json:"provider"`
	BatchID         string                    `json:"batch_id"`
	Cost            float64                   `json:"cost"`
	Usage           schemas.BifrostLLMUsage   `json:"usage"`
	ModelBreakdowns map[string]ModelBreakdown `json:"model_breakdowns"`
	PricedCount     int                       `json:"priced_count"`
	UnpricedCount   int                       `json:"unpriced_count"`
	FailedCount     int                       `json:"failed_count"`
	// UnpricedModel names the model behind wholly-unpriced usage when it is
	// unambiguous, so the logged row is attributable (and therefore backfillable).
	// Empty when the usage spans several models or carried no model at all.
	UnpricedModel string `json:"unpriced_model,omitempty"`
	// Status is the provider's batch lifecycle status as of this settlement attempt
	// (from req.BatchJob.ProviderStatus when claimed, or mirrored from the
	// already-written aggregate row otherwise). See BifrostBatchDebug.Status.
	Status string `json:"status,omitempty"`
	// Complete reports that every usage-bearing row priced, so Cost is the batch's
	// real total. When false the batch consumed tokens we could not price and Cost
	// is only the known part — it must never be persisted as the row's cost, and
	// governance must not be told the batch settled for it.
	Complete          bool   `json:"complete"`
	Accounted         bool   `json:"accounted"`
	Claimed           bool   `json:"claimed"`
	UnpriceableReason string `json:"unpriceable_reason,omitempty"`
}

// ModelBreakdown is the per-model slice of a settled batch's usage and cost.
//
// It is an alias rather than a local type because the breakdown is persisted on
// the aggregate log row (schemas.BifrostBatchDebug), and logstore cannot import
// this package — batchaccounting already imports logstore.
type ModelBreakdown = schemas.BatchModelBreakdown

type extractedUsage struct {
	model        string
	usage        *schemas.BifrostLLMUsage
	hasUsage     bool
	missingModel bool
}

// AccountBatchResults settles a completed batch: it advances the coordination
// state in stateStore (configstore) and writes the append-only aggregate cost row
// via logStore (logstore). Ownership is fenced on req.ClaimedBy (the runner id).
func AccountBatchResults(ctx context.Context, stateStore BatchJobStore, logStore AggregateLogStore, pricing PricingManager, req Request) (*Summary, error) {
	if stateStore == nil {
		return nil, fmt.Errorf("batch accounting state store is nil")
	}
	if logStore == nil {
		return nil, fmt.Errorf("batch accounting log store is nil")
	}
	if pricing == nil {
		return nil, fmt.Errorf("batch accounting pricing manager is nil")
	}
	if req.Provider == "" || req.BatchID == "" {
		return nil, nil
	}

	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	job := req.BatchJob
	if job == nil {
		job = &cstables.TableBatchJob{
			Provider:         string(req.Provider),
			BatchID:          req.BatchID,
			Model:            req.FallbackModel,
			Endpoint:         string(req.Endpoint),
			AccountingStatus: cstables.BatchJobAccountingStatusPending,
		}
	}
	if job.Endpoint == "" && req.Endpoint != "" {
		job.Endpoint = string(req.Endpoint)
	}
	if job.ID == "" {
		job.ID = cstables.BatchJobID(string(req.Provider), req.BatchID)
	}
	if err := stateStore.UpsertBatchJob(ctx, job); err != nil {
		return nil, err
	}

	summary := &Summary{
		JobID:    job.ID,
		LogID:    AccountingLogID(req.Provider, req.BatchID),
		Provider: req.Provider,
		BatchID:  req.BatchID,
	}

	runnerID := req.ClaimedBy
	// Holding results is what earns the right to re-drive an unpriceable job: the
	// state was reached by giving up on polling (max attempts, a terminal status
	// with nothing to fetch, unpriceable rows), and none of those reasons survive
	// a caller arriving with the actual rows. Without results there is nothing new
	// to say, so the terminal guard stands.
	claimed, err := stateStore.ClaimBatchJob(ctx, job.ID, runnerID, now.Add(-defaultClaimTTL), len(req.Results) > 0)
	if err != nil {
		return nil, err
	}
	summary.Claimed = claimed
	if !claimed {
		// This call did not settle the batch — either it was already settled by an
		// earlier call, or another runner is settling it right now. Either way, the
		// caller (e.g. a repeated /v1/batches/.../results fetch) still wants to show
		// a price if one already exists, so mirror the already-written aggregate row
		// for display. This never writes anything: no job transition, no governance
		// report, no count change — a losing claim earns a read, nothing more.
		populateSummaryFromExistingLog(ctx, logStore, summary)
		return summary, nil
	}
	// The persisted row carries the only record of how far a previous attempt got
	// (AggregateLogWrittenAt / GovernanceReportedAt). Callers may hand us a
	// freshly-built job with those markers unset, so if this read fails we cannot
	// tell a partially-settled batch from a new one — proceeding would re-report
	// usage that already landed. Fail closed, releasing the claim so a later
	// attempt can retry cleanly rather than waiting out the claim TTL.
	persisted, err := stateStore.GetBatchJob(ctx, job.ID)
	if err != nil {
		_ = stateStore.FailBatchJob(ctx, job.ID, runnerID, err)
		return nil, err
	}
	if persisted != nil {
		mergeBatchJobHints(job, persisted)
	}
	req.BatchJob = job
	req.SourceLog = resolveSourceLog(ctx, logStore, job, req.BaseLog)
	if req.FallbackModel == "" {
		req.FallbackModel = job.Model
	}
	if req.Endpoint == "" {
		req.Endpoint = schemas.BatchEndpoint(job.Endpoint)
	}
	req.Scopes = pricingScopesForBatchJob(req.Scopes, req.Provider, job)
	// job.ProviderStatus is fixed for the rest of this call — the caller set it
	// before invoking AccountBatchResults (from the just-fetched retrieve response,
	// or hardcoded to "completed" on the inline /results path) and nothing here
	// advances it. Every aggregate-log write below reads it via summary.Status.
	summary.Status = job.ProviderStatus

	// Malformed rows are no longer a reason to abandon the batch. One bad JSONL line
	// used to discard every correctly parsed row's tokens and cost, permanently — the
	// raw provider results are not persisted anywhere else, so what is not priced here
	// is lost for good. The parsed rows are priced normally and the count of rows we
	// could not read is carried onto the aggregate row (parse_error_count) together
	// with the incomplete marker, so the total is recorded as under-stating rather than
	// silently pretending to be whole.
	computed, err := summarizeResults(pricing, req)
	if err != nil {
		_ = stateStore.FailBatchJob(ctx, job.ID, runnerID, err)
		return nil, err
	}
	if computed == nil || computed.PricedCount == 0 {
		reason := UnpriceableReasonNoUsage
		if computed != nil && computed.UnpriceableReason != "" {
			reason = computed.UnpriceableReason
		}
		// Parse errors only become the headline reason when they are the sole cause.
		// If rows did parse and carried usage we simply could not price, that is the
		// more specific and more actionable diagnosis.
		var reasonErr error
		if len(req.ParseErrors) > 0 && (reason == UnpriceableReasonNoResults || reason == UnpriceableReasonNoUsage) {
			reason = UnpriceableReasonResultParseErrors
			reasonErr = fmt.Errorf("batch result contained %d malformed row(s)", len(req.ParseErrors))
		}
		summary.UnpriceableReason = reason
		if computed != nil {
			summary.Usage = computed.Usage
			summary.ModelBreakdowns = computed.ModelBreakdowns
			summary.UnpricedCount = computed.UnpricedCount
			summary.FailedCount = computed.FailedCount
		}
		// The batch ran and consumed tokens we simply could not price (typically the
		// catalog has no batch rates for the model yet). Log the row with an unknown
		// cost rather than dropping it: this mirrors the synchronous path, which
		// always writes the row and leaves cost NULL, and it puts the row in reach of
		// the existing missing-cost backfill (SearchFilters.MissingCostOnly selects
		// `cost IS NULL OR cost <= 0`, and recalculation re-derives cost from the
		// row's stored usage). Skipping the write would lose the usage for good — the
		// raw provider results are not persisted anywhere else.
		if computed != nil && summary.Usage.TotalTokens > 0 && job.AggregateLogWrittenAt == nil {
			entry := buildAggregateLog(req, summary, now, userAgentFromContext(ctx))
			// Unknown, not zero — a zero cost would read as "this batch was free".
			entry.Cost = nil
			if computed.UnpricedModel != "" {
				entry.Model = computed.UnpricedModel
			} else if len(computed.ModelBreakdowns) == 1 {
				// buildAggregateLog names the row after a single ModelBreakdown entry,
				// but UnpricedModel was deliberately left blank — the only way that
				// happens with exactly one breakdown is a missing-model row mixed in
				// (see summarizeResults), meaning Usage is a mixture that must not be
				// attributed to the one named model.
				entry.Model = "mixed"
			}
			if err := logStore.CreateIfNotExists(ctx, entry); err != nil {
				_ = stateStore.FailBatchJob(ctx, job.ID, runnerID, err)
				return nil, err
			}
			if err := stateStore.MarkBatchJobAggregateLogWritten(ctx, job.ID, runnerID); err != nil {
				_ = stateStore.FailBatchJob(ctx, job.ID, runnerID, err)
				return nil, err
			}
			if req.Emitter != nil {
				req.Emitter.EmitBatchAggregateLog(ctx, entry)
			}
		}
		if err := stateStore.MarkBatchJobUnpriceable(ctx, job.ID, runnerID, reason, reasonErr); err != nil {
			return nil, err
		}
		return summary, nil
	}

	summary.Cost = computed.Cost
	summary.Usage = computed.Usage
	summary.ModelBreakdowns = computed.ModelBreakdowns
	summary.PricedCount = computed.PricedCount
	summary.UnpricedCount = computed.UnpricedCount
	summary.FailedCount = computed.FailedCount
	summary.Complete = computed.Complete

	entry := buildAggregateLog(req, summary, now, userAgentFromContext(ctx))
	if !summary.Complete {
		// Some usage priced and some did not. summary.Cost is therefore only the known
		// part of the bill, and persisting it as the row's cost would be a lie with
		// consequences: a positive cost puts the row outside the missing-cost backfill
		// (`cost IS NULL OR cost <= 0`), so the unpriced share could never be recovered
		// through the designed recovery path, and governance would be told a partial
		// number is the batch's final bill. Write the row with an unknown cost instead —
		// the full usage and the per-model breakdowns (with the known costs) are all on
		// it, so a later recalculation can complete it — and leave the job re-drivable
		// rather than marking it accounted.
		entry.Cost = nil
	}
	if job.AggregateLogWrittenAt == nil {
		if err := logStore.CreateIfNotExists(ctx, entry); err != nil {
			_ = stateStore.FailBatchJob(ctx, job.ID, runnerID, err)
			return nil, err
		}
		if err := stateStore.MarkBatchJobAggregateLogWritten(ctx, job.ID, runnerID); err != nil {
			_ = stateStore.FailBatchJob(ctx, job.ID, runnerID, err)
			return nil, err
		}
		if req.Emitter != nil {
			req.Emitter.EmitBatchAggregateLog(ctx, entry)
		}
	}
	if !summary.Complete {
		// Neither report nor complete: governance would receive a total that is known
		// to be short, and "accounted" is terminal forever. Park the job unpriceable —
		// which stops polling but stays claimable by a settlement holding results (see
		// ClaimBatchJob's allowUnpriceable) — so recalculation plus a re-drive remain
		// the recovery path.
		summary.UnpriceableReason = computed.UnpriceableReason
		if summary.UnpriceableReason == "" {
			summary.UnpriceableReason = UnpriceableReasonMissingBatchPricing
		}
		if err := stateStore.MarkBatchJobUnpriceable(ctx, job.ID, runnerID, summary.UnpriceableReason, nil); err != nil {
			return nil, err
		}
		return summary, nil
	}
	if req.UsageReporter != nil && job.GovernanceReportedAt == nil {
		if err := req.UsageReporter.ReportBatchUsage(ctx, batchUsageReportFromLog(req.Provider, entry)); err != nil {
			_ = stateStore.FailBatchJob(ctx, job.ID, runnerID, err)
			return nil, err
		}
		if err := stateStore.MarkBatchJobGovernanceReported(ctx, job.ID, runnerID); err != nil {
			_ = stateStore.FailBatchJob(ctx, job.ID, runnerID, err)
			return nil, err
		}
	}
	if err := stateStore.CompleteBatchJob(ctx, job.ID, runnerID); err != nil {
		_ = stateStore.FailBatchJob(ctx, job.ID, runnerID, err)
		return nil, err
	}
	summary.Accounted = true
	return summary, nil
}

// populateSummaryFromExistingLog fills summary from the aggregate log row
// already written for it, if any, so a caller that lost the settlement claim
// can still display the batch's price. summary.LogID is deterministic
// (AccountingLogID) and set unconditionally before the claim attempt, so this
// looks up the same row settlement would have written — nothing here writes.
//
// A missing row (job never reached a write, or is still being settled by
// another runner) leaves summary at its zero value: nothing to show yet, which
// is the correct display for "not priced".
func populateSummaryFromExistingLog(ctx context.Context, logStore AggregateLogStore, summary *Summary) {
	entry, err := logStore.FindByID(ctx, summary.LogID)
	if err != nil || entry == nil {
		return
	}
	if entry.TokenUsageParsed != nil {
		summary.Usage = *entry.TokenUsageParsed
	}
	if entry.Cost != nil {
		summary.Cost = *entry.Cost
		// buildAggregateLog only ever leaves Cost nil for an incomplete total (see
		// the !summary.Complete branch in AccountBatchResults) — a non-nil Cost on
		// the persisted row means every usage-bearing result priced when it was
		// written.
		summary.Complete = true
	}
	if entry.BatchDebugParsed != nil {
		summary.Status = entry.BatchDebugParsed.Status
		if entry.BatchDebugParsed.Accounting != nil {
			summary.ModelBreakdowns = entry.BatchDebugParsed.Accounting.ModelBreakdowns
		}
	}
}

func mergeBatchJobHints(dst *cstables.TableBatchJob, src *cstables.TableBatchJob) {
	if dst == nil || src == nil {
		return
	}
	if dst.Model == "" {
		dst.Model = src.Model
	}
	if dst.Endpoint == "" {
		dst.Endpoint = src.Endpoint
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

func pricingScopesForBatchJob(scopes *modelcatalog.PricingLookupScopes, provider schemas.ModelProvider, job *cstables.TableBatchJob) *modelcatalog.PricingLookupScopes {
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

func batchUsageReportFromLog(provider schemas.ModelProvider, entry *logstore.Log) BatchUsageReport {
	report := BatchUsageReport{
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
	report.ModelUsage = modelUsageFromLog(entry)
	if entry.Cost != nil {
		report.Cost = *entry.Cost
	}
	return report
}

// modelUsageFromLog reads the settled row's per-model split.
func modelUsageFromLog(entry *logstore.Log) []BatchModelUsage {
	if entry.BatchDebugParsed == nil || entry.BatchDebugParsed.Accounting == nil {
		return nil
	}
	breakdowns := entry.BatchDebugParsed.Accounting.ModelBreakdowns
	if len(breakdowns) == 0 {
		return nil
	}
	usage := make([]BatchModelUsage, 0, len(breakdowns))
	for model, breakdown := range breakdowns {
		if model == "" {
			continue
		}
		cost := 0.0
		if breakdown.Cost != nil {
			cost = *breakdown.Cost
		}
		usage = append(usage, BatchModelUsage{Model: model, Cost: cost, TokensUsed: int64(breakdown.Usage.TotalTokens)})
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

// accountingLogNamespace is the UUIDv5 namespace for aggregate cost log ids.
//
// It must never change. The derived id is both the idempotency key for the
// aggregate write and the governance dedupe key, so a new namespace would make
// every already-settled batch look unsettled and re-report its usage.
var accountingLogNamespace = uuid.MustParse("90a2f576-5276-481f-a753-2c78ccf97607")

// AccountingLogID derives the id of the aggregate cost log for a batch.
//
// The id is deterministic by design: CreateIfNotExists keys on it so a replayed
// settlement is a no-op, and it is reused as BatchUsageReport.RequestID so the
// governance reporter can dedupe. UUIDv5 gives that determinism while keeping the
// id a plain UUID like every other log id — ids travel back to the store through
// URL path parameters, and anything needing percent-encoding does not survive
// that round trip cleanly.
func AccountingLogID(provider schemas.ModelProvider, batchID string) string {
	return uuid.NewSHA1(accountingLogNamespace, []byte(string(provider)+":"+batchID)).String()
}

func summarizeResults(pricing PricingManager, req Request) (*Summary, error) {
	if len(req.Results) == 0 {
		return &Summary{UnpriceableReason: UnpriceableReasonNoResults}, nil
	}

	breakdowns := make(map[string]ModelBreakdown)
	// Every usage-bearing row lands here, priced or not — including rows with no
	// model at all, which get no breakdown entry and would otherwise be dropped
	// entirely. The row-level usage is a record of what the batch consumed; leaving
	// the unpriced share out of it would silently shrink the batch and put the
	// missing tokens beyond the reach of any later repricing.
	totalUsage := schemas.BifrostLLMUsage{}
	unpricedModels := make(map[string]struct{})
	totalCost := 0.0
	usageSeen := false
	missingModelSeen := false
	missingPricingSeen := false
	pricedCount := 0
	unpricedCount := 0
	failedCount := 0

	for _, item := range req.Results {
		if item.Failed() {
			failedCount++
			continue
		}
		extracted, err := extractUsage(req.Provider, req.FallbackModel, item)
		if err != nil {
			return nil, err
		}
		if !extracted.hasUsage {
			unpricedCount++
			continue
		}
		usageSeen = true
		if merged := schemas.MergeBifrostLLMUsage(&totalUsage, extracted.usage); merged != nil {
			totalUsage = *merged
		}
		if extracted.missingModel {
			missingModelSeen = true
			unpricedCount++
			continue
		}

		// Every known model gets a breakdown entry regardless of pricing outcome —
		// missing-batch-pricing rows included — so a later recalculation can reprice
		// this model from its Usage once rates land, instead of the usage only
		// surviving as an anonymous blob nothing can ever re-attribute.
		breakdown := breakdowns[extracted.model]
		breakdown.Model = extracted.model
		breakdown.RequestCount++
		if merged := schemas.MergeBifrostLLMUsage(&breakdown.Usage, extracted.usage); merged != nil {
			breakdown.Usage = *merged
		}
		breakdowns[extracted.model] = breakdown

		costDetails := pricing.CalculateBatchCostDetailsForUsage(extracted.usage, req.Provider, extracted.model, BatchRequestType(req.Endpoint), req.Scopes)
		if !costDetails.Priced {
			missingPricingSeen = true
			unpricedCount++
			unpricedModels[extracted.model] = struct{}{}
			continue
		}

		// breakdown.Cost is nil until a result for this model actually prices — a
		// model that never leaves the unpriced branch above keeps Cost nil, matching
		// the row-level "nil means not priced yet, not $0" convention.
		modelCost := costDetails.Cost
		if breakdown.Cost != nil {
			modelCost += *breakdown.Cost
		}
		breakdown.Cost = &modelCost
		breakdowns[extracted.model] = breakdown

		totalCost += costDetails.Cost
		pricedCount++
	}

	// breakdowns is no longer a reliable "was anything priced" signal — it now
	// gets an entry for every known model, priced or not — so the unpriceable
	// decision keys on pricedCount instead.
	if pricedCount == 0 {
		reason := UnpriceableReasonNoUsage
		switch {
		case missingModelSeen:
			reason = UnpriceableReasonMissingModel
		case usageSeen && missingPricingSeen:
			reason = UnpriceableReasonMissingBatchPricing
		}
		// Carry the unpriced usage, counts, and per-model breakdowns out rather than
		// reporting only the reason: the caller logs this usage with an unknown cost
		// so it stays recoverable instead of being silently dropped.
		// Attribute the row-level model only when every unpriced token demonstrably
		// belongs to one model. Missing-model rows contribute usage but no model, so
		// if any are present the total is a mixture and naming the one known model
		// would let backfill price those orphan tokens as it — leave it unattributed
		// instead. This does not affect ModelBreakdowns itself: each entry there is
		// keyed by its own real model name and carries only that model's usage, so
		// per-model attribution stays unambiguous even when the row-level label isn't.
		unpricedModel := ""
		if !missingModelSeen && len(unpricedModels) == 1 {
			for model := range unpricedModels {
				unpricedModel = model
			}
		}
		return &Summary{
			Provider:          req.Provider,
			BatchID:           req.BatchID,
			Usage:             totalUsage,
			ModelBreakdowns:   breakdowns,
			UnpricedModel:     unpricedModel,
			UnpricedCount:     unpricedCount,
			FailedCount:       failedCount,
			UnpriceableReason: reason,
		}, nil
	}

	// Some usage priced, so the batch settles — but only as "complete" if nothing
	// usage-bearing was left unpriced. When it was, name the reason too: it is the
	// same diagnosis a wholly-unpriced batch would carry, and the caller parks the
	// job under it rather than closing it out on a partial total.
	summary := &Summary{
		Provider:        req.Provider,
		BatchID:         req.BatchID,
		Cost:            totalCost,
		Usage:           totalUsage,
		ModelBreakdowns: breakdowns,
		PricedCount:     pricedCount,
		UnpricedCount:   unpricedCount,
		FailedCount:     failedCount,
		Complete:        !missingModelSeen && !missingPricingSeen,
	}
	if !summary.Complete {
		if missingModelSeen {
			summary.UnpriceableReason = UnpriceableReasonMissingModel
		} else {
			summary.UnpriceableReason = UnpriceableReasonMissingBatchPricing
		}
	}
	return summary, nil
}

// BatchRequestType maps a batch endpoint to the request type its results price
// under. Exported because the aggregate row records only the endpoint (its Object
// column is always "batch_results"), so a later repricing pass outside this package
// must reach the same modality settlement used.
func BatchRequestType(endpoint schemas.BatchEndpoint) schemas.RequestType {
	switch endpoint {
	case schemas.BatchEndpointEmbeddings:
		return schemas.EmbeddingRequest
	case schemas.BatchEndpointCompletions:
		return schemas.TextCompletionRequest
	case schemas.BatchEndpointResponses:
		return schemas.ResponsesRequest
	case schemas.BatchEndpointChatCompletions, schemas.BatchEndpointMessages:
		return schemas.ChatCompletionRequest
	default:
		return schemas.BatchResultsRequest
	}
}

type usageExtractor func(fallbackModel string, item schemas.BatchResultItem) (extractedUsage, error)

var usageExtractors = map[schemas.ModelProvider]usageExtractor{
	schemas.OpenAI:    extractResponseBodyUsage,
	schemas.Azure:     extractResponseBodyUsage,
	schemas.Bedrock:   extractResponseBodyUsage,
	schemas.Gemini:    extractResponseBodyUsage,
	schemas.Vertex:    extractResponseBodyUsage,
	schemas.Anthropic: extractAnthropicUsage,
}

func IsProviderSupported(provider schemas.ModelProvider) bool {
	_, ok := usageExtractors[provider]
	return ok
}

func extractUsage(provider schemas.ModelProvider, fallbackModel string, item schemas.BatchResultItem) (extractedUsage, error) {
	extractor, ok := usageExtractors[provider]
	if !ok {
		return extractedUsage{}, nil
	}
	return extractor(fallbackModel, item)
}

func extractResponseBodyUsage(fallbackModel string, item schemas.BatchResultItem) (extractedUsage, error) {
	if item.Response == nil || item.Response.StatusCode >= 400 || item.Response.Body == nil {
		return extractedUsage{}, nil
	}
	usageValue, ok := item.Response.Body["usage"]
	if !ok || usageValue == nil {
		return extractedUsage{}, nil
	}
	usage, err := usageFromValue(usageValue)
	if err != nil {
		return extractedUsage{}, err
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if usage.TotalTokens == 0 {
		return extractedUsage{}, nil
	}
	model, _ := item.Response.Body["model"].(string)
	if model == "" {
		model = fallbackModel
	}
	if model == "" {
		return extractedUsage{usage: usage, hasUsage: true, missingModel: true}, nil
	}
	return extractedUsage{model: model, usage: usage, hasUsage: true}, nil
}

func extractAnthropicUsage(fallbackModel string, item schemas.BatchResultItem) (extractedUsage, error) {
	if item.Result == nil || item.Result.Type != "succeeded" || item.Result.Message == nil {
		return extractedUsage{}, nil
	}
	usageValue, ok := item.Result.Message["usage"]
	if !ok || usageValue == nil {
		return extractedUsage{}, nil
	}
	usage, err := anthropicUsageFromValue(usageValue)
	if err != nil {
		return extractedUsage{}, err
	}
	if usage.TotalTokens == 0 {
		return extractedUsage{}, nil
	}
	model, _ := item.Result.Message["model"].(string)
	if model == "" {
		model = fallbackModel
	}
	if model == "" {
		return extractedUsage{usage: usage, hasUsage: true, missingModel: true}, nil
	}
	return extractedUsage{model: model, usage: usage, hasUsage: true}, nil
}

func anthropicUsageFromValue(value interface{}) (*schemas.BifrostLLMUsage, error) {
	bytes, err := sonic.Marshal(value)
	if err != nil {
		return nil, err
	}
	var usage struct {
		InputTokens              int `json:"input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreation            struct {
			Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
			Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation"`
		OutputTokens int `json:"output_tokens"`
	}
	if err := sonic.Unmarshal(bytes, &usage); err != nil {
		return nil, err
	}
	promptTokens := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	totalTokens := promptTokens + usage.OutputTokens
	if totalTokens == 0 {
		return &schemas.BifrostLLMUsage{}, nil
	}
	out := &schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      totalTokens,
	}
	if usage.CacheCreationInputTokens > 0 || usage.CacheReadInputTokens > 0 {
		out.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  usage.CacheReadInputTokens,
			CachedWriteTokens: usage.CacheCreationInputTokens,
		}
		if usage.CacheCreation.Ephemeral5mInputTokens > 0 || usage.CacheCreation.Ephemeral1hInputTokens > 0 {
			out.PromptTokensDetails.CachedWriteTokenDetails = &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens5m: usage.CacheCreation.Ephemeral5mInputTokens,
				CachedWriteTokens1h: usage.CacheCreation.Ephemeral1hInputTokens,
			}
		}
	}
	return out, nil
}

func usageFromValue(value interface{}) (*schemas.BifrostLLMUsage, error) {
	bytes, err := sonic.Marshal(value)
	if err != nil {
		return nil, err
	}
	var usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`

		InputTokensSnake  int `json:"input_tokens"`
		OutputTokensSnake int `json:"output_tokens"`

		InputTokensCamel  int `json:"inputTokens"`
		OutputTokensCamel int `json:"outputTokens"`
		TotalTokensCamel  int `json:"totalTokens"`

		CacheCreationInputTokens  int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens      int `json:"cache_read_input_tokens"`
		CacheReadInputTokensCamel int `json:"cacheReadInputTokens"`
		CacheWriteInputTokens     int `json:"cacheWriteInputTokens"`
		CacheCreation             struct {
			Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
			Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation"`
		CacheDetails []struct {
			InputTokens int    `json:"inputTokens"`
			TTL         string `json:"ttl"`
		} `json:"cacheDetails"`
		// OpenAI (and Gemini, which we normalize into the same shape) report
		// cached input as a breakdown of prompt_tokens rather than as a separate
		// bucket — see the inclusive/exclusive note below.
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		Cost *schemas.BifrostCost `json:"cost,omitempty"`
	}
	if err := sonic.Unmarshal(bytes, &usage); err != nil {
		return nil, err
	}
	cacheWriteTokens := usage.CacheCreationInputTokens + usage.CacheWriteInputTokens
	cacheWrite5m := usage.CacheCreation.Ephemeral5mInputTokens
	cacheWrite1h := usage.CacheCreation.Ephemeral1hInputTokens
	cacheDetailsWriteTokens := 0
	for _, detail := range usage.CacheDetails {
		cacheDetailsWriteTokens += detail.InputTokens
		switch detail.TTL {
		case "5m":
			cacheWrite5m += detail.InputTokens
		case "1h":
			cacheWrite1h += detail.InputTokens
		}
	}
	if cacheWriteTokens == 0 {
		cacheWriteTokens = cacheDetailsWriteTokens
	}
	// Two wire conventions, keyed by field name, and the distinction is load-bearing:
	//   - Anthropic/Bedrock (cache_read_input_tokens, cacheReadInputTokens, ...) report
	//     cache tokens EXCLUSIVE of the base input count, so they are added in below to
	//     normalize into our internal convention (PromptTokens is inclusive of cache).
	//   - OpenAI/Gemini (prompt_tokens_details.cached_tokens) report cached input as a
	//     BREAKDOWN of prompt_tokens, which already includes it — adding it would
	//     double-count. It is only surfaced on PromptTokensDetails so the cache-read
	//     discount applies at pricing time.
	// A provider emits one convention or the other, never both.
	cacheReadTokens := usage.CacheReadInputTokens + usage.CacheReadInputTokensCamel
	inclusiveCacheReadTokens := usage.PromptTokensDetails.CachedTokens

	promptTokens := firstNonZero(usage.PromptTokens, usage.InputTokensSnake, usage.InputTokensCamel) + cacheReadTokens + cacheWriteTokens
	completionTokens := firstNonZero(usage.CompletionTokens, usage.OutputTokensSnake, usage.OutputTokensCamel)
	computedTotal := promptTokens + completionTokens
	totalTokens := firstNonZero(usage.TotalTokens, usage.TotalTokensCamel)
	if totalTokens == 0 || totalTokens < computedTotal {
		totalTokens = computedTotal
	}
	out := &schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
		Cost:             usage.Cost,
	}
	totalCacheReadTokens := cacheReadTokens + inclusiveCacheReadTokens
	if totalCacheReadTokens > 0 || cacheWriteTokens > 0 {
		out.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  totalCacheReadTokens,
			CachedWriteTokens: cacheWriteTokens,
		}
		if cacheWrite5m > 0 || cacheWrite1h > 0 {
			out.PromptTokensDetails.CachedWriteTokenDetails = &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens5m: cacheWrite5m,
				CachedWriteTokens1h: cacheWrite1h,
			}
		}
	}
	return out, nil
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func buildAggregateLog(req Request, summary *Summary, now time.Time, userAgent string) *logstore.Log {
	model := req.FallbackModel
	if len(summary.ModelBreakdowns) == 1 {
		for key := range summary.ModelBreakdowns {
			model = key
		}
	} else if len(summary.ModelBreakdowns) > 1 {
		model = "mixed"
	}
	if model == "" {
		model = "mixed"
	}

	// The provider is already a column on the row, and applyLogAttribution sets
	// ParentRequestID for the source request, so neither is repeated here.
	batchDebug := &schemas.BifrostBatchDebug{
		BatchID:  req.BatchID,
		Status:   summary.Status,
		Endpoint: string(req.Endpoint),
		Accounting: &schemas.BatchAccountingDebug{
			ModelBreakdowns: summary.ModelBreakdowns,
			ParseErrorCount: len(req.ParseErrors),
			// Two different ways to under-state, one marker: usage that failed to price,
			// and rows that never parsed. Parse errors are unrecoverable (the raw results
			// are not kept), so unlike unpriced usage they do not hold the settlement back
			// — the batch still settles, it just settles on record as short.
			Incomplete: !summary.Complete || len(req.ParseErrors) > 0,
		},
	}
	if requestCounts := requestCountsForAggregateLog(req); !requestCounts.IsZero() {
		batchDebug.RequestCounts = &requestCounts
	}

	entry := &logstore.Log{
		ID:               summary.LogID,
		Timestamp:        now,
		Object:           string(schemas.BatchResultsRequest),
		Provider:         string(req.Provider),
		Model:            model,
		Status:           "success",
		Cost:             &summary.Cost,
		TokenUsageParsed: &summary.Usage,
		PromptTokens:     summary.Usage.PromptTokens,
		CompletionTokens: summary.Usage.CompletionTokens,
		TotalTokens:      summary.Usage.TotalTokens,
		CreatedAt:        now,
		BatchDebugParsed: batchDebug,
		UserAgent:        &userAgent,
	}
	// Attribution must not depend on who happens to settle the batch first. The
	// sweeper reaches here with only the job; a /results call reaches here with the
	// caller's log entry too — and copying that wholesale meant the same batch billed
	// the creator's budgets or the fetcher's depending on a race, so an admin reading
	// someone else's results absorbed the entire bill. The batch job's create-time
	// attribution is the one identity that does not move, so it wins whenever it
	// exists.
	//
	// The display names come from SourceLog — the creating request's own log row —
	// rather than from whoever is settling. Matching the two on virtual key was not
	// enough to keep identities apart: an access profile hands one virtual key to
	// many users, so two different people compared equal and the settling user's name
	// landed on the creator's cost row. A batch first seen at /results has no
	// create-time attribution to prefer, so it keeps using BaseLog.
	if req.BatchJob != nil && hasBatchJobAttribution(req.BatchJob) {
		if req.SourceLog != nil {
			applyLogDenormalizations(entry, req.SourceLog)
		}
		applyBatchJobAttribution(entry, req.BatchJob)
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
	if req.BatchJob != nil {
		applyBatchJobAttribution(entry, req.BatchJob)
	}
	return entry
}

// resolveSourceLog loads the log row of the request that created the batch, for the
// display names batch_jobs does not carry. Best-effort by design: the names are
// cosmetic, the ids on the job are what bills, so a log row that has been rotated
// away or cannot be read costs nothing but the labels.
func resolveSourceLog(ctx context.Context, logStore AggregateLogStore, job *cstables.TableBatchJob, baseLog *logstore.Log) *logstore.Log {
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

// hasBatchJobAttribution reports whether the job captured who to bill at create
// time. Batches created outside Bifrost (first seen at /results) carry none.
func hasBatchJobAttribution(job *cstables.TableBatchJob) bool {
	if job.SelectedKeyID != "" || (job.VirtualKeyID != nil && *job.VirtualKeyID != "") {
		return true
	}
	return (job.BudgetIDs != nil && *job.BudgetIDs != "") || (job.RateLimitIDs != nil && *job.RateLimitIDs != "")
}

func requestCountsForAggregateLog(req Request) schemas.BatchRequestCounts {
	if req.RequestCounts != nil && !req.RequestCounts.IsZero() {
		return *req.RequestCounts
	}
	return schemas.BatchRequestCountsFromResults(req.Results)
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
// carries but batch_jobs does not persist. Deliberately excludes the ids the
// batch job owns (selected key, virtual key, budgets, rate limits): those decide
// who is billed, and mixing them with another request's would reintroduce exactly
// the attribution race this split exists to remove.
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

func applyBatchJobAttribution(entry *logstore.Log, job *cstables.TableBatchJob) {
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
