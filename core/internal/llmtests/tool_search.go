package llmtests

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// RunToolSearchTest exercises Anthropic server-side tool search through the
// Responses API: a tool_search tool plus a deferred function tool. On the
// Claude API this is the plain Messages request. On Bedrock, AWS allows tool
// search only through InvokeModel, never Converse
// (https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool),
// so the Bedrock provider routes such requests to InvokeModel and the captured
// outbound body must show that shape (#6825). CountTokens on Bedrock must use
// the matching "invokeModel" input of the AWS CountTokens union.
func RunToolSearchTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ToolSearch {
		t.Logf("ToolSearch not supported for provider %s", testConfig.Provider)
		return
	}
	switch testConfig.Provider {
	case schemas.Anthropic, schemas.Bedrock:
	default:
		t.Logf("ToolSearch test skipped: not enabled for provider %s", testConfig.Provider)
		return
	}

	t.Run("ToolSearch", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		model := testConfig.ToolSearchModel
		if model == "" {
			model = "claude-sonnet-4-6"
		}

		buildTools := func() []schemas.ResponsesTool {
			weather := GetSampleResponsesTool(SampleToolTypeWeather)
			weather.DeferLoading = bifrost.Ptr(true)
			return []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeToolSearch, Name: bifrost.Ptr("tool_search_tool_regex")},
				*weather,
			}
		}
		messages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("What is the weather in Paris right now? Search your tools and use the right one."),
		}
		newRequest := func() *schemas.BifrostResponsesRequest {
			return &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input:    messages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(256),
					Tools:           buildTools(),
				},
				Fallbacks: testConfig.Fallbacks,
			}
		}
		// Capture the outbound provider body so the Bedrock case can prove the
		// request left on InvokeModel rather than Converse.
		rawCtx := context.WithValue(ctx, schemas.BifrostContextKeyAllowPerRequestRawOverride, true)
		rawCtx = context.WithValue(rawCtx, schemas.BifrostContextKeySendBackRawRequest, true)

		t.Run("NonStreaming", func(t *testing.T) {
			bfCtx := schemas.NewBifrostContext(rawCtx, schemas.NoDeadline)
			response, err := client.ResponsesRequest(bfCtx, newRequest())
			if err != nil {
				t.Fatalf("ToolSearch non-streaming request failed: %s", GetErrorMessage(err))
			}
			if response == nil || len(response.Output) == 0 {
				t.Fatal("expected non-empty output")
			}
			var types []string
			usedTools := false
			for _, item := range response.Output {
				if item.Type == nil {
					continue
				}
				types = append(types, string(*item.Type))
				switch *item.Type {
				case schemas.ResponsesMessageTypeToolSearchCall, schemas.ResponsesMessageTypeToolSearchOutput, schemas.ResponsesMessageTypeFunctionCall:
					usedTools = true
				}
			}
			if !usedTools {
				t.Errorf("expected a tool_search_call or function_call output item, got types %v", types)
			}
			assertToolSearchEgress(t, testConfig.Provider, response.ExtraFields.RawRequest)
			t.Logf("ToolSearch non-streaming passed: output types=%v", types)
		})

		t.Run("Streaming", func(t *testing.T) {
			bfCtx := schemas.NewBifrostContext(rawCtx, schemas.NoDeadline)
			responseChan, err := client.ResponsesStreamRequest(bfCtx, newRequest())
			if err != nil {
				t.Fatalf("ToolSearch streaming request failed: %s", GetErrorMessage(err))
			}
			var chunkCount int
			var hasCompleted bool
			var streamRawRequest interface{}
			streamCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()
			for {
				select {
				case chunk, ok := <-responseChan:
					if !ok {
						goto done
					}
					chunkCount++
					if chunk.BifrostError != nil {
						t.Fatalf("stream returned an error chunk: %s", chunk.BifrostError.Error.Message)
					}
					if chunk.BifrostResponsesStreamResponse != nil {
						if rr := chunk.BifrostResponsesStreamResponse.ExtraFields.RawRequest; rr != nil {
							streamRawRequest = rr
						}
						if chunk.BifrostResponsesStreamResponse.Type == schemas.ResponsesStreamResponseTypeCompleted {
							hasCompleted = true
						}
					}
				case <-streamCtx.Done():
					t.Fatal("Streaming timed out")
				}
			}
		done:
			if chunkCount == 0 {
				t.Fatal("Expected at least one streaming chunk")
			}
			if !hasCompleted {
				t.Error("Missing response.completed event")
			}
			assertToolSearchEgress(t, testConfig.Provider, streamRawRequest)
			t.Logf("ToolSearch streaming passed: %d chunks", chunkCount)
		})

		if testConfig.Provider == schemas.Bedrock {
			t.Run("CountTokens", func(t *testing.T) {
				bfCtx := schemas.NewBifrostContext(rawCtx, schemas.NoDeadline)
				response, err := client.CountTokensRequest(bfCtx, newRequest())
				if err != nil {
					t.Fatalf("ToolSearch count-tokens request failed: %s", GetErrorMessage(err))
				}
				if response == nil || response.InputTokens <= 0 {
					t.Fatalf("expected a positive token count, got %+v", response)
				}
				// A positive count proves AWS accepted the envelope: a tool_search
				// request is counted through the "invokeModel" input (the Converse
				// input would reject the tool). The envelope shape itself is pinned
				// by TestBedrockCountTokensBody_UsesInvokeModelInputForRoutedRequests;
				// the count-tokens path does not honour per-request raw capture, so
				// only assert on the body here when it happens to be present.
				if rr := response.ExtraFields.RawRequest; rr != nil {
					rawJSON := marshalRawRequest(t, rr)
					if !gjson.GetBytes(rawJSON, "input.invokeModel.body").Exists() {
						t.Errorf("Bedrock count-tokens for a tool_search request must use the invokeModel input, got %s", string(rawJSON))
					}
				} else {
					t.Logf("raw request not captured on the count-tokens path; envelope shape is covered by the unit test")
				}
				t.Logf("ToolSearch count-tokens passed: input_tokens=%d", response.InputTokens)
			})
		}
	})
}

