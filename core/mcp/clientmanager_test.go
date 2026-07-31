package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestToolSyncFlightCoalescesAndThrottlesAttempts(t *testing.T) {
	t.Parallel()

	interval := 10 * time.Minute
	now := time.Unix(1_000, 0)
	flight := &toolSyncFlight{}

	require.True(t, flight.tryStart(now, time.Time{}, interval))
	require.False(t, flight.tryStart(now.Add(time.Second), time.Time{}, interval), "in-flight requests must coalesce")

	// A failed upstream refresh records the attempt time so request traffic
	// cannot turn an immediate failure into a tools/list retry storm.
	flight.finish(time.Time{}, false)
	require.False(t, flight.tryStart(now.Add(time.Minute), time.Time{}, interval))
	require.True(t, flight.tryStart(now.Add(interval), time.Time{}, interval))

	// A discarded stale result should be retried by the next request instead
	// of waiting a full interval.
	flight.finish(time.Time{}, true)
	require.True(t, flight.tryStart(now.Add(interval+time.Second), time.Time{}, interval))
	flight.finish(now.Add(interval+2*time.Second), false)

	// A persisted successful-sync timestamp also seeds a fresh manager's
	// throttle state after restart.
	freshFlight := &toolSyncFlight{}
	require.False(t, freshFlight.tryStart(now.Add(time.Minute), now, interval))
}

func TestApplyDiscoveredToolsRefreshUsesImmutableConfigAndRejectsStaleResults(t *testing.T) {
	t.Parallel()

	originalConfig := &schemas.MCPClientConfig{ID: "client-1", Name: "global-client"}
	manager := &MCPManager{
		clientMap: map[string]*schemas.MCPClientState{
			originalConfig.ID: {
				ExecutionConfig: originalConfig,
				ToolMap:         map[string]schemas.ChatTool{},
				ToolNameMapping: map[string]string{},
			},
		},
	}
	tools := map[string]schemas.ChatTool{
		"global-client-search": {
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name: "global-client-search",
			},
		},
	}
	mapping := map[string]string{"search": "search"}
	lastSync := time.Unix(2_000, 0)

	require.True(t, manager.applyDiscoveredToolsRefresh(originalConfig, tools, mapping, lastSync))
	refreshedConfig := manager.clientMap[originalConfig.ID].ExecutionConfig
	require.NotSame(t, originalConfig, refreshedConfig)
	require.Empty(t, originalConfig.DiscoveredTools, "published config snapshots must remain immutable")
	require.Equal(t, tools, refreshedConfig.DiscoveredTools)
	require.Equal(t, mapping, refreshedConfig.DiscoveredToolNameMapping)
	require.Equal(t, lastSync, refreshedConfig.DiscoveredToolsLastSync)
	require.Equal(t, tools, manager.clientMap[originalConfig.ID].ToolMap)

	replacementConfig := *refreshedConfig
	replacementConfig.Name = "renamed-client"
	manager.clientMap[originalConfig.ID].ExecutionConfig = &replacementConfig

	staleTools := map[string]schemas.ChatTool{
		"global-client-stale": {Type: schemas.ChatToolTypeFunction},
	}
	require.False(t, manager.applyDiscoveredToolsRefresh(refreshedConfig, staleTools, nil, lastSync.Add(time.Minute)))
	require.Equal(t, tools, manager.clientMap[originalConfig.ID].ToolMap)
}

func TestUpdateClientPreservesDiscoveredToolState(t *testing.T) {
	t.Parallel()

	lastSync := time.Unix(3_000, 0)
	tools := map[string]schemas.ChatTool{
		"client-search": {
			Type:     schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{Name: "client-search"},
		},
	}
	mapping := map[string]string{"search": "search"}
	config := &schemas.MCPClientConfig{
		ID:                        "client-1",
		Name:                      "client",
		ConnectionType:            schemas.MCPConnectionTypeHTTP,
		AuthType:                  schemas.MCPAuthTypePerUserOauth,
		DiscoveredTools:           tools,
		DiscoveredToolNameMapping: mapping,
		DiscoveredToolsLastSync:   lastSync,
		AllowOnAllVirtualKeys:     true,
	}
	manager := &MCPManager{
		clientMap: map[string]*schemas.MCPClientState{
			config.ID: {
				Name:            config.Name,
				ExecutionConfig: config,
				ToolMap:         tools,
				ToolNameMapping: mapping,
			},
		},
	}

	err := manager.UpdateClient(config.ID, &schemas.MCPClientConfig{
		Name:                  "renamed",
		ConnectionType:        config.ConnectionType,
		AuthType:              config.AuthType,
		AllowOnAllVirtualKeys: true,
	})
	require.NoError(t, err)

	updated := manager.clientMap[config.ID].ExecutionConfig
	require.Equal(t, lastSync, updated.DiscoveredToolsLastSync)
	require.Equal(t, mapping, updated.DiscoveredToolNameMapping)
	require.Contains(t, updated.DiscoveredTools, "renamed-search")
	require.Contains(t, manager.clientMap[config.ID].ToolMap, "renamed-search")
}

func TestCreateSTDIOConnectionAllowsInlineEnvAssignments(t *testing.T) {
	t.Parallel()

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_ASSIGNMENT=inline-value"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.NoError(t, err)
}

func TestCreateSTDIOConnectionAllowsSetReferencedEnvVars(t *testing.T) {
	t.Setenv("TEST_STDIO_ENV_REFERENCE_SET", "set-value")

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_REFERENCE_SET"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.NoError(t, err)
}

func TestCreateSTDIOConnectionRequiresReferencedEnvVars(t *testing.T) {
	t.Setenv("TEST_STDIO_ENV_REFERENCE_MISSING", "")

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_REFERENCE_MISSING"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "environment variable TEST_STDIO_ENV_REFERENCE_MISSING is not set")
}

func TestCreateSTDIOConnectionRejectsEmptyEnvAssignmentName(t *testing.T) {
	t.Parallel()

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"=inline-value"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "environment variable name is empty")
}
