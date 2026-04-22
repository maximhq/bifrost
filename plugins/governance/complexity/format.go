package complexity

import (
	"fmt"
	"strings"
)

// FormatLog renders the concise routing-engine log summary for complexity
// analysis.
func FormatLog(result *ComplexityResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Complexity: tier=%s score=%.2f words=%d matches=[%s]",
		result.Tier, result.Score, result.WordCount,
		formatMatchCounts(result))
	return b.String()
}

// formatMatchCounts renders the non-zero keyword match counts for the primary
// log line. Zero-valued dimensions are omitted to reduce noise; when every
// count is zero we emit "none" so the reader can tell the analyzer ran and
// found nothing rather than being silently skipped.
func formatMatchCounts(result *ComplexityResult) string {
	parts := make([]string, 0, 5)
	if result.CodeMatchCount > 0 {
		parts = append(parts, fmt.Sprintf("code:%d", result.CodeMatchCount))
	}
	if result.ReasoningMatchCount > 0 {
		parts = append(parts, fmt.Sprintf("reasoning:%d", result.ReasoningMatchCount))
	}
	if result.TechnicalMatchCount > 0 {
		parts = append(parts, fmt.Sprintf("technical:%d", result.TechnicalMatchCount))
	}
	if result.SimpleMatchCount > 0 {
		parts = append(parts, fmt.Sprintf("simple:%d", result.SimpleMatchCount))
	}
	if result.OutputMatchCount > 0 {
		parts = append(parts, fmt.Sprintf("output:%d", result.OutputMatchCount))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " ")
}
