package promptcompression

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// capLogger captures log lines so tests can assert on Warn/Info output.
type capLogger struct {
	mu    sync.Mutex
	infos []string
	warns []string
}

func (l *capLogger) add(dst *[]string, msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*dst = append(*dst, fmt.Sprintf(msg, args...))
}
func (l *capLogger) Debug(string, ...any)                                            {}
func (l *capLogger) Info(m string, a ...any)                                         { l.add(&l.infos, m, a...) }
func (l *capLogger) Warn(m string, a ...any)                                         { l.add(&l.warns, m, a...) }
func (l *capLogger) Error(string, ...any)                                            {}
func (l *capLogger) Fatal(string, ...any)                                            {}
func (l *capLogger) SetLevel(schemas.LogLevel)                                       {}
func (l *capLogger) SetOutputType(schemas.LoggerOutputType)                          {}
func (l *capLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder { return nil }

func newHookPlugin(t *testing.T) (*Plugin, *capLogger) {
	t.Helper()
	lg := &capLogger{}
	p, err := Init(nil, lg)
	require.NoError(t, err)
	return p.(*Plugin), lg
}

func hookCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}

func bigChatReq(rt schemas.RequestType) *schemas.BifrostRequest {
	in := rounds(bigOutput(400), bigOutput(400), bigOutput(400), bigOutput(400))
	return &schemas.BifrostRequest{RequestType: rt, ChatRequest: &schemas.BifrostChatRequest{Model: "m", Input: in}}
}

func bigRespReq(rt schemas.RequestType) *schemas.BifrostRequest {
	in := respRounds(bigOutput(400), bigOutput(400), bigOutput(400), bigOutput(400))
	return &schemas.BifrostRequest{RequestType: rt, ResponsesRequest: &schemas.BifrostResponsesRequest{Model: "m", Input: in}}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestPreLLMHookCompressesChat(t *testing.T) {
	p, _ := newHookPlugin(t)
	ctx := hookCtx()
	req := bigChatReq(schemas.ChatCompletionRequest)
	before := mustJSON(t, req.ChatRequest.Input)

	out, sc, err := p.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Nil(t, sc)
	require.NotSame(t, req, out)
	require.Equal(t, before, mustJSON(t, req.ChatRequest.Input), "incoming request mutated")
	require.NotEqual(t, before, mustJSON(t, out.ChatRequest.Input))
	require.Len(t, out.ChatRequest.Input, len(req.ChatRequest.Input))
	require.Equal(t, "m", out.ChatRequest.Model)

	b, ok := ctx.Value(ContextKeyEstimatedBefore).(int)
	require.True(t, ok)
	a, ok := ctx.Value(ContextKeyEstimatedAfter).(int)
	require.True(t, ok)
	require.Less(t, a, b)
}

func TestPreLLMHookCompressesResponses(t *testing.T) {
	p, _ := newHookPlugin(t)
	ctx := hookCtx()
	req := bigRespReq(schemas.ResponsesRequest)
	before := mustJSON(t, req.ResponsesRequest.Input)

	out, _, err := p.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.NotSame(t, req, out)
	require.Equal(t, before, mustJSON(t, req.ResponsesRequest.Input), "incoming request mutated")
	require.NotEqual(t, before, mustJSON(t, out.ResponsesRequest.Input))
	require.NotNil(t, ctx.Value(ContextKeyEstimatedBefore))
}

func TestPreLLMHookCompressesStream(t *testing.T) {
	p, _ := newHookPlugin(t)
	chat := bigChatReq(schemas.ChatCompletionStreamRequest)
	cb := mustJSON(t, chat.ChatRequest.Input)
	out, _, err := p.PreLLMHook(hookCtx(), chat)
	require.NoError(t, err)
	require.NotSame(t, chat, out)
	require.NotEqual(t, cb, mustJSON(t, out.ChatRequest.Input))
	require.Equal(t, cb, mustJSON(t, chat.ChatRequest.Input))

	resp := bigRespReq(schemas.ResponsesStreamRequest)
	rb := mustJSON(t, resp.ResponsesRequest.Input)
	out, _, err = p.PreLLMHook(hookCtx(), resp)
	require.NoError(t, err)
	require.NotSame(t, resp, out)
	require.NotEqual(t, rb, mustJSON(t, out.ResponsesRequest.Input))
	require.Equal(t, rb, mustJSON(t, resp.ResponsesRequest.Input))
}

func TestPreLLMHookSkipsOtherTypes(t *testing.T) {
	p, _ := newHookPlugin(t)
	req := &schemas.BifrostRequest{RequestType: schemas.EmbeddingRequest, ChatRequest: bigChatReq(schemas.ChatCompletionRequest).ChatRequest}
	out, sc, err := p.PreLLMHook(hookCtx(), req)
	require.NoError(t, err)
	require.Nil(t, sc)
	require.Same(t, req, out)
}

func TestPreLLMHookSkipsRawBody(t *testing.T) {
	p, _ := newHookPlugin(t)
	ctx := hookCtx()
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	req := bigChatReq(schemas.ChatCompletionRequest)
	out, _, err := p.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Same(t, req, out)
	require.Nil(t, ctx.Value(ContextKeyEstimatedBefore))
}

func TestPreLLMHookBelowMinSavings(t *testing.T) {
	p, _ := newHookPlugin(t)
	ctx := hookCtx()
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{
		Input: rounds("small", "small", "small", "small")}}
	out, _, err := p.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Same(t, req, out)
	require.Nil(t, ctx.Value(ContextKeyEstimatedBefore), "context values only when compressed request is used")
}

