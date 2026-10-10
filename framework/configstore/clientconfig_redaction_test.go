package configstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Nested identifiers accept secret references just like credential fields. They
// must retain literals and references without exposing resolved env/vault values
// or modifying the live configuration during redaction.
func TestProviderConfig_Redacted_NestedSecretVars(t *testing.T) {
	const canary = "synthetic-nested-secretvar-canary-0123456789"
	const envRef = "env.BIFROST_NESTED_REDACTION_TEST"
	const vaultRef = "vault.bifrost-test/nested-redaction"
	t.Setenv("BIFROST_NESTED_REDACTION_TEST", canary)
	previousHook := schemas.VaultResolveHook
	schemas.VaultResolveHook = func(_ context.Context, value *string) error {
		require.Equal(t, vaultRef, *value)
		*value = canary
		return nil
	}
	t.Cleanup(func() { schemas.VaultResolveHook = previousHook })

	for _, source := range []struct {
		name, input, ref string
	}{
		{"literal", canary, ""},
		{"env", envRef, envRef},
		{"vault", vaultRef, vaultRef},
	} {
		t.Run(source.name, func(t *testing.T) {
			secret := func() *schemas.SecretVar { return schemas.NewSecretVar(source.input) }
			endpoints := func() *schemas.BedrockEndpoints {
				return &schemas.BedrockEndpoints{
					Runtime: secret(), ControlPlane: secret(), Mantle: secret(), AgentRuntime: secret(), S3: secret(),
				}
			}
			config := ProviderConfig{Keys: []schemas.Key{{
				ID: "nested-key", Name: "nested-key",
				Aliases: schemas.KeyAliases{
					"rich": {
						ModelID: "model-id", Description: "alias metadata",
						Region: secret(), ProjectID: secret(),
						AzureAliasCfg:   &schemas.AzureAliasCfg{Endpoint: secret()},
						VertexAliasCfg:  &schemas.VertexAliasCfg{ProjectNumber: secret()},
						BedrockAliasCfg: &schemas.BedrockAliasCfg{InferenceProfileARN: secret()},
					},
					"legacy-project": {ModelID: "legacy-model", VertexAliasCfg: &schemas.VertexAliasCfg{ProjectID: secret()}},
					"simple":         {ModelID: "simple-model"},
				},
				BedrockKeyConfig:       &schemas.BedrockKeyConfig{ProjectID: secret(), Endpoints: endpoints()},
				BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{ProjectID: secret(), Endpoints: endpoints()},
			}}}
			fields := func(key schemas.Key) map[string]*schemas.SecretVar {
				alias := key.Aliases["rich"]
				result := map[string]*schemas.SecretVar{
					"alias.region": alias.Region, "alias.project_id": alias.ProjectID,
					"alias.endpoint": alias.AzureAliasCfg.Endpoint, "alias.project_number": alias.VertexAliasCfg.ProjectNumber,
					"alias.inference_profile_arn": alias.BedrockAliasCfg.InferenceProfileARN,
					"alias.legacy_project_id":     key.Aliases["legacy-project"].VertexAliasCfg.ProjectID,
					"bedrock.project_id":          key.BedrockKeyConfig.ProjectID,
					"mantle.project_id":           key.BedrockMantleKeyConfig.ProjectID,
				}
				for name, ep := range map[string]*schemas.BedrockEndpoints{
					"bedrock": key.BedrockKeyConfig.Endpoints, "mantle": key.BedrockMantleKeyConfig.Endpoints,
				} {
					result[name+".runtime"] = ep.Runtime
					result[name+".control_plane"] = ep.ControlPlane
					result[name+".mantle"] = ep.Mantle
					result[name+".agent_runtime"] = ep.AgentRuntime
					result[name+".s3"] = ep.S3
				}
				return result
			}
			originals := fields(config.Keys[0])
			for name, original := range originals {
				// An unresolved reference would otherwise make the leak check pass.
				require.Equal(t, canary, original.GetValue(), "setup: %s must resolve", name)
			}
			before, err := json.Marshal(config)
			require.NoError(t, err)
			redacted := config.Redacted()
			data, err := json.Marshal(redacted)
			require.NoError(t, err)
			if source.ref != "" {
				assert.NotContains(t, string(data), canary)
				assert.Contains(t, string(data), source.ref)
			}
			assert.Equal(t, "model-id", redacted.Keys[0].Aliases["rich"].ModelID)
			assert.Equal(t, "alias metadata", redacted.Keys[0].Aliases["rich"].Description)
			assert.Contains(t, string(data), `"simple":"simple-model"`)
			// The legacy Vertex project is promoted by MarshalJSON; it must be
			// redacted before that promotion too.
			legacyJSON, err := json.Marshal(redacted.Keys[0].Aliases["legacy-project"])
			require.NoError(t, err)
			assert.Contains(t, string(legacyJSON), `"project_id"`)
			for name, field := range fields(redacted.Keys[0]) {
				t.Run(name, func(t *testing.T) {
					require.NotNil(t, field)
					assert.Equal(t, source.ref, field.GetRawRef())
					assert.Equal(t, originals[name].Type(), field.Type())
					want := canary
					if source.ref != "" {
						want = originals[name].Redacted().GetValue()
					}
					assert.Equal(t, want, field.GetValue())
					assert.NotSame(t, originals[name], field)
					field.Val = "changed response copy"
					assert.Equal(t, canary, originals[name].GetValue(), "redaction must not alias live secrets")
				})
			}
			delete(redacted.Keys[0].Aliases, "simple")
			after, err := json.Marshal(config)
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(after), "redaction must not modify live config")
		})
	}
}

