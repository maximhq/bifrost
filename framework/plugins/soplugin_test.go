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
		Plugins: []PluginConfig{
			{
				Path:    pluginPath,
				Name:    "hello-world",
				Enabled: true,
				Config:  map[string]interface{}{"test": "config"},
			},
		},
	}

	loader := &SharedObjectPluginLoader{}
	basePlugins, err := LoadPlugins(loader, config)
	require.NoError(t, err, "Failed to load plugins")
	require.Len(t, basePlugins, 1, "Expected exactly one plugin to be loaded")

	plugins := FilterLLMPlugins(basePlugins)
	require.Len(t, plugins, 1, "Expected plugin to implement LLMPlugin")
	plugin := plugins[0]

	// Test GetName
	t.Run("GetName", func(t *testing.T) {
		name := plugin.GetName()
		assert.Equal(t, "Hello World Plugin", name, "Plugin name should match")
	})

	// Test HTTPTransportPreHook
	t.Run("HTTPTransportPreHook", func(t *testing.T) {
		ctx := context.Background()
		pluginCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, 10*time.Second)
		defer cancel()

		// Create a test HTTP request
		req := &schemas.HTTPRequest{
			Method: "POST",
			Path:   "/api",
			Headers: map[string]string{
				"Content-Type":  "application/json",
				"Authorization": "Bearer token123",
			},
			Query: map[string]string{},
			Body:  []byte(`{"test": "data"}`),
		}

		// Call HTTPTransportPreHook
		httpTransportPlugin, ok := plugin.(schemas.HTTPTransportPlugin)
		require.True(t, ok, "Plugin should be a HTTPTransportPlugin")
		resp, err := httpTransportPlugin.HTTPTransportPreHook(pluginCtx, req)
		require.NoError(t, err, "HTTPTransportPreHook should not return error")
		assert.Nil(t, resp, "HTTPTransportPreHook should return nil response to continue")

		// Verify headers were modified (hello-world plugin adds a header)
		assert.Equal(t, "transport-pre-hook-value", req.Headers["x-hello-world-plugin"], "Plugin should have added custom header")
	})

	// Test HTTPTransportPostHook
	t.Run("HTTPTransportPostHook", func(t *testing.T) {
		ctx := context.Background()
		pluginCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, 10*time.Second)
		defer cancel()

		// Create a test HTTP response
		req := &schemas.HTTPRequest{
			Method: "POST",
			Path:   "/api",
			Headers: map[string]string{
				"Content-Type":  "application/json",
				"Authorization": "Bearer token123",
			},
			Query: map[string]string{},
			Body:  []byte(`{"test": "data"}`),
		}
		resp := &schemas.HTTPResponse{
			StatusCode: 200,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body: []byte(`{"result": "success"}`),
		}

		// Call HTTPTransportPostHook
		httpTransportPlugin, ok := plugin.(schemas.HTTPTransportPlugin)
		require.True(t, ok, "Plugin should be a HTTPTransportPlugin")
		err := httpTransportPlugin.HTTPTransportPostHook(pluginCtx, req, resp)
		require.NoError(t, err, "HTTPTransportPostHook should not return error")
		// Verify headers were modified (hello-world plugin adds a header)
		assert.Equal(t, "transport-post-hook-value", resp.Headers["x-hello-world-plugin"], "Plugin should have added custom header")
	})

	// Test PreLLMHook
	t.Run("PreLLMHook", func(t *testing.T) {
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
		modifiedReq, shortCircuit, err := plugin.PreLLMHook(pluginCtx, req)
		require.NoError(t, err, "PreLLMHook should not return error")
		assert.Nil(t, shortCircuit, "PreLLMHook should not return short circuit")
		assert.Equal(t, req, modifiedReq, "Request should be unchanged")
	})

	// Test PostLLMHook
	t.Run("PostLLMHook", func(t *testing.T) {
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
		modifiedResp, modifiedErr, err := plugin.PostLLMHook(pluginCtx, resp, bifrostErr)
		require.NoError(t, err, "PostLLMHook should not return error")
		assert.Equal(t, resp, modifiedResp, "Response should be unchanged")
		assert.Equal(t, bifrostErr, modifiedErr, "Error should be unchanged")
	})

	// Test PostLLMHook with error
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
		modifiedResp, modifiedErr, err := plugin.PostLLMHook(pluginCtx, nil, bifrostErr)
		require.NoError(t, err, "PostLLMHook should not return error")
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
		Plugins: []PluginConfig{
			{
				Path:    pluginPath,
				Name:    "hello-world",
				Enabled: false, // Plugin is disabled
				Config:  nil,
			},
		},
	}

	loader := &SharedObjectPluginLoader{}
	plugins, err := LoadPlugins(loader, config)
	require.NoError(t, err, "LoadPlugins should not error for disabled plugins")
	assert.Len(t, plugins, 0, "No plugins should be loaded when all are disabled")
}

