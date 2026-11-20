package server

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/handlers"
)

// storedModelRouter reads a router as it is stored, to serve it. The caller's
// context can carry a user whose data access control hides the router, as
// when they have just moved its routing out of their own scope, and the
// gateway serves what is saved, not what the editor can see, so the read does
// not carry the caller.
func (s *BifrostHTTPServer) storedModelRouter(id string) (*configstore.ModelRouter, error) {
	return s.Config.ConfigStore.GetModelRouter(context.Background(), id)
}

// ReloadModelRouter serves a saved model router from the config store.
func (s *BifrostHTTPServer) ReloadModelRouter(_ context.Context, id string) error {
	routingPlugin, err := s.getRoutingPlugin()
	if err != nil {
		return fmt.Errorf("routing plugin not found: %w", err)
	}
	router, err := s.storedModelRouter(id)
	if err != nil {
		return fmt.Errorf("failed to get model router from config store: %w", err)
	}
	return routingPlugin.ReloadModelRouter(router)
}

// ReloadModelRouterTargets serves a saved router's targets from the config
// store without reconfiguring its classifiers, so changing which model a tier
// gets never restarts a semantic warmup.
func (s *BifrostHTTPServer) ReloadModelRouterTargets(_ context.Context, id string) error {
	routingPlugin, err := s.getRoutingPlugin()
	if err != nil {
		return fmt.Errorf("routing plugin not found: %w", err)
	}
	router, err := s.storedModelRouter(id)
	if err != nil {
		return fmt.Errorf("failed to get model router from config store: %w", err)
	}
	return routingPlugin.SetModelRouterTargets(id, router.Targets)
}

// ReloadModelRouterRouting serves a saved router's routing from the config
// store, leaving its classifiers and targets as they are.
func (s *BifrostHTTPServer) ReloadModelRouterRouting(_ context.Context, id string) error {
	routingPlugin, err := s.getRoutingPlugin()
	if err != nil {
		return fmt.Errorf("routing plugin not found: %w", err)
	}
	router, err := s.storedModelRouter(id)
	if err != nil {
		return fmt.Errorf("failed to get model router from config store: %w", err)
	}
	if router.Routing == nil {
		return fmt.Errorf("model router %q was read without routing", id)
	}
	return routingPlugin.SetModelRouterRouting(id, *router.Routing)
}

// RemoveModelRouter stops serving a deleted router.
func (s *BifrostHTTPServer) RemoveModelRouter(_ context.Context, id string) error {
	routingPlugin, err := s.getRoutingPlugin()
	if err != nil {
		return fmt.Errorf("routing plugin not found: %w", err)
	}
	return routingPlugin.RemoveModelRouter(id)
}

// GetModelRouterStatus reports one router's classifier readiness.
// configstore.ErrNotFound means the plugin does not serve the router.
func (s *BifrostHTTPServer) GetModelRouterStatus(_ context.Context, id string) (complexity.SemanticStatusInfo, complexity.LLMStatusInfo, error) {
	routingPlugin, err := s.getRoutingPlugin()
	if err != nil {
		return complexity.SemanticStatusInfo{}, complexity.LLMStatusInfo{}, fmt.Errorf("routing plugin not found: %w", err)
	}
	semantic, ok := routingPlugin.ModelRouterSemanticStatus(id)
	if !ok {
		return complexity.SemanticStatusInfo{}, complexity.LLMStatusInfo{}, configstore.ErrNotFound
	}
	llm, _ := routingPlugin.ModelRouterLLMStatus(id)
	return semantic, llm, nil
}

// RetryModelRouterWarmup restarts one router's failed semantic warmup.
// configstore.ErrNotFound means the plugin does not serve the router.
func (s *BifrostHTTPServer) RetryModelRouterWarmup(_ context.Context, id string) (complexity.SemanticStatusInfo, bool, error) {
	routingPlugin, err := s.getRoutingPlugin()
	if err != nil {
		return complexity.SemanticStatusInfo{}, false, fmt.Errorf("routing plugin not found: %w", err)
	}
	status, started, ok := routingPlugin.RetryModelRouterWarmup(id)
	if !ok {
		return complexity.SemanticStatusInfo{}, false, configstore.ErrNotFound
	}
	return status, started, nil
}

var _ handlers.ModelRouterClassifier = (*BifrostHTTPServer)(nil)

// ClassifyWithModelRouter classifies statements with one served router.
func (s *BifrostHTTPServer) ClassifyWithModelRouter(ctx context.Context, routerID string, inputs []routing.ModelRouterClassifyInput) (routing.ModelRouterClassifyResult, error) {
	routingPlugin, err := s.getRoutingPlugin()
	if err != nil {
		return routing.ModelRouterClassifyResult{}, fmt.Errorf("routing plugin not found: %w", err)
	}
	return routingPlugin.ClassifyWithModelRouter(ctx, routerID, inputs)
}