// TestProviderConfig_Redacted_AutoMasksEnvBackedFields verifies that env-backed
// values in provider config fields are redacted in the JSON output of a Redacted()
// ProviderConfig, including fields like Azure Endpoint.
func TestProviderConfig_Redacted_OAuthKeyConfig(t *testing.T) {
	cfg := &ProviderConfig{
		Keys: []schemas.Key{{
			ID:   "k1",
			Name: "oauth",
			OAuthKeyConfig: &schemas.OAuthKeyConfig{
				GrantType:    schemas.OAuthGrantClientCredentials,
				TokenURL:     *schemas.NewSecretVar("https://idp.example/oauth2/token"),
				ClientID:     schemas.NewSecretVar("client-id-value"),
				ClientSecret: schemas.NewSecretVar("client-secret-value"),
				PrivateKey:   schemas.NewSecretVar("-----BEGIN RSA PRIVATE KEY-----"),
				Scopes:       []string{"inference"},
				Audience:     "api://acme",
				ExtraParams:  map[string]string{"resource": "api://acme", "client_assertion": "assertion-secret-value"},
			},
		}},
	}

	redacted := cfg.Redacted()
	require.Len(t, redacted.Keys, 1)
	got := redacted.Keys[0].OAuthKeyConfig
	require.NotNil(t, got)
	assert.Equal(t, "https://idp.example/oauth2/token", got.TokenURL.GetValue(), "the token URL is an address and stays readable")
	assert.NotEqual(t, "client-id-value", got.ClientID.GetValue())
	assert.NotEqual(t, "client-secret-value", got.ClientSecret.GetValue())
	assert.NotEqual(t, "-----BEGIN RSA PRIVATE KEY-----", got.PrivateKey.GetValue())
	assert.Equal(t, []string{"inference"}, got.Scopes)
	assert.Equal(t, "api://acme", got.Audience)
	// extra_params is a free-form form-parameter map, so a caller may have put a credential in
	// it; every value is masked on the way out and the keys stay visible.
	require.Len(t, got.ExtraParams, 2)
	assert.NotEqual(t, "assertion-secret-value", got.ExtraParams["client_assertion"])
	assert.NotEqual(t, "api://acme", got.ExtraParams["resource"])
	// The original is untouched.
	assert.Equal(t, "client-secret-value", cfg.Keys[0].OAuthKeyConfig.ClientSecret.GetValue())
	assert.Equal(t, "assertion-secret-value", cfg.Keys[0].OAuthKeyConfig.ExtraParams["client_assertion"])
}

