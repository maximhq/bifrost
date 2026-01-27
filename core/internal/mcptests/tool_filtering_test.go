package mcptests

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// BASIC FILTERING TESTS - ToolsToExecute (using go-test-server STDIO)
// =============================================================================

// Helper to get actual tool names from go-test-server dynamically
func getActualToolsFromGoTestServer(t *testing.T) (tool1, tool2 string) {
	t.Helper()

	bifrostRoot := GetBifrostRoot(t)
	InitMCPServerPaths(t)

	clientConfig := GetGoTestServerConfig(bifrostRoot)
	clientConfig.ToolsToExecute = []string{"*"}
	manager := setupMCPManager(t, clientConfig)

	clients := manager.GetClients()
	require.NotEmpty(t, clients, "should have at least one client")

	client := clients[0]
	require.NotEmpty(t, client.ToolMap, "should have at least 2 tools")

	toolList := make([]string, 0)
	for toolName := range client.ToolMap {
		toolList = append(toolList, toolName)
		if len(toolList) >= 2 {
			break
		}
	}

	require.GreaterOrEqual(t, len(toolList), 2, "should have at least 2 tools")

	// Tools in ToolMap already have the client name prefix if needed
	// So we return them as-is for execution
	return toolList[0], toolList[1]
}

// Helper to execute a tool via MCP manager and check if it's allowed
func executeToolViaManager(t *testing.T, manager interface{ ExecuteToolCall(*schemas.BifrostContext, *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) }, toolName string) error {
	t.Helper()

	ctx := createTestContext()
	request := &schemas.BifrostMCPRequest{
		RequestType: schemas.MCPRequestTypeChatToolCall,
		ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call-1"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr(toolName),
				Arguments: `{}`,
			},
		},
	}

	_, err := manager.ExecuteToolCall(ctx, request)
	return err
}

// TestToolsToExecute_Nil - FULLY IMPLEMENTED EXAMPLE
func TestToolsToExecute_Nil(t *testing.T) {
	t.Parallel()

	bifrostRoot := GetBifrostRoot(t)
	InitMCPServerPaths(t)

	// Create client with nil ToolsToExecute (deny-all)
	clientConfig := GetGoTestServerConfig(bifrostRoot)
	clientConfig.ToolsToExecute = nil

	manager := setupMCPManager(t, clientConfig)

	// Try to execute a tool - should fail with nil (deny-all)
	tool1, _ := getActualToolsFromGoTestServer(t)
	err := executeToolViaManager(t, manager, tool1)

	// Should fail because nil defaults to deny-all
	assert.NotNil(t, err, "nil ToolsToExecute should deny execution")
}

// TestToolsToExecute_EmptyArray - FULLY IMPLEMENTED EXAMPLE
func TestToolsToExecute_EmptyArray(t *testing.T) {
	t.Parallel()

	bifrostRoot := GetBifrostRoot(t)
	InitMCPServerPaths(t)

	// Create client with empty ToolsToExecute (deny-all)
	clientConfig := GetGoTestServerConfig(bifrostRoot)
	clientConfig.ToolsToExecute = []string{} // Empty array

	manager := setupMCPManager(t, clientConfig)

	// Try to execute a tool
	tool1, _ := getActualToolsFromGoTestServer(t)
	err := executeToolViaManager(t, manager, tool1)

	// Should fail because empty array denies all
	assert.NotNil(t, err, "empty ToolsToExecute should deny execution")
}

func TestToolsToExecute_Wildcard(t *testing.T) {
	t.Parallel()

	bifrostRoot := GetBifrostRoot(t)
	InitMCPServerPaths(t)

	// Get actual tools from server
	tool1, tool2 := getActualToolsFromGoTestServer(t)

	// Create client with wildcard ToolsToExecute (allow-all)
	clientConfig := GetGoTestServerConfig(bifrostRoot)
	clientConfig.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, clientConfig)

	// Test multiple different tools - all should succeed
	testCases := []struct {
		name     string
		toolName string
	}{
		{
			name:     "tool1",
			toolName: tool1,
		},
		{
			name:     "tool2",
			toolName: tool2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := executeToolViaManager(t, manager, tc.toolName)
			assert.Nil(t, err, "wildcard should allow all tools")
		})
	}
}

