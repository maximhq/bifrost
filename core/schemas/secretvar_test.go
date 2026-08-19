package schemas

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSecretVar_UnmarshalJSON_DoubleEscapedJSON(t *testing.T) {
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
			var secretVar SecretVar
			err := secretVar.UnmarshalJSON([]byte(tt.input))
			if err != nil {
				t.Fatalf("UnmarshalJSON failed: %v", err)
			}
			if secretVar.Val != tt.expected {
				t.Errorf("Expected Val=%q, got Val=%q", tt.expected, secretVar.Val)
			}
			if secretVar.IsFromSecret() {
				t.Errorf("Expected IsFromSecret()=false, got true")
			}
		})
	}
}

func TestSecretVar_UnmarshalJSON_SecretVarReference(t *testing.T) {
	os.Setenv("TEST_API_KEY", "actual-api-key-value")
	defer os.Unsetenv("TEST_API_KEY")

	tests := []struct {
		name               string
		input              string
		expectedVal        string
		expectedRef        string
		expectedFromSecret bool
	}{
		{
			name:               "env var reference with value present",
			input:              `"env.TEST_API_KEY"`,
			expectedVal:        "actual-api-key-value",
			expectedRef:        "env.TEST_API_KEY",
			expectedFromSecret: true,
		},
		{
			name:               "env var reference with missing value",
			input:              `"env.NONEXISTENT_VAR"`,
			expectedVal:        "",
			expectedRef:        "env.NONEXISTENT_VAR",
			expectedFromSecret: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var secretVar SecretVar
			err := secretVar.UnmarshalJSON([]byte(tt.input))
			if err != nil {
				t.Fatalf("UnmarshalJSON failed: %v", err)
			}
			if secretVar.Val != tt.expectedVal {
				t.Errorf("Expected Val=%q, got Val=%q", tt.expectedVal, secretVar.Val)
			}
			if secretVar.GetRawRef() != tt.expectedRef {
				t.Errorf("Expected Ref()=%q, got %q", tt.expectedRef, secretVar.GetRawRef())
			}
			if secretVar.IsFromSecret() != tt.expectedFromSecret {
				t.Errorf("Expected IsFromSecret()=%v, got %v", tt.expectedFromSecret, secretVar.IsFromSecret())
			}
		})
	}
}

// TestSecretVar_UnmarshalJSON_BackwardCompat verifies that the old env_var/from_env JSON
// format (shipped in previous versions) still deserializes correctly to the new fields.
func TestSecretVar_UnmarshalJSON_BackwardCompat(t *testing.T) {
	os.Setenv("MY_KEY", "resolved-value")
	defer os.Unsetenv("MY_KEY")

	t.Run("from_env without value field", func(t *testing.T) {
		input := `{"env_var":"MY_KEY","from_env":true}`
		var sv SecretVar
		if err := sv.UnmarshalJSON([]byte(input)); err != nil {
			t.Fatalf("UnmarshalJSON failed: %v", err)
		}
		if sv.GetRawRef() != "env.MY_KEY" {
			t.Errorf("expected ref %q, got %q", "env.MY_KEY", sv.GetRawRef())
		}
		if !sv.IsFromSecret() {
			t.Error("expected IsFromSecret=true")
		}
		if sv.Val != "resolved-value" {
			t.Errorf("expected Val=%q, got %q", "resolved-value", sv.Val)
		}
	})

	t.Run("ref+type without value field", func(t *testing.T) {
		input := `{"ref":"env.MY_KEY","type":"env"}`
		var sv SecretVar
		if err := sv.UnmarshalJSON([]byte(input)); err != nil {
			t.Fatalf("UnmarshalJSON failed: %v", err)
		}
		if sv.GetRawRef() != "env.MY_KEY" {
			t.Errorf("expected ref %q, got %q", "env.MY_KEY", sv.GetRawRef())
		}
		if !sv.IsFromSecret() {
			t.Error("expected IsFromSecret=true")
		}
		if sv.Val != "resolved-value" {
			t.Errorf("expected Val=%q, got %q", "resolved-value", sv.Val)
		}
	})

	t.Run("ref without type is treated as literal JSON, not a secret", func(t *testing.T) {
		input := `{"ref":"project-id"}`
		var sv SecretVar
		if err := sv.UnmarshalJSON([]byte(input)); err != nil {
			t.Fatalf("UnmarshalJSON failed: %v", err)
		}
		if sv.IsFromSecret() {
			t.Error("expected IsFromSecret=false for ref-only without type")
		}
		if sv.Val != `{"ref":"project-id"}` {
			t.Errorf("expected Val to be raw JSON string, got %q", sv.Val)
		}
	})

	t.Run("env_var without from_env is treated as literal JSON, not a secret", func(t *testing.T) {
		input := `{"env_var":"MY_KEY"}`
		var sv SecretVar
		if err := sv.UnmarshalJSON([]byte(input)); err != nil {
			t.Fatalf("UnmarshalJSON failed: %v", err)
		}
		if sv.IsFromSecret() {
			t.Error("expected IsFromSecret=false for env_var without from_env")
		}
		if sv.Val != `{"env_var":"MY_KEY"}` {
			t.Errorf("expected Val to be raw JSON string, got %q", sv.Val)
		}
	})

	t.Run("old env_var/from_env format", func(t *testing.T) {
		input := `{"value":"my-api-key","env_var":"env.MY_KEY","from_env":true}`
		var secretVar SecretVar
		err := secretVar.UnmarshalJSON([]byte(input))
		if err != nil {
			t.Fatalf("UnmarshalJSON failed: %v", err)
		}
		if secretVar.GetRawRef() != "env.MY_KEY" {
			t.Errorf("Expected Ref()=%q, got %q", "env.MY_KEY", secretVar.GetRawRef())
		}
		if !secretVar.IsFromSecret() {
			t.Error("Expected IsFromSecret()=true, got false")
		}
		if secretVar.Val != "resolved-value" {
			t.Errorf("Expected Val=%q, got %q", "resolved-value", secretVar.Val)
		}
	})
}

