package batchaccounting

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
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

func assertSweeperAccountsBatchWithPricing(t *testing.T, mc *modelcatalog.ModelCatalog, job *logstore.BatchJob, results []schemas.BatchResultItem, expectedCost float64) {
	t.Helper()
	store := newFakeAccountingStore()
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{
		retrieveResp: &schemas.BifrostBatchRetrieveResponse{
			ID:     job.BatchID,
			Status: schemas.BatchStatusCompleted,
		},
		resultsResp: &schemas.BifrostBatchResultsResponse{
			BatchID: job.BatchID,
			Results: results,
		},
	}
	emitter := &fakeAggregateLogWriter{}
	reporter := &fakeUsageReporter{}

	kv := &fakeKVStore{setNXAllowed: true}
	sweeper := NewSweeper(store, mc, fetcher, emitter, reporter, SweeperConfig{
		Interval: time.Minute,
		Limit:    10,
		KVStore:  kv,
	})

	sweeper.SweepOnce(context.Background())

	accounted := store.jobs[job.ID]
	require.NotNil(t, accounted, "batch job should exist in store")
	assert.Equal(t, logstore.BatchJobAccountingStatusAccounted, accounted.AccountingStatus,
		"batch job should be accounted, got status=%s", accounted.AccountingStatus)

	assert.Len(t, store.logs, 1, "one aggregate log should be written")
	logEntry := store.logs[AccountingLogID(schemas.ModelProvider(job.Provider), job.BatchID)]
	require.NotNil(t, logEntry, "aggregate log entry should exist")
	require.NotNil(t, logEntry.Cost, "log entry should have a cost")
	assert.InDelta(t, expectedCost, *logEntry.Cost, 1e-12,
		"cost mismatch for %s/%s: expected %.12f got %.12f",
		job.Provider, job.Model, expectedCost, *logEntry.Cost)

	assert.Len(t, emitter.emitted, 1, "one emit should fire")
	assert.Len(t, reporter.reports, 1, "one governance report should fire")
	assert.Equal(t, *logEntry.Cost, reporter.reports[0].Cost, "governance cost should match log cost")
}

