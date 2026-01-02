package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

const (
	helloWorldPluginDir = "../../examples/plugins/hello-world"
	helloWorldBuildDir  = "../../examples/plugins/hello-world/build"
)

// TestDynamicPluginLifecycle tests the complete lifecycle of a dynamic plugin
func TestDynamicPluginLifecycle(t *testing.T) {
	// Build the hello-world plugin first
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	// Test loading the plugin
	config := &Config{
		Plugins: []DynamicPluginConfig{
			{
				Path:    pluginPath,
				Name:    "hello-world",
				Enabled: true,
				Config:  map[string]interface{}{"test": "config"},
			},
		},
	}

	plugins, err := LoadPlugins(config)
	require.NoError(t, err, "Failed to load plugins")
	require.Len(t, plugins, 1, "Expected exactly one plugin to be loaded")

	plugin := plugins[0]

	// Test GetName
	t.Run("GetName", func(t *testing.T) {
		name := plugin.GetName()
		assert.Equal(t, "Hello World Plugin", name, "Plugin name should match")
	})

	// Test HTTPTransportMiddleware
	t.Run("HTTPTransportMiddleware", func(t *testing.T) {
		// Track if the next handler was called
		nextHandlerCalled := false

		// Create a mock next handler
		nextHandler := func(ctx *fasthttp.RequestCtx) {
			nextHandlerCalled = true
		}

		// Get the middleware function
		middleware := plugin.HTTPTransportMiddleware()
		require.NotNil(t, middleware, "HTTPTransportMiddleware should return a middleware function")

		// Wrap the next handler with the middleware
		wrappedHandler := middleware(nextHandler)
		require.NotNil(t, wrappedHandler, "Middleware should return a wrapped handler")

		// Create a test request context
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("http://example.com/api")
		ctx.Request.Header.SetMethod("POST")
		ctx.Request.Header.Set("Content-Type", "application/json")
		ctx.Request.Header.Set("Authorization", "Bearer token123")

		// Call the wrapped handler
		wrappedHandler(ctx)

		// Verify the next handler was called
		assert.True(t, nextHandlerCalled, "Next handler should have been called")
	})

	// Test PreHook
	t.Run("PreHook", func(t *testing.T) {
		ctx := context.Background()
		req := &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: "openai",
				Model:    "gpt-4",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: stringPtr("Hello"),
						},
					},
				},
			},
		}

		pluginCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, 10*time.Second)
		defer cancel()
		modifiedReq, shortCircuit, err := plugin.PreHook(pluginCtx, req)
		require.NoError(t, err, "PreHook should not return error")
		assert.Nil(t, shortCircuit, "PreHook should not return short circuit")
		assert.Equal(t, req, modifiedReq, "Request should be unchanged")
	})

	// Test PostHook
	t.Run("PostHook", func(t *testing.T) {
		ctx := context.Background()
		resp := &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{
				ID:    "test-id",
				Model: "gpt-4",
				Choices: []schemas.BifrostResponseChoice{
					{
						Index: 0,
						ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
							Message: &schemas.ChatMessage{
								Role: schemas.ChatMessageRoleAssistant,
								Content: &schemas.ChatMessageContent{
									ContentStr: stringPtr("Hello! How can I help you?"),
								},
							},
						},
					},
				},
			},
		}
		bifrostErr := (*schemas.BifrostError)(nil)
		pluginCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, 10*time.Second)
		defer cancel()
		modifiedResp, modifiedErr, err := plugin.PostHook(pluginCtx, resp, bifrostErr)
		require.NoError(t, err, "PostHook should not return error")
		assert.Equal(t, resp, modifiedResp, "Response should be unchanged")
		assert.Equal(t, bifrostErr, modifiedErr, "Error should be unchanged")
	})

	// Test PostHook with error
	t.Run("PostHook_WithError", func(t *testing.T) {
		ctx := context.Background()
		statusCode := 500
		bifrostErr := &schemas.BifrostError{
			StatusCode: &statusCode,
			Error: &schemas.ErrorField{
				Message: "Test error",
			},
		}

		pluginCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, 10*time.Second)
		defer cancel()
		modifiedResp, modifiedErr, err := plugin.PostHook(pluginCtx, nil, bifrostErr)
		require.NoError(t, err, "PostHook should not return error")
		assert.Nil(t, modifiedResp, "Response should be nil")
		assert.Equal(t, bifrostErr, modifiedErr, "Error should be unchanged")
	})

	// Test Cleanup
	t.Run("Cleanup", func(t *testing.T) {
		err := plugin.Cleanup()
		assert.NoError(t, err, "Cleanup should not return error")
	})
}

