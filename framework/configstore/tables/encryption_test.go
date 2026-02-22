package tables

import (
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
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

// setupTestDB creates an in-memory SQLite database with all tables migrated.
func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	err = db.AutoMigrate(
		&TableKey{},
		&TableProvider{},
		&TableMCPClient{},
		&TablePlugin{},
		&TableVirtualKey{},
		&SessionsTable{},
		&TableOauthConfig{},
		&TableOauthToken{},
		&TableVectorStoreConfig{},
	)
	require.NoError(t, err)
	return db
}

// rawRow reads a single row from the given table without triggering GORM hooks,
// returning the raw column values as a map. This lets tests verify that encrypted
// ciphertext is actually stored in the database.
func rawRow(t *testing.T, db *gorm.DB, table string, id any) map[string]any {
	t.Helper()
	var row map[string]any
	err := db.Table(table).Where("id = ?", id).Take(&row).Error
	require.NoError(t, err)
	return row
}

// ============================================================================
// TableKey encryption tests
// ============================================================================

func TestTableKey_EncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	key := &TableKey{
		Name:       "test-key",
		ProviderID: 1,
		Provider:   "openai",
		KeyID:      "key-uuid-1",
		Value:      *schemas.NewEnvVar("sk-secret-api-key"),
	}

	require.NoError(t, db.Create(key).Error)

	// Verify raw DB has encrypted value
	raw := rawRow(t, db, "config_keys", key.ID)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "sk-secret-api-key", raw["value"])

	// Verify reading back decrypts
	var found TableKey
	require.NoError(t, db.First(&found, key.ID).Error)
	assert.Equal(t, "sk-secret-api-key", found.Value.GetValue())
}

func TestTableKey_AzureFieldsEncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	endpoint := schemas.NewEnvVar("https://my-azure.openai.azure.com")
	clientSecret := schemas.NewEnvVar("azure-secret-123")
	apiVersion := schemas.NewEnvVar("2024-10-21")

	key := &TableKey{
		Name:       "azure-key",
		ProviderID: 1,
		Provider:   "azure",
		KeyID:      "azure-uuid-1",
		Value:      *schemas.NewEnvVar("azure-api-key"),
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:     *endpoint,
			ClientID:     schemas.NewEnvVar("azure-client-id-123"),
			ClientSecret: clientSecret,
			TenantID:     schemas.NewEnvVar("azure-tenant-id-456"),
			APIVersion:   apiVersion,
		},
	}

	require.NoError(t, db.Create(key).Error)

	// Verify raw DB has encrypted values
	raw := rawRow(t, db, "config_keys", key.ID)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "https://my-azure.openai.azure.com", raw["azure_endpoint"])
	assert.NotEqual(t, "azure-client-id-123", raw["azure_client_id"])
	assert.NotEqual(t, "azure-secret-123", raw["azure_client_secret"])
	assert.NotEqual(t, "azure-tenant-id-456", raw["azure_tenant_id"])
	assert.NotEqual(t, "2024-10-21", raw["azure_api_version"])

	// Verify reading back decrypts and reconstructs AzureKeyConfig
	var found TableKey
	require.NoError(t, db.First(&found, key.ID).Error)
	assert.Equal(t, "azure-api-key", found.Value.GetValue())
	require.NotNil(t, found.AzureKeyConfig)
	assert.Equal(t, "https://my-azure.openai.azure.com", found.AzureKeyConfig.Endpoint.GetValue())
	require.NotNil(t, found.AzureKeyConfig.ClientID)
	assert.Equal(t, "azure-client-id-123", found.AzureKeyConfig.ClientID.GetValue())
	assert.Equal(t, "azure-secret-123", found.AzureKeyConfig.ClientSecret.GetValue())
	require.NotNil(t, found.AzureKeyConfig.TenantID)
	assert.Equal(t, "azure-tenant-id-456", found.AzureKeyConfig.TenantID.GetValue())
	require.NotNil(t, found.AzureKeyConfig.APIVersion)
	assert.Equal(t, "2024-10-21", found.AzureKeyConfig.APIVersion.GetValue())
}

func TestTableKey_VertexFieldsEncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	key := &TableKey{
		Name:       "vertex-key",
		ProviderID: 1,
		Provider:   "vertex",
		KeyID:      "vertex-uuid-1",
		Value:      *schemas.NewEnvVar("vertex-api-key"),
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID:       *schemas.NewEnvVar("my-project"),
			ProjectNumber:   *schemas.NewEnvVar("123456789"),
			Region:          *schemas.NewEnvVar("us-central1"),
			AuthCredentials: *schemas.NewEnvVar(`{"type":"service_account"}`),
		},
	}

	require.NoError(t, db.Create(key).Error)

	raw := rawRow(t, db, "config_keys", key.ID)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "my-project", raw["vertex_project_id"])
	assert.NotEqual(t, "123456789", raw["vertex_project_number"])
	assert.NotEqual(t, "us-central1", raw["vertex_region"])
	assert.NotEqual(t, `{"type":"service_account"}`, raw["vertex_auth_credentials"])

	var found TableKey
	require.NoError(t, db.First(&found, key.ID).Error)
	require.NotNil(t, found.VertexKeyConfig)
	assert.Equal(t, "my-project", found.VertexKeyConfig.ProjectID.GetValue())
	assert.Equal(t, "123456789", found.VertexKeyConfig.ProjectNumber.GetValue())
	assert.Equal(t, "us-central1", found.VertexKeyConfig.Region.GetValue())
	assert.Equal(t, `{"type":"service_account"}`, found.VertexKeyConfig.AuthCredentials.GetValue())
}

