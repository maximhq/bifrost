package handlers

import (
	"errors"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/featureflags"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// FeatureFlagsHandler serves the toggle UI/API. It mediates between the
// HTTP layer, the in-memory featureflags.Store, and the configstore where
// overrides are persisted. The store is the source of truth for the
// effective value; the configstore exists only so toggles survive restarts.
type FeatureFlagsHandler struct {
	store       *featureflags.Store
	configStore configstore.ConfigStore
}

// NewFeatureFlagsHandler wires the handler to its dependencies. Both must
// be non-nil at server boot; the handler intentionally does not lazily
// resolve them because feature flag state is needed during request
// dispatch and a missing store would cause silent off-by-default behavior.
func NewFeatureFlagsHandler(store *featureflags.Store, configStore configstore.ConfigStore) *FeatureFlagsHandler {
	return &FeatureFlagsHandler{store: store, configStore: configStore}
}

// RegisterRoutes mounts the feature flag endpoints. Only GET and PUT are
// exposed: flags are code-declared via featureflags.Register, so there is
// nothing to "create" or "delete" via the API. Stale DB rows for
// unregistered flags surface in the list with registered=false so
// operators can see them, but they cannot be toggled or removed.
func (h *FeatureFlagsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/feature-flags", lib.ChainMiddlewares(h.listFlags, middlewares...))
	r.PUT("/api/feature-flags/{id}", lib.ChainMiddlewares(h.updateFlag, middlewares...))
}

// featureFlagsListResponse keeps the wire format flexible: wrapping the
// array in an object lets us add pagination / counts later without breaking
// existing UI clients.
type featureFlagsListResponse struct {
	Flags []featureflags.FlagStatus `json:"flags"`
}

func (h *FeatureFlagsHandler) listFlags(ctx *fasthttp.RequestCtx) {
	SendJSON(ctx, featureFlagsListResponse{Flags: h.store.List()})
}

type updateFlagRequest struct {
	// Pointer so that a missing field decodes as nil rather than false;
	// otherwise a PUT with an empty body silently disables the flag.
	Enabled *bool `json:"enabled"`
}

func (h *FeatureFlagsHandler) updateFlag(ctx *fasthttp.RequestCtx) {
	id, ok := ctx.UserValue("id").(string)
	if !ok || id == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid flag id")
		return
	}

	var req updateFlagRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if req.Enabled == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Missing required field: enabled")
		return
	}
	enabled := *req.Enabled

	// Capture prior in-memory state verbatim so a failed DB write below
	// can be rolled back without corrupting metadata. Snapshot's second
	// return distinguishes "had an override" from "was at code default";
	// Restore uses that to delete vs. write-back, preserving the original
	// source (default / db / file) and timestamps exactly.
	priorSnap, priorHad := h.store.Snapshot(id)

	status, err := h.store.Set(ctx, id, enabled)
	switch {
	case errors.Is(err, featureflags.ErrFlagLocked):
		SendError(ctx, fasthttp.StatusConflict, "Feature flag is locked by config.json / Helm")
		return
	case errors.Is(err, featureflags.ErrFlagEnterpriseOnly):
		SendError(ctx, fasthttp.StatusForbidden, "Feature flag is enterprise-only")
		return
	case errors.Is(err, featureflags.ErrFlagUnregistered):
		SendError(ctx, fasthttp.StatusNotFound, "Feature flag is not registered")
		return
	case err != nil:
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to update feature flag: "+err.Error())
		return
	}

	// Persist after the in-memory toggle succeeds. If the DB write fails
	// we roll back the in-memory change to keep the two layers consistent;
	// otherwise a subsequent restart would silently revert the operator's
	// toggle and they would have no way to know.
	if h.configStore != nil {
		if err := h.configStore.UpsertFeatureFlag(ctx, id, enabled, status.UpdatedAt); err != nil {
			h.store.Restore(id, priorSnap, priorHad)
			SendError(ctx, fasthttp.StatusInternalServerError, "Failed to persist feature flag: "+err.Error())
			return
		}
	}

	SendJSON(ctx, status)
}
