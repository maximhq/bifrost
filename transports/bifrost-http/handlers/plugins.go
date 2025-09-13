package handlers

import (
	"encoding/json"
	"errors"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

type PluginsHandler struct {
	logger      schemas.Logger
	configStore configstore.ConfigStore
}

func NewPluginsHandler(configStore configstore.ConfigStore, logger schemas.Logger) *PluginsHandler {
	return &PluginsHandler{
		configStore: configStore,
		logger:      logger,
	}
}

type CreatePluginRequest struct {
	Name    string                 `json:"name"`
	Enabled bool                   `json:"enabled"`
	Config  map[string]interface{} `json:"config"`
}

type UpdatePluginRequest struct {
	Enabled bool                   `json:"enabled"`
	Config  map[string]interface{} `json:"config"`
}

func (h *PluginsHandler) RegisterRoutes(r *router.Router) {
	r.GET("/api/plugins", h.getPlugins)
	r.GET("/api/plugins/{name}", h.getPlugin)
	r.POST("/api/plugins", h.createPlugin)
	r.PUT("/api/plugins/{name}", h.updatePlugin)
	r.DELETE("/api/plugins/{name}", h.deletePlugin)
}

func (h *PluginsHandler) getPlugins(ctx *fasthttp.RequestCtx) {
	plugins, err := h.configStore.GetPlugins()
	if err != nil {
		h.logger.Error("failed to get plugins: %v", err)
		SendError(ctx, 500, "Failed to retrieve plugins", h.logger)
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"plugins": plugins,
		"count":   len(plugins),
	}, h.logger)
}

func (h *PluginsHandler) getPlugin(ctx *fasthttp.RequestCtx) {
	// Safely validate the "name" parameter
	nameValue := ctx.UserValue("name")
	if nameValue == nil {
		h.logger.Warn("missing required 'name' parameter in request")
		SendError(ctx, 400, "Missing required 'name' parameter", h.logger)
		return
	}

	name, ok := nameValue.(string)
	if !ok {
		h.logger.Warn("invalid 'name' parameter type, expected string but got %T", nameValue)
		SendError(ctx, 400, "Invalid 'name' parameter type, expected string", h.logger)
		return
	}

	if name == "" {
		h.logger.Warn("empty 'name' parameter provided")
		SendError(ctx, 400, "Empty 'name' parameter not allowed", h.logger)
		return
	}

	plugin, err := h.configStore.GetPlugin(name)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Plugin not found", h.logger)
			return
		}
		h.logger.Error("failed to get plugin: %v", err)
		SendError(ctx, 500, "Failed to retrieve plugin", h.logger)
		return
	}
	SendJSON(ctx, plugin, h.logger)
}

func (h *PluginsHandler) createPlugin(ctx *fasthttp.RequestCtx) {
	var request CreatePluginRequest
	if err := json.Unmarshal(ctx.PostBody(), &request); err != nil {
		h.logger.Error("failed to unmarshal create plugin request: %v", err)
		SendError(ctx, 400, "Invalid request body", h.logger)
		return
	}

	// Validate required fields
	if request.Name == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Plugin name is required", h.logger)
		return
	}

	// Check if plugin already exists
	existingPlugin, err := h.configStore.GetPlugin(request.Name)
	if err == nil && existingPlugin != nil {
		SendError(ctx, fasthttp.StatusConflict, "Plugin already exists", h.logger)
		return
	}

	if err := h.configStore.CreatePlugin(&configstore.TablePlugin{
		Name:    request.Name,
		Enabled: request.Enabled,
		Config:  request.Config,
	}); err != nil {
		h.logger.Error("failed to create plugin: %v", err)
		SendError(ctx, 500, "Failed to create plugin", h.logger)
		return
	}

	plugin, err := h.configStore.GetPlugin(request.Name)
	if err != nil {
		h.logger.Error("failed to get plugin: %v", err)
		SendError(ctx, 500, "Failed to retrieve plugin", h.logger)
		return
	}

	ctx.SetStatusCode(fasthttp.StatusCreated)
	SendJSON(ctx, map[string]interface{}{
		"message": "Plugin created successfully",
		"plugin":  plugin,
	}, h.logger)
}

func (h *PluginsHandler) updatePlugin(ctx *fasthttp.RequestCtx) {
	// Safely validate the "name" parameter
	nameValue := ctx.UserValue("name")
	if nameValue == nil {
		h.logger.Warn("missing required 'name' parameter in update plugin request")
		SendError(ctx, 400, "Missing required 'name' parameter", h.logger)
		return
	}

	name, ok := nameValue.(string)
	if !ok {
		h.logger.Warn("invalid 'name' parameter type in update plugin request, expected string but got %T", nameValue)
		SendError(ctx, 400, "Invalid 'name' parameter type, expected string", h.logger)
		return
	}

	if name == "" {
		h.logger.Warn("empty 'name' parameter provided in update plugin request")
		SendError(ctx, 400, "Empty 'name' parameter not allowed", h.logger)
		return
	}

	// Check if plugin exists
	if _, err := h.configStore.GetPlugin(name); err != nil {
		h.logger.Warn("plugin not found for update: %s", name)
		SendError(ctx, fasthttp.StatusNotFound, "Plugin not found", h.logger)
		return
	}

	var request UpdatePluginRequest
	if err := json.Unmarshal(ctx.PostBody(), &request); err != nil {
		h.logger.Error("failed to unmarshal update plugin request: %v", err)
		SendError(ctx, 400, "Invalid request body", h.logger)
		return
	}

	if err := h.configStore.UpdatePlugin(&configstore.TablePlugin{
		Name:    name,
		Enabled: request.Enabled,
		Config:  request.Config,
	}); err != nil {
		h.logger.Error("failed to update plugin: %v", err)
		SendError(ctx, 500, "Failed to update plugin", h.logger)
		return
	}

	plugin, err := h.configStore.GetPlugin(name)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Plugin not found", h.logger)
			return
		}
		h.logger.Error("failed to get plugin: %v", err)
		SendError(ctx, 500, "Failed to retrieve plugin", h.logger)
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Plugin updated successfully",
		"plugin":  plugin,
	}, h.logger)
}

func (h *PluginsHandler) deletePlugin(ctx *fasthttp.RequestCtx) {
	// Safely validate the "name" parameter
	nameValue := ctx.UserValue("name")
	if nameValue == nil {
		h.logger.Warn("missing required 'name' parameter in delete plugin request")
		SendError(ctx, 400, "Missing required 'name' parameter", h.logger)
		return
	}

	name, ok := nameValue.(string)
	if !ok {
		h.logger.Warn("invalid 'name' parameter type in delete plugin request, expected string but got %T", nameValue)
		SendError(ctx, 400, "Invalid 'name' parameter type, expected string", h.logger)
		return
	}

	if name == "" {
		h.logger.Warn("empty 'name' parameter provided in delete plugin request")
		SendError(ctx, 400, "Empty 'name' parameter not allowed", h.logger)
		return
	}

	if err := h.configStore.DeletePlugin(name); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Plugin not found", h.logger)
			return
		}
		h.logger.Error("failed to delete plugin: %v", err)
		SendError(ctx, 500, "Failed to delete plugin", h.logger)
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Plugin deleted successfully",
	}, h.logger)
}
