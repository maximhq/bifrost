package jobaccounting

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
	UnpriceableReasonNoResults           = "no_results"
	UnpriceableReasonNoUsage             = "no_usage"
	UnpriceableReasonMissingModel        = "missing_model"
	UnpriceableReasonMissingBatchPricing = "missing_batch_pricing"
	UnpriceableReasonResultParseErrors   = "result_parse_errors"
)

// defaultProviderPollTimeout bounds a single provider call. The sweep is serial,
// so without it one hung provider stalls every remaining due job indefinitely —
// the sweeper's context is long-lived and has no deadline of its own. Sized to the
// KV poll lease: past that window another worker may take the job anyway, so there
// is nothing to gain by waiting longer. Generous enough for a large results
// download.
const defaultProviderPollTimeout = 5 * time.Minute

const unpriceableReasonTerminalWithoutResults = "terminal_without_results"

type BatchResultFetcher interface {
	RetrieveBatch(ctx context.Context, job *cstables.TableProviderJob) (*schemas.BifrostBatchRetrieveResponse, error)
	FetchBatchResults(ctx context.Context, job *cstables.TableProviderJob) (*schemas.BifrostBatchResultsResponse, error)
}

// BatchPayload is the batch kind's settlement input, carried opaquely by the
// engine on JobRequest.Payload from Poll (or an inline caller) into Settle.
type BatchPayload struct {
	Endpoint      schemas.BatchEndpoint
	Results       []schemas.BatchResultItem
	ParseErrors   []schemas.BatchError
	RequestCounts *schemas.BatchRequestCounts
}

// ModelBreakdown is the per-model slice of a settled batch's usage and cost.
//
// It is an alias rather than a local type because the breakdown is persisted on
// the aggregate log row (schemas.BifrostBatchDebug), and logstore cannot import
// this package — jobaccounting already imports logstore.
type ModelBreakdown = schemas.BatchModelBreakdown

type extractedUsage struct {
	model        string
	usage        *schemas.BifrostLLMUsage
	hasUsage     bool
	missingModel bool
}

// batchInput is the batch settler's view of one settlement attempt: the payload
// plus the few JobRequest fields pricing actually reads.
type batchInput struct {
	Provider      schemas.ModelProvider
	BatchID       string
	FallbackModel string
	Scopes        *modelcatalog.PricingLookupScopes
	BatchPayload
}

func batchInputFrom(req JobRequest) batchInput {
	load, _ := req.Payload.(BatchPayload)
	in := batchInput{
		Provider:      req.Provider,
		BatchID:       req.ProviderJobID,
		FallbackModel: req.FallbackModel,
		Scopes:        req.Scopes,
		BatchPayload:  load,
	}
	if in.Endpoint == "" && req.Job != nil {
		in.Endpoint = schemas.BatchEndpoint(req.Job.Endpoint)
	}
	return in
}

// AccountBatchResults settles a completed batch from results the caller already
// holds. It builds the batch's coordination row and hands the rows to the batch
// settler; the claim/write/report state machine itself is AccountJob.
//
// req.Payload must be a BatchPayload.
func AccountBatchResults(ctx context.Context, stateStore JobStore, logStore AggregateLogStore, pricing PricingManager, req JobRequest) (*Outcome, error) {
	if stateStore == nil {
		return nil, fmt.Errorf("batch accounting state store is nil")
	}
	if logStore == nil {
		return nil, fmt.Errorf("batch accounting log store is nil")
	}
	if pricing == nil {
		return nil, fmt.Errorf("batch accounting pricing manager is nil")
	}
	if req.Provider == "" || req.ProviderJobID == "" {
		return nil, nil
	}

	load, _ := req.Payload.(BatchPayload)

	job := req.Job
	if job == nil {
		job = &cstables.TableProviderJob{
			Kind:             cstables.ProviderJobKindBatch,
			Provider:         string(req.Provider),
			JobID:            req.ProviderJobID,
			Model:            req.FallbackModel,
			Endpoint:         string(load.Endpoint),
			AccountingStatus: cstables.ProviderJobAccountingStatusPending,
		}
	}
	if job.Endpoint == "" && load.Endpoint != "" {
		job.Endpoint = string(load.Endpoint)
	}
	if job.ID == "" {
		job.ID = cstables.ProviderJobID(cstables.ProviderJobKindBatch, string(req.Provider), req.ProviderJobID)
	}
	req.Job = job

	// Holding results is what earns the right to re-drive an unpriceable job: the
	// state was reached by giving up on polling (max attempts, a terminal status
	// with nothing to fetch, unpriceable rows), and none of those reasons survive
	// a caller arriving with the actual rows. Without results there is nothing new
	// to say, so the terminal guard stands.
	req.ForceClaim = len(load.Results) > 0

	return AccountJob(ctx, stateStore, logStore, pricing, NewBatchSettler(nil), req)
}

