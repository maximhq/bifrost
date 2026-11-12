package heimdall

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// chain is a session of n tool turns and a final answer, n+1 rows in all.
func chain(sid string, n int) []*logstore.Log {
	var rows []*logstore.Log
	history := []schemas.ChatMessage{user("start")}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%d", i)
		out := assistantCalls(call(id, "check", fmt.Sprintf(`{"step":%d}`, i)))
		rows = append(rows, row(fmt.Sprintf("%s-%d", sid, i), sid, t0.Add(time.Duration(i)*time.Second), history, out))
		history = append(slices.Clone(history), out, toolResult(id, "ok"))
	}
	final := assistantText("done")
	return append(rows, row(fmt.Sprintf("%s-%d", sid, n), sid, t0.Add(time.Duration(n)*time.Second), history, final))
}

func TestEligibility_AllCodes(t *testing.T) {
	image := &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockTypeImage}}}
	type scenario struct {
		rows []*logstore.Log
		opts ReconOptions
		mcp  int
	}
	good := func() []*logstore.Log { return goodSession("s") }
	mutate := func(f func(rows []*logstore.Log)) scenario {
		rows := good()
		f(rows)
		return scenario{rows: rows}
	}

	cases := []struct {
		code string
		make func() scenario
	}{
		{CodeEligible, func() scenario { return scenario{rows: good()} }},
		{CodeUnknownSession, func() scenario { return scenario{} }},
		{CodeNoToolCalls, func() scenario {
			return scenario{rows: []*logstore.Log{row("s-1", "s", t0, []schemas.ChatMessage{user("hi")}, assistantText("hello"))}}
		}},
		{CodeContentHidden, func() scenario { return mutate(func(r []*logstore.Log) { r[1].ContentHidden = true }) }},
		{CodeIncomplete, func() scenario { return mutate(func(r []*logstore.Log) { r[1].Status = "error" }) }},
		{CodeUnsupportedAPI, func() scenario { return mutate(func(r []*logstore.Log) { r[0].Object = "responses" }) }},
		{CodeUnsupportedAPI, func() scenario { return mutate(func(r []*logstore.Log) { r[0].IsPassthrough = true }) }},
		{CodeMultiUserTurn, func() scenario {
			rows := good()
			h3 := append(slices.Clone(rows[1].InputHistoryParsed), *rows[1].OutputMessageParsed, user("and order o2?"))
			return scenario{rows: append(rows, row("s-3", "s", t0.Add(2*time.Second), h3, assistantText("sure")))}
		}},
		{CodeNonTextContent, func() scenario {
			return mutate(func(r []*logstore.Log) {
				for _, row := range r {
					row.InputHistoryParsed[0].Content = image
				}
			})
		}},
		{CodeNonTextContent, func() scenario {
			return mutate(func(r []*logstore.Log) { r[1].InputHistoryParsed[2].Content = image })
		}},
		{CodeLargePayload, func() scenario { return mutate(func(r []*logstore.Log) { r[1].IsLargePayloadResponse = true }) }},
		{CodeLargePayload, func() scenario { return mutate(func(r []*logstore.Log) { r[0].IsLargePayloadRequest = true }) }},
		{CodeHostedTool, func() scenario {
			return mutate(func(r []*logstore.Log) {
				for _, row := range r {
					row.ToolsParsed = append(row.ToolsParsed, schemas.ChatTool{Type: "web_search_20250305"})
				}
			})
		}},
		{CodeInjectedTool, func() scenario {
			s := scenario{rows: good()}
			s.opts.InjectedTools = func(p schemas.ModelProvider) bool { return p == "openai" }
			return s
		}},
		{CodeInjectedTool, func() scenario { return scenario{rows: good(), mcp: 2} }}, // two tool runs, one result in the history
		{CodeMixedRoute, func() scenario { return mutate(func(r []*logstore.Log) { r[1].Model = "other" }) }},
		{CodeRouteMismatch, func() scenario {
			return scenario{rows: good(), opts: ReconOptions{Baseline: &Route{Provider: "openai", Model: "baseline"}}}
		}},
		{CodeUnsupportedParams, func() scenario {
			return mutate(func(r []*logstore.Log) {
				for _, row := range r {
					row.ParamsParsed = map[string]any{"n": 2}
				}
			})
		}},
		{CodeToolsChanged, func() scenario {
			return mutate(func(r []*logstore.Log) { r[1].ToolsParsed = functionTools("check") })
		}},
		{CodeHistoryRewritten, func() scenario {
			return mutate(func(r []*logstore.Log) { r[1].InputHistoryParsed[0] = user("a different question") })
		}},
		{CodeUnpairedToolCall, func() scenario {
			return mutate(func(r []*logstore.Log) { r[1].InputHistoryParsed = r[1].InputHistoryParsed[:2] })
		}},
		{CodeTooLong, func() scenario { return scenario{rows: good(), opts: ReconOptions{MaxSteps: 1}} }},        // two steps
		{CodeTooLong, func() scenario { return scenario{rows: chain("s", 4), opts: ReconOptions{MaxSteps: 2}} }}, // five rows
	}

	seen := map[string]bool{}
	for i, tc := range cases {
		s := tc.make()
		f := newFakeLogs(s.rows...)
		f.mcpRuns["s"] = max(s.mcp, 1)
		if s.mcp == 0 {
			f.mcpRuns["s"] = 1 // the one tool the history shows
		}
		sid := "s"
		if tc.code == CodeUnknownSession {
			sid = "missing"
		}
		tr, elig, err := NewReconstructor(f, s.opts).Build(context.Background(), sid)
		if err != nil {
			t.Errorf("case %d (%s): %v", i, tc.code, err)
			continue
		}
		if elig.Code != tc.code {
			t.Errorf("case %d: code = %s (%s), want %s", i, elig.Code, elig.Message, tc.code)
		}
		if (tr != nil) != (tc.code == CodeEligible) {
			t.Errorf("case %d (%s): trace present = %v", i, tc.code, tr != nil)
		}
		if tc.code != CodeEligible && elig.Message == "" {
			t.Errorf("case %d (%s): no message", i, tc.code)
		}
		seen[tc.code] = true
	}

	// Every code of the design is covered.
	for _, code := range []string{
		CodeEligible, CodeUnknownSession, CodeNoToolCalls, CodeContentHidden, CodeIncomplete, CodeUnsupportedAPI,
		CodeMultiUserTurn, CodeNonTextContent, CodeLargePayload, CodeHostedTool, CodeInjectedTool, CodeMixedRoute,
		CodeRouteMismatch, CodeUnsupportedParams, CodeToolsChanged, CodeHistoryRewritten, CodeUnpairedToolCall, CodeTooLong,
	} {
		if !seen[code] {
			t.Errorf("no case for %s", code)
		}
	}
}

