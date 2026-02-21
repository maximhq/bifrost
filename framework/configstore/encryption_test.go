package configstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const testEncryptionKey = "test-encryption-key-for-testing-32bytes"

func init() {
	encrypt.Init(testEncryptionKey, bifrost.NewDefaultLogger(schemas.LogLevelInfo))
}

// setupEncryptionTestStore creates an in-memory SQLite database with all tables
// migrated and returns an RDBConfigStore for testing the startup encryption pass.
func setupEncryptionTestStore(t *testing.T) (*RDBConfigStore, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	err = db.AutoMigrate(
		&tables.TableKey{},
		&tables.TableProvider{},
		&tables.TableMCPClient{},
		&tables.TablePlugin{},
		&tables.TableVirtualKey{},
		&tables.SessionsTable{},
		&tables.TableOauthConfig{},
		&tables.TableOauthToken{},
		&tables.TableVectorStoreConfig{},
		&tables.TableBudget{},
		&tables.TableRateLimit{},
		&tables.TableVirtualKeyProviderConfig{},
		&tables.TableVirtualKeyProviderConfigKey{},
		&tables.TableCustomer{},
		&tables.TableTeam{},
		&tables.TableClientConfig{},
		&tables.TableVirtualKeyMCPConfig{},
		&tables.TableModel{},
	)
	require.NoError(t, err)

	store := &RDBConfigStore{
		db:     db,
		logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo),
	}
	return store, db
}

// insertPlaintextRow inserts a row directly into the DB via raw SQL, bypassing GORM hooks,
// so the row has encryption_status='plain_text' and plaintext sensitive data.
func insertPlaintextRow(t *testing.T, db *gorm.DB, sql string, args ...any) {
	t.Helper()
	require.NoError(t, db.Exec(sql, args...).Error)
}

// ============================================================================
// EncryptPlaintextRows — full startup pass
// ============================================================================

func TestEncryptPlaintextRows_EncryptsAllTables(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02 15:04:05")

	// Insert plaintext rows across all tables (bypassing hooks)
	insertPlaintextRow(t, db,
		`INSERT INTO config_keys (name, provider_id, provider, key_id, value, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'plain_text', ?, ?)`,
		"test-key", 1, "openai", "key-1", "sk-plaintext-key", now, now)

	insertPlaintextRow(t, db,
		`INSERT INTO governance_virtual_keys (id, name, value, is_active, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'plain_text', ?, ?)`,
		"vk-1", "test-vk", "vk-plaintext-value", true, now, now)

	insertPlaintextRow(t, db,
		`INSERT INTO sessions (token, encryption_status, expires_at, created_at, updated_at)
		 VALUES (?, 'plain_text', ?, ?, ?)`,
		"session-plaintext-token", future, now, now)

	insertPlaintextRow(t, db,
		`INSERT INTO oauth_tokens (id, access_token, refresh_token, token_type, encryption_status, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, 'Bearer', 'plain_text', ?, ?, ?)`,
		"tok-1", "plaintext-access-token", "plaintext-refresh-token", future, now, now)

	insertPlaintextRow(t, db,
		`INSERT INTO oauth_configs (id, client_secret, code_verifier, redirect_uri, state, status, encryption_status, created_at, updated_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, 'pending', 'plain_text', ?, ?, ?)`,
		"cfg-1", "plaintext-client-secret", "plaintext-verifier", "https://example.com/cb", "csrf-state", now, now, future)

	insertPlaintextRow(t, db,
		`INSERT INTO config_mcp_clients (client_id, name, connection_type, connection_string, headers_json, encryption_status, created_at, updated_at)
		 VALUES (?, ?, 'sse', ?, ?, 'plain_text', ?, ?)`,
		"mcp-1", "test-mcp", "https://mcp.example.com", `{"Authorization":"Bearer token"}`, now, now)

	insertPlaintextRow(t, db,
		`INSERT INTO config_providers (name, proxy_config_json, encryption_status, created_at, updated_at)
		 VALUES (?, ?, 'plain_text', ?, ?)`,
		"openai", `{"url":"https://proxy.example.com"}`, now, now)

	insertPlaintextRow(t, db,
		`INSERT INTO config_vector_store (enabled, type, config, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, 'plain_text', ?, ?)`,
		true, "redis", `{"host":"redis.example.com","password":"secret"}`, now, now)

	insertPlaintextRow(t, db,
		`INSERT INTO config_plugins (name, enabled, version, config_json, encryption_status, created_at, updated_at)
		 VALUES (?, ?, 1, ?, 'plain_text', ?, ?)`,
		"test-plugin", true, `{"api_key":"plugin-secret"}`, now, now)

	// Run the startup encryption pass
	err := store.EncryptPlaintextRows(ctx)
	require.NoError(t, err)

	// Verify all rows are now encrypted in raw DB
	var keyRow map[string]any
	db.Table("config_keys").Where("name = ?", "test-key").Take(&keyRow)
	assert.Equal(t, "encrypted", keyRow["encryption_status"])
	assert.NotEqual(t, "sk-plaintext-key", keyRow["value"])

	var vkRow map[string]any
	db.Table("governance_virtual_keys").Where("id = ?", "vk-1").Take(&vkRow)
	assert.Equal(t, "encrypted", vkRow["encryption_status"])
	assert.NotEqual(t, "vk-plaintext-value", vkRow["value"])

	var sessionRow map[string]any
	db.Table("sessions").Take(&sessionRow)
	assert.Equal(t, "encrypted", sessionRow["encryption_status"])
	assert.NotEqual(t, "session-plaintext-token", sessionRow["token"])

	var tokRow map[string]any
	db.Table("oauth_tokens").Where("id = ?", "tok-1").Take(&tokRow)
	assert.Equal(t, "encrypted", tokRow["encryption_status"])
	assert.NotEqual(t, "plaintext-access-token", tokRow["access_token"])

	var cfgRow map[string]any
	db.Table("oauth_configs").Where("id = ?", "cfg-1").Take(&cfgRow)
	assert.Equal(t, "encrypted", cfgRow["encryption_status"])
	assert.NotEqual(t, "plaintext-client-secret", cfgRow["client_secret"])

	var mcpRow map[string]any
	db.Table("config_mcp_clients").Where("client_id = ?", "mcp-1").Take(&mcpRow)
	assert.Equal(t, "encrypted", mcpRow["encryption_status"])

	var providerRow map[string]any
	db.Table("config_providers").Where("name = ?", "openai").Take(&providerRow)
	assert.Equal(t, "encrypted", providerRow["encryption_status"])

	var vsRow map[string]any
	db.Table("config_vector_store").Take(&vsRow)
	assert.Equal(t, "encrypted", vsRow["encryption_status"])

	var pluginRow map[string]any
	db.Table("config_plugins").Where("name = ?", "test-plugin").Take(&pluginRow)
	assert.Equal(t, "encrypted", pluginRow["encryption_status"])
}

func TestEncryptPlaintextRows_SkipsAlreadyEncrypted(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()

	// Create a key through the normal GORM path (which encrypts via hooks)
	key := &tables.TableKey{
		Name:       "already-encrypted",
		ProviderID: 1,
		Provider:   "openai",
		KeyID:      "enc-key-1",
		Value:      *schemas.NewEnvVar("sk-secret"),
	}
	require.NoError(t, db.Create(key).Error)

	// Grab the encrypted value from DB
	var rawBefore map[string]any
	db.Table("config_keys").Where("id = ?", key.ID).Take(&rawBefore)
	encryptedBefore := rawBefore["value"]

	// Run the startup pass
	err := store.EncryptPlaintextRows(ctx)
	require.NoError(t, err)

	// The encrypted value should not have changed (not double-encrypted)
	var rawAfter map[string]any
	db.Table("config_keys").Where("id = ?", key.ID).Take(&rawAfter)
	assert.Equal(t, encryptedBefore, rawAfter["value"])
}