// NewBatchSweeper binds the generic sweeper to the batch settler.
func NewBatchSweeper(store SweepStore, logStore AggregateLogStore, pricing PricingManager, fetcher BatchResultFetcher, emitter AggregateLogEmitter, usageReporter UsageReporter, config SweeperConfig) *Sweeper {
	if config.ClaimedBy == "" {
		// Keep the batch-specific runner-id prefix: it lands in claim rows and logs,
		// and operators grep for it.
		config.ClaimedBy = "batch-sweeper:" + newInstanceID()
	}
	return NewSweeper(store, logStore, pricing, NewBatchSettler(fetcher), emitter, usageReporter, config)
}

// BatchSettler prices provider batch jobs. It is the Settler for
// ProviderJobKindBatch; everything batch-shaped about settlement lives here, and
// everything about claiming, writing and reporting lives in the engine.
//
// fetcher may be nil on the inline settlement path, where the caller already holds
// the results and never polls.
type BatchSettler struct {
	fetcher BatchResultFetcher
}

func NewBatchSettler(fetcher BatchResultFetcher) *BatchSettler {
	return &BatchSettler{fetcher: fetcher}
}

func (s *BatchSettler) Kind() ProviderJobKind { return ProviderJobKindBatch }

func (s *BatchSettler) SupportsProvider(provider schemas.ModelProvider) bool {
	return IsProviderSupported(provider)
}

// Backoff stretches the poll interval as attempts accumulate. Batches run for
// hours, so a job that has already been pending for half an hour is not worth
// checking every minute.
func (s *BatchSettler) Backoff(attempts int, interval time.Duration) time.Duration {
	delay := interval
	switch {
	case attempts >= 30:
		delay = maxDuration(delay, 15*time.Minute)
	case attempts >= 10:
		delay = maxDuration(delay, 5*time.Minute)
	case attempts >= 5:
		delay = maxDuration(delay, 2*time.Minute)
	}
	return delay
}

func (s *BatchSettler) Poll(ctx context.Context, job *cstables.TableProviderJob) (*PollResult, error) {
	if s.fetcher == nil {
		return nil, fmt.Errorf("batch result fetcher is nil")
	}
	retrieved, err := s.retrieveBatch(ctx, job)
	if err != nil {
		return nil, err
	}
	if retrieved == nil {
		return nil, fmt.Errorf("batch retrieve returned nil response for job %s", job.ID)
	}
	if retrieved.ID != "" && retrieved.ID != job.JobID {
		return nil, fmt.Errorf("batch retrieve returned mismatched batch id for job %s", job.ID)
	}

	latest := batchJobFromRetrieve(job, retrieved)

	if retrieved.Status != schemas.BatchStatusCompleted && retrieved.Status != schemas.BatchStatusEnded {
		if !isTerminalStatus(retrieved.Status) {
			return &PollResult{Job: latest}, nil
		}
		// An expired or cancelled batch is not an empty one: the provider bills the
		// requests that did finish before the window closed, and their rows are in the
		// output file. Treating the status alone as "no results" silently threw that
		// usage away. Try the fetch, settle whatever came back, and only fall through
		// to the give-up path when there is genuinely nothing to price.
		if terminalMayHaveResults(retrieved.Status, latest) {
			if results, ok := s.fetchResultsForSettlement(ctx, latest); ok && len(results.Results) > 0 {
				return settleablePoll(latest, retrieved, results), nil
			}
		}
		return &PollResult{
			Job:               latest,
			Terminal:          true,
			UnpriceableReason: unpriceableReasonTerminalWithoutResults,
		}, nil
	}

	results, ok := s.fetchResultsForSettlement(ctx, latest)
	if !ok {
		return &PollResult{Job: latest, Retry: true}, nil
	}
	return settleablePoll(latest, retrieved, results), nil
}

func settleablePoll(job *cstables.TableProviderJob, retrieved *schemas.BifrostBatchRetrieveResponse, results *schemas.BifrostBatchResultsResponse) *PollResult {
	endpoint := schemas.BatchEndpoint(job.Endpoint)
	if results.Endpoint != "" {
		endpoint = results.Endpoint
	}
	return &PollResult{
		Job:        job,
		Terminal:   true,
		Settleable: true,
		Payload: BatchPayload{
			Endpoint:      endpoint,
			Results:       results.Results,
			ParseErrors:   results.ExtraFields.ParseErrors,
			RequestCounts: &retrieved.RequestCounts,
		},
	}
}

