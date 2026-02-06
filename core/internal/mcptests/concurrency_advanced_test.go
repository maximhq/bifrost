package mcptests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// CONCURRENT CODE MODE EXECUTION TESTS
// =============================================================================

func TestConcurrent_CodeModeExecution(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	manager := setupMCPManager(t, codeModeClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	const numConcurrent = 50
	var wg sync.WaitGroup
	errors := make(chan error, numConcurrent)
	successCount := atomic.Int32{}

	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			ctx := createTestContext()
			toolCall := CreateExecuteToolCodeCall(
				fmt.Sprintf("call-%d", id),
				fmt.Sprintf("return %d * 2", id),
			)

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
			if bifrostErr != nil {
				errors <- fmt.Errorf("execution %d failed: %v", id, bifrostErr.Error.Message)
				return
			}
			if result == nil {
				errors <- fmt.Errorf("execution %d returned nil result", id)
				return
			}

			successCount.Add(1)
		}(i)
	}

	wg.Wait()
	close(errors)

	// Collect errors
	var errorList []error
	for err := range errors {
		errorList = append(errorList, err)
	}

	// Should have high success rate (at least 80%)
	successRate := float64(successCount.Load()) / float64(numConcurrent)
	assert.Greater(t, successRate, 0.8, "Should have at least 80%% success rate, got %.2f%%, errors: %v", successRate*100, errorList)
}

func TestConcurrent_CodeModeExecutionWithToolCalls(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for reliable concurrent testing
	manager := setupMCPManager(t)

	// Register multiple tools
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	require.NoError(t, RegisterGetTimeTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	const numConcurrent = 30
	var wg sync.WaitGroup
	errors := make(chan error, numConcurrent)

	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			ctx := createTestContext()
			// Different code for each goroutine
			var code string
			switch id % 3 {
			case 0:
				code = fmt.Sprintf(`await bifrostInternal.echo({message: "test-%d"})`, id)
			case 1:
				code = fmt.Sprintf(`await bifrostInternal.calculator({operation: "add", x: %d, y: 10})`, id)
			case 2:
				code = `await bifrostInternal.get_time({timezone: "UTC"})`
			}

			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%d", id), code)
			_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if bifrostErr != nil {
				errors <- fmt.Errorf("execution %d failed: %v", id, bifrostErr.Error.Message)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent execution error: %v", err)
	}
}

// =============================================================================
// CONCURRENT CLIENT OPERATIONS TESTS
// =============================================================================

func TestConcurrent_AddRemoveClients(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	const numOperations = 20
	var wg sync.WaitGroup
	errors := make(chan error, numOperations*2)
	addCount := atomic.Int32{}
	removeCount := atomic.Int32{}

	// Concurrently add and remove clients
	for i := 0; i < numOperations; i++ {
		wg.Add(2)

		// Add client
		go func(id int) {
			defer wg.Done()

			clientConfig := schemas.MCPClientConfig{
				ID:                 fmt.Sprintf("test-client-%d", id),
				Name:               fmt.Sprintf("TestClient%d", id),
				ConnectionType:     schemas.MCPConnectionTypeInProcess,
				ToolsToExecute:     []string{"*"},
				ToolsToAutoExecute: []string{},
			}

			err := manager.AddClient(&clientConfig)
			if err != nil {
				// InProcess connections without a server instance will fail
				// This is expected - we're just testing that the operations are concurrent and don't deadlock
				if !strings.Contains(err.Error(), "server instance") {
					errors <- fmt.Errorf("failed to add client %d: %v", id, err)
				}
			} else {
				addCount.Add(1)
			}
		}(i)

		// Remove client (after a short delay)
		go func(id int) {
			defer wg.Done()

			time.Sleep(50 * time.Millisecond)
			err := manager.RemoveClient(fmt.Sprintf("test-client-%d", id))
			if err != nil {
				// It's OK if client doesn't exist (race condition)
				if err.Error() != "client not found" && !strings.Contains(err.Error(), "not found") {
					errors <- fmt.Errorf("failed to remove client %d: %v", id, err)
				}
			} else {
				removeCount.Add(1)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Collect actual errors (not expected race conditions)
	var actualErrors []error
	for err := range errors {
		actualErrors = append(actualErrors, err)
	}

	// Should have no unexpected errors
	if len(actualErrors) > 0 {
		for _, err := range actualErrors {
			t.Errorf("Concurrent client operation error: %v", err)
		}
		t.Fail()
	}

	// The test passes if operations complete without deadlock/panic
	// Even if add/remove operations fail due to missing server instances
	t.Logf("Successfully completed concurrent add/remove test: %d adds, %d removes", addCount.Load(), removeCount.Load())
}

func TestConcurrent_EditClientDuringExecution_Advanced(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)

	// Register a tool
	require.NoError(t, RegisterEchoTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	// Start multiple tool executions
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			ctx := createTestContext()
			toolCall := GetSampleEchoToolCall(fmt.Sprintf("call-%d", id), fmt.Sprintf("message-%d", id))

			_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
			if bifrostErr != nil {
				errors <- fmt.Errorf("execution %d failed: %v", id, bifrostErr.Error.Message)
			}
		}(i)
	}

	// Concurrently edit the client configuration
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			time.Sleep(time.Duration(id*10) * time.Millisecond)

			clients := manager.GetClients()
			for _, client := range clients {
				if client.ExecutionConfig.ID == "bifrostInternal" {
					// Create a fresh config instead of modifying the returned snapshot
					// to avoid race conditions with concurrent reads
					newConfig := &schemas.MCPClientConfig{
						ID:             client.ExecutionConfig.ID,
						Name:           client.ExecutionConfig.Name,
						ConnectionType: client.ExecutionConfig.ConnectionType,
						ToolsToExecute: []string{"echo"},
					}
					err := manager.UpdateClient(newConfig.ID, newConfig)
					if err != nil {
						errors <- fmt.Errorf("edit %d failed: %v", id, err)
					}
					break
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Some operations may fail due to race conditions, but system should remain stable
	errorCount := 0
	for err := range errors {
		errorCount++
		t.Logf("Expected race condition error: %v", err)
	}

	// Should have at least some successful operations
	assert.Less(t, errorCount, 40, "Too many errors, system may be unstable")
}

// =============================================================================
// CONCURRENT HEALTH MONITORING TESTS
// =============================================================================

func TestConcurrent_HealthCheckDuringExecution_Advanced(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)

	// Register a delay tool for long-running execution
	require.NoError(t, RegisterDelayTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	var wg sync.WaitGroup
	errors := make(chan error, 30)

	// Start long-running tool executions
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			ctx := createTestContext()
			argsMap := map[string]interface{}{"seconds": 2.0}
			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr(fmt.Sprintf("call-%d", id)),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("bifrostInternal-delay"),
					Arguments: toJSON(argsMap),
				},
			}

			_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
			if bifrostErr != nil {
				errors <- fmt.Errorf("execution %d failed: %v", id, bifrostErr.Error.Message)
			}
		}(i)
	}

	// Concurrently check client health
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			time.Sleep(time.Duration(id*10) * time.Millisecond)
			clients := manager.GetClients()
			if len(clients) == 0 {
				errors <- fmt.Errorf("health check %d: no clients found", id)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent health check error: %v", err)
	}
}

