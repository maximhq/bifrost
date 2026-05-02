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
		{
			name:     "uppercase redacted sentinel",
			input:    EnvVar{Val: "<REDACTED>"},
			expected: true,
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

