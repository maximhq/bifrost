package handlers

import (
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

func init() {
	traceIngestAdapters = append(traceIngestAdapters, func(h *TraceIngestHandler, rt *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
		api := langfusePrefix + "/api/public"
		// The read routes of Langfuse's current API. Trace and session reads are not part of it.
		rt.GET(api+"/v2/observations", lib.ChainMiddlewares(h.langfuseListObservations, middlewares...))
		rt.GET(api+"/v3/scores", lib.ChainMiddlewares(h.langfuseListScores, middlewares...))
		rt.DELETE(api+"/traces", lib.ChainMiddlewares(h.langfuseDeleteTraces, middlewares...))
		rt.DELETE(api+"/scores/{id}", lib.ChainMiddlewares(h.langfuseDeleteScore, middlewares...))
	})
}

// langfuseObservationTypes maps Bifrost's span kinds onto Langfuse's observation types.
var langfuseObservationTypes = map[string]string{
	logstore.TraceSpanKindLLM: "GENERATION", logstore.TraceSpanKindEvent: "EVENT", logstore.TraceSpanKindTool: "TOOL",
	logstore.TraceSpanKindAgent: "AGENT", logstore.TraceSpanKindChain: "CHAIN", logstore.TraceSpanKindRetriever: "RETRIEVER",
	logstore.TraceSpanKindEval: "EVALUATOR", logstore.TraceSpanKindEmbedding: "EMBEDDING", logstore.TraceSpanKindGuardrail: "GUARDRAIL",
}

func langfuseKindFor(observationType string) string {
	for kind, name := range langfuseObservationTypes {
		if strings.EqualFold(name, observationType) {
			return kind
		}
	}
	return ""
}

func langfuseTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// langfuseCursor reads the page position. The cursor is the offset of the next page.
func langfuseCursor(ctx *fasthttp.RequestCtx, maxLimit int) (limit, offset int, ok bool) {
	limit = 50
	if raw := string(ctx.QueryArgs().Peek("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxLimit {
			langfuseError(ctx, fasthttp.StatusBadRequest, "limit must be between 1 and "+strconv.Itoa(maxLimit))
			return 0, 0, false
		}
		limit = parsed
	}
	if raw := string(ctx.QueryArgs().Peek("cursor")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			langfuseError(ctx, fasthttp.StatusBadRequest, "cursor is not valid")
			return 0, 0, false
		}
		offset = parsed
	}
	return limit, offset, true
}

func langfuseTimeParam(ctx *fasthttp.RequestCtx, name string) (*time.Time, bool) {
	raw := string(ctx.QueryArgs().Peek(name))
	if raw == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		langfuseError(ctx, fasthttp.StatusBadRequest, name+" must be an ISO 8601 time")
		return nil, false
	}
	return &parsed, true
}

// langfuseContent returns stored span content as the JSON value it was sent as.
func langfuseContent(stored string) any {
	if stored == "" {
		return nil
	}
	if trimmed := strings.TrimSpace(stored); strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var value any
		if sonic.UnmarshalString(stored, &value) == nil {
			return value
		}
	}
	return stored
}

// langfuseListObservations serves GET /api/public/v2/observations: the project's spans in
// Langfuse's ObservationV2 shape, newest first, with cursor paging.
func (h *TraceIngestHandler) langfuseListObservations(ctx *fasthttp.RequestCtx) {
	principal, ok := h.langfusePrincipal(ctx)
	if !ok {
		return
	}
	limit, offset, ok := langfuseCursor(ctx, 1000)
	if !ok {
		return
	}
	from, ok := langfuseTimeParam(ctx, "fromStartTime")
	if !ok {
		return
	}
	to, ok := langfuseTimeParam(ctx, "toStartTime")
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
	args := ctx.QueryArgs()
	filters := logstore.TraceFilters{
		ProjectIDs: []string{project.ID}, TraceID: string(args.Peek("traceId")), Name: string(args.Peek("name")),
		SessionID: string(args.Peek("sessionId")), UserID: string(args.Peek("userId")), ParentSpanID: string(args.Peek("parentObservationId")),
		Environments: parseCommaSeparated(string(args.Peek("environment"))), StartTime: from, EndTime: to, ExcludeTraceRows: true,
	}
	if observationType := string(args.Peek("type")); observationType != "" {
		kind := langfuseKindFor(observationType)
		if kind == "" {
			// SPAN is every kind Langfuse has no type of its own for.
			filters.Kinds = []string{logstore.TraceSpanKindTask, logstore.TraceSpanKindFunction, logstore.TraceSpanKindReranker, logstore.TraceSpanKindScore, logstore.TraceSpanKindHTTP, logstore.TraceSpanKindUnknown}
		} else {
			filters.Kinds = []string{kind}
		}
	}
	if strings.EqualFold(string(args.Peek("level")), "ERROR") {
		filters.Status = logstore.TraceSpanStatusError
	}
	// One more than asked for: its presence says there is a next page.
	result, err := h.service.Store().SearchTraceSpans(storeCtx, filters, logstore.PaginationOptions{Limit: min(limit+1, 500), Offset: offset})
	if err != nil {
		h.fail(ctx, err)
		return
	}
	spans := result.Spans
	meta := map[string]any{}
	if len(spans) > limit {
		spans = spans[:limit]
		meta["cursor"] = strconv.Itoa(offset + limit)
	}
	data := make([]map[string]any, 0, len(spans))
	for _, span := range spans {
		data = append(data, langfuseObservation(span))
	}
	SendJSON(ctx, map[string]any{"data": data, "meta": meta})
}

