package logging

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestAgentCorrelationIDFromContext(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{
			name:    "explicit correlation header",
			headers: map[string]string{"x-bf-agent-correlation-id": "task-1"},
			want:    "task-1",
		},
		{
			name:    "Claude Code subagent header",
			headers: map[string]string{"x-claude-code-agent-id": "aa831ea59460aba7e"},
			want:    "aa831ea59460aba7e",
		},
		{
			name: "explicit header wins over the harness header",
			headers: map[string]string{
				"x-bf-agent-correlation-id": "task-1",
				"x-claude-code-agent-id":    "aa831ea59460aba7e",
			},
			want: "task-1",
		},
		{
			name:    "header name matched case-insensitively",
			headers: map[string]string{"X-Claude-Code-Agent-Id": "aa831ea59460aba7e"},
			want:    "aa831ea59460aba7e",
		},
		{
			name:    "main conversation sends neither",
			headers: map[string]string{"x-claude-code-session-id": "5e018618-92e5-4903-8795-d097858ad9b8"},
		},
		{name: "no headers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tc.headers != nil {
				ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, tc.headers)
			}
			if got := agentCorrelationIDFromContext(ctx); got != tc.want {
				t.Fatalf("agentCorrelationIDFromContext() = %q, want %q", got, tc.want)
			}
		})
	}
}
