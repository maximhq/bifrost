package deepseek_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	deepseek "github.com/maximhq/bifrost/core/providers/deepseek"
	"github.com/maximhq/bifrost/core/schemas"
)

// validV4FlashResponse is the canonical complete Anthropic-compatible fixture
// shared by the DeepSeek provider fidelity tests.
const validV4FlashResponse = `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":101,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":23,"prompt_tokens":121}}`

// validV4ProResponse is the canonical complete V4 Pro fixture used to verify
// exact served-model and usage fidelity through the provider transport.
const validV4ProResponse = `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-pro","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":101,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":23,"prompt_tokens":121}}`

// TestChatCompletionAnthropicV4FlashSendsEffortOnly verifies the provider sends
// output_config.effort without legacy thinking metadata.
func TestChatCompletionAnthropicV4FlashSendsEffortOnly(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "decode body", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, validV4FlashResponse)
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	effort := "medium"
	maxTokens := 4096
	req := v4FlashChatRequest()
	req.Params = &schemas.ChatParameters{
		Reasoning: &schemas.ChatReasoning{Effort: &effort, MaxTokens: &maxTokens},
	}
	if _, bifrostErr := provider.ChatCompletion(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		anthropicTestKey(),
		req,
	); bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	outputConfig, ok := captured["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != effort {
		t.Fatalf("output_config.effort = %#v, want %q; body=%#v", outputConfig, effort, captured)
	}
	if thinking, exists := captured["thinking"]; exists {
		t.Fatalf("wire body gained unsupported thinking: %#v", thinking)
	}
}

// TestChatCompletionAnthropicV4ProSendsEffortOnly verifies V4 Pro preserves
// xhigh on the final Anthropic-compatible wire body without legacy thinking.
func TestChatCompletionAnthropicV4ProSendsEffortOnly(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "decode body", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, validV4ProResponse)
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	effort := "xhigh"
	maxTokens := 4096
	req := v4ProChatRequest()
	req.Params = &schemas.ChatParameters{
		Reasoning: &schemas.ChatReasoning{Effort: &effort, MaxTokens: &maxTokens},
	}
	if _, bifrostErr := provider.ChatCompletion(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		anthropicTestKey(),
		req,
	); bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	outputConfig, ok := captured["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != effort {
		t.Fatalf("output_config.effort = %#v, want %q; body=%#v", outputConfig, effort, captured)
	}
	if thinking, exists := captured["thinking"]; exists {
		t.Fatalf("wire body gained unsupported thinking: %#v", thinking)
	}
}

// TestChatCompletionAnthropicV4FlashRejectsCollapsedUsage verifies incomplete
// unary Chat usage fails closed instead of producing a success response.
func TestChatCompletionAnthropicV4FlashRejectsCollapsedUsage(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":121,"output_tokens":23}}`)
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	resp, bifrostErr := provider.ChatCompletion(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		anthropicTestKey(),
		v4FlashChatRequest(),
	)
	if resp != nil {
		t.Fatalf("collapsed usage produced a success response: %#v", resp)
	}
	assertUsageFidelityError(t, bifrostErr)
}

// TestResponsesAnthropicV4FlashRejectsCollapsedUsage verifies incomplete unary
// Responses usage produces the typed fidelity failure.
func TestResponsesAnthropicV4FlashRejectsCollapsedUsage(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":121,"output_tokens":23}}`)
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	resp, bifrostErr := provider.Responses(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		anthropicTestKey(),
		v4FlashResponsesRequest(),
	)
	if resp != nil {
		t.Fatalf("collapsed usage produced a success response: %#v", resp)
	}
	assertUsageFidelityError(t, bifrostErr)
}

