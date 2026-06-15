package tables

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
)

const (
	// EncryptionStatusPlainText indicates the row's sensitive fields are stored as plaintext.
	EncryptionStatusPlainText = "plain_text"
	// EncryptionStatusEncrypted indicates the row's sensitive fields have been encrypted.
	EncryptionStatusEncrypted = "encrypted"
	// EncryptionStatusVault indicates the row's sensitive fields are stored as vault references.
	EncryptionStatusVault = "vault"

	// defaultVaultPrefix is the path prefix used when VaultHooks.Prefix is not set.
	defaultVaultPrefix = "bifrost"
)

// VaultHooks is populated at startup when a vault backend is configured.
// OSS table hooks check these function pointers before falling through to AES encryption.
var VaultHooks struct {
	// IsEnabled reports whether vault is active.
	IsEnabled func() bool
	// Prefix returns the configured vault path prefix (e.g. "bifrost").
	Prefix func() string
	// StoreString vaults *value at path, then replaces *value with the vault reference.
	StoreString func(ctx context.Context, path string, value *string) error
	// ResolveString resolves a vault reference, replacing *value with the secret.
	ResolveString func(ctx context.Context, value *string) error
	// Remove deletes the secret at path (best-effort; errors are ignored by callers).
	Remove func(ctx context.Context, path string) error
}

func VaultIsEnabled() bool {
	return VaultHooks.IsEnabled != nil && VaultHooks.IsEnabled() &&
		VaultHooks.StoreString != nil && VaultHooks.ResolveString != nil
}

func VaultPrefix() string {
	if VaultHooks.Prefix != nil {
		return VaultHooks.Prefix()
	}
	return defaultVaultPrefix
}

// vaultEnvVar vaults the Val field of an EnvVar at path, replacing it with a vault reference.
// No-op if nil, references an env var, or empty. Returns an error if the hook is not configured.
func vaultEnvVar(ctx context.Context, path string, field *schemas.EnvVar) error {
	if field == nil || field.IsFromEnv() || field.GetValue() == "" {
		return nil
	}
	if VaultHooks.StoreString == nil {
		return fmt.Errorf("vault store hook is not configured")
	}
	return VaultHooks.StoreString(ctx, path, &field.Val)
}

// resolveVaultEnvVar resolves a vault reference stored in EnvVar.Val.
// No-op if nil, references an env var, or empty. Returns an error if the hook is not configured.
func resolveVaultEnvVar(ctx context.Context, field *schemas.EnvVar) error {
	if field == nil || field.IsFromEnv() || field.GetValue() == "" {
		return nil
	}
	if VaultHooks.ResolveString == nil {
		return fmt.Errorf("vault resolve hook is not configured")
	}
	return VaultHooks.ResolveString(ctx, &field.Val)
}

// vaultString stores *value at path in vault, replacing it with a vault reference.
// No-op if nil or empty. Returns an error if the hook is not configured.
func vaultString(ctx context.Context, path string, value *string) error {
	if value == nil || *value == "" {
		return nil
	}
	if VaultHooks.StoreString == nil {
		return fmt.Errorf("vault store hook is not configured")
	}
	return VaultHooks.StoreString(ctx, path, value)
}

// resolveVaultString resolves a vault reference in *value, replacing it with the secret.
// No-op if nil or empty. Returns an error if the hook is not configured.
func resolveVaultString(ctx context.Context, value *string) error {
	if value == nil || *value == "" {
		return nil
	}
	if VaultHooks.ResolveString == nil {
		return fmt.Errorf("vault resolve hook is not configured")
	}
	return VaultHooks.ResolveString(ctx, value)
}

// encryptEnvVar encrypts the Val field of an EnvVar in place using AES-256-GCM.
// It is a no-op if the field is nil, references an environment variable, or has an empty value.
func encryptEnvVar(field *schemas.EnvVar) error {
	if field == nil || field.IsFromEnv() || field.GetValue() == "" {
		return nil
	}
	encrypted, err := encrypt.Encrypt(field.Val)
	if err != nil {
		return err
	}
	field.Val = encrypted
	return nil
}

// decryptEnvVar decrypts the Val field of an EnvVar in place using AES-256-GCM.
// It is a no-op if the field is nil, references an environment variable, or has an empty value.
func decryptEnvVar(field *schemas.EnvVar) error {
	if field == nil || field.IsFromEnv() || field.GetValue() == "" {
		return nil
	}
	decrypted, err := encrypt.Decrypt(field.Val)
	if err != nil {
		return err
	}
	field.Val = decrypted
	return nil
}

// encryptEnvVarPtr encrypts the Val field of a pointer-to-EnvVar in place.
// It is a no-op if the pointer or the EnvVar it points to is nil.
func encryptEnvVarPtr(field **schemas.EnvVar) error {
	if field == nil || *field == nil {
		return nil
	}
	return encryptEnvVar(*field)
}

// decryptEnvVarPtr decrypts the Val field of a pointer-to-EnvVar in place.
// It is a no-op if the pointer or the EnvVar it points to is nil.
func decryptEnvVarPtr(field **schemas.EnvVar) error {
	if field == nil || *field == nil {
		return nil
	}
	return decryptEnvVar(*field)
}

// encryptString encrypts the string pointed to by value in place using AES-256-GCM.
// It is a no-op if the pointer is nil or the string is empty.
func encryptString(value *string) error {
	if value == nil || *value == "" {
		return nil
	}
	encrypted, err := encrypt.Encrypt(*value)
	if err != nil {
		return err
	}
	*value = encrypted
	return nil
}

// decryptString decrypts the string pointed to by value in place using AES-256-GCM.
// It is a no-op if the pointer is nil or the string is empty.
func decryptString(value *string) error {
	if value == nil || *value == "" {
		return nil
	}
	decrypted, err := encrypt.Decrypt(*value)
	if err != nil {
		return err
	}
	*value = decrypted
	return nil
}

// removeVaultEnvVar best-effort removes a vault secret for the given EnvVar field.
// Called in BeforeSave when a field is nil, env-backed, or empty so stale vault
// entries are cleaned up when a field is cleared or switched away from a literal value.
func removeVaultEnvVar(ctx context.Context, path string, field *schemas.EnvVar) {
	if VaultHooks.Remove == nil {
		return
	}
	if field != nil && !field.IsFromEnv() && field.GetValue() != "" {
		return // field has a real value; vaultEnvVar will overwrite it, no cleanup needed
	}
	_ = VaultHooks.Remove(ctx, path)
}

// removeVaultString best-effort removes a vault secret for the given string field.
// Called in BeforeSave when a field is nil or empty so stale vault entries are
// cleaned up when a field is cleared.
func removeVaultString(ctx context.Context, path string, value *string) {
	if VaultHooks.Remove == nil {
		return
	}
	if value != nil && *value != "" {
		return // field has a real value; vaultString will overwrite it, no cleanup needed
	}
	_ = VaultHooks.Remove(ctx, path)
}
