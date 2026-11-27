package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// VectorStoresHandler serves the vector stores config.json configures: each one's status and
// its settings, which the dashboard can edit. Stores themselves are configured in config.json only.
type VectorStoresHandler struct {
	store    *lib.Config
	reloader VectorStoreSettingsReloader
}

// VectorStoreSettingsReloader re-reads one store's settings into memory after a write. The
// server implements it; a server that embeds it may override it to act after the reload, as a
// cluster does to have its other nodes reload too.
type VectorStoreSettingsReloader interface {
	ReloadVectorStoreSettings(ctx context.Context, name string) error
}

// NewVectorStoresHandler creates a vector stores handler.
func NewVectorStoresHandler(store *lib.Config, reloader VectorStoreSettingsReloader) *VectorStoresHandler {
	return &VectorStoresHandler{store: store, reloader: reloader}
}

// RegisterRoutes registers the vector store routes. They sit under /api/config, so they carry the
// Settings permission.
func (h *VectorStoresHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	writeRoutes := append(slices.Clip(middlewares), RequireConfigStoreMiddleware(h.store.ConfigStore))
	r.GET("/api/config/vector-stores", lib.ChainMiddlewares(h.listVectorStores, middlewares...))
	r.PUT("/api/config/vector-stores/{name}", lib.ChainMiddlewares(h.updateVectorStore, writeRoutes...))
}

// vectorStoresResponse is GET /api/config/vector-stores. Editable is false without a config store,
// when the settings come from config.json alone.
type vectorStoresResponse struct {
	Editable     bool                  `json:"editable"`
	VectorStores []lib.VectorStoreView `json:"vector_stores"`
}

// listVectorStores handles GET /api/config/vector-stores.
func (h *VectorStoresHandler) listVectorStores(ctx *fasthttp.RequestCtx) {
	SendJSON(ctx, vectorStoresResponse{Editable: h.store.ConfigStore != nil, VectorStores: h.store.VectorStoreViews()})
}

// vectorStoreSettingsUpdate is the body of PUT /api/config/vector-stores/{name}: a field left out
// keeps the stored value, and null resets it to 0.
type vectorStoreSettingsUpdate struct {
	RetentionDays *int
	MaxSizeMB     *int
}

// parseVectorStoreSettingsUpdate reads the body, telling a field left out from one set to null.
func parseVectorStoreSettingsUpdate(body []byte) (vectorStoreSettingsUpdate, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return vectorStoreSettingsUpdate{}, fmt.Errorf("invalid request format: %w", err)
	}
	var update vectorStoreSettingsUpdate
	read := func(key string) (*int, error) {
		raw, present := fields[key]
		if !present {
			return nil, nil
		}
		value := 0
		if string(raw) != "null" {
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, fmt.Errorf("%s must be a whole number", key)
			}
		}
		return &value, nil
	}
	var err error
	if update.RetentionDays, err = read("retention_days"); err != nil {
		return update, err
	}
	if update.MaxSizeMB, err = read("max_size_mb"); err != nil {
		return update, err
	}
	for key := range fields {
		if key != "retention_days" && key != "max_size_mb" {
			return update, fmt.Errorf("unknown field %q: only retention_days and max_size_mb can be changed", key)
		}
	}
	return update, nil
}

// auditVectorStoreSettings is a settings row as an audit snapshot.
func auditVectorStoreSettings(row *configstoreTables.TableVectorStoreSettings) map[string]any {
	if row == nil {
		return nil
	}
	return map[string]any{"name": row.Name, "retention_days": row.RetentionDays, "max_size_mb": row.MaxSizeMB}
}

// updateVectorStore handles PUT /api/config/vector-stores/{name}: it changes one configured
// store's retention_days and max_size_mb. The change is recorded once it is saved, before the
// reload that can still fail; a reload that fails answers 500, the settings saved but not applied.
func (h *VectorStoresHandler) updateVectorStore(ctx *fasthttp.RequestCtx) {
	name, _ := ctx.UserValue("name").(string)
	update, err := parseVectorStoreSettingsUpdate(ctx.PostBody())
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	before, after, err := h.store.SaveVectorStoreSettings(ctx, name, update.RetentionDays, update.MaxSizeMB)
	var shared *lib.SharedVectorStoreError
	var invalid *lib.InvalidVectorStoreSettingsError
	switch {
	case errors.Is(err, lib.ErrVectorStoreNotConfigured):
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("vector store %q is not configured", name))
		return
	case errors.As(err, &shared):
		SendError(ctx, fasthttp.StatusConflict, shared.Error())
		return
	case errors.As(err, &invalid):
		SendError(ctx, fasthttp.StatusBadRequest, invalid.Error())
		return
	case err != nil:
		logger.Warn("failed to save vector store settings: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to save vector store settings")
		return
	}
	rec := lib.RecordAuditChange(ctx, "vector_store_settings.updated", "vector_store", name, name, auditVectorStoreSettings(before), auditVectorStoreSettings(after))
	if err := h.reloader.ReloadVectorStoreSettings(ctx, name); err != nil {
		logger.Warn("vector store settings saved but not reloaded: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Vector store settings updated in database but failed to reload in-memory state")
		return
	}
	view, err := h.store.GetVectorStoreView(name)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("vector store %q is not configured", name))
		return
	}
	// What the request serves, as reloaded: an overlapping save of the other setting shows in it.
	rec.SetAfter(map[string]any{"name": view.Name, "retention_days": view.RetentionDays, "max_size_mb": view.MaxSizeMB})
	SendJSON(ctx, view)
}
