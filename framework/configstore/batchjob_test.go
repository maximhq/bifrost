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
	require.NoError(t, store.DB().AutoMigrate(&tables.TableProviderJob{}), "migrate batch_jobs table")
	return store
}

func seedBatchJob(t *testing.T, store *RDBConfigStore, provider, batchID string) *tables.TableProviderJob {
	t.Helper()
	job := &tables.TableProviderJob{
		ID:               tables.ProviderJobID(tables.ProviderJobKindBatch, provider, batchID),
		Provider:         provider,
		JobID:            batchID,
		AccountingStatus: tables.ProviderJobAccountingStatusPending,
	}
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))
	return job
}

func getBatchJob(t *testing.T, store *RDBConfigStore, id string) *tables.TableProviderJob {
	t.Helper()
	job, err := store.GetProviderJob(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, job)
	return job
}

// ageBatchClaim forces claimed_at back so a stale claim can be exercised without
// sleeping. Note it targets claimed_at, not updated_at: claim staleness is measured
// only on the claim timestamp, precisely so unfenced writers cannot renew it.
func ageBatchClaim(t *testing.T, store *RDBConfigStore, id string, ts time.Time) {
	t.Helper()
	require.NoError(t, store.DB().Model(&tables.TableProviderJob{}).
		Where("id = ?", id).Update("claimed_at", ts).Error)
}

func TestClaimProviderJobRunnerFencesCompletion(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	job := seedBatchJob(t, store, "openai", "batch_1")

	claimed, err := store.ClaimProviderJob(ctx, job.ID, "runner-A", time.Now().UTC().Add(-time.Minute), false)
	require.NoError(t, err)
	require.True(t, claimed)

	// A different runner cannot advance or complete the claimed job.
	assert.ErrorIs(t, store.MarkProviderJobAggregateLogWritten(ctx, job.ID, "runner-B"), ErrNotFound)
	assert.ErrorIs(t, store.CompleteProviderJob(ctx, job.ID, "runner-B"), ErrNotFound)

	// The owning runner can.
	require.NoError(t, store.MarkProviderJobAggregateLogWritten(ctx, job.ID, "runner-A"))
	require.NoError(t, store.CompleteProviderJob(ctx, job.ID, "runner-A"))
	assert.Equal(t, tables.ProviderJobAccountingStatusAccounted, getBatchJob(t, store, job.ID).AccountingStatus)
}

func TestClaimProviderJobRejectsFreshProcessingButAllowsStale(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	job := seedBatchJob(t, store, "openai", "batch_2")

	claimed, err := store.ClaimProviderJob(ctx, job.ID, "runner-A", time.Now().UTC().Add(-time.Minute), false)
	require.NoError(t, err)
	require.True(t, claimed)

	// A fresh in-flight claim blocks a second claimant.
	claimed, err = store.ClaimProviderJob(ctx, job.ID, "runner-B", time.Now().UTC().Add(-time.Minute), false)
	require.NoError(t, err)
	assert.False(t, claimed)

	// Once the claim goes stale, another runner reclaims it.
	ageBatchClaim(t, store, job.ID, time.Now().UTC().Add(-15*time.Minute))
	claimed, err = store.ClaimProviderJob(ctx, job.ID, "runner-B", time.Now().UTC().Add(-time.Minute), false)
	require.NoError(t, err)
	assert.True(t, claimed)
	owner := getBatchJob(t, store, job.ID).RunnerID
	require.NotNil(t, owner)
	assert.Equal(t, "runner-B", *owner)
}