// retrieveBatch and fetchBatchResults wrap the provider calls in a bounded context.
// The sweep is serial and the sweeper's own context is long-lived with no deadline,
// so an unbounded provider call would stall every remaining due job. The timeout is
// derived from the parent, so shutdown cancellation still propagates.
func (s *BatchSettler) retrieveBatch(ctx context.Context, job *cstables.TableProviderJob) (*schemas.BifrostBatchRetrieveResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, defaultProviderPollTimeout)
	defer cancel()
	return s.fetcher.RetrieveBatch(callCtx, job)
}

func (s *BatchSettler) fetchBatchResults(ctx context.Context, job *cstables.TableProviderJob) (*schemas.BifrostBatchResultsResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, defaultProviderPollTimeout)
	defer cancel()
	return s.fetcher.FetchBatchResults(callCtx, job)
}

// fetchResultsForSettlement returns the batch's results, or ok=false when they
// could not be obtained. What that means is the caller's to decide: a live batch
// retries later, a terminal one gives up.
func (s *BatchSettler) fetchResultsForSettlement(ctx context.Context, job *cstables.TableProviderJob) (*schemas.BifrostBatchResultsResponse, bool) {
	results, err := s.fetchBatchResults(ctx, job)
	if err != nil || results == nil {
		return nil, false
	}
	if results.BatchID != "" && results.BatchID != job.JobID {
		return nil, false
	}
	return results, true
}

// Settle prices the batch's result rows. Malformed rows are not a reason to
// abandon the batch: one bad JSONL line used to discard every correctly parsed
// row's tokens and cost permanently — the raw provider results are not persisted
// anywhere else, so what is not priced here is lost for good. The parsed rows are
// priced normally and the count we could not read rides on the aggregate row.
func (s *BatchSettler) Settle(ctx context.Context, pricing PricingManager, jobReq JobRequest) (*Settlement, error) {
	in := batchInputFrom(jobReq)

	settlement, err := summarizeResults(pricing, in)
	if err != nil {
		return nil, err
	}
	settlement.Object = schemas.BatchResultsRequest

	providerStatus := ""
	if jobReq.Job != nil {
		providerStatus = jobReq.Job.ProviderStatus
	}

	if settlement.PricedCount == 0 {
		reason := settlement.UnpriceableReason
		if reason == "" {
			reason = UnpriceableReasonNoUsage
		}
		// Parse errors only become the headline reason when they are the sole cause.
		// If rows did parse and carried usage we simply could not price, that is the
		// more specific and more actionable diagnosis.
		if len(in.ParseErrors) > 0 && (reason == UnpriceableReasonNoResults || reason == UnpriceableReasonNoUsage) {
			reason = UnpriceableReasonResultParseErrors
			settlement.ReasonErr = fmt.Errorf("batch result contained %d malformed row(s)", len(in.ParseErrors))
		}
		settlement.UnpriceableReason = reason
		settlement.RecordUnpriced = settlement.Usage.TotalTokens > 0
		settlement.Model = unpricedRowModel(in.FallbackModel, settlement)
		settlement.ApplyDebug = batchDebugFor(in, settlement.ModelBreakdowns, providerStatus, false)
		return settlement, nil
	}

	settlement.Priced = true
	if !settlement.Complete && settlement.UnpriceableReason == "" {
		settlement.UnpriceableReason = UnpriceableReasonMissingBatchPricing
	}
	settlement.Model = aggregateRowModel(in.FallbackModel, settlement.ModelBreakdowns)
	settlement.ApplyDebug = batchDebugFor(in, settlement.ModelBreakdowns, providerStatus, settlement.Complete)
	return settlement, nil
}

// aggregateRowModel labels the aggregate row. A batch spanning several models has
// no single one, so it is recorded as "mixed" rather than picking a winner.
func aggregateRowModel(fallbackModel string, breakdowns map[string]ModelBreakdown) string {
	model := fallbackModel
	if len(breakdowns) == 1 {
		for key := range breakdowns {
			model = key
		}
	} else if len(breakdowns) > 1 {
		model = "mixed"
	}
	if model == "" {
		model = "mixed"
	}
	return model
}

// unpricedRowModel labels a wholly-unpriced row, which must stay attributable so
// the missing-cost backfill can reprice it later.
func unpricedRowModel(fallbackModel string, computed *Settlement) string {
	model := aggregateRowModel(fallbackModel, computed.ModelBreakdowns)
	if computed.UnpricedModel != "" {
		return computed.UnpricedModel
	}
	if len(computed.ModelBreakdowns) == 1 {
		// aggregateRowModel named the row after the single breakdown entry, but
		// UnpricedModel was deliberately left blank — the only way that happens with
		// exactly one breakdown is a missing-model row mixed in (see summarizeResults),
		// meaning Usage is a mixture that must not be attributed to the one named model.
		return "mixed"
	}
	return model
}

