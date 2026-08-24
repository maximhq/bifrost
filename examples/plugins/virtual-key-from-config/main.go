package main

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/maximhq/bifrost/core/schemas"
)

const virtualKeyHeader = "x-bf-vk"

var configuredVirtualKey atomic.Pointer[string]

// Init reads the virtual key from the plugin configuration.
func Init(config any) error {
	configMap, ok := config.(map[string]any)
	if !ok {
		return fmt.Errorf("plugin config must be an object containing virtual_key")
	}

	virtualKey, ok := configMap["virtual_key"].(string)
	if !ok || virtualKey == "" {
		return fmt.Errorf("plugin config virtual_key must be a non-empty string")
	}

	configuredVirtualKey.Store(&virtualKey)
	return nil
}

// GetName returns the plugin's system identifier.
func GetName() string {
	return "virtual-key-from-config"
}

// HTTPTransportPreAuthHook adds the configured virtual key unless the request
// already contains an x-bf-vk header. It is the pre-auth hook, not the pre-hook:
// the injected key is a credential, so it has to be in place before the transport
// authenticates the request.
func HTTPTransportPreAuthHook(_ *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	if req == nil {
		return nil, nil
	}

	for header := range req.Headers {
		if strings.EqualFold(header, virtualKeyHeader) {
			return nil, nil
		}
	}

	virtualKey := configuredVirtualKey.Load()
	if virtualKey == nil {
		return nil, nil
	}

	if req.Headers == nil {
		req.Headers = make(map[string]string, 1)
	}
	req.Headers[virtualKeyHeader] = *virtualKey

	return nil, nil
}

// Cleanup releases plugin resources. This plugin does not allocate any.
func Cleanup() error {
	return nil
}
