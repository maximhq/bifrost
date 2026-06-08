package schemas

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedactionDataContextRoundTrip verifies typed redaction data can be read from context.
func TestRedactionDataContextRoundTrip(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	data := RedactionData{
		ReversibleMappings:  map[string]string{"EMAIL-1": "alex@example.com"},
		LiteralReplacements: map[string]string{"alex@example.com": "[EMAIL-1]"},
	}

	require.True(t, SetRedactionDataOnContext(ctx, data))

	got, ok := RedactionDataFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, data, got)
}

// TestRedactionDataCloneCopiesMaps verifies clones do not share mutable map storage.
func TestRedactionDataCloneCopiesMaps(t *testing.T) {
	data := RedactionData{
		ReversibleMappings:  map[string]string{"EMAIL-1": "alex@example.com"},
		LiteralReplacements: map[string]string{"alex@example.com": "[EMAIL-1]"},
	}

	clone := data.Clone()
	data.ReversibleMappings["EMAIL-1"] = "mutated@example.com"
	data.LiteralReplacements["alex@example.com"] = "[MUTATED]"

	assert.Equal(t, "alex@example.com", clone.ReversibleMappings["EMAIL-1"])
	assert.Equal(t, "[EMAIL-1]", clone.LiteralReplacements["alex@example.com"])
}

// TestRedactionDataFromContextRejectsSerializedValues verifies the handoff remains typed.
func TestRedactionDataFromContextRejectsSerializedValues(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	ctx.SetValue(BifrostContextKeyRedactionData, `{"reversible_mappings":{"EMAIL-1":"alex@example.com"}}`)

	_, ok := RedactionDataFromContext(ctx)

	assert.False(t, ok)
}

// TestApplyLiteralReplacementsLongestFirst verifies overlapping literals are redacted deterministically.
func TestApplyLiteralReplacementsLongestFirst(t *testing.T) {
	replacements := map[string]string{
		"alex@example.com": "[EMAIL-1]",
		"example.com":      "[DOMAIN]",
	}

	got := ApplyLiteralReplacements("email alex@example.com uses example.com", replacements)

	assert.Equal(t, "email [EMAIL-1] uses [DOMAIN]", got)
}
