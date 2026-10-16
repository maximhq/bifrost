package bedrock

import (
	"context"
	"testing"

	"github.com/bytedance/sonic"
)

// A Converse 200 whose body omits output (or carries "output": null) still
// unmarshals cleanly, leaving Output nil. ToBifrostChatResponse must surface
// that as an error rather than panic on the Output dereference.
func TestConverseResponse_MissingOutputReturnsError(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "output omitted",
			raw:  `{"stopReason": "end_turn", "usage": {"inputTokens": 12, "outputTokens": 0, "totalTokens": 12}}`,
		},
		{
			name: "output null",
			raw:  `{"output": null, "stopReason": "end_turn", "usage": {"inputTokens": 12, "outputTokens": 0, "totalTokens": 12}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var resp BedrockConverseResponse
			if err := sonic.Unmarshal([]byte(tc.raw), &resp); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}

			bifrostResp, err := resp.ToBifrostChatResponse(context.Background(), "global.anthropic.claude-opus-4-7")
			if err == nil {
				t.Fatal("ToBifrostChatResponse accepted a response with no output")
			}
			if bifrostResp != nil {
				t.Fatalf("expected nil response on error, got %+v", bifrostResp)
			}
		})
	}
}
