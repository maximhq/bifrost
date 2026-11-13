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

// TraceExperimentSummary is what an experiment's traces add up to.
type TraceExperimentSummary struct {
	TraceCount    int                `json:"trace_count"`
	ErrorCount    int                `json:"error_count"`
	Scores        map[string]float64 `json:"scores"` // the mean of each score name across the traces that carry it
	AvgDurationMs float64            `json:"avg_duration_ms"`
	TotalTokens   int                `json:"total_tokens"`
	TotalCost     float64            `json:"total_cost"`
}

// summarizeTraceExperiment adds up the rollups of all of an experiment's traces. Each figure is one
// aggregate query, so the summary covers every trace and a trace written meanwhile is counted once
// or not at all, never twice.
func summarizeTraceExperiment(ctx context.Context, store logstore.TraceStore, projectID, experimentID string) (*TraceExperimentSummary, error) {
	filters := logstore.TraceFilters{ProjectIDs: []string{projectID}, ExperimentID: experimentID}
	stats, err := store.GetTraceStats(ctx, filters)
	if err != nil {
		return nil, err
	}
	scores, err := store.GetTraceScoreMeans(ctx, filters)
	if err != nil {
		return nil, err
	}
	return &TraceExperimentSummary{
		TraceCount: int(stats.TraceCount), ErrorCount: int(stats.ErrorTraceCount), Scores: scores,
		AvgDurationMs: stats.AvgDurationMs, TotalTokens: int(stats.TotalTokens), TotalCost: stats.TotalCost,
	}, nil
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
	// A Langfuse run belongs to a dataset, and the request names only the item: the run is keyed by
	// the dataset the item is stored in, so one run name in two datasets is two runs.
	runKey := runName
	if datasetID, err := h.datasets.RowDataset(storeCtx, itemID); err == nil && datasetID != "" {
		runKey = datasetID + "/" + runName
	} else if err != nil {
		h.fail(ctx, err)
		return
	}
	runID := traceingest.BraintrustObjectID("run", project.ID, runKey)
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
