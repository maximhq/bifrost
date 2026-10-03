package handlers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

func TestProviderKeyReadRedactsNestedSecrets(t *testing.T) {
	const canary = "synthetic-provider-key-secret-canary-0123456789"
	const ref = "env.BIFROST_PROVIDER_KEY_REDACTION_TEST"
	t.Setenv("BIFROST_PROVIDER_KEY_REDACTION_TEST", canary)
	secret := func() *schemas.SecretVar { return schemas.NewSecretVar(ref) }
	for _, provider := range []schemas.ModelProvider{schemas.Bedrock, schemas.BedrockMantle} {
		t.Run(string(provider), func(t *testing.T) {
			key := schemas.Key{
				ID: "redaction-key", Name: "redaction-key", Value: *secret(),
				Aliases: schemas.KeyAliases{"test": {ModelID: "model", Region: secret(), ProjectID: secret()}},
			}
			if provider == schemas.Bedrock {
				key.BedrockKeyConfig = &schemas.BedrockKeyConfig{
					ProjectID: secret(), Endpoints: &schemas.BedrockEndpoints{Runtime: secret()},
				}
			} else {
				key.BedrockMantleKeyConfig = &schemas.BedrockMantleKeyConfig{
					ProjectID: secret(), Endpoints: &schemas.BedrockEndpoints{Runtime: secret()},
				}
			}
			h := &ProviderHandler{inMemoryStore: &lib.Config{
				Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
					provider: {Keys: []schemas.Key{key}},
				},
			}}
			for name, handler := range map[string]func(*fasthttp.RequestCtx){
				"get": h.getProviderKey, "list": h.listProviderKeys,
			} {
				t.Run(name, func(t *testing.T) {
					ctx := newTestRequestCtx("")
					ctx.SetUserValue("provider", string(provider))
					ctx.SetUserValue("key_id", key.ID)
					handler(ctx)
					body := string(ctx.Response.Body())
					if ctx.Response.StatusCode() != fasthttp.StatusOK {
						t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), body)
					}
					if strings.Contains(body, canary) {
						t.Fatalf("resolved secret leaked: %s", body)
					}
					// All five populated fields (including the credential control)
					// must be present, resolved, masked, and retain their references.
					if strings.Count(body, ref) != 5 || strings.Count(body, secret().Redacted().GetValue()) != 5 {
						t.Fatalf("missing masked values or references: %s", body)
					}
					if key.Aliases["test"].Region.GetValue() != canary {
						t.Fatal("reading a key modified the live secret")
					}
				})
			}
		})
	}
}

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

// TestMergeUpdatedKey_Name locks in the invariant that a PUT update omitting
// name must not clear the stored one. config_keys.name carries a global unique
// index, so silently clearing it lets the first such update claim "" and wedge
// every later name-omitting update behind a 409 (observed in a downstream
// production deployment: a single edit that omitted name broke every
// subsequent key edit going through this path).
func TestMergeUpdatedKey_Name(t *testing.T) {
	h := &ProviderHandler{}
	merge := func(oldRaw, update schemas.Key) schemas.Key {
		t.Helper()
		merged, err := h.mergeUpdatedKey(oldRaw, update)
		if err != nil {
			t.Fatalf("mergeUpdatedKey returned error: %v", err)
		}
		return merged
	}

	t.Run("name omitted from update preserves stored name", func(t *testing.T) {
		oldRaw := schemas.Key{ID: "key-1", Name: "prod-openai-key", Value: *schemas.NewSecretVar("sk-realkey1234567890abcdefghij")}
		update := schemas.Key{ID: "key-1", Value: oldRaw.Value} // client PUT body has no "name" field

		merged := merge(oldRaw, update)
		if merged.Name != "prod-openai-key" {
			t.Fatalf("expected stored name preserved, got %q", merged.Name)
		}
	})

	t.Run("explicit new name is applied", func(t *testing.T) {
		oldRaw := schemas.Key{ID: "key-1", Name: "prod-openai-key", Value: *schemas.NewSecretVar("sk-realkey1234567890abcdefghij")}
		update := schemas.Key{ID: "key-1", Name: "renamed-key", Value: oldRaw.Value}

		merged := merge(oldRaw, update)
		if merged.Name != "renamed-key" {
			t.Fatalf("expected new name applied, got %q", merged.Name)
		}
	})
}

