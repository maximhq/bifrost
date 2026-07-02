package gigachat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestGigaChatKeyConfigRedacted(t *testing.T) {
	t.Parallel()

	config := &schemas.GigaChatKeyConfig{
		Credentials:  schemas.NewSecretVar("secret-credentials"),
		User:         schemas.NewSecretVar("secret-user"),
		Password:     schemas.NewSecretVar("secret-password"),
		AccessToken:  schemas.NewSecretVar("secret-access-token"),
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

func TestGigaChatKeyConfigEnvVarAuthMaterial(t *testing.T) {
	t.Parallel()

	config := &schemas.GigaChatKeyConfig{
		Credentials: schemas.NewSecretVar("env.MISSING_GIGACHAT_CREDENTIALS"),
	}
	config.CheckAndSetDefaults()

	if config.Scope != schemas.DefaultGigaChatScope {
		t.Fatalf("scope mismatch: got %q, want %q", config.Scope, schemas.DefaultGigaChatScope)
	}
	if !config.HasAuthMaterial() {
		t.Fatal("expected unresolved env var reference to count as configured auth material")
	}
}

func TestGigaChatKeyConfigAuthMaterial(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		config   *schemas.GigaChatKeyConfig
		wantAuth bool
		wantTLS  bool
		wantMTLS bool
	}{
		{
			name:     "NilConfig",
			config:   nil,
			wantAuth: false,
			wantTLS:  false,
			wantMTLS: false,
		},
		{
			name: "Credentials",
			config: &schemas.GigaChatKeyConfig{
				Credentials: schemas.NewSecretVar("env.GIGACHAT_CREDENTIALS"),
			},
			wantAuth: true,
			wantTLS:  false,
			wantMTLS: false,
		},
		{
			name: "AccessToken",
			config: &schemas.GigaChatKeyConfig{
				AccessToken: schemas.NewSecretVar("env.GIGACHAT_ACCESS_TOKEN"),
			},
			wantAuth: true,
			wantTLS:  false,
			wantMTLS: false,
		},
		{
			name: "UserPassword",
			config: &schemas.GigaChatKeyConfig{
				User:     schemas.NewSecretVar("env.GIGACHAT_USER"),
				Password: schemas.NewSecretVar("env.GIGACHAT_PASSWORD"),
			},
			wantAuth: true,
			wantTLS:  false,
			wantMTLS: false,
		},
		{
			name: "ClientCertificatePair",
			config: &schemas.GigaChatKeyConfig{
				CertFile: "/secure/client.pem",
				KeyFile:  "/secure/client.key",
			},
			wantAuth: false,
			wantTLS:  true,
			wantMTLS: true,
		},
		{
			name: "CABundle",
			config: &schemas.GigaChatKeyConfig{
				CABundleFile: "/secure/ca.pem",
			},
			wantAuth: false,
			wantTLS:  true,
			wantMTLS: false,
		},
		{
			name: "CredentialsWithTLS",
			config: &schemas.GigaChatKeyConfig{
				Credentials:  schemas.NewSecretVar("env.GIGACHAT_CREDENTIALS"),
				CertFile:     "/secure/client.pem",
				KeyFile:      "/secure/client.key",
				CABundleFile: "/secure/ca.pem",
			},
			wantAuth: true,
			wantTLS:  true,
			wantMTLS: true,
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
			if got := testCase.config.HasClientCertificateMaterial(); got != testCase.wantMTLS {
				t.Fatalf("HasClientCertificateMaterial mismatch: got %v, want %v", got, testCase.wantMTLS)
			}
		})
	}
}

func TestGigaChatKeyConfigValidate(t *testing.T) {
	t.Parallel()

	t.Run("DefaultsScope", func(t *testing.T) {
		t.Parallel()

		config := &schemas.GigaChatKeyConfig{
			Credentials: schemas.NewSecretVar("env.GIGACHAT_CREDENTIALS"),
		}
		if err := config.Validate(); err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if config.Scope != schemas.DefaultGigaChatScope {
			t.Fatalf("scope mismatch: got %q, want %q", config.Scope, schemas.DefaultGigaChatScope)
		}
	})

	t.Run("PartialUserPasswordRejected", func(t *testing.T) {
		t.Parallel()

		config := &schemas.GigaChatKeyConfig{
			User: schemas.NewSecretVar("env.GIGACHAT_USER"),
		}
		err := config.Validate()
		if err == nil {
			t.Fatal("expected validation error")
		}
		if !strings.Contains(err.Error(), "user and gigachat_key_config.password") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("PartialCertificatePairRejected", func(t *testing.T) {
		t.Parallel()

		config := &schemas.GigaChatKeyConfig{
			CertFile: "/secure/client.pem",
		}
		err := config.Validate()
		if err == nil {
			t.Fatal("expected validation error")
		}
		if !strings.Contains(err.Error(), "cert_file and gigachat_key_config.key_file") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
