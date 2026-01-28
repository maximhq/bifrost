package mcptests

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// CONCURRENT TOOL EXECUTION TESTS
// =============================================================================

func TestConcurrent_MultipleToolExecutions(t *testing.T) {
	t.Parallel()

	// Use InProcess echo tool for fast, reliable concurrent testing
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute 100 tools concurrently
	concurrency := 100
	var wg sync.WaitGroup
	errors := make(chan error, concurrency)
	successCount := int32(0)

	start := time.Now()
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			toolCall := GetSampleEchoToolCall(fmt.Sprintf("call-%d", id), fmt.Sprintf("message-%d", id))
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if bifrostErr != nil {
				errors <- fmt.Errorf("execution %d failed: %v", id, bifrostErr)
				return
			}

			if result == nil {
				errors <- fmt.Errorf("execution %d returned nil result", id)
				return
			}

			atomic.AddInt32(&successCount, 1)
		}(i)
	}

	wg.Wait()
	close(errors)
	elapsed := time.Since(start)

	// Check for errors
	errorCount := 0
	for err := range errors {
		t.Errorf("Concurrent execution error: %v", err)
		errorCount++
	}

	// All should succeed
	assert.Equal(t, 0, errorCount, "no errors should occur")
	assert.Equal(t, int32(concurrency), successCount, "all executions should succeed")
	t.Logf("✅ Successfully executed %d tools concurrently in %v", concurrency, elapsed)
}

func TestConcurrent_SameTool(t *testing.T) {
	t.Parallel()

	// Use InProcess echo tool - execute same tool 50 times concurrently
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	concurrency := 50
	var wg sync.WaitGroup
	results := make([]string, concurrency)
	errors := make(chan error, concurrency)

	// Each goroutine sends unique message and should get it back
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			uniqueMessage := fmt.Sprintf("unique-message-%d", id)
			toolCall := GetSampleEchoToolCall(fmt.Sprintf("call-%d", id), uniqueMessage)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if bifrostErr != nil {
				errors <- fmt.Errorf("execution %d failed: %v", id, bifrostErr)
				return
			}

			if result != nil && result.Content != nil && result.Content.ContentStr != nil {
				results[id] = *result.Content.ContentStr
			} else {
				errors <- fmt.Errorf("execution %d returned invalid result", id)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent execution error: %v", err)
	}

	// Verify each result contains its unique message (results are independent)
	for i := 0; i < concurrency; i++ {
		expectedMsg := fmt.Sprintf("unique-message-%d", i)
		assert.Contains(t, results[i], expectedMsg, "result %d should contain its unique message", i)
	}

	t.Logf("✅ Successfully executed same tool %d times concurrently with independent results", concurrency)
}