// TestLoadPlugins_MultiplePlugins tests loading multiple plugins
func TestLoadPlugins_MultiplePlugins(t *testing.T) {
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	config := &Config{
		Plugins: []PluginConfig{
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

	loader := &SharedObjectPluginLoader{}
	plugins, err := LoadPlugins(loader, config)
	require.NoError(t, err, "LoadPlugins should succeed for multiple plugins")
	assert.Len(t, plugins, 2, "Two plugins should be loaded")

	for _, plugin := range plugins {
		assert.Equal(t, "Hello World Plugin", plugin.GetName())
	}
}

// TestLoadPlugins_InvalidPath tests loading a plugin with invalid path
func TestLoadPlugins_InvalidPath(t *testing.T) {
	config := &Config{
		Plugins: []PluginConfig{
			{
				Path:    "/nonexistent/path/plugin.so",
				Name:    "invalid-plugin",
				Enabled: true,
				Config:  nil,
			},
		},
	}

	loader := &SharedObjectPluginLoader{}
	plugins, err := LoadPlugins(loader, config)
	assert.Error(t, err, "LoadPlugins should return error for invalid path")
	assert.Nil(t, plugins, "No plugins should be loaded on error")
}

// TestLoadPlugins_EmptyConfig tests loading plugins with empty config
func TestLoadPlugins_EmptyConfig(t *testing.T) {
	config := &Config{
		Plugins: []PluginConfig{},
	}
	loader := &SharedObjectPluginLoader{}
	plugins, err := LoadPlugins(loader, config)
	require.NoError(t, err, "LoadPlugins should succeed with empty config")
	assert.Len(t, plugins, 0, "No plugins should be loaded with empty config")
}

// TestDynamicPlugin_ContextPropagation tests that context is properly propagated
func TestDynamicPlugin_ContextPropagation(t *testing.T) {
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	loader := &SharedObjectPluginLoader{}
	basePlugin, err := loader.LoadPlugin(pluginPath, nil)
	require.NoError(t, err, "Failed to load plugin")

	// Type assert to LLMPlugin
	plugin, ok := basePlugin.(schemas.LLMPlugin)
	require.True(t, ok, "Plugin should implement LLMPlugin interface")

	// Create a context with a value
	ctx := context.WithValue(context.Background(), "test-key", "test-value")

	// Test PreLLMHook with context
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: "openai",
			Model:    "gpt-4",
		},
	}
	pluginCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, _, err = plugin.PreLLMHook(pluginCtx, req)
	require.NoError(t, err, "PreLLMHook should succeed with context")

	// Test PostLLMHook with context
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID:    "test-id",
			Model: "gpt-4",
		},
	}
	_, _, err = plugin.PostLLMHook(pluginCtx, resp, nil)
	require.NoError(t, err, "PostLLMHook should succeed with context")
}