// TestDecodeKeyUpdate_PartialBody covers issue #7572: a PUT that names only
// some fields must leave the others as stored. Omitted fields used to decode to
// zero values, so a body carrying only models emptied the key's value and set
// its weight to 0, and the update still returned 200.
func TestDecodeKeyUpdate_PartialBody(t *testing.T) {
	h := &ProviderHandler{}
	const rawValue = "sk-sglrealkey1234567890abcdefghij"
	stored := func() schemas.Key {
		return schemas.Key{
			ID:                "key-1",
			Name:              "sgl-key",
			Value:             *schemas.NewSecretVar(rawValue),
			Models:            schemas.WhiteList{"old-model"},
			BlacklistedModels: schemas.BlackList{"blocked-model"},
			Weight:            1,
			SGLKeyConfig:      &schemas.SGLKeyConfig{URL: *schemas.NewSecretVar("http://old:30000")},
			Enabled:           schemas.Ptr(false),
			UseForBatchAPI:    schemas.Ptr(true),
			Description:       "primary",
		}
	}
	update := func(t *testing.T, body string) schemas.Key {
		t.Helper()
		oldRaw := stored()
		updateKey, err := decodeKeyUpdate([]byte(body), oldRaw)
		if err != nil {
			t.Fatalf("decodeKeyUpdate returned error: %v", err)
		}
		updateKey.ID = oldRaw.ID
		merged, err := h.mergeUpdatedKey(oldRaw, updateKey)
		if err != nil {
			t.Fatalf("mergeUpdatedKey returned error: %v", err)
		}
		return merged
	}

	t.Run("omitted fields keep their stored values", func(t *testing.T) {
		merged := update(t, `{"models":["m1","m2"],"sgl_key_config":{"url":"http://new:30000"}}`)
		if merged.Value.GetValue() != rawValue {
			t.Fatalf("value: got %q, want the stored value", merged.Value.GetValue())
		}
		if merged.Weight != 1 {
			t.Fatalf("weight: got %v, want 1", merged.Weight)
		}
		if len(merged.BlacklistedModels) != 1 || merged.BlacklistedModels[0] != "blocked-model" {
			t.Fatalf("blacklisted_models: got %v, want [blocked-model]", merged.BlacklistedModels)
		}
		if merged.Enabled == nil || *merged.Enabled {
			t.Fatalf("enabled: got %v, want the stored false", merged.Enabled)
		}
		if merged.UseForBatchAPI == nil || !*merged.UseForBatchAPI {
			t.Fatalf("use_for_batch_api: got %v, want the stored true", merged.UseForBatchAPI)
		}
		if merged.Description != "primary" {
			t.Fatalf("description: got %q, want primary", merged.Description)
		}
		// The fields that were sent still apply.
		if len(merged.Models) != 2 || merged.Models[0] != "m1" {
			t.Fatalf("models: got %v, want [m1 m2]", merged.Models)
		}
		if merged.SGLKeyConfig == nil || merged.SGLKeyConfig.URL.GetValue() != "http://new:30000" {
			t.Fatalf("sgl_key_config.url: got %+v, want http://new:30000", merged.SGLKeyConfig)
		}
	})

	t.Run("an omitted provider config is kept", func(t *testing.T) {
		merged := update(t, `{"weight":2}`)
		if merged.SGLKeyConfig == nil || merged.SGLKeyConfig.URL.GetValue() != "http://old:30000" {
			t.Fatalf("sgl_key_config: got %+v, want the stored config", merged.SGLKeyConfig)
		}
		if merged.Weight != 2 {
			t.Fatalf("weight: got %v, want 2", merged.Weight)
		}
	})

	t.Run("fields sent explicitly empty are cleared", func(t *testing.T) {
		merged := update(t, `{"value":"","weight":0,"models":[],"description":"","sgl_key_config":null}`)
		if merged.Value.GetValue() != "" {
			t.Fatalf("value: got %q, want it cleared", merged.Value.GetValue())
		}
		if merged.Weight != 0 {
			t.Fatalf("weight: got %v, want 0", merged.Weight)
		}
		if len(merged.Models) != 0 {
			t.Fatalf("models: got %v, want empty", merged.Models)
		}
		if merged.Description != "" {
			t.Fatalf("description: got %q, want it cleared", merged.Description)
		}
		if merged.SGLKeyConfig != nil {
			t.Fatalf("sgl_key_config: got %+v, want nil", merged.SGLKeyConfig)
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

// TestMergeUpdatedKey_GithubCopilotKeyConfig covers the masked-update path for the GitHub
// App bundle. The API renders keys redacted on GET, so a client that edits one field and
// submits the form sends back a mask for every field it did not touch. Getting the merge
// wrong here either persists the literal mask as a credential, or silently drops the four
// App fields, and both failures look like a working save.
func TestMergeUpdatedKey_GithubCopilotKeyConfig(t *testing.T) {
	h := &ProviderHandler{}
	secret := func(v string) schemas.SecretVar { return *schemas.NewSecretVar(v) }
	mask := func(prefix, suffix string) schemas.SecretVar {
		return secret(prefix + strings.Repeat("*", 24) + suffix)
	}

	stored := func() schemas.Key {
		return schemas.Key{GithubCopilotKeyConfig: &schemas.GithubCopilotKeyConfig{
			AppID:          secret("123456"),
			InstallationID: secret("87654321"),
			RepositoryID:   secret("999000111"),
			PrivateKey:     secret(pkcs1TestPEM(t)),
			GithubDomain:   secret("acme.ghe.com"),
		}}
	}

	t.Run("masks resolve back to the stored values", func(t *testing.T) {
		update := schemas.Key{GithubCopilotKeyConfig: &schemas.GithubCopilotKeyConfig{
			AppID:          mask("1234", "3456"),
			InstallationID: mask("8765", "4321"),
			RepositoryID:   mask("9990", "0111"),
			PrivateKey:     mask("----", "----"),
			GithubDomain:   mask("acme", ".com"),
		}}

		merged, err := h.mergeUpdatedKey(stored(), update)
		if err != nil {
			t.Fatalf("mergeUpdatedKey returned error: %v", err)
		}
		cfg := merged.GithubCopilotKeyConfig
		if cfg == nil {
			t.Fatal("the App config was dropped by the merge")
		}
		for _, c := range []struct{ name, got, want string }{
			{"app_id", cfg.AppID.GetValue(), "123456"},
			{"installation_id", cfg.InstallationID.GetValue(), "87654321"},
			{"repository_id", cfg.RepositoryID.GetValue(), "999000111"},
			{"private_key", cfg.PrivateKey.GetValue(), pkcs1TestPEM(t)},
			{"github_domain", cfg.GithubDomain.GetValue(), "acme.ghe.com"},
		} {
			if c.got != c.want {
				t.Errorf("%s: got %q, want the stored value %q", c.name, c.got, c.want)
			}
			if strings.Contains(c.got, "****") {
				t.Errorf("%s: the mask was persisted as a credential", c.name)
			}
		}
	})

	t.Run("a mask with no stored counterpart is rejected", func(t *testing.T) {
		// Nothing to restore from, so accepting it would write asterisks as the App ID.
		update := schemas.Key{GithubCopilotKeyConfig: &schemas.GithubCopilotKeyConfig{
			AppID: mask("1234", "3456"),
		}}

		if _, err := h.mergeUpdatedKey(schemas.Key{}, update); err == nil {
			t.Fatal("expected an error when a mask has no stored value behind it")
		}
	})

	t.Run("unmasked literals overwrite the stored values", func(t *testing.T) {
		update := schemas.Key{GithubCopilotKeyConfig: &schemas.GithubCopilotKeyConfig{
			AppID:          secret("Iv1.b507a08c87ecfe98"),
			InstallationID: secret("11112222"),
			RepositoryID:   secret("333344445"),
			PrivateKey:     secret(pkcs8TestPEM(t)),
			GithubDomain:   secret(""),
		}}

		merged, err := h.mergeUpdatedKey(stored(), update)
		if err != nil {
			t.Fatalf("mergeUpdatedKey returned error: %v", err)
		}
		cfg := merged.GithubCopilotKeyConfig
		if cfg.AppID.GetValue() != "Iv1.b507a08c87ecfe98" {
			t.Errorf("app_id: got %q, want the new literal", cfg.AppID.GetValue())
		}
		if cfg.InstallationID.GetValue() != "11112222" {
			t.Errorf("installation_id: got %q, want the new literal", cfg.InstallationID.GetValue())
		}
		if cfg.PrivateKey.GetValue() != pkcs8TestPEM(t) {
			t.Error("private_key: the rotated key did not take effect")
		}
	})
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

// TestValidateProviderKeyGithubCopilotFormats pins that a literal GitHub App credential is
// checked for shape at save time, not just for presence. A non-numeric installation_id or a
// mangled PEM otherwise persists happily and fails on the first inference call, where it
// reads as a runtime fault rather than a typo in a form.
//
// Environment and vault references are exempt: their values are not available here, so
// checking them would reject every legitimate headless configuration.
func TestValidateProviderKeyGithubCopilotFormats(t *testing.T) {
	pemBody := pkcs1TestPEM(t)

	appConfig := func(mutate func(*schemas.GithubCopilotKeyConfig)) *schemas.GithubCopilotKeyConfig {
		c := &schemas.GithubCopilotKeyConfig{
			AppID:          *schemas.NewSecretVar("123456"),
			InstallationID: *schemas.NewSecretVar("87654321"),
			RepositoryID:   *schemas.NewSecretVar("999000111"),
			PrivateKey:     *schemas.NewSecretVar(pemBody),
		}
		if mutate != nil {
			mutate(c)
		}
		return c
	}

	cases := []struct {
		name    string
		key     schemas.Key
		wantErr string
	}{
		{"valid literal app config", schemas.Key{GithubCopilotKeyConfig: appConfig(nil)}, ""},
		{"direct token needs no app config", schemas.Key{Value: *schemas.NewSecretVar("tid=abc")}, ""},
		{
			"non-numeric installation_id",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.InstallationID = *schemas.NewSecretVar("my-install")
			})},
			"installation_id",
		},
		{
			"path traversal in installation_id",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.InstallationID = *schemas.NewSecretVar("1/../../../user")
			})},
			"installation_id",
		},
		{
			"non-numeric repository_id",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.RepositoryID = *schemas.NewSecretVar("my-repo")
			})},
			"repository_id",
		},
		{
			"private key that is not PEM",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.PrivateKey = *schemas.NewSecretVar("not a key")
			})},
			"private_key",
		},
		{
			"valid PKCS#8 private key",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.PrivateKey = *schemas.NewSecretVar(pkcs8TestPEM(t))
			})},
			"",
		},
		{
			// An EC key is a perfectly well-formed PEM, so an envelope-only check waves it
			// through. GitHub App JWTs are RS256, so it fails at the first request instead.
			"EC private key",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.PrivateKey = *schemas.NewSecretVar(ecTestPEM(t))
			})},
			"private_key",
		},
		{
			// Passphrase-protected keys cannot be used unattended, and the envelope alone
			// does not say so.
			"encrypted private key envelope",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.PrivateKey = *schemas.NewSecretVar("-----BEGIN ENCRYPTED PRIVATE KEY-----\nZm9v\n-----END ENCRYPTED PRIVATE KEY-----")
			})},
			"private_key",
		},
		{
			"well-formed envelope with a garbage DER payload",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.PrivateKey = *schemas.NewSecretVar("-----BEGIN RSA PRIVATE KEY-----\nZm9vYmFy\n-----END RSA PRIVATE KEY-----")
			})},
			"private_key",
		},
		{
			"PEM whose newlines survived as literal backslash-n",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.PrivateKey = *schemas.NewSecretVar(strings.ReplaceAll(pkcs1TestPEM(t), "\n", `\n`))
			})},
			"",
		},
		{
			"no token and no app config at all",
			schemas.Key{},
			"github_copilot_key_config is required",
		},
		{
			"no token, missing app_id",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.AppID = schemas.SecretVar{}
			})},
			"github_copilot_key_config.app_id is required",
		},
		{
			"no token, missing installation_id",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.InstallationID = schemas.SecretVar{}
			})},
			"github_copilot_key_config.installation_id is required",
		},
		{
			"no token, missing repository_id",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.RepositoryID = schemas.SecretVar{}
			})},
			"github_copilot_key_config.repository_id is required",
		},
		{
			"no token, missing private_key",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.PrivateKey = schemas.SecretVar{}
			})},
			"github_copilot_key_config.private_key is required",
		},
		{
			// A token does not excuse a half-filled App block: it persists silently and only
			// breaks later, when the token expires or is removed.
			"direct token alongside an incomplete app config",
			schemas.Key{
				Value: *schemas.NewSecretVar("tid=abc"),
				GithubCopilotKeyConfig: &schemas.GithubCopilotKeyConfig{
					AppID: *schemas.NewSecretVar("123456"),
				},
			},
			"installation_id",
		},
		{
			"direct token alongside a malformed app config",
			schemas.Key{
				Value: *schemas.NewSecretVar("tid=abc"),
				GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
					c.RepositoryID = *schemas.NewSecretVar("not-a-number")
				}),
			},
			"repository_id",
		},
		{
			"direct token alongside a complete app config",
			schemas.Key{Value: *schemas.NewSecretVar("tid=abc"), GithubCopilotKeyConfig: appConfig(nil)},
			"",
		},
		{
			// The key form always sends the App block, so a token key arrives with an empty one.
			"direct token alongside an empty app config",
			schemas.Key{Value: *schemas.NewSecretVar("tid=abc"), GithubCopilotKeyConfig: &schemas.GithubCopilotKeyConfig{}},
			"",
		},
		{
			"direct token alongside an app config with only github_domain",
			schemas.Key{
				Value: *schemas.NewSecretVar("tid=abc"),
				GithubCopilotKeyConfig: &schemas.GithubCopilotKeyConfig{
					GithubDomain: *schemas.NewSecretVar("acme.ghe.com"),
				},
			},
			"",
		},
		{
			"no token and an empty app config",
			schemas.Key{GithubCopilotKeyConfig: &schemas.GithubCopilotKeyConfig{}},
			"github_copilot_key_config is required",
		},
		{
			"whitespace-only token and no app config",
			schemas.Key{Value: *schemas.NewSecretVar("   ")},
			"github_copilot_key_config is required",
		},
		{
			"whitespace-only app_id",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.AppID = *schemas.NewSecretVar("  ")
			})},
			"github_copilot_key_config.app_id is required",
		},
		{
			"installation_id longer than the provider accepts",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.InstallationID = *schemas.NewSecretVar("123456789012345678901")
			})},
			"installation_id",
		},
		{
			"repository_id of zero",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.RepositoryID = *schemas.NewSecretVar("0")
			})},
			"repository_id",
		},
		{
			"repository_id above the int64 range",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.RepositoryID = *schemas.NewSecretVar("9223372036854775808")
			})},
			"repository_id",
		},
		{
			// GitHub documents the JWT issuer as "the client ID or application ID", and says
			// "use of the client ID is recommended". Client IDs look like Iv1.b507a08c87ecfe98,
			// so a digits-only rule on app_id would reject the recommended configuration.
			// https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app
			"client ID as app_id is accepted",
			schemas.Key{GithubCopilotKeyConfig: appConfig(func(c *schemas.GithubCopilotKeyConfig) {
				c.AppID = *schemas.NewSecretVar("Iv1.b507a08c87ecfe98")
			})},
			"",
		},
		{
			"env references are not format-checked",
			schemas.Key{GithubCopilotKeyConfig: &schemas.GithubCopilotKeyConfig{
				AppID:          *schemas.NewSecretVar("env.COPILOT_APP_ID"),
				InstallationID: *schemas.NewSecretVar("env.COPILOT_INSTALLATION_ID"),
				RepositoryID:   *schemas.NewSecretVar("env.COPILOT_REPOSITORY_ID"),
				PrivateKey:     *schemas.NewSecretVar("env.COPILOT_PRIVATE_KEY"),
			}},
			"",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProviderKeyURL(schemas.GithubCopilot, tc.key)
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

// refreshHandlerForTest builds a ProviderHandler with one keyed provider and a
// recording models manager.
func refreshHandlerForTest(mgr *mockModelsManager) *ProviderHandler {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	return &ProviderHandler{
		inMemoryStore: &lib.Config{
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				"openai": {Keys: []schemas.Key{{ID: "key-1"}}},
			},
		},
		modelsManager: mgr,
	}
}