// TestLoadPlugins_DisabledPlugin tests that disabled plugins are not loaded
func TestLoadPlugins_DisabledPlugin(t *testing.T) {
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	config := &Config{
		Plugins: []DynamicPluginConfig{
			{
				Path:    pluginPath,
				Name:    "hello-world",
				Enabled: false, // Plugin is disabled
				Config:  nil,
			},
		},
	}

	plugins, err := LoadPlugins(config)
	require.NoError(t, err, "LoadPlugins should not error for disabled plugins")
	assert.Len(t, plugins, 0, "No plugins should be loaded when all are disabled")
}

// TestLoadPlugins_MultiplePlugins tests loading multiple plugins
func TestLoadPlugins_MultiplePlugins(t *testing.T) {
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	config := &Config{
		Plugins: []DynamicPluginConfig{
			{
				Path:    pluginPath,
				Name:    "hello-world-1",
				Enabled: true,
				Config:  nil,
			},
			{
				Path:    pluginPath,
				Name:    "hello-world-2",
				Enabled: true,
				Config:  map[string]interface{}{"key": "value"},
			},
		},
	}

	plugins, err := LoadPlugins(config)
	require.NoError(t, err, "LoadPlugins should succeed for multiple plugins")
	assert.Len(t, plugins, 2, "Two plugins should be loaded")

	for _, plugin := range plugins {
		assert.Equal(t, "Hello World Plugin", plugin.GetName())
	}
}

// TestLoadPlugins_InvalidPath tests loading a plugin with invalid path
func TestLoadPlugins_InvalidPath(t *testing.T) {
	config := &Config{
		Plugins: []DynamicPluginConfig{
			{
				Path:    "/nonexistent/path/plugin.so",
				Name:    "invalid-plugin",
				Enabled: true,
				Config:  nil,
			},
		},
	}

	plugins, err := LoadPlugins(config)
	assert.Error(t, err, "LoadPlugins should return error for invalid path")
	assert.Nil(t, plugins, "No plugins should be loaded on error")
}

// TestLoadPlugins_EmptyConfig tests loading plugins with empty config
func TestLoadPlugins_EmptyConfig(t *testing.T) {
	config := &Config{
		Plugins: []DynamicPluginConfig{},
	}

	plugins, err := LoadPlugins(config)
	require.NoError(t, err, "LoadPlugins should succeed with empty config")
	assert.Len(t, plugins, 0, "No plugins should be loaded with empty config")
}

// TestDynamicPlugin_ContextPropagation tests that context is properly propagated
func TestDynamicPlugin_ContextPropagation(t *testing.T) {
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	plugin, err := loadDynamicPlugin(pluginPath, nil)
	require.NoError(t, err, "Failed to load plugin")

	// Create a context with a value
	ctx := context.WithValue(context.Background(), "test-key", "test-value")

	// Test PreHook with context
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: "openai",
			Model:    "gpt-4",
		},
	}
	pluginCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, _, err = plugin.PreHook(pluginCtx, req)
	require.NoError(t, err, "PreHook should succeed with context")

	// Test PostHook with context
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID:    "test-id",
			Model: "gpt-4",
		},
	}
	_, _, err = plugin.PostHook(pluginCtx, resp, nil)
	require.NoError(t, err, "PostHook should succeed with context")
}

