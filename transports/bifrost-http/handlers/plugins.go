package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/plugins"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

type PluginsLoader interface {
	GetPluginStatus(ctx context.Context) map[string]schemas.PluginStatus
	GetLoadedPluginNames() []string
	ReloadPlugin(ctx context.Context, name string, path *string, pluginConfig any, placement *schemas.PluginPlacement, order *int) error
	RemovePlugin(ctx context.Context, name string) error
	// NormalizePluginConfig converts a raw config map to DB-storage format using
	// the loaded plugin instance if it implements ConfigMarshallerPlugin.
	// Returns nil when the plugin is not loaded or does not implement the interface.
	NormalizePluginConfig(name string, config map[string]any) (map[string]any, error)
	// ExpandPluginConfigForAPI converts a stored config map to API-response format
	// using the loaded plugin instance if it implements ConfigMarshallerPlugin.
	// Returns nil, nil when the plugin is not loaded or does not implement the interface.
	ExpandPluginConfigForAPI(name string, config map[string]any) (map[string]any, error)
}

// PluginsHandler is the handler for the plugins API
type PluginsHandler struct {
	configStore   configstore.ConfigStore
	pluginsLoader PluginsLoader
	pluginLocks   sync.Map // plugin name -> *sync.Mutex, see lockPlugin
}

