package schemas

import (
	"context"
	"slices"
	"strings"
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

	if len(stored) != 3 {
		t.Fatalf("stored %d entries, want 3: %v", len(stored), stored)
	}
	for _, tc := range []struct {
		field  *SecretVar
		column string
		value  string
	}{
		{&m.Plain, "plain_col", "p1"},
		{m.Ptr, "ptr_col", "p2"},
		{&m.Snake, "snake", "p3"},
	} {
		path := assertStoredUnder(t, stored, tc.field, "bifrost/m/1/"+tc.column)
		if stored[path] != tc.value {
			t.Errorf("stored[%q] = %q, want %q", path, stored[path], tc.value)
		}
	}
}

// assertStoredUnder checks field was converted to a vault ref at prefix/<id> and returns the path.
func assertStoredUnder(t *testing.T, stored map[string]string, field *SecretVar, prefix string) string {
	t.Helper()
	if !field.IsFromVault() {
		t.Fatalf("field for %s should be vault-backed after store", prefix)
	}
	path := field.GetRef()
	id, ok := strings.CutPrefix(path, prefix+"/")
	if !ok || id == "" || strings.Contains(id, "/") {
		t.Fatalf("ref %q is not %s/<id>", path, prefix)
	}
	if _, ok := stored[path]; !ok {
		t.Fatalf("ref %q was not stored: %v", path, stored)
	}
	return path
}

// TestStoreOwnedVaultSecretVars_NewPathPerWrite pins that storing a field again never
// overwrites the secret an earlier write left: the stored row may still reference it
// until the transaction that replaces it commits.
func TestStoreOwnedVaultSecretVars_NewPathPerWrite(t *testing.T) {
	stored := withStubVaultStore(t)
	type model struct {
		Value SecretVar `gorm:"column:value"`
	}
	first := &model{Value: SecretVar{Val: "old"}}
	second := &model{Value: SecretVar{Val: "new"}}
	for _, m := range []*model{first, second} {
		if err := StoreOwnedVaultSecretVars(context.Background(), "bifrost/m/1", m); err != nil {
			t.Fatalf("StoreOwnedVaultSecretVars: %v", err)
		}
	}
	if first.Value.GetRef() == second.Value.GetRef() {
		t.Fatalf("both writes used %q", first.Value.GetRef())
	}
	if stored[first.Value.GetRef()] != "old" || stored[second.Value.GetRef()] != "new" {
		t.Errorf("an earlier secret was overwritten: %v", stored)
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
	auth := m.Headers["Authorization"]
	path := assertStoredUnder(t, stored, &auth, "bifrost/m/1/headers/Authorization")
	if stored[path] != "secret-token" {
		t.Errorf("stored Authorization = %q, want %q", stored[path], "secret-token")
	}
	if env := m.Headers["X-Env"]; env.IsFromVault() || env.GetRawRef() != "env.X" {
		t.Errorf("env-sourced header should be left as an env ref, got %q", env.GetRawRef())
	}
}

func TestVaultSecretPaths(t *testing.T) {
	type model struct {
		Plain   SecretVar
		Ptr     *SecretVar
		NilPtr  *SecretVar
		Env     SecretVar
		Literal SecretVar
		Headers map[string]SecretVar
	}
	m := &model{
		Plain:   SecretVar{ref: "vault.bifrost/m/1/plain/a", SecretType: SecretTypeVault},
		Ptr:     &SecretVar{ref: "vault.external/db#key", SecretType: SecretTypeVault},
		Env:     SecretVar{ref: "env.X", SecretType: SecretTypeEnv},
		Literal: SecretVar{Val: "plain"},
		Headers: map[string]SecretVar{"H": {ref: "vault.bifrost/m/1/headers/H/b", SecretType: SecretTypeVault}},
	}
	got := VaultSecretPaths(m)
	slices.Sort(got)
	want := []string{"bifrost/m/1/headers/H/b", "bifrost/m/1/plain/a", "external/db#key"}
	if !slices.Equal(got, want) {
		t.Errorf("VaultSecretPaths = %v, want %v", got, want)
	}
}

func TestOwnsVaultPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"bifrost/m/1/value/abc", true},
		{"bifrost/m/10/value/abc", false},
		{"bifrost/m/1", false},
		{"bifrost/m/1/value#key", false},
		{"", false},
	} {
		if got := OwnsVaultPath("bifrost/m/1", tc.path); got != tc.want {
			t.Errorf("OwnsVaultPath(%q) = %v, want %v", tc.path, got, tc.want)
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
