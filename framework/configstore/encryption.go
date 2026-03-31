package configstore

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

const (
	encryptionStatusPlainText = "plain_text"
	encryptionStatusEncrypted = "encrypted"
	encryptionBatchSize       = 100
)

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

	// oauth_tokens
	count, err = s.encryptPlaintextOAuthTokens(ctx)
	if err != nil {
		return fmt.Errorf("failed to encrypt oauth_tokens: %w", err)
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
// re-saves them in batches. The TableKey.BeforeSave hook handles the actual encryption.
func (s *RDBConfigStore) encryptPlaintextKeys(ctx context.Context) (int, error) {
	var count int
	for {
		var keys []tables.TableKey
		if err := s.db.WithContext(ctx).
			Where("encryption_status = ? OR encryption_status IS NULL OR encryption_status = ''", encryptionStatusPlainText).
			Limit(encryptionBatchSize).
			Find(&keys).Error; err != nil {
			return count, err
		}
		if len(keys) == 0 {
			break
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for i := range keys {
				if err := tx.Save(&keys[i]).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return count, err
		}
		count += len(keys)
	}
	return count, nil
}

// encryptPlaintextVirtualKeys finds all governance_virtual_keys rows with plaintext encryption
// status and re-saves them in batches. The TableVirtualKey.BeforeSave hook handles encryption.
func (s *RDBConfigStore) encryptPlaintextVirtualKeys(ctx context.Context) (int, error) {
	var count int
	for {
		var vks []tables.TableVirtualKey
		if err := s.db.WithContext(ctx).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND value != ''", encryptionStatusPlainText).
			Limit(encryptionBatchSize).
			Find(&vks).Error; err != nil {
			return count, err
		}
		if len(vks) == 0 {
			break
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for i := range vks {
				if err := tx.Save(&vks[i]).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return count, err
		}
		count += len(vks)
	}
	return count, nil
}

// encryptPlaintextSessions finds all sessions rows with plaintext encryption status and
// re-saves them in batches. The SessionsTable.BeforeSave hook handles encryption.
func (s *RDBConfigStore) encryptPlaintextSessions(ctx context.Context) (int, error) {
	var count int
	for {
		var sessions []tables.SessionsTable
		if err := s.db.WithContext(ctx).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND token != ''", encryptionStatusPlainText).
			Limit(encryptionBatchSize).
			Find(&sessions).Error; err != nil {
			return count, err
		}
		if len(sessions) == 0 {
			break
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for i := range sessions {
				if err := tx.Save(&sessions[i]).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return count, err
		}
		count += len(sessions)
	}
	return count, nil
}

// encryptPlaintextOAuthTokens finds all oauth_tokens rows with plaintext encryption status
// and re-saves them in batches. The TableOauthToken.BeforeSave hook handles encryption.
func (s *RDBConfigStore) encryptPlaintextOAuthTokens(ctx context.Context) (int, error) {
	var count int
	for {
		var tokens []tables.TableOauthToken
		if err := s.db.WithContext(ctx).
			Where("encryption_status = ? OR encryption_status IS NULL OR encryption_status = ''", encryptionStatusPlainText).
			Limit(encryptionBatchSize).
			Find(&tokens).Error; err != nil {
			return count, err
		}
		if len(tokens) == 0 {
			break
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for i := range tokens {
				if err := tx.Save(&tokens[i]).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return count, err
		}
		count += len(tokens)
	}
	return count, nil
}

// encryptPlaintextOAuthConfigs finds all oauth_configs rows with plaintext encryption status
// and re-saves them in batches. The TableOauthConfig.BeforeSave hook handles encryption.
func (s *RDBConfigStore) encryptPlaintextOAuthConfigs(ctx context.Context) (int, error) {
	var count int
	for {
		var configs []tables.TableOauthConfig
		if err := s.db.WithContext(ctx).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND (client_secret != '' OR code_verifier != '')", encryptionStatusPlainText).
			Limit(encryptionBatchSize).
			Find(&configs).Error; err != nil {
			return count, err
		}
		if len(configs) == 0 {
			break
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for i := range configs {
				if err := tx.Save(&configs[i]).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return count, err
		}
		count += len(configs)
	}
	return count, nil
}

// encryptPlaintextMCPClients finds all config_mcp_clients rows with plaintext encryption
// status and re-saves them in batches. The TableMCPClient.BeforeSave hook handles encryption.
func (s *RDBConfigStore) encryptPlaintextMCPClients(ctx context.Context) (int, error) {
	var count int
	for {
		var clients []tables.TableMCPClient
		if err := s.db.WithContext(ctx).
			Where("encryption_status = ? OR encryption_status IS NULL OR encryption_status = ''", encryptionStatusPlainText).
			Limit(encryptionBatchSize).
			Find(&clients).Error; err != nil {
			return count, err
		}
		if len(clients) == 0 {
			break
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for i := range clients {
				if err := tx.Save(&clients[i]).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return count, err
		}
		count += len(clients)
	}
	return count, nil
}

// encryptPlaintextProviderProxies finds all config_providers rows that have a non-empty
// proxy config with plaintext encryption status and re-saves them in batches. The
// TableProvider.BeforeSave hook handles encryption.
func (s *RDBConfigStore) encryptPlaintextProviderProxies(ctx context.Context) (int, error) {
	var count int
	for {
		var providers []tables.TableProvider
		if err := s.db.WithContext(ctx).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND proxy_config_json != '' AND proxy_config_json IS NOT NULL", encryptionStatusPlainText).
			Limit(encryptionBatchSize).
			Find(&providers).Error; err != nil {
			return count, err
		}
		if len(providers) == 0 {
			break
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for i := range providers {
				if err := tx.Save(&providers[i]).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return count, err
		}
		count += len(providers)
	}
	return count, nil
}

// encryptPlaintextVectorStoreConfigs finds all config_vector_store rows that have a non-empty
// config with plaintext encryption status and re-saves them in batches. The
// TableVectorStoreConfig.BeforeSave hook handles encryption.
func (s *RDBConfigStore) encryptPlaintextVectorStoreConfigs(ctx context.Context) (int, error) {
	var count int
	for {
		var configs []tables.TableVectorStoreConfig
		if err := s.db.WithContext(ctx).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND config IS NOT NULL AND config != ''", encryptionStatusPlainText).
			Limit(encryptionBatchSize).
			Find(&configs).Error; err != nil {
			return count, err
		}
		if len(configs) == 0 {
			break
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for i := range configs {
				if err := tx.Save(&configs[i]).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return count, err
		}
		count += len(configs)
	}
	return count, nil
}

// encryptPlaintextPlugins finds all config_plugins rows that have a non-empty config with
// plaintext encryption status and re-saves them in batches. The TablePlugin.BeforeSave hook
// handles encryption.
func (s *RDBConfigStore) encryptPlaintextPlugins(ctx context.Context) (int, error) {
	var count int
	for {
		var plugins []tables.TablePlugin
		if err := s.db.WithContext(ctx).
			Where("(encryption_status = ? OR encryption_status IS NULL OR encryption_status = '') AND config_json != '' AND config_json != '{}'", encryptionStatusPlainText).
			Limit(encryptionBatchSize).
			Find(&plugins).Error; err != nil {
			return count, err
		}
		if len(plugins) == 0 {
			break
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for i := range plugins {
				if err := tx.Save(&plugins[i]).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return count, err
		}
		count += len(plugins)
	}
	return count, nil
}
