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
	// failCompleteAlways models a settlement that keeps failing (a wedged store, a
	// deleted budget), which is what the accounting retry budget has to bound.
	failCompleteAlways bool
	failGetOnce        bool
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

func (s *fakeAccountingStore) FindByID(ctx context.Context, id string) (*logstore.Log, error) {
	entry, ok := s.logs[id]
	if !ok {
		return nil, logstore.ErrNotFound
	}
	copied := *entry
	return &copied, nil
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

func (s *fakeAccountingStore) ClaimBatchJob(ctx context.Context, jobID, runnerID string, staleBefore time.Time, allowUnpriceable bool) (bool, error) {
	entry, ok := s.jobs[jobID]
	if !ok {
		return false, errors.New("missing batch job")
	}
	if entry.AccountingStatus == cstables.BatchJobAccountingStatusAccounted {
		return false, nil
	}
	// "unpriceable" is a stop-polling marker, not a refusal of money: a caller
	// holding real results may re-drive it. Mirrors the real store's allowUnpriceable.
	if entry.AccountingStatus == cstables.BatchJobAccountingStatusUnpriceable && !allowUnpriceable {
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
	if s.failCompleteAlways {
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
	require.NotNil(t, logEntry.BatchDebugParsed)
	require.NotNil(t, logEntry.BatchDebugParsed.Accounting)
	assert.Equal(t, "batch_123", logEntry.BatchDebugParsed.BatchID)
	assert.Nil(t, logEntry.MetadataParsed, "batch detail belongs in batch_debug, not the caller-owned metadata bag")
	require.NotNil(t, logEntry.BatchDebugParsed.RequestCounts)
	assert.Equal(t, schemas.BatchRequestCounts{Total: 3, Completed: 2, Failed: 1}, *logEntry.BatchDebugParsed.RequestCounts)
	breakdown := logEntry.BatchDebugParsed.Accounting.ModelBreakdowns
	require.Contains(t, breakdown, "gpt-4o-mini")
	assert.Equal(t, 1, breakdown["gpt-4o-mini"].RequestCount, "the 500-status gpt-4o-mini result is failed, not priced")
	assert.Equal(t, 18, breakdown["gpt-4o-mini"].Usage.PromptTokens)
	assert.Equal(t, 9, breakdown["gpt-4o-mini"].Usage.CompletionTokens)
	require.NotNil(t, breakdown["gpt-4o-mini"].Cost)
	assert.InDelta(t, 0.00018, *breakdown["gpt-4o-mini"].Cost, 1e-12)
	require.Contains(t, breakdown, "gpt-4o")
	assert.Equal(t, 1, breakdown["gpt-4o"].RequestCount)

	// A repeat call (e.g. a second /results fetch) does not win the claim and
	// writes nothing new, but still mirrors the already-settled price so the
	// caller can display it.
	second, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, req)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.False(t, second.Claimed)
	assert.False(t, second.Accounted)
	assert.Len(t, store.logs, 1)
	assert.Equal(t, summary.Cost, second.Cost)
	assert.True(t, second.Complete)
	assert.Equal(t, summary.Usage, second.Usage)
	assert.Equal(t, summary.ModelBreakdowns, second.ModelBreakdowns)
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

	// The unpriced model still gets a ModelBreakdown entry (Cost nil, Usage set)
	// so a later cost-recalculation pass can reprice it once rates land, instead
	// of its usage only surviving as an anonymous row-level blob.
	require.NotNil(t, logged.BatchDebugParsed)
	require.NotNil(t, logged.BatchDebugParsed.Accounting)
	breakdown, ok := logged.BatchDebugParsed.Accounting.ModelBreakdowns["unknown-model"]
	require.True(t, ok)
	assert.Equal(t, 1, breakdown.RequestCount)
	assert.Equal(t, 18, breakdown.Usage.PromptTokens)
	assert.Equal(t, 9, breakdown.Usage.CompletionTokens)
	assert.Nil(t, breakdown.Cost, "cost must be unknown (nil), not zero, matching the row-level cost")
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
	require.NotNil(t, breakdown.Cost, "provider-supplied cost must still land in the per-model breakdown")
	assert.InDelta(t, 0.123, *breakdown.Cost, 1e-12)
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
	reporter := &fakeUsageReporter{}

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
		UsageReporter: reporter,
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	// One model priced and two rows' tokens did not, so the batch is settled-as-short
	// rather than accounted: closing it out would present a partial total as the bill.
	assert.False(t, summary.Accounted)
	assert.False(t, summary.Complete)
	assert.Empty(t, reporter.reports, "governance must not be told a partial total is the batch's cost")
	assert.Equal(t, 1, summary.PricedCount)
	assert.Equal(t, 2, summary.UnpricedCount)
	assert.Equal(t, 3, summary.FailedCount)
	// Every usage-bearing row is in the row total, including the one whose model the
	// provider never named — that usage has nowhere else to survive.
	assert.Equal(t, 30, summary.Usage.PromptTokens)
	assert.Equal(t, 15, summary.Usage.CompletionTokens)

	// "" has no model at all, so it can never get a breakdown entry — its usage
	// only survives in the aggregate unpriced blob. "unknown-model" is a known
	// model that just failed to price, so it still gets an entry (Cost nil) next
	// to the priced "gpt-4o-mini" one (Cost set) — that's what lets a later
	// recalculation target exactly the models still needing a rate.
	require.Len(t, summary.ModelBreakdowns, 2)
	require.Contains(t, summary.ModelBreakdowns, "gpt-4o-mini")
	require.NotNil(t, summary.ModelBreakdowns["gpt-4o-mini"].Cost)
	assert.InDelta(t, 0.0001, *summary.ModelBreakdowns["gpt-4o-mini"].Cost, 1e-12)
	require.Contains(t, summary.ModelBreakdowns, "unknown-model")
	assert.Nil(t, summary.ModelBreakdowns["unknown-model"].Cost)
	assert.Equal(t, 10, summary.ModelBreakdowns["unknown-model"].Usage.PromptTokens)

	logEntry := store.logs[AccountingLogID(schemas.OpenAI, "batch_partial")]
	require.NotNil(t, logEntry)
	require.NotNil(t, logEntry.BatchDebugParsed)
	require.NotNil(t, logEntry.BatchDebugParsed.Accounting)
	assert.Nil(t, logEntry.Cost, "a partial total must not be persisted as the row's cost")
	assert.True(t, logEntry.BatchDebugParsed.Accounting.Incomplete)
	assert.Equal(t, 30, logEntry.PromptTokens)
	assert.Equal(t, 15, logEntry.CompletionTokens)
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

// An expired batch is not an empty one: the provider bills the requests that
// finished before the window closed, and their rows are still in the output file.
func TestSweeper_ExpiredBatchSettlesTheRowsThatCompleted(t *testing.T) {
	store := newFakeAccountingStore()
	due := time.Now().UTC().Add(-time.Minute)
	jobID := cstables.BatchJobID(string(schemas.OpenAI), "batch_expired")
	require.NoError(t, store.UpsertBatchJob(context.Background(), &cstables.TableBatchJob{
		ID:               jobID,
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_expired",
		Model:            "gpt-4o-mini",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
		NextCheckAt:      &due,
	}))

	fetcher := &fakeBatchResultFetcher{
		retrieveResp: &schemas.BifrostBatchRetrieveResponse{
			ID:     "batch_expired",
			Status: schemas.BatchStatusExpired,
		},
		resultsResp: &schemas.BifrostBatchResultsResponse{
			BatchID: "batch_expired",
			Results: []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 18, 9)},
		},
	}
	sweeper := NewSweeper(store, store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{Limit: 10})

	sweeper.SweepOnce(context.Background())

	assert.Equal(t, 1, fetcher.resultsCalls, "a terminal batch must still be asked for its results")
	require.Len(t, store.logs, 1)
	assert.Equal(t, cstables.BatchJobAccountingStatusAccounted, store.jobs[jobID].AccountingStatus)
}

// With nothing to fetch, the terminal batch ends where it always did.
func TestSweeper_ExpiredBatchWithoutResultsIsTerminalWithoutResults(t *testing.T) {
	store := newFakeAccountingStore()
	due := time.Now().UTC().Add(-time.Minute)
	jobID := cstables.BatchJobID(string(schemas.OpenAI), "batch_expired_empty")
	require.NoError(t, store.UpsertBatchJob(context.Background(), &cstables.TableBatchJob{
		ID:               jobID,
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_expired_empty",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
		NextCheckAt:      &due,
	}))

	fetcher := &fakeBatchResultFetcher{
		retrieveResp: &schemas.BifrostBatchRetrieveResponse{
			ID:     "batch_expired_empty",
			Status: schemas.BatchStatusExpired,
		},
		resultsResp: &schemas.BifrostBatchResultsResponse{BatchID: "batch_expired_empty"},
	}
	sweeper := NewSweeper(store, store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{Limit: 10})

	sweeper.SweepOnce(context.Background())

	assert.Empty(t, store.logs)
	job := store.jobs[jobID]
	require.NotNil(t, job)
	assert.Equal(t, cstables.BatchJobAccountingStatusUnpriceable, job.AccountingStatus)
	require.NotNil(t, job.UnpriceableReason)
	assert.Equal(t, "terminal_without_results", *job.UnpriceableReason)
}

// A deleted batch has nothing left to read, so it must not cost a provider call.
func TestSweeper_DeletedBatchDoesNotFetchResults(t *testing.T) {
	store := newFakeAccountingStore()
	due := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, store.UpsertBatchJob(context.Background(), &cstables.TableBatchJob{
		ID:               cstables.BatchJobID(string(schemas.OpenAI), "batch_deleted"),
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_deleted",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
		NextCheckAt:      &due,
	}))

	fetcher := &fakeBatchResultFetcher{retrieveResp: &schemas.BifrostBatchRetrieveResponse{
		ID:     "batch_deleted",
		Status: schemas.BatchStatusDeleted,
	}}
	sweeper := NewSweeper(store, store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{Limit: 10})

	sweeper.SweepOnce(context.Background())

	assert.Zero(t, fetcher.resultsCalls)
}