func TestPreLLMHookEmptyAndNilInputs(t *testing.T) {
	p, _ := newHookPlugin(t)
	for name, req := range map[string]*schemas.BifrostRequest{
		"nil chat":        {RequestType: schemas.ChatCompletionRequest},
		"nil responses":   {RequestType: schemas.ResponsesRequest},
		"empty chat":      {RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{}},
		"empty responses": {RequestType: schemas.ResponsesRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{}},
	} {
		t.Run(name, func(t *testing.T) {
			out, _, err := p.PreLLMHook(hookCtx(), req)
			require.NoError(t, err)
			require.Same(t, req, out)
		})
	}
}

func TestPreLLMHookZeroEstimateReturnsOriginal(t *testing.T) {
	p, _ := newHookPlugin(t)
	// A message with nil content estimates to 0 tokens; must not divide by zero.
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{
		Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}}}}
	out, _, err := p.PreLLMHook(hookCtx(), req)
	require.NoError(t, err)
	require.Same(t, req, out)
}

func TestPreLLMHookSkipsAutomaticCaching(t *testing.T) {
	p, _ := newHookPlugin(t)
	req := bigChatReq(schemas.ChatCompletionRequest)
	req.ChatRequest.Params = &schemas.ChatParameters{CacheControl: &schemas.CacheControl{}}
	out, _, err := p.PreLLMHook(hookCtx(), req)
	require.NoError(t, err)
	require.Same(t, req, out)
}

func TestPreLLMHookFailOpen(t *testing.T) {
	p, lg := newHookPlugin(t)
	orig := compressChatFn
	t.Cleanup(func() { compressChatFn = orig })
	compressChatFn = func([]schemas.ChatMessage, settings) ([]schemas.ChatMessage, stats) { panic("boom") }

	ctx := hookCtx()
	req := bigChatReq(schemas.ChatCompletionRequest)
	out, sc, err := p.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Nil(t, sc)
	require.Same(t, req, out)
	require.Nil(t, ctx.Value(ContextKeyEstimatedBefore))
	require.Len(t, lg.warns, 1)
}

func TestPreLLMHookFailOpenResponses(t *testing.T) {
	p, lg := newHookPlugin(t)
	orig := compressResponsesFn
	t.Cleanup(func() { compressResponsesFn = orig })
	compressResponsesFn = func([]schemas.ResponsesMessage, settings) []schemas.ResponsesMessage { panic("boom") }

	req := bigRespReq(schemas.ResponsesRequest)
	out, _, err := p.PreLLMHook(hookCtx(), req)
	require.NoError(t, err)
	require.Same(t, req, out)
	require.Len(t, lg.warns, 1)
}