func TestRefreshProviderModels_DelegatesToModelsManager(t *testing.T) {
	mgr := &mockModelsManager{}
	h := refreshHandlerForTest(mgr)

	ctx := newTestRequestCtx("")
	ctx.SetUserValue("provider", "openai")
	h.refreshProviderModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("status got %d, want 200; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if len(mgr.refreshProviderCalls) != 1 || mgr.refreshProviderCalls[0] != "openai" {
		t.Fatalf("expected one provider-level refresh for openai, got %v", mgr.refreshProviderCalls)
	}
}

func TestRefreshProviderKeyModels_DelegatesToModelsManager(t *testing.T) {
	mgr := &mockModelsManager{}
	h := refreshHandlerForTest(mgr)

	ctx := newTestRequestCtx("")
	ctx.SetUserValue("provider", "openai")
	ctx.SetUserValue("key_id", "key-1")
	h.refreshProviderKeyModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("status got %d, want 200; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if len(mgr.refreshKeyCalls) != 1 || mgr.refreshKeyCalls[0].keyID != "key-1" {
		t.Fatalf("expected one refresh for key-1, got %v", mgr.refreshKeyCalls)
	}
}

// A refresh already running for the provider must surface as 409 rather than
// stacking another (enabled keys x 2) burst of upstream calls, so the UI can
// tell the user to wait instead of silently doubling the load.
func TestRefreshProviderModels_InFlightReturns409(t *testing.T) {
	mgr := &mockModelsManager{refreshErr: ErrRefreshInProgress}
	h := refreshHandlerForTest(mgr)

	ctx := newTestRequestCtx("")
	ctx.SetUserValue("provider", "openai")
	h.refreshProviderModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusConflict {
		t.Fatalf("status got %d, want 409; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
}

func TestRefreshProviderKeyModels_UnknownKeyReturns404(t *testing.T) {
	mgr := &mockModelsManager{}
	h := refreshHandlerForTest(mgr)

	ctx := newTestRequestCtx("")
	ctx.SetUserValue("provider", "openai")
	ctx.SetUserValue("key_id", "does-not-exist")
	h.refreshProviderKeyModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusNotFound {
		t.Fatalf("status got %d, want 404; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if len(mgr.refreshKeyCalls) != 0 {
		t.Fatalf("expected no upstream refresh for an unknown key, got %v", mgr.refreshKeyCalls)
	}
}

func TestRefreshProviderModels_UnknownProviderReturns404(t *testing.T) {
	mgr := &mockModelsManager{}
	h := refreshHandlerForTest(mgr)

	ctx := newTestRequestCtx("")
	ctx.SetUserValue("provider", "does-not-exist")
	h.refreshProviderModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusNotFound {
		t.Fatalf("status got %d, want 404; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if len(mgr.refreshProviderCalls) != 0 {
		t.Fatalf("expected no upstream refresh for an unknown provider, got %v", mgr.refreshProviderCalls)
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

// TestUpdateProviderKey_EndpointGuardWhenAuthBypassed pins that the fail-open bypass only
// blocks a key update that moves the dial destination. Keeping the stored endpoint and
// editing an unrelated field (weight) must still go through, or the auth-disabled UI
// cannot edit any existing Ollama/SGL/VLLM/Azure key.
func TestUpdateProviderKey_EndpointGuardWhenAuthBypassed(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	const storedURL = "http://localhost:11434"
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantURL    string
		wantWeight float64
	}{
		{
			name:       "unchanged endpoint, weight edit",
			body:       `{"weight":2,"ollama_key_config":{"url":"` + storedURL + `"}}`,
			wantStatus: fasthttp.StatusOK,
			wantURL:    storedURL,
			wantWeight: 2,
		},
		{
			name:       "changed endpoint",
			body:       `{"weight":1,"ollama_key_config":{"url":"http://169.254.169.254/"}}`,
			wantStatus: fasthttp.StatusForbidden,
			wantURL:    storedURL,
			wantWeight: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &ProviderHandler{
				inMemoryStore: &lib.Config{
					Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
						schemas.Ollama: {Keys: []schemas.Key{{
							ID:              "key-1",
							Name:            "ollama-key",
							Weight:          1,
							OllamaKeyConfig: &schemas.OllamaKeyConfig{URL: *schemas.NewSecretVar(storedURL)},
						}}},
					},
				},
				modelsManager: &mockModelsManager{},
			}
			attachBifrostClient(t, h.inMemoryStore)

			ctx := newTestRequestCtx(tc.body)
			ctx.SetUserValue("provider", string(schemas.Ollama))
			ctx.SetUserValue("key_id", "key-1")
			ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)

			h.updateProviderKey(ctx)

			if ctx.Response.StatusCode() != tc.wantStatus {
				t.Fatalf("status got %d, want %d; body=%s", ctx.Response.StatusCode(), tc.wantStatus, ctx.Response.Body())
			}
			stored := h.inMemoryStore.Providers[schemas.Ollama].Keys[0]
			if got := stored.OllamaKeyConfig.URL.GetValue(); got != tc.wantURL {
				t.Fatalf("stored url got %q, want %q", got, tc.wantURL)
			}
			if stored.Weight != tc.wantWeight {
				t.Fatalf("stored weight got %v, want %v", stored.Weight, tc.wantWeight)
			}
		})
	}
}

// TestProviderKeyEndpointGuard_CoversEveryDialTarget pins that the fail-open bypass guard
// keys off the fields Bifrost dials, not a provider-type list. Each of these fields sends the
// key's credential to the configured host, so a bypassed caller must not be able to set or
// move any of them. A Bedrock key with only a region carries no caller-chosen host and must
// still be creatable, which a provider-type guard would wrongly reject.
func TestProviderKeyEndpointGuard_CoversEveryDialTarget(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	copilotPEM := pkcs1TestPEM(t)
	copilotConfig := func(domain string) string {
		return `{"app_id":"123","installation_id":"456","repository_id":"789","private_key":` + strconv.Quote(copilotPEM) + `,"github_domain":"` + domain + `"}`
	}

	cases := []struct {
		name     string
		provider schemas.ModelProvider
		stored   *schemas.Key // nil means the request creates the key
		body     string
		want403  bool
	}{
		{
			name:     "create bedrock key with region only",
			provider: schemas.Bedrock,
			body:     `{"name":"bedrock-key","weight":1,"bedrock_key_config":{"access_key":"AKIAEXAMPLE","secret_key":"secret","region":"us-east-1"}}`,
			want403:  false,
		},
		{
			name:     "create bedrock key with runtime endpoint override",
			provider: schemas.Bedrock,
			body:     `{"name":"bedrock-key","weight":1,"bedrock_key_config":{"access_key":"AKIAEXAMPLE","secret_key":"secret","region":"us-east-1","endpoints":{"runtime":"evil.example.com"}}}`,
			want403:  true,
		},
		{
			name:     "update databricks workspace url",
			provider: schemas.Databricks,
			stored: &schemas.Key{
				ID: "key-1", Name: "databricks-key", Weight: 1,
				Value:               *schemas.NewSecretVar("dapi-token"),
				DatabricksKeyConfig: &schemas.DatabricksKeyConfig{WorkspaceURL: *schemas.NewSecretVar("https://adb-1.azuredatabricks.net")},
			},
			body:    `{"value":"dapi-token","weight":1,"databricks_key_config":{"workspace_url":"https://evil.example.com"}}`,
			want403: true,
		},
		{
			name:     "update bedrock mantle endpoint override",
			provider: schemas.BedrockMantle,
			stored: &schemas.Key{
				ID: "key-1", Name: "mantle-key", Weight: 1,
				BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{Region: schemas.NewSecretVar("us-east-1")},
			},
			body:    `{"weight":1,"bedrock_mantle_key_config":{"region":"us-east-1","endpoints":{"mantle":"evil.example.com"}}}`,
			want403: true,
		},
		{
			name:     "update github copilot github_domain",
			provider: schemas.GithubCopilot,
			stored: &schemas.Key{
				ID: "key-1", Name: "copilot-key", Weight: 1,
				Value: *schemas.NewSecretVar("ghu_copilot_token"),
				GithubCopilotKeyConfig: &schemas.GithubCopilotKeyConfig{
					AppID:          *schemas.NewSecretVar("123"),
					InstallationID: *schemas.NewSecretVar("456"),
					RepositoryID:   *schemas.NewSecretVar("789"),
					PrivateKey:     *schemas.NewSecretVar(copilotPEM),
					GithubDomain:   *schemas.NewSecretVar("acme.ghe.com"),
				},
			},
			body:    `{"value":"ghu_copilot_token","weight":1,"github_copilot_key_config":` + copilotConfig("evil.example.com") + `}`,
			want403: true,
		},
		{
			name:     "update azure key adding a per-alias endpoint",
			provider: schemas.Azure,
			stored: &schemas.Key{
				ID: "key-1", Name: "azure-key", Weight: 1,
				Value:          *schemas.NewSecretVar("azure-api-key"),
				AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar("https://myres.openai.azure.com")},
			},
			body:    `{"value":"azure-api-key","weight":1,"azure_key_config":{"endpoint":"https://myres.openai.azure.com"},"aliases":{"gpt-4o":{"model_id":"gpt-4o","endpoint":"https://evil.example.com"}}}`,
			want403: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			providerConfig := configstore.ProviderConfig{}
			if tc.stored != nil {
				providerConfig.Keys = []schemas.Key{*tc.stored}
			}
			h := &ProviderHandler{
				inMemoryStore: &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{tc.provider: providerConfig}},
				modelsManager: &mockModelsManager{},
			}
			attachBifrostClient(t, h.inMemoryStore)

			ctx := newTestRequestCtx(tc.body)
			ctx.SetUserValue("provider", string(tc.provider))
			ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
			if tc.stored != nil {
				ctx.SetUserValue("key_id", tc.stored.ID)
				h.updateProviderKey(ctx)
			} else {
				h.createProviderKey(ctx)
			}

			got403 := ctx.Response.StatusCode() == fasthttp.StatusForbidden
			if got403 != tc.want403 {
				t.Fatalf("got status %d, want403=%v; body=%s", ctx.Response.StatusCode(), tc.want403, ctx.Response.Body())
			}
			keys := h.inMemoryStore.Providers[tc.provider].Keys
			switch {
			case tc.want403 && tc.stored != nil:
				if len(keys) != 1 || !reflect.DeepEqual(keys[0], *tc.stored) {
					t.Fatalf("expected stored key unchanged after 403, got %+v", keys)
				}
			case tc.want403:
				if len(keys) != 0 {
					t.Fatalf("expected no key persisted after 403, got %d", len(keys))
				}
			default:
				if ctx.Response.StatusCode() != fasthttp.StatusOK || len(keys) != 1 {
					t.Fatalf("expected key persisted with 200, got status %d and %d keys; body=%s", ctx.Response.StatusCode(), len(keys), ctx.Response.Body())
				}
			}
		})
	}
}