func TestTableKey_BedrockFieldsEncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	key := &TableKey{
		Name:       "bedrock-key",
		ProviderID: 1,
		Provider:   "bedrock",
		KeyID:      "bedrock-uuid-1",
		Value:      *schemas.NewEnvVar("bedrock-val"),
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			AccessKey:   *schemas.NewEnvVar("AKIAIOSFODNN7EXAMPLE"),
			SecretKey:   *schemas.NewEnvVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
			Region:      schemas.NewEnvVar("us-west-2"),
			ARN:         schemas.NewEnvVar("arn:aws:iam::123456789:role/test"),
			Deployments: map[string]string{"model-a": "profile-a"},
			BatchS3Config: &schemas.BatchS3Config{
				Buckets: []schemas.S3BucketConfig{
					{BucketName: "my-batch-bucket", Prefix: "jobs/", IsDefault: true},
				},
			},
		},
	}

	require.NoError(t, db.Create(key).Error)

	raw := rawRow(t, db, "config_keys", key.ID)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "AKIAIOSFODNN7EXAMPLE", raw["bedrock_access_key"])
	assert.NotEqual(t, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", raw["bedrock_secret_key"])
	assert.NotEqual(t, "us-west-2", raw["bedrock_region"])
	assert.NotEqual(t, "arn:aws:iam::123456789:role/test", raw["bedrock_arn"])
	if rawDeploy, ok := raw["bedrock_deployments_json"].(string); ok {
		assert.NotContains(t, rawDeploy, "profile-a")
	}
	if rawBatch, ok := raw["bedrock_batch_s3_config_json"].(string); ok {
		assert.NotContains(t, rawBatch, "my-batch-bucket")
	}

	var found TableKey
	require.NoError(t, db.First(&found, key.ID).Error)
	require.NotNil(t, found.BedrockKeyConfig)
	assert.Equal(t, "AKIAIOSFODNN7EXAMPLE", found.BedrockKeyConfig.AccessKey.GetValue())
	assert.Equal(t, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", found.BedrockKeyConfig.SecretKey.GetValue())
	require.NotNil(t, found.BedrockKeyConfig.Region)
	assert.Equal(t, "us-west-2", found.BedrockKeyConfig.Region.GetValue())
	require.NotNil(t, found.BedrockKeyConfig.ARN)
	assert.Equal(t, "arn:aws:iam::123456789:role/test", found.BedrockKeyConfig.ARN.GetValue())
	assert.Equal(t, "profile-a", found.BedrockKeyConfig.Deployments["model-a"])
	require.NotNil(t, found.BedrockKeyConfig.BatchS3Config)
	require.Len(t, found.BedrockKeyConfig.BatchS3Config.Buckets, 1)
	assert.Equal(t, "my-batch-bucket", found.BedrockKeyConfig.BatchS3Config.Buckets[0].BucketName)
	assert.Equal(t, "jobs/", found.BedrockKeyConfig.BatchS3Config.Buckets[0].Prefix)
	assert.True(t, found.BedrockKeyConfig.BatchS3Config.Buckets[0].IsDefault)
}

func TestTableKey_EnvVarNotEncrypted(t *testing.T) {
	db := setupTestDB(t)

	// When the value comes from an env var, it should NOT be encrypted
	key := &TableKey{
		Name:       "env-key",
		ProviderID: 1,
		Provider:   "openai",
		KeyID:      "env-uuid-1",
		Value:      *schemas.NewEnvVar("env.OPENAI_API_KEY"),
	}

	require.NoError(t, db.Create(key).Error)

	var found TableKey
	require.NoError(t, db.First(&found, key.ID).Error)
	// The value should be readable (either the env var value or empty if not set)
	assert.True(t, found.Value.IsFromEnv())
}

// ============================================================================
// TableProvider encryption tests
// ============================================================================

func TestTableProvider_ProxyConfigEncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	proxyConfig := &schemas.ProxyConfig{
		URL: "https://proxy.example.com",
	}

	provider := &TableProvider{
		Name:        "openai",
		ProxyConfig: proxyConfig,
	}

	require.NoError(t, db.Create(provider).Error)

	raw := rawRow(t, db, "config_providers", provider.ID)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	// The proxy config JSON should be encrypted (not valid JSON anymore)
	rawProxy, ok := raw["proxy_config_json"].(string)
	assert.True(t, ok)
	assert.NotContains(t, rawProxy, "proxy.example.com")

	var found TableProvider
	require.NoError(t, db.First(&found, provider.ID).Error)
	require.NotNil(t, found.ProxyConfig)
	assert.Equal(t, "https://proxy.example.com", found.ProxyConfig.URL)
}

func TestTableProvider_NoProxyConfig_NoEncryption(t *testing.T) {
	db := setupTestDB(t)

	provider := &TableProvider{
		Name: "anthropic",
	}

	require.NoError(t, db.Create(provider).Error)

	raw := rawRow(t, db, "config_providers", provider.ID)
	// Without proxy config, encryption status should remain plain_text
	assert.NotEqual(t, "encrypted", raw["encryption_status"])
}

// ============================================================================
// TableMCPClient encryption tests
// ============================================================================

func TestTableMCPClient_EncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	connStr := schemas.NewEnvVar("https://mcp-server.example.com/sse")
	client := &TableMCPClient{
		ClientID:         "mcp-1",
		Name:             "test-mcp",
		ConnectionType:   "sse",
		ConnectionString: connStr,
		Headers: map[string]schemas.EnvVar{
			"Authorization": *schemas.NewEnvVar("Bearer secret-token"),
		},
	}

	require.NoError(t, db.Create(client).Error)

	raw := rawRow(t, db, "config_mcp_clients", client.ID)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	// Connection string should be encrypted
	rawConnStr, ok := raw["connection_string"].(string)
	assert.True(t, ok)
	assert.NotContains(t, rawConnStr, "mcp-server.example.com")
	// Headers JSON should be encrypted
	rawHeaders, ok := raw["headers_json"].(string)
	assert.True(t, ok)
	assert.NotContains(t, rawHeaders, "secret-token")

	var found TableMCPClient
	require.NoError(t, db.First(&found, client.ID).Error)
	assert.Equal(t, "https://mcp-server.example.com/sse", found.ConnectionString.GetValue())
	require.Contains(t, found.Headers, "Authorization")
	assert.Equal(t, "Bearer secret-token", found.Headers["Authorization"].Val)
}

func TestTableMCPClient_EnvVarConnectionString_NotEncrypted(t *testing.T) {
	db := setupTestDB(t)

	connStr := schemas.NewEnvVar("env.MCP_SERVER_URL")
	client := &TableMCPClient{
		ClientID:         "mcp-env",
		Name:             "env-mcp",
		ConnectionType:   "sse",
		ConnectionString: connStr,
	}

	require.NoError(t, db.Create(client).Error)

	var found TableMCPClient
	require.NoError(t, db.First(&found, client.ID).Error)
	assert.True(t, found.ConnectionString.IsFromEnv())
}

// ============================================================================
// TablePlugin encryption tests
// ============================================================================

func TestTablePlugin_EncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	plugin := &TablePlugin{
		Name:    "test-plugin",
		Enabled: true,
		Version: 1,
		Config:  map[string]any{"api_key": "secret-plugin-key", "endpoint": "https://plugin.example.com"},
	}

	require.NoError(t, db.Create(plugin).Error)

	raw := rawRow(t, db, "config_plugins", plugin.ID)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	rawConfig, ok := raw["config_json"].(string)
	assert.True(t, ok)
	assert.NotContains(t, rawConfig, "secret-plugin-key")

	var found TablePlugin
	require.NoError(t, db.First(&found, plugin.ID).Error)
	configMap, ok := found.Config.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "secret-plugin-key", configMap["api_key"])
}

func TestTablePlugin_EmptyConfig_NoEncryption(t *testing.T) {
	db := setupTestDB(t)

	plugin := &TablePlugin{
		Name:    "empty-plugin",
		Enabled: true,
		Version: 1,
		// nil Config will serialize to "{}"
	}

	require.NoError(t, db.Create(plugin).Error)

	raw := rawRow(t, db, "config_plugins", plugin.ID)
	assert.NotEqual(t, "encrypted", raw["encryption_status"])
}

