package schemas

import (
	"database/sql/driver"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
)

// SecretVar is a wrapper around a value that can be sourced from an environment variable
// or an external vault (e.g. AWS Secrets Manager, GCP Secret Manager, HashiCorp Vault).
// Three reference forms are accepted: plain text, "env.VAR_NAME", and "vault.path/to/secret".
type SecretVar struct {
	Val       string `json:"value"`
	EnvVar    string `json:"env_var"`
	FromEnv   bool   `json:"from_env"`
	VaultRef  string `json:"vault_var,omitempty"`
	FromVault bool   `json:"from_vault,omitempty"`
}

// NewSecretVar creates a new EnvValue from a string.
func NewSecretVar(value string) *SecretVar {
	// Cleanup string if required
	// Use strconv.Unquote to properly handle JSON string escape sequences
	// This converts "\"{\\\"key\\\":\\\"value\\\"}\"" to "{\"key\":\"value\"}"
	val := value
	if unquoted, err := strconv.Unquote(value); err == nil {
		val = unquoted
	}
	// Here we will need to check if the incoming data is a valid JSON object
	// If it's a valid JSON object and follows the SecretVar schema, then we will unmarshal it into an SecretVar object
	if sonic.Valid([]byte(value)) {
		valueNode, _ := sonic.Get([]byte(val), "value")
		envNode, _ := sonic.Get([]byte(val), "env_var")
		if valueNode.Exists() && envNode.Exists() {
			// Use a type alias to avoid infinite recursion (alias doesn't inherit methods)
			type secretVarAlias SecretVar
			var secretVar secretVarAlias
			if err := sonic.Unmarshal([]byte(value), &secretVar); err == nil {
				e := &SecretVar{
					Val:       secretVar.Val,
					FromEnv:   secretVar.FromEnv,
					EnvVar: secretVar.EnvVar,
					FromVault: secretVar.FromVault,
					VaultRef:  secretVar.VaultRef,
				}
				// Explicit vault reference: {from_vault: true, vault_var: "vault.path"}
				if e.FromVault && e.VaultRef != "" {
					if !strings.HasPrefix(e.VaultRef, "vault.") {
						e.VaultRef = "vault." + e.VaultRef
					}
					e.Val = e.VaultRef
					if vaultValue, ok := LookupVault(e.VaultRef); ok {
						e.Val = vaultValue
					}
					return e
				}
				// Old format: value == env_var == "env.XXX"
				if strings.HasPrefix(e.Val, "env.") && e.Val == e.EnvVar {
					e.Val = ""
					// Load the environment variable value
					envValue, ok := os.LookupEnv(strings.TrimPrefix(e.EnvVar, "env."))
					if ok {
						e.Val = envValue
					}
					e.FromEnv = true
				}
				// New format: value is empty, from_env=true, env_var holds the reference
				if e.Val == "" && e.FromEnv && strings.HasPrefix(e.EnvVar, "env.") {
					e.FromEnv = true
					if envValue, ok := os.LookupEnv(strings.TrimPrefix(e.EnvVar, "env.")); ok {
						e.Val = envValue
					}
				}
				return e
			}
		}
	}
	if strings.HasPrefix(val, "vault.") {
		e := &SecretVar{
			Val:       val,
			VaultRef:  val,
			FromVault: true,
		}
		if vaultValue, ok := LookupVault(e.VaultRef); ok {
			e.Val = vaultValue
		}
		return e
	}
	if envKey, ok := strings.CutPrefix(val, "env."); ok {
		if envValue, ok := os.LookupEnv(envKey); ok {
			return &SecretVar{
				Val:       envValue,
				FromEnv:   true,
				EnvVar: val,
			}
		}
		return &SecretVar{
			Val:       "",
			FromEnv:   true,
			EnvVar: val,
		}
	}
	return &SecretVar{
		Val:       val,
		FromEnv:   false,
		EnvVar: "",
	}
}

// IsFromVault returns true if the value is sourced from an external vault.
func (e *SecretVar) IsFromVault() bool {
	if e == nil {
		return false
	}
	return e.FromVault
}

// IsRedacted returns true if the value is redacted.
func (e *SecretVar) IsRedacted() bool {
	if e == nil {
		return false
	}
	if e.Val == "" && !e.FromEnv && !e.FromVault {
		return false
	}
	// Vault and env references are treated as redacted (the real value is external)
	if e.FromEnv || e.FromVault {
		return true
	}
	if len(e.Val) <= 8 {
		return strings.Count(e.Val, "*") == len(e.Val)
	}
	// Check for exact redaction pattern: 4 chars + 24 asterisks + 4 chars
	if len(e.Val) == 32 {
		middle := e.Val[4:28]
		if middle == strings.Repeat("*", 24) {
			return true
		}
	}
	// Check for <redacted> sentinel (case-insensitive for compatibility)
	if strings.EqualFold(e.Val, "<redacted>") {
		return true
	}
	// Check for [REDACTED] sentinel produced by MarshalJSON in scim config serialization
	if strings.EqualFold(e.Val, "[REDACTED]") {
		return true
	}
	return false
}