// lockPlugin serializes the saves of one plugin and returns the unlock. A save writes the row,
// loads the plugin and, when the load fails, restores the row it found; another save of the same
// plugin landing in between would have its saved config overwritten by that restore. The lock
// covers this process only: gateways sharing one config store can still interleave.
func (h *PluginsHandler) lockPlugin(name string) func() {
	lock, _ := h.pluginLocks.LoadOrStore(name, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// NewPluginsHandler creates a new PluginsHandler
func NewPluginsHandler(pluginsLoader PluginsLoader, configStore configstore.ConfigStore) *PluginsHandler {
	return &PluginsHandler{
		pluginsLoader: pluginsLoader,
		configStore:   configStore,
	}
}

// CreatePluginRequest is the request body for creating a plugin
type CreatePluginRequest struct {
	Name      string                   `json:"name"`
	Enabled   bool                     `json:"enabled"`
	Config    map[string]any           `json:"config"`
	Path      *string                  `json:"path"`
	Placement *schemas.PluginPlacement `json:"placement,omitempty"`
	Order     *int                     `json:"order,omitempty"`
}

// UpdatePluginRequest is the request body for updating a plugin
type UpdatePluginRequest struct {
	Enabled   bool                     `json:"enabled"`
	Path      *string                  `json:"path"`
	Config    map[string]any           `json:"config"`
	Placement *schemas.PluginPlacement `json:"placement,omitempty"`
	Order     *int                     `json:"order,omitempty"`
}

// normalizePluginConfig calls the loaded plugin's MarshalConfigForStorage if it
// implements ConfigMarshallerPlugin. Returns config unchanged if the plugin is not
// loaded or does not implement the interface. Returns an error if marshalling fails.
func (h *PluginsHandler) normalizePluginConfig(name string, config map[string]any) (map[string]any, error) {
	out, err := h.pluginsLoader.NormalizePluginConfig(name, config)
	if err != nil {
		return nil, err
	}
	if out != nil {
		return out, nil
	}
	return config, nil
}

// expandPluginConfigForAPI calls the loaded plugin's RedactConfig if it implements
// ConfigMarshallerPlugin. Returns config unchanged if the plugin is not loaded or
// does not implement the interface. Returns an error if redaction fails.
func (h *PluginsHandler) expandPluginConfigForAPI(name string, config map[string]any) (map[string]any, error) {
	out, err := h.pluginsLoader.ExpandPluginConfigForAPI(name, config)
	if err != nil {
		return nil, err
	}
	if out != nil {
		return out, nil
	}
	return config, nil
}

// RegisterRoutes registers the routes for the PluginsHandler
func (h *PluginsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/plugins", lib.ChainMiddlewares(h.getPlugins, middlewares...))
	r.GET("/api/plugins/builtins", lib.ChainMiddlewares(h.getBuiltinPlugins, middlewares...))
	r.GET("/api/plugins/loaded", lib.ChainMiddlewares(h.getLoadedPlugins, middlewares...))
	r.GET("/api/plugins/{name}", lib.ChainMiddlewares(h.getPlugin, middlewares...))
	r.POST("/api/plugins", lib.ChainMiddlewares(h.createPlugin, middlewares...))
	r.PUT("/api/plugins/{name}", lib.ChainMiddlewares(h.updatePlugin, middlewares...))
	r.DELETE("/api/plugins/{name}", lib.ChainMiddlewares(h.deletePlugin, middlewares...))
}

type PluginResponse struct {
	Name       string                   `json:"name"`
	ActualName string                   `json:"actualName"`
	Enabled    bool                     `json:"enabled"`
	Config     any                      `json:"config"`
	IsCustom   bool                     `json:"isCustom"`
	Path       *string                  `json:"path"`
	Placement  *schemas.PluginPlacement `json:"placement,omitempty"`
	Order      *int                     `json:"order,omitempty"`
	Status     schemas.PluginStatus     `json:"status"`
}

// buildPluginResponse constructs a PluginResponse, fetching plugin statuses once.
func (h *PluginsHandler) buildPluginResponse(ctx context.Context, plugin *configstoreTables.TablePlugin) PluginResponse {
	return h.buildPluginResponseWithStatuses(plugin, h.pluginsLoader.GetPluginStatus(ctx))
}

// buildPluginResponseWithStatuses constructs a PluginResponse using pre-fetched statuses.
// Use this in list endpoints to avoid calling GetPluginStatus once per plugin.
func (h *PluginsHandler) buildPluginResponseWithStatuses(plugin *configstoreTables.TablePlugin, pluginStatuses map[string]schemas.PluginStatus) PluginResponse {
	pluginStatus := schemas.PluginStatus{
		Name:   plugin.Name,
		Status: schemas.PluginStatusUninitialized,
		Logs:   []string{},
	}
	// Report the runtime status even when the row says disabled, so a plugin that
	// loaded against its row shows as enabled=false/status=active instead of being
	// masked as disabled. Absent from the runtime set means it really is not loaded.
	loaded := false
	for _, status := range pluginStatuses {
		if plugin.Name == status.Name {
			pluginStatus = status
			loaded = true
			break
		}
	}
	if !loaded && !plugin.Enabled {
		pluginStatus.Status = schemas.PluginStatusDisabled
	}
	config := plugin.Config
	if configMap, ok := plugin.Config.(map[string]any); ok {
		redacted, err := h.expandPluginConfigForAPI(plugin.Name, configMap)
		if err != nil {
			logger.Warn("failed to redact config for plugin %s: %v", plugin.Name, err)
			config = map[string]any{}
		} else {
			config = redacted
		}
	}
	return PluginResponse{
		Name:       plugin.Name,
		ActualName: pluginStatus.Name,
		Enabled:    plugin.Enabled,
		Config:     config,
		IsCustom:   plugin.IsCustom,
		Path:       plugin.Path,
		Placement:  plugin.Placement,
		Order:      plugin.Order,
		Status:     pluginStatus,
	}
}

// getBuiltinPlugins returns the canonical list of built-in plugin names.
func (h *PluginsHandler) getBuiltinPlugins(ctx *fasthttp.RequestCtx) {
	SendJSON(ctx, map[string]any{
		"plugins": lib.GetBuiltinPluginNames(),
	})
}

// getLoadedPlugins returns the names of all plugins currently loaded at runtime, whose
// spans an observability connector can filter.
func (h *PluginsHandler) getLoadedPlugins(ctx *fasthttp.RequestCtx) {
	SendJSON(ctx, map[string]any{
		"plugins": h.pluginsLoader.GetLoadedPluginNames(),
	})
}

// getPlugins gets all plugins
func (h *PluginsHandler) getPlugins(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		pluginStatus := h.pluginsLoader.GetPluginStatus(ctx)
		finalPlugins := []PluginResponse{}
		for name, pluginStatus := range pluginStatus {
			finalPlugins = append(finalPlugins, PluginResponse{
				Name:       pluginStatus.Name,
				ActualName: name,
				Enabled:    true,
				Config:     map[string]any{},
				IsCustom:   true,
				Path:       nil,
				Status:     pluginStatus,
			})
		}
		SendJSON(ctx, map[string]any{
			"plugins": finalPlugins,
			"count":   len(finalPlugins),
		})
		return
	}
	plugins, err := h.configStore.GetPlugins(ctx)
	if err != nil {
		logger.Error("failed to get plugins: %v", err)
		SendError(ctx, 500, "Failed to retrieve plugins")
		return
	}
	pluginStatuses := h.pluginsLoader.GetPluginStatus(ctx)
	finalPlugins := []PluginResponse{}
	for _, plugin := range plugins {
		finalPlugins = append(finalPlugins, h.buildPluginResponseWithStatuses(plugin, pluginStatuses))
	}
	// Creating ephemeral struct
	SendJSON(ctx, map[string]any{
		"plugins": finalPlugins,
		"count":   len(finalPlugins),
	})
}

// getPlugin gets a plugin by name
func (h *PluginsHandler) getPlugin(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		pluginStatus := h.pluginsLoader.GetPluginStatus(ctx)
		pluginInfo := PluginResponse{}
		for name, pluginStatus := range pluginStatus {
			if pluginStatus.Name == ctx.UserValue("name") {
				pluginInfo = PluginResponse{
					Name:       pluginStatus.Name,
					ActualName: name,
					Enabled:    true,
					Config:     map[string]any{},
					IsCustom:   true,
					Path:       nil,
					Status:     pluginStatus,
				}
				break
			}
		}
		SendJSON(ctx, pluginInfo)
		return
	}
	// Safely validate the "name" parameter
	nameValue := ctx.UserValue("name")
	if nameValue == nil {
		logger.Warn("missing required 'name' parameter in request")
		SendError(ctx, 400, "Missing required 'name' parameter")
		return
	}

	name, ok := nameValue.(string)
	if !ok {
		logger.Warn("invalid 'name' parameter type, expected string but got %T", nameValue)
		SendError(ctx, 400, "Invalid 'name' parameter type, expected string")
		return
	}

	if name == "" {
		logger.Warn("empty 'name' parameter provided")
		SendError(ctx, 400, "Empty 'name' parameter not allowed")
		return
	}

	plugin, err := h.configStore.GetPlugin(ctx, name)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Plugin not found")
			return
		}
		logger.Error("failed to get plugin: %v", err)
		SendError(ctx, 500, "Failed to retrieve plugin")
		return
	}
	// Return the same shape as list/create/update — with runtime status
	// merged in — so the UI doesn't see an empty status when refetching a
	// single plugin via useGetPluginQuery.
	SendJSON(ctx, h.buildPluginResponse(ctx, plugin))
}

