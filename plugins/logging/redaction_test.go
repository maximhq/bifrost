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
		LiteralReplacements: schemas.RedactionMapsByPhase{
			Input:  map[string]string{"alex_rivera@gmail.com": "[EMAIL-1]"},
			Output: map[string]string{"rivera@example.com": "[EMAIL-2]"},
		},
		ReversibleMappings: schemas.RedactionMapsByPhase{
			Input: map[string]string{"EMAIL-1": "alex_rivera@gmail.com"},
		},
	})
	entry := &logstore.Log{}

	attachLogRedactionData(ctx, entry, true)

	require.NotNil(t, entry.RedactionData)
	assert.Equal(t, map[string]string{"EMAIL-1": "alex_rivera@gmail.com"}, entry.RedactionData.ReversibleMappings.Input)
	assert.Equal(t, map[string]string{"alex_rivera@gmail.com": "[EMAIL-1]"}, entry.RedactionData.LiteralReplacements.Input)
	assert.Equal(t, map[string]string{"rivera@example.com": "[EMAIL-2]"}, entry.RedactionData.LiteralReplacements.Output)
}

// TestAttachLogRedactionDataClonesContextMaps verifies async log entries own their redaction maps.
func TestAttachLogRedactionDataClonesContextMaps(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	reversibleMappings := map[string]string{"EMAIL-1": "alex_rivera@gmail.com"}
	inputLiteralReplacements := map[string]string{"alex_rivera@gmail.com": "[EMAIL-1]"}
	outputLiteralReplacements := map[string]string{"rivera@example.com": "[EMAIL-2]"}
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		LiteralReplacements: schemas.RedactionMapsByPhase{
			Input:  inputLiteralReplacements,
			Output: outputLiteralReplacements,
		},
		ReversibleMappings: schemas.RedactionMapsByPhase{
			Input: reversibleMappings,
		},
	})
	entry := &logstore.Log{}

	attachLogRedactionData(ctx, entry, true)
	reversibleMappings["EMAIL-1"] = "mutated@example.com"
	inputLiteralReplacements["alex_rivera@gmail.com"] = "[MUTATED]"
	outputLiteralReplacements["rivera@example.com"] = "[MUTATED]"

	require.NotNil(t, entry.RedactionData)
	assert.Equal(t, "alex_rivera@gmail.com", entry.RedactionData.ReversibleMappings.Input["EMAIL-1"])
	assert.Equal(t, "[EMAIL-1]", entry.RedactionData.LiteralReplacements.Input["alex_rivera@gmail.com"])
	assert.Equal(t, "[EMAIL-2]", entry.RedactionData.LiteralReplacements.Output["rivera@example.com"])
}

// TestAttachLogRedactionDataSkipsDisabledContentLogging verifies disabled content logging drops sensitive data.
func TestAttachLogRedactionDataSkipsDisabledContentLogging(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		ReversibleMappings: schemas.RedactionMapsByPhase{
			Input: map[string]string{"EMAIL-1": "alex_rivera@gmail.com"},
		},
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
