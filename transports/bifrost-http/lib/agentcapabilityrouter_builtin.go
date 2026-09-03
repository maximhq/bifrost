package lib

import (
	"github.com/maximhq/bifrost/plugins/agentcapabilityrouter"
	"github.com/maximhq/bifrost/plugins/governance"
)

// registerAgentCapabilityRouterBuiltin keeps the capability router in the
// canonical built-in registry without changing the relative order expected by
// the HTTP transport: governance stamps caller scope first, then capability
// classification runs before routing.
func init() {
	for _, name := range builtinPluginNames {
		if name == agentcapabilityrouter.PluginName {
			return
		}
	}

	insertAt := len(builtinPluginNames)
	for i, name := range builtinPluginNames {
		if name == governance.PluginName {
			insertAt = i + 1
			break
		}
	}

	builtinPluginNames = append(builtinPluginNames, "")
	copy(builtinPluginNames[insertAt+1:], builtinPluginNames[insertAt:])
	builtinPluginNames[insertAt] = agentcapabilityrouter.PluginName
}