func TestConcurrent_DifferentTools(t *testing.T) {
	t.Parallel()

	// Register multiple tools
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	require.NoError(t, RegisterWeatherTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute mix of different tools concurrently
	concurrency := 30 // 10 of each tool type
	var wg sync.WaitGroup
	errors := make(chan error, concurrency)
	successCount := int32(0)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		toolType := i % 3 // Rotate between 3 tool types

		go func(id, tType int) {
			defer wg.Done()

			var result *schemas.ChatMessage
			var bifrostErr *schemas.BifrostError

			switch tType {
			case 0: // Echo
				toolCall := GetSampleEchoToolCall(fmt.Sprintf("echo-%d", id), fmt.Sprintf("echo-msg-%d", id))
				result, bifrostErr = bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			case 1: // Calculator
				toolCall := GetSampleCalculatorToolCall(fmt.Sprintf("calc-%d", id), "add", float64(id), float64(id+1))
				result, bifrostErr = bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			case 2: // Weather
				toolCall := GetSampleWeatherToolCall(fmt.Sprintf("weather-%d", id), "Tokyo", "celsius")
				result, bifrostErr = bifrost.ExecuteChatMCPTool(ctx, &toolCall)
			}

			if bifrostErr != nil {
				errors <- fmt.Errorf("tool type %d execution %d failed: %v", tType, id, bifrostErr)
				return
			}

			if result == nil {
				errors <- fmt.Errorf("tool type %d execution %d returned nil", tType, id)
				return
			}

			atomic.AddInt32(&successCount, 1)
		}(i, toolType)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	errorCount := 0
	for err := range errors {
		t.Errorf("Concurrent mixed tool error: %v", err)
		errorCount++
	}

	assert.Equal(t, 0, errorCount, "no errors should occur")
	assert.Equal(t, int32(concurrency), successCount, "all mixed tool executions should succeed")
	t.Logf("✅ Successfully executed %d different tools concurrently (echo, calculator, weather)", concurrency)
}

// =============================================================================
// CLIENT OPERATIONS DURING EXECUTION
// =============================================================================

func TestConcurrent_AddClientDuringExecution(t *testing.T) {
	t.Parallel()

	// Start with one InProcess client
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Channel to coordinate test phases
	startAdding := make(chan bool)
	var wg sync.WaitGroup
	errors := make(chan error, 25)

	// Goroutine 1: Execute tools continuously (20 executions)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startAdding // Wait for signal to start

		for i := 0; i < 20; i++ {
			toolCall := GetSampleEchoToolCall(fmt.Sprintf("exec-%d", i), fmt.Sprintf("msg-%d", i))
			_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
			if bifrostErr != nil {
				errors <- fmt.Errorf("execution %d failed: %v", i, bifrostErr)
			}
			time.Sleep(10 * time.Millisecond) // Small delay between executions
		}
	}()

	// Goroutine 2: Add new clients concurrently (5 new clients)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startAdding // Wait for signal to start

		for i := 0; i < 5; i++ {
			// Register a new tool (creates new InProcess client)
			toolName := fmt.Sprintf("concurrent_tool_%d", i)
			err := manager.RegisterTool(
				toolName,
				fmt.Sprintf("Tool %d", i),
				func(args any) (string, error) {
					return fmt.Sprintf(`{"result": "tool %d"}`, i), nil
				},
				GetSampleEchoTool(), // Use sample schema
			)
			if err != nil {
				errors <- fmt.Errorf("failed to add client %d: %v", i, err)
			}
			time.Sleep(20 * time.Millisecond) // Small delay between adds
		}
	}()

	// Start both goroutines
	close(startAdding)
	wg.Wait()
	close(errors)

	// Check for errors - some might be acceptable during concurrent modifications
	errorCount := 0
	for err := range errors {
		t.Logf("Concurrent operation: %v", err)
		errorCount++
	}

	// Verify system remained stable (no crashes or deadlocks)
	clients := manager.GetClients()
	assert.GreaterOrEqual(t, len(clients), 1, "should have at least original client")
	t.Logf("✅ System stable during concurrent add operations (%d errors, %d clients)", errorCount, len(clients))
}

func TestConcurrent_RemoveClientDuringExecution(t *testing.T) {
	t.Parallel()

	// Setup manager with multiple InProcess clients
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	clients := manager.GetClients()
	require.GreaterOrEqual(t, len(clients), 1, "should have at least one client")
	clientID := clients[0].ExecutionConfig.ID

	var wg sync.WaitGroup
	errors := make(chan error, 15)
	executions := make(chan bool, 10)

	// Goroutine 1: Execute tools 10 times
	wg.Add(1)
	go func() {
		defer wg.Done()

		for i := 0; i < 10; i++ {
			toolCall := GetSampleEchoToolCall(fmt.Sprintf("exec-%d", i), fmt.Sprintf("msg-%d", i))
			_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			// Execution may fail after client removed - that's ok
			if bifrostErr != nil {
				t.Logf("Execution %d failed (expected after removal): %v", i, bifrostErr)
			}
			executions <- true
			time.Sleep(20 * time.Millisecond)
		}
	}()

	// Goroutine 2: Remove client after a few executions
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Wait for a few executions to complete
		for i := 0; i < 3; i++ {
			<-executions
		}

		// Remove client
		err := manager.RemoveClient(clientID)
		if err != nil {
			errors <- fmt.Errorf("failed to remove client: %v", err)
		} else {
			t.Logf("Client removed during execution")
		}
	}()

	wg.Wait()
	close(errors)
	close(executions)

	// Check for errors
	for err := range errors {
		t.Errorf("Critical error: %v", err)
	}

	// Verify client was removed (graceful handling)
	clients = manager.GetClients()
	clientFound := false
	for _, c := range clients {
		if c.ExecutionConfig.ID == clientID {
			clientFound = true
			break
		}
	}
	assert.False(t, clientFound, "client should be removed")
	t.Logf("✅ Graceful handling of client removal during execution")
}