func TestEligibility_ContentHiddenNeverLoads(t *testing.T) {
	for _, hide := range []int{0, 1} {
		rows := goodSession("h")
		rows[hide].ContentHidden = true
		f := newFakeLogs(rows...)
		wantIneligible(t, f, "h", ReconOptions{}, CodeContentHidden)
		if n := f.totalFindByID(); n != 0 {
			t.Errorf("hidden row %d: FindByID was called %d times; a payload may have been read", hide, n)
		}
	}
	// Nor does a session that is ruled out for another reason load anything it does not need.
	rows := goodSession("x")
	rows[0].Object = "responses"
	f := newFakeLogs(rows...)
	wantIneligible(t, f, "x", ReconOptions{}, CodeUnsupportedAPI)
	if n := f.totalFindByID(); n != 0 {
		t.Errorf("an ineligible list row still led to %d FindByID calls", n)
	}
}

func TestEligibility_HostedToolTypes(t *testing.T) {
	for _, typ := range []schemas.ChatToolType{
		"web_search_20250305", "web_search_preview", "computer_20250124", "mcp_toolset", "code_execution_20250522", "custom",
	} {
		r := row("r", "s", t0, nil, assistantText("x"))
		r.ToolsParsed = append(functionTools("check"), schemas.ChatTool{Type: typ})
		if got := checkRow(r); got != CodeHostedTool {
			t.Errorf("tool type %q: checkRow = %q, want %s", typ, got, CodeHostedTool)
		}
	}
	// A function tool with no function body is not something a replay can offer either.
	r := row("r", "s", t0, nil, assistantText("x"))
	r.ToolsParsed = []schemas.ChatTool{{Type: schemas.ChatToolTypeFunction}}
	if got := checkRow(r); got != CodeHostedTool {
		t.Errorf("function tool without a function: checkRow = %q", got)
	}

	hostedParams := map[string]map[string]any{
		"mcp_servers":     {"mcp_servers": []any{map[string]any{"type": "url", "url": "https://x.example", "name": "x", "authorization_token": "secret"}}},
		"container":       {"container": "cont_1"},
		"web_search":      {"web_search_options": map[string]any{"search_context_size": "low"}},
		"server_side_use": {"include_server_side_tool_invocations": true},
	}
	for name, params := range hostedParams {
		r := row("r", "s", t0, nil, assistantText("x"))
		r.ParamsParsed = params
		if got := checkRow(r); got != CodeHostedTool {
			t.Errorf("%s: checkRow = %q, want %s", name, got, CodeHostedTool)
		}
	}
	r = row("r", "s", t0, nil, assistantText("x"))
	r.ParamsParsed = map[string]any{"include_server_side_tool_invocations": false}
	if got := checkRow(r); got != "" {
		t.Errorf("include_server_side_tool_invocations=false: checkRow = %q, want none", got)
	}

	// Tools declared only in the params still count.
	r = row("r", "s", t0, nil, assistantText("x"))
	r.ToolsParsed = nil
	r.ParamsParsed = map[string]any{"tools": []any{map[string]any{"type": "web_search_20250305"}}}
	if got := checkRow(r); got != CodeHostedTool {
		t.Errorf("hosted tool in params only: checkRow = %q", got)
	}
}