// TestAnthropicV4FlashLargeResponseSkipsTruncatedPreviewValidation verifies a
// prefix-only large-response preview is not mistaken for missing trailing usage.
func TestAnthropicV4FlashLargeResponseSkipsTruncatedPreviewValidation(t *testing.T) {
	largeResponse := `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[{"type":"text","text":"` +
		strings.Repeat("x", 70*1024) +
		`"}],"stop_reason":"end_turn","usage":{"input_tokens":101,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":23,"prompt_tokens":121}}`

	for _, tc := range []struct {
		name string
		call func(*schemas.BifrostContext, *deepseek.DeepSeekProvider) (bool, *schemas.BifrostError)
	}{
		{
			name: "chat",
			call: func(ctx *schemas.BifrostContext, provider *deepseek.DeepSeekProvider) (bool, *schemas.BifrostError) {
				resp, err := provider.ChatCompletion(ctx, anthropicTestKey(), v4FlashChatRequest())
				return resp != nil, err
			},
		},
		{
			name: "responses",
			call: func(ctx *schemas.BifrostContext, provider *deepseek.DeepSeekProvider) (bool, *schemas.BifrostError) {
				resp, err := provider.Responses(ctx, anthropicTestKey(), v4FlashResponsesRequest())
				return resp != nil, err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, largeResponse)
			}))
			defer server.Close()

			provider, err := newTestDeepSeekProvider(server.URL)
			if err != nil {
				t.Fatalf("NewDeepSeekProvider: %v", err)
			}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyLargeResponseThreshold, int64(1))
			gotResponse, bifrostErr := tc.call(ctx, provider)
			if reader, ok := ctx.Value(schemas.BifrostContextKeyLargeResponseReader).(io.ReadCloser); ok {
				t.Cleanup(func() { _ = reader.Close() })
			}
			if bifrostErr != nil {
				t.Fatalf("large response returned fidelity error: %#v", bifrostErr)
			}
			if !gotResponse {
				t.Fatal("large response returned nil success response")
			}
		})
	}
}

// TestResponsesAnthropicV4ProRejectsCollapsedUsage verifies Pro enters the same
// fail-closed usage-fidelity path as Flash.
func TestResponsesAnthropicV4ProRejectsCollapsedUsage(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-pro","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	req := v4ProResponsesRequest()
	resp, bifrostErr := provider.Responses(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		anthropicTestKey(),
		req,
	)
	if resp != nil {
		t.Fatalf("collapsed Pro usage produced a success response: %#v", resp)
	}
	assertUsageFidelityError(t, bifrostErr)
}

// TestResponsesAnthropicV4ProRejectsWrongServedModel verifies an unsupported
// upstream remap to Flash cannot masquerade as a successful Pro attempt.
func TestResponsesAnthropicV4ProRejectsWrongServedModel(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, validV4FlashResponse)
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	resp, bifrostErr := provider.Responses(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		anthropicTestKey(),
		v4ProResponsesRequest(),
	)
	if resp != nil {
		t.Fatalf("wrong served model produced a success response: %#v", resp)
	}
	assertUsageFidelityError(t, bifrostErr)
	if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "does not match") {
		t.Fatalf("wrong served model emitted the wrong detail: %#v", bifrostErr)
	}
}

// TestResponsesAnthropicUsageGateIsExact verifies a dated Pro identity remains
// on stock decoding instead of entering the exact reviewed V4 validator.
func TestResponsesAnthropicUsageGateIsExact(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-pro-0813","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	req := v4ProResponsesRequest()
	req.Model = "deepseek-v4-pro-0813"
	resp, bifrostErr := provider.Responses(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		anthropicTestKey(),
		req,
	)
	if bifrostErr != nil {
		t.Fatalf("near-miss model changed stock decoding: %v", bifrostErr.Error.Message)
	}
	if resp == nil {
		t.Fatal("near-miss model returned nil response")
	}
}

