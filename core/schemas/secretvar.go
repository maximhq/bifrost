package schemas

import (
	"database/sql/driver"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
)

// SecretType identifies the source of a SecretVar's value.
type SecretType string

const (
	SecretTypePlainText SecretType = "plain_text"
	SecretTypeEnv       SecretType = "env"
	SecretTypeVault     SecretType = "vault"
)

// SecretVar is a wrapper around a value that can be sourced from an environment variable
// or an external vault (e.g. AWS Secrets Manager, GCP Secret Manager, HashiCorp Vault).
// Three reference forms are accepted: plain text, "env.VAR_NAME", and "vault.path/to/secret".
type SecretVar struct {
	Val        string `json:"value"`
	ref        string
	SecretType SecretType `json:"type,omitempty"`
}

// inferSecretType returns the SecretType implied by a reference string prefix.
func inferSecretType(ref string) SecretType {
	if strings.HasPrefix(ref, "vault.") {
		return SecretTypeVault
	}
	if strings.HasPrefix(ref, "env.") {
		return SecretTypeEnv
	}
	return SecretTypePlainText
}

// parseSecretRef classifies value and returns a *SecretVar with SecretType and ref
// populated but Val unresolved — no vault HTTP calls, no env lookups.
// For JSON-encoded SecretVars the raw "value" field is preserved in Val unchanged.
// Adding a new secret type only requires updating this function.
func parseSecretRef(value string) *SecretVar {
	val := value
	if unquoted, err := strconv.Unquote(value); err == nil {
		val = unquoted
	}
	if sonic.Valid([]byte(value)) {
		valueNode, _ := sonic.Get([]byte(val), "value")
		refNode, _ := sonic.Get([]byte(val), "ref")
		typeNode, _ := sonic.Get([]byte(val), "type")
		envVarNode, _ := sonic.Get([]byte(val), "env_var")
		fromEnvNode, _ := sonic.Get([]byte(val), "from_env")
		isSecretVarJSON := valueNode.Exists() ||
			(refNode.Exists() && typeNode.Exists()) ||
			(envVarNode.Exists() && fromEnvNode.Exists())
		if isSecretVarJSON {
			type secretVarCompat struct {
				Val        string     `json:"value"`
				Ref        string     `json:"ref"`
				SecretType SecretType `json:"type"`
				// shipped backward compat: env_var/from_env
				EnvVar  string `json:"env_var"`
				FromEnv bool   `json:"from_env"`
			}
			var raw secretVarCompat
			if err := sonic.Unmarshal([]byte(value), &raw); err == nil {
				e := &SecretVar{Val: raw.Val}
				if raw.SecretType != "" {
					// New format: explicit type field
					e.ref = raw.Ref
					e.SecretType = raw.SecretType
				} else if raw.Ref != "" {
					// Has ref but no type — infer from prefix
					e.ref = raw.Ref
					e.SecretType = inferSecretType(raw.Ref)
				} else if raw.FromEnv && raw.EnvVar != "" {
					// Backward compat: from_env/env_var
					ref := raw.EnvVar
					if !strings.HasPrefix(ref, "env.") {
						ref = "env." + ref
					}
					e.ref = ref
					e.SecretType = SecretTypeEnv
				} else if strings.HasPrefix(raw.Val, "env.") && raw.Val == raw.EnvVar {
					// Legacy format: value == env_var == "env.XXX"
					e.ref = raw.EnvVar
					e.SecretType = SecretTypeEnv
				} else {
					// Plain text JSON object ({value, ...} with no type/ref/from_env).
					e.SecretType = SecretTypePlainText
				}
				return e
			}
		}
	}
	if strings.HasPrefix(val, "vault.") {
		return &SecretVar{ref: val, SecretType: SecretTypeVault}
	}
	if strings.HasPrefix(val, "env.") {
		return &SecretVar{ref: val, SecretType: SecretTypeEnv}
	}
	return &SecretVar{Val: val, SecretType: SecretTypePlainText}
}

// IsSecretRef reports whether value is a secret reference (env.* or vault.* prefix,
// or a JSON-encoded SecretVar with an env/vault type) without resolving it.
// Use this instead of NewSecretVar(...).IsFromSecret() when resolution side-effects
// (vault HTTP calls, env lookups) must be avoided.
func IsSecretRef(value string) bool {
	return parseSecretRef(value).IsFromSecret()
}

// NewSecretVar creates a new SecretVar from a string.
func NewSecretVar(value string) *SecretVar {
	e := parseSecretRef(value)
	switch e.SecretType {
	case SecretTypeVault:
		e.Val = ""
		if vaultValue, ok := LookupVault(e.ref); ok {
			e.Val = vaultValue
		}
	case SecretTypeEnv:
		if envValue, ok := os.LookupEnv(e.EnvKey()); ok {
			e.Val = envValue
		} else {
			e.Val = ""
		}
	}
	return e
}

