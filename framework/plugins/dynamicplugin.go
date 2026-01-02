package plugins

import (
	"fmt"
	"os"
	"plugin"
	"strings"
	"time"

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

	getName                 func() string
	httpTransportMiddleware func() schemas.BifrostHTTPMiddleware
	preHook                 func(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error)
	postHook                func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)
	cleanup                 func() error
}

// GetName returns the name of the plugin
func (dp *DynamicPlugin) GetName() string {
	return dp.getName()
}

// HTTPTransportMiddleware returns the HTTP transport middleware function for this plugin
func (dp *DynamicPlugin) HTTPTransportMiddleware() schemas.BifrostHTTPMiddleware {
	if dp.httpTransportMiddleware == nil {
		return nil
	}
	return dp.httpTransportMiddleware()
}

// PreHook is not used for dynamic plugins
func (dp *DynamicPlugin) PreHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	return dp.preHook(ctx, req)
}

// PostHook is not used for dynamic plugins
func (dp *DynamicPlugin) PostHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
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
		req := fasthttp.AcquireRequest()
		defer fasthttp.ReleaseRequest(req)
		response := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseResponse(response)

		req.SetRequestURI(dp.Path)
		req.Header.SetMethod(fasthttp.MethodGet)
		req.Header.Set("Accept", "application/octet-stream")
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		err := fasthttp.DoTimeout(req, response, 120*time.Second)
		if err != nil {
			return nil, err
		}
		if response.StatusCode() != fasthttp.StatusOK {
			return nil, fmt.Errorf("failed to download plugin: %d", response.StatusCode())
		}
		// Create a unique temporary file for the plugin
		tempFile, err := os.CreateTemp(os.TempDir(), "bifrost-plugin-*.so")
		if err != nil {
			return nil, fmt.Errorf("failed to create temporary file: %w", err)
		}
		tempPath := tempFile.Name()
		// Write the downloaded body to the temporary file
		_, err = tempFile.Write(response.Body())
		if err != nil {
			tempFile.Close()
			os.Remove(tempPath)
			return nil, fmt.Errorf("failed to write plugin to temporary file: %w", err)
		}
		// Close the file
		err = tempFile.Close()
		if err != nil {
			os.Remove(tempPath)
			return nil, fmt.Errorf("failed to close temporary file: %w", err)
		}
		// Set file permissions to be executable
		err = os.Chmod(tempPath, 0755)
		if err != nil {
			os.Remove(tempPath)
			return nil, fmt.Errorf("failed to set executable permissions on plugin: %w", err)
		}
		dp.Path = tempPath
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
	// Looking up for HTTPTransportMiddleware method
	httpTransportMiddlewareSym, err := plugin.Lookup("HTTPTransportMiddleware")
	if err != nil {
		return nil, err
	}
	if dp.httpTransportMiddleware, ok = httpTransportMiddlewareSym.(func() schemas.BifrostHTTPMiddleware); !ok {
		return nil, fmt.Errorf("failed to cast HTTPTransportMiddleware to func() schemas.BifrostHTTPMiddleware")
	}
	// Looking up for PreHook method
	preHookSym, err := plugin.Lookup("PreHook")
	if err != nil {
		return nil, err
	}
	if dp.preHook, ok = preHookSym.(func(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error)); !ok {
		return nil, fmt.Errorf("failed to cast PreHook to func(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error)")
	}
	// Looking up for PostHook method
	postHookSym, err := plugin.Lookup("PostHook")
	if err != nil {
		return nil, err
	}
	if dp.postHook, ok = postHookSym.(func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)); !ok {
		return nil, fmt.Errorf("failed to cast PostHook to func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)")
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