// TestResponsesStreamAnthropicV4FlashRejectsCollapsedUsageFirst verifies the
// first emitted Responses chunk is the typed error when start usage is invalid.
func TestResponsesStreamAnthropicV4FlashRejectsCollapsedUsageFirst(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n"+
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[],"usage":{"input_tokens":121,"output_tokens":0}}}`+"\n\n"+
			"event: message_delta\n"+
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":23}}`+"\n\n"+
			"event: message_stop\n"+
			`data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	stream, bifrostErr := provider.ResponsesStream(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		passthroughPostHook,
		nil,
		anthropicTestKey(),
		v4FlashResponsesRequest(),
	)
	if bifrostErr != nil {
		t.Fatalf("stream setup: %v", bifrostErr.Error.Message)
	}
	select {
	case chunk, ok := <-stream:
		if !ok {
			t.Fatal("stream closed without fidelity error")
		}
		if chunk == nil || chunk.BifrostError == nil {
			t.Fatalf("first chunk was not a fidelity error: %#v", chunk)
		}
		assertUsageFidelityError(t, chunk.BifrostError)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for stream fidelity error")
	}
	for range stream {
	}
}

// TestResponsesStreamAnthropicV4FlashNormalizesUsageBeforeFidelityError verifies
// cached prompt tokens are folded into the billing snapshot on per-event exits.
func TestResponsesStreamAnthropicV4FlashNormalizesUsageBeforeFidelityError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n"+
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[],"usage":{"input_tokens":101,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":0,"prompt_tokens":121}}}`+"\n\n"+
			"event: message_delta\n"+
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":-1}}`+"\n\n")
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bifrostErr := provider.ResponsesStream(
		ctx,
		passthroughPostHook,
		nil,
		anthropicTestKey(),
		v4FlashResponsesRequest(),
	)
	if bifrostErr != nil {
		t.Fatalf("stream setup: %v", bifrostErr.Error.Message)
	}
	var fidelityErr *schemas.BifrostError
	for chunk := range stream {
		if chunk != nil && chunk.BifrostError != nil {
			if fidelityErr != nil {
				t.Fatalf("stream emitted multiple errors: first=%#v second=%#v", fidelityErr, chunk.BifrostError)
			}
			fidelityErr = chunk.BifrostError
		}
	}
	assertUsageFidelityError(t, fidelityErr)
	billedUsage, ok := ctx.Value(schemas.BifrostContextKeyStreamAccumulatedUsage).(*schemas.BifrostLLMUsage)
	if !ok || billedUsage == nil {
		t.Fatalf("stream did not retain accumulated billing usage: %#v", billedUsage)
	}
	if billedUsage.PromptTokens != 121 || billedUsage.TotalTokens != 121 {
		t.Fatalf("cached usage was not normalized before the error: %#v", billedUsage)
	}
}

// TestResponsesStreamAnthropicV4FlashAcceptsCompleteUsage verifies a complete
// Responses event lifecycle streams successfully without a fidelity error.
func TestResponsesStreamAnthropicV4FlashAcceptsCompleteUsage(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n"+
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[],"usage":{"input_tokens":101,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":0,"prompt_tokens":121}}}`+"\n\n"+
			"event: content_block_start\n"+
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
			"event: content_block_delta\n"+
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`+"\n\n"+
			"event: content_block_stop\n"+
			`data: {"type":"content_block_stop","index":0}`+"\n\n"+
			"event: message_delta\n"+
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":23}}`+"\n\n"+
			"event: message_stop\n"+
			`data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	stream, bifrostErr := provider.ResponsesStream(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		passthroughPostHook,
		nil,
		anthropicTestKey(),
		v4FlashResponsesRequest(),
	)
	if bifrostErr != nil {
		t.Fatalf("stream setup: %v", bifrostErr.Error.Message)
	}
	chunks := 0
	for chunk := range stream {
		chunks++
		if chunk == nil {
			t.Fatal("valid stream emitted nil chunk")
		}
		if chunk.BifrostError != nil {
			t.Fatalf("valid stream emitted error: %#v", chunk.BifrostError)
		}
	}
	if chunks == 0 {
		t.Fatal("valid stream emitted no chunks")
	}
}

// TestResponsesStreamAnthropicV4ProAcceptsCompleteUsage verifies the provider
// carries the Pro identity into streaming model and usage validation.
func TestResponsesStreamAnthropicV4ProAcceptsCompleteUsage(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n"+
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-pro","content":[],"usage":{"input_tokens":101,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":0,"prompt_tokens":121}}}`+"\n\n"+
			"event: content_block_start\n"+
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
			"event: content_block_delta\n"+
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`+"\n\n"+
			"event: content_block_stop\n"+
			`data: {"type":"content_block_stop","index":0}`+"\n\n"+
			"event: message_delta\n"+
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":23}}`+"\n\n"+
			"event: message_stop\n"+
			`data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	stream, bifrostErr := provider.ResponsesStream(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		passthroughPostHook,
		nil,
		anthropicTestKey(),
		v4ProResponsesRequest(),
	)
	if bifrostErr != nil {
		t.Fatalf("stream setup: %v", bifrostErr.Error.Message)
	}
	chunks := 0
	for chunk := range stream {
		chunks++
		if chunk == nil {
			t.Fatal("valid Pro stream emitted nil chunk")
		}
		if chunk.BifrostError != nil {
			t.Fatalf("valid Pro stream emitted error: %#v", chunk.BifrostError)
		}
	}
	if chunks == 0 {
		t.Fatal("valid Pro stream emitted no chunks")
	}
}

// TestResponsesStreamAnthropicV4FlashRejectsTruncatedUsage verifies a stream
// ending after message_start emits the typed fidelity error.
func TestResponsesStreamAnthropicV4FlashRejectsTruncatedUsage(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n"+
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[],"usage":{"input_tokens":101,"cache_creation_input_tokens":13,"cache_read_input_tokens":7,"output_tokens":0,"prompt_tokens":121}}}`+"\n\n")
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	stream, bifrostErr := provider.ResponsesStream(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		passthroughPostHook,
		nil,
		anthropicTestKey(),
		v4FlashResponsesRequest(),
	)
	if bifrostErr != nil {
		t.Fatalf("stream setup: %v", bifrostErr.Error.Message)
	}
	var fidelityErr *schemas.BifrostError
	for chunk := range stream {
		if chunk != nil && chunk.BifrostError != nil {
			if fidelityErr != nil {
				t.Fatalf("stream emitted multiple errors: first=%#v second=%#v", fidelityErr, chunk.BifrostError)
			}
			fidelityErr = chunk.BifrostError
		}
	}
	assertUsageFidelityError(t, fidelityErr)
	if fidelityErr.Error == nil || !strings.Contains(fidelityErr.Error.Message, "message_delta") {
		t.Fatalf("truncated stream emitted the wrong fidelity detail: %#v", fidelityErr)
	}
}

