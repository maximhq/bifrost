package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

type PluginsLoader interface {
	ReloadPlugin(ctx context.Context, name string, path *string, pluginConfig any) error
	RemovePlugin(ctx context.Context, name string) error
	GetPluginStatus(ctx context.Context) []schemas.PluginStatus
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
	Name    string         `json:"name"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
	Path    *string        `json:"path"`
}

// UpdatePluginRequest is the request body for updating a plugin
type UpdatePluginRequest struct {
	Enabled bool           `json:"enabled"`
	Path    *string        `json:"path"`
	Config  map[string]any `json:"config"`
}

// RegisterRoutes registers the routes for the PluginsHandler
func (h *PluginsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/plugins", lib.ChainMiddlewares(h.getPlugins, middlewares...))
	r.GET("/api/plugins/{name}", lib.ChainMiddlewares(h.getPlugin, middlewares...))
	r.POST("/api/plugins", lib.ChainMiddlewares(h.createPlugin, middlewares...))
	r.PUT("/api/plugins/{name}", lib.ChainMiddlewares(h.updatePlugin, middlewares...))
	r.DELETE("/api/plugins/{name}", lib.ChainMiddlewares(h.deletePlugin, middlewares...))
}

// getPlugins gets all plugins
func (h *PluginsHandler) getPlugins(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		pluginStatus := h.pluginsLoader.GetPluginStatus(ctx)
		finalPlugins := []struct {
			Name     string               `json:"name"`
			Enabled  bool                 `json:"enabled"`
			Config   any                  `json:"config"`
			IsCustom bool                 `json:"isCustom"`
			Path     *string              `json:"path"`
			Status   schemas.PluginStatus `json:"status"`
		}{}
		for _, pluginStatus := range pluginStatus {
			finalPlugins = append(finalPlugins, struct {
				Name     string               `json:"name"`
				Enabled  bool                 `json:"enabled"`
				Config   any                  `json:"config"`
				IsCustom bool                 `json:"isCustom"`
				Path     *string              `json:"path"`
				Status   schemas.PluginStatus `json:"status"`
			}{
				Name:     pluginStatus.Name,
				Enabled:  true,
				Config:   map[string]any{},
				IsCustom: true,
				Path:     nil,
				Status:   pluginStatus,
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
	// Fetching status
	pluginStatuses := h.pluginsLoader.GetPluginStatus(ctx)
	// Creating ephemeral struct for the plugins
	finalPlugins := []struct {
		Name     string               `json:"name"`
		Enabled  bool                 `json:"enabled"`
		Config   any                  `json:"config"`
		IsCustom bool                 `json:"isCustom"`
		Path     *string              `json:"path"`
		Status   schemas.PluginStatus `json:"status"`
	}{}
	// Iterating over plugin status to get the plugin info
	for _, plugin := range plugins {
		pluginStatus := schemas.PluginStatus{
			Name: plugin.Name,
			Status: schemas.PluginStatusUninitialized,
			Logs: []string{},
		}
		if !plugin.Enabled {
			pluginStatus.Status = schemas.PluginStatusDisabled
		}
		for _, status := range pluginStatuses {
			if plugin.Name == status.Name {
				pluginStatus = status
				break
			}
		}
		finalPlugins = append(finalPlugins, struct {
			Name     string               `json:"name"`
			Enabled  bool                 `json:"enabled"`
			Config   any                  `json:"config"`
			IsCustom bool                 `json:"isCustom"`
			Path     *string              `json:"path"`
			Status   schemas.PluginStatus `json:"status"`
		}{
			Name:     plugin.Name,
			Enabled:  plugin.Enabled,
			Config:   plugin.Config,
			IsCustom: plugin.IsCustom,
			Path:     plugin.Path,
			Status:   pluginStatus,
		})
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
		pluginInfo := struct {
			Name     string               `json:"name"`
			Enabled  bool                 `json:"enabled"`
			Config   any                  `json:"config"`
			IsCustom bool                 `json:"isCustom"`
			Path     *string              `json:"path"`
			Status   schemas.PluginStatus `json:"status"`
		}{}
		for _, pluginStatus := range pluginStatus {
			if pluginStatus.Name == ctx.UserValue("name") {
				pluginInfo = struct {
					Name     string               `json:"name"`
					Enabled  bool                 `json:"enabled"`
					Config   any                  `json:"config"`
					IsCustom bool                 `json:"isCustom"`
					Path     *string              `json:"path"`
					Status   schemas.PluginStatus `json:"status"`
				}{
					Name:     pluginStatus.Name,
					Enabled:  true,
					Config:   map[string]any{},
					IsCustom: true,
					Path:     nil,
					Status:   pluginStatus,
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
	SendJSON(ctx, plugin)
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
	// Check if plugin already exists
	existingPlugin, err := h.configStore.GetPlugin(ctx, request.Name)
	if err == nil && existingPlugin != nil {
		SendError(ctx, fasthttp.StatusConflict, "Plugin already exists")
		return
	}
	if err := h.configStore.CreatePlugin(ctx, &configstoreTables.TablePlugin{
		Name:     request.Name,
		Enabled:  request.Enabled,
		Config:   request.Config,
		Path:     request.Path,
		IsCustom: true,
	}); err != nil {
		logger.Error("failed to create plugin: %v", err)
		SendError(ctx, 500, "Failed to create plugin")
		return
	}

	plugin, err := h.configStore.GetPlugin(ctx, request.Name)
	if err != nil {
		logger.Error("failed to get plugin: %v", err)
		SendError(ctx, 500, "Failed to retrieve plugin")
		return
	}

	// We reload the plugin if its enabled
	if request.Enabled {
		if err := h.pluginsLoader.ReloadPlugin(ctx, request.Name, request.Path, request.Config); err != nil {
			logger.Error("failed to load plugin: %v", err)
			SendJSON(ctx, map[string]any{
				"message": fmt.Sprintf("Plugin created successfully; but failed to load plugin with new config: %v", err),
				"plugin":  plugin,
			})
			return
		}
	}

	ctx.SetStatusCode(fasthttp.StatusCreated)
	SendJSON(ctx, map[string]any{
		"message": "Plugin created successfully",
		"plugin":  plugin,
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
	// Check if plugin exists
	plugin, err = h.configStore.GetPlugin(ctx, name)
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
			SendError(ctx, 404, "Plugin not found")
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
	// Updating the plugin
	if err := h.configStore.UpdatePlugin(ctx, &configstoreTables.TablePlugin{
		Name:     name,
		Enabled:  request.Enabled,
		Config:   request.Config,
		Path:     request.Path,
		IsCustom: plugin.IsCustom,
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
		if err := h.pluginsLoader.ReloadPlugin(ctx, name, request.Path, request.Config); err != nil {
			logger.Error("failed to load plugin: %v", err)
			SendJSON(ctx, map[string]any{
				"message": fmt.Sprintf("Plugin updated successfully; but failed to load plugin with new config: %v", err),
				"plugin":  plugin,
			})
			return
		}
	} else {
		ctx.SetUserValue("isDisabled", true)
		if err := h.pluginsLoader.RemovePlugin(ctx, name); err != nil {
			logger.Error("failed to stop plugin: %v", err)
			SendJSON(ctx, map[string]any{
				"message": fmt.Sprintf("Plugin updated successfully; but failed to stop plugin: %v", err),
				"plugin":  plugin,
			})
			return
		}
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Plugin updated successfully",
		"plugin":  plugin,
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
		logger.Error("failed to stop plugin: %v", err)
		SendJSON(ctx, map[string]any{
			"message": fmt.Sprintf("Plugin deleted successfully; but failed to stop plugin: %v", err),
			"plugin":  name,
		})
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Plugin deleted successfully",
	})
}
