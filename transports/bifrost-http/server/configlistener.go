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

// handleFullReload re-reads every entity type from the store. Called on
// listener (re)connect since messages may have been missed while
// disconnected (LISTEN/NOTIFY has no replay). This enumerates the store's
// current rows and reloads each one, so it catches anything created or
// updated while disconnected. It does NOT catch deletions: a row removed
// from the store during the outage simply no longer appears in these
// lists, so nothing calls the matching Remove* for it and it stays
// cached until this pod restarts (which always reads the store fresh).
// Detecting a deletion here would require diffing against a full
// in-memory snapshot, which the governance store does not currently
// expose an enumerator for.
func (s *BifrostHTTPServer) handleFullReload(ctx context.Context) {
	logger.Info("[pgnotify] performing full config reload after (re)connect")
	if err := s.ReloadClientConfigFromConfigStore(ctx); err != nil {
		logger.Warn("[pgnotify] full reload: client config failed: %v", err)
	}
	if err := s.ForceReloadPricing(ctx); err != nil {
		logger.Warn("[pgnotify] full reload: pricing failed: %v", err)
	}

	if s.Config == nil || s.Config.ConfigStore == nil {
		return
	}
	store := s.Config.ConfigStore

	// Providers: enumerate from the store, not s.Config.Providers, so a
	// provider added while disconnected is discovered too.
	if providers, err := store.GetProvidersConfig(ctx); err != nil {
		logger.Warn("[pgnotify] full reload: listing providers failed: %v", err)
	} else {
		for p := range providers {
			if _, err := s.ReloadProvider(ctx, p); err != nil {
				logger.Warn("[pgnotify] full reload: provider %s failed: %v", p, err)
			}
		}
	}

	if vks, err := store.GetVirtualKeys(ctx); err != nil {
		logger.Warn("[pgnotify] full reload: listing virtual keys failed: %v", err)
	} else {
		for _, vk := range vks {
			if _, err := s.ReloadVirtualKey(ctx, vk.ID); err != nil {
				logger.Warn("[pgnotify] full reload: virtual key %s failed: %v", vk.ID, err)
			}
		}
	}

	if teams, err := store.GetTeams(ctx, ""); err != nil {
		logger.Warn("[pgnotify] full reload: listing teams failed: %v", err)
	} else {
		for _, team := range teams {
			if _, err := s.ReloadTeam(ctx, team.ID); err != nil {
				logger.Warn("[pgnotify] full reload: team %s failed: %v", team.ID, err)
			}
		}
	}

	if customers, err := store.GetCustomers(ctx); err != nil {
		logger.Warn("[pgnotify] full reload: listing customers failed: %v", err)
	} else {
		for _, customer := range customers {
			if _, err := s.ReloadCustomer(ctx, customer.ID); err != nil {
				logger.Warn("[pgnotify] full reload: customer %s failed: %v", customer.ID, err)
			}
		}
	}

	if modelConfigs, err := store.GetModelConfigs(ctx); err != nil {
		logger.Warn("[pgnotify] full reload: listing model configs failed: %v", err)
	} else {
		for _, mc := range modelConfigs {
			if _, err := s.ReloadModelConfig(ctx, mc.ID); err != nil {
				logger.Warn("[pgnotify] full reload: model config %s failed: %v", mc.ID, err)
			}
		}
	}

	if rules, err := store.GetRoutingRules(ctx); err != nil {
		logger.Warn("[pgnotify] full reload: listing routing rules failed: %v", err)
	} else {
		for _, rule := range rules {
			if err := s.ReloadRoutingRule(ctx, rule.ID); err != nil {
				logger.Warn("[pgnotify] full reload: routing rule %s failed: %v", rule.ID, err)
			}
		}
	}

	if plugins, err := store.GetPlugins(ctx); err != nil {
		logger.Warn("[pgnotify] full reload: listing plugins failed: %v", err)
	} else {
		for _, plugin := range plugins {
			if plugin == nil {
				continue
			}
			if err := s.ReloadPluginByName(ctx, plugin.Name); err != nil {
				logger.Warn("[pgnotify] full reload: plugin %s failed: %v", plugin.Name, err)
			}
		}
	}

	if endpoints, err := store.GetWebhookEndpoints(ctx); err != nil {
		logger.Warn("[pgnotify] full reload: listing webhook endpoints failed: %v", err)
	} else {
		for _, endpoint := range endpoints {
			if err := s.ReloadWebhookEndpoint(ctx, endpoint.ID); err != nil {
				logger.Warn("[pgnotify] full reload: webhook endpoint %s failed: %v", endpoint.ID, err)
			}
		}
	}

	// MCP clients and virtual MCPs only expose paginated listing.
	const fullReloadPageSize = 200
	for offset := 0; ; offset += fullReloadPageSize {
		clients, _, err := store.GetMCPClientsPaginated(ctx, configstore.MCPClientsQueryParams{Limit: fullReloadPageSize, Offset: offset})
		if err != nil {
			logger.Warn("[pgnotify] full reload: listing MCP clients failed: %v", err)
			break
		}
		for _, client := range clients {
			if err := s.ReloadMCPClientConfig(ctx, client.ClientID); err != nil {
				logger.Warn("[pgnotify] full reload: MCP client %s failed: %v", client.ClientID, err)
			}
		}
		if len(clients) < fullReloadPageSize {
			break
		}
	}

	for offset := 0; ; offset += fullReloadPageSize {
		defs, _, err := store.GetVirtualMCPsPaginated(ctx, configstore.VirtualMCPsQueryParams{Limit: fullReloadPageSize, Offset: offset})
		if err != nil {
			logger.Warn("[pgnotify] full reload: listing virtual MCPs failed: %v", err)
			break
		}
		for _, def := range defs {
			if _, err := s.ReloadVirtualMCP(ctx, def.ID); err != nil {
				logger.Warn("[pgnotify] full reload: virtual MCP %d failed: %v", def.ID, err)
			}
		}
		if len(defs) < fullReloadPageSize {
			break
		}
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