// createPlugin creates a new plugin
func (h *PluginsHandler) createPlugin(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, 400, "Plugins creation is  not supported when configstore is disabled")
		return
	}
	var request CreatePluginRequest
	if err := json.Unmarshal(ctx.PostBody(), &request); err != nil {
		logger.Error("failed to unmarshal create plugin request: %v", err)
		SendError(ctx, 400, "Invalid request body")
		return
	}
	// Validate required fields
	if request.Name == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Plugin name is required")
		return
	}
	// Validate placement value
	if request.Placement != nil && *request.Placement != "" &&
		*request.Placement != schemas.PluginPlacementPreBuiltin &&
		*request.Placement != schemas.PluginPlacementPostBuiltin {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid placement value. Must be 'pre_builtin' or 'post_builtin'")
		return
	}
	if request.Placement != nil && *request.Placement == "" {
		request.Placement = nil
	}
	// Normalize empty path to nil (treat empty string as built-in plugin)
	if request.Path != nil && *request.Path == "" {
		request.Path = nil
	}
	defer h.lockPlugin(request.Name)()
	// Check if plugin already exists
	existingPlugin, err := h.configStore.GetPlugin(ctx, request.Name)
	if err == nil && existingPlugin != nil {
		SendError(ctx, fasthttp.StatusConflict, "Plugin already exists")
		return
	}
	// Determine if this is a built-in or custom plugin
	isBuiltin := lib.IsBuiltinPlugin(request.Name)
	// Built-in plugins should not have a path
	if isBuiltin && request.Path != nil {
		request.Path = nil
	}
	// A custom plugin path is native code (.so) that gets dlopen()'d, running its init()
	// in-process - that's intentional admin functionality, but only for a caller who
	// actually authenticated, never for a request let through because dashboard auth is
	// unconfigured/disabled. Refuse before any DB write so an unauthenticated caller can't
	// even persist an attacker-controlled path for a later authenticated action to load.
	if !isBuiltin && request.Path != nil {
		if bypassed, _ := ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool); bypassed {
			SendError(ctx, fasthttp.StatusForbidden, "Creating a custom plugin with a path requires genuine admin authentication; dashboard auth is currently disabled or unconfigured. Enable dashboard authentication first.")
			return
		}
	}
	// Normalize before DB write so SecretVar fields are stored as plain strings.
	normalizedConfig, err := h.normalizePluginConfig(request.Name, request.Config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid plugin configuration: %v", err))
		return
	}
	if requireGenuineAuthForPluginConfigChange(ctx, nil, normalizedConfig) {
		return
	}
	// Create DB entry first to avoid orphaned in-memory state if DB write fails
	if err := h.configStore.CreatePlugin(ctx, &configstoreTables.TablePlugin{
		Name:      request.Name,
		Enabled:   request.Enabled,
		Config:    normalizedConfig,
		Path:      request.Path,
		IsCustom:  !isBuiltin,
		Placement: request.Placement,
		Order:     request.Order,
	}); err != nil {
		logger.Error("failed to create plugin: %v", err)
		SendError(ctx, 500, "Failed to create plugin")
		return
	}

	// Reload the plugin into memory if it's enabled
	if request.Enabled {
		if err := h.pluginsLoader.ReloadPlugin(ctx, request.Name, request.Path, normalizedConfig, request.Placement, request.Order); err != nil {
			logger.Error("failed to load plugin: %v", err)
			if rbErr := h.configStore.DeletePlugin(ctx, request.Name); rbErr != nil {
				logger.Error("failed to rollback plugin creation: %v", rbErr)
			}
			SendError(ctx, pluginLoadFailureStatus(err), fmt.Sprintf("Plugin was not created, it failed to load: %v", err))
			return
		}
	}

	plugin, err := h.configStore.GetPlugin(ctx, request.Name)
	if err != nil {
		logger.Error("failed to get plugin: %v", err)
		SendError(ctx, 500, "Failed to retrieve plugin")
		return
	}

	lib.RecordAuditChange(ctx, "plugin.created", "plugin", plugin.Name, plugin.Name, nil, h.auditPlugin(plugin))
	ctx.SetStatusCode(fasthttp.StatusCreated)
	SendJSON(ctx, map[string]any{
		"message": "Plugin created successfully",
		"plugin":  h.buildPluginResponse(ctx, plugin),
	})
}

