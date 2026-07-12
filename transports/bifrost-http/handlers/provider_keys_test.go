package handlers

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// TestMergeUpdatedKey_Value locks in the invariant that a masked key preview can
// never be persisted as the real key value. The provider keys API renders keys
// redacted on GET; when a client echoes that placeholder back on update, the
// stored credential must be preserved. This is the write-side guard for
// issue #4353 (a masked "*"-laden preview leaking into the config store and
// later breaking JSON re-parsing on governance reload).
func TestMergeUpdatedKey_Value(t *testing.T) {
	h := &ProviderHandler{}
	merge := func(oldRaw, update schemas.Key) schemas.Key {
		t.Helper()
		merged, err := h.mergeUpdatedKey(oldRaw, update)
		if err != nil {
			t.Fatalf("mergeUpdatedKey returned error: %v", err)
		}
		return merged
	}

	const rawValue = "sk-realkey1234567890abcdefghij"

	newRaw := func() schemas.Key {
		return schemas.Key{ID: "key-1", Value: *schemas.NewSecretVar(rawValue)}
	}
	redactedOf := func(raw schemas.Key) schemas.Key {
		return schemas.Key{ID: "key-1", Value: *raw.Value.Redacted()}
	}

	t.Run("echoed current redaction preserves stored value", func(t *testing.T) {
		oldRaw := newRaw()
		oldRedacted := redactedOf(oldRaw)
		// Client sends back exactly what GET rendered.
		update := schemas.Key{ID: "key-1", Value: oldRedacted.Value}

		merged := merge(oldRaw, update)
		if merged.Value.GetValue() != rawValue {
			t.Fatalf("expected stored raw value preserved, got %q", merged.Value.GetValue())
		}
	})

	t.Run("mismatched mask still preserves stored value", func(t *testing.T) {
		// A redacted preview whose bytes differ from the server's current
		// redaction (e.g. a stale render, a different asterisk count, or a
		// preview from another replica). The old exact-match guard let this
		// through and persisted the mask; the fix must still preserve.
		oldRaw := newRaw()
		oldRedacted := redactedOf(oldRaw)
		mismatched := "diff" + strings.Repeat("*", 24) + "XYZW" // redacted-shaped, != oldRedacted
		if !schemas.NewSecretVar(mismatched).IsRedacted() {
			t.Fatalf("test setup: %q is not recognized as redacted", mismatched)
		}
		if mismatched == oldRedacted.Value.GetValue() {
			t.Fatalf("test setup: mismatched mask unexpectedly equals current redaction")
		}
		update := schemas.Key{ID: "key-1", Value: *schemas.NewSecretVar(mismatched)}

		merged := merge(oldRaw, update)
		if merged.Value.GetValue() != rawValue {
			t.Fatalf("masked preview must not be persisted; expected %q, got %q", rawValue, merged.Value.GetValue())
		}
		if strings.Contains(merged.Value.GetValue(), "*") {
			t.Fatalf("merged value still contains mask characters: %q", merged.Value.GetValue())
		}
	})

	t.Run("genuine new plaintext value is applied", func(t *testing.T) {
		oldRaw := newRaw()
		const newValue = "sk-brandnewkey0987654321zyxwvu"
		update := schemas.Key{ID: "key-1", Value: *schemas.NewSecretVar(newValue)}

		merged := merge(oldRaw, update)
		if merged.Value.GetValue() != newValue {
			t.Fatalf("expected new plaintext value applied, got %q", merged.Value.GetValue())
		}
	})

	t.Run("genuine env ref is applied not preserved", func(t *testing.T) {
		oldRaw := newRaw()
		// env refs report IsRedacted() but are an intentional change.
		update := schemas.Key{ID: "key-1", Value: *schemas.NewSecretVar("env.SOME_NEW_KEY")}

		merged := merge(oldRaw, update)
		if !merged.Value.IsFromEnv() || merged.Value.GetRawRef() != "env.SOME_NEW_KEY" {
			t.Fatalf("expected env ref applied, got ref=%q fromEnv=%v", merged.Value.GetRawRef(), merged.Value.IsFromEnv())
		}
		if merged.Value.GetValue() == rawValue {
			t.Fatalf("stored raw value leaked into an env-ref update")
		}
	})

	t.Run("empty value is not treated as redacted", func(t *testing.T) {
		// Empty non-secret values must stay empty so the downstream
		// "must not be empty" validation still fires. The merge must not
		// silently resurrect the stored value here.
		oldRaw := newRaw()
		update := schemas.Key{ID: "key-1", Value: *schemas.NewSecretVar("")}

		merged := merge(oldRaw, update)
		if merged.Value.GetValue() != "" {
			t.Fatalf("expected empty value preserved for validation, got %q", merged.Value.GetValue())
		}
	})
}