// GetRawRef returns the full secret reference string including prefix
// (e.g. "env.MY_VAR" or "vault.path/to/secret").
// Returns an empty string for plain-value SecretVars.
func (e *SecretVar) GetRawRef() string {
	if e == nil {
		return ""
	}
	return e.ref
}

// GetRef returns the secret reference without its type prefix.
// For env secrets it strips "env." (returning "MY_VAR"), for vault it strips "vault."
// (returning "path/to/secret"), and for plain values it returns the ref as-is.
func (e *SecretVar) GetRef() string {
	if e == nil {
		return ""
	}
	switch e.SecretType {
	case SecretTypeEnv:
		return strings.TrimPrefix(e.ref, "env.")
	case SecretTypeVault:
		return strings.TrimPrefix(e.ref, "vault.")
	}
	return e.ref
}

// Type returns the SecretType of this SecretVar.
func (e *SecretVar) Type() SecretType {
	if e == nil {
		return SecretTypePlainText
	}
	if e.SecretType == "" {
		return SecretTypePlainText
	}
	return e.SecretType
}

// IsFromSecret returns true if the value is sourced from an external secret (env var or vault).
func (e *SecretVar) IsFromSecret() bool {
	if e == nil {
		return false
	}
	return e.SecretType == SecretTypeEnv || e.SecretType == SecretTypeVault
}

// IsFromVault returns true if the value is sourced from a vault path.
func (e *SecretVar) IsFromVault() bool {
	if e == nil {
		return false
	}
	return e.SecretType == SecretTypeVault
}

// VaultPath returns the vault path without the "vault." prefix.
// Returns an empty string if the SecretVar is not vault-backed.
func (e *SecretVar) VaultPath() string {
	if e == nil || e.SecretType != SecretTypeVault {
		return ""
	}
	return strings.TrimPrefix(e.ref, "vault.")
}

// EnvKey returns the environment variable name without the "env." prefix.
// Returns an empty string if the SecretVar is not env-backed.
func (e *SecretVar) EnvKey() string {
	if e == nil || e.SecretType != SecretTypeEnv {
		return ""
	}
	return strings.TrimPrefix(e.ref, "env.")
}

// IsFromEnv returns true if the value is sourced from an environment variable.
func (e *SecretVar) IsFromEnv() bool {
	if e == nil {
		return false
	}
	return e.SecretType == SecretTypeEnv
}