// updatePlugin updates an existing plugin
func (h *PluginsHandler) updatePlugin(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, 400, "Plugins update is not supported when configstore is disabled")
		return
	}
	// Safely validate the "name" parameter
	nameValue := ctx.UserValue("name")
	if nameValue == nil {
		logger.Warn("missing required 'name' parameter in update plugin request")
		SendError(ctx, 400, "Missing required 'name' parameter")
		return
	}

	name, ok := nameValue.(string)
	if !ok {
		logger.Warn("invalid 'name' parameter type in update plugin request, expected string but got %T", nameValue)
		SendError(ctx, 400, "Invalid 'name' parameter type, expected string")
		return
	}

	if name == "" {
		logger.Warn("empty 'name' parameter provided in update plugin request")
		SendError(ctx, 400, "Empty 'name' parameter not allowed")
		return
	}
	defer h.lockPlugin(name)()
	var plugin *configstoreTables.TablePlugin
	var err error
	// Fetch the existing plugin to enable config merging below.
	var existingPlugin *configstoreTables.TablePlugin
	existingPlugin, err = h.configStore.GetPlugin(ctx, name)
	// A plugin with no row is created, but only once the request has been read, validated and
	// admitted below: a refused or malformed update must leave nothing behind.
	missing := false
	if err != nil {
		if !errors.Is(err, configstore.ErrNotFound) {
			logger.Error("failed to get plugin: %v", err)
			SendError(ctx, 500, "Failed to update plugin")
			return
		}
		missing = true
	}
	before := h.auditPlugin(existingPlugin)

	// Unmarshalling the request body
	var request UpdatePluginRequest
	if err := json.Unmarshal(ctx.PostBody(), &request); err != nil {
		logger.Error("failed to unmarshal update plugin request: %v", err)
		SendError(ctx, 400, "Invalid request body")
		return
	}
	// Validate placement value
	if request.Placement != nil && *request.Placement != "" &&
		*request.Placement != schemas.PluginPlacementPreBuiltin &&
		*request.Placement != schemas.PluginPlacementPostBuiltin {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid placement value. Must be 'pre_builtin' or 'post_builtin'")
		return
	}
	if request.Placement != nil && *request.Placement == "" {
		request.Placement = nil
	}
	// Normalize empty path to nil (treat empty string as built-in plugin)
	if request.Path != nil && *request.Path == "" {
		request.Path = nil
	}
	// Determine if this is a built-in plugin
	isBuiltin := lib.IsBuiltinPlugin(name)
	// Built-in plugins should not have a path
	if isBuiltin && request.Path != nil {
		request.Path = nil
	}
	// See the matching check in createPlugin: a custom plugin path is native code that
	// gets dlopen()'d in-process, so setting or changing one requires genuine
	// authentication even while the rest of the management API stays open.
	if !isBuiltin && request.Path != nil {
		if bypassed, _ := ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool); bypassed {
			SendError(ctx, fasthttp.StatusForbidden, "Setting a custom plugin path requires genuine admin authentication; dashboard auth is currently disabled or unconfigured. Enable dashboard authentication first.")
			return
		}
	}
	// Merge incoming config over the existing DB config so fields unknown to the
	// calling form (e.g. plugin_span_filter set by a separate UI sheet) are not wiped.
	mergedConfig := request.Config
	if existingPlugin != nil {
		if existingCfg, ok := existingPlugin.Config.(map[string]any); ok && len(existingCfg) > 0 {
			mergedConfig = make(map[string]any, len(existingCfg)+len(request.Config))
			maps.Copy(mergedConfig, existingCfg)
			// Before overwriting, substitute any redacted SecretVar placeholders in the
			// incoming config with the existing stored value so credentials are not
			// replaced by "***" or similar client-side redaction markers.
			incoming := restoreRedactedFromExisting(request.Config, existingCfg)
			maps.Copy(mergedConfig, incoming)
		}
	}
	// Normalize through the typed plugin config so custom MarshalJSON (e.g. SecretVar → string) runs.
	mergedConfig, err = h.normalizePluginConfig(name, mergedConfig)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid plugin configuration: %v", err))
		return
	}
	var storedConfig map[string]any
	if existingPlugin != nil {
		storedConfig, _ = existingPlugin.Config.(map[string]any)
	}
	// The stored config is read the way the update was: a plugin provisioned through config.json
	// is stored as written (for OTEL, possibly the legacy single-profile shape), while the update
	// went through the plugin's normalization, so comparing the raw row would report an unchanged
	// destination as moved. A stored config that does not normalize is compared as it is.
	if len(storedConfig) > 0 {
		if normalized, err := h.normalizePluginConfig(name, storedConfig); err == nil {
			storedConfig = normalized
		}
	}
	if requireGenuineAuthForPluginConfigChange(ctx, storedConfig, mergedConfig) {
		return
	}
	if missing {
		plugin = &configstoreTables.TablePlugin{
			Name:     name,
			Enabled:  false,
			Config:   map[string]any{},
			Path:     nil,
			IsCustom: false,
		}
		if err := h.configStore.CreatePlugin(ctx, plugin); err != nil {
			logger.Error("failed to create plugin: %v", err)
			SendError(ctx, 500, "Failed to create plugin")
			return
		}
	}
	// Updating the plugin
	if err := h.configStore.UpdatePlugin(ctx, &configstoreTables.TablePlugin{
		Name:      name,
		Enabled:   request.Enabled,
		Config:    mergedConfig,
		Path:      request.Path,
		IsCustom:  !isBuiltin,
		Placement: request.Placement,
		Order:     request.Order,
	}); err != nil {
		logger.Error("failed to update plugin: %v", err)
		SendError(ctx, 500, "Failed to update plugin")
		return
	}
	plugin, err = h.configStore.GetPlugin(ctx, name)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Plugin not found")
			return
		}
		logger.Error("failed to get plugin: %v", err)
		SendError(ctx, 500, "Failed to retrieve plugin")
		return
	}
	auditEvent := "plugin.updated"
	if missing {
		auditEvent = "plugin.created"
	}
	// We reload the plugin if its enabled, otherwise we stop it
	if request.Enabled {
		if err := h.pluginsLoader.ReloadPlugin(ctx, name, request.Path, mergedConfig, request.Placement, request.Order); err != nil {
			logger.Error("failed to load plugin: %v", err)
			h.restorePluginRow(ctx, name, existingPlugin, err)
			SendError(ctx, pluginLoadFailureStatus(err), fmt.Sprintf("Plugin update was not applied, it failed to load and the previous configuration was kept: %v", err))
			return
		}
	} else {
		ctx.SetUserValue(PluginDisabledKey, true)
		if err := h.pluginsLoader.RemovePlugin(ctx, name); err != nil {
			if !errors.Is(err, plugins.ErrPluginNotFound) {
				// The row is committed, so record the change even though the runtime stop failed.
				lib.RecordAuditChange(ctx, auditEvent, "plugin", name, name, before, h.auditPlugin(plugin))
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Plugin updated in database but failed to stop: %v", err))
				return
			}
			// If not found then we don't need to do anything
		}
	}

	lib.RecordAuditChange(ctx, auditEvent, "plugin", name, name, before, h.auditPlugin(plugin))
	SendJSON(ctx, map[string]interface{}{
		"message": "Plugin updated successfully",
		"plugin":  h.buildPluginResponse(ctx, plugin),
	})
}

