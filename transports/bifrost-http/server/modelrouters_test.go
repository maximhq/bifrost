package server

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// callerKey marks a context as carrying the user who made the request.
type callerKey struct{}

// callerHidingStore hides every router from a context that carries the caller,
// as the enterprise data access control hides a router whose routing is
// outside the caller's scope, and returns it to any other context.
type callerHidingStore struct {
	configstore.ConfigStore
	readsWithCaller int
}

// GetModelRouter returns the router unless ctx carries the caller.
func (s *callerHidingStore) GetModelRouter(ctx context.Context, id string) (*configstore.ModelRouter, error) {
	if ctx.Value(callerKey{}) != nil {
		s.readsWithCaller++
		return nil, configstore.ErrNotFound
	}
	scopeID := "team-b"
	return &configstore.ModelRouter{ID: id, Name: "Support", Routing: &configstore.ModelRouterRouting{Scope: "team", ScopeID: &scopeID}}, nil
}

// TestModelRouterReloadsReadTheStoredRouter pins that serving a router after a
// change reads it as stored, not as the editor can see it. An editor who moves
// a router's routing out of their own scope can no longer see it, and a read on
// their context would fail and leave the gateway serving the old routing. The
// zero-value plugin serves no router, so it answers ErrNotFound itself; what
// matters is that the read got past the store to it.
func TestModelRouterReloadsReadTheStoredRouter(t *testing.T) {
	store := &callerHidingStore{}
	config := &lib.Config{ConfigStore: store}
	plugins := []schemas.BasePlugin{&routing.RoutingPlugin{}}
	config.BasePlugins.Store(&plugins)
	server := &BifrostHTTPServer{Config: config}
	callerCtx := context.WithValue(context.Background(), callerKey{}, "user-1")

	for name, reload := range map[string]func(context.Context, string) error{
		"targets": server.ReloadModelRouterTargets,
		"routing": server.ReloadModelRouterRouting,
	} {
		t.Run(name, func(t *testing.T) {
			err := reload(callerCtx, "support")
			require.ErrorIs(t, err, configstore.ErrNotFound, "the zero-value plugin serves no router")
			assert.NotContains(t, err.Error(), "config store", "the stored router was read")
		})
	}
	assert.Zero(t, store.readsWithCaller, "no reload reads on the caller's context")
}
