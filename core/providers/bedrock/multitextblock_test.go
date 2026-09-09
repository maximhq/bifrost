package bedrock

import (
	"context"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/tidwall/gjson"
)

// Bedrock Converse mirrors Anthropic's block structure, so a turn interrupted by
// a server-side tool comes back as sibling text blocks. choices[].message.content
// is typed string|null on the chat completions surface
// (openai-openapi ChatCompletionResponseMessage), so every text block has to fold
// into one string. The join is empty to match the streaming path, which forwards
// each text delta through without inserting a separator.
func TestMultiTextBlock_CollapsesToStringOnChatSurface(t *testing.T) {
	raw := `{
  "output": {
    "message": {
      "role": "assistant",
      "content": [
        { "text": "I'll compute 50! for you." },
        { "text": "**50! = 30,414,093,201,713,378,043,612,608,166,064,768,844,377,641,568,960,512,000,000,000,000**" }
      ]
    }
  },
  "stopReason": "end_turn",
  "usage": { "inputTokens": 12, "outputTokens": 40, "totalTokens": 52 }
}`

	var resp BedrockConverseResponse
	if err := sonic.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	bifrostResp, err := resp.ToBifrostChatResponse(context.Background(), "global.anthropic.claude-opus-4-7")
	if err != nil {
		t.Fatalf("ToBifrostChatResponse: %v", err)
	}
	if bifrostResp == nil || len(bifrostResp.Choices) == 0 {
		t.Fatal("ToBifrostChatResponse returned no choices")
	}

	content := bifrostResp.Choices[0].Message.Content
	if content == nil {
		t.Fatal("assistant message has no content")
	}
	if content.ContentStr == nil {
		t.Fatalf("content did not collapse to a string; blocks = %+v", content.ContentBlocks)
	}
	if len(content.ContentBlocks) != 0 {
		t.Errorf("content blocks survived alongside the string: %+v", content.ContentBlocks)
	}

	got := *content.ContentStr
	for _, want := range []string{"I'll compute 50! for you.", "**50! = 30,414,093,201,713,378,043,612,608,166,064,768,844,377,641,568,960,512,000,000,000,000**"} {
		if !strings.Contains(got, want) {
			t.Errorf("joined content lost a segment.\n got: %q\nwant substring: %q", got, want)
		}
	}

	out, err := sonic.Marshal(bifrostResp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if ct := gjson.GetBytes(out, "choices.0.message.content"); ct.Type != gjson.String {
		t.Errorf("wire content is %s, want a JSON string: %s", ct.Type, ct.Raw)
	}
}

// Unlike Anthropic's converter, Bedrock's also emits file blocks for document
// content, which have no string representation. A mixed response must keep the
// array shape rather than silently dropping the document.
func TestMultiTextBlock_NonTextBlocksKeepArrayShape(t *testing.T) {
	raw := `{
  "output": {
    "message": {
      "role": "assistant",
      "content": [
        { "text": "Here is the report." },
        { "document": { "name": "report", "format": "pdf", "source": { "bytes": "JVBERi0=" } } }
      ]
    }
  },
  "stopReason": "end_turn",
  "usage": { "inputTokens": 5, "outputTokens": 9, "totalTokens": 14 }
}`

	var resp BedrockConverseResponse
	if err := sonic.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	bifrostResp, err := resp.ToBifrostChatResponse(context.Background(), "global.anthropic.claude-opus-4-7")
	if err != nil {
		t.Fatalf("ToBifrostChatResponse: %v", err)
	}

	content := bifrostResp.Choices[0].Message.Content
	if content == nil {
		t.Fatal("assistant message has no content")
	}
	if content.ContentStr != nil {
		t.Errorf("a document block was folded into a string, losing the file: %q", *content.ContentStr)
	}
	if len(content.ContentBlocks) != 2 {
		t.Fatalf("expected both blocks preserved, got %d: %+v", len(content.ContentBlocks), content.ContentBlocks)
	}
}

// A single text block must keep collapsing exactly as before.
func TestMultiTextBlock_SingleBlockUnchanged(t *testing.T) {
	raw := `{
  "output": {
    "message": {
      "role": "assistant",
      "content": [{ "text": "just one block" }]
    }
  },
  "stopReason": "end_turn",
  "usage": { "inputTokens": 3, "outputTokens": 4, "totalTokens": 7 }
}`

	var resp BedrockConverseResponse
	if err := sonic.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	bifrostResp, err := resp.ToBifrostChatResponse(context.Background(), "global.anthropic.claude-opus-4-7")
	if err != nil {
		t.Fatalf("ToBifrostChatResponse: %v", err)
	}

	content := bifrostResp.Choices[0].Message.Content
	if content == nil || content.ContentStr == nil {
		t.Fatal("single text block did not collapse to a string")
	}
	if *content.ContentStr != "just one block" {
		t.Errorf("content = %q, want %q", *content.ContentStr, "just one block")
	}
}