// TestProviderKeyURL_RejectsLinkLocalDestination pins that a key-level server URL
// (Ollama/SGL/VLLM) is held to the same destination rule as a provider base URL, even for a
// genuinely authenticated admin: link-local and unspecified addresses are refused with 400,
// while loopback and private hosts - the documented self-hosted setup - stay allowed.
func TestProviderKeyURL_RejectsLinkLocalDestination(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	const storedURL = "http://localhost:11434"
	cases := []struct {
		name       string
		stored     bool
		body       string
		wantStatus int
	}{
		{name: "create with link-local url", body: `{"name":"ollama-key","weight":1,"ollama_key_config":{"url":"http://169.254.169.254/"}}`, wantStatus: fasthttp.StatusBadRequest},
		{name: "create with unspecified url", body: `{"name":"ollama-key","weight":1,"ollama_key_config":{"url":"http://0.0.0.0:11434"}}`, wantStatus: fasthttp.StatusBadRequest},
		{name: "create with private url", body: `{"name":"ollama-key","weight":1,"ollama_key_config":{"url":"http://10.0.0.5:11434"}}`, wantStatus: fasthttp.StatusOK},
		{name: "update to link-local url", stored: true, body: `{"weight":1,"ollama_key_config":{"url":"http://169.254.169.254/"}}`, wantStatus: fasthttp.StatusBadRequest},
		{name: "update to loopback url", stored: true, body: `{"weight":1,"ollama_key_config":{"url":"http://127.0.0.1:11435"}}`, wantStatus: fasthttp.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			providerConfig := configstore.ProviderConfig{}
			if tc.stored {
				providerConfig.Keys = []schemas.Key{{
					ID: "key-1", Name: "ollama-key", Weight: 1,
					OllamaKeyConfig: &schemas.OllamaKeyConfig{URL: *schemas.NewSecretVar(storedURL)},
				}}
			}
			h := &ProviderHandler{
				inMemoryStore: &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Ollama: providerConfig}},
				modelsManager: &mockModelsManager{},
			}
			attachBifrostClient(t, h.inMemoryStore)

			ctx := newTestRequestCtx(tc.body)
			ctx.SetUserValue("provider", string(schemas.Ollama))
			if tc.stored {
				ctx.SetUserValue("key_id", "key-1")
				h.updateProviderKey(ctx)
			} else {
				h.createProviderKey(ctx)
			}

			if ctx.Response.StatusCode() != tc.wantStatus {
				t.Fatalf("status got %d, want %d; body=%s", ctx.Response.StatusCode(), tc.wantStatus, ctx.Response.Body())
			}
			keys := h.inMemoryStore.Providers[schemas.Ollama].Keys
			switch {
			case tc.wantStatus != fasthttp.StatusOK && tc.stored:
				if len(keys) != 1 || keys[0].OllamaKeyConfig.URL.GetValue() != storedURL {
					t.Fatalf("expected stored url unchanged after 400, got %+v", keys)
				}
			case tc.wantStatus != fasthttp.StatusOK:
				if len(keys) != 0 {
					t.Fatalf("expected no key persisted after 400, got %d", len(keys))
				}
			default:
				if len(keys) != 1 {
					t.Fatalf("expected one key persisted, got %d", len(keys))
				}
			}
		})
	}
}

