package otel

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"go.opentelemetry.io/otel/attribute"
)

func TestAppendBifrostDimensionMetricAttrs(t *testing.T) {
	t.Parallel()
	base := []attribute.KeyValue{attribute.String("provider", "openai")}
	spanAttrs := map[string]any{
		schemas.AttrGenAIDimensionPrefix + "team": "eng",
		"other": "skip",
		"":      "skip",
	}
	out := AppendBifrostDimensionMetricAttrs(spanAttrs, base)
	if len(out) != 2 {
		t.Fatalf("len=%d, want 2", len(out))
	}
	if out[1].Key != schemas.AttrGenAIDimensionPrefix+"team" {
		t.Errorf("second key: got %q", out[1].Key)
	}
}
