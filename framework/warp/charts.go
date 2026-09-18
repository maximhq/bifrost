package warp

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/mcptools"
)

// RenderChartTool is the tool that draws a chart in an answer. It runs on
// Bifrost's MCP server (mcptools); Warp only registers what it returns, so an
// answer can only carry charts the tool produced this turn.
const RenderChartTool = mcptools.RenderChartToolName

// ChartFence is the fenced-block language a chart travels in. The model writes
// the block with only the id render_chart returned; expandChartBlocks swaps in
// the full spec before the text streams, so what the dashboard renders - and
// what history saves - is always data a tool read, never numbers the model
// typed. A block naming an id no tool issued this turn is dropped.
const ChartFence = "warp-chart"

// maxChartsPerTurn bounds the registry. A turn that wants more than this many
// charts is drawing the same thing repeatedly.
const maxChartsPerTurn = 6

// ChartSpec and ChartPoint are mcptools' shapes: render_chart returns them and
// the dashboard draws them.
type (
	ChartSpec  = mcptools.ChartSpec
	ChartPoint = mcptools.ChartPoint
)

// chartRegistry holds the charts render_chart produced this turn, by id. It
// lives on the Agent, which is built per turn, so ids never leak across turns.
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

// droppedChartRedirect is sent back once when an answer carried a chart block
// render_chart did not produce this turn.
const droppedChartRedirect = "That reply contained a warp-chart block that render_chart did not produce this turn, so it was removed and the person would have seen no chart. Chart data must come from render_chart - never write a chart block yourself or reuse one from an earlier answer. Call render_chart for the chart you meant (bars can run over time: kind bar with interval day or week), then paste the block it returns. If render_chart cannot draw it, answer without a chart and say in one sentence why."

// droppedChartAnswer is the answer when a dropped chart block was all a reply
// said and no step is left to redirect it: a blank answer gives no reason.
const droppedChartAnswer = "The chart for this answer could not be drawn from your logs, so it was left out. Ask again to have it drawn."

// renderChartDescription is what Warp declares render_chart with: the server's
// own description, plus how the chart reaches the answer - a block holding the
// id registerChart issued, which expandChartBlocks swaps for the spec.
const renderChartDescription = "Draw a chart in your answer from this deployment's data. The tool runs the query itself, so every point is real - you choose what to plot, not the numbers. " +
	"kind \"line\" plots a metric over time, one point per UTC hour, day or week (interval). kind \"bar\" does either: with interval, one bar per hour, day or week in time order; with group, one bar per provider, model, team, customer, app, key, error type or other query_usage_by dimension, largest first. Weeks are UTC weeks starting Monday. " +
	"error_rate is the percentage of requests that failed - use it for failure or error rates, and errors for counts. " +
	"The result carries the points, so you can describe the chart, and a block to paste into your answer exactly where the chart should appear. Write only that block for the chart: never chart code (Mermaid, Vega, ASCII art), never a warp-chart block you wrote yourself or copied from an earlier answer - those are removed - and never a table of the same numbers unless asked. " +
	"Only line and bar charts exist. For a pie or any other kind, draw a bar chart of the same numbers and say it is a bar chart."

// chartPasteNote is the note on a registered chart that has points.
const chartPasteNote = "Paste block, exactly as given, where the chart belongs in your answer. The dashboard draws the chart from it; do not repeat the points as a table unless asked."

// registerChart turns render_chart's result into what Warp's model is shown:
// the spec goes into this turn's registry, and the model gets its id and the
// block to paste instead of the spec itself. The model never handles the
// spec, so the chart the dashboard draws is always the one the tool read.
//
// A result that carries no chart - a refusal, or a server that predates the
// spec - is passed through unchanged. ok is false when the registry refused
// the chart, and text is then the refusal to hand the model.
func registerChart(text string, charts *chartRegistry) (string, bool) {
	var out map[string]any
	if err := sonic.UnmarshalString(text, &out); err != nil {
		return text, true
	}
	raw, present := out["chart"]
	if !present {
		return text, true
	}
	encoded, err := sonic.Marshal(raw)
	if err != nil {
		return text, true
	}
	var spec ChartSpec
	if err := sonic.Unmarshal(encoded, &spec); err != nil {
		return text, true
	}
	spec, err = charts.add(spec)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error()), false
	}
	delete(out, "chart")
	out["chart_id"] = spec.ID
	out["block"] = "```" + ChartFence + "\n" + spec.ID + "\n```"
	if _, noted := out["note"]; !noted {
		out["note"] = chartPasteNote
	}
	rewritten, err := sonic.MarshalString(out)
	if err != nil {
		return text, true
	}
	return rewritten, true
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
