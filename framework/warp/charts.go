package warp

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/logstore"
)

// RenderChartTool is the tool that draws a chart in an answer.
const RenderChartTool = "render_chart"

// ChartFence is the fenced-block language a chart travels in. The model writes
// the block with only the id render_chart returned; expandChartBlocks swaps in
// the full spec before the text streams, so what the dashboard renders - and
// what history saves - is always data a tool read, never numbers the model
// typed. A block naming an id no tool issued this turn is dropped.
const ChartFence = "warp-chart"

// maxChartsPerTurn bounds the registry. A turn that wants more than this many
// charts is drawing the same thing repeatedly.
const maxChartsPerTurn = 6

// ChartPoint is one mark on a chart: a time bucket on a line, a category on a
// bar. Label is the display name when X is an id (a team id, a key id).
type ChartPoint struct {
	X     string  `json:"x"`
	Label string  `json:"label,omitempty"`
	Y     float64 `json:"y"`
}

// ChartSpec is everything the dashboard needs to draw a chart, and nothing it
// has to trust the model for.
type ChartSpec struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Title    string            `json:"title"`
	Metric   string            `json:"metric"`
	Unit     string            `json:"unit"`
	Interval string            `json:"interval,omitempty"`
	Group    string            `json:"group,omitempty"`
	Points   []ChartPoint      `json:"points"`
	Window   map[string]string `json:"window"`
	Link     string            `json:"link,omitempty"`
}

// chartRegistry holds the charts render_chart produced this turn, by id. It
// lives on ToolDeps, which is built per turn, so ids never leak across turns.
type chartRegistry struct {
	mu     sync.Mutex
	charts map[string]ChartSpec
	order  []string
}

func newChartRegistry() *chartRegistry {
	return &chartRegistry{charts: map[string]ChartSpec{}}
}

func (r *chartRegistry) add(spec ChartSpec) (ChartSpec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) >= maxChartsPerTurn {
		return ChartSpec{}, fmt.Errorf("no more than %d charts in one answer; reuse a chart you already made", maxChartsPerTurn)
	}
	spec.ID = fmt.Sprintf("chart-%d", len(r.order)+1)
	r.charts[spec.ID] = spec
	r.order = append(r.order, spec.ID)
	return spec, nil
}

