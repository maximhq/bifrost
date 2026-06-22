package schemas

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"unicode"
)

const defaultVaultPrefix = "bifrost"

// VaultResolveHook is wired by enterprise startup to the vault registry's ResolveString.
// It is nil in OSS deployments; GetValue() is a no-op when nil.
var VaultResolveHook func(ctx context.Context, value *string) error

// VaultRemoveHook deletes the secret at path (best-effort; errors are ignored by callers).
// It is nil in OSS deployments.
var VaultRemoveHook func(ctx context.Context, path string) error

// VaultStoreHook stores a plaintext secret at path and rewrites *value to a
// "vault.<canonical>" reference. It is wired by enterprise startup to the vault
// registry's StoreString and is nil in OSS deployments (store helpers no-op).
var VaultStoreHook func(ctx context.Context, path string, value *string) error

// VaultPrefixHook returns the configured vault path prefix (e.g. "bifrost").
// It is nil in OSS deployments; VaultPrefix() falls back to "bifrost".
var VaultPrefixHook func() string

// VaultStoreWriteEnabled reports whether vault write storage is available (i.e. VaultStoreHook
// has been wired by enterprise startup). Use this to guard StoreOwnedVaultSecretVars / RemoveOwnedVaultSecretVars calls in BeforeSave hooks, since those
// calls in BeforeSave hooks.
func VaultStoreWriteEnabled() bool {
	return VaultStoreHook != nil && VaultRemoveHook != nil
}

// VaultPrefix returns the configured vault path prefix, defaulting to "bifrost".
func VaultPrefix() string {
	if VaultPrefixHook != nil {
		return VaultPrefixHook()
	}
	return defaultVaultPrefix
}

// VaultBasePath returns the standard vault path prefix for a table row.
func VaultBasePath(tableName, primaryKey string) string {
	return fmt.Sprintf("%s/%s/%s", VaultPrefix(), tableName, primaryKey)
}

// LookupVault resolves a vault reference string (e.g. "vault.path/to/secret") via
// the registered resolver, returning the resolved secret and true on success —
// analogous to os.LookupEnv. Returns ("", false) when ref doesn't have the "vault."
// prefix or no resolver is registered (OSS deployments / before enterprise startup).
func LookupVault(ref string) (string, bool) {
	if !strings.HasPrefix(ref, "vault.") || VaultResolveHook == nil {
		return "", false
	}
	val := ref
	if err := VaultResolveHook(context.Background(), &val); err != nil {
		return "", false
	}
	return val, true
}

// VaultPathKeyer is implemented by GORM models that own vault secrets. The
// global vault callback uses VaultPathKey() (together with the table name) to
// build the base path for auto-store and auto-remove, so individual models do
// not need to wire StoreOwnedVaultSecretVars / RemoveOwnedVaultSecretVars manually.
type VaultPathKeyer interface {
	VaultPathKey() string
}

var (
	secretVarType    = reflect.TypeOf(SecretVar{})
	secretVarPtrType = reflect.TypeOf((*SecretVar)(nil))
	secretVarMapType = reflect.TypeOf(map[string]SecretVar{})
)

// RemoveOwnedVaultSecretVars best-effort deletes the vault secret for every
// SecretVar / *SecretVar field in model whose VaultRef starts with
// ownedPrefix+"/". Refs outside that prefix are user-provided and are left alone.
func RemoveOwnedVaultSecretVars(ctx context.Context, ownedPrefix string, model interface{}) {
	if VaultRemoveHook == nil {
		return
	}
	rv := reflect.ValueOf(model)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		fv := rv.Field(i)
		if fv.Type() == secretVarMapType {
			iter := fv.MapRange()
			for iter.Next() {
				e := iter.Value().Interface().(SecretVar)
				removeOwnedVaultSecretVar(ctx, ownedPrefix, &e)
			}
			continue
		}
		var field *SecretVar
		switch fv.Type() {
		case secretVarType:
			field = fv.Addr().Interface().(*SecretVar)
		case secretVarPtrType:
			if !fv.IsNil() {
				field = fv.Interface().(*SecretVar)
			}
		}
		removeOwnedVaultSecretVar(ctx, ownedPrefix, field)
	}
}

