package jobaccounting

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadPricingFromFile(t *testing.T) *modelcatalog.ModelCatalog {
	t.Helper()
	abs, err := filepath.Abs("testdata/pricing.json")
	require.NoError(t, err, "resolve testdata/pricing.json path")
	ds := datasheet.New(nil, bifrost.NewNoOpLogger(), datasheet.Config{URL: "file://" + abs})
	require.NoError(t, ds.LoadFromURLIntoMemory(context.Background()), "failed to load pricing datasheet")
	return modelcatalog.NewTestCatalogWithDatasheet(ds)
}

func assertSweeperAccountsBatchWithPricing(t *testing.T, mc *modelcatalog.ModelCatalog, job *cstables.TableProviderJob, results []schemas.BatchResultItem, expectedCost float64) {
	t.Helper()
	store := newFakeAccountingStore()
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{
		retrieveResp: &schemas.BifrostBatchRetrieveResponse{
			ID:     job.JobID,
			Status: schemas.BatchStatusCompleted,
		},
		resultsResp: &schemas.BifrostBatchResultsResponse{
			BatchID: job.JobID,
			Results: results,
		},
	}
	emitter := &fakeAggregateLogWriter{}
	reporter := &fakeUsageReporter{}

	kv := &fakeKVStore{setNXAllowed: true}
	sweeper := NewBatchSweeper(store, store, mc, fetcher, emitter, reporter, SweeperConfig{
		Interval: time.Minute,
		Limit:    10,
		KVStore:  kv,
	})

	sweeper.SweepOnce(context.Background())

	accounted := store.jobs[job.ID]
	require.NotNil(t, accounted, "batch job should exist in store")
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, accounted.AccountingStatus,
		"batch job should be accounted, got status=%s", accounted.AccountingStatus)

	assert.Len(t, store.logs, 1, "one aggregate log should be written")
	logEntry := store.logs[AccountingLogID(ProviderJobKindBatch, schemas.ModelProvider(job.Provider), job.JobID)]
	require.NotNil(t, logEntry, "aggregate log entry should exist")
	require.NotNil(t, logEntry.Cost, "log entry should have a cost")
	assert.InDelta(t, expectedCost, *logEntry.Cost, 1e-12,
		"cost mismatch for %s/%s: expected %.12f got %.12f",
		job.Provider, job.Model, expectedCost, *logEntry.Cost)

	assert.Len(t, emitter.emitted, 1, "one emit should fire")
	require.Len(t, reporter.reports, 1, "one governance report should fire")
	assert.Equal(t, *logEntry.Cost, reporter.reports[0].Cost, "governance cost should match log cost")
}

