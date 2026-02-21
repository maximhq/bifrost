package configstore

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

const (
	encryptionStatusPlainText = "plain_text"
	encryptionStatusEncrypted = "encrypted"
	encryptionBatchSize       = 100
)

// --- Generic helpers ---

func encryptEnvVar(field *schemas.EnvVar) error {
	if field == nil || field.IsFromEnv() || field.GetValue() == "" {
		return nil
	}
	encrypted, err := encrypt.Encrypt(field.Val)
	if err != nil {
		return err
	}
	field.Val = encrypted
	return nil
}

func decryptEnvVar(field *schemas.EnvVar) error {
	if field == nil || field.IsFromEnv() || field.GetValue() == "" {
		return nil
	}
	decrypted, err := encrypt.Decrypt(field.Val)
	if err != nil {
		return err
	}
	field.Val = decrypted
	return nil
}

func encryptEnvVarPtr(field **schemas.EnvVar) error {
	if field == nil || *field == nil {
		return nil
	}
	return encryptEnvVar(*field)
}

func decryptEnvVarPtr(field **schemas.EnvVar) error {
	if field == nil || *field == nil {
		return nil
	}
	return decryptEnvVar(*field)
}

func encryptString(value *string) error {
	if value == nil || *value == "" {
		return nil
	}
	encrypted, err := encrypt.Encrypt(*value)
	if err != nil {
		return err
	}
	*value = encrypted
	return nil
}

func decryptString(value *string) error {
	if value == nil || *value == "" {
		return nil
	}
	decrypted, err := encrypt.Decrypt(*value)
	if err != nil {
		return err
	}
	*value = decrypted
	return nil
}

// --- Provider keys (config_keys) ---

func encryptKeyFields(key *tables.TableKey) error {
	if !encrypt.IsEnabled() {
		return nil
	}
	if err := encryptEnvVar(&key.Value); err != nil {
		return fmt.Errorf("failed to encrypt key value: %w", err)
	}
	if err := encryptEnvVarPtr(&key.AzureClientSecret); err != nil {
		return fmt.Errorf("failed to encrypt azure client secret: %w", err)
	}
	if err := encryptEnvVarPtr(&key.AzureEndpoint); err != nil {
		return fmt.Errorf("failed to encrypt azure endpoint: %w", err)
	}
	if err := encryptEnvVarPtr(&key.VertexAuthCredentials); err != nil {
		return fmt.Errorf("failed to encrypt vertex auth credentials: %w", err)
	}
	if err := encryptEnvVarPtr(&key.BedrockAccessKey); err != nil {
		return fmt.Errorf("failed to encrypt bedrock access key: %w", err)
	}
	if err := encryptEnvVarPtr(&key.BedrockSecretKey); err != nil {
		return fmt.Errorf("failed to encrypt bedrock secret key: %w", err)
	}
	if err := encryptEnvVarPtr(&key.BedrockSessionToken); err != nil {
		return fmt.Errorf("failed to encrypt bedrock session token: %w", err)
	}
	key.EncryptionStatus = encryptionStatusEncrypted
	return nil
}

func decryptKeyFields(key *tables.TableKey) error {
	if key.EncryptionStatus != encryptionStatusEncrypted {
		return nil
	}
	if err := decryptEnvVar(&key.Value); err != nil {
		return fmt.Errorf("failed to decrypt key value: %w", err)
	}
	if err := decryptEnvVarPtr(&key.AzureClientSecret); err != nil {
		return fmt.Errorf("failed to decrypt azure client secret: %w", err)
	}
	if err := decryptEnvVarPtr(&key.AzureEndpoint); err != nil {
		return fmt.Errorf("failed to decrypt azure endpoint: %w", err)
	}
	if err := decryptEnvVarPtr(&key.VertexAuthCredentials); err != nil {
		return fmt.Errorf("failed to decrypt vertex auth credentials: %w", err)
	}
	if err := decryptEnvVarPtr(&key.BedrockAccessKey); err != nil {
		return fmt.Errorf("failed to decrypt bedrock access key: %w", err)
	}
	if err := decryptEnvVarPtr(&key.BedrockSecretKey); err != nil {
		return fmt.Errorf("failed to decrypt bedrock secret key: %w", err)
	}
	if err := decryptEnvVarPtr(&key.BedrockSessionToken); err != nil {
		return fmt.Errorf("failed to decrypt bedrock session token: %w", err)
	}
	return nil
}

// --- Virtual keys (governance_virtual_keys) ---

func encryptVirtualKeyValue(vk *tables.TableVirtualKey) error {
	vk.ValueHash = encrypt.HashSHA256(vk.Value)
	if !encrypt.IsEnabled() {
		vk.EncryptionStatus = encryptionStatusPlainText
		return nil
	}
	if err := encryptString(&vk.Value); err != nil {
		return fmt.Errorf("failed to encrypt virtual key value: %w", err)
	}
	vk.EncryptionStatus = encryptionStatusEncrypted
	return nil
}

