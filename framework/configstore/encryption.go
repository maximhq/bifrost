package configstore

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	encryptionStatusPlainText = "plain_text"
	encryptionStatusEncrypted = "encrypted"
	// encryptionBatchSize is how many candidate rows one scan reads ahead. The
	// rows are still written one per transaction; this only bounds the read.
	encryptionBatchSize = 100
	// encryptionVaultConcurrency is how many rows are encrypted at once when the
	// BeforeSave hook writes to the vault. Each row still gets its own
	// transaction and its own single row lock, so this adds no deadlock risk; it
	// only overlaps the vault round trips, which otherwise dominate the walk.
	// Kept well under the runtime pool so a booting node cannot exhaust it.
	encryptionVaultConcurrency = 8
)

// encryptConcurrency returns how many rows to encrypt in parallel. OSS, where vault
// store is not enabled keeps concurrency to 1.
func encryptConcurrency() int {
	if schemas.VaultStoreWriteEnabled() {
		return encryptionVaultConcurrency
	}
	return 1
}

// lockRow takes a row lock so a row claimed by one node is not re-encrypted by
// another. Exactly one row is locked per transaction, so no transaction ever
// holds one lock while waiting for a second and a deadlock cycle cannot form.
// SQLite is single-writer and has no row locks, so the clause is omitted there.
func lockRow(tx *gorm.DB) *gorm.DB {
	if tx.Dialector.Name() == "sqlite" {
		return tx
	}
	return tx.Clauses(clause.Locking{Strength: "UPDATE"})
}

// encryptClaimedRows encrypts the given ids, each in its own transaction that
// locks the row and re-checks it is still plaintext before the hook runs. A
// transaction holds exactly one row lock, so no deadlock cycle can form however
// many run at once, and a row another node already encrypted is dropped before
// its hook does any work — which matters because that work includes vault
// writes. where clause must match a single row and take (status, id).
func encryptClaimedRows[T any, ID any](ctx context.Context, s *RDBConfigStore, ids []ID, where string, statusOf func(*T) string) (int, error) {
	var encrypted atomic.Int64
	errs := make([]error, len(ids))
	sem := make(chan struct{}, encryptConcurrency())
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			errs[i] = s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var row []T
				if err := lockRow(tx).Where(where, encryptionStatusPlainText, ids[i]).
					Limit(1).Find(&row).Error; err != nil {
					return err
				}
				if len(row) == 0 {
					return nil // another node encrypted it first
				}
				if err := tx.Save(&row[0]).Error; err != nil {
					return err
				}
				if statusOf(&row[0]) == encryptionStatusEncrypted {
					encrypted.Add(1)
				}
				return nil
			})
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return int(encrypted.Load()), err
		}
	}
	return int(encrypted.Load()), nil
}