func ctxWithEstimates(b, a int) *schemas.BifrostContext {
	ctx := hookCtx()
	ctx.SetValue(ContextKeyEstimatedBefore, b)
	ctx.SetValue(ContextKeyEstimatedAfter, a)
	return ctx
}

func TestPostLLMHookLogsChat(t *testing.T) {
	p, lg := newHookPlugin(t)
	resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Usage: &schemas.BifrostLLMUsage{
		PromptTokens: 123, PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedReadTokens: 45}}}}
	berr := &schemas.BifrostError{}
	gotResp, gotErr, err := p.PostLLMHook(ctxWithEstimates(1000, 600), resp, berr)
	require.NoError(t, err)
	require.Same(t, resp, gotResp)
	require.Same(t, berr, gotErr)
	require.Len(t, lg.infos, 1)
	for _, want := range []string{"estimated_before=1000", "estimated_after=600", "prompt_tokens=123", "cached_read_tokens=45"} {
		require.Contains(t, lg.infos[0], want)
	}
}

func TestPostLLMHookLogsResponses(t *testing.T) {
	p, lg := newHookPlugin(t)
	resp := &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Usage: &schemas.ResponsesResponseUsage{
		InputTokens: 77, InputTokensDetails: &schemas.ResponsesResponseInputTokens{CachedReadTokens: 9}}}}
	_, _, err := p.PostLLMHook(ctxWithEstimates(10, 5), resp, nil)
	require.NoError(t, err)
	require.Len(t, lg.infos, 1)
	require.Contains(t, lg.infos[0], "estimated_before=10")
	require.Contains(t, lg.infos[0], "prompt_tokens=77")
	require.Contains(t, lg.infos[0], "cached_read_tokens=9")
	require.NotContains(t, lg.infos[0], "%!")
}

func TestPostLLMHookNilUsage(t *testing.T) {
	p, lg := newHookPlugin(t)
	for name, resp := range map[string]*schemas.BifrostResponse{
		"empty":          {},
		"chat no usage":  {ChatResponse: &schemas.BifrostChatResponse{}},
		"chat no detail": {ChatResponse: &schemas.BifrostChatResponse{Usage: &schemas.BifrostLLMUsage{PromptTokens: 3}}},
		"resp no usage":  {ResponsesResponse: &schemas.BifrostResponsesResponse{}},
		"resp no detail": {ResponsesResponse: &schemas.BifrostResponsesResponse{Usage: &schemas.ResponsesResponseUsage{InputTokens: 4}}},
	} {
		t.Run(name, func(t *testing.T) {
			got, _, err := p.PostLLMHook(ctxWithEstimates(10, 5), resp, nil)
			require.NoError(t, err)
			require.Equal(t, resp, got)
		})
	}
	require.Len(t, lg.infos, 5)
}

func TestPostLLMHookNoStaleValuesAcrossRequests(t *testing.T) {
	p, lg := newHookPlugin(t)
	ctx := hookCtx() // core reuses one ctx across MCP agent-loop iterations
	resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Usage: &schemas.BifrostLLMUsage{PromptTokens: 7}}}

	_, _, err := p.PreLLMHook(ctx, bigChatReq(schemas.ChatCompletionRequest))
	require.NoError(t, err)
	_, _, err = p.PostLLMHook(ctx, resp, nil)
	require.NoError(t, err)
	require.Len(t, lg.infos, 1)

	small := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{
		Input: rounds("small", "small", "small", "small")}}
	out, _, err := p.PreLLMHook(ctx, small)
	require.NoError(t, err)
	require.Same(t, small, out)
	_, _, err = p.PostLLMHook(ctx, resp, nil)
	require.NoError(t, err)
	require.Len(t, lg.infos, 1, "skipped request must not log the previous request's estimates")
}

