package server

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/agentcapabilityrouter"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

func TestAgentCapabilityRouterIsRegisteredAsBuiltin(t *testing.T) {
	if !lib.IsBuiltinPlugin(agentcapabilityrouter.PluginName) {
		t.Fatalf("%q is missing from the built-in plugin registry", agentcapabilityrouter.PluginName)
	}

	plugin, err := loadBuiltinPlugin(
		context.Background(),
		agentcapabilityrouter.PluginName,
		map[string]any{"shadow_mode": false},
		&lib.Config{},
	)
	if err != nil {
		t.Fatalf("loadBuiltinPlugin() error = %v", err)
	}
	if plugin.GetName() != agentcapabilityrouter.PluginName {
		t.Fatalf("plugin name = %q, want %q", plugin.GetName(), agentcapabilityrouter.PluginName)
	}

	types := InferPluginTypes(plugin)
	for _, pluginType := range types {
		if pluginType == schemas.PluginTypeLLM {
			return
		}
	}
	t.Fatalf("plugin types = %#v, want LLM", types)
}
