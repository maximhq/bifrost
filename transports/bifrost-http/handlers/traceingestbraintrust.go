package handlers

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/traceingest"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const (
	// braintrustPrefix is what a user sets BRAINTRUST_APP_URL to, after the host. The login answer
	// names the same URL as the API URL, so every later SDK call lands here too.
	braintrustPrefix = tracingPrefix + "/braintrust"
	// braintrustOrg is the one organisation Bifrost reports. The SDK only needs it to be stable.
	braintrustOrg = "bifrost"
	// braintrustMaxPayloadBytes is the logs3 body size above which the SDK uploads the payload
	// through the overflow route instead of posting it inline.
	braintrustMaxPayloadBytes = 6 << 20
)

func init() {
	traceIngestAdapters = append(traceIngestAdapters, func(h *TraceIngestHandler, rt *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
		chain := func(handler fasthttp.RequestHandler) fasthttp.RequestHandler {
			return lib.ChainMiddlewares(handler, middlewares...)
		}
		p := braintrustPrefix
		rt.POST(p+"/api/apikey/login", chain(h.braintrustLogin))
		// Both register generations: the /api routes of older SDKs and the /v1 REST routes.
		rt.POST(p+"/api/project/register", chain(h.braintrustRegisterProject))
		rt.GET(p+"/api/project", chain(h.braintrustListProjects))
		rt.POST(p+"/api/experiment/register", chain(h.braintrustRegisterExperiment))
		rt.POST(p+"/v1/project", chain(h.braintrustCreateProject))
		rt.GET(p+"/v1/project/{id}", chain(h.braintrustGetProject))
		rt.POST(p+"/v1/experiment", chain(h.braintrustCreateExperiment))
		rt.GET(p+"/version", chain(h.braintrustVersion))
		rt.GET(p+"/ping", chain(func(ctx *fasthttp.RequestCtx) { SendJSON(ctx, map[string]any{}) }))
		rt.POST(p+"/logs3", chain(h.braintrustLogs3))
		rt.POST(p+"/logs3/overflow", chain(h.braintrustOverflow))
		rt.POST(p+"/attachment", chain(h.braintrustAttachment))
		rt.POST(p+"/attachment/status", chain(h.braintrustAttachmentStatus))
		rt.POST(p+"/otel/v1/traces", chain(h.braintrustOTLP))
		// The URLs span.link() and the eval summary print.
		rt.GET(p+"/app/{org}/object", chain(h.braintrustObjectRedirect))
		rt.GET(p+"/app/{org}/p/{project}", chain(h.braintrustProjectRedirect))
		rt.GET(p+"/app/{org}/p/{project}/{rest:*}", chain(h.braintrustProjectRedirect))
		// What Eval() calls when it finishes. Each answers with an empty, valid summary, so an eval
		// over local data ends cleanly.
		rt.GET(p+"/v1/experiment/{id}/summarize", chain(h.braintrustSummarize))
		rt.POST(p+"/api/base_experiment/get_id", chain(h.braintrustNoBaseExperiment))
		rt.GET(p+"/experiment-comparison2", chain(h.braintrustComparison))
		rt.POST(p+"/btql", chain(h.braintrustBTQL))
	})
}

func (h *TraceIngestHandler) braintrustPrincipal(ctx *fasthttp.RequestCtx) (*traceingest.Principal, bool) {
	return h.authenticate(ctx, bearerCredential(string(ctx.Request.Header.Peek("Authorization"))))
}

// braintrustLogin answers the SDK's login. org_info.api_url is where the SDK sends everything
// after this, so it points back at this adapter.
func (h *TraceIngestHandler) braintrustLogin(ctx *fasthttp.RequestCtx) {
	if _, ok := h.braintrustPrincipal(ctx); !ok {
		return
	}
	api := h.publicBaseURL(ctx) + braintrustPrefix
	SendJSON(ctx, map[string]any{"org_info": []any{map[string]any{
		"id": braintrustOrg, "name": braintrustOrg, "api_url": api, "proxy_url": api, "realtime_url": api,
		"git_metadata": nil, "is_universal_api": true,
	}}})
}

func braintrustProjectJSON(project *tables.TableTraceProject) map[string]any {
	return map[string]any{"id": project.ID, "name": project.Name, "org_id": braintrustOrg, "created": project.CreatedAt.UTC().Format(time.RFC3339Nano)}
}

