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
	compressResponsesFn = func([]schemas.ResponsesMessage, settings) ([]schemas.ResponsesMessage, stats) { panic("boom") }

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

func TestPreLLMHookNoFlapOnHugeFreshOutput(t *testing.T) {
	p, _ := newHookPlugin(t)
	old, fresh := bigOutput(300), bigOutput(15000) // ~600 KB, age 0
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{
		Input: rounds(old, "x", fresh)}}
	out, _, err := p.PreLLMHook(hookCtx(), req)
	require.NoError(t, err)
	require.NotSame(t, req, out)
	require.Contains(t, strOf(out.ChatRequest.Input[3]), "tokens trimmed by promptcompression")
	require.Equal(t, fresh, strOf(out.ChatRequest.Input[7]))
}

func TestPreLLMHookSkipsResponsesAutoCache(t *testing.T) {
	p, _ := newHookPlugin(t)
	req := bigRespReq(schemas.ResponsesRequest)
	req.ResponsesRequest.Params = &schemas.ResponsesParameters{
		ExtraParams: map[string]interface{}{"cache_control": map[string]any{"type": "ephemeral"}}}
	out, _, err := p.PreLLMHook(hookCtx(), req)
	require.NoError(t, err)
	require.Same(t, req, out)

	// params without the key, or without a map, do not skip
	req = bigRespReq(schemas.ResponsesRequest)
	req.ResponsesRequest.Params = &schemas.ResponsesParameters{}
	out, _, err = p.PreLLMHook(hookCtx(), req)
	require.NoError(t, err)
	require.NotSame(t, req, out)
}

func TestPreLLMHookEnabledNoopZeroAllocs(t *testing.T) {
	p, _ := newHookPlugin(t)
	ctx := hookCtx()
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{
		Model: "m", Input: rounds("small")[:3]}}
	allocs := testing.AllocsPerRun(100, func() {
		if out, _, _ := p.PreLLMHook(ctx, req); out != req {
			t.Fatal("expected original request")
		}
	})
	require.Zero(t, allocs)
}

func TestPreLLMHookBelowEligibleThresholdReturnsOriginal(t *testing.T) {
	// A 50-line output trims by a few hundred tokens: real, but below a 10000-token threshold.
	c := 10000
	pl, err := Init(&Config{MinSavingsTokens: &c}, &capLogger{})
	require.NoError(t, err)
	p := pl.(*Plugin)
	in := rounds(bigOutput(50), "x", "y")
	_, st := compressChat(in, p.s)
	require.True(t, st.Changed, "precondition: something is trimmed")
	require.Less(t, st.EligibleBefore-st.EligibleAfter, c)

	ctx := hookCtx()
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Input: in}}
	out, _, err := p.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Same(t, req, out)
	require.Nil(t, ctx.Value(ContextKeyEstimatedBefore))

	// The same input with the threshold at the saved amount is compressed.
	c = st.EligibleBefore - st.EligibleAfter
	pl, err = Init(&Config{MinSavingsTokens: &c}, &capLogger{})
	require.NoError(t, err)
	out, _, err = pl.(*Plugin).PreLLMHook(hookCtx(), req)
	require.NoError(t, err)
	require.NotSame(t, req, out)
}

func TestPreLLMHookLogsAreSkippedWhenUnchanged(t *testing.T) {
	p, _ := newHookPlugin(t)
	ctx := hookCtx()
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-9")
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionStreamRequest, ChatRequest: &schemas.BifrostChatRequest{
		Input: rounds("small", "small", "small", "small")}}
	out, _, err := p.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Same(t, req, out)
	require.Nil(t, ctx.Value(ContextKeyEstimatedBefore))
	require.Nil(t, ctx.Value(ContextKeyEstimatedAfter))
	require.Nil(t, ctx.Value(contextKeyStream))
}