func decryptVirtualKeyValue(vk *tables.TableVirtualKey) error {
	if vk.EncryptionStatus != encryptionStatusEncrypted {
		return nil
	}
	if err := decryptString(&vk.Value); err != nil {
		return fmt.Errorf("failed to decrypt virtual key value: %w", err)
	}
	return nil
}

// --- Sessions ---

func encryptSessionToken(s *tables.SessionsTable) error {
	s.TokenHash = encrypt.HashSHA256(s.Token)
	if !encrypt.IsEnabled() {
		s.EncryptionStatus = encryptionStatusPlainText
		return nil
	}
	if err := encryptString(&s.Token); err != nil {
		return fmt.Errorf("failed to encrypt session token: %w", err)
	}
	s.EncryptionStatus = encryptionStatusEncrypted
	return nil
}

func decryptSessionToken(s *tables.SessionsTable) error {
	if s.EncryptionStatus != encryptionStatusEncrypted {
		return nil
	}
	if err := decryptString(&s.Token); err != nil {
		return fmt.Errorf("failed to decrypt session token: %w", err)
	}
	return nil
}

// --- OAuth tokens ---

func encryptOAuthToken(t *tables.TableOauthToken) error {
	if !encrypt.IsEnabled() {
		return nil
	}
	if err := encryptString(&t.AccessToken); err != nil {
		return fmt.Errorf("failed to encrypt oauth access token: %w", err)
	}
	if err := encryptString(&t.RefreshToken); err != nil {
		return fmt.Errorf("failed to encrypt oauth refresh token: %w", err)
	}
	t.EncryptionStatus = encryptionStatusEncrypted
	return nil
}

func decryptOAuthToken(t *tables.TableOauthToken) error {
	if t.EncryptionStatus != encryptionStatusEncrypted {
		return nil
	}
	if err := decryptString(&t.AccessToken); err != nil {
		return fmt.Errorf("failed to decrypt oauth access token: %w", err)
	}
	if err := decryptString(&t.RefreshToken); err != nil {
		return fmt.Errorf("failed to decrypt oauth refresh token: %w", err)
	}
	return nil
}

// --- OAuth configs ---

func encryptOAuthConfig(c *tables.TableOauthConfig) error {
	if !encrypt.IsEnabled() {
		return nil
	}
	if err := encryptString(&c.ClientSecret); err != nil {
		return fmt.Errorf("failed to encrypt oauth client secret: %w", err)
	}
	if err := encryptString(&c.CodeVerifier); err != nil {
		return fmt.Errorf("failed to encrypt oauth code verifier: %w", err)
	}
	c.EncryptionStatus = encryptionStatusEncrypted
	return nil
}

func decryptOAuthConfig(c *tables.TableOauthConfig) error {
	if c.EncryptionStatus != encryptionStatusEncrypted {
		return nil
	}
	if err := decryptString(&c.ClientSecret); err != nil {
		return fmt.Errorf("failed to decrypt oauth client secret: %w", err)
	}
	if err := decryptString(&c.CodeVerifier); err != nil {
		return fmt.Errorf("failed to decrypt oauth code verifier: %w", err)
	}
	return nil
}

// --- MCP clients ---

func encryptMCPClient(mc *tables.TableMCPClient) error {
	if !encrypt.IsEnabled() {
		return nil
	}
	if mc.ConnectionString != nil {
		if err := encryptEnvVar(mc.ConnectionString); err != nil {
			return fmt.Errorf("failed to encrypt mcp connection string: %w", err)
		}
	}
	if err := encryptString(&mc.HeadersJSON); err != nil {
		return fmt.Errorf("failed to encrypt mcp headers: %w", err)
	}
	mc.EncryptionStatus = encryptionStatusEncrypted
	return nil
}

func decryptMCPClient(mc *tables.TableMCPClient) error {
	if mc.EncryptionStatus != encryptionStatusEncrypted {
		return nil
	}
	if mc.ConnectionString != nil {
		if err := decryptEnvVar(mc.ConnectionString); err != nil {
			return fmt.Errorf("failed to decrypt mcp connection string: %w", err)
		}
	}
	if err := decryptString(&mc.HeadersJSON); err != nil {
		return fmt.Errorf("failed to decrypt mcp headers: %w", err)
	}
	return nil
}

// --- Provider proxy config ---

