package configstore

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The startup backfill walks each table by primary key. These tests pin the two
// properties that walk depends on: it must visit every matching row across batch
// boundaries, and it must terminate even when a row's status never advances.

// oauthConfigID returns a zero-padded id so lexical and insertion order agree,
// making batch boundaries predictable.
func oauthConfigID(i int) string { return fmt.Sprintf("oauth-cfg-%05d", i) }

// seedPlaintextOAuthConfigs inserts n oauth_configs rows via raw SQL so the
// BeforeSave hook never runs and the rows stay unencrypted.
func seedPlaintextOAuthConfigs(t *testing.T, db *gorm.DB, n int, secretPrefix string, statuses ...string) {
	t.Helper()
	if len(statuses) == 0 {
		statuses = []string{encryptionStatusPlainText}
	}
	now := time.Now()
	for i := 0; i < n; i++ {
		status := statuses[i%len(statuses)]
		var statusArg any
		if status == "NULL" {
			statusArg = nil
		} else {
			statusArg = status
		}
		insertPlaintextRow(t, db,
			`INSERT INTO oauth_configs (id, client_secret, redirect_uri, status, encryption_status, created_at, updated_at)
			 VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
			oauthConfigID(i), secretPrefix+fmt.Sprint(i), "https://example.com/cb", statusArg, now, now)
	}
}

// countOAuthConfigUpdates counts UPDATE statements issued against oauth_configs,
// so a test can tell "wrote each row once" from "kept rewriting the same rows".
func countOAuthConfigUpdates(t *testing.T, db *gorm.DB, n *atomic.Int64) {
	t.Helper()
	require.NoError(t, db.Callback().Update().After("gorm:update").
		Register("test:count_oauth_updates", func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == "oauth_configs" {
				n.Add(1)
			}
		}))
}

func plaintextOAuthConfigCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Table("oauth_configs").
		Where("encryption_status = ? OR encryption_status IS NULL OR encryption_status = ''", encryptionStatusPlainText).
		Count(&n).Error)
	return n
}

// TestEncryptPlaintextOAuthConfigs_WalksEveryRowPastBatchSize covers the batch
// walk across several batches, including a partial final one.
func TestEncryptPlaintextOAuthConfigs_WalksEveryRowPastBatchSize(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	const total = encryptionBatchSize*2 + 37
	seedPlaintextOAuthConfigs(t, db, total, "secret-")

	count, err := store.encryptPlaintextOAuthConfigs(context.Background())
	require.NoError(t, err)
	assert.Equal(t, total, count)
	assert.Zero(t, plaintextOAuthConfigCount(t, db), "every row should have left plain_text")

	// Spot-check the batch boundaries, where an off-by-one cursor would drop rows.
	for _, i := range []int{0, encryptionBatchSize - 1, encryptionBatchSize, total - 1} {
		var found tables.TableOauthConfig
		require.NoError(t, db.Where("id = ?", oauthConfigID(i)).First(&found).Error)
		assert.Equal(t, fmt.Sprintf("secret-%d", i), found.ClientSecret.GetValue(),
			"row %d should round-trip through encrypt/decrypt", i)
	}
}

// TestEncryptPlaintextOAuthConfigs_WalksRowsWithStatusSentinels covers the three
// "not yet encrypted" spellings interleaved across batches. The cursor is ANDed
// onto an OR-chain, so a missing pair of parentheses would bind it to the last
// disjunct alone and strand rows.
func TestEncryptPlaintextOAuthConfigs_WalksRowsWithStatusSentinels(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	const total = encryptionBatchSize*2 + 11
	seedPlaintextOAuthConfigs(t, db, total, "secret-", encryptionStatusPlainText, "NULL", "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	count, err := store.encryptPlaintextOAuthConfigs(ctx)
	require.NoError(t, err)
	assert.Equal(t, total, count)
	assert.Zero(t, plaintextOAuthConfigCount(t, db))
}

// TestEncryptPlaintextOAuthConfigs_TerminatesWhenStatusNeverAdvances is the
// regression guard for the boot hang: the loop must not depend on a BeforeSave
// hook actually stamping the row. A hook that declines to stamp is emulated by
// pinning the column back to plain_text on every write.
func TestEncryptPlaintextOAuthConfigs_TerminatesWhenStatusNeverAdvances(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	const total = encryptionBatchSize + 5
	seedPlaintextOAuthConfigs(t, db, total, "secret-")

	require.NoError(t, db.Callback().Update().Before("gorm:update").
		Register("test:pin_plaintext", func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == "oauth_configs" {
				tx.Statement.SetColumn("encryption_status", encryptionStatusPlainText)
			}
		}))

	var updates atomic.Int64
	countOAuthConfigUpdates(t, db, &updates)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := store.encryptPlaintextOAuthConfigs(ctx)
	require.NoError(t, err, "the walk must finish even though no row leaves plain_text")
	assert.LessOrEqual(t, updates.Load(), int64(total), "each row should be written at most once")
	assert.Equal(t, int64(total), plaintextOAuthConfigCount(t, db),
		"guard check: the pin must really have held the rows at plain_text")
}

// TestEncryptPlaintextOAuthConfigs_SecretRefRowsConverge covers rows whose
// client_secret is an env/vault reference. There is nothing to cipher, but the
// row must still leave plain_text so later boots stop re-selecting and
// re-writing it.
func TestEncryptPlaintextOAuthConfigs_SecretRefRowsConverge(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	now := time.Now()
	refs := map[string]string{
		"oauth-vault": "vault.oauth_configs/cfg/client_secret",
		"oauth-env":   "env.OAUTH_CLIENT_SECRET",
	}
	for id, ref := range refs {
		insertPlaintextRow(t, db,
			`INSERT INTO oauth_configs (id, client_secret, redirect_uri, status, encryption_status, created_at, updated_at)
			 VALUES (?, ?, ?, 'pending', 'plain_text', ?, ?)`,
			id, ref, "https://example.com/cb", now, now)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := store.encryptPlaintextOAuthConfigs(ctx)
	require.NoError(t, err)

	for id, ref := range refs {
		var raw map[string]any
		require.NoError(t, db.Table("oauth_configs").Where("id = ?", id).Take(&raw).Error)
		assert.Equal(t, encryptionStatusEncrypted, raw["encryption_status"],
			"%s must leave plain_text or the next boot re-selects it forever", id)
		assert.Equal(t, ref, raw["client_secret"], "%s: the reference must be stored verbatim", id)
	}

	// A second pass must find nothing left to do.
	var updates atomic.Int64
	countOAuthConfigUpdates(t, db, &updates)
	count, err := store.encryptPlaintextOAuthConfigs(ctx)
	require.NoError(t, err)
	assert.Zero(t, count)
	assert.Zero(t, updates.Load(), "converged rows must not be rewritten on later boots")
}

// TestEncryptPlaintextKeys_WalksEveryRowPastBatchSize covers the same walk on a
// table with an integer primary key, where the cursor is numeric rather than
// lexical.
func TestEncryptPlaintextKeys_WalksEveryRowPastBatchSize(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	const total = encryptionBatchSize + 23
	now := time.Now()
	for i := 0; i < total; i++ {
		insertPlaintextRow(t, db,
			`INSERT INTO config_keys (name, provider_id, provider, key_id, value, encryption_status, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, 'plain_text', ?, ?)`,
			fmt.Sprintf("key-%05d", i), 1, "openai", fmt.Sprintf("key-id-%05d", i),
			fmt.Sprintf("sk-plaintext-%d", i), now, now)
	}

	count, err := store.encryptPlaintextKeys(context.Background())
	require.NoError(t, err)
	assert.Equal(t, total, count)

	var remaining int64
	require.NoError(t, db.Table("config_keys").
		Where("encryption_status = ? OR encryption_status IS NULL OR encryption_status = ''", encryptionStatusPlainText).
		Count(&remaining).Error)
	assert.Zero(t, remaining)

	var found tables.TableKey
	require.NoError(t, db.Where("key_id = ?", fmt.Sprintf("key-id-%05d", total-1)).First(&found).Error)
	assert.Equal(t, fmt.Sprintf("sk-plaintext-%d", total-1), found.Value.GetValue())
}
