package llmtests

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// webSearchChatPrompt asks for something only a live search can answer, so the
// model runs the server-side web_search and cites what it found.
const webSearchChatPrompt = "Use the web_search tool to find who won the 2025 Nobel Prize in Physics. Answer in one sentence and cite your source."

// RunWebSearchViaChatCompletionsTest guards the chat-completions conversion of
// a server-side web search (issue #7655). The search runs on the provider's
// side, so the OpenAI-format response must:
//   - carry no tool_calls for it, streamed or not (the server_tool_use input
//     used to leak as an orphan tool_calls delta with no id or name), and
//   - carry the search citations as url_citation annotations whose spans fall
//     inside the returned content.
//
// It runs where ServerToolsViaOpenAIEndpoint is enabled, on the providers that
// support web_search there (Anthropic, Vertex, Azure).
func RunWebSearchViaChatCompletionsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ServerToolsViaOpenAIEndpoint {
		t.Logf("WebSearchViaChatCompletions not supported for provider %s", testConfig.Provider)
		return
	}
	switch testConfig.Provider {
	case schemas.Anthropic, schemas.Vertex, schemas.Azure:
	default:
		t.Logf("web_search via chat completions not supported on %s", testConfig.Provider)
		return
	}

	newRequest := func() *schemas.BifrostChatRequest {
		maxUses := 3
		return &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    []schemas.ChatMessage{CreateBasicChatMessage(webSearchChatPrompt)},
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(1024),
				Tools: []schemas.ChatTool{{
					Type:    "web_search_20250305",
					Name:    "web_search",
					MaxUses: &maxUses,
				}},
			},
			Fallbacks: testConfig.Fallbacks,
		}
	}

	t.Run("WebSearchViaChatCompletions", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		t.Run("NonStream", func(t *testing.T) {
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			resp, err := client.ChatCompletionRequest(bfCtx, newRequest())
			if err != nil {
				t.Fatalf("web search chat request failed: %s", GetErrorMessage(err))
			}
			if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].Message == nil {
				t.Fatal("expected a response with one message")
			}
			msg := resp.Choices[0].Message
			var toolCalls []schemas.ChatAssistantMessageToolCall
			var annotations []schemas.ChatAssistantMessageAnnotation
			if msg.ChatAssistantMessage != nil {
				toolCalls = msg.ChatAssistantMessage.ToolCalls
				annotations = msg.ChatAssistantMessage.Annotations
			}
			assertWebSearchChatResult(t, GetChatContent(resp), toolCalls, annotations)
		})

		t.Run("Stream", func(t *testing.T) {
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			stream, err := client.ChatCompletionStreamRequest(bfCtx, newRequest())
			if err != nil {
				t.Fatalf("web search chat stream failed: %s", GetErrorMessage(err))
			}

			streamCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()

			var content strings.Builder
			var toolCalls []schemas.ChatAssistantMessageToolCall
			var annotations []schemas.ChatAssistantMessageAnnotation
		read:
			for {
				select {
				case chunk, ok := <-stream:
					if !ok {
						break read
					}
					if chunk == nil {
						continue
					}
					if chunk.BifrostError != nil {
						t.Fatalf("stream error: %s", GetErrorMessage(chunk.BifrostError))
					}
					if chunk.BifrostChatResponse == nil {
						continue
					}
					for _, choice := range chunk.BifrostChatResponse.Choices {
						if choice.ChatStreamResponseChoice == nil || choice.ChatStreamResponseChoice.Delta == nil {
							continue
						}
						delta := choice.ChatStreamResponseChoice.Delta
						if delta.Content != nil {
							content.WriteString(*delta.Content)
						}
						toolCalls = append(toolCalls, delta.ToolCalls...)
						annotations = append(annotations, delta.Annotations...)
					}
				case <-streamCtx.Done():
					t.Fatal("timed out reading the web search chat stream")
				}
			}
			assertWebSearchChatResult(t, content.String(), toolCalls, annotations)
		})
	})
}

// assertWebSearchChatResult checks one web search chat turn: no tool calls
// leaked from the server-side search, at least one url_citation annotation,
// and every annotation span inside the content.
func assertWebSearchChatResult(t *testing.T, content string, toolCalls []schemas.ChatAssistantMessageToolCall, annotations []schemas.ChatAssistantMessageAnnotation) {
	t.Helper()
	if content == "" {
		t.Fatal("expected answer text")
	}
	if len(toolCalls) != 0 {
		t.Errorf("server-side web search leaked as %d tool_calls: %+v", len(toolCalls), toolCalls)
	}
	if len(annotations) == 0 {
		t.Fatalf("expected url_citation annotations from the web search, got none; content=%q", content)
	}
	for i, ann := range annotations {
		if ann.Type != "url_citation" {
			t.Errorf("annotation %d type = %q, want url_citation", i, ann.Type)
		}
		if ann.URLCitation.URL == nil || *ann.URLCitation.URL == "" {
			t.Errorf("annotation %d has no url", i)
		}
		start, end := ann.URLCitation.StartIndex, ann.URLCitation.EndIndex
		if start < 0 || start > end || end > len(content) {
			t.Errorf("annotation %d span [%d,%d) is outside the %d-byte content", i, start, end, len(content))
		}
	}
	t.Logf("web search chat: %d bytes of content, %d citations", len(content), len(annotations))
}
