package schemas

import (
	"database/sql/driver"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
)

// EnvVar is a wrapper around a value that can be sourced from an environment variable.
type EnvVar struct {
	Val     string `json:"value"`
	EnvVar  string `json:"env_var"`
	FromEnv bool   `json:"from_env"`
}

// NewEnvVar creates a new EnvValue from a string.
func NewEnvVar(value string) *EnvVar {
	// Cleanup string if required
	// Use strconv.Unquote to properly handle JSON string escape sequences
	// This converts "\"{\\\"key\\\":\\\"value\\\"}\"" to "{\"key\":\"value\"}"
	val := value
	if unquoted, err := strconv.Unquote(value); err == nil {
		val = unquoted
	}
	// Here we will need to check if the incoming data is a valid JSON object
	// If it's a valid JSON object and follows the EnvVar schema, then we will unmarshal it into an EnvVar object
	if sonic.Valid([]byte(value)) {
		valueNode, _ := sonic.Get([]byte(val), "value")
		envNode, _ := sonic.Get([]byte(val), "env_var")
		if valueNode.Exists() && envNode.Exists() {
			// Use a type alias to avoid infinite recursion (alias doesn't inherit methods)
			type envVarAlias EnvVar
			var envVar envVarAlias
			if err := sonic.Unmarshal([]byte(value), &envVar); err == nil {
				e := &EnvVar{
					Val:     envVar.Val,
					FromEnv: envVar.FromEnv,
					EnvVar:  envVar.EnvVar,
				}
				// Here we will check if the Val starts with env and is same as the EnvVar
				if strings.HasPrefix(e.Val, "env.") && e.Val == e.EnvVar {
					e.Val = ""
					// Load the environment variable value
					envValue, ok := os.LookupEnv(strings.TrimPrefix(e.EnvVar, "env."))
					if ok {
						e.Val = envValue
					}
					e.FromEnv = true
				}
				return e
			}
		}
	}
	if envKey, ok := strings.CutPrefix(val, "env."); ok {
		if envValue, ok := os.LookupEnv(envKey); ok {
			return &EnvVar{
				Val:     envValue,
				FromEnv: true,
				EnvVar:  val,
			}
		}
		return &EnvVar{
			Val:     "",
			FromEnv: true,
			EnvVar:  val,
		}
	}
	return &EnvVar{
		Val:     val,
		FromEnv: false,
		EnvVar:  "",
	}
}

