package schemas

import "testing"

func pluginSpan(id, parent, name string) *Span {
	return &Span{SpanID: id, ParentID: parent, Name: name, Kind: SpanKindPlugin}
}

func TestPluginSpanFilter_Validate(t *testing.T) {
	tests := []struct {
		name    string
		filter  *PluginSpanFilter
		wantErr bool
	}{
		{"nil filter", nil, false},
		{"include", &PluginSpanFilter{Mode: PluginSpanFilterModeInclude}, false},
		{"exclude", &PluginSpanFilter{Mode: PluginSpanFilterModeExclude}, false},
		{"invalid mode", &PluginSpanFilter{Mode: "nonsense"}, true},
		{"empty mode", &PluginSpanFilter{Mode: ""}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.filter.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPluginNameFromSpan(t *testing.T) {
	tests := []struct {
		name string
		span *Span
		want string
	}{
		{"prehook", pluginSpan("1", "", "plugin.logging.prehook"), "logging"},
		{"posthook", pluginSpan("1", "", "plugin.compat.posthook"), "compat"},
		{"mcp hook stage still resolves", pluginSpan("1", "", "plugin.governance.mcp_connect_prehook"), "governance"},
		{"non-plugin kind", &Span{Name: "plugin.logging.prehook", Kind: SpanKindLLMCall}, ""},
		{"malformed name", pluginSpan("1", "", "plugin"), ""},
		{"missing stage", pluginSpan("1", "", "plugin.logging"), ""},
		{"wrong prefix", pluginSpan("1", "", "otel.logging.prehook"), ""},
		{"empty name segment", pluginSpan("1", "", "plugin..prehook"), ""},
		{"nil span", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PluginNameFromSpan(tt.span); got != tt.want {
				t.Errorf("PluginNameFromSpan() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizePluginSpanName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"logging", "logging"},
		{"enterprise-prompts", "enterprise-prompts"},
		{"Model Catalog Resolver", "model-catalog-resolver"},
		{"UPPER", "upper"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := SanitizePluginSpanName(tt.in); got != tt.want {
			t.Errorf("SanitizePluginSpanName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestSanitizedNameMatchesSpanExtraction locks the invariant that the name used to build a
// plugin span (SanitizePluginSpanName(GetName())) is exactly what PluginNameFromSpan extracts
// back out. If these ever diverge, the UI's filterable-plugin list stops matching real spans
// and span filtering silently no-ops — the bug this contract exists to prevent.
func TestSanitizedNameMatchesSpanExtraction(t *testing.T) {
	pluginNames := []string{"logging", "enterprise-prompts", "adaptive-loadbalancer", "Has Spaces", "MixedCase"}
	stages := []string{"prehook", "posthook", "prerequesthook", "mcp_prehook", "mcp_connect_posthook"}
	for _, raw := range pluginNames {
		sanitized := SanitizePluginSpanName(raw)
		for _, stage := range stages {
			spanName := "plugin." + sanitized + "." + stage
			if got := PluginNameFromSpan(pluginSpan("1", "", spanName)); got != sanitized {
				t.Errorf("PluginNameFromSpan(%q) = %q, want %q", spanName, got, sanitized)
			}
		}
	}
}

func TestPluginSpanFilter_ShouldExportSpan(t *testing.T) {
	llm := &Span{SpanID: "llm", Name: "llm.call", Kind: SpanKindLLMCall}
	logging := pluginSpan("p1", "", "plugin.logging.prehook")
	compat := pluginSpan("p2", "", "plugin.compat.prehook")

	tests := []struct {
		name   string
		filter *PluginSpanFilter
		span   *Span
		want   bool
	}{
		{"nil filter exports plugin", nil, logging, true},
		{"non-plugin always exported", &PluginSpanFilter{Mode: PluginSpanFilterModeInclude, Plugins: []string{"logging"}}, llm, true},
		{"include lists plugin", &PluginSpanFilter{Mode: PluginSpanFilterModeInclude, Plugins: []string{"logging"}}, logging, true},
		{"include omits plugin", &PluginSpanFilter{Mode: PluginSpanFilterModeInclude, Plugins: []string{"logging"}}, compat, false},
		{"exclude lists plugin", &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging"}}, logging, false},
		{"exclude omits plugin", &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging"}}, compat, true},
		{"malformed plugin span exported", &PluginSpanFilter{Mode: PluginSpanFilterModeInclude, Plugins: []string{"logging"}}, pluginSpan("p3", "", "plugin"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.filter.ShouldExportSpan(tt.span); got != tt.want {
				t.Errorf("ShouldExportSpan() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPluginSpanFilter_BuildReparentMap(t *testing.T) {
	t.Run("nil filter returns nil", func(t *testing.T) {
		f := (*PluginSpanFilter)(nil)
		if got := f.BuildReparentMap([]*Span{pluginSpan("1", "", "plugin.logging.prehook")}); got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("nothing filtered returns nil", func(t *testing.T) {
		f := &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"absent"}}
		spans := []*Span{pluginSpan("1", "", "plugin.logging.prehook")}
		if got := f.BuildReparentMap(spans); got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("single filtered span maps to its parent", func(t *testing.T) {
		// root(llm) <- logging <- compat. Exclude logging only.
		f := &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging"}}
		spans := []*Span{
			{SpanID: "root", Name: "llm.call", Kind: SpanKindLLMCall},
			pluginSpan("logging", "root", "plugin.logging.prehook"),
			pluginSpan("compat", "logging", "plugin.compat.prehook"),
		}
		got := f.BuildReparentMap(spans)
		if got["logging"] != "root" {
			t.Errorf("logging should reparent to root, got %q", got["logging"])
		}
	})

	t.Run("chain of filtered spans resolves to first exported ancestor", func(t *testing.T) {
		// root(llm) <- a <- b <- c. Exclude a and b. c should reparent to root.
		f := &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"a", "b"}}
		spans := []*Span{
			{SpanID: "root", Name: "llm.call", Kind: SpanKindLLMCall},
			pluginSpan("a", "root", "plugin.a.prehook"),
			pluginSpan("b", "a", "plugin.b.prehook"),
			pluginSpan("c", "b", "plugin.c.prehook"),
		}
		got := f.BuildReparentMap(spans)
		if got["a"] != "root" {
			t.Errorf("a should resolve to root, got %q", got["a"])
		}
		if got["b"] != "root" {
			t.Errorf("b should resolve to root, got %q", got["b"])
		}
		if _, ok := got["c"]; ok {
			t.Errorf("c is exported and should not be in the map")
		}
	})

	t.Run("cycle is bounded and does not hang", func(t *testing.T) {
		// Malformed: a's parent is b, b's parent is a. Both filtered.
		f := &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"a", "b"}}
		spans := []*Span{
			pluginSpan("a", "b", "plugin.a.prehook"),
			pluginSpan("b", "a", "plugin.b.prehook"),
		}
		_ = f.BuildReparentMap(spans) // must terminate
	})
}
