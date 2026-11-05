package databricks

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// BenchmarkStripUnsupportedChatFields is the per-request cost of the strip. "no extras" and
// "passthrough extras" are the common cases and must stay free of allocations.
func BenchmarkStripUnsupportedChatFields(b *testing.B) {
	cases := []struct {
		name   string
		extras map[string]any
	}{
		{"no extras", nil},
		{"passthrough extras", map[string]any{"service_tier": "priority", "databricks_test_param": "kept"}},
		{"claude code extras", map[string]any{"safeguards": json.RawMessage(`{"a":1}`), "diagnostics": map[string]any{"x": 1}, "service_tier": "priority"}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			request := &schemas.BifrostChatRequest{
				Provider: schemas.Databricks,
				Model:    "databricks-claude-sonnet-5",
				Params: &schemas.ChatParameters{
					ExtraParams: tc.extras,
				},
			}
			b.ReportAllocs()
			for b.Loop() {
				_ = stripUnsupportedChatFields(ctx, request)
			}
		})
	}
}
