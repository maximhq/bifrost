package bedrockmantle_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/bedrockmantle"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestBedrockMantle runs the comprehensive harness against the bedrock_mantle provider.
//
// It is gated on AWS credentials (the SigV4 path needs them). The Claude scenarios exercise the
// native-Anthropic Messages surface; gpt-oss exercises the OpenAI-compatible surface. Only the
// operations the provider actually implements are enabled — everything else (embeddings, rerank,
// batch, files, image edit/variation, text completion) is an unsupported stub.
//
// CountTokens runs against ChatModel, which is a Claude id: the count_tokens path lives on the
// native-Anthropic surface only, so pointing this scenario at a gpt-oss/Gemma model would
// (correctly) return an unsupported-operation error.
//
// The model ids live in the harness account (GetKeysForProvider, case BedrockMantle); if a model
// is reported as not found, tune the aliases there to whatever the mantle endpoints accept.
func TestBedrockMantle(t *testing.T) {
	t.Parallel()

	if strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) == "" || strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY")) == "" {
		t.Skip("Skipping Bedrock Mantle tests because AWS_ACCESS_KEY_ID or AWS_SECRET_ACCESS_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:           schemas.BedrockMantle,
		ChatModel:          "anthropic.claude-haiku-4-5",
		PromptCachingModel: "anthropic.claude-opus-4-8",
		VisionModel:        "anthropic.claude-haiku-4-5",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.BedrockMantle, Model: "anthropic.claude-opus-4-8"},
		},
		ReasoningModel:           "anthropic.claude-opus-4-8",
		InterleavedThinkingModel: "anthropic.claude-opus-4-8",
		Scenarios: llmtests.TestScenarios{
			// Supported: chat + responses surfaces (native-Anthropic and OpenAI-compatible).
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageBase64:                true, // Claude vision (native-Anthropic)
			CompleteEnd2End:            true,
			ListModels:                 true,
			Reasoning:                  true,
			InterleavedThinking:        true,
			EagerInputStreaming:        true,
			StructuredOutputs:          true,
			PromptCaching:              true,
			CountTokens:                true, // native-Anthropic /anthropic/v1/messages/count_tokens

			// Unsupported by the mantle provider (unsupported-operation stubs).
			TextCompletion: false,
			ImageURL:       false, // native-Anthropic does not accept image URLs
			MultipleImages: false,
			FileBase64:     false,
			FileURL:        false,
			Embedding:      false,
			Rerank:         false,
			BatchCreate:    false,
			BatchList:      false,
			BatchRetrieve:  false,
			BatchCancel:    false,
			BatchResults:   false,
			FileUpload:     false,
			FileList:       false,
			FileRetrieve:   false,
			FileDelete:     false,
			FileContent:    false,
			FileBatchInput: false,
			ImageEdit:      false,
			ImageVariation: false,
		},
	}

	t.Run("BedrockMantleTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// Bedrock Mantle's OpenAI-compatible surface terminates chat streams with an explicit
// `data: [DONE]`, and with stream_options.include_usage (which Bifrost always sets) it
// sends usage in a separate `choices: []` chunk AFTER the chunk carrying finish_reason.
// Captured live from bedrock-mantle.us-east-1.api.aws on 2026-09-11 (openai.gpt-5.5):
//
//	data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],...,"usage":null}
//	data: {"choices":[],...,"usage":{"completion_tokens":5,"prompt_tokens":9,"total_tokens":14}}
//	data: [DONE]
//
// Listing BedrockMantle as a provider that does not send [DONE] makes the shared
// streaming loop break on finish_reason, so the trailing usage chunk is never read and
// the synthesized final chunk reports zero tokens. See issue #7065.
func TestBedrockMantleSendsDoneMarker(t *testing.T) {
	require.True(t, providerUtils.ProviderSendsDoneMarker(nil, schemas.BedrockMantle),
		"Bedrock Mantle sends [DONE]; breaking on finish_reason drops the trailing usage chunk")
}

// mantleChatStreamServer replays the upstream event order captured in the issue and in
// the live probe above, and records the request body so the test can also pin that
// include_usage actually reaches the wire.
func mantleChatStreamServer(t *testing.T) (*httptest.Server, func() []byte) {
	t.Helper()
	var (
		mu       sync.Mutex
		captured []byte
	)
	const body = `data: {"choices":[{"delta":{"content":"Semantic search matches meaning.","role":"assistant"},"finish_reason":null,"index":0}],"created":1,"id":"chatcmpl-mantle","model":"openai.gpt-5.5","object":"chat.completion.chunk","usage":null}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"created":1,"id":"chatcmpl-mantle","model":"openai.gpt-5.5","object":"chat.completion.chunk","usage":null}` + "\n\n" +
		`data: {"choices":[],"created":1,"id":"chatcmpl-mantle","model":"openai.gpt-5.5","object":"chat.completion.chunk","usage":{"completion_tokens":29,"prompt_tokens":13,"total_tokens":42}}` + "\n\n" +
		"data: [DONE]\n\n"

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		mu.Lock()
		captured = reqBody
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server ResponseWriter is not an http.Flusher")
			return
		}
		// Flush per event so the finish_reason chunk is observable before the usage chunk,
		// the way the real endpoint delivers them.
		for _, event := range strings.SplitAfter(body, "\n\n") {
			if event == "" {
				continue
			}
			if _, err := w.Write([]byte(event)); err != nil {
				t.Errorf("writing SSE event: %v", err)
				return
			}
			flusher.Flush()
		}
	}))
	return ts, func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return captured
	}
}

