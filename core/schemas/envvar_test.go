package schemas

import (
	"encoding/json"
	"os"
	"testing"
)

func TestEnvVar_UnmarshalJSON_DoubleEscapedJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "service account credentials with escaped JSON",
			input:    `"{\"type\":\"service_account\",\"project_id\":\"test-project\"}"`,
			expected: `{"type":"service_account","project_id":"test-project"}`,
		},
		{
			name:     "nested JSON object with multiple levels of escaping",
			input:    `"{\"key\":\"value\",\"nested\":{\"inner\":\"data\"}}"`,
			expected: `{"key":"value","nested":{"inner":"data"}}`,
		},
		{
			name:     "JSON with escaped newlines in private key",
			input:    `"{\"private_key\":\"-----BEGIN PRIVATE KEY-----\\nMIIE...\\n-----END PRIVATE KEY-----\\n\"}"`,
			expected: `{"private_key":"-----BEGIN PRIVATE KEY-----\nMIIE...\n-----END PRIVATE KEY-----\n"}`,
		},
		{
			name:     "simple string value",
			input:    `"sk-test-api-key-12345"`,
			expected: "sk-test-api-key-12345",
		},
		{
			name:     "empty string",
			input:    `""`,
			expected: "",
		},
		{
			name:     "string with special characters",
			input:    `"hello\"world"`,
			expected: `hello"world`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var envVar EnvVar
			err := envVar.UnmarshalJSON([]byte(tt.input))
			if err != nil {
				t.Fatalf("UnmarshalJSON failed: %v", err)
			}
			if envVar.Val != tt.expected {
				t.Errorf("Expected Val=%q, got Val=%q", tt.expected, envVar.Val)
			}
			if envVar.FromEnv {
				t.Errorf("Expected FromEnv=false, got FromEnv=true")
			}
		})
	}
}

func TestEnvVar_UnmarshalJSON_EnvVarReference(t *testing.T) {
	// Set up test environment variable
	os.Setenv("TEST_API_KEY", "actual-api-key-value")
	defer os.Unsetenv("TEST_API_KEY")

	tests := []struct {
		name            string
		input           string
		expectedVal     string
		expectedEnvVar  string
		expectedFromEnv bool
	}{
		{
			name:            "env var reference with value present",
			input:           `"env.TEST_API_KEY"`,
			expectedVal:     "actual-api-key-value",
			expectedEnvVar:  "env.TEST_API_KEY",
			expectedFromEnv: true,
		},
		{
			name:            "env var reference with missing value",
			input:           `"env.NONEXISTENT_VAR"`,
			expectedVal:     "",
			expectedEnvVar:  "env.NONEXISTENT_VAR",
			expectedFromEnv: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var envVar EnvVar
			err := envVar.UnmarshalJSON([]byte(tt.input))
			if err != nil {
				t.Fatalf("UnmarshalJSON failed: %v", err)
			}
			if envVar.Val != tt.expectedVal {
				t.Errorf("Expected Val=%q, got Val=%q", tt.expectedVal, envVar.Val)
			}
			if envVar.EnvVar != tt.expectedEnvVar {
				t.Errorf("Expected EnvVar=%q, got EnvVar=%q", tt.expectedEnvVar, envVar.EnvVar)
			}
			if envVar.FromEnv != tt.expectedFromEnv {
				t.Errorf("Expected FromEnv=%v, got FromEnv=%v", tt.expectedFromEnv, envVar.FromEnv)
			}
		})
	}
}

func TestEnvVar_UnmarshalJSON_FullStructure(t *testing.T) {
	// Test when the input is already an EnvVar JSON object
	input := `{"value":"my-api-key","env_var":"env.MY_KEY","from_env":true}`

	var envVar EnvVar
	err := envVar.UnmarshalJSON([]byte(input))
	if err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}
	if envVar.Val != "my-api-key" {
		t.Errorf("Expected Val=%q, got Val=%q", "my-api-key", envVar.Val)
	}
	if envVar.EnvVar != "env.MY_KEY" {
		t.Errorf("Expected EnvVar=%q, got EnvVar=%q", "env.MY_KEY", envVar.EnvVar)
	}
	if !envVar.FromEnv {
		t.Errorf("Expected FromEnv=true, got FromEnv=false")
	}
}

