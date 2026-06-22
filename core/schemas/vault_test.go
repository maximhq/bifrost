package schemas

import (
	"context"
	"testing"
)

// withStubVaultStore installs a VaultStoreHook that records stored paths and
// rewrites the value to "vault.<path>", mimicking vault.StoreString. It restores
// the previous hook on cleanup.
func withStubVaultStore(t *testing.T) map[string]string {
	t.Helper()
	stored := make(map[string]string)
	prev := VaultStoreHook
	VaultStoreHook = func(_ context.Context, path string, value *string) error {
		stored[path] = *value
		*value = "vault." + path
		return nil
	}
	t.Cleanup(func() { VaultStoreHook = prev })
	return stored
}

func TestStoreVaultSecretVar_StoresPlaintext(t *testing.T) {
	stored := withStubVaultStore(t)

	e := &SecretVar{Val: "secret-key"}
	if err := StoreVaultSecretVar(context.Background(), "bifrost/tbl/id/value", e); err != nil {
		t.Fatalf("StoreVaultSecretVar: %v", err)
	}
	if got := stored["bifrost/tbl/id/value"]; got != "secret-key" {
		t.Errorf("stored plaintext = %q, want %q", got, "secret-key")
	}
	if !e.IsFromVault() {
		t.Error("IsFromVault() should be true after store")
	}
	if e.GetRawRef() != "vault.bifrost/tbl/id/value" {
		t.Errorf("Ref() = %q, want %q", e.GetRawRef(), "vault.bifrost/tbl/id/value")
	}
	if e.Val != "vault.bifrost/tbl/id/value" {
		t.Errorf("Val = %q, want rewritten to vault ref", e.Val)
	}
}

func TestStoreVaultSecretVar_NoOps(t *testing.T) {
	cases := []struct {
		name string
		e    *SecretVar
	}{
		{"nil", nil},
		{"env-sourced", &SecretVar{ref: "env.MY_VAR", SecretType: SecretTypeEnv}},
		{"already-vault", &SecretVar{Val: "vault.some/path", ref: "vault.some/path", SecretType: SecretTypeVault}},
		{"empty", &SecretVar{Val: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := withStubVaultStore(t)
			if err := StoreVaultSecretVar(context.Background(), "bifrost/tbl/id/f", tc.e); err != nil {
				t.Fatalf("StoreVaultSecretVar: %v", err)
			}
			if len(stored) != 0 {
				t.Errorf("expected no store, got %v", stored)
			}
		})
	}
}

func TestStoreVaultSecretVar_NoHookNoOp(t *testing.T) {
	prev := VaultStoreHook
	VaultStoreHook = nil
	t.Cleanup(func() { VaultStoreHook = prev })

	e := &SecretVar{Val: "secret"}
	if err := StoreVaultSecretVar(context.Background(), "p", e); err != nil {
		t.Fatalf("StoreVaultSecretVar: %v", err)
	}
	if e.IsFromSecret() || e.GetRawRef() != "" || e.Val != "secret" {
		t.Errorf("expected no mutation when hook nil, got val=%q ref=%q fromSecret=%v", e.Val, e.GetRawRef(), e.IsFromSecret())
	}
}

func TestRemoveOwnedVaultSecretVars_SkipsFragmentRefs(t *testing.T) {
	var removed []string
	prev := VaultRemoveHook
	VaultRemoveHook = func(_ context.Context, path string) error {
		removed = append(removed, path)
		return nil
	}
	t.Cleanup(func() { VaultRemoveHook = prev })

	type model struct {
		Normal   SecretVar `gorm:"column:normal"`
		Fragment SecretVar `gorm:"column:fragment"`
	}
	m := &model{
		Normal:   SecretVar{Val: "vault.bifrost/m/1/normal", ref: "vault.bifrost/m/1/normal", SecretType: SecretTypeVault},
		Fragment: SecretVar{Val: "vault.external/db#apiKey", ref: "vault.external/db#apiKey", SecretType: SecretTypeVault},
	}

	RemoveOwnedVaultSecretVars(context.Background(), "bifrost/m/1", m)

	if len(removed) != 1 || removed[0] != "bifrost/m/1/normal" {
		t.Errorf("removed = %v, want only [bifrost/m/1/normal]", removed)
	}
}