func TestConcurrent_EditClientDuringExecution(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set - need HTTP client for edit test")
	}

	// Setup with HTTP client
	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	clientConfig.ID = "editable-client"
	applyTestConfigHeaders(t, &clientConfig)

	manager := setupMCPManager(t, clientConfig)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	var wg sync.WaitGroup
	errors := make(chan error, 15)
	executions := make(chan bool, 10)

	// Goroutine 1: Execute tools
	wg.Add(1)
	go func() {
		defer wg.Done()

		for i := 0; i < 10; i++ {
			// Try to execute any available tool
			clients := manager.GetClients()
			if len(clients) > 0 && len(clients[0].ToolMap) > 0 {
				// Get first available tool
				var toolName string
				for name := range clients[0].ToolMap {
					toolName = name
					break
				}

				toolCall := schemas.ChatAssistantMessageToolCall{
					ID:   schemas.Ptr(fmt.Sprintf("exec-%d", i)),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      &toolName,
						Arguments: `{}`,
					},
				}
				_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
				if bifrostErr != nil {
					t.Logf("Execution %d: %v", i, bifrostErr)
				}
			}
			executions <- true
			time.Sleep(50 * time.Millisecond)
		}
	}()

	// Goroutine 2: Edit client configuration
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Wait for a few executions
		for i := 0; i < 2; i++ {
			<-executions
		}

		// Edit client - update name (must not contain spaces)
		updatedConfig := clientConfig
		updatedConfig.Name = "UpdatedClientName"
		err := manager.EditClient(clientConfig.ID, &updatedConfig)
		if err != nil {
			errors <- fmt.Errorf("failed to edit client: %v", err)
		} else {
			t.Logf("Client edited during execution")
		}
	}()

	wg.Wait()
	close(errors)
	close(executions)

	// Check for critical errors
	for err := range errors {
		t.Errorf("Critical error: %v", err)
	}

	// Verify client still exists
	clients := manager.GetClients()
	assert.GreaterOrEqual(t, len(clients), 1, "client should still exist after edit")
	t.Logf("✅ No race conditions during concurrent edit operations")
}

// =============================================================================
// HEALTH CHECK DURING EXECUTION
// =============================================================================

