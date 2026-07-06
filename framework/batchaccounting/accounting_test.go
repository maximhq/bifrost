package batchaccounting

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAccountingStore struct {
	logs             map[string]*logstore.Log
	jobs             map[string]*logstore.BatchJob
	failCompleteOnce bool
}

func newFakeAccountingStore() *fakeAccountingStore {
	return &fakeAccountingStore{
		logs: make(map[string]*logstore.Log),
		jobs: make(map[string]*logstore.BatchJob),
	}
}

func (s *fakeAccountingStore) CreateIfNotExists(ctx context.Context, entry *logstore.Log) error {
	if _, ok := s.logs[entry.ID]; ok {
		return nil
	}
	copied := *entry
	s.logs[entry.ID] = &copied
	return nil
}

func (s *fakeAccountingStore) UpsertBatchJob(ctx context.Context, job *logstore.BatchJob) error {
	if job.ID == "" {
		job.ID = logstore.BatchJobID(job.Provider, job.BatchID)
	}
	existing, ok := s.jobs[job.ID]
	if !ok {
		copied := *job
		if copied.AccountingStatus == "" {
			copied.AccountingStatus = logstore.BatchJobAccountingStatusPending
		}
		s.jobs[job.ID] = &copied
		return nil
	}
	if job.ProviderStatus != "" {
		existing.ProviderStatus = job.ProviderStatus
	}
	if job.OutputFileID != nil {
		existing.OutputFileID = job.OutputFileID
	}
	if job.NextCheckAt != nil {
		existing.NextCheckAt = job.NextCheckAt
	}
	if job.PollAttempts > 0 {
		existing.PollAttempts = job.PollAttempts
	}
	return nil
}

func (s *fakeAccountingStore) FindBatchJobByID(ctx context.Context, jobID string) (*logstore.BatchJob, error) {
	job, ok := s.jobs[jobID]
	if !ok {
		return nil, errors.New("missing batch job")
	}
	copied := *job
	return &copied, nil
}

func (s *fakeAccountingStore) FindDueBatchJobs(ctx context.Context, provider string, now time.Time, limit int) ([]*logstore.BatchJob, error) {
	var jobs []*logstore.BatchJob
	for _, job := range s.jobs {
		if provider != "" && job.Provider != provider {
			continue
		}
		if job.NextCheckAt == nil || job.NextCheckAt.After(now) {
			continue
		}
		if job.AccountingStatus == logstore.BatchJobAccountingStatusAccounted || job.AccountingStatus == logstore.BatchJobAccountingStatusUnpriceable {
			continue
		}
		jobs = append(jobs, job)
		if limit > 0 && len(jobs) >= limit {
			break
		}
	}
	return jobs, nil
}

func (s *fakeAccountingStore) ClaimBatchJobAccounting(ctx context.Context, jobID string, claimedBy string, ttl time.Duration) (string, bool, error) {
	entry, ok := s.jobs[jobID]
	if !ok {
		return "", false, errors.New("missing batch job")
	}
	if entry.AccountingStatus == logstore.BatchJobAccountingStatusAccounted || entry.AccountingStatus == logstore.BatchJobAccountingStatusUnpriceable {
		return "", false, nil
	}
	if entry.AccountingStatus == logstore.BatchJobAccountingStatusProcessing {
		return "", false, nil
	}
	token := "claim-token-" + jobID
	entry.AccountingStatus = logstore.BatchJobAccountingStatusProcessing
	entry.ClaimedBy = &claimedBy
	expires := time.Now().Add(ttl)
	entry.ClaimExpiresAt = &expires
	entry.ClaimToken = &token
	return token, true, nil
}

func (s *fakeAccountingStore) MarkBatchJobAggregateLogWritten(ctx context.Context, id string, claimToken string) error {
	entry, ok := s.jobs[id]
	if !ok {
		return errors.New("missing batch job")
	}
	if entry.ClaimToken == nil || *entry.ClaimToken != claimToken {
		return errors.New("stale claim token")
	}
	now := time.Now().UTC()
	entry.AggregateLogWrittenAt = &now
	return nil
}

