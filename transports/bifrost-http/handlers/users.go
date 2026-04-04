package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// UsersHandler manages HTTP requests for user management operations.
type UsersHandler struct {
	configStore configstore.ConfigStore
}

// NewUsersHandler creates a new users handler instance.
func NewUsersHandler(configStore configstore.ConfigStore) *UsersHandler {
	return &UsersHandler{
		configStore: configStore,
	}
}

// RegisterRoutes registers the user management routes.
func (h *UsersHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/users", lib.ChainMiddlewares(h.listUsers, middlewares...))
	r.GET("/api/users/me", lib.ChainMiddlewares(h.getCurrentUser, middlewares...))
	r.GET("/api/users/{id}", lib.ChainMiddlewares(h.getUser, middlewares...))
	r.PUT("/api/users/{id}/role", lib.ChainMiddlewares(h.updateUserRole, middlewares...))
	r.DELETE("/api/users/{id}", lib.ChainMiddlewares(h.deleteUser, middlewares...))
}

// listUsers handles GET /api/users - List users with pagination and optional search.
func (h *UsersHandler) listUsers(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}

	limit := 25
	offset := 0
	if l := string(ctx.QueryArgs().Peek("limit")); l != "" {
		if i, err := strconv.Atoi(l); err == nil {
			limit = i
		}
	}
	if o := string(ctx.QueryArgs().Peek("offset")); o != "" {
		if i, err := strconv.Atoi(o); err == nil {
			offset = i
		}
	}
	limit, offset = ClampPaginationParams(limit, offset)
	search := string(ctx.QueryArgs().Peek("search"))

	users, total, err := h.configStore.GetUsersPaginated(ctx, configstore.UsersQueryParams{
		Limit:  limit,
		Offset: offset,
		Search: search,
	})
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to list users: %v", err))
		return
	}

	SendJSON(ctx, map[string]any{
		"users":  users,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// getCurrentUser handles GET /api/users/me - Get the currently authenticated user.
func (h *UsersHandler) getCurrentUser(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}

	userID, _ := ctx.UserValue(schemas.BifrostContextKeyUserID).(string)
	if userID == "" {
		// No user ID in context: return a default admin representation
		SendJSON(ctx, map[string]any{
			"id":    "admin",
			"email": "admin",
			"name":  "Admin",
			"role":  "admin",
		})
		return
	}

	user, err := h.configStore.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "user not found")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get user: %v", err))
		return
	}

	SendJSON(ctx, user)
}

// getUser handles GET /api/users/{id} - Get a specific user by ID.
func (h *UsersHandler) getUser(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}

	id, ok := ctx.UserValue("id").(string)
	if !ok || id == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "user ID is required")
		return
	}

	user, err := h.configStore.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "user not found")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get user: %v", err))
		return
	}

	SendJSON(ctx, user)
}

// updateUserRole handles PUT /api/users/{id}/role - Update a user's role.
func (h *UsersHandler) updateUserRole(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}

	id, ok := ctx.UserValue("id").(string)
	if !ok || id == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "user ID is required")
		return
	}

	payload := struct {
		Role string `json:"role"`
	}{}
	if err := json.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid request format: %v", err))
		return
	}

	if payload.Role != "admin" && payload.Role != "viewer" {
		SendError(ctx, fasthttp.StatusBadRequest, "role must be 'admin' or 'viewer'")
		return
	}

	user, err := h.configStore.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "user not found")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get user: %v", err))
		return
	}

	user.Role = payload.Role
	if err := h.configStore.UpdateUser(ctx, user); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update user role: %v", err))
		return
	}

	SendJSON(ctx, user)
}

// deleteUser handles DELETE /api/users/{id} - Delete a user.
func (h *UsersHandler) deleteUser(ctx *fasthttp.RequestCtx) {
	if h.configStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}

	id, ok := ctx.UserValue("id").(string)
	if !ok || id == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "user ID is required")
		return
	}

	if err := h.configStore.DeleteUser(ctx, id); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "user not found")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to delete user: %v", err))
		return
	}

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "user deleted successfully",
	})
}