// pluginLoadFailureStatus is the status for a plugin that failed to load with the config it was
// given: 400 when the config itself is what was refused (a config that fails validation, a vector
// store namespace that already holds vectors of another size), 500 for anything else.
func pluginLoadFailureStatus(err error) int {
	if errors.Is(err, lib.ErrInvalidPluginConfig) || errors.Is(err, vectorstore.ErrDimensionMismatch) {
		return fasthttp.StatusBadRequest
	}
	return fasthttp.StatusInternalServerError
}

// restorePluginRow puts the plugin's saved row back the way it was before an update that then
// failed to load, so the saved config keeps describing the instance still running (the reload
// leaves it in place). A row the update created itself is removed instead. When the restore fails
// the saved config and the running plugin disagree until the next successful save, so it is
// logged as an error naming the plugin and both failures.
func (h *PluginsHandler) restorePluginRow(ctx context.Context, name string, previous *configstoreTables.TablePlugin, loadErr error) {
	var err error
	if previous == nil {
		err = h.configStore.DeletePlugin(ctx, name)
	} else {
		// UpdatePlugin reads a nil placement or order as "keep the current value", which would
		// keep whatever the failed update set, so the previous row is written back whole.
		restored := *previous
		err = h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			if err := h.configStore.DeletePlugin(ctx, name, tx); err != nil {
				return err
			}
			return h.configStore.CreatePlugin(ctx, &restored, tx)
		})
	}
	if err != nil {
		logger.Error("failed to restore plugin %s after a failed update, its saved config no longer matches the running instance: %v (update failed with: %v)", name, err, loadErr)
	}
}

