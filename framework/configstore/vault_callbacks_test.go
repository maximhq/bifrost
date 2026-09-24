package configstore

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// stubVaultHooks installs store/remove hooks that mimic the enterprise vault
// registry: store records the path and rewrites the value to "vault.<path>";
// remove records the deleted path. Hooks are restored on cleanup.
func stubVaultHooks(t *testing.T) (stored map[string]string, removed *[]string) {
	t.Helper()
	stored = make(map[string]string)
	rem := []string{}
	prevStore, prevRemove := schemas.VaultStoreHook, schemas.VaultRemoveHook
	schemas.VaultStoreHook = func(_ context.Context, path string, value *string) error {
		stored[path] = *value
		*value = "vault." + path
		return nil
	}
	schemas.VaultRemoveHook = func(_ context.Context, path string) error {
		rem = append(rem, path)
		return nil
	}
	t.Cleanup(func() {
		schemas.VaultStoreHook = prevStore
		schemas.VaultRemoveHook = prevRemove
	})
	return stored, &rem
}

func TestVaultCallbacks_AutoStoreAndRemove(t *testing.T) {
	stored, removed := stubVaultHooks(t)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	RegisterVaultCallbacks(db)
	require.NoError(t, db.AutoMigrate(&tables.TableMCPClient{}))

	client := &tables.TableMCPClient{
		ClientID:       "client-1",
		Name:           "test-client",
		ConnectionType: "http",
		AuthType:       "headers",
		Headers: map[string]schemas.SecretVar{
			"Authorization": {Val: "secret-token"},
		},
	}
	require.NoError(t, db.Create(client).Error)

	// The global store callback should have pushed the plaintext header to vault
	// before BeforeSave serialized Headers into HeadersJSON.
	headerPath := "bifrost/config_mcp_clients/client-1/headers/Authorization"
	require.Equal(t, "secret-token", stored[headerPath], "header secret not stored to vault")

	// HeadersJSON persisted in the row should hold the vault ref, not plaintext.
	var row tables.TableMCPClient
	require.NoError(t, db.First(&row, "client_id = ?", "client-1").Error)
	var headers map[string]string
	require.NoError(t, json.Unmarshal([]byte(row.HeadersJSON), &headers))
	require.Equal(t, "vault."+headerPath, headers["Authorization"], "HeadersJSON should store vault ref")

	// Deleting the row should trigger the global remove callback. Load first so
	// the model has its Headers populated for the reflection walk.
	var toDelete tables.TableMCPClient
	require.NoError(t, db.First(&toDelete, "client_id = ?", "client-1").Error)
	require.NoError(t, db.Delete(&toDelete).Error)

	found := false
	for _, p := range *removed {
		if p == headerPath {
			found = true
		}
	}
	require.True(t, found, "expected vault remove for %q, got %v", headerPath, *removed)
}

// TestVaultCallbacks_SelfManagedStoresPlaintext verifies that TableKey, whose
// SecretVar columns are populated inside BeforeSave, stores the PLAINTEXT secret to
// vault and persists a vault ref — both when encryption is off and when it is on.
// With encryption on, the inline vault store must run before encryption so the vault
// holds plaintext (not ciphertext) and the column holds the ref (not encrypted data).
func TestVaultCallbacks_SelfManagedStoresPlaintext(t *testing.T) {
	cases := []struct {
		name          string
		encryptionKey string
	}{
		{"encryption off", ""},
		{"encryption on", "test-encryption-key"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored, _ := stubVaultHooks(t)
			encrypt.Init(tc.encryptionKey, bifrost.NewDefaultLogger(schemas.LogLevelInfo))
			t.Cleanup(func() { encrypt.Init("", bifrost.NewDefaultLogger(schemas.LogLevelInfo)) })

			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			RegisterVaultCallbacks(db)
			require.NoError(t, db.AutoMigrate(&tables.TableKey{}))

			keyID := fmt.Sprintf("key-%d", i)
			key := &tables.TableKey{
				Name:     fmt.Sprintf("k%d", i),
				KeyID:    keyID,
				Provider: "bedrock",
				Value:    schemas.SecretVar{Val: "primary-value"},
				Models:   schemas.WhiteList{"*"},
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					SecretKey: schemas.SecretVar{Val: "bedrock-secret"},
				},
			}
			require.NoError(t, db.Create(key).Error)

			// The vault must receive PLAINTEXT, regardless of encryption state.
			secretPath := fmt.Sprintf("bifrost/config_keys/%s/bedrock_secret_key", keyID)
			require.Equal(t, "bedrock-secret", stored[secretPath], "vault must store plaintext, not ciphertext")

			// The persisted column should hold the vault ref (which is never re-encrypted).
			var row tables.TableKey
			require.NoError(t, db.First(&row, "key_id = ?", keyID).Error)
			require.NotNil(t, row.BedrockSecretKey)
			require.Equal(t, "vault."+secretPath, row.BedrockSecretKey.GetRawRef(), "column should store vault ref")
		})
	}
}

