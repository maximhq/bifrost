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

	require.NoError(t, s.UpdateMCPClientTools(ctx, "mcp-tools-client", tools, mapping))

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

	require.NoError(t, s.UpdateMCPClientTools(ctx, "mcp-empty-tools-client", map[string]schemas.ChatTool{}, map[string]string{}))

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

	err := s.UpdateMCPClientTools(ctx, "does-not-exist", map[string]schemas.ChatTool{}, map[string]string{})
	assert.ErrorIs(t, err, ErrNotFound)
}