func TestNewEnvVar_DoubleEscapedJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "service account credentials with escaped JSON",
			input:    `"{\"type\":\"service_account\",\"project_id\":\"test-project\"}"`,
			expected: `{"type":"service_account","project_id":"test-project"}`,
		},
		{
			name:     "JSON with escaped newlines",
			input:    `"{\"private_key\":\"-----BEGIN-----\\nDATA\\n-----END-----\\n\"}"`,
			expected: `{"private_key":"-----BEGIN-----\nDATA\n-----END-----\n"}`,
		},
		{
			name:     "simple string without quotes",
			input:    "sk-test-api-key",
			expected: "sk-test-api-key",
		},
		{
			name:     "simple string with outer quotes",
			input:    `"sk-test-api-key"`,
			expected: "sk-test-api-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envVar := NewEnvVar(tt.input)
			if envVar.Val != tt.expected {
				t.Errorf("Expected Val=%q, got Val=%q", tt.expected, envVar.Val)
			}
		})
	}
}

func TestNewEnvVar_EnvVarReference(t *testing.T) {
	// Set up test environment variable
	os.Setenv("TEST_NEW_ENVVAR_KEY", "resolved-value")
	defer os.Unsetenv("TEST_NEW_ENVVAR_KEY")

	tests := []struct {
		name            string
		input           string
		expectedVal     string
		expectedEnvVar  string
		expectedFromEnv bool
	}{
		{
			name:            "env var reference with value present",
			input:           "env.TEST_NEW_ENVVAR_KEY",
			expectedVal:     "resolved-value",
			expectedEnvVar:  "env.TEST_NEW_ENVVAR_KEY",
			expectedFromEnv: true,
		},
		{
			name:            "env var reference with quotes",
			input:           `"env.TEST_NEW_ENVVAR_KEY"`,
			expectedVal:     "resolved-value",
			expectedEnvVar:  "env.TEST_NEW_ENVVAR_KEY",
			expectedFromEnv: true,
		},
		{
			name:            "env var reference missing",
			input:           "env.MISSING_VAR",
			expectedVal:     "",
			expectedEnvVar:  "env.MISSING_VAR",
			expectedFromEnv: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envVar := NewEnvVar(tt.input)
			if envVar.Val != tt.expectedVal {
				t.Errorf("Expected Val=%q, got Val=%q", tt.expectedVal, envVar.Val)
			}
			if envVar.EnvVar != tt.expectedEnvVar {
				t.Errorf("Expected EnvVar=%q, got EnvVar=%q", tt.expectedEnvVar, envVar.EnvVar)
			}
			if envVar.FromEnv != tt.expectedFromEnv {
				t.Errorf("Expected FromEnv=%v, got FromEnv=%v", tt.expectedFromEnv, envVar.FromEnv)
			}
		})
	}
}