// TestVaultCallbacks_SelfManagedRemovesVaultSecrets verifies that deleting a TableKey
// (a self-managed model) removes all its owned vault secrets via the global remove callback.
func TestVaultCallbacks_SelfManagedRemovesVaultSecrets(t *testing.T) {
	stored, removed := stubVaultHooks(t)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	RegisterVaultCallbacks(db)
	require.NoError(t, db.AutoMigrate(&tables.TableKey{}))

	keyID := "key-delete-test"
	key := &tables.TableKey{
		Name:     "k-delete",
		KeyID:    keyID,
		Provider: "openai",
		Value:    schemas.SecretVar{Val: "sk-secret-value"},
		Models:   schemas.WhiteList{"*"},
	}
	require.NoError(t, db.Create(key).Error)

	valuePath := fmt.Sprintf("bifrost/config_keys/%s/value", keyID)
	require.Equal(t, "sk-secret-value", stored[valuePath], "value not stored to vault on create")

	// Load fresh to get the vault ref populated in SecretVar fields.
	var toDelete tables.TableKey
	require.NoError(t, db.First(&toDelete, "key_id = ?", keyID).Error)
	require.Equal(t, "vault."+valuePath, toDelete.Value.GetRawRef(), "loaded key should carry vault ref")

	require.NoError(t, db.Delete(&toDelete).Error)

	found := false
	for _, p := range *removed {
		if p == valuePath {
			found = true
		}
	}
	require.True(t, found, "expected vault remove for %q, got %v", valuePath, *removed)
}

func TestVaultCallbacks_NoOpWhenDisabled(t *testing.T) {
	// No hooks installed -> VaultStoreEnabled() is false -> callbacks no-op.
	prevStore := schemas.VaultStoreHook
	schemas.VaultStoreHook = nil
	t.Cleanup(func() { schemas.VaultStoreHook = prevStore })

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	RegisterVaultCallbacks(db)
	require.NoError(t, db.AutoMigrate(&tables.TableMCPClient{}))

	client := &tables.TableMCPClient{
		ClientID:       "client-2",
		Name:           "plain-client",
		ConnectionType: "http",
		AuthType:       "headers",
		Headers:        map[string]schemas.SecretVar{"Authorization": {Val: "plain-secret"}},
	}
	require.NoError(t, db.Create(client).Error)

	var row tables.TableMCPClient
	require.NoError(t, db.First(&row, "client_id = ?", "client-2").Error)
	require.False(t, strings.Contains(row.HeadersJSON, "vault."), "no vault ref expected when disabled: %s", row.HeadersJSON)
}

// stubVaultResolve installs a resolve hook that reads back what stubVaultHooks stored,
// so rows holding vault refs load with their secret values.
func stubVaultResolve(t *testing.T, stored map[string]string) {
	t.Helper()
	prev := schemas.VaultResolveHook
	schemas.VaultResolveHook = func(_ context.Context, value *string) error {
		path := strings.TrimPrefix(*value, "vault.")
		secret, ok := stored[path]
		if !ok {
			return fmt.Errorf("no vault secret at %s", path)
		}
		*value = secret
		return nil
	}
	t.Cleanup(func() { schemas.VaultResolveHook = prev })
}

// storedNetworkConfigHeaders reads network_config_json straight from the row, so the test
// sees what was persisted rather than the resolved values.
func storedNetworkConfigHeaders(t *testing.T, db *gorm.DB, name string) map[string]string {
	t.Helper()
	var row struct{ NetworkConfigJSON string }
	require.NoError(t, db.Table("config_providers").Select("network_config_json").Where("name = ?", name).Scan(&row).Error)
	var nc struct {
		ExtraHeaders map[string]string `json:"extra_headers"`
	}
	require.NoError(t, json.Unmarshal([]byte(row.NetworkConfigJSON), &nc))
	return nc.ExtraHeaders
}