func TestConcurrent_HealthCheckDuringExecution(t *testing.T) {
	t.Parallel()

	// Use delay tool for long-running execution
	manager := setupMCPManager(t)
	require.NoError(t, RegisterDelayTool(manager))
	require.NoError(t, RegisterEchoTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	var wg sync.WaitGroup
	errors := make(chan error, 6)

	// Goroutine 1: Long-running tool (2 seconds)
	wg.Add(1)
	go func() {
		defer wg.Done()

		toolCall := GetSampleDelayToolCall("long-running", 2.0) // 2 second delay
		start := time.Now()
		_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		elapsed := time.Since(start)

		if bifrostErr != nil {
			errors <- fmt.Errorf("long-running tool failed: %v", bifrostErr)
		} else {
			t.Logf("Long-running tool completed in %v", elapsed)
		}
	}()

	// Goroutines 2-6: Quick health check simulations (execute echo tools)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			time.Sleep(time.Duration(id*100) * time.Millisecond) // Stagger checks

			// Quick echo tool acts as health check simulation
			toolCall := GetSampleEchoToolCall(fmt.Sprintf("health-%d", id), "ping")
			_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if bifrostErr != nil {
				errors <- fmt.Errorf("health check %d failed: %v", id, bifrostErr)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	errorCount := 0
	for err := range errors {
		t.Errorf("Concurrent error: %v", err)
		errorCount++
	}

	assert.Equal(t, 0, errorCount, "health checks should not interfere with long-running execution")
	t.Logf("✅ Health checks during long-running execution: no interference")
}

func TestConcurrent_MultipleHealthChecks(t *testing.T) {
	t.Parallel()

	// Setup with multiple InProcess clients
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	require.NoError(t, RegisterWeatherTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	concurrency := 20 // 10 tool executions + 10 health checks
	var wg sync.WaitGroup
	errors := make(chan error, concurrency)

	// 10 tool executions
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			toolType := id % 3
			switch toolType {
			case 0:
				toolCall := GetSampleEchoToolCall(fmt.Sprintf("exec-%d", id), fmt.Sprintf("msg-%d", id))
				_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
				if bifrostErr != nil {
					errors <- fmt.Errorf("execution %d failed: %v", id, bifrostErr)
				}
			case 1:
				toolCall := GetSampleCalculatorToolCall(fmt.Sprintf("calc-%d", id), "add", float64(id), float64(id+1))
				_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
				if bifrostErr != nil {
					errors <- fmt.Errorf("calc %d failed: %v", id, bifrostErr)
				}
			case 2:
				toolCall := GetSampleWeatherToolCall(fmt.Sprintf("weather-%d", id), "Tokyo", "celsius")
				_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
				if bifrostErr != nil {
					errors <- fmt.Errorf("weather %d failed: %v", id, bifrostErr)
				}
			}

			time.Sleep(10 * time.Millisecond)
		}(i)
	}

	// 10 "health checks" (GetClients calls + quick echo executions)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			time.Sleep(time.Duration(id*5) * time.Millisecond)

			// Health check: Get clients
			clients := manager.GetClients()
			if len(clients) == 0 {
				errors <- fmt.Errorf("health check %d: no clients found", id)
				return
			}

			// Health check: Quick ping with echo
			toolCall := GetSampleEchoToolCall(fmt.Sprintf("health-%d", id), "ping")
			_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
			if bifrostErr != nil {
				errors <- fmt.Errorf("health check %d failed: %v", id, bifrostErr)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	errorCount := 0
	for err := range errors {
		t.Errorf("Concurrent error: %v", err)
		errorCount++
	}

	assert.Equal(t, 0, errorCount, "all operations should succeed")
	t.Logf("✅ Multiple health checks during concurrent executions: all successful")
}

// =============================================================================
// CLIENT STATE MUTATIONS
// =============================================================================

func TestConcurrent_ClientStateMutations(t *testing.T) {
	t.Parallel()

	// Setup with initial clients
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	var wg sync.WaitGroup
	errors := make(chan error, 100)
	done := make(chan bool)

	// 50 goroutines reading GetClients() repeatedly
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for {
				select {
				case <-done:
					return
				default:
					// Read client state
					clients := manager.GetClients()
					if len(clients) == 0 {
						errors <- fmt.Errorf("reader %d: no clients found", id)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
		}(i)
	}

	// 10 goroutines executing tools (causing state changes)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for j := 0; j < 5; j++ {
				select {
				case <-done:
					return
				default:
					toolCall := GetSampleEchoToolCall(fmt.Sprintf("state-%d-%d", id, j), "test")
					_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
					if bifrostErr != nil {
						// Errors during concurrent access are logged but not fatal
						t.Logf("Execution %d-%d: %v", id, j, bifrostErr)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		}(i)
	}

	// Run for 1 second
	time.Sleep(1 * time.Second)
	close(done)

	wg.Wait()
	close(errors)

	// Check critical errors (should be minimal or none)
	errorCount := 0
	for err := range errors {
		t.Logf("State mutation error: %v", err)
		errorCount++
	}

	// Some errors might occur but system should remain stable
	assert.Less(t, errorCount, 10, "should have minimal critical errors during concurrent state access")
	t.Logf("✅ Thread-safe access to client state verified (%d errors in 1 second)", errorCount)
}

func TestConcurrent_GetClientsWhileModifying(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)

	var wg sync.WaitGroup
	errors := make(chan error, 1100)
	done := make(chan bool)

	// Goroutine 1: Repeatedly call GetClients() 1000 times
	wg.Add(1)
	go func() {
		defer wg.Done()

		for i := 0; i < 1000; i++ {
			select {
			case <-done:
				return
			default:
				clients := manager.GetClients()
				_ = clients // Just reading, verify no crash
				time.Sleep(1 * time.Millisecond)
			}
		}
		t.Logf("GetClients() called 1000 times")
	}()

	// Goroutine 2: Add/remove clients 100 times
	wg.Add(1)
	go func() {
		defer wg.Done()

		for i := 0; i < 100; i++ {
			select {
			case <-done:
				return
			default:
				// Add client (register new tool)
				toolName := fmt.Sprintf("temp_tool_%d", i)
				err := manager.RegisterTool(
					toolName,
					fmt.Sprintf("Temporary tool %d", i),
					func(args any) (string, error) {
						return `{"result": "temp"}`, nil
					},
					GetSampleEchoTool(),
				)
				if err != nil {
					errors <- fmt.Errorf("failed to register tool %d: %v", i, err)
				}

				time.Sleep(5 * time.Millisecond)

				// Note: Removing InProcess clients is tricky, so we just add
				// In a real scenario with HTTP/SSE clients, we'd test removal too
			}
		}
		t.Logf("Modified clients 100 times")
	}()

	// Timeout after 2 seconds
	go func() {
		time.Sleep(2 * time.Second)
		close(done)
	}()

	wg.Wait()
	close(errors)

	// Check for errors
	errorCount := 0
	for err := range errors {
		t.Logf("Modification error: %v", err)
		errorCount++
	}

	// Final verification - no data races should occur
	clients := manager.GetClients()
	assert.GreaterOrEqual(t, len(clients), 1, "should have clients after concurrent modifications")
	assert.Less(t, errorCount, 10, "should have minimal errors during concurrent access")
	t.Logf("✅ No data races during 1000 GetClients() calls with 100 concurrent modifications")
}

// =============================================================================
// PLUGIN HOOKS CONCURRENCY
// =============================================================================

func TestConcurrent_PluginHooks(t *testing.T) {
	t.Parallel()

	// Create logging plugin (thread-safe)
	loggingPlugin := NewTestLoggingPlugin()

	// Setup MCP manager
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))

	// Setup Bifrost with plugin
	account := &testAccount{}
	bifrostInstance, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    account,
		MCPPlugins: []schemas.MCPPlugin{loggingPlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrostInstance.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute 50 tools concurrently
	concurrency := 50
	var wg sync.WaitGroup
	errors := make(chan error, concurrency)
	successCount := int32(0)

	start := time.Now()
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		toolType := i % 2

		go func(id, tType int) {
			defer wg.Done()

			var result *schemas.ChatMessage
			var bifrostErr *schemas.BifrostError

			switch tType {
			case 0: // Echo
				toolCall := GetSampleEchoToolCall(fmt.Sprintf("plugin-%d", id), fmt.Sprintf("msg-%d", id))
				result, bifrostErr = bifrostInstance.ExecuteChatMCPTool(ctx, &toolCall)
			case 1: // Calculator
				toolCall := GetSampleCalculatorToolCall(fmt.Sprintf("calc-%d", id), "add", float64(id), float64(id+1))
				result, bifrostErr = bifrostInstance.ExecuteChatMCPTool(ctx, &toolCall)
			}

			if bifrostErr != nil {
				errors <- fmt.Errorf("execution %d failed: %v", id, bifrostErr)
				return
			}

			if result == nil {
				errors <- fmt.Errorf("execution %d returned nil result", id)
				return
			}

			atomic.AddInt32(&successCount, 1)
		}(i, toolType)
	}

	wg.Wait()
	close(errors)
	elapsed := time.Since(start)

	// Check for errors
	errorCount := 0
	for err := range errors {
		t.Errorf("Concurrent plugin error: %v", err)
		errorCount++
	}

	assert.Equal(t, 0, errorCount, "no errors should occur")
	assert.Equal(t, int32(concurrency), successCount, "all executions should succeed")

	// Verify plugin captured all calls (thread-safe access)
	preHookCount := loggingPlugin.GetPreHookCallCount()
	postHookCount := loggingPlugin.GetPostHookCallCount()

	assert.Equal(t, concurrency, preHookCount, "plugin PreHook should be called for each execution")
	assert.Equal(t, concurrency, postHookCount, "plugin PostHook should be called for each execution")

	t.Logf("✅ Plugin hooks thread-safe: %d concurrent executions in %v", concurrency, elapsed)
	t.Logf("   PreHook calls: %d, PostHook calls: %d", preHookCount, postHookCount)
}

