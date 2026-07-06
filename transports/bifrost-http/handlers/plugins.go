package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/plugins"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
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
	if !plugin.Enabled {
		pluginStatus.Status = schemas.PluginStatusDisabled
	} else {
		for _, status := range pluginStatuses {
			if plugin.Name == status.Name {
				pluginStatus = status
				break
			}
		}
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
	// Normalize before DB write so SecretVar fields are stored as plain strings.
	normalizedConfig, err := h.normalizePluginConfig(request.Name, request.Config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid plugin configuration: %v", err))
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
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Plugin created in database but failed to load: %v", err))
			return
		}
	}

	plugin, err := h.configStore.GetPlugin(ctx, request.Name)
	if err != nil {
		logger.Error("failed to get plugin: %v", err)
		SendError(ctx, 500, "Failed to retrieve plugin")
		return
	}

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
	var plugin *configstoreTables.TablePlugin
	var err error
	// Fetch the existing plugin to enable config merging below.
	var existingPlugin *configstoreTables.TablePlugin
	existingPlugin, err = h.configStore.GetPlugin(ctx, name)
	if err != nil {
		// If doesn't exist, create it
		if errors.Is(err, configstore.ErrNotFound) {
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
		} else {
			logger.Error("failed to get plugin: %v", err)
			SendError(ctx, 500, "Failed to update plugin")
			return
		}
	}

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
	// We reload the plugin if its enabled, otherwise we stop it
	if request.Enabled {
		if err := h.pluginsLoader.ReloadPlugin(ctx, name, request.Path, mergedConfig, request.Placement, request.Order); err != nil {
			logger.Error("failed to load plugin: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Plugin updated in database but failed to load: %v", err))
			return
		}
	} else {
		ctx.SetUserValue(PluginDisabledKey, true)
		if err := h.pluginsLoader.RemovePlugin(ctx, name); err != nil {
			if !errors.Is(err, plugins.ErrPluginNotFound) {
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Plugin updated in database but failed to stop: %v", err))
				return
			}
			// If not found then we don't need to do anything
		}
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Plugin updated successfully",
		"plugin":  h.buildPluginResponse(ctx, plugin),
	})
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
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Plugin deleted in database but failed to stop: %v", err))
			return
		}
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Plugin deleted successfully",
	})
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
			if i < len(existingSlice) {
				out[i] = restoreRedactedValue(item, existingSlice[i])
			} else {
				out[i] = item
			}
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

// isSecretVarObject returns true if m has the shape of a serialised SecretVar.
func isSecretVarObject(m map[string]any) bool {
	_, hasValue := m["value"]
	_, hasSecretRef := m["ref"]
	_, hasType := m["type"]
	// shipped backward compat: env_var/from_env
	_, hasEnvVar := m["env_var"]
	_, hasFromEnv := m["from_env"]
	return hasValue && ((hasSecretRef && hasType) || (hasEnvVar && hasFromEnv))
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