// TestDynamicPlugin_ConcurrentCalls tests concurrent plugin calls
func TestDynamicPlugin_ConcurrentCalls(t *testing.T) {
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	plugin, err := loadDynamicPlugin(pluginPath, nil)
	require.NoError(t, err, "Failed to load plugin")

	// Run multiple goroutines calling plugin methods
	const numGoroutines = 10
	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()

			ctx := context.Background()
			req := &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Provider: "openai",
					Model:    "gpt-4",
				},
			}

			// Call PreHook
			pluginCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, 10*time.Second)
			defer cancel()
			_, _, err := plugin.PreHook(pluginCtx, req)
			assert.NoError(t, err, "PreHook should succeed in goroutine %d", id)

			// Call PostHook
			resp := &schemas.BifrostResponse{
				ChatResponse: &schemas.BifrostChatResponse{
					ID:    "test-id",
					Model: "gpt-4",
				},
			}
			_, _, err = plugin.PostHook(pluginCtx, resp, nil)
			assert.NoError(t, err, "PostHook should succeed in goroutine %d", id)

			// Call GetName
			name := plugin.GetName()
			assert.Equal(t, "Hello World Plugin", name, "GetName should return correct name in goroutine %d", id)
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines; i++ {
		<-done
	}
}

// Helper function to build the hello-world plugin
func buildHelloWorldPlugin(t *testing.T) string {
	t.Helper()

	// Get absolute path to the hello-world plugin directory
	absPluginDir, err := filepath.Abs(helloWorldPluginDir)
	require.NoError(t, err, "Failed to get absolute path")

	// Determine plugin extension based on OS
	pluginExt := ".so"
	if runtime.GOOS == "windows" {
		pluginExt = ".dll"
	}

	// Create build directory
	buildDir := filepath.Join(absPluginDir, "build")
	err = os.MkdirAll(buildDir, 0755)
	require.NoError(t, err, "Failed to create build directory")

	// Build the plugin directly with go build
	pluginPath := filepath.Join(buildDir, "hello-world"+pluginExt)
	cmd := exec.Command("go", "build", "-buildmode=plugin", "-o", pluginPath, "main.go")
	cmd.Dir = absPluginDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("Build output: %s", string(output))
		require.NoError(t, err, "Failed to build hello-world plugin")
	}

	// Verify the plugin was built
	_, err = os.Stat(pluginPath)
	require.NoError(t, err, "Plugin file should exist after build")

	return pluginPath
}

// Helper function to clean up the hello-world plugin build
func cleanupHelloWorldPlugin(t *testing.T) {
	t.Helper()

	absPluginDir, err := filepath.Abs(helloWorldPluginDir)
	if err != nil {
		t.Logf("Failed to get absolute path for cleanup: %v", err)
		return
	}

	cmd := exec.Command("make", "clean")
	cmd.Dir = absPluginDir
	if err := cmd.Run(); err != nil {
		t.Logf("Failed to clean hello-world plugin: %v", err)
	}
}

// TestLoadDynamicPlugin_DirectCall tests loading a plugin directly
func TestLoadDynamicPlugin_DirectCall(t *testing.T) {
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	plugin, err := loadDynamicPlugin(pluginPath, map[string]interface{}{
		"test": "config",
	})
	require.NoError(t, err, "loadDynamicPlugin should succeed")
	assert.NotNil(t, plugin, "Plugin should not be nil")

	// Verify it's a DynamicPlugin
	dynamicPlugin, ok := plugin.(*DynamicPlugin)
	assert.True(t, ok, "Plugin should be a DynamicPlugin")
	assert.Equal(t, pluginPath, dynamicPlugin.Path)
}

// TestDynamicPlugin_NilConfig tests loading a plugin with nil config
func TestDynamicPlugin_NilConfig(t *testing.T) {
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	plugin, err := loadDynamicPlugin(pluginPath, nil)
	require.NoError(t, err, "loadDynamicPlugin should succeed with nil config")
	assert.NotNil(t, plugin, "Plugin should not be nil")

	// Verify plugin works correctly
	name := plugin.GetName()
	assert.Equal(t, "Hello World Plugin", name)
}

// TestDynamicPlugin_ShortCircuitNil tests that nil short circuit is handled properly
func TestDynamicPlugin_ShortCircuitNil(t *testing.T) {
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	plugin, err := loadDynamicPlugin(pluginPath, nil)
	require.NoError(t, err, "Failed to load plugin")

	ctx := context.Background()
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: "openai",
			Model:    "gpt-4",
		},
	}

	pluginCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, 10*time.Second)
	defer cancel()
	modifiedReq, shortCircuit, err := plugin.PreHook(pluginCtx, req)
	require.NoError(t, err, "PreHook should succeed")
	assert.Nil(t, shortCircuit, "Short circuit should be nil")
	assert.NotNil(t, modifiedReq, "Modified request should not be nil")
}