// A dead runner's job must stay reclaimable. Unfenced writers — the sweeper's poll
// upsert, a user-triggered /results call, and AccountBatchResults' own upsert — must
// not be able to refresh the staleness clock and pin a job to a runner that is gone.
func TestClaimProviderJobStalenessSurvivesUnfencedUpsert(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	job := seedBatchJob(t, store, "openai", "batch_stuck")

	claimed, err := store.ClaimProviderJob(ctx, job.ID, "runner-A", time.Now().UTC().Add(-time.Minute), false)
	require.NoError(t, err)
	require.True(t, claimed)

	// runner-A dies mid-settlement; its claim ages out.
	ageBatchClaim(t, store, job.ID, time.Now().UTC().Add(-15*time.Minute))

	// An unfenced upsert touches the row (this is what every sweep does before it
	// tries to claim).
	require.NoError(t, store.UpsertProviderJob(ctx, &tables.TableProviderJob{
		ID:             job.ID,
		Provider:       "openai",
		JobID:          "batch_stuck",
		ProviderStatus: string(schemas.BatchStatusCompleted),
	}))

	claimed, err = store.ClaimProviderJob(ctx, job.ID, "runner-B", time.Now().UTC().Add(-time.Minute), false)
	require.NoError(t, err)
	assert.True(t, claimed,
		"a dead runner's job must remain reclaimable after an unfenced upsert; "+
			"otherwise the job is pinned to the dead runner forever")
}

func TestClaimProviderJobRejectsTerminalStates(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	job := seedBatchJob(t, store, "openai", "batch_3")

	claimed, err := store.ClaimProviderJob(ctx, job.ID, "runner-A", time.Now().UTC().Add(-time.Minute), false)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, store.CompleteProviderJob(ctx, job.ID, "runner-A"))

	// Accounted is terminal; no runner can reclaim it.
	claimed, err = store.ClaimProviderJob(ctx, job.ID, "runner-A", time.Now().UTC().Add(-time.Hour), false)
	require.NoError(t, err)
	assert.False(t, claimed)
}

// "unpriceable" records that polling stopped, not that the money is forfeit. A
// caller holding real results answers every reason a job reaches that state, so it
// must be able to reclaim one — while a plain claim still refuses, and "accounted"
// refuses either way.
func TestClaimProviderJobAllowUnpriceableReopensStoppedJobs(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	job := seedBatchJob(t, store, "openai", "batch_reclaim")

	_, err := store.ClaimProviderJob(ctx, job.ID, "runner-A", time.Now().UTC().Add(-time.Minute), false)
	require.NoError(t, err)
	require.NoError(t, store.MarkProviderJobUnpriceable(ctx, job.ID, "runner-A", "max_poll_attempts", nil))

	claimed, err := store.ClaimProviderJob(ctx, job.ID, "runner-B", time.Now().UTC().Add(-time.Minute), false)
	require.NoError(t, err)
	assert.False(t, claimed, "the poll loop must keep leaving unpriceable jobs alone")

	claimed, err = store.ClaimProviderJob(ctx, job.ID, "runner-B", time.Now().UTC().Add(-time.Minute), true)
	require.NoError(t, err)
	assert.True(t, claimed, "a settlement holding results must be able to re-drive it")

	require.NoError(t, store.CompleteProviderJob(ctx, job.ID, "runner-B"))
	persisted := getBatchJob(t, store, job.ID)
	assert.Equal(t, tables.ProviderJobAccountingStatusAccounted, persisted.AccountingStatus)
	assert.Nil(t, persisted.UnpriceableReason, "settling clears the reason it could not be priced")

	// Accounted stays terminal even for a caller holding results.
	claimed, err = store.ClaimProviderJob(ctx, job.ID, "runner-C", time.Now().UTC().Add(-time.Hour), true)
	require.NoError(t, err)
	assert.False(t, claimed)
}

func TestMarkProviderJobUnpriceableSetsReasonAndReleasesRunner(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	job := seedBatchJob(t, store, "openai", "batch_4")
	_, err := store.ClaimProviderJob(ctx, job.ID, "runner-A", time.Now().UTC().Add(-time.Minute), false)
	require.NoError(t, err)

	require.NoError(t, store.MarkProviderJobUnpriceable(ctx, job.ID, "runner-A", "no_usage", nil))

	persisted := getBatchJob(t, store, job.ID)
	assert.Equal(t, tables.ProviderJobAccountingStatusUnpriceable, persisted.AccountingStatus)
	require.NotNil(t, persisted.UnpriceableReason)
	assert.Equal(t, "no_usage", *persisted.UnpriceableReason)
	assert.Nil(t, persisted.RunnerID)
	assert.Nil(t, persisted.LastError, "no error reported should leave last_error nil")
}

