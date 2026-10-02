package warp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// Charts used to arrive as mermaid code the model wrote itself, numbers and
// all. render_chart reads the data, so a line chart's points are the
// histogram's buckets and the model supplies only what to plot.
func TestWarpRenderChartLineReadsTheHistogram(t *testing.T) {
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	fake := &fakeLogReader{histogramResult: &logstore.HistogramResult{Buckets: []logstore.HistogramBucket{
		{Timestamp: day, Count: 40, Error: 5},
		{Timestamp: day.Add(24 * time.Hour), Count: 55, Error: 30},
	}}}
	deps := &ToolDeps{logManager: fake, charts: newChartRegistry()}
	out, err := runTool(t, RenderChartTool, deps, map[string]any{
		"kind": "line", "title": "Errors per day", "metric": "errors", "interval": "day",
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.NoError(t, err)
	require.Equal(t, int64(86400), fake.histogramBucket)

	result := out.(map[string]any)
	require.Equal(t, "chart-1", result["chart_id"])
	require.Equal(t, "```warp-chart\nchart-1\n```", result["block"])

	spec, ok := deps.charts.get("chart-1")
	require.True(t, ok)
	require.Equal(t, "line", spec.Kind)
	require.Equal(t, "count", spec.Unit)
	require.Equal(t, []ChartPoint{{X: "2026-09-22T00:00:00Z", Y: 5}, {X: "2026-09-23T00:00:00Z", Y: 30}}, spec.Points)
}

// A bar chart ranks, largest first, and stops at the limit.
func TestWarpRenderChartBarRanksLargestFirst(t *testing.T) {
	fake := &fakeLogReader{modelRankingResult: &logstore.ModelRankingResult{Rankings: []logstore.ModelRankingWithTrend{
		{ModelRankingEntry: logstore.ModelRankingEntry{Model: "gpt-4o-mini", TotalCost: 0.02}},
		{ModelRankingEntry: logstore.ModelRankingEntry{Model: "claude-3-opus", TotalCost: 2.1}},
		{ModelRankingEntry: logstore.ModelRankingEntry{Model: "claude-3-5-sonnet", TotalCost: 0.4}},
	}}}
	deps := &ToolDeps{logManager: fake, charts: newChartRegistry()}
	_, err := runTool(t, RenderChartTool, deps, map[string]any{
		"kind": "bar", "title": "Spend by model", "metric": "cost", "group": "model", "limit": float64(2),
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.NoError(t, err)
	spec, _ := deps.charts.get("chart-1")
	require.Equal(t, []ChartPoint{{X: "claude-3-opus", Y: 2.1}, {X: "claude-3-5-sonnet", Y: 0.4}}, spec.Points)
}

// A cost chart ranked by the store's own order kept the busiest groups, not the
// costliest: the limit applied before sorting by cost. Every group is fetched,
// and the limit cuts the points only after they are sorted by the metric.
func TestWarpRenderChartBarLimitsAfterSortingByMetric(t *testing.T) {
	fake := &fakeLogReader{modelRankingResult: &logstore.ModelRankingResult{}}
	deps := &ToolDeps{logManager: fake, charts: newChartRegistry()}
	_, err := runTool(t, RenderChartTool, deps, map[string]any{
		"kind": "bar", "title": "Spend by model", "metric": "cost", "group": "model", "limit": float64(2),
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.NoError(t, err)
	require.NotNil(t, fake.rankingFilters.RankingLimit)
	require.Zero(t, *fake.rankingFilters.RankingLimit, "the store ranks by requests, so a store-side limit drops the costliest groups")
}

// "Errors by team" counts failed requests per team exactly: the same ranking,
// narrowed to status error, rather than a success rate applied to a total.
func TestWarpRenderChartErrorBarsCountFailedRequests(t *testing.T) {
	fake := &fakeLogReader{dimensionRankingResult: &logstore.DimensionRankingResult{Rankings: []logstore.DimensionRankingWithTrend{
		{DimensionRankingEntry: logstore.DimensionRankingEntry{ID: "team-platform", Name: "Platform Engineering", TotalRequests: 15}},
	}}}
	deps := &ToolDeps{logManager: fake, charts: newChartRegistry()}
	_, err := runTool(t, RenderChartTool, deps, map[string]any{
		"kind": "bar", "title": "Errors by team", "metric": "errors", "group": "team",
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"error"}, fake.rankingFilters.Status)
	spec, _ := deps.charts.get("chart-1")
	require.Equal(t, []ChartPoint{{X: "team-platform", Label: "Platform Engineering", Y: 15}}, spec.Points)
}

// Each wrong shape names the fix, so the model can correct itself in one step.
func TestWarpRenderChartRejectsShapesItCannotDraw(t *testing.T) {
	cases := map[string]struct {
		args map[string]any
		want string
	}{
		"line without interval":   {map[string]any{"kind": "line", "title": "t", "metric": "requests"}, `needs interval "hour", "day" or "week"`},
		"line with a group":       {map[string]any{"kind": "line", "title": "t", "metric": "requests", "interval": "day", "group": "model"}, "group is for bar charts"},
		"bar with neither":        {map[string]any{"kind": "bar", "title": "t", "metric": "cost"}, "a bar chart needs interval (bars over time) or group"},
		"bar with both":           {map[string]any{"kind": "bar", "title": "t", "metric": "cost", "group": "model", "interval": "day"}, "not both"},
		"latency by team":         {map[string]any{"kind": "bar", "title": "t", "metric": "latency", "group": "team"}, "latency can only be grouped by provider or model"},
		"pie":                     {map[string]any{"kind": "pie", "title": "t", "metric": "cost"}, "not supported"},
		"hourly over thirty days": {map[string]any{"kind": "line", "title": "t", "metric": "requests", "interval": "hour", "filters": map[string]any{"start_time": "-30d"}}, `use interval "day"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := c.args["filters"]; !ok {
				c.args["filters"] = map[string]any{"start_time": "-7d"}
			}
			_, err := runTool(t, RenderChartTool, &ToolDeps{logManager: &fakeLogReader{}, charts: newChartRegistry()}, c.args)
			require.ErrorContains(t, err, c.want)
		})
	}
}

// Only an id render_chart issued this turn becomes a chart. A spec the model
// wrote out itself - numbers it typed - and an id it made up are dropped.
func TestWarpExpandChartBlocksOnlyDrawsIssuedCharts(t *testing.T) {
	charts := newChartRegistry()
	spec, err := charts.add(ChartSpec{Kind: "bar", Title: "Spend by provider", Metric: "cost", Unit: "usd", Points: []ChartPoint{{X: "anthropic", Y: 2.58}}})
	require.NoError(t, err)

	answer := "Anthropic dominates.\n\n```warp-chart\n" + spec.ID + "\n```\n\nThat is 97% of spend."
	expanded := expandChartBlocks(answer, charts)
	body := strings.TrimSuffix(strings.SplitN(expanded, "```warp-chart\n", 2)[1], "\n```\n\nThat is 97% of spend.")
	var got ChartSpec
	require.NoError(t, sonic.UnmarshalString(body, &got), expanded)
	require.Equal(t, spec, got)
	require.True(t, strings.HasPrefix(expanded, "Anthropic dominates."))

	require.Contains(t, expandChartBlocks("```warp-chart\n{\"id\":\""+spec.ID+"\"}\n```", charts), `"points":[{"x":"anthropic","y":2.58}]`, "a quoted id is still an id")

	invented := `{"kind":"bar","points":[{"x":"openai","y":99}]}`
	require.Equal(t, "Look:\n\n", expandChartBlocks("Look:\n\n```warp-chart\n"+invented+"\n```", charts))
	require.Equal(t, "", expandChartBlocks("```warp-chart\nchart-9\n```", charts))
	require.Equal(t, "no charts here", expandChartBlocks("no charts here", nil))
}

// End to end: the model draws a chart and pastes the id; what streams, and so
// what history saves, carries the spec the tool built.
func TestWarpAgentStreamsExpandedCharts(t *testing.T) {
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{
		ToolTurn("c-1", RenderChartTool, `{"kind":"line","title":"Requests per day","metric":"requests","interval":"day","filters":{"start_time":"-7d"}}`),
		TextTurn("Traffic doubled on the 23rd.\n\n```warp-chart\nchart-1\n```"),
	}}
	fake := &fakeLogReader{histogramResult: &logstore.HistogramResult{Buckets: []logstore.HistogramBucket{
		{Timestamp: day, Count: 40}, {Timestamp: day.Add(24 * time.Hour), Count: 80},
	}}}
	events := collectEvents(t, newTestAgent(model, fake, 8), context.Background())

	var streamed strings.Builder
	for _, event := range events {
		if event.Type == EventDelta {
			streamed.WriteString(event.Delta)
		}
	}
	require.Contains(t, streamed.String(), `"title":"Requests per day"`)
	require.Contains(t, streamed.String(), `{"x":"2026-09-23T00:00:00Z","y":80}`)
	require.NotContains(t, streamed.String(), "```warp-chart\nchart-1\n```")
}

// "Make it a bar chart with per-week errors" had no render_chart shape: bars
// only compared groups and there was no week interval, so the model counted
// each week with count_logs and typed its own chart block - which expansion
// rightly dropped, leaving no chart. Bars now run over time too, and a week is
// a UTC ISO week built from daily buckets.
func TestWarpRenderChartBarsPerWeek(t *testing.T) {
	monday := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	fake := &fakeLogReader{histogramResult: &logstore.HistogramResult{Buckets: []logstore.HistogramBucket{
		{Timestamp: monday, Count: 100, Error: 10},
		{Timestamp: monday.Add(3 * 24 * time.Hour), Count: 50, Error: 5},
		{Timestamp: monday.Add(7 * 24 * time.Hour), Count: 40, Error: 30},
	}}}
	deps := &ToolDeps{logManager: fake, charts: newChartRegistry()}
	_, err := runTool(t, RenderChartTool, deps, map[string]any{
		"kind": "bar", "title": "Errors per week", "metric": "errors", "interval": "week",
		"filters": map[string]any{"start_time": "-28d"},
	})
	require.NoError(t, err)
	require.Equal(t, int64(86400), fake.histogramBucket, "weeks are summed from daily buckets")
	spec, _ := deps.charts.get(spec1(t, deps))
	require.Equal(t, "week", spec.Interval)
	require.Equal(t, []ChartPoint{{X: "2026-09-14T00:00:00Z", Y: 15}, {X: "2026-09-21T00:00:00Z", Y: 30}}, spec.Points, "time bars keep time order")
}

// "Failure rates" came back as error counts: there was no rate to plot. A
// week's rate is its errors over its requests, not an average of daily rates.
func TestWarpRenderChartErrorRateOverTime(t *testing.T) {
	monday := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	fake := &fakeLogReader{histogramResult: &logstore.HistogramResult{Buckets: []logstore.HistogramBucket{
		{Timestamp: monday, Count: 100, Error: 10},
		{Timestamp: monday.Add(24 * time.Hour), Count: 0},
		{Timestamp: monday.Add(3 * 24 * time.Hour), Count: 50, Error: 5},
	}}}
	deps := &ToolDeps{logManager: fake, charts: newChartRegistry()}
	_, err := runTool(t, RenderChartTool, deps, map[string]any{
		"kind": "line", "title": "Failure rate per day", "metric": "error_rate", "interval": "day",
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.NoError(t, err)
	spec, _ := deps.charts.get(spec1(t, deps))
	require.Equal(t, "percent", spec.Unit)
	require.Equal(t, []ChartPoint{{X: "2026-09-14T00:00:00Z", Y: 10}, {X: "2026-09-17T00:00:00Z", Y: 10}}, spec.Points, "a bucket with no requests has no rate and is left out")

	deps = &ToolDeps{logManager: fake, charts: newChartRegistry()}
	_, err = runTool(t, RenderChartTool, deps, map[string]any{
		"kind": "bar", "title": "Failure rate per week", "metric": "error_rate", "interval": "week",
		"filters": map[string]any{"start_time": "-28d"},
	})
	require.NoError(t, err)
	spec, _ = deps.charts.get(spec1(t, deps))
	require.Equal(t, []ChartPoint{{X: "2026-09-14T00:00:00Z", Y: 10}}, spec.Points, "15 errors over 150 requests")
}

// An empty bucket has no latency: it used to plot as 0 ms, and a week's average
// counted it as one request at 0 ms. Buckets are weighted by their requests.
func TestWarpRenderChartLatencySkipsEmptyBuckets(t *testing.T) {
	monday := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	fake := &fakeLogReader{latencyHistogramResult: &logstore.LatencyHistogramResult{Buckets: []logstore.LatencyHistogramBucket{
		{Timestamp: monday, AvgLatency: 200, TotalRequests: 100},
		{Timestamp: monday.Add(24 * time.Hour)},
		{Timestamp: monday.Add(2 * 24 * time.Hour), AvgLatency: 500, TotalRequests: 50},
	}}}
	deps := &ToolDeps{logManager: fake, charts: newChartRegistry()}
	_, err := runTool(t, RenderChartTool, deps, map[string]any{
		"kind": "line", "title": "Latency per day", "metric": "latency", "interval": "day",
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.NoError(t, err)
	spec, _ := deps.charts.get(spec1(t, deps))
	require.Equal(t, []ChartPoint{{X: "2026-09-14T00:00:00Z", Y: 200}, {X: "2026-09-16T00:00:00Z", Y: 500}}, spec.Points, "a bucket with no requests is left out, not drawn at 0 ms")

	deps = &ToolDeps{logManager: fake, charts: newChartRegistry()}
	_, err = runTool(t, RenderChartTool, deps, map[string]any{
		"kind": "bar", "title": "Latency per week", "metric": "latency", "interval": "week",
		"filters": map[string]any{"start_time": "-28d"},
	})
	require.NoError(t, err)
	spec, _ = deps.charts.get(spec1(t, deps))
	require.Equal(t, []ChartPoint{{X: "2026-09-14T00:00:00Z", Y: 300}}, spec.Points, "(100*200 + 50*500) / 150 requests")
}

// A group's failure rate is its failed requests over its requests, read from
// the same ranking with and without status error.
func TestWarpRenderChartErrorRateByModel(t *testing.T) {
	fake := &fakeLogReader{modelRankingResult: &logstore.ModelRankingResult{Rankings: []logstore.ModelRankingWithTrend{
		{ModelRankingEntry: logstore.ModelRankingEntry{Model: "claude-3-5-sonnet", TotalRequests: 100, SuccessRate: 70}},
		{ModelRankingEntry: logstore.ModelRankingEntry{Model: "gpt-4o-mini", TotalRequests: 60, SuccessRate: 95}},
	}}}
	deps := &ToolDeps{logManager: fake, charts: newChartRegistry()}
	_, err := runTool(t, RenderChartTool, deps, map[string]any{
		"kind": "bar", "title": "Failure rate by model", "metric": "error_rate", "group": "model",
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.NoError(t, err)
	spec, _ := deps.charts.get(spec1(t, deps))
	require.Equal(t, []ChartPoint{{X: "claude-3-5-sonnet", Y: 30}, {X: "gpt-4o-mini", Y: 5}}, spec.Points)
}

func spec1(t *testing.T, deps *ToolDeps) string {
	t.Helper()
	require.NotEmpty(t, deps.charts.order, "render_chart registered no chart")
	return deps.charts.order[0]
}

// expandChartBlocks reports how many blocks it dropped, so the agent can tell
// the model rather than leave a silent gap where a chart should have been.
func TestWarpExpandChartBlocksCountsWhatItDrops(t *testing.T) {
	charts := newChartRegistry()
	spec, err := charts.add(ChartSpec{Kind: "line", Title: "t", Metric: "requests", Unit: "count"})
	require.NoError(t, err)
	_, dropped := expandChartBlocksCounted("```warp-chart\n"+spec.ID+"\n```\n```warp-chart\n{\"id\":\"chart-2\",\"points\":[]}\n```", charts)
	require.Equal(t, 1, dropped)
}

// End to end: a hand-written chart block is dropped, and the model is told so
// once - here it then calls render_chart and pastes the real block.
func TestWarpAgentRedirectsAHandWrittenChart(t *testing.T) {
	day := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	var nudge string
	calls := 0
	chat := func(_ context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		calls++
		switch calls {
		case 1:
			return ToolTurn("c-1", "count_logs", `{"filters":{"start_time":"-7d"}}`), nil
		case 2:
			return TextTurn("Here:\n\n```warp-chart\n{\"id\":\"chart-2\",\"kind\":\"bar\",\"points\":[{\"x\":\"w1\",\"y\":34}]}\n```"), nil
		case 3:
			last := req.Input[len(req.Input)-1]
			if last.Content != nil && last.Content.ContentStr != nil {
				nudge = *last.Content.ContentStr
			}
			return ToolTurn("c-2", RenderChartTool, `{"kind":"bar","title":"Errors per week","metric":"errors","interval":"week","filters":{"start_time":"-7d"}}`), nil
		default:
			return TextTurn("```warp-chart\nchart-1\n```"), nil
		}
	}
	agent := newTestAgent(&scriptedModel{}, &fakeLogReader{histogramResult: &logstore.HistogramResult{Buckets: []logstore.HistogramBucket{{Timestamp: day, Count: 9, Error: 3}}}}, 8)
	agent.chat = chat
	events := collectEvents(t, agent, context.Background())

	require.Contains(t, nudge, "render_chart did not produce")
	var streamed strings.Builder
	for _, event := range events {
		if event.Type == EventDelta {
			streamed.WriteString(event.Delta)
		}
	}
	require.NotContains(t, streamed.String(), `"y":34`, "the hand-typed chart never streams")
	require.Contains(t, streamed.String(), `"interval":"week"`)
}

// A reply that was only a hand-written chart block expands to nothing. Once the
// one redirect is spent, it used to end the turn as an empty answer - a blank
// message with no reason. It now says the chart could not be drawn.
func TestWarpAgentAnswersWhenOnlyADroppedChartIsLeft(t *testing.T) {
	handWritten := TextTurn("```warp-chart\n{\"id\":\"chart-2\",\"kind\":\"bar\",\"points\":[{\"x\":\"w1\",\"y\":34}]}\n```")
	for name, maxIterations := range map[string]int{"redirect already spent": 8, "no iteration left to redirect": 2} {
		t.Run(name, func(t *testing.T) {
			model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{
				ToolTurn("c-1", "count_logs", `{"filters":{"start_time":"-7d"}}`),
				handWritten,
				handWritten,
			}}
			events := collectEvents(t, newTestAgent(model, &fakeLogReader{}, maxIterations), context.Background())

			var streamed strings.Builder
			for _, event := range events {
				if event.Type == EventDelta {
					streamed.WriteString(event.Delta)
				}
			}
			last := events[len(events)-1]
			require.Equal(t, EventDone, last.Type)
			require.Equal(t, droppedChartAnswer, strings.TrimSpace(streamed.String()), "the turn must not end as an empty answer")
			require.NotContains(t, streamed.String(), `"y":34`)
		})
	}
}

// A failures chart's "Open in Logs" opened every request in the window: the
// link was built from the chart's filters, and the error restriction only ever
// lived inside the query. For errors and error_rate it opens the failures.
func TestWarpRenderChartFailureLinksOpenTheFailures(t *testing.T) {
	for metric, wantErrors := range map[string]bool{"errors": true, "error_rate": true, "requests": false, "cost": false} {
		t.Run(metric, func(t *testing.T) {
			deps := &ToolDeps{logManager: &fakeLogReader{}, charts: newChartRegistry()}
			_, err := runTool(t, RenderChartTool, deps, map[string]any{
				"kind": "line", "title": "t", "metric": metric, "interval": "day",
				"filters": map[string]any{"start_time": "-30d"},
			})
			require.NoError(t, err)
			spec, _ := deps.charts.get(spec1(t, deps))
			require.Equal(t, wantErrors, strings.Contains(spec.Link, "status=error"), spec.Link)
		})
	}
}