func encryptProviderProxy(p *tables.TableProvider) error {
	if !encrypt.IsEnabled() || p.ProxyConfigJSON == "" {
		return nil
	}
	if err := encryptString(&p.ProxyConfigJSON); err != nil {
		return fmt.Errorf("failed to encrypt provider proxy config: %w", err)
	}
	p.EncryptionStatus = encryptionStatusEncrypted
	return nil
}

func decryptProviderProxy(p *tables.TableProvider) error {
	if p.EncryptionStatus != encryptionStatusEncrypted || p.ProxyConfigJSON == "" {
		return nil
	}
	if err := decryptString(&p.ProxyConfigJSON); err != nil {
		return fmt.Errorf("failed to decrypt provider proxy config: %w", err)
	}
	return nil
}

// --- Vector store config ---

func encryptVectorStoreConfig(vs *tables.TableVectorStoreConfig) error {
	if !encrypt.IsEnabled() || vs.Config == nil || *vs.Config == "" {
		return nil
	}
	if err := encryptString(vs.Config); err != nil {
		return fmt.Errorf("failed to encrypt vector store config: %w", err)
	}
	vs.EncryptionStatus = encryptionStatusEncrypted
	return nil
}

func decryptVectorStoreConfig(vs *tables.TableVectorStoreConfig) error {
	if vs.EncryptionStatus != encryptionStatusEncrypted || vs.Config == nil || *vs.Config == "" {
		return nil
	}
	if err := decryptString(vs.Config); err != nil {
		return fmt.Errorf("failed to decrypt vector store config: %w", err)
	}
	return nil
}

// --- Plugin config ---

func encryptPluginConfig(p *tables.TablePlugin) error {
	if !encrypt.IsEnabled() || p.ConfigJSON == "" {
		return nil
	}
	if err := encryptString(&p.ConfigJSON); err != nil {
		return fmt.Errorf("failed to encrypt plugin config: %w", err)
	}
	p.EncryptionStatus = encryptionStatusEncrypted
	return nil
}

func decryptPluginConfig(p *tables.TablePlugin) error {
	if p.EncryptionStatus != encryptionStatusEncrypted || p.ConfigJSON == "" {
		return nil
	}
	if err := decryptString(&p.ConfigJSON); err != nil {
		return fmt.Errorf("failed to decrypt plugin config: %w", err)
	}
	return nil
}

// --- Startup encryption pass ---

// EncryptPlaintextRows encrypts all rows with encryption_status='plain_text'
// across all sensitive tables. Called during startup when encryption is enabled.
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
				if err := encryptKeyFields(&keys[i]); err != nil {
					return err
				}
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

func (s *RDBConfigStore) encryptPlaintextVirtualKeys(ctx context.Context) (int, error) {
	var count int
	for {
		var vks []tables.TableVirtualKey
		if err := s.db.WithContext(ctx).
			Where("encryption_status = ? OR encryption_status IS NULL OR encryption_status = ''", encryptionStatusPlainText).
			Limit(encryptionBatchSize).
			Find(&vks).Error; err != nil {
			return count, err
		}
		if len(vks) == 0 {
			break
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for i := range vks {
				if err := encryptVirtualKeyValue(&vks[i]); err != nil {
					return err
				}
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

func (s *RDBConfigStore) encryptPlaintextSessions(ctx context.Context) (int, error) {
	var count int
	for {
		var sessions []tables.SessionsTable
		if err := s.db.WithContext(ctx).
			Where("encryption_status = ? OR encryption_status IS NULL OR encryption_status = ''", encryptionStatusPlainText).
			Limit(encryptionBatchSize).
			Find(&sessions).Error; err != nil {
			return count, err
		}
		if len(sessions) == 0 {
			break
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for i := range sessions {
				if err := encryptSessionToken(&sessions[i]); err != nil {
					return err
				}
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
				if err := encryptOAuthToken(&tokens[i]); err != nil {
					return err
				}
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

func (s *RDBConfigStore) encryptPlaintextOAuthConfigs(ctx context.Context) (int, error) {
	var count int
	for {
		var configs []tables.TableOauthConfig
		if err := s.db.WithContext(ctx).
			Where("encryption_status = ? OR encryption_status IS NULL OR encryption_status = ''", encryptionStatusPlainText).
			Limit(encryptionBatchSize).
			Find(&configs).Error; err != nil {
			return count, err
		}
		if len(configs) == 0 {
			break
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for i := range configs {
				if err := encryptOAuthConfig(&configs[i]); err != nil {
					return err
				}
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
				if err := encryptMCPClient(&clients[i]); err != nil {
					return err
				}
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
				if err := encryptProviderProxy(&providers[i]); err != nil {
					return err
				}
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
				if err := encryptVectorStoreConfig(&configs[i]); err != nil {
					return err
				}
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
				if err := encryptPluginConfig(&plugins[i]); err != nil {
					return err
				}
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