func TestPostLLMHookStreamResponsesUsage(t *testing.T) {
	p, lg := newHookPlugin(t)
	ctx := hookCtx()
	_, _, err := p.PreLLMHook(ctx, bigRespReq(schemas.ResponsesStreamRequest))
	require.NoError(t, err)
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	resp := &schemas.BifrostResponse{ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
		Response: &schemas.BifrostResponsesResponse{Usage: &schemas.ResponsesResponseUsage{
			InputTokens: 321, InputTokensDetails: &schemas.ResponsesResponseInputTokens{CachedReadTokens: 11}}}}}
	_, _, err = p.PostLLMHook(ctx, resp, nil)
	require.NoError(t, err)
	require.Len(t, lg.infos, 1)
	require.Contains(t, lg.infos[0], "prompt_tokens=321")
	require.Contains(t, lg.infos[0], "cached_read_tokens=11")

	// nil-safe at every level
	for _, r := range []*schemas.BifrostResponse{
		{ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{}},
		{ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{Response: &schemas.BifrostResponsesResponse{}}},
	} {
		_, _, err = p.PostLLMHook(ctxWithEstimates(1, 1), r, nil)
		require.NoError(t, err)
	}
}

func TestPostLLMHookLogsRequestID(t *testing.T) {
	p, lg := newHookPlugin(t)
	ctx := ctxWithEstimates(10, 5)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-1")
	_, _, err := p.PostLLMHook(ctx, &schemas.BifrostResponse{}, nil)
	require.NoError(t, err)
	require.Len(t, lg.infos, 1)
	require.Contains(t, lg.infos[0], "request_id=req-1 estimated_before=10")

	ctx = ctxWithEstimates(10, 5)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, 42) // not a string
	_, _, err = p.PostLLMHook(ctx, &schemas.BifrostResponse{}, nil)
	require.NoError(t, err)
	require.Contains(t, lg.infos[1], "request_id= estimated_before=10")
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

	out, _ := compressResponses(msgs, s)

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
		out, _ := compressResponses(prefix, s)
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

// ---- cross-turn prefix stability ----------------------------------------

// stabilityLevel runs one request prefix through the code under test and
// returns the JSON of every message of the result, plus whether the sent
// request was the compressed one.
type stabilityLevel struct {
	name  string
	chat  func(t *testing.T, prefix []schemas.ChatMessage) (out []string, compressed bool)
	respn func(t *testing.T, prefix []schemas.ResponsesMessage) (out []string, compressed bool)
}

func jsonItems[T any](t *testing.T, items []T) []string {
	t.Helper()
	out := make([]string, len(items))
	for i := range items {
		out[i] = mustJSON(t, items[i])
	}
	return out
}

var stabilityLevels = []stabilityLevel{
	{
		name: "compressor",
		chat: func(t *testing.T, prefix []schemas.ChatMessage) ([]string, bool) {
			out, st := compressChat(prefix, defaults(t))
			require.Len(t, out, len(prefix))
			return jsonItems(t, out), st.Changed
		},
		respn: func(t *testing.T, prefix []schemas.ResponsesMessage) ([]string, bool) {
			out, st := compressResponses(prefix, defaults(t))
			require.Len(t, out, len(prefix))
			return jsonItems(t, out), st.Changed
		},
	},
	{
		name: "hook",
		chat: func(t *testing.T, prefix []schemas.ChatMessage) ([]string, bool) {
			p, _ := newHookPlugin(t)
			req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{Model: "m", Input: prefix}}
			res, _, err := p.PreLLMHook(hookCtx(), req)
			require.NoError(t, err)
			require.Len(t, res.ChatRequest.Input, len(prefix))
			return jsonItems(t, res.ChatRequest.Input), res != req
		},
		respn: func(t *testing.T, prefix []schemas.ResponsesMessage) ([]string, bool) {
			p, _ := newHookPlugin(t)
			req := &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest,
				ResponsesRequest: &schemas.BifrostResponsesRequest{Model: "m", Input: prefix}}
			res, _, err := p.PreLLMHook(hookCtx(), req)
			require.NoError(t, err)
			require.Len(t, res.ResponsesRequest.Input, len(prefix))
			return jsonItems(t, res.ResponsesRequest.Input), res != req
		},
	},
}

// chatAgeIn is the number of assistant messages after index i, computed here
// independently of the compressor.
func chatAgeIn(msgs []schemas.ChatMessage, i int) int {
	n := 0
	for _, m := range msgs[i+1:] {
		if m.Role == schemas.ChatMessageRoleAssistant {
			n++
		}
	}
	return n
}

