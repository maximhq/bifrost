package batchaccounting

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAccountingStore struct {
	logs             map[string]*logstore.Log
	jobs             map[string]*cstables.TableBatchJob
	failCompleteOnce bool
	failGetOnce      bool
}

func newFakeAccountingStore() *fakeAccountingStore {
	return &fakeAccountingStore{
		logs: make(map[string]*logstore.Log),
		jobs: make(map[string]*cstables.TableBatchJob),
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

func (s *fakeAccountingStore) UpsertBatchJob(ctx context.Context, job *cstables.TableBatchJob) error {
	if job.ID == "" {
		job.ID = cstables.BatchJobID(job.Provider, job.BatchID)
	}
	existing, ok := s.jobs[job.ID]
	if !ok {
		copied := *job
		if copied.AccountingStatus == "" {
			copied.AccountingStatus = cstables.BatchJobAccountingStatusPending
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
	// Refresh updated_at like the real store does. This is deliberately unfenced,
	// which is exactly why claim staleness must not be measured on it.
	existing.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *fakeAccountingStore) GetBatchJob(ctx context.Context, jobID string) (*cstables.TableBatchJob, error) {
	if s.failGetOnce {
		s.failGetOnce = false
		return nil, errors.New("transient read failure")
	}
	job, ok := s.jobs[jobID]
	if !ok {
		return nil, errors.New("missing batch job")
	}
	copied := *job
	return &copied, nil
}

func (s *fakeAccountingStore) ListDueBatchJobs(ctx context.Context, provider string, now time.Time, limit int) ([]*cstables.TableBatchJob, error) {
	var jobs []*cstables.TableBatchJob
	for _, job := range s.jobs {
		if provider != "" && job.Provider != provider {
			continue
		}
		if job.NextCheckAt == nil || job.NextCheckAt.After(now) {
			continue
		}
		if job.AccountingStatus == cstables.BatchJobAccountingStatusAccounted || job.AccountingStatus == cstables.BatchJobAccountingStatusUnpriceable {
			continue
		}
		// Hand back a detached copy: a real store returns rows, not live pointers.
		// Returning the map pointer would let sweeper mutations persist without an
		// UpsertBatchJob and hide missing-write bugs.
		copied := *job
		jobs = append(jobs, &copied)
		if limit > 0 && len(jobs) >= limit {
			break
		}
	}
	return jobs, nil
}

func (s *fakeAccountingStore) ClaimBatchJob(ctx context.Context, jobID, runnerID string, staleBefore time.Time) (bool, error) {
	entry, ok := s.jobs[jobID]
	if !ok {
		return false, errors.New("missing batch job")
	}
	if entry.AccountingStatus == cstables.BatchJobAccountingStatusAccounted || entry.AccountingStatus == cstables.BatchJobAccountingStatusUnpriceable {
		return false, nil
	}
	// Staleness reads claimed_at, never updated_at — mirroring the real store, where
	// updated_at is refreshed by the unfenced UpsertBatchJob and so cannot be trusted
	// to represent claim age.
	if entry.AccountingStatus == cstables.BatchJobAccountingStatusProcessing &&
		entry.ClaimedAt != nil && entry.ClaimedAt.After(staleBefore) {
		return false, nil
	}
	now := time.Now().UTC()
	rid := runnerID
	entry.AccountingStatus = cstables.BatchJobAccountingStatusProcessing
	entry.RunnerID = &rid
	entry.ClaimedAt = &now
	entry.LastError = nil
	entry.UpdatedAt = now
	return true, nil
}

func (s *fakeAccountingStore) ownedJob(id, runnerID string) (*cstables.TableBatchJob, error) {
	entry, ok := s.jobs[id]
	if !ok {
		return nil, errors.New("missing batch job")
	}
	if entry.RunnerID == nil || *entry.RunnerID != runnerID {
		return nil, errors.New("stale runner")
	}
	return entry, nil
}

func (s *fakeAccountingStore) MarkBatchJobAggregateLogWritten(ctx context.Context, id, runnerID string) error {
	entry, err := s.ownedJob(id, runnerID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	entry.AggregateLogWrittenAt = &now
	return nil
}

func (s *fakeAccountingStore) MarkBatchJobGovernanceReported(ctx context.Context, id, runnerID string) error {
	entry, err := s.ownedJob(id, runnerID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	entry.GovernanceReportedAt = &now
	return nil
}

func (s *fakeAccountingStore) CompleteBatchJob(ctx context.Context, id, runnerID string) error {
	entry, err := s.ownedJob(id, runnerID)
	if err != nil {
		return err
	}
	if s.failCompleteOnce {
		s.failCompleteOnce = false
		return errors.New("complete failed")
	}
	entry.AccountingStatus = cstables.BatchJobAccountingStatusAccounted
	entry.RunnerID = nil
	entry.ClaimedAt = nil
	return nil
}

func (s *fakeAccountingStore) MarkBatchJobUnpriceable(ctx context.Context, id, runnerID, reason string, err error) error {
	entry, ownErr := s.ownedJob(id, runnerID)
	if ownErr != nil {
		return ownErr
	}
	entry.AccountingStatus = cstables.BatchJobAccountingStatusUnpriceable
	entry.UnpriceableReason = &reason
	entry.RunnerID = nil
	entry.ClaimedAt = nil
	return nil
}

func (s *fakeAccountingStore) FailBatchJob(ctx context.Context, id, runnerID string, err error) error {
	// Fenced on runnerID like the real finishBatchJob: a stale runner must not be
	// able to release another runner's live claim.
	entry, ownErr := s.ownedJob(id, runnerID)
	if ownErr != nil {
		return ownErr
	}
	entry.AccountingStatus = cstables.BatchJobAccountingStatusError
	entry.RunnerID = nil
	entry.ClaimedAt = nil
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
	case "gemini-2.0-flash":
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

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, req)
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

	second, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, req)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.False(t, second.Accounted)
	assert.Len(t, store.logs, 1)
}

func TestAccountBatchResults_MissingModelMarksUnpriceable(t *testing.T) {
	store := newFakeAccountingStore()
	result := openAIResult(200, "", 18, 9)

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider:  schemas.OpenAI,
		BatchID:   "batch_missing_model",
		ClaimedBy: "test-node",
		Results:   []schemas.BatchResultItem{result},
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.False(t, summary.Accounted)
	assert.Equal(t, UnpriceableReasonMissingModel, summary.UnpriceableReason)

	job := store.jobs[cstables.BatchJobID(string(schemas.OpenAI), "batch_missing_model")]
	require.NotNil(t, job)
	assert.Equal(t, cstables.BatchJobAccountingStatusUnpriceable, job.AccountingStatus)
	require.NotNil(t, job.UnpriceableReason)
	assert.Equal(t, UnpriceableReasonMissingModel, *job.UnpriceableReason)

	// Tokens were consumed, so the row is still logged rather than dropped. Unlike
	// the missing-rates case this one is not backfillable — there is no model to
	// look up — so the row is a record of the usage, not a recoverable cost.
	require.Len(t, store.logs, 1)
	logged := store.logs[AccountingLogID(schemas.OpenAI, "batch_missing_model")]
	require.NotNil(t, logged)
	assert.Nil(t, logged.Cost)
	assert.Equal(t, 18, logged.PromptTokens)
	assert.Equal(t, 9, logged.CompletionTokens)
}

func TestAccountBatchResults_MissingBatchPricingMarksUnpriceable(t *testing.T) {
	store := newFakeAccountingStore()

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider: schemas.OpenAI,
		BatchID:  "batch_missing_pricing",
		Results:  []schemas.BatchResultItem{openAIResult(200, "unknown-model", 18, 9)},
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.False(t, summary.Accounted)
	assert.Equal(t, UnpriceableReasonMissingBatchPricing, summary.UnpriceableReason)

	job := store.jobs[cstables.BatchJobID(string(schemas.OpenAI), "batch_missing_pricing")]
	require.NotNil(t, job)
	assert.Equal(t, cstables.BatchJobAccountingStatusUnpriceable, job.AccountingStatus)
	require.NotNil(t, job.UnpriceableReason)
	assert.Equal(t, UnpriceableReasonMissingBatchPricing, *job.UnpriceableReason)

	// The model is known, only its batch rates are missing — so the row is logged
	// with an unknown cost and stays attributable, which is exactly what the
	// missing-cost backfill needs to price it once rates are added.
	require.Len(t, store.logs, 1, "usage we could not price must still be logged, not dropped")
	logged := store.logs[AccountingLogID(schemas.OpenAI, "batch_missing_pricing")]
	require.NotNil(t, logged)
	assert.Nil(t, logged.Cost, "cost must be unknown (nil), not zero")
	assert.Equal(t, "unknown-model", logged.Model)
	assert.Equal(t, 18, logged.PromptTokens)
	assert.Equal(t, 9, logged.CompletionTokens)
}

func TestAccountBatchResults_ProviderCostPassthrough(t *testing.T) {
	store := newFakeAccountingStore()
	result := openAIResult(200, "unknown-provider-priced-model", 18, 9)
	result.Response.Body["usage"].(map[string]interface{})["cost"] = map[string]interface{}{
		"total_cost": 0.123,
	}

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
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

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
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

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
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

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
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

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
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

func TestAccountBatchResults_GeminiAggregatesUsageFromResponseBody(t *testing.T) {
	store := newFakeAccountingStore()

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider:      schemas.Gemini,
		BatchID:       "gemini_batch",
		FallbackModel: "gemini-2.0-flash",
		Results: []schemas.BatchResultItem{
			geminiResult(11, 3),
			geminiResult(7, 2),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	assert.Equal(t, 18, summary.Usage.PromptTokens)
	assert.Equal(t, 5, summary.Usage.CompletionTokens)
	assert.Equal(t, 23, summary.Usage.TotalTokens)
	assert.InDelta(t, 0.00028, summary.Cost, 1e-12)
}

func TestAccountBatchResults_UsesAggregateWriterAndUsageReporter(t *testing.T) {
	store := newFakeAccountingStore()
	writer := &fakeAggregateLogWriter{}
	reporter := &fakeUsageReporter{}

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
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

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, req)
	require.Error(t, err)
	require.Nil(t, summary)
	require.Len(t, writer.emitted, 1)
	require.Len(t, reporter.reports, 1)

	summary, err = AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, req)
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	require.Len(t, store.logs, 1)
	require.Len(t, writer.emitted, 1)
	require.Len(t, reporter.reports, 1)
}

func TestAccountBatchResults_RecordsPartialPricingMetadata(t *testing.T) {
	store := newFakeAccountingStore()

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
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

func (f *fakeBatchResultFetcher) RetrieveBatch(ctx context.Context, job *cstables.TableBatchJob) (*schemas.BifrostBatchRetrieveResponse, error) {
	f.retrieveCalls++
	return f.retrieveResp, nil
}

func (f *fakeBatchResultFetcher) FetchBatchResults(ctx context.Context, job *cstables.TableBatchJob) (*schemas.BifrostBatchResultsResponse, error) {
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
	job := &cstables.TableBatchJob{
		ID:               cstables.BatchJobID(string(schemas.OpenAI), "batch_sweep"),
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_sweep",
		Model:            "gpt-4o-mini",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
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
	sweeper := NewSweeper(store, store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{
		Provider: schemas.OpenAI,
		Limit:    10,
	})

	sweeper.SweepOnce(context.Background())

	assert.Equal(t, 1, fetcher.retrieveCalls)
	assert.Equal(t, 1, fetcher.resultsCalls)
	accounted := store.jobs[cstables.BatchJobID(string(schemas.OpenAI), "batch_sweep")]
	require.NotNil(t, accounted)
	assert.Equal(t, cstables.BatchJobAccountingStatusAccounted, accounted.AccountingStatus)
	assert.Len(t, store.logs, 1)
}

func TestSweeper_AccountsCompletedAnthropicJob(t *testing.T) {
	store := newFakeAccountingStore()
	now := time.Now().UTC().Add(-time.Minute)
	job := &cstables.TableBatchJob{
		ID:               cstables.BatchJobID(string(schemas.Anthropic), "anthropic_sweep"),
		Provider:         string(schemas.Anthropic),
		BatchID:          "anthropic_sweep",
		Model:            "claude-3-5-haiku",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
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
	sweeper := NewSweeper(store, store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{
		Limit: 10,
	})

	sweeper.SweepOnce(context.Background())

	assert.Equal(t, 1, fetcher.retrieveCalls)
	assert.Equal(t, 1, fetcher.resultsCalls)
	accounted := store.jobs[cstables.BatchJobID(string(schemas.Anthropic), "anthropic_sweep")]
	require.NotNil(t, accounted)
	assert.Equal(t, cstables.BatchJobAccountingStatusAccounted, accounted.AccountingStatus)
}

func TestSweeper_AccountsCompletedGeminiJob(t *testing.T) {
	store := newFakeAccountingStore()
	now := time.Now().UTC().Add(-time.Minute)
	job := &cstables.TableBatchJob{
		ID:               cstables.BatchJobID(string(schemas.Gemini), "gemini_sweep"),
		Provider:         string(schemas.Gemini),
		BatchID:          "gemini_sweep",
		Model:            "gemini-2.0-flash",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{
		retrieveResp: &schemas.BifrostBatchRetrieveResponse{
			ID:     "gemini_sweep",
			Status: schemas.BatchStatusCompleted,
		},
		resultsResp: &schemas.BifrostBatchResultsResponse{
			BatchID: "gemini_sweep",
			Results: []schemas.BatchResultItem{
				geminiResult(10, 5),
			},
		},
	}
	sweeper := NewSweeper(store, store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{
		Limit: 10,
	})

	sweeper.SweepOnce(context.Background())

	assert.Equal(t, 1, fetcher.retrieveCalls)
	assert.Equal(t, 1, fetcher.resultsCalls)
	accounted := store.jobs[cstables.BatchJobID(string(schemas.Gemini), "gemini_sweep")]
	require.NotNil(t, accounted)
	assert.Equal(t, cstables.BatchJobAccountingStatusAccounted, accounted.AccountingStatus)
}

func TestSweeper_SkipsProviderPollWhenKVLeaseIsHeld(t *testing.T) {
	store := newFakeAccountingStore()
	now := time.Now().UTC().Add(-time.Minute)
	job := &cstables.TableBatchJob{
		ID:               cstables.BatchJobID(string(schemas.OpenAI), "batch_sweep_lease"),
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_sweep_lease",
		Model:            "gpt-4o-mini",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{}
	kv := &fakeKVStore{setNXAllowed: false}
	sweeper := NewSweeper(store, store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{
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
	job := &cstables.TableBatchJob{
		ID:               cstables.BatchJobID(string(schemas.OpenAI), "batch_release_lease"),
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_release_lease",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{retrieveResp: &schemas.BifrostBatchRetrieveResponse{
		ID:     job.BatchID,
		Status: schemas.BatchStatusInProgress,
	}}
	kv := &fakeKVStore{setNXAllowed: true}
	sweeper := NewSweeper(store, store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{
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
	job := &cstables.TableBatchJob{
		ID:               cstables.BatchJobID(string(schemas.OpenAI), "batch_backoff"),
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_backoff",
		Model:            "gpt-4o-mini",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
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
	sweeper := NewSweeper(store, store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{
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

func geminiResult(promptTokens int, completionTokens int) schemas.BatchResultItem {
	return schemas.BatchResultItem{
		CustomID: "custom-id",
		Response: &schemas.BatchResultResponse{
			StatusCode: 200,
			Body: map[string]interface{}{
				"usage": map[string]interface{}{
					"prompt_tokens":     promptTokens,
					"completion_tokens": completionTokens,
					"total_tokens":      promptTokens + completionTokens,
				},
			},
		},
	}
}

func TestAccountBatchResults_ExternalBatchWithoutRow(t *testing.T) {
	store := newFakeAccountingStore()
	writer := &fakeAggregateLogWriter{}
	reporter := &fakeUsageReporter{}

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
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

	jobID := cstables.BatchJobID(string(schemas.OpenAI), "ext-batch-no-job")
	job, ok := store.jobs[jobID]
	require.True(t, ok, "batch_jobs row should be created for externally-created batch")
	assert.Equal(t, cstables.BatchJobAccountingStatusAccounted, job.AccountingStatus)
}

func TestAccountBatchResults_EmbeddingEndpointUsesEmbeddingPricing(t *testing.T) {
	store := newFakeAccountingStore()
	pricing := &requestTypePricing{}
	summary, err := AccountBatchResults(context.Background(), store, store, pricing, Request{
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

// The two cache-token wire conventions must normalize to the same internal shape:
// OpenAI/Gemini report cached input as a breakdown of an already-inclusive
// prompt_tokens, while Anthropic/Bedrock report it exclusive of the base count.
// Getting this wrong either double-counts prompt tokens or drops the cache discount.
func TestUsageFromValue_CacheTokenConventions(t *testing.T) {
	t.Run("openai cached_tokens is inclusive of prompt_tokens", func(t *testing.T) {
		usage, err := usageFromValue(map[string]interface{}{
			"prompt_tokens":         1000, // already includes the 400 cached
			"completion_tokens":     50,
			"total_tokens":          1050,
			"prompt_tokens_details": map[string]interface{}{"cached_tokens": 400},
		})
		require.NoError(t, err)
		assert.Equal(t, 1000, usage.PromptTokens, "cached tokens must not be added again")
		require.NotNil(t, usage.PromptTokensDetails, "cache details must be surfaced for the discount")
		assert.Equal(t, 400, usage.PromptTokensDetails.CachedReadTokens)
	})

	t.Run("anthropic cache_read_input_tokens is exclusive of input_tokens", func(t *testing.T) {
		usage, err := usageFromValue(map[string]interface{}{
			"input_tokens":            600, // excludes the 400 cached
			"output_tokens":           50,
			"cache_read_input_tokens": 400,
		})
		require.NoError(t, err)
		assert.Equal(t, 1000, usage.PromptTokens, "exclusive cache tokens must be added in")
		require.NotNil(t, usage.PromptTokensDetails)
		assert.Equal(t, 400, usage.PromptTokensDetails.CachedReadTokens)
	})
}

// When unpriced usage mixes a missing-model row with a known model, the row must
// NOT be attributed to that known model: the logged usage is a blend, and naming
// one model would let backfill price the orphan tokens as it.
func TestAccountBatchResults_MixedMissingModelIsNotAttributed(t *testing.T) {
	store := newFakeAccountingStore()

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider: schemas.OpenAI,
		BatchID:  "batch_mixed_unpriced",
		Results: []schemas.BatchResultItem{
			openAIResult(200, "", 10, 5),              // missing model: 15 tokens, unattributable
			openAIResult(200, "unknown-model", 20, 8), // known model, no batch rates
		},
		ClaimedBy: "test",
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.False(t, summary.Accounted)

	require.Len(t, store.logs, 1, "the usage must still be logged")
	logged := store.logs[AccountingLogID(schemas.OpenAI, "batch_mixed_unpriced")]
	require.NotNil(t, logged)
	assert.Nil(t, logged.Cost)
	// Both rows' tokens are present, so the row cannot claim to be one model.
	assert.Equal(t, 30, logged.PromptTokens)
	assert.Equal(t, 13, logged.CompletionTokens)
	assert.NotEqual(t, "unknown-model", logged.Model,
		"usage blended with missing-model rows must not be attributed to the one known model")
}

// ClaimedBy becomes the runner id every ownership fence keys on, so two sweepers
// that both omit it must not end up indistinguishable.
func TestNewSweeperDefaultsToDistinctRunnerIDs(t *testing.T) {
	store := newFakeAccountingStore()
	cfg := SweeperConfig{Interval: time.Minute}

	a := NewSweeper(store, store, fakeBatchPricing{}, &fakeBatchResultFetcher{}, nil, nil, cfg)
	b := NewSweeper(store, store, fakeBatchPricing{}, &fakeBatchResultFetcher{}, nil, nil, cfg)

	assert.NotEmpty(t, a.config.ClaimedBy)
	assert.NotEqual(t, a.config.ClaimedBy, b.config.ClaimedBy,
		"two sweepers defaulting ClaimedBy must not share a runner identity")

	// An explicit id is still honored untouched.
	explicit := NewSweeper(store, store, fakeBatchPricing{}, &fakeBatchResultFetcher{}, nil, nil,
		SweeperConfig{Interval: time.Minute, ClaimedBy: "node-7"})
	assert.Equal(t, "node-7", explicit.config.ClaimedBy)
}

// A failed refresh of the persisted job must fail closed: the markers it carries
// are the only record of a partially-settled batch, so settling on top of unknown
// markers could re-report usage that already landed.
func TestAccountBatchResults_PersistedJobReadFailureFailsClosed(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	store.failGetOnce = true

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "read-failure",
		FallbackModel: "gpt-4o-mini",
		Results: []schemas.BatchResultItem{
			openAIResult(200, "gpt-4o-mini", 10, 5),
		},
		UsageReporter: reporter,
		ClaimedBy:     "test",
	})

	require.Error(t, err, "a failed persisted-job read must surface, not be swallowed")
	assert.Nil(t, summary)
	assert.Empty(t, store.logs, "no aggregate log should be written")
	assert.Empty(t, reporter.reports, "no usage should be reported")

	// The claim is released so a later attempt can retry without waiting out the TTL.
	job := store.jobs[cstables.BatchJobID(string(schemas.OpenAI), "read-failure")]
	require.NotNil(t, job)
	assert.Equal(t, cstables.BatchJobAccountingStatusError, job.AccountingStatus)
	assert.Nil(t, job.RunnerID)
}

func TestAccountBatchResults_ParseErrorsNeverPartiallyFinalize(t *testing.T) {
	store := newFakeAccountingStore()
	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
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
	job := store.jobs[cstables.BatchJobID(string(schemas.OpenAI), "malformed-results")]
	require.NotNil(t, job)
	assert.Equal(t, cstables.BatchJobAccountingStatusUnpriceable, job.AccountingStatus)
	assert.Empty(t, store.logs)
}
