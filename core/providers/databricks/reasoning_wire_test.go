package databricks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Observed on a live workspace: the thinking phase streams as reasoning content blocks, the
// answer as plain string deltas, and the signature arrives on a final empty reasoning block.
const stubClaudeReasoningStream = `data: {"model":"m","choices":[{"delta":{"role":"assistant","content":[{"type":"reasoning","summary":[{"type":"summary_text","text":"17 * 23","signature":""}]}]},"index":0,"finish_reason":null}],"object":"chat.completion.chunk","id":"msg-1","created":1}` + "\n\n" +
	`data: {"model":"m","choices":[{"delta":{"role":"assistant","content":[{"type":"reasoning","summary":[{"type":"summary_text","text":" = 391","signature":""}]}]},"index":0,"finish_reason":null}],"object":"chat.completion.chunk","id":"msg-1","created":1}` + "\n\n" +
	`data: {"model":"m","choices":[{"delta":{"role":"assistant","content":[{"type":"reasoning","summary":[{"type":"summary_text","text":"","signature":"sig-1"}]}]},"index":0,"finish_reason":null}],"object":"chat.completion.chunk","id":"msg-1","created":1}` + "\n\n" +
	`data: {"model":"m","choices":[{"delta":{"role":"assistant","content":"391"},"index":0,"finish_reason":null}],"object":"chat.completion.chunk","id":"msg-1","created":1}` + "\n\n" +
	`data: {"model":"m","choices":[{"delta":{"role":"assistant","content":""},"index":0,"finish_reason":"stop"}],"usage":{"completion_tokens":9,"prompt_tokens":4,"total_tokens":13},"object":"chat.completion.chunk","id":"msg-1","created":1}` + "\n\n" +
	"data: [DONE]\n\n"

const stubClaudeReasoningResponse = `{"model":"m","choices":[{"message":{"role":"assistant","content":[{"type":"reasoning","summary":[{"type":"summary_text","text":"17 * 23 = 391","signature":"sig-1"}]},{"type":"text","text":"391"}]},"index":0,"finish_reason":"stop"}],"usage":{"completion_tokens":9,"prompt_tokens":4,"total_tokens":13},"object":"chat.completion","id":"msg-1","created":1}`

// TestDatabricksClaudeReasoningOverTheWire reproduces the live gap: a reasoning request to a
// Claude endpoint used to lose reasoning entirely (reasoning_effort dropped, nothing sent in
// its place) and the endpoint's reasoning blocks were not readable. Now the request carries a
// thinking budget with a ceiling above it, and the reasoning comes back on Bifrost's fields.
func TestDatabricksClaudeReasoningOverTheWire(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotBody map[string]any
	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		mu.Lock()
		gotBody = body
		mu.Unlock()
		if stream, _ := body["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(stubClaudeReasoningStream))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubClaudeReasoningResponse))
	})

	key := schemas.Key{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("dapi-test"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
		},
	}
	request := func() *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: schemas.Databricks,
			Model:    "databricks-claude-sonnet-4-5",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("17*23?")}}},
			Params: &schemas.ChatParameters{
				Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("high"), MaxTokens: schemas.Ptr(1500)},
			},
		}
	}
	assertWire := func(t *testing.T) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if _, exists := gotBody["reasoning_effort"]; exists {
			t.Errorf("reasoning_effort reached Databricks: %#v", gotBody["reasoning_effort"])
		}
		thinking, _ := gotBody["thinking"].(map[string]any)
		if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(1500) {
			t.Errorf("thinking: got %#v, want enabled with a 1500 budget", gotBody["thinking"])
		}
		if ceiling, _ := gotBody["max_completion_tokens"].(float64); ceiling <= 1500 {
			t.Errorf("max_completion_tokens: got %v, want above the budget", gotBody["max_completion_tokens"])
		}
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Run("unary", func(t *testing.T) {
		response, bErr := provider.ChatCompletion(ctx, key, request())
		if bErr != nil {
			t.Fatalf("ChatCompletion returned an error: %v", bErr)
		}
		assertWire(t)
		message := response.Choices[0].Message
		if message.Content == nil || message.Content.ContentStr == nil || *message.Content.ContentStr != "391" {
			t.Errorf("content: got %#v, want \"391\"", message.Content)
		}
		if message.ChatAssistantMessage == nil || message.Reasoning == nil || *message.Reasoning != "17 * 23 = 391" {
			t.Errorf("reasoning was not lifted out of the content blocks: %#v", message.ChatAssistantMessage)
		}
		if len(message.ReasoningDetails) != 1 || message.ReasoningDetails[0].Signature == nil || *message.ReasoningDetails[0].Signature != "sig-1" {
			t.Errorf("reasoning details: got %#v, want one entry carrying the signature", message.ReasoningDetails)
		}
	})

	t.Run("stream", func(t *testing.T) {
		stream, bErr := provider.ChatCompletionStream(ctx, noopPostHook, nil, key, request())
		if bErr != nil {
			t.Fatalf("ChatCompletionStream returned an error: %v", bErr)
		}
		var reasoning, content strings.Builder
		var signature string
		for chunk := range stream {
			if chunk.BifrostChatResponse == nil {
				continue
			}
			for _, choice := range chunk.BifrostChatResponse.Choices {
				if choice.ChatStreamResponseChoice == nil || choice.Delta == nil {
					continue
				}
				if choice.Delta.Reasoning != nil {
					reasoning.WriteString(*choice.Delta.Reasoning)
				}
				if choice.Delta.Content != nil {
					content.WriteString(*choice.Delta.Content)
				}
				for _, detail := range choice.Delta.ReasoningDetails {
					if detail.Signature != nil {
						signature = *detail.Signature
					}
				}
			}
		}
		assertWire(t)
		if reasoning.String() != "17 * 23 = 391" {
			t.Errorf("streamed reasoning: got %q", reasoning.String())
		}
		if content.String() != "391" {
			t.Errorf("streamed content: got %q", content.String())
		}
		if signature != "sig-1" {
			t.Errorf("streamed signature: got %q", signature)
		}
	})
}
