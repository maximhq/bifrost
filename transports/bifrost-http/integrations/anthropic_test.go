package integrations

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFilterVertexUnsupportedBetaHeaders(t *testing.T) {
	t.Run("filters known exact header values", func(t *testing.T) {
		headers := map[string][]string{
			"anthropic-beta": {"advanced-tool-use-2025-11-20,structured-outputs-2025-11-13,mcp-client-2025-04-04,prompt-caching-scope-2026-01-05"},
		}
		result := filterVertexUnsupportedBetaHeaders(headers)
		_, ok := result["anthropic-beta"]
		assert.False(t, ok, "all unsupported beta headers should be removed, leaving no anthropic-beta key")
	})

	t.Run("filters bumped date variants", func(t *testing.T) {
		// Simulate Anthropic bumping version dates in the future
		headers := map[string][]string{
			"anthropic-beta": {"structured-outputs-2025-12-15,advanced-tool-use-2026-03-01,mcp-client-2026-01-01,prompt-caching-scope-2027-06-30"},
		}
		result := filterVertexUnsupportedBetaHeaders(headers)
		_, ok := result["anthropic-beta"]
		assert.False(t, ok, "bumped-date variants of unsupported headers should also be filtered")
	})

	t.Run("passes through unrelated beta headers", func(t *testing.T) {
		headers := map[string][]string{
			"anthropic-beta": {"interleaved-thinking-2025-05-14,files-api-2025-04-14"},
		}
		result := filterVertexUnsupportedBetaHeaders(headers)
		vals, ok := result["anthropic-beta"]
		assert.True(t, ok, "unrelated beta headers should be preserved")
		assert.Equal(t, []string{"interleaved-thinking-2025-05-14,files-api-2025-04-14"}, vals)
	})

	t.Run("filters unsupported and keeps supported in mixed list", func(t *testing.T) {
		headers := map[string][]string{
			"anthropic-beta": {"interleaved-thinking-2025-05-14,structured-outputs-2025-11-13,files-api-2025-04-14,mcp-client-2025-04-04"},
		}
		result := filterVertexUnsupportedBetaHeaders(headers)
		vals, ok := result["anthropic-beta"]
		assert.True(t, ok, "supported beta headers should be preserved")
		assert.Equal(t, []string{"interleaved-thinking-2025-05-14,files-api-2025-04-14"}, vals)
	})

	t.Run("filters bumped unsupported mixed with supported", func(t *testing.T) {
		// Future-proof: bumped dates should still be filtered
		headers := map[string][]string{
			"anthropic-beta": {"structured-outputs-2026-01-01,interleaved-thinking-2025-05-14,advanced-tool-use-2026-06-15"},
		}
		result := filterVertexUnsupportedBetaHeaders(headers)
		vals, ok := result["anthropic-beta"]
		assert.True(t, ok, "supported beta headers should be preserved even when mixed with bumped unsupported ones")
		assert.Equal(t, []string{"interleaved-thinking-2025-05-14"}, vals)
	})

	t.Run("returns headers unchanged when no anthropic-beta key present", func(t *testing.T) {
		headers := map[string][]string{
			"content-type": {"application/json"},
		}
		result := filterVertexUnsupportedBetaHeaders(headers)
		assert.Equal(t, headers, result)
	})

	t.Run("handles empty anthropic-beta value gracefully", func(t *testing.T) {
		headers := map[string][]string{
			"anthropic-beta": {""},
		}
		result := filterVertexUnsupportedBetaHeaders(headers)
		// Empty string after trimming is not an unsupported header, but it is also empty — key should be removed
		_, ok := result["anthropic-beta"]
		assert.False(t, ok, "empty beta header list should result in key removal")
	})

	t.Run("case-insensitive key matching for Anthropic-Beta header", func(t *testing.T) {
		headers := map[string][]string{
			"Anthropic-Beta": {"structured-outputs-2025-11-13,interleaved-thinking-2025-05-14"},
		}
		result := filterVertexUnsupportedBetaHeaders(headers)
		vals, ok := result["Anthropic-Beta"]
		assert.True(t, ok, "header key casing should be preserved and matching should be case-insensitive")
		assert.Equal(t, []string{"interleaved-thinking-2025-05-14"}, vals)
	})
}
