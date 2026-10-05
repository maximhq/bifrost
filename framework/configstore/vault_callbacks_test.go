package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
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

// requireVaultRefUnder checks ref is "vault.<prefix>/<id>", the per-write path
// StoreOwnedVaultSecretVars uses, and returns the vault path.
func requireVaultRefUnder(t *testing.T, ref, prefix string) string {
	t.Helper()
	path, ok := strings.CutPrefix(ref, "vault.")
	require.True(t, ok, "expected a vault ref, got %q", ref)
	id, ok := strings.CutPrefix(path, prefix+"/")
	require.True(t, ok && id != "" && !strings.Contains(id, "/"), "ref %q is not %s/<id>", ref, prefix)
	return path
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

	// HeadersJSON persisted in the row should hold the vault ref, not plaintext.
	var row tables.TableMCPClient
	require.NoError(t, db.First(&row, "client_id = ?", "client-1").Error)
	var headers map[string]string
	require.NoError(t, json.Unmarshal([]byte(row.HeadersJSON), &headers))
	headerPath := requireVaultRefUnder(t, headers["Authorization"], "bifrost/config_mcp_clients/client-1/headers/Authorization")

	// The global store callback should have pushed the plaintext header to vault
	// before BeforeSave serialized Headers into HeadersJSON.
	require.Equal(t, "secret-token", stored[headerPath], "header secret not stored to vault")

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

			// The persisted column should hold the vault ref (which is never re-encrypted).
			var row tables.TableKey
			require.NoError(t, db.First(&row, "key_id = ?", keyID).Error)
			require.NotNil(t, row.BedrockSecretKey)
			secretPath := requireVaultRefUnder(t, row.BedrockSecretKey.GetRawRef(), fmt.Sprintf("bifrost/config_keys/%s/bedrock_secret_key", keyID))

			// The vault must receive PLAINTEXT, regardless of encryption state.
			require.Equal(t, "bedrock-secret", stored[secretPath], "vault must store plaintext, not ciphertext")
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

	// Load fresh to get the vault ref populated in SecretVar fields.
	var toDelete tables.TableKey
	require.NoError(t, db.First(&toDelete, "key_id = ?", keyID).Error)
	valuePath := requireVaultRefUnder(t, toDelete.Value.GetRawRef(), fmt.Sprintf("bifrost/config_keys/%s/value", keyID))
	require.Equal(t, "sk-secret-value", stored[valuePath], "value not stored to vault on create")

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
// keeps only refs, env refs are left alone, the caller's config is not rewritten, a changed
// value gets a new path and its old secret is removed, a header dropped by update or sync
// has its owned secret removed, and deleting the provider removes the secrets it still owns.
func TestVaultCallbacks_ProviderExtraHeaders(t *testing.T) {
	stored, removed := stubVaultHooks(t)
	stubVaultResolve(t, stored)
	t.Setenv("BF_TEST_HEADER_FROM_ENV", "env-value")

	store := setupRDBTestStore(t)
	RegisterVaultCallbacks(store.DB())
	ctx := context.Background()
	const provider = schemas.ModelProvider("hyperpod")
	base := "bifrost/config_providers/hyperpod/extra_headers/"
	headerPath := func(t *testing.T, header string) string {
		t.Helper()
		return requireVaultRefUnder(t, storedNetworkConfigHeaders(t, store.DB(), string(provider))[header], base+header)
	}

	config := ProviderConfig{
		NetworkConfig: &schemas.NetworkConfig{ExtraHeaders: map[string]schemas.SecretVar{
			"X-Client-Secret": {Val: "oauth-client-secret"},
			"X-From-Env":      *schemas.NewSecretVar("env.BF_TEST_HEADER_FROM_ENV"),
		}},
		CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI},
	}
	require.NoError(t, store.AddProvider(ctx, provider, config))

	t.Run("add stores plaintext values and persists refs", func(t *testing.T) {
		secretPath := headerPath(t, "X-Client-Secret")
		require.Equal(t, "oauth-client-secret", stored[secretPath])
		require.Len(t, stored, 1, "env refs must not be copied into the vault")

		headers := storedNetworkConfigHeaders(t, store.DB(), string(provider))
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

	t.Run("update stores a changed value at a new path and removes the old one", func(t *testing.T) {
		oldPath := headerPath(t, "X-Client-Secret")
		updated := config
		updated.NetworkConfig = &schemas.NetworkConfig{ExtraHeaders: map[string]schemas.SecretVar{
			"X-Client-Secret": {Val: "rotated-secret"},
			"X-From-Env":      *schemas.NewSecretVar("env.BF_TEST_HEADER_FROM_ENV"),
			"X-Client-Id":     {Val: "client-id-123"},
			"X-Temporary":     {Val: "temporary-secret"},
		}}
		require.NoError(t, store.UpdateProvider(ctx, provider, updated))
		newPath := headerPath(t, "X-Client-Secret")
		require.NotEqual(t, oldPath, newPath)
		require.Equal(t, "rotated-secret", stored[newPath])
		require.Equal(t, "client-id-123", stored[headerPath(t, "X-Client-Id")])
		require.Equal(t, []string{oldPath}, *removed, "only the replaced value is removed")
	})

	t.Run("update removes the secret of a dropped header", func(t *testing.T) {
		temporaryPath := headerPath(t, "X-Temporary")
		loaded, err := store.GetProviderConfig(ctx, provider)
		require.NoError(t, err)
		headers := maps.Clone(loaded.NetworkConfig.ExtraHeaders)
		delete(headers, "X-Temporary")
		updated := config
		updated.NetworkConfig = &schemas.NetworkConfig{ExtraHeaders: headers}
		before := len(*removed)
		require.NoError(t, store.UpdateProvider(ctx, provider, updated))
		require.Equal(t, []string{temporaryPath}, (*removed)[before:], "unchanged headers keep their secrets")
	})

	t.Run("config.json sync stores values too", func(t *testing.T) {
		secretPath, clientIDPath := headerPath(t, "X-Client-Secret"), headerPath(t, "X-Client-Id")
		synced := config
		synced.NetworkConfig = &schemas.NetworkConfig{ExtraHeaders: map[string]schemas.SecretVar{
			"X-Client-Secret": {Val: "from-config-json"},
		}}
		before := len(*removed)
		require.NoError(t, store.UpdateProvidersConfig(ctx, map[schemas.ModelProvider]ProviderConfig{provider: synced}))
		syncedPath := headerPath(t, "X-Client-Secret")
		require.Equal(t, "from-config-json", stored[syncedPath])
		caller := synced.NetworkConfig.ExtraHeaders["X-Client-Secret"]
		require.False(t, caller.IsFromVault(), "sync must not rewrite the live config's header map")
		require.ElementsMatch(t, []string{secretPath, clientIDPath}, (*removed)[before:], "sync replaced X-Client-Secret and dropped X-Client-Id")
	})

	t.Run("delete removes the owned secrets", func(t *testing.T) {
		syncedPath := headerPath(t, "X-Client-Secret")
		before := len(*removed)
		require.NoError(t, store.DeleteProvider(ctx, provider))
		require.Equal(t, []string{syncedPath}, (*removed)[before:])
	})

	t.Run("every secret the provider stored was removed exactly once", func(t *testing.T) {
		require.ElementsMatch(t, slices.Collect(maps.Keys(stored)), *removed)
	})
}

var errLaterStep = errors.New("a later step in the transaction failed")

// newVaultTxProvider creates a store with vault hooks stubbed and one provider whose two
// extra headers, X-Keep and X-Drop, are stored in the vault.
func newVaultTxProvider(t *testing.T) (store *RDBConfigStore, stored map[string]string, removed *[]string) {
	t.Helper()
	stored, removed = stubVaultHooks(t)
	stubVaultResolve(t, stored)
	store = setupRDBTestStore(t)
	RegisterVaultCallbacks(store.DB())
	require.NoError(t, store.AddProvider(context.Background(), vaultTxProvider, vaultTxConfig(map[string]schemas.SecretVar{
		"X-Keep": {Val: "keep-secret"},
		"X-Drop": {Val: "drop-secret"},
	})))
	return store, stored, removed
}

const vaultTxProvider = schemas.ModelProvider("hyperpod")

func vaultTxConfig(headers map[string]schemas.SecretVar) ProviderConfig {
	return ProviderConfig{
		NetworkConfig:        &schemas.NetworkConfig{ExtraHeaders: headers},
		CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI},
	}
}

// storedHeaderRefs returns the provider's headers as persisted, so they carry vault refs
// rather than resolved values.
func storedHeaderRefs(t *testing.T, store *RDBConfigStore) map[string]schemas.SecretVar {
	t.Helper()
	loaded, err := store.GetProviderConfig(context.Background(), vaultTxProvider)
	require.NoError(t, err)
	return maps.Clone(loaded.NetworkConfig.ExtraHeaders)
}

// headerVaultPath returns the vault path the persisted row references for header.
func headerVaultPath(t *testing.T, store *RDBConfigStore, header string) string {
	t.Helper()
	ref := storedNetworkConfigHeaders(t, store.DB(), string(vaultTxProvider))[header]
	require.True(t, strings.HasPrefix(ref, "vault."), "header %s should be a vault ref, got %q", header, ref)
	return strings.TrimPrefix(ref, "vault.")
}

// pathsHolding returns every vault path the stub stored value at.
func pathsHolding(stored map[string]string, value string) []string {
	var paths []string
	for path, v := range stored {
		if v == value {
			paths = append(paths, path)
		}
	}
	return paths
}

// TestVaultCallbacks_TransactionOutcome pins vault writes and removals to the outcome of the
// transaction a save runs in. Nothing is removed before the commit. A rollback leaves every
// secret the restored row references intact and removes the secrets the failed save wrote.
// A commit removes the secrets the row stopped referencing.
func TestVaultCallbacks_TransactionOutcome(t *testing.T) {
	ctx := context.Background()

	t.Run("rollback after dropping a header keeps its secret", func(t *testing.T) {
		store, stored, removed := newVaultTxProvider(t)
		dropPath := headerVaultPath(t, store, "X-Drop")
		headers := storedHeaderRefs(t, store)
		delete(headers, "X-Drop")

		err := store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			require.NoError(t, store.UpdateProvider(ctx, vaultTxProvider, vaultTxConfig(headers), tx))
			return errLaterStep
		})
		require.ErrorIs(t, err, errLaterStep)

		require.Equal(t, dropPath, headerVaultPath(t, store, "X-Drop"), "the rollback restores the dropped header")
		require.NotContains(t, *removed, dropPath, "the restored row still references this secret")
		require.Equal(t, "drop-secret", stored[dropPath])
	})

	t.Run("rollback after changing a value keeps serving the old value", func(t *testing.T) {
		store, stored, _ := newVaultTxProvider(t)
		keepPath := headerVaultPath(t, store, "X-Keep")
		headers := storedHeaderRefs(t, store)
		headers["X-Keep"] = schemas.SecretVar{Val: "rotated-secret"}

		err := store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			require.NoError(t, store.UpdateProvider(ctx, vaultTxProvider, vaultTxConfig(headers), tx))
			return errLaterStep
		})
		require.ErrorIs(t, err, errLaterStep)

		require.Equal(t, keepPath, headerVaultPath(t, store, "X-Keep"))
		require.Equal(t, "keep-secret", stored[keepPath], "the restored row must still resolve to the old value")
	})

	t.Run("rollback removes the secrets the failed save wrote", func(t *testing.T) {
		store, stored, removed := newVaultTxProvider(t)
		headers := storedHeaderRefs(t, store)
		headers["X-New"] = schemas.SecretVar{Val: "new-secret"}

		err := store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			require.NoError(t, store.UpdateProvider(ctx, vaultTxProvider, vaultTxConfig(headers), tx))
			return errLaterStep
		})
		require.ErrorIs(t, err, errLaterStep)

		written := pathsHolding(stored, "new-secret")
		require.Len(t, written, 1)
		require.Equal(t, written, *removed, "no row references the secret the rolled-back save wrote")
	})

	t.Run("commit removes a dropped header's secret only after the commit", func(t *testing.T) {
		store, _, removed := newVaultTxProvider(t)
		dropPath := headerVaultPath(t, store, "X-Drop")
		headers := storedHeaderRefs(t, store)
		delete(headers, "X-Drop")

		require.NoError(t, store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			require.NoError(t, store.UpdateProvider(ctx, vaultTxProvider, vaultTxConfig(headers), tx))
			require.Empty(t, *removed, "nothing may be removed before the commit")
			return nil
		}))
		require.Equal(t, []string{dropPath}, *removed)
	})

	t.Run("commit removes the replaced value of a changed header", func(t *testing.T) {
		store, stored, removed := newVaultTxProvider(t)
		oldPath := headerVaultPath(t, store, "X-Keep")
		headers := storedHeaderRefs(t, store)
		headers["X-Keep"] = schemas.SecretVar{Val: "rotated-secret"}

		require.NoError(t, store.UpdateProvider(ctx, vaultTxProvider, vaultTxConfig(headers)))

		newPath := headerVaultPath(t, store, "X-Keep")
		require.NotEqual(t, oldPath, newPath, "a changed value is written to a new path, never over the old one")
		require.Equal(t, "rotated-secret", stored[newPath])
		require.Equal(t, []string{oldPath}, *removed)
	})

	t.Run("rollback after deleting the provider keeps its secrets", func(t *testing.T) {
		store, _, removed := newVaultTxProvider(t)

		err := store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			require.NoError(t, store.DeleteProvider(ctx, vaultTxProvider, tx))
			return errLaterStep
		})
		require.ErrorIs(t, err, errLaterStep)
		require.Empty(t, *removed, "the provider row is back, so its secrets must be too")
	})

	t.Run("a savepoint rolled back inside a committed transaction keeps the secrets it dropped", func(t *testing.T) {
		store, stored, removed := newVaultTxProvider(t)
		dropPath := headerVaultPath(t, store, "X-Drop")
		headers := storedHeaderRefs(t, store)
		delete(headers, "X-Drop")
		headers["X-New"] = schemas.SecretVar{Val: "new-secret"}

		require.NoError(t, store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			err := tx.Transaction(func(inner *gorm.DB) error {
				require.NoError(t, store.UpdateProvider(ctx, vaultTxProvider, vaultTxConfig(headers), inner))
				return errLaterStep
			})
			require.ErrorIs(t, err, errLaterStep)
			return nil
		}))

		require.Equal(t, dropPath, headerVaultPath(t, store, "X-Drop"))
		require.NotContains(t, *removed, dropPath)
		require.Equal(t, pathsHolding(stored, "new-secret"), *removed, "only the secret the rolled-back savepoint wrote is removed")
	})

	t.Run("a partial update removes nothing", func(t *testing.T) {
		store, _, removed := newVaultTxProvider(t)
		require.NoError(t, store.DB().Model(&tables.TableProvider{}).Where("name = ?", string(vaultTxProvider)).Update("status", "ok").Error)
		var row tables.TableProvider
		require.NoError(t, store.DB().Select("id", "name").First(&row, "name = ?", string(vaultTxProvider)).Error)
		require.NoError(t, store.DB().Model(&row).Updates(map[string]any{"description": "partial"}).Error)
		require.Empty(t, *removed)
	})
}