// IsRedacted returns true if the value is redacted.
func (e *SecretVar) IsRedacted() bool {
	if e == nil {
		return false
	}
	if e.Val == "" && !e.IsFromSecret() {
		return false
	}
	// Secret references (env/vault) are treated as redacted — the real value is external
	if e.IsFromSecret() {
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
		e.ref == other.ref &&
		e.SecretType == other.SecretType
}

// Redacted returns a new SecretVar with the value redacted.
func (e *SecretVar) Redacted() *SecretVar {
	if e == nil {
		return nil
	}
	if e.Val == "" {
		return &SecretVar{Val: "", ref: e.ref, SecretType: e.SecretType}
	}
	if len(e.Val) <= 8 {
		return &SecretVar{Val: strings.Repeat("*", len(e.Val)), ref: e.ref, SecretType: e.SecretType}
	}
	prefix := e.Val[:4]
	suffix := e.Val[len(e.Val)-4:]
	middle := strings.Repeat("*", 24)
	return &SecretVar{Val: prefix + middle + suffix, ref: e.ref, SecretType: e.SecretType}
}

// FullyRedacted returns a copy of the SecretVar with Val replaced by a fixed placeholder
// so no substring of the original value is exposed. Use for API responses where
// Redacted is unsafe (e.g. literal proxy passwords). secretRef and secretType are
// preserved so references remain visible.
func (e *SecretVar) FullyRedacted() *SecretVar {
	if e == nil {
		return nil
	}
	if e.Val == "" {
		return &SecretVar{Val: "", ref: e.ref, SecretType: e.SecretType}
	}
	return &SecretVar{Val: "<REDACTED>", ref: e.ref, SecretType: e.SecretType}
}

// MarshalJSON serializes the SecretVar, emitting ref and type fields.
func (e SecretVar) MarshalJSON() ([]byte, error) {
	return Marshal(struct {
		Val        string     `json:"value"`
		Ref        string     `json:"ref,omitempty"`
		SecretType SecretType `json:"type,omitempty"`
	}{Val: e.Val, Ref: e.ref, SecretType: e.SecretType})
}

// UnmarshalJSON unmarshals the value from JSON.
func (e *SecretVar) UnmarshalJSON(data []byte) error {
	val := string(data)
	if unquoted, err := strconv.Unquote(val); err == nil {
		val = unquoted
	}
	if sonic.Valid(data) {
		valueNode, _ := sonic.Get(data, "value")
		refNode, _ := sonic.Get(data, "ref")
		typeNode, _ := sonic.Get(data, "type")
		envVarNode, _ := sonic.Get(data, "env_var")
		fromEnvNode, _ := sonic.Get(data, "from_env")
		isSecretVarJSON := valueNode.Exists() ||
			(refNode.Exists() && typeNode.Exists()) ||
			(envVarNode.Exists() && fromEnvNode.Exists())
		if isSecretVarJSON {
			type secretVarCompat struct {
				Val        string     `json:"value"`
				Ref        string     `json:"ref"`
				SecretType SecretType `json:"type"`
				// shipped backward compat: env_var/from_env
				EnvVar  string `json:"env_var"`
				FromEnv bool   `json:"from_env"`
			}
			var raw secretVarCompat
			if err := sonic.Unmarshal(data, &raw); err == nil {
				e.Val = raw.Val
				if raw.SecretType != "" {
					// New format: explicit type field
					e.ref = raw.Ref
					e.SecretType = raw.SecretType
				} else if raw.Ref != "" {
					// Has ref but no type — infer from prefix
					e.ref = raw.Ref
					e.SecretType = inferSecretType(raw.Ref)
				} else if raw.FromEnv && raw.EnvVar != "" {
					// Backward compat: from_env/env_var
					ref := raw.EnvVar
					if !strings.HasPrefix(ref, "env.") {
						ref = "env." + ref
					}
					e.ref = ref
					e.SecretType = SecretTypeEnv
					if envValue, ok := os.LookupEnv(strings.TrimPrefix(ref, "env.")); ok {
						e.Val = envValue
					} else {
						e.Val = ""
					}
					return nil
				} else if strings.HasPrefix(raw.Val, "env.") && raw.Val == raw.EnvVar {
					// Legacy format: value == env_var == "env.XXX"
					e.ref = raw.EnvVar
					e.SecretType = SecretTypeEnv
					e.Val = ""
					if envValue, ok := os.LookupEnv(strings.TrimPrefix(raw.EnvVar, "env.")); ok {
						e.Val = envValue
					}
					return nil
				}
				// Resolve references
				if e.SecretType == SecretTypeVault {
					e.Val = ""
					if vaultValue, ok := LookupVault(e.ref); ok {
						e.Val = vaultValue
					}
				}
				if e.SecretType == SecretTypeEnv {
					if envValue, ok := os.LookupEnv(e.EnvKey()); ok {
						e.Val = envValue
					} else {
						e.Val = ""
					}
				}
				if e.SecretType == "" {
					e.SecretType = SecretTypePlainText
				}
				return nil
			}
		}
	}
	// Plain string forms
	if strings.HasPrefix(val, "vault.") {
		e.ref = val
		e.SecretType = SecretTypeVault
		e.Val = ""
		if vaultValue, ok := LookupVault(val); ok {
			e.Val = vaultValue
		}
		return nil
	}
	if envKey, ok := strings.CutPrefix(val, "env."); ok {
		e.ref = val
		e.SecretType = SecretTypeEnv
		if envValue, ok := os.LookupEnv(envKey); ok {
			e.Val = envValue
		} else {
			e.Val = ""
		}
		return nil
	}
	e.Val = val
	e.ref = ""
	e.SecretType = SecretTypePlainText
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
		e.ref = ""
		e.SecretType = SecretTypePlainText
		return nil
	}
	switch v := value.(type) {
	case []byte:
		return e.Scan(string(v))
	case string:
		// Check raw value first — quoted strings (e.g. `"vault.x"`) are literals, not refs.
		if strings.HasPrefix(v, "vault.") {
			e.Val = ""
			e.ref = v
			e.SecretType = SecretTypeVault
			if vaultValue, ok := LookupVault(v); ok {
				e.Val = vaultValue
			}
			return nil
		}
		if envKey, ok := strings.CutPrefix(v, "env."); ok {
			e.ref = v
			e.SecretType = SecretTypeEnv
			if envValue, ok := os.LookupEnv(envKey); ok {
				e.Val = envValue
			} else {
				e.Val = ""
			}
			return nil
		}
		e.Val = strings.Trim(v, "\"")
		e.ref = ""
		e.SecretType = SecretTypePlainText
		return nil
	}
	return fmt.Errorf("failed to scan value: %v", value)
}

// Value implements driver.Valuer for database storage.
// It stores the secret reference (e.g., "env.API_KEY" or "vault.path/to/secret") if
// the type is env or vault, otherwise the raw value.
func (e SecretVar) Value() (driver.Value, error) {
	if e.IsFromSecret() {
		return e.ref, nil
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
	if e.IsFromSecret() {
		return false
	}
	return e.GetValue() == "" || e.IsRedacted()
}

// IsMaskedPlaceholder reports whether the value is a client-side redaction
// placeholder that must not overwrite a stored credential. Secret references
// are intentional updates and are never treated as placeholders.
func (e *SecretVar) IsMaskedPlaceholder() bool {
	return e != nil && e.IsRedacted() && !e.IsFromSecret()
}

// IsSet returns true if the SecretVar has a resolved value or a secret reference.
// Use instead of GetValue() != "" when checking whether a field was configured,
// because references may have an empty Val before resolution.
func (e *SecretVar) IsSet() bool {
	if e == nil {
		return false
	}
	if e.IsFromSecret() {
		return e.ref != ""
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