// respAgeIn is the number of assistant turns (maximal runs of assistant items,
// classified by respIsAssistantItem) after index i.
func respAgeIn(msgs []schemas.ResponsesMessage, i int) int {
	turns, prev := 0, false
	for j := i + 1; j < len(msgs); j++ {
		a := respIsAssistantItem(&msgs[j])
		if a && !prev {
			turns++
		}
		prev = a
	}
	return turns
}

// allowedChat returns the indices that may differ between the results for
// prefix a and the longer prefix b: tool outputs outside the protected prefix
// that were too young in a and old enough in b.
func allowedChat(a, b []schemas.ChatMessage, s settings) map[int]bool {
	set := map[int]bool{}
	lo := max(chatProtectedPrefix(a, s.ProtectedLeading), chatProtectedPrefix(b, s.ProtectedLeading))
	for i := lo; i < len(a); i++ {
		if a[i].Role == schemas.ChatMessageRoleTool && chatAgeIn(a, i) < s.MinAgeTurns && chatAgeIn(b, i) >= s.MinAgeTurns {
			set[i] = true
		}
	}
	return set
}

func allowedResp(a, b []schemas.ResponsesMessage, s settings) map[int]bool {
	set := map[int]bool{}
	lo := max(responsesProtectedPrefix(a, s.ProtectedLeading), responsesProtectedPrefix(b, s.ProtectedLeading))
	for i := lo; i < len(a); i++ {
		if respIsToolOutput(&a[i]) && respAgeIn(a, i) < s.MinAgeTurns && respAgeIn(b, i) >= s.MinAgeTurns {
			set[i] = true
		}
	}
	return set
}

func clip(s string) string {
	if len(s) > 160 {
		return fmt.Sprintf("%s ... (%d bytes)", s[:160], len(s))
	}
	return s
}

// checkStability walks consecutive pairs of per-turn results and reports every
// index that changed without being in that pair's allowed set. It returns how
// many allowed indices did change, so a caller can assert the test is not vacuous.
func checkStability(t *testing.T, outs [][]string, flags []bool, allowed []map[int]bool) (edits int) {
	t.Helper()
	for k := 0; k+1 < len(outs); k++ {
		for i := range outs[k] {
			if outs[k][i] == outs[k+1][i] {
				continue
			}
			if allowed[k][i] {
				edits++
				continue
			}
			t.Errorf("turn %d -> %d (compressed %v -> %v): index %d changed but did not become eligible\n  was: %s\n  now: %s",
				k+1, k+2, flags[k], flags[k+1], i, clip(outs[k][i]), clip(outs[k+1][i]))
		}
	}
	return edits
}

func runChatStability(t *testing.T, msgs []schemas.ChatMessage, wantEdits bool) {
	t.Helper()
	s := defaults(t)
	bounds := chatTurnBounds(msgs)
	require.Greater(t, len(bounds), 2)
	for _, lvl := range stabilityLevels {
		t.Run(lvl.name, func(t *testing.T) {
			outs := make([][]string, len(bounds))
			flags := make([]bool, len(bounds))
			for k, end := range bounds {
				outs[k], flags[k] = lvl.chat(t, msgs[:end])
			}
			allowed := make([]map[int]bool, len(bounds)-1)
			for k := range allowed {
				allowed[k] = allowedChat(msgs[:bounds[k]], msgs[:bounds[k+1]], s)
			}
			edits := checkStability(t, outs, flags, allowed)
			t.Logf("%d turns, %d eligibility-time edits, compressed per turn: %v", len(bounds), edits, flags)
			if wantEdits {
				require.Positive(t, edits, "vacuous: nothing was ever edited")
			}
		})
	}
}

func runRespStability(t *testing.T, msgs []schemas.ResponsesMessage, wantEdits bool) {
	t.Helper()
	s := defaults(t)
	bounds := responsesTurnBounds(msgs)
	require.Greater(t, len(bounds), 2)
	for _, lvl := range stabilityLevels {
		t.Run(lvl.name, func(t *testing.T) {
			outs := make([][]string, len(bounds))
			flags := make([]bool, len(bounds))
			for k, end := range bounds {
				outs[k], flags[k] = lvl.respn(t, msgs[:end])
			}
			allowed := make([]map[int]bool, len(bounds)-1)
			for k := range allowed {
				allowed[k] = allowedResp(msgs[:bounds[k]], msgs[:bounds[k+1]], s)
			}
			edits := checkStability(t, outs, flags, allowed)
			t.Logf("%d turns, %d eligibility-time edits, compressed per turn: %v", len(bounds), edits, flags)
			if wantEdits {
				require.Positive(t, edits, "vacuous: nothing was ever edited")
			}
		})
	}
}