// langfuseObservation renders a span as a Langfuse ObservationV2. The fields the schema requires
// are always present, as null when Bifrost holds no value for them.
func langfuseObservation(span *logstore.TraceSpan) map[string]any {
	observationType := langfuseObservationTypes[span.Kind]
	if observationType == "" {
		observationType = "SPAN"
	}
	observation := map[string]any{
		"id": span.SpanID, "traceId": span.TraceID, "projectId": span.ProjectID, "type": observationType,
		"startTime": langfuseTimestamp(span.StartTime), "endTime": nil, "parentObservationId": nil,
		"modelId": nil, "inputPrice": nil, "outputPrice": nil, "totalPrice": nil,
		"name": span.Name, "level": "DEFAULT", "input": langfuseContent(span.Input), "output": langfuseContent(span.Output),
		"isRootObservation": span.ParentSpanID == "" || span.ParentSpanID == span.TraceID,
	}
	if span.EndTime != nil {
		observation["endTime"] = langfuseTimestamp(*span.EndTime)
	}
	// A Langfuse trace is stored as a root span whose id is the trace id: it is not an observation's parent.
	if span.ParentSpanID != "" && span.ParentSpanID != span.TraceID {
		observation["parentObservationId"] = span.ParentSpanID
	}
	if span.Status == logstore.TraceSpanStatusError {
		observation["level"] = "ERROR"
		observation["statusMessage"] = span.Error
	}
	for key, value := range map[string]string{"environment": span.Environment, "userId": span.UserID, "sessionId": span.SessionID, "model": span.Model} {
		if value != "" {
			observation[key] = value
		}
	}
	if len(span.MetadataParsed) > 0 {
		observation["metadata"] = span.MetadataParsed
	}
	if len(span.TagsParsed) > 0 {
		observation["tags"] = span.TagsParsed
	}
	// A linked span reports the gateway's numbers, the ones Bifrost counts.
	prompt, completion, total, cost := span.PromptTokens, span.CompletionTokens, span.TotalTokens, span.Cost
	if linked := span.LinkSnapshotParsed; linked != nil {
		prompt, completion, total, cost = linked.PromptTokens, linked.CompletionTokens, linked.TotalTokens, linked.Cost
	}
	if total > 0 {
		observation["usageDetails"] = map[string]any{"input": prompt, "output": completion, "total": total}
	}
	if cost != nil {
		observation["costDetails"] = map[string]any{"total": *cost}
		observation["totalCost"] = *cost
	}
	if span.DurationMs != nil {
		observation["latency"] = *span.DurationMs / 1000
	}
	if span.TimeToFirstTokenMs != nil {
		observation["timeToFirstToken"] = *span.TimeToFirstTokenMs / 1000
	}
	return observation
}

// langfuseScoreSources maps Bifrost's score authors onto Langfuse's score sources, both ways.
var langfuseScoreSources = map[string]string{logstore.TraceAuthorSDK: "API", logstore.TraceAuthorEvaluator: "EVAL", logstore.TraceAuthorHuman: "ANNOTATION"}