// deletePlugin deletes an existing plugin
func (h *PluginsHandler) deletePlugin(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, 400, "Plugins deletion is not supported when configstore is disabled")
		return
	}
	// Safely validate the "name" parameter
	nameValue := ctx.UserValue("name")
	if nameValue == nil {
		logger.Warn("missing required 'name' parameter in delete plugin request")
		SendError(ctx, 400, "Missing required 'name' parameter")
		return
	}

	name, ok := nameValue.(string)
	if !ok {
		logger.Warn("invalid 'name' parameter type in delete plugin request, expected string but got %T", nameValue)
		SendError(ctx, 400, "Invalid 'name' parameter type, expected string")
		return
	}

	if name == "" {
		logger.Warn("empty 'name' parameter provided in delete plugin request")
		SendError(ctx, 400, "Empty 'name' parameter not allowed")
		return
	}
	defer h.lockPlugin(name)()

	existingPlugin, _ := h.configStore.GetPlugin(ctx, name)
	before := h.auditPlugin(existingPlugin)
	if err := h.configStore.DeletePlugin(ctx, name); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Plugin not found")
			return
		}
		logger.Error("failed to delete plugin: %v", err)
		SendError(ctx, 500, "Failed to delete plugin")
		return
	}

	if err := h.pluginsLoader.RemovePlugin(ctx, name); err != nil {
		if !errors.Is(err, plugins.ErrPluginNotFound) {
			// The row is already gone, so the change is real even though the runtime stop failed.
			lib.RecordAuditChange(ctx, "plugin.deleted", "plugin", name, name, before, nil)
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Plugin deleted in database but failed to stop: %v", err))
			return
		}
	}
	lib.RecordAuditChange(ctx, "plugin.deleted", "plugin", name, name, before, nil)
	SendJSON(ctx, map[string]interface{}{
		"message": "Plugin deleted successfully",
	})
}

// pluginEndpointKeys are plugin config keys whose string value is a host the plugin dials with
// what the rest of its config tells it to send: the OTEL collector and metrics endpoints, the
// telemetry push gateway, and the generic names a future plugin is likely to use.
var pluginEndpointKeys = map[string]bool{
	"collector_url":    true,
	"metrics_endpoint": true,
	"push_gateway_url": true,
	"endpoint":         true,
	"url":              true,
	"base_url":         true,
}

// isPluginEndpointKey reports key as a pluginEndpointKeys key, matched the way the typed plugin
// loaders match JSON keys to fields: case-insensitively with Unicode case folding, as encoding/json
// and sonic do, so PUSH_GATEWAY_URL sets the same field as push_gateway_url.
func isPluginEndpointKey(key string) bool {
	if pluginEndpointKeys[key] {
		return true
	}
	for endpoint := range pluginEndpointKeys {
		if strings.EqualFold(key, endpoint) {
			return true
		}
	}
	return false
}

// isSecretReference reports an env. or vault. reference by its prefix alone. The value is never
// handed to NewSecretVar here, which would resolve it from the gateway's environment or vault.
func isSecretReference(value string) bool {
	return strings.HasPrefix(value, "env.") || strings.HasPrefix(value, "vault.")
}

// pluginConfigDialTargets flattens a plugin config into the values that decide where the plugin
// sends data and what it sends: every env./vault. reference at any depth, keyed by its path, and
// every non-empty value under a pluginEndpointKeys key. A loaded plugin's normalization turns
// SecretVar objects into plain strings, but a plugin that is not loaded has no normalizer and its
// config keeps them as objects ({"value": ...}, {"ref": "env.X"}, or the legacy
// {"env_var": "X", "from_env": true}), which its loader still reads; such an object counts as
// the string it stands for.
func pluginConfigDialTargets(prefix string, value any, out map[string]string) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			// Any object can stand for a SecretVar: SecretVar.UnmarshalJSON reads ref, type, env_var
			// and value and ignores other fields, so extra fields cannot hide a reference. An object
			// made only of those fields may also be a plain map whose keys match them (an OTEL header
			// named ref), so besides its target, each reference among its values that the target does
			// not already account for is recorded under its own path; its fields are not walked again,
			// so an unchanged reference written as an object is not reported as new. An object with
			// other fields is walked below.
			if object, ok := item.(map[string]any); ok {
				target := secretVarObjectTarget(object, isPluginEndpointKey(key))
				if target != "" {
					out[path] = target
				}
				if isSecretVarFieldsOnly(object) {
					for field, fieldValue := range object {
						if ref, ok := fieldValue.(string); ok && isSecretReference(ref) && ref != target {
							out[path+"."+field] = ref
						}
					}
					continue
				}
			}
			if s, ok := item.(string); ok && isPluginEndpointKey(key) && s != "" {
				out[path] = s
				continue
			}
			pluginConfigDialTargets(path, item, out)
		}
	case []any:
		for i, item := range v {
			pluginConfigDialTargets(fmt.Sprintf("%s[%d]", prefix, i), item, out)
		}
	case string:
		if isSecretReference(v) {
			out[prefix] = v
		}
	}
}