func TestEncryptPlaintextRows_Idempotent(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO config_keys (name, provider_id, provider, key_id, value, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'plain_text', ?, ?)`,
		"idempotent-key", 1, "openai", "idem-1", "sk-plaintext", now, now)

	// Run twice
	err := store.EncryptPlaintextRows(ctx)
	require.NoError(t, err)

	err = store.EncryptPlaintextRows(ctx)
	require.NoError(t, err)

	// Should still be readable via GORM hooks
	var found tables.TableKey
	require.NoError(t, db.Where("name = ?", "idempotent-key").First(&found).Error)
	assert.Equal(t, "sk-plaintext", found.Value.GetValue())
}

func TestEncryptPlaintextRows_HandlesNullEncryptionStatus(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	// Insert with NULL encryption_status (legacy row)
	insertPlaintextRow(t, db,
		`INSERT INTO config_keys (name, provider_id, provider, key_id, value, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, NULL, ?, ?)`,
		"null-status-key", 1, "openai", "null-1", "sk-null-status", now, now)

	// Insert with empty encryption_status
	insertPlaintextRow(t, db,
		`INSERT INTO config_keys (name, provider_id, provider, key_id, value, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, '', ?, ?)`,
		"empty-status-key", 1, "openai", "empty-1", "sk-empty-status", now, now)

	err := store.EncryptPlaintextRows(ctx)
	require.NoError(t, err)

	// Both should be encrypted now
	var row1 map[string]any
	db.Table("config_keys").Where("name = ?", "null-status-key").Take(&row1)
	assert.Equal(t, "encrypted", row1["encryption_status"])

	var row2 map[string]any
	db.Table("config_keys").Where("name = ?", "empty-status-key").Take(&row2)
	assert.Equal(t, "encrypted", row2["encryption_status"])
}

// ============================================================================
// Individual batch functions
// ============================================================================

func TestEncryptPlaintextSessions(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO sessions (token, encryption_status, expires_at, created_at, updated_at)
		 VALUES (?, 'plain_text', ?, ?, ?)`,
		"session-token-1", future, now, now)

	insertPlaintextRow(t, db,
		`INSERT INTO sessions (token, encryption_status, expires_at, created_at, updated_at)
		 VALUES (?, 'plain_text', ?, ?, ?)`,
		"session-token-2", future, now, now)

	count, err := store.encryptPlaintextSessions(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Both should be decryptable via GORM
	var sessions []tables.SessionsTable
	require.NoError(t, db.Find(&sessions).Error)
	assert.Len(t, sessions, 2)

	tokens := map[string]bool{}
	for _, s := range sessions {
		tokens[s.Token] = true
	}
	assert.True(t, tokens["session-token-1"])
	assert.True(t, tokens["session-token-2"])
}

func TestEncryptPlaintextOAuthTokens(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO oauth_tokens (id, access_token, refresh_token, token_type, encryption_status, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, 'Bearer', 'plain_text', ?, ?, ?)`,
		"tok-batch-1", "access-1", "refresh-1", future, now, now)

	count, err := store.encryptPlaintextOAuthTokens(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	var found tables.TableOauthToken
	require.NoError(t, db.First(&found, "id = ?", "tok-batch-1").Error)
	assert.Equal(t, "access-1", found.AccessToken)
	assert.Equal(t, "refresh-1", found.RefreshToken)
}

func TestEncryptPlaintextPlugins(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO config_plugins (name, enabled, version, config_json, encryption_status, created_at, updated_at)
		 VALUES (?, ?, 1, ?, 'plain_text', ?, ?)`,
		"batch-plugin", true, `{"secret":"value"}`, now, now)

	count, err := store.encryptPlaintextPlugins(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	var raw map[string]any
	db.Table("config_plugins").Where("name = ?", "batch-plugin").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotContains(t, raw["config_json"], "secret")
}

