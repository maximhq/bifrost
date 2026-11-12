package handlers

import (
	"math"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

func init() {
	tracesHandlerExtensions = append(tracesHandlerExtensions, func(h *TracesHandler, r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
		r.POST("/api/traces/trace/{project_id}/{trace_id}/scores", lib.ChainMiddlewares(h.addScore, middlewares...))
		r.DELETE("/api/traces/trace/{project_id}/{trace_id}/scores/{id}", lib.ChainMiddlewares(h.deleteScore, middlewares...))
		r.POST("/api/traces/trace/{project_id}/{trace_id}/comments", lib.ChainMiddlewares(h.addComment, middlewares...))
		r.DELETE("/api/traces/trace/{project_id}/{trace_id}/comments/{id}", lib.ChainMiddlewares(h.deleteComment, middlewares...))
	})
}

// reviewAuthor is who is signed in to the dashboard, when the auth layer says. It is never taken
// from the request body: a reviewer cannot sign a score with someone else's name.
func reviewAuthor(ctx *fasthttp.RequestCtx) string {
	author, _ := ctx.UserValue(schemas.BifrostContextKeyUserEmail).(string)
	return author
}

// reviewTarget reads the trace a review route names and checks it exists, along with the span when
// the review is on one.
func (h *TracesHandler) reviewTarget(ctx *fasthttp.RequestCtx, spanID string) (projectID, traceID string, ok bool) {
	projectID, _ = ctx.UserValue("project_id").(string)
	traceID, _ = ctx.UserValue("trace_id").(string)
	storeCtx, cancel := h.storeContext()
	defer cancel()
	spans, err := h.store.ListTraceSpans(storeCtx, projectID, traceID)
	if err != nil {
		h.fail(ctx, "load trace", err)
		return "", "", false
	}
	if len(spans) == 0 {
		SendError(ctx, fasthttp.StatusNotFound, "trace not found")
		return "", "", false
	}
	if spanID != "" {
		found := false
		for _, span := range spans {
			found = found || span.SpanID == spanID
		}
		if !found {
			SendError(ctx, fasthttp.StatusBadRequest, "span_id is not a span of this trace")
			return "", "", false
		}
	}
	return projectID, traceID, true
}

// rollup rebuilds the trace's rollup now, so the list shows a reviewer's score at once.
func (h *TracesHandler) rollup(projectID, traceID string) {
	storeCtx, cancel := h.storeContext()
	defer cancel()
	if _, err := h.store.RecomputeTraceRollup(storeCtx, projectID, traceID); err != nil && h.logger != nil {
		h.logger.Warn("failed to rebuild trace rollup after a review: %v", err)
	}
}

// addScore records a reviewer's score on a trace or on one of its spans. Sending an id replaces
// that score, which is how a reviewer edits one.
func (h *TracesHandler) addScore(ctx *fasthttp.RequestCtx) {
	var payload struct {
		ID          string   `json:"id"`
		SpanID      string   `json:"span_id"`
		Name        string   `json:"name"`
		Value       *float64 `json:"value"`
		StringValue string   `json:"string_value"`
		Comment     string   `json:"comment"`
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	payload.Name = strings.TrimSpace(payload.Name)
	switch {
	case payload.Name == "" || len(payload.Name) > 255:
		SendError(ctx, fasthttp.StatusBadRequest, "name is required and must be at most 255 characters")
		return
	case payload.Value == nil && strings.TrimSpace(payload.StringValue) == "":
		SendError(ctx, fasthttp.StatusBadRequest, "a score needs a value or a string_value")
		return
	case payload.Value != nil && (math.IsNaN(*payload.Value) || math.IsInf(*payload.Value, 0)):
		SendError(ctx, fasthttp.StatusBadRequest, "value must be a finite number")
		return
	}
	projectID, traceID, ok := h.reviewTarget(ctx, payload.SpanID)
	if !ok {
		return
	}
	storeCtx, cancel := h.storeContext()
	defer cancel()
	score := &logstore.TraceScore{
		ID: payload.ID, ProjectID: projectID, TraceID: traceID, SpanID: payload.SpanID, Name: payload.Name, Value: payload.Value,
		StringValue: strings.TrimSpace(payload.StringValue), Comment: payload.Comment, Source: logstore.TraceAuthorHuman, Author: reviewAuthor(ctx), DataType: "numeric",
	}
	if score.Value == nil {
		score.DataType = "categorical"
	}
	if score.ID == "" {
		score.ID = uuid.NewString()
	} else {
		// An edit may only replace a reviewer's score of this same trace.
		existing, err := h.store.ListTraceScores(storeCtx, projectID, traceID)
		if err != nil {
			h.fail(ctx, "save score", err)
			return
		}
		editable := false
		for _, candidate := range existing {
			editable = editable || (candidate.ID == score.ID && candidate.Source == logstore.TraceAuthorHuman)
		}
		if !editable {
			SendError(ctx, fasthttp.StatusNotFound, "score not found")
			return
		}
	}
	if err := h.store.UpsertTraceScores(storeCtx, []*logstore.TraceScore{score}); err != nil {
		h.fail(ctx, "save score", err)
		return
	}
	h.rollup(projectID, traceID)
	SendJSON(ctx, map[string]any{"score": score})
}

func (h *TracesHandler) deleteScore(ctx *fasthttp.RequestCtx) {
	projectID, traceID, ok := h.reviewTarget(ctx, "")
	if !ok {
		return
	}
	id, _ := ctx.UserValue("id").(string)
	storeCtx, cancel := h.storeContext()
	defer cancel()
	existing, err := h.store.ListTraceScores(storeCtx, projectID, traceID)
	if err != nil {
		h.fail(ctx, "delete score", err)
		return
	}
	for _, score := range existing {
		if score.ID != id {
			continue
		}
		if err := h.store.DeleteTraceScore(storeCtx, projectID, id); err != nil {
			h.fail(ctx, "delete score", err)
			return
		}
		h.rollup(projectID, traceID)
		SendJSON(ctx, map[string]any{"deleted": true})
		return
	}
	SendError(ctx, fasthttp.StatusNotFound, "score not found")
}

// addComment records a reviewer's note on a trace or on one of its spans.
func (h *TracesHandler) addComment(ctx *fasthttp.RequestCtx) {
	var payload struct {
		SpanID string `json:"span_id"`
		Text   string `json:"text"`
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	payload.Text = strings.TrimSpace(payload.Text)
	if payload.Text == "" || len(payload.Text) > 10000 {
		SendError(ctx, fasthttp.StatusBadRequest, "text is required and must be at most 10000 characters")
		return
	}
	projectID, traceID, ok := h.reviewTarget(ctx, payload.SpanID)
	if !ok {
		return
	}
	comment := &logstore.TraceComment{ID: uuid.NewString(), ProjectID: projectID, TraceID: traceID, SpanID: payload.SpanID, Text: payload.Text, Source: logstore.TraceAuthorHuman, Author: reviewAuthor(ctx)}
	storeCtx, cancel := h.storeContext()
	defer cancel()
	if err := h.store.CreateTraceComment(storeCtx, comment); err != nil {
		h.fail(ctx, "save comment", err)
		return
	}
	SendJSON(ctx, map[string]any{"comment": comment})
}

func (h *TracesHandler) deleteComment(ctx *fasthttp.RequestCtx) {
	projectID, traceID, ok := h.reviewTarget(ctx, "")
	if !ok {
		return
	}
	id, _ := ctx.UserValue("id").(string)
	storeCtx, cancel := h.storeContext()
	defer cancel()
	existing, err := h.store.ListTraceComments(storeCtx, projectID, traceID)
	if err != nil {
		h.fail(ctx, "delete comment", err)
		return
	}
	for _, comment := range existing {
		if comment.ID != id {
			continue
		}
		if err := h.store.DeleteTraceComment(storeCtx, projectID, id); err != nil {
			h.fail(ctx, "delete comment", err)
			return
		}
		SendJSON(ctx, map[string]any{"deleted": true})
		return
	}
	SendError(ctx, fasthttp.StatusNotFound, "comment not found")
}
