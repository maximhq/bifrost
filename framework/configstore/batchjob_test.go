package configstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupBatchJobTestStore extends the base test store with the batch_jobs table.
func setupBatchJobTestStore(t *testing.T) *RDBConfigStore {
	store := setupRDBTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(&tables.TableBatchJob{}), "migrate batch_jobs table")
	return store
}

func seedBatchJob(t *testing.T, store *RDBConfigStore, provider, batchID string) *tables.TableBatchJob {
	t.Helper()
	job := &tables.TableBatchJob{
		ID:               tables.BatchJobID(provider, batchID),
		Provider:         provider,
		BatchID:          batchID,
		AccountingStatus: tables.BatchJobAccountingStatusPending,
	}
	require.NoError(t, store.UpsertBatchJob(context.Background(), job))
	return job
}

func getBatchJob(t *testing.T, store *RDBConfigStore, id string) *tables.TableBatchJob {
	t.Helper()
	job, err := store.GetBatchJob(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, job)
	return job
}

// setBatchUpdatedAt forces updated_at so staleness can be exercised without sleeping.
func setBatchUpdatedAt(t *testing.T, store *RDBConfigStore, id string, ts time.Time) {
	t.Helper()
	require.NoError(t, store.DB().Model(&tables.TableBatchJob{}).
		Where("id = ?", id).Update("updated_at", ts).Error)
}

func TestClaimBatchJobRunnerFencesCompletion(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	job := seedBatchJob(t, store, "openai", "batch_1")

	claimed, err := store.ClaimBatchJob(ctx, job.ID, "runner-A", time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)

	// A different runner cannot advance or complete the claimed job.
	assert.ErrorIs(t, store.MarkBatchJobAggregateLogWritten(ctx, job.ID, "runner-B"), ErrNotFound)
	assert.ErrorIs(t, store.CompleteBatchJob(ctx, job.ID, "runner-B"), ErrNotFound)

	// The owning runner can.
	require.NoError(t, store.MarkBatchJobAggregateLogWritten(ctx, job.ID, "runner-A"))
	require.NoError(t, store.CompleteBatchJob(ctx, job.ID, "runner-A"))
	assert.Equal(t, tables.BatchJobAccountingStatusAccounted, getBatchJob(t, store, job.ID).AccountingStatus)
}

func TestClaimBatchJobRejectsFreshProcessingButAllowsStale(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	job := seedBatchJob(t, store, "openai", "batch_2")

	claimed, err := store.ClaimBatchJob(ctx, job.ID, "runner-A", time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)

	// A fresh in-flight claim blocks a second claimant.
	claimed, err = store.ClaimBatchJob(ctx, job.ID, "runner-B", time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)
	assert.False(t, claimed)

	// Once the claim goes stale, another runner reclaims it.
	setBatchUpdatedAt(t, store, job.ID, time.Now().UTC().Add(-15*time.Minute))
	claimed, err = store.ClaimBatchJob(ctx, job.ID, "runner-B", time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)
	assert.True(t, claimed)
	owner := getBatchJob(t, store, job.ID).RunnerID
	require.NotNil(t, owner)
	assert.Equal(t, "runner-B", *owner)
}

func TestClaimBatchJobRejectsTerminalStates(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	job := seedBatchJob(t, store, "openai", "batch_3")

	claimed, err := store.ClaimBatchJob(ctx, job.ID, "runner-A", time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, store.CompleteBatchJob(ctx, job.ID, "runner-A"))

	// Accounted is terminal; no runner can reclaim it.
	claimed, err = store.ClaimBatchJob(ctx, job.ID, "runner-A", time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	assert.False(t, claimed)
}

func TestMarkBatchJobUnpriceableSetsReasonAndReleasesRunner(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	job := seedBatchJob(t, store, "openai", "batch_4")
	_, err := store.ClaimBatchJob(ctx, job.ID, "runner-A", time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)

	require.NoError(t, store.MarkBatchJobUnpriceable(ctx, job.ID, "runner-A", "no_usage", nil))

	persisted := getBatchJob(t, store, job.ID)
	assert.Equal(t, tables.BatchJobAccountingStatusUnpriceable, persisted.AccountingStatus)
	require.NotNil(t, persisted.UnpriceableReason)
	assert.Equal(t, "no_usage", *persisted.UnpriceableReason)
	assert.Nil(t, persisted.RunnerID)
	assert.Nil(t, persisted.LastError, "no error reported should leave last_error nil")
}

func TestFailBatchJobRecordsErrorAndAllowsRetry(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	job := seedBatchJob(t, store, "openai", "batch_5")
	_, err := store.ClaimBatchJob(ctx, job.ID, "runner-A", time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)

	require.NoError(t, store.FailBatchJob(ctx, job.ID, "runner-A", errors.New("boom")))
	persisted := getBatchJob(t, store, job.ID)
	require.NotNil(t, persisted.LastError)
	assert.Equal(t, "boom", *persisted.LastError)
	assert.Nil(t, persisted.RunnerID)

	// Error is non-terminal: the job can be reclaimed and retried.
	claimed, err := store.ClaimBatchJob(ctx, job.ID, "runner-B", time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)
	assert.True(t, claimed)
}

func TestUpsertBatchJobNextCheckAtTerminalHandling(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	next := time.Now().UTC().Add(time.Hour).UTC()

	cases := []struct {
		name          string
		batchID       string
		status        schemas.BatchStatus
		expectCleared bool
	}{
		{"completed_preserves", "b_completed", schemas.BatchStatusCompleted, false},
		{"ended_preserves", "b_ended", schemas.BatchStatusEnded, false},
		{"failed_clears", "b_failed", schemas.BatchStatusFailed, true},
		{"expired_clears", "b_expired", schemas.BatchStatusExpired, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, store.UpsertBatchJob(ctx, &tables.TableBatchJob{
				ID:               tables.BatchJobID("openai", tc.batchID),
				Provider:         "openai",
				BatchID:          tc.batchID,
				AccountingStatus: tables.BatchJobAccountingStatusPending,
				NextCheckAt:      &next,
				ProviderStatus:   string(tc.status),
			}))
			persisted := getBatchJob(t, store, tables.BatchJobID("openai", tc.batchID))
			if tc.expectCleared {
				assert.Nil(t, persisted.NextCheckAt, "terminal-but-not-completed clears next_check_at")
			} else {
				assert.NotNil(t, persisted.NextCheckAt, "completed/ended preserves next_check_at")
			}
		})
	}
}

func TestListDueBatchJobsFiltersByDueAndStatus(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)

	mkJob := func(batchID string, status string, next *time.Time) {
		require.NoError(t, store.UpsertBatchJob(ctx, &tables.TableBatchJob{
			ID:               tables.BatchJobID("openai", batchID),
			Provider:         "openai",
			BatchID:          batchID,
			AccountingStatus: status,
			NextCheckAt:      next,
		}))
	}
	mkJob("due", tables.BatchJobAccountingStatusPending, &past)
	mkJob("not_due", tables.BatchJobAccountingStatusPending, &future)
	mkJob("accounted", tables.BatchJobAccountingStatusAccounted, &past)
	mkJob("no_next", tables.BatchJobAccountingStatusPending, nil)

	due, err := store.ListDueBatchJobs(ctx, "openai", now, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "due", due[0].BatchID)
}