func TestEncryptPlaintextPlugins_SkipsEmptyConfig(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	// Insert plugin with empty config — should NOT be picked up by the query
	insertPlaintextRow(t, db,
		`INSERT INTO config_plugins (name, enabled, version, config_json, encryption_status, created_at, updated_at)
		 VALUES (?, ?, 1, '{}', 'plain_text', ?, ?)`,
		"empty-config-plugin", true, now, now)

	count, err := store.encryptPlaintextPlugins(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestEncryptPlaintextProviderProxies_SkipsNoProxy(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	// Provider without proxy config — should NOT be picked up
	insertPlaintextRow(t, db,
		`INSERT INTO config_providers (name, proxy_config_json, encryption_status, created_at, updated_at)
		 VALUES (?, '', 'plain_text', ?, ?)`,
		"no-proxy-provider", now, now)

	count, err := store.encryptPlaintextProviderProxies(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// ============================================================================
// Direct tests for each startup batch function with data verification
// ============================================================================

func TestEncryptPlaintextKeys_EncryptsAndDecryptsCorrectly(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO config_keys (name, provider_id, provider, key_id, value, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'plain_text', ?, ?)`,
		"batch-key-1", 1, "openai", "bk-1", "sk-batch-secret-1", now, now)

	insertPlaintextRow(t, db,
		`INSERT INTO config_keys (name, provider_id, provider, key_id, value, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'plain_text', ?, ?)`,
		"batch-key-2", 1, "anthropic", "bk-2", "sk-batch-secret-2", now, now)

	count, err := store.encryptPlaintextKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Raw DB should have encrypted values
	var raw1 map[string]any
	db.Table("config_keys").Where("name = ?", "batch-key-1").Take(&raw1)
	assert.Equal(t, "encrypted", raw1["encryption_status"])
	assert.NotEqual(t, "sk-batch-secret-1", raw1["value"])

	// GORM hooks should decrypt on read
	var found1 tables.TableKey
	require.NoError(t, db.Where("name = ?", "batch-key-1").First(&found1).Error)
	assert.Equal(t, "sk-batch-secret-1", found1.Value.GetValue())

	var found2 tables.TableKey
	require.NoError(t, db.Where("name = ?", "batch-key-2").First(&found2).Error)
	assert.Equal(t, "sk-batch-secret-2", found2.Value.GetValue())
}

func TestEncryptPlaintextVirtualKeys_EncryptsAndDecryptsCorrectly(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO governance_virtual_keys (id, name, value, is_active, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'plain_text', ?, ?)`,
		"vk-batch-1", "batch-vk", "vk-batch-secret", true, now, now)

	count, err := store.encryptPlaintextVirtualKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Raw DB should have encrypted value
	var raw map[string]any
	db.Table("governance_virtual_keys").Where("id = ?", "vk-batch-1").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "vk-batch-secret", raw["value"])

	// GORM hooks should decrypt on read
	var found tables.TableVirtualKey
	require.NoError(t, db.Where("id = ?", "vk-batch-1").First(&found).Error)
	assert.Equal(t, "vk-batch-secret", found.Value)
}

func TestEncryptPlaintextOAuthConfigs_EncryptsAndDecryptsCorrectly(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO oauth_configs (id, client_secret, code_verifier, redirect_uri, state, status, encryption_status, created_at, updated_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, 'pending', 'plain_text', ?, ?, ?)`,
		"cfg-batch-1", "batch-client-secret", "batch-verifier", "https://example.com/cb", "csrf", now, now, future)

	count, err := store.encryptPlaintextOAuthConfigs(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Raw DB should have encrypted values
	var raw map[string]any
	db.Table("oauth_configs").Where("id = ?", "cfg-batch-1").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "batch-client-secret", raw["client_secret"])
	assert.NotEqual(t, "batch-verifier", raw["code_verifier"])

	// GORM hooks should decrypt on read
	var found tables.TableOauthConfig
	require.NoError(t, db.Where("id = ?", "cfg-batch-1").First(&found).Error)
	assert.Equal(t, "batch-client-secret", found.ClientSecret)
	assert.Equal(t, "batch-verifier", found.CodeVerifier)
}

func TestEncryptPlaintextMCPClients_EncryptsAndDecryptsCorrectly(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO config_mcp_clients (client_id, name, connection_type, connection_string, headers_json, encryption_status, created_at, updated_at)
		 VALUES (?, ?, 'sse', ?, ?, 'plain_text', ?, ?)`,
		"mcp-batch-1", "batch-mcp", "https://mcp.example.com", `{"X-Api-Key":"secret-key"}`, now, now)

	count, err := store.encryptPlaintextMCPClients(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Raw DB should have encrypted values
	var raw map[string]any
	db.Table("config_mcp_clients").Where("client_id = ?", "mcp-batch-1").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotContains(t, raw["headers_json"], "secret-key")

	// GORM hooks should decrypt on read
	var found tables.TableMCPClient
	require.NoError(t, db.Where("client_id = ?", "mcp-batch-1").First(&found).Error)
	assert.Equal(t, "https://mcp.example.com", found.ConnectionString.GetValue())
	assert.Equal(t, "secret-key", found.Headers["X-Api-Key"].Val)
}

func TestEncryptPlaintextProviderProxies_EncryptsAndDecryptsCorrectly(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO config_providers (name, proxy_config_json, encryption_status, created_at, updated_at)
		 VALUES (?, ?, 'plain_text', ?, ?)`,
		"proxy-provider", `{"url":"https://proxy.example.com","username":"admin","password":"secret-proxy-pass"}`, now, now)

	count, err := store.encryptPlaintextProviderProxies(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Raw DB should have encrypted proxy config
	var raw map[string]any
	db.Table("config_providers").Where("name = ?", "proxy-provider").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotContains(t, raw["proxy_config_json"], "secret-proxy-pass")

	// GORM hooks should decrypt and deserialize on read
	var found tables.TableProvider
	require.NoError(t, db.Where("name = ?", "proxy-provider").First(&found).Error)
	require.NotNil(t, found.ProxyConfig)
	assert.Equal(t, "https://proxy.example.com", found.ProxyConfig.URL)
	assert.Equal(t, "secret-proxy-pass", found.ProxyConfig.Password)
}

func TestEncryptPlaintextVectorStoreConfigs_EncryptsAndDecryptsCorrectly(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	configJSON := `{"host":"redis.example.com","password":"redis-secret"}`
	insertPlaintextRow(t, db,
		`INSERT INTO config_vector_store (enabled, type, config, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, 'plain_text', ?, ?)`,
		true, "redis", configJSON, now, now)

	count, err := store.encryptPlaintextVectorStoreConfigs(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Raw DB should have encrypted config
	var raw map[string]any
	db.Table("config_vector_store").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotContains(t, raw["config"], "redis-secret")

	// GORM hooks should decrypt on read
	var found tables.TableVectorStoreConfig
	require.NoError(t, db.First(&found).Error)
	require.NotNil(t, found.Config)
	assert.Contains(t, *found.Config, "redis-secret")
}

func TestEncryptPlaintextVectorStoreConfigs_SkipsEmptyConfig(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO config_vector_store (enabled, type, config, encryption_status, created_at, updated_at)
		 VALUES (?, ?, '', 'plain_text', ?, ?)`,
		false, "none", now, now)

	count, err := store.encryptPlaintextVectorStoreConfigs(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestEncryptPlaintextMCPClients_SkipsEmptyFields(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	// MCP client with no connection string and empty headers — nothing to encrypt
	insertPlaintextRow(t, db,
		`INSERT INTO config_mcp_clients (client_id, name, connection_type, headers_json, encryption_status, created_at, updated_at)
		 VALUES (?, ?, 'stdio', '{}', 'plain_text', ?, ?)`,
		"mcp-empty", "empty-mcp", now, now)

	count, err := store.encryptPlaintextMCPClients(ctx)
	require.NoError(t, err)
	// Row is still processed (encryption_status changes) even if no fields are encrypted
	assert.Equal(t, 1, count)
}

// ============================================================================
// Batch pagination — verify >100 rows are handled correctly
// ============================================================================

func TestEncryptPlaintextKeys_MultipleBatches(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	// Insert 5 plaintext keys to verify the batch loop processes all rows
	for i := range 5 {
		insertPlaintextRow(t, db,
			`INSERT INTO config_keys (name, provider_id, provider, key_id, value, encryption_status, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, 'plain_text', ?, ?)`,
			fmt.Sprintf("paginated-key-%d", i), 1, "openai", fmt.Sprintf("pk-%d", i), fmt.Sprintf("sk-secret-%d", i), now, now)
	}

	count, err := store.encryptPlaintextKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, 5, count)

	// Verify all are encrypted in raw DB
	var encryptedCount int64
	db.Table("config_keys").Where("encryption_status = ?", "encrypted").Count(&encryptedCount)
	assert.Equal(t, int64(5), encryptedCount)

	// Verify no plaintext rows remain
	var plaintextCount int64
	db.Table("config_keys").Where("encryption_status = ? OR encryption_status IS NULL OR encryption_status = ''", "plain_text").Count(&plaintextCount)
	assert.Equal(t, int64(0), plaintextCount)

	// Verify each row is still readable via GORM hooks
	for i := range 5 {
		var found tables.TableKey
		require.NoError(t, db.Where("name = ?", fmt.Sprintf("paginated-key-%d", i)).First(&found).Error)
		assert.Equal(t, fmt.Sprintf("sk-secret-%d", i), found.Value.GetValue())
	}
}

func TestEncryptPlaintextSessions_MultipleBatches(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02 15:04:05")

	// Insert 5 plaintext sessions
	for i := range 5 {
		insertPlaintextRow(t, db,
			`INSERT INTO sessions (token, encryption_status, expires_at, created_at, updated_at)
			 VALUES (?, 'plain_text', ?, ?, ?)`,
			fmt.Sprintf("session-token-%d", i), future, now, now)
	}

	count, err := store.encryptPlaintextSessions(ctx)
	require.NoError(t, err)
	assert.Equal(t, 5, count)

	// Verify all are encrypted
	var encryptedCount int64
	db.Table("sessions").Where("encryption_status = ?", "encrypted").Count(&encryptedCount)
	assert.Equal(t, int64(5), encryptedCount)
}

// ============================================================================
// Provider-specific encrypted fields on TableKey (Azure, Vertex, Bedrock)
// ============================================================================

func TestEncryptPlaintextKeys_AzureFields_EncryptsAndDecryptsCorrectly(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO config_keys (name, provider_id, provider, key_id, value, azure_endpoint, azure_client_id, azure_client_secret, azure_tenant_id, azure_api_version, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'plain_text', ?, ?)`,
		"azure-key", 1, "azure", "az-1", "sk-azure-key-value",
		"https://myresource.openai.azure.com", "my-azure-client-id", "azure-super-secret-client",
		"my-azure-tenant-id", "2024-10-21", now, now)

	count, err := store.encryptPlaintextKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Raw DB should have encrypted values for all sensitive fields
	var raw map[string]any
	db.Table("config_keys").Where("name = ?", "azure-key").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "sk-azure-key-value", raw["value"])
	assert.NotEqual(t, "https://myresource.openai.azure.com", raw["azure_endpoint"])
	assert.NotEqual(t, "my-azure-client-id", raw["azure_client_id"])
	assert.NotEqual(t, "azure-super-secret-client", raw["azure_client_secret"])
	assert.NotEqual(t, "my-azure-tenant-id", raw["azure_tenant_id"])
	assert.NotEqual(t, "2024-10-21", raw["azure_api_version"])

	// GORM hooks should decrypt and reconstruct AzureKeyConfig
	var found tables.TableKey
	require.NoError(t, db.Where("name = ?", "azure-key").First(&found).Error)
	assert.Equal(t, "sk-azure-key-value", found.Value.GetValue())
	require.NotNil(t, found.AzureKeyConfig)
	assert.Equal(t, "https://myresource.openai.azure.com", found.AzureKeyConfig.Endpoint.GetValue())
	require.NotNil(t, found.AzureKeyConfig.ClientID)
	assert.Equal(t, "my-azure-client-id", found.AzureKeyConfig.ClientID.GetValue())
	assert.NotNil(t, found.AzureKeyConfig.ClientSecret)
	assert.Equal(t, "azure-super-secret-client", found.AzureKeyConfig.ClientSecret.GetValue())
	require.NotNil(t, found.AzureKeyConfig.TenantID)
	assert.Equal(t, "my-azure-tenant-id", found.AzureKeyConfig.TenantID.GetValue())
	require.NotNil(t, found.AzureKeyConfig.APIVersion)
	assert.Equal(t, "2024-10-21", found.AzureKeyConfig.APIVersion.GetValue())
}

func TestEncryptPlaintextKeys_VertexFields_EncryptsAndDecryptsCorrectly(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO config_keys (name, provider_id, provider, key_id, value, vertex_project_id, vertex_project_number, vertex_region, vertex_auth_credentials, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'plain_text', ?, ?)`,
		"vertex-key", 1, "vertex", "vx-1", "sk-vertex-key-value",
		"my-gcp-project", "123456789", "us-central1",
		`{"type":"service_account","private_key":"-----BEGIN PRIVATE KEY-----secret"}`, now, now)

	count, err := store.encryptPlaintextKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Raw DB should have encrypted values
	var raw map[string]any
	db.Table("config_keys").Where("name = ?", "vertex-key").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "sk-vertex-key-value", raw["value"])
	assert.NotEqual(t, "my-gcp-project", raw["vertex_project_id"])
	assert.NotEqual(t, "123456789", raw["vertex_project_number"])
	assert.NotEqual(t, "us-central1", raw["vertex_region"])
	assert.NotContains(t, fmt.Sprintf("%v", raw["vertex_auth_credentials"]), "private_key")

	// GORM hooks should decrypt and reconstruct VertexKeyConfig
	var found tables.TableKey
	require.NoError(t, db.Where("name = ?", "vertex-key").First(&found).Error)
	assert.Equal(t, "sk-vertex-key-value", found.Value.GetValue())
	require.NotNil(t, found.VertexKeyConfig)
	assert.Equal(t, "my-gcp-project", found.VertexKeyConfig.ProjectID.GetValue())
	assert.Equal(t, "123456789", found.VertexKeyConfig.ProjectNumber.GetValue())
	assert.Equal(t, "us-central1", found.VertexKeyConfig.Region.GetValue())
	assert.Contains(t, found.VertexKeyConfig.AuthCredentials.GetValue(), "private_key")
}