// A settlement that keeps failing used to re-download the whole results payload and
// re-fail every sweep, forever: FailBatchJob left next_check_at in the past and never
// advanced poll_attempts. It has to consume the same retry budget as any other failure.
func TestSweeper_ReschedulesAfterSettlementFailure(t *testing.T) {
	store := newFakeAccountingStore()
	store.failCompleteAlways = true
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	jobID := cstables.BatchJobID(string(schemas.OpenAI), "batch_settle_fail")
	job := &cstables.TableBatchJob{
		ID:               jobID,
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_settle_fail",
		Model:            "gpt-4o-mini",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
		PollAttempts:     9,
		NextCheckAt:      &now,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{
		retrieveResp: &schemas.BifrostBatchRetrieveResponse{
			ID:     "batch_settle_fail",
			Status: schemas.BatchStatusCompleted,
		},
		resultsResp: &schemas.BifrostBatchResultsResponse{
			BatchID: "batch_settle_fail",
			Results: []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 18, 9)},
		},
	}
	sweeper := NewSweeper(store, store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{
		Interval: time.Minute,
		Limit:    10,
	})

	sweeper.sweepJob(context.Background(), job, now)

	updated := store.jobs[jobID]
	require.NotNil(t, updated)
	assert.Equal(t, 10, updated.PollAttempts, "an accounting failure must consume an attempt")
	require.NotNil(t, updated.NextCheckAt)
	assert.True(t, updated.NextCheckAt.After(now), "next_check_at must move forward, not stay past-due")
}

