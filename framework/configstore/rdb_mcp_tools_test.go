package configstore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpdateMCPClientDiscovery_TargetedColumnUpdate pins that UpdateMCPClientDiscovery
// only ever touches discovered_tools_json/tool_name_mapping_json — unlike
// UpdateMCPClientConfig's full-row overwrite, a periodic tool-sync writer
// using this method can never clobber an unrelated field a concurrent config
// edit just wrote (e.g. Name).
func TestUpdateMCPClientDiscovery_TargetedColumnUpdate(t *testing.T) {
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

	require.NoError(t, s.UpdateMCPClientDiscovery(ctx, "mcp-tools-client", &schemas.MCPDiscovery{Tools: tools, ToolNameMapping: mapping}))

	got, err := s.GetMCPClientByID(ctx, "mcp-tools-client")
	require.NoError(t, err)
	assert.Equal(t, tools, got.DiscoveredTools)
	assert.Equal(t, mapping, got.DiscoveredToolNameMapping)
	// Untouched column proves this was a targeted update, not a full-row one.
	assert.Equal(t, "original_name", got.Name)
}

// TestUpdateMCPClientDiscovery_EmptyMapPersistsAsLegitimateZeroTools confirms an
// empty (non-nil) result is written as-is — "the server has zero tools" is a
// real, distinct outcome from "never discovered", and must round-trip rather
// than being silently skipped.
func TestUpdateMCPClientDiscovery_EmptyMapPersistsAsLegitimateZeroTools(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID:               "mcp-empty-tools-client",
		Name:             "empty_tools_client",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
		DiscoveredTools:  map[string]schemas.ChatTool{"stale": {Type: "function"}},
	}))

	require.NoError(t, s.UpdateMCPClientDiscovery(ctx, "mcp-empty-tools-client", &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{}, ToolNameMapping: map[string]string{}}))

	got, err := s.GetMCPClientByID(ctx, "mcp-empty-tools-client")
	require.NoError(t, err)
	assert.Empty(t, got.DiscoveredTools)
}

// TestUpdateMCPClientDiscovery_UnknownClientReturnsNotFound mirrors
// UpdateMCPClientOAuthConfigID's own not-found contract for the same
// Updates()+RowsAffected==0 pattern.
func TestUpdateMCPClientDiscovery_UnknownClientReturnsNotFound(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()

	err := s.UpdateMCPClientDiscovery(ctx, "does-not-exist", &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{}, ToolNameMapping: map[string]string{}})
	assert.ErrorIs(t, err, ErrNotFound)
}

// TestMCPClientConfigUnknownIDReturnsNotFound pins that deleting or updating an MCP
// client id with no row reports ErrNotFound, so a handler can answer 404 rather than
// 500, while the message still names the id.
func TestMCPClientConfigUnknownIDReturnsNotFound(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()

	err := s.DeleteMCPClientConfig(ctx, "does-not-exist")
	assert.ErrorIs(t, err, ErrNotFound)
	assert.ErrorContains(t, err, "does-not-exist")

	err = s.UpdateMCPClientConfig(ctx, "does-not-exist", &tables.TableMCPClient{Name: "renamed"})
	assert.ErrorIs(t, err, ErrNotFound)
	assert.ErrorContains(t, err, "does-not-exist")
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

// The global and per-client max protocol versions round-trip through the store, and GetMCPConfig
// hands both to core.
func TestMaxProtocolVersion_RoundTripsThroughStore(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.UpdateClientConfig(ctx, &ClientConfig{MCPMaxProtocolVersion: schemas.MCPProtocolVersion20250618}))
	cc, err := s.GetClientConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, schemas.MCPProtocolVersion20250618, cc.MCPMaxProtocolVersion)

	require.NoError(t, s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID:                 "capped",
		Name:               "capped",
		ConnectionType:     schemas.MCPConnectionTypeHTTP,
		ConnectionString:   schemas.NewSecretVar("https://example.invalid/mcp"),
		MaxProtocolVersion: schemas.MCPProtocolVersion20251125,
	}))
	byID, err := s.GetMCPClientByID(ctx, "capped")
	require.NoError(t, err)
	assert.Equal(t, schemas.MCPProtocolVersion20251125, byID.MaxProtocolVersion)

	row, err := s.GetMCPClientByName(ctx, "capped")
	require.NoError(t, err)
	row.MaxProtocolVersion = schemas.MCPMaxProtocolVersionAuto
	require.NoError(t, s.UpdateMCPClientConfig(ctx, "capped", row))

	mcpConfig, err := s.GetMCPConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, schemas.MCPProtocolVersion20250618, mcpConfig.MaxProtocolVersion)
	require.Len(t, mcpConfig.ClientConfigs, 1)
	assert.Equal(t, schemas.MCPMaxProtocolVersionAuto, mcpConfig.ClientConfigs[0].MaxProtocolVersion)
}