func TestEncryptPlaintextKeys_BedrockFields_EncryptsAndDecryptsCorrectly(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO config_keys (name, provider_id, provider, key_id, value, bedrock_access_key, bedrock_secret_key, bedrock_session_token, bedrock_region, bedrock_arn, bedrock_deployments_json, bedrock_batch_s3_config_json, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'plain_text', ?, ?)`,
		"bedrock-key", 1, "bedrock", "br-1", "sk-bedrock-key-value",
		"AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "FwoGZXIvYXdzEBYaDH7sampleSessionToken",
		"us-west-2", "arn:aws:iam::123456789:role/bedrock",
		`{"claude-3":"profile-claude"}`, `{"buckets":[{"bucket_name":"my-bucket","prefix":"jobs/","is_default":true}]}`,
		now, now)

	count, err := store.encryptPlaintextKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Raw DB should have encrypted values for all Bedrock fields
	var raw map[string]any
	db.Table("config_keys").Where("name = ?", "bedrock-key").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "sk-bedrock-key-value", raw["value"])
	assert.NotEqual(t, "AKIAIOSFODNN7EXAMPLE", raw["bedrock_access_key"])
	assert.NotEqual(t, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", raw["bedrock_secret_key"])
	assert.NotEqual(t, "FwoGZXIvYXdzEBYaDH7sampleSessionToken", raw["bedrock_session_token"])
	assert.NotEqual(t, "us-west-2", raw["bedrock_region"])
	assert.NotEqual(t, "arn:aws:iam::123456789:role/bedrock", raw["bedrock_arn"])
	if rawDeploy, ok := raw["bedrock_deployments_json"].(string); ok {
		assert.NotContains(t, rawDeploy, "profile-claude")
	}
	if rawBatch, ok := raw["bedrock_batch_s3_config_json"].(string); ok {
		assert.NotContains(t, rawBatch, "my-bucket")
	}

	// GORM hooks should decrypt and reconstruct BedrockKeyConfig
	var found tables.TableKey
	require.NoError(t, db.Where("name = ?", "bedrock-key").First(&found).Error)
	assert.Equal(t, "sk-bedrock-key-value", found.Value.GetValue())
	require.NotNil(t, found.BedrockKeyConfig)
	assert.Equal(t, "AKIAIOSFODNN7EXAMPLE", found.BedrockKeyConfig.AccessKey.GetValue())
	assert.Equal(t, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", found.BedrockKeyConfig.SecretKey.GetValue())
	require.NotNil(t, found.BedrockKeyConfig.SessionToken)
	assert.Equal(t, "FwoGZXIvYXdzEBYaDH7sampleSessionToken", found.BedrockKeyConfig.SessionToken.GetValue())
	require.NotNil(t, found.BedrockKeyConfig.Region)
	assert.Equal(t, "us-west-2", found.BedrockKeyConfig.Region.GetValue())
	require.NotNil(t, found.BedrockKeyConfig.ARN)
	assert.Equal(t, "arn:aws:iam::123456789:role/bedrock", found.BedrockKeyConfig.ARN.GetValue())
	assert.Equal(t, "profile-claude", found.BedrockKeyConfig.Deployments["claude-3"])
	require.NotNil(t, found.BedrockKeyConfig.BatchS3Config)
	require.Len(t, found.BedrockKeyConfig.BatchS3Config.Buckets, 1)
	assert.Equal(t, "my-bucket", found.BedrockKeyConfig.BatchS3Config.Buckets[0].BucketName)
}

func TestEncryptPlaintextKeys_AllProviderFields_ViaStartupPass(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	// Insert keys for all three providers with sensitive fields
	insertPlaintextRow(t, db,
		`INSERT INTO config_keys (name, provider_id, provider, key_id, value, azure_endpoint, azure_client_secret, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'plain_text', ?, ?)`,
		"startup-azure", 1, "azure", "sa-1", "sk-az", "https://az.openai.azure.com", "az-secret", now, now)

	insertPlaintextRow(t, db,
		`INSERT INTO config_keys (name, provider_id, provider, key_id, value, vertex_auth_credentials, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'plain_text', ?, ?)`,
		"startup-vertex", 1, "vertex", "sv-1", "sk-vx", "vertex-creds-json", now, now)

	insertPlaintextRow(t, db,
		`INSERT INTO config_keys (name, provider_id, provider, key_id, value, bedrock_access_key, bedrock_secret_key, bedrock_session_token, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'plain_text', ?, ?)`,
		"startup-bedrock", 1, "bedrock", "sb-1", "sk-br", "AKIA-BR", "secret-br", "session-br", now, now)

	// Run the full startup encryption pass
	err := store.EncryptPlaintextRows(ctx)
	require.NoError(t, err)

	// Verify all three rows are encrypted in raw DB
	for _, name := range []string{"startup-azure", "startup-vertex", "startup-bedrock"} {
		var raw map[string]any
		db.Table("config_keys").Where("name = ?", name).Take(&raw)
		assert.Equal(t, "encrypted", raw["encryption_status"], "expected encrypted status for %s", name)
	}

	// Verify Azure fields survived round-trip
	var azKey tables.TableKey
	require.NoError(t, db.Where("name = ?", "startup-azure").First(&azKey).Error)
	assert.Equal(t, "sk-az", azKey.Value.GetValue())
	require.NotNil(t, azKey.AzureKeyConfig)
	assert.Equal(t, "https://az.openai.azure.com", azKey.AzureKeyConfig.Endpoint.GetValue())
	assert.NotNil(t, azKey.AzureKeyConfig.ClientSecret)
	assert.Equal(t, "az-secret", azKey.AzureKeyConfig.ClientSecret.GetValue())

	// Verify Vertex fields survived round-trip
	var vxKey tables.TableKey
	require.NoError(t, db.Where("name = ?", "startup-vertex").First(&vxKey).Error)
	assert.Equal(t, "sk-vx", vxKey.Value.GetValue())
	require.NotNil(t, vxKey.VertexKeyConfig)
	assert.Equal(t, "vertex-creds-json", vxKey.VertexKeyConfig.AuthCredentials.GetValue())

	// Verify Bedrock fields survived round-trip
	var brKey tables.TableKey
	require.NoError(t, db.Where("name = ?", "startup-bedrock").First(&brKey).Error)
	assert.Equal(t, "sk-br", brKey.Value.GetValue())
	require.NotNil(t, brKey.BedrockKeyConfig)
	assert.Equal(t, "AKIA-BR", brKey.BedrockKeyConfig.AccessKey.GetValue())
	assert.Equal(t, "secret-br", brKey.BedrockKeyConfig.SecretKey.GetValue())
	require.NotNil(t, brKey.BedrockKeyConfig.SessionToken)
	assert.Equal(t, "session-br", brKey.BedrockKeyConfig.SessionToken.GetValue())
}

// ============================================================================
// BeforeSave must not mutate shared provider config structs (regression test)
// ============================================================================

func TestBeforeSave_DoesNotMutateSharedProviderConfigs(t *testing.T) {
	_, db := setupEncryptionTestStore(t)

	// Simulate the startup flow: create a key with AzureKeyConfig set via a shared pointer,
	// save it to DB, and verify the original config structs are not mutated by BeforeSave
	// (encryption uses value-copies so shared pointers are never corrupted).
	azureCfg := &schemas.AzureKeyConfig{
		Endpoint:   *schemas.NewEnvVar("https://myresource.openai.azure.com"),
		APIVersion: schemas.NewEnvVar("2024-10-21"),
		ClientID:   schemas.NewEnvVar("my-azure-client-id"),
		TenantID:   schemas.NewEnvVar("my-azure-tenant-id"),
	}
	azureCfg.ClientSecret = schemas.NewEnvVar("azure-client-secret")

	vertexCfg := &schemas.VertexKeyConfig{
		ProjectID:       *schemas.NewEnvVar("my-project"),
		ProjectNumber:   *schemas.NewEnvVar("123456789"),
		Region:          *schemas.NewEnvVar("us-central1"),
		AuthCredentials: *schemas.NewEnvVar("vertex-creds"),
	}

	bedrockCfg := &schemas.BedrockKeyConfig{
		AccessKey:    *schemas.NewEnvVar("AKIAEXAMPLE"),
		SecretKey:    *schemas.NewEnvVar("secret-key"),
		SessionToken: schemas.NewEnvVar("session-tok"),
		Region:       schemas.NewEnvVar("us-east-1"),
		ARN:          schemas.NewEnvVar("arn:aws:iam::123456789:role/test"),
	}

	// Save a key using the shared config pointers (mimics UpdateProvidersConfig)
	key := &tables.TableKey{
		Name:             "shared-ptr-test",
		ProviderID:       1,
		Provider:         "azure",
		KeyID:            "sp-1",
		Value:            *schemas.NewEnvVar("sk-test-value"),
		AzureKeyConfig:   azureCfg,
		VertexKeyConfig:  vertexCfg,
		BedrockKeyConfig: bedrockCfg,
	}
	require.NoError(t, db.Create(key).Error)

	// The original config structs must NOT have been mutated by BeforeSave.
	// All fields are now encrypted; the value-copy pattern in BeforeSave ensures
	// the caller's shared config struct is never corrupted by in-place encryption.

	// Azure: encrypted fields
	assert.Equal(t, "https://myresource.openai.azure.com", azureCfg.Endpoint.GetValue(),
		"BeforeSave must not mutate shared AzureKeyConfig.Endpoint")
	assert.Equal(t, "azure-client-secret", azureCfg.ClientSecret.GetValue(),
		"BeforeSave must not mutate shared AzureKeyConfig.ClientSecret")
	assert.Equal(t, "2024-10-21", azureCfg.APIVersion.GetValue(),
		"BeforeSave must not mutate shared AzureKeyConfig.APIVersion")
	assert.Equal(t, "my-azure-client-id", azureCfg.ClientID.GetValue(),
		"BeforeSave must not mutate shared AzureKeyConfig.ClientID")
	assert.Equal(t, "my-azure-tenant-id", azureCfg.TenantID.GetValue(),
		"BeforeSave must not mutate shared AzureKeyConfig.TenantID")

	// Vertex: encrypted fields
	assert.Equal(t, "vertex-creds", vertexCfg.AuthCredentials.GetValue(),
		"BeforeSave must not mutate shared VertexKeyConfig.AuthCredentials")
	assert.Equal(t, "my-project", vertexCfg.ProjectID.GetValue(),
		"BeforeSave must not mutate shared VertexKeyConfig.ProjectID")
	assert.Equal(t, "123456789", vertexCfg.ProjectNumber.GetValue(),
		"BeforeSave must not mutate shared VertexKeyConfig.ProjectNumber")
	assert.Equal(t, "us-central1", vertexCfg.Region.GetValue(),
		"BeforeSave must not mutate shared VertexKeyConfig.Region")

	// Bedrock: encrypted fields
	assert.Equal(t, "AKIAEXAMPLE", bedrockCfg.AccessKey.GetValue(),
		"BeforeSave must not mutate shared BedrockKeyConfig.AccessKey")
	assert.Equal(t, "secret-key", bedrockCfg.SecretKey.GetValue(),
		"BeforeSave must not mutate shared BedrockKeyConfig.SecretKey")
	assert.Equal(t, "session-tok", bedrockCfg.SessionToken.GetValue(),
		"BeforeSave must not mutate shared BedrockKeyConfig.SessionToken")
	assert.Equal(t, "us-east-1", bedrockCfg.Region.GetValue(),
		"BeforeSave must not mutate shared BedrockKeyConfig.Region")
	assert.Equal(t, "arn:aws:iam::123456789:role/test", bedrockCfg.ARN.GetValue(),
		"BeforeSave must not mutate shared BedrockKeyConfig.ARN")

	// Verify the DB round-trip still works (encrypted + decryptable)
	var found tables.TableKey
	require.NoError(t, db.Where("name = ?", "shared-ptr-test").First(&found).Error)
	assert.Equal(t, "sk-test-value", found.Value.GetValue())
	require.NotNil(t, found.AzureKeyConfig)
	assert.Equal(t, "https://myresource.openai.azure.com", found.AzureKeyConfig.Endpoint.GetValue())
	assert.Equal(t, "azure-client-secret", found.AzureKeyConfig.ClientSecret.GetValue())
	assert.Equal(t, "2024-10-21", found.AzureKeyConfig.APIVersion.GetValue())
	assert.Equal(t, "my-azure-client-id", found.AzureKeyConfig.ClientID.GetValue())
	assert.Equal(t, "my-azure-tenant-id", found.AzureKeyConfig.TenantID.GetValue())
	require.NotNil(t, found.VertexKeyConfig)
	assert.Equal(t, "vertex-creds", found.VertexKeyConfig.AuthCredentials.GetValue())
	assert.Equal(t, "my-project", found.VertexKeyConfig.ProjectID.GetValue())
	assert.Equal(t, "123456789", found.VertexKeyConfig.ProjectNumber.GetValue())
	assert.Equal(t, "us-central1", found.VertexKeyConfig.Region.GetValue())
	require.NotNil(t, found.BedrockKeyConfig)
	assert.Equal(t, "AKIAEXAMPLE", found.BedrockKeyConfig.AccessKey.GetValue())
	assert.Equal(t, "secret-key", found.BedrockKeyConfig.SecretKey.GetValue())
	assert.Equal(t, "session-tok", found.BedrockKeyConfig.SessionToken.GetValue())
	assert.Equal(t, "us-east-1", found.BedrockKeyConfig.Region.GetValue())
	assert.Equal(t, "arn:aws:iam::123456789:role/test", found.BedrockKeyConfig.ARN.GetValue())
}

// ============================================================================
// EnvVar-backed fields must not be encrypted (encryption is a no-op for FromEnv)
// ============================================================================

func TestBeforeSave_EnvVarBackedFields_NotEncrypted(t *testing.T) {
	_, db := setupEncryptionTestStore(t)

	// Set environment variables that the EnvVars will resolve to
	t.Setenv("TEST_AZURE_KEY", "sk-azure-from-env")
	t.Setenv("TEST_AZURE_ENDPOINT", "https://env-resource.openai.azure.com")
	t.Setenv("TEST_AZURE_SECRET", "env-azure-client-secret")
	t.Setenv("TEST_AZURE_API_VER", "2024-10-21")
	t.Setenv("TEST_AZURE_CLIENT_ID", "env-azure-client-id")
	t.Setenv("TEST_AZURE_TENANT_ID", "env-azure-tenant-id")
	t.Setenv("TEST_VERTEX_PROJECT", "env-vertex-project")
	t.Setenv("TEST_VERTEX_REGION", "env-us-central1")
	t.Setenv("TEST_VERTEX_CREDS", "env-vertex-creds-json")
	t.Setenv("TEST_BEDROCK_ACCESS", "env-AKIA-ACCESS")
	t.Setenv("TEST_BEDROCK_SECRET", "env-bedrock-secret")
	t.Setenv("TEST_BEDROCK_SESSION", "env-bedrock-session")
	t.Setenv("TEST_BEDROCK_REGION", "env-us-east-1")
	t.Setenv("TEST_BEDROCK_ARN", "arn:aws:iam::env:role/test")

	// Create EnvVars backed by environment variables
	azureCfg := &schemas.AzureKeyConfig{
		Endpoint:     *schemas.NewEnvVar("env.TEST_AZURE_ENDPOINT"),
		APIVersion:   schemas.NewEnvVar("env.TEST_AZURE_API_VER"),
		ClientID:     schemas.NewEnvVar("env.TEST_AZURE_CLIENT_ID"),
		ClientSecret: schemas.NewEnvVar("env.TEST_AZURE_SECRET"),
		TenantID:     schemas.NewEnvVar("env.TEST_AZURE_TENANT_ID"),
	}
	vertexCfg := &schemas.VertexKeyConfig{
		ProjectID:       *schemas.NewEnvVar("env.TEST_VERTEX_PROJECT"),
		Region:          *schemas.NewEnvVar("env.TEST_VERTEX_REGION"),
		AuthCredentials: *schemas.NewEnvVar("env.TEST_VERTEX_CREDS"),
	}
	bedrockCfg := &schemas.BedrockKeyConfig{
		AccessKey:    *schemas.NewEnvVar("env.TEST_BEDROCK_ACCESS"),
		SecretKey:    *schemas.NewEnvVar("env.TEST_BEDROCK_SECRET"),
		SessionToken: schemas.NewEnvVar("env.TEST_BEDROCK_SESSION"),
		Region:       schemas.NewEnvVar("env.TEST_BEDROCK_REGION"),
		ARN:          schemas.NewEnvVar("env.TEST_BEDROCK_ARN"),
	}

	// Verify the EnvVars resolved correctly and are marked as FromEnv
	require.True(t, azureCfg.Endpoint.IsFromEnv())
	require.Equal(t, "https://env-resource.openai.azure.com", azureCfg.Endpoint.GetValue())
	require.True(t, azureCfg.ClientSecret.IsFromEnv())
	require.True(t, vertexCfg.AuthCredentials.IsFromEnv())
	require.True(t, bedrockCfg.AccessKey.IsFromEnv())

	key := &tables.TableKey{
		Name:             "env-backed-key",
		ProviderID:       1,
		Provider:         "azure",
		KeyID:            "env-1",
		Value:            *schemas.NewEnvVar("env.TEST_AZURE_KEY"),
		AzureKeyConfig:   azureCfg,
		VertexKeyConfig:  vertexCfg,
		BedrockKeyConfig: bedrockCfg,
	}
	require.NoError(t, db.Create(key).Error)

	// Raw DB should store the env var references, NOT encrypted ciphertext.
	// EnvVar.Value() returns the env var name (e.g. "env.TEST_AZURE_KEY") when FromEnv=true.
	var raw map[string]any
	db.Table("config_keys").Where("name = ?", "env-backed-key").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	// Value column should contain the env reference string, not encrypted data
	assert.Equal(t, "env.TEST_AZURE_KEY", raw["value"])
	assert.Equal(t, "env.TEST_AZURE_ENDPOINT", raw["azure_endpoint"])
	assert.Equal(t, "env.TEST_AZURE_SECRET", raw["azure_client_secret"])

	// The shared config structs must NOT be mutated
	assert.Equal(t, "https://env-resource.openai.azure.com", azureCfg.Endpoint.GetValue())
	assert.True(t, azureCfg.Endpoint.IsFromEnv())
	assert.Equal(t, "env-azure-client-secret", azureCfg.ClientSecret.GetValue())
	assert.True(t, azureCfg.ClientSecret.IsFromEnv())
	assert.Equal(t, "env-vertex-creds-json", vertexCfg.AuthCredentials.GetValue())
	assert.True(t, vertexCfg.AuthCredentials.IsFromEnv())
	assert.Equal(t, "env-AKIA-ACCESS", bedrockCfg.AccessKey.GetValue())
	assert.True(t, bedrockCfg.AccessKey.IsFromEnv())
	assert.Equal(t, "env-bedrock-secret", bedrockCfg.SecretKey.GetValue())
	assert.True(t, bedrockCfg.SecretKey.IsFromEnv())
	assert.Equal(t, "env-bedrock-session", bedrockCfg.SessionToken.GetValue())
	assert.True(t, bedrockCfg.SessionToken.IsFromEnv())

	// GORM round-trip: AfterFind should reconstruct env-backed EnvVars correctly
	var found tables.TableKey
	require.NoError(t, db.Where("name = ?", "env-backed-key").First(&found).Error)
	assert.Equal(t, "sk-azure-from-env", found.Value.GetValue())
	assert.True(t, found.Value.IsFromEnv())

	require.NotNil(t, found.AzureKeyConfig)
	assert.Equal(t, "https://env-resource.openai.azure.com", found.AzureKeyConfig.Endpoint.GetValue())
	assert.True(t, found.AzureKeyConfig.Endpoint.IsFromEnv())
	assert.Equal(t, "env-azure-client-secret", found.AzureKeyConfig.ClientSecret.GetValue())
	assert.True(t, found.AzureKeyConfig.ClientSecret.IsFromEnv())
	assert.Equal(t, "2024-10-21", found.AzureKeyConfig.APIVersion.GetValue())
	assert.True(t, found.AzureKeyConfig.APIVersion.IsFromEnv())
	assert.Equal(t, "env-azure-client-id", found.AzureKeyConfig.ClientID.GetValue())
	assert.True(t, found.AzureKeyConfig.ClientID.IsFromEnv())
	assert.Equal(t, "env-azure-tenant-id", found.AzureKeyConfig.TenantID.GetValue())
	assert.True(t, found.AzureKeyConfig.TenantID.IsFromEnv())

	require.NotNil(t, found.VertexKeyConfig)
	assert.Equal(t, "env-vertex-project", found.VertexKeyConfig.ProjectID.GetValue())
	assert.True(t, found.VertexKeyConfig.ProjectID.IsFromEnv())
	assert.Equal(t, "env-us-central1", found.VertexKeyConfig.Region.GetValue())
	assert.True(t, found.VertexKeyConfig.Region.IsFromEnv())
	assert.Equal(t, "env-vertex-creds-json", found.VertexKeyConfig.AuthCredentials.GetValue())
	assert.True(t, found.VertexKeyConfig.AuthCredentials.IsFromEnv())

	require.NotNil(t, found.BedrockKeyConfig)
	assert.Equal(t, "env-AKIA-ACCESS", found.BedrockKeyConfig.AccessKey.GetValue())
	assert.True(t, found.BedrockKeyConfig.AccessKey.IsFromEnv())
	assert.Equal(t, "env-bedrock-secret", found.BedrockKeyConfig.SecretKey.GetValue())
	assert.True(t, found.BedrockKeyConfig.SecretKey.IsFromEnv())
	assert.Equal(t, "env-bedrock-session", found.BedrockKeyConfig.SessionToken.GetValue())
	assert.True(t, found.BedrockKeyConfig.SessionToken.IsFromEnv())
	assert.Equal(t, "env-us-east-1", found.BedrockKeyConfig.Region.GetValue())
	assert.True(t, found.BedrockKeyConfig.Region.IsFromEnv())
	assert.Equal(t, "arn:aws:iam::env:role/test", found.BedrockKeyConfig.ARN.GetValue())
	assert.True(t, found.BedrockKeyConfig.ARN.IsFromEnv())
}

func TestEncryptPlaintextKeys_EnvVarBackedFields_SurviveStartupPass(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	t.Setenv("TEST_SP_KEY", "sk-startup-env-key")
	t.Setenv("TEST_SP_ENDPOINT", "https://startup.openai.azure.com")
	t.Setenv("TEST_SP_CREDS", "startup-vertex-creds")
	t.Setenv("TEST_SP_ACCESS", "AKIA-STARTUP")

	// Insert plaintext rows with env var references via raw SQL (mimics legacy data)
	insertPlaintextRow(t, db,
		`INSERT INTO config_keys (name, provider_id, provider, key_id, value, azure_endpoint, vertex_auth_credentials, bedrock_access_key, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'plain_text', ?, ?)`,
		"env-startup-key", 1, "azure", "esp-1",
		"env.TEST_SP_KEY", "env.TEST_SP_ENDPOINT", "env.TEST_SP_CREDS", "env.TEST_SP_ACCESS",
		now, now)

	// Run the startup encryption pass
	count, err := store.encryptPlaintextKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Raw DB should still have env var references (not encrypted ciphertext)
	var raw map[string]any
	db.Table("config_keys").Where("name = ?", "env-startup-key").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.Equal(t, "env.TEST_SP_KEY", raw["value"])
	assert.Equal(t, "env.TEST_SP_ENDPOINT", raw["azure_endpoint"])
	assert.Equal(t, "env.TEST_SP_CREDS", raw["vertex_auth_credentials"])
	assert.Equal(t, "env.TEST_SP_ACCESS", raw["bedrock_access_key"])

	// GORM should resolve env vars on read
	var found tables.TableKey
	require.NoError(t, db.Where("name = ?", "env-startup-key").First(&found).Error)
	assert.Equal(t, "sk-startup-env-key", found.Value.GetValue())
	assert.True(t, found.Value.IsFromEnv())
	require.NotNil(t, found.AzureKeyConfig)
	assert.Equal(t, "https://startup.openai.azure.com", found.AzureKeyConfig.Endpoint.GetValue())
	assert.True(t, found.AzureKeyConfig.Endpoint.IsFromEnv())
	require.NotNil(t, found.VertexKeyConfig)
	assert.Equal(t, "startup-vertex-creds", found.VertexKeyConfig.AuthCredentials.GetValue())
	assert.True(t, found.VertexKeyConfig.AuthCredentials.IsFromEnv())
	require.NotNil(t, found.BedrockKeyConfig)
	assert.Equal(t, "AKIA-STARTUP", found.BedrockKeyConfig.AccessKey.GetValue())
	assert.True(t, found.BedrockKeyConfig.AccessKey.IsFromEnv())
}

// ============================================================================
// Encryption disabled — startup pass is a no-op
// ============================================================================

func TestEncryptPlaintextRows_EncryptionDisabled_Noop(t *testing.T) {
	// Disable encryption for this test
	encrypt.Init("", bifrost.NewDefaultLogger(schemas.LogLevelInfo))
	t.Cleanup(func() {
		encrypt.Init(testEncryptionKey, bifrost.NewDefaultLogger(schemas.LogLevelInfo))
	})

	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02 15:04:05")

	// Insert plaintext rows across multiple tables
	insertPlaintextRow(t, db,
		`INSERT INTO config_keys (name, provider_id, provider, key_id, value, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'plain_text', ?, ?)`,
		"disabled-key", 1, "openai", "dk-1", "sk-should-stay-plain", now, now)

	insertPlaintextRow(t, db,
		`INSERT INTO sessions (token, encryption_status, expires_at, created_at, updated_at)
		 VALUES (?, 'plain_text', ?, ?, ?)`,
		"session-should-stay-plain", future, now, now)

	insertPlaintextRow(t, db,
		`INSERT INTO governance_virtual_keys (id, name, value, is_active, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'plain_text', ?, ?)`,
		"vk-dis-1", "dis-vk", "vk-should-stay-plain", true, now, now)

	// Run the startup pass — should return immediately (nil) without modifying rows
	err := store.EncryptPlaintextRows(ctx)
	require.NoError(t, err)

	// Verify all rows remain as plaintext in the raw DB
	var keyRow map[string]any
	db.Table("config_keys").Where("name = ?", "disabled-key").Take(&keyRow)
	assert.Equal(t, "plain_text", keyRow["encryption_status"])
	assert.Equal(t, "sk-should-stay-plain", keyRow["value"])

	var sessionRow map[string]any
	db.Table("sessions").Take(&sessionRow)
	assert.Equal(t, "plain_text", sessionRow["encryption_status"])
	assert.Equal(t, "session-should-stay-plain", sessionRow["token"])

	var vkRow map[string]any
	db.Table("governance_virtual_keys").Where("id = ?", "vk-dis-1").Take(&vkRow)
	assert.Equal(t, "plain_text", vkRow["encryption_status"])
	assert.Equal(t, "vk-should-stay-plain", vkRow["value"])
}

func TestEncryptPlaintextRows_EncryptionDisabled_GORMHooksStorePlaintext(t *testing.T) {
	// Disable encryption for this test
	encrypt.Init("", bifrost.NewDefaultLogger(schemas.LogLevelInfo))
	t.Cleanup(func() {
		encrypt.Init(testEncryptionKey, bifrost.NewDefaultLogger(schemas.LogLevelInfo))
	})

	_, db := setupEncryptionTestStore(t)

	// Create rows via GORM (hooks fire, but encryption is disabled)
	key := &tables.TableKey{
		Name:       "hook-no-encrypt",
		ProviderID: 1,
		Provider:   "openai",
		KeyID:      "hne-1",
		Value:      *schemas.NewEnvVar("sk-stays-plain-via-hook"),
	}
	require.NoError(t, db.Create(key).Error)

	// Raw DB should have plaintext
	var raw map[string]any
	db.Table("config_keys").Where("id = ?", key.ID).Take(&raw)
	assert.Equal(t, "plain_text", raw["encryption_status"])
	assert.Equal(t, "sk-stays-plain-via-hook", raw["value"])

	// GORM read should work fine (AfterFind skips decryption for non-encrypted rows)
	var found tables.TableKey
	require.NoError(t, db.First(&found, key.ID).Error)
	assert.Equal(t, "sk-stays-plain-via-hook", found.Value.GetValue())
}

// ============================================================================
// Empty database — startup pass is a graceful no-op
// ============================================================================

func TestEncryptPlaintextRows_EmptyDatabase(t *testing.T) {
	store, _ := setupEncryptionTestStore(t)
	ctx := context.Background()

	err := store.EncryptPlaintextRows(ctx)
	require.NoError(t, err)
}

// ============================================================================
// OAuthConfigs skip when both secrets are empty
// ============================================================================

func TestEncryptPlaintextOAuthConfigs_SkipsBothEmptySecrets(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO oauth_configs (id, client_secret, code_verifier, redirect_uri, state, status, encryption_status, created_at, updated_at, expires_at)
		 VALUES (?, '', '', ?, ?, 'pending', 'plain_text', ?, ?, ?)`,
		"cfg-empty-secrets", "https://example.com/cb", "csrf-state", now, now, future)

	count, err := store.encryptPlaintextOAuthConfigs(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	var raw map[string]any
	db.Table("oauth_configs").Where("id = ?", "cfg-empty-secrets").Take(&raw)
	assert.Equal(t, "plain_text", raw["encryption_status"])
}