// TestVaultCallbacks_ProviderExtraHeaders covers provider extra headers through the store's
// add, update, config.json sync and delete paths: plaintext values go to the vault, the row
// keeps only refs, env refs are left alone, the caller's config is not rewritten, a header
// dropped by update or sync has its owned secret removed, and deleting the provider removes
// the secrets it still owns.
func TestVaultCallbacks_ProviderExtraHeaders(t *testing.T) {
	stored, removed := stubVaultHooks(t)
	stubVaultResolve(t, stored)
	t.Setenv("BF_TEST_HEADER_FROM_ENV", "env-value")

	store := setupRDBTestStore(t)
	RegisterVaultCallbacks(store.DB())
	ctx := context.Background()
	const provider = schemas.ModelProvider("hyperpod")
	base := "bifrost/config_providers/hyperpod/extra_headers/"

	config := ProviderConfig{
		NetworkConfig: &schemas.NetworkConfig{ExtraHeaders: map[string]schemas.SecretVar{
			"X-Client-Secret": {Val: "oauth-client-secret"},
			"X-From-Env":      *schemas.NewSecretVar("env.BF_TEST_HEADER_FROM_ENV"),
		}},
		CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI},
	}
	require.NoError(t, store.AddProvider(ctx, provider, config))

	t.Run("add stores plaintext values and persists refs", func(t *testing.T) {
		require.Equal(t, "oauth-client-secret", stored[base+"X-Client-Secret"])
		_, envStored := stored[base+"X-From-Env"]
		require.False(t, envStored, "env refs must not be copied into the vault")

		headers := storedNetworkConfigHeaders(t, store.DB(), string(provider))
		require.Equal(t, "vault."+base+"X-Client-Secret", headers["X-Client-Secret"])
		require.Equal(t, "env.BF_TEST_HEADER_FROM_ENV", headers["X-From-Env"])

		caller := config.NetworkConfig.ExtraHeaders["X-Client-Secret"]
		require.False(t, caller.IsFromVault(), "the caller's header map must not be rewritten")
	})

	t.Run("loaded config resolves the vault ref", func(t *testing.T) {
		loaded, err := store.GetProviderConfig(ctx, provider)
		require.NoError(t, err)
		secret := loaded.NetworkConfig.ExtraHeaders["X-Client-Secret"]
		require.True(t, secret.IsFromVault())
		require.Equal(t, "oauth-client-secret", secret.GetValue())
	})

	t.Run("update stores a changed value and new headers", func(t *testing.T) {
		updated := config
		updated.NetworkConfig = &schemas.NetworkConfig{ExtraHeaders: map[string]schemas.SecretVar{
			"X-Client-Secret": {Val: "rotated-secret"},
			"X-From-Env":      *schemas.NewSecretVar("env.BF_TEST_HEADER_FROM_ENV"),
			"X-Client-Id":     {Val: "client-id-123"},
			"X-Temporary":     {Val: "temporary-secret"},
		}}
		require.NoError(t, store.UpdateProvider(ctx, provider, updated))
		require.Equal(t, "rotated-secret", stored[base+"X-Client-Secret"])
		require.Equal(t, "client-id-123", stored[base+"X-Client-Id"])
		headers := storedNetworkConfigHeaders(t, store.DB(), string(provider))
		require.Equal(t, "vault."+base+"X-Client-Id", headers["X-Client-Id"])
		require.Empty(t, *removed, "a rewritten header keeps its path, so nothing is removed")
	})

	t.Run("update removes the secret of a dropped header", func(t *testing.T) {
		loaded, err := store.GetProviderConfig(ctx, provider)
		require.NoError(t, err)
		headers := maps.Clone(loaded.NetworkConfig.ExtraHeaders)
		delete(headers, "X-Temporary")
		updated := config
		updated.NetworkConfig = &schemas.NetworkConfig{ExtraHeaders: headers}
		require.NoError(t, store.UpdateProvider(ctx, provider, updated))
		require.Equal(t, []string{base + "X-Temporary"}, *removed)
	})

	t.Run("config.json sync stores values too", func(t *testing.T) {
		synced := config
		synced.NetworkConfig = &schemas.NetworkConfig{ExtraHeaders: map[string]schemas.SecretVar{
			"X-Client-Secret": {Val: "from-config-json"},
		}}
		require.NoError(t, store.UpdateProvidersConfig(ctx, map[schemas.ModelProvider]ProviderConfig{provider: synced}))
		require.Equal(t, "from-config-json", stored[base+"X-Client-Secret"])
		headers := storedNetworkConfigHeaders(t, store.DB(), string(provider))
		require.Equal(t, "vault."+base+"X-Client-Secret", headers["X-Client-Secret"])
		caller := synced.NetworkConfig.ExtraHeaders["X-Client-Secret"]
		require.False(t, caller.IsFromVault(), "sync must not rewrite the live config's header map")
		require.Contains(t, *removed, base+"X-Client-Id", "sync dropped X-Client-Id, so its secret must be removed")
		require.NotContains(t, *removed, base+"X-Client-Secret", "X-Client-Secret is still in use")
	})

	t.Run("delete removes the owned secrets", func(t *testing.T) {
		require.NoError(t, store.DeleteProvider(ctx, provider))
		require.Contains(t, *removed, base+"X-Client-Secret")
		for _, path := range []string{base + "X-Client-Secret", base + "X-Client-Id", base + "X-Temporary"} {
			require.Contains(t, *removed, path, "every secret the provider stored must be removed by the end")
		}
		for _, path := range *removed {
			require.True(t, strings.HasPrefix(path, base), "removed a path the provider does not own: %s", path)
		}
	})
}
