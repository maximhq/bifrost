package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// normalizeCachedUsage must fold cached read/write tokens into the top-level
// prompt + total counters, matching the per-request totals Anthropic reports
// only at the end of a stream. This is the value billed when a stream is
// cancelled mid-flight.
func TestNormalizeCachedUsage_FoldsCacheIntoPromptAndTotal(t *testing.T) {
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  3,
			CachedWriteTokens: 7,
		},
	}
	normalizeCachedUsage(usage)
	if usage.PromptTokens != 20 {
		t.Fatalf("PromptTokens = %d, want 20 (10 + 3 + 7)", usage.PromptTokens)
	}
	if usage.TotalTokens != 25 {
		t.Fatalf("TotalTokens = %d, want 25 (15 + 3 + 7)", usage.TotalTokens)
	}
	if usage.CompletionTokens != 5 {
		t.Fatalf("CompletionTokens = %d, want 5 (unchanged)", usage.CompletionTokens)
	}
}

func TestNormalizeCachedUsage_NoCacheDetailsIsNoOp(t *testing.T) {
	usage := &schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 15}
	normalizeCachedUsage(usage)
	if usage.PromptTokens != 10 || usage.TotalTokens != 15 {
		t.Fatalf("unexpected mutation: prompt=%d total=%d", usage.PromptTokens, usage.TotalTokens)
	}
}

func TestNormalizeCachedUsage_NilSafe(t *testing.T) {
	normalizeCachedUsage(nil) // must not panic
}

func TestAccumulateAnthropicResponsesUsage_MirrorsCacheIntoBilledUsage(t *testing.T) {
	responseUsage := &schemas.ResponsesResponseUsage{}
	billedUsage := &schemas.BifrostLLMUsage{}
	upstreamUsage := &AnthropicUsage{
		InputTokens:              2,
		CacheReadInputTokens:     1300,
		CacheCreationInputTokens: 78456,
		CacheCreation: AnthropicUsageCacheCreation{
			Ephemeral1hInputTokens: 78456,
		},
		OutputTokens: 3,
	}

	accumulateResponsesUsageFromStruct(responseUsage, billedUsage, upstreamUsage)

	if responseUsage.InputTokens != 2 || responseUsage.OutputTokens != 3 || responseUsage.TotalTokens != 5 {
		t.Fatalf("unexpected response usage before final normalization: %+v", responseUsage)
	}
	if responseUsage.InputTokensDetails == nil {
		t.Fatal("expected response cache details")
	}
	if responseUsage.InputTokensDetails.CachedReadTokens != 1300 {
		t.Fatalf("response cached read = %d, want 1300", responseUsage.InputTokensDetails.CachedReadTokens)
	}
	if responseUsage.InputTokensDetails.CachedWriteTokens != 78456 {
		t.Fatalf("response cached write = %d, want 78456", responseUsage.InputTokensDetails.CachedWriteTokens)
	}
	if responseUsage.InputTokensDetails.CachedWriteTokenDetails == nil ||
		responseUsage.InputTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h != 78456 {
		t.Fatalf("response cached write details = %+v, want 1h=78456", responseUsage.InputTokensDetails.CachedWriteTokenDetails)
	}

	if billedUsage.PromptTokens != 2 || billedUsage.CompletionTokens != 3 || billedUsage.TotalTokens != 5 {
		t.Fatalf("unexpected billed usage before normalization: %+v", billedUsage)
	}
	if billedUsage.PromptTokensDetails == nil {
		t.Fatal("expected billed cache details")
	}
	if billedUsage.PromptTokensDetails.CachedReadTokens != 1300 {
		t.Fatalf("billed cached read = %d, want 1300", billedUsage.PromptTokensDetails.CachedReadTokens)
	}
	if billedUsage.PromptTokensDetails.CachedWriteTokens != 78456 {
		t.Fatalf("billed cached write = %d, want 78456", billedUsage.PromptTokensDetails.CachedWriteTokens)
	}
	if billedUsage.PromptTokensDetails.CachedWriteTokenDetails == nil ||
		billedUsage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h != 78456 {
		t.Fatalf("billed cached write details = %+v, want 1h=78456", billedUsage.PromptTokensDetails.CachedWriteTokenDetails)
	}

	normalizeCachedUsage(billedUsage)
	if billedUsage.PromptTokens != 79758 {
		t.Fatalf("normalized billed prompt = %d, want 79758", billedUsage.PromptTokens)
	}
	if billedUsage.TotalTokens != 79761 {
		t.Fatalf("normalized billed total = %d, want 79761", billedUsage.TotalTokens)
	}
}