func TestStoreOwnedVaultSecretVars_WalksFields(t *testing.T) {
	stored := withStubVaultStore(t)

	type model struct {
		Plain    SecretVar  `gorm:"column:plain_col"`
		Ptr      *SecretVar `gorm:"column:ptr_col"`
		NilPtr   *SecretVar
		Snake    SecretVar // no gorm tag -> snake_case of field name
		Ignored  string
		EnvBased SecretVar `gorm:"column:env_col"`
	}
	m := &model{
		Plain:    SecretVar{Val: "p1"},
		Ptr:      &SecretVar{Val: "p2"},
		Snake:    SecretVar{Val: "p3"},
		EnvBased: SecretVar{ref: "env.X", SecretType: SecretTypeEnv},
	}

	if err := StoreOwnedVaultSecretVars(context.Background(), "bifrost/m/1", m); err != nil {
		t.Fatalf("StoreOwnedVaultSecretVars: %v", err)
	}

	want := map[string]string{
		"bifrost/m/1/plain_col": "p1",
		"bifrost/m/1/ptr_col":   "p2",
		"bifrost/m/1/snake":     "p3",
	}
	if len(stored) != len(want) {
		t.Fatalf("stored %d entries, want %d: %v", len(stored), len(want), stored)
	}
	for path, val := range want {
		if stored[path] != val {
			t.Errorf("stored[%q] = %q, want %q", path, stored[path], val)
		}
	}
	if !m.Plain.IsFromVault() || !m.Ptr.IsFromVault() || !m.Snake.IsFromVault() {
		t.Error("SecretVar fields should be vault-backed after store")
	}
}

func TestStoreOwnedVaultSecretVars_WalksMap(t *testing.T) {
	stored := withStubVaultStore(t)

	type model struct {
		Headers map[string]SecretVar `gorm:"column:headers"`
	}
	m := &model{
		Headers: map[string]SecretVar{
			"Authorization": {Val: "secret-token"},
			"X-Env":         SecretVar{ref: "env.X", SecretType: SecretTypeEnv},
		},
	}

	if err := StoreOwnedVaultSecretVars(context.Background(), "bifrost/m/1", m); err != nil {
		t.Fatalf("StoreOwnedVaultSecretVars: %v", err)
	}

	if len(stored) != 1 {
		t.Fatalf("stored %d entries, want 1: %v", len(stored), stored)
	}
	if got := stored["bifrost/m/1/headers/Authorization"]; got != "secret-token" {
		t.Errorf("stored Authorization = %q, want %q", got, "secret-token")
	}
	auth := m.Headers["Authorization"]
	if !auth.IsFromVault() || auth.GetRawRef() != "vault.bifrost/m/1/headers/Authorization" {
		t.Errorf("map entry not converted to vault ref: val=%q ref=%q fromVault=%v", auth.Val, auth.GetRawRef(), auth.IsFromVault())
	}
	if env := m.Headers["X-Env"]; env.IsFromSecret() && env.GetRawRef() == "env.X" {
		if stored["bifrost/m/1/headers/X-Env"] != "" {
			t.Error("env-sourced header should not be vault-stored")
		}
	}
}

func TestRemoveOwnedVaultSecretVars_WalksMap(t *testing.T) {
	var removed []string
	prev := VaultRemoveHook
	VaultRemoveHook = func(_ context.Context, path string) error {
		removed = append(removed, path)
		return nil
	}
	t.Cleanup(func() { VaultRemoveHook = prev })

	type model struct {
		Headers map[string]SecretVar `gorm:"column:headers"`
	}
	m := &model{
		Headers: map[string]SecretVar{
			"Owned":    SecretVar{Val: "vault.bifrost/m/1/headers/Owned", ref: "vault.bifrost/m/1/headers/Owned", SecretType: SecretTypeVault},
			"External": SecretVar{Val: "vault.external/db#key", ref: "vault.external/db#key", SecretType: SecretTypeVault},
		},
	}

	RemoveOwnedVaultSecretVars(context.Background(), "bifrost/m/1", m)

	if len(removed) != 1 || removed[0] != "bifrost/m/1/headers/Owned" {
		t.Errorf("removed = %v, want only [bifrost/m/1/headers/Owned]", removed)
	}
}
