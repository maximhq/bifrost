package bedrock

import (
	"context"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
)

// A Bedrock Converse 200 can carry only stopReason and usage -- no `output`
// field at all. That body unmarshals cleanly, so the converter is the only
// place that can reject it. Before the guard, the dereference on the
// request worker goroutine panicked and took the process with it; the
// sibling ToBifrostResponsesResponse already tolerated the same body shape.
func TestChatResponse_MissingOutputIsAnErrorNotAPanic(t *testing.T) {
	raw := `{"stopReason":"end_turn","usage":{"inputTokens":12,"outputTokens":0,"totalTokens":12}}`

	var resp BedrockConverseResponse
	if err := sonic.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if resp.Output != nil {
		t.Fatal("fixture should leave Output nil")
	}

	converted, err := resp.ToBifrostChatResponse(
		context.Background(), "anthropic.claude-sonnet-4-20250514-v1:0",
	)
	if err == nil {
		t.Fatalf("expected an error for a body without a field output, got %+v", converted)
	}
	if !strings.Contains(err.Error(), "output") {
		t.Fatalf("error should name the missing field, got %q", err.Error())
	}
}

// An explicitly null `output` is the same shape as an omitted one.
func TestChatResponse_NullOutputIsAnError(t *testing.T) {
	raw := `{"output":null,"stopReason":"end_turn"}`

	var resp BedrockConverseResponse
	if err := sonic.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	if _, err := resp.ToBifrostChatResponse(context.Background(), "m"); err == nil {
		t.Fatal("expected an error for an explicit null output")
	}
}

// A message whose content is empty is still a valid response: the guard must
// key on Output, not on the presence of content.
func TestChatResponse_EmptyContentStillConverts(t *testing.T) {
	raw := `{"output":{"message":{"role":"assistant","content":[]}},"stopReason":"end_turn"}`

	var resp BedrockConverseResponse
	if err := sonic.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	converted, err := resp.ToBifrostChatResponse(context.Background(), "m")
	if err != nil {
		t.Fatalf("ToBifrostChatResponse: %v", err)
	}
	if converted == nil {
		t.Fatal("expected a response for an empty-content message")
	}
}
