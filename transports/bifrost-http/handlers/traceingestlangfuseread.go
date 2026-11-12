package handlers

import (
	"math"
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

// langfuseContent returns stored span content as the raw string the observations API defines.
func langfuseContent(stored string) any {
	if stored == "" {
		return nil
	}
	return stored
}

// langfuseObservationFieldGroups names the field group each optional observation field belongs to.
// Fields not listed (core, plus the schema's required nullable fields) are always returned.
var langfuseObservationFieldGroups = map[string]string{
	"name": "basic", "level": "basic", "statusMessage": "basic", "environment": "basic", "userId": "basic", "sessionId": "basic", "isRootObservation": "basic",
	"input": "io", "output": "io",
	"metadata":     "metadata",
	"model":        "model",
	"usageDetails": "usage", "costDetails": "usage", "totalCost": "usage",
	"latency": "metrics", "timeToFirstToken": "metrics",
	"tags": "trace_context",
}

// langfuseFieldGroups reads the fields parameter. Core and basic are returned when it is absent.
func langfuseFieldGroups(raw string) map[string]bool {
	groups := map[string]bool{"core": true}
	if strings.TrimSpace(raw) == "" {
		groups["basic"] = true
		return groups
	}
	for _, group := range parseCommaSeparated(raw) {
		groups[strings.ToLower(group)] = true
	}
	return groups
}

// langfuseQueryValues reads every occurrence of a repeatable parameter, each comma separated.
func langfuseQueryValues(args *fasthttp.Args, name string) []string {
	var values []string
	for _, raw := range args.PeekMulti(name) {
		values = append(values, parseCommaSeparated(string(raw))...)
	}
	return values
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
	args := ctx.QueryArgs()
	if parseIO, err := strconv.ParseBool(string(args.Peek("parseIoAsJson"))); err == nil && parseIO {
		langfuseError(ctx, fasthttp.StatusBadRequest, "parseIoAsJson is deprecated: input and output are always returned as raw strings")
		return
	}
	filters := logstore.TraceFilters{
		TraceID: string(args.Peek("traceId")), Name: string(args.Peek("name")),
		SessionID: string(args.Peek("sessionId")), UserID: string(args.Peek("userId")), ParentSpanID: string(args.Peek("parentObservationId")),
		Environments: langfuseQueryValues(args, "environment"), StartTime: from, EndTime: to, ExcludeTraceRows: true,
	}
	// A present but empty parentObservationId means observations without a parent.
	filters.RootOnly = args.Has("parentObservationId") && filters.ParentSpanID == ""
	// Bifrost renders an observation as ERROR or DEFAULT, so DEBUG and WARNING match nothing.
	noMatches := false
	switch level := strings.ToUpper(string(args.Peek("level"))); level {
	case "":
	case "DEFAULT":
		filters.ExcludeErrors = true
	case "ERROR":
		filters.Status = logstore.TraceSpanStatusError
	case "DEBUG", "WARNING":
		noMatches = true
	default:
		langfuseError(ctx, fasthttp.StatusBadRequest, "level must be one of DEBUG, DEFAULT, WARNING, ERROR")
		return
	}
	groups := langfuseFieldGroups(string(args.Peek("fields")))
	storeCtx, cancel := ingestContext()
	defer cancel()
	project, err := h.service.ResolveProject(storeCtx, principal, "")
	if err != nil {
		h.fail(ctx, err)
		return
	}
	if noMatches {
		SendJSON(ctx, map[string]any{"data": []map[string]any{}, "meta": map[string]any{}})
		return
	}
	filters.ProjectIDs = []string{project.ID}
	if observationType := string(args.Peek("type")); observationType != "" {
		kind := langfuseKindFor(observationType)
		if kind == "" {
			// SPAN is every kind Langfuse has no type of its own for.
			filters.Kinds = []string{logstore.TraceSpanKindTask, logstore.TraceSpanKindFunction, logstore.TraceSpanKindReranker, logstore.TraceSpanKindScore, logstore.TraceSpanKindHTTP, logstore.TraceSpanKindUnknown}
		} else {
			filters.Kinds = []string{kind}
		}
	}
	// The store serves at most 500 rows a page. Langfuse allows a page shorter than the limit while
	// a cursor is present, so a larger limit pages through the same rows in steps of 500.
	result, err := h.service.Store().SearchTraceSpans(storeCtx, filters, logstore.PaginationOptions{Limit: min(limit, 500), Offset: offset})
	if err != nil {
		h.fail(ctx, err)
		return
	}
	spans := result.Spans
	meta := map[string]any{}
	if next := offset + len(spans); len(spans) > 0 && int64(next) < result.Pagination.TotalCount {
		meta["cursor"] = strconv.Itoa(next)
	}
	data := make([]map[string]any, 0, len(spans))
	for _, span := range spans {
		data = append(data, langfuseObservation(span, groups))
	}
	SendJSON(ctx, map[string]any{"data": data, "meta": meta})
}

// langfuseObservation renders a span as a Langfuse ObservationV2, with the requested field groups.
// The fields the schema requires are always present, as null when Bifrost holds no value for them.
func langfuseObservation(span *logstore.TraceSpan, groups map[string]bool) map[string]any {
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
	for key := range observation {
		if group, optional := langfuseObservationFieldGroups[key]; optional && !groups[group] {
			delete(observation, key)
		}
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
	args := ctx.QueryArgs()
	list := func(name string) []string { return parseCommaSeparated(string(args.Peek(name))) }
	filters := logstore.TraceScoreFilters{IDs: list("id"), Names: list("name"), TraceIDs: list("traceId"), SpanIDs: list("observationId"), From: from, To: to}
	if message := langfuseScoreValueFilters(args, &filters); message != "" {
		langfuseError(ctx, fasthttp.StatusBadRequest, message)
		return
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	project, err := h.service.ResolveProject(storeCtx, principal, "")
	if err != nil {
		h.fail(ctx, err)
		return
	}
	filters.ProjectID = project.ID
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

// langfuseScoreTypes maps Langfuse's score data types onto Bifrost's score types. CORRECTION has no
// Bifrost equivalent, so it matches nothing.
var langfuseScoreTypes = map[string]string{
	"NUMERIC": logstore.TraceScoreTypeNumeric, "BOOLEAN": logstore.TraceScoreTypeBoolean, "CATEGORICAL": logstore.TraceScoreTypeCategorical,
	"TEXT": logstore.TraceScoreTypeText, "CORRECTION": "correction",
}

// langfuseScoreValueFilters reads the dataType, value, valueMin and valueMax filters of Scores v3,
// with its rules: value needs one dataType of NUMERIC, BOOLEAN or CATEGORICAL, and the bounds need
// dataType=NUMERIC alone. It returns the reason a request is refused, or "".
func langfuseScoreValueFilters(args *fasthttp.Args, filters *logstore.TraceScoreFilters) string {
	var langfuseTypes []string
	for _, raw := range parseCommaSeparated(string(args.Peek("dataType"))) {
		name := strings.ToUpper(raw)
		scoreType, known := langfuseScoreTypes[name]
		if !known {
			return "dataType must be NUMERIC, BOOLEAN, CATEGORICAL, TEXT or CORRECTION"
		}
		langfuseTypes = append(langfuseTypes, name)
		filters.DataTypes = append(filters.DataTypes, scoreType)
	}
	single := ""
	if len(langfuseTypes) == 1 {
		single = langfuseTypes[0]
	}
	finite := func(raw string) (float64, bool) {
		value, err := strconv.ParseFloat(raw, 64)
		return value, err == nil && !math.IsInf(value, 0) && !math.IsNaN(value)
	}
	for name, bound := range map[string]**float64{"valueMin": &filters.ValueMin, "valueMax": &filters.ValueMax} {
		raw := string(args.Peek(name))
		if raw == "" {
			continue
		}
		if single != "NUMERIC" {
			return name + " requires dataType=NUMERIC as a single value"
		}
		value, ok := finite(raw)
		if !ok {
			return name + " must be a finite number"
		}
		*bound = new(value)
	}
	values := parseCommaSeparated(string(args.Peek("value")))
	if len(values) == 0 {
		return ""
	}
	switch single {
	case "NUMERIC":
		for _, raw := range values {
			value, ok := finite(raw)
			if !ok {
				return "value must hold finite numbers for dataType=NUMERIC"
			}
			filters.Values = append(filters.Values, value)
		}
	case "BOOLEAN":
		for _, raw := range values {
			switch raw {
			case "true":
				filters.Values = append(filters.Values, 1)
			case "false":
				filters.Values = append(filters.Values, 0)
			default:
				return "value must be true or false for dataType=BOOLEAN"
			}
		}
	case "CATEGORICAL":
		filters.StringValues = values
	default:
		return "value requires a single dataType of NUMERIC, BOOLEAN or CATEGORICAL"
	}
	return ""
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