// braintrustBody decodes a JSON body. The SDK does not always send a content type.
func braintrustBody(ctx *fasthttp.RequestCtx) (map[string]any, bool) {
	body := map[string]any{}
	if len(ctx.PostBody()) == 0 {
		return body, true
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return nil, false
	}
	return body, true
}

func stringField(body map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := body[name].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// braintrustProject resolves the project a register call names, by id or by name. A name that does
// not exist is created when the key may create projects.
func (h *TraceIngestHandler) braintrustProject(ctx *fasthttp.RequestCtx, body map[string]any) (*tables.TableTraceProject, *traceingest.Principal, bool) {
	principal, ok := h.braintrustPrincipal(ctx)
	if !ok {
		return nil, nil, false
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	project, err := h.service.ResolveProject(storeCtx, principal, stringField(body, "project_id", "project_name", "name"))
	if err != nil {
		h.fail(ctx, err)
		return nil, nil, false
	}
	return project, principal, true
}

func (h *TraceIngestHandler) braintrustRegisterProject(ctx *fasthttp.RequestCtx) {
	body, ok := braintrustBody(ctx)
	if !ok {
		return
	}
	if project, _, ok := h.braintrustProject(ctx, body); ok {
		SendJSON(ctx, map[string]any{"project": braintrustProjectJSON(project)})
	}
}

func (h *TraceIngestHandler) braintrustCreateProject(ctx *fasthttp.RequestCtx) {
	body, ok := braintrustBody(ctx)
	if !ok {
		return
	}
	if project, _, ok := h.braintrustProject(ctx, body); ok {
		SendJSON(ctx, braintrustProjectJSON(project))
	}
}

func (h *TraceIngestHandler) braintrustGetProject(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("id").(string)
	if project, _, ok := h.braintrustProject(ctx, map[string]any{"project_id": id}); ok {
		SendJSON(ctx, braintrustProjectJSON(project))
	}
}

// braintrustListProjects answers GET /api/project, filtered by the id or the name in the query.
func (h *TraceIngestHandler) braintrustListProjects(ctx *fasthttp.RequestCtx) {
	args := ctx.QueryArgs()
	ref := string(args.Peek("id"))
	if ref == "" {
		ref = string(args.Peek("project_name"))
	}
	if ref == "" {
		ref = string(args.Peek("name"))
	}
	project, _, ok := h.braintrustProject(ctx, map[string]any{"project_id": ref})
	if !ok {
		return
	}
	SendJSON(ctx, []any{braintrustProjectJSON(project)})
}

// braintrustExperiment issues the experiment a register call names. An experiment is an id that
// carries its project: its rows are spans of that project, tagged with the experiment.
func (h *TraceIngestHandler) braintrustExperiment(ctx *fasthttp.RequestCtx) (map[string]any, *tables.TableTraceProject, bool) {
	body, ok := braintrustBody(ctx)
	if !ok {
		return nil, nil, false
	}
	project, _, ok := h.braintrustProject(ctx, body)
	if !ok {
		return nil, nil, false
	}
	name := stringField(body, "experiment_name", "name")
	if name == "" {
		name = "experiment-" + time.Now().UTC().Format("20060102-150405")
	}
	return map[string]any{
		"id": traceingest.BraintrustObjectID("exp", project.ID, name), "name": name, "project_id": project.ID,
		"created": time.Now().UTC().Format(time.RFC3339Nano), "public": false,
	}, project, true
}

func (h *TraceIngestHandler) braintrustRegisterExperiment(ctx *fasthttp.RequestCtx) {
	if experiment, project, ok := h.braintrustExperiment(ctx); ok {
		SendJSON(ctx, map[string]any{"project": braintrustProjectJSON(project), "experiment": experiment})
	}
}

func (h *TraceIngestHandler) braintrustCreateExperiment(ctx *fasthttp.RequestCtx) {
	if experiment, _, ok := h.braintrustExperiment(ctx); ok {
		SendJSON(ctx, experiment)
	}
}

// braintrustVersion tells the SDK the largest logs3 body it may post inline. The limit is left out
// when the store has nowhere to keep an overflow upload, which makes the SDK post inline always.
func (h *TraceIngestHandler) braintrustVersion(ctx *fasthttp.RequestCtx) {
	version := map[string]any{"version": "bifrost"}
	if h.service.Store().TraceUploadsAvailable() {
		version["logs3_payload_max_bytes"] = braintrustMaxPayloadBytes
	}
	SendJSON(ctx, version)
}

// braintrustLogs3 handles the SDK's log flush: {"rows": [...], "api_version": 2}. A payload too
// large to post inline arrives as a reference to an overflow upload instead of a row list.
func (h *TraceIngestHandler) braintrustLogs3(ctx *fasthttp.RequestCtx) {
	principal, ok := h.braintrustPrincipal(ctx)
	if !ok {
		return
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	var request struct {
		Rows any `json:"rows"`
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &request); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	reference, isOverflow := request.Rows.(map[string]any)
	if isOverflow {
		key := stringField(reference, "key")
		upload, data, err := h.service.Store().ReadTraceUploadBytes(storeCtx, key)
		if err != nil || !principalAllowsProject(principal, upload.ProjectID) || upload.Status != logstore.TraceUploadStatusDone {
			SendError(ctx, fasthttp.StatusBadRequest, "overflow payload not found")
			return
		}
		request.Rows = nil
		if err := sonic.Unmarshal(data, &request); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "overflow payload is not a logs3 body")
			return
		}
	}
	rows, _ := request.Rows.([]any)
	rowIDs, projects, err := h.ingestBraintrustRows(principal, rows)
	if err != nil {
		h.fail(ctx, err)
		return
	}
	if !isOverflow {
		// An overflow flush is a reference to an upload, not the rows: there is nothing to replay.
		h.forward(ctx, logstore.TraceSourceBraintrust, "/logs3", projects...)
	}
	SendJSON(ctx, map[string]any{"row_ids": rowIDs})
}

// ingestBraintrustRows stores the rows of one flush. A row that cannot be mapped is skipped, never
// a reason to refuse the flush: the SDK would resend every row of it.
func (h *TraceIngestHandler) ingestBraintrustRows(principal *traceingest.Principal, rows []any) ([]string, []*tables.TableTraceProject, error) {
	storeCtx, cancel := ingestContext()
	defer cancel()
	rowIDs := make([]string, 0, len(rows))
	byProject := make(map[string][]*traceingest.BraintrustRow)
	for _, raw := range rows {
		row, isObject := raw.(map[string]any)
		if !isObject {
			continue
		}
		mapped, err := traceingest.MapBraintrustRow(row)
		if err != nil || mapped.Deleted {
			continue
		}
		rowIDs = append(rowIDs, stringField(row, "id"))
		projectRef := mapped.ProjectID
		if projectRef == "" {
			if fromExperiment, ok := traceingest.BraintrustObjectProject("exp", mapped.ExperimentID); ok {
				projectRef = fromExperiment
			}
		}
		if mapped.DatasetID != "" && mapped.ExperimentID == "" && mapped.ProjectID == "" {
			continue // a dataset record, not a span
		}
		byProject[projectRef] = append(byProject[projectRef], mapped)
	}
	projects := make([]*tables.TableTraceProject, 0, len(byProject))
	for projectRef, mappedRows := range byProject {
		project, err := h.service.ResolveProject(storeCtx, principal, projectRef)
		if err != nil {
			return nil, nil, err
		}
		// Feedback and comments name their span by row id: find the span each one means.
		var targets []string
		for _, row := range mappedRows {
			if row.TargetRowID != "" {
				targets = append(targets, row.TargetRowID)
			}
		}
		byRowID := make(map[string]*logstore.TraceSpan)
		// A span sent earlier in this same flush is found in the batch itself.
		for _, row := range mappedRows {
			if row.TargetRowID == "" && row.Part != nil && row.Part.RowID != "" {
				byRowID[row.Part.RowID] = &logstore.TraceSpan{TraceID: row.Part.TraceID, SpanID: row.Part.SpanID}
			}
		}
		if len(targets) > 0 {
			found, err := h.service.Store().FindTraceSpansByRowIDs(storeCtx, project.ID, targets)
			if err != nil {
				return nil, nil, err
			}
			for _, span := range found {
				byRowID[span.RowID] = span
			}
		}
		var parts []*traceingest.SpanPart
		var comments []*logstore.TraceComment
		for _, row := range mappedRows {
			if row.TargetRowID != "" {
				target := byRowID[row.TargetRowID]
				if target == nil {
					continue // feedback on a row this project does not hold
				}
				if row.Comment != "" {
					comments = append(comments, &logstore.TraceComment{TraceID: target.TraceID, SpanID: target.SpanID, Text: row.Comment, Source: logstore.TraceAuthorSDK})
					continue
				}
				row.Part.TraceID, row.Part.SpanID = target.TraceID, target.SpanID
			}
			parts = append(parts, row.Part)
		}
		if err := h.service.Ingest(storeCtx, principal, project, logstore.TraceSourceBraintrust, parts); err != nil {
			return nil, nil, err
		}
		if err := h.service.AddComments(storeCtx, principal, project.ID, comments); err != nil {
			return nil, nil, err
		}
		projects = append(projects, project)
	}
	return rowIDs, projects, nil
}

// braintrustUploadTarget hands the SDK a URL to PUT a file to, in the shape of a presigned
// object-store URL: {signedUrl, headers}.
func (h *TraceIngestHandler) braintrustUploadTarget(ctx *fasthttp.RequestCtx, id, contentType, filename string) (map[string]any, bool) {
	principal, ok := h.braintrustPrincipal(ctx)
	if !ok {
		return nil, false
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	project, err := h.service.ResolveProject(storeCtx, principal, "")
	if errors.Is(err, traceingest.ErrForbidden) && len(principal.ProjectIDs) > 0 {
		// A key with several projects and no default: the upload is filed under its first.
		project, err = h.service.ResolveProject(storeCtx, principal, principal.ProjectIDs[0])
	}
	if err != nil {
		h.fail(ctx, err)
		return nil, false
	}
	upload, uploadURL, err := h.newUpload(ctx, project, logstore.TraceSourceBraintrust, id, contentType, filename)
	if err != nil {
		h.fail(ctx, err)
		return nil, false
	}
	return map[string]any{"method": "PUT", "signedUrl": uploadURL, "headers": map[string]any{}, "key": upload.ID}, true
}

func (h *TraceIngestHandler) braintrustOverflow(ctx *fasthttp.RequestCtx) {
	body, ok := braintrustBody(ctx)
	if !ok {
		return
	}
	if target, ok := h.braintrustUploadTarget(ctx, "", stringField(body, "content_type"), "logs3-overflow"); ok {
		SendJSON(ctx, target)
	}
}

// braintrustAttachmentID is the upload id of an attachment. The SDK picks the attachment's key and
// refers to it from span data, so the id is derived from the key and the viewer can find the file
// from the span. Knowing the key is not enough to overwrite the file: an upload URL takes one
// write, inside a short window, and then closes.
func braintrustAttachmentID(key string) string {
	return "bt-" + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, key)
}

func (h *TraceIngestHandler) braintrustAttachment(ctx *fasthttp.RequestCtx) {
	body, ok := braintrustBody(ctx)
	if !ok {
		return
	}
	key := stringField(body, "key")
	if key == "" || len(key) > 100 {
		SendError(ctx, fasthttp.StatusBadRequest, "key is required")
		return
	}
	if target, ok := h.braintrustUploadTarget(ctx, braintrustAttachmentID(key), stringField(body, "content_type"), stringField(body, "filename")); ok {
		SendJSON(ctx, map[string]any{"signedUrl": target["signedUrl"], "headers": target["headers"]})
	}
}

// braintrustAttachmentStatus is the SDK's report of how its upload went. The bytes already told
// Bifrost, so the report is acknowledged and not stored.
func (h *TraceIngestHandler) braintrustAttachmentStatus(ctx *fasthttp.RequestCtx) {
	if _, ok := h.braintrustPrincipal(ctx); ok {
		SendJSON(ctx, map[string]any{})
	}
}

// braintrustOTLP handles Braintrust's OTLP endpoint. The project comes from x-bt-parent, which is
// "project_id:<id>", "project_name:<name>", "experiment_id:<id>" or an exported span.
func (h *TraceIngestHandler) braintrustOTLP(ctx *fasthttp.RequestCtx) {
	principal, ok := h.braintrustPrincipal(ctx)
	if !ok {
		return
	}
	isJSON := strings.HasPrefix(strings.ToLower(string(ctx.Request.Header.ContentType())), "application/json")
	resources, err := decodeOTLP(ctx.PostBody(), isJSON)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "malformed OTLP trace export: "+err.Error())
		return
	}
	project := ""
	parent := strings.TrimSpace(string(ctx.Request.Header.Peek("x-bt-parent")))
	switch kind, value, found := strings.Cut(parent, ":"); {
	case found && (kind == "project_id" || kind == "project_name"):
		project = value
	case found && kind == "experiment_id":
		project, _ = traceingest.BraintrustObjectProject("exp", value)
	case parent != "":
		if components, err := traceingest.DecodeBraintrustParent(parent); err == nil {
			project = components.ObjectID
			if components.ObjectType == "experiment" {
				project, _ = traceingest.BraintrustObjectProject("exp", components.ObjectID)
			}
			if project == "" {
				project = components.ProjectName
			}
		}
	}
	if _, err := h.ingestOTLP(principal, logstore.TraceSourceBraintrust, resources, project, false); err != nil {
		h.fail(ctx, err)
		return
	}
	writeOTLPSuccess(ctx, isJSON)
}

