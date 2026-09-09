package server

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// TestGetRoutingPluginResolvesRegisteredPlugin pins the type the routing plugin is looked up
// as. Init registers a *RoutingPlugin, and FindPluginAs type-asserts against its type
// parameter, which the compiler cannot check for a generic target. Asserting against the
// struct type instead of the pointer therefore builds fine and fails only at runtime, taking
// routing rule reload, rule removal and complexity analyzer reload down with it.
func TestGetRoutingPluginResolvesRegisteredPlugin(t *testing.T) {
	config := &lib.Config{}
	// A zero-value plugin is enough: the lookup is a type assertion on the registered value,
	// so what matters is the type, not a working plugin. Constructing one would tie this test
	// to the constructor's dependencies, which change further up the stack.
	plugins := []schemas.BasePlugin{&routing.RoutingPlugin{}}
	config.BasePlugins.Store(&plugins)

	server := &BifrostHTTPServer{Config: config}

	// Deliberately does not dereference the result: calling a pointer method here would make
	// this file fail to build against the buggy value-typed signature, turning a runtime bug
	// into a compile error and hiding what actually breaks in production. The returned error
	// is the symptom that matters, since it is what the reload paths surface to callers.
	if _, err := server.getRoutingPlugin(); err != nil {
		t.Fatalf("getRoutingPlugin returned error for a registered routing plugin: %v", err)
	}
}
