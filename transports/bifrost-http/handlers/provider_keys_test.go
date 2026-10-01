package handlers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"sync"
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

func TestMergeUpdatedKey_BedrockProfile(t *testing.T) {
	const profileName = "corporate-bedrock-profile-east"
	oldRaw := schemas.Key{
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Profile: schemas.NewSecretVar(profileName),
			Region:  schemas.NewSecretVar("us-east-1"),
		},
	}
	masked := oldRaw.BedrockKeyConfig.Profile.Redacted()
	if !masked.IsMaskedPlaceholder() {
		t.Fatal("test setup: profile preview is not a masked placeholder")
	}
	update := schemas.Key{
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Profile: masked,
			Region:  schemas.NewSecretVar("us-west-2"),
		},
	}

	merged, err := (&ProviderHandler{}).mergeUpdatedKey(oldRaw, update)
	if err != nil {
		t.Fatalf("mergeUpdatedKey returned error: %v", err)
	}
	if got := merged.BedrockKeyConfig.Profile.GetValue(); got != profileName {
		t.Fatalf("profile got %q, want stored %q", got, profileName)
	}
	if got := merged.BedrockKeyConfig.Region.GetValue(); got != "us-west-2" {
		t.Fatalf("region edit was lost: got %q", got)
	}
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

func TestValidateProviderKeyBedrockProfile(t *testing.T) {
	region := schemas.NewSecretVar("us-east-1")
	cases := []struct {
		name    string
		key     schemas.Key
		wantErr string
	}{
		{
			name: "profile only",
			key: schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{
				Profile: schemas.NewSecretVar("team-a"), Region: region,
			}},
		},
		{
			name: "profile with assume role",
			key: schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{
				Profile: schemas.NewSecretVar("team-a"), Region: region, RoleARN: schemas.NewSecretVar("arn:aws:iam::123456789012:role/Bedrock"),
			}},
		},
		{
			name: "profile through environment reference",
			key: schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{
				Profile: schemas.NewSecretVar("env.BIFROST_BEDROCK_PROFILE"), Region: region,
			}},
		},
		{
			name: "blank profile",
			key: schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{
				Profile: schemas.NewSecretVar(""), Region: region,
			}},
			wantErr: "bedrock_key_config.profile",
		},
		{
			name: "whitespace-only profile",
			key: schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{
				Profile: schemas.NewSecretVar("   "), Region: region,
			}},
			wantErr: "bedrock_key_config.profile",
		},
		{
			name: "profile with static credentials",
			key: schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{
				Profile: schemas.NewSecretVar("team-a"), Region: region,
				AccessKey: *schemas.NewSecretVar("AKIAEXAMPLE"), SecretKey: *schemas.NewSecretVar("secret"),
			}},
			wantErr: "bedrock_key_config.profile",
		},
		{
			name: "profile with explicit session token",
			key: schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{
				Profile: schemas.NewSecretVar("team-a"), Region: region, SessionToken: schemas.NewSecretVar("temporary-token"),
			}},
			wantErr: "bedrock_key_config.profile",
		},
		{
			name: "profile with Bedrock API key",
			key: schemas.Key{
				Value: *schemas.NewSecretVar("bedrock-api-key"),
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					Profile: schemas.NewSecretVar("team-a"), Region: region,
				},
			},
			wantErr: "bedrock_key_config.profile",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProviderKeyURL(schemas.Bedrock, tc.key)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid Bedrock key, got %v", err)
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