// The attempt cap is what eventually stops a settlement that can never succeed —
// and it parks the job somewhere a later /results call can still re-drive it.
func TestSweeper_SettlementFailureHitsPollAttemptCap(t *testing.T) {
	store := newFakeAccountingStore()
	store.failCompleteAlways = true
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	jobID := cstables.BatchJobID(string(schemas.OpenAI), "batch_settle_cap")
	job := &cstables.TableBatchJob{
		ID:               jobID,
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_settle_cap",
		Model:            "gpt-4o-mini",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
		PollAttempts:     maxPollAttempts - 1,
		NextCheckAt:      &now,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{
		retrieveResp: &schemas.BifrostBatchRetrieveResponse{
			ID:     "batch_settle_cap",
			Status: schemas.BatchStatusCompleted,
		},
		resultsResp: &schemas.BifrostBatchResultsResponse{
			BatchID: "batch_settle_cap",
			Results: []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 18, 9)},
		},
	}
	sweeper := NewSweeper(store, store, fakeBatchPricing{}, fetcher, nil, nil, SweeperConfig{
		Interval: time.Minute,
		Limit:    10,
	})

	sweeper.sweepJob(context.Background(), job, now)

	updated := store.jobs[jobID]
	require.NotNil(t, updated)
	assert.Equal(t, cstables.BatchJobAccountingStatusUnpriceable, updated.AccountingStatus)
	require.NotNil(t, updated.UnpriceableReason)
	assert.Equal(t, UnpriceableReasonMaxPollAttempts, *updated.UnpriceableReason)
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

	// The row's Object is "batch_results" for every batch, so the endpoint is the
	// only thing that lets a later repricing pass reach these same embedding rates.
	logged := store.logs[AccountingLogID(schemas.OpenAI, "embedding-batch")]
	require.NotNil(t, logged)
	require.NotNil(t, logged.BatchDebugParsed)
	assert.Equal(t, string(schemas.BatchEndpointEmbeddings), logged.BatchDebugParsed.Endpoint)
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