func TestBatchPricing_ClaudeSonnet5_Anthropic(t *testing.T) {
	mc := loadPricingFromFile(t)
	now := time.Now().UTC().Add(-time.Minute)
	job := &logstore.BatchJob{
		ID:               logstore.BatchJobID(string(schemas.Anthropic), "batch_claude_sonnet_5"),
		Provider:         string(schemas.Anthropic),
		BatchID:          "batch_claude_sonnet_5",
		Model:            "claude-sonnet-5",
		AccountingStatus: logstore.BatchJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	results := []schemas.BatchResultItem{
		anthropicResult("claude-sonnet-5", 500, 0, 0, 200),
	}
	assertSweeperAccountsBatchWithPricing(t, mc, job, results, 500*1.5e-05+200*7.5e-06)
}

func TestBatchPricing_Gemini25Flash(t *testing.T) {
	mc := loadPricingFromFile(t)
	now := time.Now().UTC().Add(-time.Minute)
	job := &logstore.BatchJob{
		ID:               logstore.BatchJobID(string(schemas.Gemini), "batch_gemini_25_flash"),
		Provider:         string(schemas.Gemini),
		BatchID:          "batch_gemini_25_flash",
		Model:            "gemini-2.5-flash",
		AccountingStatus: logstore.BatchJobAccountingStatusPending,
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
	job := &logstore.BatchJob{
		ID:               logstore.BatchJobID(string(schemas.Bedrock), "batch_claude_sonnet_46"),
		Provider:         string(schemas.Bedrock),
		BatchID:          "batch_claude_sonnet_46",
		Model:            "anthropic.claude-sonnet-4-6",
		AccountingStatus: logstore.BatchJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	results := []schemas.BatchResultItem{
		bedrockResult("anthropic.claude-sonnet-4-6", 400, 100),
	}
	assertSweeperAccountsBatchWithPricing(t, mc, job, results, 400*1.5e-05+100*7.5e-06)
}

func TestBatchPricing_GlobalClaudeSonnet46_Bedrock(t *testing.T) {
	mc := loadPricingFromFile(t)
	now := time.Now().UTC().Add(-time.Minute)
	job := &logstore.BatchJob{
		ID:               logstore.BatchJobID(string(schemas.Bedrock), "batch_global_claude_sonnet_46"),
		Provider:         string(schemas.Bedrock),
		BatchID:          "batch_global_claude_sonnet_46",
		Model:            "global.anthropic.claude-sonnet-4-6",
		AccountingStatus: logstore.BatchJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	results := []schemas.BatchResultItem{
		bedrockResult("global.anthropic.claude-sonnet-4-6", 250, 75),
	}
	assertSweeperAccountsBatchWithPricing(t, mc, job, results, 250*1.5e-05+75*7.5e-06)
}

func TestBatchPricing_ClaudeHaiku45_NoBatchPricing(t *testing.T) {
	mc := loadPricingFromFile(t)
	now := time.Now().UTC().Add(-time.Minute)
	job := &logstore.BatchJob{
		ID:               logstore.BatchJobID(string(schemas.Anthropic), "batch_haiku_45"),
		Provider:         string(schemas.Anthropic),
		BatchID:          "batch_haiku_45",
		Model:            "claude-haiku-4-5",
		AccountingStatus: logstore.BatchJobAccountingStatusPending,
		NextCheckAt:      &now,
	}

	store := newFakeAccountingStore()
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

	fetcher := &fakeBatchResultFetcher{
		retrieveResp: &schemas.BifrostBatchRetrieveResponse{
			ID:     "batch_haiku_45",
			Status: schemas.BatchStatusCompleted,
		},
		resultsResp: &schemas.BifrostBatchResultsResponse{
			BatchID: "batch_haiku_45",
			Results: []schemas.BatchResultItem{
				anthropicResult("claude-haiku-4-5", 100, 0, 0, 50),
			},
		},
	}

	kv := &fakeKVStore{setNXAllowed: true}
	sweeper := NewSweeper(store, mc, fetcher, nil, nil, SweeperConfig{
		Interval: time.Minute,
		Limit:    10,
		KVStore:  kv,
	})

	sweeper.SweepOnce(context.Background())

	unpriced := store.jobs[job.ID]
	require.NotNil(t, unpriced)
	assert.Equal(t, logstore.BatchJobAccountingStatusUnpriceable, unpriced.AccountingStatus,
		"batch without batch pricing should be marked unpriceable")
	require.NotNil(t, unpriced.UnpriceableReason)
	assert.Equal(t, UnpriceableReasonMissingBatchPricing, *unpriced.UnpriceableReason,
		"reason should be missing_batch_pricing")

	assert.Len(t, store.logs, 0, "no aggregate log should be written for unpriceable batch")
}

func TestBatchPricing_MultiModel(t *testing.T) {
	mc := loadPricingFromFile(t)
	now := time.Now().UTC().Add(-time.Minute)
	job := &logstore.BatchJob{
		ID:               logstore.BatchJobID(string(schemas.Bedrock), "batch_multimodel"),
		Provider:         string(schemas.Bedrock),
		BatchID:          "batch_multimodel",
		Model:            "anthropic.claude-sonnet-4-6",
		AccountingStatus: logstore.BatchJobAccountingStatusPending,
		NextCheckAt:      &now,
	}

	store := newFakeAccountingStore()
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))

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
	sweeper := NewSweeper(store, mc, fetcher, nil, nil, SweeperConfig{
		Interval: time.Minute,
		Limit:    10,
		KVStore:  kv,
	})

	sweeper.SweepOnce(context.Background())

	accounted := store.jobs[job.ID]
	require.NotNil(t, accounted)
	assert.Equal(t, logstore.BatchJobAccountingStatusAccounted, accounted.AccountingStatus)

	require.Len(t, store.logs, 1)
	logEntry := store.logs[AccountingLogID(schemas.Bedrock, "batch_multimodel")]
	require.NotNil(t, logEntry)

	expectedCost := (200+150)*1.5e-05 + (50+30)*7.5e-06
	assert.InDelta(t, expectedCost, *logEntry.Cost, 1e-12)
	assert.Equal(t, "mixed", logEntry.Model, "mixed-model batch should have model='mixed'")

	breakdown := logEntry.MetadataParsed["model_breakdown"].(map[string]ModelBreakdown)
	require.Contains(t, breakdown, "anthropic.claude-sonnet-4-6")
	require.Contains(t, breakdown, "global.anthropic.claude-sonnet-4-6")

	for _, bm := range []string{"anthropic.claude-sonnet-4-6", "global.anthropic.claude-sonnet-4-6"} {
		require.NotNil(t, breakdown[bm].InputCostPerTokenBatches)
		require.NotNil(t, breakdown[bm].OutputCostPerTokenBatches)
		assert.Equal(t, 1.5e-05, *breakdown[bm].InputCostPerTokenBatches)
		assert.Equal(t, 7.5e-06, *breakdown[bm].OutputCostPerTokenBatches)
	}
}