// TestProviderConfig_Redacted_OAuthExtraParamsMaskTheLiteralString pins that extra_params
// values are masked as the strings they are. They are sent to the token endpoint verbatim, so
// a value that merely looks like a secret reference ("env.production") must not be resolved
// on the way out: an unset variable would mask to "", which an update then saves as "".
func TestProviderConfig_Redacted_OAuthExtraParamsMaskTheLiteralString(t *testing.T) {
	cfg := &ProviderConfig{
		Keys: []schemas.Key{{
			ID:   "k1",
			Name: "oauth",
			OAuthKeyConfig: &schemas.OAuthKeyConfig{
				GrantType:    schemas.OAuthGrantClientCredentials,
				TokenURL:     *schemas.NewSecretVar("https://idp.example/oauth2/token"),
				ClientID:     schemas.NewSecretVar("client-id-value"),
				ClientSecret: schemas.NewSecretVar("client-secret-value"),
				ExtraParams:  map[string]string{"resource": "env.production", "tenant": "vault.tenants/acme", "short": "abc"},
			},
		}},
	}

	got := cfg.Redacted().Keys[0].OAuthKeyConfig.ExtraParams
	require.Len(t, got, 3)
	for name, original := range cfg.Keys[0].OAuthKeyConfig.ExtraParams {
		masked := got[name]
		assert.NotEmpty(t, masked, "%s: a non-empty parameter must mask to a non-empty placeholder", name)
		assert.NotEqual(t, original, masked, "%s: the value must be masked", name)
		assert.True(t, (&schemas.SecretVar{Val: masked}).IsMaskedPlaceholder(),
			"%s: the mask %q must be recognised as a placeholder so an update restores the stored value", name, masked)
	}
	assert.Equal(t, "env."+strings.Repeat("*", 24)+"tion", got["resource"], "the literal string is masked, not the variable it names")
}

func TestProviderConfig_Redacted_AutoMasksEnvBackedFields(t *testing.T) {
	t.Setenv("MY_AZURE_ENDPOINT_SECRET", "https://secret-resource.openai.azure.com")

	endpoint := schemas.NewSecretVar("env.MY_AZURE_ENDPOINT_SECRET")
	require.True(t, endpoint.IsFromSecret(), "setup: Endpoint should be FromSecret")
	require.Equal(t, "https://secret-resource.openai.azure.com", endpoint.GetValue(),
		"setup: Endpoint should be resolved")

	config := ProviderConfig{
		Keys: []schemas.Key{{
			ID:    "k1",
			Name:  "test",
			Value: schemas.SecretVar{Val: ""},
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint: *endpoint,
			},
		}},
	}

	redacted := config.Redacted()
	require.NotNil(t, redacted)
	require.Len(t, redacted.Keys, 1)
	require.NotNil(t, redacted.Keys[0].AzureKeyConfig)

	// Marshal the Endpoint field as it would be sent to the UI.
	data, err := json.Marshal(redacted.Keys[0].AzureKeyConfig.Endpoint)
	require.NoError(t, err)

	var out struct {
		Value      string `json:"value"`
		Ref        string `json:"ref"`
		SecretType string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(data, &out))

	assert.NotContains(t, out.Value, "secret-resource",
		"resolved env value leaked through Endpoint JSON output: %q", out.Value)
	assert.Equal(t, "env.MY_AZURE_ENDPOINT_SECRET", out.Ref,
		"secret ref must be preserved so the UI can show it")
	assert.Equal(t, "env", out.SecretType, "type field must be preserved")
}