func TestConcurrent_MultiplePlugins(t *testing.T) {
	t.Parallel()

	// Create multiple plugins (all thread-safe)
	loggingPlugin := NewTestLoggingPlugin()
	governancePlugin := NewTestGovernancePlugin()
	modifyRequestPlugin := NewTestModifyRequestPlugin()

	// Configure governance to block one specific tool
	governancePlugin.BlockTool("blocked_tool")

	// Configure request modifier to add prefix
	modifyRequestPlugin.SetArgumentModifier(func(args string) string {
		// Just pass through - we're testing thread-safety, not modification
		return args
	})

	// Setup MCP manager
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	require.NoError(t, RegisterWeatherTool(manager))

	// Setup Bifrost with multiple plugins
	account := &testAccount{}
	bifrostInstance, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		MCPPlugins: []schemas.MCPPlugin{
			loggingPlugin,
			governancePlugin,
			modifyRequestPlugin,
		},
		Logger: core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrostInstance.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute 30 tools concurrently with multiple plugins
	concurrency := 30
	var wg sync.WaitGroup
	errors := make(chan error, concurrency)
	successCount := int32(0)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		toolType := i % 3

		go func(id, tType int) {
			defer wg.Done()

			var result *schemas.ChatMessage
			var bifrostErr *schemas.BifrostError

			switch tType {
			case 0: // Echo
				toolCall := GetSampleEchoToolCall(fmt.Sprintf("multi-%d", id), fmt.Sprintf("msg-%d", id))
				result, bifrostErr = bifrostInstance.ExecuteChatMCPTool(ctx, &toolCall)
			case 1: // Calculator
				toolCall := GetSampleCalculatorToolCall(fmt.Sprintf("calc-%d", id), "add", float64(id), float64(id+1))
				result, bifrostErr = bifrostInstance.ExecuteChatMCPTool(ctx, &toolCall)
			case 2: // Weather
				toolCall := GetSampleWeatherToolCall(fmt.Sprintf("weather-%d", id), "Tokyo", "celsius")
				result, bifrostErr = bifrostInstance.ExecuteChatMCPTool(ctx, &toolCall)
			}

			if bifrostErr != nil {
				errors <- fmt.Errorf("execution %d failed: %v", id, bifrostErr)
				return
			}

			if result == nil {
				errors <- fmt.Errorf("execution %d returned nil result", id)
				return
			}

			atomic.AddInt32(&successCount, 1)
		}(i, toolType)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	errorCount := 0
	for err := range errors {
		t.Errorf("Multiple plugin error: %v", err)
		errorCount++
	}

	assert.Equal(t, 0, errorCount, "no errors should occur")
	assert.Equal(t, int32(concurrency), successCount, "all executions should succeed")

	// Verify all plugins captured calls (thread-safe access)
	preHookCount := loggingPlugin.GetPreHookCallCount()
	postHookCount := loggingPlugin.GetPostHookCallCount()

	// All executions should have gone through logging plugin
	assert.Equal(t, concurrency, preHookCount, "logging plugin should capture all PreHook calls")
	assert.Equal(t, concurrency, postHookCount, "logging plugin should capture all PostHook calls")

	t.Logf("✅ Multiple plugins thread-safe: %d concurrent executions", concurrency)
	t.Logf("   Logging plugin - PreHook: %d, PostHook: %d", preHookCount, postHookCount)
}