// EncryptPlaintextRows encrypts all rows with encryption_status='plain_text'
// across all sensitive tables. Called during startup when encryption is enabled.
// Each table's GORM BeforeSave hook handles the actual encryption.
func (s *RDBConfigStore) EncryptPlaintextRows(ctx context.Context) error {
	if !encrypt.IsEnabled() {
		return nil
	}

	var totalEncrypted int

	// config_keys
	count, err := s.encryptPlaintextKeys(ctx)
	if err != nil {
		return fmt.Errorf("failed to encrypt config_keys: %w", err)
	}
	totalEncrypted += count

	// governance_virtual_keys
	count, err = s.encryptPlaintextVirtualKeys(ctx)
	if err != nil {
		return fmt.Errorf("failed to encrypt virtual_keys: %w", err)
	}
	totalEncrypted += count

	// sessions
	count, err = s.encryptPlaintextSessions(ctx)
	if err != nil {
		return fmt.Errorf("failed to encrypt sessions: %w", err)
	}
	totalEncrypted += count

	// temp_tokens
	count, err = s.encryptPlaintextTempTokens(ctx)
	if err != nil {
		return fmt.Errorf("failed to encrypt temp_tokens: %w", err)
	}
	totalEncrypted += count

	// mcp_oauth_tokens
	count, err = s.encryptPlaintextOAuthTokens(ctx)
	if err != nil {
		return fmt.Errorf("failed to encrypt mcp_oauth_tokens: %w", err)
	}
	totalEncrypted += count

	// oauth_configs
	count, err = s.encryptPlaintextOAuthConfigs(ctx)
	if err != nil {
		return fmt.Errorf("failed to encrypt oauth_configs: %w", err)
	}
	totalEncrypted += count

	// config_mcp_clients
	count, err = s.encryptPlaintextMCPClients(ctx)
	if err != nil {
		return fmt.Errorf("failed to encrypt mcp_clients: %w", err)
	}
	totalEncrypted += count

	// config_providers (proxy config)
	count, err = s.encryptPlaintextProviderProxies(ctx)
	if err != nil {
		return fmt.Errorf("failed to encrypt provider proxy configs: %w", err)
	}
	totalEncrypted += count

	// config_vector_store
	count, err = s.encryptPlaintextVectorStoreConfigs(ctx)
	if err != nil {
		return fmt.Errorf("failed to encrypt vector_store configs: %w", err)
	}
	totalEncrypted += count

	// config_plugins
	count, err = s.encryptPlaintextPlugins(ctx)
	if err != nil {
		return fmt.Errorf("failed to encrypt plugin configs: %w", err)
	}
	totalEncrypted += count

	if totalEncrypted > 0 && s.logger != nil {
		s.logger.Info(fmt.Sprintf("encrypted %d plaintext rows across all tables", totalEncrypted))
	}

	return nil
}

