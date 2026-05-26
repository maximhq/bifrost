package otel

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func makeSpan(id, parentID, name string, kind schemas.SpanKind) *schemas.Span {
	return &schemas.Span{
		SpanID:    id,
		ParentID:  parentID,
		Name:      name,
		Kind:      kind,
		StartTime: time.Now(),
		EndTime:   time.Now(),
	}
}

func TestShouldExportSpan(t *testing.T) {
	tests := []struct {
		name   string
		filter *PluginSpanFilter
		span   *schemas.Span
		want   bool
	}{
		{
			name:   "nil filter exports everything",
			filter: nil,
			span:   makeSpan("1", "", "plugin.logging.prehook", schemas.SpanKindPlugin),
			want:   true,
		},
		{
			name:   "non-plugin span always exported regardless of filter",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging"}},
			span:   makeSpan("1", "", "llm.call", schemas.SpanKindLLMCall),
			want:   true,
		},
		{
			name:   "exclude mode: plugin in list is suppressed",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging", "compat"}},
			span:   makeSpan("1", "", "plugin.logging.prehook", schemas.SpanKindPlugin),
			want:   false,
		},
		{
			name:   "exclude mode: plugin not in list is exported",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging"}},
			span:   makeSpan("1", "", "plugin.governance.posthook", schemas.SpanKindPlugin),
			want:   true,
		},
		{
			name:   "exclude mode: posthook variant suppressed the same as prehook",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging"}},
			span:   makeSpan("1", "", "plugin.logging.posthook", schemas.SpanKindPlugin),
			want:   false,
		},
		{
			name:   "include mode: plugin in list is exported",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeInclude, Plugins: []string{"guardrails"}},
			span:   makeSpan("1", "", "plugin.guardrails.prehook", schemas.SpanKindPlugin),
			want:   true,
		},
		{
			name:   "include mode: plugin not in list is suppressed",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeInclude, Plugins: []string{"guardrails"}},
			span:   makeSpan("1", "", "plugin.logging.prehook", schemas.SpanKindPlugin),
			want:   false,
		},
		{
			name:   "exclude mode: empty list suppresses nothing",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{}},
			span:   makeSpan("1", "", "plugin.logging.prehook", schemas.SpanKindPlugin),
			want:   true,
		},
		{
			name:   "include mode: empty list suppresses everything",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeInclude, Plugins: []string{}},
			span:   makeSpan("1", "", "plugin.logging.prehook", schemas.SpanKindPlugin),
			want:   false,
		},
		{
			name:   "span name without dots passes through",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging"}},
			span:   makeSpan("1", "", "nodots", schemas.SpanKindPlugin),
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &OtelPlugin{pluginSpanFilter: tt.filter}
			if got := p.shouldExportSpan(tt.span); got != tt.want {
				t.Errorf("shouldExportSpan() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildReparentMap(t *testing.T) {
	excludeLogging := &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging"}}

	t.Run("nil filter returns nil map", func(t *testing.T) {
		p := &OtelPlugin{pluginSpanFilter: nil}
		spans := []*schemas.Span{makeSpan("a", "root", "plugin.logging.prehook", schemas.SpanKindPlugin)}
		if m := p.buildReparentMap(spans); m != nil {
			t.Errorf("expected nil, got %v", m)
		}
	})

	t.Run("no filtered spans returns nil map", func(t *testing.T) {
		p := &OtelPlugin{pluginSpanFilter: excludeLogging}
		spans := []*schemas.Span{
			makeSpan("a", "root", "plugin.governance.prehook", schemas.SpanKindPlugin),
		}
		if m := p.buildReparentMap(spans); m != nil {
			t.Errorf("expected nil, got %v", m)
		}
	})

	t.Run("single filtered span maps to its direct parent", func(t *testing.T) {
		p := &OtelPlugin{pluginSpanFilter: excludeLogging}
		// root -> logging (filtered) -> governance
		spans := []*schemas.Span{
			makeSpan("root", "", "request", schemas.SpanKindInternal),
			makeSpan("log-pre", "root", "plugin.logging.prehook", schemas.SpanKindPlugin),
			makeSpan("gov-pre", "log-pre", "plugin.governance.prehook", schemas.SpanKindPlugin),
		}
		m := p.buildReparentMap(spans)
		if m == nil {
			t.Fatal("expected non-nil map")
		}
		if got := m["log-pre"]; got != "root" {
			t.Errorf("filtered span should map to parent 'root', got %q", got)
		}
	})

	t.Run("chain of filtered spans resolves to nearest exported ancestor", func(t *testing.T) {
		// root -> telemetry (filtered) -> logging (filtered) -> governance
		p := &OtelPlugin{pluginSpanFilter: &PluginSpanFilter{
			Mode:    PluginSpanFilterModeExclude,
			Plugins: []string{"telemetry", "logging"},
		}}
		spans := []*schemas.Span{
			makeSpan("root", "", "request", schemas.SpanKindInternal),
			makeSpan("tel-pre", "root", "plugin.telemetry.prehook", schemas.SpanKindPlugin),
			makeSpan("log-pre", "tel-pre", "plugin.logging.prehook", schemas.SpanKindPlugin),
			makeSpan("gov-pre", "log-pre", "plugin.governance.prehook", schemas.SpanKindPlugin),
		}
		m := p.buildReparentMap(spans)
		if m == nil {
			t.Fatal("expected non-nil map")
		}
		// Both filtered spans must resolve to "root" so governance.prehook re-parents there.
		if got := m["tel-pre"]; got != "root" {
			t.Errorf("tel-pre should resolve to 'root', got %q", got)
		}
		if got := m["log-pre"]; got != "root" {
			t.Errorf("log-pre should skip the chain and resolve to 'root', got %q", got)
		}
	})

	t.Run("filtered span with no parent resolves to empty string", func(t *testing.T) {
		p := &OtelPlugin{pluginSpanFilter: excludeLogging}
		spans := []*schemas.Span{
			// logging span has no parent (root of trace)
			makeSpan("log-pre", "", "plugin.logging.prehook", schemas.SpanKindPlugin),
			makeSpan("gov-pre", "log-pre", "plugin.governance.prehook", schemas.SpanKindPlugin),
		}
		m := p.buildReparentMap(spans)
		if m == nil {
			t.Fatal("expected non-nil map")
		}
		if got := m["log-pre"]; got != "" {
			t.Errorf("root-level filtered span should resolve to empty string, got %q", got)
		}
	})
}