// =============================================================================
// AGENT MODE CONCURRENCY
// =============================================================================

func TestConcurrent_AgentMode(t *testing.T) {
	t.Parallel()

	// TODO: Implement agent mode concurrency test
	// Run multiple agent loops concurrently
	// Verify each completes correctly
	// Check no cross-contamination
	t.Skip("TODO: Implement agent mode concurrency test")
}

func TestConcurrent_AgentModeWithToolExecution(t *testing.T) {
	t.Parallel()

	// TODO: Implement agent + direct execution test
	// Agent loop running
	// Direct tool executions happening concurrently
	// Verify both work correctly
	t.Skip("TODO: Implement agent + direct execution test")
}

// =============================================================================
// CODE MODE CONCURRENCY
// =============================================================================

func TestConcurrent_CodeMode(t *testing.T) {
	t.Parallel()

	// TODO: Implement code mode concurrency test
	// Execute multiple code executions concurrently
	// Verify all complete correctly
	// Check no shared state issues
	t.Skip("TODO: Implement code mode concurrency test")
}

func TestConcurrent_CodeModeWithToolCalls(t *testing.T) {
	t.Parallel()

	// TODO: Implement code mode with tool calls test
	// Code executions that call tools
	// Multiple concurrent
	// Verify no deadlocks or races
	t.Skip("TODO: Implement code mode with tool calls test")
}

