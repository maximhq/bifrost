package batchaccounting

import (
	"context"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
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
	ClaimBatchJob(ctx context.Context, jobID, runnerID string, staleBefore time.Time) (bool, error)
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
	claimed, err := stateStore.ClaimBatchJob(ctx, job.ID, runnerID, now.Add(-defaultClaimTTL))
	if err != nil {
		return nil, err
	}
	summary.Claimed = claimed
	if !claimed {
		return summary, nil
	}
	if persisted, err := stateStore.GetBatchJob(ctx, job.ID); err == nil && persisted != nil {
		mergeBatchJobHints(job, persisted)
	}
	req.BatchJob = job
	if req.FallbackModel == "" {
		req.FallbackModel = job.Model
	}
	if req.Endpoint == "" {
		req.Endpoint = schemas.BatchEndpoint(job.Endpoint)
	}
	req.Scopes = pricingScopesForBatchJob(req.Scopes, req.Provider, job)
	if len(req.ParseErrors) > 0 {
		summary.UnpriceableReason = UnpriceableReasonResultParseErrors
		parseErr := fmt.Errorf("batch result contained %d malformed row(s)", len(req.ParseErrors))
		if err := stateStore.MarkBatchJobUnpriceable(ctx, job.ID, runnerID, UnpriceableReasonResultParseErrors, parseErr); err != nil {
			return nil, err
		}
		return summary, nil
	}

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
		summary.UnpriceableReason = reason
		if err := stateStore.MarkBatchJobUnpriceable(ctx, job.ID, runnerID, reason, nil); err != nil {
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

		costDetails := pricing.CalculateBatchCostDetailsForUsage(extracted.usage, req.Provider, extracted.model, batchRequestType(req.Endpoint), req.Scopes)
		if !costDetails.Priced {
			missingPricingSeen = true
			unpricedCount++
			unpricedReasons[UnpriceableReasonMissingBatchPricing]++
			continue
		}

		breakdown := breakdowns[extracted.model]
		breakdown.Model = extracted.model
		breakdown.RequestCount++
		if merged := schemas.MergeBifrostLLMUsage(&breakdown.Usage, extracted.usage); merged != nil {
			breakdown.Usage = *merged
		}
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

		if merged := schemas.MergeBifrostLLMUsage(&totalUsage, extracted.usage); merged != nil {
			totalUsage = *merged
		}
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

func batchRequestType(endpoint schemas.BatchEndpoint) schemas.RequestType {
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
	cacheReadTokens := usage.CacheReadInputTokens + usage.CacheReadInputTokensCamel

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
	if cacheReadTokens > 0 || cacheWriteTokens > 0 {
		out.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  cacheReadTokens,
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
	if !requestCounts.IsZero() {
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
	if req.RequestCounts != nil && !req.RequestCounts.IsZero() {
		return *req.RequestCounts
	}
	return schemas.BatchRequestCountsFromResults(req.Results)
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

func applyBatchJobAttribution(entry *logstore.Log, job *cstables.TableBatchJob) {
	entry.SelectedKeyID = job.SelectedKeyID
	entry.VirtualKeyID = job.VirtualKeyID
	entry.BudgetIDs = job.BudgetIDs
	entry.RateLimitIDs = job.RateLimitIDs
}
