package configstore

import (
	"context"
	"encoding/json"
	"fmt"
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
			require.Equal(t, "vault."+secretPath, row.BedrockSecretKey.Val, "column should store vault ref")
		})
	}
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