func TestNewSecretVar_DoubleEscapedJSON(t *testing.T) {
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
			secretVar := NewSecretVar(tt.input)
			if secretVar.Val != tt.expected {
				t.Errorf("Expected Val=%q, got Val=%q", tt.expected, secretVar.Val)
			}
		})
	}
}

func TestNewSecretVar_SecretVarReference(t *testing.T) {
	os.Setenv("TEST_NEW_ENVVAR_KEY", "resolved-value")
	defer os.Unsetenv("TEST_NEW_ENVVAR_KEY")

	tests := []struct {
		name               string
		input              string
		expectedVal        string
		expectedRef        string
		expectedFromSecret bool
	}{
		{
			name:               "env var reference with value present",
			input:              "env.TEST_NEW_ENVVAR_KEY",
			expectedVal:        "resolved-value",
			expectedRef:        "env.TEST_NEW_ENVVAR_KEY",
			expectedFromSecret: true,
		},
		{
			name:               "env var reference with quotes",
			input:              `"env.TEST_NEW_ENVVAR_KEY"`,
			expectedVal:        "resolved-value",
			expectedRef:        "env.TEST_NEW_ENVVAR_KEY",
			expectedFromSecret: true,
		},
		{
			name:               "env var reference missing",
			input:              "env.MISSING_VAR",
			expectedVal:        "",
			expectedRef:        "env.MISSING_VAR",
			expectedFromSecret: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secretVar := NewSecretVar(tt.input)
			if secretVar.Val != tt.expectedVal {
				t.Errorf("Expected Val=%q, got Val=%q", tt.expectedVal, secretVar.Val)
			}
			if secretVar.GetRawRef() != tt.expectedRef {
				t.Errorf("Expected Ref()=%q, got %q", tt.expectedRef, secretVar.GetRawRef())
			}
			if secretVar.IsFromSecret() != tt.expectedFromSecret {
				t.Errorf("Expected IsFromSecret()=%v, got %v", tt.expectedFromSecret, secretVar.IsFromSecret())
			}
		})
	}
}

func TestNewSecretVar_FromEnvWithoutValueField(t *testing.T) {
	os.Setenv("MY_KEY", "resolved-value")
	defer os.Unsetenv("MY_KEY")

	sv := NewSecretVar(`{"env_var":"MY_KEY","from_env":true}`)
	if sv.GetRawRef() != "env.MY_KEY" {
		t.Errorf("expected ref %q, got %q", "env.MY_KEY", sv.GetRawRef())
	}
	if !sv.IsFromSecret() {
		t.Error("expected IsFromSecret=true")
	}
	if sv.Val != "resolved-value" {
		t.Errorf("expected Val=%q, got %q", "resolved-value", sv.Val)
	}
}

