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

func TestReplaceVirtualKeyProviderConfigsResolvesKeyIDsByName(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	seedKeyAndVirtualKey(t, store, "vk-replace")

	pc := parseProviderConfig(t, testKeyName)
	pc.VirtualKeyID = "vk-replace"

	require.NoError(t, store.DB().Transaction(func(tx *gorm.DB) error {
		return store.ReplaceVirtualKeyProviderConfigs(ctx, "vk-replace", []tables.TableVirtualKeyProviderConfig{pc}, tx)
	}))

	var configs []tables.TableVirtualKeyProviderConfig
	require.NoError(t, store.DB().Where("virtual_key_id = ?", "vk-replace").Find(&configs).Error)
	require.Len(t, configs, 1)
	assertAssociatedKey(t, store, configs[0].ID)
}