// Equals checks if two SecretKeys are equal.
func (e *SecretVar) Equals(other *SecretVar) bool {
	if e == nil && other == nil {
		return true
	}
	if e == nil || other == nil {
		return false
	}
	return e.Val == other.Val &&
		e.EnvVar == other.EnvVar &&
		e.FromEnv == other.FromEnv &&
		e.VaultRef == other.VaultRef &&
		e.FromVault == other.FromVault
}

// Redacted returns a new SecretKey with the value redacted.
func (e *SecretVar) Redacted() *SecretVar {
	if e == nil {
		return nil
	}
	if e.Val == "" {
		return &SecretVar{
			Val:       "",
			FromEnv:   e.FromEnv,
			EnvVar: e.EnvVar,
			FromVault: e.FromVault,
			VaultRef:  e.VaultRef,
		}
	}
	// If key is 8 characters or less, just return all asterisks
	if len(e.Val) <= 8 {
		return &SecretVar{
			Val:       strings.Repeat("*", len(e.Val)),
			FromEnv:   e.FromEnv,
			EnvVar: e.EnvVar,
			FromVault: e.FromVault,
			VaultRef:  e.VaultRef,
		}
	}
	// Show first 4 and last 4 characters, replace middle with asterisks
	prefix := e.Val[:4]
	suffix := e.Val[len(e.Val)-4:]
	middle := strings.Repeat("*", 24)

	return &SecretVar{
		Val:       prefix + middle + suffix,
		FromEnv:   e.FromEnv,
		EnvVar: e.EnvVar,
		FromVault: e.FromVault,
		VaultRef:  e.VaultRef,
	}
}

// FullyRedacted returns a copy of the SecretVar with Val replaced by a fixed placeholder
// so no substring of the original value is exposed. Use for API responses where
// Redacted is unsafe (e.g. literal proxy passwords). FromEnv/SecretVar and
// FromVault/VaultRef are preserved so references remain visible.
func (e *SecretVar) FullyRedacted() *SecretVar {
	if e == nil {
		return nil
	}
	if e.Val == "" {
		return &SecretVar{
			Val:       "",
			FromEnv:   e.FromEnv,
			EnvVar: e.EnvVar,
			FromVault: e.FromVault,
			VaultRef:  e.VaultRef,
		}
	}
	return &SecretVar{
		Val:       "<REDACTED>",
		FromEnv:   e.FromEnv,
		EnvVar: e.EnvVar,
		FromVault: e.FromVault,
		VaultRef:  e.VaultRef,
	}
}

// UnmarshalJSON unmarshals the value from JSON.
func (e *SecretVar) UnmarshalJSON(data []byte) error {
	val := string(data)
	// Cleanup string if required
	// Use strconv.Unquote to properly handle JSON string escape sequences
	// This converts "\"{\\\"key\\\":\\\"value\\\"}\"" to "{\"key\":\"value\"}"
	if unquoted, err := strconv.Unquote(val); err == nil {
		val = unquoted
	}
	// Check if the incoming data is a valid JSON object matching the SecretVar schema.
	if sonic.Valid(data) {
		valueNode, _ := sonic.Get(data, "value")
		envNode, _ := sonic.Get(data, "env_var")
		if valueNode.Exists() && envNode.Exists() {
			// Use a type alias to avoid infinite recursion (alias doesn't inherit methods)
			type secretVarAlias SecretVar
			var secretVar secretVarAlias
			if err := sonic.Unmarshal(data, &secretVar); err == nil {
				e.Val = secretVar.Val
				e.FromEnv = secretVar.FromEnv
				e.EnvVar = secretVar.EnvVar
				e.FromVault = secretVar.FromVault
				e.VaultRef = secretVar.VaultRef

				// Explicit vault reference: {from_vault: true, vault_var: "vault.path"}
				if e.FromVault && e.VaultRef != "" {
					if !strings.HasPrefix(e.VaultRef, "vault.") {
						e.VaultRef = "vault." + e.VaultRef
					}
					e.Val = e.VaultRef
					if vaultValue, ok := LookupVault(e.VaultRef); ok {
						e.Val = vaultValue
					}
					return nil
				}
				// Old format: value == env_var == "env.XXX"
				if strings.HasPrefix(e.Val, "env.") && e.Val == e.EnvVar {
					e.Val = ""
					envValue, ok := os.LookupEnv(strings.TrimPrefix(e.EnvVar, "env."))
					if ok {
						e.Val = envValue
					}
					e.FromEnv = true
				}
				// New format: value is empty, from_env=true, env_var holds the reference
				if e.Val == "" && e.FromEnv && strings.HasPrefix(e.EnvVar, "env.") {
					if envValue, ok := os.LookupEnv(strings.TrimPrefix(e.EnvVar, "env.")); ok {
						e.Val = envValue
					}
				}
				return nil
			}
			// Else the value is JSON, so we will treat this as a normal value
		}
	}
	// Plain string forms: "vault.path/to/secret", "env.VAR", or literal value
	if strings.HasPrefix(val, "vault.") {
		e.VaultRef = val
		e.FromVault = true
		e.Val = val
		e.FromEnv = false
		e.EnvVar = ""
		if vaultValue, ok := LookupVault(val); ok {
			e.Val = vaultValue
		}
		return nil
	}
	if envKey, ok := strings.CutPrefix(val, "env."); ok {
		if envValue, ok := os.LookupEnv(envKey); ok {
			e.Val = envValue
			e.FromEnv = true
			e.EnvVar = val
			return nil
		}
		e.Val = ""
		e.FromEnv = true
		e.EnvVar = val
		e.FromVault = false
		e.VaultRef = ""
		return nil
	}
	e.Val = val
	e.FromEnv = false
	e.EnvVar = ""
	e.FromVault = false
	e.VaultRef = ""
	return nil
}