// ============================================================================
// TableVirtualKey encryption tests
// ============================================================================

func TestTableVirtualKey_EncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	vk := &TableVirtualKey{
		ID:       "vk-1",
		Name:     "test-vk",
		Value:    "vk-secret-value-xyz",
		IsActive: true,
	}

	require.NoError(t, db.Create(vk).Error)

	raw := rawRow(t, db, "governance_virtual_keys", "vk-1")
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "vk-secret-value-xyz", raw["value"])

	// Verify hash was computed from plaintext
	expectedHash := encrypt.HashSHA256("vk-secret-value-xyz")
	assert.Equal(t, expectedHash, raw["value_hash"])

	var found TableVirtualKey
	require.NoError(t, db.First(&found, "id = ?", "vk-1").Error)
	assert.Equal(t, "vk-secret-value-xyz", found.Value)
	assert.Equal(t, expectedHash, found.ValueHash)
}

func TestTableVirtualKey_HashComputedBeforeEncryption(t *testing.T) {
	db := setupTestDB(t)

	vk := &TableVirtualKey{
		ID:       "vk-hash",
		Name:     "hash-test",
		Value:    "plaintext-value",
		IsActive: true,
	}

	require.NoError(t, db.Create(vk).Error)

	raw := rawRow(t, db, "governance_virtual_keys", "vk-hash")
	// Hash should be of the plaintext, not the ciphertext
	assert.Equal(t, encrypt.HashSHA256("plaintext-value"), raw["value_hash"])
}

// ============================================================================
// SessionsTable encryption tests
// ============================================================================

func TestSessionsTable_EncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	session := &SessionsTable{
		Token:     "session-secret-token-abc",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}

	require.NoError(t, db.Create(session).Error)

	raw := rawRow(t, db, "sessions", session.ID)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "session-secret-token-abc", raw["token"])

	// Verify hash was computed from plaintext
	expectedHash := encrypt.HashSHA256("session-secret-token-abc")
	assert.Equal(t, expectedHash, raw["token_hash"])

	var found SessionsTable
	require.NoError(t, db.First(&found, session.ID).Error)
	assert.Equal(t, "session-secret-token-abc", found.Token)
}

func TestSessionsTable_HashComputedBeforeEncryption(t *testing.T) {
	db := setupTestDB(t)

	session := &SessionsTable{
		Token:     "hash-test-token",
		ExpiresAt: time.Now().Add(time.Hour),
	}

	require.NoError(t, db.Create(session).Error)

	raw := rawRow(t, db, "sessions", session.ID)
	assert.Equal(t, encrypt.HashSHA256("hash-test-token"), raw["token_hash"])
}

// ============================================================================
// TableOauthConfig encryption tests
// ============================================================================

func TestTableOauthConfig_EncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	config := &TableOauthConfig{
		ID:           "oauth-cfg-1",
		ClientID:     "client-id-public",
		ClientSecret: "super-secret-client-secret",
		RedirectURI:  "https://example.com/callback",
		State:        "csrf-state-token",
		CodeVerifier: "pkce-code-verifier-secret",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
	}

	require.NoError(t, db.Create(config).Error)

	raw := rawRow(t, db, "oauth_configs", "oauth-cfg-1")
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "super-secret-client-secret", raw["client_secret"])
	assert.NotEqual(t, "pkce-code-verifier-secret", raw["code_verifier"])

	var found TableOauthConfig
	require.NoError(t, db.First(&found, "id = ?", "oauth-cfg-1").Error)
	assert.Equal(t, "super-secret-client-secret", found.ClientSecret)
	assert.Equal(t, "pkce-code-verifier-secret", found.CodeVerifier)
	// Non-sensitive fields should be unchanged
	assert.Equal(t, "client-id-public", found.ClientID)
	assert.Equal(t, "https://example.com/callback", found.RedirectURI)
}

func TestTableOauthConfig_EmptySecret_NoError(t *testing.T) {
	db := setupTestDB(t)

	config := &TableOauthConfig{
		ID:          "oauth-cfg-empty",
		RedirectURI: "https://example.com/callback",
		State:       "csrf-state-2",
		ExpiresAt:   time.Now().Add(15 * time.Minute),
	}

	require.NoError(t, db.Create(config).Error)

	var found TableOauthConfig
	require.NoError(t, db.First(&found, "id = ?", "oauth-cfg-empty").Error)
	assert.Equal(t, "", found.ClientSecret)
	assert.Equal(t, "", found.CodeVerifier)
}

// ============================================================================
// TableOauthToken encryption tests
// ============================================================================

func TestTableOauthToken_EncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	token := &TableOauthToken{
		ID:           "oauth-tok-1",
		AccessToken:  "access-token-secret-value",
		RefreshToken: "refresh-token-secret-value",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
	}

	require.NoError(t, db.Create(token).Error)

	raw := rawRow(t, db, "oauth_tokens", "oauth-tok-1")
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "access-token-secret-value", raw["access_token"])
	assert.NotEqual(t, "refresh-token-secret-value", raw["refresh_token"])

	var found TableOauthToken
	require.NoError(t, db.First(&found, "id = ?", "oauth-tok-1").Error)
	assert.Equal(t, "access-token-secret-value", found.AccessToken)
	assert.Equal(t, "refresh-token-secret-value", found.RefreshToken)
}

func TestTableOauthToken_EmptyRefreshToken(t *testing.T) {
	db := setupTestDB(t)

	token := &TableOauthToken{
		ID:          "oauth-tok-norefresh",
		AccessToken: "access-only-token",
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(time.Hour),
	}

	require.NoError(t, db.Create(token).Error)

	var found TableOauthToken
	require.NoError(t, db.First(&found, "id = ?", "oauth-tok-norefresh").Error)
	assert.Equal(t, "access-only-token", found.AccessToken)
	assert.Equal(t, "", found.RefreshToken)
}

// ============================================================================
// TableVectorStoreConfig encryption tests
// ============================================================================

func TestTableVectorStoreConfig_EncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	configJSON := `{"host":"redis.example.com","port":6379,"password":"redis-secret"}`
	vs := &TableVectorStoreConfig{
		Enabled: true,
		Type:    "redis",
		Config:  &configJSON,
	}

	require.NoError(t, db.Create(vs).Error)

	raw := rawRow(t, db, "config_vector_store", vs.ID)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	rawConfig, ok := raw["config"].(string)
	assert.True(t, ok)
	assert.NotContains(t, rawConfig, "redis-secret")

	var found TableVectorStoreConfig
	require.NoError(t, db.First(&found, vs.ID).Error)
	require.NotNil(t, found.Config)
	assert.Contains(t, *found.Config, "redis-secret")
	assert.Contains(t, *found.Config, "redis.example.com")
}

