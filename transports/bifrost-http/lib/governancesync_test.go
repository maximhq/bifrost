package lib

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skipGovernanceSyncBackoff removes the wait between governance write attempts
// for the duration of a test.
func skipGovernanceSyncBackoff(t *testing.T) {
	t.Helper()
	previous := governanceSyncSleep
	governanceSyncSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { governanceSyncSleep = previous })
}

// deadlockErr is a synthetic error rendered the way pgx renders a Postgres
// deadlock, wrapped as the governance sync wraps store errors.
var deadlockErr = errors.New("failed to update rate limit rl-1: ERROR: deadlock detected (SQLSTATE 40P01)")

// statementTimeoutErr is a synthetic error rendered the way pgx renders a
// statement_timeout cancellation.
var statementTimeoutErr = errors.New("failed to update rate limit rl-1: ERROR: canceling statement due to statement timeout (SQLSTATE 57014)")

// TestRetryTransientGovernanceWrite_RetriesLockErrorsThenSucceeds verifies a
// deadlock and a statement timeout are retried, with inputs restored before each
// retry, and that the write then succeeds instead of failing boot.
func TestRetryTransientGovernanceWrite_RetriesLockErrorsThenSucceeds(t *testing.T) {
	initTestLogger()
	skipGovernanceSyncBackoff(t)

	results := []error{deadlockErr, statementTimeoutErr, nil}
	attempts, restores := 0, 0
	err := retryTransientGovernanceWrite(context.Background(), func() error {
		err := results[attempts]
		attempts++
		return err
	}, func() { restores++ })

	require.NoError(t, err)
	assert.Equal(t, 3, attempts)
	assert.Equal(t, 2, restores, "inputs are restored before every retry, not after success")
}

// TestRetryTransientGovernanceWrite_DoesNotRetryOtherErrors verifies a genuine
// configuration error is surfaced on the first attempt.
func TestRetryTransientGovernanceWrite_DoesNotRetryOtherErrors(t *testing.T) {
	initTestLogger()
	skipGovernanceSyncBackoff(t)

	invalid := errors.New("failed to create rate limit rl-1: invalid token reset duration")
	attempts := 0
	err := retryTransientGovernanceWrite(context.Background(), func() error {
		attempts++
		return invalid
	}, nil)

	require.ErrorIs(t, err, invalid)
	assert.Equal(t, 1, attempts)
}

// TestRetryTransientGovernanceWrite_GivesUpAfterMaxAttempts verifies sustained
// contention still fails boot with the last error rather than retrying forever.
func TestRetryTransientGovernanceWrite_GivesUpAfterMaxAttempts(t *testing.T) {
	initTestLogger()
	skipGovernanceSyncBackoff(t)

	attempts := 0
	err := retryTransientGovernanceWrite(context.Background(), func() error {
		attempts++
		return deadlockErr
	}, nil)

	require.ErrorIs(t, err, deadlockErr)
	assert.Equal(t, governanceWriteMaxAttempts, attempts)
}

// TestGovernanceWriteInputs_RestoreUndoesPartialAttempt verifies a failed
// attempt's in-place edits are undone, including on the provider configs a new
// virtual key shares with config.json's own slice.
func TestGovernanceWriteInputs_RestoreUndoesPartialAttempt(t *testing.T) {
	rateLimitID := "rl-pc"
	fileProviderConfigs := []tables.TableVirtualKeyProviderConfig{{
		Provider:    "openai",
		RateLimitID: &rateLimitID,
		Budgets:     []tables.TableBudget{{ID: "budget-pc"}},
	}}
	customersToAdd := []tables.TableCustomer{{ID: "c1", Budgets: []tables.TableBudget{{ID: "budget-c1"}}}}
	customersToUpdate := []tables.TableCustomer{{ID: "c2", ConfigHash: "hash-c2"}}
	virtualKeysToAdd := []tables.TableVirtualKey{{ID: "vk1", ProviderConfigs: fileProviderConfigs}}

	snapshot := snapshotGovernanceWriteInputs(customersToAdd, customersToUpdate, virtualKeysToAdd)

	// What the write does to its inputs before a statement fails mid-loop.
	customersToAdd[0].Budgets = nil
	customersToUpdate[0].ConfigHash = ""
	providerConfigs := virtualKeysToAdd[0].ProviderConfigs
	virtualKeysToAdd[0].ProviderConfigs = nil
	providerConfigs[0].ID = 17
	providerConfigs[0].VirtualKeyID = "vk1"
	providerConfigs[0].Budgets = nil
	providerConfigs[0].RateLimitID = nil

	snapshot.restoreInto(customersToAdd, customersToUpdate, virtualKeysToAdd)

	require.Len(t, customersToAdd[0].Budgets, 1)
	assert.Equal(t, "hash-c2", customersToUpdate[0].ConfigHash)
	require.Len(t, virtualKeysToAdd[0].ProviderConfigs, 1)
	restored := fileProviderConfigs[0]
	assert.Zero(t, restored.ID, "the rolled-back insert's generated id must not carry into the retry")
	assert.Empty(t, restored.VirtualKeyID)
	require.NotNil(t, restored.RateLimitID, "config.json's own provider config must get its rate limit back")
	assert.Equal(t, "rl-pc", *restored.RateLimitID)
	require.Len(t, restored.Budgets, 1)
}