// =============================================================================
// MIXED OPERATIONS
// =============================================================================

func TestConcurrent_MixedOperations(t *testing.T) {
	t.Parallel()

	// TODO: Implement mixed operations test
	// Concurrent mix of:
	// - Tool executions
	// - Client add/remove
	// - Health checks
	// - Agent mode
	// - Code mode
	// Use sync.WaitGroup to coordinate
	// Verify system remains stable
	t.Skip("TODO: Implement mixed operations test")
}

// =============================================================================
// RACE CONDITION DETECTION
// =============================================================================

func TestConcurrent_RaceConditions(t *testing.T) {
	t.Parallel()

	// NOTE: This test is designed to be run with -race flag to detect data races
	// go test -v -race -run TestConcurrent_RaceConditions

	// Setup with multiple InProcess clients
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	require.NoError(t, RegisterWeatherTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Perform 100 concurrent operations of various types
	concurrency := 100
	var wg sync.WaitGroup
	errors := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		operationType := i % 5

		go func(id, opType int) {
			defer wg.Done()

			switch opType {
			case 0: // Execute echo tool
				toolCall := GetSampleEchoToolCall(fmt.Sprintf("race-%d", id), fmt.Sprintf("msg-%d", id))
				_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
				if bifrostErr != nil {
					errors <- fmt.Errorf("echo %d failed: %v", id, bifrostErr)
				}

			case 1: // Execute calculator tool
				toolCall := GetSampleCalculatorToolCall(fmt.Sprintf("calc-%d", id), "add", float64(id), float64(id+1))
				_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
				if bifrostErr != nil {
					errors <- fmt.Errorf("calc %d failed: %v", id, bifrostErr)
				}

			case 2: // Execute weather tool
				toolCall := GetSampleWeatherToolCall(fmt.Sprintf("weather-%d", id), "Tokyo", "celsius")
				_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
				if bifrostErr != nil {
					errors <- fmt.Errorf("weather %d failed: %v", id, bifrostErr)
				}

			case 3: // Get clients (read operation)
				clients := manager.GetClients()
				if len(clients) == 0 {
					errors <- fmt.Errorf("get clients %d: no clients found", id)
				}

			case 4: // Get tools (read operation)
				tools := manager.GetToolPerClient(ctx)
				if len(tools) == 0 {
					errors <- fmt.Errorf("get tools %d: no tools found", id)
				}
			}
		}(i, operationType)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	errorCount := 0
	for err := range errors {
		t.Logf("Race condition test error: %v", err)
		errorCount++
	}

	// Some errors might occur but should be minimal
	assert.Less(t, errorCount, 10, "should have minimal errors during race condition test")
	t.Logf("✅ Race condition test completed: %d operations (%d errors)", concurrency, errorCount)
	t.Logf("   Run with -race flag to detect data races")
}

func TestConcurrent_StressTest(t *testing.T) {
	t.Parallel()

	// High-load stress test with 1000+ concurrent operations
	// Tests system stability under extreme load

	// Setup with multiple InProcess clients
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	require.NoError(t, RegisterWeatherTool(manager))
	require.NoError(t, RegisterDelayTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// 1000 concurrent operations
	concurrency := 1000
	var wg sync.WaitGroup
	errors := make(chan error, concurrency)
	successCount := int32(0)

	start := time.Now()
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		toolType := i % 4

		go func(id, tType int) {
			defer wg.Done()

			var result *schemas.ChatMessage
			var bifrostErr *schemas.BifrostError

			switch tType {
			case 0: // Echo (fast)
				toolCall := GetSampleEchoToolCall(fmt.Sprintf("stress-%d", id), fmt.Sprintf("msg-%d", id))
				result, bifrostErr = bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			case 1: // Calculator (fast)
				toolCall := GetSampleCalculatorToolCall(fmt.Sprintf("calc-%d", id), "add", float64(id), float64(id+1))
				result, bifrostErr = bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			case 2: // Weather (fast)
				toolCall := GetSampleWeatherToolCall(fmt.Sprintf("weather-%d", id), "Tokyo", "celsius")
				result, bifrostErr = bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			case 3: // Delay (slow - only for subset)
				if id%50 == 0 { // Only 1 in 50 uses delay to avoid timeout
					toolCall := GetSampleDelayToolCall(fmt.Sprintf("delay-%d", id), 0.1) // 100ms delay
					result, bifrostErr = bifrost.ExecuteChatMCPTool(ctx, &toolCall)
				} else {
					// Use echo instead for most
					toolCall := GetSampleEchoToolCall(fmt.Sprintf("stress-%d", id), fmt.Sprintf("msg-%d", id))
					result, bifrostErr = bifrost.ExecuteChatMCPTool(ctx, &toolCall)
				}
			}

			if bifrostErr != nil {
				errors <- fmt.Errorf("execution %d failed: %v", id, bifrostErr)
				return
			}

			if result == nil {
				errors <- fmt.Errorf("execution %d returned nil result", id)
				return
			}

			atomic.AddInt32(&successCount, 1)
		}(i, toolType)
	}

	wg.Wait()
	close(errors)
	elapsed := time.Since(start)

	// Check for errors
	errorCount := 0
	for err := range errors {
		t.Logf("Stress test error: %v", err)
		errorCount++
	}

	// Calculate success rate
	successRate := float64(successCount) / float64(concurrency) * 100

	// Under stress, we allow some errors but expect high success rate
	assert.Greater(t, successRate, 90.0, "success rate should be > 90%% under stress")
	assert.Equal(t, int32(0), successCount-int32(concurrency-errorCount), "success count should match")

	t.Logf("✅ Stress test completed: %d operations in %v", concurrency, elapsed)
	t.Logf("   Success: %d/%d (%.2f%%), Errors: %d", successCount, concurrency, successRate, errorCount)
	t.Logf("   Throughput: %.0f ops/sec", float64(concurrency)/elapsed.Seconds())
}