func TestTableVectorStoreConfig_NilConfig_NoEncryption(t *testing.T) {
	db := setupTestDB(t)

	vs := &TableVectorStoreConfig{
		Enabled: false,
		Type:    "redis",
	}

	require.NoError(t, db.Create(vs).Error)

	raw := rawRow(t, db, "config_vector_store", vs.ID)
	assert.NotEqual(t, "encrypted", raw["encryption_status"])
}

// ============================================================================
// Round-trip: save, read, update, read again
// ============================================================================

func TestTableKey_UpdatePreservesDecryption(t *testing.T) {
	db := setupTestDB(t)

	key := &TableKey{
		Name:       "update-key",
		ProviderID: 1,
		Provider:   "openai",
		KeyID:      "update-uuid",
		Value:      *schemas.NewEnvVar("original-key"),
	}
	require.NoError(t, db.Create(key).Error)

	// Read back
	var found TableKey
	require.NoError(t, db.First(&found, key.ID).Error)
	assert.Equal(t, "original-key", found.Value.GetValue())

	// Update value
	found.Value = *schemas.NewEnvVar("updated-key")
	require.NoError(t, db.Save(&found).Error)

	// Read again
	var found2 TableKey
	require.NoError(t, db.First(&found2, key.ID).Error)
	assert.Equal(t, "updated-key", found2.Value.GetValue())

	// Verify DB still has encrypted value
	raw := rawRow(t, db, "config_keys", key.ID)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "updated-key", raw["value"])
}

func TestSessionsTable_UpdatePreservesDecryption(t *testing.T) {
	db := setupTestDB(t)

	session := &SessionsTable{
		Token:     "original-token",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, db.Create(session).Error)

	var found SessionsTable
	require.NoError(t, db.First(&found, session.ID).Error)
	assert.Equal(t, "original-token", found.Token)

	found.Token = "updated-token"
	require.NoError(t, db.Save(&found).Error)

	var found2 SessionsTable
	require.NoError(t, db.First(&found2, session.ID).Error)
	assert.Equal(t, "updated-token", found2.Token)

	raw := rawRow(t, db, "sessions", session.ID)
	assert.Equal(t, encrypt.HashSHA256("updated-token"), raw["token_hash"])
}

// ============================================================================
// Helper function unit tests
// ============================================================================

func TestEncryptString_NilIsNoop(t *testing.T) {
	require.NoError(t, encryptString(nil))
}

func TestEncryptString_EmptyIsNoop(t *testing.T) {
	s := ""
	require.NoError(t, encryptString(&s))
	assert.Equal(t, "", s)
}

func TestDecryptString_NilIsNoop(t *testing.T) {
	require.NoError(t, decryptString(nil))
}

func TestDecryptString_EmptyIsNoop(t *testing.T) {
	s := ""
	require.NoError(t, decryptString(&s))
	assert.Equal(t, "", s)
}

func TestEncryptDecryptString_RoundTrip(t *testing.T) {
	original := "my-secret-value"
	s := original
	require.NoError(t, encryptString(&s))
	assert.NotEqual(t, original, s, "should be encrypted")
	require.NoError(t, decryptString(&s))
	assert.Equal(t, original, s, "should match after decrypt")
}

func TestEncryptEnvVar_NilIsNoop(t *testing.T) {
	require.NoError(t, encryptEnvVar(nil))
}

func TestEncryptEnvVar_EmptyValueIsNoop(t *testing.T) {
	ev := schemas.NewEnvVar("")
	require.NoError(t, encryptEnvVar(ev))
	assert.Equal(t, "", ev.Val)
}

func TestEncryptEnvVar_EnvRefIsNoop(t *testing.T) {
	ev := schemas.NewEnvVar("env.MY_VAR")
	originalVal := ev.Val
	require.NoError(t, encryptEnvVar(ev))
	// Value should not change — env var references are never encrypted
	assert.Equal(t, originalVal, ev.Val)
	assert.True(t, ev.IsFromEnv())
}

func TestDecryptEnvVar_NilIsNoop(t *testing.T) {
	require.NoError(t, decryptEnvVar(nil))
}

func TestDecryptEnvVar_EmptyValueIsNoop(t *testing.T) {
	ev := schemas.NewEnvVar("")
	require.NoError(t, decryptEnvVar(ev))
	assert.Equal(t, "", ev.Val)
}

func TestDecryptEnvVar_EnvRefIsNoop(t *testing.T) {
	ev := schemas.NewEnvVar("env.MY_VAR")
	originalVal := ev.Val
	require.NoError(t, decryptEnvVar(ev))
	assert.Equal(t, originalVal, ev.Val)
}

func TestEncryptDecryptEnvVar_RoundTrip(t *testing.T) {
	ev := schemas.NewEnvVar("super-secret")
	require.NoError(t, encryptEnvVar(ev))
	assert.NotEqual(t, "super-secret", ev.Val)
	require.NoError(t, decryptEnvVar(ev))
	assert.Equal(t, "super-secret", ev.Val)
}

func TestEncryptEnvVarPtr_NilOuterIsNoop(t *testing.T) {
	require.NoError(t, encryptEnvVarPtr(nil))
}

func TestEncryptEnvVarPtr_NilInnerIsNoop(t *testing.T) {
	var ev *schemas.EnvVar
	require.NoError(t, encryptEnvVarPtr(&ev))
}

func TestDecryptEnvVarPtr_NilOuterIsNoop(t *testing.T) {
	require.NoError(t, decryptEnvVarPtr(nil))
}

func TestDecryptEnvVarPtr_NilInnerIsNoop(t *testing.T) {
	var ev *schemas.EnvVar
	require.NoError(t, decryptEnvVarPtr(&ev))
}

func TestEncryptDecryptEnvVarPtr_RoundTrip(t *testing.T) {
	ev := schemas.NewEnvVar("ptr-secret")
	require.NoError(t, encryptEnvVarPtr(&ev))
	assert.NotEqual(t, "ptr-secret", ev.Val)
	require.NoError(t, decryptEnvVarPtr(&ev))
	assert.Equal(t, "ptr-secret", ev.Val)
}

// ============================================================================
// TableKey — BedrockSessionToken (pointer EnvVar field)
// ============================================================================