// TestVaultCallbacks_KeySecretsFollowTransaction covers a self-managed model, whose secrets
// are stored from its own BeforeSave: rotating a key's value through UpdateProvider keeps the
// old secret on rollback and removes it after commit, and dropping the key removes its secret.
func TestVaultCallbacks_KeySecretsFollowTransaction(t *testing.T) {
	stored, removed := stubVaultHooks(t)
	stubVaultResolve(t, stored)
	store := setupRDBTestStore(t)
	RegisterVaultCallbacks(store.DB())
	ctx := context.Background()
	withKey := func(value string) ProviderConfig {
		return ProviderConfig{Keys: []schemas.Key{{ID: "key-1", Name: "primary", Value: schemas.SecretVar{Val: value}, Weight: 1.0}}}
	}
	keyPath := func(t *testing.T) string {
		t.Helper()
		var row tables.TableKey
		require.NoError(t, store.DB().First(&row, "key_id = ?", "key-1").Error)
		return requireVaultRefUnder(t, row.Value.GetRawRef(), "bifrost/config_keys/key-1/value")
	}
	require.NoError(t, store.AddProvider(ctx, schemas.OpenAI, withKey("sk-old")))
	oldPath := keyPath(t)
	require.Equal(t, "sk-old", stored[oldPath])

	err := store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		require.NoError(t, store.UpdateProvider(ctx, schemas.OpenAI, withKey("sk-new"), tx))
		return errLaterStep
	})
	require.ErrorIs(t, err, errLaterStep)
	require.Equal(t, oldPath, keyPath(t))
	require.Equal(t, "sk-old", stored[oldPath], "the rolled-back key still resolves to its old value")
	require.Equal(t, pathsHolding(stored, "sk-new"), *removed, "only the rolled-back write is removed")

	before := len(*removed)
	require.NoError(t, store.UpdateProvider(ctx, schemas.OpenAI, withKey("sk-new")))
	newPath := keyPath(t)
	require.NotEqual(t, oldPath, newPath)
	require.Equal(t, []string{oldPath}, (*removed)[before:])

	before = len(*removed)
	require.NoError(t, store.UpdateProvider(ctx, schemas.OpenAI, ProviderConfig{}))
	require.Equal(t, []string{newPath}, (*removed)[before:], "a dropped key's secret is removed")
}