// langfuseListScores serves GET /api/public/v3/scores in Langfuse's ScoreV3 shape.
func (h *TraceIngestHandler) langfuseListScores(ctx *fasthttp.RequestCtx) {
	principal, ok := h.langfusePrincipal(ctx)
	if !ok {
		return
	}
	limit, offset, ok := langfuseCursor(ctx, 100)
	if !ok {
		return
	}
	from, ok := langfuseTimeParam(ctx, "fromTimestamp")
	if !ok {
		return
	}
	to, ok := langfuseTimeParam(ctx, "toTimestamp")
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
	args := ctx.QueryArgs()
	list := func(name string) []string { return parseCommaSeparated(string(args.Peek(name))) }
	filters := logstore.TraceScoreFilters{ProjectID: project.ID, IDs: list("id"), Names: list("name"), TraceIDs: list("traceId"), SpanIDs: list("observationId"), From: from, To: to}
	for _, source := range list("source") {
		for author, name := range langfuseScoreSources {
			if strings.EqualFold(name, source) {
				filters.Sources = append(filters.Sources, author)
			}
		}
	}
	scores, err := h.service.Store().SearchTraceScores(storeCtx, filters, limit+1, offset)
	if err != nil {
		h.fail(ctx, err)
		return
	}
	meta := map[string]any{"limit": limit}
	if len(scores) > limit {
		scores = scores[:limit]
		meta["cursor"] = strconv.Itoa(offset + limit)
	}
	data := make([]map[string]any, 0, len(scores))
	for _, score := range scores {
		entry := map[string]any{
			"id": score.ID, "projectId": score.ProjectID, "name": score.Name, "source": langfuseScoreSources[score.Source],
			"timestamp": langfuseTimestamp(score.CreatedAt), "createdAt": langfuseTimestamp(score.CreatedAt), "updatedAt": langfuseTimestamp(score.UpdatedAt),
			"environment": "default", "traceId": score.TraceID,
		}
		if entry["source"] == "" {
			entry["source"] = "API"
		}
		if score.SpanID != "" && score.SpanID != score.TraceID {
			entry["observationId"] = score.SpanID
		}
		if score.Comment != "" {
			entry["comment"] = score.Comment
		}
		switch {
		case score.Value != nil && strings.EqualFold(score.DataType, "boolean"):
			entry["dataType"], entry["value"] = "BOOLEAN", *score.Value != 0
		case score.Value != nil:
			entry["dataType"], entry["value"] = "NUMERIC", *score.Value
		case strings.EqualFold(score.DataType, "text"):
			entry["dataType"], entry["value"] = "TEXT", score.StringValue
		default:
			entry["dataType"], entry["value"] = "CATEGORICAL", score.StringValue
		}
		data = append(data, entry)
	}
	SendJSON(ctx, map[string]any{"data": data, "meta": meta})
}

// langfuseDeleteTraces serves DELETE /api/public/traces with a list of trace ids.
func (h *TraceIngestHandler) langfuseDeleteTraces(ctx *fasthttp.RequestCtx) {
	principal, ok := h.langfusePrincipal(ctx)
	if !ok {
		return
	}
	var body struct {
		TraceIDs []string `json:"traceIds"`
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil || len(body.TraceIDs) == 0 || len(body.TraceIDs) > 1000 {
		langfuseError(ctx, fasthttp.StatusBadRequest, "traceIds must hold between 1 and 1000 ids")
		return
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	project, err := h.service.ResolveProject(storeCtx, principal, "")
	if err != nil {
		h.fail(ctx, err)
		return
	}
	keys := make([]logstore.TraceKey, len(body.TraceIDs))
	for i, id := range body.TraceIDs {
		keys[i] = logstore.TraceKey{ProjectID: project.ID, TraceID: id}
	}
	if err := h.service.Store().DeleteTraces(storeCtx, keys); err != nil {
		h.fail(ctx, err)
		return
	}
	SendJSON(ctx, map[string]any{"message": "Traces deleted successfully"})
}

// langfuseDeleteScore serves DELETE /api/public/scores/{id}.
func (h *TraceIngestHandler) langfuseDeleteScore(ctx *fasthttp.RequestCtx) {
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
	store := h.service.Store()
	found, err := store.SearchTraceScores(storeCtx, logstore.TraceScoreFilters{ProjectID: project.ID, IDs: []string{id}}, 1, 0)
	if err != nil {
		h.fail(ctx, err)
		return
	}
	if len(found) == 0 {
		langfuseError(ctx, fasthttp.StatusNotFound, "score not found")
		return
	}
	if err := store.DeleteTraceScore(storeCtx, project.ID, id); err != nil {
		h.fail(ctx, err)
		return
	}
	if err := store.MarkTracesDirty(storeCtx, []logstore.TraceKey{{ProjectID: project.ID, TraceID: found[0].TraceID}}); err != nil {
		h.fail(ctx, err)
		return
	}
	ctx.SetStatusCode(fasthttp.StatusNoContent)
}