func (s *fakeAccountingStore) MarkBatchJobGovernanceReported(ctx context.Context, id string, claimToken string) error {
	entry, ok := s.jobs[id]
	if !ok {
		return errors.New("missing batch job")
	}
	if entry.ClaimToken == nil || *entry.ClaimToken != claimToken {
		return errors.New("stale claim token")
	}
	now := time.Now().UTC()
	entry.GovernanceReportedAt = &now
	return nil
}

func (s *fakeAccountingStore) CompleteBatchJobAccounting(ctx context.Context, id string, claimToken string) error {
	entry, ok := s.jobs[id]
	if !ok {
		return errors.New("missing batch job")
	}
	if entry.ClaimToken == nil || *entry.ClaimToken != claimToken {
		return errors.New("stale claim token")
	}
	if s.failCompleteOnce {
		s.failCompleteOnce = false
		return errors.New("complete failed")
	}
	entry.AccountingStatus = logstore.BatchJobAccountingStatusAccounted
	entry.ClaimExpiresAt = nil
	entry.ClaimToken = nil
	return nil
}

func (s *fakeAccountingStore) MarkBatchJobUnpriceable(ctx context.Context, id string, claimToken string, reason string, err error) error {
	entry, ok := s.jobs[id]
	if !ok {
		return errors.New("missing batch job")
	}
	if entry.ClaimToken == nil || *entry.ClaimToken != claimToken {
		return errors.New("stale claim token")
	}
	entry.AccountingStatus = logstore.BatchJobAccountingStatusUnpriceable
	entry.UnpriceableReason = &reason
	entry.ClaimToken = nil
	entry.ClaimExpiresAt = nil
	return nil
}

func (s *fakeAccountingStore) FailBatchJobAccounting(ctx context.Context, id string, claimToken string, err error) error {
	entry, ok := s.jobs[id]
	if !ok {
		return errors.New("missing batch job")
	}
	entry.AccountingStatus = logstore.BatchJobAccountingStatusError
	entry.ClaimToken = nil
	return nil
}

type fakeBatchPricing struct{}

func (fakeBatchPricing) CalculateBatchCostDetailsForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *modelcatalog.PricingLookupScopes) modelcatalog.BatchCostDetails {
	if usage == nil || requestType != schemas.BatchResultsRequest {
		return modelcatalog.BatchCostDetails{}
	}
	if usage.Cost != nil {
		return modelcatalog.BatchCostDetails{Cost: usage.Cost.TotalCost, Priced: true, ProviderCostUsed: true}
	}
	inputRate := 0.00001
	outputRate := 0.00002
	switch model {
	case "gpt-4o-mini":
		inputRate = 0.000005
		outputRate = 0.000010
	case "gpt-4o":
	case "claude-3-5-haiku":
	case "amazon.nova-lite-v1:0":
	default:
		return modelcatalog.BatchCostDetails{}
	}
	return modelcatalog.BatchCostDetails{
		Cost:                      float64(usage.PromptTokens)*inputRate + float64(usage.CompletionTokens)*outputRate,
		Priced:                    true,
		InputCostPerTokenBatches:  &inputRate,
		OutputCostPerTokenBatches: &outputRate,
	}
}

type fakeAggregateLogWriter struct {
	emitted []*logstore.Log
}

func (w *fakeAggregateLogWriter) EmitBatchAggregateLog(ctx context.Context, entry *logstore.Log) {
	copied := *entry
	w.emitted = append(w.emitted, &copied)
}

type fakeUsageReporter struct {
	reports []BatchUsageReport
}

type requestTypePricing struct {
	requestTypes []schemas.RequestType
}

func (p *requestTypePricing) CalculateBatchCostDetailsForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *modelcatalog.PricingLookupScopes) modelcatalog.BatchCostDetails {
	p.requestTypes = append(p.requestTypes, requestType)
	return modelcatalog.BatchCostDetails{Cost: float64(usage.PromptTokens), Priced: true}
}

func (r *fakeUsageReporter) ReportBatchUsage(ctx context.Context, report BatchUsageReport) error {
	r.reports = append(r.reports, report)
	return nil
}

