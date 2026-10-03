package configstore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Each reopen owns a new SQL pool and GORM instance; no Go object is reused.
func openMCPSchemaStore(t *testing.T, path string) *RDBConfigStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&tables.TableMCPClient{}, &tables.TableVirtualMCP{}))
	s := &RDBConfigStore{}
	s.db.Store(db)
	return s
}

func storedMCPSchemaTools() map[string]schemas.ChatTool {
	return map[string]schemas.ChatTool{
		"fixture-boolean": {Type: "function", Function: &schemas.ChatToolFunction{Name: "fixture-boolean"}, MCPToolSchema: &schemas.MCPToolSchema{InputSchema: json.RawMessage(`false`), OutputSchema: json.RawMessage(`true`)}},
		"fixture-edge": {Type: "function", Function: &schemas.ChatToolFunction{Name: "fixture-edge"}, MCPToolSchema: &schemas.MCPToolSchema{
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"n":{"$ref":"#/$defs/N"}},"$defs":{"N":{"type":"integer","maximum":9007199254740993}},"anyOf":[{"required":["n"]}],"x-extension":{"bound":9007199254740993}}`), OutputSchema: json.RawMessage(`{}`),
		}, Annotations: &schemas.MCPToolAnnotations{Title: "edge", ReadOnlyHint: schemas.Ptr(true), DestructiveHint: schemas.Ptr(false), IdempotentHint: schemas.Ptr(true), OpenWorldHint: schemas.Ptr(false)}},
		"fixture-absent":   {Type: "function", Function: &schemas.ChatToolFunction{Name: "fixture-absent"}, MCPToolSchema: &schemas.MCPToolSchema{InputSchema: json.RawMessage(`{}`)}},
		"fixture-no-input": {Type: "function", Function: &schemas.ChatToolFunction{Name: "fixture-no-input"}, MCPToolSchema: &schemas.MCPToolSchema{OutputSchema: json.RawMessage(`{}`)}},
	}
}

func TestMCPSchemaSQLiteReopenWriteEntrypoints(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"create", "config-update", "tools-update"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "schema.db")
			s := openMCPSchemaStore(t, path)
			tools := storedMCPSchemaTools()
			mapping := map[string]string{"fixture-edge": "edge", "fixture-absent": "absent", "fixture-no-input": "no-input"}
			cfg := &schemas.MCPClientConfig{ID: "fixture-id", Name: "fixture", ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"), AuthType: schemas.MCPAuthTypePerUserHeaders, PerUserHeaderKeys: []string{"X-Fixture"}, ToolsToExecute: schemas.WhiteList{"edge"}, Headers: map[string]schemas.SecretVar{"X-Synthetic": *schemas.NewSecretVar("local-test-only")}, ConfigHash: "preserve-hash"}
			if mode == "create" {
				cfg.DiscoveredTools, cfg.DiscoveredToolNameMapping = tools, mapping
			}
			require.NoError(t, s.CreateMCPClientConfig(ctx, cfg))
			switch mode {
			case "config-update":
				var row tables.TableMCPClient
				require.NoError(t, s.DB().Where("client_id = ?", cfg.ID).First(&row).Error)
				row.DiscoveredTools, row.DiscoveredToolNameMapping = tools, mapping
				require.NoError(t, s.UpdateMCPClientConfig(ctx, cfg.ID, &row))
			case "tools-update":
				// Compare every persisted column, not just Name. The periodic writer may
				// update only its three declared columns, including credentials and ACL.
				var before, after map[string]any
				require.NoError(t, s.DB().Table("config_mcp_clients").Where("client_id = ?", cfg.ID).Take(&before).Error)
				require.NoError(t, s.UpdateMCPClientTools(ctx, cfg.ID, tools, mapping))
				require.NoError(t, s.DB().Table("config_mcp_clients").Where("client_id = ?", cfg.ID).Take(&after).Error)
				for _, col := range []string{"discovered_tools_json", "tool_name_mapping_json", "updated_at"} {
					delete(before, col)
					delete(after, col)
				}
				require.Equal(t, before, after)
			}
			// An old reader still sees exactly the old projection.
			var row tables.TableMCPClient
			require.NoError(t, s.DB().Where("client_id = ?", cfg.ID).First(&row).Error)
			var oldReader map[string]schemas.ChatTool
			require.NoError(t, json.Unmarshal([]byte(row.DiscoveredToolsJSON), &oldReader))
			expectedProvider, err := json.Marshal(tools)
			require.NoError(t, err)
			oldProvider, err := json.Marshal(oldReader)
			require.NoError(t, err)
			require.JSONEq(t, string(expectedProvider), string(oldProvider))
			require.NotContains(t, string(oldProvider), "_bifrost_mcp")
			for _, tool := range oldReader {
				require.Nil(t, tool.MCPToolSchema)
				require.Nil(t, tool.Annotations)
			}
			require.NoError(t, s.Close(ctx))
			tools, cfg, oldReader = nil, nil, nil
			s = openMCPSchemaStore(t, path)
			got, err := s.GetMCPClientByID(ctx, "fixture-id")
			require.NoError(t, err)
			require.Equal(t, mapping, got.DiscoveredToolNameMapping)
			expected := storedMCPSchemaTools()
			for name, tool := range expected {
				require.Equal(t, tool.MCPToolSchema, got.DiscoveredTools[name].MCPToolSchema)
				require.Equal(t, tool.Annotations, got.DiscoveredTools[name].Annotations)
			}
			// Clearing the entire cache is distinct from an unknown/nil cache.
			require.NoError(t, s.UpdateMCPClientTools(ctx, "fixture-id", map[string]schemas.ChatTool{}, map[string]string{}))
			require.NoError(t, s.Close(ctx))
			s = openMCPSchemaStore(t, path)
			got, err = s.GetMCPClientByID(ctx, "fixture-id")
			require.NoError(t, err)
			require.NotNil(t, got.DiscoveredTools)
			require.Empty(t, got.DiscoveredTools)
			require.NotNil(t, got.DiscoveredToolNameMapping)
			require.Empty(t, got.DiscoveredToolNameMapping)
			require.NoError(t, s.UpdateMCPClientTools(ctx, "fixture-id", nil, nil))
			require.NoError(t, s.Close(ctx))
			s = openMCPSchemaStore(t, path)
			got, err = s.GetMCPClientByID(ctx, "fixture-id")
			require.NoError(t, err)
			require.Nil(t, got.DiscoveredTools)
			require.Nil(t, got.DiscoveredToolNameMapping)
			require.NoError(t, s.Close(ctx))
		})
	}
}

func TestMCPSchemaLegacyAndDamagedRecords(t *testing.T) {
	ctx := context.Background()
	s := setupRDBTestStore(t)
	require.NoError(t, s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{ID: "legacy", Name: "legacy", ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp")}))
	legacy := `{"edge":{"type":"function","function":{"name":"edge","parameters":{"type":"object"}}}}`
	require.NoError(t, s.DB().Table("config_mcp_clients").Where("client_id = ?", "legacy").Update("discovered_tools_json", legacy).Error)
	got, err := s.GetMCPClientByID(ctx, "legacy")
	require.NoError(t, err)
	require.Equal(t, "edge", got.DiscoveredTools["edge"].Function.Name)
	require.Nil(t, got.DiscoveredTools["edge"].MCPToolSchema, "legacy needs rediscovery, never fabricate a contract")
	for _, metadata := range []string{`null`, `{}`, `{"version":2}`, `{"version":1}`, `{"version":1,"schema":null}`, `{"version":1,"annotations":null}`, `{"version":1,"unknown":true}`, `{"version":1,"schema":{"inputSchema":null}}`, `{"version":1,"schema":{"outputSchema":[]}}`, `{"version":1,"annotations":{"readOnlyHint":"true"}}`, `{"version":1,"schema":{"unknown":{}}}`} {
		bad := `{"edge":{"type":"function","function":{"name":"edge"},"_bifrost_mcp":` + metadata + `}}`
		require.NoError(t, s.DB().Table("config_mcp_clients").Where("client_id = ?", "legacy").Update("discovered_tools_json", bad).Error)
		_, err = s.GetMCPClientByID(ctx, "legacy")
		require.Error(t, err, metadata)
	}
}

func TestMCPSchemaTableSaveClearsReusedCache(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{ID: "reused", Name: "reused", ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"), DiscoveredTools: storedMCPSchemaTools(), DiscoveredToolNameMapping: map[string]string{"edge": "edge"}}))
	var row tables.TableMCPClient
	require.NoError(t, s.DB().Where("client_id = ?", "reused").First(&row).Error)
	row.DiscoveredTools = map[string]schemas.ChatTool{}
	row.DiscoveredToolNameMapping = map[string]string{}
	require.NoError(t, s.DB().Save(&row).Error)
	got, err := s.GetMCPClientByID(ctx, "reused")
	require.NoError(t, err)
	require.NotNil(t, got.DiscoveredTools)
	require.Empty(t, got.DiscoveredTools)
	require.NotNil(t, got.DiscoveredToolNameMapping)
	require.Empty(t, got.DiscoveredToolNameMapping)
	// Legacy empty columns must also reset fields on a reused table receiver.
	row.DiscoveredTools = storedMCPSchemaTools()
	row.DiscoveredToolNameMapping = map[string]string{"stale": "stale"}
	row.DiscoveredToolsJSON = ""
	row.ToolNameMappingJSON = ""
	require.NoError(t, row.AfterFind(nil))
	require.Nil(t, row.DiscoveredTools)
	require.Nil(t, row.DiscoveredToolNameMapping)
}

func TestMCPSchemaConfigUpdateNilPreservesEmptyClears(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	tools := storedMCPSchemaTools()
	mapping := map[string]string{"edge": "original-edge"}
	require.NoError(t, s.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{ID: "patch", Name: "patch", ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"), DiscoveredTools: tools, DiscoveredToolNameMapping: mapping}))
	require.NoError(t, s.UpdateMCPClientConfig(ctx, "patch", &tables.TableMCPClient{Name: "patch-updated"}))
	got, err := s.GetMCPClientByID(ctx, "patch")
	require.NoError(t, err)
	require.Equal(t, tools, got.DiscoveredTools)
	require.Equal(t, mapping, got.DiscoveredToolNameMapping)
	require.NoError(t, s.UpdateMCPClientConfig(ctx, "patch", &tables.TableMCPClient{Name: "patch-updated", DiscoveredTools: map[string]schemas.ChatTool{}, DiscoveredToolNameMapping: map[string]string{}}))
	got, err = s.GetMCPClientByID(ctx, "patch")
	require.NoError(t, err)
	require.NotNil(t, got.DiscoveredTools)
	require.Empty(t, got.DiscoveredTools)
	require.NotNil(t, got.DiscoveredToolNameMapping)
	require.Empty(t, got.DiscoveredToolNameMapping)
}
