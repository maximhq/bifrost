package logstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBatchJobAccountingClaimTokenGuardsCompletion(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "batch-jobs.db")}, hybridTestLogger{})
	require.NoError(t, err)

	job := &BatchJob{
		Provider:         "openai",
		BatchID:          "batch_claim_race",
		AccountingStatus: BatchJobAccountingStatusPending,
	}
	require.NoError(t, store.UpsertBatchJob(ctx, job))

	firstToken, claimed, err := store.ClaimBatchJobAccounting(ctx, job.ID, "node-a", time.Minute)
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.NotEmpty(t, firstToken)

	secondToken, claimed, err := store.ClaimBatchJobAccounting(ctx, job.ID, "node-b", time.Minute)
	require.NoError(t, err)
	assert.False(t, claimed)
	assert.Empty(t, secondToken)

	err = store.CompleteBatchJobAccounting(ctx, job.ID, "wrong-token")
	assert.True(t, errors.Is(err, ErrNotFound))

	require.NoError(t, store.CompleteBatchJobAccounting(ctx, job.ID, firstToken))

	err = store.CompleteBatchJobAccounting(ctx, job.ID, firstToken)
	assert.True(t, errors.Is(err, ErrNotFound))
}

func TestUpsertBatchJobRejectsNonCanonicalID(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "batch-canonical-id.db")}, hybridTestLogger{})
	require.NoError(t, err)

	job := &BatchJob{
		ID:       "arbitrary-id",
		Provider: "openai",
		BatchID:  "batch_canonical",
	}
	err = store.UpsertBatchJob(ctx, job)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match canonical id")
}

func TestBatchJobAccountingPhaseMarkersUseClaimToken(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "batch-phases.db")}, hybridTestLogger{})
	require.NoError(t, err)

	job := &BatchJob{
		Provider:         "openai",
		BatchID:          "batch_phases",
		AccountingStatus: BatchJobAccountingStatusPending,
	}
	require.NoError(t, store.UpsertBatchJob(ctx, job))

	token, claimed, err := store.ClaimBatchJobAccounting(ctx, job.ID, "node-a", time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)

	err = store.MarkBatchJobAggregateLogWritten(ctx, job.ID, "wrong-token")
	assert.True(t, errors.Is(err, ErrNotFound))
	require.NoError(t, store.MarkBatchJobAggregateLogWritten(ctx, job.ID, token))

	err = store.MarkBatchJobGovernanceReported(ctx, job.ID, "wrong-token")
	assert.True(t, errors.Is(err, ErrNotFound))
	require.NoError(t, store.MarkBatchJobGovernanceReported(ctx, job.ID, token))

	persisted, err := store.FindBatchJobByID(ctx, job.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted.AggregateLogWrittenAt)
	require.NotNil(t, persisted.GovernanceReportedAt)
}

func TestMarkBatchJobUnpriceableWithoutErrorClearsLastError(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "batch-unpriceable.db")}, hybridTestLogger{})
	require.NoError(t, err)

	job := &BatchJob{
		Provider:         "openai",
		BatchID:          "batch_unpriceable",
		AccountingStatus: BatchJobAccountingStatusPending,
	}
	require.NoError(t, store.UpsertBatchJob(ctx, job))

	token, claimed, err := store.ClaimBatchJobAccounting(ctx, job.ID, "node-a", time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, store.MarkBatchJobUnpriceable(ctx, job.ID, token, "no_usage", nil))

	persisted, err := store.FindBatchJobByID(ctx, job.ID)
	require.NoError(t, err)
	assert.Nil(t, persisted.LastError)
}

func TestUpsertBatchJob_CompletedPreservesNextCheckAt(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "batch-upsert-nextcheck.db")}, hybridTestLogger{})
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)

	job := &BatchJob{
		Provider:         "openai",
		BatchID:          "batch_completed_nextcheck",
		ProviderStatus:   string(schemas.BatchStatusCompleted),
		Model:            "gpt-4o-mini",
		NextCheckAt:      &now,
		AccountingStatus: BatchJobAccountingStatusPending,
	}
	require.NoError(t, store.UpsertBatchJob(ctx, job))

	persisted, err := store.FindBatchJobByID(ctx, job.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted.NextCheckAt, "completed batch should preserve NextCheckAt")
	assert.Equal(t, now.Unix(), persisted.NextCheckAt.Unix(),
		"NextCheckAt should not be cleared for completed status")
}

func TestUpsertBatchJob_FailedClearsNextCheckAt(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "batch-upsert-failed.db")}, hybridTestLogger{})
	require.NoError(t, err)

	now := time.Now().UTC()

	job := &BatchJob{
		Provider:         "openai",
		BatchID:          "batch_failed_nextcheck",
		ProviderStatus:   string(schemas.BatchStatusFailed),
		AccountingStatus: BatchJobAccountingStatusPending,
		NextCheckAt:      &now,
	}
	require.NoError(t, store.UpsertBatchJob(ctx, job))

	persisted, err := store.FindBatchJobByID(ctx, job.ID)
	require.NoError(t, err)
	assert.Nil(t, persisted.NextCheckAt, "failed/terminal (non-completed) batch should clear NextCheckAt")
}

func TestUpsertBatchJob_ClearsNextCheckAt_OnlyForNonCompletedTerminal(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		status  string
		wantNil bool
	}{
		{name: "failed", status: string(schemas.BatchStatusFailed), wantNil: true},
		{name: "expired", status: string(schemas.BatchStatusExpired), wantNil: true},
		{name: "cancelled", status: string(schemas.BatchStatusCancelled), wantNil: true},
		{name: "ended", status: string(schemas.BatchStatusEnded), wantNil: false},
		{name: "deleted", status: string(schemas.BatchStatusDeleted), wantNil: true},
		{name: "completed", status: string(schemas.BatchStatusCompleted), wantNil: false},
		{name: "in_progress", status: string(schemas.BatchStatusInProgress), wantNil: false},
		{name: "validating", status: string(schemas.BatchStatusValidating), wantNil: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "batch-term-"+tt.name+".db")}, hybridTestLogger{})
			require.NoError(t, err)

			now := time.Now().UTC().Truncate(time.Second)
			job := &BatchJob{
				Provider:         "openai",
				BatchID:          "batch_nextcheck_" + tt.name,
				ProviderStatus:   tt.status,
				AccountingStatus: BatchJobAccountingStatusPending,
				NextCheckAt:      &now,
			}
			require.NoError(t, store.UpsertBatchJob(ctx, job))

			persisted, err := store.FindBatchJobByID(ctx, job.ID)
			require.NoError(t, err)

			if tt.wantNil {
				assert.Nil(t, persisted.NextCheckAt, "status=%s should clear NextCheckAt", tt.status)
			} else {
				assert.NotNil(t, persisted.NextCheckAt, "status=%s should preserve NextCheckAt", tt.status)
				if persisted.NextCheckAt != nil {
					assert.Equal(t, now.Unix(), persisted.NextCheckAt.Unix())
				}
			}
		})
	}
}