func marshalRawRequest(t *testing.T, rawRequest interface{}) []byte {
	t.Helper()
	if rawRequest == nil {
		t.Fatal("raw request not captured; BifrostContextKeySendBackRawRequest should have been honoured")
	}
	rawJSON, err := sonic.Marshal(rawRequest)
	if err != nil {
		t.Fatalf("raw request is not JSON-marshalable: %v", err)
	}
	return rawJSON
}

// assertToolSearchEgress checks the captured outbound body carries the tool
// search tool, the deferred tool's defer_loading, and the beta, and on Bedrock
// that it is the InvokeModel shape (anthropic_version "bedrock-2023-05-31").
func assertToolSearchEgress(t *testing.T, provider schemas.ModelProvider, rawRequest interface{}) {
	t.Helper()
	rawJSON := marshalRawRequest(t, rawRequest)
	body := gjson.ParseBytes(rawJSON)
	sawSearch, sawDeferred := false, false
	for _, tool := range body.Get("tools").Array() {
		if len(tool.Get("type").String()) >= len("tool_search_tool_") && tool.Get("type").String()[:len("tool_search_tool_")] == "tool_search_tool_" {
			sawSearch = true
		}
		if tool.Get("defer_loading").Bool() {
			sawDeferred = true
		}
	}
	if !sawSearch {
		t.Errorf("tool_search tool missing from outbound body: %s", string(rawJSON))
	}
	if !sawDeferred {
		t.Errorf("defer_loading missing from outbound body: %s", string(rawJSON))
	}
	switch provider {
	case schemas.Bedrock:
		if got := body.Get("anthropic_version").String(); got != "bedrock-2023-05-31" {
			t.Errorf("Bedrock tool search must egress via InvokeModel: anthropic_version=%q; body=%s", got, string(rawJSON))
		}
		found := false
		for _, b := range body.Get("anthropic_beta").Array() {
			if b.String() == anthropic.AnthropicToolSearchBetaHeader {
				found = true
			}
		}
		if !found {
			t.Errorf("anthropic_beta must carry %q on InvokeModel, got %s", anthropic.AnthropicToolSearchBetaHeader, body.Get("anthropic_beta").Raw)
		}
	case schemas.Anthropic:
		if body.Get("anthropic_version").Exists() {
			t.Errorf("Claude API body must not carry anthropic_version: %s", string(rawJSON))
		}
	}
}
