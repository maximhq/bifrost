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
	SecretRef  string `json:"secret_ref,omitempty"`
	FromSecret bool   `json:"from_secret,omitempty"`
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
					e.SecretRef = raw.SecretRef
					e.FromSecret = raw.FromSecret
				} else {
					// Backward compat: env (shipped — must keep working)
					if raw.FromEnv && raw.EnvVar != "" {
						ref := raw.EnvVar
						if !strings.HasPrefix(ref, "env.") {
							ref = "env." + ref
						}
						e.SecretRef = ref
						e.FromSecret = true
						if envValue, ok := os.LookupEnv(strings.TrimPrefix(ref, "env.")); ok {
							e.Val = envValue
						} else {
							e.Val = ""
						}
						return e
					}
					// Legacy format: value == env_var == "env.XXX"
					if strings.HasPrefix(raw.Val, "env.") && raw.Val == raw.EnvVar {
						e.SecretRef = raw.EnvVar
						e.FromSecret = true
						e.Val = ""
						if envValue, ok := os.LookupEnv(strings.TrimPrefix(raw.EnvVar, "env.")); ok {
							e.Val = envValue
						}
						return e
					}
				}
				// Resolve vault reference
				if e.FromSecret && strings.HasPrefix(e.SecretRef, "vault.") {
					e.Val = e.SecretRef
					if vaultValue, ok := LookupVault(e.SecretRef); ok {
						e.Val = vaultValue
					}
				}
				// Resolve env reference
				if e.FromSecret && strings.HasPrefix(e.SecretRef, "env.") {
					if envValue, ok := os.LookupEnv(strings.TrimPrefix(e.SecretRef, "env.")); ok {
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
			Val:        val,
			SecretRef:  val,
			FromSecret: true,
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
				SecretRef:  val,
				FromSecret: true,
			}
		}
		return &SecretVar{
			Val:        "",
			SecretRef:  val,
			FromSecret: true,
		}
	}
	return &SecretVar{
		Val: val,
	}
}

// IsFromSecret returns true if the value is sourced from an external secret (env var or vault).
func (e *SecretVar) IsFromSecret() bool {
	if e == nil {
		return false
	}
	return e.FromSecret
}

// IsRedacted returns true if the value is redacted.
func (e *SecretVar) IsRedacted() bool {
	if e == nil {
		return false
	}
	if e.Val == "" && !e.FromSecret {
		return false
	}
	// Secret references (env/vault) are treated as redacted — the real value is external
	if e.FromSecret {
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
		e.SecretRef == other.SecretRef &&
		e.FromSecret == other.FromSecret
}

// Redacted returns a new SecretVar with the value redacted.
func (e *SecretVar) Redacted() *SecretVar {
	if e == nil {
		return nil
	}
	if e.Val == "" {
		return &SecretVar{
			Val:        "",
			SecretRef:  e.SecretRef,
			FromSecret: e.FromSecret,
		}
	}
	// If key is 8 characters or less, just return all asterisks
	if len(e.Val) <= 8 {
		return &SecretVar{
			Val:        strings.Repeat("*", len(e.Val)),
			SecretRef:  e.SecretRef,
			FromSecret: e.FromSecret,
		}
	}
	// Show first 4 and last 4 characters, replace middle with asterisks
	prefix := e.Val[:4]
	suffix := e.Val[len(e.Val)-4:]
	middle := strings.Repeat("*", 24)

	return &SecretVar{
		Val:        prefix + middle + suffix,
		SecretRef:  e.SecretRef,
		FromSecret: e.FromSecret,
	}
}

// FullyRedacted returns a copy of the SecretVar with Val replaced by a fixed placeholder
// so no substring of the original value is exposed. Use for API responses where
// Redacted is unsafe (e.g. literal proxy passwords). SecretRef and FromSecret are
// preserved so references remain visible.
func (e *SecretVar) FullyRedacted() *SecretVar {
	if e == nil {
		return nil
	}
	if e.Val == "" {
		return &SecretVar{
			Val:        "",
			SecretRef:  e.SecretRef,
			FromSecret: e.FromSecret,
		}
	}
	return &SecretVar{
		Val:        "<REDACTED>",
		SecretRef:  e.SecretRef,
		FromSecret: e.FromSecret,
	}
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
					e.SecretRef = raw.SecretRef
					e.FromSecret = raw.FromSecret
				} else if raw.FromEnv && raw.EnvVar != "" {
					// Backward compat: env
					ref := raw.EnvVar
					if !strings.HasPrefix(ref, "env.") {
						ref = "env." + ref
					}
					e.SecretRef = ref
					e.FromSecret = true
					if envValue, ok := os.LookupEnv(strings.TrimPrefix(ref, "env.")); ok {
						e.Val = envValue
					} else {
						e.Val = ""
					}
					return nil
				} else if strings.HasPrefix(raw.Val, "env.") && raw.Val == raw.EnvVar {
					// Old format: value == env_var == "env.XXX"
					e.SecretRef = raw.EnvVar
					e.FromSecret = true
					e.Val = ""
					if envValue, ok := os.LookupEnv(strings.TrimPrefix(raw.EnvVar, "env.")); ok {
						e.Val = envValue
					}
					return nil
				}

				// Resolve references
				if e.FromSecret && strings.HasPrefix(e.SecretRef, "vault.") {
					e.Val = e.SecretRef
					if vaultValue, ok := LookupVault(e.SecretRef); ok {
						e.Val = vaultValue
					}
				}
				if e.FromSecret && strings.HasPrefix(e.SecretRef, "env.") {
					if envValue, ok := os.LookupEnv(strings.TrimPrefix(e.SecretRef, "env.")); ok {
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
		e.SecretRef = val
		e.FromSecret = true
		e.Val = val
		if vaultValue, ok := LookupVault(val); ok {
			e.Val = vaultValue
		}
		return nil
	}
	if envKey, ok := strings.CutPrefix(val, "env."); ok {
		e.SecretRef = val
		e.FromSecret = true
		if envValue, ok := os.LookupEnv(envKey); ok {
			e.Val = envValue
		} else {
			e.Val = ""
		}
		return nil
	}
	e.Val = val
	e.SecretRef = ""
	e.FromSecret = false
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
		e.SecretRef = ""
		e.FromSecret = false
		return nil
	}
	switch v := value.(type) {
	case []byte:
		return e.Scan(string(v))
	case string:
		val := strings.Trim(v, "\"")
		if strings.HasPrefix(val, "vault.") {
			e.Val = val
			e.SecretRef = val
			e.FromSecret = true
			if vaultValue, ok := LookupVault(val); ok {
				e.Val = vaultValue
			}
			return nil
		}
		if envKey, ok := strings.CutPrefix(val, "env."); ok {
			e.SecretRef = val
			e.FromSecret = true
			if envValue, ok := os.LookupEnv(envKey); ok {
				e.Val = envValue
			} else {
				e.Val = ""
			}
			return nil
		}
		e.Val = val
		e.SecretRef = ""
		e.FromSecret = false
		return nil
	}
	return fmt.Errorf("failed to scan value: %v", value)
}

// Value implements driver.Valuer for database storage.
// It stores the secret reference (e.g., "env.API_KEY" or "vault.path/to/secret") if
// FromSecret is true, otherwise the raw value.
func (e SecretVar) Value() (driver.Value, error) {
	if e.FromSecret {
		return e.SecretRef, nil
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
	if e.FromSecret {
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
	if e.FromSecret {
		return e.SecretRef != ""
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
