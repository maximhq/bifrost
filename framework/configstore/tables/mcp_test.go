package tables

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTableMCPClient_OpenAPIConfigRoundTripsEncrypted pins the storage shape of
// an openapi client: the spec document lands in its own plaintext column, the
// remainder of openapi_config (credentials included) is one encrypted JSON
// column, and AfterFind reassembles the runtime struct with the spec and secret
// references intact.
func TestTableMCPClient_OpenAPIConfigRoundTripsEncrypted(t *testing.T) {
	t.Setenv("PETSTORE_KEY", "env-key-value")
	db := setupTestDB(t)

	spec := "openapi: 3.0.3\ninfo: {title: Petstore, version: 1.0.0}\npaths: {}\n"
	base := "https://petstore.example.com/v1"
	row := &TableMCPClient{
		ClientID:       "openapi-1",
		Name:           "petstore",
		ConnectionType: string(schemas.MCPConnectionTypeOpenAPI),
		AuthType:       string(schemas.MCPAuthTypeNone),
		OpenAPIConfig: &schemas.MCPOpenAPIConfig{
			Spec:    spec,
			BaseURL: &base,
			SecurityCredentials: map[string]schemas.MCPOpenAPICredential{
				"ApiKeyAuth": {Value: schemas.NewSecretVar("env.PETSTORE_KEY")},
				"BasicAuth":  {Username: schemas.NewSecretVar("svc"), Password: schemas.NewSecretVar("literal-secret")},
			},
			IncludeDeprecated: true,
			MaxResponseBytes:  4096,
			SpecSize:          len(spec),
			SpecHash:          "abc",
			SpecTitle:         "Petstore",
			OpenAPIVersion:    "3.0.3",
			OperationCount:    3,
		},
	}
	require.NoError(t, db.Create(row).Error)

	raw := rawRow(t, db, "config_mcp_clients", row.ID)
	assert.Equal(t, spec, raw["openapi_spec"], "the spec is stored as plaintext in its own column")
	stored, _ := raw["openapi_config_json"].(string)
	require.NotEmpty(t, stored)
	assert.NotContains(t, stored, "literal-secret", "credentials are encrypted at rest")
	assert.NotContains(t, stored, "petstore.example.com", "the whole block is ciphertext")
	assert.Equal(t, EncryptionStatusEncrypted, raw["encryption_status"])

	var loaded TableMCPClient
	require.NoError(t, db.First(&loaded, row.ID).Error)
	require.NotNil(t, loaded.OpenAPIConfig)
	assert.Equal(t, spec, loaded.OpenAPIConfig.Spec, "AfterFind re-attaches the spec")
	require.NotNil(t, loaded.OpenAPIConfig.BaseURL)
	assert.Equal(t, base, *loaded.OpenAPIConfig.BaseURL)
	assert.True(t, loaded.OpenAPIConfig.IncludeDeprecated)
	assert.Equal(t, 4096, loaded.OpenAPIConfig.MaxResponseBytes)
	assert.Equal(t, "Petstore", loaded.OpenAPIConfig.SpecTitle)
	assert.Equal(t, 3, loaded.OpenAPIConfig.OperationCount)

	apiKey := loaded.OpenAPIConfig.SecurityCredentials["ApiKeyAuth"].Value
	require.NotNil(t, apiKey)
	assert.True(t, apiKey.IsFromSecret(), "env reference survives the round trip")
	assert.Equal(t, "env.PETSTORE_KEY", apiKey.GetRawRef())
	assert.Equal(t, "env-key-value", apiKey.GetValue())
	basic := loaded.OpenAPIConfig.SecurityCredentials["BasicAuth"]
	assert.Equal(t, "svc", basic.Username.GetValue())
	assert.Equal(t, "literal-secret", basic.Password.GetValue())
}

// TestTableMCPClient_NonOpenAPIClientLeavesOpenAPIColumnsEmpty confirms the two
// new columns stay NULL/empty for every other connection type and that a row
// without them loads with a nil OpenAPIConfig.
func TestTableMCPClient_NonOpenAPIClientLeavesOpenAPIColumnsEmpty(t *testing.T) {
	db := setupTestDB(t)
	row := &TableMCPClient{
		ClientID:         "http-1",
		Name:             "remote",
		ConnectionType:   string(schemas.MCPConnectionTypeHTTP),
		ConnectionString: schemas.NewSecretVar("https://mcp.example.com"),
	}
	require.NoError(t, db.Create(row).Error)
	raw := rawRow(t, db, "config_mcp_clients", row.ID)
	assert.Empty(t, raw["openapi_spec"])
	assert.Nil(t, raw["openapi_config_json"])

	var loaded TableMCPClient
	require.NoError(t, db.First(&loaded, row.ID).Error)
	assert.Nil(t, loaded.OpenAPIConfig)
}

func TestSplitOpenAPIConfigForStorage(t *testing.T) {
	spec, cfgJSON, err := SplitOpenAPIConfigForStorage(nil)
	require.NoError(t, err)
	assert.Empty(t, spec)
	assert.Nil(t, cfgJSON)

	base := "https://a"
	spec, cfgJSON, err = SplitOpenAPIConfigForStorage(&schemas.MCPOpenAPIConfig{Spec: "openapi: 3.0.0", BaseURL: &base})
	require.NoError(t, err)
	assert.Equal(t, "openapi: 3.0.0", spec)
	require.NotNil(t, cfgJSON)
	assert.False(t, strings.Contains(*cfgJSON, "openapi: 3.0.0"), "the spec text is not duplicated into the JSON column")
	assert.Contains(t, *cfgJSON, `"base_url":"https://a"`)
}
