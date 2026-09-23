// Command soplugin builds the Nadir routing plugin as a Go shared object, for a Bifrost
// built with dynamic linking (see docs/plugins/building-dynamic-binary).
//
//	go build -buildmode=plugin -o nadir.so ./plugins/nadir/cmd/soplugin
//
// Operators running a stock built-in Bifrost do not need this: the plugin is registered
// under the name "nadir" and configured the same way every other built-in plugin is. This
// exists for the dynamic-plugin path, where the loader looks up these symbols by name.
package main

import (
	"encoding/json"
	"fmt"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/nadir"
)

var plugin *nadir.Plugin

// Init receives the plugin's config block as it was written in Bifrost's config, which
// reaches here as decoded JSON rather than as a typed struct, so it is re-encoded to run
// through the same tags and validation a built-in registration uses.
func Init(config any) error {
	raw, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("nadir: could not read plugin config: %w", err)
	}
	cfg := &nadir.Config{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return fmt.Errorf("nadir: could not read plugin config: %w", err)
	}
	plugin, err = nadir.Init(cfg, bifrost.NewDefaultLogger(schemas.LogLevelInfo))
	return err
}

// GetName implements the loader's BasePlugin lookup.
func GetName() string { return nadir.PluginName }

// PreRequestHook implements the routing phase the loader looks up by name.
func PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if plugin == nil {
		return fmt.Errorf("nadir: plugin used before Init")
	}
	return plugin.PreRequestHook(ctx, req)
}

// Cleanup implements the loader's BasePlugin lookup.
func Cleanup() error {
	if plugin == nil {
		return nil
	}
	return plugin.Cleanup()
}

// main is never called: -buildmode=plugin produces a shared object, not an executable. It
// exists so a plain `go build ./...` over the repo compiles this package like any other.
func main() {}