func TestTableKey_BedrockSessionTokenEncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	sessionToken := schemas.NewEnvVar("FwoGZXIvYXdzEBYaDH...")
	region := schemas.NewEnvVar("us-east-1")

	key := &TableKey{
		Name:       "bedrock-session-key",
		ProviderID: 1,
		Provider:   "bedrock",
		KeyID:      "bedrock-st-uuid",
		Value:      *schemas.NewEnvVar("bedrock-val-2"),
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			AccessKey:    *schemas.NewEnvVar("AKIA-ST-EXAMPLE"),
			SecretKey:    *schemas.NewEnvVar("wJalr-ST-EXAMPLE"),
			SessionToken: sessionToken,
			Region:       region,
		},
	}

	require.NoError(t, db.Create(key).Error)

	raw := rawRow(t, db, "config_keys", key.ID)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	// SessionToken and Region should be encrypted in the raw DB
	assert.NotEqual(t, "FwoGZXIvYXdzEBYaDH...", raw["bedrock_session_token"])
	assert.NotEqual(t, "us-east-1", raw["bedrock_region"])

	var found TableKey
	require.NoError(t, db.First(&found, key.ID).Error)
	require.NotNil(t, found.BedrockKeyConfig)
	require.NotNil(t, found.BedrockKeyConfig.SessionToken)
	assert.Equal(t, "FwoGZXIvYXdzEBYaDH...", found.BedrockKeyConfig.SessionToken.GetValue())
	assert.Equal(t, "AKIA-ST-EXAMPLE", found.BedrockKeyConfig.AccessKey.GetValue())
	assert.Equal(t, "wJalr-ST-EXAMPLE", found.BedrockKeyConfig.SecretKey.GetValue())
	require.NotNil(t, found.BedrockKeyConfig.Region)
	assert.Equal(t, "us-east-1", found.BedrockKeyConfig.Region.GetValue())
}

// ============================================================================
// MCP — edge cases for connection string / headers combinations
// ============================================================================

func TestTableMCPClient_DirectConnStr_EmptyHeaders(t *testing.T) {
	db := setupTestDB(t)

	// Direct connection string (not env var), no headers
	connStr := schemas.NewEnvVar("https://mcp-direct.example.com/sse")
	client := &TableMCPClient{
		ClientID:         "mcp-direct-nohdr",
		Name:             "direct-no-headers",
		ConnectionType:   "sse",
		ConnectionString: connStr,
		// No headers — serializes to "{}" which should NOT be encrypted
	}

	require.NoError(t, db.Create(client).Error)

	raw := rawRow(t, db, "config_mcp_clients", client.ID)
	assert.Equal(t, "encrypted", raw["encryption_status"])

	var found TableMCPClient
	require.NoError(t, db.First(&found, client.ID).Error)
	assert.Equal(t, "https://mcp-direct.example.com/sse", found.ConnectionString.GetValue())
}

func TestTableMCPClient_HeadersOnly_NoConnStr(t *testing.T) {
	db := setupTestDB(t)

	client := &TableMCPClient{
		ClientID:       "mcp-hdr-only",
		Name:           "headers-only",
		ConnectionType: "sse",
		Headers: map[string]schemas.EnvVar{
			"X-Api-Key": *schemas.NewEnvVar("secret-api-key"),
		},
	}

	require.NoError(t, db.Create(client).Error)

	raw := rawRow(t, db, "config_mcp_clients", client.ID)
	assert.Equal(t, "encrypted", raw["encryption_status"])

	var found TableMCPClient
	require.NoError(t, db.First(&found, client.ID).Error)
	require.Contains(t, found.Headers, "X-Api-Key")
	assert.Equal(t, "secret-api-key", found.Headers["X-Api-Key"].Val)
}

// ============================================================================
// Round-trip update tests for remaining tables
// ============================================================================

func TestTableVirtualKey_UpdatePreservesDecryption(t *testing.T) {
	db := setupTestDB(t)

	vk := &TableVirtualKey{
		ID:       "vk-update",
		Name:     "update-vk",
		Value:    "original-vk-value",
		IsActive: true,
	}
	require.NoError(t, db.Create(vk).Error)

	var found TableVirtualKey
	require.NoError(t, db.First(&found, "id = ?", "vk-update").Error)
	assert.Equal(t, "original-vk-value", found.Value)

	found.Value = "updated-vk-value"
	require.NoError(t, db.Save(&found).Error)

	var found2 TableVirtualKey
	require.NoError(t, db.First(&found2, "id = ?", "vk-update").Error)
	assert.Equal(t, "updated-vk-value", found2.Value)

	raw := rawRow(t, db, "governance_virtual_keys", "vk-update")
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.Equal(t, encrypt.HashSHA256("updated-vk-value"), raw["value_hash"])
}

func TestTableOauthConfig_UpdatePreservesDecryption(t *testing.T) {
	db := setupTestDB(t)

	config := &TableOauthConfig{
		ID:           "oauth-cfg-update",
		ClientSecret: "original-secret",
		RedirectURI:  "https://example.com/callback",
		State:        "csrf-update",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
	}
	require.NoError(t, db.Create(config).Error)

	var found TableOauthConfig
	require.NoError(t, db.First(&found, "id = ?", "oauth-cfg-update").Error)
	assert.Equal(t, "original-secret", found.ClientSecret)

	found.ClientSecret = "rotated-secret"
	require.NoError(t, db.Save(&found).Error)

	var found2 TableOauthConfig
	require.NoError(t, db.First(&found2, "id = ?", "oauth-cfg-update").Error)
	assert.Equal(t, "rotated-secret", found2.ClientSecret)
}