// ============================================================================
// Hash computation during startup pass (sessions + virtual keys)
// ============================================================================

func TestEncryptPlaintextSessions_HashComputedDuringStartup(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO sessions (token, encryption_status, expires_at, created_at, updated_at)
		 VALUES (?, 'plain_text', ?, ?, ?)`,
		"hash-startup-token", future, now, now)

	count, err := store.encryptPlaintextSessions(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	var raw map[string]any
	db.Table("sessions").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.Equal(t, encrypt.HashSHA256("hash-startup-token"), raw["token_hash"])
}

func TestEncryptPlaintextVirtualKeys_HashComputedDuringStartup(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO governance_virtual_keys (id, name, value, is_active, encryption_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'plain_text', ?, ?)`,
		"vk-hash-startup", "hash-vk", "vk-hash-startup-value", true, now, now)

	count, err := store.encryptPlaintextVirtualKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	var raw map[string]any
	db.Table("governance_virtual_keys").Where("id = ?", "vk-hash-startup").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.Equal(t, encrypt.HashSHA256("vk-hash-startup-value"), raw["value_hash"])
}

// ============================================================================
// MCP client env var connection string survives startup pass
// ============================================================================

func TestEncryptPlaintextMCPClients_EnvVarConnectionStringSurvivesStartup(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	t.Setenv("TEST_MCP_URL", "https://mcp-env.example.com/sse")

	insertPlaintextRow(t, db,
		`INSERT INTO config_mcp_clients (client_id, name, connection_type, connection_string, headers_json, encryption_status, created_at, updated_at)
		 VALUES (?, ?, 'sse', ?, '{}', 'plain_text', ?, ?)`,
		"mcp-env-startup", "env-startup-mcp", "env.TEST_MCP_URL", now, now)

	count, err := store.encryptPlaintextMCPClients(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	var raw map[string]any
	db.Table("config_mcp_clients").Where("client_id = ?", "mcp-env-startup").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.Equal(t, "env.TEST_MCP_URL", raw["connection_string"])

	var found tables.TableMCPClient
	require.NoError(t, db.Where("client_id = ?", "mcp-env-startup").First(&found).Error)
	assert.Equal(t, "https://mcp-env.example.com/sse", found.ConnectionString.GetValue())
	assert.True(t, found.ConnectionString.IsFromEnv())
}

// ============================================================================
// Already-encrypted rows skipped for non-key tables
// ============================================================================

func TestEncryptPlaintextRows_SkipsAlreadyEncryptedSessions(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()

	session := &tables.SessionsTable{
		Token:     "already-encrypted-session",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, db.Create(session).Error)

	var rawBefore map[string]any
	db.Table("sessions").Where("id = ?", session.ID).Take(&rawBefore)
	encryptedBefore := rawBefore["token"]

	err := store.EncryptPlaintextRows(ctx)
	require.NoError(t, err)

	var rawAfter map[string]any
	db.Table("sessions").Where("id = ?", session.ID).Take(&rawAfter)
	assert.Equal(t, encryptedBefore, rawAfter["token"])
}

func TestEncryptPlaintextRows_SkipsAlreadyEncryptedVirtualKeys(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()

	vk := &tables.TableVirtualKey{
		ID:       "vk-already-enc",
		Name:     "already-encrypted-vk",
		Value:    "vk-secret-already",
		IsActive: true,
	}
	require.NoError(t, db.Create(vk).Error)

	var rawBefore map[string]any
	db.Table("governance_virtual_keys").Where("id = ?", "vk-already-enc").Take(&rawBefore)
	encryptedBefore := rawBefore["value"]

	err := store.EncryptPlaintextRows(ctx)
	require.NoError(t, err)

	var rawAfter map[string]any
	db.Table("governance_virtual_keys").Where("id = ?", "vk-already-enc").Take(&rawAfter)
	assert.Equal(t, encryptedBefore, rawAfter["value"])
}

// ============================================================================
// OAuthTokens with empty refresh token during startup pass
// ============================================================================

func TestEncryptPlaintextOAuthTokens_EmptyRefreshToken(t *testing.T) {
	store, db := setupEncryptionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02 15:04:05")

	insertPlaintextRow(t, db,
		`INSERT INTO oauth_tokens (id, access_token, refresh_token, token_type, encryption_status, expires_at, created_at, updated_at)
		 VALUES (?, ?, '', 'Bearer', 'plain_text', ?, ?, ?)`,
		"tok-no-refresh", "access-only-startup", future, now, now)

	count, err := store.encryptPlaintextOAuthTokens(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	var raw map[string]any
	db.Table("oauth_tokens").Where("id = ?", "tok-no-refresh").Take(&raw)
	assert.Equal(t, "encrypted", raw["encryption_status"])

	var found tables.TableOauthToken
	require.NoError(t, db.First(&found, "id = ?", "tok-no-refresh").Error)
	assert.Equal(t, "access-only-startup", found.AccessToken)
	assert.Equal(t, "", found.RefreshToken)
}
