package server

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestModelRouterReloadsReadTheStoredRouter pins that serving a router after a
// change reads it as stored, not as the editor can see it. An editor who moves
// a router's routing out of their own scope can no longer see it, and a read on
// their context would fail and leave the gateway serving the old routing. The
// caller's context here carries a query scope that hides every row, as a
// data access policy hides a router outside the caller's scope. The
// zero-value plugin serves no router, so it answers ErrNotFound itself; what
// matters is that the read got past the store to it.
func TestModelRouterReloadsReadTheStoredRouter(t *testing.T) {
	ctx := context.Background()
	config := libtest.Config(t, nil, nil)
	router := &configstore.ModelRouter{ID: "support", Name: "Support", ComplexityAnalyzerConfig: configstore.ComplexityAnalyzerConfig{
		TierBoundaries: configstore.ComplexityTierBoundaries{SimpleMedium: 0.10, MediumComplex: 0.30},
		Keywords: configstore.ComplexityEditableKeywordConfig{
			SimpleKeywords: []string{"hello"}, MediumKeywords: []string{"api"}, ComplexKeywords: []string{"tradeoffs"},
		},
	}}
	require.NoError(t, config.ConfigStore.CreateModelRouter(ctx, router))
	scopeID := "team-b"
	require.NoError(t, config.ConfigStore.SetModelRouterRouting(ctx, "support", configstore.ModelRouterRouting{Enabled: true, Scope: "team", ScopeID: &scopeID}))
	plugins := []schemas.BasePlugin{&routing.RoutingPlugin{}}
	config.BasePlugins.Store(&plugins)
	server := &BifrostHTTPServer{Config: config}
	callerCtx := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("1 = 0") })
	_, err := config.ConfigStore.GetModelRouter(callerCtx, "support")
	require.ErrorIs(t, err, configstore.ErrNotFound, "the caller cannot see the router")

	for name, reload := range map[string]func(context.Context, string) error{
		"targets": server.ReloadModelRouterTargets,
		"routing": server.ReloadModelRouterRouting,
	} {
		t.Run(name, func(t *testing.T) {
			err := reload(callerCtx, "support")
			require.ErrorIs(t, err, configstore.ErrNotFound, "the zero-value plugin serves no router")
			assert.NotContains(t, err.Error(), "config store", "the stored router was read")
			record, ok := config.LiveStore.Collections.ModelRouters.Get("support")
			require.True(t, ok)
			require.NotNil(t, record.Router.Routing)
			assert.Equal(t, "team", record.Router.Routing.Scope)
		})
	}
}
