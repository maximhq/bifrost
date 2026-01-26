package plugins

import (
	"fmt"
	"plugin"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// SharedObjectPluginLoader is the loader for shared object plugins
type SharedObjectPluginLoader struct{}

// LoadDynamicPlugin loads a dynamic plugin from a shared object file
func (l *SharedObjectPluginLoader) LoadDynamicPlugin(path string, config any) (schemas.Plugin, error) {
	dp := &DynamicPlugin{
		Path: path,
	}
	// Checking if path is URL or file path
	if strings.HasPrefix(dp.Path, "http") {
		// Download the file
		tempPath, err := DownloadPlugin(dp.Path, ".so")
		if err != nil {
			return nil, err
		}
		dp.Path = tempPath
	}
	// For allowing reloads, we replace
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
		return nil, fmt.Errorf("failed to cast GetName to func() string\nSee docs for more information: https://docs.getbifrost.ai/plugins/writing-go-plugin")
	}
	// Looking up for HTTPTransportPreHook method
	httpTransportPreHookSym, err := plugin.Lookup("HTTPTransportPreHook")
	if err != nil {
		return nil, err
	}
	if dp.httpTransportPreHook, ok = httpTransportPreHookSym.(func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error)); !ok {
		return nil, fmt.Errorf("failed to cast HTTPTransportPreHook to func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error)\nSee docs for more information: https://docs.getbifrost.ai/plugins/writing-go-plugin")
	}
	// Looking up for HTTPTransportPostHook method
	httpTransportPostHookSym, err := plugin.Lookup("HTTPTransportPostHook")
	if err != nil {
		return nil, err
	}
	if dp.httpTransportPostHook, ok = httpTransportPostHookSym.(func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error); !ok {
		return nil, fmt.Errorf("failed to cast HTTPTransportPostHook to func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error\nSee docs for more information: https://docs.getbifrost.ai/plugins/writing-go-plugin")
	}
	// Looking up for HTTPTransportStreamChunkHook method
	httpTransportStreamChunkHookSym, err := plugin.Lookup("HTTPTransportStreamChunkHook")
	if err != nil {
		return nil, err
	}
	if dp.httpTransportStreamChunkHook, ok = httpTransportStreamChunkHookSym.(func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error)); !ok {
		return nil, fmt.Errorf("failed to cast HTTPTransportStreamChunkHook to func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error).\nSee docs for more information: https://docs.getbifrost.ai/plugins/writing-go-plugin")
	}
	// Looking up for PreHook method
	preHookSym, err := plugin.Lookup("PreHook")
	if err != nil {
		return nil, err
	}
	if dp.preHook, ok = preHookSym.(func(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error)); !ok {
		return nil, fmt.Errorf("failed to cast PreHook to func(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error)\nSee docs for more information: https://docs.getbifrost.ai/plugins/writing-go-plugin")
	}
	// Looking up for PostHook method
	postHookSym, err := plugin.Lookup("PostHook")
	if err != nil {
		return nil, err
	}
	if dp.postHook, ok = postHookSym.(func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)); !ok {
		return nil, fmt.Errorf("failed to cast PostHook to func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)\nSee docs for more information: https://docs.getbifrost.ai/plugins/writing-go-plugin")
	}
	// Looking up for Cleanup method
	cleanupSym, err := plugin.Lookup("Cleanup")
	if err != nil {
		return nil, err
	}
	if dp.cleanup, ok = cleanupSym.(func() error); !ok {
		return nil, fmt.Errorf("failed to cast Cleanup to func() error\nSee docs for more information: https://docs.getbifrost.ai/plugins/writing-go-plugin")
	}
	dp.plugin = plugin
	return dp, nil
}