func TestFailProviderJobRecordsErrorAndAllowsRetry(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	job := seedBatchJob(t, store, "openai", "batch_5")
	_, err := store.ClaimProviderJob(ctx, job.ID, "runner-A", time.Now().UTC().Add(-time.Minute), false)
	require.NoError(t, err)

	require.NoError(t, store.FailProviderJob(ctx, job.ID, "runner-A", errors.New("boom")))
	persisted := getBatchJob(t, store, job.ID)
	require.NotNil(t, persisted.LastError)
	assert.Equal(t, "boom", *persisted.LastError)
	assert.Nil(t, persisted.RunnerID)

	// Error is non-terminal: the job can be reclaimed and retried.
	claimed, err := store.ClaimProviderJob(ctx, job.ID, "runner-B", time.Now().UTC().Add(-time.Minute), false)
	require.NoError(t, err)
	assert.True(t, claimed)
}

func TestUpsertProviderJobNextCheckAtTerminalHandling(t *testing.T) {
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
			require.NoError(t, store.UpsertProviderJob(ctx, &tables.TableProviderJob{
				ID:               tables.ProviderJobID(tables.ProviderJobKindBatch, "openai", tc.batchID),
				Provider:         "openai",
				JobID:            tc.batchID,
				AccountingStatus: tables.ProviderJobAccountingStatusPending,
				NextCheckAt:      &next,
				ProviderStatus:   string(tc.status),
			}))
			persisted := getBatchJob(t, store, tables.ProviderJobID(tables.ProviderJobKindBatch, "openai", tc.batchID))
			if tc.expectCleared {
				assert.Nil(t, persisted.NextCheckAt, "terminal-but-not-completed clears next_check_at")
			} else {
				assert.NotNil(t, persisted.NextCheckAt, "completed/ended preserves next_check_at")
			}
		})
	}
}

func TestListDueProviderJobsFiltersByDueAndStatus(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)

	mkJob := func(batchID string, status string, next *time.Time) {
		require.NoError(t, store.UpsertProviderJob(ctx, &tables.TableProviderJob{
			ID:               tables.ProviderJobID(tables.ProviderJobKindBatch, "openai", batchID),
			Provider:         "openai",
			JobID:            batchID,
			AccountingStatus: status,
			NextCheckAt:      next,
		}))
	}
	mkJob("due", tables.ProviderJobAccountingStatusPending, &past)
	mkJob("not_due", tables.ProviderJobAccountingStatusPending, &future)
	mkJob("accounted", tables.ProviderJobAccountingStatusAccounted, &past)
	mkJob("no_next", tables.ProviderJobAccountingStatusPending, nil)

	due, err := store.ListDueProviderJobs(ctx, tables.ProviderJobKindBatch, "openai", now, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "due", due[0].JobID)
}

