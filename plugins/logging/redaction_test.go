package logging

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAttachLogRedactionDataCopiesContextValue verifies async log entries carry transient redaction data.
func TestAttachLogRedactionDataCopiesContextValue(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		ReversibleMappings:  map[string]string{"EMAIL-1": "alex_rivera@gmail.com"},
		LiteralReplacements: map[string]string{"alex_rivera@gmail.com": "[EMAIL-1]"},
	})
	entry := &logstore.Log{}

	attachLogRedactionData(ctx, entry, true)

	require.NotNil(t, entry.RedactionData)
	assert.Equal(t, map[string]string{"EMAIL-1": "alex_rivera@gmail.com"}, entry.RedactionData.ReversibleMappings)
	assert.Equal(t, map[string]string{"alex_rivera@gmail.com": "[EMAIL-1]"}, entry.RedactionData.LiteralReplacements)
}

// TestAttachLogRedactionDataClonesContextMaps verifies async log entries own their redaction maps.
func TestAttachLogRedactionDataClonesContextMaps(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	reversibleMappings := map[string]string{"EMAIL-1": "alex_rivera@gmail.com"}
	literalReplacements := map[string]string{"alex_rivera@gmail.com": "[EMAIL-1]"}
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		ReversibleMappings:  reversibleMappings,
		LiteralReplacements: literalReplacements,
	})
	entry := &logstore.Log{}

	attachLogRedactionData(ctx, entry, true)
	reversibleMappings["EMAIL-1"] = "mutated@example.com"
	literalReplacements["alex_rivera@gmail.com"] = "[MUTATED]"

	require.NotNil(t, entry.RedactionData)
	assert.Equal(t, "alex_rivera@gmail.com", entry.RedactionData.ReversibleMappings["EMAIL-1"])
	assert.Equal(t, "[EMAIL-1]", entry.RedactionData.LiteralReplacements["alex_rivera@gmail.com"])
}

// TestAttachLogRedactionDataSkipsDisabledContentLogging verifies disabled content logging drops sensitive data.
func TestAttachLogRedactionDataSkipsDisabledContentLogging(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		ReversibleMappings: map[string]string{"EMAIL-1": "alex_rivera@gmail.com"},
	})
	entry := &logstore.Log{}

	attachLogRedactionData(ctx, entry, false)

	assert.Nil(t, entry.RedactionData)
}

// TestAttachLogRedactionDataIgnoresMissingContext verifies nil inputs are safe for processing callbacks.
func TestAttachLogRedactionDataIgnoresMissingContext(t *testing.T) {
	entry := &logstore.Log{}

	attachLogRedactionData(nil, entry, true)
	attachLogRedactionData(schemas.NewBifrostContext(context.Background(), time.Time{}), entry, true)

	assert.Nil(t, entry.RedactionData)
}
