package configstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProviderConfig_Redacted_AutoMasksEnvBackedFields verifies that env-backed
// values in provider config fields are redacted in the JSON output of a Redacted()
// ProviderConfig, including fields like Azure Endpoint.
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
	t.Setenv("LEAK_TEST_GIGACHAT_CREDENTIALS", "leaked-gigachat-credentials")

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
			{
				ID:    "gigachat-k",
				Name:  "gigachat",
				Value: schemas.SecretVar{Val: ""},
				GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
					Credentials:  schemas.NewSecretVar("env.LEAK_TEST_GIGACHAT_CREDENTIALS"),
					CertFile:     "/secure/client.pem",
					KeyFile:      "/secure/client.key",
					CABundleFile: "/secure/ca.pem",
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
		"leaked-gigachat-credentials",
		"/secure/client.pem",
		"/secure/client.key",
		"/secure/ca.pem",
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
		"env.LEAK_TEST_GIGACHAT_CREDENTIALS",
	}
	for _, ref := range expectedRefs {
		assert.True(t, strings.Contains(jsonStr, ref),
			"env var reference %q missing from redacted JSON output", ref)
	}
}