// chatBase loads the fixture without its closing assistant answer, so a
// variant can keep appending rounds.
func chatBase(t *testing.T) []schemas.ChatMessage {
	t.Helper()
	var msgs []schemas.ChatMessage
	loadFixture(t, "testdata/agent_session_chat.json", &msgs)
	return msgs[:len(msgs)-1]
}

func respBase(t *testing.T) []schemas.ResponsesMessage {
	t.Helper()
	var msgs []schemas.ResponsesMessage
	loadFixture(t, "testdata/agent_session_responses.json", &msgs)
	return msgs[:len(msgs)-1]
}

func chatFinal(msgs []schemas.ChatMessage) []schemas.ChatMessage {
	return append(msgs, textMsg(schemas.ChatMessageRoleAssistant, "done"))
}

func respFinal(msgs []schemas.ResponsesMessage) []schemas.ResponsesMessage {
	return append(msgs, rmText(schemas.ResponsesInputMessageRoleAssistant, "done"))
}

// singleLine returns a large single-line text (no newlines, no repeated
// spaces) that cannot be trimmed by line count; seed keeps copies distinct.
func singleLine(seed, bytes int) string {
	var sb strings.Builder
	for i := 0; sb.Len() < bytes; i++ {
		fmt.Fprintf(&sb, "b%d-%05d;", seed, i)
	}
	return sb.String()
}

// chatDupVariant appends the read_file output of the first read twice more at later turns.
func chatDupVariant(t *testing.T) []schemas.ChatMessage {
	msgs := chatBase(t)
	dup := strOf(msgs[5])
	filler := func(k int) string { return fmt.Sprintf("filler output %d", k) }
	id := 100
	for _, text := range []string{filler(1), filler(2), dup, filler(3), filler(4), filler(5), dup, filler(6), filler(7)} {
		id++
		cid := fmt.Sprintf("call_%d", id)
		msgs = append(msgs, assistantCall(cid), toolMsg(cid, text))
	}
	return chatFinal(msgs)
}

func respDupVariant(t *testing.T) []schemas.ResponsesMessage {
	msgs := respBase(t)
	dup := rmOutStr(msgs[5])
	id := 100
	for k, text := range []string{"filler 1", "filler 2", dup, "filler 3", "filler 4", "filler 5", dup, "filler 6", "filler 7"} {
		_ = k
		id++
		cid := fmt.Sprintf("call_%d", id)
		msgs = append(msgs, rmCall(cid), rmOut(cid, text))
	}
	return respFinal(msgs)
}

// chatHugeVariant replaces the output that closes the request of turn 10 with ~600 KB.
func chatHugeVariant(t *testing.T) []schemas.ChatMessage {
	msgs := chatBase(t)
	msgs = append(msgs, assistantCall("call_900"), toolMsg("call_900", "ok"), assistantCall("call_901"), toolMsg("call_901", "ok"))
	idx := chatTurnBounds(msgs)[9] - 1
	require.Equal(t, schemas.ChatMessageRoleTool, msgs[idx].Role)
	huge := bigOutput(15000)
	msgs[idx] = toolMsg(*msgs[idx].ChatToolMessage.ToolCallID, huge)
	return chatFinal(msgs)
}

func respHugeVariant(t *testing.T) []schemas.ResponsesMessage {
	msgs := respBase(t)
	msgs = append(msgs, rmCall("call_900"), rmOut("call_900", "ok"), rmCall("call_901"), rmOut("call_901", "ok"))
	idx := responsesTurnBounds(msgs)[9] - 1
	require.True(t, respIsToolOutput(&msgs[idx]))
	msgs[idx] = rmOut(*msgs[idx].ResponsesToolMessage.CallID, bigOutput(15000))
	return respFinal(msgs)
}

