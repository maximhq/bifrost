package server

import (
	"context"
	"fmt"
	"strconv"

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
	case configstore.ConfigEntityFullReload:
		// Fired on (re)connect to catch up on anything missed while disconnected.
		s.handleFullReload(ctx)

	case configstore.ConfigEntityProvider:
		if event.Action == configstore.ConfigActionDelete {
			if err := s.RemoveProvider(ctx, schemas.ModelProvider(event.Provider)); err != nil {
				logger.Warn("[pgnotify] failed to remove provider %s: %v", event.Provider, err)
			}
		} else {
			if _, err := s.ReloadProvider(ctx, schemas.ModelProvider(event.Provider)); err != nil {
				logger.Warn("[pgnotify] failed to reload provider %s: %v", event.Provider, err)
			}
		}

	case configstore.ConfigEntityProviderKey:
		// Key changes are reflected through a provider reload.
		if _, err := s.ReloadProvider(ctx, schemas.ModelProvider(event.Provider)); err != nil {
			logger.Warn("[pgnotify] failed to reload provider %s after key change: %v", event.Provider, err)
		}

	case configstore.ConfigEntityVirtualKey:
		if event.Action == configstore.ConfigActionDelete {
			if err := s.RemoveVirtualKey(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to remove virtual key %s: %v", event.ID, err)
			}
		} else {
			if _, err := s.ReloadVirtualKey(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to reload virtual key %s: %v", event.ID, err)
			}
		}

	case configstore.ConfigEntityVKProviderConfig, configstore.ConfigEntityVKMCPConfig:
		// event.ID is the owning virtual key's ID (relations don't have a
		// standalone in-memory representation). A relation delete still means
		// the VK needs reloading, never removing — the VK row itself didn't
		// change.
		if _, err := s.ReloadVirtualKey(ctx, event.ID); err != nil {
			logger.Warn("[pgnotify] failed to reload virtual key %s after %s change: %v", event.ID, event.Entity, err)
		}

	case configstore.ConfigEntityClientConfig:
		if err := s.ReloadClientConfigFromConfigStore(ctx); err != nil {
			logger.Warn("[pgnotify] failed to reload client config: %v", err)
		}

	case configstore.ConfigEntityMCPClient:
		if event.Action == configstore.ConfigActionDelete {
			if err := s.RemoveMCPClient(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to remove MCP client %s: %v", event.ID, err)
			}
		} else {
			if err := s.ReloadMCPClientConfig(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to reload MCP client %s: %v", event.ID, err)
			}
		}

	case configstore.ConfigEntityPlugin:
		if event.Action == configstore.ConfigActionDelete {
			if err := s.RemovePlugin(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to remove plugin %s: %v", event.ID, err)
			}
		} else {
			if err := s.ReloadPluginByName(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to reload plugin %s: %v", event.ID, err)
			}
		}

	case configstore.ConfigEntityRoutingRule:
		if event.Action == configstore.ConfigActionDelete {
			if err := s.RemoveRoutingRule(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to remove routing rule %s: %v", event.ID, err)
			}
		} else {
			if err := s.ReloadRoutingRule(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to reload routing rule %s: %v", event.ID, err)
			}
		}

	case configstore.ConfigEntityTeam:
		if event.Action == configstore.ConfigActionDelete {
			if err := s.RemoveTeam(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to remove team %s: %v", event.ID, err)
			}
		} else {
			if _, err := s.ReloadTeam(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to reload team %s: %v", event.ID, err)
			}
		}

	case configstore.ConfigEntityCustomer:
		if event.Action == configstore.ConfigActionDelete {
			if err := s.RemoveCustomer(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to remove customer %s: %v", event.ID, err)
			}
		} else {
			if _, err := s.ReloadCustomer(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to reload customer %s: %v", event.ID, err)
			}
		}

	case configstore.ConfigEntityModelConfig:
		if event.Action == configstore.ConfigActionDelete {
			if err := s.RemoveModelConfig(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to remove model config %s: %v", event.ID, err)
			}
		} else {
			if _, err := s.ReloadModelConfig(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to reload model config %s: %v", event.ID, err)
			}
		}

	case configstore.ConfigEntityPricingOverride:
		if err := s.ForceReloadPricing(ctx); err != nil {
			logger.Warn("[pgnotify] failed to reload pricing after override change: %v", err)
		}

	case configstore.ConfigEntityVirtualMCP:
		vmcpID, err := strconv.ParseUint(event.ID, 10, 64)
		if err != nil {
			logger.Warn("[pgnotify] malformed virtual MCP id %q: %v", event.ID, err)
			break
		}
		if event.Action == configstore.ConfigActionDelete {
			if err := s.RemoveVirtualMCP(ctx, uint(vmcpID)); err != nil {
				logger.Warn("[pgnotify] failed to remove virtual MCP %s: %v", event.ID, err)
			}
		} else {
			if _, err := s.ReloadVirtualMCP(ctx, uint(vmcpID)); err != nil {
				logger.Warn("[pgnotify] failed to reload virtual MCP %s: %v", event.ID, err)
			}
		}

	case configstore.ConfigEntityWebhookEndpoint:
		if event.Action == configstore.ConfigActionDelete {
			if err := s.RemoveWebhookEndpoint(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to remove webhook endpoint %s: %v", event.ID, err)
			}
		} else {
			if err := s.ReloadWebhookEndpoint(ctx, event.ID); err != nil {
				logger.Warn("[pgnotify] failed to reload webhook endpoint %s: %v", event.ID, err)
			}
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

// ReloadMCPClientConfig re-reads one MCP client's config from the store and
// applies it. This is the peer-sync counterpart to UpdateMCPClient, which
// applies a caller-supplied config without reading the store itself.
func (s *BifrostHTTPServer) ReloadMCPClientConfig(ctx context.Context, id string) error {
	if s.Config == nil || s.Config.ConfigStore == nil {
		return fmt.Errorf("config store not found")
	}
	config, err := s.Config.ConfigStore.GetMCPClientConfigByID(ctx, id)
	if err != nil {
		return err
	}
	return s.UpdateMCPClient(ctx, id, config)
}

// ReloadPluginByName re-reads one plugin's config from the store and applies
// it. This is the peer-sync counterpart to ReloadPlugin, which takes an
// already-in-hand definition instead of reading the store itself.
func (s *BifrostHTTPServer) ReloadPluginByName(ctx context.Context, name string) error {
	if s.Config == nil || s.Config.ConfigStore == nil {
		return fmt.Errorf("config store not found")
	}
	plugin, err := s.Config.ConfigStore.GetPlugin(ctx, name)
	if err != nil {
		return err
	}
	return s.ReloadPlugin(ctx, plugin.Name, plugin.Path, plugin.Config, plugin.Placement, plugin.Order)
}