// String returns the value as a string.
func (e *SecretVar) String() string {
	if e == nil {
		return ""
	}
	return e.Val
}

// Scan scans the value from the database.
func (e *SecretVar) Scan(value any) error {
	if value == nil {
		e.Val = ""
		e.FromEnv = false
		e.EnvVar = ""
		e.FromVault = false
		e.VaultRef = ""
		return nil
	}
	switch v := value.(type) {
	case []byte:
		return e.Scan(string(v))
	case string:
		// Cleanup string if required
		// The string may have "\"env.TEST\"", "env.TEST" or "env.TEST\"", we need to clean it up to "env.TEST"
		val := strings.Trim(v, "\"")
		// Vault reference: keep the reference in Val so the AfterFind GORM hook
		// (resolveVaultSecretVar) can resolve it via ResolveString(&e.Val). VaultRef
		// preserves the original path so it survives resolution and can be surfaced
		// in API responses and re-stored correctly on writes.
		if strings.HasPrefix(val, "vault.") {
			e.Val = val
			e.VaultRef = val
			e.FromVault = true
			e.FromEnv = false
			e.EnvVar = ""
			if vaultValue, ok := LookupVault(val); ok {
				e.Val = vaultValue
			}
			return nil
		}
		if envKey, ok := strings.CutPrefix(val, "env."); ok {
			if envValue, ok := os.LookupEnv(envKey); ok {
				e.Val = envValue
				e.FromEnv = true
				e.EnvVar = val
				e.FromVault = false
				e.VaultRef = ""
				return nil
			}
			e.Val = ""
			e.FromEnv = true
			e.EnvVar = val
			e.FromVault = false
			e.VaultRef = ""
			return nil
		}
		e.Val = val
		e.FromEnv = false
		e.EnvVar = ""
		e.FromVault = false
		e.VaultRef = ""
		return nil
	}
	return fmt.Errorf("failed to scan value: %v", value)
}

// Value implements driver.Valuer for database storage.
// It stores the vault reference (e.g., "vault.path/to/secret") if FromVault is true,
// the env reference (e.g., "env.API_KEY") if FromEnv is true, otherwise the raw value.
func (e SecretVar) Value() (driver.Value, error) {
	if e.FromVault {
		return e.VaultRef, nil
	}
	if e.FromEnv {
		return e.EnvVar, nil
	}
	return e.Val, nil
}

// IsFromEnv returns true if the value is sourced from an environment variable.
func (e *SecretVar) IsFromEnv() bool {
	if e == nil {
		return false
	}
	return e.FromEnv
}

// ShouldPreserveStored returns true when the SecretVar is a client-side placeholder
// that should not overwrite the stored credential. Returns true for a nil receiver,
// an empty non-env/non-vault value, or a redacted non-env/non-vault value. Returns
// false for env/vault references (always intentional) and plain non-empty values.
func (e *SecretVar) ShouldPreserveStored() bool {
	if e == nil {
		return true
	}
	if e.IsFromEnv() || e.IsFromVault() {
		return false
	}
	return e.GetValue() == "" || e.IsRedacted()
}

// IsSet returns true if the SecretVar has a resolved value or an environment variable
// or vault reference. This should be used instead of GetValue() != "" when checking
// whether a field was configured, because references may have an empty Val before
// resolution (e.g., when the env var is not set in the current environment).
func (e *SecretVar) IsSet() bool {
	if e == nil {
		return false
	}
	if e.IsFromVault() {
		return e.VaultRef != ""
	}
	if e.IsFromEnv() {
		return e.EnvVar != ""
	}
	return e.Val != ""
}

// GetValue returns the resolved value.
func (e *SecretVar) GetValue() string {
	if e == nil {
		return ""
	}
	return e.Val
}

// GetValuePtr returns a pointer to the value.
func (e *SecretVar) GetValuePtr() *string {
	if e == nil {
		return nil
	}
	return &e.Val
}

// CoerceInt coerces value to int
func (e *SecretVar) CoerceInt(defaultValue int) int {
	if e == nil {
		return defaultValue
	}
	val, err := strconv.Atoi(e.GetValue())
	if err != nil {
		return defaultValue
	}
	return val
}

// CoerceBool coerces value to bool
func (e *SecretVar) CoerceBool(defaultValue bool) bool {
	if e == nil {
		return defaultValue
	}
	val, err := strconv.ParseBool(e.GetValue())
	if err != nil {
		return defaultValue
	}
	return val
}