// TestEnvVar_RealWorldVertexCredentials tests the actual use case that triggered
// the double-escaping bug: Vertex AI service account credentials
func TestEnvVar_RealWorldVertexCredentials(t *testing.T) {
	// This simulates what happens when parsing config.json with embedded service account JSON
	type VertexKeyConfig struct {
		ProjectID       EnvVar `json:"project_id"`
		Region          EnvVar `json:"region"`
		AuthCredentials EnvVar `json:"auth_credentials"`
	}

	jsonInput := `{
		"project_id": "my-project",
		"region": "us-central1",
		"auth_credentials": "{\"type\":\"service_account\",\"project_id\":\"my-project\",\"private_key_id\":\"abc123\",\"private_key\":\"-----BEGIN PRIVATE KEY-----\\nMIIE...\\n-----END PRIVATE KEY-----\\n\",\"client_email\":\"test@my-project.iam.gserviceaccount.com\"}"
	}`

	var config VertexKeyConfig
	err := json.Unmarshal([]byte(jsonInput), &config)
	if err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Verify auth_credentials is properly unescaped
	expectedAuthCreds := `{"type":"service_account","project_id":"my-project","private_key_id":"abc123","private_key":"-----BEGIN PRIVATE KEY-----\nMIIE...\n-----END PRIVATE KEY-----\n","client_email":"test@my-project.iam.gserviceaccount.com"}`
	if config.AuthCredentials.Val != expectedAuthCreds {
		t.Errorf("AuthCredentials not properly unescaped.\nExpected: %s\nGot: %s", expectedAuthCreds, config.AuthCredentials.Val)
	}

	// Verify simple string fields work correctly
	if config.ProjectID.Val != "my-project" {
		t.Errorf("Expected ProjectID=%q, got %q", "my-project", config.ProjectID.Val)
	}
	if config.Region.Val != "us-central1" {
		t.Errorf("Expected Region=%q, got %q", "us-central1", config.Region.Val)
	}
}

// TestEnvVar_MixedConfigParsing tests parsing a config with both env var references
// and embedded JSON credentials
func TestEnvVar_MixedConfigParsing(t *testing.T) {
	os.Setenv("TEST_PROJECT_ID", "env-project-id")
	defer os.Unsetenv("TEST_PROJECT_ID")

	type Config struct {
		ProjectID   EnvVar `json:"project_id"`
		Credentials EnvVar `json:"credentials"`
	}

	jsonInput := `{
		"project_id": "env.TEST_PROJECT_ID",
		"credentials": "{\"type\":\"service_account\",\"key\":\"value\"}"
	}`

	var config Config
	err := json.Unmarshal([]byte(jsonInput), &config)
	if err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Verify env var reference is resolved
	if config.ProjectID.Val != "env-project-id" {
		t.Errorf("Expected ProjectID=%q, got %q", "env-project-id", config.ProjectID.Val)
	}
	if !config.ProjectID.FromEnv {
		t.Errorf("Expected ProjectID.FromEnv=true")
	}

	// Verify JSON credentials are properly unescaped
	expectedCreds := `{"type":"service_account","key":"value"}`
	if config.Credentials.Val != expectedCreds {
		t.Errorf("Expected Credentials=%q, got %q", expectedCreds, config.Credentials.Val)
	}
	if config.Credentials.FromEnv {
		t.Errorf("Expected Credentials.FromEnv=false")
	}
}

func TestEnvVar_Equals(t *testing.T) {
	tests := []struct {
		name     string
		a        *EnvVar
		b        *EnvVar
		expected bool
	}{
		{
			name:     "both nil",
			a:        nil,
			b:        nil,
			expected: true,
		},
		{
			name:     "first nil",
			a:        nil,
			b:        &EnvVar{Val: "test"},
			expected: false,
		},
		{
			name:     "second nil",
			a:        &EnvVar{Val: "test"},
			b:        nil,
			expected: false,
		},
		{
			name:     "equal values",
			a:        &EnvVar{Val: "test", EnvVar: "env.TEST", FromEnv: true},
			b:        &EnvVar{Val: "test", EnvVar: "env.TEST", FromEnv: true},
			expected: true,
		},
		{
			name:     "different values",
			a:        &EnvVar{Val: "test1"},
			b:        &EnvVar{Val: "test2"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.a.Equals(tt.b)
			if result != tt.expected {
				t.Errorf("Expected Equals=%v, got %v", tt.expected, result)
			}
		})
	}
}

func TestEnvVar_Redacted(t *testing.T) {
	tests := []struct {
		name        string
		input       EnvVar
		expectedVal string
	}{
		{
			name:        "empty value",
			input:       EnvVar{Val: ""},
			expectedVal: "",
		},
		{
			name:        "short value (8 chars)",
			input:       EnvVar{Val: "12345678"},
			expectedVal: "********",
		},
		{
			name:        "long value",
			input:       EnvVar{Val: "sk-1234567890abcdefghijklmnop"},
			expectedVal: "sk-1************************mnop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.input.Redacted()
			if result.Val != tt.expectedVal {
				t.Errorf("Expected Redacted Val=%q, got %q", tt.expectedVal, result.Val)
			}
		})
	}
}