// A malformed row costs the batch that row, not every other row. The raw provider
// results are not persisted anywhere else, so abandoning the batch on the first bad
// JSONL line lost the parsed rows' money permanently.
func TestAccountBatchResults_ParseErrorsStillPriceTheParsedRows(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "malformed-results",
		FallbackModel: "gpt-4o-mini",
		Results: []schemas.BatchResultItem{
			openAIResult(200, "gpt-4o-mini", 10, 5),
			openAIResult(200, "gpt-4o-mini", 20, 4),
		},
		ParseErrors:   []schemas.BatchError{{Code: "parse_error", Message: "invalid JSONL"}},
		UsageReporter: reporter,
		ClaimedBy:     "test",
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	assert.Equal(t, 2, summary.PricedCount)
	assert.Equal(t, 30, summary.Usage.PromptTokens)
	assert.Equal(t, 9, summary.Usage.CompletionTokens)

	job := store.jobs[cstables.BatchJobID(string(schemas.OpenAI), "malformed-results")]
	require.NotNil(t, job)
	assert.Equal(t, cstables.BatchJobAccountingStatusAccounted, job.AccountingStatus)

	logged := store.logs[AccountingLogID(schemas.OpenAI, "malformed-results")]
	require.NotNil(t, logged)
	require.NotNil(t, logged.Cost)
	assert.InDelta(t, 0.00024, *logged.Cost, 1e-12)
	require.NotNil(t, logged.BatchDebugParsed)
	require.NotNil(t, logged.BatchDebugParsed.Accounting)
	// The count is the record of what the row omits; the marker says the total is
	// short. Neither is recoverable — the unparsed rows are gone — so both are kept.
	assert.Equal(t, 1, logged.BatchDebugParsed.Accounting.ParseErrorCount)
	assert.True(t, logged.BatchDebugParsed.Accounting.Incomplete)

	require.Len(t, reporter.reports, 1, "priced rows must still be billed")
	assert.InDelta(t, 0.00024, reporter.reports[0].Cost, 1e-12)
}

// When the parse errors are the only thing that went wrong — nothing parsed at all —
// they stay the reason the batch is unpriceable.
func TestAccountBatchResults_AllRowsUnparseableMarksUnpriceable(t *testing.T) {
	store := newFakeAccountingStore()
	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "all-malformed",
		FallbackModel: "gpt-4o-mini",
		ParseErrors: []schemas.BatchError{
			{Code: "parse_error", Message: "invalid JSONL"},
			{Code: "parse_error", Message: "invalid JSONL"},
		},
		ClaimedBy: "test",
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.False(t, summary.Accounted)
	assert.Equal(t, UnpriceableReasonResultParseErrors, summary.UnpriceableReason)
	job := store.jobs[cstables.BatchJobID(string(schemas.OpenAI), "all-malformed")]
	require.NotNil(t, job)
	assert.Equal(t, cstables.BatchJobAccountingStatusUnpriceable, job.AccountingStatus)
	require.NotNil(t, job.UnpriceableReason)
	assert.Equal(t, UnpriceableReasonResultParseErrors, *job.UnpriceableReason)
	assert.Empty(t, store.logs)
}

// Two models, one of them absent from the catalog: the row must carry the whole
// batch's usage with an unknown cost, so the missing-cost backfill can still select
// it, and governance must not be handed the half of the bill that did price.
func TestAccountBatchResults_PartiallyPricedBatchKeepsCostUnknown(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider: schemas.OpenAI,
		BatchID:  "batch_half_priced",
		Results: []schemas.BatchResultItem{
			openAIResult(200, "gpt-4o-mini", 10, 5),
			openAIResult(200, "unknown-model", 20, 8),
		},
		UsageReporter: reporter,
		ClaimedBy:     "test",
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.False(t, summary.Accounted)
	assert.False(t, summary.Complete)
	assert.Equal(t, UnpriceableReasonMissingBatchPricing, summary.UnpriceableReason)
	assert.Empty(t, reporter.reports)

	logged := store.logs[AccountingLogID(schemas.OpenAI, "batch_half_priced")]
	require.NotNil(t, logged)
	assert.Nil(t, logged.Cost, "cost must be unknown, not the priced half")
	assert.Equal(t, 30, logged.PromptTokens, "the unpriced model's tokens belong in the row total")
	assert.Equal(t, 13, logged.CompletionTokens)
	require.NotNil(t, logged.BatchDebugParsed.Accounting)
	assert.True(t, logged.BatchDebugParsed.Accounting.Incomplete)

	// What did price is still recorded per model, so a recalculation only has to
	// find the rate that was missing.
	breakdowns := logged.BatchDebugParsed.Accounting.ModelBreakdowns
	require.NotNil(t, breakdowns["gpt-4o-mini"].Cost)
	assert.InDelta(t, 0.0001, *breakdowns["gpt-4o-mini"].Cost, 1e-12)
	assert.Nil(t, breakdowns["unknown-model"].Cost)

	// Parked, not closed: the job stops being polled but stays re-drivable.
	job := store.jobs[cstables.BatchJobID(string(schemas.OpenAI), "batch_half_priced")]
	require.NotNil(t, job)
	assert.Equal(t, cstables.BatchJobAccountingStatusUnpriceable, job.AccountingStatus)
	require.NotNil(t, job.UnpriceableReason)
	assert.Equal(t, UnpriceableReasonMissingBatchPricing, *job.UnpriceableReason)
}

