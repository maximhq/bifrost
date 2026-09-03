package tables

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTableKey_DatabricksRoundTrip covers the four persistence shapes a Databricks
// key takes: a fully populated OAuth M2M key, the switch to a personal access token
// (which must clear the service principal rather than leave it stale), an env-backed
// workspace URL, and a config carrying nothing but forward_gateway_tags — the last of
// which AfterFind used to drop, because its reconstruct guard ignored that column.
func TestTableKey_DatabricksRoundTrip(t *testing.T) {
	require.NoError(t, os.Unsetenv("FAKE_DBX_URL_FOR_TEST"))
	db := setupTestDB(t)

	// 1. OAuth M2M key with every field populated.
	key := &TableKey{
		Name: "dbx-oauth", ProviderID: 1, Provider: "databricks", KeyID: "dbx-1",
		Value: *schemas.NewSecretVar(""),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL:       *schemas.NewSecretVar("https://dbc-1.cloud.databricks.com"),
			APIFormat:          schemas.DatabricksAPIFormatAIGateway,
			ClientID:           schemas.NewSecretVar("client-id-value"),
			ClientSecret:       schemas.NewSecretVar("client-secret-value"),
			ForwardGatewayTags: true,
		},
	}
	require.NoError(t, db.Create(key).Error)
	var found TableKey
	require.NoError(t, db.First(&found, key.ID).Error)
	require.NotNil(t, found.DatabricksKeyConfig, "config wiped on reload")
	assert.Equal(t, "https://dbc-1.cloud.databricks.com", found.DatabricksKeyConfig.WorkspaceURL.GetValue())
	assert.Equal(t, schemas.DatabricksAPIFormatAIGateway, found.DatabricksKeyConfig.APIFormat)
	assert.Equal(t, "client-id-value", found.DatabricksKeyConfig.ClientID.GetValue())
	assert.Equal(t, "client-secret-value", found.DatabricksKeyConfig.ClientSecret.GetValue())
	assert.True(t, found.DatabricksKeyConfig.ForwardGatewayTags, "forward_gateway_tags lost")

	// 2. Switch to PAT: service principal must be cleared, not left stale.
	found.Value = *schemas.NewSecretVar("dapi-token")
	found.DatabricksKeyConfig.ClientID = nil
	found.DatabricksKeyConfig.ClientSecret = nil
	require.NoError(t, db.Save(&found).Error)
	var afterSwitch TableKey
	require.NoError(t, db.First(&afterSwitch, key.ID).Error)
	require.NotNil(t, afterSwitch.DatabricksKeyConfig)
	assert.Nil(t, afterSwitch.DatabricksKeyConfig.ClientID, "stale client_id survived the switch to PAT")
	assert.Nil(t, afterSwitch.DatabricksKeyConfig.ClientSecret, "stale client_secret survived the switch to PAT")
	assert.Equal(t, "dapi-token", afterSwitch.Value.GetValue())
	assert.True(t, afterSwitch.DatabricksKeyConfig.ForwardGatewayTags)

	// 3. env-backed workspace URL must round-trip as a reference.
	envKey := &TableKey{
		Name: "dbx-env", ProviderID: 1, Provider: "databricks", KeyID: "dbx-2",
		Value: *schemas.NewSecretVar("dapi-token"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar("env.FAKE_DBX_URL_FOR_TEST"),
		},
	}
	require.NoError(t, db.Create(envKey).Error)
	var foundEnv TableKey
	require.NoError(t, db.First(&foundEnv, envKey.ID).Error)
	require.NotNil(t, foundEnv.DatabricksKeyConfig, "env-backed config wiped on reload")
	assert.Equal(t, "env.FAKE_DBX_URL_FOR_TEST", foundEnv.DatabricksKeyConfig.WorkspaceURL.GetRawRef())

	// 4. Gateway tags with the workspace URL supplied by the provider base_url:
	//    nothing else is set, so the config must still survive.
	tagsOnly := &TableKey{
		Name: "dbx-tags-only", ProviderID: 1, Provider: "databricks", KeyID: "dbx-3",
		Value:               *schemas.NewSecretVar("dapi-token"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{ForwardGatewayTags: true},
	}
	require.NoError(t, db.Create(tagsOnly).Error)
	var foundTags TableKey
	require.NoError(t, db.First(&foundTags, tagsOnly.ID).Error)
	require.NotNil(t, foundTags.DatabricksKeyConfig, "config carrying only forward_gateway_tags was wiped on reload")
	assert.True(t, foundTags.DatabricksKeyConfig.ForwardGatewayTags)
}
