package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// snapshotWarpContext lifts everything the agent needs out of the RequestCtx and
// into a plain context.Context.
//
// This is not a tidiness measure. fasthttp recycles *RequestCtx once the handler
// returns, and the agent goroutine outlives it - so reading the scope later
// reads freed memory or nothing at all. And "nothing at all" is the dangerous
// outcome: queryscope.FromContext treats a missing scope as no restriction, so a
// dropped scope silently returns every row in the deployment to whoever asked,
// with no error and no log line.
//
// If you add anything to this function, add it here rather than reading ctx
// inside the goroutine.
func snapshotWarpContext(ctx *fasthttp.RequestCtx, timeout time.Duration) (context.Context, context.CancelFunc) {
	base := context.Background()

	if scope, ok := ctx.UserValue(schemas.BifrostContextKeyQueryScope).(queryscope.QueryScope); ok && scope != nil {
		base = queryscope.WithQueryScope(base, scope)
	}
	for _, key := range []any{
		schemas.IsLocalAdminContextKey,
		schemas.BifrostContextKeyUserID,
		schemas.BifrostContextKeyUserRoleID,
	} {
		if value := ctx.UserValue(key); value != nil {
			base = context.WithValue(base, key, value)
		}
	}
	return context.WithTimeout(base, timeout)
}

// chat is the agent endpoint. It is registered only when the service reports
// CanChat, so reaching it always means Warp has something to read.
func (h *WarpHandler) chat(ctx *fasthttp.RequestCtx) {
	var request warp.ChatRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &request); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}

	turn, err := h.service.NewTurn(ctx, &request, len(ctx.PostBody()))
	switch {
	case errors.Is(err, warp.ErrUnavailable):
		h.sendUnavailable(ctx, schemas.WarpUnavailableNotConfigured,
			"Warp is not configured. Set a provider and model in Settings to enable it.")
		return
	case errors.Is(err, warp.ErrNoModelClient):
		h.sendUnavailable(ctx, schemas.WarpUnavailableNotConfigured, "Warp has no model client available.")
		return
	case errors.Is(err, warp.ErrConversationTooLong):
		SendError(ctx, fasthttp.StatusRequestEntityTooLarge, "Conversation is too long. Start a new chat.")
		return
	case errors.Is(err, warp.ErrEmptyConversation), errors.Is(err, warp.ErrBadRole):
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	case err != nil:
		logger.Warn("failed to prepare warp turn: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to read warp configuration")
		return
	}

	agentCtx, cancel := snapshotWarpContext(ctx, turn.Budget)
	if request.Stream != nil && !*request.Stream {
		defer cancel()
		SendJSON(ctx, h.service.RunTurn(agentCtx, turn, nil))
		return
	}
	h.chatStreaming(ctx, agentCtx, cancel, turn)
}

// chatStreaming formats each event as an SSE frame.
func (h *WarpHandler) chatStreaming(ctx *fasthttp.RequestCtx, agentCtx context.Context, cancel context.CancelFunc, turn *warp.Turn) {
	ctx.Response.Header.Set("Content-Type", "text/event-stream")
	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.Response.Header.Set("Connection", "keep-alive")

	reader := lib.NewSSEStreamReader()
	ctx.Response.SetBodyStream(reader, -1)

	go func() {
		defer cancel()
		// A heartbeat forces periodic writes during otherwise-idle gaps. Client
		// disconnects are only detected when a write fails, and a slow upstream
		// turn can leave minutes with no frames to send - long enough for a closed
		// tab to go unnoticed while the request keeps spending tokens.
		heartbeatDone, heartbeatExited := lib.StartSSEHeartbeat(lib.DefaultSSEHeartbeatInterval, reader.SendHeartbeat, cancel)
		defer func() {
			// Must run before reader.Done(): closing the event channel while the
			// heartbeat goroutine could still be mid-send panics.
			lib.StopSSEHeartbeat(reader, heartbeatDone, heartbeatExited)
			reader.Done()
		}()

		h.service.RunTurn(agentCtx, turn, func(event warp.Event) bool {
			payload, err := sonic.Marshal(event)
			if err != nil {
				return true
			}
			// A failed write means the client is gone. Returning false stops the
			// loop and, importantly, stops paying the provider for an answer nobody
			// will read.
			return reader.SendEvent(string(event.Type), payload)
		})
	}()
}

// sendUnavailable answers 503 with a machine-readable reason. The dashboard
// branches on it: an unconfigured Warp stays visible with a link to settings,
// while a deployment with no log store hides the launcher entirely.
func (h *WarpHandler) sendUnavailable(ctx *fasthttp.RequestCtx, reason schemas.WarpUnavailableReason, message string) {
	SendJSONWithStatus(ctx, schemas.WarpUnavailableResponse{Reason: reason, Message: message}, fasthttp.StatusServiceUnavailable)
}
