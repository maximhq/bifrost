package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"plugin"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// DynamicPlugin is the interface for a dynamic plugin
type DynamicPlugin struct {
	Enabled bool
	Path    string

	Config any

	filename string
	plugin   *plugin.Plugin

	getName              func() string
	transportInterceptor func(ctx *context.Context, url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error)
	preHook              func(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error)
	postHook             func(ctx *context.Context, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)
	cleanup              func() error
}

// GetName returns the name of the plugin
func (dp *DynamicPlugin) GetName() string {
	return dp.getName()
}

// TransportInterceptor is not used for dynamic plugins
func (dp *DynamicPlugin) TransportInterceptor(ctx *context.Context, url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error) {
	return dp.transportInterceptor(ctx, url, headers, body)
}

// PreHook is not used for dynamic plugins
func (dp *DynamicPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	return dp.preHook(ctx, req)
}

// PostHook is not used for dynamic plugins
func (dp *DynamicPlugin) PostHook(ctx *context.Context, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return dp.postHook(ctx, resp, bifrostErr)
}

// Cleanup is not used for dynamic plugins
func (dp *DynamicPlugin) Cleanup() error {
	return dp.cleanup()
}

// loadDynamicPlugin loads a dynamic plugin from a path
func loadDynamicPlugin(path string, config any) (schemas.Plugin, error) {
	dp := &DynamicPlugin{
		Path: path,
	}
	// Checking if path is URL or file path
	if strings.HasPrefix(dp.Path, "http") {
		// Download the file
		response := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseResponse(response)
		statusCode, body, err := fasthttp.Get(nil, dp.Path)
		if err != nil {
			return nil, err
		}
		if statusCode != fasthttp.StatusOK {
			return nil, fmt.Errorf("failed to download plugin: %d", statusCode)
		}
		// Saving the file to a temporary directory
		tempDir := os.TempDir()
		tempFile := filepath.Join(tempDir, "bifrost-plugin.zip")
		err = os.WriteFile(tempFile, body, 0644)
		if err != nil {
			return nil, err
		}
		dp.Path = tempFile
	}
	plugin, err := plugin.Open(dp.Path)
	if err != nil {
		return nil, err
	}
	ok := false
	// Looking up for optional Init method
	initSym, err := plugin.Lookup("Init")
	if err != nil {
		if strings.Contains(err.Error(), "symbol Init not found") {
			initSym = nil
		} else {
			return nil, err
		}
	}
	if initSym != nil {
		initFunc, ok := initSym.(func(config any) error)
		if !ok {
			return nil, fmt.Errorf("failed to cast Init to func(config any) error")
		}
		err := initFunc(config)
		if err != nil {
			return nil, err
		}
	}
	// Looking up for GetName method
	getNameSym, err := plugin.Lookup("GetName")
	if err != nil {
		return nil, err
	}
	if dp.getName, ok = getNameSym.(func() string); !ok {
		return nil, fmt.Errorf("failed to cast GetName to func() string")
	}
	// Looking up for TransportInterceptor method
	transportInterceptorSym, err := plugin.Lookup("TransportInterceptor")
	if err != nil {
		return nil, err
	}
	if dp.transportInterceptor, ok = transportInterceptorSym.(func(ctx *context.Context, url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error)); !ok {
		return nil, fmt.Errorf("failed to cast TransportInterceptor to func(ctx *context.Context, url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error)")
	}
	// Looking up for PreHook method
	preHookSym, err := plugin.Lookup("PreHook")
	if err != nil {
		return nil, err
	}
	if dp.preHook, ok = preHookSym.(func(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error)); !ok {
		return nil, fmt.Errorf("failed to cast PreHook to func(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error)")
	}
	// Looking up for PostHook method
	postHookSym, err := plugin.Lookup("PostHook")
	if err != nil {
		return nil, err
	}
	if dp.postHook, ok = postHookSym.(func(ctx *context.Context, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)); !ok {
		return nil, fmt.Errorf("failed to cast PostHook to func(ctx *context.Context, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)")
	}
	// Looking up for Cleanup method
	cleanupSym, err := plugin.Lookup("Cleanup")
	if err != nil {
		return nil, err
	}
	if dp.cleanup, ok = cleanupSym.(func() error); !ok {
		return nil, fmt.Errorf("failed to cast Cleanup to func() error")
	}
	dp.plugin = plugin
	return dp, nil
}