func TestMergeUpdatedKey_ProviderConfigMaskedPreviews(t *testing.T) {
	h := &ProviderHandler{}
	merge := func(oldRaw, update schemas.Key) schemas.Key {
		t.Helper()
		merged, err := h.mergeUpdatedKey(oldRaw, update)
		if err != nil {
			t.Fatalf("mergeUpdatedKey returned error: %v", err)
		}
		return merged
	}
	secret := func(value string) schemas.SecretVar { return *schemas.NewSecretVar(value) }
	secretPtr := func(value string) *schemas.SecretVar { return schemas.NewSecretVar(value) }
	staleMaskValue := func(prefix, suffix string) string {
		return prefix + strings.Repeat("*", 24) + suffix
	}
	staleMask := func(prefix, suffix string) schemas.SecretVar {
		return secret(staleMaskValue(prefix, suffix))
	}

	oldRaw := schemas.Key{
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:     secret("https://current.azure.example.com"),
			ClientSecret: secretPtr("azure-client-secret-current"),
		},
		VertexKeyConfig: &schemas.VertexKeyConfig{
			AuthCredentials: secret("vertex-auth-credentials-current"),
		},
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			AccessKey:    secret("bedrock-access-key-current"),
			SessionToken: secretPtr("bedrock-session-token-current"),
		},
		BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{
			SecretKey: secret("mantle-secret-key-current"),
		},
		VLLMKeyConfig:   &schemas.VLLMKeyConfig{URL: secret("https://current.vllm.example.com")},
		OllamaKeyConfig: &schemas.OllamaKeyConfig{URL: secret("https://current.ollama.example.com")},
		SGLKeyConfig:    &schemas.SGLKeyConfig{URL: secret("https://current.sgl.example.com")},
	}
	update := schemas.Key{
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:     staleMask("azur", "0001"),
			ClientSecret: secretPtr(staleMaskValue("azcs", "0002")),
		},
		VertexKeyConfig: &schemas.VertexKeyConfig{
			AuthCredentials: staleMask("vert", "0003"),
		},
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			AccessKey:    staleMask("beda", "0004"),
			SessionToken: secretPtr(staleMaskValue("beds", "0005")),
		},
		BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{
			SecretKey: staleMask("mant", "0006"),
		},
		VLLMKeyConfig:   &schemas.VLLMKeyConfig{URL: staleMask("vllm", "0007")},
		OllamaKeyConfig: &schemas.OllamaKeyConfig{URL: staleMask("olla", "0008")},
		SGLKeyConfig:    &schemas.SGLKeyConfig{URL: staleMask("sgla", "0009")},
	}

	merged := merge(oldRaw, update)
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"azure endpoint", merged.AzureKeyConfig.Endpoint.GetValue(), oldRaw.AzureKeyConfig.Endpoint.GetValue()},
		{"azure client secret", merged.AzureKeyConfig.ClientSecret.GetValue(), oldRaw.AzureKeyConfig.ClientSecret.GetValue()},
		{"vertex credentials", merged.VertexKeyConfig.AuthCredentials.GetValue(), oldRaw.VertexKeyConfig.AuthCredentials.GetValue()},
		{"bedrock access key", merged.BedrockKeyConfig.AccessKey.GetValue(), oldRaw.BedrockKeyConfig.AccessKey.GetValue()},
		{"bedrock session token", merged.BedrockKeyConfig.SessionToken.GetValue(), oldRaw.BedrockKeyConfig.SessionToken.GetValue()},
		{"mantle secret key", merged.BedrockMantleKeyConfig.SecretKey.GetValue(), oldRaw.BedrockMantleKeyConfig.SecretKey.GetValue()},
		{"vllm url", merged.VLLMKeyConfig.URL.GetValue(), oldRaw.VLLMKeyConfig.URL.GetValue()},
		{"ollama url", merged.OllamaKeyConfig.URL.GetValue(), oldRaw.OllamaKeyConfig.URL.GetValue()},
		{"sgl url", merged.SGLKeyConfig.URL.GetValue(), oldRaw.SGLKeyConfig.URL.GetValue()},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s: expected stored value %q, got %q", check.name, check.want, check.got)
		}
	}

	update.VLLMKeyConfig.URL = secret("env.NEW_VLLM_URL")
	merged = merge(oldRaw, update)
	if !merged.VLLMKeyConfig.URL.IsFromEnv() || merged.VLLMKeyConfig.URL.GetRawRef() != "env.NEW_VLLM_URL" {
		t.Fatalf("expected nested env ref applied, got ref=%q", merged.VLLMKeyConfig.URL.GetRawRef())
	}
}

