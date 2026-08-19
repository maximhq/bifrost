package bedrock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInputTokensFromHeaders(t *testing.T) {
	t.Run("reads canonical header", func(t *testing.T) {
		n, ok := inputTokensFromHeaders(map[string]string{"X-Amzn-Bedrock-Input-Token-Count": "42"})
		require.True(t, ok)
		assert.Equal(t, 42, n)
	})

	t.Run("is case-insensitive", func(t *testing.T) {
		n, ok := inputTokensFromHeaders(map[string]string{"x-amzn-bedrock-input-token-count": "7"})
		require.True(t, ok)
		assert.Equal(t, 7, n)
	})

	t.Run("trims surrounding whitespace", func(t *testing.T) {
		n, ok := inputTokensFromHeaders(map[string]string{"X-Amzn-Bedrock-Input-Token-Count": "  15 "})
		require.True(t, ok)
		assert.Equal(t, 15, n)
	})

	t.Run("returns false when header is absent", func(t *testing.T) {
		n, ok := inputTokensFromHeaders(map[string]string{"Content-Type": "application/json"})
		assert.False(t, ok)
		assert.Equal(t, 0, n)
	})

	t.Run("returns false for nil map", func(t *testing.T) {
		n, ok := inputTokensFromHeaders(nil)
		assert.False(t, ok)
		assert.Equal(t, 0, n)
	})

	t.Run("returns false for non-numeric value", func(t *testing.T) {
		n, ok := inputTokensFromHeaders(map[string]string{"X-Amzn-Bedrock-Input-Token-Count": "not-a-number"})
		assert.False(t, ok)
		assert.Equal(t, 0, n)
	})

	t.Run("returns false for negative value", func(t *testing.T) {
		n, ok := inputTokensFromHeaders(map[string]string{"X-Amzn-Bedrock-Input-Token-Count": "-3"})
		assert.False(t, ok)
		assert.Equal(t, 0, n)
	})
}