func TestAccountBatchResults_OpenAIAggregatesAndWritesOnce(t *testing.T) {
	store := newFakeAccountingStore()
	baseLog := &logstore.Log{
		ID:              "request-1",
		Provider:        string(schemas.OpenAI),
		Model:           "gpt-4o-mini",
		SelectedKeyID:   "key-1",
		SelectedKeyName: "primary",
	}

	req := Request{
		Provider:      schemas.OpenAI,
		BatchID:       "batch_123",
		FallbackModel: "gpt-4o-mini",
		BaseLog:       baseLog,
		ClaimedBy:     "test-node",
		Now:           time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC),
		RequestCounts: &schemas.BatchRequestCounts{Total: 3, Completed: 2, Failed: 1},
		Results: []schemas.BatchResultItem{
			openAIResult(200, "gpt-4o-mini", 18, 9),
			openAIResult(200, "gpt-4o", 20, 5),
			openAIResult(500, "gpt-4o-mini", 100, 100),
		},
	}

	summary, err := AccountBatchResults(context.Background(), store, fakeBatchPricing{}, req)
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	assert.Equal(t, 38, summary.Usage.PromptTokens)
	assert.Equal(t, 14, summary.Usage.CompletionTokens)
	assert.Equal(t, 52, summary.Usage.TotalTokens)
	assert.InDelta(t, 0.00048, summary.Cost, 1e-12)
	require.Len(t, store.logs, 1)

	logEntry := store.logs[AccountingLogID(schemas.OpenAI, "batch_123")]
	require.NotNil(t, logEntry)
	assert.Equal(t, "request-1", *logEntry.ParentRequestID)
	assert.Equal(t, string(schemas.BatchResultsRequest), logEntry.Object)
	assert.Equal(t, "mixed", logEntry.Model)
	assert.Equal(t, summary.Cost, *logEntry.Cost)
	assert.Equal(t, "key-1", logEntry.SelectedKeyID)
	assert.Equal(t, true, logEntry.MetadataParsed["batch_accounting"])
	assert.Equal(t, schemas.BatchRequestCounts{Total: 3, Completed: 2, Failed: 1}, logEntry.MetadataParsed["request_counts"])
	breakdown := logEntry.MetadataParsed["model_breakdown"].(map[string]ModelBreakdown)
	require.NotNil(t, breakdown["gpt-4o-mini"].InputCostPerTokenBatches)
	assert.Equal(t, 0.000005, *breakdown["gpt-4o-mini"].InputCostPerTokenBatches)
	require.NotNil(t, breakdown["gpt-4o-mini"].OutputCostPerTokenBatches)
	assert.Equal(t, 0.000010, *breakdown["gpt-4o-mini"].OutputCostPerTokenBatches)

	second, err := AccountBatchResults(context.Background(), store, fakeBatchPricing{}, req)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.False(t, second.Accounted)
	assert.Len(t, store.logs, 1)
}

