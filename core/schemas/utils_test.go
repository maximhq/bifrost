package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsStandardProvider verifies that IsStandardProvider reflects exactly the
// built-in StandardProviders set and, unlike IsKnownProvider, never counts a
// custom provider registered at runtime as standard.
func TestIsStandardProvider(t *testing.T) {
	// Every built-in provider is standard. Guards against the set being
	// shadowed or cleared.
	for _, p := range StandardProviders {
		assert.Truef(t, IsStandardProvider(p), "built-in provider %q should be standard", p)
	}

	// The empty provider is not standard.
	assert.False(t, IsStandardProvider(""), "empty provider should not be standard")

	// An arbitrary custom key is not standard.
	assert.False(t, IsStandardProvider("amd_qre_001"), "custom key should not be standard")

	// Crux of the design: a custom provider registered at runtime becomes a
	// KNOWN provider but must NOT become a STANDARD provider. This is what lets
	// the mid-conversation-system gate treat custom Anthropic-base providers as
	// custom even after they are registered for model-string parsing.
	const customKey ModelProvider = "is-standard-provider-test-custom"
	RegisterKnownProvider(customKey)
	defer UnregisterKnownProvider(customKey)
	assert.True(t, IsKnownProvider(string(customKey)), "registered custom provider should be known")
	assert.False(t, IsStandardProvider(customKey), "registered custom provider must not be standard")
}