func TestBedrockMantleChatStreamKeepsTrailingUsage(t *testing.T) {
	ts, requestBody := mantleChatStreamServer(t)
	defer ts.Close()

	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 5,
			// A loop that keeps waiting for a marker it already saw must fail fast, not
			// after the 60s default idle timeout.
			StreamIdleTimeoutInSeconds: 2,
			InsecureSkipVerify:         true,
			AllowPrivateNetwork:        true,
		},
	}
	config.CheckAndSetDefaults()
	provider, err := bedrockmantle.NewBedrockMantleProvider(config, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)

	// Bearer value: no SigV4 signer. The Mantle host override points at the local server.
	key := schemas.Key{
		Value: *schemas.NewSecretVar("test-bearer"),
		BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{
			Region:    schemas.NewSecretVar("us-east-1"),
			Endpoints: &schemas.BedrockEndpoints{Mantle: schemas.NewSecretVar(strings.TrimPrefix(ts.URL, "https://"))},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	passthrough := func(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return resp, bifrostErr
	}
	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthrough, nil, key, &schemas.BifrostChatRequest{
		Provider: schemas.BedrockMantle,
		Model:    "openai.gpt-5.5",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Explain semantic search in one sentence.")},
		}},
	})
	require.Nil(t, bifrostErr, "stream setup failed: %+v", bifrostErr)

	var chunks []*schemas.BifrostStreamChunk
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
collect:
	for {
		select {
		case chunk, ok := <-stream:
			if !ok {
				break collect
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		case <-timeout.C:
			t.Fatal("timed out waiting for the provider stream to close")
		}
	}

	require.Contains(t, string(requestBody()), `"stream_options":{"include_usage":true}`,
		"Bifrost must ask Mantle for the trailing usage chunk")

	require.NotEmpty(t, chunks, "expected chunks from a well-formed stream")
	for i, chunk := range chunks {
		require.Nil(t, chunk.BifrostError, "chunk %d unexpectedly carried an error", i)
	}
	final := chunks[len(chunks)-1].BifrostChatResponse
	require.NotNil(t, final, "expected a synthesized final chat chunk")
	require.Len(t, final.Choices, 1)
	require.NotNil(t, final.Choices[0].FinishReason)
	require.Equal(t, "stop", *final.Choices[0].FinishReason)

	require.NotNil(t, final.Usage, "final chunk must carry the usage Mantle sent after finish_reason")
	require.Equal(t, 13, final.Usage.PromptTokens, "prompt_tokens")
	require.Equal(t, 29, final.Usage.CompletionTokens, "completion_tokens")
	require.Equal(t, 42, final.Usage.TotalTokens, "total_tokens")
}

// mantleDSMLServer serves body as-is; the bodies below are shaped like the live deepseek.v3.2 capture.
func mantleDSMLServer(t *testing.T, contentType, body string) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
}

func newMantleDSMLProvider(t *testing.T, ts *httptest.Server) (*bedrockmantle.BedrockMantleProvider, schemas.Key) {
	t.Helper()
	config := &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{
		DefaultRequestTimeoutInSeconds: 5,
		StreamIdleTimeoutInSeconds:     2,
		InsecureSkipVerify:             true,
		AllowPrivateNetwork:            true,
	}}
	config.CheckAndSetDefaults()
	provider, err := bedrockmantle.NewBedrockMantleProvider(config, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	key := schemas.Key{
		Value: *schemas.NewSecretVar("test-bearer"),
		BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{
			Region:    schemas.NewSecretVar("us-east-1"),
			Endpoints: &schemas.BedrockEndpoints{Mantle: schemas.NewSecretVar(strings.TrimPrefix(ts.URL, "https://"))},
		},
	}
	return provider, key
}

func dsmlChatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.BedrockMantle,
		Model:    "deepseek.v3.2",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("List the files.")},
		}},
	}
}

const mantleDSMLStreamBody = `data: {"choices":[{"delta":{"content":"I'll list the files","role":"assistant"},"finish_reason":null,"index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
	`data: {"choices":[{"delta":{"content":" now.\n\n<｜DSML｜function_c"},"finish_reason":null,"index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
	`data: {"choices":[{"delta":{"content":"alls"},"finish_reason":null,"index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"command\": \"ls -la\"}"}}]},"finish_reason":null,"index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
	`data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
	`data: {"choices":[],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk","usage":{"completion_tokens":5,"prompt_tokens":10,"total_tokens":15}}` + "\n\n" +
	"data: [DONE]\n\n"

func drainMantleToJSON(t *testing.T, stream chan *schemas.BifrostStreamChunk) string {
	t.Helper()
	var b strings.Builder
	timeout := time.After(10 * time.Second)
	for {
		select {
		case chunk, ok := <-stream:
			if !ok {
				return b.String()
			}
			require.Nil(t, chunk.BifrostError, "unexpected error chunk: %+v", chunk.BifrostError)
			out, err := sonic.Marshal(chunk)
			require.NoError(t, err)
			b.Write(out)
			b.WriteByte('\n')
		case <-timeout:
			t.Fatal("timed out draining stream")
		}
	}
}

func requireMantleDSMLStripped(t *testing.T, out string) {
	t.Helper()
	require.NotContains(t, out, "DSML", "leaked DSML tag reached the client")
	require.Contains(t, out, "I'll list the files", "assistant text must survive")
	require.Contains(t, out, "ls -la", "tool call must survive")
}

func TestBedrockMantleStripsDSMLLeak_Chat(t *testing.T) {
	ts := mantleDSMLServer(t, "application/json", `{"id":"c1","object":"chat.completion","created":1,"model":"deepseek.v3.2",`+
		`"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"I'll list the files.\n\n<｜DSML｜function_calls",`+
		`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"command\": \"ls -la\"}"}}]}}],`+
		`"usage":{"completion_tokens":5,"prompt_tokens":10,"total_tokens":15}}`)
	defer ts.Close()
	provider, key := newMantleDSMLProvider(t, ts)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	resp, bifrostErr := provider.ChatCompletion(ctx, key, dsmlChatRequest())
	require.Nil(t, bifrostErr, "%+v", bifrostErr)
	out, err := sonic.Marshal(resp)
	require.NoError(t, err)
	requireMantleDSMLStripped(t, string(out))
}

func TestBedrockMantleStripsDSMLLeak_ChatStream(t *testing.T) {
	ts := mantleDSMLServer(t, "text/event-stream", mantleDSMLStreamBody)
	defer ts.Close()
	provider, key := newMantleDSMLProvider(t, ts)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, key, dsmlChatRequest())
	require.Nil(t, bifrostErr, "%+v", bifrostErr)
	requireMantleDSMLStripped(t, drainMantleToJSON(t, stream))
}

// The /anthropic and /v1/responses routes reach Mantle through this Responses->Chat fallback.
func TestBedrockMantleStripsDSMLLeak_ResponsesFallbackStream(t *testing.T) {
	ts := mantleDSMLServer(t, "text/event-stream", mantleDSMLStreamBody)
	defer ts.Close()
	provider, key := newMantleDSMLProvider(t, ts)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)

	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, key, dsmlChatRequest())
	require.Nil(t, bifrostErr, "%+v", bifrostErr)
	out := drainMantleToJSON(t, stream)
	require.Contains(t, out, "response.output_text.delta", "expected Responses-shaped chunks on the fallback path")
	requireMantleDSMLStripped(t, out)
}

func passthroughPostHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return resp, bifrostErr
}

// "<" and "｜DSML｜" are separate tokens, so the chunk boundary can fall between them.
func TestBedrockMantleStripsDSMLLeak_ChatStreamSplitMarker(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":"I'll list the files now.\n\n<","role":"assistant"},"finish_reason":null,"index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"｜DSML｜function_calls"},"finish_reason":null,"index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"command\": \"ls -la\"}"}}]},"finish_reason":null,"index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
		"data: [DONE]\n\n"
	ts := mantleDSMLServer(t, "text/event-stream", body)
	defer ts.Close()
	provider, key := newMantleDSMLProvider(t, ts)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, key, dsmlChatRequest())
	require.Nil(t, bifrostErr, "%+v", bifrostErr)
	requireMantleDSMLStripped(t, drainMantleToJSON(t, stream))
}