// BenchmarkDynamicPlugin_PreHook benchmarks the PreHook method
func BenchmarkDynamicPlugin_PreHook(b *testing.B) {
	pluginPath := buildHelloWorldPluginForBenchmark(b)
	defer cleanupHelloWorldPluginForBenchmark(b)

	plugin, err := loadDynamicPlugin(pluginPath, nil)
	require.NoError(b, err, "Failed to load plugin")

	ctx := context.Background()
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: "openai",
			Model:    "gpt-4",
		},
	}

	b.ResetTimer()
	pluginCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, 10*time.Second)
	defer cancel()
	for i := 0; i < b.N; i++ {
		_, _, _ = plugin.PreHook(pluginCtx, req)
	}
}

// BenchmarkDynamicPlugin_PostHook benchmarks the PostHook method
func BenchmarkDynamicPlugin_PostHook(b *testing.B) {
	pluginPath := buildHelloWorldPluginForBenchmark(b)
	defer cleanupHelloWorldPluginForBenchmark(b)

	plugin, err := loadDynamicPlugin(pluginPath, nil)
	require.NoError(b, err, "Failed to load plugin")

	ctx := context.Background()
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID:    "test-id",
			Model: "gpt-4",
		},
	}
	pluginCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, 10*time.Second)
	defer cancel()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = plugin.PostHook(pluginCtx, resp, nil)
	}
}

// BenchmarkDynamicPlugin_GetName benchmarks the GetName method
func BenchmarkDynamicPlugin_GetName(b *testing.B) {
	pluginPath := buildHelloWorldPluginForBenchmark(b)
	defer cleanupHelloWorldPluginForBenchmark(b)

	plugin, err := loadDynamicPlugin(pluginPath, nil)
	require.NoError(b, err, "Failed to load plugin")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = plugin.GetName()
	}
}

// Helper function to build plugin for benchmarks
func buildHelloWorldPluginForBenchmark(b *testing.B) string {
	b.Helper()

	absPluginDir, err := filepath.Abs(helloWorldPluginDir)
	require.NoError(b, err, "Failed to get absolute path")

	pluginExt := ".so"
	if runtime.GOOS == "windows" {
		pluginExt = ".dll"
	}

	// Check if plugin already exists
	pluginPath := filepath.Join(absPluginDir, "build", "hello-world"+pluginExt)
	if _, err := os.Stat(pluginPath); err == nil {
		return pluginPath
	}

	// Create build directory
	buildDir := filepath.Join(absPluginDir, "build")
	err = os.MkdirAll(buildDir, 0755)
	require.NoError(b, err, "Failed to create build directory")

	// Build the plugin directly with go build
	cmd := exec.Command("go", "build", "-buildmode=plugin", "-o", pluginPath, "main.go")
	cmd.Dir = absPluginDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		b.Logf("Build output: %s", string(output))
		require.NoError(b, err, "Failed to build hello-world plugin")
	}

	return pluginPath
}

// Helper function to clean up plugin for benchmarks
func cleanupHelloWorldPluginForBenchmark(b *testing.B) {
	b.Helper()

	absPluginDir, err := filepath.Abs(helloWorldPluginDir)
	if err != nil {
		b.Logf("Failed to get absolute path for cleanup: %v", err)
		return
	}

	cmd := exec.Command("make", "clean")
	cmd.Dir = absPluginDir
	if err := cmd.Run(); err != nil {
		b.Logf("Failed to clean hello-world plugin: %v", err)
	}
}

// TestDynamicPlugin_GetNameNotEmpty tests that GetName returns non-empty string
func TestDynamicPlugin_GetNameNotEmpty(t *testing.T) {
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	plugin, err := loadDynamicPlugin(pluginPath, nil)
	require.NoError(t, err, "Failed to load plugin")

	name := plugin.GetName()
	assert.NotEmpty(t, name, "Plugin name should not be empty")
	assert.True(t, strings.Contains(name, "Plugin"), "Plugin name should contain 'Plugin'")
}

// Helper function to create a pointer to a string
func stringPtr(s string) *string {
	return &s
}