// encryptPlaintextKeys finds all config_keys rows with plaintext encryption status and
// re-saves them one row per transaction. The TableKey.BeforeSave hook handles the actual encryption.
func (s *RDBConfigStore) encryptPlaintextKeys(ctx context.Context) (int, error) {
	var count int
	var cursor uint
	for {
		// Read ids only. Loading whole rows here would run SecretVar.Scan on
		// every secret column, and a stored vault ref resolves over the
		// network — work the claim below would then repeat.
		var ids []uint
		if err := s.DB().WithContext(ctx).
			Model(&tables.TableKey{}).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND id > ?", encryptionStatusPlainText, cursor).
			Order("id").
			Limit(encryptionBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return count, err
		}
		if len(ids) == 0 {
			break
		}
		cursor = ids[len(ids)-1]
		n, err := encryptClaimedRows(ctx, s, ids, "(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND id = ?",
			func(r *tables.TableKey) string { return r.EncryptionStatus })
		count += n
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

// encryptPlaintextVirtualKeys finds all governance_virtual_keys rows with plaintext encryption
// status and re-saves them one row per transaction. The TableVirtualKey.BeforeSave hook handles encryption.
func (s *RDBConfigStore) encryptPlaintextVirtualKeys(ctx context.Context) (int, error) {
	var count int
	var cursor string
	for {
		// Read ids only. Loading whole rows here would run SecretVar.Scan on
		// every secret column, and a stored vault ref resolves over the
		// network — work the claim below would then repeat.
		var ids []string
		if err := s.DB().WithContext(ctx).
			Model(&tables.TableVirtualKey{}).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND value != '' AND id > ?", encryptionStatusPlainText, cursor).
			Order("id").
			Limit(encryptionBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return count, err
		}
		if len(ids) == 0 {
			break
		}
		cursor = ids[len(ids)-1]
		n, err := encryptClaimedRows(ctx, s, ids, "(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND value != '' AND id = ?",
			func(r *tables.TableVirtualKey) string { return r.EncryptionStatus })
		count += n
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

// encryptPlaintextSessions finds all sessions rows with plaintext encryption status and
// re-saves them one row per transaction. The SessionsTable.BeforeSave hook handles encryption.
func (s *RDBConfigStore) encryptPlaintextSessions(ctx context.Context) (int, error) {
	var count int
	var cursor int
	for {
		// Read ids only. Loading whole rows here would run SecretVar.Scan on
		// every secret column, and a stored vault ref resolves over the
		// network — work the claim below would then repeat.
		var ids []int
		if err := s.DB().WithContext(ctx).
			Model(&tables.SessionsTable{}).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND token != '' AND id > ?", encryptionStatusPlainText, cursor).
			Order("id").
			Limit(encryptionBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return count, err
		}
		if len(ids) == 0 {
			break
		}
		cursor = ids[len(ids)-1]
		n, err := encryptClaimedRows(ctx, s, ids, "(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND token != '' AND id = ?",
			func(r *tables.SessionsTable) string { return r.EncryptionStatus })
		count += n
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

// encryptPlaintextTempTokens finds all temp_tokens rows with plaintext encryption status
// and re-saves them one row per transaction. The TempToken.BeforeSave hook handles encryption.
func (s *RDBConfigStore) encryptPlaintextTempTokens(ctx context.Context) (int, error) {
	var count int
	var cursor string
	for {
		// Read ids only. Loading whole rows here would run SecretVar.Scan on
		// every secret column, and a stored vault ref resolves over the
		// network — work the claim below would then repeat.
		var ids []string
		if err := s.DB().WithContext(ctx).
			Model(&tables.TempToken{}).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND token != '' AND id > ?", encryptionStatusPlainText, cursor).
			Order("id").
			Limit(encryptionBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return count, err
		}
		if len(ids) == 0 {
			break
		}
		cursor = ids[len(ids)-1]
		n, err := encryptClaimedRows(ctx, s, ids, "(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND token != '' AND id = ?",
			func(r *tables.TempToken) string { return r.EncryptionStatus })
		count += n
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

// encryptPlaintextOAuthTokens finds all mcp_oauth_tokens rows with plaintext encryption status
// and re-saves them one row per transaction. The TableMCPOauthToken.BeforeSave hook handles encryption.
func (s *RDBConfigStore) encryptPlaintextOAuthTokens(ctx context.Context) (int, error) {
	var count int
	var cursor string
	for {
		// Read ids only. Loading whole rows here would run SecretVar.Scan on
		// every secret column, and a stored vault ref resolves over the
		// network — work the claim below would then repeat.
		var ids []string
		if err := s.DB().WithContext(ctx).
			Model(&tables.TableMCPOauthToken{}).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND id > ?", encryptionStatusPlainText, cursor).
			Order("id").
			Limit(encryptionBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return count, err
		}
		if len(ids) == 0 {
			break
		}
		cursor = ids[len(ids)-1]
		n, err := encryptClaimedRows(ctx, s, ids, "(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND id = ?",
			func(r *tables.TableMCPOauthToken) string { return r.EncryptionStatus })
		count += n
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

// encryptPlaintextOAuthConfigs finds all oauth_configs rows with plaintext encryption status
// and re-saves them one row per transaction. The TableOauthConfig.BeforeSave hook handles encryption.
// client_secret is the only sensitive column left on this table — state/
// code_verifier/code_challenge/expires_at moved to mcp_oauth_flows (see that
// migration) and code_verifier was the only one of those that was ever
// encrypted, so the WHERE clause below no longer needs an OR branch for it.
func (s *RDBConfigStore) encryptPlaintextOAuthConfigs(ctx context.Context) (int, error) {
	var count int
	var cursor string
	for {
		// Read ids only. Loading whole rows here would run SecretVar.Scan on
		// every secret column, and a stored vault ref resolves over the
		// network — work the claim below would then repeat.
		var ids []string
		if err := s.DB().WithContext(ctx).
			Model(&tables.TableOauthConfig{}).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND client_secret != '' AND id > ?", encryptionStatusPlainText, cursor).
			Order("id").
			Limit(encryptionBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return count, err
		}
		if len(ids) == 0 {
			break
		}
		cursor = ids[len(ids)-1]
		n, err := encryptClaimedRows(ctx, s, ids, "(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND client_secret != '' AND id = ?",
			func(r *tables.TableOauthConfig) string { return r.EncryptionStatus })
		count += n
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

// encryptPlaintextMCPClients finds all config_mcp_clients rows with plaintext encryption
// status and re-saves them one row per transaction. The TableMCPClient.BeforeSave hook handles encryption.
func (s *RDBConfigStore) encryptPlaintextMCPClients(ctx context.Context) (int, error) {
	var count int
	var cursor uint
	for {
		// Read ids only. Loading whole rows here would run SecretVar.Scan on
		// every secret column, and a stored vault ref resolves over the
		// network — work the claim below would then repeat.
		var ids []uint
		if err := s.DB().WithContext(ctx).
			Model(&tables.TableMCPClient{}).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND id > ?", encryptionStatusPlainText, cursor).
			Order("id").
			Limit(encryptionBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return count, err
		}
		if len(ids) == 0 {
			break
		}
		cursor = ids[len(ids)-1]
		n, err := encryptClaimedRows(ctx, s, ids, "(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND id = ?",
			func(r *tables.TableMCPClient) string { return r.EncryptionStatus })
		count += n
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

// encryptPlaintextProviderProxies finds all config_providers rows that have a non-empty
// proxy config with plaintext encryption status and re-saves them one row per transaction. The
// TableProvider.BeforeSave hook handles encryption.
func (s *RDBConfigStore) encryptPlaintextProviderProxies(ctx context.Context) (int, error) {
	var count int
	var cursor uint
	for {
		// Read ids only. Loading whole rows here would run SecretVar.Scan on
		// every secret column, and a stored vault ref resolves over the
		// network — work the claim below would then repeat.
		var ids []uint
		if err := s.DB().WithContext(ctx).
			Model(&tables.TableProvider{}).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND proxy_config_json != '' AND proxy_config_json IS NOT NULL AND id > ?", encryptionStatusPlainText, cursor).
			Order("id").
			Limit(encryptionBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return count, err
		}
		if len(ids) == 0 {
			break
		}
		cursor = ids[len(ids)-1]
		n, err := encryptClaimedRows(ctx, s, ids, "(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND proxy_config_json != '' AND proxy_config_json IS NOT NULL AND id = ?",
			func(r *tables.TableProvider) string { return r.EncryptionStatus })
		count += n
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

// encryptPlaintextVectorStoreConfigs finds all config_vector_store rows that have a non-empty
// config with plaintext encryption status and re-saves them one row per transaction. The
// TableVectorStoreConfig.BeforeSave hook handles encryption.
func (s *RDBConfigStore) encryptPlaintextVectorStoreConfigs(ctx context.Context) (int, error) {
	var count int
	var cursor uint
	for {
		// Read ids only. Loading whole rows here would run SecretVar.Scan on
		// every secret column, and a stored vault ref resolves over the
		// network — work the claim below would then repeat.
		var ids []uint
		if err := s.DB().WithContext(ctx).
			Model(&tables.TableVectorStoreConfig{}).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND config IS NOT NULL AND config != '' AND id > ?", encryptionStatusPlainText, cursor).
			Order("id").
			Limit(encryptionBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return count, err
		}
		if len(ids) == 0 {
			break
		}
		cursor = ids[len(ids)-1]
		n, err := encryptClaimedRows(ctx, s, ids, "(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND config IS NOT NULL AND config != '' AND id = ?",
			func(r *tables.TableVectorStoreConfig) string { return r.EncryptionStatus })
		count += n
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

// encryptPlaintextPlugins finds all config_plugins rows that have a non-empty config with
// plaintext encryption status and re-saves them one row per transaction. The TablePlugin.BeforeSave hook
// handles encryption.
func (s *RDBConfigStore) encryptPlaintextPlugins(ctx context.Context) (int, error) {
	var count int
	var cursor uint
	for {
		// Read ids only. Loading whole rows here would run SecretVar.Scan on
		// every secret column, and a stored vault ref resolves over the
		// network — work the claim below would then repeat.
		var ids []uint
		if err := s.DB().WithContext(ctx).
			Model(&tables.TablePlugin{}).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND config_json != '' AND config_json != '{}' AND id > ?", encryptionStatusPlainText, cursor).
			Order("id").
			Limit(encryptionBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return count, err
		}
		if len(ids) == 0 {
			break
		}
		cursor = ids[len(ids)-1]
		n, err := encryptClaimedRows(ctx, s, ids, "(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND config_json != '' AND config_json != '{}' AND id = ?",
			func(r *tables.TablePlugin) string { return r.EncryptionStatus })
		count += n
		if err != nil {
			return count, err
		}
	}
	return count, nil
}
