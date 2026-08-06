package server

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// TestIsBuiltinPluginDisabled covers the gate that lets a stored plugin row turn off a
// built-in plugin whose load decision otherwise comes from ClientConfig. Default-on
// matters here: a plugin with no stored row must keep loading, so that upgrading does
// not silently drop logging, governance, compat or prompts.
func TestIsBuiltinPluginDisabled(t *testing.T) {
	s := &BifrostHTTPServer{
		Config: &lib.Config{
			PluginConfigs: []*schemas.PluginConfig{
				{Name: "logging", Enabled: false},
				{Name: "governance", Enabled: true},
			},
		},
	}

	tests := []struct {
		name   string
		plugin string
		want   bool
	}{
		{"explicitly disabled row disables the plugin", "logging", true},
		{"explicitly enabled row keeps the plugin on", "governance", false},
		{"missing row defaults to enabled", "compat", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.isBuiltinPluginDisabled(tt.plugin); got != tt.want {
				t.Errorf("isBuiltinPluginDisabled(%q) = %v, want %v", tt.plugin, got, tt.want)
			}
		})
	}
}

// TestIsBuiltinPluginDisabled_NoStoredPlugins verifies the default-on path when the
// config store holds no plugin rows at all, which is the state of a fresh install.
func TestIsBuiltinPluginDisabled_NoStoredPlugins(t *testing.T) {
	s := &BifrostHTTPServer{Config: &lib.Config{}}

	for _, name := range []string{"logging", "governance", "compat", "prompts"} {
		if s.isBuiltinPluginDisabled(name) {
			t.Errorf("isBuiltinPluginDisabled(%q) = true on a fresh install, want false", name)
		}
	}
}
