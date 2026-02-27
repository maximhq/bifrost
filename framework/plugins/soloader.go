package plugins

import (
	"context"
	"fmt"
	"plugin"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// SharedObjectPluginLoader is the loader for shared object plugins
type SharedObjectPluginLoader struct{}

func openPlugin(dp *DynamicPlugin) (*plugin.Plugin, error) {
	// Checking if path is URL or file path
	if strings.HasPrefix(dp.Path, "http") {
		// Download the file
		tempPath, err := DownloadPlugin(dp.Path, ".so")
		if err != nil {
			return nil, err
		}
		dp.Path = tempPath
	}
	pluginObj, err := plugin.Open(dp.Path)
	if err != nil {
		return nil, err
	}
	dp.plugin = pluginObj
	return pluginObj, nil
}

// LoadPlugin loads a generic plugin from a shared object file
// It uses optional symbol lookup - only GetName and Cleanup are required
// All other hook methods are optional and stored as nil if not implemented
func (l *SharedObjectPluginLoader) LoadPlugin(path string, config any) (schemas.BasePlugin, error) {
	dp := &DynamicPlugin{
		Path: path,
	}

	pluginObj, err := openPlugin(dp)
	if err != nil {
		return nil, err
	}

	// Optional Init method
	if initSym, err := pluginObj.Lookup("Init"); err == nil {
		if initFunc, ok := initSym.(func(config any) error); ok {
			if err := initFunc(config); err != nil {
				return nil, fmt.Errorf("plugin Init failed: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to cast Init to func(config any) error")
		}
	}

	// Required: GetName
	getNameSym, err := pluginObj.Lookup("GetName")
	if err != nil {
		return nil, fmt.Errorf("required symbol GetName not found: %w", err)
	}
	var ok bool
	if dp.getName, ok = getNameSym.(func() string); !ok {
		return nil, fmt.Errorf("failed to cast GetName to func() string\nSee docs for more information: https://docs.getbifrost.ai/plugins/writing-go-plugin")
	}

	// Required: Cleanup
	cleanupSym, err := pluginObj.Lookup("Cleanup")
	if err != nil {
		return nil, fmt.Errorf("required symbol Cleanup not found: %w", err)
	}
	if dp.cleanup, ok = cleanupSym.(func() error); !ok {
		return nil, fmt.Errorf("failed to cast Cleanup to func() error\nSee docs for more information: https://docs.getbifrost.ai/plugins/writing-go-plugin")
	}

	// Optional: HTTPTransportPreHook
	if sym, err := pluginObj.Lookup("HTTPTransportPreHook"); err == nil {
		if dp.httpTransportPreHook, ok = sym.(func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error)); !ok {
			return nil, fmt.Errorf("failed to cast HTTPTransportPreHook to expected signature")
		}
	}

	// Optional: HTTPTransportPostHook
	if sym, err := pluginObj.Lookup("HTTPTransportPostHook"); err == nil {
		if dp.httpTransportPostHook, ok = sym.(func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error); !ok {
			return nil, fmt.Errorf("failed to cast HTTPTransportPostHook to expected signature")
		}
	}

	// Optional: HTTPTransportStreamChunkHook
	if sym, err := pluginObj.Lookup("HTTPTransportStreamChunkHook"); err == nil {
		if dp.httpTransportStreamChunkHook, ok = sym.(func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error)); !ok {
			return nil, fmt.Errorf("failed to cast HTTPTransportStreamChunkHook to expected signature")
		}
	}

	// Optional: PreLLMHook (with backward compatibility for legacy PreHook)
	if sym, err := pluginObj.Lookup("PreLLMHook"); err == nil {
		if dp.preLLMHook, ok = sym.(func(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error)); !ok {
			return nil, fmt.Errorf("failed to cast PreLLMHook to expected signature")
		}
	} else if sym, err := pluginObj.Lookup("PreHook"); err == nil {
		// Legacy backward compatibility (v1.3.x): treat PreHook as PreLLMHook
		if dp.preLLMHook, ok = sym.(func(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error)); !ok {
			return nil, fmt.Errorf("failed to cast PreHook to expected signature (legacy backward compatibility)")
		}
	}

	// Optional: PostLLMHook (with backward compatibility for legacy PostHook)
	if sym, err := pluginObj.Lookup("PostLLMHook"); err == nil {
		if dp.postLLMHook, ok = sym.(func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)); !ok {
			return nil, fmt.Errorf("failed to cast PostLLMHook to expected signature")
		}
	} else if sym, err := pluginObj.Lookup("PostHook"); err == nil {
		// Legacy backward compatibility (v1.3.x): treat PostHook as PostLLMHook
		if dp.postLLMHook, ok = sym.(func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)); !ok {
			return nil, fmt.Errorf("failed to cast PostHook to expected signature (legacy backward compatibility)")
		}
	}

	// Optional: PreMCPHook
	if sym, err := pluginObj.Lookup("PreMCPHook"); err == nil {
		if dp.preMCPHook, ok = sym.(func(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error)); !ok {
			return nil, fmt.Errorf("failed to cast PreMCPHook to expected signature")
		}
	}

	// Optional: PostMCPHook
	if sym, err := pluginObj.Lookup("PostMCPHook"); err == nil {
		if dp.postMCPHook, ok = sym.(func(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error)); !ok {
			return nil, fmt.Errorf("failed to cast PostMCPHook to expected signature")
		}
	}

	// Optional: Inject (ObservabilityPlugin)
	if sym, err := pluginObj.Lookup("Inject"); err == nil {
		if dp.inject, ok = sym.(func(ctx context.Context, trace *schemas.Trace) error); !ok {
			return nil, fmt.Errorf("failed to cast Inject to expected signature")
		}
	}

	return dp, nil
}

// VerifyBasePlugin verifies a plugin at the given path
// Returns the name of the plugin or an empty string if the plugin is invalid
// Returns an error if the plugin is invalid
// This method is used to verify that the plugin is a valid base plugin and has the required symbols
func (l *SharedObjectPluginLoader) VerifyBasePlugin(path string) (string, error) {
	dp := &DynamicPlugin{
		Path: path,
	}
	pluginObj, err := openPlugin(dp)
	if err != nil {
		return "", err
	}
	// Required: GetName
	getNameSym, err := pluginObj.Lookup("GetName")
	if err != nil {
		return "", fmt.Errorf("required symbol GetName not found: %w", err)
	}
	var ok bool
	if dp.getName, ok = getNameSym.(func() string); !ok {
		return "", fmt.Errorf("failed to cast GetName to func() string")
	}
	// Required: Cleanup
	cleanupSym, err := pluginObj.Lookup("Cleanup")
	if err != nil {
		return "", fmt.Errorf("required symbol Cleanup not found: %w", err)
	}
	if dp.cleanup, ok = cleanupSym.(func() error); !ok {
		return "", fmt.Errorf("failed to cast Cleanup to func() error")
	}
	return dp.getName(), nil
}