func TestPostLLMHookErrorOnlyAttemptDoesNotLog(t *testing.T) {
	p, lg := newHookPlugin(t)
	ctx := hookCtx()
	_, _, err := p.PreLLMHook(ctx, bigChatReq(schemas.ChatCompletionRequest))
	require.NoError(t, err)

	berr := &schemas.BifrostError{}
	_, gotErr, err := p.PostLLMHook(ctx, nil, berr)
	require.NoError(t, err)
	require.Same(t, berr, gotErr)
	require.Empty(t, lg.infos, "error-only attempt logs nothing")

	// the fallback attempt succeeds on the same ctx
	resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Usage: &schemas.BifrostLLMUsage{PromptTokens: 9}}}
	_, _, err = p.PostLLMHook(ctx, resp, nil)
	require.NoError(t, err)
	require.Len(t, lg.infos, 1)
	require.Contains(t, lg.infos[0], "prompt_tokens=9")
}

func TestPreLLMHookSkippedFallbackClearsEarlierEstimates(t *testing.T) {
	p, lg := newHookPlugin(t)
	ctx := hookCtx()
	_, _, err := p.PreLLMHook(ctx, bigChatReq(schemas.ChatCompletionRequest))
	require.NoError(t, err)
	_, _, err = p.PostLLMHook(ctx, nil, &schemas.BifrostError{}) // first attempt fails
	require.NoError(t, err)

	// the fallback attempt is not compressed (too little to save): nothing may leak
	small := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{
		Input: rounds("small", "small", "small", "small")}}
	_, _, err = p.PreLLMHook(ctx, small)
	require.NoError(t, err)
	_, _, err = p.PostLLMHook(ctx, &schemas.BifrostResponse{}, nil)
	require.NoError(t, err)
	require.Empty(t, lg.infos)
}

func TestPreLLMHookSkipsLargePayloadMode(t *testing.T) {
	p, _ := newHookPlugin(t)
	ctx := hookCtx()
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
	req := bigChatReq(schemas.ChatCompletionRequest)
	out, _, err := p.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Same(t, req, out)
	require.Nil(t, ctx.Value(ContextKeyEstimatedBefore))
}

func TestPostLLMHookWithoutEstimatesIsSilent(t *testing.T) {
	p, lg := newHookPlugin(t)
	resp := &schemas.BifrostResponse{}
	got, _, err := p.PostLLMHook(hookCtx(), resp, nil)
	require.NoError(t, err)
	require.Same(t, resp, got)
	require.Empty(t, lg.infos)
}

func TestPostLLMHookStreamLogsOnceOnFinalChunk(t *testing.T) {
	p, lg := newHookPlugin(t)
	ctx := hookCtx()
	req := bigChatReq(schemas.ChatCompletionStreamRequest)
	_, _, err := p.PreLLMHook(ctx, req)
	require.NoError(t, err)

	chunk := func(prompt int) *schemas.BifrostResponse {
		return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Usage: &schemas.BifrostLLMUsage{PromptTokens: prompt}}}
	}
	for _, c := range []struct {
		final  bool
		prompt int
	}{{false, 0}, {false, 0}, {true, 321}} {
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, c.final)
		_, _, err := p.PostLLMHook(ctx, chunk(c.prompt), nil)
		require.NoError(t, err)
	}
	require.Len(t, lg.infos, 1)
	require.Contains(t, lg.infos[0], "prompt_tokens=321")
	require.Contains(t, lg.infos[0], "estimated_before=")
}

// benchHook runs PreLLMHook b.N times against one request and context. The
// plugin never writes to the incoming request, so reuse is safe.
func benchHook(b *testing.B, p *Plugin, req *schemas.BifrostRequest) {
	b.Helper()
	ctx := hookCtx()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := p.PreLLMHook(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

func benchPlugin(b *testing.B) *Plugin {
	b.Helper()
	p, err := Init(nil, &capLogger{})
	require.NoError(b, err)
	return p.(*Plugin)
}

// BenchmarkPreLLMHookDisabledPath is the common-case cost: a request type the
// plugin does not touch (embedding) must add no allocations.
func BenchmarkPreLLMHookDisabledPath(b *testing.B) {
	req := &schemas.BifrostRequest{RequestType: schemas.EmbeddingRequest}
	benchHook(b, benchPlugin(b), req)
}

// BenchmarkPreLLMHookSmallChat is a short chat with no large tool outputs: the
// compressor runs, finds too little to save and the original request is returned.
func BenchmarkPreLLMHookSmallChat(b *testing.B) {
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{
		Model: "m", Input: rounds("small")[:3]}}
	benchHook(b, benchPlugin(b), req)
}

// BenchmarkPreLLMHookFixtureChat replays the synthetic 12-round agent session in Chat shape.
func BenchmarkPreLLMHookFixtureChat(b *testing.B) {
	data, err := os.ReadFile("testdata/agent_session_chat.json")
	require.NoError(b, err)
	var msgs []schemas.ChatMessage
	require.NoError(b, json.Unmarshal(data, &msgs))
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Model: "m", Input: msgs}}
	benchHook(b, benchPlugin(b), req)
}

