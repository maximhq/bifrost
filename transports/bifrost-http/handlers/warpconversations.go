package handlers

import (
	"errors"
	"strconv"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/valyala/fasthttp"
)

// warpOwnerFor resolves the caller's history owner from the request context,
// never from the body; see warp.OwnerFromContext for why.
func warpOwnerFor(ctx *fasthttp.RequestCtx) string {
	userID, _ := ctx.UserValue(schemas.BifrostContextKeyUserID).(string)
	return schemas.WarpOwnerID(userID)
}

// listConversations returns the caller's threads, most recent first.
func (h *WarpHandler) listConversations(ctx *fasthttp.RequestCtx) {
	limit := 50
	if raw := string(ctx.QueryArgs().Peek("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			SendError(ctx, fasthttp.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}

	conversations, err := h.service.ListConversations(ctx, warpOwnerFor(ctx), limit)
	if errors.Is(err, warp.ErrUnavailable) {
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		logger.Warn("failed to list warp conversations: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to list conversations")
		return
	}
	SendJSON(ctx, map[string]any{"conversations": conversations})
}

// getConversation returns one thread with its transcript.
func (h *WarpHandler) getConversation(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	if id == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "conversation id is required")
		return
	}

	detail, err := h.service.GetConversation(ctx, warpOwnerFor(ctx), id)
	switch {
	case errors.Is(err, warp.ErrUnavailable):
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, configstore.ErrWarpConversationNotFound):
		// 404 for someone else's thread as well as a missing one. Distinguishing
		// them would confirm that another person's conversation exists.
		SendError(ctx, fasthttp.StatusNotFound, "Conversation not found")
		return
	case err != nil:
		logger.Warn("failed to read warp conversation: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to read conversation")
		return
	}
	SendJSON(ctx, detail)
}

// deleteConversation removes a thread.
func (h *WarpHandler) deleteConversation(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	if id == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "conversation id is required")
		return
	}

	err := h.service.DeleteConversation(ctx, warpOwnerFor(ctx), id)
	switch {
	case errors.Is(err, warp.ErrUnavailable):
		SendError(ctx, fasthttp.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, configstore.ErrWarpConversationNotFound):
		SendError(ctx, fasthttp.StatusNotFound, "Conversation not found")
		return
	case err != nil:
		logger.Warn("failed to delete warp conversation: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete conversation")
		return
	}
	ctx.SetStatusCode(fasthttp.StatusNoContent)
}