// TestProviderKeyRegion_RejectsHostShapedValues pins that a key-level or per-alias region can
// only name a vendor region. Region is interpolated into the provider host
// (<region>-aiplatform.googleapis.com, <service>.<region>.amazonaws.com), so a host-shaped value
// would carry the key's credential to a caller-chosen host while region stays outside the
// endpoint guard (a region-only Bedrock key must remain creatable without an admin session).
func TestProviderKeyRegion_RejectsHostShapedValues(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	cases := []struct {
		name       string
		provider   schemas.ModelProvider
		body       string
		wantStatus int
	}{
		{name: "bedrock valid region", provider: schemas.Bedrock, body: `{"name":"k","weight":1,"bedrock_key_config":{"access_key":"AKIAEXAMPLE","secret_key":"secret","region":"us-gov-west-1"}}`, wantStatus: fasthttp.StatusOK},
		{name: "bedrock host-shaped region", provider: schemas.Bedrock, body: `{"name":"k","weight":1,"bedrock_key_config":{"access_key":"AKIAEXAMPLE","secret_key":"secret","region":"evil.example/#"}}`, wantStatus: fasthttp.StatusBadRequest},
		{name: "bedrock region with userinfo", provider: schemas.Bedrock, body: `{"name":"k","weight":1,"bedrock_key_config":{"access_key":"AKIAEXAMPLE","secret_key":"secret","region":"us-east-1@evil.example"}}`, wantStatus: fasthttp.StatusBadRequest},
		{name: "bedrock alias host-shaped region", provider: schemas.Bedrock, body: `{"name":"k","weight":1,"bedrock_key_config":{"access_key":"AKIAEXAMPLE","secret_key":"secret","region":"us-east-1"},"aliases":{"claude":{"model_id":"anthropic.claude-3","region":"evil.example/#"}}}`, wantStatus: fasthttp.StatusBadRequest},
		{name: "bedrock alias valid region", provider: schemas.Bedrock, body: `{"name":"k","weight":1,"bedrock_key_config":{"access_key":"AKIAEXAMPLE","secret_key":"secret","region":"us-east-1"},"aliases":{"claude":{"model_id":"anthropic.claude-3","region":"eu-west-1"}}}`, wantStatus: fasthttp.StatusOK},
		{name: "vertex host-shaped region", provider: schemas.Vertex, body: `{"name":"k","weight":1,"vertex_key_config":{"project_id":"p","region":"attacker.example/x?","auth_credentials":"{}"}}`, wantStatus: fasthttp.StatusBadRequest},
		{name: "mantle host-shaped region", provider: schemas.BedrockMantle, body: `{"name":"k","weight":1,"bedrock_mantle_key_config":{"region":"evil.example/#"}}`, wantStatus: fasthttp.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &ProviderHandler{
				inMemoryStore: &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{tc.provider: {}}},
				modelsManager: &mockModelsManager{},
			}
			attachBifrostClient(t, h.inMemoryStore)
			ctx := newTestRequestCtx(tc.body)
			ctx.SetUserValue("provider", string(tc.provider))
			h.createProviderKey(ctx)
			if ctx.Response.StatusCode() != tc.wantStatus {
				t.Fatalf("status got %d, want %d; body=%s", ctx.Response.StatusCode(), tc.wantStatus, ctx.Response.Body())
			}
			if tc.wantStatus == fasthttp.StatusBadRequest {
				if !strings.Contains(string(ctx.Response.Body()), "region") {
					t.Fatalf("rejection must name the region field, got %s", ctx.Response.Body())
				}
				if len(h.inMemoryStore.Providers[tc.provider].Keys) != 0 {
					t.Fatal("a rejected key must not be persisted")
				}
			}
		})
	}
}