// =============================================================================
// CONCURRENT TOOL REGISTRATION TESTS
// =============================================================================

func TestConcurrent_ToolRegistration(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	const numTools = 50
	var wg sync.WaitGroup
	errors := make(chan error, numTools)
	successCount := atomic.Int32{}

	// Register tools concurrently
	for i := 0; i < numTools; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			toolName := fmt.Sprintf("test_tool_%d", id)
			toolSchema := schemas.ChatTool{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        toolName,
					Description: schemas.Ptr(fmt.Sprintf("Test tool %d", id)),
					Parameters: &schemas.ToolFunctionParameters{
						Type:       "object",
						Properties: &schemas.OrderedMap{},
					},
				},
			}

			err := manager.RegisterTool(
				toolName,
				fmt.Sprintf("Test tool %d", id),
				func(args any) (string, error) {
					return fmt.Sprintf("Result from tool %d", id), nil
				},
				toolSchema,
			)

			if err != nil {
				errors <- fmt.Errorf("failed to register tool %d: %v", id, err)
			} else {
				successCount.Add(1)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check errors
	for err := range errors {
		t.Errorf("Tool registration error: %v", err)
	}

	// Verify tools were registered
	ctx := createTestContext()
	tools := manager.GetToolPerClient(ctx)
	totalTools := 0
	for _, clientTools := range tools {
		totalTools += len(clientTools)
	}

	assert.Greater(t, totalTools, 40, "Should have most tools registered successfully")
}