// BenchmarkPreLLMHookFixtureResponses is the Responses-shape counterpart.
func BenchmarkPreLLMHookFixtureResponses(b *testing.B) {
	data, err := os.ReadFile("testdata/agent_session_responses.json")
	require.NoError(b, err)
	var msgs []schemas.ResponsesMessage
	require.NoError(b, json.Unmarshal(data, &msgs))
	req := &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{Model: "m", Input: msgs}}
	benchHook(b, benchPlugin(b), req)
}

// maxFixtureRatio is the headline target: the replayed agent session must
// shrink to at most 70% of its estimated tokens (at least 30% saved).
const maxFixtureRatio = 0.70

func loadFixture(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, v))
}

// TestFixtureChatSavings replays a synthetic 12-round coding-agent session in
// Chat Completions shape and checks the default settings save at least 30%.
func TestFixtureChatSavings(t *testing.T) {
	var msgs []schemas.ChatMessage
	loadFixture(t, "testdata/agent_session_chat.json", &msgs)
	require.NotEmpty(t, msgs)

	s := defaults(t)
	inBefore, err := json.Marshal(msgs)
	require.NoError(t, err)

	out, _ := compressChat(msgs, s)

	inAfter, err := json.Marshal(msgs)
	require.NoError(t, err)
	require.Equal(t, string(inBefore), string(inAfter), "input mutated")
	assertChatInvariants(t, msgs, out, chatProtectedPrefix(msgs, s.ProtectedLeading))

	before, after := estimateChat(msgs), estimateChat(out)
	t.Logf("chat fixture: %d -> %d tokens, saved %.1f%%", before, after, 100*float64(before-after)/float64(before))
	require.LessOrEqual(t, float64(after), maxFixtureRatio*float64(before))
}

// TestFixtureResponsesSavings is the Responses-shape counterpart.
func TestFixtureResponsesSavings(t *testing.T) {
	var msgs []schemas.ResponsesMessage
	loadFixture(t, "testdata/agent_session_responses.json", &msgs)
	require.NotEmpty(t, msgs)

	s := defaults(t)
	inBefore, err := json.Marshal(msgs)
	require.NoError(t, err)

	out := compressResponses(msgs, s)

	inAfter, err := json.Marshal(msgs)
	require.NoError(t, err)
	require.Equal(t, string(inBefore), string(inAfter), "input mutated")
	assertResponsesInvariants(t, msgs, out, responsesProtectedPrefix(msgs, s.ProtectedLeading))

	before, after := estimateResponses(msgs), estimateResponses(out)
	t.Logf("responses fixture: %d -> %d tokens, saved %.1f%%", before, after, 100*float64(before-after)/float64(before))
	require.LessOrEqual(t, float64(after), maxFixtureRatio*float64(before))
}

// chatTurnBounds returns, for each assistant message, the index of that
// message: the request for turn k is msgs[:bounds[k]], as a real agent loop
// would send it just before the model answers.
func chatTurnBounds(msgs []schemas.ChatMessage) []int {
	var b []int
	for i, m := range msgs {
		if m.Role == schemas.ChatMessageRoleAssistant {
			b = append(b, i)
		}
	}
	return b
}