// One RSA key for the whole file. Generating them is slow enough to notice per case.
var (
	handlerKeyOnce sync.Once
	handlerRSAKey  *rsa.PrivateKey
)

func handlerRSA(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	handlerKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		handlerRSAKey = key
	})
	return handlerRSAKey
}

func pkcs1TestPEM(t *testing.T) string {
	t.Helper()
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(handlerRSA(t))}))
}

func pkcs8TestPEM(t *testing.T) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(handlerRSA(t))
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func ecTestPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ec: %v", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal ec: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

// TestMergeUpdatedKey_VertexAWSWorkloadIdentityRoleARN covers the one masked field inside the
// Vertex federation block. GET renders aws_role_arn as a mask, so an edit that leaves it alone
// sends the mask back; the merge must restore the stored ARN and must never persist the mask.
// The identifiers in the block (audience, service account, region) are returned in clear and
// therefore pass through untouched.
func TestMergeUpdatedKey_VertexAWSWorkloadIdentityRoleARN(t *testing.T) {
	h := &ProviderHandler{}
	secret := func(value string) schemas.SecretVar { return *schemas.NewSecretVar(value) }
	secretPtr := func(value string) *schemas.SecretVar { return schemas.NewSecretVar(value) }
	mask := "arn:" + strings.Repeat("*", 24) + "Hop0"

	stored := schemas.Key{
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID: secret("my-project"),
			Region:    secret("us-central1"),
			AWSWorkloadIdentity: &schemas.VertexAWSWorkloadIdentityConfig{
				Audience:            secret("//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/aws"),
				ServiceAccountEmail: secretPtr("vertex@my-project.iam.gserviceaccount.com"),
				AWSRoleARN:          secretPtr("arn:aws:iam::123456789012:role/VertexHop"),
			},
		},
	}
	update := schemas.Key{
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID: secret("my-project"),
			Region:    secret("us-central1"),
			AWSWorkloadIdentity: &schemas.VertexAWSWorkloadIdentityConfig{
				Audience:            secret("//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/aws"),
				ServiceAccountEmail: secretPtr("other@my-project.iam.gserviceaccount.com"),
				AWSRoleARN:          secretPtr(mask),
			},
		},
	}

	merged, err := h.mergeUpdatedKey(stored, update)
	if err != nil {
		t.Fatalf("mergeUpdatedKey returned error: %v", err)
	}
	if got := merged.VertexKeyConfig.AWSWorkloadIdentity.AWSRoleARN.GetValue(); got != "arn:aws:iam::123456789012:role/VertexHop" {
		t.Errorf("expected stored aws_role_arn to be preserved, got %q", got)
	}
	if got := merged.VertexKeyConfig.AWSWorkloadIdentity.ServiceAccountEmail.GetValue(); got != "other@my-project.iam.gserviceaccount.com" {
		t.Errorf("expected edited service_account_email to be applied, got %q", got)
	}

	// A mask with nothing stored behind it is a client bug and must be rejected, not persisted.
	// The first merge rewrote the update's pointer in place, so send a fresh mask.
	stored.VertexKeyConfig.AWSWorkloadIdentity = nil
	update.VertexKeyConfig.AWSWorkloadIdentity.AWSRoleARN = secretPtr(mask)
	if _, err := h.mergeUpdatedKey(stored, update); err == nil {
		t.Fatal("expected masked aws_role_arn without a stored value to be rejected")
	}

	// Omitting the role ARN entirely (direct access, no hop) must not trip the guard.
	update.VertexKeyConfig.AWSWorkloadIdentity.AWSRoleARN = nil
	if _, err := h.mergeUpdatedKey(stored, update); err != nil {
		t.Fatalf("expected federation block without aws_role_arn to merge cleanly, got: %v", err)
	}
}