func (r *chartRegistry) get(id string) (ChartSpec, bool) {
	if r == nil {
		return ChartSpec{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	spec, ok := r.charts[id]
	return spec, ok
}

// chartMetricUnits maps each chartable metric to the unit the dashboard formats
// it in.
var chartMetricUnits = map[string]string{
	"requests":   "count",
	"errors":     "count",
	"error_rate": "percent",
	"cost":       "usd",
	"tokens":     "tokens",
	"latency":    "ms",
}

var chartMetrics = []string{"requests", "errors", "error_rate", "cost", "tokens", "latency"}

// droppedChartRedirect is sent back once when an answer carried a chart block
// render_chart did not produce this turn.
const droppedChartRedirect = "That reply contained a warp-chart block that render_chart did not produce this turn, so it was removed and the person would have seen no chart. Chart data must come from render_chart - never write a chart block yourself or reuse one from an earlier answer. Call render_chart for the chart you meant (bars can run over time: kind bar with interval day or week), then paste the block it returns. If render_chart cannot draw it, answer without a chart and say in one sentence why."

// droppedChartAnswer is the answer when a dropped chart block was all a reply
// said and no step is left to redirect it: a blank answer gives no reason.
const droppedChartAnswer = "The chart for this answer could not be drawn from your logs, so it was left out. Ask again to have it drawn."

func renderChartTool() Tool {
	var dimensions []string
	for _, dim := range rankingDimensions {
		dimensions = append(dimensions, string(dim.value))
	}
	groups := append([]string{"provider", "model"}, dimensions...)

	return Tool{
		name: RenderChartTool,
		description: "Draw a chart in your answer from this deployment's data. The tool runs the query itself, so every point is real - you choose what to plot, not the numbers. " +
			"kind \"line\" plots a metric over time, one point per UTC hour, day or week (interval). kind \"bar\" does either: with interval, one bar per hour, day or week in time order; with group, one bar per provider, model, team, customer, app, key, error type or other query_usage_by dimension, largest first. Weeks are UTC weeks starting Monday. " +
			"error_rate is the percentage of requests that failed - use it for failure or error rates, and errors for counts. " +
			"The result carries the points, so you can describe the chart, and a block to paste into your answer exactly where the chart should appear. Write only that block for the chart: never chart code (Mermaid, Vega, ASCII art), never a warp-chart block you wrote yourself or copied from an earlier answer - those are removed - and never a table of the same numbers unless asked. " +
			"Only line and bar charts exist. For a pie or any other kind, draw a bar chart of the same numbers and say it is a bar chart.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "kind": {"type": "string", "enum": ["line", "bar"]},
    "title": {"type": "string", "minLength": 1, "maxLength": 120, "description": "Short title naming the metric and window, e.g. \"Errors per day, last 7 days\"."},
    "metric": {"type": "string", "enum": ["requests", "errors", "error_rate", "cost", "tokens", "latency"], "description": "error_rate is the percent of requests that failed; latency is the average in milliseconds."},
    "interval": {"type": "string", "enum": ["hour", "day", "week"], "description": "One point or bar per UTC hour, day or week (weeks start Monday). Required for line charts; for bar charts, use interval or group, not both. hour covers up to about 8 days, day and week up to 200 days."},
    "group": {"type": "string", "enum": [` + quotedJoin(groups) + `], "description": "Bar charts only, instead of interval: what each bar is. latency can be grouped by provider or model only."},
    "limit": {"type": "integer", "minimum": 2, "maximum": 20, "description": "Bar charts only: the most bars to draw, largest first. Defaults to 10."},
    "filters": ` + FilterSchema + `
  },
  "required": ["kind", "title", "metric", "filters"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			kind, err := enumArg(args, "kind", "", []string{"line", "bar"})
			if err != nil {
				return nil, err
			}
			if kind == "" {
				return nil, fmt.Errorf(`kind is required: "line" for a metric over time, "bar" to compare values of a group`)
			}
			metric, err := enumArg(args, "metric", "", chartMetrics)
			if err != nil {
				return nil, err
			}
			if metric == "" {
				return nil, fmt.Errorf("metric is required")
			}
			title, _ := args["title"].(string)
			title = strings.TrimSpace(title)
			if title == "" {
				return nil, fmt.Errorf("title is required")
			}
			if len(title) > 120 {
				title = truncateText(title, 120)
			}
			filters, err := filterArg(args, Now(), deps.scope)
			if err != nil {
				return nil, err
			}

			// A failures chart links to the failures. The error restriction lives
			// inside the queries below, so a link built from the chart's own
			// filters opened every request in the window.
			linkFilters := filters
			if metric == "errors" || metric == "error_rate" {
				failures := *filters
				failures.Status = []string{"error"}
				linkFilters = &failures
			}
			spec := ChartSpec{Kind: kind, Title: title, Metric: metric, Unit: chartMetricUnits[metric], Window: resolvedWindow(filters), Link: logsViewLink(linkFilters)}
			switch kind {
			case "line":
				if _, present := args["group"]; present {
					return nil, fmt.Errorf(`group is for bar charts; a line chart plots one metric over time`)
				}
				interval, err := enumArg(args, "interval", "", []string{"hour", "day", "week"})
				if err != nil {
					return nil, err
				}
				if interval == "" {
					return nil, fmt.Errorf(`a line chart needs interval "hour", "day" or "week"`)
				}
				spec.Interval = interval
				if spec.Points, err = timeSeriesPoints(ctx, deps, filters, metric, interval); err != nil {
					return nil, err
				}
			case "bar":
				interval, err := enumArg(args, "interval", "", []string{"hour", "day", "week"})
				if err != nil {
					return nil, err
				}
				group, err := enumArg(args, "group", "", groups)
				if err != nil {
					return nil, err
				}
				if interval != "" && group != "" {
					return nil, fmt.Errorf("a bar chart takes interval (bars over time) or group (bars per provider, model, team...), not both")
				}
				if interval != "" {
					// Bars over time: the same series a line chart plots, drawn as bars.
					spec.Interval = interval
					if spec.Points, err = timeSeriesPoints(ctx, deps, filters, metric, interval); err != nil {
						return nil, err
					}
					break
				}
				if group == "" {
					return nil, fmt.Errorf("a bar chart needs interval (bars over time) or group - what each bar is, e.g. provider, model or team")
				}
				limit, err := intArg(args, "limit", 10, MaxRankingRows)
				if err != nil {
					return nil, err
				}
				spec.Group = group
				if spec.Points, err = barChartPoints(ctx, deps, filters, metric, group, limit); err != nil {
					return nil, err
				}
			}

			if deps.charts == nil {
				deps.charts = newChartRegistry()
			}
			spec, err = deps.charts.add(spec)
			if err != nil {
				return nil, err
			}
			out := map[string]any{
				"chart_id": spec.ID,
				"block":    "```" + ChartFence + "\n" + spec.ID + "\n```",
				"points":   spec.Points,
				"unit":     spec.Unit,
				"scope":    scopeNote(filters, deps.scope),
				"window":   spec.Window,
				"note":     "Paste block, exactly as given, where the chart belongs in your answer. The dashboard draws the chart from it; do not repeat the points as a table unless asked.",
			}
			if len(spec.Points) == 0 {
				out["note"] = "Nothing matched these filters, so the chart is empty. Say so rather than pasting an empty chart; widen the window or check the filter values with describe_filter_space."
			}
			return setLogsLink(out, linkFilters), nil
		},
	}
}

// timeSeriesPoints reads one metric bucket by bucket - the same histograms
// query_metrics reads with interval. A week is summed from daily buckets into
// UTC ISO weeks (Monday start): the store's buckets are epoch-aligned, and a
// seven-day epoch bucket starts on a Thursday.
func timeSeriesPoints(ctx context.Context, deps *ToolDeps, filters *logstore.SearchFilters, metric, interval string) ([]ChartPoint, error) {
	bucket := int64(3600)
	if interval != "hour" {
		bucket = 86400
	}
	if span := filters.EndTime.Sub(*filters.StartTime).Seconds(); span/float64(bucket) > MaxHistogramBuckets {
		if interval == "hour" {
			return nil, fmt.Errorf(`interval "hour" over this range makes more than %d points; use interval "day" or a shorter range`, MaxHistogramBuckets)
		}
		return nil, fmt.Errorf("this range is longer than %d days; use a shorter range", MaxHistogramBuckets)
	}
	// A sample is one bucket's numbers before they are turned into the metric,
	// so a week can be summed first: a week's error rate is its errors over its
	// requests, and its latency is weighted by requests, never an average of
	// daily figures.
	type sample struct{ requests, errors, tokens, cost, latencyWeighted, latencyRequests float64 }
	var times []time.Time
	samples := map[time.Time]*sample{}
	at := func(ts time.Time) *sample {
		key := ts.UTC()
		if interval == "week" {
			day := key.Truncate(24 * time.Hour)
			key = day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
		}
		if samples[key] == nil {
			samples[key] = &sample{}
			times = append(times, key)
		}
		return samples[key]
	}
	switch metric {
	case "requests", "errors", "error_rate":
		result, err := deps.logManager.GetHistogram(ctx, filters, bucket)
		if err != nil {
			return nil, fmt.Errorf("request histogram failed: %w", err)
		}
		for _, b := range result.Buckets {
			s := at(b.Timestamp)
			s.requests += float64(b.Count)
			s.errors += float64(b.Error)
		}
	case "cost":
		result, err := deps.logManager.GetCostHistogram(ctx, filters, bucket)
		if err != nil {
			return nil, fmt.Errorf("cost histogram failed: %w", err)
		}
		for _, b := range result.Buckets {
			at(b.Timestamp).cost += b.TotalCost
		}
	case "tokens":
		result, err := deps.logManager.GetTokenHistogram(ctx, filters, bucket)
		if err != nil {
			return nil, fmt.Errorf("token histogram failed: %w", err)
		}
		for _, b := range result.Buckets {
			at(b.Timestamp).tokens += float64(b.TotalTokens)
		}
	case "latency":
		result, err := deps.logManager.GetLatencyHistogram(ctx, filters, bucket)
		if err != nil {
			return nil, fmt.Errorf("latency histogram failed: %w", err)
		}
		for _, b := range result.Buckets {
			// A bucket with no requests has no latency; counting it would pull
			// a week's average toward 0 ms.
			if b.TotalRequests <= 0 {
				continue
			}
			s := at(b.Timestamp)
			weight := float64(b.TotalRequests)
			s.latencyWeighted += b.AvgLatency * weight
			s.latencyRequests += weight
		}
	}
	points := make([]ChartPoint, 0, len(times))
	for _, ts := range times {
		s := samples[ts]
		var y float64
		switch metric {
		case "requests":
			y = s.requests
		case "errors":
			y = s.errors
		case "error_rate":
			// No requests, no rate: a zero there would read as "nothing failed".
			if s.requests == 0 {
				continue
			}
			y = roundTo(s.errors/s.requests*100, 2)
		case "cost":
			y = roundTo(s.cost, 6)
		case "tokens":
			y = s.tokens
		case "latency":
			// No requests, no latency: a zero there would read as instant.
			if s.latencyRequests == 0 {
				continue
			}
			y = roundTo(s.latencyWeighted/s.latencyRequests, 1)
		}
		points = append(points, ChartPoint{X: ts.Format(time.RFC3339), Y: y})
	}
	return points, nil
}

// barChartPoints reads one metric per value of a group, largest first. errors
// re-runs the same ranking over failed requests only, so each bar is an exact
// count rather than a rate applied to a total.
func barChartPoints(ctx context.Context, deps *ToolDeps, filters *logstore.SearchFilters, metric, group string, limit int) ([]ChartPoint, error) {
	scoped := *filters
	if metric == "errors" {
		scoped.Status = []string{"error"}
	}
	// The store ranks by requests, so a limit there would keep the busiest
	// groups rather than the top ones by this metric. Fetch every group (0 is
	// unlimited) and cut to limit after sorting.
	unlimited := 0
	scoped.RankingLimit = &unlimited
	// failureRate turns a success rate into the share that failed, for the
	// groups whose results carry one (providers and models).
	failureRate := func(requests int64, successRate float64) (float64, bool) {
		if requests == 0 {
			return 0, false
		}
		return roundTo(100-successRate, 2), true
	}
	value := func(requests, tokens int64, cost, latency float64) float64 {
		switch metric {
		case "cost":
			return roundTo(cost, 6)
		case "tokens":
			return float64(tokens)
		case "latency":
			return roundTo(latency, 1)
		default:
			return float64(requests)
		}
	}
	points := []ChartPoint{}
	switch group {
	case "provider":
		bucket, err := coarseBucketSize(&scoped)
		if err != nil {
			return nil, err
		}
		discovered, err := deps.logManager.GetProviderCostHistogram(ctx, &scoped, bucket)
		if err != nil {
			return nil, fmt.Errorf("provider lookup failed: %w", err)
		}
		totals, err := providerTotals(ctx, deps, &scoped, discovered.Providers)
		if err != nil {
			return nil, fmt.Errorf("per-provider totals failed: %w", err)
		}
		for name, t := range totals {
			if metric == "error_rate" {
				if rate, ok := failureRate(t.TotalRequests, t.SuccessRate); ok {
					points = append(points, ChartPoint{X: name, Y: rate})
				}
				continue
			}
			points = append(points, ChartPoint{X: name, Y: value(t.TotalRequests, t.TotalTokens, t.TotalCost, t.AverageLatency)})
		}
	case "model":
		result, err := deps.logManager.GetModelRankings(ctx, &scoped)
		if err != nil {
			return nil, fmt.Errorf("model rankings failed: %w", err)
		}
		for _, r := range result.Rankings {
			if metric == "error_rate" {
				if rate, ok := failureRate(r.TotalRequests, r.SuccessRate); ok {
					points = append(points, ChartPoint{X: r.Model, Y: rate})
				}
				continue
			}
			points = append(points, ChartPoint{X: r.Model, Y: value(r.TotalRequests, r.TotalTokens, r.TotalCost, r.AvgLatency)})
		}
	default:
		if metric == "latency" {
			return nil, fmt.Errorf("latency can only be grouped by provider or model")
		}
		dimension, ok := validRankingDimension(group)
		if !ok {
			return nil, fmt.Errorf("unknown group %q", group)
		}
		result, err := deps.logManager.GetDimensionRankings(ctx, &scoped, dimension)
		if err != nil {
			return nil, fmt.Errorf("%s rankings failed: %w", group, err)
		}
		// Dimension rankings carry no success rate, so a group's failure rate is
		// the same ranking over failed requests divided by the full one.
		var failed map[string]int64
		if metric == "error_rate" {
			errorsOnly := scoped
			errorsOnly.Status = []string{"error"}
			failedResult, err := deps.logManager.GetDimensionRankings(ctx, &errorsOnly, dimension)
			if err != nil {
				return nil, fmt.Errorf("%s error rankings failed: %w", group, err)
			}
			failed = map[string]int64{}
			for _, r := range failedResult.Rankings {
				failed[r.ID] = r.TotalRequests
			}
		}
		for _, r := range result.Rankings {
			label := r.Name
			if label == r.ID {
				label = ""
			}
			if metric == "error_rate" {
				if r.TotalRequests > 0 {
					points = append(points, ChartPoint{X: r.ID, Label: label, Y: roundTo(float64(failed[r.ID])/float64(r.TotalRequests)*100, 2)})
				}
				continue
			}
			points = append(points, ChartPoint{X: r.ID, Label: label, Y: value(r.TotalRequests, r.TotalTokens, r.TotalCost, 0)})
		}
	}
	sortPointsDescending(points)
	if len(points) > limit {
		points = points[:limit]
	}
	return points, nil
}

func sortPointsDescending(points []ChartPoint) {
	for i := 1; i < len(points); i++ {
		for j := i; j > 0 && (points[j].Y > points[j-1].Y || (points[j].Y == points[j-1].Y && points[j].X < points[j-1].X)); j-- {
			points[j], points[j-1] = points[j-1], points[j]
		}
	}
}

func roundTo(value float64, places int) float64 {
	scale := math.Pow(10, float64(places))
	return math.Round(value*scale) / scale
}

// chartBlock matches a fenced warp-chart block, whatever the model put in it.
var chartBlock = regexp.MustCompile("(?s)```" + ChartFence + "[ \t]*\n(.*?)```")

// expandChartBlocks replaces each warp-chart block's id with the spec
// render_chart stored for it, and drops any block whose content is not an id
// issued this turn - a spec the model wrote out itself, or an id it invented.
// A chart is data the tool read or it is nothing.
func expandChartBlocks(answer string, charts *chartRegistry) string {
	expanded, _ := expandChartBlocksCounted(answer, charts)
	return expanded
}

// expandChartBlocksCounted is expandChartBlocks that also reports how many
// blocks it dropped, so the agent can tell the model instead of leaving a gap.
func expandChartBlocksCounted(answer string, charts *chartRegistry) (string, int) {
	if !strings.Contains(answer, "```"+ChartFence) {
		return answer, 0
	}
	dropped := 0
	expanded := chartBlock.ReplaceAllStringFunc(answer, func(block string) string {
		body := strings.TrimSpace(chartBlock.FindStringSubmatch(block)[1])
		id := body
		// Tolerate {"id":"chart-1"}: the id is what matters, not how it was quoted.
		if strings.HasPrefix(body, "{") {
			var ref struct {
				ID string `json:"id"`
			}
			if sonic.UnmarshalString(body, &ref) == nil {
				id = ref.ID
			}
		}
		spec, ok := charts.get(id)
		if !ok {
			dropped++
			return ""
		}
		encoded, err := sonic.MarshalString(spec)
		if err != nil {
			dropped++
			return ""
		}
		return "```" + ChartFence + "\n" + encoded + "\n```"
	})
	return expanded, dropped
}