func TestEnvVar_IsRedacted(t *testing.T) {
	tests := []struct {
		name     string
		input    EnvVar
		expected bool
	}{
		{
			name:     "empty not from env",
			input:    EnvVar{Val: "", FromEnv: false},
			expected: false,
		},
		{
			name:     "from env",
			input:    EnvVar{Val: "test", FromEnv: true},
			expected: true,
		},
		{
			name:     "short all asterisks",
			input:    EnvVar{Val: "****"},
			expected: true,
		},
		{
			name:     "redacted pattern 32 chars",
			input:    EnvVar{Val: "sk-1************************mnop"},
			expected: true,
		},
		{
			name:     "normal value",
			input:    EnvVar{Val: "sk-test-key"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.input.IsRedacted()
			if result != tt.expected {
				t.Errorf("Expected IsRedacted=%v, got %v", tt.expected, result)
			}
		})
	}
}

// TestEnvVar_IsSet verifies the semantic difference between GetValue() != "" and IsSet().
// IsSet() must return true when the EnvVar references an env var (regardless of whether
// that env var has been resolved to a non-empty Val). This is the property that the
// BeforeSave hooks rely on so env var references survive persistence.
func TestEnvVar_IsSet(t *testing.T) {
	tests := []struct {
		name     string
		input    *EnvVar
		expected bool
	}{
		{
			name:     "nil envvar",
			input:    nil,
			expected: false,
		},
		{
			name:     "completely empty",
			input:    &EnvVar{},
			expected: false,
		},
		{
			name:     "only Val set (plain value)",
			input:    &EnvVar{Val: "abc"},
			expected: true,
		},
		{
			name:     "only EnvVar reference set (env not resolved on this server)",
			input:    &EnvVar{EnvVar: "env.MISSING", FromEnv: true},
			expected: true,
		},
		{
			name:     "Val and EnvVar both set (env was resolved)",
			input:    &EnvVar{Val: "resolved-secret", EnvVar: "env.X", FromEnv: true},
			expected: true,
		},
		{
			name:     "FromEnv true but no reference and no value",
			input:    &EnvVar{FromEnv: true},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.IsSet(); got != tt.expected {
				t.Errorf("IsSet() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestEnvVar_MarshalJSON_AutoRedactsEnvBackedValues verifies that any EnvVar marshaled
// to JSON with FromEnv=true is automatically masked, regardless of whether the
// surrounding code remembered to call Redacted() explicitly. This is the defense-in-depth
// guarantee that prevents env-resolved secrets from leaking through unredacted fields.
func TestEnvVar_MarshalJSON_AutoRedactsEnvBackedValues(t *testing.T) {
	tests := []struct {
		name        string
		input       EnvVar
		wantValue   string
		wantEnvVar  string
		wantFromEnv bool
	}{
		{
			name:        "env-backed long secret is redacted",
			input:       EnvVar{Val: "sk-1234567890abcdefghijklmnop", EnvVar: "env.OPENAI_API_KEY", FromEnv: true},
			wantValue:   "sk-1************************mnop",
			wantEnvVar:  "env.OPENAI_API_KEY",
			wantFromEnv: true,
		},
		{
			name:        "env-backed short secret is fully masked",
			input:       EnvVar{Val: "12345678", EnvVar: "env.SHORT", FromEnv: true},
			wantValue:   "********",
			wantEnvVar:  "env.SHORT",
			wantFromEnv: true,
		},
		{
			name:        "env-backed unresolved on this server keeps empty value",
			input:       EnvVar{Val: "", EnvVar: "env.MISSING", FromEnv: true},
			wantValue:   "",
			wantEnvVar:  "env.MISSING",
			wantFromEnv: true,
		},
		{
			name:        "plain value (not from env) is NOT redacted",
			input:       EnvVar{Val: "2024-10-21", EnvVar: "", FromEnv: false},
			wantValue:   "2024-10-21",
			wantEnvVar:  "",
			wantFromEnv: false,
		},
		{
			name:        "empty plain value passes through",
			input:       EnvVar{Val: "", EnvVar: "", FromEnv: false},
			wantValue:   "",
			wantEnvVar:  "",
			wantFromEnv: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}
			var got struct {
				Value   string `json:"value"`
				EnvVar  string `json:"env_var"`
				FromEnv bool   `json:"from_env"`
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal of marshaled output failed: %v", err)
			}
			if got.Value != tt.wantValue {
				t.Errorf("value: got %q, want %q", got.Value, tt.wantValue)
			}
			if got.EnvVar != tt.wantEnvVar {
				t.Errorf("env_var: got %q, want %q", got.EnvVar, tt.wantEnvVar)
			}
			if got.FromEnv != tt.wantFromEnv {
				t.Errorf("from_env: got %v, want %v", got.FromEnv, tt.wantFromEnv)
			}
		})
	}
}

// TestEnvVar_MarshalJSON_DoesNotMutateOriginal ensures the auto-redaction in MarshalJSON
// does not mutate the receiver. The inference path calls GetValue() to build the actual
// HTTP request to the LLM provider, so the original Val must remain intact.
func TestEnvVar_MarshalJSON_DoesNotMutateOriginal(t *testing.T) {
	original := EnvVar{Val: "real-secret-value", EnvVar: "env.SECRET", FromEnv: true}
	if _, err := json.Marshal(original); err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if original.Val != "real-secret-value" {
		t.Errorf("MarshalJSON mutated Val: got %q, want %q", original.Val, "real-secret-value")
	}
	if original.GetValue() != "real-secret-value" {
		t.Errorf("GetValue() returns mutated value: got %q", original.GetValue())
	}
}

// TestEnvVar_MarshalJSON_RoundTripIsRedacted verifies that a marshaled-then-unmarshaled
// env-backed EnvVar is recognized as redacted. The merge logic in provider_keys.go relies
// on this so it can detect "the UI sent back the same redacted value, don't overwrite".
func TestEnvVar_MarshalJSON_RoundTripIsRedacted(t *testing.T) {
	original := EnvVar{Val: "sk-1234567890abcdefghijklmnop", EnvVar: "env.KEY", FromEnv: true}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var roundTripped EnvVar
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if !roundTripped.IsRedacted() {
		t.Errorf("Round-tripped env-backed value should be IsRedacted, got Val=%q", roundTripped.Val)
	}
	if roundTripped.EnvVar != "env.KEY" {
		t.Errorf("env_var reference lost in round-trip: got %q, want %q", roundTripped.EnvVar, "env.KEY")
	}
}

// TestEnvVar_MarshalJSON_DoesNotAffectGetValue is a critical safety net: marshaling an
// EnvVar to JSON must NOT change what GetValue() returns. The inference path uses
// GetValue() to build outgoing LLM requests; if marshaling were to mutate the value,
// every request after a UI fetch would silently start using the redacted mask as the
// API key.
func TestEnvVar_MarshalJSON_DoesNotAffectGetValue(t *testing.T) {
	os.Setenv("MY_REAL_API_KEY", "sk-real-secret-1234567890abcdef")
	defer os.Unsetenv("MY_REAL_API_KEY")

	ev := NewEnvVar("env.MY_REAL_API_KEY")
	if ev.GetValue() != "sk-real-secret-1234567890abcdef" {
		t.Fatalf("setup: GetValue() = %q, want resolved env value", ev.GetValue())
	}

	// Marshaling would redact in the JSON output, but must not touch the in-memory Val.
	if _, err := json.Marshal(ev); err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	if ev.GetValue() != "sk-real-secret-1234567890abcdef" {
		t.Errorf("GetValue() returns mutated value after MarshalJSON: got %q", ev.GetValue())
	}
}
