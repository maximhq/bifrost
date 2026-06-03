package schemas

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGigaChatKeyConfig_Redacted(t *testing.T) {
	t.Parallel()

	config := &GigaChatKeyConfig{
		Credentials:  NewEnvVar("secret-credentials"),
		User:         NewEnvVar("secret-user"),
		Password:     NewEnvVar("secret-password"),
		AccessToken:  NewEnvVar("secret-access-token"),
		CertFile:     "/secure/client.pem",
		KeyFile:      "/secure/client.key",
		CABundleFile: "/secure/ca.pem",
		BaseURL:      "https://api.giga.chat",
		AuthURL:      "https://ngw.devices.sberbank.ru:9443/api/v2/oauth",
	}

	redacted := config.Redacted()
	data, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	output := string(data)

	for _, secret := range []string{
		"secret-credentials",
		"secret-user",
		"secret-password",
		"secret-access-token",
		"/secure/client.pem",
		"/secure/client.key",
		"/secure/ca.pem",
	} {
		if strings.Contains(output, secret) {
			t.Fatalf("redacted config leaked %q in %s", secret, output)
		}
	}
	if !strings.Contains(output, "https://api.giga.chat") {
		t.Fatalf("non-secret base_url should be preserved in %s", output)
	}
}

func TestGigaChatKeyConfig_EnvVarAuthMaterial(t *testing.T) {
	t.Parallel()

	config := &GigaChatKeyConfig{
		Credentials: NewEnvVar("env.MISSING_GIGACHAT_CREDENTIALS"),
	}
	config.CheckAndSetDefaults()

	if config.Scope != DefaultGigaChatScope {
		t.Fatalf("scope mismatch: got %q, want %q", config.Scope, DefaultGigaChatScope)
	}
	if !config.HasAuthMaterial() {
		t.Fatal("expected unresolved env var reference to count as configured auth material")
	}
}

func TestGigaChatKeyConfig_AuthMaterial(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		config   *GigaChatKeyConfig
		wantAuth bool
		wantTLS  bool
	}{
		{
			name:     "NilConfig",
			config:   nil,
			wantAuth: false,
			wantTLS:  false,
		},
		{
			name: "Credentials",
			config: &GigaChatKeyConfig{
				Credentials: NewEnvVar("env.GIGACHAT_CREDENTIALS"),
			},
			wantAuth: true,
			wantTLS:  false,
		},
		{
			name: "AccessToken",
			config: &GigaChatKeyConfig{
				AccessToken: NewEnvVar("env.GIGACHAT_ACCESS_TOKEN"),
			},
			wantAuth: true,
			wantTLS:  false,
		},
		{
			name: "UserPassword",
			config: &GigaChatKeyConfig{
				User:     NewEnvVar("env.GIGACHAT_USER"),
				Password: NewEnvVar("env.GIGACHAT_PASSWORD"),
			},
			wantAuth: true,
			wantTLS:  false,
		},
		{
			name: "ClientCertificatePair",
			config: &GigaChatKeyConfig{
				CertFile: "/secure/client.pem",
				KeyFile:  "/secure/client.key",
			},
			wantAuth: false,
			wantTLS:  true,
		},
		{
			name: "CABundle",
			config: &GigaChatKeyConfig{
				CABundleFile: "/secure/ca.pem",
			},
			wantAuth: false,
			wantTLS:  true,
		},
		{
			name: "CredentialsWithTLS",
			config: &GigaChatKeyConfig{
				Credentials:  NewEnvVar("env.GIGACHAT_CREDENTIALS"),
				CertFile:     "/secure/client.pem",
				KeyFile:      "/secure/client.key",
				CABundleFile: "/secure/ca.pem",
			},
			wantAuth: true,
			wantTLS:  true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.config.HasAuthMaterial(); got != testCase.wantAuth {
				t.Fatalf("HasAuthMaterial mismatch: got %v, want %v", got, testCase.wantAuth)
			}
			if got := testCase.config.HasTLSMaterial(); got != testCase.wantTLS {
				t.Fatalf("HasTLSMaterial mismatch: got %v, want %v", got, testCase.wantTLS)
			}
		})
	}
}