// removeOwnedVaultSecretVar removes a single SecretVar's vault secret if it is a
// vault-backed, non-fragment reference under ownedPrefix. Fragment refs (#key)
// point at shared, externally-managed secrets and are never auto-deleted.
func removeOwnedVaultSecretVar(ctx context.Context, ownedPrefix string, field *SecretVar) {
	path := field.GetRef()
	if path == "" {
		return
	}
	if strings.IndexByte(path, '#') >= 0 {
		return
	}
	if !strings.HasPrefix(path, ownedPrefix+"/") {
		return
	}
	_ = VaultRemoveHook(ctx, path)
}

// StoreVaultSecretVar pushes a single plaintext SecretVar value into the vault at path
// and converts the field to a vault reference. No-op when vault disabled, field
// is nil, env/vault-sourced, empty, or redacted.
func StoreVaultSecretVar(ctx context.Context, path string, e *SecretVar) error {
	if VaultStoreHook == nil || e == nil {
		return nil
	}
	if e.IsFromSecret() || e.Val == "" || e.IsRedacted() {
		return nil
	}
	if err := VaultStoreHook(ctx, path, &e.Val); err != nil {
		return err
	}
	e.ref = "vault." + path
	e.SecretType = SecretTypeVault
	return nil
}

// StoreOwnedVaultSecretVars stores every plaintext SecretVar / *SecretVar struct field of
// model into the vault under basePath/<column>, converting each to a vault ref.
// The reflection walk mirrors RemoveOwnedVaultSecretVars.
func StoreOwnedVaultSecretVars(ctx context.Context, basePath string, model interface{}) error {
	if VaultStoreHook == nil {
		return nil
	}
	rv := reflect.ValueOf(model)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		fv := rv.Field(i)
		seg := vaultFieldSegment(rt.Field(i))
		// map[string]SecretVar (e.g. MCP Headers): each entry gets its own secret
		// at basePath/<column>/<mapKey>. Map values are not addressable, so copy
		// out, store (mutates the copy to a ref), then write back.
		if fv.Type() == secretVarMapType {
			iter := fv.MapRange()
			for iter.Next() {
				key := iter.Key()
				e := iter.Value().Interface().(SecretVar)
				path := basePath + "/" + seg + "/" + url.PathEscape(key.String())
				if err := StoreVaultSecretVar(ctx, path, &e); err != nil {
					return fmt.Errorf("vault store field %s[%s]: %w", rt.Field(i).Name, key.String(), err)
				}
				fv.SetMapIndex(key, reflect.ValueOf(e))
			}
			continue
		}
		var field *SecretVar
		switch fv.Type() {
		case secretVarType:
			field = fv.Addr().Interface().(*SecretVar)
		case secretVarPtrType:
			if !fv.IsNil() {
				field = fv.Interface().(*SecretVar)
			}
		default:
			continue
		}
		if field == nil {
			continue
		}
		path := basePath + "/" + seg
		if err := StoreVaultSecretVar(ctx, path, field); err != nil {
			return fmt.Errorf("vault store field %s: %w", rt.Field(i).Name, err)
		}
	}
	return nil
}

// vaultFieldSegment returns the vault path segment for a struct field.
// It prefers the gorm column tag; otherwise it converts the Go field name to snake_case.
func vaultFieldSegment(f reflect.StructField) string {
	if tag := f.Tag.Get("gorm"); tag != "" {
		for _, part := range strings.Split(tag, ";") {
			if col, ok := strings.CutPrefix(strings.TrimSpace(part), "column:"); ok {
				col = strings.TrimSpace(col)
				if col != "" {
					return col
				}
			}
		}
	}
	return toSnakeCase(f.Name)
}

func toSnakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
