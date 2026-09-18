package warp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

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
	events := collectEvents(t, newTestAgent(t, model, fake, 8), context.Background())

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
	agent := newTestAgent(t, &scriptedModel{}, &fakeLogReader{histogramResult: &logstore.HistogramResult{Buckets: []logstore.HistogramBucket{{Timestamp: day, Count: 9, Error: 3}}}}, 8)
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
			events := collectEvents(t, newTestAgent(t, model, &fakeLogReader{}, maxIterations), context.Background())

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

// chartResultExecutor answers every tool call with result, the way the MCP
// server answers render_chart: plain text, the spec under "chart".
func chartResultExecutor(result string, calls *int) MCPExecutor {
	return func(_ *schemas.BifrostContext, _ *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
		*calls++
		return &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleTool,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(result)},
		}, nil
	}
}

// render_chart runs on the MCP server, which returns the spec it read. Warp
// keeps that spec in the turn's registry and hands the model only the id and
// the block to paste, so the chart the answer carries is always the one the
// tool read - the model never sees numbers it could retype.
func TestWarpRenderChartOverMCPRegistersTheSpec(t *testing.T) {
	const result = `{"chart":{"id":"server-side","kind":"line","title":"Requests per day","metric":"requests","unit":"count","interval":"day","points":[{"x":"2026-09-22T00:00:00Z","y":40}]},"points":[{"x":"2026-09-22T00:00:00Z","y":40}],"window":{"start":"a","end":"b"}}`
	calls := 0
	agent := newTestAgent(t, &scriptedModel{}, &fakeLogReader{}, 8)
	agent.mcp = chartResultExecutor(result, &calls)

	text, failed := agent.executeTool(context.Background(), RenderChartTool, `{"kind":"line"}`)
	require.False(t, failed, text)
	require.Equal(t, 1, calls)

	var out map[string]any
	require.NoError(t, sonic.UnmarshalString(text, &out))
	require.NotContains(t, out, "chart", "the spec stays in the registry; the model gets only its id")
	require.Equal(t, "chart-1", out["chart_id"], "the id is Warp's own, not whatever the server put on the spec")
	require.Equal(t, "```"+ChartFence+"\nchart-1\n```", out["block"])
	require.Equal(t, chartPasteNote, out["note"])
	require.Contains(t, out, "points", "the points stay, so the model can describe the chart")
	require.Contains(t, out, "window")

	spec, ok := agent.charts.get("chart-1")
	require.True(t, ok)
	require.Equal(t, "Requests per day", spec.Title)
	require.Equal(t, []ChartPoint{{X: "2026-09-22T00:00:00Z", Y: 40}}, spec.Points)

	expanded := expandChartBlocks("Look:\n\n"+out["block"].(string), agent.charts)
	require.Contains(t, expanded, `"title":"Requests per day"`, "the pasted block expands to the registered spec")
	require.Contains(t, expanded, `{"x":"2026-09-22T00:00:00Z","y":40}`)
}

// An empty chart keeps the server's own note - "nothing matched, say so" - not
// the paste instruction, which would tell the model to show an empty chart.
func TestWarpRenderChartOverMCPKeepsTheServersNote(t *testing.T) {
	calls := 0
	agent := newTestAgent(t, &scriptedModel{}, &fakeLogReader{}, 8)
	agent.mcp = chartResultExecutor(`{"chart":{"kind":"line","title":"t","points":[]},"points":[],"note":"Nothing matched these filters"}`, &calls)

	text, failed := agent.executeTool(context.Background(), mcpToolPrefix+RenderChartTool, `{}`)
	require.False(t, failed, text)
	require.Contains(t, text, "Nothing matched these filters")
	require.NotContains(t, text, chartPasteNote)
}

// A turn that has already drawn maxChartsPerTurn charts is refused the next
// one: the model is told why, the call is marked failed, and nothing is
// registered - so no block can name a chart that does not exist.
func TestWarpRenderChartOverMCPRefusesPastTheRegistryCap(t *testing.T) {
	calls := 0
	agent := newTestAgent(t, &scriptedModel{}, &fakeLogReader{}, 8)
	agent.mcp = chartResultExecutor(`{"chart":{"kind":"bar","title":"t","points":[{"x":"a","y":1}]}}`, &calls)
	for i := range maxChartsPerTurn {
		_, failed := agent.executeTool(context.Background(), RenderChartTool, `{}`)
		require.False(t, failed, "chart %d fits", i+1)
	}

	text, failed := agent.executeTool(context.Background(), RenderChartTool, `{}`)
	require.True(t, failed, "a refused chart is a failed call")
	require.Contains(t, text, "no more than")
	require.NotContains(t, text, "chart_id")
	_, ok := agent.charts.get(fmt.Sprintf("chart-%d", maxChartsPerTurn+1))
	require.False(t, ok)
}

// A failed render_chart is passed through untouched: there is no spec to
// register, and the server's refusal is what the model needs to read.
func TestWarpRenderChartOverMCPPassesRefusalsThrough(t *testing.T) {
	agent := newTestAgent(t, &scriptedModel{}, &fakeLogReader{}, 8)
	agent.mcp = func(*schemas.BifrostContext, *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
		return errorChatMessage(`a bar chart needs interval (bars over time) or group`), nil
	}
	text, failed := agent.executeTool(context.Background(), RenderChartTool, `{"kind":"bar"}`)
	require.True(t, failed)
	require.Contains(t, text, "a bar chart needs interval")
	_, ok := agent.charts.get("chart-1")
	require.False(t, ok)
}