// The batch's create-time attribution is the one identity that does not depend on
// who happens to settle first — a /results call by a different key must not move
// the bill onto that key's budgets.
func TestAccountBatchResults_PrefersBatchJobAttributionOverFetcher(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	creatorVK := "vk-creator"
	creatorBudgets := `["budget-creator"]`
	fetcherVK := "vk-fetcher"

	job := &cstables.TableBatchJob{
		ID:               cstables.BatchJobID(string(schemas.OpenAI), "batch_attribution"),
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_attribution",
		Model:            "gpt-4o-mini",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
		SelectedKeyID:    "key-creator",
		VirtualKeyID:     &creatorVK,
		BudgetIDs:        &creatorBudgets,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "batch_attribution",
		FallbackModel: "gpt-4o-mini",
		BatchJob:      job,
		BaseLog: &logstore.Log{
			ID:              "results-request",
			SelectedKeyID:   "key-fetcher",
			SelectedKeyName: "fetcher key",
			VirtualKeyID:    &fetcherVK,
			VirtualKeyName:  strPtr("fetcher vk"),
			TeamID:          strPtr("team-fetcher"),
			BudgetIDsParsed: []string{"budget-fetcher"},
		},
		Results:       []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 10, 5)},
		UsageReporter: reporter,
		ClaimedBy:     "test",
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)

	logged := store.logs[AccountingLogID(schemas.OpenAI, "batch_attribution")]
	require.NotNil(t, logged)
	assert.Equal(t, "key-creator", logged.SelectedKeyID)
	require.NotNil(t, logged.VirtualKeyID)
	assert.Equal(t, creatorVK, *logged.VirtualKeyID)
	require.NotNil(t, logged.BudgetIDs)
	assert.Equal(t, creatorBudgets, *logged.BudgetIDs)
	assert.Empty(t, logged.BudgetIDsParsed, "the fetcher's budgets must not ride along")
	// A mismatched virtual key means the fetcher's names describe a different
	// identity, so they are left off rather than mixed into the creator's row.
	assert.Nil(t, logged.VirtualKeyName)
	assert.Nil(t, logged.TeamID)
	// The triggering request is still recorded as provenance.
	require.NotNil(t, logged.ParentRequestID)
	assert.Equal(t, "results-request", *logged.ParentRequestID)

	require.Len(t, reporter.reports, 1)
	assert.Equal(t, []string{"budget-creator"}, reporter.reports[0].BudgetIDs)
}

// The denormalized names come from the log of the request that CREATED the batch,
// not from whoever settles it.
func TestAccountBatchResults_FillsNamesFromSourceLog(t *testing.T) {
	store := newFakeAccountingStore()
	sharedVK := "vk-shared"
	sourceLogID := "create-request"

	store.logs[sourceLogID] = &logstore.Log{
		ID:             sourceLogID,
		VirtualKeyID:   &sharedVK,
		VirtualKeyName: strPtr("shared vk"),
		UserID:         strPtr("user-creator"),
		UserName:       strPtr("Creator"),
		TeamID:         strPtr("team-1"),
		TeamName:       strPtr("Team One"),
	}

	job := &cstables.TableBatchJob{
		ID:               cstables.BatchJobID(string(schemas.OpenAI), "batch_same_vk"),
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_same_vk",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
		SelectedKeyID:    "key-1",
		VirtualKeyID:     &sharedVK,
		UserID:           strPtr("user-creator"),
		TeamID:           strPtr("team-1"),
		SourceLogID:      &sourceLogID,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	_, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "batch_same_vk",
		FallbackModel: "gpt-4o-mini",
		BatchJob:      job,
		BaseLog: &logstore.Log{
			ID:           "results-request",
			VirtualKeyID: &sharedVK,
		},
		Results:   []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 10, 5)},
		ClaimedBy: "test",
	})
	require.NoError(t, err)

	logged := store.logs[AccountingLogID(schemas.OpenAI, "batch_same_vk")]
	require.NotNil(t, logged)
	require.NotNil(t, logged.VirtualKeyName)
	assert.Equal(t, "shared vk", *logged.VirtualKeyName)
	require.NotNil(t, logged.UserID)
	assert.Equal(t, "user-creator", *logged.UserID)
	require.NotNil(t, logged.UserName)
	assert.Equal(t, "Creator", *logged.UserName)
	require.NotNil(t, logged.TeamID)
	assert.Equal(t, "team-1", *logged.TeamID)
}

