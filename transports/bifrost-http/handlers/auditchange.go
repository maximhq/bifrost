package handlers

import (
	"encoding/json"
	neturl "net/url"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// auditVirtualKey snapshots a virtual key for the audit log without its value or any nested secret-bearing relation.
func auditVirtualKey(vk *configstoreTables.TableVirtualKey) json.RawMessage {
	if vk == nil {
		return nil
	}
	c := *vk
	c.Team = nil
	c.Customer = nil
	if vk.ProviderConfigs != nil {
		c.ProviderConfigs = make([]configstoreTables.TableVirtualKeyProviderConfig, len(vk.ProviderConfigs))
		for i, pc := range vk.ProviderConfigs {
			if pc.Keys != nil {
				keys := make([]configstoreTables.TableKey, len(pc.Keys))
				for j, k := range pc.Keys {
					keys[j] = configstoreTables.TableKey{ID: k.ID, Name: k.Name, KeyID: k.KeyID, Provider: k.Provider}
				}
				pc.Keys = keys
			}
			c.ProviderConfigs[i] = pc
		}
	}
	if vk.MCPConfigs != nil {
		c.MCPConfigs = make([]configstoreTables.TableVirtualKeyMCPConfig, len(vk.MCPConfigs))
		for i, mc := range vk.MCPConfigs {
			mc.MCPClient = configstoreTables.TableMCPClient{ID: mc.MCPClient.ID, ClientID: mc.MCPClient.ClientID, Name: mc.MCPClient.Name}
			c.MCPConfigs[i] = mc
		}
	}
	return lib.AuditSnapshot(c, "value")
}

// auditTeam snapshots a team for the audit log without its nested customer or virtual keys.
func auditTeam(team *configstoreTables.TableTeam) json.RawMessage {
	if team == nil {
		return nil
	}
	c := *team
	c.Customer = nil
	c.VirtualKeys = nil
	return lib.AuditSnapshot(c)
}

// auditCustomer snapshots a customer for the audit log without its virtual keys.
func auditCustomer(customer *configstoreTables.TableCustomer) json.RawMessage {
	if customer == nil {
		return nil
	}
	c := *customer
	c.VirtualKeys = nil
	if customer.Teams != nil {
		c.Teams = make([]configstoreTables.TableTeam, len(customer.Teams))
		for i, t := range customer.Teams {
			t.Customer = nil
			t.VirtualKeys = nil
			c.Teams[i] = t
		}
	}
	return lib.AuditSnapshot(c, "virtual_keys")
}

// auditProviderConfig snapshots an already-redacted provider config for the audit log; keys have
// their own events. ProviderConfig has no "keys" field any more, but naming it to drop routes the
// snapshot through AuditSnapshot's field map, which keeps the fields in sorted order: byte for byte
// the snapshot the audit log stored when the config still carried keys.
func auditProviderConfig(cfg *configstore.ProviderConfig) json.RawMessage {
	if cfg == nil {
		return nil
	}
	return lib.AuditSnapshot(cfg, "keys")
}

// auditMCPClient snapshots a redacted copy of an MCP client config, also masking credentials embedded in its connection URL
// and STDIO command line: every argument, and the value of each inline KEY=value env entry (the names stay).
func auditMCPClient(store *lib.Config, cfg *schemas.MCPClientConfig) json.RawMessage {
	if store == nil || cfg == nil {
		return nil
	}
	c := *cfg
	if c.StdioConfig != nil {
		stdio := *c.StdioConfig
		stdio.Args = make([]string, len(c.StdioConfig.Args))
		for i := range stdio.Args {
			stdio.Args[i] = "<redacted>"
		}
		stdio.Envs = make([]string, len(c.StdioConfig.Envs))
		for i, env := range c.StdioConfig.Envs {
			if name, _, hasValue := strings.Cut(env, "="); hasValue {
				env = name + "=<redacted>"
			}
			stdio.Envs[i] = env
		}
		c.StdioConfig = &stdio
	}
	redacted := store.RedactMCPClientConfig(&c)
	if redacted.ConnectionString != nil && !redacted.ConnectionString.IsFromSecret() {
		redacted.ConnectionString = schemas.NewSecretVar(lib.RedactURLCredentials(redacted.ConnectionString.GetValue()))
	}
	return lib.AuditSnapshot(redacted)
}

// mcpClientAfterUpdate overlays an update's resolved config on the existing
// client, keeping the fields the update handler does not rebuild (slug, OAuth
// view, state, pending OAuth, an untouched OpenAPI block) so the audit diff does
// not report them as cleared.
func mcpClientAfterUpdate(existing, updated *schemas.MCPClientConfig) *schemas.MCPClientConfig {
	if updated == nil || existing == nil {
		return updated
	}
	c := *updated
	c.EndpointSlug = existing.EndpointSlug
	c.OauthClientID = existing.OauthClientID
	c.OauthClientSecret = existing.OauthClientSecret
	c.OauthAuthorizeURL = existing.OauthAuthorizeURL
	c.OauthTokenURL = existing.OauthTokenURL
	c.OauthRegistrationURL = existing.OauthRegistrationURL
	c.OauthScopes = existing.OauthScopes
	c.OauthResource = existing.OauthResource
	c.State = existing.State
	c.PendingOAuthConfig = existing.PendingOAuthConfig
	if c.OpenAPIConfig == nil {
		c.OpenAPIConfig = existing.OpenAPIConfig
	}
	return &c
}

// auditDeletedIDsLimit caps how many IDs a bulk delete puts in its audit snapshot.
const auditDeletedIDsLimit = 100

// auditDeletedIDs is the before snapshot of a bulk delete: the count, and the
// IDs themselves up to auditDeletedIDsLimit so a large delete stays small.
func auditDeletedIDs(ids []string) map[string]any {
	snap := map[string]any{"count": len(ids)}
	if len(ids) > auditDeletedIDsLimit {
		snap["ids"] = ids[:auditDeletedIDsLimit]
		snap["ids_truncated"] = true
	} else {
		snap["ids"] = ids
	}
	return snap
}

// findMCPClientConfig returns the MCP client config with the given id from the live store, or
// nil. The config is the store's record: read it, never change it. A refresh replaces the record
// rather than changing it, so the config keeps the values it was read with.
func findMCPClientConfig(store *lib.Config, id string) *schemas.MCPClientConfig {
	if store == nil {
		return nil
	}
	client, ok := store.LiveStore.Collections.MCPClients.Get(id)
	if !ok {
		return nil
	}
	return &client.Config
}

// pluginAuditSnapshot is the audit view of a plugin; Config is set only when the plugin itself redacted it.
type pluginAuditSnapshot struct {
	Name      string                   `json:"name"`
	Enabled   bool                     `json:"enabled"`
	Path      *string                  `json:"path,omitempty"`
	IsCustom  bool                     `json:"is_custom"`
	Placement *schemas.PluginPlacement `json:"placement,omitempty"`
	Order     *int                     `json:"order,omitempty"`
	Config    map[string]any           `json:"config,omitempty"`
}

// auditPlugin snapshots a plugin row, keeping its config only when the loaded plugin redacts it.
func (h *PluginsHandler) auditPlugin(plugin *configstoreTables.TablePlugin) json.RawMessage {
	if plugin == nil {
		return nil
	}
	snap := pluginAuditSnapshot{
		Name:      plugin.Name,
		Enabled:   plugin.Enabled,
		Path:      plugin.Path,
		IsCustom:  plugin.IsCustom,
		Placement: plugin.Placement,
		Order:     plugin.Order,
	}
	if configMap, ok := plugin.Config.(map[string]any); ok && h.pluginsLoader != nil {
		if redacted, err := h.pluginsLoader.ExpandPluginConfigForAPI(plugin.Name, configMap); err == nil && redacted != nil {
			snap.Config = redacted
		}
	}
	return lib.AuditSnapshot(snap)
}

// auditProxyConfig snapshots the global proxy config without its password or credentials embedded in the URL.
func auditProxyConfig(cfg *configstoreTables.GlobalProxyConfig) json.RawMessage {
	if cfg == nil {
		return nil
	}
	c := *cfg
	c.Password = ""
	if c.URL != "" {
		// RedactURLCredentials returns a URL it cannot parse unchanged, and a disabled
		// proxy is saved unvalidated, so fall back to masking the userinfo password as text.
		redacted := lib.RedactURLCredentials(c.URL)
		if redacted == c.URL {
			redacted = lib.RedactProxyURLPassword(c.URL)
		}
		c.URL = redacted
	}
	return lib.AuditSnapshot(c, "password")
}

// auditWebhookEndpoint snapshots a webhook endpoint with header values and URL credentials masked; the signing secret never serializes.
func auditWebhookEndpoint(endpoint *configstoreTables.TableWebhookEndpoint) json.RawMessage {
	if endpoint == nil {
		return nil
	}
	c := *redactedWebhookEndpoint(endpoint)
	c.Secret = nil
	c.URL = lib.RedactURLCredentials(c.URL)
	return lib.AuditSnapshot(c)
}

// unparseableURLPlaceholder stands in for a stored URL url.Parse rejects, whose credentials cannot be located to mask.
const unparseableURLPlaceholder = "<redacted: unparseable url>"

// auditFrameworkConfig snapshots the framework config with any credentials stripped from its URLs.
func auditFrameworkConfig(cfg *configstoreTables.TableFrameworkConfig) map[string]any {
	if cfg == nil {
		return nil
	}
	url := func(v *string) any {
		if v == nil {
			return nil
		}
		// RedactURLCredentials hands back a URL it cannot parse unchanged, and an opaque one
		// (https:user:pass@host) keeps its credentials in a part it never inspects.
		if u, err := neturl.Parse(*v); err != nil || u.Opaque != "" {
			return unparseableURLPlaceholder
		}
		return lib.RedactURLCredentials(*v)
	}
	return map[string]any{
		"pricing_url":               url(cfg.PricingURL),
		"pricing_sync_interval":     cfg.PricingSyncInterval,
		"model_parameters_url":      url(cfg.ModelParametersURL),
		"mcp_library_url":           url(cfg.MCPLibraryURL),
		"mcp_library_sync_interval": cfg.MCPLibrarySyncInterval,
		"live_models_sync_interval": cfg.LiveModelsSyncInterval,
		"openapi_url":               url(cfg.OpenAPIURL),
	}
}

// recordAuthConfigChange records an auth config write without either credential: only whether
// auth is on and whether the username or password changed.
func recordAuthConfigChange(ctx *fasthttp.RequestCtx, before, after *configstore.AuthConfig) {
	if after == nil {
		return
	}
	var beforeSnap map[string]any
	if before != nil {
		beforeSnap = map[string]any{"is_enabled": before.IsEnabled}
	}
	changed := func(prev, next *schemas.SecretVar) bool {
		return !prev.Equals(next) // a swapped reference is a change even when both resolve alike
	}
	usernameChanged := before == nil || changed(before.AdminUserName, after.AdminUserName)
	passwordChanged := before == nil || changed(before.AdminPassword, after.AdminPassword)
	if before != nil && before.IsEnabled == after.IsEnabled && !usernameChanged && !passwordChanged {
		return // a save that resubmits the stored auth config changes nothing
	}
	afterSnap := map[string]any{"is_enabled": after.IsEnabled}
	if usernameChanged {
		afterSnap["admin_username_changed"] = true
	}
	if passwordChanged {
		afterSnap["admin_password_changed"] = true
	}
	lib.RecordAuditChange(ctx, "auth_config.updated", "auth_config", "auth_config", "Auth config", beforeSnap, afterSnap)
}