func TestAccountBatchResults_MissingModelMarksUnpriceable(t *testing.T) {
	store := newFakeAccountingStore()
	result := openAIResult(200, "", 18, 9)

	summary, err := AccountBatchResults(context.Background(), store, fakeBatchPricing{}, Request{
		Provider:  schemas.OpenAI,
		BatchID:   "batch_missing_model",
		ClaimedBy: "test-node",
		Results:   []schemas.BatchResultItem{result},
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.False(t, summary.Accounted)
	assert.Equal(t, UnpriceableReasonMissingModel, summary.UnpriceableReason)

	job := store.jobs[logstore.BatchJobID(string(schemas.OpenAI), "batch_missing_model")]
	require.NotNil(t, job)
	assert.Equal(t, logstore.BatchJobAccountingStatusUnpriceable, job.AccountingStatus)
	require.NotNil(t, job.UnpriceableReason)
	assert.Equal(t, UnpriceableReasonMissingModel, *job.UnpriceableReason)
	assert.Empty(t, store.logs)
}

func TestAccountBatchResults_MissingBatchPricingMarksUnpriceable(t *testing.T) {
	store := newFakeAccountingStore()

	summary, err := AccountBatchResults(context.Background(), store, fakeBatchPricing{}, Request{
		Provider: schemas.OpenAI,
		BatchID:  "batch_missing_pricing",
		Results:  []schemas.BatchResultItem{openAIResult(200, "unknown-model", 18, 9)},
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.False(t, summary.Accounted)
	assert.Equal(t, UnpriceableReasonMissingBatchPricing, summary.UnpriceableReason)

	job := store.jobs[logstore.BatchJobID(string(schemas.OpenAI), "batch_missing_pricing")]
	require.NotNil(t, job)
	assert.Equal(t, logstore.BatchJobAccountingStatusUnpriceable, job.AccountingStatus)
	require.NotNil(t, job.UnpriceableReason)
	assert.Equal(t, UnpriceableReasonMissingBatchPricing, *job.UnpriceableReason)
	assert.Empty(t, store.logs)
}

func TestAccountBatchResults_ProviderCostPassthrough(t *testing.T) {
	store := newFakeAccountingStore()
	result := openAIResult(200, "unknown-provider-priced-model", 18, 9)
	result.Response.Body["usage"].(map[string]interface{})["cost"] = map[string]interface{}{
		"total_cost": 0.123,
	}

	summary, err := AccountBatchResults(context.Background(), store, fakeBatchPricing{}, Request{
		Provider:  schemas.OpenAI,
		BatchID:   "batch_provider_cost",
		ClaimedBy: "test-node",
		Results:   []schemas.BatchResultItem{result},
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	assert.InDelta(t, 0.123, summary.Cost, 1e-12)

	breakdown := summary.ModelBreakdowns["unknown-provider-priced-model"]
	assert.True(t, breakdown.ProviderCostUsed)
	assert.InDelta(t, 0.123, breakdown.ProviderCost, 1e-12)
}

func TestAccountBatchResults_ZeroProviderCostIsStillPriced(t *testing.T) {
	store := newFakeAccountingStore()
	result := openAIResult(200, "unknown-provider-priced-model", 18, 9)
	result.Response.Body["usage"].(map[string]interface{})["cost"] = map[string]interface{}{
		"total_cost": 0.0,
	}

	summary, err := AccountBatchResults(context.Background(), store, fakeBatchPricing{}, Request{
		Provider: schemas.OpenAI,
		BatchID:  "batch_zero_cost",
		Results:  []schemas.BatchResultItem{result},
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	assert.Equal(t, 1, summary.PricedCount)
	assert.Equal(t, 0.0, summary.Cost)
}

func TestAccountBatchResults_AnthropicAggregatesUsage(t *testing.T) {
	store := newFakeAccountingStore()

	summary, err := AccountBatchResults(context.Background(), store, fakeBatchPricing{}, Request{
		Provider: schemas.Anthropic,
		BatchID:  "anthropic_batch",
		Results: []schemas.BatchResultItem{
			anthropicResult("claude-3-5-haiku", 10, 2, 3, 5),
			anthropicResult("claude-3-5-haiku", 7, 0, 0, 4),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	assert.Equal(t, 22, summary.Usage.PromptTokens)
	assert.Equal(t, 9, summary.Usage.CompletionTokens)
	assert.Equal(t, 31, summary.Usage.TotalTokens)
	assert.InDelta(t, 0.00040, summary.Cost, 1e-12)
}

func TestAccountBatchResults_BedrockAggregatesUsageFromResponseBody(t *testing.T) {
	store := newFakeAccountingStore()

	summary, err := AccountBatchResults(context.Background(), store, fakeBatchPricing{}, Request{
		Provider:      schemas.Bedrock,
		BatchID:       "bedrock_batch",
		FallbackModel: "amazon.nova-lite-v1:0",
		Results: []schemas.BatchResultItem{
			bedrockResult("", 12, 6),
			bedrockResult("amazon.nova-lite-v1:0", 8, 2),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	assert.Equal(t, 20, summary.Usage.PromptTokens)
	assert.Equal(t, 8, summary.Usage.CompletionTokens)
	assert.Equal(t, 28, summary.Usage.TotalTokens)
	assert.InDelta(t, 0.00036, summary.Cost, 1e-12)
}

func TestAccountBatchResults_BedrockIncludesCacheDetailsInPromptUsage(t *testing.T) {
	store := newFakeAccountingStore()

	summary, err := AccountBatchResults(context.Background(), store, fakeBatchPricing{}, Request{
		Provider:      schemas.Bedrock,
		BatchID:       "bedrock_cache_batch",
		FallbackModel: "amazon.nova-lite-v1:0",
		Results: []schemas.BatchResultItem{
			{
				CustomID: "custom-id",
				Response: &schemas.BatchResultResponse{
					StatusCode: 200,
					Body: map[string]interface{}{
						"usage": map[string]interface{}{
							"inputTokens":  12,
							"outputTokens": 6,
							"totalTokens":  18,
							"cacheDetails": []map[string]interface{}{
								{"inputTokens": 4, "ttl": "5m"},
								{"inputTokens": 3, "ttl": "1h"},
							},
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	assert.Equal(t, 19, summary.Usage.PromptTokens)
	assert.Equal(t, 6, summary.Usage.CompletionTokens)
	assert.Equal(t, 25, summary.Usage.TotalTokens)
	require.NotNil(t, summary.Usage.PromptTokensDetails)
	assert.Equal(t, 7, summary.Usage.PromptTokensDetails.CachedWriteTokens)
	require.NotNil(t, summary.Usage.PromptTokensDetails.CachedWriteTokenDetails)
	assert.Equal(t, 4, summary.Usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m)
	assert.Equal(t, 3, summary.Usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h)
	assert.InDelta(t, 0.00031, summary.Cost, 1e-12)
}

func TestAccountBatchResults_UsesAggregateWriterAndUsageReporter(t *testing.T) {
	store := newFakeAccountingStore()
	writer := &fakeAggregateLogWriter{}
	reporter := &fakeUsageReporter{}

	summary, err := AccountBatchResults(context.Background(), store, fakeBatchPricing{}, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "batch_writer",
		FallbackModel: "gpt-4o-mini",
		Emitter:       writer,
		UsageReporter: reporter,
		Results:       []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 18, 9)},
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	require.Len(t, store.logs, 1)
	require.Len(t, writer.emitted, 1)
	require.Len(t, reporter.reports, 1)
	assert.Equal(t, AccountingLogID(schemas.OpenAI, "batch_writer"), reporter.reports[0].RequestID)
	assert.Equal(t, int64(27), reporter.reports[0].TokensUsed)
}

func TestAccountBatchResults_RetryAfterCompleteFailureDoesNotReportGovernanceTwice(t *testing.T) {
	store := newFakeAccountingStore()
	store.failCompleteOnce = true
	writer := &fakeAggregateLogWriter{}
	reporter := &fakeUsageReporter{}

	req := Request{
		Provider:      schemas.OpenAI,
		BatchID:       "batch_retry_governance",
		FallbackModel: "gpt-4o-mini",
		Emitter:       writer,
		UsageReporter: reporter,
		Results:       []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 18, 9)},
	}

	summary, err := AccountBatchResults(context.Background(), store, fakeBatchPricing{}, req)
	require.Error(t, err)
	require.Nil(t, summary)
	require.Len(t, writer.emitted, 1)
	require.Len(t, reporter.reports, 1)

	summary, err = AccountBatchResults(context.Background(), store, fakeBatchPricing{}, req)
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	require.Len(t, store.logs, 1)
	require.Len(t, writer.emitted, 1)
	require.Len(t, reporter.reports, 1)
}

func TestAccountBatchResults_RecordsPartialPricingMetadata(t *testing.T) {
	store := newFakeAccountingStore()

	summary, err := AccountBatchResults(context.Background(), store, fakeBatchPricing{}, Request{
		Provider: schemas.OpenAI,
		BatchID:  "batch_partial",
		Results: []schemas.BatchResultItem{
			openAIResult(200, "gpt-4o-mini", 10, 5),
			openAIResult(200, "", 10, 5),
			openAIResult(200, "unknown-model", 10, 5),
			{CustomID: "failed", Error: &schemas.BatchResultError{Code: "bad_request"}},
			{CustomID: "http-failed", Response: &schemas.BatchResultResponse{StatusCode: 400}},
			{CustomID: "anthropic-failed", Result: &schemas.BatchResultData{Type: "errored"}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	assert.Equal(t, 1, summary.PricedCount)
	assert.Equal(t, 2, summary.UnpricedCount)
	assert.Equal(t, 3, summary.FailedCount)
	assert.Equal(t, 1, summary.UnpricedReasons[UnpriceableReasonMissingModel])
	assert.Equal(t, 1, summary.UnpricedReasons[UnpriceableReasonMissingBatchPricing])

	logEntry := store.logs[AccountingLogID(schemas.OpenAI, "batch_partial")]
	require.NotNil(t, logEntry)
	assert.Equal(t, 1, logEntry.MetadataParsed["priced_count"])
	assert.Equal(t, 2, logEntry.MetadataParsed["unpriced_count"])
	assert.Equal(t, 3, logEntry.MetadataParsed["failed_count"])
}

type fakeBatchResultFetcher struct {
	retrieveCalls int
	resultsCalls  int
	retrieveResp  *schemas.BifrostBatchRetrieveResponse
	resultsResp   *schemas.BifrostBatchResultsResponse
}

func (f *fakeBatchResultFetcher) RetrieveBatch(ctx context.Context, job *logstore.BatchJob) (*schemas.BifrostBatchRetrieveResponse, error) {
	f.retrieveCalls++
	return f.retrieveResp, nil
}

func (f *fakeBatchResultFetcher) FetchBatchResults(ctx context.Context, job *logstore.BatchJob) (*schemas.BifrostBatchResultsResponse, error) {
	f.resultsCalls++
	return f.resultsResp, nil
}

type fakeKVStore struct {
	setNXAllowed bool
	setNXCalls   int
	deleteCalls  int
}

func (s *fakeKVStore) Get(key string) (any, error) {
	return nil, nil
}

func (s *fakeKVStore) SetWithTTL(key string, value any, ttl time.Duration) error {
	return nil
}

func (s *fakeKVStore) SetNXWithTTL(key string, value any, ttl time.Duration) (bool, error) {
	s.setNXCalls++
	return s.setNXAllowed, nil
}

func (s *fakeKVStore) Delete(key string) (bool, error) {
	s.deleteCalls++
	return true, nil
}

func TestSweeper_AccountsCompletedOpenAIJob(t *testing.T) {
	store := newFakeAccountingStore()
	now := time.Now().UTC().Add(-time.Minute)
	job := &logstore.BatchJob{
		ID:               logstore.BatchJobID(string(schemas.OpenAI), "batch_sweep"),
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_sweep",
		Model:            "gpt-4o-mini",
		AccountingStatus: logstore.BatchJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{
		retrieveResp: &schemas.BifrostBatchRetrieveResponse{
			ID:     "batch_sweep",
			Status: schemas.BatchStatusCompleted,
		},
		resultsResp: &schemas.BifrostBatchResultsResponse{
			BatchID: "batch_sweep",
			Results: []schemas.BatchResultItem{
				openAIResult(200, "gpt-4o-mini", 18, 9),
			},
		},
	}
	sweeper := NewSweeper(store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{
		Provider: schemas.OpenAI,
		Limit:    10,
	})

	sweeper.SweepOnce(context.Background())

	assert.Equal(t, 1, fetcher.retrieveCalls)
	assert.Equal(t, 1, fetcher.resultsCalls)
	accounted := store.jobs[logstore.BatchJobID(string(schemas.OpenAI), "batch_sweep")]
	require.NotNil(t, accounted)
	assert.Equal(t, logstore.BatchJobAccountingStatusAccounted, accounted.AccountingStatus)
	assert.Len(t, store.logs, 1)
}

func TestSweeper_AccountsCompletedAnthropicJob(t *testing.T) {
	store := newFakeAccountingStore()
	now := time.Now().UTC().Add(-time.Minute)
	job := &logstore.BatchJob{
		ID:               logstore.BatchJobID(string(schemas.Anthropic), "anthropic_sweep"),
		Provider:         string(schemas.Anthropic),
		BatchID:          "anthropic_sweep",
		Model:            "claude-3-5-haiku",
		AccountingStatus: logstore.BatchJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{
		retrieveResp: &schemas.BifrostBatchRetrieveResponse{
			ID:     "anthropic_sweep",
			Status: schemas.BatchStatusCompleted,
		},
		resultsResp: &schemas.BifrostBatchResultsResponse{
			BatchID: "anthropic_sweep",
			Results: []schemas.BatchResultItem{
				anthropicResult("claude-3-5-haiku", 10, 0, 0, 5),
			},
		},
	}
	sweeper := NewSweeper(store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{
		Limit: 10,
	})

	sweeper.SweepOnce(context.Background())

	assert.Equal(t, 1, fetcher.retrieveCalls)
	assert.Equal(t, 1, fetcher.resultsCalls)
	accounted := store.jobs[logstore.BatchJobID(string(schemas.Anthropic), "anthropic_sweep")]
	require.NotNil(t, accounted)
	assert.Equal(t, logstore.BatchJobAccountingStatusAccounted, accounted.AccountingStatus)
}

func TestSweeper_SkipsProviderPollWhenKVLeaseIsHeld(t *testing.T) {
	store := newFakeAccountingStore()
	now := time.Now().UTC().Add(-time.Minute)
	job := &logstore.BatchJob{
		ID:               logstore.BatchJobID(string(schemas.OpenAI), "batch_sweep_lease"),
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_sweep_lease",
		Model:            "gpt-4o-mini",
		AccountingStatus: logstore.BatchJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{}
	kv := &fakeKVStore{setNXAllowed: false}
	sweeper := NewSweeper(store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{
		Provider: schemas.OpenAI,
		Limit:    10,
		KVStore:  kv,
	})

	sweeper.SweepOnce(context.Background())

	assert.Equal(t, 1, kv.setNXCalls)
	assert.Zero(t, kv.deleteCalls)
	assert.Zero(t, fetcher.retrieveCalls)
	assert.Zero(t, fetcher.resultsCalls)
}

func TestSweeper_ReleasesProviderPollLeaseAfterSuccessfulPoll(t *testing.T) {
	store := newFakeAccountingStore()
	now := time.Now().UTC().Add(-time.Minute)
	job := &logstore.BatchJob{
		ID:               logstore.BatchJobID(string(schemas.OpenAI), "batch_release_lease"),
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_release_lease",
		AccountingStatus: logstore.BatchJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{retrieveResp: &schemas.BifrostBatchRetrieveResponse{
		ID:     job.BatchID,
		Status: schemas.BatchStatusInProgress,
	}}
	kv := &fakeKVStore{setNXAllowed: true}
	sweeper := NewSweeper(store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{
		Provider: schemas.OpenAI,
		Limit:    10,
		KVStore:  kv,
	})

	sweeper.SweepOnce(context.Background())

	assert.Equal(t, 1, kv.setNXCalls)
	assert.Equal(t, 1, kv.deleteCalls)
}

func TestSweeper_RescheduleUsesBackoffAndJitter(t *testing.T) {
	store := newFakeAccountingStore()
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	job := &logstore.BatchJob{
		ID:               logstore.BatchJobID(string(schemas.OpenAI), "batch_backoff"),
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_backoff",
		Model:            "gpt-4o-mini",
		AccountingStatus: logstore.BatchJobAccountingStatusPending,
		PollAttempts:     9,
		NextCheckAt:      &now,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{
		retrieveResp: &schemas.BifrostBatchRetrieveResponse{
			ID:     "batch_backoff",
			Status: schemas.BatchStatusInProgress,
		},
	}
	sweeper := NewSweeper(store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{
		Interval: time.Minute,
		Limit:    10,
	})

	sweeper.sweepJob(context.Background(), job, now)

	updated := store.jobs[job.ID]
	require.NotNil(t, updated)
	assert.Equal(t, 10, updated.PollAttempts)
	require.NotNil(t, updated.NextCheckAt)
	assert.True(t, updated.NextCheckAt.After(now.Add(5*time.Minute-time.Second)))
	assert.True(t, updated.NextCheckAt.Before(now.Add(6*time.Minute+time.Second)))
}

func openAIResult(status int, model string, promptTokens int, completionTokens int) schemas.BatchResultItem {
	return schemas.BatchResultItem{
		CustomID: "custom-id",
		Response: &schemas.BatchResultResponse{
			StatusCode: status,
			Body: map[string]interface{}{
				"model": model,
				"usage": map[string]interface{}{
					"prompt_tokens":     promptTokens,
					"completion_tokens": completionTokens,
					"total_tokens":      promptTokens + completionTokens,
				},
			},
		},
	}
}

func anthropicResult(model string, inputTokens int, cacheReadTokens int, cacheWriteTokens int, outputTokens int) schemas.BatchResultItem {
	return schemas.BatchResultItem{
		CustomID: "custom-id",
		Result: &schemas.BatchResultData{
			Type: "succeeded",
			Message: map[string]interface{}{
				"model": model,
				"usage": map[string]interface{}{
					"input_tokens":                inputTokens,
					"cache_read_input_tokens":     cacheReadTokens,
					"cache_creation_input_tokens": cacheWriteTokens,
					"output_tokens":               outputTokens,
					"cache_creation":              map[string]interface{}{"ephemeral_5m_input_tokens": cacheWriteTokens},
				},
			},
		},
	}
}

func bedrockResult(model string, promptTokens int, completionTokens int) schemas.BatchResultItem {
	body := map[string]interface{}{
		"usage": map[string]interface{}{
			"inputTokens":  promptTokens,
			"outputTokens": completionTokens,
			"totalTokens":  promptTokens + completionTokens,
		},
	}
	if model != "" {
		body["model"] = model
	}
	return schemas.BatchResultItem{
		CustomID: "custom-id",
		Response: &schemas.BatchResultResponse{
			StatusCode: 200,
			Body:       body,
		},
	}
}

func TestAccountBatchResults_ExternalBatchWithoutRow(t *testing.T) {
	store := newFakeAccountingStore()
	writer := &fakeAggregateLogWriter{}
	reporter := &fakeUsageReporter{}

	summary, err := AccountBatchResults(context.Background(), store, fakeBatchPricing{}, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "ext-batch-no-job",
		FallbackModel: "gpt-4o-mini",
		Results: []schemas.BatchResultItem{
			openAIResult(200, "gpt-4o-mini", 10, 5),
		},
		Emitter:       writer,
		UsageReporter: reporter,
		ClaimedBy:     "test",
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	assert.Greater(t, summary.Cost, 0.0)

	jobID := logstore.BatchJobID(string(schemas.OpenAI), "ext-batch-no-job")
	job, ok := store.jobs[jobID]
	require.True(t, ok, "batch_jobs row should be created for externally-created batch")
	assert.Equal(t, logstore.BatchJobAccountingStatusAccounted, job.AccountingStatus)
}

func TestAccountBatchResults_EmbeddingEndpointUsesEmbeddingPricing(t *testing.T) {
	store := newFakeAccountingStore()
	pricing := &requestTypePricing{}
	summary, err := AccountBatchResults(context.Background(), store, pricing, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "embedding-batch",
		FallbackModel: "text-embedding-3-small",
		Endpoint:      schemas.BatchEndpointEmbeddings,
		Results: []schemas.BatchResultItem{
			openAIResult(200, "text-embedding-3-small", 25, 0),
		},
		ClaimedBy: "test",
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	require.Equal(t, []schemas.RequestType{schemas.EmbeddingRequest}, pricing.requestTypes)
}

func TestAccountBatchResults_ParseErrorsNeverPartiallyFinalize(t *testing.T) {
	store := newFakeAccountingStore()
	summary, err := AccountBatchResults(context.Background(), store, fakeBatchPricing{}, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "malformed-results",
		FallbackModel: "gpt-4o-mini",
		Results: []schemas.BatchResultItem{
			openAIResult(200, "gpt-4o-mini", 10, 5),
		},
		ParseErrors: []schemas.BatchError{{Code: "parse_error", Message: "invalid JSONL"}},
		ClaimedBy:   "test",
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.False(t, summary.Accounted)
	assert.Equal(t, UnpriceableReasonResultParseErrors, summary.UnpriceableReason)
	job := store.jobs[logstore.BatchJobID(string(schemas.OpenAI), "malformed-results")]
	require.NotNil(t, job)
	assert.Equal(t, logstore.BatchJobAccountingStatusUnpriceable, job.AccountingStatus)
	assert.Empty(t, store.logs)
}