// chatPressureVariant appends 8 rounds of large untrimmable single-line outputs.
func chatPressureVariant(t *testing.T) []schemas.ChatMessage {
	msgs := chatBase(t)
	for i := 0; i < 8; i++ {
		cid := fmt.Sprintf("call_%d", 200+i)
		msgs = append(msgs, assistantCall(cid), toolMsg(cid, singleLine(i, 8000)))
	}
	return chatFinal(msgs)
}

func respPressureVariant(t *testing.T) []schemas.ResponsesMessage {
	msgs := respBase(t)
	for i := 0; i < 8; i++ {
		cid := fmt.Sprintf("call_%d", 200+i)
		msgs = append(msgs, rmCall(cid), rmOut(cid, singleLine(i, 8000)))
	}
	return respFinal(msgs)
}

// The stability invariant: once an output has been edited it stays
// byte-identical on every later request; between consecutive requests only
// outputs that just became old enough may change.
func TestCrossTurnPrefixStabilityChat(t *testing.T) {
	t.Run("base", func(t *testing.T) { runChatStability(t, chatFinal(chatBase(t)), true) })
	t.Run("duplicates", func(t *testing.T) { runChatStability(t, chatDupVariant(t), true) })
	t.Run("huge_fresh_output", func(t *testing.T) { runChatStability(t, chatHugeVariant(t), true) })
	t.Run("threshold_pressure", func(t *testing.T) { runChatStability(t, chatPressureVariant(t), false) })
}

func TestCrossTurnPrefixStabilityResponses(t *testing.T) {
	t.Run("base", func(t *testing.T) { runRespStability(t, respFinal(respBase(t)), true) })
	t.Run("duplicates", func(t *testing.T) { runRespStability(t, respDupVariant(t), true) })
	t.Run("huge_fresh_output", func(t *testing.T) { runRespStability(t, respHugeVariant(t), true) })
	t.Run("threshold_pressure", func(t *testing.T) { runRespStability(t, respPressureVariant(t), false) })
}

// ---- long-history benchmarks --------------------------------------------

// largeHistoryChat builds a ~800 KB agent session of 100 rounds. Each output is
// distinct multi-line text so old ones get trimmed.
func largeHistoryChat() []schemas.ChatMessage {
	msgs := []schemas.ChatMessage{textMsg(schemas.ChatMessageRoleSystem, "sys"), textMsg(schemas.ChatMessageRoleUser, "go")}
	for r := 0; r < 100; r++ {
		cid := fmt.Sprintf("call_%d", r)
		var sb strings.Builder
		for l := 0; sb.Len() < 8000; l++ {
			fmt.Fprintf(&sb, "round %03d line %04d: some tool output content here\n", r, l)
		}
		msgs = append(msgs, assistantCall(cid), toolMsg(cid, sb.String()))
	}
	return msgs
}

func largeHistoryResponses() []schemas.ResponsesMessage {
	msgs := []schemas.ResponsesMessage{rmText(schemas.ResponsesInputMessageRoleSystem, "sys"), rmText(schemas.ResponsesInputMessageRoleUser, "go")}
	for r := 0; r < 100; r++ {
		cid := fmt.Sprintf("call_%d", r)
		var sb strings.Builder
		for l := 0; sb.Len() < 8000; l++ {
			fmt.Fprintf(&sb, "round %03d line %04d: some tool output content here\n", r, l)
		}
		msgs = append(msgs, rmCall(cid), rmOut(cid, sb.String()))
	}
	return msgs
}

// BenchmarkPreLLMHookLargeHistoryChat is the steady-state cost of a long
// agent loop: 100 rounds, ~800 KB, most outputs old enough to trim.
func BenchmarkPreLLMHookLargeHistoryChat(b *testing.B) {
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Model: "m", Input: largeHistoryChat()}}
	benchHook(b, benchPlugin(b), req)
}

// BenchmarkPreLLMHookLargeHistoryResponses is the Responses-shape counterpart.
func BenchmarkPreLLMHookLargeHistoryResponses(b *testing.B) {
	req := &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Model: "m", Input: largeHistoryResponses()}}
	benchHook(b, benchPlugin(b), req)
}

// BenchmarkPreLLMHookNoopEnabledChat is an enabled request with nothing
// eligible (every output is too young): it must stay allocation-free.
func BenchmarkPreLLMHookNoopEnabledChat(b *testing.B) {
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Model: "m", Input: rounds(bigOutput(200), bigOutput(200))}}
	benchHook(b, benchPlugin(b), req)
}