// braintrustObjectRedirect sends a span.link() URL to that trace in Bifrost. The URL names the row,
// so the row id is looked up for its trace. An unknown row lands on the project's trace list.
func (h *TraceIngestHandler) braintrustObjectRedirect(ctx *fasthttp.RequestCtx) {
	args := ctx.QueryArgs()
	objectID, rowID := string(args.Peek("object_id")), string(args.Peek("id"))
	projectID := objectID
	if fromExperiment, ok := traceingest.BraintrustObjectProject("exp", objectID); ok {
		projectID = fromExperiment
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	if rowID != "" && projectID != "" {
		if spans, err := h.service.Store().FindTraceSpansByRowIDs(storeCtx, projectID, []string{rowID}); err == nil && len(spans) > 0 {
			ctx.Redirect(traceDashboardPath(projectID, spans[0].TraceID)+"&span="+url.QueryEscape(spans[0].SpanID), fasthttp.StatusFound)
			return
		}
	}
	ctx.Redirect("/workspace/tracing?project="+url.QueryEscape(projectID), fasthttp.StatusFound)
}

// braintrustProjectRedirect sends a project or experiment URL to the project's trace list.
func (h *TraceIngestHandler) braintrustProjectRedirect(ctx *fasthttp.RequestCtx) {
	project, _ := ctx.UserValue("project").(string)
	if unescaped, err := url.PathUnescape(project); err == nil {
		project = unescaped
	}
	target := "/workspace/tracing?project=" + url.QueryEscape(project)
	if rest, _ := ctx.UserValue("rest").(string); strings.HasPrefix(rest, "experiments/") {
		target += "&experiment=" + url.QueryEscape(strings.TrimPrefix(rest, "experiments/"))
	}
	ctx.Redirect(target, fasthttp.StatusFound)
}

// braintrustSummarize answers the Python SDK's end-of-eval summary with an empty one.
func (h *TraceIngestHandler) braintrustSummarize(ctx *fasthttp.RequestCtx) {
	if _, ok := h.braintrustPrincipal(ctx); !ok {
		return
	}
	id, _ := ctx.UserValue("id").(string)
	projectID, _ := traceingest.BraintrustObjectProject("exp", id)
	base := h.publicBaseURL(ctx) + braintrustPrefix + "/app/" + braintrustOrg + "/p/" + url.PathEscape(projectID)
	SendJSON(ctx, map[string]any{
		"project_name": projectID, "experiment_name": id, "project_url": base, "experiment_url": base + "/experiments/" + url.PathEscape(id),
		"comparison_experiment_name": nil, "scores": map[string]any{}, "metrics": map[string]any{},
	})
}

// braintrustNoBaseExperiment tells the JS SDK there is no experiment to compare against, which is
// the answer Braintrust itself gives for a first run.
func (h *TraceIngestHandler) braintrustNoBaseExperiment(ctx *fasthttp.RequestCtx) {
	if _, ok := h.braintrustPrincipal(ctx); ok {
		SendError(ctx, fasthttp.StatusBadRequest, "no base experiment")
	}
}

func (h *TraceIngestHandler) braintrustComparison(ctx *fasthttp.RequestCtx) {
	if _, ok := h.braintrustPrincipal(ctx); ok {
		SendJSON(ctx, map[string]any{"scores": map[string]any{}, "metrics": map[string]any{}})
	}
}

// braintrustBTQL refuses Braintrust's query language, which Bifrost does not implement. The error
// says so plainly, since the SDK surfaces it to the user.
func (h *TraceIngestHandler) braintrustBTQL(ctx *fasthttp.RequestCtx) {
	if _, ok := h.braintrustPrincipal(ctx); ok {
		SendError(ctx, fasthttp.StatusNotImplemented, fmt.Sprintf("BTQL is not supported by Bifrost. Read traces from the Bifrost dashboard or from %s/api/traces.", h.publicBaseURL(ctx)))
	}
}