// TestProviderConfig_Redacted_DoesNotMaskPlainNonSecretFields verifies that the
// auto-redaction does NOT touch plain (non-env-backed) values. A plain endpoint
// URL must show as-is in the UI.
func TestProviderConfig_Redacted_DoesNotMaskPlainNonSecretFields(t *testing.T) {
	config := ProviderConfig{
		Keys: []schemas.Key{{
			ID:    "k1",
			Name:  "test",
			Value: schemas.SecretVar{Val: ""},
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint: *schemas.NewSecretVar("https://foo.openai.azure.com"),
			},
		}},
	}

	redacted := config.Redacted()
	require.NotNil(t, redacted)
	require.Len(t, redacted.Keys, 1)
	require.NotNil(t, redacted.Keys[0].AzureKeyConfig)

	data, err := json.Marshal(redacted.Keys[0].AzureKeyConfig.Endpoint)
	require.NoError(t, err)

	var out struct {
		Value      string `json:"value"`
		SecretType string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(data, &out))

	assert.Equal(t, "https://foo.openai.azure.com", out.Value,
		"plain Endpoint was incorrectly redacted")
	assert.Equal(t, out.SecretType, string(schemas.SecretTypePlainText))
}

// TestProviderConfig_Redacted_PreservesSecretVarReferenceForVertex verifies that
// env-backed Vertex fields appear in the redacted output with the env reference
// intact and the resolved value masked. This is the user-facing fix for the
// "I see resolved env values in the UI" bug.
func TestProviderConfig_Redacted_PreservesSecretVarReferenceForVertex(t *testing.T) {
	t.Setenv("MY_VERTEX_PROJECT_ID_SECRET", "super-secret-project-12345")

	projectID := schemas.NewSecretVar("env.MY_VERTEX_PROJECT_ID_SECRET")
	require.Equal(t, "super-secret-project-12345", projectID.GetValue())

	config := ProviderConfig{
		Keys: []schemas.Key{{
			ID:    "k1",
			Name:  "test",
			Value: schemas.SecretVar{Val: ""},
			VertexKeyConfig: &schemas.VertexKeyConfig{
				ProjectID: *projectID,
				Region:    *schemas.NewSecretVar("us-central1"),
			},
		}},
	}

	redacted := config.Redacted()
	data, err := json.Marshal(redacted.Keys[0].VertexKeyConfig.ProjectID)
	require.NoError(t, err)

	var out struct {
		Value      string `json:"value"`
		Ref        string `json:"ref"`
		SecretType string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(data, &out))

	assert.NotContains(t, out.Value, "super-secret-project",
		"resolved Vertex ProjectID env value leaked: %q", out.Value)
	assert.Equal(t, "env.MY_VERTEX_PROJECT_ID_SECRET", out.Ref)
	assert.Equal(t, "env", out.SecretType)
}

// TestProviderConfig_Redacted_DoesNotMutateOriginal ensures Redacted() does not
// mutate the original config in memory. The inference path reads from the in-memory
// config and calls GetValue() to build outgoing LLM requests.
func TestProviderConfig_Redacted_DoesNotMutateOriginal(t *testing.T) {
	t.Setenv("MY_REAL_KEY", "sk-real-secret-1234567890abcdef")

	keyValue := schemas.NewSecretVar("env.MY_REAL_KEY")
	require.Equal(t, "sk-real-secret-1234567890abcdef", keyValue.GetValue())

	config := ProviderConfig{
		Keys: []schemas.Key{{
			ID:    "k1",
			Name:  "test",
			Value: *keyValue,
		}},
	}

	redacted := config.Redacted()
	_, err := json.Marshal(redacted)
	require.NoError(t, err)

	// Original must still hold the resolved value.
	assert.Equal(t, "sk-real-secret-1234567890abcdef", config.Keys[0].Value.GetValue(),
		"Redacted() or MarshalJSON mutated the original key Value")
}

