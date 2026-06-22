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
	Val        string `json:"value"`
	secretRef  string
	fromSecret bool
}

// NewSecretVar creates a new SecretVar from a string.
func NewSecretVar(value string) *SecretVar {
	// Use strconv.Unquote to properly handle JSON string escape sequences
	val := value
	if unquoted, err := strconv.Unquote(value); err == nil {
		val = unquoted
	}
	// If it's a valid JSON object following the SecretVar schema, unmarshal it
	if sonic.Valid([]byte(value)) {
		valueNode, _ := sonic.Get([]byte(val), "value")
		envNode, _ := sonic.Get([]byte(val), "env_var")
		secretRefNode, _ := sonic.Get([]byte(val), "secret_ref")
		if valueNode.Exists() && (envNode.Exists() || secretRefNode.Exists()) {
			type secretVarCompat struct {
				Val        string `json:"value"`
				SecretRef  string `json:"secret_ref"`
				FromSecret bool   `json:"from_secret"`
				// backward compat: env_var/from_env (shipped)
				EnvVar  string `json:"env_var"`
				FromEnv bool   `json:"from_env"`
			}
			var raw secretVarCompat
			if err := sonic.Unmarshal([]byte(value), &raw); err == nil {
				e := &SecretVar{Val: raw.Val}
				// New format
				if raw.SecretRef != "" || raw.FromSecret {
					e.secretRef = raw.SecretRef
					e.fromSecret = raw.FromSecret
				} else {
					// Backward compat: env (shipped — must keep working)
					if raw.FromEnv && raw.EnvVar != "" {
						ref := raw.EnvVar
						if !strings.HasPrefix(ref, "env.") {
							ref = "env." + ref
						}
						e.secretRef = ref
						e.fromSecret = true
						if envValue, ok := os.LookupEnv(strings.TrimPrefix(ref, "env.")); ok {
							e.Val = envValue
						} else {
							e.Val = ""
						}
						return e
					}
					// Legacy format: value == env_var == "env.XXX"
					if strings.HasPrefix(raw.Val, "env.") && raw.Val == raw.EnvVar {
						e.secretRef = raw.EnvVar
						e.fromSecret = true
						e.Val = ""
						if envValue, ok := os.LookupEnv(strings.TrimPrefix(raw.EnvVar, "env.")); ok {
							e.Val = envValue
						}
						return e
					}
				}
				// Resolve vault reference
				if e.fromSecret && strings.HasPrefix(e.secretRef, "vault.") {
					e.Val = ""
					if vaultValue, ok := LookupVault(e.secretRef); ok {
						e.Val = vaultValue
					}
				}
				// Resolve env reference
				if e.fromSecret && strings.HasPrefix(e.secretRef, "env.") {
					if envValue, ok := os.LookupEnv(strings.TrimPrefix(e.secretRef, "env.")); ok {
						e.Val = envValue
					} else {
						e.Val = ""
					}
				}
				return e
			}
		}
	}
	if strings.HasPrefix(val, "vault.") {
		e := &SecretVar{
			secretRef:  val,
			fromSecret: true,
		}
		if vaultValue, ok := LookupVault(val); ok {
			e.Val = vaultValue
		}
		return e
	}
	if envKey, ok := strings.CutPrefix(val, "env."); ok {
		if envValue, ok := os.LookupEnv(envKey); ok {
			return &SecretVar{
				Val:        envValue,
				secretRef:  val,
				fromSecret: true,
			}
		}
		return &SecretVar{
			Val:        "",
			secretRef:  val,
			fromSecret: true,
		}
	}
	return &SecretVar{
		Val: val,
	}
}

// Ref returns the secret reference string (e.g. "env.MY_VAR" or "vault.path/to/secret").
// Returns an empty string for plain-value SecretVars.
func (e *SecretVar) Ref() string {
	if e == nil {
		return ""
	}
	return e.secretRef
}

// IsFromSecret returns true if the value is sourced from an external secret (env var or vault).
func (e *SecretVar) IsFromSecret() bool {
	if e == nil {
		return false
	}
	return e.fromSecret
}