// IsRedacted returns true if the value is redacted.
func (e *EnvVar) IsRedacted() bool {
	if e.Val == "" && !e.FromEnv {
		return false
	}
	// Check if it's an environment variable reference
	if e.FromEnv {
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
	return false
}

// Equals checks if two SecretKeys are equal.
func (e *EnvVar) Equals(other *EnvVar) bool {
	if e == nil && other == nil {
		return true
	}
	if e == nil || other == nil {
		return false
	}
	return e.Val == other.Val &&
		e.EnvVar == other.EnvVar &&
		e.FromEnv == other.FromEnv
}

// Redacted returns a new SecretKey with the value redacted.
func (e *EnvVar) Redacted() *EnvVar {
	if e.Val == "" {
		return &EnvVar{
			Val:     "",
			FromEnv: e.FromEnv,
			EnvVar:  e.EnvVar,
		}
	}
	// If key is 8 characters or less, just return all asterisks
	if len(e.Val) <= 8 {
		return &EnvVar{
			Val:     strings.Repeat("*", len(e.Val)),
			FromEnv: e.FromEnv,
			EnvVar:  e.EnvVar,
		}
	}
	// Show first 4 and last 4 characters, replace middle with asterisks
	prefix := e.Val[:4]
	suffix := e.Val[len(e.Val)-4:]
	middle := strings.Repeat("*", 24)

	return &EnvVar{
		Val:     prefix + middle + suffix,
		FromEnv: e.FromEnv,
		EnvVar:  e.EnvVar,
	}
}

// UnmarshalJSON unmarshals the value from JSON.
func (e *EnvVar) UnmarshalJSON(data []byte) error {
	// This is always going to be value
	// Here we will first considering this as value
	// if it has env. then we will process it and set the FromEnv to true
	// if it doesn't have env. then we will set the FromEnv to false
	// if it has env. then we will process it and set the FromEnv to true
	val := string(data)
	// Cleanup string if required
	// Use strconv.Unquote to properly handle JSON string escape sequences
	// This converts "\"{\\\"key\\\":\\\"value\\\"}\"" to "{\"key\":\"value\"}"
	if unquoted, err := strconv.Unquote(val); err == nil {
		val = unquoted
	}
	// Here we will need to check if the incoming data is a valid JSON object
	// If it's a valid JSON object and follows the EnvVar schema, then we will unmarshal it into an EnvVar object
	if sonic.Valid(data) {
		valueNode, _ := sonic.Get(data, "value")
		envNode, _ := sonic.Get(data, "env_var")
		if valueNode.Exists() && envNode.Exists() {
			// Use a type alias to avoid infinite recursion (alias doesn't inherit methods)
			type envVarAlias EnvVar
			var envVar envVarAlias
			if err := sonic.Unmarshal(data, &envVar); err == nil {
				e.Val = envVar.Val
				e.FromEnv = envVar.FromEnv
				e.EnvVar = envVar.EnvVar
				// Here we will check if the Val starts with env and is same as the EnvVar
				if strings.HasPrefix(e.Val, "env.") && e.Val == e.EnvVar {
					e.Val = ""
					// Load the environment variable value
					envValue, ok := os.LookupEnv(strings.TrimPrefix(e.EnvVar, "env."))
					if ok {
						e.Val = envValue
					}
					e.FromEnv = true
				}
				return nil
			}
			// Else the value is JSON, so we will treat this as a normal value
		}
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
		return nil
	}
	e.Val = val
	e.FromEnv = false
	e.EnvVar = ""
	return nil
}

// String returns the value as a string.
func (e *EnvVar) String() string {
	return e.Val
}

// Scan scans the value from the database.
func (e *EnvVar) Scan(value any) error {
	if value == nil {
		e.Val = ""
		e.FromEnv = false
		e.EnvVar = ""
		return nil
	}
	switch v := value.(type) {
	case []byte:
		return e.Scan(string(v))
	case string:
		// Cleanup string if required
		// The string may have "\"env.TEST\"", "env.TEST" or "env.TEST\"", we need to clean it up to "env.TEST"
		val := strings.Trim(v, "\"")
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
			return nil
		}
		e.Val = val
		e.FromEnv = false
		e.EnvVar = ""
		return nil
	}
	return fmt.Errorf("failed to scan value: %v", value)
}

// Value implements driver.Valuer for database storage.
// It stores the original env reference (e.g., "env.API_KEY") if FromEnv is true,
// otherwise stores the raw value.
func (e EnvVar) Value() (driver.Value, error) {
	if e.FromEnv {
		return e.EnvVar, nil
	}
	return e.Val, nil
}

// IsFromEnv returns true if the value is sourced from an environment variable.
func (e *EnvVar) IsFromEnv() bool {
	return e.FromEnv
}

// GetValue returns the value.
func (e *EnvVar) GetValue() string {
	if e == nil {
		return ""
	}
	return e.Val
}

// GetValuePtr returns a pointer to the value.
func (e *EnvVar) GetValuePtr() *string {
	if e == nil {
		return nil
	}
	return &e.Val
}

// CoerceInt coerces value to int
func (e *EnvVar) CoerceInt(defaultValue int) int {
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
func (e *EnvVar) CoerceBool(defaultValue bool) bool {
	if e == nil {
		return defaultValue
	}
	val, err := strconv.ParseBool(e.GetValue())
	if err != nil {
		return defaultValue
	}
	return val
}