// TestDynamicPlugin_ConcurrentCalls tests concurrent plugin calls
func TestDynamicPlugin_ConcurrentCalls(t *testing.T) {
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	loader := &SharedObjectPluginLoader{}
	basePlugin, err := loader.LoadPlugin(pluginPath, nil)
	require.NoError(t, err, "Failed to load plugin")

	// Type assert to LLMPlugin
	plugin, ok := basePlugin.(schemas.LLMPlugin)
	require.True(t, ok, "Plugin should implement LLMPlugin interface")

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

			// Call PreLLMHook
			pluginCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, 10*time.Second)
			defer cancel()
			_, _, err := plugin.PreLLMHook(pluginCtx, req)
			assert.NoError(t, err, "PreLLMHook should succeed in goroutine %d", id)

			// Call PostLLMHook
			resp := &schemas.BifrostResponse{
				ChatResponse: &schemas.BifrostChatResponse{
					ID:    "test-id",
					Model: "gpt-4",
				},
			}
			_, _, err = plugin.PostLLMHook(pluginCtx, resp, nil)
			assert.NoError(t, err, "PostLLMHook should succeed in goroutine %d", id)

			// Call GetName
			name := basePlugin.GetName()
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

	// Clean and create build directory to ensure fresh build with current Go version
	buildDir := filepath.Join(absPluginDir, "build")
	os.RemoveAll(buildDir)
	err = os.MkdirAll(buildDir, 0o755)
	require.NoError(t, err, "Failed to create build directory")

	// Build the plugin directly with go build
	pluginPath := filepath.Join(buildDir, "hello-world"+pluginExt)
	args := []string{"build", "-buildmode=plugin", "-o", pluginPath}
	if raceEnabled {
		args = append(args, "-race")
	}
	args = append(args, "main.go")
	cmd := exec.Command("go", args...)
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

	loader := &SharedObjectPluginLoader{}
	plugin, err := loader.LoadPlugin(pluginPath, map[string]interface{}{
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

	loader := &SharedObjectPluginLoader{}
	plugin, err := loader.LoadPlugin(pluginPath, nil)
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

	loader := &SharedObjectPluginLoader{}
	basePlugin, err := loader.LoadPlugin(pluginPath, nil)
	require.NoError(t, err, "Failed to load plugin")

	// Type assert to LLMPlugin
	plugin, ok := basePlugin.(schemas.LLMPlugin)
	require.True(t, ok, "Plugin should implement LLMPlugin interface")

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
	modifiedReq, shortCircuit, err := plugin.PreLLMHook(pluginCtx, req)
	require.NoError(t, err, "PreLLMHook should succeed")
	assert.Nil(t, shortCircuit, "Short circuit should be nil")
	assert.NotNil(t, modifiedReq, "Modified request should not be nil")
}

// BenchmarkDynamicPlugin_PreHook benchmarks the PreLLMHook method
func BenchmarkDynamicPlugin_PreHook(b *testing.B) {
	pluginPath := buildHelloWorldPluginForBenchmark(b)
	defer cleanupHelloWorldPluginForBenchmark(b)

	loader := &SharedObjectPluginLoader{}
	basePlugin, err := loader.LoadPlugin(pluginPath, nil)
	require.NoError(b, err, "Failed to load plugin")

	// Type assert to LLMPlugin
	plugin, ok := basePlugin.(schemas.LLMPlugin)
	require.True(b, ok, "Plugin should implement LLMPlugin interface")

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
		_, _, _ = plugin.PreLLMHook(pluginCtx, req)
	}
}

// BenchmarkDynamicPlugin_PostHook benchmarks the PostLLMHook method
func BenchmarkDynamicPlugin_PostHook(b *testing.B) {
	pluginPath := buildHelloWorldPluginForBenchmark(b)
	defer cleanupHelloWorldPluginForBenchmark(b)

	loader := &SharedObjectPluginLoader{}
	basePlugin, err := loader.LoadPlugin(pluginPath, nil)
	require.NoError(b, err, "Failed to load plugin")

	// Type assert to LLMPlugin
	plugin, ok := basePlugin.(schemas.LLMPlugin)
	require.True(b, ok, "Plugin should implement LLMPlugin interface")

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
		_, _, _ = plugin.PostLLMHook(pluginCtx, resp, nil)
	}
}