// responsesTurnBounds is the Responses counterpart: a turn starts at an
// assistant message or at the first function_call of a parallel run.
func responsesTurnBounds(msgs []schemas.ResponsesMessage) []int {
	var b []int
	for i, m := range msgs {
		isCall := m.Type != nil && *m.Type == schemas.ResponsesMessageTypeFunctionCall
		isAssistant := m.Role != nil && *m.Role == schemas.ResponsesInputMessageRoleAssistant
		if !isCall && !isAssistant {
			continue
		}
		prevCall := i > 0 && msgs[i-1].Type != nil && *msgs[i-1].Type == schemas.ResponsesMessageTypeFunctionCall
		if isCall && prevCall {
			continue
		}
		b = append(b, i)
	}
	return b
}

type turnRow struct{ turn, messages, before, after int }

func (r turnRow) pct() float64 {
	if r.before == 0 {
		return 0
	}
	return 100 * float64(r.before-r.after) / float64(r.before)
}

// TestFixturePerTurnEstimates computes the per-turn estimated token cost of
// each request prefix with and without compression (default settings). When
// PROMPTCOMPRESSION_WRITE_ESTIMATES=1 it writes benchmark/estimates.md.
func TestFixturePerTurnEstimates(t *testing.T) {
	s := defaults(t)

	var chat []schemas.ChatMessage
	loadFixture(t, "testdata/agent_session_chat.json", &chat)
	var chatRows []turnRow
	for k, end := range chatTurnBounds(chat) {
		prefix := chat[:end]
		out, _ := compressChat(prefix, s)
		require.Len(t, out, len(prefix), "chat turn %d: message count changed", k+1)
		r := turnRow{k + 1, len(prefix), estimateChat(prefix), estimateChat(out)}
		require.LessOrEqual(t, r.after, r.before, "chat turn %d", k+1)
		t.Logf("chat turn %2d: msgs=%2d before=%5d after=%5d saved=%5.1f%%", r.turn, r.messages, r.before, r.after, r.pct())
		chatRows = append(chatRows, r)
	}
	require.NotEmpty(t, chatRows)

	var resp []schemas.ResponsesMessage
	loadFixture(t, "testdata/agent_session_responses.json", &resp)
	var respRows []turnRow
	for k, end := range responsesTurnBounds(resp) {
		prefix := resp[:end]
		out := compressResponses(prefix, s)
		require.Len(t, out, len(prefix), "responses turn %d: item count changed", k+1)
		r := turnRow{k + 1, len(prefix), estimateResponses(prefix), estimateResponses(out)}
		require.LessOrEqual(t, r.after, r.before, "responses turn %d", k+1)
		t.Logf("responses turn %2d: items=%2d before=%5d after=%5d saved=%5.1f%%", r.turn, r.messages, r.before, r.after, r.pct())
		respRows = append(respRows, r)
	}
	require.NotEmpty(t, respRows)

	if os.Getenv("PROMPTCOMPRESSION_WRITE_ESTIMATES") != "1" {
		return
	}
	var sb strings.Builder
	sb.WriteString("# Per-turn token estimates\n\n")
	sb.WriteString("Estimates: chars÷4 on the synthetic fixture, not provider-reported usage; cache effects not modelled.\n\n")
	section := func(title, unit string, rows []turnRow) {
		var tb, ta int
		var sum float64
		fmt.Fprintf(&sb, "## %s\n\n| Turn | %s | Estimated tokens before | Estimated tokens after | %% saved |\n|---:|---:|---:|---:|---:|\n", title, unit)
		for _, r := range rows {
			fmt.Fprintf(&sb, "| %d | %d | %d | %d | %.1f%% |\n", r.turn, r.messages, r.before, r.after, r.pct())
			tb += r.before
			ta += r.after
			sum += r.pct()
		}
		fmt.Fprintf(&sb, "| **Total** | | **%d** | **%d** | **%.1f%%** |\n\n", tb, ta, 100*float64(tb-ta)/float64(tb))
		fmt.Fprintf(&sb, "Mean per-turn %% saved: **%.1f%%**\n\n", sum/float64(len(rows)))
	}
	section("Chat Completions", "Messages", chatRows)
	section("Responses", "Items", respRows)
	require.NoError(t, os.MkdirAll("benchmark", 0o755))
	require.NoError(t, os.WriteFile("benchmark/estimates.md", []byte(sb.String()), 0o644))
}