// An idle timeout fails the read without cancelling the context, so the
// deferred normalization never runs before the error post-hook reads the usage
// handle. Both stream handlers must fold cached tokens in before they send the
// error, or a cancelled-usage estimate bills the uncached prompt only.
func TestStreamIdleTimeoutNormalizesCachedUsage(t *testing.T) {
	const messageStart = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_idle","type":"message","role":"assistant","model":"claude-repro","content":[],"usage":{"input_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}` + "\n\n"
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(messageStart))
		w.(http.Flusher).Flush()
		select { // go silent until the client gives up or the test ends
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	defer server.Close()
	defer close(stop) // runs first, so Close does not wait on the stalled handler
	key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}
	responsesRequest := &schemas.BifrostResponsesRequest{Provider: schemas.Anthropic, Model: "claude-repro", Input: []schemas.ResponsesMessage{{
		Type: new(schemas.ResponsesMessageTypeMessage), Role: new(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: new("hi")},
	}}}
	for name, open := range map[string]func(*schemas.BifrostContext, schemas.PostHookRunner) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError){
		"chat": func(ctx *schemas.BifrostContext, hook schemas.PostHookRunner) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			return newTruncationTestProvider(server.URL).ChatCompletionStream(ctx, hook, nil, key, truncationChatRequest())
		},
		"responses": func(ctx *schemas.BifrostContext, hook schemas.PostHookRunner) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			return newTruncationTestProvider(server.URL).ResponsesStream(ctx, hook, nil, key, responsesRequest)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, 300*time.Millisecond)
			var atError *schemas.BifrostLLMUsage
			hook := func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				if err != nil {
					if u, ok := ctx.Value(schemas.BifrostContextKeyStreamAccumulatedUsage).(*schemas.BifrostLLMUsage); ok && u != nil {
						atError = u.DeepCopy()
					}
				}
				return resp, err
			}
			stream, bifrostErr := open(ctx, hook)
			if bifrostErr != nil {
				t.Fatalf("stream setup failed: %v", bifrostErr.GetErrorString())
			}
			collectTruncationChunks(t, stream)
			if atError == nil {
				t.Fatal("the error post-hook saw no usage handle")
			}
			if atError.PromptTokens != 125 || atError.TotalTokens != atError.PromptTokens+atError.CompletionTokens {
				t.Fatalf("usage at the idle-timeout error = prompt %d total %d, want prompt 125 (5 + 100 cached read + 20 cached write) and total = prompt + completion %d",
					atError.PromptTokens, atError.TotalTokens, atError.CompletionTokens)
			}
		})
	}
}

// A stream that goes silent after message_delta has final provider usage, so no
// estimate replaces it; the idle-timeout error must still carry that usage as
// BilledUsage, or governance and logging bill nothing for tokens Claude produced.
func TestStreamIdleTimeoutAfterMessageDeltaBillsReportedUsage(t *testing.T) {
	const body = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_idle_delta","type":"message","role":"assistant","model":"claude-repro","content":[],"usage":{"input_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial answer"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":7}}` + "\n\n"
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
		w.(http.Flusher).Flush()
		select { // go silent until the client gives up or the test ends
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	defer server.Close()
	defer close(stop) // runs first, so Close does not wait on the stalled handler
	key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}
	for name, responses := range map[string]bool{"chat": false, "responses": true} {
		t.Run(name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, 300*time.Millisecond)
			var billed *schemas.BifrostLLMUsage
			sawError := false
			hook := func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				if err != nil {
					sawError = true
					billed = err.ExtraFields.BilledUsage.DeepCopy()
				}
				return resp, err
			}
			provider := newTruncationTestProvider(server.URL)
			var stream chan *schemas.BifrostStreamChunk
			var bifrostErr *schemas.BifrostError
			if responses {
				stream, bifrostErr = provider.ResponsesStream(ctx, hook, nil, key, truncationChatRequest().ToResponsesRequest())
			} else {
				stream, bifrostErr = provider.ChatCompletionStream(ctx, hook, nil, key, truncationChatRequest())
			}
			if bifrostErr != nil {
				t.Fatalf("stream setup failed: %v", bifrostErr.GetErrorString())
			}
			collectTruncationChunks(t, stream)
			if !sawError {
				t.Fatal("the stream never reached the error post-hook")
			}
			if billed == nil {
				t.Fatal("the idle-timeout error carries no billed usage although the provider reported final usage")
			}
			if billed.PromptTokens != 125 || billed.CompletionTokens != 7 || billed.TotalTokens != 132 || billed.Estimation != nil {
				t.Fatalf("billed usage = prompt %d completion %d total %d estimation %+v, want prompt 125 (5 + 120 cached), completion 7, total 132, not estimated",
					billed.PromptTokens, billed.CompletionTokens, billed.TotalTokens, billed.Estimation)
			}
		})
	}
}