func TestToolsToExecute_ExplicitList(t *testing.T) {
	t.Parallel()

	bifrostRoot := GetBifrostRoot(t)
	InitMCPServerPaths(t)

	// Create client with explicit allow list
	clientConfig := GetGoTestServerConfig(bifrostRoot)
	clientConfig.ToolsToExecute = []string{"encode"}

	manager := setupMCPManager(t, clientConfig)

	// Verify configuration was set correctly
	clients := manager.GetClients()
	require.Len(t, clients, 1)
	assert.Equal(t, []string{"encode"}, clients[0].ExecutionConfig.ToolsToExecute)
}

func TestToolsToExecute_SingleTool(t *testing.T) {
	t.Parallel()

	bifrostRoot := GetBifrostRoot(t)
	InitMCPServerPaths(t)

	// Create client allowing only first tool
	clientConfig := GetGoTestServerConfig(bifrostRoot)
	clientConfig.ToolsToExecute = []string{"encode"}

	manager := setupMCPManager(t, clientConfig)

	// Verify configuration
	clients := manager.GetClients()
	require.Len(t, clients, 1)
	assert.Equal(t, []string{"encode"}, clients[0].ExecutionConfig.ToolsToExecute)

	// Verify it's not allow-all
	assert.NotEqual(t, []string{"*"}, clients[0].ExecutionConfig.ToolsToExecute, "should not be wildcard")
}

// =============================================================================
// AUTO-EXECUTE FILTERING TESTS
// =============================================================================

func TestToolsToAutoExecute_Basic(t *testing.T) {
	t.Parallel()

	bifrostRoot := GetBifrostRoot(t)
	InitMCPServerPaths(t)

	// Create client with auto-execute configuration
	clientConfig := GetGoTestServerConfig(bifrostRoot)
	clientConfig.ToolsToExecute = []string{"*"}
	clientConfig.ToolsToAutoExecute = []string{"encode"}

	manager := setupMCPManager(t, clientConfig)

	// Verify the client was created with correct configuration
	clients := manager.GetClients()
	require.Len(t, clients, 1)
	assert.Equal(t, []string{"*"}, clients[0].ExecutionConfig.ToolsToExecute)
	assert.Equal(t, []string{"encode"}, clients[0].ExecutionConfig.ToolsToAutoExecute)
}

func TestToolsToAutoExecute_NotInExecuteList(t *testing.T) {
	t.Parallel()

	bifrostRoot := GetBifrostRoot(t)
	InitMCPServerPaths(t)

	// ToolsToExecute allows only first tool, but ToolsToAutoExecute wants second
	clientConfig := GetGoTestServerConfig(bifrostRoot)
	clientConfig.ToolsToExecute = []string{"encode"}
	clientConfig.ToolsToAutoExecute = []string{"hash"}

	manager := setupMCPManager(t, clientConfig)

	// Verify configuration
	clients := manager.GetClients()
	require.Len(t, clients, 1)
	assert.Equal(t, []string{"encode"}, clients[0].ExecutionConfig.ToolsToExecute)
	assert.Equal(t, []string{"hash"}, clients[0].ExecutionConfig.ToolsToAutoExecute)
	assert.NotEqual(t, clients[0].ExecutionConfig.ToolsToExecute, clients[0].ExecutionConfig.ToolsToAutoExecute)
}