// longPressureRounds is enough untrimmable single-line rounds that a
// percentage threshold on the eligible region would eventually fall below 5%
// and flip the hook back to the original request (it did at about 74 rounds).
const longPressureRounds = 90

func chatLongPressureVariant(t *testing.T) []schemas.ChatMessage {
	msgs := chatBase(t)
	for i := 0; i < longPressureRounds; i++ {
		cid := fmt.Sprintf("call_%d", 200+i)
		msgs = append(msgs, assistantCall(cid), toolMsg(cid, singleLine(i, 8000)))
	}
	return chatFinal(msgs)
}

func respLongPressureVariant(t *testing.T) []schemas.ResponsesMessage {
	msgs := respBase(t)
	for i := 0; i < longPressureRounds; i++ {
		cid := fmt.Sprintf("call_%d", 200+i)
		msgs = append(msgs, rmCall(cid), rmOut(cid, singleLine(i, 8000)))
	}
	return respFinal(msgs)
}

// Hook level only: compression must never switch off once it is on, however
// much untrimmable output accumulates.
func TestHookStabilityLongThresholdPressureChat(t *testing.T) {
	msgs := chatLongPressureVariant(t)
	s := defaults(t)
	bounds := chatTurnBounds(msgs)
	outs := make([][]string, len(bounds))
	flags := make([]bool, len(bounds))
	for k, end := range bounds {
		outs[k], flags[k] = stabilityLevels[1].chat(t, msgs[:end])
	}
	allowed := make([]map[int]bool, len(bounds)-1)
	for k := range allowed {
		allowed[k] = allowedChat(msgs[:bounds[k]], msgs[:bounds[k+1]], s)
	}
	checkStability(t, outs, flags, allowed)
	require.True(t, flags[len(flags)-1], "hook must still compress after %d rounds", longPressureRounds)
}

func TestHookStabilityLongThresholdPressureResponses(t *testing.T) {
	msgs := respLongPressureVariant(t)
	s := defaults(t)
	bounds := responsesTurnBounds(msgs)
	outs := make([][]string, len(bounds))
	flags := make([]bool, len(bounds))
	for k, end := range bounds {
		outs[k], flags[k] = stabilityLevels[1].respn(t, msgs[:end])
	}
	allowed := make([]map[int]bool, len(bounds)-1)
	for k := range allowed {
		allowed[k] = allowedResp(msgs[:bounds[k]], msgs[:bounds[k+1]], s)
	}
	checkStability(t, outs, flags, allowed)
	require.True(t, flags[len(flags)-1], "hook must still compress after %d rounds", longPressureRounds)
}

// savedTokens runs the hook and returns the tokens it saved (0 when the
// original request was sent).
func savedTokens(t *testing.T, p *Plugin, req *schemas.BifrostRequest) int {
	t.Helper()
	ctx := hookCtx()
	_, _, err := p.PreLLMHook(ctx, req)
	require.NoError(t, err)
	b, ok := ctx.Value(ContextKeyEstimatedBefore).(int)
	if !ok {
		return 0
	}
	return b - ctx.Value(ContextKeyEstimatedAfter).(int)
}

// TestHookSavedTokensMonotone: edits are stable and the eligible set only
// grows, so the tokens saved never decrease from one request to the next.
func TestHookSavedTokensMonotone(t *testing.T) {
	p, _ := newHookPlugin(t)
	chat := chatFinal(chatBase(t))
	prev := 0
	for k, end := range chatTurnBounds(chat) {
		got := savedTokens(t, p, &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{Model: "m", Input: chat[:end]}})
		require.GreaterOrEqual(t, got, prev, "chat turn %d", k+1)
		prev = got
	}
	require.Positive(t, prev)
	resp := respFinal(respBase(t))
	prev = 0
	for k, end := range responsesTurnBounds(resp) {
		got := savedTokens(t, p, &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest,
			ResponsesRequest: &schemas.BifrostResponsesRequest{Model: "m", Input: resp[:end]}})
		require.GreaterOrEqual(t, got, prev, "responses turn %d", k+1)
		prev = got
	}
	require.Positive(t, prev)
}
