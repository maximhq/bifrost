package llmtests

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

// passthroughChatReq holds the provider-native path and JSON body for a
// minimal one-turn chat request used by the passthrough API tests.
type passthroughChatReq struct {
	path  string
	body  []byte
	query string
}

// basePassthroughChatRequest returns a minimal BifrostChatRequest suitable for
// conversion into a provider-native passthrough body.
func basePassthroughChatRequest(model string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Model: model,
		Input: []schemas.ChatMessage{
			CreateBasicChatMessage("Say hello in one word"),
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: bifrost.Ptr(300),
		},
	}
}

// buildPassthroughChatReq converts a minimal BifrostChatRequest into the
// provider-native HTTP path and JSON body using each provider's own converter.
//
// Streaming is requested when stream is true.
// Returns (req, true) for supported providers, (zero, false) to signal skip.
func buildPassthroughChatReq(provider schemas.ModelProvider, model string, stream bool) (passthroughChatReq, bool) {
	bfReq := basePassthroughChatRequest(model)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	switch provider {
	case schemas.OpenAI:
		nativeReq := openai.ToOpenAIChatRequest(ctx, bfReq)
		if stream {
			nativeReq.Stream = bifrost.Ptr(true)
		}
		body, err := sonic.Marshal(nativeReq)
		if err != nil {
			return passthroughChatReq{}, false
		}
		return passthroughChatReq{path: "/v1/chat/completions", body: body}, true

	case schemas.Anthropic:
		nativeReq, err := anthropic.ToAnthropicChatRequest(ctx, bfReq)
		if err != nil {
			return passthroughChatReq{}, false
		}
		if stream {
			nativeReq.Stream = bifrost.Ptr(true)
		}
		body, err := sonic.Marshal(nativeReq)
		if err != nil {
			return passthroughChatReq{}, false
		}
		return passthroughChatReq{path: "/v1/messages", body: body}, true

	case schemas.Gemini:
		nativeReq := gemini.ToGeminiChatCompletionRequest(ctx, bfReq)
		body, err := sonic.Marshal(nativeReq)
		if err != nil {
			return passthroughChatReq{}, false
		}
		endpoint := ":generateContent"
		query := ""
		if stream {
			endpoint = ":streamGenerateContent"
			query = "alt=sse"
		}
		req := passthroughChatReq{
			path: fmt.Sprintf("/models/%s%s", model, endpoint),
			body: body,
		}
		if query != "" {
			req.query = query
		}
		return req, true

	default:
		return passthroughChatReq{}, false
	}
}

// resolvePassthroughModel returns the model to use for passthrough tests:
// PassthroughModel if set, otherwise ChatModel.
func resolvePassthroughModel(cfg ComprehensiveTestConfig) string {
	if cfg.PassthroughModel != "" {
		return cfg.PassthroughModel
	}
	return cfg.ChatModel
}

// RunPassthroughAPITest exercises Bifrost's raw HTTP passthrough API for the
// configured provider using two sub-tests:
//
//   - PassthroughAPI/NonStream – calls client.Passthrough and verifies a 2xx
//     response with a non-empty body and correct ExtraFields.
//   - PassthroughAPI/Stream   – calls client.PassthroughStream and verifies
//     that at least one chunk with body data is received.
//
// The test is skipped when Scenarios.PassthroughAPI is false or the provider's
// native request format is not yet covered by buildPassthroughChatReq.
func RunPassthroughAPITest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.PassthroughAPI {
		t.Logf("PassthroughAPI not enabled for provider %s, skipping", testConfig.Provider)
		return
	}

	model := resolvePassthroughModel(testConfig)
	if model == "" {
		t.Logf("No model configured for PassthroughAPI test on provider %s, skipping", testConfig.Provider)
		return
	}

	t.Run("PassthroughAPI", func(t *testing.T) {
		t.Run("NonStream", func(t *testing.T) {
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}

			req, ok := buildPassthroughChatReq(testConfig.Provider, model, false)
			if !ok {
				t.Skipf("PassthroughAPI/NonStream: no native request format defined for provider %s", testConfig.Provider)
			}

			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

			resp, bifrostErr := client.Passthrough(bfCtx, testConfig.Provider, &schemas.BifrostPassthroughRequest{
				Method: "POST",
				Path:   req.path,
				Body:   req.body,
				SafeHeaders: map[string]string{
					"content-type": "application/json",
				},
				Model: model,
			})

			if bifrostErr != nil {
				t.Fatalf("❌ Passthrough request failed: %s", GetErrorMessage(bifrostErr))
			}
			if resp == nil {
				t.Fatal("❌ Passthrough response is nil")
			}

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				t.Fatalf("❌ Passthrough returned non-2xx status %d; body: %s", resp.StatusCode, string(resp.Body))
			}
			if len(resp.Body) == 0 {
				t.Fatal("❌ Passthrough response body is empty")
			}
			if resp.ExtraFields.Provider == "" {
				t.Error("❌ ExtraFields.Provider is empty")
			}
			if resp.ExtraFields.Latency <= 0 {
				t.Error("❌ ExtraFields.Latency is not positive")
			}
			if resp.ExtraFields.RequestType != schemas.PassthroughRequest {
				t.Errorf("❌ ExtraFields.RequestType = %q, want %q", resp.ExtraFields.RequestType, schemas.PassthroughRequest)
			}

			t.Logf("✅ Passthrough non-streaming OK: status=%d body_len=%d latency=%dms",
				resp.StatusCode, len(resp.Body), resp.ExtraFields.Latency)
		})

		t.Run("Stream", func(t *testing.T) {
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}

			req, ok := buildPassthroughChatReq(testConfig.Provider, model, true)
			if !ok {
				t.Skipf("PassthroughAPI/Stream: no native request format defined for provider %s", testConfig.Provider)
			}

			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

			ch, bifrostErr := client.PassthroughStream(bfCtx, testConfig.Provider, &schemas.BifrostPassthroughRequest{
				Method:   "POST",
				Path:     req.path,
				Body:     req.body,
				RawQuery: req.query,
				SafeHeaders: map[string]string{
					"content-type": "application/json",
				},
				Model: model,
			})

			if bifrostErr != nil {
				t.Fatalf("❌ PassthroughStream failed: %s", GetErrorMessage(bifrostErr))
			}
			if ch == nil {
				t.Fatal("❌ PassthroughStream returned nil channel")
			}

			var totalBytes int
			var chunkCount int
			for chunk := range ch {
				if chunk == nil {
					continue
				}
				if chunk.BifrostError != nil {
					t.Fatalf("❌ Stream chunk contained error: %s", GetErrorMessage(chunk.BifrostError))
				}
				if chunk.BifrostPassthroughResponse != nil {
					totalBytes += len(chunk.BifrostPassthroughResponse.Body)
					if len(chunk.BifrostPassthroughResponse.Body) > 0 {
						chunkCount++
					}
				}
			}

			if chunkCount == 0 {
				t.Fatal("❌ PassthroughStream received no chunks with body data")
			}

			t.Logf("✅ Passthrough streaming OK: %d chunks, %d total bytes", chunkCount, totalBytes)
		})
	})
}