// secretVarObjectTarget is the dial target a SecretVar-shaped object stands for, read with the
// precedence SecretVar.UnmarshalJSON applies: an explicit type decides (env or vault make the ref
// the target, plain_text makes the value the target even when a ref is present); without a type a
// ref is classified by its prefix; without either, the legacy env_var with from_env names an env
// reference. A plain value is a target under an endpoint key, or when it is itself a reference.
// Fields are found case-insensitively, as the decoder finds them; two keys that fold to the same
// field are decided by their order in the request, which the parsed map no longer has, so such an
// object stands for itself as a whole and any change to it counts. Nothing is resolved here.
func secretVarObjectTarget(object map[string]any, endpoint bool) string {
	fields := map[string]any{}
	for _, name := range secretVarFieldNames {
		value, ambiguous := secretVarField(object, name)
		if ambiguous {
			whole, _ := json.Marshal(object)
			return "ambiguous SecretVar " + string(whole)
		}
		fields[name] = value
	}
	ref, _ := fields["ref"].(string)
	ref = strings.TrimSpace(ref)
	secretType, _ := fields["type"].(string)
	switch {
	case secretType != "":
		switch schemas.SecretType(secretType) {
		case schemas.SecretTypeEnv:
			if ref != "" {
				return withSecretPrefix("env.", ref)
			}
		case schemas.SecretTypeVault:
			if ref != "" {
				return withSecretPrefix("vault.", ref)
			}
		}
	case ref != "":
		if isSecretReference(ref) {
			return ref
		}
	default:
		if envVar, _ := fields["env_var"].(string); envVar != "" {
			if fromEnv, _ := fields["from_env"].(bool); fromEnv {
				return withSecretPrefix("env.", envVar)
			}
		}
	}
	value, _ := fields["value"].(string)
	if isSecretReference(value) || endpoint {
		return value
	}
	return ""
}

// withSecretPrefix returns ref with prefix, unless it already carries an env. or vault. prefix.
func withSecretPrefix(prefix, ref string) string {
	if isSecretReference(ref) {
		return ref
	}
	return prefix + ref
}

// secretVarFieldNames are the fields SecretVar.UnmarshalJSON reads.
var secretVarFieldNames = []string{"value", "ref", "type", "env_var", "from_env"}

// secretVarField returns the value under the key that folds to name, as the JSON decoder matches
// it. ambiguous reports more than one such key.
func secretVarField(object map[string]any, name string) (value any, ambiguous bool) {
	found := false
	for key, item := range object {
		if strings.EqualFold(key, name) {
			if found {
				return nil, true
			}
			value, found = item, true
		}
	}
	return value, false
}

// isSecretVarFieldsOnly reports an object whose keys all fold to SecretVar fields.
func isSecretVarFieldsOnly(object map[string]any) bool {
	for key := range object {
		if !slices.ContainsFunc(secretVarFieldNames, func(name string) bool { return strings.EqualFold(key, name) }) {
			return false
		}
	}
	return len(object) > 0
}

// requireGenuineAuthForPluginConfigChange refuses a built-in plugin create or update that adds
// or changes an outbound endpoint or an env./vault. reference when the caller was only let
// through by the fail-open bypass (dashboard auth disabled, unconfigured, or a whitelisted
// route), not by a real credential. A plugin's config names where the gateway sends data and
// what it sends: the OTEL plugin resolves env. references in its headers from the gateway's own
// process environment and posts them to the collector on every export, and the telemetry plugin
// does the same for its push gateway and basic auth, so a caller with no credential check must
// not be able to read the gateway's secrets by naming them, nor redirect exports. This is the
// plugins counterpart of the MCP, provider, provider-key and webhook gates. stored is the
// persisted normalized config on update and nil on create; a value identical to the stored one
// passes, as does removing one, so bypassed callers can still toggle a plugin or edit its other
// fields. Config.json provisioning is not gated. On rejection the response has been sent and
// true is returned; callers should return immediately.
func requireGenuineAuthForPluginConfigChange(ctx *fasthttp.RequestCtx, stored, next map[string]any) bool {
	if !isAuthBypassed(ctx) {
		return false
	}
	before, after := map[string]string{}, map[string]string{}
	pluginConfigDialTargets("", stored, before)
	pluginConfigDialTargets("", next, after)
	var changed []string
	for path, value := range after {
		if before[path] != value {
			changed = append(changed, path)
		}
	}
	if len(changed) == 0 {
		return false
	}
	slices.Sort(changed)
	SendError(ctx, fasthttp.StatusForbidden, fmt.Sprintf("Setting a plugin's endpoints or env./vault. references (%s) requires an authenticated admin session; dashboard auth is currently disabled or unconfigured. Enable dashboard authentication, or provision the plugin via config.json instead.", strings.Join(changed, ", ")))
	return true
}