// accumulateResponsesUsageFromStruct drives accumulateAnthropicResponsesUsage
// from a typed usage struct. A struct cannot say which keys the wire carried, so
// a non-zero prompt counter counts as reported and a zero one as omitted, which
// is what these struct-built fixtures mean. Tests that have the raw frame use
// reportedAnthropicStreamPromptCounters instead, as the handler does.
func accumulateResponsesUsageFromStruct(usage *schemas.ResponsesResponseUsage, billed *schemas.BifrostLLMUsage, u *AnthropicUsage) {
	var reported anthropicPromptCounterBit
	if u != nil {
		if u.InputTokens != 0 {
			reported |= anthropicPromptCounterInputTokens
		}
		if u.CacheReadInputTokens != 0 {
			reported |= anthropicPromptCounterCacheReadInputTokens
		}
		if u.CacheCreationInputTokens != 0 {
			reported |= anthropicPromptCounterCacheCreationInputTokens
		}
		if u.CacheCreation.Ephemeral5mInputTokens != 0 || u.CacheCreation.Ephemeral1hInputTokens != 0 {
			reported |= anthropicPromptCounterCacheCreation
		}
	}
	accumulateAnthropicResponsesUsage(usage, billed, u, reported)
}

// streamPromptUsageCase is one upstream split of the prompt-side usage across
// message_start and message_delta, with the prompt Bifrost must bill for it.
type streamPromptUsageCase struct {
	name        string
	startUsage  string // message_start message.usage object
	deltaUsage  string // message_delta usage object
	prompt      int    // billed prompt tokens, cache folded in
	cachedRead  int
	cachedWrite int
	write1h     int
	completion  int
}

