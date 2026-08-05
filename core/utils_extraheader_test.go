package bifrost

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestExtraHeaderSpanAttribute pins what reaches the tracer. The virtual key is the sharp
// edge: IsSensitiveHeader does not match "x-bf-vk", so before this guard it was exported
// verbatim as gen_ai.request.extra_header.x-bf-vk and shipped to the trace backend in
// plaintext. Everything else keeps its previous behaviour — redact credentials, forward the
// rest — so attribution headers are unaffected.
func TestExtraHeaderSpanAttribute(t *testing.T) {
	cases := []struct {
		name       string
		header     string
		values     []string
		wantExport bool
		wantValue  any
	}{
		{"virtual key dropped entirely", "x-bf-vk", []string{"sk-bf-secret"}, false, nil},
		{"virtual key dropped case-insensitively", "X-BF-VK", []string{"sk-bf-secret"}, false, nil},
		{"other x-bf-* still exported", "x-bf-custom", []string{"v"}, true, "v"},
		{"authorization redacted", "authorization", []string{"Bearer sk-ant-oat01"}, true, schemas.RedactedAttrValue},
		{"api key redacted", "x-api-key", []string{"sk-ant-key"}, true, schemas.RedactedAttrValue},
		{"attribution header forwarded", "x-claude-code-session-id", []string{"sess-1"}, true, "sess-1"},
		{"client telemetry forwarded", "x-app", []string{"cli"}, true, "cli"},
		{"beta header forwarded", "anthropic-beta", []string{"interleaved-thinking-2025-05-14"}, true, "interleaved-thinking-2025-05-14"},
		{"empty name skipped", "", []string{"v"}, false, nil},
		{"no values skipped", "x-app", nil, false, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, export := extraHeaderSpanAttribute(tc.header, tc.values)
			if export != tc.wantExport {
				t.Fatalf("export=%v, want %v", export, tc.wantExport)
			}
			if export && got != tc.wantValue {
				t.Errorf("value=%v, want %v", got, tc.wantValue)
			}
		})
	}
}

// TestExtraHeaderSpanAttribute_MultiValue — multi-value headers are exported as the slice.
func TestExtraHeaderSpanAttribute_MultiValue(t *testing.T) {
	values := []string{"a", "b"}
	got, export := extraHeaderSpanAttribute("x-app", values)
	if !export {
		t.Fatal("expected export")
	}
	slice, ok := got.([]string)
	if !ok || len(slice) != 2 {
		t.Fatalf("expected the value slice, got %#v", got)
	}
}