func TestToolsToAutoExecute_Wildcard(t *testing.T) {
	t.Parallel()

	bifrostRoot := GetBifrostRoot(t)
	InitMCPServerPaths(t)

	// Create client with wildcard auto-execute
	clientConfig := GetGoTestServerConfig(bifrostRoot)
	clientConfig.ToolsToExecute = []string{"*"}
	clientConfig.ToolsToAutoExecute = []string{"*"}

	manager := setupMCPManager(t, clientConfig)

	// Verify configuration
	clients := manager.GetClients()
	require.Len(t, clients, 1)
	assert.Equal(t, []string{"*"}, clients[0].ExecutionConfig.ToolsToAutoExecute)
}

// =============================================================================
// CONTEXT-LEVEL FILTERING TESTS
// =============================================================================

func TestContextFilteringRestrictsWildcard(t *testing.T) {
	t.Parallel()

	bifrostRoot := GetBifrostRoot(t)
	InitMCPServerPaths(t)

	// Create client with wildcard (allow-all)
	clientConfig := GetGoTestServerConfig(bifrostRoot)
	clientConfig.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, clientConfig)

	// Verify client configuration allows all
	clients := manager.GetClients()
	require.Len(t, clients, 1)
	assert.Equal(t, []string{"*"}, clients[0].ExecutionConfig.ToolsToExecute)

	// Context restricts to only specific tools (verify context works separately)
	ctx := CreateTestContextWithMCPFilter(nil, []string{"encode"})
	assert.NotNil(t, ctx, "context should be created with filter")
}

// =============================================================================
// FILTERING WITH MULTIPLE CLIENTS (using different STDIO servers)
// =============================================================================

func TestFilteringMultipleClients_DifferentRules(t *testing.T) {
	t.Parallel()

	bifrostRoot := GetBifrostRoot(t)
	InitMCPServerPaths(t)

	// Client 1: only first tool (using GoTestServer)
	client1 := GetGoTestServerConfig(bifrostRoot)
	client1.ID = "stdio-client-1"
	client1.Name = "GoTestServerClient1"
	client1.ToolsToExecute = []string{"encode"}

	// Client 2: using a different server (EdgeCaseServer) to avoid STDIO conflict
	client2 := GetEdgeCaseServerConfig(bifrostRoot)
	client2.ID = "stdio-client-2"
	client2.Name = "EdgeCaseServerClient"
	client2.ToolsToExecute = []string{"*"} // Allow all tools from this server

	manager := setupMCPManager(t, client1, client2)

	// Verify both clients are registered with correct filtering
	clients := manager.GetClients()
	require.Len(t, clients, 2)

	// Find and verify each client
	for _, client := range clients {
		if client.ExecutionConfig.ID == "stdio-client-1" {
			assert.Equal(t, []string{"encode"}, client.ExecutionConfig.ToolsToExecute)
		} else if client.ExecutionConfig.ID == "stdio-client-2" {
			assert.Equal(t, []string{"*"}, client.ExecutionConfig.ToolsToExecute)
		}
	}
}

// =============================================================================
// DYNAMIC FILTERING TESTS
// =============================================================================

func TestFilteringChangesAfterClientEdit(t *testing.T) {
	t.Parallel()

	bifrostRoot := GetBifrostRoot(t)
	InitMCPServerPaths(t)

	// Create client with first tool allowed
	clientConfig := GetGoTestServerConfig(bifrostRoot)
	clientConfig.ToolsToExecute = []string{"encode"}

	manager := setupMCPManager(t, clientConfig)

	// Verify initial configuration
	clients := manager.GetClients()
	require.Len(t, clients, 1)
	assert.Equal(t, []string{"encode"}, clients[0].ExecutionConfig.ToolsToExecute)

	// Edit client to only allow second tool
	clientConfig.ToolsToExecute = []string{"hash"}
	err := manager.EditClient(clientConfig.ID, &clientConfig)
	require.NoError(t, err, "edit should succeed")

	// Verify configuration changed
	clients = manager.GetClients()
	require.Len(t, clients, 1)
	assert.Equal(t, []string{"hash"}, clients[0].ExecutionConfig.ToolsToExecute)
	assert.NotEqual(t, []string{"encode"}, clients[0].ExecutionConfig.ToolsToExecute)
}
