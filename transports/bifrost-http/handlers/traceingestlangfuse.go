package handlers

import (
	"bytes"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/traceingest"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// langfusePrefix is what a user sets LANGFUSE_BASE_URL (or LANGFUSE_HOST) to, after the host.
const langfusePrefix = tracingPrefix + "/langfuse"

func init() {
	traceIngestAdapters = append(traceIngestAdapters, func(h *TraceIngestHandler, rt *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
		chain := func(handler fasthttp.RequestHandler) fasthttp.RequestHandler {
			return lib.ChainMiddlewares(handler, middlewares...)
		}
		api := langfusePrefix + "/api/public"
		// Current SDKs (Python v3+, JS v4+) export OTLP.
		rt.POST(api+"/otel/v1/traces", chain(func(ctx *fasthttp.RequestCtx) { h.receiveOTLP(ctx, logstore.TraceSourceLangfuse, false) }))
		// Older SDKs and the low-level clients post event batches.
		rt.POST(api+"/ingestion", chain(h.langfuseIngestion))
		rt.POST(api+"/scores", chain(h.langfuseCreateScore))
		rt.POST(api+"/media", chain(h.langfuseCreateMedia))
		rt.PATCH(api+"/media/{id}", chain(h.langfusePatchMedia))
		rt.GET(api+"/media/{id}", chain(h.langfuseGetMedia))
		rt.GET(api+"/projects", chain(h.langfuseProjects))
		rt.GET(api+"/health", chain(h.langfuseHealth))
		rt.DELETE(api+"/traces/{id}", chain(h.langfuseDeleteTrace))
		// get_trace_url() builds {host}/project/{id}/traces/{trace_id}: send it to the Bifrost trace.
		rt.GET(langfusePrefix+"/project/{project}/traces/{trace}", chain(h.langfuseTraceRedirect))
	})
}

// traceDashboardPath is the dashboard URL path that opens one trace.
func traceDashboardPath(projectID, traceID string) string {
	return "/workspace/tracing?project=" + url.QueryEscape(projectID) + "&trace=" + url.QueryEscape(traceID)
}

// langfuseError writes an error in the shape Langfuse SDKs parse.
func langfuseError(ctx *fasthttp.RequestCtx, status int, message string) {
	ctx.SetStatusCode(status)
	ctx.SetContentType("application/json")
	body, _ := schemas.MarshalSorted(map[string]any{"message": message, "error": message})
	ctx.SetBody(body)
}

func writeJSONStatus(ctx *fasthttp.RequestCtx, status int, payload any) {
	body, err := schemas.MarshalSorted(payload)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to encode response")
		return
	}
	ctx.SetStatusCode(status)
	ctx.SetContentType("application/json")
	ctx.SetBody(body)
}

// langfusePrincipal authenticates a Langfuse request. The SDK sends Basic public:secret; the
// secret key is the tracing key and the public key is not checked.
func (h *TraceIngestHandler) langfusePrincipal(ctx *fasthttp.RequestCtx) (*traceingest.Principal, bool) {
	return h.authenticate(ctx)
}