// TestProviderConfig_Redacted_FullJSONHasNoLeakedEnvSecrets is a high-level smoke
// test: build a config containing env-backed values across multiple provider types
// and assert that no resolved secret string appears anywhere in the marshaled
// redacted JSON.
func TestProviderConfig_Redacted_FullJSONHasNoLeakedEnvSecrets(t *testing.T) {
	t.Setenv("LEAK_TEST_AZURE_ENDPOINT", "https://leaked-azure.example.com")
	t.Setenv("LEAK_TEST_VERTEX_PROJECT", "leaked-vertex-project-id")
	t.Setenv("LEAK_TEST_BEDROCK_ACCESS", "AKIAIOSFODNN7LEAKED1")
	t.Setenv("LEAK_TEST_OPENAI_KEY", "sk-leaked-openai-key-1234567890")

	config := ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:    "openai-k",
				Name:  "openai",
				Value: *schemas.NewSecretVar("env.LEAK_TEST_OPENAI_KEY"),
			},
			{
				ID:    "azure-k",
				Name:  "azure",
				Value: schemas.SecretVar{Val: ""},
				AzureKeyConfig: &schemas.AzureKeyConfig{
					Endpoint: *schemas.NewSecretVar("env.LEAK_TEST_AZURE_ENDPOINT"),
				},
			},
			{
				ID:    "vertex-k",
				Name:  "vertex",
				Value: schemas.SecretVar{Val: ""},
				VertexKeyConfig: &schemas.VertexKeyConfig{
					ProjectID: *schemas.NewSecretVar("env.LEAK_TEST_VERTEX_PROJECT"),
					Region:    *schemas.NewSecretVar("us-central1"),
				},
			},
			{
				ID:    "bedrock-k",
				Name:  "bedrock",
				Value: schemas.SecretVar{Val: ""},
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey: *schemas.NewSecretVar("env.LEAK_TEST_BEDROCK_ACCESS"),
					SecretKey: schemas.SecretVar{Val: ""},
				},
			},
		},
	}

	redacted := config.Redacted()
	data, err := json.Marshal(redacted)
	require.NoError(t, err)
	jsonStr := string(data)

	leakedSecrets := []string{
		"https://leaked-azure.example.com",
		"leaked-vertex-project-id",
		"AKIAIOSFODNN7LEAKED1",
		"sk-leaked-openai-key-1234567890",
	}
	for _, secret := range leakedSecrets {
		assert.False(t, strings.Contains(jsonStr, secret),
			"resolved env secret %q leaked into redacted JSON output", secret)
	}

	// And the env var references must be present so the UI can render them.
	expectedRefs := []string{
		"env.LEAK_TEST_OPENAI_KEY",
		"env.LEAK_TEST_AZURE_ENDPOINT",
		"env.LEAK_TEST_VERTEX_PROJECT",
		"env.LEAK_TEST_BEDROCK_ACCESS",
	}
	for _, ref := range expectedRefs {
		assert.True(t, strings.Contains(jsonStr, ref),
			"env var reference %q missing from redacted JSON output", ref)
	}
}

// TestProviderConfig_Redacted_SurfacesLiteralIdentifiers pins the rule that a
// region or a self-hosted service URL is an identifier, not a credential: when
// it is stored as a literal it must read back verbatim so the update form shows
// something an operator can actually read.
func TestProviderConfig_Redacted_SurfacesLiteralIdentifiers(t *testing.T) {
	config := ProviderConfig{
		Keys: []schemas.Key{{
			ID:    "k1",
			Name:  "test",
			Value: schemas.SecretVar{Val: ""},
			VertexKeyConfig: &schemas.VertexKeyConfig{
				Region: *schemas.NewSecretVar("us-central1"),
			},
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: schemas.SecretVar{Val: ""},
				SecretKey: schemas.SecretVar{Val: ""},
				Region:    schemas.NewSecretVar("us-east-1"),
			},
			BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{
				AccessKey: schemas.SecretVar{Val: ""},
				SecretKey: schemas.SecretVar{Val: ""},
				Region:    schemas.NewSecretVar("us-east-1"),
			},
			VLLMKeyConfig: &schemas.VLLMKeyConfig{
				URL: *schemas.NewSecretVar("http://vllm.internal.example.com:8000"),
			},
			OllamaKeyConfig: &schemas.OllamaKeyConfig{
				URL: *schemas.NewSecretVar("http://ollama.internal.example.com:11434"),
			},
			SGLKeyConfig: &schemas.SGLKeyConfig{
				URL: *schemas.NewSecretVar("http://sgl.internal.example.com:30000"),
			},
		}},
	}

	redacted := config.Redacted()
	require.NotNil(t, redacted)
	require.Len(t, redacted.Keys, 1)
	key := redacted.Keys[0]

	assert.Equal(t, "us-central1", key.VertexKeyConfig.Region.GetValue())
	assert.Equal(t, "us-east-1", key.BedrockKeyConfig.Region.GetValue())
	assert.Equal(t, "us-east-1", key.BedrockMantleKeyConfig.Region.GetValue())
	assert.Equal(t, "http://vllm.internal.example.com:8000", key.VLLMKeyConfig.URL.GetValue())
	assert.Equal(t, "http://ollama.internal.example.com:11434", key.OllamaKeyConfig.URL.GetValue())
	assert.Equal(t, "http://sgl.internal.example.com:30000", key.SGLKeyConfig.URL.GetValue())

	// The redacted copy must not alias the live config: these are pointer
	// fields, and an API response handing out the live SecretVar would let a
	// caller edit the running configuration.
	assert.NotSame(t, config.Keys[0].BedrockKeyConfig.Region, key.BedrockKeyConfig.Region)
	assert.NotSame(t, config.Keys[0].BedrockMantleKeyConfig.Region, key.BedrockMantleKeyConfig.Region)
}