// batchDebugFor builds the closure that attaches batch detail to the aggregate row.
func batchDebugFor(in batchInput, breakdowns map[string]ModelBreakdown, providerStatus string, complete bool) func(*logstore.Log) {
	return func(entry *logstore.Log) {
		// The provider is already a column on the row, and the attribution step sets
		// ParentRequestID for the source request, so neither is repeated here.
		debug := &schemas.BifrostBatchDebug{
			BatchID:  in.BatchID,
			Status:   providerStatus,
			Endpoint: string(in.Endpoint),
			Accounting: &schemas.BatchAccountingDebug{
				ModelBreakdowns: breakdowns,
				ParseErrorCount: len(in.ParseErrors),
				// Two different ways to under-state, one marker: usage that failed to price,
				// and rows that never parsed. Parse errors are unrecoverable (the raw results
				// are not kept), so unlike unpriced usage they do not hold the settlement back
				// — the batch still settles, it just settles on record as short.
				Incomplete: !complete || len(in.ParseErrors) > 0,
			},
		}
		if requestCounts := requestCountsForAggregateLog(in); !requestCounts.IsZero() {
			debug.RequestCounts = &requestCounts
		}
		entry.BatchDebugParsed = debug
	}
}

func (s *BatchSettler) HydrateFromLog(entry *logstore.Log, out *Outcome) {
	if entry.BatchDebugParsed == nil {
		return
	}
	out.Status = entry.BatchDebugParsed.Status
	if entry.BatchDebugParsed.Accounting != nil {
		out.ModelBreakdowns = entry.BatchDebugParsed.Accounting.ModelBreakdowns
	}
}

func batchJobFromRetrieve(existing *cstables.TableProviderJob, retrieved *schemas.BifrostBatchRetrieveResponse) *cstables.TableProviderJob {
	job := *existing
	job.ProviderStatus = string(retrieved.Status)
	if retrieved.Endpoint != "" {
		job.Endpoint = retrieved.Endpoint
	}

	if retrieved.InputFileID != "" {
		job.InputFileID = retrieved.InputFileID
	}
	if retrieved.OutputFileID != nil {
		job.OutputFileID = retrieved.OutputFileID
	}
	if retrieved.ErrorFileID != nil {
		job.ErrorFileID = retrieved.ErrorFileID
	}
	if retrieved.ResultsURL != nil {
		job.ResultsURL = retrieved.ResultsURL
	}
	return &job
}

func isTerminalStatus(status schemas.BatchStatus) bool {
	switch status {
	case schemas.BatchStatusCompleted, schemas.BatchStatusFailed, schemas.BatchStatusExpired, schemas.BatchStatusCancelled, schemas.BatchStatusEnded, schemas.BatchStatusDeleted:
		return true
	default:
		return false
	}
}

// terminalMayHaveResults reports whether a terminal provider status can still have
// completed rows worth fetching. Expired and cancelled batches keep the rows that
// finished; a failed batch only has an output file when the provider says so; a
// deleted batch has nothing left to read.
func terminalMayHaveResults(status schemas.BatchStatus, job *cstables.TableProviderJob) bool {
	switch status {
	case schemas.BatchStatusExpired, schemas.BatchStatusCancelled:
		return true
	case schemas.BatchStatusFailed:
		return (job.OutputFileID != nil && *job.OutputFileID != "") || (job.ResultsURL != nil && *job.ResultsURL != "")
	default:
		return false
	}
}

func requestCountsForAggregateLog(in batchInput) schemas.BatchRequestCounts {
	if in.RequestCounts != nil && !in.RequestCounts.IsZero() {
		return *in.RequestCounts
	}
	return schemas.BatchRequestCountsFromResults(in.Results)
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func summarizeResults(pricing PricingManager, in batchInput) (*Settlement, error) {
	if len(in.Results) == 0 {
		return &Settlement{UnpriceableReason: UnpriceableReasonNoResults}, nil
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

	for _, item := range in.Results {
		if item.Failed() {
			failedCount++
			continue
		}
		extracted, err := extractUsage(in.Provider, in.FallbackModel, item)
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

		costDetails := pricing.CalculateBatchCostDetailsForUsage(extracted.usage, in.Provider, extracted.model, BatchRequestType(in.Endpoint), in.Scopes)
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
		return &Settlement{
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
	settlement := &Settlement{
		Cost:            totalCost,
		Usage:           totalUsage,
		ModelBreakdowns: breakdowns,
		PricedCount:     pricedCount,
		UnpricedCount:   unpricedCount,
		FailedCount:     failedCount,
		Complete:        !missingModelSeen && !missingPricingSeen,
	}
	if !settlement.Complete {
		if missingModelSeen {
			settlement.UnpriceableReason = UnpriceableReasonMissingModel
		} else {
			settlement.UnpriceableReason = UnpriceableReasonMissingBatchPricing
		}
	}
	return settlement, nil
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
