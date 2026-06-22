package tables

import (
	"context"
	"encoding/json"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTableVirtualKey_EnvSourcedRoundTrip verifies that an env-sourced VK hashes the RESOLVED
// value (so the x-bf-vk lookup keeps working) and round-trips the env reference, mirroring
// TableKey.Value behavior.
func TestTableVirtualKey_EnvSourcedRoundTrip(t *testing.T) {
	db := setupTestDB(t)

	t.Setenv("TEST_VK_ENV", "sk-bf-env-resolved")
	sv := schemas.NewSecretVar("env.TEST_VK_ENV")
	require.True(t, sv.IsFromSecret())
	require.Equal(t, "sk-bf-env-resolved", sv.GetValue())

	vk := &TableVirtualKey{
		ID:       "vk-env",
		Name:     "env-vk",
		Value:    *sv,
		IsActive: bifrost.Ptr(true),
	}
	require.NoError(t, db.Create(vk).Error)

	raw := rawRow(t, db, "governance_virtual_keys", "vk-env")
	// The hash must be of the resolved plaintext so x-bf-vk lookup matches the client value.
	assert.Equal(t, encrypt.HashSHA256("sk-bf-env-resolved"), raw["value_hash"])

	var found TableVirtualKey
	require.NoError(t, db.First(&found, "id = ?", "vk-env").Error)
	assert.True(t, found.Value.IsFromSecret())
	assert.Equal(t, "env.TEST_VK_ENV", found.Value.GetRawRef())
	assert.Equal(t, "sk-bf-env-resolved", found.Value.GetValue())
}

// TestTableVirtualKey_LiteralRoundTrip verifies a plain literal value encrypts at rest and
// decrypts back to the same plaintext.
func TestTableVirtualKey_LiteralRoundTrip(t *testing.T) {
	db := setupTestDB(t)

	vk := &TableVirtualKey{
		ID:       "vk-lit",
		Name:     "literal-vk",
		Value:    *schemas.NewSecretVar("sk-bf-literal"),
		IsActive: bifrost.Ptr(true),
	}
	require.NoError(t, db.Create(vk).Error)

	raw := rawRow(t, db, "governance_virtual_keys", "vk-lit")
	assert.NotEqual(t, "sk-bf-literal", raw["value"]) // encrypted at rest

	var found TableVirtualKey
	require.NoError(t, db.First(&found, "id = ?", "vk-lit").Error)
	assert.False(t, found.Value.IsFromSecret())
	assert.False(t, found.Value.IsFromVault())
	assert.Equal(t, "sk-bf-literal", found.Value.GetValue())
}

// TestTableVirtualKey_HashStableAcrossEnvAndLiteral verifies that two VKs resolving to the same
// plaintext (one literal, one env-sourced) produce the same value_hash, so lookup-by-value is
// source-agnostic.
func TestTableVirtualKey_HashStableAcrossEnvAndLiteral(t *testing.T) {
	t.Setenv("TEST_VK_SAME", "sk-bf-shared")
	sv := schemas.NewSecretVar("env.TEST_VK_SAME")

	literal := &TableVirtualKey{ID: "a", Name: "a", Value: *schemas.NewSecretVar("sk-bf-shared")}
	envSourced := &TableVirtualKey{ID: "b", Name: "b", Value: *sv}

	require.NoError(t, literal.BeforeSave(nil))
	require.NoError(t, envSourced.BeforeSave(nil))

	assert.Equal(t, literal.ValueHash, envSourced.ValueHash)
}

// TestTableVirtualKey_MarshalJSON_SecretVarShape verifies the JSON `value` is a SecretVar object
// carrying the env source metadata (like provider keys).
func TestTableVirtualKey_MarshalJSON_SecretVarShape(t *testing.T) {
	t.Setenv("TEST_VK_MARSHAL", "sk-bf-secret-1234")
	vk := TableVirtualKey{
		ID:    "e",
		Name:  "e",
		Value: *schemas.NewSecretVar("env.TEST_VK_MARSHAL"),
	}
	data, err := json.Marshal(vk)
	require.NoError(t, err)
	var out struct {
		Value schemas.SecretVar `json:"value"`
	}
	require.NoError(t, json.Unmarshal(data, &out))
	assert.True(t, out.Value.IsFromSecret())
	assert.Equal(t, "env.TEST_VK_MARSHAL", out.Value.GetRawRef())
}

// TestTableVirtualKey_UnmarshalJSON_Forms verifies `value` accepts a bare string and an "env.X"
// string, resolving env references like TableKey.Value.
func TestTableVirtualKey_UnmarshalJSON_Forms(t *testing.T) {
	t.Setenv("TEST_VK_UMARSHAL", "sk-bf-um")

	t.Run("bare string", func(t *testing.T) {
		var vk TableVirtualKey
		require.NoError(t, json.Unmarshal([]byte(`{"name":"n","value":"sk-bf-plain"}`), &vk))
		assert.Equal(t, "sk-bf-plain", vk.Value.GetValue())
		assert.False(t, vk.Value.IsFromSecret())
	})

	t.Run("env string", func(t *testing.T) {
		var vk TableVirtualKey
		require.NoError(t, json.Unmarshal([]byte(`{"name":"n","value":"env.TEST_VK_UMARSHAL"}`), &vk))
		assert.Equal(t, "sk-bf-um", vk.Value.GetValue())
		assert.True(t, vk.Value.IsFromSecret())
		assert.Equal(t, "env.TEST_VK_UMARSHAL", vk.Value.GetRawRef())
	})
}

// TestTableVirtualKey_VaultRotation_ReSaveRefreshesHash simulates a vault secret rotation + cache
// flush: re-Saving a vault-sourced VK must recompute value_hash from the NEW resolved value, keep
// the vault reference in the value column, and never write the secret back to the vault. This is
// the single-writer DB effect behind ReloadVaultSourcedVirtualKeys(reSave=true).
func TestTableVirtualKey_VaultRotation_ReSaveRefreshesHash(t *testing.T) {
	db := setupTestDB(t)

	const vaultRef = "vault.secret/vk-rotate"
	current := "sk-bf-vault-v1"

	prevResolve := schemas.VaultResolveHook
	prevStore := schemas.VaultStoreHook
	prevRemove := schemas.VaultRemoveHook
	schemas.VaultResolveHook = func(_ context.Context, value *string) error {
		if *value == vaultRef {
			*value = current
		}
		return nil
	}
	// Write hooks make VaultStoreWriteEnabled() true; the store hook must NEVER fire for a
	// vault-sourced (read-only ref) VK.
	schemas.VaultStoreHook = func(_ context.Context, _ string, _ *string) error {
		t.Fatalf("vault store hook must not be called for a vault-sourced virtual key")
		return nil
	}
	schemas.VaultRemoveHook = func(_ context.Context, _ string) error { return nil }
	t.Cleanup(func() {
		schemas.VaultResolveHook = prevResolve
		schemas.VaultStoreHook = prevStore
		schemas.VaultRemoveHook = prevRemove
	})

	vk := &TableVirtualKey{
		ID:       "vk-rotate",
		Name:     "vault-rotate-vk",
		Value:    *schemas.NewSecretVar(vaultRef),
		IsActive: bifrost.Ptr(true),
	}
	require.Equal(t, "sk-bf-vault-v1", vk.Value.GetValue())
	require.NoError(t, db.Create(vk).Error)

	raw := rawRow(t, db, "governance_virtual_keys", "vk-rotate")
	assert.Equal(t, vaultRef, raw["value"], "column stores the vault reference, not the secret")
	assert.Equal(t, encrypt.HashSHA256("sk-bf-vault-v1"), raw["value_hash"])

	// Rotate the backend secret (the vault cache flush would force re-resolution).
	current = "sk-bf-vault-v2"

	// Re-read: AfterFind/Scan re-resolves the ref to the new secret.
	var found TableVirtualKey
	require.NoError(t, db.First(&found, "id = ?", "vk-rotate").Error)
	require.Equal(t, "sk-bf-vault-v2", found.Value.GetValue())

	// Re-Save (the single writer): BeforeSave recomputes value_hash from the new value.
	require.NoError(t, db.Save(&found).Error)

	raw = rawRow(t, db, "governance_virtual_keys", "vk-rotate")
	assert.Equal(t, vaultRef, raw["value"], "value column still holds the vault reference after re-save")
	assert.Equal(t, encrypt.HashSHA256("sk-bf-vault-v2"), raw["value_hash"], "value_hash refreshed to the rotated secret")
}