// A client's discovered catalog round-trips through the targeted update, GetMCPClientByID and
// GetMCPConfig; resource templates come back as templates.
func TestUpdateMCPClientDiscovery_ResourcesAndPromptsRoundTrip(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID: "listing-client", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
	}))
	discovery := &schemas.MCPDiscovery{
		Tools:             map[string]schemas.ChatTool{},
		Resources:         []mcp.Resource{mcp.NewResource("file:///handbook.md", "handbook")},
		ResourceTemplates: []mcp.ResourceTemplate{mcp.NewResourceTemplate("repo://{owner}/{name}", "repo")},
		Prompts:           []mcp.Prompt{mcp.NewPrompt("docs-review")},
	}
	require.NoError(t, s.UpdateMCPClientDiscovery(ctx, "listing-client", discovery))

	row, err := s.GetMCPClientByID(ctx, "listing-client")
	require.NoError(t, err)
	require.Len(t, row.DiscoveredResources, 1)
	assert.Equal(t, "file:///handbook.md", row.DiscoveredResources[0].URI)
	require.Len(t, row.DiscoveredResourceTemplates, 1)
	assert.Equal(t, "repo://{owner}/{name}", row.DiscoveredResourceTemplates[0].URITemplate.Raw())
	require.Len(t, row.DiscoveredPrompts, 1)
	assert.Equal(t, "docs-review", row.DiscoveredPrompts[0].Name)

	mcpConfig, err := s.GetMCPConfig(ctx)
	require.NoError(t, err)
	require.Len(t, mcpConfig.ClientConfigs, 1)
	assert.Len(t, mcpConfig.ClientConfigs[0].DiscoveredResources, 1)
	assert.Len(t, mcpConfig.ClientConfigs[0].DiscoveredResourceTemplates, 1)
	assert.Len(t, mcpConfig.ClientConfigs[0].DiscoveredPrompts, 1)

	// A server that stopped offering prompts clears the stored list.
	discovery.Prompts = nil
	require.NoError(t, s.UpdateMCPClientDiscovery(ctx, "listing-client", discovery))
	row, err = s.GetMCPClientByID(ctx, "listing-client")
	require.NoError(t, err)
	assert.Nil(t, row.DiscoveredPrompts)
	assert.Len(t, row.DiscoveredResources, 1)

	require.ErrorIs(t, s.UpdateMCPClientDiscovery(ctx, "missing", discovery), ErrNotFound)
}

// A full config update leaves the discovery columns other than tools alone, even when it carries a
// discovery: an edit carries the saved tools forward, and UpdateMCPClientDiscovery owns the rest.
func TestUpdateMCPClientConfig_KeepsSavedSnapshot(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID: "verified-client", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
	}))
	require.NoError(t, s.UpdateMCPClientDiscovery(ctx, "verified-client", &schemas.MCPDiscovery{
		Tools:             map[string]schemas.ChatTool{"docs-search": {Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "docs-search"}}},
		Instructions:      "Use the handbook first.",
		Resources:         []mcp.Resource{mcp.NewResource("file:///handbook.md", "handbook")},
		ResourceTemplates: []mcp.ResourceTemplate{mcp.NewResourceTemplate("repo://{name}", "repo")},
		Prompts:           []mcp.Prompt{mcp.NewPrompt("docs-review")},
	}))
	row, err := s.GetMCPClientByID(ctx, "verified-client")
	require.NoError(t, err)

	edited := *row
	edited.Name = "handbook"
	edited.DiscoveredResources, edited.DiscoveredResourceTemplates, edited.DiscoveredPrompts = nil, nil, nil
	edited.DiscoveredInstructions = ""
	require.NoError(t, s.UpdateMCPClientConfig(ctx, "verified-client", &edited))

	row, err = s.GetMCPClientByID(ctx, "verified-client")
	require.NoError(t, err)
	assert.Equal(t, "handbook", row.Name)
	assert.Len(t, row.DiscoveredTools, 1)
	assert.Len(t, row.DiscoveredResources, 1, "a tools-carrying edit keeps the saved resources")
	assert.Len(t, row.DiscoveredResourceTemplates, 1)
	assert.Len(t, row.DiscoveredPrompts, 1)
	assert.Equal(t, "Use the handbook first.", row.DiscoveredInstructions)
}