// Attribution is create-time state. A later upsert — the /results path builds a job
// from the fetching request's own log entry — must not be able to move the bill onto
// whoever happened to settle the batch.
func TestUpsertProviderJobDoesNotOverwriteAttribution(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()

	creatorVK := "vk-creator"
	creatorUser := "user-alice"
	creatorBudgets := `["budget-creator"]`
	sourceLog := "req-create"
	id := tables.ProviderJobID(tables.ProviderJobKindBatch, "openai", "batch-attr")

	require.NoError(t, store.UpsertProviderJob(ctx, &tables.TableProviderJob{
		ID:               id,
		Provider:         "openai",
		JobID:            "batch-attr",
		AccountingStatus: tables.ProviderJobAccountingStatusPending,
		SelectedKeyID:    "key-creator",
		VirtualKeyID:     &creatorVK,
		UserID:           &creatorUser,
		BudgetIDs:        &creatorBudgets,
		SourceLogID:      &sourceLog,
	}))

	fetcherVK := "vk-fetcher"
	fetcherUser := "user-bob"
	fetcherBudgets := `["budget-fetcher"]`
	fetcherLog := "req-results"
	require.NoError(t, store.UpsertProviderJob(ctx, &tables.TableProviderJob{
		ID:               id,
		Provider:         "openai",
		JobID:            "batch-attr",
		AccountingStatus: tables.ProviderJobAccountingStatusPending,
		ProviderStatus:   string(schemas.BatchStatusCompleted),
		SelectedKeyID:    "key-fetcher",
		VirtualKeyID:     &fetcherVK,
		UserID:           &fetcherUser,
		BudgetIDs:        &fetcherBudgets,
		SourceLogID:      &fetcherLog,
	}))

	job := getBatchJob(t, store, id)
	assert.Equal(t, "key-creator", job.SelectedKeyID)
	require.NotNil(t, job.VirtualKeyID)
	assert.Equal(t, creatorVK, *job.VirtualKeyID)
	require.NotNil(t, job.UserID)
	assert.Equal(t, creatorUser, *job.UserID)
	require.NotNil(t, job.BudgetIDs)
	assert.Equal(t, creatorBudgets, *job.BudgetIDs)
	require.NotNil(t, job.SourceLogID)
	assert.Equal(t, sourceLog, *job.SourceLogID)
	// Lifecycle state, unlike identity, is expected to advance.
	assert.Equal(t, string(schemas.BatchStatusCompleted), job.ProviderStatus)
}

// TestListDueProviderJobsFiltersByKind is the guard on the whole point of the kind
// column: each kind has its own settler and its own provider client, so a sweeper
// handed another kind's rows would poll them with the wrong one and burn their
// attempt budget against a provider that has never heard of them.
func TestListDueProviderJobsFiltersByKind(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-time.Minute)

	mkJob := func(kind, jobID string) {
		require.NoError(t, store.UpsertProviderJob(ctx, &tables.TableProviderJob{
			ID:               tables.ProviderJobID(kind, "openai", jobID),
			Kind:             kind,
			Provider:         "openai",
			JobID:            jobID,
			AccountingStatus: tables.ProviderJobAccountingStatusPending,
			NextCheckAt:      &past,
		}))
	}
	mkJob(tables.ProviderJobKindBatch, "batch-due")
	mkJob(tables.ProviderJobKindVideo, "video-due")

	batches, err := store.ListDueProviderJobs(ctx, tables.ProviderJobKindBatch, "", now, 10)
	require.NoError(t, err)
	require.Len(t, batches, 1)
	assert.Equal(t, "batch-due", batches[0].JobID)

	videos, err := store.ListDueProviderJobs(ctx, tables.ProviderJobKindVideo, "", now, 10)
	require.NoError(t, err)
	require.Len(t, videos, 1)
	assert.Equal(t, "video-due", videos[0].JobID)

	// An empty kind means batch, so a caller that predates the column keeps seeing
	// exactly the rows it always saw.
	legacy, err := store.ListDueProviderJobs(ctx, "", "", now, 10)
	require.NoError(t, err)
	require.Len(t, legacy, 1)
	assert.Equal(t, "batch-due", legacy[0].JobID)
}

// TestUpsertProviderJobDefaultsKindToBatch pins the NOT NULL column's default on
// the write path: a caller written before kinds existed still produces a valid row.
func TestUpsertProviderJobDefaultsKindToBatch(t *testing.T) {
	store := setupBatchJobTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.UpsertProviderJob(ctx, &tables.TableProviderJob{
		ID:               tables.ProviderJobID(tables.ProviderJobKindBatch, "openai", "batch-nokind"),
		Provider:         "openai",
		JobID:            "batch-nokind",
		AccountingStatus: tables.ProviderJobAccountingStatusPending,
	}))

	job, err := store.GetProviderJob(ctx, tables.ProviderJobID(tables.ProviderJobKindBatch, "openai", "batch-nokind"))
	require.NoError(t, err)
	assert.Equal(t, tables.ProviderJobKindBatch, job.Kind)
}