// IsFromVault returns true if the value is sourced from a vault path.
func (e *SecretVar) IsFromVault() bool {
	if e == nil {
		return false
	}
	return e.fromSecret && strings.HasPrefix(e.secretRef, "vault.")
}

// IsRedacted returns true if the value is redacted.
func (e *SecretVar) IsRedacted() bool {
	if e == nil {
		return false
	}
	if e.Val == "" && !e.fromSecret {
		return false
	}
	// Secret references (env/vault) are treated as redacted — the real value is external
	if e.fromSecret {
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

// Equals checks if two SecretVars are equal.
func (e *SecretVar) Equals(other *SecretVar) bool {
	if e == nil && other == nil {
		return true
	}
	if e == nil || other == nil {
		return false
	}
	return e.Val == other.Val &&
		e.secretRef == other.secretRef &&
		e.fromSecret == other.fromSecret
}

// Redacted returns a new SecretVar with the value redacted.
func (e *SecretVar) Redacted() *SecretVar {
	if e == nil {
		return nil
	}
	if e.Val == "" {
		return &SecretVar{
			Val:        "",
			secretRef:  e.secretRef,
			fromSecret: e.fromSecret,
		}
	}
	// If key is 8 characters or less, just return all asterisks
	if len(e.Val) <= 8 {
		return &SecretVar{
			Val:        strings.Repeat("*", len(e.Val)),
			secretRef:  e.secretRef,
			fromSecret: e.fromSecret,
		}
	}
	// Show first 4 and last 4 characters, replace middle with asterisks
	prefix := e.Val[:4]
	suffix := e.Val[len(e.Val)-4:]
	middle := strings.Repeat("*", 24)

	return &SecretVar{
		Val:        prefix + middle + suffix,
		secretRef:  e.secretRef,
		fromSecret: e.fromSecret,
	}
}

// FullyRedacted returns a copy of the SecretVar with Val replaced by a fixed placeholder
// so no substring of the original value is exposed. Use for API responses where
// Redacted is unsafe (e.g. literal proxy passwords). secretRef and fromSecret are
// preserved so references remain visible.
func (e *SecretVar) FullyRedacted() *SecretVar {
	if e == nil {
		return nil
	}
	if e.Val == "" {
		return &SecretVar{
			Val:        "",
			secretRef:  e.secretRef,
			fromSecret: e.fromSecret,
		}
	}
	return &SecretVar{
		Val:        "<REDACTED>",
		secretRef:  e.secretRef,
		fromSecret: e.fromSecret,
	}
}

// MarshalJSON serializes the SecretVar, emitting secret_ref and from_secret fields.
func (e SecretVar) MarshalJSON() ([]byte, error) {
	return Marshal(struct {
		Val        string `json:"value"`
		SecretRef  string `json:"secret_ref,omitempty"`
		FromSecret bool   `json:"from_secret,omitempty"`
	}{
		Val:        e.Val,
		SecretRef:  e.secretRef,
		FromSecret: e.fromSecret,
	})
}

// UnmarshalJSON unmarshals the value from JSON.
func (e *SecretVar) UnmarshalJSON(data []byte) error {
	val := string(data)
	if unquoted, err := strconv.Unquote(val); err == nil {
		val = unquoted
	}
	if sonic.Valid(data) {
		valueNode, _ := sonic.Get(data, "value")
		envNode, _ := sonic.Get(data, "env_var")
		secretRefNode, _ := sonic.Get(data, "secret_ref")
		if valueNode.Exists() && (envNode.Exists() || secretRefNode.Exists()) {
			type secretVarCompat struct {
				Val        string `json:"value"`
				SecretRef  string `json:"secret_ref"`
				FromSecret bool   `json:"from_secret"`
				// backward compat: env_var/from_env (shipped)
				EnvVar  string `json:"env_var"`
				FromEnv bool   `json:"from_env"`
			}
			var raw secretVarCompat
			if err := sonic.Unmarshal(data, &raw); err == nil {
				e.Val = raw.Val

				// New format
				if raw.SecretRef != "" || raw.FromSecret {
					e.secretRef = raw.SecretRef
					e.fromSecret = raw.FromSecret
				} else if raw.FromEnv && raw.EnvVar != "" {
					// Backward compat: env
					ref := raw.EnvVar
					if !strings.HasPrefix(ref, "env.") {
						ref = "env." + ref
					}
					e.secretRef = ref
					e.fromSecret = true
					if envValue, ok := os.LookupEnv(strings.TrimPrefix(ref, "env.")); ok {
						e.Val = envValue
					} else {
						e.Val = ""
					}
					return nil
				} else if strings.HasPrefix(raw.Val, "env.") && raw.Val == raw.EnvVar {
					// Old format: value == env_var == "env.XXX"
					e.secretRef = raw.EnvVar
					e.fromSecret = true
					e.Val = ""
					if envValue, ok := os.LookupEnv(strings.TrimPrefix(raw.EnvVar, "env.")); ok {
						e.Val = envValue
					}
					return nil
				}

				// Resolve references
				if e.fromSecret && strings.HasPrefix(e.secretRef, "vault.") {
					e.Val = ""
					if vaultValue, ok := LookupVault(e.secretRef); ok {
						e.Val = vaultValue
					}
				}
				if e.fromSecret && strings.HasPrefix(e.secretRef, "env.") {
					if envValue, ok := os.LookupEnv(strings.TrimPrefix(e.secretRef, "env.")); ok {
						e.Val = envValue
					} else {
						e.Val = ""
					}
				}
				return nil
			}
		}
	}
	// Plain string forms
	if strings.HasPrefix(val, "vault.") {
		e.secretRef = val
		e.fromSecret = true
		e.Val = ""
		if vaultValue, ok := LookupVault(val); ok {
			e.Val = vaultValue
		}
		return nil
	}
	if envKey, ok := strings.CutPrefix(val, "env."); ok {
		e.secretRef = val
		e.fromSecret = true
		if envValue, ok := os.LookupEnv(envKey); ok {
			e.Val = envValue
		} else {
			e.Val = ""
		}
		return nil
	}
	e.Val = val
	e.secretRef = ""
	e.fromSecret = false
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
		e.secretRef = ""
		e.fromSecret = false
		return nil
	}
	switch v := value.(type) {
	case []byte:
		return e.Scan(string(v))
	case string:
		val := strings.Trim(v, "\"")
		if strings.HasPrefix(val, "vault.") {
			e.Val = ""
			e.secretRef = val
			e.fromSecret = true
			if vaultValue, ok := LookupVault(val); ok {
				e.Val = vaultValue
			}
			return nil
		}
		if envKey, ok := strings.CutPrefix(val, "env."); ok {
			e.secretRef = val
			e.fromSecret = true
			if envValue, ok := os.LookupEnv(envKey); ok {
				e.Val = envValue
			} else {
				e.Val = ""
			}
			return nil
		}
		e.Val = strings.Trim(v, "\"")
		e.secretRef = ""
		e.fromSecret = false
		return nil
	}
	return fmt.Errorf("failed to scan value: %v", value)
}

// Value implements driver.Valuer for database storage.
// It stores the secret reference (e.g., "env.API_KEY" or "vault.path/to/secret") if
// fromSecret is true, otherwise the raw value.
func (e SecretVar) Value() (driver.Value, error) {
	if e.fromSecret {
		return e.secretRef, nil
	}
	return e.Val, nil
}

// ShouldPreserveStored returns true when the SecretVar is a client-side placeholder
// that should not overwrite the stored credential. Returns true for a nil receiver,
// an empty non-secret value, or a redacted non-secret value. Returns false for secret
// references (always intentional) and plain non-empty values.
func (e *SecretVar) ShouldPreserveStored() bool {
	if e == nil {
		return true
	}
	if e.fromSecret {
		return false
	}
	return e.GetValue() == "" || e.IsRedacted()
}

// IsSet returns true if the SecretVar has a resolved value or a secret reference.
// Use instead of GetValue() != "" when checking whether a field was configured,
// because references may have an empty Val before resolution.
func (e *SecretVar) IsSet() bool {
	if e == nil {
		return false
	}
	if e.fromSecret {
		return e.secretRef != ""
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