// A discovered tool's annotations, title, icons and output schema are left out of ChatTool's JSON,
// so they are stored beside it and restored with it.
func TestUpdateMCPClientDiscovery_ToolDetailsRoundTrip(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID: "details-client", Name: "weather", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
	}))
	tool := schemas.ChatTool{
		Type:        schemas.ChatToolTypeFunction,
		Function:    &schemas.ChatToolFunction{Name: "weather-forecast"},
		Annotations: &schemas.MCPToolAnnotations{ReadOnlyHint: schemas.Ptr(true)},
		MCP: &schemas.MCPToolMetadata{
			Title:        "Forecast",
			Icons:        []mcp.Icon{{Src: "https://example.com/sun.png"}},
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"temp":{"type":"number"}}}`),
		},
	}
	require.NoError(t, s.UpdateMCPClientDiscovery(ctx, "details-client", &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{"weather-forecast": tool}}))

	row, err := s.GetMCPClientByID(ctx, "details-client")
	require.NoError(t, err)
	restored := row.DiscoveredTools["weather-forecast"]
	require.NotNil(t, restored.MCP)
	assert.Equal(t, "Forecast", restored.MCP.Title)
	assert.Equal(t, "https://example.com/sun.png", restored.MCP.Icons[0].Src)
	assert.JSONEq(t, string(tool.MCP.OutputSchema), string(restored.MCP.OutputSchema))
	require.NotNil(t, restored.Annotations)
	assert.True(t, *restored.Annotations.ReadOnlyHint, "annotations survive a restart too")

	// A full save (BeforeSave) keeps them as well.
	require.NoError(t, s.DB().Save(row).Error)
	row, err = s.GetMCPClientByID(ctx, "details-client")
	require.NoError(t, err)
	assert.Equal(t, "Forecast", row.DiscoveredTools["weather-forecast"].MCP.Title)
}

// A full config update that carries discovered tools stores their details too, so an edit made through
// the API never leaves the details of the previous discovery behind.
func TestUpdateMCPClientConfig_StoresToolDetails(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID: "edited-client", Name: "weather", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
	}))
	tool := func(title string) map[string]schemas.ChatTool {
		return map[string]schemas.ChatTool{"weather-forecast": {
			Type:     schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{Name: "weather-forecast"},
			MCP:      &schemas.MCPToolMetadata{Title: title},
		}}
	}
	require.NoError(t, s.UpdateMCPClientDiscovery(ctx, "edited-client", &schemas.MCPDiscovery{Tools: tool("Forecast")}))

	row, err := s.GetMCPClientByID(ctx, "edited-client")
	require.NoError(t, err)
	row.DiscoveredTools = tool("Forecast v2")
	require.NoError(t, s.UpdateMCPClientConfig(ctx, "edited-client", row))

	row, err = s.GetMCPClientByID(ctx, "edited-client")
	require.NoError(t, err)
	require.NotNil(t, row.DiscoveredTools["weather-forecast"].MCP)
	assert.Equal(t, "Forecast v2", row.DiscoveredTools["weather-forecast"].MCP.Title)
}

// The prompts and resources allow-lists keep nil (expose all) apart from [] (expose none) through
// create, update and every read path.
func TestMCPClientExposeLists_RoundTrip(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID: "expose-client", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
	}))
	config, err := s.GetMCPClientConfigByID(ctx, "expose-client")
	require.NoError(t, err)
	assert.Nil(t, config.PromptsToExpose, "unset exposes every prompt")
	assert.Nil(t, config.ResourcesToExpose)

	row, err := s.GetMCPClientByID(ctx, "expose-client")
	require.NoError(t, err)
	row.PromptsToExpose = schemas.WhiteList{}
	row.ResourcesToExpose = schemas.WhiteList{"repo://{owner}/{name}"}
	require.NoError(t, s.UpdateMCPClientConfig(ctx, "expose-client", row))

	config, err = s.GetMCPClientConfigByID(ctx, "expose-client")
	require.NoError(t, err)
	require.NotNil(t, config.PromptsToExpose, "[] is kept, not read back as unset")
	assert.Empty(t, config.PromptsToExpose)
	assert.Equal(t, schemas.WhiteList{"repo://{owner}/{name}"}, config.ResourcesToExpose)
	mcpConfig, err := s.GetMCPConfig(ctx)
	require.NoError(t, err)
	require.Len(t, mcpConfig.ClientConfigs, 1)
	assert.NotNil(t, mcpConfig.ClientConfigs[0].PromptsToExpose)
	assert.Equal(t, schemas.WhiteList{"repo://{owner}/{name}"}, mcpConfig.ClientConfigs[0].ResourcesToExpose)

	row.PromptsToExpose, row.ResourcesToExpose = nil, nil
	require.NoError(t, s.UpdateMCPClientConfig(ctx, "expose-client", row))
	config, err = s.GetMCPClientConfigByID(ctx, "expose-client")
	require.NoError(t, err)
	assert.Nil(t, config.PromptsToExpose)
	assert.Nil(t, config.ResourcesToExpose)

	row.PromptsToExpose = schemas.WhiteList{"*", "review"}
	assert.Error(t, s.UpdateMCPClientConfig(ctx, "expose-client", row), "an invalid list is refused")
}
