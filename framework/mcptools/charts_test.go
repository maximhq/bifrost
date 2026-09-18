package mcptools

import (
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// renderChart runs render_chart and returns its result and the chart spec in
// it, as the tool built them.
func renderChart(t *testing.T, deps *Deps, args map[string]any) (map[string]any, ChartSpec) {
	t.Helper()
	out, err := runTool(t, RenderChartToolName, deps, args)
	require.NoError(t, err)
	result, ok := out.(map[string]any)
	require.True(t, ok, "result is %T", out)
	spec, ok := result["chart"].(ChartSpec)
	require.True(t, ok, "chart is %T", result["chart"])
	return result, spec
}

// Charts used to arrive as mermaid code the model wrote itself, numbers and
// all. render_chart reads the data, so a line chart's points are the
// histogram's buckets and the model supplies only what to plot. The spec comes
// back whole, with no id: a client that refers to charts by id assigns one.
func TestRenderChartLineReadsTheHistogram(t *testing.T) {
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	fake := &fakeLogReader{histogramResult: &logstore.HistogramResult{Buckets: []logstore.HistogramBucket{
		{Timestamp: day, Count: 40, Error: 5},
		{Timestamp: day.Add(24 * time.Hour), Count: 55, Error: 30},
	}}}
	result, spec := renderChart(t, &Deps{LogManager: fake}, map[string]any{
		"kind": "line", "title": "Errors per day", "metric": "errors", "interval": "day",
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.Equal(t, int64(86400), fake.histogramBucket)

	require.Empty(t, spec.ID)
	require.Equal(t, "line", spec.Kind)
	require.Equal(t, "Errors per day", spec.Title)
	require.Equal(t, "count", spec.Unit)
	require.Equal(t, "day", spec.Interval)
	want := []ChartPoint{{X: "2026-09-22T00:00:00Z", Y: 5}, {X: "2026-09-23T00:00:00Z", Y: 30}}
	require.Equal(t, want, spec.Points)
	require.Equal(t, want, result["points"], "the points ride beside the spec too")
	require.Equal(t, "count", result["unit"])
	require.Equal(t, "all", result["scope"])
	require.Equal(t, spec.Window, result["window"])
	require.NotEmpty(t, spec.Window)
	require.NotContains(t, result, "note", "a chart with points needs no empty-result note")
}

// An empty chart says so: drawn, it reads as a flat zero.
func TestRenderChartNotesAnEmptyResult(t *testing.T) {
	result, spec := renderChart(t, &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"kind": "line", "title": "Requests per day", "metric": "requests", "interval": "day",
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.Empty(t, spec.Points)
	require.Contains(t, result["note"], "Nothing matched these filters")
}

// A bar chart ranks, largest first, and stops at the limit.
func TestRenderChartBarRanksLargestFirst(t *testing.T) {
	fake := &fakeLogReader{modelRankingResult: &logstore.ModelRankingResult{Rankings: []logstore.ModelRankingWithTrend{
		{ModelRankingEntry: logstore.ModelRankingEntry{Model: "gpt-4o-mini", TotalCost: 0.02}},
		{ModelRankingEntry: logstore.ModelRankingEntry{Model: "claude-3-opus", TotalCost: 2.1}},
		{ModelRankingEntry: logstore.ModelRankingEntry{Model: "claude-3-5-sonnet", TotalCost: 0.4}},
	}}}
	_, spec := renderChart(t, &Deps{LogManager: fake}, map[string]any{
		"kind": "bar", "title": "Spend by model", "metric": "cost", "group": "model", "limit": float64(2),
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.Equal(t, "model", spec.Group)
	require.Equal(t, "usd", spec.Unit)
	require.Equal(t, []ChartPoint{{X: "claude-3-opus", Y: 2.1}, {X: "claude-3-5-sonnet", Y: 0.4}}, spec.Points)
}

// A cost chart ranked by the store's own order kept the busiest groups, not the
// costliest: the limit applied before sorting by cost. Every group is fetched,
// and the limit cuts the points only after they are sorted by the metric.
func TestRenderChartBarLimitsAfterSortingByMetric(t *testing.T) {
	fake := &fakeLogReader{modelRankingResult: &logstore.ModelRankingResult{}}
	renderChart(t, &Deps{LogManager: fake}, map[string]any{
		"kind": "bar", "title": "Spend by model", "metric": "cost", "group": "model", "limit": float64(2),
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.NotNil(t, fake.rankingFilters.RankingLimit)
	require.Zero(t, *fake.rankingFilters.RankingLimit, "the store ranks by requests, so a store-side limit drops the costliest groups")
}

// "Errors by team" counts failed requests per team exactly: the same ranking,
// narrowed to status error, rather than a success rate applied to a total.
func TestRenderChartErrorBarsCountFailedRequests(t *testing.T) {
	fake := &fakeLogReader{dimensionRankingResult: &logstore.DimensionRankingResult{Rankings: []logstore.DimensionRankingWithTrend{
		{DimensionRankingEntry: logstore.DimensionRankingEntry{ID: "team-platform", Name: "Platform Engineering", TotalRequests: 15}},
	}}}
	_, spec := renderChart(t, &Deps{LogManager: fake}, map[string]any{
		"kind": "bar", "title": "Errors by team", "metric": "errors", "group": "team",
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.Equal(t, []string{"error"}, fake.rankingFilters.Status)
	require.Equal(t, []ChartPoint{{X: "team-platform", Label: "Platform Engineering", Y: 15}}, spec.Points)
}

// Each wrong shape names the fix, so the model can correct itself in one step.
func TestRenderChartRejectsShapesItCannotDraw(t *testing.T) {
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
		"no title":                {map[string]any{"kind": "line", "title": "  ", "metric": "requests", "interval": "day"}, "title is required"},
		"hourly over thirty days": {map[string]any{"kind": "line", "title": "t", "metric": "requests", "interval": "hour", "filters": map[string]any{"start_time": "-30d"}}, `use interval "day"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := c.args["filters"]; !ok {
				c.args["filters"] = map[string]any{"start_time": "-7d"}
			}
			_, err := runTool(t, RenderChartToolName, &Deps{LogManager: &fakeLogReader{}}, c.args)
			require.ErrorContains(t, err, c.want)
		})
	}
}

// "Make it a bar chart with per-week errors" had no render_chart shape: bars
// only compared groups and there was no week interval, so the model counted
// each week with count_logs and typed its own chart. Bars now run over time
// too, and a week is a UTC ISO week built from daily buckets.
func TestRenderChartBarsPerWeek(t *testing.T) {
	monday := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	fake := &fakeLogReader{histogramResult: &logstore.HistogramResult{Buckets: []logstore.HistogramBucket{
		{Timestamp: monday, Count: 100, Error: 10},
		{Timestamp: monday.Add(3 * 24 * time.Hour), Count: 50, Error: 5},
		{Timestamp: monday.Add(7 * 24 * time.Hour), Count: 40, Error: 30},
	}}}
	_, spec := renderChart(t, &Deps{LogManager: fake}, map[string]any{
		"kind": "bar", "title": "Errors per week", "metric": "errors", "interval": "week",
		"filters": map[string]any{"start_time": "-28d"},
	})
	require.Equal(t, int64(86400), fake.histogramBucket, "weeks are summed from daily buckets")
	require.Equal(t, "week", spec.Interval)
	require.Equal(t, []ChartPoint{{X: "2026-09-14T00:00:00Z", Y: 15}, {X: "2026-09-21T00:00:00Z", Y: 30}}, spec.Points, "time bars keep time order")
}

// "Failure rates" came back as error counts: there was no rate to plot. A
// week's rate is its errors over its requests, not an average of daily rates.
func TestRenderChartErrorRateOverTime(t *testing.T) {
	monday := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	fake := &fakeLogReader{histogramResult: &logstore.HistogramResult{Buckets: []logstore.HistogramBucket{
		{Timestamp: monday, Count: 100, Error: 10},
		{Timestamp: monday.Add(24 * time.Hour), Count: 0},
		{Timestamp: monday.Add(3 * 24 * time.Hour), Count: 50, Error: 5},
	}}}
	_, spec := renderChart(t, &Deps{LogManager: fake}, map[string]any{
		"kind": "line", "title": "Failure rate per day", "metric": "error_rate", "interval": "day",
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.Equal(t, "percent", spec.Unit)
	require.Equal(t, []ChartPoint{{X: "2026-09-14T00:00:00Z", Y: 10}, {X: "2026-09-17T00:00:00Z", Y: 10}}, spec.Points, "a bucket with no requests has no rate and is left out")

	_, spec = renderChart(t, &Deps{LogManager: fake}, map[string]any{
		"kind": "bar", "title": "Failure rate per week", "metric": "error_rate", "interval": "week",
		"filters": map[string]any{"start_time": "-28d"},
	})
	require.Equal(t, []ChartPoint{{X: "2026-09-14T00:00:00Z", Y: 10}}, spec.Points, "15 errors over 150 requests")
}

// An empty bucket has no latency: it used to plot as 0 ms, and a week's average
// counted it as one request at 0 ms. Buckets are weighted by their requests.
func TestRenderChartLatencySkipsEmptyBuckets(t *testing.T) {
	monday := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	fake := &fakeLogReader{latencyHistogramResult: &logstore.LatencyHistogramResult{Buckets: []logstore.LatencyHistogramBucket{
		{Timestamp: monday, AvgLatency: 200, TotalRequests: 100},
		{Timestamp: monday.Add(24 * time.Hour)},
		{Timestamp: monday.Add(2 * 24 * time.Hour), AvgLatency: 500, TotalRequests: 50},
	}}}
	_, spec := renderChart(t, &Deps{LogManager: fake}, map[string]any{
		"kind": "line", "title": "Latency per day", "metric": "latency", "interval": "day",
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.Equal(t, []ChartPoint{{X: "2026-09-14T00:00:00Z", Y: 200}, {X: "2026-09-16T00:00:00Z", Y: 500}}, spec.Points, "a bucket with no requests is left out, not drawn at 0 ms")

	_, spec = renderChart(t, &Deps{LogManager: fake}, map[string]any{
		"kind": "bar", "title": "Latency per week", "metric": "latency", "interval": "week",
		"filters": map[string]any{"start_time": "-28d"},
	})
	require.Equal(t, []ChartPoint{{X: "2026-09-14T00:00:00Z", Y: 300}}, spec.Points, "(100*200 + 50*500) / 150 requests")
}

// A group's failure rate is its failed requests over its requests, read from
// the same ranking with and without status error.
func TestRenderChartErrorRateByModel(t *testing.T) {
	fake := &fakeLogReader{modelRankingResult: &logstore.ModelRankingResult{Rankings: []logstore.ModelRankingWithTrend{
		{ModelRankingEntry: logstore.ModelRankingEntry{Model: "claude-3-5-sonnet", TotalRequests: 100, SuccessRate: 70}},
		{ModelRankingEntry: logstore.ModelRankingEntry{Model: "gpt-4o-mini", TotalRequests: 60, SuccessRate: 95}},
	}}}
	_, spec := renderChart(t, &Deps{LogManager: fake}, map[string]any{
		"kind": "bar", "title": "Failure rate by model", "metric": "error_rate", "group": "model",
		"filters": map[string]any{"start_time": "-7d"},
	})
	require.Equal(t, []ChartPoint{{X: "claude-3-5-sonnet", Y: 30}, {X: "gpt-4o-mini", Y: 5}}, spec.Points)
}

// A failures chart's "Open in Logs" opened every request in the window: the
// link was built from the chart's filters, and the error restriction only ever
// lived inside the query. For errors and error_rate it opens the failures.
func TestRenderChartFailureLinksOpenTheFailures(t *testing.T) {
	for metric, wantErrors := range map[string]bool{"errors": true, "error_rate": true, "requests": false, "cost": false} {
		t.Run(metric, func(t *testing.T) {
			_, spec := renderChart(t, &Deps{LogManager: &fakeLogReader{}}, map[string]any{
				"kind": "line", "title": "t", "metric": metric, "interval": "day",
				"filters": map[string]any{"start_time": "-30d"},
			})
			require.Equal(t, wantErrors, strings.Contains(spec.Link, "status=error"), spec.Link)
		})
	}
}

// A chart's link opens what the chart counts: failures for a failures chart,
// every request otherwise - on the spec and on the result alike.
func TestRenderChartLinkPerMetric(t *testing.T) {
	failureMetrics := map[string]bool{"errors": true, "error_rate": true}
	shapes := map[string]map[string]any{
		"line":          {"kind": "line", "interval": "day"},
		"bars by time":  {"kind": "bar", "interval": "day"},
		"bars by group": {"kind": "bar", "group": "model"},
	}
	for _, metric := range chartMetrics {
		for shape, args := range shapes {
			t.Run(metric+"/"+shape, func(t *testing.T) {
				call := map[string]any{"metric": metric, "title": "t", "filters": matrixFilters(map[string]any{"providers": []any{"openai"}})}
				for key, value := range args {
					call[key] = value
				}
				result, spec := renderChart(t, &Deps{LogManager: &fakeLogReader{}}, call)
				want := map[string]string{"providers": "openai"}
				if failureMetrics[metric] {
					want["status"] = "error"
				}
				requireLogsLink(t, spec.Link, matrixQuery(want), "the chart's Open in Logs")
				requireLogsLink(t, result["logs_link"], matrixQuery(want), "the result's logs_link")
			})
		}
	}
}