// streamPromptUsageCases covers both ways upstreams split the prompt side. The
// Anthropic shape (message_start carries the uncached input plus the cache
// counters, message_delta only output_tokens) must keep message_start's figures;
// the kimi-code shape (message_start carries the whole prompt with zero cache
// counters, message_delta re-reports the split) must take message_delta's. The
// first case is #5354: keeping the larger input_tokens and folding cache_read on
// top billed 40756.
var streamPromptUsageCases = []streamPromptUsageCase{
	{
		name:       "delta re-reports the cache split (kimi-code)",
		startUsage: `{"input_tokens":20532,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":1}`,
		deltaUsage: `{"input_tokens":308,"cache_read_input_tokens":20224,"cache_creation_input_tokens":0,"output_tokens":300}`,
		prompt:     20532, cachedRead: 20224, completion: 300,
	},
	{
		name:       "delta carries only output_tokens (Anthropic) with a cache read",
		startUsage: `{"input_tokens":10,"cache_read_input_tokens":20000,"cache_creation_input_tokens":0,"output_tokens":1}`,
		deltaUsage: `{"output_tokens":300}`,
		prompt:     20010, cachedRead: 20000, completion: 300,
	},
	{
		name:       "delta carries only output_tokens on a full cache hit",
		startUsage: `{"input_tokens":0,"cache_read_input_tokens":20000,"cache_creation_input_tokens":0,"output_tokens":1}`,
		deltaUsage: `{"output_tokens":300}`,
		prompt:     20000, cachedRead: 20000, completion: 300,
	},
	{
		name:       "delta carries only output_tokens with a 1h cache write",
		startUsage: `{"input_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":20000,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":20000},"output_tokens":1}`,
		deltaUsage: `{"output_tokens":300}`,
		prompt:     20010, cachedWrite: 20000, write1h: 20000, completion: 300,
	},
	{
		name:       "delta repeats message_start's prompt counters",
		startUsage: `{"input_tokens":10,"cache_read_input_tokens":20000,"cache_creation_input_tokens":50,"output_tokens":1}`,
		deltaUsage: `{"input_tokens":10,"cache_read_input_tokens":20000,"cache_creation_input_tokens":50,"output_tokens":300}`,
		prompt:     20060, cachedRead: 20000, cachedWrite: 50, completion: 300,
	},
	{
		name:       "delta reports null prompt counters",
		startUsage: `{"input_tokens":10,"cache_read_input_tokens":20000,"cache_creation_input_tokens":0,"output_tokens":1}`,
		deltaUsage: `{"input_tokens":null,"cache_read_input_tokens":null,"cache_creation_input_tokens":null,"output_tokens":300}`,
		prompt:     20010, cachedRead: 20000, completion: 300,
	},
	{
		name:       "delta carries only output_tokens and advisor iterations",
		startUsage: `{"input_tokens":10,"cache_read_input_tokens":20000,"cache_creation_input_tokens":0,"output_tokens":1}`,
		deltaUsage: `{"output_tokens":300,"iterations":[{"type":"message","input_tokens":10,"output_tokens":120},{"type":"advisor_message","model":"claude-opus-4-6","input_tokens":900,"output_tokens":80}]}`,
		prompt:     20010, cachedRead: 20000, completion: 300,
	},
	{
		name:       "delta reports the full split plus a compaction pass",
		startUsage: `{"input_tokens":10,"cache_read_input_tokens":20000,"cache_creation_input_tokens":0,"output_tokens":1}`,
		deltaUsage: `{"input_tokens":10,"cache_read_input_tokens":20000,"cache_creation_input_tokens":0,"output_tokens":300,"iterations":[{"type":"compaction","input_tokens":500,"cache_read_input_tokens":100,"output_tokens":50},{"type":"message","input_tokens":10,"cache_read_input_tokens":20000,"output_tokens":250}]}`,
		prompt:     20610, cachedRead: 20100, completion: 350,
	},
	{
		name:       "no prompt caching",
		startUsage: `{"input_tokens":20458,"output_tokens":1}`,
		deltaUsage: `{"input_tokens":20458,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":200}`,
		prompt:     20458, completion: 200,
	},
}

func streamPromptUsageBody(c streamPromptUsageCase) string {
	return "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_cache","type":"message","role":"assistant","model":"claude-repro","content":[],"usage":` + c.startUsage + `}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":` + c.deltaUsage + `}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
}