func TestTableOauthToken_UpdatePreservesDecryption(t *testing.T) {
	db := setupTestDB(t)

	token := &TableOauthToken{
		ID:           "oauth-tok-update",
		AccessToken:  "original-access",
		RefreshToken: "original-refresh",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	require.NoError(t, db.Create(token).Error)

	var found TableOauthToken
	require.NoError(t, db.First(&found, "id = ?", "oauth-tok-update").Error)

	found.AccessToken = "refreshed-access"
	require.NoError(t, db.Save(&found).Error)

	var found2 TableOauthToken
	require.NoError(t, db.First(&found2, "id = ?", "oauth-tok-update").Error)
	assert.Equal(t, "refreshed-access", found2.AccessToken)
	assert.Equal(t, "original-refresh", found2.RefreshToken)
}

func TestTableProvider_UpdatePreservesDecryption(t *testing.T) {
	db := setupTestDB(t)

	provider := &TableProvider{
		Name:        "update-provider",
		ProxyConfig: &schemas.ProxyConfig{URL: "https://proxy-v1.example.com"},
	}
	require.NoError(t, db.Create(provider).Error)

	var found TableProvider
	require.NoError(t, db.First(&found, provider.ID).Error)
	assert.Equal(t, "https://proxy-v1.example.com", found.ProxyConfig.URL)

	found.ProxyConfig = &schemas.ProxyConfig{URL: "https://proxy-v2.example.com"}
	require.NoError(t, db.Save(&found).Error)

	var found2 TableProvider
	require.NoError(t, db.First(&found2, provider.ID).Error)
	require.NotNil(t, found2.ProxyConfig)
	assert.Equal(t, "https://proxy-v2.example.com", found2.ProxyConfig.URL)
}

func TestTablePlugin_UpdatePreservesDecryption(t *testing.T) {
	db := setupTestDB(t)

	plugin := &TablePlugin{
		Name:    "update-plugin",
		Enabled: true,
		Version: 1,
		Config:  map[string]any{"key": "original-secret"},
	}
	require.NoError(t, db.Create(plugin).Error)

	var found TablePlugin
	require.NoError(t, db.First(&found, plugin.ID).Error)
	configMap := found.Config.(map[string]any)
	assert.Equal(t, "original-secret", configMap["key"])

	found.Config = map[string]any{"key": "updated-secret"}
	require.NoError(t, db.Save(&found).Error)

	var found2 TablePlugin
	require.NoError(t, db.First(&found2, plugin.ID).Error)
	configMap2 := found2.Config.(map[string]any)
	assert.Equal(t, "updated-secret", configMap2["key"])
}

func TestTableVectorStoreConfig_UpdatePreservesDecryption(t *testing.T) {
	db := setupTestDB(t)

	configV1 := `{"host":"redis-v1.example.com","password":"secret-v1"}`
	vs := &TableVectorStoreConfig{
		Enabled: true,
		Type:    "redis",
		Config:  &configV1,
	}
	require.NoError(t, db.Create(vs).Error)

	var found TableVectorStoreConfig
	require.NoError(t, db.First(&found, vs.ID).Error)
	assert.Contains(t, *found.Config, "secret-v1")

	configV2 := `{"host":"redis-v2.example.com","password":"secret-v2"}`
	found.Config = &configV2
	require.NoError(t, db.Save(&found).Error)

	var found2 TableVectorStoreConfig
	require.NoError(t, db.First(&found2, vs.ID).Error)
	assert.Contains(t, *found2.Config, "secret-v2")
}

func TestTableMCPClient_UpdatePreservesDecryption(t *testing.T) {
	db := setupTestDB(t)

	connStr := schemas.NewEnvVar("https://mcp-v1.example.com/sse")
	client := &TableMCPClient{
		ClientID:         "mcp-update",
		Name:             "update-mcp",
		ConnectionType:   "sse",
		ConnectionString: connStr,
		Headers: map[string]schemas.EnvVar{
			"Authorization": *schemas.NewEnvVar("Bearer token-v1"),
		},
	}
	require.NoError(t, db.Create(client).Error)

	var found TableMCPClient
	require.NoError(t, db.First(&found, client.ID).Error)
	assert.Equal(t, "https://mcp-v1.example.com/sse", found.ConnectionString.GetValue())

	found.ConnectionString = schemas.NewEnvVar("https://mcp-v2.example.com/sse")
	found.Headers = map[string]schemas.EnvVar{
		"Authorization": *schemas.NewEnvVar("Bearer token-v2"),
	}
	require.NoError(t, db.Save(&found).Error)

	var found2 TableMCPClient
	require.NoError(t, db.First(&found2, client.ID).Error)
	assert.Equal(t, "https://mcp-v2.example.com/sse", found2.ConnectionString.GetValue())
	assert.Equal(t, "Bearer token-v2", found2.Headers["Authorization"].Val)
}

// ============================================================================
// Multi-row Find — verify all rows get decrypted
// ============================================================================

func TestTableKey_FindMultipleDecryptsAll(t *testing.T) {
	db := setupTestDB(t)

	for i, val := range []string{"key-alpha", "key-beta", "key-gamma"} {
		key := &TableKey{
			Name:       val,
			ProviderID: 1,
			Provider:   "openai",
			KeyID:      val + "-uuid",
			Value:      *schemas.NewEnvVar("secret-" + val),
			Models:     []string{"gpt-4"},
		}
		_ = i
		require.NoError(t, db.Create(key).Error)
	}

	var keys []TableKey
	require.NoError(t, db.Find(&keys).Error)
	assert.Len(t, keys, 3)

	for _, k := range keys {
		assert.Contains(t, k.Value.GetValue(), "secret-")
		assert.NotContains(t, k.Value.GetValue(), "=") // base64 ciphertext artefact
	}
}

func TestSessionsTable_FindMultipleDecryptsAll(t *testing.T) {
	db := setupTestDB(t)

	for _, tok := range []string{"token-1", "token-2", "token-3"} {
		session := &SessionsTable{
			Token:     tok,
			ExpiresAt: time.Now().Add(time.Hour),
		}
		require.NoError(t, db.Create(session).Error)
	}

	var sessions []SessionsTable
	require.NoError(t, db.Find(&sessions).Error)
	assert.Len(t, sessions, 3)

	tokens := map[string]bool{}
	for _, s := range sessions {
		tokens[s.Token] = true
	}
	assert.True(t, tokens["token-1"])
	assert.True(t, tokens["token-2"])
	assert.True(t, tokens["token-3"])
}

func TestTableOauthToken_FindMultipleDecryptsAll(t *testing.T) {
	db := setupTestDB(t)

	for _, id := range []string{"multi-tok-1", "multi-tok-2"} {
		token := &TableOauthToken{
			ID:           id,
			AccessToken:  "access-" + id,
			RefreshToken: "refresh-" + id,
			TokenType:    "Bearer",
			ExpiresAt:    time.Now().Add(time.Hour),
		}
		require.NoError(t, db.Create(token).Error)
	}

	var tokens []TableOauthToken
	require.NoError(t, db.Find(&tokens).Error)
	assert.Len(t, tokens, 2)

	for _, tok := range tokens {
		assert.Contains(t, tok.AccessToken, "access-multi-tok-")
		assert.Contains(t, tok.RefreshToken, "refresh-multi-tok-")
	}
}

// ============================================================================
// Key with all provider configs simultaneously (complex round-trip)
// ============================================================================

func TestTableKey_AllProviderConfigs_EncryptDecrypt(t *testing.T) {
	db := setupTestDB(t)

	sessionToken := schemas.NewEnvVar("aws-session-token")
	key := &TableKey{
		Name:       "multi-provider-key",
		ProviderID: 1,
		Provider:   "custom",
		KeyID:      "multi-uuid",
		Value:      *schemas.NewEnvVar("multi-api-key"),
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:     *schemas.NewEnvVar("https://azure.endpoint.com"),
			ClientID:     schemas.NewEnvVar("multi-azure-cid"),
			ClientSecret: schemas.NewEnvVar("azure-cs"),
			TenantID:     schemas.NewEnvVar("multi-azure-tid"),
			APIVersion:   schemas.NewEnvVar("2024-10-21"),
		},
		VertexKeyConfig: &schemas.VertexKeyConfig{
			AuthCredentials: *schemas.NewEnvVar(`{"type":"sa"}`),
			ProjectID:       *schemas.NewEnvVar("proj-123"),
			ProjectNumber:   *schemas.NewEnvVar("987654321"),
			Region:          *schemas.NewEnvVar("us-central1"),
		},
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			AccessKey:    *schemas.NewEnvVar("AKIA-MULTI"),
			SecretKey:    *schemas.NewEnvVar("wJalr-MULTI"),
			SessionToken: sessionToken,
			Region:       schemas.NewEnvVar("eu-west-1"),
			ARN:          schemas.NewEnvVar("arn:aws:bedrock:eu-west-1:123:role"),
			Deployments:  map[string]string{"claude-3": "profile-claude"},
		},
	}

	require.NoError(t, db.Create(key).Error)

	// Verify raw DB has encrypted values for all new fields
	raw := rawRow(t, db, "config_keys", key.ID)
	assert.Equal(t, "encrypted", raw["encryption_status"])
	assert.NotEqual(t, "multi-azure-cid", raw["azure_client_id"])
	assert.NotEqual(t, "multi-azure-tid", raw["azure_tenant_id"])
	assert.NotEqual(t, "2024-10-21", raw["azure_api_version"])
	assert.NotEqual(t, "proj-123", raw["vertex_project_id"])
	assert.NotEqual(t, "987654321", raw["vertex_project_number"])
	assert.NotEqual(t, "us-central1", raw["vertex_region"])
	assert.NotEqual(t, "eu-west-1", raw["bedrock_region"])
	assert.NotEqual(t, "arn:aws:bedrock:eu-west-1:123:role", raw["bedrock_arn"])
	if rawDeploy, ok := raw["bedrock_deployments_json"].(string); ok {
		assert.NotContains(t, rawDeploy, "profile-claude")
	}

	var found TableKey
	require.NoError(t, db.First(&found, key.ID).Error)

	assert.Equal(t, "multi-api-key", found.Value.GetValue())

	require.NotNil(t, found.AzureKeyConfig)
	assert.Equal(t, "https://azure.endpoint.com", found.AzureKeyConfig.Endpoint.GetValue())
	require.NotNil(t, found.AzureKeyConfig.ClientID)
	assert.Equal(t, "multi-azure-cid", found.AzureKeyConfig.ClientID.GetValue())
	assert.Equal(t, "azure-cs", found.AzureKeyConfig.ClientSecret.GetValue())
	require.NotNil(t, found.AzureKeyConfig.TenantID)
	assert.Equal(t, "multi-azure-tid", found.AzureKeyConfig.TenantID.GetValue())
	require.NotNil(t, found.AzureKeyConfig.APIVersion)
	assert.Equal(t, "2024-10-21", found.AzureKeyConfig.APIVersion.GetValue())

	require.NotNil(t, found.VertexKeyConfig)
	assert.Equal(t, `{"type":"sa"}`, found.VertexKeyConfig.AuthCredentials.GetValue())
	assert.Equal(t, "proj-123", found.VertexKeyConfig.ProjectID.GetValue())
	assert.Equal(t, "987654321", found.VertexKeyConfig.ProjectNumber.GetValue())
	assert.Equal(t, "us-central1", found.VertexKeyConfig.Region.GetValue())

	require.NotNil(t, found.BedrockKeyConfig)
	assert.Equal(t, "AKIA-MULTI", found.BedrockKeyConfig.AccessKey.GetValue())
	assert.Equal(t, "wJalr-MULTI", found.BedrockKeyConfig.SecretKey.GetValue())
	require.NotNil(t, found.BedrockKeyConfig.SessionToken)
	assert.Equal(t, "aws-session-token", found.BedrockKeyConfig.SessionToken.GetValue())
	require.NotNil(t, found.BedrockKeyConfig.Region)
	assert.Equal(t, "eu-west-1", found.BedrockKeyConfig.Region.GetValue())
	require.NotNil(t, found.BedrockKeyConfig.ARN)
	assert.Equal(t, "arn:aws:bedrock:eu-west-1:123:role", found.BedrockKeyConfig.ARN.GetValue())
	assert.Equal(t, "profile-claude", found.BedrockKeyConfig.Deployments["claude-3"])
}