// BenchmarkDynamicPlugin_GetName benchmarks the GetName method
func BenchmarkDynamicPlugin_GetName(b *testing.B) {
	pluginPath := buildHelloWorldPluginForBenchmark(b)
	defer cleanupHelloWorldPluginForBenchmark(b)

	loader := &SharedObjectPluginLoader{}
	plugin, err := loader.LoadPlugin(pluginPath, nil)
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

	// Clean and create build directory to ensure fresh build with current Go version
	buildDir := filepath.Join(absPluginDir, "build")
	pluginPath := filepath.Join(buildDir, "hello-world"+pluginExt)
	os.RemoveAll(buildDir)
	err = os.MkdirAll(buildDir, 0o755)
	require.NoError(b, err, "Failed to create build directory")

	// Build the plugin directly with go build
	args := []string{"build", "-buildmode=plugin", "-o", pluginPath}
	if raceEnabled {
		args = append(args, "-race")
	}
	args = append(args, "main.go")
	cmd := exec.Command("go", args...)
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

	loader := &SharedObjectPluginLoader{}
	plugin, err := loader.LoadPlugin(pluginPath, nil)
	require.NoError(t, err, "Failed to load plugin")

	name := plugin.GetName()
	assert.NotEmpty(t, name, "Plugin name should not be empty")
	assert.True(t, strings.Contains(name, "Plugin"), "Plugin name should contain 'Plugin'")
}

// Helper function to create a pointer to a string
func stringPtr(s string) *string {
	return &s
}

// TestLoadPlugins tests the new generic LoadPlugins function
func TestLoadPlugins(t *testing.T) {
	// Build the hello-world plugin first
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	t.Run("LoadSinglePlugin", func(t *testing.T) {
		config := &Config{
			Plugins: []PluginConfig{
				{
					Path:    pluginPath,
					Name:    "hello-world",
					Enabled: true,
					Config:  map[string]interface{}{"test": "config"},
				},
			},
		}

		loader := &SharedObjectPluginLoader{}
		plugins, err := LoadPlugins(loader, config)
		require.NoError(t, err, "Failed to load plugins")
		require.Len(t, plugins, 1, "Expected exactly one plugin to be loaded")

		plugin := plugins[0]
		assert.Equal(t, "Hello World Plugin", plugin.GetName())
	})

	t.Run("LoadMultiplePlugins", func(t *testing.T) {
		config := &Config{
			Plugins: []PluginConfig{
				{
					Path:    pluginPath,
					Name:    "hello-world-1",
					Enabled: true,
					Config:  map[string]interface{}{"test": "config1"},
				},
				{
					Path:    pluginPath,
					Name:    "hello-world-2",
					Enabled: true,
					Config:  map[string]interface{}{"test": "config2"},
				},
			},
		}

		loader := &SharedObjectPluginLoader{}
		plugins, err := LoadPlugins(loader, config)
		require.NoError(t, err, "Failed to load plugins")
		require.Len(t, plugins, 2, "Expected two plugins to be loaded")
	})

	t.Run("SkipDisabledPlugins", func(t *testing.T) {
		config := &Config{
			Plugins: []PluginConfig{
				{
					Path:    pluginPath,
					Name:    "hello-world-enabled",
					Enabled: true,
					Config:  map[string]interface{}{"test": "config"},
				},
				{
					Path:    pluginPath,
					Name:    "hello-world-disabled",
					Enabled: false,
					Config:  map[string]interface{}{"test": "config"},
				},
			},
		}

		loader := &SharedObjectPluginLoader{}
		plugins, err := LoadPlugins(loader, config)
		require.NoError(t, err, "Failed to load plugins")
		require.Len(t, plugins, 1, "Expected only enabled plugin to be loaded")
	})
}

