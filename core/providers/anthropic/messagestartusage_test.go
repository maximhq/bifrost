package anthropic

import (
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// message_start is the first frame every Anthropic-dialect client validates, and
// @ai-sdk/anthropic's zod schema marks message.usage (and message.content) as
// required — a frame missing either aborts the stream before the first token with
// "Type validation failed ... path: [message, usage]". These tests pin the wire
// shape of every message_start Bifrost emits, on both the Responses and the Chat
// Completions converter, so no upstream shape (Bedrock Converse, which has no
// counts this early, included) can produce a frame the client rejects.

// requireValidMessageStart asserts the marshalled frame carries the fields the
// strict clients require: a message object with usage (object) and content (array).
func requireValidMessageStart(t *testing.T, raw string) {
	t.Helper()

	if got := gjson.Get(raw, "type").String(); got != "message_start" {
		t.Fatalf("expected type message_start, got %q (%s)", got, raw)
	}
	msg := gjson.Get(raw, "message")
	if !msg.IsObject() {
		t.Fatalf("message_start must carry a message object, got %s", raw)
	}
	if usage := msg.Get("usage"); !usage.IsObject() {
		t.Errorf("message_start.message.usage must be an object, got %s", raw)
	} else {
		if !usage.Get("input_tokens").Exists() {
			t.Errorf("message_start.message.usage.input_tokens must be present, got %s", raw)
		}
		if !usage.Get("output_tokens").Exists() {
			t.Errorf("message_start.message.usage.output_tokens must be present, got %s", raw)
		}
	}
	if content := msg.Get("content"); !content.IsArray() {
		t.Errorf("message_start.message.content must be an array, got %s", raw)
	}
}

func marshalEvent(t *testing.T, event *AnthropicStreamEvent) string {
	t.Helper()
	data, err := sonic.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return string(data)
}

// TestResponsesMessageStartAlwaysCarriesUsage covers the Responses converter, the
// path a /v1/messages request against a Bedrock Converse model takes.
func TestResponsesMessageStartAlwaysCarriesUsage(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})

	t.Run("response payload without usage", func(t *testing.T) {
		events := ToAnthropicResponsesStreamResponse(ctx, &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeCreated,
			Response: &schemas.BifrostResponsesResponse{
				ID:    schemas.Ptr("msg_1785995503"),
				Model: "au.anthropic.claude-opus-4-8",
			},
		})
		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}
		requireValidMessageStart(t, marshalEvent(t, events[0]))
	})

	// Providers routed through providerUtils.SendCreatedEventResponsesChunk emit a
	// created event with no Response payload at all; the frame must still be valid.
	t.Run("no response payload", func(t *testing.T) {
		events := ToAnthropicResponsesStreamResponse(ctx, &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeCreated,
			ExtraFields: schemas.BifrostResponseExtraFields{
				ResolvedModelUsed: "au.anthropic.claude-opus-4-8",
			},
		})
		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}
		requireValidMessageStart(t, marshalEvent(t, events[0]))
	})
}

// TestChatMessageStartAlwaysCarriesUsage covers ToAnthropicChatStreamResponse, the
// path taken when a /v1/messages request is served over the Chat Completions dialect.
func TestChatMessageStartAlwaysCarriesUsage(t *testing.T) {
	sse := ToAnthropicChatStreamResponse(&schemas.BifrostChatResponse{
		ID:    "msg_1785995503",
		Model: "au.anthropic.claude-opus-4-8",
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
					},
				},
			},
		},
	})

	data := gjson.Parse(sseData(t, sse))
	requireValidMessageStart(t, data.Raw)
}

// sseData extracts the JSON payload from an "event: X\ndata: {...}\n\n" frame.
func sseData(t *testing.T, sse string) string {
	t.Helper()
	idx := strings.Index(sse, "data: ")
	if idx < 0 {
		t.Fatalf("no data line in SSE frame: %q", sse)
	}
	return strings.TrimSpace(sse[idx+len("data: "):])
}