// TestProviderConfig_Redacted_MasksSecretBackedIdentifiers is the other half of
// the rule: an operator who deliberately sourced a region or URL from env/vault
// still gets the resolved value masked, with the reference intact for the UI.
func TestProviderConfig_Redacted_MasksSecretBackedIdentifiers(t *testing.T) {
	t.Setenv("LEAK_TEST_REGION", "ap-southeast-2")
	t.Setenv("LEAK_TEST_VLLM_URL", "http://vllm-secret.internal.example.com:8000")

	config := ProviderConfig{
		Keys: []schemas.Key{{
			ID:    "k1",
			Name:  "test",
			Value: schemas.SecretVar{Val: ""},
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: schemas.SecretVar{Val: ""},
				SecretKey: schemas.SecretVar{Val: ""},
				Region:    schemas.NewSecretVar("env.LEAK_TEST_REGION"),
			},
			VLLMKeyConfig: &schemas.VLLMKeyConfig{
				URL: *schemas.NewSecretVar("env.LEAK_TEST_VLLM_URL"),
			},
		}},
	}
	require.Equal(t, "ap-southeast-2", config.Keys[0].BedrockKeyConfig.Region.GetValue(),
		"setup: region should resolve from the environment")

	redacted := config.Redacted()
	data, err := json.Marshal(redacted)
	require.NoError(t, err)
	jsonStr := string(data)

	for _, secret := range []string{"ap-southeast-2", "vllm-secret.internal.example.com"} {
		assert.NotContains(t, jsonStr, secret,
			"resolved env value %q leaked into redacted JSON output", secret)
	}
	for _, ref := range []string{"env.LEAK_TEST_REGION", "env.LEAK_TEST_VLLM_URL"} {
		assert.Contains(t, jsonStr, ref,
			"env var reference %q missing from redacted JSON output", ref)
	}
}

// TestProviderConfig_InjectedToolsSurvivesRedactionAndHash covers the two copies of
// ProviderConfig that are built field by field: Redacted (GET responses) and
// GenerateConfigHash (config.json vs DB drift detection).
func TestProviderConfig_InjectedToolsSurvivesRedactionAndHash(t *testing.T) {
	cfg := ProviderConfig{InjectedTools: &schemas.InjectedToolsConfig{
		WebSearch: &schemas.InjectedToolRef{MCPClientName: "tavily", ToolName: "search"},
	}}
	assert.Equal(t, cfg.InjectedTools, cfg.Redacted().InjectedTools)

	withTool, err := cfg.GenerateConfigHash("openai")
	require.NoError(t, err)
	without, err := (&ProviderConfig{}).GenerateConfigHash("openai")
	require.NoError(t, err)
	assert.NotEqual(t, without, withTool)

	cfg.InjectedTools.WebSearch.ToolName = "web_search"
	changed, err := cfg.GenerateConfigHash("openai")
	require.NoError(t, err)
	assert.NotEqual(t, withTool, changed)
}