// langfuseIngestion handles a legacy event batch. The answer is always 207 with a result per
// event, so one bad event does not make the SDK resend the good ones.
func (h *TraceIngestHandler) langfuseIngestion(ctx *fasthttp.RequestCtx) {
	principal, ok := h.langfusePrincipal(ctx)
	if !ok {
		return
	}
	var request struct {
		Batch []*traceingest.LangfuseEvent `json:"batch"`
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &request); err != nil {
		langfuseError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	project, err := h.service.ResolveProject(storeCtx, principal, "")
	if err != nil {
		h.fail(ctx, err)
		return
	}
	type result struct {
		ID      string `json:"id"`
		Status  int    `json:"status"`
		Message string `json:"message,omitempty"`
	}
	successes, failures := []result{}, []result{}
	var parts []*traceingest.SpanPart
	var scores []*logstore.TraceScore
	var accepted []string
	for _, event := range request.Batch {
		if event == nil {
			continue
		}
		part, score, err := traceingest.MapLangfuseEvent(event)
		if err != nil {
			failures = append(failures, result{ID: event.ID, Status: fasthttp.StatusBadRequest, Message: err.Error()})
			continue
		}
		if part != nil {
			parts = append(parts, part)
		}
		if score != nil {
			scores = append(scores, score.TraceScore(logstore.TraceAuthorSDK))
		}
		accepted = append(accepted, event.ID)
	}
	err = h.service.Ingest(storeCtx, principal, project, logstore.TraceSourceLangfuse, parts)
	if err == nil {
		err = h.service.AddScores(storeCtx, principal, project.ID, scores)
	}
	if err != nil {
		if errors.Is(err, traceingest.ErrRateLimited) || errors.Is(err, traceingest.ErrForbidden) {
			h.fail(ctx, err)
			return
		}
		// The store failed: nothing of the batch is acknowledged, so the SDK sends it again.
		h.fail(ctx, err)
		return
	}
	h.forward(ctx, logstore.TraceSourceLangfuse, "/api/public/ingestion", project)
	for _, id := range accepted {
		successes = append(successes, result{ID: id, Status: fasthttp.StatusCreated})
	}
	writeJSONStatus(ctx, fasthttp.StatusMultiStatus, map[string]any{"successes": successes, "errors": failures})
}

// langfuseCreateScore handles POST /api/public/scores. Older SDKs post one score and get its id
// back. Langfuse 5 posts a JSON array of scores and expects 202 when all were taken, else 207 with
// the counts and a message per rejected score.
func (h *TraceIngestHandler) langfuseCreateScore(ctx *fasthttp.RequestCtx) {
	principal, ok := h.langfusePrincipal(ctx)
	if !ok {
		return
	}
	body := bytes.TrimSpace(ctx.PostBody())
	batch := len(body) > 0 && body[0] == '['
	var items []map[string]any
	if batch {
		if err := sonic.Unmarshal(body, &items); err != nil {
			langfuseError(ctx, fasthttp.StatusBadRequest, "invalid request body")
			return
		}
	} else {
		var item map[string]any
		if err := sonic.Unmarshal(body, &item); err != nil {
			langfuseError(ctx, fasthttp.StatusBadRequest, "invalid request body")
			return
		}
		items = []map[string]any{item}
	}
	var scores []*logstore.TraceScore
	var rejected []map[string]any
	for _, item := range items {
		_, score, err := traceingest.MapLangfuseEvent(&traceingest.LangfuseEvent{Type: "score-create", Body: item})
		if err != nil {
			if !batch {
				langfuseError(ctx, fasthttp.StatusBadRequest, err.Error())
				return
			}
			rejected = append(rejected, map[string]any{"message": err.Error()})
			continue
		}
		stored := score.TraceScore(logstore.TraceAuthorSDK)
		if stored.ID == "" {
			// A score sent with no id gets one now, so the answer can name it.
			stored.ID = uuid.NewString()
		}
		scores = append(scores, stored)
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	project, err := h.service.ResolveProject(storeCtx, principal, "")
	if err != nil {
		h.fail(ctx, err)
		return
	}
	if err := h.service.AddScores(storeCtx, principal, project.ID, scores); err != nil {
		h.fail(ctx, err)
		return
	}
	h.forward(ctx, logstore.TraceSourceLangfuse, "/api/public/scores", project)
	switch {
	case !batch:
		SendJSON(ctx, map[string]any{"id": scores[0].ID})
	case len(rejected) == 0:
		writeJSONStatus(ctx, fasthttp.StatusAccepted, map[string]any{"message": "Accepted"})
	default:
		writeJSONStatus(ctx, fasthttp.StatusMultiStatus, map[string]any{"accepted": len(scores), "rejected": len(rejected), "errors": rejected})
	}
}

// langfuseCreateMedia hands the SDK an upload URL for a media file. The media id is derived from
// the file's hash, so a file the project already holds is not uploaded again: the SDK is told so
// with a null uploadUrl.
func (h *TraceIngestHandler) langfuseCreateMedia(ctx *fasthttp.RequestCtx) {
	principal, ok := h.langfusePrincipal(ctx)
	if !ok {
		return
	}
	var body struct {
		ContentType string `json:"contentType"`
		SHA256Hash  string `json:"sha256Hash"`
		Field       string `json:"field"`
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil || body.SHA256Hash == "" {
		langfuseError(ctx, fasthttp.StatusBadRequest, "sha256Hash is required")
		return
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	project, err := h.service.ResolveProject(storeCtx, principal, "")
	if err != nil {
		h.fail(ctx, err)
		return
	}
	mediaID := "lf" + strings.NewReplacer("+", "", "/", "", "=", "").Replace(body.SHA256Hash)
	if len(mediaID) > 40 {
		mediaID = mediaID[:40]
	}
	mediaID += "-" + project.ID[:min(len(project.ID), 8)]
	store := h.service.Store()
	if existing, err := store.FindTraceUpload(storeCtx, mediaID); err == nil {
		if existing.ProjectID != project.ID {
			h.fail(ctx, traceingest.ErrForbidden)
			return
		}
		if existing.Status == logstore.TraceUploadStatusDone || time.Since(existing.CreatedAt) < traceUploadWindow {
			var uploadURL any
			if existing.Status != logstore.TraceUploadStatusDone {
				uploadURL = h.publicBaseURL(ctx) + tracingPrefix + "/uploads/" + mediaID
			}
			writeJSONStatus(ctx, fasthttp.StatusCreated, map[string]any{"mediaId": mediaID, "uploadUrl": uploadURL})
			return
		}
		// A stale pending row from an upload that never came: a fresh id reopens the window.
		mediaID += "-" + time.Now().UTC().Format("150405")
	}
	_, uploadURL, err := h.newUpload(ctx, project, logstore.TraceSourceLangfuse, mediaID, body.ContentType, body.Field)
	if err != nil {
		h.fail(ctx, err)
		return
	}
	writeJSONStatus(ctx, fasthttp.StatusCreated, map[string]any{"mediaId": mediaID, "uploadUrl": uploadURL})
}

// langfusePatchMedia is the SDK's report of how its upload went. The bytes already told Bifrost,
// so the report is acknowledged and not stored.
func (h *TraceIngestHandler) langfusePatchMedia(ctx *fasthttp.RequestCtx) {
	if _, ok := h.langfusePrincipal(ctx); !ok {
		return
	}
	ctx.SetStatusCode(fasthttp.StatusNoContent)
}

func (h *TraceIngestHandler) langfuseGetMedia(ctx *fasthttp.RequestCtx) {
	principal, ok := h.langfusePrincipal(ctx)
	if !ok {
		return
	}
	id, _ := ctx.UserValue("id").(string)
	storeCtx, cancel := ingestContext()
	defer cancel()
	upload, err := h.service.Store().FindTraceUpload(storeCtx, id)
	if err != nil || !principalAllowsProject(principal, upload.ProjectID) {
		langfuseError(ctx, fasthttp.StatusNotFound, "media not found")
		return
	}
	SendJSON(ctx, map[string]any{
		"mediaId": upload.ID, "contentType": upload.ContentType, "contentLength": upload.SizeBytes,
		"uploadedAt": upload.CreatedAt.UTC().Format(time.RFC3339Nano), "url": h.publicBaseURL(ctx) + "/api/traces/uploads/" + upload.ID,
		"urlExpiry": time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
	})
}

func principalAllowsProject(principal *traceingest.Principal, projectID string) bool {
	if principal.AllProjects {
		return true
	}
	for _, id := range principal.ProjectIDs {
		if id == projectID {
			return true
		}
	}
	return false
}

// langfuseProjects answers the SDK's auth check and gives get_trace_url() its project id.
func (h *TraceIngestHandler) langfuseProjects(ctx *fasthttp.RequestCtx) {
	principal, ok := h.langfusePrincipal(ctx)
	if !ok {
		return
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	project, err := h.service.ResolveProject(storeCtx, principal, "")
	if err != nil {
		h.fail(ctx, err)
		return
	}
	entry := map[string]any{"id": project.ID, "name": project.Name, "metadata": map[string]any{}}
	if project.RetentionDays != nil {
		entry["retentionDays"] = *project.RetentionDays
	}
	SendJSON(ctx, map[string]any{"data": []any{entry}})
}

func (h *TraceIngestHandler) langfuseHealth(ctx *fasthttp.RequestCtx) {
	SendJSON(ctx, map[string]any{"status": "OK", "version": "3.0.0"})
}

func (h *TraceIngestHandler) langfuseDeleteTrace(ctx *fasthttp.RequestCtx) {
	principal, ok := h.langfusePrincipal(ctx)
	if !ok {
		return
	}
	id, _ := ctx.UserValue("id").(string)
	storeCtx, cancel := ingestContext()
	defer cancel()
	project, err := h.service.ResolveProject(storeCtx, principal, "")
	if err != nil {
		h.fail(ctx, err)
		return
	}
	if err := h.service.Store().DeleteTraces(storeCtx, []logstore.TraceKey{{ProjectID: project.ID, TraceID: id}}); err != nil {
		h.fail(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"message": "Trace deleted successfully"})
}

// langfuseTraceRedirect sends the URL the SDK prints for a trace to that trace in Bifrost. It needs
// no key: the dashboard it lands on does its own sign-in.
func (h *TraceIngestHandler) langfuseTraceRedirect(ctx *fasthttp.RequestCtx) {
	project, _ := ctx.UserValue("project").(string)
	trace, _ := ctx.UserValue("trace").(string)
	ctx.Redirect(traceDashboardPath(project, trace), fasthttp.StatusFound)
}
