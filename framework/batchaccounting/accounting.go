package batchaccounting

import (
	"context"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
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
)

type Store interface {
	CreateIfNotExists(ctx context.Context, entry *logstore.Log) error
	UpsertBatchJob(ctx context.Context, job *logstore.BatchJob) error
	FindBatchJobByID(ctx context.Context, jobID string) (*logstore.BatchJob, error)
	ClaimBatchJobAccounting(ctx context.Context, jobID string, claimedBy string, ttl time.Duration) (string, bool, error)
	MarkBatchJobAggregateLogWritten(ctx context.Context, jobID string, claimToken string) error
	MarkBatchJobGovernanceReported(ctx context.Context, jobID string, claimToken string) error
	CompleteBatchJobAccounting(ctx context.Context, jobID string, claimToken string) error
	MarkBatchJobUnpriceable(ctx context.Context, jobID string, claimToken string, reason string, err error) error
	FailBatchJobAccounting(ctx context.Context, jobID string, claimToken string, err error) error
}

type AggregateLogEmitter interface {
	EmitBatchAggregateLog(ctx context.Context, entry *logstore.Log)
}

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
}

type PricingManager interface {
	CalculateBatchCostForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *modelcatalog.PricingLookupScopes) (float64, bool)
}

type batchCostDetailProvider interface {
	CalculateBatchCostDetailsForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *modelcatalog.PricingLookupScopes) modelcatalog.BatchCostDetails
}

type Request struct {
	Provider      schemas.ModelProvider
	BatchID       string
	FallbackModel string
	Results       []schemas.BatchResultItem
	RequestCounts *schemas.BatchRequestCounts
	BatchJob      *logstore.BatchJob
	BaseLog       *logstore.Log
	Emitter       AggregateLogEmitter
	UsageReporter UsageReporter
	ClaimedBy     string
	Scopes        *modelcatalog.PricingLookupScopes
	Now           time.Time
}

type Summary struct {
	JobID             string                    `json:"job_id"`
	LogID             string                    `json:"log_id"`
	Provider          schemas.ModelProvider     `json:"provider"`
	BatchID           string                    `json:"batch_id"`
	Cost              float64                   `json:"cost"`
	Usage             schemas.BifrostLLMUsage   `json:"usage"`
	ModelBreakdowns   map[string]ModelBreakdown `json:"model_breakdowns"`
	PricedCount       int                       `json:"priced_count"`
	UnpricedCount     int                       `json:"unpriced_count"`
	FailedCount       int                       `json:"failed_count"`
	UnpricedReasons   map[string]int            `json:"unpriced_reasons,omitempty"`
	Accounted         bool                      `json:"accounted"`
	Claimed           bool                      `json:"claimed"`
	UnpriceableReason string                    `json:"unpriceable_reason,omitempty"`
}

type ModelBreakdown struct {
	Model                     string                  `json:"model"`
	RequestCount              int                     `json:"request_count"`
	Usage                     schemas.BifrostLLMUsage `json:"usage"`
	Cost                      float64                 `json:"cost"`
	InputCostPerTokenBatches  *float64                `json:"input_cost_per_token_batches,omitempty"`
	OutputCostPerTokenBatches *float64                `json:"output_cost_per_token_batches,omitempty"`
	ProviderCost              float64                 `json:"provider_cost,omitempty"`
	ProviderCostUsed          bool                    `json:"provider_cost_used,omitempty"`
}

type extractedUsage struct {
	model        string
	usage        *schemas.BifrostLLMUsage
	hasUsage     bool
	missingModel bool
}

