package server

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// startConfigChangeListener wires up PostgreSQL LISTEN/NOTIFY for cross-pod
// config sync. If the config store does not support it (SQLite, or the
// listener failed to initialise), this is a silent no-op.
func (s *BifrostHTTPServer) startConfigChangeListener() {
	if s.Config == nil || s.Config.ConfigStore == nil {
		return
	}
	listenable, ok := s.Config.ConfigStore.(configstore.ConfigChangeListenable)
	if !ok {
		return
	}
	if err := listenable.ListenForChanges(s.backgroundCtx(), s.handleConfigChangeEvent); err != nil {
		logger.Warn("failed to start config change listener: %v", err)
		return
	}
	s.configListenerStop = listenable.StopListening
	logger.Info("config change listener started — cross-pod sync enabled")
}

// handleConfigChangeEvent dispatches a config change event received via
// PostgreSQL LISTEN/NOTIFY to the appropriate in-memory reload method.
// These are the same methods the REST API handlers call after a write,
// so the receiving pod ends up in the same state as the writing pod.
func (s *BifrostHTTPServer) handleConfigChangeEvent(ctx context.Context, event configstore.ConfigChangeEvent) {
	switch event.Entity {
	case "full_reload":
		// Fired on (re)connect to catch up on anything missed while disconnected.
		s.handleFullReload(ctx)

	case "provider":
		if event.Action == "delete" {
			if err := s.RemoveProvider(ctx, schemas.ModelProvider(event.Provider)); err != nil {
				logger.Warn("[pgnotify] failed to remove provider %s: %v", event.Provider, err)
			}
		} else {
			if _, err := s.ReloadProvider(ctx, schemas.ModelProvider(event.Provider)); err != nil {
				logger.Warn("[pgnotify] failed to reload provider %s: %v", event.Provider, err)
			}
		}

	case "provider_key":
		// Key changes are reflected through a provider reload.
		if _, err := s.ReloadProvider(ctx, schemas.ModelProvider(event.Provider)); err != nil {
			logger.Warn("[pgnotify] failed to reload provider %s after key change: %v", event.Provider, err)
		}

	case "virtual_key", "vk_provider_config", "vk_mcp_config":
		if err := s.ReloadClientConfigFromConfigStore(ctx); err != nil {
			logger.Warn("[pgnotify] failed to reload client config after %s change: %v", event.Entity, err)
		}

	case "client_config":
		if err := s.ReloadClientConfigFromConfigStore(ctx); err != nil {
			logger.Warn("[pgnotify] failed to reload client config: %v", err)
		}

	case "mcp_client":
		// MCP client changes are best handled via a full client config reload
		// which re-reads all MCP client configs from the store.
		if err := s.ReloadClientConfigFromConfigStore(ctx); err != nil {
			logger.Warn("[pgnotify] failed to reload config after MCP client change: %v", err)
		}

	case "plugin":
		if err := s.ReloadClientConfigFromConfigStore(ctx); err != nil {
			logger.Warn("[pgnotify] failed to reload config after plugin change: %v", err)
		}

	case "routing_rule":
		if event.Action == "delete" {
			if err := s.RemoveRoutingRule(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to remove routing rule %s: %v", event.ID, err)
			}
		} else {
			if err := s.ReloadRoutingRule(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to reload routing rule %s: %v", event.ID, err)
			}
		}

	case "team":
		if event.Action == "delete" {
			if err := s.RemoveTeam(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to remove team %s: %v", event.ID, err)
			}
		} else {
			if _, err := s.ReloadTeam(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to reload team %s: %v", event.ID, err)
			}
		}

	case "customer":
		if event.Action == "delete" {
			if err := s.RemoveCustomer(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to remove customer %s: %v", event.ID, err)
			}
		} else {
			if _, err := s.ReloadCustomer(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to reload customer %s: %v", event.ID, err)
			}
		}

	case "budget":
		// Budget definition changes require a full governance reload.
		if err := s.ReloadClientConfigFromConfigStore(ctx); err != nil {
			logger.Warn("[pgnotify] failed to reload config after budget change: %v", err)
		}

	case "model_config":
		if event.Action == "delete" {
			if err := s.RemoveModelConfig(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to remove model config %s: %v", event.ID, err)
			}
		} else {
			if _, err := s.ReloadModelConfig(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to reload model config %s: %v", event.ID, err)
			}
		}

	case "pricing_override":
		if err := s.ForceReloadPricing(ctx); err != nil {
			logger.Warn("[pgnotify] failed to reload pricing after override change: %v", err)
		}

	case "virtual_mcp":
		if err := s.ReloadClientConfigFromConfigStore(ctx); err != nil {
			logger.Warn("[pgnotify] failed to reload config after virtual MCP change: %v", err)
		}

	case "webhook_endpoint":
		if err := s.ReloadClientConfigFromConfigStore(ctx); err != nil {
			logger.Warn("[pgnotify] failed to reload config after webhook endpoint change: %v", err)
		}

	default:
		logger.Debug("[pgnotify] unknown entity %q, ignoring", event.Entity)
	}
}

// handleFullReload re-reads all config from the database. Called on listener
// (re)connect since messages may have been missed while disconnected.
func (s *BifrostHTTPServer) handleFullReload(ctx context.Context) {
	logger.Info("[pgnotify] performing full config reload after (re)connect")
	if err := s.ReloadClientConfigFromConfigStore(ctx); err != nil {
		logger.Warn("[pgnotify] full reload: client config failed: %v", err)
	}
	// Reload all providers.
	if s.Config != nil {
		s.Config.Mu.RLock()
		providers := make([]schemas.ModelProvider, 0, len(s.Config.Providers))
		for p := range s.Config.Providers {
			providers = append(providers, p)
		}
		s.Config.Mu.RUnlock()
		for _, p := range providers {
			if _, err := s.ReloadProvider(ctx, p); err != nil {
				logger.Warn("[pgnotify] full reload: provider %s failed: %v", p, err)
			}
		}
	}
	if err := s.ForceReloadPricing(ctx); err != nil {
		logger.Warn("[pgnotify] full reload: pricing failed: %v", err)
	}
}
