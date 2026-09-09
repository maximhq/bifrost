package anthropic

import (
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"

	"github.com/bytedance/sonic"
	"github.com/tidwall/gjson"
)

// rawMultiTextBlockResponse is a trimmed capture of the harness case
// "Cross-cut: anthropic/claude-opus-4-7 code_execution" (Compute 50! with the
// code_execution tool). Anthropic splits the assistant prose around the
// server-side tool round-trip, so the final message carries two sibling text
// blocks rather than one.
var rawMultiTextBlockResponse = `{
  "model": "claude-opus-4-7",
  "id": "msg_011CdohXDrGNmLDRvzkhVUzi",
  "type": "message",
  "role": "assistant",
  "stop_reason": "end_turn",
  "content": [
    { "type": "text", "text": "I'll compute 50! (50 factorial) for you." },
    { "type": "text", "text": "**50! = 30,414,093,201,713,378,043,612,608,166,064,768,844,377,641,568,960,512,000,000,000,000**" }
  ],
  "usage": { "input_tokens": 12, "output_tokens": 40 }
}`

// The OpenAI chat completions contract types choices[].message.content as a
// string. ToBifrostChatResponse only collapses to a string when exactly one
// text block came back (the narrow fix from #1139), so any Anthropic turn that
// emits sibling text blocks - which server-side tools like code_execution
// routinely do - serializes content as an array and breaks every client that
// types the field as a string.
func TestMultiTextBlock_CollapsesToStringOnChatSurface(t *testing.T) {
	var anthropicResp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(rawMultiTextBlockResponse), &anthropicResp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	ctx := &schemas.BifrostContext{}
	bifrostResp := anthropicResp.ToBifrostChatResponse(ctx)
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

	// Both segments must survive the join - collapsing must not drop prose.
	got := *content.ContentStr
	for _, want := range []string{"I'll compute 50! (50 factorial) for you.", "**50! = 30,414,093,201,713,378,043,612,608,166,064,768,844,377,641,568,960,512,000,000,000,000**"} {
		if !strings.Contains(got, want) {
			t.Errorf("joined content lost a segment.\n got: %q\nwant substring: %q", got, want)
		}
	}

	// And the serialized wire shape must be a JSON string, not an array.
	out, err := sonic.Marshal(bifrostResp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if ct := gjson.GetBytes(out, "choices.0.message.content"); ct.Type != gjson.String {
		t.Errorf("wire content is %s, want a JSON string: %s", ct.Type, ct.Raw)
	}
}

// A single text block must keep collapsing exactly as before - this is the
// behaviour #1139 added and the join must not regress it by introducing
// separators around a lone segment.
func TestMultiTextBlock_SingleBlockUnchanged(t *testing.T) {
	single := `{
  "model": "claude-opus-4-7",
  "id": "msg_single",
  "type": "message",
  "role": "assistant",
  "stop_reason": "end_turn",
  "content": [{ "type": "text", "text": "just one block" }],
  "usage": { "input_tokens": 3, "output_tokens": 4 }
}`

	var anthropicResp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(single), &anthropicResp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	ctx := &schemas.BifrostContext{}
	bifrostResp := anthropicResp.ToBifrostChatResponse(ctx)
	if bifrostResp == nil || len(bifrostResp.Choices) == 0 {
		t.Fatal("ToBifrostChatResponse returned no choices")
	}

	content := bifrostResp.Choices[0].Message.Content
	if content == nil || content.ContentStr == nil {
		t.Fatal("single text block did not collapse to a string")
	}
	if *content.ContentStr != "just one block" {
		t.Errorf("content = %q, want %q", *content.ContentStr, "just one block")
	}
}
