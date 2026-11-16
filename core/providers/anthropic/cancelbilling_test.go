package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
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

	accumulateAnthropicResponsesUsage(responseUsage, billedUsage, upstreamUsage)

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