func TestMergeUpdatedKey_RejectsMaskWithoutStoredCounterpart(t *testing.T) {
	h := &ProviderHandler{}
	mask := *schemas.NewSecretVar("abcd" + strings.Repeat("*", 24) + "wxyz")

	tests := []struct {
		name    string
		oldRaw  schemas.Key
		update  schemas.Key
		wantErr string
	}{
		{
			name:    "missing config section",
			oldRaw:  schemas.Key{},
			update:  schemas.Key{VLLMKeyConfig: &schemas.VLLMKeyConfig{URL: mask}},
			wantErr: "vllm_key_config.url",
		},
		{
			name: "missing optional field",
			oldRaw: schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewSecretVar("stored-access-key"),
			}},
			update: schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{
				SessionToken: schemas.NewSecretVar(mask.GetValue()),
			}},
			wantErr: "bedrock_key_config.session_token",
		},
		{
			name: "empty stored value",
			oldRaw: schemas.Key{VLLMKeyConfig: &schemas.VLLMKeyConfig{
				URL: *schemas.NewSecretVar(""),
			}},
			update:  schemas.Key{VLLMKeyConfig: &schemas.VLLMKeyConfig{URL: mask}},
			wantErr: "vllm_key_config.url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.mergeUpdatedKey(tt.oldRaw, tt.update)
			if err == nil {
				t.Fatal("expected masked preview without stored counterpart to fail")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error to name %q, got %q", tt.wantErr, err)
			}
		})
	}
}

// validateProviderKeyURL must enforce every nested field config.schema.json
// marks as required, so neither create nor a masked-update merge can persist a
// key missing them.
func TestValidateProviderKeyRequiredNestedFields(t *testing.T) {
	region := schemas.NewSecretVar("us-east-1")
	cases := []struct {
		name     string
		provider schemas.ModelProvider
		key      schemas.Key
		wantErr  string
	}{
		{"azure missing endpoint", schemas.Azure, schemas.Key{AzureKeyConfig: &schemas.AzureKeyConfig{}}, "azure_key_config.endpoint"},
		{"azure nil config", schemas.Azure, schemas.Key{}, "azure_key_config.endpoint"},
		{"azure ok", schemas.Azure, schemas.Key{AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar("https://x.openai.azure.com")}}, ""},
		{"bedrock missing region", schemas.Bedrock, schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{}}, "bedrock_key_config.region"},
		{"bedrock ok", schemas.Bedrock, schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{Region: region}}, ""},
		{"mantle missing region", schemas.BedrockMantle, schemas.Key{BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{}}, "bedrock_mantle_key_config.region"},
		{"mantle ok", schemas.BedrockMantle, schemas.Key{BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{Region: region}}, ""},
		{"vllm missing url", schemas.VLLM, schemas.Key{VLLMKeyConfig: &schemas.VLLMKeyConfig{ModelName: "m"}}, "vllm_key_config.url"},
		{"vllm missing model_name", schemas.VLLM, schemas.Key{VLLMKeyConfig: &schemas.VLLMKeyConfig{URL: *schemas.NewSecretVar("http://vllm:8000")}}, "vllm_key_config.model_name"},
		{"vllm ok", schemas.VLLM, schemas.Key{VLLMKeyConfig: &schemas.VLLMKeyConfig{URL: *schemas.NewSecretVar("http://vllm:8000"), ModelName: "m"}}, ""},
		{"openai unaffected", schemas.OpenAI, schemas.Key{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProviderKeyURL(tc.provider, tc.key)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// Regression for the custom-provider path: required-field validation must run
// against the resolved BASE provider, not the custom route name, or a custom
// provider based on Bedrock would skip the region requirement entirely.
func TestCreateProviderKey_CustomBedrockRequiresRegion(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	h := &ProviderHandler{
		inMemoryStore: &lib.Config{
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"aws-custom": {
					CustomProviderConfig: &schemas.CustomProviderConfig{
						BaseProviderType: schemas.Bedrock,
					},
				},
			},
		},
		modelsManager: &mockModelsManager{},
	}

	ctx := newTestRequestCtx(`{"value":"AKIAEXAMPLEKEY","weight":1.0,"bedrock_key_config":{}}`)
	ctx.SetUserValue("provider", "aws-custom")

	h.createProviderKey(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("custom-bedrock create without region: status got %d, want 400; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if body := string(ctx.Response.Body()); !strings.Contains(body, "bedrock_key_config.region") {
		t.Fatalf("expected bedrock_key_config.region error, got %s", body)
	}
}