func TestEligibility_Params(t *testing.T) {
	unsupported := map[string]map[string]any{
		"n above one":    {"n": 2},
		"audio":          {"audio": map[string]any{"voice": "alloy", "format": "wav"}},
		"modalities":     {"modalities": []any{"text", "audio"}},
		"logit_bias":     {"logit_bias": map[string]any{"50256": -100}},
		"prediction":     {"prediction": map[string]any{"type": "content", "content": "x"}},
		"logprobs":       {"logprobs": true},
		"top_logprobs":   {"top_logprobs": 3},
		"speed":          {"speed": "fast"},
		"inference_geo":  {"inference_geo": "us"},
		"context mgmt":   {"context_management": map[string]any{"edits": []any{}}},
		"task_budget":    {"task_budget": map[string]any{"type": "tokens", "total": 1000}},
		"undecodable":    {"temperature": "hot"},
		"conflicting rs": {"reasoning": map[string]any{"effort": "low"}, "reasoning_effort": "high"},
	}
	for name, params := range unsupported {
		r := row("r", "s", t0, nil, assistantText("x"))
		r.ParamsParsed = params
		if got := checkRow(r); got != CodeUnsupportedParams {
			t.Errorf("%s: checkRow = %q, want %s", name, got, CodeUnsupportedParams)
		}
	}

	harmless := map[string]any{
		"n": 1, "logprobs": false, "store": true, "user": "u1", "metadata": map[string]any{"k": "v"},
		"safety_identifier": "s", "prompt_cache_key": "k", "prompt_cache_retention": "24h",
		"stream_options": map[string]any{"include_usage": true}, "service_tier": "auto", "temperature": 0.7,
		"top_p": 0.9, "stop": []any{"END"}, "seed": 7, "max_completion_tokens": 100,
	}
	r := row("r", "s", t0, nil, assistantText("x"))
	r.ParamsParsed = harmless
	if got := checkRow(r); got != "" {
		t.Errorf("harmless params: checkRow = %q, want none", got)
	}

	r.ParamsParsed = make(chan int) // cannot be encoded at all
	if got := checkRow(r); got != CodeUnsupportedParams {
		t.Errorf("unencodable params: checkRow = %q", got)
	}
}

func TestEligibility_ReplayParamsCarryOnlyTheAllowList(t *testing.T) {
	rows := goodSession("p")
	for _, r := range rows {
		r.ParamsParsed = map[string]any{
			"temperature": 0.3, "top_p": 0.8, "top_k": 40, "frequency_penalty": 0.1, "presence_penalty": 0.2,
			"max_completion_tokens": 256, "stop": []any{"END"}, "seed": 11, "parallel_tool_calls": false,
			"verbosity": "low", "response_format": map[string]any{"type": "json_object"},
			"tool_choice": "auto", "reasoning": map[string]any{"effort": "low"},
			// Dropped: no effect on behaviour.
			"store": true, "user": "u1", "metadata": map[string]any{"k": "v"}, "prompt_cache_key": "k",
		}
	}
	got := mustBuild(t, newFakeLogs(rows...), "p", ReconOptions{}).Params

	if got.Temperature == nil || *got.Temperature != 0.3 || got.TopP == nil || *got.TopP != 0.8 || got.TopK == nil || *got.TopK != 40 {
		t.Errorf("sampling params = %+v", got)
	}
	if got.FrequencyPenalty == nil || got.PresencePenalty == nil || got.MaxCompletionTokens == nil || *got.MaxCompletionTokens != 256 {
		t.Errorf("penalty or length params = %+v", got)
	}
	if !slices.Equal(got.Stop, []string{"END"}) || got.Seed == nil || *got.Seed != 11 {
		t.Errorf("stop or seed = %+v", got)
	}
	if got.ParallelToolCalls == nil || *got.ParallelToolCalls || got.Verbosity == nil || *got.Verbosity != "low" {
		t.Errorf("parallel_tool_calls or verbosity = %+v", got)
	}
	if got.ResponseFormat == nil || got.ToolChoice == nil || got.Reasoning == nil {
		t.Errorf("response_format, tool_choice or reasoning lost: %+v", got)
	}

	if !reflect.DeepEqual(replayParams(nil), ReplayParams{}) {
		t.Error("nil params must give the zero value")
	}
}