// TestRateLimitNeedsFileSync covers how the decision depends on the source of truth:
// split mode trusts the stored hash, and config.json mode compares the stored row's
// content against the file.
func TestRateLimitNeedsFileSync(t *testing.T) {
	tokenLimit, duration := int64(1000), "1m"
	file := tables.TableRateLimit{ID: "rl-1", TokenMaxLimit: &tokenLimit, TokenResetDuration: &duration}
	fileHash, err := configstore.GenerateRateLimitHash(file)
	require.NoError(t, err)

	unchanged := file
	unchanged.ConfigHash = "stale-hash"
	unchanged.TokenCurrentUsage = 500

	drifted := file
	drifted.ConfigHash = fileHash
	editedLimit := int64(5)
	drifted.TokenMaxLimit = &editedLimit

	assert.False(t, rateLimitNeedsFileSync(true, unchanged, fileHash),
		"a row whose content matches the file is not rewritten, whatever its stored hash or usage")
	assert.True(t, rateLimitNeedsFileSync(true, drifted, fileHash),
		"a UI edit that left the stored hash untouched is still reverted to the file")
	assert.True(t, rateLimitNeedsFileSync(false, unchanged, fileHash),
		"split mode keeps trusting the stored hash")
	assert.False(t, rateLimitNeedsFileSync(false, drifted, fileHash),
		"split mode keeps UI edits while the file is unchanged")
}

// TestMergeGovernanceConfig_ForceFileSyncSkipsUnchangedRateLimits verifies a
// source_of_truth=config.json restart with an unchanged file doesn't rewrite
// (and so doesn't lock) rate limits whose stored content already matches, while
// a drifted row is still reverted.
func TestMergeGovernanceConfig_ForceFileSyncSkipsUnchangedRateLimits(t *testing.T) {
	initTestLogger()

	tokenLimit, duration := int64(1000), "1m"
	driftedLimit := int64(7)
	store := NewMockConfigStore()
	dbGovernance := &configstore.GovernanceConfig{
		RateLimits: []tables.TableRateLimit{
			{ID: "rl-unchanged", TokenMaxLimit: &tokenLimit, TokenResetDuration: &duration, TokenCurrentUsage: 300},
			{ID: "rl-drifted", TokenMaxLimit: &driftedLimit, TokenResetDuration: &duration},
		},
	}
	store.governanceConfig = dbGovernance
	config := &Config{ConfigStore: store, GovernanceConfig: dbGovernance}
	configData := &ConfigData{
		SourceOfTruth: SourceOfTruthConfigJSON,
		Governance: &configstore.GovernanceConfig{
			RateLimits: []tables.TableRateLimit{
				{ID: "rl-unchanged", TokenMaxLimit: &tokenLimit, TokenResetDuration: &duration},
				{ID: "rl-drifted", TokenMaxLimit: &tokenLimit, TokenResetDuration: &duration},
			},
		},
	}

	mergeGovernanceConfig(context.Background(), config, configData, dbGovernance)

	require.Len(t, store.governanceItemsUpdated.rateLimits, 1, "only the drifted rate limit is written")
	written := store.governanceItemsUpdated.rateLimits[0]
	assert.Equal(t, "rl-drifted", written.ID)
	require.NotNil(t, written.TokenMaxLimit)
	assert.Equal(t, tokenLimit, *written.TokenMaxLimit)
}

// TestMergeGovernanceConfig_RetriesDeadlockedWrite reproduces the reported
// rolling-deploy crash with a synthetic deadlock. The governance write fails
// once with SQLSTATE 40P01, as when a running replica's usage dump locks the
// same rate limit, and boot must retry and persist the file's values rather than
// exit.
func TestMergeGovernanceConfig_RetriesDeadlockedWrite(t *testing.T) {
	initTestLogger()
	skipGovernanceSyncBackoff(t)

	storedLimit, fileLimit, duration := int64(10), int64(1000), "1m"
	store := NewMockConfigStore()
	dbGovernance := &configstore.GovernanceConfig{
		RateLimits: []tables.TableRateLimit{{ID: "rl-1", TokenMaxLimit: &storedLimit, TokenResetDuration: &duration}},
	}
	store.governanceConfig = dbGovernance
	store.rateLimitUpdateErrs = []error{deadlockErr}
	config := &Config{ConfigStore: store, GovernanceConfig: dbGovernance}
	configData := &ConfigData{
		Governance: &configstore.GovernanceConfig{
			RateLimits: []tables.TableRateLimit{{ID: "rl-1", TokenMaxLimit: &fileLimit, TokenResetDuration: &duration}},
		},
	}

	mergeGovernanceConfig(context.Background(), config, configData, dbGovernance)

	require.Len(t, store.governanceItemsUpdated.rateLimits, 1, "the retried attempt persists the rate limit")
	require.NotNil(t, store.governanceItemsUpdated.rateLimits[0].TokenMaxLimit)
	assert.Equal(t, fileLimit, *store.governanceItemsUpdated.rateLimits[0].TokenMaxLimit)
}

// TestSortedByID verifies writes are ordered by id without reordering the
// caller's slice.
func TestSortedByID(t *testing.T) {
	rows := []tables.TableRateLimit{{ID: "c"}, {ID: "a"}, {ID: "b"}}
	sorted := sortedByID(rows, func(r tables.TableRateLimit) string { return r.ID })
	assert.Equal(t, []string{"a", "b", "c"}, []string{sorted[0].ID, sorted[1].ID, sorted[2].ID})
	assert.Equal(t, "c", rows[0].ID, "the caller's slice keeps its order")
}