// TestChatCompletionStreamAnthropicV4FlashRejectsCollapsedUsageFirst verifies
// invalid start usage becomes the first Chat stream chunk and ends the stream.
func TestChatCompletionStreamAnthropicV4FlashRejectsCollapsedUsageFirst(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n"+
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[],"usage":{"input_tokens":121,"output_tokens":0}}}`+"\n\n"+
			"event: message_delta\n"+
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":23}}`+"\n\n"+
			"event: message_stop\n"+
			`data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	stream, bifrostErr := provider.ChatCompletionStream(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		passthroughPostHook,
		nil,
		anthropicTestKey(),
		v4FlashChatRequest(),
	)
	if bifrostErr != nil {
		t.Fatalf("stream setup: %v", bifrostErr.Error.Message)
	}
	select {
	case chunk, ok := <-stream:
		if !ok {
			t.Fatal("stream closed without fidelity error")
		}
		if chunk == nil || chunk.BifrostError == nil {
			t.Fatalf("first chunk was not a fidelity error: %#v", chunk)
		}
		assertUsageFidelityError(t, chunk.BifrostError)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for stream fidelity error")
	}
	for range stream {
	}
}

// anthropicTestKey returns the synthetic key configuration used by provider
// fidelity tests that exercise Anthropic-compatible endpoints.
func anthropicTestKey() schemas.Key {
	return schemas.Key{
		Value:                 schemas.SecretVar{Val: "test-api-key"},
		UseAnthropicEndpoints: schemas.Ptr(true),
	}
}

// v4FlashResponsesRequest returns a minimal Responses request for the exact V4
// Flash model gate.
func v4FlashResponsesRequest() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ResponsesMessage{{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: schemas.Ptr("hello"),
			},
		}},
	}
}

// v4FlashChatRequest returns a minimal Chat request for the exact V4 Flash
// model gate.
func v4FlashChatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
	}
}

// v4ProResponsesRequest returns a minimal Responses request for the exact V4
// Pro model gate.
func v4ProResponsesRequest() *schemas.BifrostResponsesRequest {
	req := v4FlashResponsesRequest()
	req.Model = "deepseek-v4-pro"
	return req
}

// v4ProChatRequest returns a minimal Chat request for the exact V4 Pro model
// gate.
func v4ProChatRequest() *schemas.BifrostChatRequest {
	req := v4FlashChatRequest()
	req.Model = "deepseek-v4-pro"
	return req
}

// passthroughPostHook preserves stream responses and errors unchanged so the
// provider tests can observe their native ordering.
func passthroughPostHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return resp, err
}

// assertUsageFidelityError checks the typed, fallback-eligible, payload-safe
// error contract shared by unary and streaming tests.
func assertUsageFidelityError(t *testing.T, err *schemas.BifrostError) {
	t.Helper()
	if err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected typed 502 fidelity error, got %#v", err)
	}
	if err.Error == nil || err.Error.Code == nil || *err.Error.Code != "deepseek_usage_fidelity" {
		t.Fatalf("unexpected fidelity error shape: %#v", err)
	}
	if err.AllowFallbacks == nil || !*err.AllowFallbacks {
		t.Fatalf("fidelity error must permit fallback: %#v", err.AllowFallbacks)
	}
	if err.ExtraFields.RawResponse != nil {
		t.Fatalf("fidelity error leaked raw response: %#v", err.ExtraFields.RawResponse)
	}
}
