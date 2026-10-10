package configstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpdateMCPClientTools_TargetedColumnUpdate pins that UpdateMCPClientTools
// only ever touches discovered_tools_json/tool_name_mapping_json — unlike
// UpdateMCPClientConfig's full-row overwrite, a periodic tool-sync writer
// using this method can never clobber an unrelated field a concurrent config
// edit just wrote (e.g. Name).
func TestUpdateMCPClientTools_TargetedColumnUpdate(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID:               "mcp-tools-client",
		Name:             "original_name",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
	}))

	tools := map[string]schemas.ChatTool{
		"echo": {Type: "function"},
	}
	mapping := map[string]string{"echo": "echo-server"}

	require.NoError(t, s.UpdateMCPClientTools(ctx, "mcp-tools-client", tools, mapping, ""))

	got, err := s.GetMCPClientByID(ctx, "mcp-tools-client")
	require.NoError(t, err)
	assert.Equal(t, tools, got.DiscoveredTools)
	assert.Equal(t, mapping, got.DiscoveredToolNameMapping)
	// Untouched column proves this was a targeted update, not a full-row one.
	assert.Equal(t, "original_name", got.Name)
}

// TestUpdateMCPClientTools_EmptyMapPersistsAsLegitimateZeroTools confirms an
// empty (non-nil) result is written as-is — "the server has zero tools" is a
// real, distinct outcome from "never discovered", and must round-trip rather
// than being silently skipped.
func TestUpdateMCPClientTools_EmptyMapPersistsAsLegitimateZeroTools(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID:               "mcp-empty-tools-client",
		Name:             "empty_tools_client",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
		DiscoveredTools:  map[string]schemas.ChatTool{"stale": {Type: "function"}},
	}))

	require.NoError(t, s.UpdateMCPClientTools(ctx, "mcp-empty-tools-client", map[string]schemas.ChatTool{}, map[string]string{}, ""))

	got, err := s.GetMCPClientByID(ctx, "mcp-empty-tools-client")
	require.NoError(t, err)
	assert.Empty(t, got.DiscoveredTools)
}

// TestUpdateMCPClientTools_UnknownClientReturnsNotFound mirrors
// UpdateMCPClientOAuthConfigID's own not-found contract for the same
// Updates()+RowsAffected==0 pattern.
func TestUpdateMCPClientTools_UnknownClientReturnsNotFound(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()

	err := s.UpdateMCPClientTools(ctx, "does-not-exist", map[string]schemas.ChatTool{}, map[string]string{}, "")
	assert.ErrorIs(t, err, ErrNotFound)
}

// TestMCPClientConfig_OpenAPIConfigRoundTripsThroughStore covers the three
// store paths an openapi client's config travels: create, read back (both
// GetMCPConfig and GetMCPClientByID) and the map-based update, including the
// PATCH convention that a nil openapi_config preserves the stored one.
func TestMCPClientConfig_OpenAPIConfigRoundTripsThroughStore(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	base := "https://petstore.example.com/v1"
	spec := "openapi: 3.0.3\ninfo: {title: Petstore, version: 1.0.0}\npaths: {}\n"

	require.NoError(t, s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID:             "openapi-store-1",
		Name:           "petstore",
		ConnectionType: schemas.MCPConnectionTypeOpenAPI,
		AuthType:       schemas.MCPAuthTypeNone,
		ToolsToExecute: schemas.WhiteList{"listPets"},
		OpenAPIConfig: &schemas.MCPOpenAPIConfig{
			Spec:                spec,
			BaseURL:             &base,
			SecurityCredentials: map[string]schemas.MCPOpenAPICredential{"ApiKeyAuth": {Value: schemas.NewSecretVar("k-1")}},
			SpecTitle:           "Petstore",
			OperationCount:      3,
		},
	}))

	byID, err := s.GetMCPClientByID(ctx, "openapi-store-1")
	require.NoError(t, err)
	require.NotNil(t, byID.OpenAPIConfig)
	assert.Equal(t, spec, byID.OpenAPIConfig.Spec)
	assert.Equal(t, "k-1", byID.OpenAPIConfig.SecurityCredentials["ApiKeyAuth"].Value.GetValue())
	assert.Equal(t, 3, byID.OpenAPIConfig.OperationCount)

	all, err := s.GetMCPConfig(ctx)
	require.NoError(t, err)
	var found *schemas.MCPClientConfig
	for _, c := range all.ClientConfigs {
		if c.ID == "openapi-store-1" {
			found = c
		}
	}
	require.NotNil(t, found)
	require.NotNil(t, found.OpenAPIConfig, "GetMCPConfig carries openapi_config")
	assert.Equal(t, base, *found.OpenAPIConfig.BaseURL)

	// A PATCH-style update without openapi_config keeps the stored block.
	row, err := s.GetMCPClientByName(ctx, "petstore")
	require.NoError(t, err)
	row.OpenAPIConfig = nil
	row.ToolsToExecute = schemas.WhiteList{"listPets", "getPetById"}
	require.NoError(t, s.UpdateMCPClientConfig(ctx, "openapi-store-1", row))
	afterPatch, err := s.GetMCPClientByID(ctx, "openapi-store-1")
	require.NoError(t, err)
	require.NotNil(t, afterPatch.OpenAPIConfig, "nil openapi_config on update preserves the stored one")
	assert.Equal(t, spec, afterPatch.OpenAPIConfig.Spec)
	assert.Equal(t, schemas.WhiteList{"listPets", "getPetById"}, afterPatch.ToolsToExecute)

	// A replacement spec and credential set lands in both columns.
	newBase := "https://other.example.com"
	row.OpenAPIConfig = &schemas.MCPOpenAPIConfig{
		Spec:                spec + "# v2\n",
		BaseURL:             &newBase,
		SecurityCredentials: map[string]schemas.MCPOpenAPICredential{"ApiKeyAuth": {Value: schemas.NewSecretVar("k-2")}},
		OperationCount:      4,
	}
	require.NoError(t, s.UpdateMCPClientConfig(ctx, "openapi-store-1", row))
	afterReplace, err := s.GetMCPClientByID(ctx, "openapi-store-1")
	require.NoError(t, err)
	assert.Equal(t, spec+"# v2\n", afterReplace.OpenAPIConfig.Spec)
	assert.Equal(t, newBase, *afterReplace.OpenAPIConfig.BaseURL)
	assert.Equal(t, "k-2", afterReplace.OpenAPIConfig.SecurityCredentials["ApiKeyAuth"].Value.GetValue())
	assert.Equal(t, 4, afterReplace.OpenAPIConfig.OperationCount)
}