// An access profile hands one virtual key to many users, so matching on the key
// cannot tell two people apart. The settling user's identity must not land on the
// creator's cost row even though both requests carry the same virtual key.
func TestAccountBatchResults_SharedVirtualKeyKeepsUsersApart(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	sharedVK := "vk-access-profile"
	sourceLogID := "create-request"
	creatorBudgets := `["budget-creator"]`

	store.logs[sourceLogID] = &logstore.Log{
		ID:             sourceLogID,
		VirtualKeyID:   &sharedVK,
		VirtualKeyName: strPtr("access profile key"),
		UserID:         strPtr("user-alice"),
		UserName:       strPtr("Alice"),
	}

	job := &cstables.TableBatchJob{
		ID:               cstables.BatchJobID(string(schemas.OpenAI), "batch_shared_vk"),
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_shared_vk",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
		SelectedKeyID:    "key-1",
		VirtualKeyID:     &sharedVK,
		UserID:           strPtr("user-alice"),
		BudgetIDs:        &creatorBudgets,
		SourceLogID:      &sourceLogID,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	// Bob fetches Alice's results. Same access-profile virtual key, different person.
	_, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "batch_shared_vk",
		FallbackModel: "gpt-4o-mini",
		BatchJob:      job,
		BaseLog: &logstore.Log{
			ID:             "results-request",
			VirtualKeyID:   &sharedVK,
			VirtualKeyName: strPtr("access profile key"),
			UserID:         strPtr("user-bob"),
			UserName:       strPtr("Bob"),
		},
		Results:       []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 10, 5)},
		UsageReporter: reporter,
		ClaimedBy:     "test",
	})
	require.NoError(t, err)

	logged := store.logs[AccountingLogID(schemas.OpenAI, "batch_shared_vk")]
	require.NotNil(t, logged)
	require.NotNil(t, logged.UserID)
	assert.Equal(t, "user-alice", *logged.UserID, "the batch is billed to whoever created it")
	require.NotNil(t, logged.UserName)
	assert.Equal(t, "Alice", *logged.UserName)
	require.NotNil(t, logged.ParentRequestID)
	assert.Equal(t, "results-request", *logged.ParentRequestID)

	require.Len(t, reporter.reports, 1)
	assert.Equal(t, "user-alice", reporter.reports[0].UserID)
	assert.Equal(t, []string{"budget-creator"}, reporter.reports[0].BudgetIDs)
}

// The sweeper settles with no request context at all: every identity on the cost
// row has to come off the batch job and the creating request's log.
func TestAccountBatchResults_SweeperPathKeepsUserAttribution(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	vk := "vk-access-profile"
	sourceLogID := "create-request"

	store.logs[sourceLogID] = &logstore.Log{
		ID:           sourceLogID,
		VirtualKeyID: &vk,
		UserID:       strPtr("user-alice"),
		UserName:     strPtr("Alice"),
		TeamID:       strPtr("team-1"),
		TeamName:     strPtr("Team One"),
	}

	job := &cstables.TableBatchJob{
		ID:               cstables.BatchJobID(string(schemas.OpenAI), "batch_sweeper"),
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_sweeper",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
		SelectedKeyID:    "key-1",
		VirtualKeyID:     &vk,
		UserID:           strPtr("user-alice"),
		TeamID:           strPtr("team-1"),
		SourceLogID:      &sourceLogID,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	// No BaseLog: this is the sweeper, hours after the request that created the batch.
	_, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "batch_sweeper",
		FallbackModel: "gpt-4o-mini",
		BatchJob:      job,
		Results:       []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 10, 5)},
		UsageReporter: reporter,
		ClaimedBy:     "test",
	})
	require.NoError(t, err)

	logged := store.logs[AccountingLogID(schemas.OpenAI, "batch_sweeper")]
	require.NotNil(t, logged)
	require.NotNil(t, logged.UserID)
	assert.Equal(t, "user-alice", *logged.UserID)
	require.NotNil(t, logged.UserName)
	assert.Equal(t, "Alice", *logged.UserName)
	require.NotNil(t, logged.TeamID)
	assert.Equal(t, "team-1", *logged.TeamID)
	assert.Nil(t, logged.ParentRequestID, "nothing triggered this settlement")

	require.Len(t, reporter.reports, 1)
	assert.Equal(t, "user-alice", reporter.reports[0].UserID, "the user must be billable without a request context")
}