// TestFilterPlugins tests the plugin filter functions
func TestFilterPlugins(t *testing.T) {
	// Build the hello-world plugin first
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	loader := &SharedObjectPluginLoader{}
	plugin, err := loader.LoadPlugin(pluginPath, nil)
	require.NoError(t, err, "Failed to load plugin")

	plugins := []schemas.BasePlugin{plugin}

	t.Run("FilterLLMPlugins", func(t *testing.T) {
		llmPlugins := FilterLLMPlugins(plugins)
		assert.Len(t, llmPlugins, 1, "Hello world plugin should implement LLMPlugin")
	})

	t.Run("FilterHTTPTransportPlugins", func(t *testing.T) {
		httpPlugins := FilterHTTPTransportPlugins(plugins)
		assert.Len(t, httpPlugins, 1, "Hello world plugin should implement HTTPTransportPlugin")
	})

	t.Run("FilterMCPPlugins", func(t *testing.T) {
		mcpPlugins := FilterMCPPlugins(plugins)
		assert.Len(t, mcpPlugins, 0, "Hello world plugin does not implement MCPPlugin")
	})

	t.Run("FilterObservabilityPlugins", func(t *testing.T) {
		obsPlugins := FilterObservabilityPlugins(plugins)
		assert.Len(t, obsPlugins, 0, "Hello world plugin does not implement ObservabilityPlugin")
	})
}

// TestLoadPluginWithOptionalHooks tests that plugins can implement only a subset of hooks
func TestLoadPluginWithOptionalHooks(t *testing.T) {
	// Build the hello-world plugin first
	pluginPath := buildHelloWorldPlugin(t)
	defer cleanupHelloWorldPlugin(t)

	loader := &SharedObjectPluginLoader{}
	plugin, err := loader.LoadPlugin(pluginPath, nil)
	require.NoError(t, err, "Failed to load plugin")

	// The plugin should load successfully even if it doesn't implement all hooks
	assert.NotNil(t, plugin, "Plugin should be loaded")

	// Test that DynamicPlugin properly handles unimplemented methods by returning no-op values
	dynamicPlugin, ok := plugin.(*DynamicPlugin)
	require.True(t, ok, "Plugin should be a DynamicPlugin")

	// Test MCP hooks (not implemented by hello-world plugin)
	t.Run("UnimplementedMCPHooks", func(t *testing.T) {
		ctx := context.Background()
		pluginCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, 10*time.Second)
		defer cancel()

		// PreMCPHook should return no-op values
		mcpReq := &schemas.BifrostMCPRequest{}
		returnedReq, shortCircuit, err := dynamicPlugin.PreMCPHook(pluginCtx, mcpReq)
		assert.NoError(t, err, "PreMCPHook should not error for unimplemented hook")
		assert.Equal(t, mcpReq, returnedReq, "PreMCPHook should return original request")
		assert.Nil(t, shortCircuit, "PreMCPHook should return nil short circuit")

		// PostMCPHook should return no-op values
		mcpResp := &schemas.BifrostMCPResponse{}
		bifrostErr := &schemas.BifrostError{}
		returnedResp, returnedErr, hookErr := dynamicPlugin.PostMCPHook(pluginCtx, mcpResp, bifrostErr)
		assert.NoError(t, hookErr, "PostMCPHook should not error for unimplemented hook")
		assert.Equal(t, mcpResp, returnedResp, "PostMCPHook should return original response")
		assert.Equal(t, bifrostErr, returnedErr, "PostMCPHook should return original error")
	})

	// Test Observability hooks (not implemented by hello-world plugin)
	t.Run("UnimplementedObservabilityHooks", func(t *testing.T) {
		ctx := context.Background()
		trace := &schemas.Trace{}
		err := dynamicPlugin.Inject(ctx, trace)
		assert.NoError(t, err, "Inject should not error for unimplemented hook")
	})
}