// ============================================================================
// Encryption disabled — verify hooks are no-ops and data stays plaintext
// ============================================================================

// disableEncryption temporarily disables encryption for the duration of a test
// by reinitializing with an empty key. It registers a cleanup to restore the key.
func disableEncryption(t *testing.T) {
	t.Helper()
	encrypt.Init("", bifrost.NewDefaultLogger(schemas.LogLevelInfo))
	t.Cleanup(func() {
		encrypt.Init(testEncryptionKey, bifrost.NewDefaultLogger(schemas.LogLevelInfo))
	})
}

func TestTableKey_EncryptionDisabled_StoresPlaintext(t *testing.T) {
	disableEncryption(t)
	db := setupTestDB(t)

	endpoint := schemas.NewEnvVar("https://azure.example.com")
	key := &TableKey{
		Name:       "disabled-key",
		ProviderID: 1,
		Provider:   "azure",
		KeyID:      "dis-1",
		Value:      *schemas.NewEnvVar("sk-plaintext-stays"),
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint: *endpoint,
		},
	}

	require.NoError(t, db.Create(key).Error)

	// Raw DB should have plaintext values
	raw := rawRow(t, db, "config_keys", key.ID)
	assert.Equal(t, "plain_text", raw["encryption_status"])
	assert.Equal(t, "sk-plaintext-stays", raw["value"])
	assert.Equal(t, "https://azure.example.com", raw["azure_endpoint"])

	// GORM read should return same plaintext (no decrypt attempt)
	var found TableKey
	require.NoError(t, db.First(&found, key.ID).Error)
	assert.Equal(t, "sk-plaintext-stays", found.Value.GetValue())
	require.NotNil(t, found.AzureKeyConfig)
	assert.Equal(t, "https://azure.example.com", found.AzureKeyConfig.Endpoint.GetValue())
}

func TestTableMCPClient_EncryptionDisabled_StoresPlaintext(t *testing.T) {
	disableEncryption(t)
	db := setupTestDB(t)

	client := &TableMCPClient{
		ClientID:       "mcp-dis-1",
		Name:           "disabled-mcp",
		ConnectionType: "sse",
		ConnectionString: schemas.NewEnvVar("https://mcp.example.com"),
		Headers: map[string]schemas.EnvVar{
			"Authorization": *schemas.NewEnvVar("Bearer secret-token"),
		},
	}

	require.NoError(t, db.Create(client).Error)

	// Raw DB should have plaintext
	raw := rawRow(t, db, "config_mcp_clients", client.ID)
	assert.Equal(t, "plain_text", raw["encryption_status"])
	assert.Equal(t, "https://mcp.example.com", raw["connection_string"])
	assert.Contains(t, raw["headers_json"], "Bearer secret-token")

	// GORM read should return same plaintext
	var found TableMCPClient
	require.NoError(t, db.First(&found, client.ID).Error)
	assert.Equal(t, "https://mcp.example.com", found.ConnectionString.GetValue())
	assert.Equal(t, "Bearer secret-token", found.Headers["Authorization"].Val)
}

