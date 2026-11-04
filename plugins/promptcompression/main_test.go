package promptcompression

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	compressChatFn = func([]schemas.ChatMessage, settings) []schemas.ChatMessage { panic("boom") }

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
		"nil resp":       nil,
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
	require.Len(t, lg.infos, 6)
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

// BenchmarkPreLLMHookFixtureChat replays the recorded 12-round agent session in Chat shape.
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