func TestBatchPricing_ClaudeSonnet5_Anthropic(t *testing.T) {
	mc := loadPricingFromFile(t)
	now := time.Now().UTC().Add(-time.Minute)
	job := &cstables.TableProviderJob{
		ID:               cstables.ProviderJobID(cstables.ProviderJobKindBatch, string(schemas.Anthropic), "batch_claude_sonnet_5"),
		Provider:         string(schemas.Anthropic),
		JobID:            "batch_claude_sonnet_5",
		Model:            "claude-sonnet-5",
		AccountingStatus: cstables.ProviderJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	results := []schemas.BatchResultItem{
		anthropicResult("claude-sonnet-5", 500, 0, 0, 200),
	}
	assertSweeperAccountsBatchWithPricing(t, mc, job, results, 500*1.5e-06+200*7.5e-06)
}

func TestBatchPricing_Gemini25Flash(t *testing.T) {
	mc := loadPricingFromFile(t)
	now := time.Now().UTC().Add(-time.Minute)
	job := &cstables.TableProviderJob{
		ID:               cstables.ProviderJobID(cstables.ProviderJobKindBatch, string(schemas.Gemini), "batch_gemini_25_flash"),
		Provider:         string(schemas.Gemini),
		JobID:            "batch_gemini_25_flash",
		Model:            "gemini-2.5-flash",
		AccountingStatus: cstables.ProviderJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	results := []schemas.BatchResultItem{
		geminiResult(300, 150),
	}
	assertSweeperAccountsBatchWithPricing(t, mc, job, results, 300*1.5e-07+150*1.25e-06)
}

func TestBatchPricing_ClaudeSonnet46_Bedrock(t *testing.T) {
	mc := loadPricingFromFile(t)
	now := time.Now().UTC().Add(-time.Minute)
	job := &cstables.TableProviderJob{
		ID:               cstables.ProviderJobID(cstables.ProviderJobKindBatch, string(schemas.Bedrock), "batch_claude_sonnet_46"),
		Provider:         string(schemas.Bedrock),
		JobID:            "batch_claude_sonnet_46",
		Model:            "anthropic.claude-sonnet-4-6",
		AccountingStatus: cstables.ProviderJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	results := []schemas.BatchResultItem{
		bedrockResult("anthropic.claude-sonnet-4-6", 400, 100),
	}
	assertSweeperAccountsBatchWithPricing(t, mc, job, results, 400*1.5e-06+100*7.5e-06)
}

func TestBatchPricing_GlobalClaudeSonnet46_Bedrock(t *testing.T) {
	mc := loadPricingFromFile(t)
	now := time.Now().UTC().Add(-time.Minute)
	job := &cstables.TableProviderJob{
		ID:               cstables.ProviderJobID(cstables.ProviderJobKindBatch, string(schemas.Bedrock), "batch_global_claude_sonnet_46"),
		Provider:         string(schemas.Bedrock),
		JobID:            "batch_global_claude_sonnet_46",
		Model:            "global.anthropic.claude-sonnet-4-6",
		AccountingStatus: cstables.ProviderJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	results := []schemas.BatchResultItem{
		bedrockResult("global.anthropic.claude-sonnet-4-6", 250, 75),
	}
	assertSweeperAccountsBatchWithPricing(t, mc, job, results, 250*1.5e-06+75*7.5e-06)
}

// TestBatchPricing_ClaudeHaiku45_DefaultsToHalfStandardRate covers a model
// with standard chat pricing (input_cost_per_token: 8e-07, output: 4e-06 in
// testdata/pricing.json) but no batch-specific rate. Rather than going
// unpriceable, it prices at defaultBatchPricingRatio (0.5) of the standard
// rate: 100*4e-07 + 50*2e-06 = 0.00014.
func TestBatchPricing_ClaudeHaiku45_DefaultsToHalfStandardRate(t *testing.T) {
	mc := loadPricingFromFile(t)
	now := time.Now().UTC().Add(-time.Minute)
	job := &cstables.TableProviderJob{
		ID:               cstables.ProviderJobID(cstables.ProviderJobKindBatch, string(schemas.Anthropic), "batch_haiku_45"),
		Provider:         string(schemas.Anthropic),
		JobID:            "batch_haiku_45",
		Model:            "claude-haiku-4-5",
		AccountingStatus: cstables.ProviderJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	results := []schemas.BatchResultItem{
		anthropicResult("claude-haiku-4-5", 100, 0, 0, 50),
	}
	assertSweeperAccountsBatchWithPricing(t, mc, job, results, 100*4e-07+50*2e-06)
}

// TestBatchPricing_UnknownModel_StillUnpriceable covers a model with no
// pricing at all — not even a standard rate to default 50% of — which must
// still go unpriceable rather than silently pricing at zero.
func TestBatchPricing_UnknownModel_StillUnpriceable(t *testing.T) {
	mc := loadPricingFromFile(t)
	now := time.Now().UTC().Add(-time.Minute)
	job := &cstables.TableProviderJob{
		ID:               cstables.ProviderJobID(cstables.ProviderJobKindBatch, string(schemas.Anthropic), "batch_unknown_model"),
		Provider:         string(schemas.Anthropic),
		JobID:            "batch_unknown_model",
		Model:            "totally-unknown-model-xyz",
		AccountingStatus: cstables.ProviderJobAccountingStatusPending,
		NextCheckAt:      &now,
	}

	store := newFakeAccountingStore()
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{
		retrieveResp: &schemas.BifrostBatchRetrieveResponse{
			ID:     "batch_unknown_model",
			Status: schemas.BatchStatusCompleted,
		},
		resultsResp: &schemas.BifrostBatchResultsResponse{
			BatchID: "batch_unknown_model",
			Results: []schemas.BatchResultItem{
				anthropicResult("totally-unknown-model-xyz", 100, 0, 0, 50),
			},
		},
	}

	kv := &fakeKVStore{setNXAllowed: true}
	sweeper := NewBatchSweeper(store, store, mc, fetcher, nil, nil, SweeperConfig{
		Interval: time.Minute,
		Limit:    10,
		KVStore:  kv,
	})

	sweeper.SweepOnce(context.Background())

	unpriced := store.jobs[job.ID]
	require.NotNil(t, unpriced)
	assert.Equal(t, cstables.ProviderJobAccountingStatusUnpriceable, unpriced.AccountingStatus,
		"batch with no pricing at all should be marked unpriceable")
	require.NotNil(t, unpriced.UnpriceableReason)
	assert.Equal(t, UnpriceableReasonMissingBatchPricing, *unpriced.UnpriceableReason,
		"reason should be missing_batch_pricing")

	// The batch really ran and consumed tokens; we just have no pricing for the
	// model at all. The row is still written with an unknown cost so the usage is
	// not lost and the missing-cost backfill can price it once rates land — the
	// same treatment a synchronous call gets.
	require.Len(t, store.logs, 1, "an unpriceable batch with real usage must still be logged")
	var logged *logstore.Log
	for _, entry := range store.logs {
		logged = entry
	}
	require.NotNil(t, logged)
	assert.Nil(t, logged.Cost, "cost must be unknown (nil), not zero")
	assert.Equal(t, "totally-unknown-model-xyz", logged.Model,
		"row must stay attributable to its model so backfill can resolve it")
	assert.Equal(t, 100, logged.PromptTokens, "usage must be preserved for later recalculation")
	assert.Equal(t, 50, logged.CompletionTokens)
	assert.Equal(t, 150, logged.TotalTokens)
}

func TestBatchPricing_MultiModel(t *testing.T) {
	mc := loadPricingFromFile(t)
	now := time.Now().UTC().Add(-time.Minute)
	job := &cstables.TableProviderJob{
		ID:               cstables.ProviderJobID(cstables.ProviderJobKindBatch, string(schemas.Bedrock), "batch_multimodel"),
		Provider:         string(schemas.Bedrock),
		JobID:            "batch_multimodel",
		Model:            "anthropic.claude-sonnet-4-6",
		AccountingStatus: cstables.ProviderJobAccountingStatusPending,
		NextCheckAt:      &now,
	}

	store := newFakeAccountingStore()
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{
		retrieveResp: &schemas.BifrostBatchRetrieveResponse{
			ID:     "batch_multimodel",
			Status: schemas.BatchStatusCompleted,
		},
		resultsResp: &schemas.BifrostBatchResultsResponse{
			BatchID: "batch_multimodel",
			Results: []schemas.BatchResultItem{
				bedrockResult("anthropic.claude-sonnet-4-6", 200, 50),
				bedrockResult("global.anthropic.claude-sonnet-4-6", 150, 30),
			},
		},
	}

	kv := &fakeKVStore{setNXAllowed: true}
	sweeper := NewBatchSweeper(store, store, mc, fetcher, nil, nil, SweeperConfig{
		Interval: time.Minute,
		Limit:    10,
		KVStore:  kv,
	})

	sweeper.SweepOnce(context.Background())

	accounted := store.jobs[job.ID]
	require.NotNil(t, accounted)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, accounted.AccountingStatus)

	require.Len(t, store.logs, 1)
	logEntry := store.logs[AccountingLogID(ProviderJobKindBatch, schemas.Bedrock, "batch_multimodel")]
	require.NotNil(t, logEntry)
	require.NotNil(t, logEntry.Cost, "accounted batch should have a cost")

	expectedCost := (200+150)*1.5e-06 + (50+30)*7.5e-06
	assert.InDelta(t, expectedCost, *logEntry.Cost, 1e-12)
	assert.Equal(t, "mixed", logEntry.Model, "mixed-model batch should have model='mixed'")

	require.NotNil(t, logEntry.BatchDebugParsed)
	require.NotNil(t, logEntry.BatchDebugParsed.Accounting)
	breakdown := logEntry.BatchDebugParsed.Accounting.ModelBreakdowns
	require.Contains(t, breakdown, "anthropic.claude-sonnet-4-6")
	require.Contains(t, breakdown, "global.anthropic.claude-sonnet-4-6")

	require.NotNil(t, breakdown["anthropic.claude-sonnet-4-6"].Cost)
	assert.InDelta(t, 200*1.5e-06+50*7.5e-06, *breakdown["anthropic.claude-sonnet-4-6"].Cost, 1e-12)
	require.NotNil(t, breakdown["global.anthropic.claude-sonnet-4-6"].Cost)
	assert.InDelta(t, 150*1.5e-06+30*7.5e-06, *breakdown["global.anthropic.claude-sonnet-4-6"].Cost, 1e-12)
}
