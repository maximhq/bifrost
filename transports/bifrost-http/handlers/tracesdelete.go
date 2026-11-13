package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

func init() {
	tracesHandlerExtensions = append(tracesHandlerExtensions, func(h *TracesHandler, r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
		r.DELETE("/api/traces/trace/{project_id}/{trace_id}", lib.ChainMiddlewares(h.deleteTrace, middlewares...))
	})
}

// deleteTrace removes one trace: its parts, spans, links, scores, comments and rollup.
func (h *TracesHandler) deleteTrace(ctx *fasthttp.RequestCtx) {
	projectID, _ := ctx.UserValue("project_id").(string)
	traceID, _ := ctx.UserValue("trace_id").(string)
	storeCtx, cancel := h.storeContext()
	defer cancel()
	spans, err := h.store.ListTraceSpans(storeCtx, projectID, traceID)
	if err != nil {
		h.fail(ctx, "delete trace", err)
		return
	}
	if len(spans) == 0 {
		SendError(ctx, fasthttp.StatusNotFound, "trace not found")
		return
	}
	if err := h.store.DeleteTraces(storeCtx, []logstore.TraceKey{{ProjectID: projectID, TraceID: traceID}}); err != nil {
		h.fail(ctx, "delete trace", err)
		return
	}
	SendJSON(ctx, map[string]any{"deleted": true})
}
