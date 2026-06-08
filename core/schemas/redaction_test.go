package schemas

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedactionPayloadContextRoundTrip verifies typed payloads can be read and serialized from context.
func TestRedactionPayloadContextRoundTrip(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	payload := RedactionPayload{
		ReversibleMappings:  map[string]string{"EMAIL-1": "alex@example.com"},
		LiteralReplacements: map[string]string{"alex@example.com": "[EMAIL-1]"},
		InputHistory:        `[{"role":"user","content":"[EMAIL-1]"}]`,
	}

	require.True(t, SetRedactionPayloadOnContext(ctx, payload))

	got, ok := RedactionPayloadFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, payload, got)

	serialized, ok := RedactionPayloadStringFromContext(ctx)
	require.True(t, ok)
	assert.Contains(t, serialized, "alex@example.com")
}

// TestRedactionPayloadFromSerializedContext verifies existing serialized payload values still decode.
func TestRedactionPayloadFromSerializedContext(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	ctx.SetValue(BifrostContextKeyRedactionData, `{"reversible_mappings":{"EMAIL-1":"alex@example.com"}}`)

	payload, ok := RedactionPayloadFromContext(ctx)

	require.True(t, ok)
	assert.Equal(t, map[string]string{"EMAIL-1": "alex@example.com"}, payload.ReversibleMappings)
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