// TestSecretVar_RealWorldVertexCredentials tests the actual use case that triggered
// the double-escaping bug: Vertex AI service account credentials
func TestSecretVar_RealWorldVertexCredentials(t *testing.T) {
	type VertexKeyConfig struct {
		ProjectID       SecretVar `json:"project_id"`
		Region          SecretVar `json:"region"`
		AuthCredentials SecretVar `json:"auth_credentials"`
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

	expectedAuthCreds := `{"type":"service_account","project_id":"my-project","private_key_id":"abc123","private_key":"-----BEGIN PRIVATE KEY-----\nMIIE...\n-----END PRIVATE KEY-----\n","client_email":"test@my-project.iam.gserviceaccount.com"}`
	if config.AuthCredentials.Val != expectedAuthCreds {
		t.Errorf("AuthCredentials not properly unescaped.\nExpected: %s\nGot: %s", expectedAuthCreds, config.AuthCredentials.Val)
	}
	if config.ProjectID.Val != "my-project" {
		t.Errorf("Expected ProjectID=%q, got %q", "my-project", config.ProjectID.Val)
	}
	if config.Region.Val != "us-central1" {
		t.Errorf("Expected Region=%q, got %q", "us-central1", config.Region.Val)
	}
}

// TestSecretVar_MixedConfigParsing tests parsing a config with both env var references
// and embedded JSON credentials
func TestSecretVar_MixedConfigParsing(t *testing.T) {
	os.Setenv("TEST_PROJECT_ID", "env-project-id")
	defer os.Unsetenv("TEST_PROJECT_ID")

	type Config struct {
		ProjectID   SecretVar `json:"project_id"`
		Credentials SecretVar `json:"credentials"`
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

	if config.ProjectID.Val != "env-project-id" {
		t.Errorf("Expected ProjectID=%q, got %q", "env-project-id", config.ProjectID.Val)
	}
	if !config.ProjectID.IsFromSecret() {
		t.Error("Expected ProjectID.IsFromSecret()=true")
	}

	expectedCreds := `{"type":"service_account","key":"value"}`
	if config.Credentials.Val != expectedCreds {
		t.Errorf("Expected Credentials=%q, got %q", expectedCreds, config.Credentials.Val)
	}
	if config.Credentials.IsFromSecret() {
		t.Error("Expected Credentials.IsFromSecret()=false")
	}
}

func TestSecretVar_Equals(t *testing.T) {
	tests := []struct {
		name     string
		a        *SecretVar
		b        *SecretVar
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
			b:        &SecretVar{Val: "test"},
			expected: false,
		},
		{
			name:     "second nil",
			a:        &SecretVar{Val: "test"},
			b:        nil,
			expected: false,
		},
		{
			name:     "equal values",
			a:        &SecretVar{Val: "test", ref: "env.TEST", SecretType: SecretTypeEnv},
			b:        &SecretVar{Val: "test", ref: "env.TEST", SecretType: SecretTypeEnv},
			expected: true,
		},
		{
			name:     "different values",
			a:        &SecretVar{Val: "test1"},
			b:        &SecretVar{Val: "test2"},
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

func TestSecretVar_Redacted(t *testing.T) {
	tests := []struct {
		name        string
		input       SecretVar
		expectedVal string
	}{
		{
			name:        "empty value",
			input:       SecretVar{Val: ""},
			expectedVal: "",
		},
		{
			name:        "short value (8 chars)",
			input:       SecretVar{Val: "12345678"},
			expectedVal: "********",
		},
		{
			name:        "long value",
			input:       SecretVar{Val: "sk-1234567890abcdefghijklmnop"},
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

func TestSecretVar_FullyRedacted(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var ev *SecretVar
		if got := ev.FullyRedacted(); got != nil {
			t.Fatalf("expected nil, got %+v", got)
		}
	})

	tests := []struct {
		name           string
		input          SecretVar
		wantVal        string
		wantFromSecret bool
		wantRef        string
	}{
		{
			name:           "empty value",
			input:          SecretVar{Val: ""},
			wantVal:        "",
			wantFromSecret: false,
		},
		{
			name:           "long literal never leaks prefix or suffix",
			input:          SecretVar{Val: "mysecretpassword"},
			wantVal:        "<REDACTED>",
			wantFromSecret: false,
		},
		{
			name:           "resolved env password preserves reference metadata",
			input:          SecretVar{Val: "resolved-secret", ref: "env.PROXY_PASS", SecretType: SecretTypeEnv},
			wantVal:        "<REDACTED>",
			wantFromSecret: true,
			wantRef:        "env.PROXY_PASS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.input.FullyRedacted()
			if result.Val != tt.wantVal {
				t.Errorf("Val: want %q, got %q", tt.wantVal, result.Val)
			}
			if result.IsFromSecret() != tt.wantFromSecret {
				t.Errorf("IsFromSecret(): want %v, got %v", tt.wantFromSecret, result.IsFromSecret())
			}
			if result.GetRawRef() != tt.wantRef {
				t.Errorf("Ref(): want %q, got %q", tt.wantRef, result.GetRawRef())
			}
		})
	}
}

func TestSecretVar_IsRedacted(t *testing.T) {
	tests := []struct {
		name     string
		input    SecretVar
		expected bool
	}{
		{
			name:     "empty not from secret",
			input:    SecretVar{Val: ""},
			expected: false,
		},
		{
			name:     "from secret",
			input:    SecretVar{Val: "test", ref: "env.KEY", SecretType: SecretTypeEnv},
			expected: true,
		},
		{
			name:     "short all asterisks",
			input:    SecretVar{Val: "****"},
			expected: true,
		},
		{
			name:     "redacted pattern 32 chars",
			input:    SecretVar{Val: "sk-1************************mnop"},
			expected: true,
		},
		{
			name:     "normal value",
			input:    SecretVar{Val: "sk-test-key"},
			expected: false,
		},
		{
			name:     "uppercase redacted sentinel",
			input:    SecretVar{Val: "<REDACTED>"},
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

// TestSecretVar_IsSet verifies the semantic difference between GetValue() != "" and IsSet().
// IsSet() must return true when the SecretVar references an env var or vault secret
// (regardless of whether the reference has been resolved to a non-empty Val).
func TestSecretVar_IsSet(t *testing.T) {
	tests := []struct {
		name     string
		input    *SecretVar
		expected bool
	}{
		{
			name:     "nil",
			input:    nil,
			expected: false,
		},
		{
			name:     "completely empty",
			input:    &SecretVar{},
			expected: false,
		},
		{
			name:     "only Val set (plain value)",
			input:    &SecretVar{Val: "abc"},
			expected: true,
		},
		{
			name:     "env reference not yet resolved",
			input:    &SecretVar{ref: "env.MISSING", SecretType: SecretTypeEnv},
			expected: true,
		},
		{
			name:     "env reference resolved",
			input:    &SecretVar{Val: "resolved-secret", ref: "env.X", SecretType: SecretTypeEnv},
			expected: true,
		},
		{
			name:     "secret type set but no reference and no value",
			input:    &SecretVar{SecretType: SecretTypeEnv},
			expected: false,
		},
		{
			name:     "vault reference set",
			input:    &SecretVar{Val: "vault.bifrost/key", ref: "vault.bifrost/key", SecretType: SecretTypeVault},
			expected: true,
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

func TestSecretVar_Scan_VaultRef(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantVal        string
		wantRef        string
		wantFromSecret bool
	}{
		{
			name:           "plain vault reference",
			input:          "vault.bifrost/providers/openai/key",
			wantVal:        "",
			wantRef:        "vault.bifrost/providers/openai/key",
			wantFromSecret: true,
		},
		{
			name:    "quoted vault string is treated as literal, not a vault ref",
			input:   `"vault.myproject/secret"`,
			wantVal: "vault.myproject/secret",
		},
		{
			name:           "env reference",
			input:          "env.MY_VAR",
			wantRef:        "env.MY_VAR",
			wantFromSecret: true,
		},
		{
			name:    "plain string unaffected",
			input:   "sk-abc123",
			wantVal: "sk-abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e SecretVar
			if err := e.Scan(tt.input); err != nil {
				t.Fatalf("Scan() error: %v", err)
			}
			if e.IsFromSecret() != tt.wantFromSecret {
				t.Errorf("IsFromSecret() = %v, want %v", e.IsFromSecret(), tt.wantFromSecret)
			}
			if tt.wantFromSecret && e.GetRawRef() != tt.wantRef {
				t.Errorf("Ref() = %q, want %q", e.GetRawRef(), tt.wantRef)
			}
			if tt.wantVal != "" && e.Val != tt.wantVal {
				t.Errorf("Val = %q, want %q", e.Val, tt.wantVal)
			}
		})
	}
}

func TestSecretVar_UnmarshalJSON_VaultRef(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantVal        string
		wantRef        string
		wantFromSecret bool
	}{
		{
			name:           "new format: type field",
			input:          `{"value":"","ref":"vault.bifrost/key","type":"vault"}`,
			wantVal:        "",
			wantRef:        "vault.bifrost/key",
			wantFromSecret: true,
		},
		{
			name:           "plain string vault reference",
			input:          `"vault.myproject/secret"`,
			wantVal:        "",
			wantRef:        "vault.myproject/secret",
			wantFromSecret: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e SecretVar
			if err := json.Unmarshal([]byte(tt.input), &e); err != nil {
				t.Fatalf("UnmarshalJSON() error: %v", err)
			}
			if e.IsFromSecret() != tt.wantFromSecret {
				t.Errorf("IsFromSecret() = %v, want %v", e.IsFromSecret(), tt.wantFromSecret)
			}
			if e.GetRawRef() != tt.wantRef {
				t.Errorf("Ref() = %q, want %q", e.GetRawRef(), tt.wantRef)
			}
			if e.Val != tt.wantVal {
				t.Errorf("Val = %q, want %q", e.Val, tt.wantVal)
			}
		})
	}
}

func TestSecretVar_Value_VaultRef(t *testing.T) {
	e := &SecretVar{Val: "actual-secret", ref: "vault.bifrost/key", SecretType: SecretTypeVault}
	got, err := e.Value()
	if err != nil {
		t.Fatalf("Value() error: %v", err)
	}
	if got != "vault.bifrost/key" {
		t.Errorf("Value() = %q, want %q", got, "vault.bifrost/key")
	}
}

func TestSecretVar_Redacted_VaultRef(t *testing.T) {
	e := &SecretVar{Val: "actual-secret-value", ref: "vault.bifrost/key", SecretType: SecretTypeVault}
	r := e.Redacted()
	wantVal := "actu************************alue"
	if r.Val != wantVal {
		t.Errorf("Redacted().Val = %q, want %q", r.Val, wantVal)
	}
	if !r.IsFromSecret() {
		t.Error("Redacted().IsFromSecret() = false, want true")
	}
	if r.GetRawRef() != "vault.bifrost/key" {
		t.Errorf("Redacted().GetRawRef() = %q, want %q", r.GetRawRef(), "vault.bifrost/key")
	}
}

func TestSecretVar_IsSet_VaultRef(t *testing.T) {
	set := &SecretVar{Val: "vault.bifrost/key", ref: "vault.bifrost/key", SecretType: SecretTypeVault}
	unset := &SecretVar{SecretType: SecretTypeVault}
	if !set.IsSet() {
		t.Error("IsSet() = false for vault ref, want true")
	}
	if unset.IsSet() {
		t.Error("IsSet() = true for empty vault ref, want false")
	}
}

func TestSecretVar_IsMaskedPlaceholder(t *testing.T) {
	tests := []struct {
		name  string
		value *SecretVar
		want  bool
	}{
		{name: "nil", value: nil, want: false},
		{name: "empty", value: NewSecretVar(""), want: false},
		{name: "plain", value: NewSecretVar("sk-real-credential"), want: false},
		{name: "masked", value: NewSecretVar("abcd************************wxyz"), want: true},
		{name: "redacted sentinel", value: NewSecretVar("<redacted>"), want: true},
		{name: "env reference", value: NewSecretVar("env.NEW_KEY"), want: false},
		{name: "vault reference", value: NewSecretVar("vault.path/to/key"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.value.IsMaskedPlaceholder(); got != tt.want {
				t.Fatalf("IsMaskedPlaceholder() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewSecretVar_VaultRef(t *testing.T) {
	e := NewSecretVar("vault.bifrost/mykey")
	if !e.IsFromVault() {
		t.Error("IsFromVault() = false, want true")
	}
	if e.GetRawRef() != "vault.bifrost/mykey" {
		t.Errorf("Ref() = %q, want %q", e.GetRawRef(), "vault.bifrost/mykey")
	}
	if e.Val != "" {
		t.Errorf("Val = %q, want empty string when vault lookup fails", e.Val)
	}
}

func TestSecretVar_MarshalJSON(t *testing.T) {
	tests := []struct {
		name  string
		input SecretVar
		want  string
	}{
		{
			name:  "plain value",
			input: SecretVar{Val: "my-api-key"},
			want:  `{"value":"my-api-key"}`,
		},
		{
			name:  "env reference",
			input: SecretVar{Val: "resolved", ref: "env.MY_KEY", SecretType: SecretTypeEnv},
			want:  `{"value":"resolved","ref":"env.MY_KEY","type":"env"}`,
		},
		{
			name:  "vault reference",
			input: SecretVar{ref: "vault.path/to/secret", SecretType: SecretTypeVault},
			want:  `{"value":"","ref":"vault.path/to/secret","type":"vault"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("MarshalJSON() error: %v", err)
			}
			if string(b) != tt.want {
				t.Errorf("MarshalJSON() = %s, want %s", b, tt.want)
			}
		})
	}
}