func TestTableVirtualKey_EncryptionDisabled_StoresPlaintext(t *testing.T) {
	disableEncryption(t)
	db := setupTestDB(t)

	vk := &TableVirtualKey{
		ID:       "vk-dis-1",
		Name:     "disabled-vk",
		Value:    "vk-plaintext-value",
		IsActive: true,
	}

	require.NoError(t, db.Create(vk).Error)

	// Raw DB should have plaintext value — hash should still be computed
	var raw map[string]any
	db.Table("governance_virtual_keys").Where("id = ?", "vk-dis-1").Take(&raw)
	assert.Equal(t, "plain_text", raw["encryption_status"])
	assert.Equal(t, "vk-plaintext-value", raw["value"])
	assert.NotEmpty(t, raw["value_hash"], "hash should still be computed even without encryption")

	// GORM read should return same plaintext
	var found TableVirtualKey
	require.NoError(t, db.Where("id = ?", "vk-dis-1").First(&found).Error)
	assert.Equal(t, "vk-plaintext-value", found.Value)
}

func TestSessionsTable_EncryptionDisabled_StoresPlaintext(t *testing.T) {
	disableEncryption(t)
	db := setupTestDB(t)

	session := &SessionsTable{
		Token:     "session-plaintext-token",
		ExpiresAt: time.Now().Add(time.Hour),
	}

	require.NoError(t, db.Create(session).Error)

	// Raw DB should have plaintext token — hash should still be computed
	raw := rawRow(t, db, "sessions", session.ID)
	assert.Equal(t, "plain_text", raw["encryption_status"])
	assert.Equal(t, "session-plaintext-token", raw["token"])
	assert.NotEmpty(t, raw["token_hash"], "hash should still be computed even without encryption")

	// GORM read should return same plaintext
	var found SessionsTable
	require.NoError(t, db.First(&found, session.ID).Error)
	assert.Equal(t, "session-plaintext-token", found.Token)
}

func TestTableOauthConfig_EncryptionDisabled_StoresPlaintext(t *testing.T) {
	disableEncryption(t)
	db := setupTestDB(t)

	cfg := &TableOauthConfig{
		ID:           "cfg-dis-1",
		ClientSecret: "client-secret-plain",
		CodeVerifier: "verifier-plain",
		RedirectURI:  "https://example.com/cb",
		State:        "csrf-state",
		Status:       "pending",
		ExpiresAt:    time.Now().Add(time.Hour),
	}

	require.NoError(t, db.Create(cfg).Error)

	// Raw DB should have plaintext
	var raw map[string]any
	db.Table("oauth_configs").Where("id = ?", "cfg-dis-1").Take(&raw)
	assert.Equal(t, "plain_text", raw["encryption_status"])
	assert.Equal(t, "client-secret-plain", raw["client_secret"])
	assert.Equal(t, "verifier-plain", raw["code_verifier"])

	// GORM read should return same plaintext
	var found TableOauthConfig
	require.NoError(t, db.Where("id = ?", "cfg-dis-1").First(&found).Error)
	assert.Equal(t, "client-secret-plain", found.ClientSecret)
	assert.Equal(t, "verifier-plain", found.CodeVerifier)
}

func TestTableOauthToken_EncryptionDisabled_StoresPlaintext(t *testing.T) {
	disableEncryption(t)
	db := setupTestDB(t)

	token := &TableOauthToken{
		ID:           "tok-dis-1",
		AccessToken:  "access-plain",
		RefreshToken: "refresh-plain",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
	}

	require.NoError(t, db.Create(token).Error)

	// Raw DB should have plaintext
	var raw map[string]any
	db.Table("oauth_tokens").Where("id = ?", "tok-dis-1").Take(&raw)
	assert.Equal(t, "plain_text", raw["encryption_status"])
	assert.Equal(t, "access-plain", raw["access_token"])
	assert.Equal(t, "refresh-plain", raw["refresh_token"])

	// GORM read should return same plaintext
	var found TableOauthToken
	require.NoError(t, db.Where("id = ?", "tok-dis-1").First(&found).Error)
	assert.Equal(t, "access-plain", found.AccessToken)
	assert.Equal(t, "refresh-plain", found.RefreshToken)
}

func TestTableProvider_EncryptionDisabled_StoresPlaintext(t *testing.T) {
	disableEncryption(t)
	db := setupTestDB(t)

	provider := &TableProvider{
		Name: "disabled-provider",
		ProxyConfig: &schemas.ProxyConfig{
			URL:      "https://proxy.example.com",
			Password: "proxy-secret",
		},
	}

	require.NoError(t, db.Create(provider).Error)

	// Raw DB should have plaintext proxy config
	raw := rawRow(t, db, "config_providers", provider.ID)
	assert.Equal(t, "plain_text", raw["encryption_status"])
	assert.Contains(t, raw["proxy_config_json"], "proxy-secret")

	// GORM read should return same plaintext
	var found TableProvider
	require.NoError(t, db.First(&found, provider.ID).Error)
	require.NotNil(t, found.ProxyConfig)
	assert.Equal(t, "proxy-secret", found.ProxyConfig.Password)
}

func TestTablePlugin_EncryptionDisabled_StoresPlaintext(t *testing.T) {
	disableEncryption(t)
	db := setupTestDB(t)

	plugin := &TablePlugin{
		Name:    "disabled-plugin",
		Enabled: true,
		Version: 1,
		Config:  map[string]any{"api_key": "plugin-secret"},
	}

	require.NoError(t, db.Create(plugin).Error)

	// Raw DB should have plaintext config
	raw := rawRow(t, db, "config_plugins", plugin.ID)
	assert.Equal(t, "plain_text", raw["encryption_status"])
	assert.Contains(t, raw["config_json"], "plugin-secret")

	// GORM read should return same plaintext
	var found TablePlugin
	require.NoError(t, db.First(&found, plugin.ID).Error)
	assert.Contains(t, found.ConfigJSON, "plugin-secret")
}

func TestTableVectorStoreConfig_EncryptionDisabled_StoresPlaintext(t *testing.T) {
	disableEncryption(t)
	db := setupTestDB(t)

	config := `{"host":"redis.example.com","password":"redis-secret"}`
	vs := &TableVectorStoreConfig{
		Enabled: true,
		Type:    "redis",
		Config:  &config,
	}

	require.NoError(t, db.Create(vs).Error)

	// Raw DB should have plaintext config
	raw := rawRow(t, db, "config_vector_store", vs.ID)
	assert.Equal(t, "plain_text", raw["encryption_status"])
	assert.Contains(t, raw["config"], "redis-secret")

	// GORM read should return same plaintext
	var found TableVectorStoreConfig
	require.NoError(t, db.First(&found, vs.ID).Error)
	require.NotNil(t, found.Config)
	assert.Contains(t, *found.Config, "redis-secret")
}
