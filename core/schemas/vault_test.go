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

func TestStoreVaultEnvVar_StoresPlaintext(t *testing.T) {
	stored := withStubVaultStore(t)

	e := &EnvVar{Val: "secret-key"}
	if err := StoreVaultEnvVar(context.Background(), "bifrost/tbl/id/value", e); err != nil {
		t.Fatalf("StoreVaultEnvVar: %v", err)
	}
	if got := stored["bifrost/tbl/id/value"]; got != "secret-key" {
		t.Errorf("stored plaintext = %q, want %q", got, "secret-key")
	}
	if !e.FromVault {
		t.Error("FromVault should be true after store")
	}
	if e.VaultRef != "vault.bifrost/tbl/id/value" {
		t.Errorf("VaultRef = %q, want %q", e.VaultRef, "vault.bifrost/tbl/id/value")
	}
	if e.Val != "vault.bifrost/tbl/id/value" {
		t.Errorf("Val = %q, want rewritten to vault ref", e.Val)
	}
}

func TestStoreVaultEnvVar_NoOps(t *testing.T) {
	cases := []struct {
		name string
		e    *EnvVar
	}{
		{"nil", nil},
		{"env-sourced", &EnvVar{FromEnv: true, EnvVar: "MY_VAR"}},
		{"already-vault", &EnvVar{FromVault: true, VaultRef: "vault.some/path", Val: "vault.some/path"}},
		{"empty", &EnvVar{Val: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := withStubVaultStore(t)
			if err := StoreVaultEnvVar(context.Background(), "bifrost/tbl/id/f", tc.e); err != nil {
				t.Fatalf("StoreVaultEnvVar: %v", err)
			}
			if len(stored) != 0 {
				t.Errorf("expected no store, got %v", stored)
			}
		})
	}
}

func TestStoreVaultEnvVar_NoHookNoOp(t *testing.T) {
	prev := VaultStoreHook
	VaultStoreHook = nil
	t.Cleanup(func() { VaultStoreHook = prev })

	e := &EnvVar{Val: "secret"}
	if err := StoreVaultEnvVar(context.Background(), "p", e); err != nil {
		t.Fatalf("StoreVaultEnvVar: %v", err)
	}
	if e.FromVault || e.VaultRef != "" || e.Val != "secret" {
		t.Errorf("expected no mutation when hook nil, got %+v", e)
	}
}

func TestRemoveOwnedVaultEnvVars_SkipsFragmentRefs(t *testing.T) {
	var removed []string
	prev := VaultRemoveHook
	VaultRemoveHook = func(_ context.Context, path string) error {
		removed = append(removed, path)
		return nil
	}
	t.Cleanup(func() { VaultRemoveHook = prev })

	type model struct {
		Normal   EnvVar `gorm:"column:normal"`
		Fragment EnvVar `gorm:"column:fragment"`
	}
	m := &model{
		Normal:   EnvVar{FromVault: true, VaultRef: "vault.bifrost/m/1/normal", Val: "vault.bifrost/m/1/normal"},
		Fragment: EnvVar{FromVault: true, VaultRef: "vault.external/db#apiKey", Val: "vault.external/db#apiKey"},
	}

	RemoveOwnedVaultEnvVars(context.Background(), "bifrost/m/1", m)

	if len(removed) != 1 || removed[0] != "bifrost/m/1/normal" {
		t.Errorf("removed = %v, want only [bifrost/m/1/normal]", removed)
	}
}

func TestStoreOwnedVaultEnvVars_WalksFields(t *testing.T) {
	stored := withStubVaultStore(t)

	type model struct {
		Plain    EnvVar  `gorm:"column:plain_col"`
		Ptr      *EnvVar `gorm:"column:ptr_col"`
		NilPtr   *EnvVar
		Snake    EnvVar // no gorm tag -> snake_case of field name
		Ignored  string
		EnvBased EnvVar `gorm:"column:env_col"`
	}
	m := &model{
		Plain:    EnvVar{Val: "p1"},
		Ptr:      &EnvVar{Val: "p2"},
		Snake:    EnvVar{Val: "p3"},
		EnvBased: EnvVar{FromEnv: true, EnvVar: "X"},
	}

	if err := StoreOwnedVaultEnvVars(context.Background(), "bifrost/m/1", m); err != nil {
		t.Fatalf("StoreOwnedVaultEnvVars: %v", err)
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
	if !m.Plain.FromVault || !m.Ptr.FromVault || !m.Snake.FromVault {
		t.Error("EnvVar fields should be marked FromVault after store")
	}
}