// restoreRedactedFromExisting walks the incoming config map and, for any field whose
// value is a redacted placeholder (a masked EnvVar object, or a masked plain string),
// replaces it with the corresponding value from the existing DB
// config so client-side redaction never overwrites real credentials. It descends into
// nested maps AND slices (e.g. the OTEL `profiles` array), and handles header values that
// are stored as plain strings rather than EnvVar objects. Mirrors the mergeUpdatedKey
// pattern used by provider keys.
func restoreRedactedFromExisting(incoming, existing map[string]any) map[string]any {
	if len(incoming) == 0 {
		return incoming
	}
	result := make(map[string]any, len(incoming))
	for k, v := range incoming {
		result[k] = restoreRedactedValue(v, existing[k])
	}
	return result
}

// restoreRedactedValue restores a single incoming value against its corresponding existing
// value. It recurses through maps and slices, and treats both EnvVar-shaped objects and
// plain redacted strings as placeholders to swap back to the stored original. Returns the
// incoming value unchanged when it is not a redaction placeholder or has no stored match.
func restoreRedactedValue(incoming, existing any) any {
	switch val := incoming.(type) {
	case map[string]any:
		if isSecretVarObject(val) {
			if schemas.NewSecretVar(marshalSecretVarObject(val)).ShouldPreserveStored() && existing != nil {
				return existing
			}
			return val
		}
		if existingNested, ok := existing.(map[string]any); ok {
			return restoreRedactedFromExisting(val, existingNested)
		}
		return val
	case []any:
		// Restore element-by-element against the existing slice (index-aligned). New
		// elements beyond the existing length carry user-supplied values, so keep them.
		existingSlice, ok := existing.([]any)
		if !ok {
			return val
		}
		out := make([]any, len(val))
		for i, item := range val {
			// A plain string inside an array is a list entry (header pattern, excluded
			// attribute, broker), never a redacted scalar: no plugin config stores secrets
			// as array elements. Restoring them swapped a lone "*" header pattern for the
			// stored one, because IsRedacted treats an all-asterisk string as a placeholder.
			if _, isString := item.(string); isString || i >= len(existingSlice) {
				out[i] = item
				continue
			}
			out[i] = restoreRedactedValue(item, existingSlice[i])
		}
		return out
	case string:
		// Plain-string secrets (e.g. OTEL headers): restore only when the incoming string
		// is a redaction artifact and not an intentional env reference. Empty strings are
		// left as-is so clearing a value works.
		if existingStr, ok := existing.(string); ok {
			secretVal := schemas.NewSecretVar(val)
			if !secretVal.IsFromSecret() && secretVal.IsRedacted() {
				return existingStr
			}
		}
		return val
	default:
		return incoming
	}
}

// isSecretVarObject returns true if m has the shape of a serialised SecretVar: a string
// "value" plus, optionally, only SecretVar keys. Plain-text SecretVars marshal as
// {"value": "..."} alone (ref/type are omitempty), so value-only objects must match too —
// e.g. the Kafka SASL credentials round-tripped by the UI after a redacted GET.
func isSecretVarObject(m map[string]any) bool {
	if _, ok := m["value"].(string); !ok {
		return false
	}
	for k := range m {
		switch k {
		// "env_var"/"from_env" are shipped backward compat for "ref"/"type".
		case "value", "ref", "type", "env_var", "from_env":
		default:
			return false
		}
	}
	return true
}

// marshalSecretVarObject serialises a SecretVar-shaped map back to the JSON string that
// schemas.NewSecretVar expects so we can call ShouldPreserveStored on it.
func marshalSecretVarObject(m map[string]any) string {
	value, _ := m["value"].(string)
	if secretRef, ok := m["ref"].(string); ok {
		secretType, _ := m["type"].(string)
		if secretType != "" {
			return fmt.Sprintf(`{"value":%q,"ref":%q,"type":%q}`, value, secretRef, secretType)
		}
		return fmt.Sprintf(`{"value":%q}`, value)
	}
	// backward compat: old env_var/from_env format
	secretVar, _ := m["env_var"].(string)
	fromEnv, _ := m["from_env"].(bool)
	if fromEnv {
		return fmt.Sprintf(`{"value":%q,"env_var":%q,"from_env":true}`, value, secretVar)
	}
	return fmt.Sprintf(`{"value":%q,"env_var":%q,"from_env":false}`, value, secretVar)
}