func TestConcurrent_ToolExecutionMixedClients(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)

	// Register multiple tools on internal client
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	require.NoError(t, RegisterGetTimeTool(manager))
	require.NoError(t, RegisterSearchTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	const numConcurrent = 100
	var wg sync.WaitGroup
	errors := make(chan error, numConcurrent)
	successCount := atomic.Int32{}

	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			ctx := createTestContext()

			// Execute different tools
			var toolCall schemas.ChatAssistantMessageToolCall
			switch id % 4 {
			case 0:
				toolCall = GetSampleEchoToolCall(fmt.Sprintf("call-%d", id), fmt.Sprintf("msg-%d", id))
			case 1:
				toolCall = GetSampleCalculatorToolCall(fmt.Sprintf("call-%d", id), "add", float64(id), 10)
			case 2:
				argsMap := map[string]interface{}{"timezone": "UTC"}
				toolCall = schemas.ChatAssistantMessageToolCall{
					ID:   schemas.Ptr(fmt.Sprintf("call-%d", id)),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr("bifrostInternal-get_time"),
						Arguments: toJSON(argsMap),
					},
				}
			case 3:
				argsMap := map[string]interface{}{"query": fmt.Sprintf("search-%d", id), "max_results": 5.0}
				toolCall = schemas.ChatAssistantMessageToolCall{
					ID:   schemas.Ptr(fmt.Sprintf("call-%d", id)),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr("bifrostInternal-search"),
						Arguments: toJSON(argsMap),
					},
				}
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
			if bifrostErr != nil {
				errors <- fmt.Errorf("execution %d failed: %v", id, bifrostErr.Error.Message)
			} else if result != nil {
				successCount.Add(1)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Collect errors
	var errorList []error
	for err := range errors {
		errorList = append(errorList, err)
	}

	// Should have high success rate
	successRate := float64(successCount.Load()) / float64(numConcurrent)
	assert.Greater(t, successRate, 0.9, "Should have at least 90%% success rate, got %.2f%%, errors: %v", successRate*100, errorList)
}

// =============================================================================
// CONCURRENT FILTERING TESTS
// =============================================================================

func TestConcurrent_FilteringChanges(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)

	// Register multiple tools
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	require.NoError(t, RegisterGetTimeTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	// Execute tools while concurrently changing filters
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Create context with different filter settings
			var ctx *schemas.BifrostContext

			if id%2 == 0 {
				// Even: allow all tools
				baseCtx := context.Background()
				baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeClients, []string{"*"})
				baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeTools, []string{"bifrostInternal-*"})
				ctx = schemas.NewBifrostContext(baseCtx, schemas.NoDeadline)
			} else {
				// Odd: allow only echo
				baseCtx := context.Background()
				baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeClients, []string{"*"})
				baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeTools, []string{"bifrostInternal-echo"})
				ctx = schemas.NewBifrostContext(baseCtx, schemas.NoDeadline)
			}

			toolCall := GetSampleEchoToolCall(fmt.Sprintf("call-%d", id), fmt.Sprintf("msg-%d", id))
			_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if bifrostErr != nil {
				errors <- fmt.Errorf("execution %d failed: %v", id, bifrostErr.Error.Message)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent filtering error: %v", err)
	}
}

// =============================================================================
// STRESS TESTS
// =============================================================================

func TestConcurrent_HighLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	const numConcurrent = 500
	const duration = 10 * time.Second

	var wg sync.WaitGroup
	errors := make(chan error, numConcurrent)
	successCount := atomic.Int32{}
	stopTime := time.Now().Add(duration)

	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			counter := 0
			for time.Now().Before(stopTime) {
				ctx := createTestContext()
				toolCall := GetSampleEchoToolCall(
					fmt.Sprintf("call-%d-%d", id, counter),
					fmt.Sprintf("msg-%d-%d", id, counter),
				)

				result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
				if bifrostErr != nil {
					errors <- fmt.Errorf("execution %d-%d failed: %v", id, counter, bifrostErr.Error.Message)
					return
				}
				if result != nil {
					successCount.Add(1)
				}

				counter++
				time.Sleep(50 * time.Millisecond)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Count errors
	errorCount := 0
	for range errors {
		errorCount++
	}

	totalExecutions := int(successCount.Load()) + errorCount
	successRate := float64(successCount.Load()) / float64(totalExecutions)

	t.Logf("Stress test completed: %d successful, %d failed, %.2f%% success rate",
		successCount.Load(), errorCount, successRate*100)

	assert.Greater(t, successRate, 0.95, "Should maintain >95%% success rate under load")
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

func toJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