// A reply that really ends in "<" keeps it: the closing chunk carries the held byte.
func TestBedrockMantleKeepsTrailingAngleBracket(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":"use a <","role":"assistant"},"finish_reason":null,"index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
		"data: [DONE]\n\n"
	ts := mantleDSMLServer(t, "text/event-stream", body)
	defer ts.Close()
	provider, key := newMantleDSMLProvider(t, ts)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, key, dsmlChatRequest())
	require.Nil(t, bifrostErr, "%+v", bifrostErr)
	var text strings.Builder
	timeout := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case chunk, ok := <-stream:
			if !ok {
				done = true
				break
			}
			require.Nil(t, chunk.BifrostError)
			if r := chunk.BifrostChatResponse; r != nil {
				for _, c := range r.Choices {
					if c.ChatStreamResponseChoice != nil && c.Delta != nil && c.Delta.Content != nil {
						text.WriteString(*c.Delta.Content)
					}
				}
			}
		case <-timeout:
			t.Fatal("timed out draining stream")
		}
	}
	require.Equal(t, "use a <", text.String())
}

// drainMantleText collects the streamed assistant text.
func drainMantleText(t *testing.T, stream chan *schemas.BifrostStreamChunk) string {
	t.Helper()
	var text strings.Builder
	timeout := time.After(10 * time.Second)
	for {
		select {
		case chunk, ok := <-stream:
			if !ok {
				return text.String()
			}
			require.Nil(t, chunk.BifrostError)
			if r := chunk.BifrostChatResponse; r != nil {
				for _, c := range r.Choices {
					if c.ChatStreamResponseChoice != nil && c.Delta != nil && c.Delta.Content != nil {
						text.WriteString(*c.Delta.Content)
					}
				}
			}
		case <-timeout:
			t.Fatal("timed out draining stream")
		}
	}
}

// An answer that literally ends in the tag, with no tool call, reaches the client whole.
func TestBedrockMantleKeepsLiteralTagWithoutToolCall_Stream(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":"The prefix is <｜DSML｜function_calls","role":"assistant"},"finish_reason":null,"index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
		"data: [DONE]\n\n"
	ts := mantleDSMLServer(t, "text/event-stream", body)
	defer ts.Close()
	provider, key := newMantleDSMLProvider(t, ts)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, key, dsmlChatRequest())
	require.Nil(t, bifrostErr, "%+v", bifrostErr)
	require.Equal(t, "The prefix is <｜DSML｜function_calls", drainMantleText(t, stream))
}

func TestBedrockMantleKeepsLiteralTagWithoutToolCall_Chat(t *testing.T) {
	ts := mantleDSMLServer(t, "application/json", `{"id":"c1","object":"chat.completion","created":1,"model":"deepseek.v3.2",`+
		`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"The prefix is <｜DSML｜function_calls"}}],`+
		`"usage":{"completion_tokens":5,"prompt_tokens":10,"total_tokens":15}}`)
	defer ts.Close()
	provider, key := newMantleDSMLProvider(t, ts)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	resp, bifrostErr := provider.ChatCompletion(ctx, key, dsmlChatRequest())
	require.Nil(t, bifrostErr, "%+v", bifrostErr)
	require.Equal(t, "The prefix is <｜DSML｜function_calls", *resp.Choices[0].Message.Content.ContentStr)
}

// The closing chunk carries no "delta" key; the held tail of a literal answer must still arrive.
func TestBedrockMantleKeepsHeldTextOnFinishWithoutDelta(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":"this result is <","role":"assistant"},"finish_reason":null,"index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
		`data: {"choices":[{"finish_reason":"stop","index":0}],"created":1,"id":"c1","model":"deepseek.v3.2","object":"chat.completion.chunk"}` + "\n\n" +
		"data: [DONE]\n\n"
	ts := mantleDSMLServer(t, "text/event-stream", body)
	defer ts.Close()
	provider, key := newMantleDSMLProvider(t, ts)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, key, dsmlChatRequest())
	require.Nil(t, bifrostErr, "%+v", bifrostErr)
	require.Equal(t, "this result is <", drainMantleText(t, stream))
}
