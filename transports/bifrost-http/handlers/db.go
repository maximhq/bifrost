package handlers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

type DbHandler struct {
	client *bifrost.Bifrost
	logger schemas.Logger
	store  *lib.Config
}

func NewDbHandler(client *bifrost.Bifrost, logger schemas.Logger, store *lib.Config) *DbHandler {
	return &DbHandler{
		client: client,
		logger: logger,
		store:  store,
	}
}

func (h *DbHandler) RegisterRoutes(r *router.Router) {
	r.GET("/api/db", h.GetDbState)
	r.POST("/api/db", h.UpdateDbState)
}

// GetDbState returns the current database configuration from memory
func (h *DbHandler) GetDbState(ctx *fasthttp.RequestCtx) {
	configPath := filepath.Join(filepath.Dir(h.store.ConfigPath), "config.json")

	// Check if config.json exists
	if _, err := os.Stat(configPath); err == nil {
		// config.json exists - read from file
		data, err := os.ReadFile(configPath)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError,
				fmt.Sprintf("failed to read config.json: %v", err), h.logger)
			return
		}

		var configData lib.ConfigData
		if err := json.Unmarshal(data, &configData); err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError,
				fmt.Sprintf("failed to parse config.json: %v", err), h.logger)
			return
		}

		// Return config store config from file
		if configData.ConfigStoreConfig != nil {
			SendJSON(ctx, map[string]any{
				"enabled": configData.ConfigStoreConfig.Enabled,
				"type":    string(configData.ConfigStoreConfig.Type),
				"config":  configData.ConfigStoreConfig.Config,
			}, h.logger)
			return
		}
	}

	// No config.json exists - return SQLite config from memory (default)
	SendJSON(ctx, map[string]any{
		"enabled": true,
		"type":    "sqlite",
		"config": map[string]any{
			"path": h.store.ConfigPath,
		},
	}, h.logger)
}

// UpdateDbState creates or updates config.json with the provided Config struct
func (h *DbHandler) UpdateDbState(ctx *fasthttp.RequestCtx) {
	configPath := filepath.Join(filepath.Dir(h.store.ConfigPath), "config.json")

	var newConfig configstore.Config
	if err := json.Unmarshal(ctx.PostBody(), &newConfig); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest,
			fmt.Sprintf("invalid config format: %v", err), h.logger)
		return
	}

	var configData lib.ConfigData
	if _, err := os.Stat(configPath); err == nil {
		data, err := os.ReadFile(configPath)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to read config.json: %v", err), h.logger)
			return
		}
		if err := json.Unmarshal(data, &configData); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid config.json: %v", err), h.logger)
			return
		}
	}
	// Update config and write back to config.json
	configData.ConfigStoreConfig = &newConfig

	data, err := json.MarshalIndent(configData, "", "  ")
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError,
			"failed to encode config", h.logger)
		return
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError,
			"failed to write config.json", h.logger)
		return
	}

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "config.json updated successfully",
		"config":  newConfig,
	}, h.logger)
}
