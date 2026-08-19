package schemas

import (
	"fmt"
	"slices"
	"strings"
)

// PluginSpanFilterMode controls whether the plugins list is an allowlist or denylist.
type PluginSpanFilterMode string

const (
	// PluginSpanFilterModeInclude exports only the listed plugins' spans.
	PluginSpanFilterModeInclude PluginSpanFilterMode = "include"
	// PluginSpanFilterModeExclude exports everything except the listed plugins' spans.
	PluginSpanFilterModeExclude PluginSpanFilterMode = "exclude"
)

// PluginSpanFilter configures which plugin spans an observability connector exports.
// Mode "include" exports only the listed plugins; mode "exclude" exports everything
// except them. It is shared by every observability connector (OTEL, Datadog, BigQuery)
// so the span-name contract and reparenting behavior stay consistent across exporters.
type PluginSpanFilter struct {
	Mode    PluginSpanFilterMode `json:"mode"`
	Plugins []string             `json:"plugins"`
}

// Validate reports whether the filter's mode is one of the two valid modes.
// A nil filter is valid (it filters nothing).
func (f *PluginSpanFilter) Validate() error {
	if f == nil {
		return nil
	}
	switch f.Mode {
	case PluginSpanFilterModeInclude, PluginSpanFilterModeExclude:
		return nil
	default:
		return fmt.Errorf("plugin_span_filter.mode %q is invalid: must be %q or %q",
			f.Mode, PluginSpanFilterModeInclude, PluginSpanFilterModeExclude)
	}
}

// SanitizePluginSpanName normalizes a plugin's name into the form embedded in its
// span names.
func SanitizePluginSpanName(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, " ", "-"))
}

// PluginNameFromSpan extracts "<name>" from a plugin span whose name follows the
// core tracer contract "plugin.<name>.<stage>", where <stage> is one of prehook,
// posthook, prerequesthook, mcp_prehook, mcp_posthook, mcp_connect_prehook, or mcp_connect_posthook
// (see core/bifrost.go). It returns "" for non-plugin spans or names that don't match
// the contract (wrong prefix, or fewer than three segments), so malformed names pass
// through ShouldExportSpan as exported rather than being silently filtered.
//
// The <stage> segment is intentionally not constrained to a fixed list: the tracer
// emits several hook stages (including the mcp_* variants above), so pinning it to
// just prehook/posthook would make every MCP-hook span unfilterable.
func PluginNameFromSpan(span *Span) string {
	if span == nil || span.Kind != SpanKindPlugin {
		return ""
	}
	parts := strings.SplitN(span.Name, ".", 3)
	if len(parts) != 3 || parts[0] != "plugin" || parts[1] == "" {
		return ""
	}
	return parts[1]
}

// ShouldExportSpan reports whether a span survives the filter. Non-plugin spans and
// spans evaluated against a nil filter are always exported. Plugin spans are checked
// against the filter's plugin list and mode.
func (f *PluginSpanFilter) ShouldExportSpan(span *Span) bool {
	if f == nil || span == nil || span.Kind != SpanKindPlugin {
		return true
	}
	pluginName := PluginNameFromSpan(span)
	if pluginName == "" {
		// Malformed plugin span name: export rather than silently drop.
		return true
	}
	inList := slices.Contains(f.Plugins, pluginName)
	if f.Mode == PluginSpanFilterModeInclude {
		return inList
	}
	return !inList // exclude mode
}

// BuildReparentMap returns a map of filteredSpanID → effective ancestor spanID for all
// spans that the filter removes. When plugin spans are chained (each span's parent is the
// previous plugin's span), removing a span from the middle would leave its children with a
// dangling parent ID. The map lets callers rewrite those parent IDs to the nearest exported
// ancestor, handling consecutive filtered spans in a chain. Returns nil when the filter is
// nil or nothing is filtered.
func (f *PluginSpanFilter) BuildReparentMap(spans []*Span) map[string]string {
	if f == nil {
		return nil
	}
	// First pass: record direct parent ID for every filtered span.
	filtered := make(map[string]string) // spanID -> parentID
	for _, span := range spans {
		if !f.ShouldExportSpan(span) {
			filtered[span.SpanID] = span.ParentID
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	// Second pass: resolve chains so each filtered span maps to its first exported ancestor.
	// Cap the walk at len(filtered) to break out of any cycle caused by malformed span data.
	maxHops := len(filtered)
	for spanID := range filtered {
		parentID := filtered[spanID]
		for range maxHops {
			grandParentID, isFiltered := filtered[parentID]
			if !isFiltered {
				break
			}
			parentID = grandParentID
		}
		filtered[spanID] = parentID
	}
	return filtered
}