func AccountBatchResults(ctx context.Context, store Store, pricing PricingManager, req Request) (*Summary, error) {
	if store == nil {
		return nil, fmt.Errorf("batch accounting store is nil")
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
		job = &logstore.BatchJob{
			Provider:         string(req.Provider),
			BatchID:          req.BatchID,
			Model:            req.FallbackModel,
			AccountingStatus: logstore.BatchJobAccountingStatusPending,
		}
	}
	if job.ID == "" {
		job.ID = logstore.BatchJobID(string(req.Provider), req.BatchID)
	}
	if err := store.UpsertBatchJob(ctx, job); err != nil {
		return nil, err
	}

	summary := &Summary{
		JobID:    job.ID,
		LogID:    AccountingLogID(req.Provider, req.BatchID),
		Provider: req.Provider,
		BatchID:  req.BatchID,
	}

	claimToken, claimed, err := store.ClaimBatchJobAccounting(ctx, job.ID, req.ClaimedBy, defaultClaimTTL)
	if err != nil {
		return nil, err
	}
	summary.Claimed = claimed
	if !claimed {
		return summary, nil
	}
	if persisted, err := store.FindBatchJobByID(ctx, job.ID); err == nil && persisted != nil {
		mergeBatchJobHints(job, persisted)
	}
	req.BatchJob = job
	if req.FallbackModel == "" {
		req.FallbackModel = job.Model
	}
	req.Scopes = pricingScopesForBatchJob(req.Scopes, req.Provider, job)

	computed, err := summarizeResults(pricing, req)
	if err != nil {
		_ = store.FailBatchJobAccounting(ctx, job.ID, claimToken, err)
		return nil, err
	}
	if computed == nil || computed.PricedCount == 0 {
		reason := UnpriceableReasonNoUsage
		if computed != nil && computed.UnpriceableReason != "" {
			reason = computed.UnpriceableReason
		}
		summary.UnpriceableReason = reason
		if err := store.MarkBatchJobUnpriceable(ctx, job.ID, claimToken, reason, nil); err != nil {
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
	summary.UnpricedReasons = computed.UnpricedReasons

	entry := buildAggregateLog(req, summary, now)
	if job.AggregateLogWrittenAt == nil {
		if err := store.CreateIfNotExists(ctx, entry); err != nil {
			_ = store.FailBatchJobAccounting(ctx, job.ID, claimToken, err)
			return nil, err
		}
		if err := store.MarkBatchJobAggregateLogWritten(ctx, job.ID, claimToken); err != nil {
			_ = store.FailBatchJobAccounting(ctx, job.ID, claimToken, err)
			return nil, err
		}
		if req.Emitter != nil {
			req.Emitter.EmitBatchAggregateLog(ctx, entry)
		}
	}
	if req.UsageReporter != nil && job.GovernanceReportedAt == nil {
		if err := req.UsageReporter.ReportBatchUsage(ctx, batchUsageReportFromLog(req.Provider, entry)); err != nil {
			_ = store.FailBatchJobAccounting(ctx, job.ID, claimToken, err)
			return nil, err
		}
		if err := store.MarkBatchJobGovernanceReported(ctx, job.ID, claimToken); err != nil {
			_ = store.FailBatchJobAccounting(ctx, job.ID, claimToken, err)
			return nil, err
		}
	}
	if err := store.CompleteBatchJobAccounting(ctx, job.ID, claimToken); err != nil {
		_ = store.FailBatchJobAccounting(ctx, job.ID, claimToken, err)
		return nil, err
	}
	summary.Accounted = true
	return summary, nil
}

func mergeBatchJobHints(dst *logstore.BatchJob, src *logstore.BatchJob) {
	if dst == nil || src == nil {
		return
	}
	if dst.Model == "" {
		dst.Model = src.Model
	}
	dst.AggregateLogWrittenAt = src.AggregateLogWrittenAt
	dst.GovernanceReportedAt = src.GovernanceReportedAt
	if dst.SelectedKeyID == "" {
		dst.SelectedKeyID = src.SelectedKeyID
	}
	if dst.VirtualKeyID == nil {
		dst.VirtualKeyID = src.VirtualKeyID
	}
	if dst.BudgetIDs == nil {
		dst.BudgetIDs = src.BudgetIDs
	}
	if dst.RateLimitIDs == nil {
		dst.RateLimitIDs = src.RateLimitIDs
	}
}

func pricingScopesForBatchJob(scopes *modelcatalog.PricingLookupScopes, provider schemas.ModelProvider, job *logstore.BatchJob) *modelcatalog.PricingLookupScopes {
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
		RateLimitIDs: entry.RateLimitIDsParsed,
	}
	report.RateLimitIDs = stringSliceFromParsedOrJSON(entry.RateLimitIDsParsed, entry.RateLimitIDs)
	if entry.Cost != nil {
		report.Cost = *entry.Cost
	}
	return report
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

func AccountingLogID(provider schemas.ModelProvider, batchID string) string {
	return fmt.Sprintf("batch-cost:%s:%s", provider, batchID)
}

func summarizeResults(pricing PricingManager, req Request) (*Summary, error) {
	if len(req.Results) == 0 {
		return &Summary{UnpriceableReason: UnpriceableReasonNoResults}, nil
	}

	breakdowns := make(map[string]ModelBreakdown)
	unpricedReasons := make(map[string]int)
	totalUsage := schemas.BifrostLLMUsage{}
	totalCost := 0.0
	usageSeen := false
	missingModelSeen := false
	missingPricingSeen := false
	pricedCount := 0
	unpricedCount := 0
	failedCount := 0

	for _, item := range req.Results {
		if item.Error != nil {
			failedCount++
			continue
		}
		extracted, err := extractUsage(req.Provider, req.FallbackModel, item)
		if err != nil {
			return nil, err
		}
		if !extracted.hasUsage {
			unpricedCount++
			unpricedReasons[UnpriceableReasonNoUsage]++
			continue
		}
		usageSeen = true
		if extracted.missingModel {
			missingModelSeen = true
			unpricedCount++
			unpricedReasons[UnpriceableReasonMissingModel]++
			continue
		}

		costDetails := calculateBatchCostDetails(pricing, extracted.usage, req.Provider, extracted.model, schemas.BatchResultsRequest, req.Scopes)
		if !costDetails.Priced {
			missingPricingSeen = true
			unpricedCount++
			unpricedReasons[UnpriceableReasonMissingBatchPricing]++
			continue
		}

		breakdown := breakdowns[extracted.model]
		breakdown.Model = extracted.model
		breakdown.RequestCount++
		addUsage(&breakdown.Usage, extracted.usage)
		breakdown.Cost += costDetails.Cost
		if breakdown.InputCostPerTokenBatches == nil && costDetails.InputCostPerTokenBatches != nil {
			breakdown.InputCostPerTokenBatches = costDetails.InputCostPerTokenBatches
		}
		if breakdown.OutputCostPerTokenBatches == nil && costDetails.OutputCostPerTokenBatches != nil {
			breakdown.OutputCostPerTokenBatches = costDetails.OutputCostPerTokenBatches
		}
		if costDetails.ProviderCostUsed {
			breakdown.ProviderCost += costDetails.Cost
			breakdown.ProviderCostUsed = true
		}
		breakdowns[extracted.model] = breakdown

		addUsage(&totalUsage, extracted.usage)
		totalCost += costDetails.Cost
		pricedCount++
	}

	if len(breakdowns) == 0 {
		reason := UnpriceableReasonNoUsage
		switch {
		case missingModelSeen:
			reason = UnpriceableReasonMissingModel
		case usageSeen && missingPricingSeen:
			reason = UnpriceableReasonMissingBatchPricing
		}
		return &Summary{UnpriceableReason: reason}, nil
	}

	return &Summary{
		Provider:        req.Provider,
		BatchID:         req.BatchID,
		Cost:            totalCost,
		Usage:           totalUsage,
		ModelBreakdowns: breakdowns,
		PricedCount:     pricedCount,
		UnpricedCount:   unpricedCount,
		FailedCount:     failedCount,
		UnpricedReasons: unpricedReasons,
	}, nil
}

func calculateBatchCostDetails(pricing PricingManager, usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *modelcatalog.PricingLookupScopes) modelcatalog.BatchCostDetails {
	if detailProvider, ok := pricing.(batchCostDetailProvider); ok {
		return detailProvider.CalculateBatchCostDetailsForUsage(usage, provider, model, requestType, scopes)
	}
	cost, priced := pricing.CalculateBatchCostForUsage(usage, provider, model, requestType, scopes)
	return modelcatalog.BatchCostDetails{
		Cost:             cost,
		Priced:           priced,
		ProviderCostUsed: usage != nil && usage.Cost != nil,
	}
}

func extractUsage(provider schemas.ModelProvider, fallbackModel string, item schemas.BatchResultItem) (extractedUsage, error) {
	switch provider {
	case schemas.OpenAI:
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
	default:
		return "", false
	}
}

type usageExtractor func(fallbackModel string, item schemas.BatchResultItem) (extractedUsage, error)

var usageExtractors = map[schemas.ModelProvider]usageExtractor{
	schemas.OpenAI:    extractResponseBodyUsage,
	schemas.Bedrock:   extractResponseBodyUsage,
	schemas.Gemini:    extractResponseBodyUsage,
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

func usageFromValue(value interface{}) (*schemas.BifrostLLMUsage, error) {
	bytes, err := sonic.Marshal(value)
	if err != nil {
		return nil, err
	}
	var usage schemas.BifrostLLMUsage
	if err := sonic.Unmarshal(bytes, &usage); err != nil {
		return nil, err
	}
	return &usage, nil
}

func addUsage(dst *schemas.BifrostLLMUsage, src *schemas.BifrostLLMUsage) {
	if src == nil {
		return
	}
	dst.PromptTokens += src.PromptTokens
	dst.CompletionTokens += src.CompletionTokens
	dst.TotalTokens += src.TotalTokens
}

func buildAggregateLog(req Request, summary *Summary, now time.Time) *logstore.Log {
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

	metadata := map[string]interface{}{
		"batch_accounting": true,
		"batch_id":         req.BatchID,
		"provider":         string(req.Provider),
		"model_breakdown":  summary.ModelBreakdowns,
		"priced_count":     summary.PricedCount,
		"unpriced_count":   summary.UnpricedCount,
		"failed_count":     summary.FailedCount,
		"unpriced_reasons": summary.UnpricedReasons,
	}
	requestCounts := requestCountsForAggregateLog(req)
	if !IsZeroBatchRequestCounts(requestCounts) {
		metadata["request_counts"] = requestCounts
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
		MetadataParsed:   metadata,
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

func requestCountsForAggregateLog(req Request) schemas.BatchRequestCounts {
	if req.RequestCounts != nil && !IsZeroBatchRequestCounts(*req.RequestCounts) {
		return *req.RequestCounts
	}
	return requestCountsFromResults(req.Results)
}

func requestCountsFromResults(results []schemas.BatchResultItem) schemas.BatchRequestCounts {
	counts := schemas.BatchRequestCounts{Total: len(results)}
	for _, item := range results {
		if batchResultItemFailed(item) {
			counts.Failed++
			continue
		}
		counts.Completed++
	}
	return counts
}

func batchResultItemFailed(item schemas.BatchResultItem) bool {
	if item.Error != nil {
		return true
	}
	if item.Response != nil && item.Response.StatusCode >= 400 {
		return true
	}
	if item.Result != nil && item.Result.Type != "" && item.Result.Type != "succeeded" {
		return true
	}
	return false
}

func IsZeroBatchRequestCounts(counts schemas.BatchRequestCounts) bool {
	return counts.Total == 0 &&
		counts.Completed == 0 &&
		counts.Failed == 0 &&
		counts.Succeeded == 0 &&
		counts.Expired == 0 &&
		counts.Canceled == 0 &&
		counts.Pending == 0
}

func applyLogAttribution(entry *logstore.Log, source *logstore.Log) {
	if source.ID != "" {
		entry.ParentRequestID = &source.ID
		entry.MetadataParsed["source_request_id"] = source.ID
	}
	entry.SelectedKeyID = source.SelectedKeyID
	entry.SelectedKeyName = source.SelectedKeyName
	entry.VirtualKeyID = source.VirtualKeyID
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
	entry.BudgetIDsParsed = source.BudgetIDsParsed
	entry.RateLimitIDsParsed = source.RateLimitIDsParsed
	entry.ClusterNodeID = source.ClusterNodeID
	entry.Alias = source.Alias
	entry.CanonicalModelName = source.CanonicalModelName
	entry.AliasModelFamily = source.AliasModelFamily
}

func applyBatchJobAttribution(entry *logstore.Log, job *logstore.BatchJob) {
	entry.SelectedKeyID = job.SelectedKeyID
	entry.VirtualKeyID = job.VirtualKeyID
	entry.BudgetIDs = job.BudgetIDs
	entry.RateLimitIDs = job.RateLimitIDs
}