// TestValidateVertexKeyAuth pins the API-side counterpart of the config.schema.json rule: a Vertex key
// must not carry both an aws_workload_identity block and a non-empty auth_credentials JSON. Without
// this check the API silently let federation take precedence over a credentials JSON the operator
// also supplied, while config.json rejected the same combination.
func TestValidateVertexKeyAuth(t *testing.T) {
	secret := func(v string) schemas.SecretVar { return *schemas.NewSecretVar(v) }
	wif := &schemas.VertexAWSWorkloadIdentityConfig{Audience: secret("//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/aws")}
	vertex := func(cfg *schemas.VertexKeyConfig) schemas.Key { return schemas.Key{VertexKeyConfig: cfg} }

	err := validateVertexKeyAuth(schemas.Vertex, vertex(&schemas.VertexKeyConfig{
		ProjectID: secret("p"), Region: secret("r"),
		AuthCredentials:     secret(`{"type":"service_account"}`),
		AWSWorkloadIdentity: wif,
	}))
	if err == nil {
		t.Fatal("expected auth_credentials alongside aws_workload_identity to be rejected")
	}
	for _, want := range []string{"auth_credentials", "aws_workload_identity"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err.Error(), want)
		}
	}

	// An env reference for the credentials is just as much a second identity.
	t.Setenv("TEST_VERTEX_CREDS", `{"type":"service_account"}`)
	if err := validateVertexKeyAuth(schemas.Vertex, vertex(&schemas.VertexKeyConfig{
		ProjectID: secret("p"), Region: secret("r"),
		AuthCredentials: secret("env.TEST_VERTEX_CREDS"), AWSWorkloadIdentity: wif,
	})); err == nil {
		t.Fatal("expected env-referenced auth_credentials alongside aws_workload_identity to be rejected")
	}

	// A present block without an audience is a broken federation config, not "no federation": the
	// configuration schema requires audience whenever the block exists, and so does the API.
	for name, block := range map[string]*schemas.VertexAWSWorkloadIdentityConfig{
		"empty block":    {},
		"email only":     {ServiceAccountEmail: schemas.NewSecretVar("sa@p.iam.gserviceaccount.com")},
		"empty audience": {Audience: secret("")},
	} {
		err := validateVertexKeyAuth(schemas.Vertex, vertex(&schemas.VertexKeyConfig{ProjectID: secret("p"), Region: secret("r"), AWSWorkloadIdentity: block}))
		if err == nil || !strings.Contains(err.Error(), "audience") {
			t.Errorf("%s: expected an audience error, got %v", name, err)
		}
	}
	// token_lifetime_seconds mirrors the configuration schema bounds; zero means "not set".
	for _, lifetime := range []int{599, 43201, -1} {
		err := validateVertexKeyAuth(schemas.Vertex, vertex(&schemas.VertexKeyConfig{ProjectID: secret("p"), Region: secret("r"),
			AWSWorkloadIdentity: &schemas.VertexAWSWorkloadIdentityConfig{Audience: wif.Audience, TokenLifetimeSeconds: lifetime}}))
		if err == nil || !strings.Contains(err.Error(), "token_lifetime_seconds") {
			t.Errorf("lifetime %d: expected a token_lifetime_seconds error, got %v", lifetime, err)
		}
	}

	accepted := []schemas.Key{
		vertex(&schemas.VertexKeyConfig{ProjectID: secret("p"), Region: secret("r"), AWSWorkloadIdentity: wif}),
		vertex(&schemas.VertexKeyConfig{ProjectID: secret("p"), Region: secret("r"), AuthCredentials: secret(""), AWSWorkloadIdentity: wif}),
		vertex(&schemas.VertexKeyConfig{ProjectID: secret("p"), Region: secret("r"), AuthCredentials: secret(`{"type":"service_account"}`)}),
		vertex(&schemas.VertexKeyConfig{ProjectID: secret("p"), Region: secret("r")}),
		vertex(&schemas.VertexKeyConfig{ProjectID: secret("p"), Region: secret("r"), AWSWorkloadIdentity: &schemas.VertexAWSWorkloadIdentityConfig{Audience: wif.Audience, TokenLifetimeSeconds: 600}}),
		vertex(&schemas.VertexKeyConfig{ProjectID: secret("p"), Region: secret("r"), AWSWorkloadIdentity: &schemas.VertexAWSWorkloadIdentityConfig{Audience: wif.Audience, TokenLifetimeSeconds: 43200}}),
		{Value: secret("api-key")},
	}
	for i, key := range accepted {
		if err := validateVertexKeyAuth(schemas.Vertex, key); err != nil {
			t.Errorf("accepted case %d rejected: %v", i, err)
		}
	}
	// Other providers never carry the block and are never touched.
	if err := validateVertexKeyAuth(schemas.OpenAI, vertex(&schemas.VertexKeyConfig{AuthCredentials: secret("x"), AWSWorkloadIdentity: wif})); err != nil {
		t.Errorf("non-vertex provider must not be validated: %v", err)
	}
}