// A batch created outside Bifrost has no create-time attribution to prefer, so the
// /results caller's remains the only identity available — unchanged behavior.
func TestAccountBatchResults_WithoutJobAttributionUsesBaseLog(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	fetcherVK := "vk-fetcher"

	_, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "batch_external",
		FallbackModel: "gpt-4o-mini",
		BaseLog: &logstore.Log{
			ID:              "results-request",
			SelectedKeyID:   "key-fetcher",
			SelectedKeyName: "fetcher key",
			VirtualKeyID:    &fetcherVK,
			BudgetIDsParsed: []string{"budget-fetcher"},
		},
		Results:       []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 10, 5)},
		UsageReporter: reporter,
		ClaimedBy:     "test",
	})
	require.NoError(t, err)

	logged := store.logs[AccountingLogID(schemas.OpenAI, "batch_external")]
	require.NotNil(t, logged)
	assert.Equal(t, "key-fetcher", logged.SelectedKeyID)
	assert.Equal(t, "fetcher key", logged.SelectedKeyName)
	require.NotNil(t, logged.VirtualKeyID)
	assert.Equal(t, fetcherVK, *logged.VirtualKeyID)
	require.Len(t, reporter.reports, 1)
	assert.Equal(t, []string{"budget-fetcher"}, reporter.reports[0].BudgetIDs)
}

// "unpriceable" means stop polling, not refuse money. A caller arriving with real
// results answers every reason the job reached that state, so it must be able to
// re-drive it — otherwise a slow batch that outran the poll budget loses its bill
// permanently.
func TestAccountBatchResults_ReclaimsUnpriceableJobWhenResultsArrive(t *testing.T) {
	store := newFakeAccountingStore()
	jobID := cstables.BatchJobID(string(schemas.OpenAI), "batch_reclaim")
	reason := UnpriceableReasonMaxPollAttempts
	require.NoError(t, store.UpsertBatchJob(context.Background(), &cstables.TableBatchJob{
		ID:                jobID,
		Provider:          string(schemas.OpenAI),
		BatchID:           "batch_reclaim",
		Model:             "gpt-4o-mini",
		AccountingStatus:  cstables.BatchJobAccountingStatusUnpriceable,
		UnpriceableReason: &reason,
	}))

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "batch_reclaim",
		FallbackModel: "gpt-4o-mini",
		Results:       []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 10, 5)},
		ClaimedBy:     "test",
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Claimed)
	assert.True(t, summary.Accounted)
	assert.Equal(t, cstables.BatchJobAccountingStatusAccounted, store.jobs[jobID].AccountingStatus)
	assert.Len(t, store.logs, 1)
}

// A losing claim against a job another runner is mid-settling (fresh claimed_at,
// no aggregate row written yet) has nothing to mirror. The summary must come back
// zero-valued rather than erroring, so a caller displays "not priced yet".
func TestAccountBatchResults_LosingClaimWithNoExistingLogMirrorsNothing(t *testing.T) {
	store := newFakeAccountingStore()
	jobID := cstables.BatchJobID(string(schemas.OpenAI), "batch_in_flight")
	now := time.Now().UTC()
	store.jobs[jobID] = &cstables.TableBatchJob{
		ID:               jobID,
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_in_flight",
		Model:            "gpt-4o-mini",
		AccountingStatus: cstables.BatchJobAccountingStatusProcessing,
		RunnerID:         schemas.Ptr("other-node"),
		ClaimedAt:        &now,
	}

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "batch_in_flight",
		FallbackModel: "gpt-4o-mini",
		Results:       []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 10, 5)},
		ClaimedBy:     "test",
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.False(t, summary.Claimed)
	assert.Zero(t, summary.Cost)
	assert.False(t, summary.Complete)
	assert.Nil(t, summary.ModelBreakdowns)
	assert.Empty(t, store.logs)
}

// The provider's batch lifecycle status flows onto the aggregate row at
// settlement and is mirrored (not re-derived) on a repeat call that loses the
// claim — the display must reflect what settlement actually observed, not
// silently relabel it "completed" just because it settled.
func TestAccountBatchResults_StatusPersistsAndMirrorsOnRepeatCall(t *testing.T) {
	store := newFakeAccountingStore()
	req := Request{
		Provider:      schemas.OpenAI,
		BatchID:       "batch_status",
		FallbackModel: "gpt-4o-mini",
		BatchJob: &cstables.TableBatchJob{
			Provider:       string(schemas.OpenAI),
			BatchID:        "batch_status",
			Model:          "gpt-4o-mini",
			ProviderStatus: string(schemas.BatchStatusEnded),
		},
		Results:   []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 10, 5)},
		ClaimedBy: "test",
	}

	summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, req)
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.True(t, summary.Accounted)
	assert.Equal(t, string(schemas.BatchStatusEnded), summary.Status)

	logged := store.logs[AccountingLogID(schemas.OpenAI, "batch_status")]
	require.NotNil(t, logged)
	require.NotNil(t, logged.BatchDebugParsed)
	assert.Equal(t, string(schemas.BatchStatusEnded), logged.BatchDebugParsed.Status)

	// A repeat call loses the claim; it must mirror the persisted status rather
	// than re-deriving it from this call's own (possibly different) BatchJob hint.
	second, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, req)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.False(t, second.Claimed)
	assert.Equal(t, string(schemas.BatchStatusEnded), second.Status)
}

