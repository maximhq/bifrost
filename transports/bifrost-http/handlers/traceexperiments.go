package handlers

import (
	"context"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/traceingest"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// traceExperimentMaxTraces bounds how many traces one experiment summary reads. An experiment with
// more is summarised over its newest ones, and the summary says so.
const traceExperimentMaxTraces = 10000

// TraceExperimentSummary is what an experiment's traces add up to.
type TraceExperimentSummary struct {
	TraceCount    int                `json:"trace_count"`
	ErrorCount    int                `json:"error_count"`
	Scores        map[string]float64 `json:"scores"` // the mean of each score name across the traces that carry it
	AvgDurationMs float64            `json:"avg_duration_ms"`
	TotalTokens   int                `json:"total_tokens"`
	TotalCost     float64            `json:"total_cost"`
	// Truncated is set when the experiment has more traces than the summary read.
	Truncated bool `json:"truncated,omitempty"`
}

// summarizeTraceExperiment adds up the rollups of an experiment's traces.
func summarizeTraceExperiment(ctx context.Context, store logstore.TraceStore, projectID, experimentID string) (*TraceExperimentSummary, error) {
	summary := &TraceExperimentSummary{Scores: map[string]float64{}}
	sums, counts := map[string]float64{}, map[string]int{}
	var duration float64
	timed := 0
	const page = 500
	for offset := 0; offset < traceExperimentMaxTraces; offset += page {
		result, err := store.SearchTraces(ctx, logstore.TraceFilters{ProjectIDs: []string{projectID}, ExperimentID: experimentID}, logstore.PaginationOptions{Limit: page, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, trace := range result.Traces {
			summary.TraceCount++
			if trace.ErrorCount > 0 {
				summary.ErrorCount++
			}
			summary.TotalTokens += trace.TotalTokens
			if trace.Cost != nil {
				summary.TotalCost += *trace.Cost
			}
			if trace.DurationMs != nil {
				duration += *trace.DurationMs
				timed++
			}
			for name, value := range trace.ScoresParsed {
				sums[name] += value
				counts[name]++
			}
		}
		if len(result.Traces) < page {
			break
		}
		if offset+page >= traceExperimentMaxTraces && result.Pagination.TotalCount > int64(traceExperimentMaxTraces) {
			summary.Truncated = true
		}
	}
	for name, sum := range sums {
		summary.Scores[name] = sum / float64(counts[name])
	}
	if timed > 0 {
		summary.AvgDurationMs = duration / float64(timed)
	}
	return summary, nil
}

func init() {
	tracesHandlerExtensions = append(tracesHandlerExtensions, func(h *TracesHandler, r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
		r.GET("/api/traces/experiments", lib.ChainMiddlewares(h.listExperiments, middlewares...))
	})
	traceIngestAdapters = append(traceIngestAdapters, func(h *TraceIngestHandler, rt *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
		// Langfuse: item.run() and item.link() attach a trace to a named run of a dataset.
		rt.POST(langfusePrefix+"/api/public/dataset-run-items", lib.ChainMiddlewares(h.langfuseCreateDatasetRunItem, middlewares...))
	})
}

// listExperiments returns the experiments SDKs registered, newest first, each with its summary.
func (h *TracesHandler) listExperiments(ctx *fasthttp.RequestCtx) {
	storeCtx, cancel := h.storeContext()
	defer cancel()
	experiments, err := h.store.ListTraceExperiments(storeCtx, parseCommaSeparated(string(ctx.QueryArgs().Peek("project_ids"))), 50)
	if err != nil {
		h.fail(ctx, "list experiments", err)
		return
	}
	type row struct {
		*logstore.TraceExperiment
		Summary *TraceExperimentSummary `json:"summary"`
	}
	rows := make([]row, 0, len(experiments))
	for _, experiment := range experiments {
		summary, err := summarizeTraceExperiment(storeCtx, h.store, experiment.ProjectID, experiment.ID)
		if err != nil {
			h.fail(ctx, "summarize experiment", err)
			return
		}
		rows = append(rows, row{TraceExperiment: experiment, Summary: summary})
	}
	SendJSON(ctx, map[string]any{"experiments": rows})
}

// registerExperiment records an experiment the first time an SDK names it.
func (h *TraceIngestHandler) registerExperiment(projectID, id, name, source, datasetName, description string) {
	storeCtx, cancel := ingestContext()
	defer cancel()
	err := h.service.Store().RegisterTraceExperiment(storeCtx, &logstore.TraceExperiment{ProjectID: projectID, ID: id, Name: name, Source: source, DatasetName: datasetName, Description: description})
	if err != nil && h.logger != nil {
		// The experiment's traces are still stored and tagged; only its name is missing from the list.
		h.logger.Warn("failed to record experiment %s: %v", name, err)
	}
}

// langfuseCreateDatasetRunItem serves POST /api/public/dataset-run-items. A Langfuse dataset run is
// an experiment: the run is recorded by name and the trace is tagged with it, so the run's traces
// can be listed and summarised together.
func (h *TraceIngestHandler) langfuseCreateDatasetRunItem(ctx *fasthttp.RequestCtx) {
	principal, ok := h.datasetUser(ctx)
	if !ok {
		return
	}
	var body map[string]any
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil {
		langfuseError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	runName, itemID, traceID := stringField(body, "runName"), stringField(body, "datasetItemId"), stringField(body, "traceId")
	if runName == "" || itemID == "" {
		langfuseError(ctx, fasthttp.StatusBadRequest, "runName and datasetItemId are required")
		return
	}
	storeCtx, cancel := ingestContext()
	defer cancel()
	project, err := h.service.ResolveProject(storeCtx, principal, "")
	if err != nil {
		h.fail(ctx, err)
		return
	}
	runID := traceingest.BraintrustObjectID("run", project.ID, runName)
	h.registerExperiment(project.ID, runID, runName, logstore.TraceSourceLangfuse, stringField(body, "datasetName"), stringField(body, "runDescription"))
	observationID := stringField(body, "observationId")
	if traceID != "" {
		// The trace is tagged on the span the run item names, else on its root. A trace whose root
		// is not known yet is tagged on the span that stands for the Langfuse trace object.
		spanID := observationID
		if spanID == "" {
			spanID = traceID
			if trace, err := h.service.Store().FindTrace(storeCtx, project.ID, traceID); err == nil && trace.RootSpanID != "" {
				spanID = trace.RootSpanID
			}
		}
		tag := &traceingest.SpanPart{TraceID: traceID, SpanID: spanID, IsMerge: true, ExperimentID: runID, Metadata: map[string]any{"dataset_item_id": itemID}}
		if err := h.service.Ingest(storeCtx, principal, project, logstore.TraceSourceLangfuse, []*traceingest.SpanPart{tag}); err != nil {
			h.fail(ctx, err)
			return
		}
	}
	now := langfuseTimestamp(time.Now())
	item := map[string]any{
		"id": strings.TrimPrefix(runID, "run-") + "-" + itemID, "datasetRunId": runID, "datasetRunName": runName, "datasetItemId": itemID,
		"traceId": traceID, "observationId": nil, "createdAt": now, "updatedAt": now,
	}
	if observationID != "" {
		item["observationId"] = observationID
	}
	SendJSON(ctx, item)
}
