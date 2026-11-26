package server

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/handlers"
)

// refreshModelRouter brings a saved router's row into memory and returns its record. A row that
// cannot be read, or is stored in a form this version cannot read, keeps the router's record and
// returns the read error; a row gone by the time it is read was deleted meanwhile, so its record is
// dropped and configstore.ErrNotFound returned, as a reload of it always has. The read does not
// carry the caller's data access: the gateway serves what is saved, not what the editor can see.
func (s *BifrostHTTPServer) refreshModelRouter(ctx context.Context, id string) (*registry.ModelRouter, error) {
	if err := s.refreshRows(ctx, []livestore.Ref{{Kind: livestore.KindModelRouter, ID: id}}); err != nil {
		return nil, fmt.Errorf("failed to get model router from config store: %w", err)
	}
	live, err := s.liveStore()
	if err != nil {
		return nil, err
	}
	record, ok := live.Collections.ModelRouters.Get(id)
	if !ok {
		return nil, fmt.Errorf("failed to get model router from config store: %w", configstore.ErrNotFound)
	}
	return record, nil
}

// ReloadModelRouter brings a saved model router into memory and serves it. Without the routing
// plugin it returns that plugin's error after refreshing, so memory follows the database either
// way.
func (s *BifrostHTTPServer) ReloadModelRouter(ctx context.Context, id string) error {
	routingPlugin, pluginErr := s.getRoutingPlugin()
	record, err := s.refreshModelRouter(ctx, id)
	if pluginErr != nil {
		return fmt.Errorf("routing plugin not found: %w", pluginErr)
	}
	if err != nil {
		return err
	}
	return routingPlugin.ReloadModelRouter(&record.Router)
}

// ReloadModelRouterTargets brings a saved router's targets into memory without reconfiguring its
// classifiers, so changing which model a tier gets never restarts a semantic warmup. The router
// reads its targets from the record. configstore.ErrNotFound means the plugin does not serve the
// router.
func (s *BifrostHTTPServer) ReloadModelRouterTargets(ctx context.Context, id string) error {
	routingPlugin, pluginErr := s.getRoutingPlugin()
	_, err := s.refreshModelRouter(ctx, id)
	if pluginErr != nil {
		return fmt.Errorf("routing plugin not found: %w", pluginErr)
	}
	if err != nil {
		return err
	}
	if _, served := routingPlugin.ModelRouterSemanticStatus(id); !served {
		return configstore.ErrNotFound
	}
	return nil
}

// ReloadModelRouterRouting brings a saved router's routing into memory and records it for
// matching, leaving its classifiers and targets as they are.
func (s *BifrostHTTPServer) ReloadModelRouterRouting(ctx context.Context, id string) error {
	routingPlugin, pluginErr := s.getRoutingPlugin()
	record, err := s.refreshModelRouter(ctx, id)
	if pluginErr != nil {
		return fmt.Errorf("routing plugin not found: %w", pluginErr)
	}
	if err != nil {
		return err
	}
	if record.Router.Routing == nil {
		return fmt.Errorf("model router %q was read without routing", id)
	}
	return routingPlugin.SetModelRouterRouting(id, *record.Router.Routing)
}

// RemoveModelRouter drops a deleted router from memory and stops serving it. Without the routing
// plugin it returns that plugin's error after dropping the record.
func (s *BifrostHTTPServer) RemoveModelRouter(ctx context.Context, id string) error {
	routingPlugin, pluginErr := s.getRoutingPlugin()
	err := s.removeRecord(ctx, livestore.KindModelRouter, id)
	if pluginErr != nil {
		return fmt.Errorf("routing plugin not found: %w", pluginErr)
	}
	if err != nil {
		return err
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