// The final usage of a completed stream must bill each cached token exactly
// once, whichever frame the upstream reports the cache split on, on both the
// chat stream (/v1/chat/completions) and the Responses stream (/v1/responses and
// /anthropic/v1/messages).
func TestStreamFinalUsageCountsCachedTokensOnce(t *testing.T) {
	for _, c := range streamPromptUsageCases {
		t.Run(c.name, func(t *testing.T) {
			body := streamPromptUsageBody(c)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}

			t.Run("chat", func(t *testing.T) {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				stream, bifrostErr := newTruncationTestProvider(server.URL).ChatCompletionStream(ctx, truncationPassthroughPostHook, nil, key, truncationChatRequest())
				if bifrostErr != nil {
					t.Fatalf("stream setup failed: %v", bifrostErr.GetErrorString())
				}
				var usage *schemas.BifrostLLMUsage
				for _, chunk := range collectTruncationChunks(t, stream) {
					if chunk.BifrostError != nil {
						t.Fatalf("stream error: %v", chunk.BifrostError.GetErrorString())
					}
					if chunk.BifrostChatResponse != nil && chunk.BifrostChatResponse.Usage != nil {
						usage = chunk.BifrostChatResponse.Usage
					}
				}
				if usage == nil {
					t.Fatal("no chunk carried usage")
				}
				var read, write, write1h int
				if d := usage.PromptTokensDetails; d != nil {
					read, write = d.CachedReadTokens, d.CachedWriteTokens
					if d.CachedWriteTokenDetails != nil {
						write1h = d.CachedWriteTokenDetails.CachedWriteTokens1h
					}
				}
				if usage.PromptTokens != c.prompt || read != c.cachedRead || write != c.cachedWrite || write1h != c.write1h ||
					usage.CompletionTokens != c.completion || usage.TotalTokens != c.prompt+c.completion {
					t.Fatalf("chat usage = prompt %d (read %d write %d write1h %d) completion %d total %d, want prompt %d (read %d write %d write1h %d) completion %d total %d",
						usage.PromptTokens, read, write, write1h, usage.CompletionTokens, usage.TotalTokens,
						c.prompt, c.cachedRead, c.cachedWrite, c.write1h, c.completion, c.prompt+c.completion)
				}
			})

			t.Run("responses", func(t *testing.T) {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				stream, bifrostErr := newTruncationTestProvider(server.URL).ResponsesStream(ctx, truncationPassthroughPostHook, nil, key, truncationChatRequest().ToResponsesRequest())
				if bifrostErr != nil {
					t.Fatalf("stream setup failed: %v", bifrostErr.GetErrorString())
				}
				var usage *schemas.ResponsesResponseUsage
				for _, chunk := range collectTruncationChunks(t, stream) {
					if chunk.BifrostError != nil {
						t.Fatalf("stream error: %v", chunk.BifrostError.GetErrorString())
					}
					if r := chunk.BifrostResponsesStreamResponse; r != nil && r.Response != nil && r.Response.Usage != nil {
						usage = r.Response.Usage
					}
				}
				if usage == nil {
					t.Fatal("no chunk carried usage")
				}
				var read, write, write1h int
				if d := usage.InputTokensDetails; d != nil {
					read, write = d.CachedReadTokens, d.CachedWriteTokens
					if d.CachedWriteTokenDetails != nil {
						write1h = d.CachedWriteTokenDetails.CachedWriteTokens1h
					}
				}
				if usage.InputTokens != c.prompt || read != c.cachedRead || write != c.cachedWrite || write1h != c.write1h ||
					usage.OutputTokens != c.completion || usage.TotalTokens != c.prompt+c.completion {
					t.Fatalf("responses usage = input %d (read %d write %d write1h %d) output %d total %d, want input %d (read %d write %d write1h %d) output %d total %d",
						usage.InputTokens, read, write, write1h, usage.OutputTokens, usage.TotalTokens,
						c.prompt, c.cachedRead, c.cachedWrite, c.write1h, c.completion, c.prompt+c.completion)
				}
			})
		})
	}
}