// Without results there is nothing new to say about an unpriceable job, and an
// accounted one is closed forever either way.
func TestAccountBatchResults_TerminalJobsStayClosedWithoutResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
	}{
		{"unpriceable without results", cstables.BatchJobAccountingStatusUnpriceable},
		{"accounted", cstables.BatchJobAccountingStatusAccounted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeAccountingStore()
			jobID := cstables.BatchJobID(string(schemas.OpenAI), "batch_closed")
			require.NoError(t, store.UpsertBatchJob(context.Background(), &cstables.TableBatchJob{
				ID:               jobID,
				Provider:         string(schemas.OpenAI),
				BatchID:          "batch_closed",
				AccountingStatus: tc.status,
			}))

			results := []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 10, 5)}
			if tc.status == cstables.BatchJobAccountingStatusUnpriceable {
				results = nil
			}
			summary, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
				Provider:  schemas.OpenAI,
				BatchID:   "batch_closed",
				Results:   results,
				ClaimedBy: "test",
			})
			require.NoError(t, err)
			require.NotNil(t, summary)
			assert.False(t, summary.Claimed)
			assert.Empty(t, store.logs)
		})
	}
}

func strPtr(v string) *string {
	return &v
}

// Production hands AccountBatchResults a job built from the settling request's own
// log entry, so that job arrives already populated with the fetcher's key, virtual
// key and budgets. Merging the persisted row into it must overwrite those, not fill
// only the gaps — otherwise nothing of the creator's identity ever survives.
func TestAccountBatchResults_PersistedIdentityOverridesFetcherBuiltJob(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	creatorVK := "vk-creator"
	creatorBudgets := `["budget-creator"]`
	fetcherVK := "vk-fetcher"
	fetcherBudgets := `["budget-fetcher"]`
	jobID := cstables.BatchJobID(string(schemas.OpenAI), "batch_merge")

	require.NoError(t, store.UpsertBatchJob(context.Background(), &cstables.TableBatchJob{
		ID:               jobID,
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_merge",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
		SelectedKeyID:    "key-creator",
		VirtualKeyID:     &creatorVK,
		UserID:           strPtr("user-creator"),
		BudgetIDs:        &creatorBudgets,
	}))

	// What plugins/logging builds on the /results path: a job derived from the
	// fetcher's log entry, carrying the fetcher's identity end to end.
	fetcherBuiltJob := &cstables.TableBatchJob{
		ID:               jobID,
		Provider:         string(schemas.OpenAI),
		BatchID:          "batch_merge",
		AccountingStatus: cstables.BatchJobAccountingStatusPending,
		SelectedKeyID:    "key-fetcher",
		VirtualKeyID:     &fetcherVK,
		UserID:           strPtr("user-fetcher"),
		BudgetIDs:        &fetcherBudgets,
	}

	_, err := AccountBatchResults(context.Background(), store, store, fakeBatchPricing{}, Request{
		Provider:      schemas.OpenAI,
		BatchID:       "batch_merge",
		FallbackModel: "gpt-4o-mini",
		BatchJob:      fetcherBuiltJob,
		Results:       []schemas.BatchResultItem{openAIResult(200, "gpt-4o-mini", 10, 5)},
		UsageReporter: reporter,
		ClaimedBy:     "test",
	})
	require.NoError(t, err)

	logged := store.logs[AccountingLogID(schemas.OpenAI, "batch_merge")]
	require.NotNil(t, logged)
	assert.Equal(t, "key-creator", logged.SelectedKeyID)
	require.NotNil(t, logged.VirtualKeyID)
	assert.Equal(t, creatorVK, *logged.VirtualKeyID)
	require.NotNil(t, logged.UserID)
	assert.Equal(t, "user-creator", *logged.UserID)

	require.Len(t, reporter.reports, 1)
	assert.Equal(t, []string{"budget-creator"}, reporter.reports[0].BudgetIDs)
	assert.Equal(t, "user-creator", reporter.reports[0].UserID)
}
