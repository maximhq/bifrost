package configstore

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Regression tests for issue #5980: the governance docs tell config.json users
// to reference provider keys by name in key_ids, but the parsed entries only
// carried KeyID, so the store's provider-scoped name fallback never ran and
// startup failed with ErrUnresolvedKeys.

const (
	testKeyName = "my-provider-key"
	testKeyUUID = "0d3a1c1e-1111-2222-3333-444455556666"
)

func seedKeyAndVirtualKey(t *testing.T, store *RDBConfigStore, vkID string) {
	t.Helper()
	key := tables.TableKey{
		Name:     testKeyName,
		Provider: "openai",
		KeyID:    testKeyUUID,
		Value:    *schemas.NewSecretVar("sk-test"),
	}
	require.NoError(t, store.DB().Create(&key).Error)
	vk := tables.TableVirtualKey{
		ID:    vkID,
		Name:  vkID,
		Value: *schemas.NewSecretVar("bfvk-" + vkID),
	}
	require.NoError(t, store.DB().Create(&vk).Error)
}

// parseProviderConfig runs a provider config through the same UnmarshalJSON
// path config.json entries take.
func parseProviderConfig(t *testing.T, keyIDsEntry string) tables.TableVirtualKeyProviderConfig {
	t.Helper()
	var pc tables.TableVirtualKeyProviderConfig
	require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{
		"provider": "openai",
		"allowed_models": ["*"],
		"key_ids": [%q]
	}`, keyIDsEntry)), &pc))
	return pc
}

func assertAssociatedKey(t *testing.T, store *RDBConfigStore, pcID uint) {
	t.Helper()
	var joins []tables.TableVirtualKeyProviderConfigKey
	require.NoError(t, store.DB().Where("table_virtual_key_provider_config_id = ?", pcID).Find(&joins).Error)
	require.Len(t, joins, 1)
	var dbKey tables.TableKey
	require.NoError(t, store.DB().First(&dbKey, joins[0].TableKeyID).Error)
	require.Equal(t, testKeyUUID, dbKey.KeyID)
}

func TestCreateVirtualKeyProviderConfigResolvesKeyIDsByName(t *testing.T) {
	tests := []struct {
		name        string
		keyIDsEntry string
		wantErr     bool
	}{
		{name: "key name resolves", keyIDsEntry: testKeyName},
		{name: "uuid still resolves", keyIDsEntry: testKeyUUID},
		{name: "unknown entry still fails", keyIDsEntry: "no-such-key", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := setupRDBTestStore(t)
			ctx := context.Background()
			seedKeyAndVirtualKey(t, store, "vk-create")

			pc := parseProviderConfig(t, tt.keyIDsEntry)
			pc.VirtualKeyID = "vk-create"

			err := store.CreateVirtualKeyProviderConfig(ctx, &pc)
			if tt.wantErr {
				var unresolved *ErrUnresolvedKeys
				require.ErrorAs(t, err, &unresolved)
				return
			}
			require.NoError(t, err)
			assertAssociatedKey(t, store, pc.ID)
		})
	}
}

func replaceConfigs(store *RDBConfigStore, ctx context.Context, vkID string, configs []tables.TableVirtualKeyProviderConfig) error {
	return store.DB().Transaction(func(tx *gorm.DB) error {
		return store.ReplaceVirtualKeyProviderConfigs(ctx, vkID, configs, tx)
	})
}

func TestReplaceVirtualKeyProviderConfigsResolvesKeyIDsByName(t *testing.T) {
	tests := []struct {
		name        string
		keyIDsEntry string
		wantErr     bool
	}{
		{name: "key name resolves", keyIDsEntry: testKeyName},
		{name: "uuid still resolves", keyIDsEntry: testKeyUUID},
		{name: "unknown entry still fails", keyIDsEntry: "no-such-key", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := setupRDBTestStore(t)
			ctx := context.Background()
			seedKeyAndVirtualKey(t, store, "vk-replace")

			pc := parseProviderConfig(t, tt.keyIDsEntry)
			pc.VirtualKeyID = "vk-replace"

			err := replaceConfigs(store, ctx, "vk-replace", []tables.TableVirtualKeyProviderConfig{pc})
			if tt.wantErr {
				var unresolved *ErrUnresolvedKeys
				require.ErrorAs(t, err, &unresolved)
				return
			}
			require.NoError(t, err)

			var configs []tables.TableVirtualKeyProviderConfig
			require.NoError(t, store.DB().Where("virtual_key_id = ?", "vk-replace").Find(&configs).Error)
			require.Len(t, configs, 1)
			assertAssociatedKey(t, store, configs[0].ID)
		})
	}
}

// Mixed identifiers across several configs in one replacement call: name and
// UUID entries resolve through the batched per-column lookups independently.
func TestReplaceVirtualKeyProviderConfigsResolvesMixedIdentifiers(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	seedKeyAndVirtualKey(t, store, "vk-mixed")
	anthropicKey := tables.TableKey{
		Name:     "my-anthropic-key",
		Provider: "anthropic",
		KeyID:    "9f8e7d6c-aaaa-bbbb-cccc-ddddeeeeffff",
		Value:    *schemas.NewSecretVar("sk-ant-test"),
	}
	require.NoError(t, store.DB().Create(&anthropicKey).Error)

	openaiPC := parseProviderConfig(t, testKeyName) // by name
	openaiPC.VirtualKeyID = "vk-mixed"
	var anthropicPC tables.TableVirtualKeyProviderConfig
	require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{
		"provider": "anthropic",
		"allowed_models": ["*"],
		"key_ids": [%q]
	}`, anthropicKey.KeyID)), &anthropicPC)) // by uuid
	anthropicPC.VirtualKeyID = "vk-mixed"

	require.NoError(t, replaceConfigs(store, ctx, "vk-mixed", []tables.TableVirtualKeyProviderConfig{openaiPC, anthropicPC}))

	var configs []tables.TableVirtualKeyProviderConfig
	require.NoError(t, store.DB().Where("virtual_key_id = ?", "vk-mixed").Order("provider").Find(&configs).Error)
	require.Len(t, configs, 2)
	wantKeyIDs := map[string]string{"anthropic": anthropicKey.KeyID, "openai": testKeyUUID}
	for _, pc := range configs {
		var joins []tables.TableVirtualKeyProviderConfigKey
		require.NoError(t, store.DB().Where("table_virtual_key_provider_config_id = ?", pc.ID).Find(&joins).Error)
		require.Len(t, joins, 1)
		var dbKey tables.TableKey
		require.NoError(t, store.DB().First(&dbKey, joins[0].TableKeyID).Error)
		require.Equal(t, wantKeyIDs[pc.Provider], dbKey.KeyID)
	}
}