// A stream cancelled after message_delta bills from the accumulated usage
// handle, before any final chunk exists; it must apply the same rule, on both
// stream paths.
func TestStreamIdleTimeoutAfterCacheSplitDeltaBillsCachedTokensOnce(t *testing.T) {
	for _, c := range streamPromptUsageCases {
		t.Run(c.name, func(t *testing.T) {
			full := streamPromptUsageBody(c)
			// Everything up to and including message_delta, then silence.
			body := full[:strings.Index(full, "event: message_stop")]
			stop := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(body))
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-stop:
				}
			}))
			defer server.Close()
			defer close(stop)
			key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}
			for name, responses := range map[string]bool{"chat": false, "responses": true} {
				t.Run(name, func(t *testing.T) {
					ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
					ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, 300*time.Millisecond)
					var billed *schemas.BifrostLLMUsage
					hook := func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
						if err != nil && err.ExtraFields.BilledUsage != nil {
							billed = err.ExtraFields.BilledUsage.DeepCopy()
						}
						return resp, err
					}
					provider := newTruncationTestProvider(server.URL)
					var stream chan *schemas.BifrostStreamChunk
					var bifrostErr *schemas.BifrostError
					if responses {
						stream, bifrostErr = provider.ResponsesStream(ctx, hook, nil, key, truncationChatRequest().ToResponsesRequest())
					} else {
						stream, bifrostErr = provider.ChatCompletionStream(ctx, hook, nil, key, truncationChatRequest())
					}
					if bifrostErr != nil {
						t.Fatalf("stream setup failed: %v", bifrostErr.GetErrorString())
					}
					collectTruncationChunks(t, stream)
					if billed == nil {
						t.Fatal("the idle-timeout error carries no billed usage")
					}
					var read int
					if billed.PromptTokensDetails != nil {
						read = billed.PromptTokensDetails.CachedReadTokens
					}
					if billed.PromptTokens != c.prompt || read != c.cachedRead || billed.TotalTokens != c.prompt+c.completion {
						t.Fatalf("billed usage = prompt %d (read %d) total %d, want prompt %d (read %d) total %d",
							billed.PromptTokens, read, billed.TotalTokens, c.prompt, c.cachedRead, c.prompt+c.completion)
					}
				})
			}
		})
	}
}

// BenchmarkAnthropicStreamUsageMerge measures the per-stream usage merge: one
// message_start (nested usage, no presence check) and one message_delta (top-level
// usage, one key-presence read of the raw frame), as the chat stream loop runs it.
func BenchmarkAnthropicStreamUsageMerge(b *testing.B) {
	startFrame := []byte(`{"type":"message_start","message":{"id":"msg_b","type":"message","role":"assistant","model":"claude-repro","content":[],"usage":{"input_tokens":10,"cache_read_input_tokens":20000,"cache_creation_input_tokens":0,"output_tokens":1}}}`)
	deltaFrame := []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":300}}`)
	start := &AnthropicUsage{InputTokens: 10, CacheReadInputTokens: 20000, OutputTokens: 1}
	delta := &AnthropicUsage{OutputTokens: 300}
	b.ReportAllocs()
	for b.Loop() {
		usage := schemas.BifrostLLMUsage{}
		accumulateAnthropicStreamUsage(&usage, start, reportedAnthropicStreamPromptCounters(false, start, startFrame))
		accumulateAnthropicStreamUsage(&usage, delta, reportedAnthropicStreamPromptCounters(true, delta, deltaFrame))
		normalizeCachedUsage(&usage)
		if usage.PromptTokens != 20010 {
			b.Fatalf("prompt = %d", usage.PromptTokens)
		}
	}
}
